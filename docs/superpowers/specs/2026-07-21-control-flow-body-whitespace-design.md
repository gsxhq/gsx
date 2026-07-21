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

## Chosen model (Option B: trim brace-interior edges)

Two things must both hold:

1. **Interior whitespace follows the JSX rule.** Whitespace *between* nodes
   inside a control-flow body (e.g. the ` - ` in `{ env } - { bar }`) is
   preserved/collapsed exactly as `wsnorm` (the React/Babel JSXText port) does
   for element and fragment bodies. This is the bug fix.
2. **Whitespace immediately inside the control-flow braces is ignored.** The
   space just after the body-opening `{` and just before the body-closing `}` is
   syntactic — trimmed — exactly as gsx already trims the interior of `{ expr }`
   interps and `{{ code }}` Go-blocks. This matches JSX conditionals, which are
   expressions (`{cond ? <x/> : …}`) that introduce no surrounding text.

So `<div>{ if c { <span/> } }</div>` renders `<div><span></span></div>` (no edge
spaces), while `{ env } - { bar }` preserves the spaces around the hyphen.

### Why not full element-body parity (Option A)

Option A — treat a control-flow body byte-identically to a `<div>` body, keeping
single-line brace-interior spaces (`<div> <span/> </div>`) — was prototyped and
rejected. It changes ~20 existing corpus goldens (single-line blocks gain edge
spaces), makes whitespace-only bodies render a space, and surprises authors who
read `{ if c { … } }` braces as syntactic. Option B changes **zero** existing
corpus goldens and matches gsx's own brace conventions.

## The fix

`parseMarkupUntilClose` is shared by **two** callers with different needs:

- **Control-flow bodies** (`parseControlBody`, used by `{ if/else }`, `{ for }`,
  `{ switch }` cases) — real markup child lists; interior whitespace must be
  preserved for `wsnorm`.
- **Markup-attribute slots** (`name={ <Icon/> }`, `parseMarkupUntilClose(
  "markup attribute")` at markup.go:685) — single-value slots. Consumers do
  `Value[0].(*ast.Element)`; a leading/trailing whitespace `*ast.Text` node makes
  them **panic** (proven by prototype). Attribute slots MUST keep the current
  whitespace-skipping behavior.

Therefore the fix is scoped, not a blanket `skipSpace()` removal:

1. Add a `preserveWS bool` parameter. `parseMarkupUntilClose(what)` keeps its
   current signature and delegates with `preserveWS=false` (attribute slots,
   unchanged). The loop skips inter-node whitespace only when `!preserveWS`.

   ```go
   func (p *parser) parseMarkupUntilClose(what string) ([]ast.Markup, error) {
       return p.parseMarkupUntilCloseWS(what, false)
   }
   func (p *parser) parseMarkupUntilCloseWS(what string, preserveWS bool) ([]ast.Markup, error) {
       var nodes []ast.Markup
       for {
           if !preserveWS {
               p.skipSpace()
           }
           if p.eof() { return nil, p.errorf(p.pos(), "unterminated %s, expected `}`", what) }
           switch {
           case p.peek() == '}': p.i++; return nodes, nil
           case p.peek() == '<': /* parseElement */
           case p.peek() == '{': /* parseBraceNode */
           default: nodes = append(nodes, p.parseTextCtx(true))
           }
       }
   }
   ```

2. `parseControlBody` calls it with `preserveWS=true`, then trims the two
   brace-interior edges:

   ```go
   func (p *parser) parseControlBody() ([]ast.Markup, error) {
       nodes, err := p.parseMarkupUntilCloseWS("control-flow body", true)
       if err != nil { return nil, err }
       return trimBodyEdges(nodes), nil
   }

   // trimBodyEdges strips whitespace immediately inside the control-flow body
   // braces: the leading whitespace of the first node and the trailing whitespace
   // of the last node, when those nodes are Text. Interior whitespace is left for
   // wsnorm's JSX rule. An emptied edge Text node is dropped.
   func trimBodyEdges(nodes []ast.Markup) []ast.Markup {
       if len(nodes) > 0 {
           if t, ok := nodes[0].(*ast.Text); ok {
               t.Value = strings.TrimLeft(t.Value, " \t\r\n")
               if t.Value == "" { nodes = nodes[1:] }
           }
       }
       if len(nodes) > 0 {
           if t, ok := nodes[len(nodes)-1].(*ast.Text); ok {
               t.Value = strings.TrimRight(t.Value, " \t\r\n")
               if t.Value == "" { nodes = nodes[:len(nodes)-1] }
           }
       }
       return nodes
   }
   ```

`wsnorm.Normalize(f)` still runs unconditionally after parse on both consumer
paths (`internal/codegen/module_importer.go:1755`, `internal/gsxfmt/gsxfmt.go:95`)
and handles the surviving interior text nodes; `wsnorm` needs no change.

## Behavior change (must be pinned)

The **only** render change versus today is the bug fix: interior whitespace that
was being wrongly eaten is now preserved. Prototype confirms **zero** existing
corpus goldens change. Specific pins:

- `{ env } - { bar }` → `env - bar` (interior spaces around the hyphen kept).
- `<div>{ if c { <span/> } }</div>` → `<div><span></span></div>` (edge spaces
  ignored — unchanged from today).
- Whitespace-only body `{ if x {   } }` → empty (both edges trimmed) — unchanged
  from today.
- Multiline bodies (`{ if x {\n <span/> \n} }`) → unchanged (edge whitespace
  contains newlines, trimmed either way).

Ergonomic consequence to document: a trailing separator space that must sit
*outside* a block belongs in the parent context, not at the block's inner edge —
e.g. `{ if !prod { { env } - } } { title }` (space after `}` lives in the parent)
renders `env - title`, whereas the space would be trimmed if placed just before
the inner `}`.

## Testing

Strategy: **React `renderToStaticMarkup` as the dev-time oracle for interior
whitespace → pinned gsx goldens.** No node/React dependency in CI (corpus stays
canonical, runtime stays std-lib-only). A throwaway differential script renders
each *interior* scenario through both React (as an element body) and gsx and
asserts byte-equality; the confirmed outputs are committed as gsx txtar corpus
goldens. Edge behavior is gsx-specific (brace-interior trimming) and is pinned
directly, not validated against React.

### Scenario matrix

Interior scenarios (S1–S5, S10) validate that a control-flow body collapses
*inter-node* whitespace identically to an element body — instantiated per
context: element body (interior oracle), `if`-then, `else`, `else if`, `for`,
`switch`-case, and fragment. Edge scenarios (E1–E4) pin the brace-interior
trimming and are control-flow-specific.

| # | Source shape | Expected |
|---|---|---|
| S1 | `{ x } - text` (one line) | space after hole kept |
| S2 | `text - { x }` | space before hole kept |
| S3 | `{ x } { y }` | single inter-hole space kept |
| S4 | `{ x }   -   { y }` | multiple spaces collapse to one |
| S5 | `{ x }\n  - { y }` | newline-indentation dropped (no space) |
| S10 | nested control-flow + interior text | recursion consistency |
| E1 | `{ if c { <span/> } }` (inline edge spaces) | edges trimmed → `<span></span>` |
| E2 | `{ if c {   } }` (whitespace-only body) | empty |
| E3 | `{ if c {\n <span/> \n} }` (multiline) | edges trimmed → `<span></span>` |
| E4 | `{ if c { { env } - } } { title }` | block → `env -`; parent space kept → `env - title` |

### Attribute-slot guard

A parser test (or corpus case) asserting `name={ <Icon/> }` markup-attribute
slots are **unchanged** — `Value` is `[]ast.Markup{*ast.Element}` with no
surrounding whitespace text nodes, and rendering does not panic. This guards the
`preserveWS=false` scoping decision.

### Test artifacts

- **Corpus (`internal/corpus`):** txtar cases (`input.gsx` +
  `generated.x.go.golden` + `render.golden`) for S1–S5/S10 per context and
  E1–E4, plus a regression case reproducing the `layout.gsx` title (written the
  Option-B-idiomatic way). Regenerate with
  `go test ./internal/corpus -run TestCorpus -update`, verify without `-update`.
- **fmt corpus (`internal/gsxfmt`):** a case proving one-line ↔ block reflow of a
  control-flow body stays render-identical (idempotence / semantic preservation).
- **Parser unit tests (`parser`):** for S1–S5, assert the control-flow body's
  *interior* text nodes match the element-body parse; for E1–E4, assert the
  trimmed edges. The existing `TestParseIf*` tests pass unchanged — their bodies
  are single-line lone-element (no interior whitespace), which trims to the same
  structure the old skip-space produced; new cases cover the interior and edge
  behavior. Add a `trimBodyEdges` unit test.
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
