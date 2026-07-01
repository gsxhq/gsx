# Status

gsx is alpha software. It is usable end to end, but the language and APIs may still change before a stable release.

## Shipped

- `gsx init`, `gsx dev`, `gsx generate`, `gsx fmt`, `gsx info`, `gsx clean`, `gsx lsp`, `gsx version`, and `gsx help`.
- Component declarations, method components, generated props, bring-your-own props, `{children}`, named slots, and explicit attribute forwarding.
- Interpolation, control flow, attributes, contextual escaping, pipelines, `(T, error)` auto-unwrap, fragments, raw Go blocks, raw-text elements, composable `class`, element-level composable `style`, class/style merge, ordered attrs, and value-form `if`/`switch` in class/style lists.
- Vite-backed development loop with warm generation, server rebuild, browser reload, and browser error overlay.
- Language server diagnostics, go-to-definition, hover, references, formatting, and editor integration paths.

## Partial

- LSP completion is deferred.
- References cover project components discovered during module analysis; external/non-project references are deferred.
- CLI `vet`, `render`, `explain`, and stable numeric diagnostic codes are deferred.
- Component-invocation `style={...}` composition and `[]string` class parts are deferred.

## Known Gaps

- CSP nonce threading for emitted `<script>` and `<style>` is not implemented.

## Detailed Roadmap

The detailed engineering roadmap lives in
[Roadmap & Status](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
