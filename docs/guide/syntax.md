# Syntax reference

> **Syntax is alpha.** This page is the compact reference and
> topic hub. If you are new to gsx, start with [Learn gsx](./learn), then use this
> page while writing templates.

A `.gsx` file is ordinary Go (package, imports, types, funcs) plus `component`
declarations. A component has a templ-style header and a JSX-style body — the
markup *is* the result, so there is no return type and no `return`:

```gsx
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
	</section>
}
```

## Topics

Each per-topic page goes deeper with runnable examples sourced from
golden-tested `examples/*.txtar` fixtures.

::: v-pre
| Page | What it covers |
|------|----------------|
| [Basic syntax](./syntax/basic-syntax) | Component declarations, elements vs components, method components |
| [Raw Go](./syntax/raw-go) | `{{ stmt }}` Go statement blocks |
| [Elements](./syntax/elements) | Tags, void elements, raw-text (`<script>`/`<style>`), DOCTYPE |
| [Comments](./syntax/comments) | Content comments `{/* … */}` and HTML comments |
| [Fragments](./syntax/fragments) | `<>…</>` wrapper-free grouping |
| [Interpolation](./syntax/interpolation) | `{ expr }` value embedding and `(T, error)` auto-unwrap |
| [Attributes](./syntax/attributes) | Expression, boolean, conditional, spread, and ordered attributes |
| [Control flow](./syntax/control-flow) | `{ if }`, `{ for }`, `{ switch }` |
| [Components & composition](./syntax/composition) | Invoking components, `{children}`, named slots, explicit attribute forwarding |
| [Props model](./syntax/props) | Bring-your-own struct, generated props, whole-struct splat |
| [Styling](./syntax/styling) | Composable `class`/`style`, style blocks, class merger |
| [JavaScript](./syntax/javascript) | `@{ expr }` in `<script>`, `` js`...` `` attribute literals, data islands |
| [Pipelines](./syntax/pipelines) | `\|>` filter pipelines and `std` filters |
| [Rendering raw HTML](./syntax/raw-html) | `gsx.Raw` — bypass escaping for trusted HTML strings |
| [Escaping](./syntax/escaping) | Context-aware auto-escaping and opt-out helpers |
| [Context](./syntax/context) | `context.Context` threading through render |
| [Standard functions](./syntax/std-functions) | Built-in filter functions |
| [Interop](./syntax/interop) | Using gsx components from plain Go |
| [Forms](./syntax/forms) | Form elements and helpers |
:::

## Quick reference

::: v-pre
| Form | Meaning |
|------|---------|
| `component X(params) { … }` | component declaration (emission body — no return) |
| `component X[T constraint](params) { … }` | generic component declaration |
| `component (p T) Name(params) { … }` | method component (receiver) |
| `component (p T) Name[U constraint](params) { … }` | generic method component; requires a go1.27+ toolchain — older toolchains report `error[unsupported-toolchain]` for the component and generation continues |
| `<div>`, `<el-dialog>` | HTML element (lowercase / hyphenated) |
| `<Card>`, `<ui.Button>` | component (Capitalized / dotted) |
| `<Card[T]>`, `<ui.Button[T]>`, `<p.Row[T]>` | explicit type arguments for a generic component call |
| `{ expr }` | interpolation in body (auto HTML-escaped) |
| any expression returning `(T, error)` | auto-unwraps to `T`; error propagates from the enclosing `Render` — no marker needed, applies in all expression positions (text, attrs, child-prop values, `{{ }}` pair values, pipelines) |
| `name="lit"` | static string attribute |
| `name={ expr }` | dynamic attribute (Go expression) |
| `name` (bare) | boolean attribute = `true` |
| `disabled={ cond }` | type-driven boolean attr (bool → bare/omitted) |
| `{ expr... }` | spread/splat — on an **element**: spreads `gsx.Attrs` as HTML attrs; on a **component**: whole-struct splat (passes the prebuilt struct as props) |
| `name={{ "k": v, "k2": v2 }}` | ordered-attrs literal — binds to a `gsx.Attrs` prop; renders in source order |
| `{ if … }` / `{ for … }` inside a tag | conditional attributes |
| `{ if/for/switch … { <markup> } }` | control flow contributing children |
| `{{ stmt }}` | Go statement escape hatch (no output) |
| `<>…</>` | fragment |
| `class={ a, "cls": cond }` | composable `class`/`style` (comma list; conditional sugar) |
| `class={ a, switch x { case A: "b" default: "c" } }` | value-form `if`/`switch` inside `class`/`style` — exclusive selection |
| `{children}` | explicit children placement |
| `gsx.Raw(s)` | unescaped HTML |
:::

> **Status — alpha.** Follow [Status](./status.md) and the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) before relying
> on deferred features.
