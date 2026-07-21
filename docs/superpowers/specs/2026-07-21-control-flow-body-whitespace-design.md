# JSX-consistent whitespace in control-flow block bodies

**Date:** 2026-07-21
**Status:** Approved (design), pending implementation plan

## Problem

Text immediately following an interpolation (or any node) inside a control-flow
block body loses its leading whitespace at **parse time**, before `wsnorm` — the
faithful React/Babel JSXText port — ever runs. Element and fragment bodies do
not have this bug: they capture whitespace verbatim into `ast.Text` nodes and
let `wsnorm` apply the real JSX rule.

Concrete divergence (raw parse, before wsnorm):

| Source | Element body (`parseChildren`) | Control-flow body (`parseMarkupUntilClose`) |
|---|---|---|
| `{ x } - { y }` | `I` `Text " - "` `I` (space kept) | `I` `Text "- "` `I` (**leading space eaten**) |
| `{ x }  -  { y }` | `I` `Text "  -  "` `I` | `I` `Text "-  "` `I` (**all leading ws eaten**) |
| `a { x } b` | `Text "a "` `I` `Text " b "` | `Text "a "` `I` `Text "b "` (**after-hole ws eaten**) |

The eating is asymmetric: whitespace *before* a `{` is preserved, whitespace
*after* a `}` (i.e. leading whitespace of the next text run) is consumed.

### Real-world symptom

`one-learning/ui/layout.gsx` `<title>`:

```gsx
{ if !isProd {
    { env } - { " " }
} }
```

renders `development- Team Management …` (dash glued to `development`), because
the space between `{ env }` and `-` is destroyed by the parser regardless of how
the source is laid out. The formatter is **not** the cause: the one-line and
formatter-reflowed two-line forms parse to the identical (already-stripped) AST.

## Root cause

`parser/markup.go`, `parseMarkupUntilClose` (shared by control-flow bodies and
`name={…}` markup-attribute slots) calls `p.skipSpace()` at the top of **every**
loop iteration:

```go
for {
    p.skipSpace()            // <-- eats leading whitespace of the next text run
    if p.eof() { ... }
    switch {
    case p.peek() == '}': p.i++; return nodes, nil
    case p.peek() == '<': parseElement
    case p.peek() == '{': parseBraceNode
    default: parseTextCtx(true)
    }
}
```

`parseChildren` (elements) and `parseChildren("")` (fragments) have **no** such
`skipSpace()`; whitespace falls through to `parseText`/`parseTextCtx`, becoming a
text node that `wsnorm` later normalizes. `parseMarkupUntilClose` is the single
point of divergence in the parser.

## Design principle

**`wsnorm` is the single source of whitespace truth.** The parser preserves text
verbatim; `wsnorm.Normalize` applies the JSX (React/Babel JSXText) rule
uniformly. Control-flow bodies must obey the same principle as element and
fragment bodies. This is "consistent with JSX" — gsx has no separate whitespace
model, only the one JSX port already in `internal/wsnorm`.

Safety of deferring to wsnorm is established: `wsnorm.Normalize(f)` runs
unconditionally right after parse on **both** consumer paths —
`internal/codegen/module_importer.go:1755` (codegen) and
`internal/gsxfmt/gsxfmt.go:95` (fmt). Any raw whitespace text node the parser now
emits in a block body is normalized identically to element bodies before either
codegen or printing. `wsnorm` already recurses `IfMarkup.Then/Else`,
`ForMarkup.Body`, and `SwitchMarkup` cases with the element-body rule; no wsnorm
change is required.

## The fix

Restructure `parseMarkupUntilClose` to mirror `parseChildren`: remove the
per-iteration `skipSpace()` and let whitespace fall into `parseTextCtx(true)`
(which already stops at the terminating `}`). Dispatch order:

```go
for {
    if p.eof() { return err("unterminated ...") }
    if p.peek() == '}' { p.i++; return nodes, nil }
    if p.peek() == '<' { parseElement; continue }
    if p.peek() == '{' { parseBraceNode; continue }   // comments/interp/control-flow
    nodes = append(nodes, p.parseTextCtx(true))        // captures leading whitespace
}
```

Whitespace before `}` becomes a trailing text node (wsnorm drops it if it
contains a newline, keeps a single space otherwise); whitespace between two
structural nodes becomes an inter-node text node — exactly as in element bodies.

Scope: one function, ~5 lines. Fragments and elements are already correct and
unchanged. Markup-attribute slots (also routed through `parseMarkupUntilClose`)
gain the same JSX-consistent behavior, which is the desired outcome.

## Intentional behavior change (must be pinned)

Whitespace-only control-flow bodies change:

- `{ if x {   } }` (inline spaces, no newline): was empty → now renders a single
  space, **consistent** with `<span>   </span>` rendering a space.
- `{ if x {\n} }` (newline): still collapses to empty (wsnorm drops
  newline-bearing whitespace) — real-world code is unaffected.

This is the single deliberate render change and gets its own corpus case with a
comment explaining the consistency rationale.

## Testing

Strategy: **React `renderToStaticMarkup` as the dev-time oracle → pinned gsx
goldens.** No node/React dependency in CI (corpus stays canonical, runtime stays
std-lib-only). A throwaway differential script renders each scenario through both
React and gsx and asserts byte-equality; the confirmed outputs are committed as
gsx txtar corpus goldens.

### Scenario matrix (JSX whitespace rules)

Each scenario is instantiated **per context**: element body (baseline), `if`,
`else`, `else if`, `for`, `switch`-case, fragment, and markup-attribute slot.

| # | Source shape | JSX rule |
|---|---|---|
| S1 | `{ x } - text` (one line) | space after hole kept |
| S2 | `text - { x }` | space before hole kept |
| S3 | `{ x } { y }` | single inter-hole space kept |
| S4 | `{ x }   -   { y }` | multiple spaces collapse to one |
| S5 | `{ x }\n  - { y }` | newline-indentation dropped (no space) |
| S6 | leading/trailing body whitespace with newline | dropped |
| S7 | leading/trailing inline whitespace (no newline) | kept as one space |
| S8 | `<a/> <b/>` inline vs newline-separated | inline space kept / newline dropped |
| S9 | whitespace-only body (see intentional change) | inline → space, newline → empty |
| S10 | nested control-flow + text | recursion consistency |

### Test artifacts

- **Corpus (`internal/corpus`):** txtar cases (`input.gsx` +
  `generated.x.go.golden` + `render.golden`) for the matrix per context, plus a
  regression case reproducing the `layout.gsx` title. Regenerate with
  `go test ./internal/corpus -run TestCorpus -update`, verify without `-update`.
- **fmt corpus (`internal/gsxfmt`):** a case proving one-line ↔ block reflow of
  `{ x } - { y }` stays render-identical (idempotence / semantic preservation).
- **Parser unit tests (`parser`):** assert block-body raw parse == element-body
  raw parse for the matrix (guards the invariant directly at the parser layer,
  independent of wsnorm).
- **wsnorm unit tests:** existing tests retained (they already pass; they were
  never the bug).

## Siblings & docs

Whitespace-semantics only — **not** a grammar/token change. `tree-sitter-gsx`,
`vscode-gsx`, and CodeMirror highlighting tokenize whitespace no differently and
need no updates. One prose note in the guide's whitespace/JSX section stating
control-flow bodies follow the same JSX whitespace rule as element bodies.

## Verification

- `make check` green.
- New corpus + fmt-corpus goldens regenerated, then verified without `-update`.
- Throwaway differential script confirms gsx render == React render across the
  full matrix before goldens are pinned.
- `layout.gsx` title confirmed end-to-end in the one-learning app (renders
  `development - Team Management …`).

## Out of scope

- No changes to the JSX rule itself (`wsnorm` is already the faithful port).
- No formatter behavior change (it is already semantics-preserving here).
- No grammar/highlighting/sibling-repo changes.
