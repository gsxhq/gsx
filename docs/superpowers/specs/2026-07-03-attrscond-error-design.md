# AttrsCond → (Attrs, error): uniform error-capable cond-attr thunks

**Date:** 2026-07-03
**Status:** Approved
**Supersedes:** the cond-attr portion (§3) of `2026-07-03-pipe-error-any-stage-design.md` — the statement-form lowering shipped there is retired by this design.

## Problem

PR #29 shipped `(R, error)` filters at any pipeline stage, but component
conditional-attr branches remained a partial cell: expression attrs with
error-stage pipes work (via a special statement-form lowering), while class
parts in the same position do not — a mid-stage pipe is rejected with a
positioned diagnostic, and a plain tuple call or final-stage error pipe leaks
a raw go/types error (`too many arguments in call to _gsxrt.Class`). Root
causes: (a) the branch thunk `func() Attrs` cannot propagate an error, so
hoisting is illegal inside it; (b) the probe phase harvests no types for
positions nested in component cond-attr branches, so emit-time tuple
detection (`resolved`) never fires there.

## Design

### 1. Runtime

```go
func AttrsCond(cond bool, then, els func() (Attrs, error)) (Attrs, error)
```

Same selection semantics as today: calls and returns `then()` when cond is
true, else `els()`; `els` may be nil; untaken/nil-thunk yields `(nil, nil)`.
The taken thunk's error propagates. Breaking change to the public runtime
API — accepted: pre-1.0, generated code is the only known caller, and
runtime + generated code version together. Root package stays stdlib-only.

### 2. Emit lowering — one uniform form

`condAttrsExpr` always emits `(Attrs, error)` thunks; plain branches return
`gsx.Attrs{…}, nil`. Branch bodies become real emit contexts:

- `condBranchAttrs` / `classEntryExpr` branch mode receive a live
  `b`/`interpTemp`/`emitPipeWrap`, with a thunk-local hoist variant that
  emits `return nil, _gsxerr` (two-value form) instead of `return _gsxerr`.
- The `AttrsCond(...)` call site is an ordinary `(Attrs, error)` expression,
  hoisted by the existing tuple machinery before the consuming statement:

```go
_gsxvN, _gsxerr := gsx.AttrsCond(hot, func() (gsx.Attrs, error) {
    _gsxv1, _gsxerr := _gsxf0.Parse((csv))
    if _gsxerr != nil {
        return nil, _gsxerr
    }
    return gsx.Attrs{ /* … */ }, nil
}, nil)
if _gsxerr != nil {
    return _gsxerr
}
```

Laziness is preserved by the thunks themselves (their entire purpose);
`cond` still evaluates exactly once at the same point.

**Deleted:** `condAttrsStmt`, `condBranchNeedsHoist`, the `b == nil`
"not supported yet" edges for tuple/ordered/CF class parts in branches, the
`errFailingStageUnsupported` class-pipe rejection (added in PR #29's
b088b5d), and the pre-existing "pipeline in a conditional attribute branch
… not supported yet" rejection. Pipes and tuples in branch positions —
error-returning or not — lower like any other position.

### 3. Probe/analyze — branch positions join the type harvest

The skeleton lowers cond-attr branches in the same `(Attrs, error)` thunk
form (emit ≡ probe; statements are legal inside the skeleton's thunk).
`collectExprs` + `emitProbes` are extended — following their documented
k-th-probe ↔ k-th-node ordering recipe — to cover, inside component
cond-attr branches: ExprAttr values, plain class parts, and value-form CF
arms. `resolved` is then populated for these nodes, so plain tuple calls
(`class={ cls(csv) }`, `data-x={ cls(csv) }`) hoist via the existing
machinery. Pipeline stages keep the established `probePipeWrap`
(`_gsxunwrap`) expression form.

### 4. Out of scope (unchanged)

Spread and nested cond-attr inside a branch stay rejected. Conditional
class on forwarding ELEMENTS (`fallthrough/cond_attr_class_rejected`) is a
different mechanism and stays rejected. No syntax change; no other runtime
change.

### 5. Corpus & docs

- ~5 existing goldens referencing `AttrsCond` regenerate to the new thunk
  form (mechanical churn; reviewed for shape, not suppressed).
- `pipeerr/cond_attr_branch_class_pipe_rejected.txtar` flips to a working
  case (renamed, e.g. `cond_attr_branch_class_pipe.txtar`).
- New cases: plain tuple class part in a branch (the old raw-leak); plain
  tuple ExprAttr in a branch; no-error pipe in a branch class part;
  else-branch with an error pipe; value-form CF arm with an error pipe in a
  branch; untaken-branch laziness re-proven under the new form (taken=false
  + would-fail filter → no error, no attr).
- `docs/ROADMAP.md`: close the class-parts-in-cond-attr-branch known-gap
  row, pointing here. `docs/guide/syntax/pipelines.md`: remove the
  branch-class-parts exception sentence. Prior spec gets a superseded note
  (done via the header of this spec; add a forward pointer in the old spec's
  §3 correction).

### 6. Testing & process

Corpus is the canonical gate (`-update` then verify without). Unit tests
where behavior is new (runtime `AttrsCond` error propagation; probe
alignment for branch positions). Subagent-driven execution with per-task
reviews; final independent adversarial review with throwaway probe
programs; full `make ci` before merge.
