# Phase 2d — Full-Result Snapshot Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cache the immutable `PackageResult` per package in the warm `Module` so a repeat `Package(dir)` for an unchanged package returns instantly without re-analysis — dropped in lockstep with `pkgTypes` by the same reverse-closure invalidation and fset rebuild.

**Architecture:** A `pkgResults map[string]*PackageResult` cache populated only by `Package`. `Package` reads it after `maybeRebuildFset`+`applyDirty` (which have already dropped any stale entry), returns a hit, else builds + caches. `invalidateLocked` deletes the reverse-closure from `pkgResults` as well as `pkgTypes`; `rebuildFset` clears `pkgResults` with the fset (it is position-bearing). The edited package is always in its own dirty closure, so it always re-analyzes; unchanged packages hit.

**Tech Stack:** Go; the existing `internal/codegen` warm `Module` (Phase 2b invalidation + Phase 2c fset rebuild).

## Global Constraints

- **Coherence with `pkgTypes`.** Every site that drops/clears `pkgTypes` (`invalidateLocked`, `rebuildFset`) MUST drop/clear `pkgResults` identically. A `pkgResults` entry must never outlive the `pkgTypes` of a package it (or its dep) was built against. (Spec §6.)
- **No orphaned positions.** `PackageResult` indexes into `m.fset` (`Info`/`Fset`/`ExprMap`/`CtrlMap`); `rebuildFset` MUST clear `pkgResults` together with the fset — a retained result after a rebuild resolves positions into the discarded fset. (Spec §3.4; Phase 2c invariant.)
- **`Package`-only.** Do NOT cache `Generate`, the batch path, or the adapted `lsp.Package`. `Generate`/`GeneratePackagesWithFilters` stay byte-for-byte as they are; the corpus equivalence gate MUST stay green. (Spec §1 non-goals.)
- **Edited package always fresh.** The cache read happens AFTER `applyDirty`, so a dirty (edited) package's entry is already dropped before the read. Never return a cached result for a package whose buffer changed. (Spec §3.2.)
- **`.x.go`-independent.** Unchanged — resolution flows through skeletons/`//line`.
- **No "simple heuristics."** Real implementations only (CLAUDE.md).
- **Locks.** `m.mu` guards `pkgResults` (alongside `pkgTypes`/etc.); `analysisMu` serializes `Package`. The cache read/write in `Package` take `m.mu` briefly; never hold `m.mu` across `analyze`. (module.go concurrency contract.)
- **gofmt.** Run `gofmt -w` on every edited file; CI enforces gofmt.
- **Every change ships test coverage** in `internal/codegen`. (CLAUDE.md / [[gsx-syntax-change-test-coverage]].)

---

## File Structure

- `internal/codegen/module.go` — `Module` gains `pkgResults map[string]*PackageResult`; `Open` inits it; `Package` reads/populates it; `rebuildFset` clears it.
- `internal/codegen/module_importer.go` — `invalidateLocked` deletes `pkgResults` in the closure loop; add a `cachedResultDirs()` test hook.
- `internal/codegen/snapshot_cache_test.go` — new unit tests (hit/miss by pointer identity, dependency invalidation, unrelated-no-drop, rebuild clears, hit-correctness/diags, concurrency).

---

## Task 1: The cache — field, invalidation, rebuild clear

**Files:**
- Modify: `internal/codegen/module.go` (`Module` field, `Open` init, `rebuildFset` clear)
- Modify: `internal/codegen/module_importer.go` (`invalidateLocked` drop, `cachedResultDirs()` hook)
- Test: `internal/codegen/snapshot_cache_test.go` (create)

**Interfaces:**
- Produces: `Module.pkgResults map[string]*PackageResult` (field). `(*Module).cachedResultDirs() []string` (test hook, sorted keys under `m.mu`). After this task `invalidateLocked` and `rebuildFset` drop `pkgResults`, but `Package` does NOT yet read/populate it (Task 2) — so `cachedResultDirs()` stays empty until Task 2. This task wires the DROP sites first so Task 2's populate is immediately coherent.

- [ ] **Step 1: Add the field + init.** In `module.go`, add to the `Module` struct (next to `pkgTypes`):
```go
	pkgResults map[string]*PackageResult // abs dir -> cached full analysis result (Package path only)
```
and in `Open`'s `&Module{...}` literal add:
```go
		pkgResults: map[string]*PackageResult{},
```
Run `gofmt -w internal/codegen/module.go` (the struct field comment alignment).

- [ ] **Step 2: Drop in `invalidateLocked`.** In `module_importer.go`, extend the closure loop:
```go
func (m *Module) invalidateLocked(dirs []string) {
	for d := range m.reverseClosure(dirs) {
		delete(m.pkgTypes, d)
		delete(m.pkgResults, d)
	}
}
```

- [ ] **Step 3: Clear in `rebuildFset`.** In `module.go`, add to `rebuildFset` (with the other resets, under the held `m.mu`):
```go
	m.pkgResults = map[string]*PackageResult{}
```

- [ ] **Step 4: Add the `cachedResultDirs` hook.** In `module_importer.go` (next to `cachedDirs`):
```go
// cachedResultDirs returns the sorted set of dirs with a cached PackageResult (test hook).
func (m *Module) cachedResultDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pkgResults))
	for d := range m.pkgResults {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
```
(`sort` is already imported in `module_importer.go`.)

- [ ] **Step 5: Write a guard test** (proves the drop sites are wired even before populate exists). Create `internal/codegen/snapshot_cache_test.go` (package `codegen`). Reuse `setupChainModule` from `invalidation_test.go`. Directly seed and drop:
```go
package codegen

import (
	"path/filepath"
	"testing"
)

func TestInvalidateDropsPkgResults(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	pages := filepath.Join(root, "pages")
	solo := filepath.Join(root, "solo")
	// Warm the import graph (analyze records util←components←pages edges). In Task 1
	// Package does NOT yet populate pkgResults, and no SetOverride was called, so
	// applyDirty is a no-op — we seed pkgResults by hand AFTER warming.
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(solo); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, d := range []string{util, comp, pages, solo} {
		m.pkgResults[d] = &PackageResult{}
	}
	m.mu.Unlock()
	// Invalidate util's reverse closure {util, components, pages}; solo is unrelated.
	m.Invalidate(util)
	got := m.cachedResultDirs()
	if len(got) != 1 || got[0] != solo {
		t.Errorf("Invalidate(util) must drop the util-importer closure from pkgResults and keep solo; remaining=%v", got)
	}
}

func TestRebuildClearsPkgResults(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	m.mu.Lock()
	m.pkgResults[comp] = &PackageResult{}
	m.mu.Unlock()
	m.rebuildFset()
	if got := m.cachedResultDirs(); len(got) != 0 {
		t.Errorf("rebuildFset must clear pkgResults; remaining=%v", got)
	}
}
```
(If the `TestInvalidateDropsPkgResults` re-seed comment is awkward, simplify: seed AFTER `Package(pages)` warms the graph, then `Invalidate(util)`, then assert empty — the graph edges come from the `Package(pages)` analyze. Keep the assertion: closure of `util` over `importedBy` = {util, components, pages}, all dropped.)

- [ ] **Step 6: Run + gofmt.** `go build ./...` clean; `gofmt -l internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/snapshot_cache_test.go` empty; `go test ./internal/codegen -run 'TestInvalidateDropsPkgResults|TestRebuildClearsPkgResults' -v` → PASS.

- [ ] **Step 7: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/snapshot_cache_test.go
git commit -m "feat(codegen): pkgResults cache field, dropped by invalidateLocked + rebuildFset"
```

---

## Task 2: `Package` reads + populates the cache

**Files:**
- Modify: `internal/codegen/module.go` (`Package`)
- Test: `internal/codegen/snapshot_cache_test.go`

**Interfaces:**
- Consumes: `pkgResults` (Task 1), `applyDirty`/`maybeRebuildFset` (Phase 2b/2c).
- Behavior: `Package(dir)` returns a cached `*PackageResult` for an unchanged `dir` (identical pointer), and re-analyzes + re-caches when `dir` or a dep changed (different pointer).

- [ ] **Step 1: Add the cache read + write to `Package`.** In `module.go`, `Package` currently is:
```go
	m.maybeRebuildFset()
	m.applyDirty()
	ext, err := m.externalImporter()
	...
	res.UnusedImports = detectUnusedImportsFromErrs(a.typeErrs, a.importSpecs, a.gsxFset)
	return res, nil
```
Insert the cache READ immediately after `m.applyDirty()` (before `externalImporter`):
```go
	m.applyDirty()
	m.mu.Lock()
	cached := m.pkgResults[dir]
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	ext, err := m.externalImporter()
```
and the cache WRITE immediately before the final `return res, nil`:
```go
	res.UnusedImports = detectUnusedImportsFromErrs(a.typeErrs, a.importSpecs, a.gsxFset)
	m.mu.Lock()
	m.pkgResults[dir] = res
	m.mu.Unlock()
	return res, nil
```
`gofmt -w internal/codegen/module.go`.

- [ ] **Step 2: Write the hit/miss test** (pointer identity is the observable). Append to `snapshot_cache_test.go`:
```go
func TestPackageResultCacheHitAndMiss(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	r1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Errorf("repeat Package(comp) with no edit must hit the cache (same pointer); got distinct results")
	}
	// Edit components → its dirty closure drops the cached result → re-analysis → new pointer.
	m.SetOverride(filepath.Join(comp, "card.gsx"), componentsEdited)
	r3, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if r3 == r1 {
		t.Errorf("Package(comp) after an edit must re-analyze (different pointer); got the stale cached result")
	}
}
```
(`componentsEdited` is an existing fixture in `invalidation_test.go` — reuse it.)

- [ ] **Step 3: Run.** `go test ./internal/codegen -run TestPackageResultCacheHitAndMiss -v` → PASS. `go build ./...` clean.

- [ ] **Step 4: Dependency-invalidation + unrelated-no-drop test.** Append:
```go
func TestPackageResultCacheDependencyInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	solo := filepath.Join(root, "solo")
	rc1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	rs1, err := m.Package(solo)
	if err != nil {
		t.Fatal(err)
	}
	// Edit util (a dep of components): components' cached result must drop; solo (unrelated) stays.
	m.SetOverride(filepath.Join(util, "util.gsx"),
		[]byte("package util\n\ncomponent Y(label string) {\n\t<em>{label}</em>\n}\n"))
	rc2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if rc2 == rc1 {
		t.Errorf("editing dep util must invalidate components' cached result (different pointer)")
	}
	rs2, err := m.Package(solo)
	if err != nil {
		t.Fatal(err)
	}
	if rs2 != rs1 {
		t.Errorf("editing util must NOT drop unrelated solo's cached result (same pointer expected)")
	}
}
```

- [ ] **Step 5: Run.** `go test ./internal/codegen -run TestPackageResultCacheDependencyInvalidation -v` → PASS.

- [ ] **Step 6: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/snapshot_cache_test.go
git commit -m "feat(codegen): Package returns/populates the pkgResults snapshot cache"
```

---

## Task 3: Correctness (hit resolves + diags), rebuild, concurrency, corpus

**Files:**
- Test: `internal/codegen/snapshot_cache_test.go`

**Interfaces:**
- Consumes: the populated cache (Task 2), `rebuildFset`, `fsetRebuildBytes`, the cross-pkg position helpers.

- [ ] **Step 1: Hit-correctness + diags-parity test.** A cache-hit result must answer go-to-def correctly and carry the same diagnostics as a fresh analysis. Warm `Package(components)` (resolves `util.Y` via its `Info`); call `Package(components)` again (a hit); assert the hit result resolves `util.Y` to `util/util.gsx` at the correct line (model the lookup on `gen/definition_invalidation_e2e_test.go`'s `badgeDeclLine` — scan `pr.Info.Uses` for `Y` from a `util`-suffixed package, resolve via `pr.Fset.Position`, assert filename ends in `.gsx` not `.x.go` and the line matches). For diags-parity: capture `r_hit.Diags`, then force a miss (edit then revert, or a fresh Module) and capture the fresh `Diags`, and assert they are equal (same count + messages) — i.e. the cache never serves diagnostics a fresh analysis wouldn't.

- [ ] **Step 2: Rebuild-then-resolve test.** Warm `Package(components)`; set `m.fsetRebuildBytes = 1`; `Package(components)` again (forces a rebuild via `maybeRebuildFset`, which clears `pkgResults`, then re-analyzes) — assert (a) a **new** pointer (the pre-rebuild result was cleared), (b) `m.rebuilds() > 0`, (c) `util.Y` still resolves to the correct `.gsx` position in the new result (no orphaned positions). This is the key adversarial test: a `rebuildFset` that failed to clear `pkgResults` would return the stale (orphaned-position) result here.

- [ ] **Step 3: Concurrency under `-race`.** Concurrent `Package` on one Module with interleaved hits and an occasional edit:
```go
func TestConcurrentPackageResultCache(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				m.SetOverride(filepath.Join(comp, "card.gsx"),
					fmt.Appendf(nil, "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
			}
			_, _ = m.Package(comp)
		}(i)
	}
	wg.Wait()
}
```
Run: `go test ./internal/codegen -run TestConcurrentPackageResultCache -race -count=1` → PASS (no race on `pkgResults`).

- [ ] **Step 4: Run the correctness/rebuild/concurrency tests + gofmt.** `go test ./internal/codegen -run 'TestPackageResultCache|TestConcurrentPackageResultCache|TestSnapshot' -count=1` (names per your tests). `gofmt -l` empty.

- [ ] **Step 5: Corpus gate** (the cache is `Package`-only; `Generate`/batch untouched): `go test ./internal/corpus -run TestCorpus -count=1` → PASS. (The full `TestModuleMatchesBatchOverCorpus` uses `Generate`, never `Package`, so it is unaffected — run it in the final whole-branch gate.)

- [ ] **Step 6: Commit.**
```bash
git add internal/codegen/snapshot_cache_test.go
git commit -m "test(codegen): snapshot cache hit resolves correctly, survives rebuild, race-clean"
```

---

## Final Whole-Branch Review

After all 3 tasks: dispatch the final reviewer (superpowers:requesting-code-review) on `git merge-base main HEAD`..HEAD. Then the full gate (per-package / lanes, NOT one oversubscribed `go test ./...`):
```bash
go build ./...
go test ./internal/codegen ./gen ./internal/lsp -count=1
go test ./internal/codegen -race -run 'Concurrent|Package|Invalidate|Rebuild' -count=1
go test ./internal/corpus -count=1 -timeout 560s   # incl. TestModuleMatchesBatchOverCorpus (~500s)
```
Review focus: every `pkgTypes` mutation site has a matching `pkgResults` mutation (grep `pkgTypes` vs `pkgResults`); the cache read is strictly AFTER `applyDirty`/`maybeRebuildFset` (never returns a stale/orphaned result); `Generate`/batch/corpus untouched; no `m.mu` held across `analyze`; the edited package always re-analyzes.

## Self-Review notes (spec coverage)

- Spec §3.1 (field) → Task 1 Step 1. §3.2 (Package read/populate) → Task 2. §3.3 (invalidateLocked drop) → Task 1 Step 2. §3.4 (rebuildFset clear) → Task 1 Step 3.
- Spec §7 tests: hit/miss pointer identity (T2), dep-invalidation + unrelated-no-drop (T2), rebuild clears + resolves (T3), hit-correctness + diags parity (T3), concurrency (T3), corpus (T3).
- Names: `pkgResults` (field), `cachedResultDirs()` (hook). Consistent.
