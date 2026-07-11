# Universal element-spread sanitization — a spread is a sink

**Status:** design
**Date:** 2026-07-11
**Fixes:** issue #75 (local-var / func-result / byo-2nd-field / dot-import bag
spreads render URL values unsanitized) — the four demonstrated XSS holes.
**Follow-up to:** `2026-07-10-bag-spread-hardening-design.md` (which sanitized
*recognized forwarding* spreads at the leaf but left non-recognized ones on the
old "trusted bag" path).

## The flaw in the premise

Bag hardening made a spread sanitize URL-classified keys **only when codegen
recognized its base as a forwarding position** (`attrs`, byo `p.Attrs`, a named
`gsx.Attrs` param). Every other element spread — a body-local `:=` bag, a
function-result bag, a byo struct's *second* `gsx.Attrs` field, an arbitrary
expression — fell through to a plain `gw.Spread`, whose code comment
(`emit.go:2655`) asserts *"a bag's keys/values are trusted developer input"* and
does no URL sanitization. That is a rationalization, not a guarantee: the moment
an author writes `b := gsx.Attrs{{Key: "href", Value: userInput}}`, the value is
not trusted. Demonstrated, rendered through gsx's own `TestCorpus` harness:

| Spread form on `<a>`/`<img>` | Renders |
|---|---|
| `{{ b := gsx.Attrs{…} }}` then `{ b... }` | `href="javascript:alert(1)"` |
| `{ mkBag(u)... }` (function result) | `href="javascript:alert(1)"` |
| `{ p.Extra... }` (byo 2nd `gsx.Attrs` field) | `href="javascript:alert(1)"` |
| local bag → `{ b... }` on `<img>` | `src="javascript:alert(1)"` |
| **control:** `{ bag... }` (declared param) | `href="about:invalid#gsx"` |

The correct model, which gsx already applies everywhere else (the `html/template`
port): **a spread onto an element is a sink; escaping/sanitization is determined
by the context — the element tag and the attribute name — not by where the value
came from.** `<a href={u}>` sanitizes `u` regardless of `u`'s provenance; a
spread writing `href` onto `<a>` is the same sink and must behave the same.

## Design

### Principle

Every element spread `{ x... }` sanitizes URL-classified keys by context and
follows the full fallthrough rule (Has-guarded defaults before the spread,
forced statics after, position-exempt `class`/`style` merge), **independent of
what `x` is**. `gsx.RawURL` (per value) is the only opt-out. No provenance
distinction, no whole-bag "trusted" escape. This needs no type analysis: on an
element, `{ x... }` already requires `x` to be `gsx.Attrs` — the spread would not
compile otherwise.

### Mechanism — delete the recognition

`genNode`'s element case (`emit.go:1749`) calls `bagSpreadIndex(attrs, bagBases)`
to locate a spread whose expression matches a *recognized forwarding base*, and
only then routes to `emitManualSpreadElement` (the sanitizing machinery). The fix:

- `bagSpreadIndex` matches **any** `SpreadAttr` on the element (not just ones
  referencing `bagBases`), still erroring when an element carries more than one
  spread (position-ambiguous — the existing "one forwarding spread per element"
  rule, now applied to all spreads).
- Every element carrying a spread therefore routes to `emitManualSpreadElement`,
  which already: hoists a derived/arbitrary bag expression into a single-eval
  temp, emits `!bag.Has(k)` default guards for pre-spread statics, forces
  post-spread statics, merges `class`/`style` via `ClassMerged`/`StyleMerged`,
  and emits the URL-sanitizing `SpreadForwarding` residual. None of this is
  base-specific — it operates on whatever bag expression it is given.
- The plain-`Spread` emit for a `SpreadAttr` in `emitAttr` (`emit.go:2651`, the
  "trusted bag" hole) is removed for element spreads.
- `bagBases` (threaded through `genNode` and its recursion during bag hardening
  purely for forwarding-base recognition) and its matchers
  `spreadMatchesBase`/`spreadMatchesAnyBase` lose their only consumer and are
  deleted. This is a broad but mechanical signature diff across `emit.go`; the
  corpus is the safety net.

Net effect: the four XSS holes close, the pre-existing standalone-spread
duplicate-`class` quirk (`<a class="base" { b... }>` → `class="base" class="x"`)
becomes a clean merge (`class="base x"`), and the codegen shrinks (recognition
machinery removed). Emit-only change; the probe/skeleton path is unaffected
(spreads type-check identically whether or not they were "recognized").

### Runtime and contract

- **`gw.Spread` is removed entirely.** Under this design, `SpreadForwarding` is
  the sole spread emission and codegen emits zero `Spread` calls (both plain-
  `Spread` emit sites die: `emitAttr`'s SpreadAttr case becomes unreachable —
  every element with a spread routes to `emitManualSpreadElement` — and
  `emitFallthroughAttrs`'s non-splitIdx SpreadAttr becomes the existing
  one-spread error, below). Leaving a public, unsanitizing `Spread` primitive
  around while nothing generates it is exactly the pre-adoption tech debt to
  avoid — a silent footgun with no consumer. The only non-generated callers are
  three root-package tests/benches (`gsx_test.go:36`,
  `root_attr_bench_test.go:36,69`), migrated to `SpreadForwarding` (or covered by
  the existing `BenchmarkForwardingLeafNoURL`).
- `class`/`style` aggregation that `Spread` performed (`a.Class()`/`a.Style()`)
  is preserved with no special handling: the forwarding path already emits
  `ClassMerged(…, bag.Class())` / `StyleMerged(…, bag.Style())` upstream of
  `SpreadForwarding` (which excludes them), so a standalone bag's duplicate
  `class`/`style` keys still aggregate — and now also merge with any static
  `class`/`style` on the element.
- `gsx.Attrs` godoc + the `emit.go:2655` comment are rewritten: the "values are
  NOT URL-sanitized / trusted developer input" language becomes "a spread onto
  an element sanitizes URL-classified keys by context; `RawURL` (per value) opts
  out." There is no unsanitizing spread primitive.
- `attributes.md`/`props.md`/`composition.md` drop the provenance caveats — the
  local-var, byo-second-field, and dot-imported-runtime cases are no longer
  exceptions. The matching #75 items (and their ROADMAP entries) are struck as
  fixed; the byo-second-field and dot-import notes become "covered."

### Behavior change: at most one spread per element

Because every element spread is now a forwarding spread, an element may carry
**at most one**. Today an element can carry one *recognized* forwarding spread
plus a second *non-recognized* spread emitted inline (the unsanitized path this
spec closes); under this design the second spread hits the existing one-spread
diagnostic (`element with an attrs-forwarding spread cannot carry another
spread … merge them into one spread ({ a.Merge(b)... })`). This turns a
currently-generating combination into a generate-time error with a clear fix —
a deliberate consistency gain (the second spread was precisely the ambiguous,
unsanitized case). Pinned by a corpus diagnostic case.

### Scope boundaries

- **URL context only**, as in bag hardening: `class`/`style` (CSS) and `on*`
  (JS) remain literal-opt-in (`css`` / `js`` → `RawCSS`/`RawJS`); a spread does
  not name-classify them. This spec does not revisit that — it closes the URL
  hole, which is the demonstrated one.
- **Element spreads only.** A spread on a component tag (`<Comp { x... }/>`) is a
  props splat, handled by `genChildComponent`, and is unchanged.
- Attribute-NAME trust (keys emitted after `validAttrName` without
  entity-encoding) is a separate, pre-existing contract and out of scope.

## Testing

- The five cases already built (`internal/corpus/testdata/cases/spread-sanitize/`)
  are the spine: the four `❌` render goldens flip from raw `javascript:` to
  `about:invalid#gsx` (matching the control) as the RED→GREEN pins. Delete their
  "VULN" framing once fixed.
- Add: `<a class="base" { localBag... }>` → merged not duplicated; a `RawURL`
  value in a local bag passing verbatim; the `<img src>` image-sink split;
  a dot-imported-runtime bag (now covered); a conditional/derived local bag
  (`{ b.Without("id")... }`) hoisted once.
- **Golden audit is load-bearing:** existing standalone-spread goldens WILL move
  (gaining class-merge + URL sanitization). Each changed render golden must be
  verifiable as exactly one of: duplicate-attr → merged, or unsafe-URL →
  `about:invalid#gsx`. Any other movement is a bug — stop and diagnose.
- Runtime: `SpreadForwarding` already tested; the `Spread` unit test/benches are
  migrated to it (or removed if redundant with `BenchmarkForwardingLeafNoURL`).
  Two existing corpus cases that pin `_gsxgw.Spread(` — `fallthrough/
  local_bag_inline` (which pinned local bags *staying* inline — the exact
  behavior this spec inverts) and `nonce/spread_in_cond_attr_on` — flip to
  `SpreadForwarding`; re-pin them and verify the nonce+spread interaction still
  emits correctly through the forwarding path. Add the one-spread-per-element
  diagnostic case.
- Fresh **adversarial probe pass** (security core): multi-hop laundering through
  a local bag, case-variant keys, `RawURL` at each position, an element with a
  static URL attr + a local-bag URL attr (precedence), two-spread rejection.
- **one-learning revalidation**: its templates use local/derived bags; confirm
  render parity except the intended safe deltas, and that no legitimate output
  regressed.

No surface syntax changes — tree-sitter-gsx / vscode-gsx / CodeMirror / `gsx fmt`
unaffected.
