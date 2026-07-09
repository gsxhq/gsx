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

Every `codegen.Open` consumer pays #2 once per Module, so this is not purely a
corpus problem — but it is **one** load per Module, not 27. Only the corpus opens
Modules in bulk.

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

```go
// DirOptions overrides Module-level options for a single package dir.
// Zero value means "inherit from Options".
type DirOptions struct {
	FilterPkgs  []string        // nil = inherit Options.FilterPkgs
	ClassMerger *ClassMergerRef // nil = inherit Options.ClassMerger
}

type Options struct {
	FilterPkgs []string             // loaded AND harvested into the default table
	LoadPkgs   []string             // loaded, NO filter semantics (the union half)
	PerDir     map[string]DirOptions // per-dir narrowing (the table half)
	// ...
}
```

An earlier draft proposed `Module.GenerateWith(dir, DirOptions)` — a per-call
override. That does not work. `analyze(dir)` is the recursion point for the
import graph: a multi-package case's imported sibling is analyzed through
`moduleImporter → typesPackageWith → analyze`, never through a top-level
`Generate`, so a per-call override never reaches it and the sibling would
type-check its pipes against the wrong table. The override must be addressable
by dir, hence the map, consulted inside `analyze`.

Relatedly, the table is **not** an emit-time concern: `buildSkeleton(f, table, …)`
consumes it during skeleton construction, so it cannot be swapped in at
`generateFile`. `ClassMerger` *is* emit-only, and rides on `analyzed.merger`.

Filter tables are memoized per distinct `FilterPkgs` key (deduped, joined). The
key is order-sensitive on purpose: filter precedence is last-wins, so
`[std, a, b]` and `[std, b, a]` are different tables and must not share a memo
entry.

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

- **`gsx generate` with a per-dir class merger**: drops `packages.Load` #3,
  validating from the types the importer already loaded.
- **Multi-root analysis (Module Phase 2)**: per-dir option scoping is a
  prerequisite for one warm graph serving dirs with differing `gsx.toml`.

**Correction (post-implementation).** An earlier draft claimed `fmt` and the LSP
would also drop load #2. They will not, and must not. `buildPackageSkeletons` —
the syntactic-imports fast path that took `gsx fmt -l` from ~16s to 0.58s —
deliberately never calls `externalImporter`. Harvesting its table from loaded
types would *add* the full `./...` load it exists to avoid. So `filterTableFor`
falls back to `cachedFilterTable()` whenever a dir has no `PerDir` entry, which
is every dir on the `fmt`/LSP path: their behavior is byte-identical and no
faster. The win here is bulk-Module callers only.

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

## Outcome (implemented in #56)

`TestCorpus` 13.2s → **2.4s**; `go test ./internal/corpus` 14.1s → 3.5s. Zero
golden churn. `make ci` and `make lint` exit 0; `-race` clean.

Every guard was mutation-tested: making `filterTableFor` return the union turns
three unit tests red **plus** a corpus golden.

Adversarial review found two silent-wrong-answer gaps, both fixed:
`validatePerDirMergers` ran only in `GenerateDirs`, so `Open`+`Generate`
(watch/LSP) emitted `_gsxcm.<Missing>` and exited 0; and `filterTableFor`
returned `Bundle.filters()` before consulting `PerDir`, handing a dir the
Bundle's whole table. `Open` now rejects `Bundle` + `PerDir`/`LoadPkgs`.

**This does not speed up `make ci`.** Its lanes run `-j4` and `go test ./...`
runs packages in parallel, so wall time (~120s) is set by the slowest package —
`internal/codegen`'s own suite (~91s), which the 13.2s corpus never gated. That
suite makes ~78 `GenerateDirs` calls across 52 test files, each opening its own
temp module and paying its own `packages.Load`: **the same disease, now the
dominant one.** ~730 CPU-seconds. Fixing it means sharing a Module fixture
across tests, and is the natural next step.
