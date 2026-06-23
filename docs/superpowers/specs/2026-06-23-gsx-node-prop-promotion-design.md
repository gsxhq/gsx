# Design: renderable values auto-promote to `gsx.Node` props

**Date:** 2026-06-23
**Status:** Approved (brainstorm), pending implementation plan

---

## 1. Goal & scope

A component prop typed **`gsx.Node`** should accept, from a `.gsx` caller, **any value gsx already renders as text or markup** — not only `{ <markup/> }`. So:

```gsx
component Card(title gsx.Node, content gsx.Node) { <h2>{title}</h2><p>{content}</p> }

<Card title="Card Title"            content={ count } />      <!-- string / int -->
<Card title={ <span>Rich</span> }   content={ someNode } />   <!-- markup / node -->
```

both compile and render. This is the React-`ReactNode` ergonomic: a `gsx.Node` prop is a "text-or-markup" slot.

**Key principle (the whole design):** `gsx.Node` is just `Render(ctx, w) error` — anything renderable is a Node. gsx's `{ x }` interpolation (§5) already renders the full scalar set as escaped text. So promotion = **wrap the value's existing §5 rendering in a `gsx.Func` closure**; the closure satisfies `Render`, so it *is* a `gsx.Node`. No per-type runtime helper, no reflection — reuse the codegen we have.

**Go stays strict.** A `string`/`int` never *is* a `gsx.Node` in Go (a hand-written `CardProps{Title: "x"}` still won't compile). The promotion happens only at the **`.gsx` → Go boundary**, in the child-props codegen.

**In scope:** the codegen rule that wraps a renderable non-node attribute value bound to a `gsx.Node` prop in a `gsx.Func` closure rendering it via §5; for every §5 category. Tests.

**Out of scope:** the `gsx.Text` / `gsx.Group` runtime constructors (optional Go-side conveniences — a separate, tiny add if wanted; the `.gsx` ergonomic does NOT depend on them). Changing the `gsx.Node` field type or introducing a union type (explicitly rejected — Go strictness is desirable).

**Global constraints:** runtime stdlib-only (unchanged — this is codegen-only). No regression to existing child-props / slot behavior. Escaping is preserved (the wrapped §5 render does the same escaping as inline `{ x }`).

---

## 2. The promotion rule

When a child-component attribute binds to a prop whose declared type is **`gsx.Node`**, map the value by its §5 `classify` category (`analyze.go:686`):

| Value | category | Emitted field value |
|---|---|---|
| markup `{ <…/> }` (a `MarkupAttr`) | — | the slot closure (`emitSlotClosure`) — **unchanged** |
| a `gsx.Node` value | `catNode` | the expr as-is — **unchanged** |
| a `[]gsx.Node` value | `catNodeSlice` | a `gsx.Func` closure rendering each (the `catNodeSlice` branch of `emitRender`) |
| `string` / `[]byte` / `int*` / `uint*` / `float*` / `bool` / `fmt.Stringer` | `catString`/`catBytes`/`catInt`/`catUint`/`catFloat`/`catBool`/`catStringer` | **a `gsx.Func` closure that renders the value via `emitRender`** ← the new behavior |
| anything else | `catUnsupported` | the same friendly error `{ x }` gives for an unrenderable value |

A static string attribute (`title="Card Title"`) is the `catString` case (the value is a string literal). `gsx.Raw(...)` is already a `gsx.Node` (`catNode`) — unchanged.

The emitted closure has the canonical shape (same as `emitSlotClosure`, but the body is the §5 render of the value rather than markup):
```go
gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {
    _gsxgw := gsx.W(_gsxw)
    <emitRender output for the value, e.g. _gsxgw.Text(string(title)) / _gsxgw.S(strconv.Itoa(count))>
    return _gsxgw.Err()
})
```

---

## 3. Architecture & the one real implementation problem

The promotion lives in **`childPropsLiteral`** (`emit.go:1647`) — which builds the `CardProps{Title: …, Content: …}` field list — and reuses **`emitRender`** (`emit.go:823`, the §5 per-category emitter) inside an `emitSlotClosure`-style (`emit.go:1609`) `gsx.Func` wrapper.

**The crux:** `childPropsLiteral` must know **each target field's declared type** (is it `gsx.Node`?) to decide whether to wrap. Today it maps by attribute *kind* (markup→slot, expr→value, static→string), not by field type. So the work is:

1. **Thread the child component's prop types to the mapping site.** The child component's prop declarations (`component Card(title gsx.Node, …)`) give each field's type. Source it from the same place the props-field set (`propFields` / `childPropsFields`) is derived — extend it from field *names* to field *names→types* (or a `name→isNode` predicate, since only "is this field `gsx.Node`" matters for the rule). The probe/skeleton already type-resolves the child invocation, so the type is available; the emit side must carry it. Determine the cleanest carrier (AST param types of the child component, vs the resolved props type) during planning — both emit and probe must agree (the emit ≡ probe invariant).
2. **In `childPropsLiteral`,** for an attr bound to a `gsx.Node` field: if the value is already a node/markup → today's path; else if `classify(value)` is a renderable scalar/slice → emit the `gsx.Func`-closure-with-`emitRender`; else → the catUnsupported error.
3. **emit ≡ probe:** whatever drives the promotion decision (field-is-Node + value category) must be identical in the probe (`buildSkeleton`/`emitProbes`) and the emit, so the type-check and the generated code agree (the standing gsx invariant). If the probe emits the child call differently, align it.

---

## 4. Data flow

```
<Card title="Card Title" content={ count }>
  → childPropsLiteral, with child-prop types known:
      Title  field is gsx.Node, value "Card Title" is catString
         → Title: gsx.Func(func(ctx,w){ gw:=gsx.W(w); gw.Text("Card Title"); return gw.Err() })
      Content field is gsx.Node, value count is catInt
         → Content: gsx.Func(func(ctx,w){ gw:=gsx.W(w); gw.S(strconv.Itoa(count)); return gw.Err() })
  → Card(CardProps{Title: <closure>, Content: <closure>, Children: <slot closure>})
```

Inside `Card`, `<h2>{title}</h2>` renders the closure via the existing `catNode` path (`gw.Node`, nil-safe) — unchanged.

---

## 5. Error handling

- A value bound to a `gsx.Node` prop whose type is `catUnsupported` (e.g. a struct with no `String()`) → the same clear codegen error the `{ x }` interpolation already emits for that type, naming the attribute.
- A `gsx.Node` prop left unset → the field is the zero `gsx.Node` (nil); rendering a nil node is a no-op (existing `gw.Node` nil-safety).
- Non-`gsx.Node` props are unaffected (a `string` prop still takes a string; an `int` prop an int) — the promotion is gated on the field being `gsx.Node`.

---

## 6. Testing

- **Corpus render** (the real surface): the user's two-`Card` file —
  - `<Card title="Card Title" content="…">…children…</Card>` (string → text node),
  - `<Card title={ <span>…</span> } content={ <em>…</em> }/>` (markup, unchanged),
  - a `<Card title={ n }>` with `n int` (int → text node),
  - a `<Card title={ node }>` with a `gsx.Node`-typed value (passthrough),
  - escaping check: `<Card title={ userStr }>` with hostile `userStr` → rendered escaped (the closure's `gw.Text` escapes).
  Pin `generated.x.go.golden` (shows the `gsx.Func(... emitRender ...)` wrapper) + `render.golden`.
- **Error case:** a `gsx.Node` prop bound to an unrenderable type → `diagnostics.golden`.
- **No-regression:** existing named-slot / `{children}` / child-props corpus stays green (markup and node values map exactly as before). Bump `internal/codegen/version.go`.
- `go test ./...` green; `go vet ./...` clean.

---

## 7. Risks

- **Threading field types to `childPropsLiteral`** while preserving the **emit ≡ probe** invariant is the main risk — the plan must source the field-is-`gsx.Node` signal from a place both probe and emit read identically (as the existing `childPropsFields` already does for field *names*).
- **Escaping parity** — the wrapped render must use the SAME `emitRender` the inline `{ x }` uses, so a promoted string/Stringer is escaped identically; a render corpus case with a hostile value proves it.
- **`catNodeSlice` into a single `gsx.Node` prop** — wrap via the `emitRender` `catNodeSlice` branch (renders each); confirm that branch is closure-safe.
- **Optional `gsx.Text`/`gsx.Group`** are explicitly out — revisit only if hand-written Go callers need them; the `.gsx` ergonomic is independent.
