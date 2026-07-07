# Fragment literals in Go-expression position — `<>…</>` as a value

**Status:** design · **Date:** 2026-07-07

## Idea

Let a `<>…</>` fragment appear in **Go expression position** inside a `.gsx`
file, evaluating to a `gsx.Node`. Fragments already work in **markup position**
(component bodies, nested children); the element-literals work (PR #42)
deliberately errored them in expression position as a v1 boundary. This closes
that boundary: `<>…</>` becomes valid **everywhere** `<tag>` is, and is the
JSX-idiomatic way to produce *a list of sibling nodes as one value* — including
the empty fragment `<></>` as the render-nothing nop (the `templ.NopComponent`
equivalent).

```go
// a list of elements as one gsx.Node value — no wrapper element in the output
var items gsx.Node = <>{ for _, x := range xs { <Item x={x}/> } }</>

// multi-node value handed to a framework inline
return structpages.RenderComponent(<><Header/><Body/></>)

// the nop: an icon slot that renders nothing
{ label: "Home", icon: <></> }
```

**No type-structure change.** This is purely a parser + codegen addition that
produces a `gsx.Node` value, mirroring element literals exactly.

## What already exists (and what does not)

Fragments are **not new** — only their use as a *value* is.

- **Markup position — works today.** `parser/markup.go:709` produces
  `*ast.Fragment`; `emit.go` renders its children in sequence with no wrapper
  (control-flow children included). Corpus: `elements/fragment.txtar`,
  `parser/05_fragment.txtar`.
  ```gsx
  component Pair(a string, b string) {
      <><span>{a}</span><span>{b}</span></>   // → <span>x</span><span>y</span>
  }
  ```
- **Runtime target — exists today.** `gsx.Fragment(nodes ...Node) Node`
  (`val.go:98`) renders each child in order; `Fragment()` renders nothing. Its
  doc comment already names it "the lowering target for a future `<>…</>`
  syntax."
- **Detection — exists today.** `parser/goexpr.go`'s `scanGoElementMarks`
  already *detects* the `<>` fragment-open mark at an operand position; the
  split path (`goexpr.go:292`) deliberately turns it into the error
  *"a fragment (<>...</>) literal is not supported as a Go expression value
  here"* and preserves the consumed bytes.

So the feature is: **turn that one deliberate error into a real lowering.** The
detection half is already built; this is the direct mirror of how `<tag>` was
lifted into expression position.

## Semantics

- A `<>…</>` expression evaluates to `gsx.Node`. It renders each child in order
  with **no wrapper element** in the output.
- **Empty `<></>`** renders nothing — the nop / `templ.NopComponent` equivalent,
  equal to `gsx.Fragment()`.
- Inside a fragment: text, `{ }` interpolation, nested elements/fragments,
  control flow (`{ if }`, `{ for }`), pipes — the same child grammar a component
  body allows (inherited from the existing markup-position fragment parser).
- **No attributes.** `<class="x">…</>` is illegal. `<>` has no attribute slot;
  the grammar already enforces this (`<` immediately followed by `>` is a
  fragment open, with no place to write attrs).

### Contexts (all inside `.gsx` files, Go-expression position)

Every position element literals reach: `var x = <>…</>` · struct-literal field
`F: <>…</>` · call arg `RenderComponent(<>…</>)` · `return <>…</>` · slice/map
elements · playground top-level render expression.

### Rule: explicit wrapping required (JSX-faithful)

A bare adjacent sequence in expression position — `var x = <A/><B/>` — stays
**illegal**. Multiple sibling nodes as one value MUST be wrapped: `<><A/><B/></>`.
This matches JSX and avoids ambiguity for the operand/operator scanner (a second
`<A` after a complete `<B/>` operand is in operator position, so it is not
retokenized as a tag). Element literals already enforce single-root; this rule
is the fragment analog.

## Model: fragment is an Element-less group

A fragment is **not** a component or an element — it applies no tag and injects
no attrs. It is a grouping of children. As a value it is an ordinary `gsx.Node`,
exactly like an element literal. There is no "Component vs Element" distinction
to make here: `<>…</>` has no name to be bare-vs-tagged about.

## Codegen (reuses element-literal machinery)

A `<>…</>` expression lowers to the same self-contained node an element literal
uses — an inline render closure over the child sequence:

```go
gsx.Func(func(ctx context.Context, w io.Writer) error { /* …render each child… */ })
```

- The child-sequence emission is exactly `emitNodeFuncBody` — the shared body
  already used by `genComponent` and element values. A fragment's children feed
  it directly (no wrapper open/close tag).
- **Empty `<></>`** lowers to the uniform no-op closure
  (`gsx.Func(func(ctx, w) error { return nil })`), NOT a special-cased
  `gsx.Fragment()`. Rationale: one lowering path for all fragments; the empty
  case is just a closure with no writes, and renders identically. (Decision
  recorded so we don't re-litigate: uniform closure over `gsx.Fragment()`.)
- The type-check probe (`analyze.go`) mirrors it with the **inline
  scope-capturing IIFE** element literals introduced, so interps inside a
  fragment (`{ x }`, `{ for … }`) resolve against the enclosing func's
  params/locals/receiver (emit ≡ probe).

## AST

`*ast.Fragment` gains membership in the `GoPart` sealed interface (alongside
`GoText` and `*ast.Element`), so it can ride inside `ast.GoWithElements`. This is
the only AST change — the `Fragment` node type itself already exists and is
unchanged.

## Scope / effort

- **Parser (`goexpr.go`)** — replace the fragment-mark error with: parse the
  `<>…</>` span via the existing markup parser and emit an `*ast.Fragment` part.
  The scan/detection already exists.
- **AST** — add `*ast.Fragment` to `GoPart`.
- **Codegen** — `emit.go`: lower a fragment part to the inline `gsx.Func` child
  sequence. `analyze.go`: mirror with the scope-capturing IIFE.
- **Printer / wsnorm** — handle `*ast.Fragment` inside `GoWithElements`
  (both already handle `*ast.Element` there; fragment reuses `p.fragment` /
  the existing markup-fragment printer).
- **Corpus** — a txtar case per expression context (var, return, call-arg,
  struct-field, nested-in-element-literal), plus the empty-fragment nop and a
  loop-built list. Regenerate `coverage.golden`.
- **Docs** — extend the "Elements as values" guide section with fragments;
  document `<></>` as the nop.
- **Sibling grammars** — tree-sitter-gsx, vscode-gsx, gsxhq.github.io
  (CodeMirror + VitePress) already highlight `<>` in markup mode; verify
  expression-position highlighting and update if the grammar gates fragments to
  body context.

## Adoption

Opt-in and non-breaking. Existing markup-position fragments, element literals,
`gsx.Fragment(...)`, and `component` declarations all stay valid. The new
capability is only *additive*: `<>…</>` now also works where a `gsx.Node` value
is expected.

## Out of scope

- **Keyed fragments / fragment attrs** — fragments carry no attributes, keys, or
  identity. Not a React reconciler; nothing to key.
- **Bare adjacent sequences** — `<A/><B/>` without a wrapper stays illegal (see
  rule above).
- **Component-value machinery** (`gsx.Component`, attr-only collapse) — remains
  deferred; independent of this feature.
