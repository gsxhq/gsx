# gsx Parser — Brace-Matching Soundness Fix

**Status:** design approved (sequencing + I2 fidelity); ready for implementation plan.

## Problem

The parser uses `go/scanner` for brace/paren matching in several helpers
(`goExprEnd`, `parenEnd`, `scanToBlockBrace`, `scanToCaseColon`,
`splitComposed`). The binding invariant is "`go/scanner` only ever runs over
**pure-Go** regions, never markup prose." That invariant is **violated**: these
helpers tokenize markup text, and an apostrophe (or any lone `'`/`` ` ``/`"`) in
prose opens a Go rune/string literal that swallows source to end-of-line —
eating the very brace they are trying to match.

Two confirmed failure shapes (live repros):

- **C1 (Critical, pervasive, pre-exists on `main`).** Any `{…}` construct after
  an apostrophe in the same scanned region breaks. Examples that fail today:
  - `<p>Today's items: {n}</p>` → `3:21: unterminated \`{\``
    (`parseInterp` → `goExprEnd` scans the whole region from offset 0, so the
    prose `Today's` desyncs the scanner before it reaches `{n}`).
  - `component C() { { if c { <p>it's here</p> } } }` → `unterminated component
    body` (`component` body bounding via `goExprEnd` over the markup body).
  - The apostrophe only bites when a `{` or `}` that must be matched appears
    **on the same line, after** the `'`. `<p>it's fine</p>` alone parses,
    because a Go rune literal terminates at the newline before the closing
    brace — which is exactly why existing tests (no same-line apostrophe+brace)
    missed it.

- **I2 (Important, Part-2-introduced).** `scanToBlockBrace` assumes composite
  literals in control-flow headers are always paren-wrapped, so it takes the
  `{` of a bare composite literal as the body brace:
  - `{ for _, v := range []int{1,2} { … } }` →
    `expected \`}\` to close \`{ for … }\`` (valid Go, wrongly rejected).
  - Same for `range map[string]int{…}`, `range [N]T{…}`.

## Root-cause framing

Brace handling must be split by **what lies between the delimiters**:

| Region | Content between delimiters | Safe technique |
|--------|----------------------------|----------------|
| `{ expr }` interpolation, `{{ stmt }}`, `{ ...e }` spread, `class={ … }`, `(recv)`, `(params)`, control-flow headers, case lists | **pure Go** | `go/scanner`, but scan **from the opening delimiter** (never from offset 0, so preceding prose is never tokenized) |
| `component X() { …markup… }` body, `name={ <markup/> }` markup attribute | **markup** | do **not** use `go/scanner` at all — parse recursively until the top-level `}` |

## Design

### A. Pure-Go delimiter matching — scan from the opening delimiter

`goExprEnd(src, open)` and `parenEnd(src, open)` currently `s.Init` over the
entire `src` and skip tokens with `off < open`. Change them to tokenize the
slice **starting at `open`** and translate offsets back (`return open + off`).
For synced input the result is identical; for input with preceding prose the
desync is eliminated because the prose is never scanned. The content between
the delimiters in every remaining caller of these two helpers is pure Go (or a
`//` / `/* */` comment, which `go/scanner` consumes as a single token even when
it contains apostrophes), so the scanner stays synced.

Callers that remain on `goExprEnd` after change B: `parseInterp`, `parseGoBlock`
(both braces), `parseSpreadAttr`, `parseComposedAttr`, `skipBracedComment`.
Callers that remain on `parenEnd`: component receiver and params.

### B. Markup-bounded regions — recursive parse, no scanner

Three sites feed `go/scanner` over **markup**. All must stop using it.

**B1 / B2 — component body and markup attribute (brace-bounded markup).** Both
already sub-parse the inner content; switch them to parse **in place** until the
matching top-level `}`, which the existing `parseControlBody` already does (it
parses markup children — text/elements/`{…}` constructs/`{{ }}`/comments via
`parseBraceNode` — and stops at and consumes the first top-level `}`):

- **Component body** (`parser/component.go`): after locating the body `{`,
  advance past it and call the in-place markup-until-`}` parser instead of
  `goExprEnd` + `newSub` + `parseNodesUntilEOF`. Parsing in place keeps
  positions correct without the substring/sub-parser indirection. The
  whitespace behavior is unchanged (the current `parseNodesUntilEOF` already
  skips inter-node whitespace, as does `parseControlBody`).
- **Markup attribute** (`parser/markup.go` `parseAttrBraceValue`, the markup
  branch): replace `goExprEnd` + `newSub` + `parseNodesUntilEOF` with the same
  in-place markup-until-`}` parse.

Implementation note: `parseControlBody`'s EOF error says "control-flow body".
For these callers a more accurate message is warranted (e.g. "unterminated
component body" / "unterminated markup attribute"). Either parameterize the
terminator-error text or wrap with a context-specific check. A literal top-level
`}` in body text remains the terminator (consistent with control bodies and the
existing design); a `}` inside a nested element's text is literal (parsed by
`parseChildren`/`parseText`, which do not treat `}` as special).

**B3 — file-level component discovery (`parser/file.go`).**
`topLevelComponentOffsets` scans the **entire file** with `go/scanner` to find
`component` keywords at brace depth 0 — so it tokenizes every component body's
markup, and a same-line apostrophe+brace desyncs its depth count, which can drop
or misplace a later component. Replace the whole-file pre-scan with an
**interleaved** walk:

- Maintain a `cursor` (starts after the package clause).
- Find the next top-level `component` by scanning Go tokens **from `cursor`**
  only (a new `nextTopLevelComponent(src, from) (off int, found bool)` using
  `go/scanner` with brace-depth tracking, returning the first depth-0
  `component` IDENT offset or `found=false`). The region `cursor`→that offset is
  a **pure-Go gap** (imports / funcs / type decls), so the scanner never sees
  markup: component bodies live *after* the `component` keyword and are consumed
  by the recursive parse below, never by this scan.
- Emit the gap text as a `GoChunk`, then `parseComponent()` in place (B1
  consumes the body recursively, advancing `p.i` past the body `}`), then set
  `cursor = p.i` and repeat. Trailing Go after the last component is a final
  `GoChunk`.

This preserves the existing `File.Decls` shape (interleaved `GoChunk` /
`Component`) but computes component boundaries incrementally so the scanner only
ever traverses pure-Go gaps. `topLevelComponentOffsets` is removed.

After changes B1–B3, `go/scanner` is never invoked over a region that can
contain markup prose. The invariant holds.

### C. Full-fidelity control-flow body brace (`scanToBlockBrace`)

Replace the depth-only heuristic with a Go-parser-validated search, giving true
Go fidelity for composite literals (the user-selected option):

- `scanToBlockBrace(src, from)` takes `from` pointing at the **keyword**
  (`if` / `for` / `switch`) so the header text includes it.
- Tokenize from `from`; track paren/bracket depth (`(`/`[` increment, `)`/`]`
  decrement). Enumerate, left to right, each `{` that occurs at paren/bracket
  depth 0 as a **candidate** body brace.
- For each candidate at byte offset `b`, test-parse the header as a Go
  statement with an empty body:
  `parser.ParseFile(fset, "", "package p\nfunc _(){\n" + src[from:b] + "{}\n}", 0)`.
  The **first** candidate that parses without error is the body brace; return
  `b`. If none parse, return `(0, false)`.
- This delegates composite-literal disambiguation to `go/parser` itself.
  Verified discriminations: `for _, v := range []int{1,2} {}` ✓ accepts the
  body brace while `… range []int {}` (truncated at the composite brace) is
  rejected; `range map[string]int{…}`, `if (struct{…}{}).Ok {}`, `if a > b {}`,
  `switch x.(type) {}`, and tagless `switch {}` all resolve to the correct
  brace.

Callers (`parseIfTail`, `parseForMarkup`, `parseSwitchMarkup`,
`parseCondAttrTail`) must pass the keyword-start offset (currently they advance
past the keyword first, then call). Each then takes the header text as
`src[from:bodyBrace]` minus its leading keyword for the captured
`Cond`/`Clause`/`Tag` (unchanged semantics — only the `from` offset moves).

`scanToCaseColon` is **not** changed: its depth tracking already handles
`s[1:2]` slice colons correctly (verified), and case lists never contain markup.

### What does not change

- AST node shapes, `Inspect`, `Fprint`, the public go/ast-parity API.
- The grammar and all Part 2 constructs.
- `splitComposed` scans the pure-Go inner of `class={ … }` and is already
  bounded by `goExprEnd` (which becomes scan-from-open); its own scan starts at
  offset 0 of the **inner** Go string, which contains no markup — unaffected by
  C1. (Confirm during implementation that the inner passed to it is the Go
  contribution list, not markup.)

## Sequencing

Per the approved plan: this soundness fix lands on `main` **first** (it repairs
already-merged parser-core behavior), then the `feat/parser-part2` branch is
rebased onto the corrected `main` and merged. The I2 portion (`scanToBlockBrace`
fidelity) only matters once Part 2's control flow exists; if the fix lands on
`main` before Part 2 merges, the `scanToBlockBrace` change ships with Part 2 (it
is introduced there) — i.e. the C-section change rides with whichever branch
owns `scanToBlockBrace`. Implementation plan will resolve the exact branch split
(A+B are core/`main`; C lives with `scanToBlockBrace`).

## Test obligations

Regression tests (must fail before, pass after):

- C1: `<p>Today's items: {n}</p>`; possessive/contraction before an
  interpolation, a `{{ }}` block, a `class={…}`, a `{ if … }`, and a spread;
  apostrophe inside a control-flow body and inside a nested element; component
  body whose markup contains apostrophes on the same line as later braces.
- B3: a **multi-component** file where an earlier component's body contains a
  same-line apostrophe+brace, followed by a later `component` — the later
  component must still be discovered and parsed (not dropped or folded into a
  GoChunk). Also a file with interleaved Go funcs/types between components still
  splits into the correct `GoChunk`/`Component` sequence.
- I2: `range []int{1,2}`, `range map[string]int{…}`, `range [2]int{…}`; ensure
  paren-wrapped composites in `if` conditions, type switches, tagless switches,
  C-style `for`, and `range ident` all still resolve correctly.
- Negative: genuinely unterminated `{`, `(`, component body, markup attribute,
  and control header still produce clean `line:col:` errors (no panic, no hang).
- The whole existing suite (`go test ./...`) stays green, including the Part 2
  pipeline goldens after rebase.
