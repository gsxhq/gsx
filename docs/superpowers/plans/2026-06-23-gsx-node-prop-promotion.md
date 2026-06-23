# `gsx.Node` prop promotion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A `gsx.Node`-typed component prop accepts any §5-renderable value (`string`/`int`/`float`/`bool`/`[]byte`/`Stringer` + `Node`/`[]Node`) from a `.gsx` caller, by wrapping the value's existing §5 render in a `gsx.Func` closure.

**Architecture:** Codegen-only. The child-props literal builder (`childPropsLiteral`) learns which target fields are `gsx.Node` (a new AST-derived `nodeProps` signal, parallel to the existing `propFields`), and for a non-node value bound to such a field, emits a `gsx.Func` closure that renders the value via the §5 emitter `emitRender`. Because the **emit ≡ probe** invariant must hold (the type-check skeleton and the emitted code must drive the SAME field set), the promotion uses an emit-vs-probe callback exactly like the existing `slotValue` for markup slots.

**Tech Stack:** Go; codegen in `internal/codegen`. No runtime change (the `.gsx` ergonomic rides entirely on existing §5 codegen). Stdlib-only runtime unchanged.

## Global Constraints

- **Go stays strict:** promotion happens ONLY at the `.gsx`→Go boundary (in `childPropsLiteral`). A hand-written Go `CardProps{Title: "x"}` still won't compile.
- **emit ≡ probe:** the promotion decision (field-is-`gsx.Node` + value category) and the field set must be IDENTICAL in `emitProbes` (skeleton) and emit (`genChildComponent`) — they share `childPropsLiteral`; the only allowed difference is the field VALUE supplied by a callback.
- **Escaping parity:** a promoted scalar is rendered by the SAME `emitRender` (`emit.go:823`) the inline `{ x }` uses → identical escaping.
- Renderable set = the §5 `classify` categories (`analyze.go:671-680`): `catString/catBytes/catInt/catUint/catFloat/catBool/catStringer` (promote) + `catNode/catNodeSlice` (already nodes); `catUnsupported` → the same error `{ x }` gives.
- `gsx.Text`/`gsx.Group` are OUT of scope.
- After each task: `go build ./...` and `go test ./...` pass. Bump `internal/codegen/version.go` when emitted code changes.

---

### Task 1: Derive + thread the `nodeProps` signal (which declared props are `gsx.Node`)

**Files:** Modify `internal/codegen/analyze.go` (the `propFields` derivation ~line 146-165, and the functions that thread `propFields`); `internal/codegen/emit.go` / `codegen.go` / `batch.go` (thread the new map to where `childPropsLiteral` is called).

**Interfaces — Produces:** `nodeProps map[string]map[string]bool` (propsType → fieldName → isGsxNode), built in the SAME loop as `propFields`, threaded the SAME way. A helper `func isGsxNodeType(typ string) bool { return strings.TrimSpace(typ) == "gsx.Node" }`.

- [ ] **Step 1:** In `analyze.go` where `propFields` is built (the `fields := map[string]bool{}` loop, ~146), also build a `nodeFields := map[string]bool{}` capturing node-typed params:
```go
fields := map[string]bool{}
nodeFields := map[string]bool{}
for _, p := range params {
	fields[fieldName(p.name)] = true
	if isGsxNodeType(p.typ) {
		nodeFields[fieldName(p.name)] = true
	}
}
```
and `nodeOut[propsName] = nodeFields` alongside `out[propsName] = fields`. Add `isGsxNodeType` (a small func near `fieldName`). (`p.typ` is the param's declared type source string — confirm `parseParams` yields `.typ`; it does, used elsewhere.)
- [ ] **Step 2:** Return/thread `nodeProps` from the function that builds `propFields` to every site that ultimately calls `childPropsLiteral` — i.e. parallel the existing `propFields map[string]map[string]bool` parameter through `genComponent`/`genChildComponent`/`emitProbes`/`buildSkeleton` (search for `propFields map[string]map[string]bool` signatures and add `nodeProps map[string]map[string]bool` beside each). Where `propFields` is constructed (the caller of the analyze function), construct and pass `nodeProps` too.
- [ ] **Step 3: Unit test** (`internal/codegen/analyze_test.go` or wherever `propFields` derivation is tested — search): assert that for `component Card(title gsx.Node, n int)`, the derived `nodeProps["CardProps"]` has `Title: true` and NOT `N`. If no such test file exists, add a focused one constructing a `*gsxast.Component` and checking the map (mirror how `propFields` is currently tested, if at all; else a minimal table on `isGsxNodeType`).
- [ ] **Step 4: Run** `go build ./...` (the threading compiles) and `go test ./...` (no behavior change yet — `nodeProps` is derived but unused). Green.
- [ ] **Step 5: Commit** `codegen: derive nodeProps (which declared props are gsx.Node), threaded alongside propFields`.

---

### Task 2: Promote a non-node value bound to a `gsx.Node` prop (the emit/probe callback)

**Files:** Modify `internal/codegen/emit.go` (`childPropsLiteral` ~1647; `genChildComponent` ~where it builds `slotValue`; the probe's `childPropsLiteral` call in `emitProbes`/`buildSkeleton`).

**Interfaces — Consumes:** `nodeProps` (Task 1); `emitRender` (`emit.go:823`); `resolved` + `classify` (for the value type). **Produces:** a `gsx.Node` prop receiving a `Static`/`Expr` non-node value emits a `gsx.Func` closure rendering it via §5; markup/node values unchanged.

**Design — the emit/probe asymmetry (mirror `slotValue`).** `childPropsLiteral` already takes a `slotValue` callback so emit and probe can supply different field VALUES for the same field set. Add a second callback `promoteValue func(expr string, static bool) (string, error)` (or extend the signature), invoked when a Static/Expr attr binds to a `nodeProps` field and is NOT already a node:
- **Emit form** (`genChildComponent` provides it): a real render closure —
  ```go
  gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {
      _gsxgw := gsx.W(_gsxw)
      <emitRender(expr, resolved[attr], imports) output>   // catString→gw.Text, catInt→gw.S(strconv...), etc.
      return _gsxgw.Err()
  })
  ```
  For a STATIC string attr the value is the quoted literal and the body is `_gsxgw.Text(<quoted>)` (string → `catString`).
- **Probe form** (`emitProbes` provides it): a typed-nil that STILL references the expr so its type resolves —
  ```go
  func() <rtPkg>.Node { _ = <expr>; return nil }()
  ```
  (`_ = <expr>` forces the value's type to resolve — emit needs `resolved[attr]` to pick the `emitRender` branch; without referencing it, the type would be unknown. For a static string, `_ = <quoted>` is a harmless no-op.) This satisfies `<rtPkg>.Node` and keeps emit ≡ probe on the resolved set.

**`childPropsLiteral` logic** (`emit.go:1647`): in the `*ast.StaticAttr` and `*ast.ExprAttr` cases, when `isPropField(declared, name)` is true AND `isPropField(nodeFields, fieldName(name))` is true (the field is `gsx.Node`):
- Static → always promote (a string literal → `catString`): `fields = append(..., fieldName(name)+": "+promoteValue(strconv.Quote(t.Value), true))`.
- Expr → if the value's `classify(resolved[attr])` is `catNode`/`catNodeSlice` already a node? `catNode` → pass as-is (today's behavior). Otherwise (a renderable scalar, or `catNodeSlice`) → `promoteValue(strings.TrimSpace(t.Expr), false)`. `catUnsupported` → return the same error `{ x }` gives (reuse the `emitRender` default-case error or `genInterp`'s unsupported message).
  - NOTE: `childPropsLiteral` must be able to see `classify(resolved[attr])` — thread `resolved` (and the attr node) in, OR push the category decision INTO `promoteValue` (cleaner: `promoteValue` is a closure over `resolved`/`imports` in `genChildComponent`, so it can `classify` and call `emitRender` itself, returning an error for `catUnsupported`; `childPropsLiteral` just decides "field is node + attr is not markup → call promoteValue"). PREFER pushing classify+emitRender into the emit `promoteValue` closure (keeps `childPropsLiteral` signature minimal). The probe `promoteValue` ignores the type and emits the `_ = expr` form.
- `catNode` passthrough: an expr already typed `gsx.Node` keeps `fieldName: <expr>` (don't wrap a node in a node).

**Wire**: `genChildComponent` builds the emit `promoteValue` (with `emitRender`/`resolved`/`imports`); `emitProbes`/`buildSkeleton` builds the probe `promoteValue` (the `_ = expr; return nil` form, under the `_gsxrt` alias). Pass `nodeProps[propsType]` (the node-field set for this child) into `childPropsLiteral`.

- [ ] **Step 1: Failing corpus** `internal/corpus/testdata/cases/slots/node_prop_promotion.txtar`: a `component Card(title gsx.Node, content gsx.Node)` invoked `<Card title="Card Title" content={ n }/>` (string + int), and a sibling `<Card title={ <span>x</span> } content={ someNode }/>` (markup + node passthrough). Pin `generated.x.go.golden` (shows the `gsx.Func(... gw.Text("Card Title") ...)` and `gsx.Func(... gw.S(strconv.Itoa(n)) ...)` wrappers, and the markup/node forms unchanged) + `render.golden`. (Model the case file on an existing `slots`/named-slot corpus case; check how to supply `n int` — a prop on the enclosing component.)
- [ ] **Step 2: Run** `go test ./internal/corpus/ -run 'TestCorpus/slots'` — FAIL (today: `cannot use "Card Title" (string) as gsx.Node`, a type-resolution diagnostic).
- [ ] **Step 3: Implement** the emit + probe `promoteValue` callbacks and the `childPropsLiteral` node-field promotion. Bump `internal/codegen/version.go`.
- [ ] **Step 4: Run** `go test ./internal/corpus/` then full `go test ./...`. Existing named-slot/`{children}`/child-props cases must stay green (markup/node values map exactly as before — only string/scalar→Node values change). Inspect the golden's closure shape.
- [ ] **Step 5: Commit** `codegen: promote renderable values to gsx.Node props (gsx.Func + §5 emitRender); emit≡probe via promoteValue callback`.

---

### Task 3: Escaping + unrenderable-type coverage + the user's file

**Files:** add corpus cases under `internal/corpus/testdata/cases/slots/`.

**Interfaces — Consumes:** Task 2's promotion. **Produces:** security + error + end-to-end coverage.

- [ ] **Step 1: Add cases.**
  - `node_prop_escaping.txtar`: `<Card title={ userStr }/>` with `userStr string` = `"<script>alert(1)</script>"` → `render.golden` shows it ESCAPED (`&lt;script&gt;…`) — proving the promoted closure's `gw.Text` escapes (the §5 path).
  - `node_prop_unrenderable.txtar`: a `gsx.Node` prop bound to an unrenderable value (e.g. a struct with no `String()`) → `diagnostics.golden` with the same clear error `{ x }` gives for that type.
  - `node_prop_realworld.txtar`: the user's two-`Card` shape (string title/content + markup title/content + children) → `render.golden` matching the verified output `<div class="card"><h2>…</h2><p>…</p>…children…</div>`.
- [ ] **Step 2: Run** `go test ./internal/corpus/ -run 'TestCorpus/slots'` — the escaping/unrenderable cases FAIL until pinned; regenerate goldens with the corpus `-update` flag and EYEBALL: escaping case shows `&lt;`, unrenderable shows the diagnostic, realworld renders correctly.
- [ ] **Step 3:** Regenerate the corpus coverage manifest (`-update` on the full `TestCorpus`) so `coverage.golden` reflects the new cases (avoid the count-mismatch failure).
- [ ] **Step 4: Run** full `go test ./...` green; `go vet ./...` clean.
- [ ] **Step 5: Commit** `codegen: node-prop promotion corpus — escaping, unrenderable error, real-world Card`.

---

## Self-Review

**Spec coverage:** §2 promotion rule → T2 (the per-category mapping in `childPropsLiteral` + `promoteValue`); §3 the field-type/emit≡probe crux → T1 (nodeProps) + T2 (the emit/probe `promoteValue`); §5 errors → T2 (catUnsupported) + T3 (unrenderable case); §6 tests → T2/T3 (string/int/markup/node + escaping + error + real-world); escaping parity → T3.

**Placeholder scan:** the hard parts have exact forms — the emit closure (`gsx.Func`+`emitRender`), the probe form (`func() Node { _ = expr; return nil }()`), the `nodeProps` derivation, and the `childPropsLiteral` decision. The "push classify into promoteValue" choice is specified with a rationale, not left open.

**Type consistency:** `nodeProps map[string]map[string]bool`, `isGsxNodeType`, `promoteValue func(expr string, static bool) (string, error)`, `emitRender`, `classify` — consistent across tasks; threaded parallel to the existing `propFields`/`slotValue`.

## Risks
- **emit ≡ probe** is the central risk: the probe form must reference the value expr (`_ = expr`) so its type resolves for the emit's `emitRender`, AND type-check as `gsx.Node`. The corpus harness compiles+runs the emit while the probe drove resolution — a mismatch surfaces as a build error in the renderable case. Verify with the `int` case (the probe must resolve `n`'s type).
- **Static-string promotion** is the simplest case (always `catString`/`gw.Text` of the quoted value) — implement it first to de-risk, then the expr/`emitRender` path.
- **`nodeProps` threading** is mechanical but wide — mirror every `propFields` signature exactly to keep emit and probe symmetric.
- **`isGsxNodeType` is a string match** (`"gsx.Node"`) — robust for the common import; a dot-import/alias of gsx would miss (documented limitation; the param type is author-written source, normally `gsx.Node`).
