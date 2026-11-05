# Whitespace normalization: HTML collapse with one exception

**Status:** Implemented (PR #274). Supersedes the JSX edge-dropping rule of
`2026-06-21-gsx-whitespace-model-design.md` and the `child-element-spacing` lint.

## Problem

JSX-style normalization dropped every newline beside text, so
`Hello⏎<b>world</b>` rendered `Hello<b>world</b>`. Users hit this constantly;
the lint only warned.

## Rules

1. A whitespace run containing a newline is layout and is dropped when it sits
   between two non-text siblings or at a children-list edge.
2. Every other ASCII whitespace run becomes one space.
3. Expression values (`{" "}`, strings) and non-ASCII spaces are data.

No tag, component, punctuation, CSS or branch-output classification. Braced
and HTML comments and Go blocks are transparent: text around them is one run,
and its single space stays at the run's first whitespace position. Padding
inside component, slot and control-flow braces is trimmed (through transparent
nodes). `pre`/`textarea`/raw `script`/`style` are verbatim.

Consequences, accepted: `{v}⏎%` renders `42 %`; a source space beside a branch
that renders nothing stays (`Hello !`). Authors join content by adjacency.

## Implementation

- `wsnorm.normalizeTextRegion` scans each run of Text plus transparent nodes;
  the parser keeps body whitespace uniformly and `trimBodyEdges` trims braces.
- The formatter bonds every Text/sibling boundary (`TextBoundarySibling`) so
  it never introduces whitespace at an adjacent boundary. Wrapping at existing
  text/markup spaces is a tracked ROADMAP debt.
- Corpus: `whitespace/*` pins render bytes exactly (`internal/rendergolden`).
