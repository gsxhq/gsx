# JSON-aware minification for `js`…`` attribute values

**Status:** design · **Date:** 2026-07-17 · **Branch:** `json-attr-minify`

## Problem

gsx minifies inline `js`…`` attribute values as JavaScript. For a `{`-leading
value it wraps the object in `(…)` (via `cascadeJS`) so tdewolff minifies it as an
*expression* rather than a labeled-block statement, and tdewolff drops quotes from
object keys that are valid identifiers. Both are correct, size-reducing transforms
for JavaScript (Alpine `x-data`, `:class`, event handlers).

They are **fatal for JSON**. htmx's `hx-vals` / `hx-headers` / `hx-vars` carry a
JSON payload that htmx parses with `JSON.parse` (unless prefixed `js:`). A source
value `hx-vals=js`{ "exclude": @{selfID} }`` — written with `js`…`` because it needs
`@{}` hole value-quoting — minifies to `({exclude:"SELF-1"})`:

- key unquoted: `"exclude"` → `exclude`
- object paren-wrapped: `{…}` → `({…})`

`JSON.parse` rejects both, so htmx silently drops the param at runtime. This is a
real regression relative to the pre-minifier (templ) output, confirmed at five
one-learning sites (`entity_links`, `admin_license_usage`, `header`,
`filter_dialog`, `reactions`).

Failing reproduction: `internal/jsmin` ·
`TestMinifyJSAttrJSONShapedValueStaysValidJSON` (asserts the minified value is
valid JSON; currently fails with `({exclude:"SELF-1"})`).

The `js` literal prefix alone cannot distinguish an Alpine JS expression from a
JSON payload — both are `{`-leading `js`…`` values.

## Approach

Classify a `{`/`[`-leading `js`…`` **attribute** value by whether it is JSON, using
a real JSON parse (deterministic, not a shape heuristic), and minify JSON payloads
as JSON. Transparent: no new syntax, no source changes, no sibling-repo/tree-sitter/
vscode updates.

### Detection (in the `{`/`[`-leading branch of the attr-JS minify path)

1. Cheap gate: value (whitespace-trimmed) starts with `{` or `[`. Otherwise it is a
   handler/statement/expression → unchanged JS path.
2. `encoding/json`.`json.Valid`:
   - **holeless**: check the assembled text directly.
   - **holey** (`@{expr}`): substitute every hole with a JSON-valid placeholder
     (`null`) and check that. `{ "exclude": @{selfID} }` → `{ "exclude": null }`
     → valid → classified JSON. (`null` is only for the *validity check*; the real
     holes are preserved for minification.)

Valid JSON → JSON minify path. Not valid JSON (unquoted keys, single quotes,
handler statements, call expressions, …) → the existing JS path, **unchanged**.

### JSON minify path

tdewolff's `application/json` minifier (already a dependency; `internal/fullmin`
gains a `JSON` entry alongside `CSS`/`JS`). It strips insignificant whitespace and
keeps validity — quoted keys, no `(…)` wrap, no `false`→`!1`.

- **holeless**: `{ "exclude": "SELF-1" }` → `{"exclude":"SELF-1"}`.
- **holey**: reuse the existing sentinel round-trip (`minifyJSSegmentsHoley`) but
  with a **JSON-valid integer sentinel** instead of the `gsxHole<n>z` identifier
  (identifiers are not JSON). A bare integer survives the JSON minifier verbatim
  (verified: `909090900` → `909090900`), so it round-trips cleanly:
  `{ "exclude": @{selfID} }` → sentinel `{ "exclude": 909090900 }` → JSON-minify
  `{"exclude":909090900}` → split back → `{"exclude":@{selfID}}` → renders
  `{"exclude":"SELF-1"}`. The sentinel is chosen absent from the static text (same
  collision-avoidance discipline as the current identifier prefix), and delimited so
  `<base><i>` and `<base><j>` never alias (fixed-width index or a non-digit-free
  gap — resolved in the plan).

### Non-JSON path

Byte-for-byte the current behavior. `x-data="{ open: false }"` (unquoted key → not
valid JSON) → `({open:!1})`, unchanged.

## Trade-off (explicit)

A `js`…`` attribute value that *is* valid JSON but was intended as an Alpine JS
expression — e.g. `x-data="{ "count": 0 }"` (quoted key) — now gets whitespace-only
JSON minification (`{"count":0}`) instead of the aggressive JS rewrite. Still valid
for Alpine, marginally larger. This is deliberate: apply the lossy JS-only rewrites
**only** when the value is provably not JSON. A false positive (JSON path on a value
that renders as JS) is harmless — JSON whitespace-stripping is correctness-preserving
for JS too; the JS lexer accepts the same token stream. The design specifically
prevents false *negatives*, which are the current bug.

## Scope & boundaries

- **In scope:** the attribute-value path — `minifyJSAttrs` → `minifyJSSegments` /
  `minifyJSSegmentsHoley` → `cascadeJS`, in `internal/jsmin/file.go`. A new JSON
  entry in `internal/fullmin`.
- **Out of scope:** `<script>` blocks (a JSON data-island already uses
  `type="application/json"` and is skipped — see `TestMinifyFileSkipsDataIslandScript`);
  `{{ }}`-block and `{ expr }`-embedded `js`…`` literals (rare as JSON; can extend
  later using the same classifier if a case appears). Not changing the `js` literal
  syntax, so no parser/classifier/tree-sitter/vscode work.
- **Levels:** the JSON path needs the full (tdewolff) minifier. Under the built-in
  *safe* level (`ext == nil`) the JSON branch behaves like the current safe pass
  (reindent / whitespace reduction, never a value rewrite) — validity is already
  preserved there, so safe-level output is unaffected.

## Testing

- `internal/jsmin` unit tests:
  - `TestMinifyJSAttrJSONShapedValueStaysValidJSON` flips green; assert the exact
    compact output `{"exclude":"SELF-1"}`.
  - holey JSON value round-trips to valid JSON with the hole preserved (extend the
    existing holey pattern).
  - regression: `x-data="{ open: false }"` still → `({open:!1})` (JS path untouched);
    a quoted-key `x-data` → valid-JSON compact form.
  - detection edges: single-quoted keys (JS, not JSON), trailing commas (JS, not
    JSON), arrays (`[ … ]`), nested objects.
- Corpus case (`internal/corpus/testdata/cases`): an `hx-vals`-style `{`-leading
  `js`…`` attribute (holeless + holey) pinned through parse → generate → render, with
  the minify gate on, asserting valid compact JSON in `render.golden`.
- End-to-end: rebuild the tool, regenerate one-learning, unskip the two blocked
  tests (`entity_links`, `admin_license_usage` Partials), confirm valid `hx-vals`.
- `make ci` (corpus goldens, `gofmt`/`gsx fmt`).

## Docs

Short note in the config/minify guide: JSON-shaped `js`…`` attribute values (e.g.
`hx-vals`) are minified as JSON, preserving validity. No syntax change to document.
