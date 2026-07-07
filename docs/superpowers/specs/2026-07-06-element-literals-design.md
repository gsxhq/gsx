# Element literals — `<tag>` as a value anywhere in a `.gsx` file

**Status:** design · **Date:** 2026-07-06

## Idea

Let a `<tag>…</tag>` expression appear in **Go expression position** inside a
`.gsx` file, evaluating to a `gsx.Node`. Today tag syntax lives only inside
`component Foo() { … }` bodies, so any markup you want as a *value* forces a
throwaway `component` declaration (or an unsafe `gsx.Raw` string).

```go
// build a node inline and hand it to structpages — no throwaway component
return structpages.RenderComponent(<TopContributors tabs={tabs} month={month}/>)

// a one-off row, as a value
var help = <a href="/help" class="text-blue-600">?</a>   // help is gsx.Node

// nav-item icon: a baked element, class is constant so no injection needed
{label: "Dashboard", icon: <HomeIcon class="w-5 h-5"/>}  // icon is gsx.Node
```

**No type-structure change.** `gsx.Component`, `FooProps`, generics, the
props-struct architecture — all untouched. This is purely a parser + codegen
addition that produces a `gsx.Node` value.

## Why (the real use cases)

The value is **removing throwaway single-use `component` declarations** and
letting markup be an ordinary Go value where one is already expected:

- **One-off components** — one-learning has 575 components, many declared only to
  be rendered once. Inline the markup at the use site instead of declaring
  `component Foo() {…}` and calling it.
- **structpages render targets** — ~93 non-test `RenderComponent` / `RenderTarget`
  sites build a node to hand to the framework; `RenderComponent(<Foo …/>)` lets
  that node be written inline.
- **Playground** — the render expression can itself be a `<tag>` form, so a
  snippet can produce markup without wrapping it in a component.
- **Nav-item icons** (the original driver) — `icon: <HomeIcon class="w-5 h-5"/>`
  as a baked `gsx.Node`. The class is *constant* across nav, so no render-site
  injection is needed — which is exactly why the component-value machinery (below)
  can be deferred.

(Note: the `gsx.Raw("<…>")` markup sites are **test-only** — 2 non-test vs 16 in
tests — so replacing `gsx.Raw` is a minor bonus, not the motivation.)

## Semantics

- A `<tag>` expression evaluates to `gsx.Node`. **Baked** — it's an *Element*, the
  result of applying a tag/component; render-site attrs do not apply (see model).
- Works for **HTML tags** (`<div/>`) and **component tags**
  (`<Button label="hi"/>` → `Button(ButtonProps{Label:"hi"})`).
- Inside the tag: nesting, `{ }` interpolation, children, attrs, pipes — same as a
  component body.

### Contexts (all inside `.gsx` files)

`var x = <…>` · struct-literal field `F: <…>` · **call arg** `RenderComponent(<…/>)` ·
`return <…>` · slice/map elements · playground top-level render expression.

## Model: Component vs Element (why attrs don't inject)

| Form | Is a | Type | Attrs at render? |
|---|---|---|:--:|
| `Button` (bare name) | Component | `func(…) gsx.Node` | yes |
| `<Button …/>` (tag) | **Element** | `gsx.Node` | **no — baked** |

`<tag>` always yields an Element. This is the JSX distinction (`Button` vs
`<Button/>`), and it's why an element literal is a plain `gsx.Node`.

## The crux: parser ambiguity

The hard part. Inside a component body gsx is already in "tag mode"; in Go
expression position `<` is ambiguous — `a < b`, `<-ch`, `<<`. The fix is the
standard JSX rule: **`<` in expression-start position begins a tag; `<` in infix
position is less-than.** gsx must scan Go chunks well enough to know which
position it's in (a light Go-expression awareness, not a full parse).

- Detect `<Ident`, `</`, `<>` at an operand-start boundary → element literal.
- Everywhere else `<` keeps its Go meaning.

This is the main design/implementation risk and where most effort goes.

## Codegen (mostly exists)

A `<tag>` expression compiles to a self-contained node:

```go
gsx.Func(func(ctx context.Context, w io.Writer) error { /* …write tag… */ })
```

gsx **already** emits exactly this for component bodies (`return gsx.Func(func…)`),
so the element-writing machinery is reused — the difference is emitting it as an
inline expression value rather than a function body. Composite `<Button …/>`
reuses the existing call-site / `childPropsLiteral` path.

The type-check probe (`analyze.go`) must also accept tags-in-expression so
`go/types` validates them (props, interpolated expressions).

## Scope / effort

- **Parser** — tag-in-expression detection + expression-start disambiguation. The
  bulk of the work; the ambiguity is the risk.
- **Codegen** — modest: reuse body element-emission as an inline `gsx.Node`.
- **Probe** — `analyze.go` accepts the new position for type-checking.
- **Corpus** — a txtar case per context (var, field, arg, return), plus the
  Component/Element distinction.

## Adoption

Opt-in and non-breaking: existing `component` declarations and `gsx.Raw` stay
valid; inline markup where a throwaway component or a node value is wanted.

---

## Deferred: component values (`gsx.Component`)

Parked. Its one real use — a stored component you feed **render-site attributes**
to — turns out to be narrow: the nav-icon driver needs only a *constant* class, so
a baked element literal (`icon: <HomeIcon class="w-5 h-5"/>`, above) already covers
it. Component values only earn their keep when the attrs must vary per render site,
which is rare. Recorded so we don't re-derive it.

- **Type:** `type Component = func(...gsx.Attr) gsx.Node`. A component is a thing
  that takes attributes and returns a node.
- **Collapse (the core change):** components with **no typed params** would
  generate `func Foo(...gsx.Attr) gsx.Node` (no `FooProps`); children ride as a
  reserved `gsx.Child` attr. Brings icons, no-arg, and wrappers into one type.
- **Typed-props** join via a **binding closure** —
  `func(a ...gsx.Attr) gsx.Node { return Badge(BadgeProps{Count: 12, Attrs: a}) }`.
- **Interim, zero-compiler-change:** the binding closure + a userland
  `type Component = func(...gsx.Attr) gsx.Node` alias already solve the nav case
  today. That's why Phase 1 can wait.
- **Rejected:** `type Component any` (not invokable; defers the typed-field wall to
  runtime), `Component[T]` (distinct `T` doesn't unify storage), and erasing every
  component to the attr bag (guts the `go/types` prop-check, breaks generic
  inference, boxes every scalar prop — the doors it opens are already opened by the
  binding closure).
