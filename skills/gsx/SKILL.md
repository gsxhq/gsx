---
name: gsx
description: Use when writing or editing .gsx files — gsx templating components, JSX-style markup in Go, or when asked about gsx syntax (component declarations, interpolation, attributes, control flow, class/style, children/fallthrough).
---

# Authoring gsx templates

gsx is a Go templating language: templ-style `component` declarations with a
JSX-style markup body, compiled to plain Go (`.gsx` → `.x.go`).

> **Status:** gsx is runnable — `gsx generate` compiles `.gsx` → `.x.go`. The
> language is stable but still evolving (some CLI commands and `style` composition
> are in progress). Treat the `internal/corpus/testdata/cases/**/*.txtar` corpus
> as the canonical, current reference — each case pins the `.gsx` input, generated
> Go, and rendered output — and prefer copying patterns from there.

## Core rules

- A `.gsx` file is ordinary Go (package, imports, types, funcs) **plus** `component`
  declarations. Put non-template Go (helpers, types) in the same file as normal Go.
- A component body is **emission** — the markup *is* the result. No return type, no
  `return` keyword. Bare Go statements are not allowed in the body; wrap them in
  `{{ … }}`.
- **Capitalization decides the tag's meaning:** lowercase/hyphenated → HTML element
  (`<div>`, `<el-dialog>`); Capitalized/dotted → component (`<Card>`, `<ui.Button>`).
- A component's authored parameter list is emitted unchanged. Markup binds ordinary
  parameters by exact name; direct Go callers use the same positional signature.
  For `component Card(title string, featured bool)`, `<Card title="Hi" featured/>`
  binds `title` and `featured`, while Go calls `Card("Hi", true)`.
- Lowercase `children` and `attrs` are reserved roles only when declared as
  parameters. `children` receives the markup body. `attrs` receives unmatched
  attributes and ordered bag contributors such as `attrs={bag}` and
  `attrs={{ "id": id }}`. Capitalized `Children` and `Attrs` are ordinary inputs.

## Forms

- Interpolation: `{ expr }` (auto-escaped per context: HTML/attr/URL/CSS). Opt-outs,
  each bypassing one check: `gsx.Raw(s)` (HTML), `gsx.RawURL(s)` (URL scheme check),
  `gsx.RawCSS(s)` (CSS value-filter). JS/event-handler (`on*`) contexts are a
  compile error, not auto-escaped.
- Error handling: a `(T, error)` interpolation/attr value (`{ f() }`, `name={ f() }`)
  auto-unwraps to `T`; the error propagates out of the enclosing `Render`. There is
  no `?` try-marker. To handle the error instead, use `{ if v, err := f(); err != nil { … } }`.
- Attributes: static `name="lit"`, dynamic `name={ expr }`, boolean bare `name`,
  type-driven `disabled={ cond }` (bool → bare/omitted), spread `{ expr... }`,
  conditional `{ if … }` / `{ for … }` inside the tag.
- Composable `class`/`style`: `class={ a, "cls": cond, x }` (comma list;
  `"cls": cond` is conditional sugar).
- Control flow contributing children: `{ if/for/switch … { <markup> } }`.
- Go escape hatch (no output): `{{ stmt }}`. Fragments: `<>…</>`.
- Children: declare `children gsx.Node` or `children ...gsx.Node`, then place it
  with `{children}`. A component without that parameter rejects a non-empty body.
- Attribute fallthrough: declare `attrs gsx.Attrs` (or another supported attrs-bag
  shape), then spread `{ attrs... }` where the component chooses. A component
  without `attrs` rejects unmatched attributes and attrs-bag contributors.

## The one subtlety: markup vs Go

Inside `{ }`, gsx decides markup-vs-Go **positionally** (the Babel rule):
`{ <div/> }` is markup, `{ a < b }` is a Go expression. If a comparison or generic
looks like a tag, reach for parentheses or a `{{ }}` block. See the
`internal/corpus/testdata/cases/parser/` corpus cases.

## When in doubt

Read the matching corpus directory under `internal/corpus/testdata/cases/`:
elements `elements/`, escaping `interpolation/` + `security/`, control flow
`control_flow/`, components/children/slots `components/` + `slots/`, attributes
`attrs/` + `class/` + `style/` + `jsattr/`, corner cases `parser/`, method
components `methods/`, fallthrough `fallthrough/`. Full guide: `docs/guide/syntax.md`.
