# gsx fmt: preserve author line breaks in control-flow bodies and element children

Status: design → implementation
Date: 2026-07-06

## Problem

`gsx fmt` collapses a control-flow body (or element children) onto one line
whenever the body is *inline-only* (text/interp) and fits the print width — even
when the author deliberately wrote it across multiple lines. Example:

```gsx
{ if props.viewing != nil {
	{ *props.viewing }
} }
```

is reformatted to

```gsx
{ if props.viewing != nil { { *props.viewing } } }
```

Users expect the formatter to keep the vertical layout they chose, the same way
`gofmt` keeps a composite literal multi-line when its first element sits on a
line below the opening `{`.

## Root cause

`internal/printer/printer.go` decides a children list's layout purely from
*content*:

- `cfBody` (control-flow bodies) and `element` force a break only when the body
  has a **block-level child** (`hasBlockChild`). An inline-only body stays on one
  line and breaks only on width overflow.

The printer never consults the *source layout*. There is no signal recording
that the author put a line break after the opening delimiter.

## Rule (what we add)

Preserve the author's vertical layout: **if the source places a line break
immediately after the container's opening delimiter, the body stays
block-formatted (each segment on its own line), regardless of width.**

Opening delimiters, by container:

| Container                         | Delimiter        |
|-----------------------------------|------------------|
| `{ if COND {` … `}` (then body)   | the body `{`     |
| `… else {` … `}` (plain else)     | the else `{`     |
| `{ for CLAUSE {` … `}`            | the body `{`     |
| `<tag …>` … `</tag>` (children)   | the opening `>`  |
| `<>` … `</>` (fragment children)  | the `>` of `<>`  |

Out of scope (unchanged): `switch` (already always breaks), `component` body
(already always breaks), raw-text `<script>`/`<style>`/`pre`/`textarea` bodies
(verbatim), and attributes (always inline). `else if` is a nested `IfMarkup` and
carries its own then-flag.

"Line break immediately after the delimiter" = the contiguous whitespace run
following the delimiter contains a `\n`/`\r` before the next non-whitespace byte.
This is checked against the **whitespace run**, not the first child's start line,
because a leading text child's `Pos()` begins *at* the newline (the newline is
part of the text), so a line-number comparison would miss it.

## Design

Record the fact at parse time; consume it in the printer. This mirrors the
existing precedent where the printer preserves author whitespace intent from
AST-carried source facts (`GoChunk.Src` → `endsWithBlankLine` for blank lines
between declarations).

### AST (`ast/ast.go`) — new exported bool fields

- `IfMarkup.ThenMultiline bool` — a line break follows the then-body `{`.
- `IfMarkup.ElseMultiline bool` — a line break follows a plain-else `{` (unused
  for `else if`).
- `ForMarkup.BodyMultiline bool` — a line break follows the for-body `{`.
- `Element.ChildrenMultiline bool` — a line break follows the opening tag `>`.
- `Fragment.ChildrenMultiline bool` — a line break follows the `<>` `>`.

Exported because `internal/printer` (a different package) reads them; consistent
with existing exported position fields (`CondPos`, `CloseNamePos`).

### Parser (`parser/markup.go`)

Helper: `newlineFollows(src string, off int) bool` — true iff the whitespace run
starting at `off` contains a line break before the next non-whitespace byte.

Set each flag at the point the parser advances past the delimiter (all offsets
already known: `braceOff+1` for `if`/`for` bodies, `p.i` after `p.i++` past the
else `{`, the fragment `>`, and the element `>`).

### Printer (`internal/printer/printer.go`)

OR the new flag into the existing force-break condition, **after** the
edge-safety guards (so a body that leads/trails a significant space still stays
inline for correctness — the flag is ignored there):

- `cfBody`: `if hasBlockChild(nodes) || multiline { … BreakParent … }`
  (pass `multiline` in from `ifChain`/`forMarkup`).
- `element`: `if hasBlockChild(e.Children) || e.ChildrenMultiline { force = BreakParent }`.
- `fragment`: force-break when `f.ChildrenMultiline` (fragment currently has no
  block-child force-break; add the author-newline one).

### wsnorm

No change: `Normalize` mutates nodes in place (same pointers), so the new bools
survive. It may drop a leading all-whitespace text child, but the flag was
computed from raw source pre-normalization and is unaffected.

## Idempotency

`format(format(x)) == format(x)` holds:

- Author newline → flag true → printer breaks → output has a newline after the
  delimiter → re-parse sets flag true → stays broken.
- No author newline → flag false, fits width → inline → re-parse flag false →
  stays inline.
- Author newline but body is edge-unsafe → printer keeps inline (correctness) →
  output has no post-delimiter newline → re-parse flag false → stable inline.

The force-break only fires in already-edge-safe branches, so no cosmetic newline
ever absorbs a significant space.

## Tests

Corpus cases (per context) under `internal/corpus/testdata/cases/`:

- `control_flow/if_multiline_preserved` — inline-only then body, author-broken → stays multi-line.
- `control_flow/if_inline_preserved` — inline-only then body, author-inline → stays inline.
- `control_flow/if_else_multiline_preserved` — else body author-broken.
- `control_flow/for_multiline_preserved` — for body author-broken.
- `elements/children_multiline_preserved` — element children author-broken, inline-only.
- `elements/fragment_multiline_preserved` — fragment children author-broken.

Printer unit tests (`internal/printer/printer_test.go`): idempotency +
edge-unsafe-stays-inline for the new cases. Parser unit test for
`newlineFollows` / flag wiring.

## Siblings

No syntax change — grammar is identical. tree-sitter-gsx / vscode-gsx /
website need no update.
