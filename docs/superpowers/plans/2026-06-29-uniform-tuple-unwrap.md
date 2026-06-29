# Uniform `(T, error)` Auto-Unwrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make gsx's `(T, error)` auto-unwrap apply to child-component prop values and `{{ }}` ordered-attrs pair values (the two remaining inlined-into-literal positions), with exhaustive corpus coverage of every supported scenario.

**Architecture:** A type-check-skeleton helper `_gsxunwrap[T any](v T, _ ...error) T` lets a tuple value type-check in the props literal while preserving field-type checking; the raw expression type is harvested separately (via the existing `_gsxuse` probe) to detect which values are tuples; at emit time, when any value in a child call is a tuple, all of that call's values are hoisted to temps in source order (`tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }` for tuples, `tmp := expr` for the rest) and the literal references the temps. The duplicated 5× hoist is first extracted into one helper.

**Tech Stack:** Go 1.26.1, `go/types` (skeleton + harvest), `go/token`, the txtar corpus harness.

## Global Constraints

- Runtime root package stays standard-library only (this work touches `internal/codegen` + `ast`, not the runtime — fine).
- **Skeleton tolerance must NOT weaken field-type checking:** `_gsxunwrap[T any](v T, _ ...error) T` keeps the assigned value checked against the field; a `(int,error)` into a `string` field must still error, and a non-`(_,error)` tuple (e.g. `(int,string)`) must still be rejected.
- The existing `(T,error)` guard is `tup.Len() == 2 && tup.At(1).Type().String() == "error"`; anything else → the existing diagnostic "only (T, error) is supported".
- No behavior change for non-tuple values; the error propagates with `return _gsxerr` (binds to whichever enclosing func/closure the hoist sits in).
- **Every codegen change ships corpus cases.** Regenerate goldens: `go test ./internal/corpus -run TestCorpus -update`; then verify WITHOUT `-update`. The `-update` run rewrites `coverage.golden` — `git add` it (a forgotten bump fails the suite).
- Don't hand-edit `.x.go`/golden files — change source and regenerate.
- `make check` (inner loop) before each task done; `make ci` before merge.
- Pin Go to `GO_VERSION` (1.26.1). Commit frequently.

## File Structure

- **Modify** `internal/codegen/emit.go` — extract `hoistTuple`/`tupleUnwrapType` (Task 1); emit-time hoist in `genChildComponent` (`~2009-2013`) and the `{{ }}` splice (`~2298`) (Tasks 2, 3).
- **Modify** `internal/codegen/analyze.go` — skeleton `_gsxunwrap` wrapping + raw-type harvest of child-prop values (Task 2) and `{{ }}` pair values (Task 3); `collectExprs`/`emitProbes` ordering.
- **Modify** `internal/codegen/module_importer.go` (`~438`) — add the `_gsxunwrap` skeleton-preamble declaration.
- **Modify** `ast/ast.go` (`OrderedPair` `~379`) — give pairs a `resolved`-mappable key (Task 3).
- **Create** corpus cases under `internal/corpus/testdata/cases/tuple/` (new positions + scenarios) and fill audit gaps in existing dirs.
- **Modify** `internal/corpus/testdata/cases/components/child_prop_tuple_error.txtar` — flip error→success (Task 2).

---

### Task 1 (Phase 0): Extract the shared `(T, error)` unwrap helper

Pure refactor, **no behavior change** — de-risks Tasks 2–3 which reuse it.

**Files:**
- Modify: `internal/codegen/emit.go` (the 5 duplicated hoist sites: `genInterp` ~947, `emitCSSInterp` ~1123, `emitJSInterp` ~1204, `emitJSAttrInterp` ~1401, `emitExprAttr` ~1577)

**Interfaces:**
- Produces:
  - `func tupleUnwrapType(t types.Type) (T types.Type, ok bool)` — returns `(tup.At(0).Type(), true)` when `t` is a `*types.Tuple` of len 2 with the 2nd element type `"error"`; `(nil, false)` otherwise.
  - `func hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) (tmp string)` — emits `\t\t<tmp>, _gsxerr := <expr>\n\t\tif _gsxerr != nil {\n\t\t\treturn _gsxerr\n\t\t}\n` with `tmp = fmt.Sprintf("_gsxv%d", *interpTemp)` then `*interpTemp++`, and returns `tmp`.

- [ ] **Step 1: Add the two helpers**

In `internal/codegen/emit.go`:

```go
// tupleUnwrapType reports whether t is a (T, error) tuple, returning T. Any other
// tuple shape is not unwrappable (callers emit the "only (T, error)" diagnostic).
func tupleUnwrapType(t types.Type) (types.Type, bool) {
	tup, ok := t.(*types.Tuple)
	if !ok || tup.Len() != 2 || tup.At(1).Type().String() != "error" {
		return nil, false
	}
	return tup.At(0).Type(), true
}

// hoistTuple emits `tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`
// and returns the temp name. interpTemp is the shared per-component counter, so
// temps are unique across all unwrap sites and `return _gsxerr` binds to the
// enclosing func/closure.
func hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) string {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\treturn _gsxerr\n\t\t}\n", tmp, expr)
	return tmp
}
```

- [ ] **Step 2: Replace each of the 5 sites**

At each site, the existing shape is (e.g. `genInterp`):
```go
if tup, ok := t.(*types.Tuple); ok {
	if tup.Len() != 2 || tup.At(1).Type().String() != "error" {
		bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "...only (T, error) is supported", expr, t)
		return false
	}
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n...", tmp, expr)
	return emitRender(b, tmp, tup.At(0).Type(), imports, n, bag)
}
```
Rewrite to:
```go
if _, isTuple := t.(*types.Tuple); isTuple {
	elemT, ok := tupleUnwrapType(t)
	if !ok {
		bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "...only (T, error) is supported", expr, t)
		return false
	}
	tmp := hoistTuple(b, expr, interpTemp)
	return emitRender(b, tmp, elemT, imports, n, bag)  // (the per-site consumer call is unchanged)
}
```
Keep each site's diagnostic message string and its downstream consumer (`emitRender` / CSS / JS / attr writer) EXACTLY as-is — only the tuple-shape check and the hoist lines are replaced by the helpers. Do all 5 sites.

- [ ] **Step 3: Verify no behavior change**

Run: `go test ./internal/corpus -run TestCorpus` and `go test ./internal/codegen`
Expected: PASS with NO golden changes (this is a refactor). If any golden changes, the refactor altered output — fix until goldens are byte-identical. Run `git diff --stat internal/corpus/testdata` — expect empty.

- [ ] **Step 4: `make check` + commit**

```bash
git add internal/codegen/emit.go
git commit -m "refactor(codegen): extract hoistTuple/tupleUnwrapType ((T,error) unwrap helper)"
```

---

### Task 2 (Phase 1): `(T, error)` auto-unwrap for child-component prop values

**Files:**
- Modify: `internal/codegen/module_importer.go` (~438, skeleton preamble) — add `_gsxunwrap`
- Modify: `internal/codegen/analyze.go` — skeleton wraps child-prop values in `_gsxunwrap(...)`; harvest each child-prop value's RAW type (add `_gsxuse(rawexpr)` probes to the `collectExprs`/`emitProbes` ordering that currently excludes child-prop simple attrs: `collectExprs` ~1399-1411, `emitProbes` child branch ~744-776)
- Modify: `internal/codegen/emit.go` — `genChildComponent` (~1945) / `childPropsLiteral` (~2129): hoist-all-when-any at the call-emit point (~2009-2013)
- Test: `internal/corpus/testdata/cases/components/child_prop_tuple_error.txtar` (flip) + `internal/corpus/testdata/cases/tuple/child_prop_*.txtar` (new)

**Interfaces:**
- Consumes: Task 1's `hoistTuple`, `tupleUnwrapType`.
- Produces: child-prop ExprAttr values are present in `resolved` (raw type, tuple-tolerant); generated child calls hoist tuple-valued props before the `_gsxgw.Node(ctx, Comp(CompProps{…}))` statement.

- [ ] **Step 1: Write the failing corpus case (flip the existing one)**

Edit `internal/corpus/testdata/cases/components/child_prop_tuple_error.txtar` — keep `input.gsx` (`<Card title={lookup(t)}/>`, `lookup` returns `(string,error)`), add `-- invoke --` `Page(PageProps{T:"7"})`, and REPLACE the `diagnostics.golden` (currently the multiple-value error) with empty diagnostics + a `generated.x.go.golden` + `render.golden` (the `-update` run fills them).

- [ ] **Step 2: Run to confirm RED**

Run: `go test ./internal/corpus -run TestCorpus` (without update).
Expected: FAIL — the case still emits the `multiple-value … single-value context` diagnostic (skeleton) / or won't render. Capture this as RED.

- [ ] **Step 3: Add the skeleton helper**

In `internal/codegen/module_importer.go` near the `_gsxuse` decl (~438), add to the skeleton preamble text:
```go
func _gsxunwrap[T any](v T, _ ...error) T { return v }
```
(Confirm the preamble is a string/template assembled there; append this line so every probe file declares it. `_ = _gsxunwrap[int]` is not needed — it's used by the wrapped literal.)

- [ ] **Step 4: Skeleton — wrap child-prop values + harvest raw types**

In `internal/codegen/analyze.go`:
- Where the probe emits the child props literal for type-checking (the shared `childPropsLiteral` path used at ~754 / child branch ~744-776), wrap each child-prop **value expression** as `_gsxunwrap(<expr>)` in the skeleton ONLY (the emit-phase literal is handled in Step 5). This makes `CardProps{Title: _gsxunwrap(lookup(t))}` type-check for tuple and non-tuple alike while still checking `Title`'s type.
- Add a `_gsxuse(<rawexpr>)` probe for each child-prop ExprAttr value and register the ExprAttr node into the `collectExprs` ordering (currently skipped at ~1399-1411) so `harvest` (~1016-1033) assigns `resolved[exprAttr]` the RAW type (the tuple, when it is one). Keep the k-th ordering of `_gsxuse` calls and `nodes` in lockstep (the harvest walks them positionally).

- [ ] **Step 5: Emit — hoist-all-when-any in `genChildComponent`**

In `internal/codegen/emit.go`, make `childPropsLiteral` (or `genChildComponent`) expose per-field structure (field name + value expr + the ExprAttr node) instead of a pre-joined string for the emit phase (the probe phase keeps the `_gsxunwrap`-wrapped string). At the call-emit insertion point (~2009-2013, before `_gsxgw.Node(ctx, %s(%s{%s}))`):
- For each prop value, look up `resolved[exprAttr]`. If ANY is a tuple (`tupleUnwrapType` ok), hoist EVERY value of this call in source order: tuple → `tmp := hoistTuple(b, expr, interpTemp)`; non-tuple → emit `\t\t<tmpN> := <expr>\n` (a plain temp, name from the same `interpTemp` counter). Substitute each field's value with its temp in the literal.
- If NONE is a tuple, emit the literal unchanged (current behavior — no temps).

- [ ] **Step 6: Add new child-prop corpus cases**

Create under `internal/corpus/testdata/cases/tuple/`:
- `child_prop_single.txtar` — one tuple prop (string T).
- `child_prop_multi.txtar` — `<Card a={f()} b={g()}/>` two tuple props (order in render).
- `child_prop_mixed.txtar` — `<Card a={f()} b={x}/>` tuple + non-tuple (source order preserved).
- `child_prop_node.txtar` — `(gsx.Node, error)` valued prop.
- `child_prop_pipeline.txtar` — `<Card x={ seed |> filt }/>` where `filt` returns `(R,error)`.
Each: `input.gsx` + `invoke` + pinned `generated.x.go.golden` + `render.golden`.

- [ ] **Step 7: Regenerate + verify GREEN**

Run: `go test ./internal/corpus -run TestCorpus -update`, inspect that `child_prop_tuple_error.txtar` now renders (e.g. `<div>user 7</div>`) and the generated golden hoists `_gsxv0, _gsxerr := lookup(t); if _gsxerr != nil { return _gsxerr }` before `Card(CardProps{Title: _gsxv0})`. Verify WITHOUT `-update`. `go test ./internal/codegen` green.

- [ ] **Step 8: `make check` + commit**

```bash
git add internal/codegen/ internal/corpus/testdata/
git commit -m "feat(codegen): (T,error) auto-unwrap for child-component prop values"
```

---

### Task 3 (Phase 2): `(T, error)` auto-unwrap for `{{ }}` ordered-attrs pair values

**Files:**
- Modify: `ast/ast.go` (`OrderedPair` ~379) — make pairs `resolved`-mappable
- Modify: `internal/codegen/analyze.go` — probe/harvest each pair value (currently absent from `collectExprs`/`emitProbes`; only in `collectAttrSrc` liveness ~1742); wrap pair values in `_gsxunwrap` in the skeleton `OrderedAttrs{…}` literal
- Modify: `internal/codegen/emit.go` — hoist at the `{{ }}` splice (`*ast.OrderedAttrsAttr` case ~2286-2301, value at ~2298)
- Test: `internal/corpus/testdata/cases/tuple/ordered_*.txtar`

**Interfaces:**
- Consumes: Task 1 helpers; Task 2's skeleton `_gsxunwrap` + hoist-all machinery.
- Produces: `{{ }}` pair values are in `resolved` (raw type); tuple pair values are hoisted before the child call and referenced in `gsx.OrderedAttrs{{Key:…, Value: tmp}}`.

- [ ] **Step 1: Give `OrderedPair` a resolved key**

In `ast/ast.go`, the minimal approach: add a position to `OrderedPair` and make it addressable. Simplest that fits the `resolved map[ast.Node]types.Type`: embed `span` and add `Pos()/End()` + a `nodeNode()`/`attrNode()`-style marker so `*OrderedPair` satisfies `ast.Node`, and recurse into pairs in `Inspect` (so walkers see them). Update `parser/attrs.go` `parseOrderedAttrsLiteral`/`splitOrderedPairs` to `ast.SetSpan` each pair (value span). Update `ast/print.go` + `internal/printer/printer.go` if they pattern-match pairs (they iterate `Pairs` by value — confirm a struct→node change compiles; if `OrderedPair` must stay a value in `[]OrderedPair`, instead use a synthetic key: a `map[*OrderedAttrsAttr][]types.Type` keyed by attr+index, populated in harvest — choose whichever is least invasive and document it).

- [ ] **Step 2: Write the failing corpus case**

Create `internal/corpus/testdata/cases/tuple/ordered_single.txtar`: a component with a `gsx.OrderedAttrs` prop, a `<Card bag={{ "data-signals": sig(t) }}/>` where `sig` returns `(string,error)`, invoke + empty diagnostics + goldens. RED: before the change, `-update` then verify → the `multiple-value sig(t) … single-value context` error.

- [ ] **Step 3: Skeleton — probe/harvest + wrap pair values**

In `analyze.go`: add each pair value to the `collectExprs`/`emitProbes` ordering with a `_gsxuse(<rawpairexpr>)` probe (so `resolved[pairKey]` gets the raw tuple type), and wrap the pair value as `_gsxunwrap(<expr>)` in the skeleton `gsx.OrderedAttrs{{Key:…, Value: _gsxunwrap(<expr>)}}` literal so it type-checks (`Attr.Value` is `any`, so this mainly silences the multiple-value error). Keep k-ordering in lockstep.

- [ ] **Step 4: Emit — hoist pair tuples**

In `emit.go` `*ast.OrderedAttrsAttr` case (~2286-2301): this is inside the same child-call emit as Task 2, so it participates in the SAME hoist-all-when-any pass. For each pair, look up the pair's resolved type; if tuple, hoist before the call and emit `{Key: %q, Value: %s}` with the temp; else inline `pr.Value` as today. Ensure the hoist-all trigger considers pair values too (any tuple among props OR pairs → hoist all).

- [ ] **Step 5: Add `{{ }}` tuple corpus cases**

Under `internal/corpus/testdata/cases/tuple/`:
- `ordered_single.txtar` (from Step 2).
- `ordered_multi.txtar` — `{{ "a": f(), "b": g() }}` two tuple pairs.
- `ordered_mixed.txtar` — `{{ "a": f(), "b": "lit" }}` tuple + literal pair.
- `ordered_threaded.txtar` — tuple `{{ }}` bound to a prop spread one layer down (`<Outer x={{ "k": f() }}/>` → `Outer` passes to `Inner` → spread).
- `ordered_ws.txtar` — `bag = {{ "k": f() }}` (whitespace-around-`=` interaction).

- [ ] **Step 6: Regenerate + verify GREEN**

`go test ./internal/corpus -run TestCorpus -update`; confirm `ordered_single` renders `<div data-signals="…">` and the generated golden hoists the temp before the call. Verify WITHOUT `-update`.

- [ ] **Step 7: `make check` + commit**

```bash
git add ast/ parser/ internal/ internal/corpus/testdata/
git commit -m "feat(codegen): (T,error) auto-unwrap for {{ }} ordered-attrs pair values"
```

---

### Task 4: Complete the test matrix (coverage audit + scenario + rejection + error-path)

Fills the remaining cells of the spec's Test Matrix so EVERY supported scenario has a pinned test.

**Files:**
- Create: corpus cases under `internal/corpus/testdata/cases/tuple/`
- Create/Modify: a runtime/codegen test for the error-propagation path

- [ ] **Step 1: Audit + fill position coverage (Matrix A)**

For each already-unwrapping position that lacks an explicit `(T,error)` corpus case, add one under `cases/tuple/`: `pos_text_interp.txtar`, `pos_attr_url.txtar` (`href={f()}`), `pos_attr_js.txtar` (`onclick={f()}`), `pos_jsattr_hole.txtar` (`x-data="@{ f() }"`), `pos_children_slot.txtar` (`{ f() }` as children), `pos_named_slot.txtar`. (Grep `cases/` first — `grep -rl "(string, error)" internal/corpus/testdata/cases` — and only add what's missing; skip ones already covered like `attrs/attr_error_autounwrap`.)

- [ ] **Step 2: Rejection diagnostics (Matrix C)**

Add `cases/tuple/`:
- `reject_non_error_tuple_childprop.txtar` — `<Card x={twoInts()}/>` (`(int,int)`) → pin the "only (T, error) is supported" diagnostic, correct position.
- `reject_non_error_tuple_ordered.txtar` — same in a `{{ }}` value.
- `reject_field_type_mismatch.txtar` — `(int,error)` into a `string` field → field-type error still fires (tolerance didn't weaken checking).
Pin `diagnostics.golden` for each.

- [ ] **Step 3: Error-propagation runtime test (Matrix B)**

Corpus renders use nil-error funcs, so add a Go test (in `internal/codegen` or a small generated-and-run harness) that: defines a component with a child prop / `{{ }}` value whose function returns a non-nil error, renders it, and asserts `Render` returns that error and output stops at the failing point. Place it where similar generate-and-run tests live (grep for an existing one that compiles+runs generated output; if none, a focused unit test that checks the generated code contains `if _gsxerr != nil { return _gsxerr }` plus a hand-written runtime test exercising the pattern).

- [ ] **Step 4: Regenerate + verify; update coverage manifest**

`go test ./internal/corpus -run TestCorpus -update`; verify WITHOUT `-update`; `git add` `coverage.golden`.

- [ ] **Step 5: `make check` + commit**

```bash
git add internal/
git commit -m "test(tuple): exhaustive (T,error) unwrap coverage (positions, scenarios, rejections, error path)"
```

---

### Task 5: Docs + `make ci` + adversarial + final review

- [ ] **Step 1: Docs**

Update `docs/guide/syntax.md` (the `(T,error)` / error-handling section, or the ordered-attrs + child-component sections) to state that `(T,error)` auto-unwrap now works in child-prop and `{{ }}` value positions — "anywhere an expression is allowed." Update `docs/ROADMAP.md`. Commit.

- [ ] **Step 2: `make ci`**

Run `make ci`; fix any drift; regenerate goldens if needed. Must be green (uncached, both modules, gofmt + gsx fmt, examples drift).

- [ ] **Step 3: Independent adversarial review**

Dispatch one independent adversarial reviewer that builds throwaway `.gsx` probes: a tuple value with side effects mixed among non-tuple props (assert eval order), a tuple deep in nested children/slots (assert `return` binds correctly), a `(int,error)`→`string` field (assert still errors), a non-`(_,error)` tuple (assert clean diagnostic), a tuple `{{ }}` value threaded two layers down, and `gsx fmt` idempotence on all. Address findings.

- [ ] **Step 4: Final whole-branch review** (strongest model) then **superpowers:finishing-a-development-branch**.

---

## Self-Review

**Spec coverage:** Mechanism (`_gsxunwrap` + raw harvest + hoist-all) → Tasks 2,3. Phase 0 refactor → Task 1. Child-prop (#9) → Task 2. `{{ }}` (#10) → Task 3. Test Matrix A/B/C/D → Tasks 2,3 (their cases) + Task 4 (audit/scenario/rejection/error-path/regeneration). Out-of-scope list → not implemented, documented in spec. Docs → Task 5. ✓

**Placeholder scan:** Codegen Tasks 2–3 intentionally defer exact skeleton-threading/k-ordering to the implementer but pin the observable contract (golden output: the hoist statements + the temp-referencing literal) and name exact functions/anchors (`childPropsLiteral`, `emitProbes`, `collectExprs`, `harvest`, the `~2009-2013`/`~2298` splice) and the mechanism (`_gsxunwrap`, raw `_gsxuse` harvest). Golden-driven, gsx-canonical — not a placeholder. The `OrderedPair` node-key approach offers two concrete options with a "least invasive, document it" decision rule.

**Type consistency:** `tupleUnwrapType(types.Type) (types.Type, bool)`, `hoistTuple(*bytes.Buffer, string, *int) string`, `_gsxunwrap[T any](v T, _ ...error) T`, `interpTemp *int` (`_gsxv%d`) — used consistently across Tasks 1–3.
