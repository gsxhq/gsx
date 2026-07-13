# Syntax reference

Use this page to look up `.gsx` syntax. If you are new to gsx, follow
[Learn gsx](./learn.md) first for a guided path.

## Start here

1. [Basic syntax](./syntax/basic-syntax.md) — files, imports, and component declarations
2. [Elements](./syntax/elements.md) — tags, nesting, void elements, and documents
3. [Interpolation](./syntax/interpolation.md) — Go expressions and interpolated literals
4. [Attributes](./syntax/attributes.md) — static, dynamic, conditional, and spread attributes
5. [Control flow](./syntax/control-flow.md) — `if`, `for`/`range`, and `switch`
6. [Composition](./syntax/composition.md) — component calls, children, slots, and forwarding
7. [Props](./syntax/props.md) — bring-your-own structs and generated props

## More topics

- **Styling and scripts:** [Styling](./syntax/styling.md), [JavaScript](./syntax/javascript.md), and [Forms](./syntax/forms.md)
- **Security and raw output:** [Escaping](./syntax/escaping.md) and [Raw HTML](./syntax/raw-html.md)
- **Go, context, interop, and runtime helpers:** [Raw Go](./syntax/raw-go.md), [Comments](./syntax/comments.md), [Fragments](./syntax/fragments.md), [Pipelines](./syntax/pipelines.md), [Context](./syntax/context.md), [Interop](./syntax/interop.md), and [Runtime helpers](./syntax/std-functions.md)

## Build constraints and `//go:` directives

File-level Go directives before the `package` clause apply to the generated Go
file. The Go toolchain, not gsx, evaluates build constraints; see the Go
documentation for [build constraints](https://pkg.go.dev/cmd/go#hdr-Build_constraints).

Build-tag variants may declare the same component when their constraints are
mutually exclusive and their signatures match:

```gsx
// platform_linux.gsx
//go:build linux

package views

component PlatformName() {
	<span>Linux</span>
}

// platform_other.gsx
//go:build !linux

package views

component PlatformName() {
	<span>Other</span>
}
```

Generation processes both files; `go build` selects the matching variant.

## Quick reference

### Declarations

| Form | Meaning |
|---|---|
| `component Card(title string) { … }` | [Component declaration](./syntax/basic-syntax.md#declare-a-component) |
| `component List[T any](items []T) { … }` | [Generic component](./syntax/composition.md#generic-components) |
| `component (p Page) Header() { … }` | [Method component](./syntax/composition.md#method-components) |

### Body forms

::: v-pre
| Form | Meaning |
|---|---|
| `<div>…</div>` | [Element](./syntax/elements.md#tags-and-nesting) |
| `{ expr }` | [Escaped Go expression](./syntax/interpolation.md#go-expressions) |
| `{ if … }`, `{ for … }`, `{ switch … }` | [Control flow](./syntax/control-flow.md) |
| `{{ stmt }}` | [GoBlock](./syntax/raw-go.md#goblock) |
| `<>…</>` | [Fragment](./syntax/fragments.md#multiple-roots) |
| `{/* … */}` | [Content comment](./syntax/comments.md) |
| `{ value |> filter }` | [Pipeline](./syntax/pipelines.md#chain-filters) |
:::

### Attribute forms

::: v-pre
| Form | Meaning |
|---|---|
| `name="value"` | [Static attribute](./syntax/attributes.md) |
| `name={expr}` | [Expression attribute](./syntax/attributes.md#expression-attributes) |
| `disabled`, `disabled={cond}` | [Boolean attribute](./syntax/attributes.md#boolean-attributes) |
| `{ if cond { name="value" } }` | [Conditional attributes](./syntax/attributes.md#conditional-attributes) |
| `{ attrs... }` | [Attribute spread](./syntax/attributes.md#spread-x-—-ordered) |
| `attrs={{ "name": value }}` | [Ordered attribute literal](./syntax/attributes.md#ordered-attrs-literal) |
| `` name=f`prefix-@{value}` `` | [Interpolated literal](./syntax/attributes.md#interpolating-attribute-literals) |
| `class={ … }`, `style={ … }` | [Composable styling](./syntax/styling.md) |
| `` @click=js`save(@{id})` `` | [JavaScript attribute](./syntax/javascript.md#javascript-valued-attributes) |
:::

### Component forms

| Form | Meaning |
|---|---|
| `<Card title="Hi"/>` | [Component call](./syntax/composition.md#calling-components) |
| `<ui.Button/>` | [Cross-package call](./syntax/composition.md#cross-file-and-cross-package-calls) |
| `<List[string] items={items}/>` | [Explicit type arguments](./syntax/composition.md#explicit-type-arguments) |
| `<Card>…</Card>` | [Nested children](./syntax/composition.md#children-children) |
| `header={ <h2>Title</h2> }` | [Named slot](./syntax/composition.md#named-slots) |
| `{children}` | [Children placement](./syntax/composition.md#children-children) |
| `<Inner { attrs... }/>` | [Attribute forwarding](./syntax/composition.md#forwarding-through-components) |
| `<Card { props... }/>` | [Whole-struct splat](./syntax/props.md#whole-struct-splat) |

> **Status — alpha.** Check [Status](./status.md) and the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) before relying
> on deferred features.
