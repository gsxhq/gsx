# Design: renderable values auto-promote to `gsx.Node` props (`gsx.Val`)

**Date:** 2026-06-23
**Status:** Approved (brainstorm), pending implementation plan

---

## 1. Goal & scope

A component prop typed **`gsx.Node`** should accept, from a `.gsx` caller, **any value gsx renders as text or markup** — not only `{ <markup/> }`:

```gsx
component Card(title gsx.Node, content gsx.Node) { <h2>{title}</h2><p>{content}</p> }

<Card title="Card Title"          content={ n } />        <!-- string / int -->
<Card title={ <span>Rich</span> } content={ someNode } /> <!-- markup / node -->
```

**Mechanism: a universal value box.** A new runtime `gsx.Val(v any) Node` wraps any value as a Node and renders it by a type switch (the §5 set). Codegen, when a non-node value is bound to a `gsx.Node` prop, emits `gsx.Val(expr)`. A *static string* attribute takes a free fast-path, `gsx.Text(literal)`, that skips the `any` box.

**Why a single `any` box, not per-type boxes or an inline closure:**
- It is **extensible** — supporting a new renderable type is one `case` in `Val`'s switch, not a new constructor + codegen branch.
- It **keeps codegen trivial and preserves emit ≡ probe for free**: `gsx.Val(expr)` needs no resolved type and type-checks identically in the type-check skeleton and the emitted code, so there is **no classify-at-codegen and no emit/probe asymmetry** (the hard part of the alternatives).
- Cost is **~2 small allocations per promoted prop** (box the value into `any`, box the node into the `gsx.Node` interface), once per render-of-that-prop — negligible against the hundreds of allocs a page already makes per element/attr/string. The static-string fast-path hits the **1-alloc floor** for the common literal-text case.

**Go stays strict.** A `string`/`int` is never a `gsx.Node` in Go — a hand-written `CardProps{Title: "x"}` still won't compile. Promotion happens only at the `.gsx`→Go boundary (codegen emits `gsx.Val(...)`). For hand-written Go callers, `gsx.Val`/`gsx.Text` are the explicit constructors (`CardProps{Title: gsx.Val(5)}`).

**In scope:** runtime `gsx.Val(any) Node` + `gsx.Text(string) Node`; the codegen rule mapping a non-node value bound to a `gsx.Node` prop to `gsx.Val(expr)` (static string → `gsx.Text`); tests.

**Out of scope:** per-type concrete boxes (`gsx.Int`/`gsx.Float`) — the universal box covers the cases and concrete types only pay off if gsx ever needs to *introspect* a node's type, which it doesn't (a `Node`'s only operation is `Render`). Nice number/float formatting is the **pipeline's** job (`{ f | money("$") }`), not `Val`'s — `Val` renders the plain Go default.

**Global constraints:** runtime stays stdlib-only. Threat model unchanged — `Val`'s string/Stringer rendering goes through the same `gw.Text` escaper as `{ x }`, so promoted text is escaped identically.

---

## 2. Runtime: `gsx.Val` + `gsx.Text`

```go
// Val wraps any renderable value as a Node, so a value can fill a gsx.Node prop.
// It renders v by type: a Node renders itself; string/[]byte/Stringer render as
// escaped text; the numeric and bool kinds render their plain Go form (use the
// |> pipeline for formatted output, e.g. { f | money("$") }). nil renders nothing.
func Val(v any) Node { return valNode{v} }

type valNode struct{ v any }
func (n valNode) Render(ctx context.Context, w io.Writer) error {
    if n.v == nil { return nil }
    switch t := n.v.(type) {
    case Node:           if t == nil { return nil }; return t.Render(ctx, w)
    case string:         /* gw.Text(t) */
    case []byte:         /* gw.Text(string(t)) */
    case fmt.Stringer:   /* gw.Text(t.String()) */
    case int, int8, int16, int32, int64:    /* gw.S(strconv.FormatInt(...)) */
    case uint, …:        /* gw.S(strconv.FormatUint(...)) */
    case float32, float64: /* gw.S(strconv.FormatFloat(..., 'g', -1, …)) */
    case bool:           /* gw.S(strconv.FormatBool(t)) */
    default:             /* a clear "unrenderable in a gsx.Node prop" error */
    }
    …
}

// Text is the escaped-text Node — the static-string fast-path (codegen emits it
// for a literal attribute) and a Go-side text constructor. One alloc, no any-box.
func Text(s string) Node { return textNode(s) }
type textNode string
func (t textNode) Render(_ context.Context, w io.Writer) error { /* gw.Text(string(t)) */ }
```

The numeric/bool cases mirror the §5 `emitRender` formatting so `gsx.Val(n)` and inline `{ n }` produce the same bytes. The `default` case errors at *render* time for a truly unrenderable type — but codegen also rejects it at *build* time (§4), so render-time default is a backstop.

---

## 3. Codegen rule

When a child-component attribute binds to a prop whose declared type is **`gsx.Node`** (the `nodeProps` signal, §4):

| Attr | Emits | Allocs |
|---|---|---|
| `title="literal"` (`StaticAttr`) | `Title: gsx.Text("literal")` | 1 |
| `content={ expr }` (`ExprAttr`, any value) | `Content: gsx.Val(expr)` | 2 (1 if `expr` is a pointer/interface) |
| `header={ <markup/> }` (`MarkupAttr`) | the slot closure — **unchanged** |
| a value already `gsx.Node` via `{ nodeVar }` | `gsx.Val(nodeVar)` — `Val`'s `case Node` delegates (a small over-wrap; rare, accepted to keep codegen classify-free) |
| unrenderable (`catUnsupported`) | the same build error `{ x }` gives (§4) |

**The whole win:** `gsx.Text(lit)` / `gsx.Val(expr)` are emitted **identically by both the emit and the type-check probe** — no `resolved` type needed, no `classify`, no emit/probe callback. `childPropsLiteral` only needs to know "is this field `gsx.Node`?" to choose `gsx.Text`/`gsx.Val` over a bare value. (A bool attribute bound to a `gsx.Node` prop → `gsx.Val(true)`, renders `true`; an edge case, consistent.)

Non-`gsx.Node` props are untouched — a `string` prop still takes a string, an `int` prop an int.

---

## 4. The one real implementation problem — the `nodeProps` signal

`childPropsLiteral` (`emit.go:1647`) today maps by attribute *kind* (static→quoted, expr→expr, markup→slot). To apply the rule it must know **each target field's declared type is `gsx.Node`**. Source it AST-derived, parallel to the existing `propFields` (field-name) map: in the same derivation loop (`analyze.go:146`), build `nodeProps[propsType][fieldName] = isGsxNodeType(p.typ)` where `isGsxNodeType(typ) == (strings.TrimSpace(typ) == "gsx.Node")`, and thread it to `childPropsLiteral` exactly as `propFields` is threaded. Because both emit (`genChildComponent`) and probe (`emitProbes`) call `childPropsLiteral` with the SAME `nodeProps`, and both emit the SAME `gsx.Val`/`gsx.Text`, emit ≡ probe holds with no extra machinery.

**Build-time error for an unrenderable value:** because both emit and probe wrap the value in `gsx.Val(...)`, the Go type-checker accepts any type at the boundary (`Val` takes `any`), so an unrenderable type would only fail at *render* (the `default` case). To keep the friendly *build-time* error, the probe/emit can additionally reference the expr in a `{ x }`-style position that `classify` checks — OR (simpler) accept the `Val` render-time error and document it. **Decision: accept the build-time looseness for v1** (any value compiles; a non-renderable one is caught by `Val`'s `default` returning a clear error at render). Revisit if a build-time guard is wanted. *(This is the one deliberate trade of the `any` box: `Val` is permissive by type.)*

---

## 5. Data flow

```
<Card title="Card Title" content={ n }>
  → childPropsLiteral, nodeProps["CardProps"]={Title,Content}:
      Title  is gsx.Node, static  → Title: gsx.Text("Card Title")
      Content is gsx.Node, expr   → Content: gsx.Val(n)
  → Card(CardProps{Title: gsx.Text("Card Title"), Content: gsx.Val(n), Children: <slot>})
identical in emit and probe.
```
Inside `Card`, `<h2>{title}</h2>` renders the Node via the existing `catNode` path (`gw.Node`, nil-safe).

---

## 6. Testing

- **Runtime unit** (`val_test.go`): `gsx.Val` renders each kind — `Val("a")`→`a`, `Val(5)`→`5`, `Val(3.14)`→`3.14`, `Val(true)`→`true`, `Val(someNode)`→the node's output, `Val(nil)`→``, a `Stringer`→its `String()`; **escaping**: `Val("<b>")`→`&lt;b&gt;`. `gsx.Text("<b>")`→`&lt;b&gt;`. Parity: `Val(n)` bytes == inline `{ n }` bytes for each scalar.
- **Corpus render**: the user's two-`Card` file — `<Card title="Card Title" content="…">…children…</Card>` (string), `<Card title={ <span>…</span> } content={ node }/>` (markup + node), `<Card content={ n }>` (int). Pin `generated.x.go.golden` (`Title: gsx.Text(...)`, `Content: gsx.Val(...)`, markup slot unchanged) + `render.golden`.
- **Escaping corpus**: `<Card title={ userStr }>` with hostile `userStr` → render escaped.
- **No-regression**: existing named-slot / `{children}` / child-props cases stay green (markup/node map exactly as before). Bump `internal/codegen/version.go`.
- `go test ./...` green; `go vet ./...` clean.

---

## 7. Risks
- **`nodeProps` threading** (the one real change) is mechanical but wide — mirror every `propFields` signature so emit and probe stay symmetric.
- **`isGsxNodeType` is a string match** (`"gsx.Node"`) — robust for the normal `import "github.com/gsxhq/gsx"`; a dot-import/alias would miss (documented; the param type is author-written source).
- **`Val` render/escaping parity with §5** — the numeric/string cases must format/escape identically to `emitRender`; the parity unit test pins it.
- **Build-time looseness** (§4) — an unrenderable value compiles and errors at render via `Val`'s `default`; accepted for v1.
- **Alloc cost** — ~2 per promoted prop (1 for static-string via `gsx.Text`); per-prop, not per-element; revisit with per-type fast-paths *behind the same API* only if profiling demands.

---

## 8. Decision record: `Val` box vs `classify`-at-emit specialization

**Status:** decided — keep the `Val` runtime box as shipped (2026-06-23). Recorded after a post-merge discussion that sharpened *why*, and corrected an overstatement in §1/§3.

**The correction.** §1 and §3 imply the prop expression's type is unavailable ("needs no resolved type", "classify-at-codegen [is] the hard part"). That is only true of the **probe (skeleton) pass**, which runs *before* type resolution. There are two passes: the probe is the *input* to `go/packages` resolution; the **emit pass runs after** and has the full `resolved map[ast.Node]types.Type` — the same map `emitRender` already uses to specialize inline `{ len(title) }` into `strconv.FormatInt(...)`. So for a concretely-typed prop expression like `content={ len(title) }`, the type (`int`) **is** known at emit. The earlier "we can't know the type" framing was imprecise.

**The alternative we did NOT take.** Because the type *is* known at emit, codegen could specialize the prop value exactly like `emitAttrValue`/`emitRender` do for attributes and interpolations — `int`→`gsx.Text(strconv.FormatInt(...))`, `string`→`gsx.Text(string(expr))`, `fmt.Stringer`→`gsx.Text(expr.String())`, `[]Node`→`gsx.Fragment(expr...)`, already-`Node`→raw `expr` — and emit **no `Val` box at all**. That alternative is strictly *more* capable in two ways: it rejects unrenderable types at **build time** (via `classify`'s `default`), and it handles **named scalar types** (`type Slug string`) correctly, because `classify` keys on the underlying type. Both are things the shipped `Val` does *not* do (see the named-scalar limitation documented on `Val` and in §4's build-time looseness).

**Why we kept `Val` anyway (the trade).**
- The **probe pass forces a Node-typed wrapper regardless**: the skeleton literal `CardProps{Title: <X>}` must type-check against the `gsx.Node` field *before* resolution exists, so `<X>` must accept an arbitrary expression yet be a `Node`. `gsx.Val(any) Node` is the natural such wrapper. Some box must exist on the probe side; that is not optional.
- Given the probe already needs `Val(expr)`, emitting the *same* thing on the emit side buys **one uniform path**: no need to (a) thread/harvest each prop expression's resolved type (today prop-literal exprs are deliberately *not* type-harvested — `Val` taking `any` is exactly why they needn't be), (b) add a `classify`+Node-wrap branch to `childPropsLiteral`, or (c) maintain an emit-value-vs-probe-value asymmetry in the shared literal builder. The classify alternative is more machinery and reintroduces the kind of emit/probe value divergence the shared builder was written to avoid.
- **One case where `Val` is genuinely irreducible:** an expression whose **static type is `any`** (or a non-`Node`, non-`Stringer` interface). There the static type says nothing about renderability — only the runtime type does. `classify(interface{})`→unsupported, but `gsx.Val(x)` switches on the dynamic type and renders it. So a runtime box is unavoidable for genuinely dynamic values; `Val` covers them uniformly.

**The cost we accept by keeping `Val`:** ~2 allocs per promoted prop; render-time (not build-time) error for an unrenderable type; and named scalar types (`type Slug string`) hit `Val`'s `default` rather than rendering — even though inline `{ slug }` renders fine (documented on `Val`'s doc-comment).

**Future option (not scheduled):** a hybrid — `classify`-specialize at emit for concrete renderable types (gaining build-time strictness + named-scalar support + zero box), falling back to `gsx.Val` only when the static type is `any`/a non-concrete interface. This needs prop-expression type harvesting in the probe and a `classify` branch in the emit path; weigh it in its own spec if the limitations above start to bite.
