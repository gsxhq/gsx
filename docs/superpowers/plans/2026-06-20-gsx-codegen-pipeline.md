# Plan: Codegen Pipeline `|>` + `std` filters (first slice)

**Date:** 2026-06-20
**Branch:** `feat/codegen-pipeline` off `main`
**Design:** `docs/superpowers/specs/2026-06-19-gsx-pipeline-and-extensions-design.md` (approved)
**Status:** ready for SDD

## Goal

Make `{ name |> upper }` and `{ s |> truncate(5) }` actually render, in both
interpolation and attribute context, by **lowering** `Stages` to nested,
type-checked Go calls resolved against a shipped `std` filter package via
harvest-by-contract (`go/types`). No runtime FuncMap, no reflection in generated
code — gsx resolves each stage name to a qualified Go func at codegen.

## The load-bearing insight (architecture)

The existing type pass probes `_gsxuse(expr)` and harvests the arg's type. For a
pipeline we **lower first, then probe the lowered expression**: emit
`_gsxuse(_gsxstd.Upper(seed))`. The harvest then returns the pipeline's **result**
type directly, so the result flows through the *existing* `classify` →
`emitRender` (interp) / context-escaper (attr) path unchanged. Lowering is a pure,
deterministic function of `(seed, stages, table)`, called identically from
`emitProbes` (analyze.go) and `genInterp`/`emitExprAttr` (emit.go) — so the
**order invariant holds for free** (same lowered string in probe and emission).

`a |> R ≡ R(a)`, left-to-right: `{ x |> a |> b(n) }` → `_gsxstd.B(n)(_gsxstd.A(x))`.

## Scope

**IN (this slice):**
- `github.com/gsxhq/gsx/std` package: starter filters, harvest-by-contract style.
- Filter resolver: harvest `std` exports via `go/packages`, name→qualified-call
  table, first-letter-lower naming, bare-vs-parameterized classification.
- Lowering wired through probe + emission, for **interpolation** and **plain/URL
  attribute** context. Result rendered/escaped type-aware by the existing path.
- Fixed filter-package set = `[std]` (the resolver loads exactly `std`).

**OUT (deferred — clear errors, not silent):**
- Per-stage `?` and seed `?` on a pipeline → error (keep guarding). *(Real
  unwrap-threading is a follow-up task; the existing `(T,error)` single-unwrap is
  for whole-interp `?` only.)*
- `gen.Main` / `cmd/gsx` / `WithFilters` extensibility seam; user filter packages;
  collision/precedence; `gsx info`/`vet`; LSP. (Resolver hardcodes `[std]` now;
  registration is a separate phase.)
- Initialism-aware naming; pipeline-as-filter-argument; ambient `mapEach`.
- Composable `class`/`style` pipelines beyond what a plain `ExprAttr` already
  supports (class/style still route to `ClassAttr`, still deferred).

## Tasks

### Task 1: `std` filter package

Create `std/std.go` (+ `std/std_test.go`). **Stdlib-only.** Starter set, all
real implementations (no heuristics):

- **bare** `func(string) string`: `Upper` (strings.ToUpper), `Lower`
  (strings.ToLower), `Trim` (strings.TrimSpace).
- **parameterized**:
  - `Truncate(n int) func(string) string` — rune-safe cut to ≤ n runes (no
    ellipsis in v1; document it).
  - `Join(sep string) func([]string) string` — strings.Join.
  - `Default(fallback string) func(string) string` — returns fallback when input
    is `""`, else input.

Unit-test each (bare + curried application). No dependency on the codegen package.
Commit: `std: starter filter package (upper/lower/trim/truncate/join/default)`.

### Task 2: Filter resolver (harvest-by-contract)

New `internal/codegen/filters.go`. A `filterTable` mapping template-name →
`{funcName string, kind (bare|param)}`, all qualified under the `std` import.

- `loadFilterTable(dir string) (filterTable, error)`: `packages.Load` with
  `NeedTypes|NeedImports|NeedDeps|NeedName` for pattern
  `"github.com/gsxhq/gsx/std"` in `dir` (resolves via the module's go.mod, incl.
  the test replace). Iterate `pkg.Types.Scope().Names()`; for each **exported**
  `*types.Func` whose signature matches the contract, register it.
- **Contract classification** on `*types.Signature` sig (no receiver):
  - **param**: 1 result that is itself a `*types.Signature` with exactly 1 param
    and 1 result → kind=param (outer params are the filter args).
  - **bare**: exactly 1 param and 1 result, result not the above → kind=bare.
  - else: skip (not a filter).
- **Name**: first rune lowered (`Upper`→`upper`, `Truncate`→`truncate`). Document
  the initialism rough edge as deferred.
- White-box test `filters_test.go`: load the real `std`, assert `upper`→bare
  `Upper`, `truncate`→param `Truncate`, `join`→param, and that a non-filter export
  (add a `var`/non-contract func to std in the test? no — assert only what std
  exports) is absent. Run resolver against repo root.
Commit: `codegen: filter resolver — harvest std by contract (name→qualified func)`.

### Task 3: Lowering — interpolation

Pure lowering + wire into the type pass and `genInterp`.

- `lowerPipe(seed string, stages []ast.PipeStage, table filterTable) (expr string, usesStd bool, err error)`:
  - error if any `stage.Try` (deferred) — message names the attr/interp.
  - left-fold: start `acc = "("+strings.TrimSpace(seed)+")"`; for each stage look
    up `table[stage.Name]`:
    - unknown name → error (`unknown filter %q`).
    - bare with non-empty `stage.Args` → error (`filter %q takes no arguments`).
    - param with empty `stage.Args` → error (`filter %q requires arguments`).
    - bare: `acc = "_gsxstd."+FuncName+"("+acc+")"`.
    - param: `acc = "_gsxstd."+FuncName+"("+Args+")("+acc+")"`.
  - return acc, usesStd=true.
- analyze.go: in `emitProbes`, for an `Interp`/`ExprAttr` with non-empty `Stages`,
  emit `_gsxuse(<lowered>)`; thread a `usesStd` flag so `buildSkeleton` adds
  `import _gsxstd "github.com/gsxhq/gsx/std"`. The resolver `filterTable` must be
  available to `emitProbes` — load it once in `resolveTypesPkg` (pass `dir`),
  thread into the emit path. **Order invariant:** lowering is deterministic, so the
  probe and the later `genInterp` produce identical text — keep them calling the
  same `lowerPipe`.
- `genInterp`: replace the `len(n.Stages) > 0` guard with: compute
  `lowerPipe(n.Expr, n.Stages, table)`, set `imports["github.com/gsxhq/gsx/std"]`
  (aliased `_gsxstd` in `writeImports`), then `emitRender(lowered, resolved[n], …)`
  — `resolved[n]` is already the **result** type (probe was lowered).
- `writeImports`: emit `_gsxstd "github.com/gsxhq/gsx/std"` when that import key set.
- Tests (e2e_test.go): `{name |> upper}`, `{name |> upper |> trim}`,
  `{s |> truncate(5)}` (rune-safe), `{tags |> join(", ")}` ([]string→string),
  loop-var pipeline `{ for _,x := range xs { <li>{x |> upper}</li> } }`. Error
  tests: unknown filter, args-arity mismatch, `?`-stage rejected.
Commit: `codegen: lower interpolation pipelines to std filter calls`.

### Task 4: Lowering — attribute context

- `emitExprAttr` (emit.go): replace the `len(a.Stages) > 0` guard with the same
  `lowerPipe` call; the lowered result type (`resolved[a]`) flows through the
  existing context dispatch — plain → `AttrValue`, URL → `URL`, bool-typed →
  `BoolAttr`. JS/CSS context still rejects **before** lowering (a pipeline does not
  unlock those yet). `a.Try`/per-stage `?` still error.
- The probe side is already handled by Task 3 (`emitProbes` covers `ExprAttr`
  stages). Verify the skeleton import + order invariant for attrs.
- Tests: `data-x={name |> upper}` (plain, escaped), `data-tags={tags |> join(",")}`,
  `href={u |> trim}` (URL-sanitized after lowering), JS-context pipeline
  (`onclick={x |> upper}`) still rejected, `?`-stage in attr rejected.
Commit: `codegen: lower attribute pipelines (context-aware escaping of result)`.

## After tasks

- Final whole-feature review (adversarial: arity/unknown-filter errors, order
  invariant with mixed pipeline+non-pipeline nodes of different types, URL/plain
  escaping of a piped result, rune-safety of truncate).
- Independent adversarial review with live probing (project merge gate).
- Merge `--no-ff` to main; update ROADMAP (phase-2 #4 → done; note deferrals).

## Open risks

- **`std` import resolution under the probe load.** `loadFilterTable` and the
  overlay typecheck both run in the generated module's dir; `std` resolves via that
  module's go.mod (test replace → repoRoot/std). Verify in Task 2 against repo root
  and in Task 3 inside the e2e temp module.
- **`_gsxstd` alias collision.** Use the reserved `_gsx*` prefix (already the
  machinery convention) so no user param can shadow it; no `checkReservedParams`
  change needed (params can't start with `_gsx`).
- **Probe arity.** `harvest` asserts `len(call.Args) == 1`; the lowered pipeline is
  a single expression → one arg. Safe.
