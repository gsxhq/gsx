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

## Migration lints and `gsx fix whitespace` (#276)

Earlier `gsx fmt` versions broke lines before `. , ; :` because that newline
rendered nothing; `{ " " }` was the idiom for a space beside a newline. Under
the new rule both change output.

- `wsnorm.scanRegion` is the single whitespace rule: `Normalize` and `Lint`
  consume its word/run events, so a finding is always whitespace that renders.
- Both lints concern a *layout run*: it renders a space and every part of it
  contains a line break (a same-line space anywhere in the run is authored and
  never flagged). Facing a string literal whose value has a space on that
  side (`{ " " }`, `{ " and " }`; also as the first/last node of an
  `if`/`for`/`switch` body, branches not evaluated) it is
  `redundant-space-literal`; before
  `.,;:!?)]’”%` or after `([“‘` (entities decoded) it is
  `punctuation-line-break` — an explicit closed set used only by the lint,
  never by normalization. The fix deletes the run, so an author's `{ " " }` is
  kept and fixes never cascade.
- `Finding.Migrate`: every part is a line break at a Text edge, which v0.4
  dropped; a line break inside one Text always rendered a space. `gsx fix
  whitespace` applies only Migrate findings, so it restores v0.4's output
  exactly (pinned against an in-test port of the v0.4 rule); the editor offers
  every fix.
- Findings are computed on the pristine parse (cached with it) and reported
  only for generated packages. `internal/wsfix` (parse → Lint → apply until
  none) backs `gsx fix whitespace` and the LSP quick fixes.
- Limitation: markup nested inside a Go expression (`{ f(<p>…</p>) }`) is split
  only by codegen and is not linted.
- EOS (674 files, pre-upgrade sources): `gsx fix whitespace` makes 61 fixes in
  33 files; all 55 changed generated spans equal v0.4.5's output bytes.
