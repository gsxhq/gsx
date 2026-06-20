# Syntax

> **Syntax is roughly fixed, not frozen.** This page is a quick tour. The
> [`examples/`](https://github.com/gsxhq/gsx/tree/main/examples) corpus is the
> canonical reference — nearly every accepted form is demonstrated there (one file,
> `02_text_escaping.gsx`, has a tracked parser gap; see the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md)).

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

## Elements vs components

Capitalization decides what a tag means:

- lowercase / hyphenated → HTML element: `<div>`, `<el-dialog>`
- Capitalized / dotted → component: `<Card>`, `<ui.Button>`, `<p.Content>`

Inline component params become a generated props struct, so gsx owns the field
names: `<Card title="Hi" featured/>` → `Card(CardProps{Title: "Hi", Featured: true})`.

## Quick reference

| Form | Meaning |
|------|---------|
| `component X(params) { … }` | component declaration (emission body — no return) |
| `component (p T) Name(params) { … }` | method component (receiver) |
| `<div>`, `<el-dialog>` | HTML element (lowercase / hyphenated) |
| `<Card>`, `<ui.Button>` | component (Capitalized / dotted) |
| `{ expr }` | interpolation in body (auto HTML-escaped) |
| `{ expr? }` | try-marker: unwrap `(T, error)`, propagate the error |
| `name="lit"` | static string attribute |
| `name={ expr }` | dynamic attribute (Go expression) |
| `name` (bare) | boolean attribute = `true` |
| `disabled={ cond }` | type-driven boolean attr (bool → bare/omitted) |
| `{...expr}` | spread attributes (element) / props (component) |
| `{ if … }` / `{ for … }` inside a tag | conditional attributes |
| `{ if/for/switch … { <markup> } }` | control flow contributing children |
| `{{ stmt }}` | Go statement escape hatch (no output) |
| `<>…</>` | fragment |
| `class={ a, "cls": cond }` | composable `class`/`style` (comma list; conditional sugar) |
| `{children}` | explicit children placement |
| `gsx.Raw(s)` | unescaped HTML |

## Markup vs Go (the one subtlety)

Inside `{ }`, gsx decides markup-vs-Go positionally — the Babel rule: `{ <div/> }`
is markup, `{ a < b }` is a Go expression. When in doubt, see
[`examples/06_corner_cases.gsx`](https://github.com/gsxhq/gsx/blob/main/examples/06_corner_cases.gsx).

## Learn by example

| Topic | Example |
|-------|---------|
| Elements, attrs, void, DOCTYPE, SVG, web components | `01_elements.gsx` |
| Interpolation, raw HTML, escaping contexts | `02_text_escaping.gsx` |
| if / for / switch, fragments, `{{ }}` | `03_control_flow.gsx` |
| `component` decls, props, `{children}`, slots | `04_components.gsx` |
| The full attribute system | `05_attributes.gsx` |
| Markup-vs-Go corner cases | `06_corner_cases.gsx` |
| Method components, page composition | `11_struct_methods.gsx` |
| Children & attribute fallthrough | `12_children_attrs.gsx` |

> **Status — alpha.** `.gsx` files are illustrative; the CLI that generates `.x.go`
> is a work in progress. Follow the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
