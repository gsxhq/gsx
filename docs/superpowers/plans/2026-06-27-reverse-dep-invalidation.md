# Phase 2b — Reverse-Dependency Invalidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the warm `Module`'s coarse `ResetPackageCache()` (wipes all project type info every keystroke) with gopls-style reverse-reflexive-transitive-closure invalidation, so editing package A re-type-checks only A and its transitive importers.

**Architecture:** A project-internal import graph (`imports`/`importedBy`) is maintained as a side-effect of `analyze`. `SetOverride` detects real content changes and marks the changed dir dirty. `Package`/`Generate` consume the pending-dirty set at the start of each run, dropping the reverse-reflexive-transitive closure from `pkgTypes`; everything outside the closure stays warm. All caching (`pkgTypes` + graph edges) is centralized at the tail of `analyze`, which also fixes a latent stale-cache bug.

**Tech Stack:** Go, `go/types`, `go/token`; the existing `internal/codegen` warm `Module`.

## Global Constraints

- **`.x.go`-independent.** Invalidation reads/writes only the in-memory `pkgTypes` cache and the skeleton-derived import graph. No generated `.x.go` is read or written. (Spec §1, §7.)
- **No "simple heuristics."** Real implementations only (CLAUDE.md). If a task tempts you toward an approximation, stop and ask.
- **Prefer unexported.** New methods/fields/test hooks start lowercase unless a consumer outside `internal/codegen` needs them. `Invalidate` is the one method that may stay exported (public Module API); everything else (`recordImports`, `reverseClosure`, `invalidateLocked`, `applyDirty`, `currentSource`, test hooks) is unexported. (CLAUDE.md.)
- **Locks.** `m.mu` guards `overrides`, `ext`, `pkgTypes`, and the new `imports`/`importedBy`/`dirty` maps. `analysisMu` serializes `Package`/`Generate`/`typesPackage`. Never call a method that locks `m.mu` while already holding `m.mu` (e.g. `isGsxPackage`, `recordImports`, `Invalidate` all lock `m.mu` themselves — resolve their inputs before taking the lock). (module.go concurrency contract.)
- **Corpus gate stays green.** These changes are analysis-internal; generated output is unchanged. Run `go test ./internal/corpus -run TestCorpus` (no `-update`) as part of the final review. (Spec §7.)
- **Every change ships test coverage** in `internal/codegen` (unit) and, for the LSP-facing behavior, `gen` (e2e). (CLAUDE.md / [[gsx-syntax-change-test-coverage]].)

---

## File Structure

- `internal/codegen/module.go` — `Module` struct gains `imports`, `importedBy`, `dirty` fields (init in `Open`); `SetOverride` gains content-diff dirtiness + `currentSource` helper; `Package`/`Generate` call `applyDirty()` at the top.
- `internal/codegen/module_importer.go` — `recordImports`, `reverseClosure`/`invalidateLocked`, `Invalidate`, `applyDirty`; `analyze` tail centralizes `pkgTypes[dir]` caching + `recordImports`; `typesPackageWith` drops its now-redundant cache write; `ResetPackageCache` removed (Task 5).
- `internal/codegen/invalidation_test.go` — new unit tests (graph, consistency, dirtiness, closure, concurrency).
- `gen/lsp.go` — `Analyze` drops the `m.ResetPackageCache()` line.
- `gen/definition_invalidation_e2e_test.go` — new e2e: edit a dependency buffer, importer reflects it, no `.x.go` on disk.

---

## Task 1: Import graph + `recordImports`

**Files:**
- Modify: `internal/codegen/module.go` (struct fields + `Open`)
- Modify: `internal/codegen/module_importer.go` (`recordImports`; call site in `analyze`; test hook)
- Test: `internal/codegen/invalidation_test.go` (create)

**Interfaces:**
- Produces: `(*Module).recordImports(dir string, specs []importSpec)` — maintains `imports`/`importedBy`, replacing `dir`'s edges. `(*Module).importGraphSnapshot() (map[string][]string, map[string][]string)` — test-only deep copy of `imports` and a flattened `importedBy` for assertions.
- Consumes: `dirForImportPath` (module_importer.go:36), `isGsxPackage` (module_importer.go:123), `importSpec.path` (analyze.go:1887).

- [ ] **Step 1: Add struct fields + init.** In `module.go`, add to `Module`:
```go
	imports    map[string][]string         // dir -> its project-gsx dependency dirs (forward edges)
	importedBy map[string]map[string]bool  // dep dir -> set of importer dirs (reverse edges)
	dirty      map[string]bool             // dirs with a pending content change (consumed by applyDirty)
```
and in `Open`'s returned literal, add `imports: map[string][]string{}, importedBy: map[string]map[string]bool{}, dirty: map[string]bool{}`.

- [ ] **Step 2: Write `recordImports`** in `module_importer.go`:
```go
// recordImports updates the project-internal import graph for dir, REPLACING
// dir's previous forward edges. Only project gsx packages (the things that can
// live in pkgTypes) become edges; external/stdlib/Go-only imports are ignored.
// Replacement keeps the graph precise across import add/remove: because the
// edited package always re-analyzes in the same turn, its outgoing edges are
// refreshed before any later edit could consult them.
//
// Resolves dep dirs (isGsxPackage locks m.mu) BEFORE taking m.mu, then mutates
// the graph under the lock.
func (m *Module) recordImports(dir string, specs []importSpec) {
	deps := map[string]bool{}
	for _, s := range specs {
		if dd, ok := dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, s.path); ok && m.isGsxPackage(dd) {
			deps[dd] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.imports[dir] {
		if set := m.importedBy[old]; set != nil {
			delete(set, dir)
		}
	}
	newDeps := make([]string, 0, len(deps))
	for dd := range deps {
		newDeps = append(newDeps, dd)
		if m.importedBy[dd] == nil {
			m.importedBy[dd] = map[string]bool{}
		}
		m.importedBy[dd][dir] = true
	}
	m.imports[dir] = newDeps
}
```

- [ ] **Step 3: Call `recordImports` at the tail of `analyze`.** In `module_importer.go`, immediately before `analyze`'s final `return &analyzed{...}, nil` (after the `mi.cycleErr` early-return at the current line ~315, so only successful analyses record), insert:
```go
	m.recordImports(dir, allImportSpecs)
```
(`allImportSpecs` is the local already built in the file loop.)

- [ ] **Step 4: Add the test hook** in `module_importer.go`:
```go
// importGraphSnapshot returns deep copies of the forward and reverse import
// graphs for tests. Reverse edges are flattened (dep -> sorted importer dirs).
func (m *Module) importGraphSnapshot() (fwd map[string][]string, rev map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fwd = map[string][]string{}
	for k, v := range m.imports {
		fwd[k] = append([]string(nil), v...)
	}
	rev = map[string][]string{}
	for dep, set := range m.importedBy {
		for imp := range set {
			rev[dep] = append(rev[dep], imp)
		}
		sort.Strings(rev[dep])
	}
	return fwd, rev
}
```
Add `"sort"` to the import block if not present.

- [ ] **Step 5: Write the failing test.** Create `internal/codegen/invalidation_test.go` (package `codegen`, in-package — so it can call unexported `Open`, `Package`, `importGraphSnapshot`, etc.). Model the temp-module helper directly on `writeCrossPkgModule` in `internal/codegen/batch_crosspkg_test.go` (read it first): `root := t.TempDir()`; a `must(path, content)` closure that `MkdirAll`+`WriteFile`s; a `go.mod` of `module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => <repoRoot>` (compute `repoRoot` exactly as that helper does); per-package `.gsx` files where a subpackage imports another via `import "example.com/x/<sub>"`. Construct the Module in-package: `m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x", FilterPkgs: []string{StdImportPath}})`. Build `setupChainModule(t) (*Module, string)` returning the Module and `root`, with a 3-package chain where `pages` imports `components` imports `util` (a gsx component in `pages` renders `<components.X/>`, one in `components` renders `<util.Y/>`), plus an unrelated `solo` package. Then:
```go
func TestImportGraphRecorded(t *testing.T) {
	m, root := setupChainModule(t) // helper: util<-components<-pages + solo; returns Module + module root
	pagesDir := filepath.Join(root, "pages")
	if _, err := m.Package(pagesDir); err != nil {
		t.Fatalf("analyze pages: %v", err)
	}
	if _, err := m.Package(filepath.Join(root, "solo")); err != nil {
		t.Fatalf("analyze solo: %v", err)
	}
	fwd, rev := m.importGraphSnapshot()
	// pages -> components, components -> util.
	assertEdge(t, fwd, filepath.Join(root, "pages"), filepath.Join(root, "components"))
	assertEdge(t, fwd, filepath.Join(root, "components"), filepath.Join(root, "util"))
	// reverse: util importedBy components; components importedBy pages.
	assertEdge(t, rev, filepath.Join(root, "util"), filepath.Join(root, "components"))
	assertEdge(t, rev, filepath.Join(root, "components"), filepath.Join(root, "pages"))
	// solo has no project edges.
	if len(fwd[filepath.Join(root, "solo")]) != 0 {
		t.Errorf("solo should have no deps, got %v", fwd[filepath.Join(root, "solo")])
	}
}
```
Write `setupChainModule`, `assertEdge` helpers in the same file. (`assertEdge(t, g, from, to)` fails unless `to` ∈ `g[from]`.)

- [ ] **Step 6: Run, expect FAIL** (graph empty before Steps 2-3 wired): `go test ./internal/codegen -run TestImportGraphRecorded -v` → FAIL.
- [ ] **Step 7: Confirm Steps 2-4 make it PASS:** `go test ./internal/codegen -run TestImportGraphRecorded -v` → PASS.

- [ ] **Step 8: Edge-replacement test.** Add:
```go
func TestImportGraphEdgeReplacedOnImportRemoval(t *testing.T) {
	m, root := setupChainModule(t)
	compDir := filepath.Join(root, "components")
	utilDir := filepath.Join(root, "util")
	if _, err := m.Package(compDir); err != nil { t.Fatal(err) }
	_, rev := m.importGraphSnapshot()
	assertEdge(t, rev, utilDir, compDir) // components importedBy util initially
	// Edit components to drop its import of util (override with a version that
	// no longer references util).
	m.SetOverride(filepath.Join(compDir, "card.gsx"), componentsWithoutUtil)
	if _, err := m.Package(compDir); err != nil { t.Fatal(err) }
	_, rev = m.importGraphSnapshot()
	if contains(rev[utilDir], compDir) {
		t.Errorf("after removing import, util.importedBy should not contain components")
	}
}
```
Provide `componentsWithoutUtil []byte` (a `card.gsx` body that compiles without importing `util`) and a `contains` helper.

- [ ] **Step 9: Run both tests:** `go test ./internal/codegen -run TestImportGraph -v` → PASS.

- [ ] **Step 10: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/invalidation_test.go
git commit -m "feat(codegen): project import graph maintained by analyze"
```

---

## Task 2: Centralize `pkgTypes` caching in `analyze` (consistency fix)

**Files:**
- Modify: `internal/codegen/module_importer.go` (`analyze` tail; `typesPackageWith`)
- Test: `internal/codegen/invalidation_test.go`

**Interfaces:**
- Produces: `(*Module).cachedDirs() []string` — test-only sorted snapshot of `pkgTypes` keys.
- Behavior change: after any successful `analyze(dir)` — whether reached via `Package`, `Generate`, or the recursive importer — `pkgTypes[dir]` holds the freshly checked package. `Package(A)`/`Generate(A)` now populate `pkgTypes[A]` (they did not before).

- [ ] **Step 1: Write the failing regression test** (fails before the fix because `Package(A)` never cached `pkgTypes[A]`, so an importer would re-check or see a stale A):
```go
func TestPackageCachesEditedPackageForImporters(t *testing.T) {
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	// Analyze util alone (as an LSP target).
	if _, err := m.Package(utilDir); err != nil { t.Fatal(err) }
	if !contains(m.cachedDirs(), utilDir) {
		t.Fatalf("Package(util) must populate pkgTypes[util]; cached=%v", m.cachedDirs())
	}
	// Add a new exported symbol to util via override.
	m.SetOverride(filepath.Join(utilDir, "helper.gsx"), utilWithNewExport)
	if _, err := m.Package(utilDir); err != nil { t.Fatal(err) }
	// components imports util: analyzing it must resolve the NEW symbol's type,
	// proving it read the fresh cached util (not a stale one).
	pr, err := m.Package(filepath.Join(root, "components"))
	if err != nil { t.Fatal(err) }
	assertResolvesUtilSymbol(t, pr) // helper: the new util export is typed (non-nil) in components' Info
}
```
Provide `utilWithNewExport []byte` and `assertResolvesUtilSymbol`. (If writing `assertResolvesUtilSymbol` against `Info` is awkward, assert instead that `components` produces zero "undefined"/type-error diagnostics referencing the new symbol via `pr.Diags`.)

- [ ] **Step 2: Add the `cachedDirs` hook** in `module_importer.go`:
```go
// cachedDirs returns the sorted set of dirs currently in pkgTypes (test hook).
func (m *Module) cachedDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pkgTypes))
	for d := range m.pkgTypes {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 3: Run, expect FAIL:** `go test ./internal/codegen -run TestPackageCachesEditedPackageForImporters -v` → FAIL (pkgTypes[util] absent after Package, or stale resolution).

- [ ] **Step 4: Centralize caching in `analyze`.** In `module_importer.go`, at the tail of `analyze`, just before `m.recordImports(dir, allImportSpecs)` (Task 1 Step 3), insert the cache write:
```go
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = pkg
	m.mu.Unlock()
	m.recordImports(dir, allImportSpecs)
```
(`pkg` is the local returned by `checkSkeletonPackage`.)

- [ ] **Step 5: Remove the now-redundant cache write in `typesPackageWith`.** Delete its post-`analyze` block:
```go
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = a.pkg
	m.mu.Unlock()
```
so `typesPackageWith` becomes: cache-hit check → `analyze` → `return a.pkg, nil`. (The cache-hit check at the top stays; `analyze` now does the write.)

- [ ] **Step 6: Run the regression test:** `go test ./internal/codegen -run TestPackageCachesEditedPackageForImporters -v` → PASS.

- [ ] **Step 7: Run the codegen suite** to confirm no regression in cycle-guard / cross-pkg behavior: `go test ./internal/codegen -count=1` → PASS.

- [ ] **Step 8: Commit.**
```bash
git add internal/codegen/module_importer.go internal/codegen/invalidation_test.go
git commit -m "fix(codegen): centralize pkgTypes caching in analyze; Package/Generate now cache the edited package"
```

---

## Task 3: Content-diff dirtiness in `SetOverride`

**Files:**
- Modify: `internal/codegen/module.go` (`SetOverride`, new `currentSource`)
- Test: `internal/codegen/invalidation_test.go`

**Interfaces:**
- Produces: `(*Module).dirtyDirs() []string` — test-only sorted snapshot of the `dirty` set keys (does NOT clear it).
- Behavior: `SetOverride(path, src)` marks `filepath.Dir(path)` dirty iff `src` differs from the current source (override-or-disk). Identical bytes mark nothing. Disk is read only when no prior override exists for `path`.

- [ ] **Step 1: Write `currentSource`** in `module.go` — a lock-free-on-entry variant `SetOverride` can use (it reads `overrides` under a short lock, then reads disk outside any lock):
```go
// currentSource returns the bytes currently backing absPath (override if present,
// else disk) and whether any source was found. Used by SetOverride to detect a
// real content change. It takes m.mu only briefly to read the override map and
// reads disk outside the lock.
func (m *Module) currentSource(absPath string) ([]byte, bool) {
	m.mu.Lock()
	ov, ok := m.overrides[absPath]
	m.mu.Unlock()
	if ok {
		return ov, true
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, false
	}
	return b, true
}
```
(This duplicates `source`'s logic; leave `source` as-is — it is used elsewhere. If `source` is identical, have `source` call `currentSource` to stay DRY. Verify `source`'s body before deciding.)

- [ ] **Step 2: Rewrite `SetOverride`** in `module.go`:
```go
func (m *Module) SetOverride(absPath string, src []byte) {
	base, haveBase := m.currentSource(absPath)
	changed := !haveBase || !bytes.Equal(base, src)
	m.mu.Lock()
	if changed {
		if m.dirty == nil {
			m.dirty = map[string]bool{}
		}
		m.dirty[filepath.Dir(absPath)] = true
	}
	m.overrides[absPath] = src
	m.mu.Unlock()
}
```
Add `"bytes"` and `"path/filepath"` to `module.go`'s imports if absent. Update `SetOverride`'s doc comment: it now marks the dir dirty on a real content change; invalidation is applied lazily by `applyDirty` at the next `Package`/`Generate`.

- [ ] **Step 3: Add the `dirtyDirs` hook** in `module.go`:
```go
// dirtyDirs returns the sorted pending-dirty dirs (test hook; does not clear).
func (m *Module) dirtyDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.dirty))
	for d := range m.dirty {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
```
Add `"sort"` if absent.

- [ ] **Step 4: Write the test:**
```go
func TestSetOverrideDirtinessDetection(t *testing.T) {
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	helper := filepath.Join(utilDir, "helper.gsx")
	disk, _ := os.ReadFile(helper)
	// Identical-to-disk override (didOpen) marks nothing dirty.
	m.SetOverride(helper, disk)
	if got := m.dirtyDirs(); len(got) != 0 {
		t.Errorf("identical-to-disk override must not mark dirty; got %v", got)
	}
	// A real change marks util dirty.
	m.SetOverride(helper, utilWithNewExport)
	if !contains(m.dirtyDirs(), utilDir) {
		t.Errorf("changed override must mark util dirty; got %v", m.dirtyDirs())
	}
	// Re-setting the same changed bytes does not un-mark, but a no-op set of the
	// now-current override bytes adds nothing new.
	m.SetOverride(helper, utilWithNewExport)
	if got := m.dirtyDirs(); !contains(got, utilDir) {
		t.Errorf("dirty must persist until consumed; got %v", got)
	}
}
```

- [ ] **Step 5: Run:** `go test ./internal/codegen -run TestSetOverrideDirtinessDetection -v` → PASS.

- [ ] **Step 6: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/invalidation_test.go
git commit -m "feat(codegen): SetOverride marks the changed dir dirty on a real content change"
```

---

## Task 4: Reverse-closure invalidation + wire into Package/Generate

**Files:**
- Modify: `internal/codegen/module_importer.go` (`reverseClosure`/`invalidateLocked`, `Invalidate`, `applyDirty`)
- Modify: `internal/codegen/module.go` (`Package`/`Generate` call `applyDirty` at top)
- Test: `internal/codegen/invalidation_test.go`

**Interfaces:**
- Produces:
  - `(*Module).Invalidate(dirs ...string)` — drops the reverse-reflexive-transitive closure of `dirs` (over `importedBy`) from `pkgTypes`. Exported (public Module API).
  - `(*Module).applyDirty()` — consumes the `dirty` set: invalidates its closure, then clears `dirty`. Unexported.
- Consumes: `importedBy` (Task 1), `dirty` (Task 3), `pkgTypes`, `analysisMu` (already held by Package/Generate).

- [ ] **Step 1: Write `reverseClosure` + `invalidateLocked`** in `module_importer.go` (both assume `m.mu` held):
```go
// reverseClosure returns the reverse-reflexive-transitive closure of seeds over
// importedBy: each seed plus every dir that transitively imports it. Assumes m.mu.
func (m *Module) reverseClosure(seeds []string) map[string]bool {
	out := map[string]bool{}
	stack := append([]string(nil), seeds...)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if out[d] {
			continue
		}
		out[d] = true
		for importer := range m.importedBy[d] {
			if !out[importer] {
				stack = append(stack, importer)
			}
		}
	}
	return out
}

// invalidateLocked drops the reverse-closure of dirs from pkgTypes. Assumes m.mu.
func (m *Module) invalidateLocked(dirs []string) {
	for d := range m.reverseClosure(dirs) {
		delete(m.pkgTypes, d)
	}
}
```

- [ ] **Step 2: Write `Invalidate` + `applyDirty`** in `module_importer.go`:
```go
// Invalidate drops the reverse-reflexive-transitive closure of dirs (the dirs
// plus every project gsx package that transitively imports them) from pkgTypes,
// so each is re-type-checked from current skeletons on next use. Graph edges are
// retained (refreshed on re-analyze). Everything outside the closure stays warm.
// This supersedes the coarse whole-cache reset.
func (m *Module) Invalidate(dirs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateLocked(dirs)
}

// applyDirty consumes the pending-dirty set (populated by SetOverride): it drops
// the reverse-closure of the dirty dirs from pkgTypes and clears the set. Called
// at the start of each Package/Generate run (under analysisMu).
func (m *Module) applyDirty() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dirty) == 0 {
		return
	}
	seeds := make([]string, 0, len(m.dirty))
	for d := range m.dirty {
		seeds = append(seeds, d)
	}
	m.invalidateLocked(seeds)
	m.dirty = map[string]bool{}
}
```

- [ ] **Step 3: Wire `applyDirty` into `Package` and `Generate`.** In `module.go`, as the first statement inside each (after `m.analysisMu.Lock(); defer m.analysisMu.Unlock()`, before `m.externalImporter()`), add:
```go
	m.applyDirty()
```

- [ ] **Step 4: Write the closure tests:**
```go
func TestEditInvalidatesReverseClosureOnly(t *testing.T) {
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	pages := filepath.Join(root, "pages")
	solo := filepath.Join(root, "solo")
	// Warm everything.
	for _, d := range []string{pages, solo} {
		if _, err := m.Package(d); err != nil { t.Fatal(err) }
	}
	// All four cached (pages pulls util+components transitively; solo standalone).
	for _, d := range []string{util, comp, pages, solo} {
		if !contains(m.cachedDirs(), d) { t.Fatalf("expected %s cached; got %v", d, m.cachedDirs()) }
	}
	// Edit components: closure {components, pages} drops; util + solo stay.
	m.SetOverride(filepath.Join(comp, "card.gsx"), componentsEdited)
	m.applyDirty() // simulate the start of the next analysis
	cached := m.cachedDirs()
	if contains(cached, comp) || contains(cached, pages) {
		t.Errorf("components/pages should be invalidated; cached=%v", cached)
	}
	if !contains(cached, util) || !contains(cached, solo) {
		t.Errorf("util/solo must stay warm; cached=%v", cached)
	}
}

func TestEditLeafInvalidatesOnlyItself(t *testing.T) {
	m, root := setupChainModule(t)
	pages := filepath.Join(root, "pages")
	if _, err := m.Package(pages); err != nil { t.Fatal(err) }
	m.SetOverride(filepath.Join(pages, "index.gsx"), pagesEdited)
	m.applyDirty()
	// util + components (pages' deps, nothing imports pages) stay cached.
	if !contains(m.cachedDirs(), filepath.Join(root, "util")) ||
		!contains(m.cachedDirs(), filepath.Join(root, "components")) {
		t.Errorf("editing pages (a leaf importer) must not invalidate its deps; cached=%v", m.cachedDirs())
	}
}

func TestNoOpEditInvalidatesNothing(t *testing.T) {
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	if _, err := m.Package(filepath.Join(root, "pages")); err != nil { t.Fatal(err) }
	before := m.cachedDirs()
	disk, _ := os.ReadFile(filepath.Join(comp, "card.gsx"))
	m.SetOverride(filepath.Join(comp, "card.gsx"), disk) // identical
	m.applyDirty()
	if got := m.cachedDirs(); !sameSet(got, before) {
		t.Errorf("no-op edit must not invalidate; before=%v after=%v", before, got)
	}
}
```
Provide `componentsEdited`, `pagesEdited` (real content changes that still compile) and a `sameSet` helper.

- [ ] **Step 5: Run:** `go test ./internal/codegen -run 'TestEdit|TestNoOp' -v` → PASS.

- [ ] **Step 6: Re-resolution test** (proves the dropped importer actually re-checks against fresh deps end-to-end through `Package`, not just cache bookkeeping):
```go
func TestImporterReResolvesAgainstEditedDependency(t *testing.T) {
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	if _, err := m.Package(comp); err != nil { t.Fatal(err) } // warms util+components
	// Change a util export that components depends on, then re-analyze components
	// WITHOUT explicitly invalidating: applyDirty (inside Package) must do it.
	m.SetOverride(filepath.Join(util, "helper.gsx"), utilWithNewExport)
	pr, err := m.Package(comp)
	if err != nil { t.Fatal(err) }
	assertResolvesUtilSymbol(t, pr) // new util symbol typed in components
}
```

- [ ] **Step 7: Run:** `go test ./internal/codegen -run TestImporterReResolves -v` → PASS.

- [ ] **Step 8: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/invalidation_test.go
git commit -m "feat(codegen): reverse-closure invalidation via Invalidate/applyDirty in Package/Generate"
```

---

## Task 5: LSP wiring + remove `ResetPackageCache` + concurrency + e2e

**Files:**
- Modify: `gen/lsp.go` (`Analyze` drops `ResetPackageCache`)
- Modify: `internal/codegen/module_importer.go` (delete `ResetPackageCache`)
- Modify: `internal/codegen/module.go` (update the stale "Phase 2" comments that promised this work)
- Test: `internal/codegen/invalidation_test.go` (concurrency), `gen/definition_invalidation_e2e_test.go` (create)

**Interfaces:**
- Consumes: the warm self-invalidating Module (Tasks 1-4). After this task the LSP performs no manual cache management — `SetOverride` + `Package` is the whole contract.

- [ ] **Step 1: Drop the LSP reset call.** In `gen/lsp.go:Analyze`, delete the line:
```go
	m.ResetPackageCache() // Phase-1: project types fresh per edit; ext stays warm
```
Update the `module` doc comment (`gen/lsp.go:46-48`) that tells callers to call `ResetPackageCache()` before each `Package()` — replace with a note that the Module self-invalidates from `SetOverride` content diffs.

- [ ] **Step 2: Delete `ResetPackageCache`** from `module_importer.go` (the whole method + its doc comment). Confirm no remaining callers:
```bash
grep -rn "ResetPackageCache" --include=*.go .
```
Expected: no matches. (If a test references it, migrate that test to `Invalidate` or delete it.)

- [ ] **Step 3: Refresh stale comments** in `module.go`: the struct doc's "Cache invalidation: pkgTypes and ext are NOT invalidated on SetOverride — ... Invalidate is Phase 2." and the `SetOverride` "must call Invalidate (Phase 2)" lines, and the `ResetPackageCache ... Phase 2 replaces it` note — rewrite to describe the now-shipped reverse-closure invalidation. Keep the FileSet "Growth" note (still accurate; that bounding is Phase 2c).

- [ ] **Step 4: Build + full codegen/gen suites:**
```bash
go build ./... && go test ./internal/codegen ./gen -count=1
```
Expected: PASS.

- [ ] **Step 5: Concurrency test** (extends the existing race coverage; run under `-race`). Add to `invalidation_test.go`:
```go
func TestConcurrentSetOverrideAndPackage(t *testing.T) {
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); m.SetOverride(filepath.Join(comp, "card.gsx"), componentsEdited) }()
		go func() { defer wg.Done(); _, _ = m.Package(comp) }()
	}
	wg.Wait()
}
```

- [ ] **Step 6: Run under race:** `go test ./internal/codegen -run TestConcurrentSetOverrideAndPackage -race -count=1` → PASS (no data race on `imports`/`importedBy`/`dirty`/`pkgTypes`).

- [ ] **Step 7: Write the e2e.** Create `gen/definition_invalidation_e2e_test.go`, modeled on `gen/definition_controlflow_e2e_test.go` (reuse its `runLSP` JSON-RPC harness). Two project packages: `widgets` (a component `Badge`) and `home` (imports `widgets`, renders `<widgets.Badge .../>` and references a `Badge`-exported symbol). Drive the server:
  1. `initialize`.
  2. `didOpen` both files (disk content).
  3. `didChange` the `widgets` buffer to add/rename an exported symbol used by `home`.
  4. `didChange`/request go-to-def or hover in `home` on that symbol → assert it resolves to the EDITED `widgets` position (proving `home` re-resolved against the invalidated, re-checked `widgets`).
  5. Assert no `.x.go` exists on disk anywhere under the temp module (the standard `.x.go`-free guard, as in the controlflow e2e).

- [ ] **Step 8: Run the e2e:** `go test ./gen -run TestDefinitionInvalidation -v` → PASS.

- [ ] **Step 9: Corpus gate** (must stay green — analysis-internal change): `go test ./internal/corpus -run TestCorpus -count=1` → PASS.

- [ ] **Step 10: Commit.**
```bash
git add gen/lsp.go internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/invalidation_test.go gen/definition_invalidation_e2e_test.go
git commit -m "feat(lsp): drop coarse ResetPackageCache; Module self-invalidates via reverse-closure"
```

---

## Final Whole-Branch Review

After all 5 tasks: dispatch the final reviewer (superpowers:requesting-code-review) on the full branch diff (`git merge-base main HEAD`..HEAD). Then run the full gate before merge:
```bash
go build ./... && go test ./... && go test ./internal/codegen -race -count=1 && go test ./internal/corpus -run TestCorpus -count=1
```
Review focus: the closure traversal (reflexive + transitive correctness, no infinite loop on import cycles — the `out[d]` guard handles cycles), lock discipline (no `m.mu` re-entry; `isGsxPackage`/`recordImports`/`Invalidate` inputs resolved before locking), the `.x.go`-free invariant, and that the consistency fix did not change corpus output.

## Self-Review notes (spec coverage)

- Spec §4.1 graph → Task 1. §4.2 dirtiness → Task 3. §4.3 closure/applyDirty → Task 4. §4.4 consistency fix → Task 2. §4.5 LSP wiring → Task 5.
- Spec §8 tests: graph + edge-replacement (T1), consistency (T2), dirtiness incl. no-op (T3), closure chain/leaf/no-op + re-resolution (T4), concurrency + e2e + corpus (T5).
- Type names used consistently: `recordImports`, `reverseClosure`, `invalidateLocked`, `Invalidate`, `applyDirty`, `currentSource`, `cachedDirs`, `dirtyDirs`, `importGraphSnapshot`.
