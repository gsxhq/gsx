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
2. A literal nested inside another literal's `@{ }` hole dies with a cryptic
   parse error instead of a positioned diagnostic.
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

## W3 — Nested literal in a hole: positioned diagnostic

A prefixed literal (`f`/`js`/`css`, either delimiter) written inside another
literal's `@{ }` hole — including inside a pipe stage's arguments — is
**not supported** and reports a positioned diagnostic:

> `nested f`/`js`/`css` literals inside @{ } holes are not supported; assign
> the inner literal to a variable and interpolate that`

- One diagnostic code (`nested-embedded-literal`), applied uniformly across
  every hole-bearing context: attribute-local literals, body f`` literals,
  expression-position literals (all three container sites), and GoBlock
  literals.
- Detection is **tokenize-based** (go/scanner + the existing
  `langPrefixStart` value-position test over the hole's `Expr` and each
  stage's `Args`) — never a fragment parse, per the Go-as-blob rule.
- Detection lives in the **analyzer** (diagnostics never live in the
  formatter/parser presentation layer; the LSP gets it for free). If the
  parser's hole scanner itself mis-nests on the inner literal's delimiters
  (the plan probes both delimiter combinations), the detection point moves to
  wherever the segments are still reliably formed, but the surfaced
  diagnostic — position, code, wording — is as specified.
- Support for nested literals stays out of scope, demand-driven. The cost is
  not parsing (the recursive split machinery exists) but defining and
  adversarially proving nested escaping semantics (a RawJS landing in a
  string/template/regexp-context hole), formatter recursion, and nested
  grammar injections in the sibling tooling.

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
- Nested-literal **support** (W3 is diagnostic-only).
- No runtime API changes; no new syntax; no sibling-grammar changes.

## Testing

- **W2 corpus**: success + render goldens for an error-pipe js`` hole and a
  `(T, error)` css`` hole at the `Interp.Embedded` site; a braced-prop
  binding with an error hole (stmts replay); a halt-on-error render pin; an
  error-renderer hole; a differential pin — the same error-carrying js``
  literal attribute-local vs in-closure expression form renders
  byte-identical under hostile input. Top-level and GoBlock rejection cases
  re-verified unchanged.
- **W3 corpus**: the diagnostic in each hole-bearing context (attr-local,
  body f``, top-level expression, `{ }` interp, GoBlock; backtick and dquote
  delimiters; literal in a stage's args).
- **W1′ corpus**: the diagnostic at each container site.
- No fmt-corpus changes (no new accepted syntax). Existing fmt cases
  re-verified.
