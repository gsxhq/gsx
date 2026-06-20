# Plan: `gen.WithFilters` extension seam (user filter packages)

**Date:** 2026-06-20
**Branch:** `feat/gsx-with-filters` off `main`
**Design:** `specs/2026-06-19-gsx-pipeline-and-extensions-design.md` Part B (harvest,
last-wins) + Part C (`gen.Main`/`WithFilters`/marker types/reflect PkgPath).
**Status:** ready for SDD

## Goal

Let a project register its OWN filter packages for the `|>` pipeline:
`gen.Main(gen.WithFilters(std.Pkg, myfilters.Pkg))`. Today `loadFilterTable` hardcodes
`std` and `lowerPipe` hardcodes the `_gsxstd.` qualifier. After this, pipelines resolve
filters across all registered packages (last-wins precedence), and generated code
imports each used filter package under its own reserved alias.

## Conflict-safety (a parallel agent is reorganizing tests)
- Keep std's alias **`_gsxstd`** unchanged → existing pipeline goldens don't churn.
- Add an **additive** `GeneratePackageWithFilters(dir, filterPkgs []string)`; keep
  `GeneratePackage(dir)` as `GeneratePackageWithFilters(dir, []string{stdImportPath})`
  → `renderPackage`/`e2e_test.go` callers stay untouched.
- All new tests go in NEW files (`internal/codegen/filters_multi_test.go`,
  `gen/options_test.go`), not appended to reorganized test files.

## Task 1: Multi-package filter resolution (codegen)

`internal/codegen/filters.go` (+ threading in analyze.go/emit.go/codegen.go).
- **`filterEntry`** gains `alias string` (the import alias the lowered call uses) and
  `pkgPath string`. (Keep `funcName`, `kind`.)
- **Alias assignment**: for an ORDERED list of filter package paths, assign each an
  alias: `stdImportPath` → `_gsxstd` (preserve!); every other package → `_gsxf<i>`
  where i is a stable index (e.g. its position among non-std packages). Deterministic.
- **`loadFilterTable(dir string, pkgPaths []string) (filterTable, error)`** — load ALL
  pkgPaths in one `packages.Load` (variadic patterns), harvest each by contract, build
  the table with **LAST-WINS**: iterate pkgPaths in order, later packages overwrite an
  earlier same-named filter (so a user filter after std shadows the std built-in). Each
  entry records its package's alias + path. Guard load/type errors per package.
- **`lowerPipe`**: replace the hardcoded `"_gsxstd."` with `e.alias + "."`. Change the
  return from `(expr, usesStd bool, err)` to report WHICH filter packages were used —
  e.g. `(expr string, usedPkgs map[string]string /*alias→path*/, err error)` (or a
  small set). Both call sites (probe in analyze.go, emit in emit.go) use it.
- **Imports**: emit + skeleton must import each USED filter package under its alias.
  Today the std import is special-cased (`writeImports` emits `_gsxstd "<std>"`, and
  buildSkeleton emits `import _gsxstd "<std>"` when `usesStd`). Generalize: thread the
  used-filter aliased-imports (a `map[path]alias` or list of `importSpec{name,path}`)
  out of lowerPipe to the import region of BOTH the generated file (writeImports) and
  the skeleton (buildSkeleton's import block). Keep `_gsxstd` for std so std-only files
  are byte-identical to today. Minimize threading churn — a small `map[string]string`
  (path→alias) accumulated alongside `imports`.
- **`GeneratePackageWithFilters(dir string, filterPkgs []string)`** in codegen.go:
  the existing `GeneratePackage(dir)` becomes a thin wrapper calling it with
  `[]string{stdImportPath}`. Thread `filterPkgs` into `resolveTypesPkg` →
  `loadFilterTable(dir, filterPkgs)`. (Don't change `GeneratePackage`'s signature.)
- **Default + dedup**: if `filterPkgs` is empty, default to `[stdImportPath]`. Dedup
  preserving order (last-wins already handles name collisions; dedup avoids double
  imports of the same path).
- Tests (`internal/codegen/filters_multi_test.go`): a temp module with a SECOND filter
  package (`myfilters` with e.g. `func Shout(s string) string { return s+"!" }`) and a
  `.gsx` using `{ name |> shout }`; call `GeneratePackageWithFilters(pkgDir, [std,
  myfilters])`; write the .x.go + build/run → asserts `shout` resolves to the user
  package (imported under `_gsxf0`), and a std filter (`upper`) still works (`_gsxstd`).
  A last-wins test: `myfilters.Upper` shadows `std.Upper` when listed after. An
  unknown filter still errors. (Mirror renderPackage's temp-module pattern; gate slow
  ones behind `testing.Short()`.)

Commit: `codegen: multi-package filter resolution (WithFilters backend, last-wins)`.

## Task 2: `gen.WithFilters` + `std.Pkg` marker + cmd/gsx wiring

- **`std.Pkg` marker** (`std/std.go` or `std/marker.go`): an exported package-level
  value whose TYPE lives in std, so `reflect.TypeOf(std.Pkg).PkgPath() ==
  "github.com/gsxhq/gsx/std"`. E.g. `type pkgMarker struct{}; var Pkg pkgMarker`.
  Doc it as the `gen.WithFilters` registration token. (A user filter package exports
  its own `Pkg` the same way.)
- **`gen.WithFilters(markers ...any) Option`** (gen/main.go or a new gen/options.go):
  for each marker, recover its import path via `reflect.TypeOf(marker).PkgPath()`
  (guard a nil/invalid marker with a clear error path — collect into the config or
  fail at run). Store the ORDERED, de-duplicated list of filter package paths in the
  `config`. Document last-wins (order matters; put overrides last).
- **config + threading**: `config` gains `filterPkgs []string`. `runGenerate` /
  `Generate` must use them: add `gen.Generate` plumbing so the configured filter paths
  reach `codegen.GeneratePackageWithFilters`. Cleanest: an unexported
  `generate(paths, filterPkgs)` core that both `Generate` (default `[std]`) and the
  `Main`→`run`→`runGenerate` path (config.filterPkgs, default `[std]` when none) call.
  Keep the public `Generate(paths)` signature stable (defaults to std) so it's not a
  breaking change; the filter-package path flows from `Main`'s options.
- **Default**: no `WithFilters` → `[stdImportPath]`. With `WithFilters(...)` → exactly
  the listed packages (the user includes `std.Pkg` if they want std — matches the
  design example `WithFilters(gsxstd.Pkg, filters.Pkg)`).
- **`cmd/gsx/main.go`**: keep `gen.Main()` (stock = std default). (No change needed if
  the default is std; optionally `gen.Main(gen.WithFilters(std.Pkg))` to be explicit —
  pick whichever is cleaner; document.)
- Tests (`gen/options_test.go`): unit — `reflect.TypeOf(std.Pkg).PkgPath()` ==
  the std path; `WithFilters(std.Pkg)` populates config.filterPkgs with the std path;
  order + dedup preserved. End-to-end (gate behind `testing.Short()`): a temp module
  with a user filter pkg + a `.gsx` using it; drive the gen path with the user filter
  registered (via the unexported `generate(paths, filterPkgs)` or a test seam) →
  the .x.go builds and renders through the user filter.

Commit: `gen: WithFilters option + std.Pkg marker (register user filter packages)`.

## Deferred (note, don't build)
- `WithClassMerger` — the runtime `ClassMerger` is a render-time package var; the
  design's generation-time class-merge model doesn't match the current runtime-merge
  impl. Defer; revisit when the merge story is unified.
- `gsx info` (list the resolved filter table) + `gsx vet` shadow-warning.
- Initialism-aware filter naming; per-stage `?`; pipeline-as-filter-arg.
- The fail-fast guard (stock `gsx` errors on a declared code hook) — tied to
  WithClassMerger; deferred with it.

## After tasks
- Final whole-feature review; independent adversarial review (focus: last-wins
  correctness, per-package alias collisions, std-alias preservation = no golden churn,
  unknown/cross-package filter errors, the reflect-PkgPath recovery, dedup).
- Merge `--no-ff`; ROADMAP update (WithFilters shipped; WithClassMerger/info/vet next).

## Risks
- **Import threading** — used-filter aliased imports must reach BOTH the generated file
  AND the skeleton, or the probe/emit disagree (a missing import = type-check failure or
  uncompilable output). Drive both from the same lowerPipe-reported used set.
- **std-alias preservation** — std MUST stay `_gsxstd` or every pipeline golden churns
  (and risks conflicting with the parallel test reorg). Special-case std's alias.
- **packages.Load with multiple patterns** — confirm one Load call resolves all filter
  packages (incl. a user package in the same module via the test replace).
