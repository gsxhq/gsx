# Changelog

Tagged releases of `github.com/gsxhq/gsx`. Before 1.0, a minor bump may change
syntax or APIs; a patch bump does not. See
[Releases and versioning](docs/guide/status.md#releases-and-versioning).

## v0.1.0 — 2026-09-03

First tagged release. Everything the [Status](docs/guide/status.md) page lists as
shipped is in this tag:

- **Language** — verbatim component signatures with declared `children` and
  `attrs` roles, control flow, `|>` pipelines with `(T, error)` auto-unwrap,
  attribute forwarding and ordered `{{ }}` bags, named slots, element literals,
  `js`/`css`/`f` tagged literals, bare `//` comments, processing instructions.
- **Rendering** — standard-library-only runtime; contextual HTML, URL, CSS and
  JavaScript escaping ported from `html/template`; name-driven boolean
  attributes with `gsx.Toggle`; `Attrs.Bool`; CSP nonce injection; renderers
  registry.
- **Toolchain** — `gsx init`, `generate` (with `--watch`), `fmt`, `info`,
  `clean`, `version`; the `gsx dev` loop with the Vite plugin
  (`@gsxhq/vite-plugin-gsx` 0.11) and `github.com/gsxhq/vite` (v0.3).
- **Editor** — `gsx lsp`: diagnostics, definition, references, hover, symbols,
  formatting, code actions, completion with auto-import; VS Code extension and
  tree-sitter grammar in sibling repos.

Requires Go 1.26.
