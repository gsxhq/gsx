# gox examples

These `.gox` files are the **design corpus** for the gox templating language.
They serve two purposes:

1. **Design principle** — every accepted syntax decision is demonstrated by at
   least one real example here. If a pattern can't be written cleanly in these
   files, the design is wrong.
2. **Test fixtures** — once the generator exists, each file is a golden input.
   `*.gox` → generated `*.x.go` → compiles → renders expected HTML.

> Status: the generator does not exist yet. These files are illustrative source.
> They are intentionally *not* valid Go (they contain markup) and are not built.

The syntax demonstrated reflects the approved design in
[`../docs/superpowers/specs/2026-06-18-gox-templating-design.md`](../docs/superpowers/specs/2026-06-18-gox-templating-design.md).

## Quick syntax reference

| Form | Meaning |
|------|---------|
| `component X(params) { … }` | component declaration — no return type, no `return` (emission body) |
| `component (p T) Name(params) { … }` | method component (receiver) |
| `func … gox.Node { return … }` | ordinary Go; manual component escape hatch |
| `<div>`, `<my-el>`, `<el-dialog>` | HTML element (lowercase / hyphenated) |
| `<Card>`, `<ui.Button>`, `<p.Content>` | component (Capitalized / dotted) |
| inline params → generated `XProps` | `<Card title="Hi"/>` → `Card(CardProps{Title:"Hi"})` |
| `{children}` | explicit placement; passing children to a component that never places it → compile error |
| attribute fallthrough | undeclared attrs auto-apply to the single root (`class`/`style` merge); touch `attrs` to override; ambiguous root → compile error |
| ambient `ctx` | `context.Context` is implicitly in scope in every component body (never declared) |
| `name="lit"` | static string attribute |
| `name={ expr }` | dynamic attribute (Go expression) |
| `name` (bare) | boolean attribute = `true` |
| `disabled={ cond }` | **type-driven** boolean attr (bool value → bare/omitted) |
| `{ expr? }` | `?` try-marker: unwrap `(T, error)`, propagate err (implicit error return) |
| `{ if … }` `{ for … }` inside a tag | conditional attributes |
| `{...expr}` | spread attributes (element) / props (component) |
| `{ expr }` in body | interpolation (auto HTML-escaped) |
| `{ if/for/switch … { <markup> } }` | control flow contributing children |
| `{{ stmt }}` | Go statement escape hatch (no output; between markup siblings) |
| `<>…</>` | fragment |
| `class={ a, "cls": cond, x }` | composable `class`/`style` (comma-list; `"cls": cond` conditional sugar; pluggable merger) |
| `gox.Raw(s)` | unescaped HTML |

## Files

| File | Demonstrates | Corner cases |
|------|--------------|--------------|
| [01_elements.gox](01_elements.gox) | elements, attrs, void elements, DOCTYPE, comments, SVG, web components | hyphenated tags, self-closing, namespaced attrs |
| [02_text_escaping.gox](02_text_escaping.gox) | interpolation, raw HTML, entities, URL/JS/JSON contexts | strings containing `<`, escaped quotes |
| [03_control_flow.gox](03_control_flow.gox) | if/else-if/else, for variants, switch, nested, fragments | the `{{ }}` escape-hatch strong use cases |
| [04_components.gox](04_components.gox) | `component` decl, inline params → `XProps`, `{children}`, named slots, zero-param, cross-package, attr fallthrough | children misplacement → error; undeclared attrs fall through to root |
| [05_attributes.gox](05_attributes.gox) | the full attribute system incl. a compound Button: dynamic/boolean/conditional attrs, spread, implicit rest, composable `class`, special-char attr names | `@click`, `:class`, `hx-on::click`, `_` |
| [06_corner_cases.gox](06_corner_cases.gox) | markup-vs-Go-expression ambiguity | `a < b`, strings with `<`, nested braces, multiline, raw `<script>`/`<style>` with braces |
| [11_struct_methods.gox](11_struct_methods.gox) | method components for page composition: receiver-as-page-data, Page/Content split, method-with-params → props, value & pointer receivers, method calling sibling | `<p.Content/>` (method) vs `<ui.Button/>` (package) disambiguation; reusable families are packages |
| [12_children_attrs.gox](12_children_attrs.gox) | children placement & attribute fallthrough; rich gox.Attrs utilities | auto-fallthrough to single root, ambiguity→compile error, children-misplacement→compile error |
| [07_realworld_dialog.gox](07_realworld_dialog.gox) | Dialog/Header/Body/Footer compound + banner variants | data-state attrs, conditional close button, switch-on-variant icon |
| [08_realworld_table.gox](08_realworld_table.gox) | data table with column metadata + loops, selection, pagination | sticky headers, Alpine attr strings built in Go |
| [09_realworld_form_htmx.gox](09_realworld_form_htmx.gox) | form fields with error display, HTMX submission, type-safe URLs | conditional + boolean + spread attrs combined, `(val, err)` via `{{ }}` |
| [10_realworld_layout_email.gox](10_realworld_layout_email.gox) | app-shell layout with slots & grouped nav loop, HTML email | global overlay slot, sanitized URLs, same language for chrome + email |
