# Phase 2c — Bound Shared-FileSet Growth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the warm `Module`'s lifetime `*token.FileSet` (`m.fset`) from growing unboundedly across re-analyses by rebuilding `fset`+`ext`+`pkgTypes` together when project re-parse growth crosses a threshold — output-neutral, no orphaned positions.

**Architecture:** Measure growth as `m.fset.Base() - m.fsetBaseline` where `fsetBaseline` is captured after each `packages.Load`. At the start of `Package`/`Generate` (under `analysisMu`, before `applyDirty`), if growth exceeds `fsetRebuildBytes`, reset `fset`/`ext`/`pkgTypes`/`fsetBaseline` together. The import graph, dirty set, and overrides survive (path/content-based), so reverse-dep invalidation keeps working immediately.

**Tech Stack:** Go, `go/token`, `go/types`, `golang.org/x/tools/go/packages`; the existing `internal/codegen` warm `Module`.

## Global Constraints

- **No orphaned positions.** `fset`, `ext`, and `pkgTypes` MUST reset atomically together; never rebuild the fset while keeping `ext` or `pkgTypes` (they hold positions into the old fset). (Spec §3.2, §6.)
- **Output-neutral.** A rebuild MUST NOT change generated `.x.go` bytes; the corpus byte-equivalence gate (`go test ./internal/corpus`) MUST stay green. The threshold is NOT in `computeKey`. (Spec §6.)
- **`.x.go`-independent.** Unchanged — resolution still flows through skeletons/`//line`, never generated `.x.go`.
- **Internal knob, not user config.** The threshold is an internal perf knob like `GSXCACHE`: an internal default constant + env `GSX_FSET_REBUILD_BYTES`. NOT in `gsx.toml`, NOT in `computeKey`, NOT documented in `docs/guide/config.md`. (CLAUDE.md config guidance; spec §3.1.)
- **No "simple heuristics."** Real implementations only (CLAUDE.md). A byte threshold for a cache bound is a real, measurable limit, not a heuristic — but if any sub-decision tempts you toward an approximation, stop and ask.
- **Locks.** `m.mu` guards `overrides`/`ext`/`pkgTypes`/`imports`/`importedBy`/`dirty` and the new `fset`/`fsetBaseline`/`fsetRebuildBytes`/`rebuilds` fields. `analysisMu` serializes `Package`/`Generate`/`typesPackage`. `maybeRebuildFset`/`rebuildFset` run under `analysisMu` (called from `Package`/`Generate`) and take `m.mu` for field writes; the recursive importer path MUST NOT trigger a rebuild. (Spec §6; module.go concurrency contract.)
- **Every change ships test coverage** in `internal/codegen` (and a `gen` e2e for the LSP-facing correctness). (CLAUDE.md / [[gsx-syntax-change-test-coverage]].)

---

## File Structure

- `internal/codegen/module.go` — `Module` gains `fsetBaseline int`, `fsetRebuildBytes int`, `rebuilds int`; `Open` sets `fsetRebuildBytes` (default const + env); `externalImporter` captures `fsetBaseline` after the load; `Package`/`Generate` call `maybeRebuildFset()` before `applyDirty()`; new `maybeRebuildFset`/`rebuildFset`/`rebuilds()` methods; `defaultFsetRebuildBytes` const + `fsetRebuildBytesFromEnv` helper.
- `internal/codegen/fset_growth_test.go` — new unit tests (growth bounded, disabled, correctness-across-rebuild, graph-survives, output-identical, concurrency).
- `gen/definition_fset_rebuild_e2e_test.go` — new e2e: cross-package go-to-def through the real LSP still resolves correctly after a forced rebuild, no `.x.go` on disk.

---

## Task 1: Threshold config knob

**Files:**
- Modify: `internal/codegen/module.go` (struct fields, `Open`, const + env helper)
- Test: `internal/codegen/fset_growth_test.go` (create)

**Interfaces:**
- Produces: `Module.fsetRebuildBytes int` (threshold; 0 = disabled), `Module.fsetBaseline int`, `Module.rebuilds int` (fields). `defaultFsetRebuildBytes` const. `fsetRebuildBytesFromEnv() int` helper.

- [ ] **Step 1: Add fields + const.** In `module.go`, add to the `Module` struct (after `dirty`):
```go
	fsetBaseline     int // m.fset.Base() captured after the last packages.Load (growth measured since here)
	fsetRebuildBytes int // rebuild fset when fset.Base()-fsetBaseline exceeds this; 0 disables
	rebuildCount     int // count of fset rebuilds performed (observability; exposed via rebuilds())
```
and above `Open`, add:
```go
// defaultFsetRebuildBytes bounds the module-lifetime FileSet's project re-parse
// growth: when fset.Base() climbs this many bytes past the post-load baseline, the
// Module rebuilds fset+ext+pkgTypes. 256 MiB is generous enough that a rebuild is
// rare (tens of full re-analyses of a large package) yet caps leaked token.File
// memory. Internal perf knob (not gsx.toml / computeKey); overridable via
// GSX_FSET_REBUILD_BYTES (0 disables; like GSXCACHE).
const defaultFsetRebuildBytes = 256 << 20

// fsetRebuildBytesFromEnv returns the GSX_FSET_REBUILD_BYTES override if set to a
// valid non-negative integer (0 disables rebuilding), else defaultFsetRebuildBytes.
func fsetRebuildBytesFromEnv() int {
	if v, ok := os.LookupEnv("GSX_FSET_REBUILD_BYTES"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultFsetRebuildBytes
}
```
Add `"strconv"` to `module.go` imports (`os` is already imported).

- [ ] **Step 2: Init in `Open`.** In `Open`'s returned `&Module{...}` literal, add:
```go
		fsetRebuildBytes: fsetRebuildBytesFromEnv(),
```

- [ ] **Step 3: Write the test.** Create `internal/codegen/fset_growth_test.go` (package `codegen`):
```go
package codegen

import (
	"os"
	"testing"
)

func TestOpenReadsFsetRebuildThreshold(t *testing.T) {
	// Default when env unset.
	t.Setenv("GSX_FSET_REBUILD_BYTES", "")
	os.Unsetenv("GSX_FSET_REBUILD_BYTES")
	m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.fsetRebuildBytes != defaultFsetRebuildBytes {
		t.Errorf("default threshold = %d, want %d", m.fsetRebuildBytes, defaultFsetRebuildBytes)
	}
	// Env override (including 0 = disabled).
	for _, tc := range []struct {
		env  string
		want int
	}{{"4096", 4096}, {"0", 0}, {"bogus", defaultFsetRebuildBytes}, {"-5", defaultFsetRebuildBytes}} {
		t.Setenv("GSX_FSET_REBUILD_BYTES", tc.env)
		m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
		if err != nil {
			t.Fatal(err)
		}
		if m.fsetRebuildBytes != tc.want {
			t.Errorf("env %q: threshold = %d, want %d", tc.env, m.fsetRebuildBytes, tc.want)
		}
	}
}
```

- [ ] **Step 4: Run.** `go test ./internal/codegen -run TestOpenReadsFsetRebuildThreshold -v` → PASS. `go build ./...` clean.

- [ ] **Step 5: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/fset_growth_test.go
git commit -m "feat(codegen): GSX_FSET_REBUILD_BYTES threshold knob for fset growth bounding"
```

---

## Task 2: Rebuild mechanism + wiring + growth-bound test

**Files:**
- Modify: `internal/codegen/module.go` (`externalImporter` baseline capture; `maybeRebuildFset`/`rebuildFset`/`rebuilds()`; `Package`/`Generate` wiring)
- Test: `internal/codegen/fset_growth_test.go`

**Interfaces:**
- Consumes: `Module.fsetBaseline`/`fsetRebuildBytes`/`rebuilds` (Task 1), `m.fset.Base()`.
- Produces: `(*Module).maybeRebuildFset()`, `(*Module).rebuildFset()`, `(*Module).rebuilds() int` (test hook, reads the counter under `m.mu`). After this task, `Package`/`Generate` rebuild the fset when growth exceeds the threshold.

- [ ] **Step 1: Capture the baseline in `externalImporter`.** In `module.go`, find the block that sets `m.ext`:
```go
	m.mu.Lock()
	m.ext = mapImporter(mp)
	m.mu.Unlock()
```
and add the baseline capture in the same critical section:
```go
	m.mu.Lock()
	m.ext = mapImporter(mp)
	m.fsetBaseline = m.fset.Base()
	m.mu.Unlock()
```
(Growth is measured from the moment `ext` is loaded into the fset, so the fixed `ext` baseline does not count toward the threshold.)

- [ ] **Step 2: Add `rebuildFset` + `maybeRebuildFset` + `rebuilds()`** in `module.go` (place near `applyDirty`'s definition region or after `Open`):
```go
// maybeRebuildFset rebuilds the FileSet (and ext/pkgTypes) when project re-parse
// growth since the last load exceeds fsetRebuildBytes. A zero threshold disables it.
// Called at the start of Package/Generate (under analysisMu), before applyDirty.
func (m *Module) maybeRebuildFset() {
	m.mu.Lock()
	over := m.fsetRebuildBytes > 0 && m.fset.Base()-m.fsetBaseline > m.fsetRebuildBytes
	m.mu.Unlock()
	if over {
		m.rebuildFset()
	}
}

// rebuildFset discards the grown FileSet and the caches that hold positions into it
// — ext and pkgTypes — together, so nothing live references the old fset (no orphaned
// positions). The next externalImporter reloads ext into the fresh fset and recaptures
// fsetBaseline; analyze re-parses into it. The import graph, dirty set, and overrides
// survive (path/content-based), so reverse-dependency invalidation keeps working.
// Assumes analysisMu held by the caller (Package/Generate); takes m.mu for the writes.
func (m *Module) rebuildFset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fset = token.NewFileSet()
	m.ext = nil
	m.pkgTypes = map[string]*types.Package{}
	m.fsetBaseline = 0
	m.rebuildCount++
}

// rebuilds returns the number of fset rebuilds performed (test hook).
func (m *Module) rebuilds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rebuildCount
}
```
(`token` and `types` are already imported in `module.go`. The field is `rebuildCount` — NOT `rebuilds` — because a field and method cannot share a name in Go; the method `rebuilds()` reads the `rebuildCount` field.)

- [ ] **Step 3: Wire into `Package` and `Generate`.** In `module.go`, both methods currently begin:
```go
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.applyDirty()
```
Insert `m.maybeRebuildFset()` immediately before `m.applyDirty()` in BOTH:
```go
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
```

- [ ] **Step 4: Write the growth-bounded + disabled tests.** Add to `fset_growth_test.go`. Reuse the temp-module helper pattern from `internal/codegen/invalidation_test.go` (`setupChainModule` or a local `writeCrossPkgModule`-style builder). The test sets a LOW threshold by assigning the field directly (in-package test) after `Open`, then drives repeated edits:
```go
func TestFsetGrowthIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	utilFile := filepath.Join(util, "util.gsx")
	// Force frequent rebuilds: tiny threshold.
	m.fsetRebuildBytes = 4096
	var maxBase int
	for i := 0; i < 12; i++ {
		// Each edit changes content (distinct label text) so the dir is marked dirty
		// and re-parsed, growing the fset.
		src := fmt.Appendf(nil, "package util\n\ncomponent Y(label string) {\n\t<span>%d:{label}</span>\n}\n", i)
		m.SetOverride(utilFile, src)
		if _, err := m.Package(util); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		if b := m.fset.Base(); b > maxBase {
			maxBase = b
		}
	}
	if m.rebuilds() == 0 {
		t.Fatalf("expected ≥1 rebuild under a 4 KiB threshold over 12 edits; got 0 (maxBase=%d)", maxBase)
	}
	// Bounded: the final fset.Base() reflects at most a post-rebuild baseline + a bit,
	// far below the unbounded 12×-growth it would reach without rebuilds.
	t.Logf("rebuilds=%d finalBase=%d maxBase=%d", m.rebuilds(), m.fset.Base(), maxBase)
}

func TestFsetRebuildDisabledAtZero(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	m.fsetRebuildBytes = 0 // disabled
	util := filepath.Join(root, "util")
	utilFile := filepath.Join(util, "util.gsx")
	for i := 0; i < 5; i++ {
		m.SetOverride(utilFile, fmt.Appendf(nil, "package util\n\ncomponent Y(label string) {\n\t<b>%d:{label}</b>\n}\n", i))
		if _, err := m.Package(util); err != nil {
			t.Fatal(err)
		}
	}
	if m.rebuilds() != 0 {
		t.Errorf("threshold 0 must disable rebuilding; got %d rebuilds", m.rebuilds())
	}
}
```
Add `"fmt"` to the test imports if needed.

- [ ] **Step 5: Run.** `go test ./internal/codegen -run 'TestFsetGrowthIsBounded|TestFsetRebuildDisabledAtZero' -v` → PASS. `go build ./...` clean.

- [ ] **Step 6: Commit.**
```bash
git add internal/codegen/module.go internal/codegen/fset_growth_test.go
git commit -m "feat(codegen): rebuild fset+ext+pkgTypes on threshold to bound FileSet growth"
```

---

## Task 3: Correctness across a rebuild (adversarial)

**Files:**
- Test: `internal/codegen/fset_growth_test.go`

**Interfaces:**
- Consumes: the rebuild mechanism (Task 2), `Package`, `SetOverride`, `cachedDirs()`, `importGraphSnapshot()`, `Module.Generate`.

This task is tests-only: it proves a rebuild does not corrupt positions, drop the graph, or change output. These are the crux of the phase.

- [ ] **Step 1: Cross-package resolution survives a rebuild.** Add a test that warms a 2-package module (`home` imports `widgets`, renders `<widgets.Badge/>`), resolves `widgets.Badge`'s decl position, forces a rebuild (low threshold + an edit to a THIRD unrelated package, or to `widgets` itself), then re-resolves and asserts the position is still correct (and points into the `.gsx`, not `.x.go`). Use `setupChainModule` (util←components←pages): warm `pages`; resolve a `util.Y` use position via `pages`/`components` Info; set `m.fsetRebuildBytes` tiny; edit + `Package` to trigger a rebuild (assert `rebuilds()>0`); then `Package(components)` and assert the `util.Y` decl still resolves to `util/util.gsx` at the right line via the (new) fset. Model the position lookup on `gen/definition_invalidation_e2e_test.go`'s `badgeDeclLine` (Info.Uses + Fset.Position) or `internal/codegen` cross-nav. Assert `dp.Filename` ends in `.gsx`, not `.x.go`.

- [ ] **Step 2: Graph survives a rebuild.** After a forced rebuild, assert reverse-dep invalidation still works: `m.fsetRebuildBytes` tiny; warm `pages`; trigger a rebuild; then edit `util` and assert `cachedDirs()` no longer contains `components`/`pages` (closure intact) while unrelated `solo` stays — i.e. `importedBy` survived the rebuild. (If the warm-then-rebuild sequence empties `pkgTypes` anyway, structure it so the assertion is about the GRAPH driving invalidation after a re-warm post-rebuild; the load-bearing claim is that `importedBy` edges are not lost by `rebuildFset`.)

- [ ] **Step 3: Output identical pre/post rebuild.** `Module.Generate(dir)` byte-identical before and after a forced rebuild:
```go
func TestGenerateOutputIdenticalAcrossRebuild(t *testing.T) {
	if testing.Short() { t.Skip() }
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	before, _, err := m.Generate(comp)
	if err != nil { t.Fatal(err) }
	// Force a rebuild, then regenerate the SAME (unedited) package.
	m.fsetRebuildBytes = 1 // any growth triggers a rebuild on the next call
	after, _, err := m.Generate(comp)
	if err != nil { t.Fatal(err) }
	if m.rebuilds() == 0 { t.Fatalf("expected a rebuild") }
	for path, b := range before {
		if string(after[path]) != string(b) {
			t.Errorf("file %s changed across rebuild:\n--- before ---\n%s\n--- after ---\n%s", path, b, after[path])
		}
	}
	if len(after) != len(before) {
		t.Errorf("file count changed across rebuild: before=%d after=%d", len(before), len(after))
	}
}
```
(Threshold 1 with `>` means the second `Generate` rebuilds before analyzing, since the first `Generate`+its `externalImporter` load already advanced `Base()` past `baseline+1`. If the timing is off, set `m.fsetRebuildBytes = 1` AND assert `rebuilds()>0`; if it doesn't fire, lower further or add an intervening `SetOverride`+`Package`. The non-negotiable assertion is byte-identity given a rebuild occurred.)

- [ ] **Step 4: Run.** `go test ./internal/codegen -run 'AcrossRebuild|SurvivesRebuild|GraphSurvives' -v` → PASS (names per the tests you write). `go build ./...` clean.

- [ ] **Step 5: Commit.**
```bash
git add internal/codegen/fset_growth_test.go
git commit -m "test(codegen): rebuild preserves cross-pkg positions, graph, and output"
```

---

## Task 4: Concurrency, e2e, corpus gate

**Files:**
- Test: `internal/codegen/fset_growth_test.go` (concurrency), `gen/definition_fset_rebuild_e2e_test.go` (create)

**Interfaces:**
- Consumes: the rebuild mechanism; the LSP `runLSP`/`newLSPAnalyzer.Analyze` harness from `gen/definition_invalidation_e2e_test.go`.

- [ ] **Step 1: Concurrency under `-race`.** Add a test: one Module, low `fsetRebuildBytes`, N goroutines each doing `SetOverride`(changing content)+`Package` so rebuilds interleave with analyses. No data race on `fset`/`ext`/`pkgTypes`/`fsetBaseline`/`rebuildCount`:
```go
func TestConcurrentRebuildAndPackage(t *testing.T) {
	if testing.Short() { t.Skip() }
	m, root := setupChainModule(t)
	m.fsetRebuildBytes = 2048
	comp := filepath.Join(root, "components")
	card := filepath.Join(comp, "card.gsx")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.SetOverride(card, fmt.Appendf(nil, "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
			_, _ = m.Package(comp)
		}(i)
	}
	wg.Wait()
}
```
Note: concurrent `m.fsetRebuildBytes = N` assignment in the test must happen BEFORE launching goroutines (it is not guarded). Run: `go test ./internal/codegen -run TestConcurrentRebuildAndPackage -race -count=1` → PASS (no race).

- [ ] **Step 2: Write the e2e.** Create `gen/definition_fset_rebuild_e2e_test.go`, modeled on `gen/definition_invalidation_e2e_test.go`. Two project packages (`home` imports `widgets`). Set `GSX_FSET_REBUILD_BYTES` to a small value via `t.Setenv` BEFORE constructing the analyzer (so the warm Module created in `Analyze` reads it). Drive: warm both packages; perform enough edit+Analyze cycles to force a rebuild; then go-to-def in `home` on `widgets.Badge` and assert it resolves to the correct `widgets/...gsx` position. Assert no `.x.go` anywhere under the temp module (the standard `.x.go`-free walk). State in the test how it would fail if `rebuildFset` orphaned positions (resolution would land in the wrong file/line or error). Name it `TestDefinitionSurvivesFsetRebuild`.
  - Note: `newLSPAnalyzer` builds the Module lazily in `Analyze` via `module()`; `Open` reads the env at construction, so `t.Setenv` before the first `Analyze` is sufficient. Confirm by reading `gen/lsp.go` `module()`.

- [ ] **Step 3: Run the e2e.** `go test ./gen -run TestDefinitionSurvivesFsetRebuild -v` → PASS.

- [ ] **Step 4: Corpus gate** (output-neutrality — analysis-internal change): `go test ./internal/corpus -run TestCorpus -count=1` → PASS. (The full `TestModuleMatchesBatchOverCorpus` creates a fresh Module per case with the default threshold and never rebuilds, so it is unaffected; run it in the final whole-branch gate, not per-task — it is ~500s standalone.)

- [ ] **Step 5: Commit.**
```bash
git add internal/codegen/fset_growth_test.go gen/definition_fset_rebuild_e2e_test.go
git commit -m "test: fset rebuild is race-clean and preserves LSP go-to-def end-to-end"
```

---

## Final Whole-Branch Review

After all 4 tasks: dispatch the final reviewer (superpowers:requesting-code-review) on the branch diff (`git merge-base main HEAD`..HEAD). Then run the full gate (per-package / lanes, NOT one oversubscribed `go test ./...`, which times out in `go list` under parallelism):
```bash
go build ./...
go test ./internal/codegen -count=1
go test ./gen ./internal/lsp -count=1
go test ./internal/codegen -race -run 'Concurrent|Rebuild|Invalidate|ImportGraph' -count=1
go test ./internal/corpus -count=1 -timeout 560s   # incl. TestModuleMatchesBatchOverCorpus (~500s)
```
Review focus: the atomic reset of `fset`/`ext`/`pkgTypes` (no path that rebuilds one without the others), no rebuild on the recursive importer path, baseline-capture correctness (growth excludes the ext load), output-neutrality (corpus green), and that the threshold is never folded into `computeKey` or `gsx.toml`.

## Self-Review notes (spec coverage)

- Spec §3.1 (gauge + threshold) → Task 1 + Task 2 Step 1. §3.2 (rebuild) → Task 2 Steps 2–2b. §3.3 (wiring) → Task 2 Step 3. §3.4 (baseline) → Task 2 Step 1.
- Spec §7 tests: growth-bounded + disabled (T2), correctness-across-rebuild + graph-survives + output-identical (T3), concurrency + e2e + corpus (T4).
- Names used consistently: `fsetBaseline`, `fsetRebuildBytes`, `rebuildCount` (field), `rebuilds()` (method), `maybeRebuildFset`, `rebuildFset`, `defaultFsetRebuildBytes`, `fsetRebuildBytesFromEnv`.
