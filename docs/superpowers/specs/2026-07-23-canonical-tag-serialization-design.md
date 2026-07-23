# Canonical tag serialization (issue #144)

## Problem

gsx emits authored self-closing syntax verbatim. `<div/>` reaches the browser as
an **open** tag — HTML ignores the trailing slash on non-void elements — so every
following sibling silently nests inside it. Found in gsxui: a self-closed
separator swallowed the rest of a dropdown menu. The mirror case is also legal
today: `<br></br>` emits a void close tag that browsers discard as a parse
error.

## Decision

Generated output is **canonical HTML** by default: gsx fixes tag shapes for the
author instead of erroring. A config option restores today's verbatim
emission.

| Authored | `canonical` (default) | `verbatim` |
|---|---|---|
| `<div/>` (any non-void, incl. SVG/MathML) | `<div></div>` | `<div/>` |
| `<br/>` (void) | `<br>` | `<br/>` |
| `<br></br>` (void, empty close pair) | `<br>` | `<br></br>` |
| `<br>text</br>` (void with children) | **error** | **error** |

- Canonical output contains no `/>` and no void close tags at all. The
  WHATWG fragment-serialization algorithm emits void elements without the
  slash; the slash "has no effect" per spec. (React's server renderer emits
  `<br/>`; both are valid — gsx picks the spec-canonical shape.)
- Expansion is **uniform by name**: any tag not in the void set expands,
  including SVG/MathML names (`<path/>` → `<path></path>`, equally valid in
  foreign content). No foreign-content name table, no ancestor tracking.
- A void element with children has no valid meaning and errors in **both**
  modes: "void element `<br>` cannot have children", anchored at the element.
  Analyzer diagnostic (diagnostics live in the analyzer, never the formatter);
  errors gate emission as usual.
- This also fixes the `<script/>`-swallows-the-document hazard: script is
  non-void, so it expands.

## Void set

The WHATWG void elements, matched case-insensitively on non-component tags:
`area base br col embed hr img input link meta param source track wbr`.
Lives beside the existing HTML name table in
`internal/codegen/htmlnames.go`.

## Config

`serialization = "canonical" | "verbatim"`, default `canonical`. Standard three
layers, most deliberate wins: `gen.WithSerialization(...)` option >
`GSX_SERIALIZATION` env > gsx.toml top-level `serialization` key (mirrors the
minify pattern). Unknown values are a config error. The mode changes generated
output, so it **folds into computeKey**.

## Implementation shape

- **Parser/AST unchanged.** `Element.Void` keeps meaning "authored
  self-closed"; `<br>` without any close tag remains a parse error (gsx source
  stays well-formed). The formatter (printer.go) and LSP stay untouched —
  `gsx fmt` still prints `<div/>` as authored; only generated output
  normalizes.
- **Emit tails.** The sites that write element tails — the plain path
  (emit.go ~1904), the manual-spread path (emit.go ~1329), and the fold path's
  tail — replace unconditional `emitS(b, "/>")` with a mode+void-set decision:
  void → `>` (canonical) or `/>` (verbatim); non-void self-closed →
  `></tag>` (canonical) or `/>` (verbatim). An authored empty void close pair
  (`<br></br>`) drops its close tag in canonical mode. Nonce-guard ordering
  unchanged (tail after `ni.emitGuard`).
- **Diagnostic** in the analyzer: non-component tag in the void set with
  non-empty children → error, both modes.

## Testing

Corpus cases per emit context: `<div/>` plain, `<div {attrs...}/>` (manual
spread), multi-spread fold, element literal in Go-expr position, `<path/>`
(SVG), `<br/>` slash-drop, `<br></br>` collapse, `verbatim` mode pinning
today's bytes, and diagnostic cases `<br>text</br>` / `<img>x</img>`.
`examples/210-void-elements.txtar` goldens regenerate (`<br/>` → `<br>`) with
doc prose updated. Config resolution (option/env/config precedence, unknown
value, computeKey invalidation) gets unit tests in `gen`.

## Docs / siblings

`docs/guide` elements page documents canonical serialization and the
`serialization` key (concise; website is a synced copy — edit gsx-side only).
tree-sitter-gsx and vscode-gsx need nothing: syntax is unchanged, only emitted
bytes. gsxui can drop its `docs/jsx-parity.md` ledger warning once released.
