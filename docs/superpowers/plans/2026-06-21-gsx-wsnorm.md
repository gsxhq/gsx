# Plan: `internal/wsnorm` — the JSX whitespace Normalize pass (slice 1)

**Date:** 2026-06-21
**Branch:** `feat/gsx-wsnorm` off `main`
**Design:** `specs/2026-06-21-gsx-whitespace-model-design.md`
**Status:** ready for SDD

## Goal

The standalone, pure `internal/wsnorm.Normalize(*ast.File)` pass + its unit tests.
No codegen wiring, no `fmt` (slice 2). Fully self-contained and mergeable.

## Single task: `wsnorm.Normalize` + unit tests

### The per-text rule (the load-bearing core)
`normalizeText(v string) (out string, keep bool)` — normalizes ONE `Text` value in a
non-preserve children list:
- If `v` is **all whitespace**: drop it (`keep=false`) when it contains a newline
  (cosmetic indentation); else return a single space `" "` (inline spacing), keep.
- Else: let `lead` / `trail` be the leading / trailing whitespace runs and `core` the
  middle. **Collapse every internal whitespace run in `core` to a single space.**
  Prepend a space iff `lead` is non-empty AND contains no newline; append a space iff
  `trail` is non-empty AND contains no newline. (A newline at an edge means cosmetic
  indentation → no space; inline-only edge whitespace → one significant space.)

This satisfies the design's litmus exactly:
```
"\n  " (between block tags)  → dropped
" " (between inline tags)    → " "
"Hello,   "  (before {x})    → "Hello, "       (trailing inline run → one space)
"\nworld"    (after {x})      → "world"          (leading newline edge → dropped)
"foo   bar"                  → "foo bar"        (internal run collapsed)
"\n  a\n  b\n"               → "a b"            (lines trimmed, joined by one space)
```
(The whole-text view: split is unnecessary — the per-edge + internal-collapse rule
above produces the same result and is self-contained per Text node; no neighbor
context needed because the newline lives inside the Text's own whitespace.)

### Traversal
`Normalize(f *ast.File)` walks in place:
- Each `*Component` → normalize `Body` as a children list (preserve=false).
- `normalizeMarkup(nodes []ast.Markup, preserve bool) []ast.Markup`:
  - `*Text`: if `preserve` keep verbatim; else apply `normalizeText` (rewrite `.Value`,
    drop if `!keep`). Re-span rewritten nodes to the original node's span (exact text
    spans are not load-bearing; document this).
  - `*Element`: `childPreserve = preserve || isPreserveTag(t.Tag)` where preserve tags
    (lowercased) are **`pre`, `textarea`, `script`, `style`** (script/style bodies are
    raw JS/CSS — never collapse). Recurse `t.Children` with `childPreserve`. ALSO call
    `normalizeAttrs(t.Attrs)` to reach any `MarkupAttr.Value` (a named-slot markup list
    — normalized as a FRESH context, preserve=false, NOT inheriting the element's
    preserve).
  - `*Fragment` → recurse Children (preserve). `*IfMarkup` → Then+Else. `*ForMarkup` →
    Body. `*SwitchMarkup` → each `Cases[i].Body`. All thread `preserve`.
  - `*Interp`, `*GoBlock`, `*Doctype`, `*HTMLComment` → leaves (untouched; Doctype/
    HTMLComment are verbatim by design).
- `normalizeAttrs(attrs []ast.Attr)`: for each `*MarkupAttr` → its `Value =
  normalizeMarkup(Value, false)`; for each `*CondAttr` → `normalizeAttrs(Then)` +
  `normalizeAttrs(Else)` (to reach MarkupAttrs nested in conditional attrs). Other attr
  kinds carry no markup.
- **Idempotent by construction**: normalized text re-normalizes to itself.

### Package
New `internal/wsnorm/wsnorm.go` (+ small helpers). Imports only `github.com/gsxhq/gsx/ast`
and stdlib (`strings`/`unicode`). Public `Normalize(*ast.File)`. No other package wired
yet (slice 2 wires codegen + fmt).

### Tests (`internal/wsnorm/wsnorm_test.go`, plain Go unit tests — the Unit layer)
- **`normalizeText` table** covering every case above (all-ws+newline drop; all-ws
  no-newline → " "; lead/trail newline vs inline; internal collapse; multi-line join;
  content-only unchanged; tabs treated as whitespace).
- **`Normalize` on parsed files** (use `parser.ParseFile`; assert resulting Text nodes
  via `ast.Inspect`): the design litmus — indentation between block elements removed;
  `<b>x</b> y` keeps the space; `<b>x</b>\ny` drops it; `Hello,   {name}` → `Hello, `.
- **Preserve contexts**: `<pre>  a\n  b</pre>`, `<textarea>\n x \n</textarea>`,
  `<script>\n  let x = 1;\n</script>`, `<style>\n  a { } \n</style>` — Text unchanged.
  A `pre` wrapping nested elements + indentation → all inner whitespace preserved
  (nested-preserve flag stays on).
- **MarkupAttr slot**: `<Panel header={ <h1>\n  Hi \n</h1> }/>` → the slot's markup is
  normalized (`<h1>Hi</h1>`), and a `<pre>` slot is preserved.
- **Control flow**: `{ for _, x := range xs { <li>\n {x} \n</li> } }` → the `<li>`
  body normalized.
- **Idempotence property**: for each test input, `Normalize` then a re-parse+Normalize
  (or re-run on the same tree) yields an identical AST/text — assert twice == once.

## Verify + commit
`go test ./... && go vet ./... && gofmt -l internal/wsnorm/` (prints NOTHING). The
existing suite (codegen/corpus) MUST stay green — wsnorm is NOT wired in yet, so it
cannot affect rendering or any golden.

Commit: `wsnorm: JSX whitespace Normalize pass (standalone, pure)`.

## After task
- Review + independent review (focus: the per-text rule's edge cases, preserve-flag
  nesting, MarkupAttr-slot fresh context, idempotence).
- Merge `--no-ff`.
- **Slice 2 (separate plan, coordinate goldens):** wire `Normalize` into `GeneratePackage`
  (before emit) + build `internal/printer` + `gsx fmt`; regenerate the corpus render
  goldens once (they'll lose rendered indentation); add the render-faithfulness +
  idempotence property tests over the corpus.

## Risks
- **The per-text rule** is the whole correctness surface — the table test must be
  exhaustive. Keep `normalizeText` a small pure function, separately unit-tested.
- **Preserve nesting** — `preserve` must stay on through arbitrary descendants of a
  `pre`/`textarea` but a `MarkupAttr` slot is a fresh boundary. Tested both ways.
