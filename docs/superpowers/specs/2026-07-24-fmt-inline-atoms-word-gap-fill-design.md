# gsx fmt: inline atoms + word-gap fill

Date: 2026-07-24. Status: approved design, pre-implementation.

## Problem

The printer treats a glued run (children joined by significant spaces) as one
unbreakable line. In long prose the only break candidate is an inline element's
children group, so the printer explodes `<code>x</code>` across three lines —
a break that cannot fix the overflow (the glued tail stays over budget) and
ruins reading flow. Three related defects share the root cause:

1. **Inline explosion.** `fits` charges the whole glued tail to the one
   breakable group in range (`internal/printer/printer.go` element children
   group), so a small `<code>` breaks open whenever long glued text follows it.
2. **Forced parent break.** Every element child is "block-level"
   (`segment.go` `blockLevel`), so `<p>vendors real <code>.gsx</code>
   source.</p>` written on one line is forced open even when it fits.
3. **No legal wrap.** Authors wrap prose by hand; the printer joins those
   word-gap breaks back into a 200-column line. Worse, the printer's own output
   style (breaks adjacent to tags) teaches authors a habit that silently drops
   render-significant spaces — wsnorm, like JSX, removes newline runs adjacent
   to tags but collapses newline runs *between two words of one Text node* to
   a single space. Word-gap breaks are render-free; the printer just never uses
   them.

Also: the `{" "}` spacing idiom is laid out as an orphan `{ " " }` line.

Verified 2026-07-24: fmt is render-preserving today (gsxui `getting_started`
pre/post fmt renders byte-identical); this work changes only layout quality,
and must keep that property.

## Decisions (user-approved)

1. **Inline atoms**: phrasing-content tags are atoms in text flow — width never
   breaks their children open. Fully atomic: no wrapping inside an atom's own
   text either; a line that still cannot meet budget stays long.
2. **Atoms do not force the parent open**: an all-inline children list stays on
   one line when it fits. A true block child still forces the break.
3. **Canonical fill**: prose re-wraps greedily at the width budget
   (gofmt-style single canonical layout). Author word-gap break points are not
   preserved; author structure breaks (`ChildrenMultiline`, `AttrsMultiline`)
   still are.
4. **`{" "}` is spacing glue**: it bonds to the previous leaf (`see{" "}` ends
   a line) and offers a break after.

## Specification

### Inline tag set

Ported from prettier's HTML inline-elements list, minus gsx preserve tags
(`textarea`; `script`/`style` were never in it):

```
a abbr acronym b bdo big br button cite code dfn em font i img input kbd label
map object output q samp select small span strong sub sup time tt u var video audio
```

Lowercased comparison, HTML tags only — components (uppercase or dotted) are
never inline.

### Atom predicate

An element is an **atom** iff:

- its tag is in the inline set, and
- every child is Text, Interp, or an atom (recursively), and
- its doc contains no forced break: no `//` line-comment attr, no author
  `AttrsMultiline`, no author `ChildrenMultiline` anywhere in the subtree
  (author layout outranks atom status — a `<code>` the author wrote multi-line
  renders multi-line and is treated as block).

Void inline tags (`br`, `img`, `input`) with no children are atoms. An atom
renders its whole subtree flat, unconditionally.

### Spacing interp

An `Interp` whose expression is a single Go string literal (interpreted or raw)
whose value consists only of ASCII spaces. Layout only — codegen is untouched.

### Leaf stream and joints

A children list flattens to leaves: **words** (maximal non-whitespace runs of
Text values), **atoms**, **plain interps** (`{expr}`), **spacing interps**.
Every joint between adjacent leaves is one of:

| joint | breakable | flat | broken | where |
|---|---|---|---|---|
| bond | no | as-is | — | significant space touching a non-word leaf (`real <code>`, `count: {n}` — a break there would drop the space at render); left side of a spacing interp (`see{" "}`) |
| word gap | yes | `" "` | newline+indent | space between two words of one Text node |
| safe gap | yes | `""` | newline+indent | direct adjacency (`</code>.` — a break there drops nothing at render, and greedy fill keeps punctuation attached in practice); right side of a spacing interp |

Bonded leaves form **clusters**. The list's inline layout is
`pretty.Fill(cluster, gap, cluster, …)` with `pretty.Line` for word gaps and
`pretty.SoftLine` for safe gaps (`fillStep` already implements greedy packing).
Flat output is byte-identical to the normalized one-line form; broken output
only inserts newline+indent at gaps, which wsnorm collapses back — render
faithfulness and idempotence hold by construction.

### Author breaks at markup boundaries (amendment 2026-07-24b)

Canonical fill reflows **prose** — but it must never join a break the author
placed **next to markup**. Historically that preservation held by accident
(every element was block-level, so siblings and case arms always got their
own lines); atoms removed the accident, so the facts are now recorded
explicitly, extending the existing layout-fact mechanism:

- `CaseClause.BodyMultiline` — the source placed a line break after the
  `case …:`/`default:` colon; `caseBody` honors it exactly as `cfBody`
  honors `ThenMultiline` (a case body written inline stays inline).
- **Leading-break fact** on non-Text markup leaves (Element, Interp,
  EmbeddedInterp): the source placed a line break in the whitespace
  immediately before this child. The fill honors it where the joint is a
  safe gap — the separator becomes a preserved (hard) break, which also
  correctly forces the enclosing element open. Bonds are unaffected (a
  significant space adjacent to a tag never coexists with a surviving
  newline: wsnorm drops newline-bearing edge runs). Breaks before **Text**
  nodes carry no fact — prose keeps reflowing canonically.

Net rule: the formatter reflows prose, and never joins an author's break
adjacent to markup. One-liners stay one-liners (no fact → no break).

### List layout

- **Inline-only list** (every child Text/Interp/atom): one Fill. Fits → one
  line; over budget → greedy wrap. `ChildrenMultiline` forces the block form
  (break after `>`, indented Fill, closing tag on its own line) but the Fill
  inside still packs greedily.
- **Block list** (any non-atom element, Fragment, If/For/Switch, GoBlock,
  Doctype, HTMLComment): unchanged structure — forced open, one segment per
  line — but each segment's interior is a Fill, so prose inside still wraps at
  word gaps.
- **Edge-unsafe list** (leading/trailing significant space): fully inline,
  unchanged from today.
- Preserve subtrees (`pre`, `textarea`, `script`, `style`) untouched, as today.

`blockLevel()` changes to: block iff not (Text | Interp | atom element).

## Non-goals

No wrapping inside atoms — and "inside" includes the opening tag: an atom
renders fully flat, attribute list included, so a solo inline element with a
long dynamic attr stays on one long line (this is decision 1's "fully atomic"
applied consistently; it also matches the motivating complaint, where
breaking an `<a>`'s attrs mid-prose was the problem). Attr-value wrapping for
non-atom elements is unchanged. No `{" "}` insertion/materialization by the
formatter. No syntax change (tree-sitter, vscode-gsx, CodeMirror unaffected).
No codegen change (semantic corpus goldens unaffected).

## Testing

- **fmt corpus** (`internal/gsxfmt/testdata/cases`), new cases: long-prose
  word wrap; atom stays inline under pressure; atom with author-multiline
  child falls back to block; `{" "}` glue at line end; mixed block+inline
  list; edge-unsafe list; inline-only one-liner stays one line; preserve tags.
  Flagship: a `getting_started.gsx`-style page — the hand-written word-gap
  style must round-trip to itself or its canonical fill.
- **Existing goldens** reflow; regenerate with `-update`, verify with the
  render-faithfulness property harness (`internal/printer` corpus property
  tests) and idempotence — every changed golden must render byte-identically.
- Examples drift gate (`examples/*.txtar`) may need a regen.
- Docs: fmt behavior section in `docs/guide/**` (concise; behavior only).

## Implementation shape

`internal/printer/segment.go` grows the leaf/joint model (replacing
segment-only glue analysis); `printer.go` `element()`/`childrenInner`/
`blockLevel` consume it; `internal/pretty` needs no changes (`Fill`,
`Line`, `SoftLine` exist). `fits` interaction: a Fill inside a broken group is
already handled (`fillStep` measures per pair). Atom detection lives beside
`blockLevel` in the printer — the analyzer/diagnostics are untouched
(formatter never diagnoses, per repo doctrine).
