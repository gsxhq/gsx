# Syntax reference

> **Syntax is alpha.** This page is the compact reference and
> topic hub. If you are new to gsx, start with [Learn gsx](./learn.md), then use this
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
| [Basic syntax](./syntax/basic-syntax.md) | Component declarations, elements vs components, method components |
| [Raw Go](./syntax/raw-go.md) | `{{ stmt }}` Go statement blocks |
| [Elements](./syntax/elements.md) | Tags, void elements, raw-text (`<script>`/`<style>`), DOCTYPE, elements as values in Go expression position |
| [Comments](./syntax/comments.md) | Content comments `{/* … */}` and HTML comments |
| [Fragments](./syntax/fragments.md) | `<>…</>` wrapper-free grouping |
| [Interpolation](./syntax/interpolation.md) | `{ expr }` value embedding and `(T, error)` auto-unwrap |
| [Attributes](./syntax/attributes.md) | Expression, boolean, conditional, spread, and ordered attributes |
| [Control flow](./syntax/control-flow.md) | `{ if }`, `{ for }`, `{ switch }` |
| [Components & composition](./syntax/composition.md) | Invoking components, `{children}`, named slots, explicit attribute forwarding |
| [Props model](./syntax/props.md) | Bring-your-own struct, generated props, whole-struct splat |
| [Styling](./syntax/styling.md) | Composable `class`/`style`, style blocks, class merger |
| [JavaScript](./syntax/javascript.md) | `@{ expr }` in `<script>`, `` js`...` `` attribute literals, data islands |
| [Pipelines](./syntax/pipelines.md) | `\|>` filter pipelines and `std` filters |
| [Rendering raw HTML](./syntax/raw-html.md) | `gsx.Raw` — bypass escaping for trusted HTML strings |
| [Escaping](./syntax/escaping.md) | Context-aware auto-escaping and opt-out helpers |
| [Context](./syntax/context.md) | `context.Context` threading through render |
| [Standard functions](./syntax/std-functions.md) | Built-in filter functions |
| [Interop](./syntax/interop.md) | Using gsx components from plain Go |
| [Forms](./syntax/forms.md) | Form elements and helpers |
:::

## Build constraints and `//go:` directives

Comment lines the Go toolchain acts on — `//go:build`, `//go:generate`,
`//go:debug`, and legacy `// +build` — written before the `package` clause of
a `.gsx` file are copied verbatim into the generated `.x.go`, so build
constraints work exactly as they do for hand-written Go:

```gsx
//go:build linux

package views

component LinuxOnly() {
	<p>linux</p>
}
```

`gsx generate` always generates every `.gsx` file regardless of the host
platform — constraints take effect at `go build`, so one generate pass serves
cross-compilation. Prose comments (license headers, docs) stay in the `.gsx`
only. Note the explicit constraint comment is the only mechanism: generated
file names never acquire Go's implicit `_GOOS` filename constraints.

### Build-tag component variants

Two `.gsx` files under mutually exclusive `//go:build` constraints may declare a
component with the **same name**, as long as they share the **same signature**
(same props, same generic parameters, same receiver). gsx generates a `.x.go`
for every file regardless of tags; `go build` then compiles exactly the variant
whose constraint matches the build, exactly as it does for Go's own
`foo_linux.go` / `foo_windows.go` files.

gsx does not evaluate build tags itself. If two same-named components have
**different** signatures, gsx reports a `duplicate-component` error — build-tag
variants must be drop-in replacements. A genuinely duplicated component with no
tags is reported by `go build` (the generated files collide), which remains the
authority on same-configuration duplicates.

Editor go-to-definition and find-references on such a component show **all**
variants, so you can jump to the platform you care about.

## Quick reference

::: v-pre
| Form | Meaning |
|------|---------|
| `component X(params) { … }` | component declaration (emission body — no return) |
| `component X[T constraint](params) { … }` | generic component declaration |
| `component (p T) Name(params) { … }` | method component (receiver) |
| `component (p T) Name[U constraint](params) { … }` | generic method component; requires a go1.27+ toolchain — older toolchains report `error[unsupported-toolchain]` for the component and generation continues |
| `<div>`, `<el-dialog>` | HTML element — lowercase / hyphenated with no matching package declaration |
| `<Card>`, `<ui.Button>` | component (Capitalized / dotted) |
| `<card>` | component, if the package declares `card` — see [Basic syntax](./syntax/basic-syntax.md#element-vs-component) |
| `<Card[T]>`, `<ui.Button[T]>`, `<p.Row[T]>` | explicit type arguments for a generic component call; omitted type arguments are inferred from supplied props when Go can infer them |
| `{ expr }` | interpolation in body (auto HTML-escaped) |
| any expression returning `(T, error)` | auto-unwraps to `T`; error propagates from the enclosing `Render` — no marker needed, applies in all expression positions (text, attrs, child-prop values, `{{ }}` pair values, pipelines) |
| `name="lit"` | static string attribute |
| `name={ expr }` | dynamic attribute (Go expression) |
| `name` (bare) | boolean attribute = `true` |
| `disabled={ cond }` | type-driven boolean attr (bool → bare/omitted) |
| `{ expr... }` | spread/splat — on an **element**: spreads `gsx.Attrs` as HTML attrs; on a **component**: whole-struct splat (passes the prebuilt struct as props) |
| `name={{ "k": v, "k2": v2 }}` | ordered-attrs literal — binds to a `gsx.Attrs` prop; renders in source order. `attrs={{ }}` (lowercase canonical) targets a component's synthesized fallthrough bag and merges last among bag contributors — see [Attributes](./syntax/attributes.md) |
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
