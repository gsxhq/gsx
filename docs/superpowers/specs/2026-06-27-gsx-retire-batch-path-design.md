# gsx Phase 4 — retire the go-list batch codegen path

**Status:** approved design (brainstormed 2026-06-27), ready for an implementation plan.

**One-line:** Delete `codegen.GeneratePackagesWithFilters` (+ the `GeneratePackages` wrapper)
— the redundant `go list` batch codegen path — by migrating its last consumers (one-shot
`generate`, `fmt`, `AnalyzeModule`, and the golden corpus test) onto the warm `Module`, so
there is **one** `go-list`-based codegen path. `CachedResolver` (the bundle-based WASM path)
is untouched.

## 1. Goal & non-goals

### Goal
After this phase, `GeneratePackagesWithFilters`/`GeneratePackages` no longer exist; every
non-WASM codegen flows through `Module.Generate`/`Module.Package`. This removes the
two-implementations-of-one-thing maintenance burden (batch and Module both `go list`,
kept in lockstep only by `TestModuleMatchesBatchOverCorpus`).

### Non-goals
- **Touching `CachedResolver`/`GeneratePackagesWithResolver`.** That is the *bundle*-based
  path the WASM playground requires (a browser has no Go toolchain to run `go list`). It is
  not redundant with the Module and stays. Folding it into a pluggable-importer Module is a
  separate future effort.
- **Changing generated `.x.go` bytes.** Every migration is byte-identical. `Module.Generate ≡
  batch` is proven by `TestModuleMatchesBatchOverCorpus`; `TestCorpus` validates batch against
  pinned goldens; so `Module ≡ batch ≡ goldens` holds at every step *before* deletion.
- **Changing the warm core** (LSP, watch). They already use the Module.

## 2. Background — the batch path's consumers

`codegen.GeneratePackagesWithFilters(moduleDir, dirs, filterPkgs, aliases, cls, fm, cssMin,
jsMin, cssMinify, jsMinify, srcOverride) (map[string]*PackageResult, error)` — one
`packages.Load("./...")` over the dirs, then per-package generate. Callers:

| Caller | What it needs | Migrates to |
|---|---|---|
| `gen/cache.go` `generateCached` (one-shot `generate`, behind the file cache) | generated `.x.go` bytes for the miss dirs | warm `Module` per root, `Generate` each miss dir |
| `gen/fmt.go` | `pr.UnusedImports` only | `Module.Package(dir).UnusedImports` |
| `gen/lsp.go` `AnalyzeModule` (whole-module find-references, with override) | each pkg's `CrossIndex` | `SetOverride` + `Module.Package` per dir, aggregate `CrossIndex` |
| `internal/corpus` `codegenGeneratePackages` (golden `TestCorpus`) | per-dir `PackageResult` for the whole tmp module | one `Module`, `Generate`/analysis per dir |
| ~8 `internal/codegen/*_test.go` | batch behavior under test | `Module.Generate`/`Package`, or delete if Module-covered |
| `codegen.GeneratePackages` wrapper | thin default-args call | inline at the corpus caller, then delete |

`Module.Generate` already threads minify (Phase 3 `Options.CSSMin/JSMin/CSSMinify/JSMinify`)
and is byte-equivalent to batch — so it is a drop-in for the codegen.

## 3. Design

### 3.1 One-shot `generate` (`gen/cache.go`)
`generateCached` keeps its content-hash file cache. Its two `GeneratePackagesWithFilters`
sites (the cache-miss path ~`cache.go:140` and the no-cache fallback ~`cache.go:235`) become:
open one `*codegen.Module` per module root (`codegen.Open` with the same
filters/aliases/classifier/fieldmatcher **and** the minify config), then `Module.Generate(dir)`
for each target dir, mapping its `map[gsxPath][]byte` output to `.x.go` files (the same
`gsxPath → .x.go` mapping watch uses). `paths` may span roots → group by root (the helpers
already exist). Output byte-identical (corpus-proven).

### 3.2 `fmt` (`gen/fmt.go`)
The single call becomes `m := codegen.Open(Options{ModuleRoot: root, …}); pr, err :=
m.Package(absDir)` and read `pr.UnusedImports`. `Package` (not `Generate`) — fmt needs only the
analysis, no `.x.go` emission. Same `UnusedImports` (computed by the same
`detectUnusedImportsFromErrs`).

### 3.3 `AnalyzeModule` find-references (`gen/lsp.go`)
Reuse the warm per-root Module the LSP analyzer already holds (`a.module(root, …)`). For the
whole module: `SetOverride` every buffer in `override`, then `Module.Package(dir)` for each
discovered dir, flattening each result's `CrossIndex` into `[]lsp.CrossRef`. The Module's
single shared fset across the per-dir `Package` calls preserves the cross-package CrossRef
routing the batch call got from its one shared load. (Existing find-references tests validate.)

### 3.4 Golden corpus test (`internal/corpus/codegen.go` + `batch.go`) — the crux
`codegenGeneratePackages(tmp, allPkgDirs)` (→ `codegen.GeneratePackages`) becomes: one
`codegen.Open(Options{ModuleRoot: tmp, FilterPkgs: []string{StdImportPath}, CSSMinify: true,
JSMinify: true})`, then analyze/generate each dir into the same `map[dir]*PackageResult` the
corpus batch step consumes. The corpus harness's downstream steps (build + run renderable
cases) are unchanged. **Goldens must stay byte-identical** — guaranteed by the equivalence
proof (corpus cases are single-package; `Module.Generate(dir) ≡ batch(dir)`). This is the
highest-risk task; it runs under the full corpus golden gate.

### 3.5 The ~8 codegen unit tests
`batch_test`, `crossindex_test`, `navindex_test`, `retention_test`, `minify_gate_test`,
`byo_lsp_test`, `unused_imports_test`, `batch_override_test` call
`GeneratePackagesWithFilters` to assert codegen behavior (CrossIndex, NavIndex, retention,
minify gating, unused imports, src overrides). Per test: migrate to `Module.Generate`/`Package`
(threading `CSSMinify:true` where it asserted minified output, `SetOverride` where it passed
`srcOverride`), or **delete** when an existing `internal/codegen` Module test already covers
the same property (note which, per test, in the task).

### 3.6 Deletion
Once §3.1–3.5 land and are green: delete `GeneratePackagesWithFilters`, `GeneratePackages`,
and any now-orphaned batch-only helpers in `batch.go` that nothing else uses (keep
`buildCrossNav` and anything the Module shares — grep before removing). Delete
`TestModuleMatchesBatchOverCorpus` (no batch to compare; the golden test now validates the
Module directly). Confirm `grep -rn "GeneratePackagesWithFilters\|GeneratePackages\b"` is empty
(except history). `CachedResolver`/`GeneratePackagesWithResolver` and `buildCrossNav` remain.

## 4. Order (de-risking)
Production migrations first (validated by existing tests), then the golden test, then the unit
tests, then deletion — so the batch path is removed only after every caller is gone and green:
1. one-shot `generate`  2. `fmt`  3. `AnalyzeModule`  4. golden corpus test  5. unit tests
6. delete + final sweep.

## 5. Invariants
- **Byte-identical `.x.go`** at every step; goldens unchanged; `make ci` examples-drift clean.
- **`Module ≡ batch ≡ goldens`** holds until deletion (the equivalence test is the safety net
  through tasks 1–5; removed only in task 6 once the golden test validates the Module directly).
- **`CachedResolver`/WASM untouched**; `buildCrossNav` retained.
- **Perf:** migrations use the warm Module, whose go-list caches (ext + filter, with the new
  regression guard) keep one-shot `generate` at most one `go list` per root (same as batch).
- **`.x.go`-independent** resolution unchanged.

## 6. Testing (per [[gsx-syntax-change-test-coverage]])
- Each production migration: existing tests for that consumer stay green (`gen` generate/fmt/
  find-references suites). Add a focused assertion only where coverage is thin.
- Golden corpus: `TestCorpus` byte-identical (no `-update` needed — if any golden moves, STOP:
  it means a real divergence, not an expected change).
- Unit-test migrations: the migrated test asserts the same property via the Module; deleted
  ones are justified by a named existing Module test.
- Deletion: full gate — `go build ./...`, `go vet ./...`, `go test ./gen ./internal/codegen
  ./internal/lsp -count=1`, `go test ./internal/corpus -count=1` (golden), `make ci` examples
  drift, gofmt + `gsx fmt`. Zero references to the deleted symbols.

## 7. Out of scope / follow-ups
- Pluggable-importer Module (go-list OR bundle) + override-only operation → fold `CachedResolver`
  in for a true single path incl. WASM.
- Migrate the playground server's vestigial seed-generate / remove it.
- gsx→Go-only→gsx transitive boundary; `didChangeWatchedFiles`; cross-module watch closure.
