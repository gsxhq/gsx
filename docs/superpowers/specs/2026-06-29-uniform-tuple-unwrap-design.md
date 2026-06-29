# Uniform `(T, error)` Auto-Unwrap — Design

- **Date:** 2026-06-29
- **Status:** Draft (awaiting review)
- **Topic:** Make gsx's `(T, error)` auto-unwrap apply to **every renderable value-expression position**, closing the two gaps (child-component prop values and `{{ }}` ordered-attrs pair values), and ship **exhaustive corpus coverage for all supported scenarios**.

## Problem

gsx auto-unwraps a `(T, error)` expression — emitting `tmp, _gsxerr := <expr>; if _gsxerr != nil { return _gsxerr }` then using `tmp` — in *most* positions, but **not** where an expression is inlined verbatim into a Go composite literal:

- **Child-component prop value** — `<Card title={lookup(t)}/>` (`lookup` returns `(string,error)`) → `multiple-value lookup(t) … in single-value context` (`components/child_prop_tuple_error.txtar`).
- **`{{ }}` ordered-attrs pair value** — `{{ "data-signals": sig(t) }}` → same Go error.

User invariant: **the `(T, error)` auto-unwrap should be accepted anywhere an expression is allowed.** These two positions violate it.

## Goals

- `(T, error)` unwraps in child-prop values and `{{ }}` pair values, identical in semantics to the existing positions (error propagates out of the enclosing `Render`/closure).
- **Exhaustive corpus coverage** of every supported `(T, error)` scenario across every position (see Test Matrix) — including an audit of the already-working positions to fill any gaps.
- One shared unwrap helper (the hoist is currently copy-pasted 5×).
- Preserve field-type checking (a `(int,error)` into a `string` field still errors) and the rejection of non-`(T,error)` tuples.
- No behavior change for non-tuple values; no perf regression.

## Non-goals (out of scope — not `(T,error)` value slots)

Positions that inline into a literal but are **not** single renderable value slots, so unwrap does not apply:
- spread `{ expr... }` (must be `gsx.Attrs`), byo whole-struct splat `{ d... }` (whole struct),
- conditional-attr `cond` (bool) and its branch attr values, composed `class`/`style` parts (string-ish seed),
- `for`/`if`/`switch` clauses and `GoBlock` (statement/control-flow, not value positions).

These are documented as deliberately excluded.

## Mechanism

### Position inventory (from investigation)
Already unwrap (verify coverage, don't change): (1) text interp, (2) element attr value, (3) `<style>` interp, (4) `<script>` interp, (5) JS-attr `@{}` hole, (6) children/slot, (7) named-slot, (8) pipeline result at its host. **Close:** (9) child-prop value, (10) `{{ }}` pair value. Both flow through the shared `childPropsLiteral` builder (`emit.go`).

### The skeleton-tolerance helper (the crux)
The type-check skeleton currently emits `CardProps{Title: lookup(t)}`, which go/types rejects *before* type harvest. Add to the skeleton preamble (`module_importer.go` ~438, beside `_gsxuse`):

```go
func _gsxunwrap[T any](v T, _ ...error) T { return v }
```

Go's `f(g())` multi-value spread makes `_gsxunwrap(lookup(t))` bind `v` = the value and `...error` = the error, so it type-checks for **both** tuples and plain values, while the returned `T` is still assigned to the field — **field-type checking is preserved** (`(int,error)`→`string` field still errors; a non-`(_,error)` tuple like `(int,string)` errors because the 2nd value isn't `error`). The skeleton wraps every child-prop / `{{ }}` value in `_gsxunwrap(...)`.

### Tuple detection (separate from tolerance)
To know which values to hoist at emit time, harvest the **raw** expr type via a `_gsxuse(<rawexpr>)` probe (variadic `...any` already binds a tuple). So each child-prop / pair value gets two skeleton emissions: `_gsxuse(raw)` (harvest → tuple-ness) and the `_gsxunwrap`-wrapped literal field (field-compat). The existing `(T,error)` guard (`tup.Len()==2 && tup.At(1).String()=="error"`) classifies; anything else is the existing "only (T, error) is supported" diagnostic.

### Emit-time hoist
In `genChildComponent`, at the insertion point right before the `_gsxgw.Node(ctx, Comp(CompProps{…}))` write (`emit.go` ~2009–2013): when **any** prop/pair value in the call is a tuple, hoist **all** of that call's value expressions to temps **in source order** (tuples via `tmp,_gsxerr:=…;if _gsxerr!=nil{return _gsxerr}`, non-tuples via `tmp := expr`), then reference the temps in the literal. Hoisting all-when-any preserves left-to-right side-effect order. Temp names reuse the existing `interpTemp *int` counter (`_gsxv%d`) — collision-free with all other unwrap sites and correct inside slot closures (`return _gsxerr` binds to the enclosing func).

### `{{ }}` node-key plumbing (Phase 2)
`OrderedPair` is a plain struct, not an `ast.Node`, so it cannot key `resolved`. Promote it (add `span` + `Pos()/End()` + `Inspect` recursion) **or** use a synthetic `(OrderedAttrsAttr, index)` key; then add per-pair probing into `collectExprs`/`emitProbes` (pair values are currently absent from harvest entirely — only present in `collectAttrSrc` liveness). Then the same hoist applies at the `{Key:…, Value:…}` splice.

## Phasing

- **Phase 0 — Extract the shared unwrap helper** (no behavior change). Replace the 5 copy-pasted hoists (`emit.go` 947, 1123, 1204, 1401, 1577) with `hoistTuple(b, expr, t, interpTemp) (tmp string, T types.Type)` + `tupleUnwrapType(t) (T types.Type, ok bool)`. Existing goldens unchanged. **Refactor gate before any new behavior.**
- **Phase 1 — Child-prop values (#9).** Skeleton `_gsxunwrap` + raw-type harvest for child-prop ExprAttrs; emit hoist-all-when-any in `genChildComponent`; flip `child_prop_tuple_error.txtar` error→success.
- **Phase 2 — `{{ }}` pair values (#10).** Node-key plumbing + probe/harvest for pair values; reuse Phase-1 skeleton tolerance + emit hoist at the pair splice.

## Test Matrix (acceptance criteria — exhaustive)

Every cell is a corpus case (per CLAUDE.md per-context rule) unless noted. **P** = pre-existing (audit; add if missing). **N** = new this work.

### A. Positions × happy-path `(T, error)` unwrap (T = string)
| Position | Status |
|---|---|
| text interp `{ f() }` | P (verify) |
| element attr `name={ f() }` (plain) | P `attrs/attr_error_autounwrap.txtar` |
| element attr URL context (`href={ f() }`) | P (verify) |
| element attr JS-context (`onclick={ f() }`) | N if missing |
| `<style>` interp | P `style/block_tuple_error` family (verify happy path) |
| `<script>` interp | P `script/value_tuple` (verify) |
| JS-attr `@{ f() }` hole | N if missing |
| children/slot `{ f() }` | N if missing |
| named-slot value `{ f() }` | N if missing |
| pipeline `seed \|> filt` returning `(R,error)` | P `pipelines/*` (verify) |
| **child-prop value `<Card x={ f() }/>`** | **N** |
| **`{{ }}` pair value `{{ "k": f() }}`** | **N** |

### B. Scenario variations (apply to the two new positions; spot-check existing)
- **T variety:** `(string,error)`, `(int,error)`, `(gsx.Node,error)`, `([]gsx.Node,error)`, a `Stringer` — render correctly. (N)
- **Multiple tuples in one call:** `<Card a={f()} b={g()}/>` and `{{ "a": f(), "b": g() }}` — two hoists, correct order. (N)
- **Mixed tuple + non-tuple in one call:** `<Card a={f()} b={x}/>` — source-order eval preserved (hoist-all). (N)
- **Nested / threaded:** a tuple value in `{{ }}` bound to a prop spread one layer down; a child component nested inside a slot with a tuple prop. (N)
- **Pipeline into the new positions:** `<Card x={ seed \|> filt }/>` where filt returns `(R,error)`. (N)
- **Error-propagation path:** a function returning a non-nil error → `Render` returns it. *Runtime unit test* in the root `gsx`/codegen test (corpus render uses the nil-error path); assert the returned error propagates and output halts. (N)
- **Whitespace interaction:** `x = {{ "k": f() }}` still parses + unwraps (regression with the ws-around-`=` feature). (N)

### C. Rejection / diagnostics (must stay/clarify)
- **Non-`(T,error)` tuple:** `(int, string)` into a child-prop / `{{ }}` value → pointed gsx diagnostic "only (T, error) is supported", correct position. (N — was a raw Go error before for these positions)
- **`(int,error)` into a `string` field:** still a field-type error (tolerance must not weaken checking). (N)
- **Three-value return** into the position → diagnostic, not a panic. (N)

### D. Regeneration audit
Re-baseline goldens whose `_gsxv%d` indices shift (`orderedattrs/*`, `fallthrough/manual_split_container_bag`, etc.); confirm `//line` column accuracy tests still pass after the skeleton change.

## Risks
- **Skeleton tolerance must not weaken type checking** — `_gsxunwrap[T any](v T, _ ...error) T` keeps the field assignment checked; verify with a Section-C case.
- **Eval order** — hoist-all-when-any preserves order; verify with a side-effect-ordering case.
- **`//line` column math** — `analyze.go` hardcodes `len("_gsxuse(")`; adding `_gsxunwrap(`/per-pair probes can shift columns. Keep probe formatting consistent and re-verify column tests.
- **Phase 2 node-key change** ripples through `Inspect`, printer, and any AST walker — keep it minimal and covered.
