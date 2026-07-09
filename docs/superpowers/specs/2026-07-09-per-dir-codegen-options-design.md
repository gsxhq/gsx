# Per-dir codegen options: one `Module`, many filter tables

## Problem

`internal/corpus` runs in **13.2s**. **10.7s of that (82%)** is a single loop —
batch.go:99-108 — which gives every case carrying a `gsx.toml` its own
`codegenDirs` call:

```go
for _, cs := range states {
	if cs.c.classMerger == nil && len(cs.c.filterPkgs) == 0 {
		continue
	}
	mergerResults, merr := codegenDirs(tmp, cs.pkgDirs, cs.c.classMerger, cs.c.filterPkgs)
	...
}
```

27 cases → 27 calls → 27 `codegen.Module`s. Measured against the shared
all-cases temp module:

```
step3a default codegenDirs (509 dirs)   550ms   ← ONE call
step3b per-case codegenDirs (27 cases) 10.73s   ← 27 calls
step5  go run                            1.51s
```

509 dirs cost 550ms; 27 dirs cost 10.7s. The asymmetry is entirely per-`Module`
fixed cost.

Each `Module` pays up to **three** `packages.Load` (`go list`) invocations:

| # | Site | Cost | Cases affected |
|---|------|------|----------------|
| 1 | `externalImporter` — module.go:253 | ~340ms | all 27 |
| 2 | `cachedFilterTable` → `loadFilterTableMulti` — filters.go:370 | ~150ms | all 27 |
| 3 | `ValidateClassMerger` — generate_dirs.go:35 | ~150ms | 4 merger cases |

That is ~60–80 subprocess `go list` runs to generate 27 directories.

Loads 1 and 2 are **not** corpus-only. Every `codegen.Open` consumer pays #2 on
first `Generate` — the LSP, `gsx fmt`, and the warm-core `--watch` path included.

## Two hypotheses, both measured false

Recording these so they are not re-litigated.

**Parallelize the loop.** `errgroup` with `GOMAXPROCS` limit: **11.8s vs 10.7s
serial — no speedup**, and 204s on a cold build cache. `packages.Load` already
saturates every core (parallel `go list` + parallel type-check), so the loop is
CPU-bound, not latency-bound. Adding goroutines only adds build-cache contention.

**Shrink the load set.** `externalImporter` ends `loadPaths` with `"./..."`
(module.go:254). Against the shared temp module holding all 566 cases, that is
509 packages type-checked per call. Isolating one case into its own 2-package
module: **404ms → 340ms, a 1.2× win.** The `./...` walk is nearly free; the fixed
load of `github.com/gsxhq/gsx` + `gsx/std` dominates. Splitting the corpus into
per-case temp modules is therefore not worth it.

## The ceiling

One `Module` over all 27 dirs, loading the **union** of their filter packages:

```
today (27 serial Modules):  11.23s
ceiling (1 Module, union):   0.42s   → 26.8×
```

Corpus total: **13.2s → ~3s.**

## Why the union cannot simply be adopted

`FilterPkgs` feeds two consumers with different semantics:

1. **`externalImporter` load set** (module.go:253) — decides which packages the
   skeleton type-checker *can* import. A superset here is **inert**: it makes
   more packages available to an importer that is only consulted by import path.
2. **`cachedFilterTable`** (module.go:305) — decides which packages' exported
   funcs become *pipe filters in scope*. A superset here is **semantically
   load-bearing**: it silently widens the filter whitelist.

`testdata/cases/filterimport/user_plain_import_no_filter.txtar` asserts that a
package which is imported but **not** listed in `filterPackages` is rejected as
a filter source. A union load would make that case pass for the wrong reason —
worse, it would keep passing while testing nothing.

The corpus filter packages are also genuinely distinct (`Parse`+`Pick`,
`First[T any]`, `Fail`/`Detonate`, `Who(ctx)`), so consolidating testdata into
one shared `filters` package is rejected too: it would erode the
self-containment that makes each txtar readable as a standalone syntax
reference.

## Design

**Split the union (load) from the per-dir (table).**

- Load the union of all filter packages **once**, into one `Module`'s importer.
- Build the filter table **per directory**, from the already-loaded
  `*types.Package` values — no subprocess.

The primitive already exists. `bundle.go` has:

```go
func loadFilterTableFromTypes(byPath map[string]*types.Package, pkgPaths []string, explicitAliases []FilterAlias) (filterTable, error)
```

It mirrors `loadFilterTableMulti`'s precedence (whole-package paths in order,
last-wins, then explicit aliases) and harvests purely from type information.
Today it is reachable only via `NewCachedResolverFromTypes` on the WASM
`Bundle` path.

`externalImporter` already retains exactly the map it needs
(module.go:260-264):

```go
mp := map[string]*types.Package{}
packages.Visit(pkgs, nil, func(p *packages.Package) {
	if p.Types != nil { mp[p.PkgPath] = p.Types }
})
```

So a per-dir table is `loadFilterTableFromTypes(mp, dirFilterPkgs, aliases)` —
a scope walk over already-type-checked packages, microseconds.

### API

Add a per-dir override carried alongside the dir, leaving `Generate(dir)`
untouched:

```go
// DirOptions overrides Module-level options for a single Generate call.
// Zero value means "inherit from Options".
type DirOptions struct {
	FilterPkgs  []string        // nil = inherit Options.FilterPkgs
	ClassMerger *ClassMergerRef // nil = inherit Options.ClassMerger
}

// GenerateWith is Generate with per-dir option overrides. FilterPkgs must be a
// subset of the Module's loaded filter packages (Options.FilterPkgs); the table
// is harvested from loaded types with no packages.Load.
func (m *Module) GenerateWith(dir string, over DirOptions) (map[string][]byte, []diag.Diagnostic, error)
```

`Generate(dir)` becomes `GenerateWith(dir, DirOptions{})`.

`ClassMerger` already reaches `generateFile` as a plain parameter
(module.go:409, module.go:461), so threading a per-dir value is a parameter
change, not a redesign.

Filter tables are memoized per distinct `FilterPkgs` key (sorted, deduped, joined)
rather than once per Module — `cachedFilterTable` becomes
`filterTableFor(pkgs []string)` over a `map[string]filterTable`. The existing
`filterTableLoads` test hook keeps guarding the warm-regen invariant (a warm
regeneration must trigger **zero** go-list reloads); it should now also assert
zero reloads across *differing* per-dir filter sets.

**Invariant:** `Options.FilterPkgs` is the union (the load set);
`DirOptions.FilterPkgs` is a subset (the table). `GenerateWith` returns an error
if a dir names a filter package absent from the loaded importer — this must be a
hard error, never a silent empty table, or `user_plain_import_no_filter` degrades
into a vacuous pass.

### `ValidateClassMerger`

Its doc comment already explains why it is *not* called from `Open`: "that path
is shared by the LSP and fmt, which must not pay a packages.Load per-Open."
Given a loaded importer we can validate from types with no load at all. Add:

```go
func validateClassMergerFromTypes(byPath map[string]*types.Package, ref *ClassMergerRef) error
```

reusing `isStringSliceToString` verbatim. `ValidateClassMerger` (the
`packages.Load` form) stays for `gen.newWatchSession`, which validates at
startup before any Module exists.

### Corpus harness

batch.go step 3 collapses to one call:

```go
perDir := map[string]codegen.DirOptions{} // absDir → its case's options
unionFilters := []string{codegen.StdImportPath}
// ...populate from states...
pkgResults, err := codegenDirsWithPerDir(tmp, allDirs, unionFilters, perDir)
```

`selectedCaseNamesForRun` **stays**. It is what makes a focused
`-run TestCorpus/attrs/spread_byo` cost 0.5s, and after this change a full batch
is still ~1s codegen + ~1.5s `go run`. Keeping it also documents the lesson: that
optimization made the inner dev loop fast by *skipping* the 27-case loop
entirely, which is precisely why the 10.7s never surfaced.

## Beneficiaries beyond the corpus

- **LSP / `fmt` / warm `--watch`**: drop `packages.Load` #2 (the filter-table
  load) on first `Generate` of every Module.
- **`gsx generate` with a class merger**: drops `packages.Load` #3.
- **Multi-root analysis (Module Phase 2)**: per-dir option scoping is a
  prerequisite for one warm graph serving dirs with differing `gsx.toml`.

## Non-goals

- Parallelizing codegen. Measured useless (above); revisit only if the load
  itself becomes cheap.
- Per-case temp modules. Measured 1.2×.
- Consolidating corpus filter packages. Erodes case self-containment.
- Changing `Bundle`/WASM mode. It already has per-Bundle tables.

## Verification

- `go test ./internal/corpus -run TestCorpus -count=1` — all 566 cases green,
  **no golden churn**. Any diff in `generated.x.go.golden` means a filter table
  changed scope; that is the failure mode this design exists to prevent.
- `filterimport/user_plain_import_no_filter` must still pin its rejection
  diagnostic. Add a probe that inverts it: a case whose filter lives in *another
  case's* filter package must be rejected. Without this, a regression to
  union-table semantics is invisible.
- New unit test in `internal/codegen`: two dirs, disjoint `FilterPkgs`, one
  Module — assert `externalLoads() == 1`, `filterTableLoads() == 0`, and that
  each dir sees only its own filters.
- Wall-clock gate: corpus `TestCorpus` under 4s on CI hardware.
