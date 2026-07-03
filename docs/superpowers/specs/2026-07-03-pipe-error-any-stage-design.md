# Pipeline filters returning (R, error) at any stage

**Date:** 2026-07-03
**Status:** Approved (probe form to be validated against generated corpus output during implementation)

## Problem

The filter contract already admits `func([ctx,] subject, args...) (R, error)`
(`classifyFilter` / `validFilterResults`, `internal/codegen/filters.go`), and a
pipeline whose **final** stage returns `(R, error)` auto-unwraps through the
existing per-context tuple machinery. But `lowerPipe` nests stage calls
textually (`f2(f1(seed))`), so an error-returning **mid** stage produces a
multi-value-in-single-value-context Go error — caught by the probe, but
surfaced as raw go/types noise instead of working or producing a friendly
diagnostic.

There is also zero end-to-end test coverage for error-returning filters: the
corpus can only wire the std filter package (`codegenDirs` hardcodes
`FilterPkgs: [std]`), no std filter returns an error, and the gap was
explicitly deferred (note in `tuple/child_prop_pipeline.txtar`).

## Goals

- A filter returning `(R, error)` works at **any** pipeline stage, in every
  context that accepts pipelines.
- On error: the chain halts at the failing stage (later filters never run),
  the error returns out of the component's `gsx.Func` closure, and rendering
  stops — identical semantics to the existing `(T, error)` auto-unwrap.
- Corpus infrastructure for case-local filter packages, and a full coverage
  matrix (contexts × stage positions × ctx/generic variants).
- No syntax change. Parser, AST, formatter, tree-sitter, vscode-gsx,
  CodeMirror all untouched. The `?` try-marker stays rejected.
- No runtime API change.

## Non-goals

- Mid-chain error *handling* (recover/default). The escape hatch remains
  `{ if v, err := ...; err != nil { … } }`.
- Pipelines in script/JS contexts (already rejected; unchanged).
- Migrating all cond-attr lowering to statement form (only branches that need
  hoisting; see below).

## Design

### 1. Harvest: `hasErr` on `filterEntry`

`filterEntry` gains `hasErr bool`, set in `harvestFilters` from
`sig.Results().Len() == 2` (the contract already guarantees the second result
is `error`). Static — no type resolution at lowering time; correct for generic
filters too (result arity is not instantiation-dependent).

### 2. Stage-aware lowering, two forms from one core

`lowerPipe` is restructured so emit and probe derive from the same
stage-lowering core (preserving the emit ≡ probe invariant):

- **Emit form.** For each error-returning **non-final** stage, write
  `_gsxvN, _gsxerr := <stage call>; if _gsxerr != nil { return _gsxerr }`
  into the context's statement buffer (`b` + `interpTemp`, already threaded
  through every context — same mechanism as `hoistTuple`), then feed `_gsxvN`
  to the next stage. The **final** stage remains an expression; its
  `(T, error)` flows into the existing per-context tuple machinery
  (`tupleUnwrapType` / `hoistTuple`) unchanged.
- **Probe form.** Each error-returning non-final stage wraps in the existing
  skeleton helper `_gsxunwrap[T any](v T, _ ...any) T`
  (`module_importer.go`), keeping the probe a single expression:
  `_gsxuse(f2(_gsxunwrap(f1(seed))))`. Type harvest yields the pipeline's
  final result type; positioned go/types errors for bad user args are
  preserved. (To be validated by inspecting corpus goldens + a
  skeleton-snapshot unit test during implementation.)

### 3. Cond-attr branches: statement-form `if/else` when hoisting is needed

Component conditional attrs currently lower to
`gsx.AttrsCond(cond, func() gsx.Attrs { … }, els)` — evaluated eagerly at
bag-build time; the thunks exist only so the untaken branch's expressions
never run. When (and only when) a branch needs statement hoisting, lower to:

```go
var _gsxvN gsx.Attrs
if cond {
    _gsxv1, _gsxerr := _gsxf0.Parse(x)
    if _gsxerr != nil { return _gsxerr }
    _gsxvN = gsx.Attrs{{Key: "class", Value: _gsxv1}}
} else {
    _gsxvN = gsx.Attrs{…}
}
```

and reference `_gsxvN` where the `AttrsCond` call stood. Same semantics:
cond evaluated once at the same point, untaken branch never evaluates.
Plain branches keep the `AttrsCond` expression form, so existing goldens stay
byte-identical. This also lifts the three pre-existing "not supported yet"
edges in cond-attr branches (tuple-returning class parts, ordered class
parts, value-form CF). `gsx.AttrsCond` remains in the runtime. Promoting
statement form to the *only* lowering is a possible follow-up cleanup.

> **Correction (2026-07-03, final review):** the claim above — that
> statement-form lowering "lifts the three pre-existing 'not supported yet'
> edges" — did not hold as shipped. The statement form triggers only for
> branches whose lowering already needs a hoist because they contain an
> error-returning pipeline stage (`hasErr`); the three class-part edges
> (tuple-returning class parts, ordered class parts, value-form CF) key off
> resolved-type/ordering information the probe phase never harvests for class
> parts nested in a component cond-attr branch, not off `hasErr`, so they were
> not lifted. Class-part error pipelines (mid-stage rejected with a generic
> message, final-stage/plain-tuple leaking a raw `_gsxrt.Class` go/types
> error) remain unsupported in that specific position — see the ROADMAP
> known-gap entry for the current, verified behavior.
>
> **Superseded (2026-07-03):** the statement-form lowering this section
> describes is retired by `2026-07-03-attrscond-error-design.md`, which
> changes `AttrsCond` thunks to return `(Attrs, error)` — restoring one
> uniform thunk lowering and closing the branch class-part gap entirely.

### 4. Contexts

Supported everywhere pipelines are legal: text interp, attr values, class
parts (incl. value-form CF arms), style, child-props, ordered attrs,
cond-attr branches (via §3). Script/JS contexts already reject pipelines.

### 5. Corpus infrastructure: per-case filter packages

`caseToml` (`internal/corpus/loader.go`) gains
`FilterPackages []string` (TOML key `filterPackages` — the same key real
config uses in `gen/configfile.go`, so cases exercise the real config-name
path). Entries use the relative form `"./filters"`; the loader resolves them
against the case's import root (`corpustest/cases/<dir>/filters`) and threads
them into `codegenDirs`'s `FilterPkgs` alongside std. An absolute import path
is passed through verbatim (allowing a case to reference std explicitly). Case-local pure-Go packages already work
(class-merger precedent).

### 6. Coverage matrix

Corpus cases (each pinning `input.gsx` + `generated.x.go.golden` +
`render.golden`, error cases also pinning the failing-render error text):

| Dimension | Cases |
|---|---|
| Stage position | mid-stage error; final-stage error; both stages error (`a \|> parse \|> validate`) |
| Contexts | text, attr value, class part, value-form CF arm, style, child-prop, ordered attrs, cond-attr branch |
| Ctx filters | `func(ctx, subject) (R, error)` mid-stage |
| Generics | generic error filter mid + final (`func First[T any]([]T) (T, error)`); inference through a chained generic pipeline (`data \|> parse \|> first \|> upper`); a failing-inference case pinning diagnostic quality |
| Runtime behavior | render halts at failing stage; later filters never run (filter with observable side effect); error value propagates out of render |
| Negative | contexts where lowering still fails pin positioned diagnostics (no raw go/types noise) |

Unit tests: `hasErr` harvest; `classifyFilter` accepts generic funcs;
skeleton-snapshot test showing the probe form for an error-mid-stage
pipeline.

### 7. Docs

`docs/guide/` pipeline page: document error-returning filters (any stage,
halt-on-error semantics, filter contract reminder, escape hatch).
Update `docs/ROADMAP.md`. No sibling-repo changes (no syntax change).

## Open question (validate during implementation)

The probe form (§2) is approved in principle; the user wants to inspect the
actual generated corpus output and skeleton snapshot to confirm it matches
expectations before merge.

## Process

Feature branch in a git worktree; `make check` inner loop; `make ci` +
independent adversarial review before merge.
