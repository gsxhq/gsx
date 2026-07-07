# Embedded `<tag>`/`<>` literals inside `{ }` interpolation expressions

**Status:** design · **Date:** 2026-07-07

## Idea

A `{ … }` interpolation is a Go expression. Now that a `<tag>…</tag>` and a
`<>…</>` are legal Go-expression *values* (element literals #42, fragment
literals #44), they should be embeddable **inside** an interpolation too:

```gsx
<div>{ wrap(<>…</>) }</div>              // pass a fragment to a helper, inline
<ul>{ items |> render }</ul>            // already works
<div>{ pick(cond, <A/>, <B/>) }</div>   // choose a node inline
{ maybe(<></>) }                        // the nop, inline
```

Today this fails, and its absence is surprising: the *same* expression works one
level out — `var x = wrap(<>…</>)` compiles, but `{ wrap(<>…</>) }` does not.
This closes that gap so a `<tag>`/`<>` literal is valid in **every** Go-expression
position, including interpolation interiors.

**No type-structure change.** Purely parser (one unified scanner) + a codegen
splice at the interpolation emit/probe sites.

## Why it fails today: two divergent Go-scanners

gsx scans Go source in two places, and only one understands tags:

- **Top-level Go regions** (between component boundaries) are scanned by
  `scanGoElementMarks` (`parser/goexpr.go`) — a `go/scanner`-based walk with
  **operand/operator tracking** that recognizes a `<` at operand position as a
  tag start and skips the whole tag span. This is why `var x = wrap(<>…</>)`
  works.
- **Interpolation interiors** are delimited by a *separate*, **tag-blind**
  family of byte-level scanners in `parser/boundary.go` / `parser/pipe.go`:
  - `goDepth1End` — finds the matching `}` (used by `goExprEnd`, `goStagesEnd`).
  - `composedDelims` — splits an ordered-attrs `{{ }}` literal on depth-0 `,`/`:`.
  - `splitPipe` — splits on depth-0 `|>`.
  - `parenEnd` — matches `()` inside a pipe stage.

  These count `{}()[]` depth and skip strings/runes/comments
  (`skipQuotedOrComment`) and gsx embedded `js`…`/`css`…` literals
  (`skipGSXEmbeddedLiteral`) — but have **no notion of a tag span**. So a
  `<>…</>`'s interior prose (an apostrophe, a `</`, a nested `{ }`) misdirects
  the delimiter scan, and even if delimiting succeeded, `genInterp` emits the
  expression verbatim → `<>` is invalid Go.

The limitation is the divergence. The fix is to **unify**: make interpolation
scanning tag-aware by routing it through the same `go/scanner`-based scanner the
top level already trusts.

## Decision: one `go/scanner`-based expression scanner, taught `|>`

Rather than bolt tag-awareness onto each byte scanner (an operand/operator
heuristic in byte space — which our "no heuristics in core logic" rule forbids),
promote `scanGoElementMarks` into a fuller **expression scanner** and route all
interpolation delimiting through it. In a single operand/operator-aware walk of
the interp source it reports:

- the depth-0 `}` that closes the interpolation (replaces `goDepth1End`),
- depth-0 `|>` pipe-operator positions (replaces `splitPipe`),
- tag/fragment spans (already produced as marks; now consumed for skipping),
- depth-0 `,`/`:` for the ordered-attrs literal (replaces `composedDelims`),

while skipping strings/runes/comments (`go/scanner`), tag spans, and gsx
embedded literals (see wrinkle below). The byte scanners retire or become thin
callers over this one pass.

### `|>` is unambiguous in `go/scanner` (empirically confirmed)

`go/scanner` tokenizes `|>` as `token.OR` immediately followed by `token.GTR`
at the next offset, with **zero scan errors**:

| Source | Tokens | Pipe? |
|---|---|---|
| `x \|> f` | `OR`@2, `GTR`@3 (adjacent) | yes |
| `x\|>f` | `OR`@1, `GTR`@2 (adjacent) | yes |
| `a \| b > c` | `OR`@2, … `GTR`@6 (**not** adjacent) | no — bitwise-or then greater-than |
| `a \|>= b` | `OR`@2, `GEQ`@3 | no — `GEQ`, not `GTR` |

Adjacent `OR`+`GTR` (a `|` immediately followed by `>`) is **not producible by
valid Go** — a bitwise-or always has an operand between it and any `>`; `a|>c`
is a Go syntax error. So "OR token at offset `p`, GTR token at offset `p+1`" is
an exact, false-positive-free pipe marker. This also *hardens* pipe splitting
over the current byte `splitPipe`: a `|>` inside a string literal is a single
`STRING` token and is never miscounted.

### The wrinkle: gsx embedded literals

`go/scanner` mis-tokenizes gsx's escaped embedded literals. `js`a\`b`` scans as
`` IDENT(js) STRING(`a\`) IDENT(b) STRING(`) `` — `go/scanner` ends the raw
string at the escaped backtick because it doesn't know gsx's `\`` escape
convention. The byte scanners handle this via `skipGSXEmbeddedLiteral` +
`embeddedLiteralEnd`; `scanGoElementMarks` currently has **no** such handling
(it has never needed it — top-level Go regions don't host `js`…`/`css`…`, but
interp interiors do). So the unified scanner MUST detect a `js`…`/`css`…` span
and skip it with gsx's escape-aware `embeddedLiteralEnd`, re-initialising
`go/scanner` past it — the identical "skip span, resume scanning" pattern it
already uses for tag spans (`elementSpanEnd`). Proving this skip is byte-correct
is part of the risk-gate spike.

## Codegen: split + lower at emit time; `ast.Interp.Expr` stays a string

No AST change. `ast.Interp.Expr` (and each `PipeStage`'s expression) remains a
string holding the verbatim expression source — which may now contain tag
literals. Only two sites change:

- **`genInterp`** — instead of emitting `Expr` verbatim, run the existing
  `splitGoElements` over `Expr` (and each stage expression) and lower every
  embedded element/fragment to its inline `gsx.Func(...)` value, exactly as the
  top-level `GoWithElements` emitter does. `wrap(<>…</>)` → `wrap(gsx.Func(func…))`.
- **The interpolation type-check probe** (`analyze.go`) — mirror it with the
  same scope-capturing IIFE lowering, preserving emit ≡ probe.

Consequences of keeping `Expr` a string:

- **Printer** prints `Expr` verbatim → round-trips (the source held the tags).
- **wsnorm** never touches interp Go content → unaffected.
- **LSP** go-to-definition *inside* an embedded tag degrades gracefully (the
  tag source is still in the string) — full nav is a follow-up, not a blocker.
- Applies uniformly to the seed and every `|>` stage argument.

## Contexts fall out for free

Every `{ }` — child/text, `attr={…}`, value-form `{ if … }`/`{ switch … }`,
pipe stages, and the ordered-attrs `{{ }}` literal — flows through the same
delimiting scanners, so the fix is uniform. A tag in a JS/CSS context
(`<script>{ … }`, `<style>{ … }`) is simply a type error surfaced by the probe
(a `gsx.Node` where a string/number is expected) — no special-casing needed.

## Semantics (what becomes valid)

- A `<tag>…</tag>` / `<>…</>` may appear anywhere inside a `{ }` interpolation
  expression where a `gsx.Node` value is valid: a call argument
  (`{ wrap(<>…</>) }`), a conditional operand (`{ pick(c, <A/>, <B/>) }`), a
  pipe seed (`{ <A/> |> f }`), etc.
- Nesting is recursive: `{ f(<>{ g(<B/>) }</>) }` — the tag-span skip already
  recurses through child interps (via the element parser), so the outer `}` and
  `|>` scans skip the entire embedded span, nested interps included.
- The same emit≡probe scope capture element/fragment literals have applies:
  interps inside an embedded tag resolve against the enclosing scope.
- Bare adjacency rules are unchanged (a tag literal is a single operand;
  ordinary Go grouping/commas separate multiple).

## Risk-gate spike (Task 1)

This touches the **most common construct in the language** — every `{ }` is
delimited by these scanners. The correctness bar: **for any interpolation with
no embedded tag (the overwhelming majority), delimiting must be byte-identical
to today.** Safety comes from reusing the operand-aware detector the top level
already trusts (only an operand-position `<` is a tag, so `a < b` / `x <= y` /
`<-ch` inside interps stay untouched), but this MUST be proven before anything
builds on it. The spike:

- Implements the unified scanner's boundary + `|>` + delimiter reporting.
- Runs it against the **entire existing corpus** and asserts the computed
  interpolation boundaries, pipe splits, and ordered-attrs delimiters match the
  current byte scanners exactly (a differential harness: old vs new over every
  `{ }` in `internal/corpus/testdata`).
- Proves the embedded `js`…`/`css`…` skip is byte-correct (the escaped-backtick
  case above).

Only after the spike is green does the interp-lowering work proceed.

## Scope / effort

- **Parser (the bulk):** promote `scanGoElementMarks` into an expression scanner
  reporting boundary/`|>`/tag-spans/`,`/`:`, with a `js`…`/`css`…` skip; reroute
  `goExprEnd`/`goStagesEnd`/`splitPipe`/`composedDelims`/`parenEnd` through it (or
  retire them). The risk is entirely here.
- **Codegen:** `genInterp` + the interp probe run `splitGoElements` on the seed
  and stage expressions and lower embedded tags — reusing element/fragment
  literal machinery. Modest.
- **Corpus:** cases per interp context (child, attr value, pipe seed, pipe-stage
  arg, conditional operand, nested) + a JS/CSS-context type-error case.
- **Docs:** extend the "as values" guide — a tag now works inside `{ }` too;
  remove the "not supported inside interpolation" limitation.

## Prerequisites & relationship

Builds on element literals (#42) and fragment literals (#44), both merged to
`main`. This is the third and final piece that makes `<tag>`/`<>` a uniform
Go-expression value everywhere.

## Out of scope

- **General Go-expression parsing.** gsx still does not build a Go-expression
  tree; it scans for tags at operand positions. This extends that scan into
  interpolation interiors — it does not turn interps into fully-parsed Go.
- **LSP nav / hover inside embedded tags** — graceful degradation now; a
  follow-up wires the two-bridge nav recipe into the interp position.
- **Non-`gsx.Node` embedding.** Only tag/fragment literals are lowered; the rest
  of the interpolation expression stays opaque Go, as today.
