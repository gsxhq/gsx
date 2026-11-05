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
- `punctuation-line-break`: a newline-bearing run that renders before
  `.,;:!?)]’”%` or after `([“‘` (entity spellings decoded). An explicit
  closed set, used only for the lint, never for normalization. Fix: delete the
  run. A same-line space is the opt-out.
- `redundant-space-literal`: `{ " " }` facing a rendering newline run. Fix:
  delete the literal when a source sibling lies beyond it (the run keeps one
  space), else delete the run; beside punctuation, keep the literal and delete
  the run. Every fix reproduces the old rule's output at that boundary.
- Findings are computed on the pristine parse (cached with it) and reported
  only for generated packages. `internal/wsfix` (parse → Lint → apply until
  none) backs `gsx fix whitespace` and the LSP quick fixes.
- Limitation: markup nested inside a Go expression (`{ f(<p>…</p>) }`) is split
  only by codegen and is not linted.
- EOS (674 files, pre-upgrade sources): 30 + 13 findings in 26 files, no false
  positives; all 36 changed generated literals equal v0.4.5's output bytes.
