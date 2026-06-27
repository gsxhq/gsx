# gsx warm core — Phase 2c: bound shared-fset growth (rebuild-on-threshold)

**Status:** approved design (brainstormed 2026-06-27), ready for an implementation plan.

**One-line:** Stop the warm `Module`'s lifetime `*token.FileSet` (`m.fset`) from growing
unboundedly across re-analyses by rebuilding it — together with `ext` and `pkgTypes`
— when accumulated project re-parse growth crosses a threshold, with **zero change to
generated output** and **no orphaned positions**.

## 1. Goal & non-goals

### Goal
`m.fset` size stays bounded over a long LSP session. Today every `analyze(P)` parses
P's `.gsx` files, their skeletons, and companion `.go` into `m.fset` (Go's
`token.FileSet` is append-only — no removal), so each edit of P leaks ~P's-file-count
`token.File` entries (the prior versions stay forever, referenced by nothing live). On
the driver workload (a ~74k-line package, ~100 files) that is ~100 leaked entries per
keystroke-settle → hundreds of MB and slower `Position` lookups over a session. After
this phase, fset size has a hard upper bound ≈ `ext baseline + threshold + one package`.

### Non-goals
- **Per-edit fset rebuild.** Rebuilding on every edit would orphan the warm `ext`
  importer's positions (the `module.go` Growth note's explicit warning). We rebuild
  rarely (threshold-gated) and reset `ext`/`pkgTypes` *together* with the fset.
- **Content-keyed parse-reuse cache (gopls-faithful "approach B").** Reusing unchanged
  parsed files to slow growth + speed re-analysis is a worthwhile future optimization,
  but it is blocked here by (1) the gsx AST being mutated by `emit` (so it cannot be
  safely cached — `Package`'s own comment relies on a fresh re-parse each call) and
  (2) gsx + skeleton sharing `m.fset` (reuse needs a dual-fset split). It is a larger,
  riskier refactor whose payoff is speed/growth-rate, not a hard guarantee. Deferred.
- **Changing generated `.x.go` output.** A rebuild must be output-neutral; the corpus
  byte-equivalence gate stays green.

## 2. Background — why it grows

`m.fset` is one FileSet for the Module's whole lifetime, covering BOTH the external
`packages.Load` (`externalImporter`) AND every project `analyze()` call (the
`module.go` "FileSet" note: this single fset is what makes cross-package go-to-def
resolve a sibling's `obj.Pos()` to the sibling's source). `analyze` parses into it at:
`parsePackageWithFset` (the `.gsx` files), `module_importer.go` skeleton parse
(`goparser.ParseFile(fset, absXpath, skel, …)`), the helper shim, and each companion
`.go`. `token.FileSet` has no removal API, so re-analyzing a package after an edit
(post-2b: `applyDirty` drops the package's `pkgTypes` → it re-parses) appends fresh
`token.File`s while the prior ones leak. The `module.go` Growth note already names the
fix: *"Bounding this (rebuild the Module / incremental re-analysis) is a Phase-2
concern. Do NOT rebuild the fset per edit: that would orphan the warm ext importer's
positions."* This phase implements the rebuild, done safely (reset `ext`/`pkgTypes`
with the fset).

## 3. Design

All additions are in `internal/codegen` (`module.go` / `module_importer.go`), guarded
by the existing locks (`analysisMu` serializes `Package`/`Generate`/`typesPackage`;
`m.mu` guards the field writes).

### 3.1 Growth gauge + threshold
New `Module` fields:
- `fsetBaseline int` — `m.fset.Base()` captured immediately after the `packages.Load`
  in `externalImporter`, so growth is measured **since the last (re)load**, excluding
  the fixed `ext` baseline.
- `fsetRebuildBytes int` — the threshold (bytes of project re-parse growth). Set in
  `Open` from an internal default constant `defaultFsetRebuildBytes`, overridable via
  env `GSX_FSET_REBUILD_BYTES` (internal perf knob, like `GSXCACHE`; not `gsx.toml`,
  not in `computeKey` — it does not change output). A value of 0 disables rebuilding
  (treated as "unbounded", the pre-2c behaviour) so tests and ops can opt out; an
  invalid/absent env leaves the default.

`token.FileSet.Base()` returns the next base offset = cumulative size of all added
files + 1 per file, a monotonic proxy for "total bytes parsed into this fset". Growth
since baseline = `m.fset.Base() - m.fsetBaseline`.

### 3.2 The rebuild
New method `rebuildFset()` (assumes `analysisMu` held; takes `m.mu` for the writes):
```
m.mu.Lock()
m.fset = token.NewFileSet()
m.ext = nil            // force externalImporter to reload into the fresh fset
m.pkgTypes = map[string]*types.Package{}
m.fsetBaseline = 0     // reset; externalImporter recaptures after the next load
m.mu.Unlock()
m.rebuilds++           // test/observability counter (guarded by m.mu in the block above)
```
**Survives** (path/content-based, position-free — untouched): `imports`, `importedBy`,
`dirty`, `overrides`, `opts`. So reverse-dependency invalidation keeps working the
instant after a rebuild.

### 3.3 Trigger wiring
At the very start of `Package` and `Generate`, after `analysisMu.Lock()`/`defer` and
**before** `applyDirty()`:
```
m.maybeRebuildFset()   // rebuild iff fsetRebuildBytes>0 && fset.Base()-fsetBaseline > fsetRebuildBytes
m.applyDirty()
```
`maybeRebuildFset` reads `fset.Base()`/`fsetBaseline`/`fsetRebuildBytes` under `m.mu`,
and calls `rebuildFset` if over threshold. Doing it before `applyDirty` is fine: a
rebuild wipes `pkgTypes` entirely, so the subsequent `applyDirty` (closure over the
intact graph) deletes nothing and just clears `dirty` — correct and cheap.

### 3.4 Baseline capture
In `externalImporter`, after a successful `packages.Load` populates `m.ext`, set
`m.fsetBaseline = m.fset.Base()` (under `m.mu`, in the same critical section that sets
`m.ext`). This runs on the first load and on every reload after a rebuild.

## 4. Data flow

Long session editing package P (a 100-file, ~6 MB-per-skeleton package), threshold
256 MB:
- Edits 1..N each re-analyze P (its deps stay cached), each adding ~6 MB to
  `fset.Base()`. After ~`256MB / 6MB ≈ 42` edits, `fset.Base()-fsetBaseline` crosses
  256 MB.
- Edit N+1: `Package(P)` → `maybeRebuildFset` fires → fresh fset, `ext=nil`,
  `pkgTypes={}` → `externalImporter` reloads `ext` (recaptures baseline) → `analyze(P)`
  re-parses P into the fresh fset. Growth resets; the graph/overrides/dirty are intact,
  so the very next dependency edit still invalidates P's importers correctly.

## 5. Components & boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `fsetBaseline`/`fsetRebuildBytes` fields + `Open` init (+ env) | configure the bound | `defaultFsetRebuildBytes`, `GSX_FSET_REBUILD_BYTES` |
| `externalImporter` baseline capture | record `fset.Base()` after each load | `packages.Load` |
| `maybeRebuildFset` | decide + trigger under threshold | the gauge fields |
| `rebuildFset` | reset fset+ext+pkgTypes+baseline; bump counter | `m.mu` |
| `Package`/`Generate` wiring | call `maybeRebuildFset` before `applyDirty` | `analysisMu` |

## 6. Invariants

- **No orphaned positions:** `fset`, `ext`, and `pkgTypes` reset atomically together;
  nothing live references the discarded fset after a rebuild.
- **Single-fset per result:** a rebuild only happens between top-level analyses (under
  `analysisMu`); each `Analyze` returns a Package whose `Info`/`Fset` are mutually
  consistent, and the LSP replaces its stored Package each call — no request mixes
  fsets.
- **Graph/overrides survive:** reverse-dep invalidation and unsaved buffers are
  unaffected by a rebuild (path/content-based state is retained).
- **Output-neutral:** generated `.x.go` bytes do not depend on fset identity (`//line`
  positions are source-relative); corpus equivalence stays green; the threshold is not
  in `computeKey`.
- **Concurrency:** the gauge/rebuild touch only `m.mu`-guarded fields and run under
  `analysisMu`; the recursive importer path never triggers a rebuild (only top-level
  `Package`/`Generate` check). Race-clean.
- **`.x.go`-independent:** unchanged — resolution still flows through skeletons/`//line`.

## 7. Testing (per [[gsx-syntax-change-test-coverage]])

- **Growth bounded (`internal/codegen`):** set `fsetRebuildBytes` low (in-package field
  set, or `GSX_FSET_REBUILD_BYTES`); repeatedly `SetOverride`(changing content)+`Package`
  a package; assert `m.fset.Base()` stays bounded (does not grow monotonically without
  limit) and a `rebuilds()` test hook shows ≥1 rebuild.
- **Correctness across a rebuild (the key adversarial test):** a 2-package module where
  `home` imports `widgets`; warm both so cross-pkg go-to-def resolves `widgets.Badge` to
  its `.gsx` decl; force a rebuild (low threshold + an edit); then assert cross-package
  go-to-def **still resolves to the correct `.gsx` position** — proving positions are
  consistent in the new fset, `ext` reloaded, no orphaned/corrupt positions. Drive
  through the LSP (`gen` e2e) or the Module directly (`internal/codegen`).
- **Graph survives rebuild:** after a forced rebuild, editing `widgets` still
  invalidates `home` (reverse-closure intact) — assert via `cachedDirs()`.
- **Output identical pre/post rebuild:** `Module.Generate(dir)` produces byte-identical
  output before and after a forced rebuild.
- **Disabled (threshold 0):** with `fsetRebuildBytes==0`, no rebuild ever fires
  (`rebuilds()==0`) and behaviour matches pre-2c.
- **Concurrency (`-race`):** concurrent `SetOverride`+`Package` with a low threshold so
  rebuilds interleave; no data race; extends the existing concurrency test.
- **No regression:** corpus gate green (Module.Generate ≡ batch); existing LSP/codegen
  suites green.

## 8. Out of scope / follow-ups
- Content-keyed parse-reuse cache (approach B) — slow growth + faster re-analysis.
- Full-result `PackageResult` snapshot cache (Phase-2b deferral).
- `didChangeWatchedFiles` disk-edit invalidation; gsx→Go-only→gsx transitive boundary.
- Bounding the LSP server's retained per-dir Packages (orthogonal; they hold the latest
  fset only).
