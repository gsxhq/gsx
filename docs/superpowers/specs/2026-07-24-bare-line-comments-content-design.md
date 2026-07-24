# Bare `//` line comments in child content

**Date:** 2026-07-24
**Status:** Approved design, not yet implemented

## Summary

A `//` that is the first non-whitespace on its source line, in child-content
position, starts a source-only line comment running to end of line — but only
when the comment does not touch text content. A bare comment line adjacent to
text is a hard error. Whitespace-significant and raw-text subtrees are exempt.

Previously rejected because `//` is valid HTML text and JSX has no such form.
Revisited: `//` lines are rarely intended markup, authors overwhelmingly mean
"comment", and `{"//"}` renders the literal when needed. This design goes
beyond JSX (React never treats `//` in JSX text as a comment), so the rule is
ours to define — and it is defined strictly so it can be relaxed later.

## Recognition rule

In child-content position, `//` starts a bare line comment iff every character
before it on its source line is whitespace (`\r` counts as whitespace, so CRLF
files behave identically). The comment runs to end of line.

Never a comment:

- Mid-line `//`: `hello // world`, `<p>// hi</p>` — literal text.
- `/* … */` in content — no bare block form; `{/* … */}` remains the only
  block spelling between children.
- Inside exempt contexts (below).

## Legality: comments never touch text

A bare comment line is legal only when its nearest non-whitespace siblings on
**both** sides are non-text: tags, holes (`{expr}`, `{"…"}`), control flow,
other comments (braced, bare, or HTML), doctype, or the element/body boundary.

If either neighbor is a text word, generation fails with an error positioned
at the `//`:

```
bare // comment cannot touch text content; use {// …} for a comment or {"// …"} to render it
```

No carve-outs: an empty `//` line next to text is the same error. A comment at
the top of a paragraph followed by text errors too — "comments never touch
text" is one sentence with no exceptions.

```gsx
<div>
  <span>a</span>
  // legal — between tags
  <span>b</span>
</div>

<p>
  hello
  // error — touches text on both sides
  world
</p>
```

Because legal comments only ever occupy inter-node whitespace that wsnorm
already drops, rendering around a legal comment is byte-identical to deleting
the comment line. The feature has zero interaction with word-gap/whitespace
semantics, including the planned Fill-based reflow (prose runs are
comment-free by construction).

### Reserved for relaxation

The error deliberately reserves the ambiguous position. If ever relaxed, the
relaxation direction is **render as text** (`<p>hello\n// xyz\nworld</p>` →
`<p>hello // xyz world</p>`), not "become a comment": error→text is
backward-compatible later; either silent behavior now would be permanent.
Non-normative; recorded so the future discussion starts from this intent.

## Exempt contexts

- **`<script>` / `<style>`**: raw-text parse path, untouched — `//` there is
  JS/CSS.
- **`<pre>` / `<textarea>` subtrees**: no recognition and no error; `//` lines
  are verbatim display content. Uses the same tag set wsnorm uses for
  whitespace significance — one shared table, not a second list.
- Components that render into a `<pre>` are undetectable; same documented
  limitation wsnorm has.

## Where it applies

Uniformly in every child-content context: element bodies, component children,
control-flow bodies (`{if}`/`{for}`/`{switch}` and case bodies), fragment
literals, child-prop values, element literals in Go-expr position. Attribute
position is unchanged (bare `//` already works there).

## Implementation shape

- The text lexer (`parseTextCtx`) additionally stops at a line-start `//`,
  suppressed inside significant subtrees — the parser threads a significance
  flag down `parseChildren`, seeded from the shared tag table.
- Parser emits the existing `ast.Comment` node with a new `Bare bool` field;
  codegen already emits nothing for `ast.Comment`, so generated output shape
  is unchanged.
- The adjacency check runs where the sibling list is complete and reports
  through the normal diagnostics bag, so corpus `diagnostics.golden` pins it.
- LSP: diagnostics surface automatically via the shared analyze path; there
  is no semantic-tokens feature, so editor comment coloring comes from the
  sibling grammars (TextMate / tree-sitter / CodeMirror).

## Formatter

`gsxfmt` prints a bare comment on its own line at body indent. It never
converts between `{// }` and bare forms — the author's spelling is a layout
fact. Fmt-corpus cases ship with the change.

## Compatibility

Behavior change, accepted deliberately:

- Bare `//` line between tags: previously rendered as text, now a comment
  (disappears). Intentional content of that shape is vanishingly rare;
  `{"//"}` renders it.
- Bare `//` line touching text: previously rendered, now errors loudly —
  the author chooses `{// …}` or `{"// …"}`.

## Testing

- Semantic corpus: a case per child-content context (standing rule), pinning
  `input.gsx` + `generated.x.go.golden` + `render.golden`; error cases pin
  `diagnostics.golden`.
- Edge cases covered: mid-line `//` stays text; empty `//` next to text
  errors; consecutive comment lines; comment as sole child; neighbors of each
  non-text kind (tag, hole, control flow, braced/HTML comment); pre/textarea
  subtree verbatim (including nested `<pre><code>`); script/style untouched;
  CRLF input.
- Fmt corpus: layout cases for bare comments (indent, preservation,
  no-conversion).
- wsnorm unit tests: render byte-identical to comment-line deletion.

## Sibling projects

- `tree-sitter-gsx`: line-comment rule in content position.
- `vscode-gsx`: TextMate grammar.
- `gsxhq.github.io`: CodeMirror + VitePress highlighting.
- Docs: `docs/guide/syntax/comments.md` — bare `//` between child nodes
  becomes source-only; note the touch-text error and both escape hatches.
