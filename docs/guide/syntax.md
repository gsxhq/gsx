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
| `{ f() }` where `f` returns `(T, error)` | auto-unwraps to `T`; the error propagates out of the enclosing `Render` (no `?` marker) |
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

## Escaping & safe contexts

Encoding is **automatic and context-aware** — you write the value, gsx picks the
escaper from *where* it sits (the codegen knows the context). Helpers are
**opt-outs** for trusted values, never required for safety.

| Context | What gsx does | Opt-out (trusted) |
|---------|---------------|-------------------|
| Text / attribute (`{ x }`, `attr={ x }`) | HTML / attribute escape | `gsx.Raw(s)` |
| URL attribute (`href`, `src`, `action`, `hx-*`, …) | scheme-sanitize + escape | `gsx.RawURL(s)` |
| JS value (`@{ x }` in `<script>` or a JS attr like `x-data`/`@click`/`hx-on*`) | **JSON-encode** (HTML-safe), Go value → JS literal | `gsx.RawJS(s)` |
| JSON data island (`<script type="application/json">@{ data }</script>`) | **JSON-encode** the whole body | — |
| CSS value (`<style>` body, CSS-context attrs) | value-filter (`gw.CSS`); risky tokens like `(` `/` collapse to a safe placeholder | `gsx.RawCSS(s)` |

**JSON and CSS are automatic, not filters.** Any JS-value position JSON-encodes via
the runtime `JSVal`; CSS values (`<style>` bodies, `style=` and CSS-context attrs,
composable `style={ … }`) auto value-filter via `gw.CSS`/`gw.Style`. There is no
`|> json` or `|> css`. Every context above is safe by default — **CSS is just the
most conservative** (its value-filter drops `(`/`/`, so a dynamic
`rgb(...)`/`calc(...)`/`url(...)` needs `gsx.RawCSS`). The one genuinely
*fail-closed* context is a **JS event-handler expression value** (`onclick={ … }`,
`@click={ … }`, `hx-on*`), which is a compile error — use `gsx.RawJS` for trusted
JS. See the `security/`, `style/`, `jsattr/`, and `datajson/` corpus cases.

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
