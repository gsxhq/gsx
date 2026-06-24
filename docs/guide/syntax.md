# Syntax

> **Syntax is roughly fixed, not frozen.** This page is a quick tour. The
> [test corpus](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases)
> is the canonical, always-current reference — every accepted form is a case that
> parses, generates Go, and pins its rendered output, so it can never drift from
> what the compiler actually does.

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
is markup, `{ a < b }` is a Go expression. When in doubt, see the
[`parser/`](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases/parser)
corpus cases.

## Learn by example

Each topic maps to a directory of [corpus cases](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases)
— every case is a `.txtar` holding the `.gsx` input, the generated Go, and the
rendered output, all verified on every test run.

| Topic | Corpus cases |
|-------|--------------|
| Elements, void, DOCTYPE, SVG, web components | `elements/`, `doctype/` |
| Interpolation, raw HTML, escaping contexts | `interpolation/`, `security/` |
| if / for / switch, fragments | `control_flow/` |
| `component` decls, props, `{children}`, slots | `components/`, `slots/` |
| The full attribute system | `attrs/`, `class/`, `style/`, `jsattr/` |
| `|>` pipelines & filters | `pipelines/` |
| Markup-vs-Go corner cases | `parser/` |
| Method components, page composition | `methods/` |
| Children & attribute fallthrough | `fallthrough/` |

> **Status — alpha.** `.gsx` compiles to plain Go via `gsx generate`; syntax is
> stable but still evolving. Follow the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
