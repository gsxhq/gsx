---
name: gsx
description: Use when writing or editing .gsx files — gsx templating components, JSX-style markup in Go, or when asked about gsx syntax (component declarations, interpolation, attributes, control flow, class/style, children/fallthrough).
---

# Authoring gsx templates

gsx is a Go templating language: templ-style `component` declarations with a
JSX-style markup body, compiled to plain Go (`.gsx` → `.x.go`).

> **Status:** gsx is runnable — `gsx generate` compiles `.gsx` → `.x.go`. The
> language is stable but still evolving (some CLI commands and `style` composition
> are in progress). Treat `examples/*.gsx` as the canonical, current reference and
> prefer copying patterns from there.

## Core rules

- A `.gsx` file is ordinary Go (package, imports, types, funcs) **plus** `component`
  declarations. Put non-template Go (helpers, types) in the same file as normal Go.
- A component body is **emission** — the markup *is* the result. No return type, no
  `return` keyword. Bare Go statements are not allowed in the body; wrap them in
  `{{ … }}`.
- **Capitalization decides the tag's meaning:** lowercase/hyphenated → HTML element
  (`<div>`, `<el-dialog>`); Capitalized/dotted → component (`<Card>`, `<ui.Button>`).
- Inline component params become a generated `XProps` struct — gsx owns the field
  names: `<Card title="Hi" featured/>` → `Card(CardProps{Title: "Hi", Featured: true})`.

## Forms

- Interpolation: `{ expr }` (auto HTML-escaped). Unescaped: `gsx.Raw(s)`.
- Try-marker: `{ expr? }` unwraps `(T, error)` and propagates the error.
- Attributes: static `name="lit"`, dynamic `name={ expr }`, boolean bare `name`,
  type-driven `disabled={ cond }` (bool → bare/omitted), spread `{...expr}`,
  conditional `{ if … }` / `{ for … }` inside the tag.
- Composable `class`/`style`: `class={ a, "cls": cond, x }` (comma list;
  `"cls": cond` is conditional sugar).
- Control flow contributing children: `{ if/for/switch … { <markup> } }`.
- Go escape hatch (no output): `{{ stmt }}`. Fragments: `<>…</>`.
- Children: `{children}` places passed children. Passing children to a component
  that never places them is a compile error.
- Attribute fallthrough: undeclared call-site attrs auto-apply to a single root
  element (`class`/`style` merge); an ambiguous root is a compile error.

## The one subtlety: markup vs Go

Inside `{ }`, gsx decides markup-vs-Go **positionally** (the Babel rule):
`{ <div/> }` is markup, `{ a < b }` is a Go expression. If a comparison or generic
looks like a tag, reach for parentheses or a `{{ }}` block. See
`examples/06_corner_cases.gsx`.

## When in doubt

Read the matching example: elements `01`, escaping `02`, control flow `03`,
components/children `04`, attributes `05`, corner cases `06`, method components
`11`, fallthrough `12`. Full guide: `docs/guide/syntax.md`.
