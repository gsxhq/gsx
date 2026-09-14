# Changelog

Tagged releases of `github.com/gsxhq/gsx`. Before 1.0, a minor bump may change
syntax or APIs; a patch bump does not. See
[Releases and versioning](docs/guide/status.md#releases-and-versioning).

## Unreleased

- **Language** — attribute names follow the HTML rule (any characters except
  controls, space, `"`, `'`, `>`, `/`, `=`, noncharacters, plus gsx's `<`,
  `{`, `}`, `` ` ``); there is no distinct first character, so `.prop`,
  `?disabled`, `#ref`, `[prop]`, `(event)` and `on:click|mod` parse. The
  runtime spread keeps bag keys by the same rule: it now accepts `&`, and drops
  C1 controls, noncharacters, `{`, `}`, backticks and invalid UTF-8 that it
  previously emitted.
- **Escaping** — the `htmx` URL preset covers htmx 4: `hx-query` and
  `hx-action` join the five method attributes, and every name is also
  sanitized in its `:inherited`, `:append` and `:inherited:append` spellings,
  which htmx 4 reads through the same lookup. One preset serves htmx 2 and 4.
- **Runtime** — a url preset is now a predicate compiled into the runtime:
  `gsx.AttrSinks` gains `Presets gsx.URLPreset`, and generated spread sites
  carry `Presets: _gsxrt.PresetHTMX` instead of a name list, so a preset's
  coverage can change without touching generated code.
- **Editor** — `hx-*` completion is the union of the htmx 2 and htmx 4
  attribute tables; an attribute only one version has says so in its hover
  text, and `hx-disable` documents both meanings. (#199)
- **Toolchain** — `gsx.toml` resolves per module: `generate`, `dev` and `fmt`
  still walk into nested Go modules, but each module is configured from the
  `gsx.toml` found walking up from ITS root, so a nested module's own file is
  honoured instead of ignored. A nested module without one inherits the
  ancestor file as before; when that file names a package the nested module
  cannot import (a `class_merger`, filter or renderer of the outer module),
  the error now names the inherited file, the module, and the per-module
  `gsx.toml` that fixes it. (#200)

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
