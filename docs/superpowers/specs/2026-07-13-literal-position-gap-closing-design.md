# Literal position-gap closing: in-closure js``/css`` error holes, nested-literal diagnostic

**Date:** 2026-07-13
**Status:** Approved design, pre-plan
**Follows:** `2026-07-13-expression-valued-js-css-literals-requirements.md` (PR #106)

## Motivation

PR #106 shipped expression-valued js``/css`` literals with capability-gated
rejections (`hasCtx`/`canHoist`). Two gaps remain from the position/capability
matrix, plus one cryptic-error cleanup:

1. js``/css`` holes reject error-carrying shapes **everywhere**, but inside a
   component's render closure the hoist channel exists — f`` holes use it
   today. The uniform rejection is stricter than the machinery requires.
2. A literal nested inside another literal's `@{ }` hole works in body
   position (real machinery, probed correct), but poisons the `.x.go` in
   attribute-local holes and parse-errors in expression-position holes —
   an inconsistent rule surface.
3. A whole-literal pipeline on a Go-expression-position literal
   (``var x = f`hi` |> upper``) dies with `expected operand, found '>'`.

## W2 — In-closure js``/css`` error-carrying holes hoist

**Rule: wherever an f`` hole can hoist, a js``/css`` hole can too.** The
rejection remains only where hoisting is genuinely impossible.

- **Sites that gain support** (both already pass `canHoist=true` to
  `emitGoExprEmbeddedInterp`):
  - an `{ }` interpolation's `Interp.Embedded` split
    (``{ send(js`load(@{csv |> parse})`) }``), and
  - the braced component-prop binding (``Handler={ js`…` }`` on a declared
    prop), whose `componentValueEntry.stmts` machinery already replays hoists.
- **Shapes that gain support** in those sites: error-returning pipe stages,
  `(T, error)` hole seeds, and error-returning renderers — exactly the three
  shapes `goexpr-literal-error` rejects for js``/css`` today. (The fourth
  rejected shape, mixed type-parameter `AttrString` conversion, is f``-path
  only and unaffected.)
- **Sites that keep the rejection**: top-level `GoWithElements` values and
  `{{ }}` GoBlocks (no clean hoist channel; GoBlock reconstruction interleaves
  GoText into the hoist buffer). GoBlock behavior is pinned as-is: pure and
  ctx-taking holes allowed, error-carrying holes rejected.
- **ctx gating is unchanged**: `rejectCtx = !hasCtx` (top-level only).

### Mechanics

`emitGoExprEmbeddedInterp` currently hardcodes `exprPos=true` for its js/css
branches. Change to `exprPos = !canHoist`. With `exprPos=false` the existing
fold-path lowering applies: each dynamic hole materializes to a source-ordered
`_gsxvN` temp in `hoistBuf` (the render closure, before the consuming
statement) and error shapes hoist through `return _gsxerr` — identical to how
an attribute-local js`` literal folds through a bag today, and identical in
placement to an f`` hole's hoist at the same site. Evaluation order stays
source order by construction (the temps are why the fold path exists).

The analyze-side probe already harvests these holes' tuple types (f`` proves
it at the same site); the plan verifies emit ≡ probe holds and whether any
analyze-side mirror of the rejection must be relaxed in lockstep.

## W3 — Nested literal in a hole: supported everywhere

**Revised after live probing** (original draft said diagnostic): nested
literals in **body-position** f`` holes already work correctly through real
machinery — a hole is an `ast.Interp`, `splitInterpEmbedded` recurses into it,
and `genInterp` splices the inner literal's lowered value. There are **no
nested-escaping semantics to define**: the inner literal is an ordinary Go
expression of type `string`/`gsx.RawJS`/`gsx.RawCSS`, and the outer hole
escapes the resulting *value* by its own context (probed: an inner js`` in an
f`` hole lowers to `RawJS("f(" + EscapeJSVal(who) + ")")` and the outer hole
HTML-escapes it).

**Rule: a `@{ }` hole is a Go-expression position; nested prefixed literals
are supported there like any other Go expression, in every hole-bearing
context.** Two contexts are broken today and get fixed to match:

1. **Expression-position literal holes** (parse errors today):
   `embeddedLiteralEnd` (`parser/boundary.go:160`) is a flat scan to the
   closing delimiter, so a nested literal's backtick inside a hole terminates
   the outer literal early. Fix: make the Go-expression split's literal-end
   scan hole-aware — on `@{`, skip the hole with the same Go-expression
   scanner the attribute path already uses (`scanGoExpr` /
   `skipGSXEmbeddedLiteral`), which handles strings, comments, brace balance,
   and nested prefixed literals. Both delimiters.
2. **Attribute-local literal holes** (poison today): the hole already *gets*
   the `Embedded` split (`walkMarkupAttrs` yields `EmbeddedAttr.Segments`
   into the walk), but `holeStringExpr`/`embeddedHoleExpr` splice the hole's
   raw `Expr` verbatim. Fix: a shared seed assembler — when `Interp.Embedded`
   is non-nil, assemble the seed from GoText runs + each inner literal's
   `emitGoExprEmbeddedInterp` value (mirroring `genInterp`'s loop), and use
   the assembled expression wherever `n.Expr` was used (including as the
   `lowerPipe` seed).

Capability flags compose through recursion for free: the inner literal's
`hasCtx`/`canHoist` derive from the hole's existing `rejectCtx`/`rejectErr`
(`hasCtx = !rejectCtx`, `canHoist = !rejectErr`), so e.g. an error-carrying
hole inside a nested literal at top level is still rejected with the same
positioned diagnostics.

**Bounded scope** (positioned diagnostics, not support):
- An **element literal** (`<tag>`) in an attr-literal hole: diagnostic
  (mirrors the GoBlock element rejection; the body-interp path, which does
  support embedded elements, is unchanged).
- A prefixed literal inside a **pipe stage's arguments**
  (``@{x |> printf(f`%s`)}``): diagnostic — `prefixed literals in pipe-stage
  arguments are not supported; assign the literal to a variable first`.
  Detection is tokenize-based over `Stages[].Args` (never a fragment parse).
- Formatter: nested literals round-trip verbatim (the printer prints hole
  `Expr` text, never `Embedded`); idempotence is pinned, hole-expression
  reformatting may degrade gracefully to verbatim.

## W1′ — Whole-literal pipe in Go-expression position: positioned diagnostic

A `|>` chain after a value-position literal is **not supported** (dropped as a
feature: a function call does the same — ``upper(f`hi @{name}`)``). The
split scanner peeks past a value-position literal's end for `|>` and reports:

> `whole-literal pipelines are not supported in Go-expression position; wrap
> the literal in a function call instead`

Positioned at the `|>`. Applies to all three langs at all three container
sites. Scanner-level detection is required — the `|>` never reaches a
skeleton parse as valid Go, so today's failure is unpositioned noise.

## Out of scope (pinned)

- GoBlock semantics unchanged (ctx allowed, error rejected, element literals
  rejected — the latter stays a ROADMAP item).
- Top-level error/ctx rejections unchanged (no channel exists; by design).
- Whole-literal pipe **support** in Go-expression position (W1 dropped).
- Element-literal support in attr-literal holes and pipe-stage-argument
  literals (both are positioned diagnostics; see W3's bounded scope).
- No runtime API changes; no new syntax; no sibling-grammar changes.

## Testing

- **W2 corpus**: success + render goldens for an error-pipe js`` hole and a
  `(T, error)` css`` hole at the `Interp.Embedded` site; a braced-prop
  binding with an error hole (stmts replay); a halt-on-error render pin; an
  error-renderer hole; a differential pin — the same error-carrying js``
  literal attribute-local vs in-closure expression form renders
  byte-identical under hostile input. Top-level and GoBlock rejection cases
  re-verified unchanged.
- **W3 corpus**: nesting pinned per context — body f`` hole (existing
  behavior, now pinned: plain, depth-2 with inner hole, inner js`` with
  RawJS provenance), attr-local f``/js`` holes, expression-position holes
  (top-level, `{ }` interp, GoBlock), both delimiters; render goldens prove
  escaping. Diagnostics pinned for the bounded scope (element in attr-literal
  hole, literal in stage args).
- **W1′ corpus**: the diagnostic at each container site.
- No fmt-corpus changes (no new accepted syntax). Existing fmt cases
  re-verified.
