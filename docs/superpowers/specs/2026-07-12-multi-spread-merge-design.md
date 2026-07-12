# Multi-spread merge — design

- **Date:** 2026-07-12
- **Status:** approved, pre-plan
- **Supersedes:** the "one spread per element" rule from
  `2026-07-02-attrs-forwarding-hardening-design.md` (decision D2) and the
  at-most-one-spread extension in `2026-07-11-srcset-sanitization-design.md`
  (Task 8) / `2026-07-11-universal-spread-sanitization-design.md`.

## Summary

An element or component may carry **more than one attribute spread**. Spreads,
and any statics or conditional-attr groups sitting between them, merge by
**strict source order, last writer wins per key** (`class`/`style` aggregate
across all of them). This is gsx's universal "later attribute wins" rule applied
to spreads — nothing new semantically.

Today a second spread is a generate-time error that tells the author to rewrite
`{ a... } { b... }` into `{ a.Merge(b)... }`. Because the compiler already knows
the exact, deterministic rewrite, forcing the author to type it is pure
boilerplate. This change performs that merge automatically.

```gsx
<!-- was: generate-time error. now: b overrides a per key, source order -->
<input type="hidden" name={props.Name} x-ref="hidden"
       { hiddenSyncAttrs... } { inputAttrs... }/>
```

## Motivation

The pattern shows up naturally whenever an element forwards two independent
attribute sources — e.g. `one-learning-gsx/ui/common_edit_components.gsx:91`
combines `hiddenSyncAttrs` (framework wiring) with `inputAttrs` (caller
forwarding). The `.Merge()` workaround is noise, and the diagnostic offering
*both* `a.Merge(b)` and `b.Merge(a)` implies an ambiguity that does not exist:
source order decides, exactly as it does for two static attributes of the same
name.

### Why the original D2 objection no longer holds

D2 rejected auto-merge for a **security** reason: statically layering a scalar
*between* two spreads would force lowering the whole attribute section to a
runtime bag, "which loses per-context escaping (`gw.URL` etc. — a security
regression)." That premise was written **before** PR #79
(`universal-spread-sanitization`) made the leaf `Spread` route *every*
bag key through the same tag-aware URL/image/srcset sinks a static attribute
uses (`attrs.go:302-355`). Post-#79 a value routed through the bag is sanitized
identically to a compile-time literal — same sink, byte-identical output. The
regression D2 protected against does not exist anymore, so the perf/allocation
cost is the only residual, and it is confined to the rare "static wedged between
two spreads" shape.

## Current behavior (precise)

Element attribute spreads (`internal/codegen/emit.go`):

| Shape | Handling today |
|---|---|
| 0 spreads | normal element emit |
| 1 top-level spread | `emitManualSpreadElement` — pre-zone overridable, bag, post-zone forced; root class/style merge |
| 1 spread nested in a cond-attr (lone) | inline guarded `if cond { Spread(bag, …) }` (`srcset-sanitize/cond_nested_spread.txtar`) — **no** forwarding zones, **no** root class merge |
| ≥2 spreads (top-level or cond-nested, in any mix) | **rejected** by `firstTwoSpreadAttrs` (`emit.go:1664-1697`), three diagnostic variants |

Component attribute spreads (`genChildComponent` → `buildAttrsBag`,
`emit.go:5040-5341`):

| Shape | Handling today |
|---|---|
| N top-level spreads + cond-attrs + interposed statics | **already folds**: spreads and `AttrsCond` cond-attrs accumulate into `segments`, emitted as `Attrs: ConcatAttrs(base, seg₁, seg₂, …)` in source order (`emit.go:5337`); statics matching a Props field become fields, the rest join the bag |
| spread nested *inside* a cond branch (`{ if c { {x...} } }`) | **rejected** — `unsupported-component-attr` "cond branches stay shallow" (`component_cond_branch_spread_rejected.txtar`) |

**Key observation:** the component path is already the target design (ConcatAttrs +
AttrsCond, last-wins at the leaf). This change brings the **element** path into
line with it and closes the shared spread-in-cond-branch gap. One composition
model, two call sites — not a new mechanism.

## Governing principle

This design is ordered by a single rule that keeps bug surface low:

1. **Correct behaviour first.** The reference semantics is the *full fold*:
   concatenate the element's attributes in source order into one
   `ConcatAttrs(...)` bag and resolve last-wins at the leaf. It is correct by
   construction — a direct transcription of "the rule" below, with no special
   cases to get wrong.
2. **Merge is the default.** No error, no opt-in flag; multiple spreads just
   merge.
3. **Simple cases are optimisations *on top of* the baseline.** The single-spread
   path, the zero-spread path, and the pre/post compile-time-static zones exist
   only because they are **provably byte-identical** to what the full fold would
   emit. An optimisation that cannot be shown equivalent is a bug and yields to
   the fold — it is never independently "reasoned correct."

Consequence: every fast path is guarded by a **differential test** (see Test
plan) asserting its output renders identically to the naive full-fold reference.
This is where a divergent fast path would otherwise hide (cf. O1).

## The rule

For the run of attributes from the **first spread to the last spread inclusive**
(the *spread-span*):

- Every key resolves to its **last writer in source order**. A later spread's
  key overrides an earlier spread's; an interposed static overrides an earlier
  spread and is overridden by a later spread.
- `class` and `style` **aggregate** across all span contributors
  (position-independent, matching existing class/style semantics).
- Each key renders **once, at its last occurrence** (leaf
  `lastValidAttrIndexes`, `attrs.go:306`), with the last writer's value.
- URL/image/srcset keys sanitize at the leaf by the element's (statically known)
  tag context.

Attributes **before** the first spread keep the existing *overridable* zone
(`!bag.Has(name)` guard); attributes **after** the last spread keep the existing
*forced* zone (root wins, incl. the `planPostCond` post-spread cond-attr
machinery). Only the span itself is new.

## Case dispatch

Every attribute shape and the path it takes after this change. The **fold** is
the correct baseline (Governing principle); every "current (optimised)" row is
retained *only* because the differential test proves it byte-identical to the
fold. Boundary rule: **simple = fewer than two spreads → keep the current
optimised path; two-or-more spreads (or a spread inside a cond branch) → the
general fold.**

### Elements

| # | Shape | Spreads | Path | Change |
|---|---|---|---|---|
| E0 | `<el a="1" b="2">` | 0 | current normal emit | none |
| E1 | `<el { a... }>` | 1 top-level | current `emitManualSpreadElement` | none |
| E2 | `<el x="1" { a... } y="2">` | 1 + pre/post statics | current `emitManualSpreadElement` (pre overridable, post forced) | none |
| E3 | `<el { if c { {a...} } }>` | 1 cond-nested (lone) | current inline `if c { Spread }` | none¹ |
| E4 | `<el { a... } { b... }>` | ≥2 adjacent | **fold** → `ConcatAttrs(a, b)` → leaf | NEW |
| E5 | `<el { a... } k="v" { b... }>` | ≥2 + interposed static | **fold** → `ConcatAttrs(a, Attrs{{k,v}}, b)` | NEW |
| E6 | `<el { a... } { if c { {b...} } }>` | ≥2, cond spread | **fold** → `ConcatAttrs(a, AttrsCond(c,→b))` | NEW² |
| E7 | `<el { a... } { if c { k="v" } } { b... }>` | ≥2 + cond static | **fold** → `ConcatAttrs(a, AttrsCond(c,→{k,v}), b)` | NEW |

¹ Retained pending the O1 differential check; if it diverges on root-class merge
it is replaced by an E4-style fold. ² Also covers the previously-rejected
two-cond-spread and cond-spread-after-top-level shapes.

### Components

| # | Shape | Path | Change |
|---|---|---|---|
| C0 | `<C f="x"/>` (fields only) | current field emit | none |
| C1 | `<C f="x" { a... } { b... } attrs={{…}}/>` | current — **already folds**: `Attrs: ConcatAttrs(base, a, b, lit)` | none |
| C2 | `<C { if c { {a...} } }>` (spread inside cond branch) | **legalise** → `AttrsCond(c, →ConcatAttrs(branch, a), →else)` | NEW |

The element fold (E4–E7) fires iff **≥2 spreads** are present (counting
cond-nested). Components already compose via `ConcatAttrs` (C1); only C2 (spread
inside a cond branch) is new.

## Design

### Core mechanism: fold the span into one bag

Lower the spread-span to a single `ConcatAttrs(...)` expression, then reuse the
**existing** single-bag leaf unchanged. `ConcatAttrs` (`attrs.go:228`) already
concatenates in source order with last-wins/aggregation resolved at the leaf and
is already the component path's composition primitive; `AttrsCond`
(`attrs.go:251`, `(Attrs, error)` thunked) already models conditional bags. **No
new runtime surface.**

Each span entry lowers to one `ConcatAttrs` argument:

| Span entry | ConcatAttrs argument |
|---|---|
| `{ e... }` (optionally piped `{ e \|> f... }`) | `e` (or `lowerPipe` result) |
| `{ if c { {e...} } }` / with `else { {g...} }` | hoisted `AttrsCond(c, →e, →g)` temp |
| interposed static `k="v"` | `_gsxrt.Attrs{{Key: "k", Value: "v"}}` |
| interposed hole `k={expr}` / `k={expr \|> f}` | `_gsxrt.Attrs{{Key: "k", Value: <hoisted value temp>}}` |
| interposed cond static `{ if c { k="v" } }` | `AttrsCond(c, →Attrs{{k,v}}, nil)` |
| interposed `class="x"` / `style="y"` | `_gsxrt.Attrs{{Key: "class", Value: "x"}}` — leaf aggregates via `bag.Class()` |

The element case then builds a synthetic single-spread element (span replaced by
one `SpreadAttr` whose `Expr` is the `ConcatAttrs(...)` string / hoisted temp,
positioned at the first spread's `Pos`) and calls `emitManualSpreadElement` with
that split index. Everything downstream — class/style merge, URL sinks, forced
post-zone, nonce injection — is inherited verbatim.

### Shared composition helper (no duplication)

Extract the span → `ConcatAttrs(...)` segment composition into a helper shared by
both call sites, factored out of the component `buildAttrsBag` segment logic
(`emit.go:5197-5339`). The element path feeds it the span entries; the component
path keeps feeding it non-field attrs. This avoids a second, drifting copy of the
lowering rules (the tech-debt failure mode). Differences stay at the edges:

- **Elements** have no Props fields — every interposed static folds into the bag.
- **Components** route field-matched statics to fields; only unmatched attrs and
  spreads/cond-attrs reach the bag.

### Hoisting and error threading

Conditional and piped entries produce `(Attrs, error)` and are hoisted **in
source order**, each with a **site-correct** `errReturn` (the component path's
`hoistTuple` / `bagErrReturn` discipline, and the `errReturn`-shape rule from the
renderers work), so an error in an earlier span entry returns before a later one
is evaluated. `AttrsCond` thunks preserve untaken-branch non-evaluation
(`{ if u != nil { {u.Attrs...} } }` never touches `u.Attrs` when `u == nil`).

Source order governs evaluation as well as final key precedence. Before
emitting a hoisted conditional or error-returning contributor, codegen must
materialize every earlier contributor whose expression would otherwise remain
inline in the final `ConcatAttrs(...)` call. For example, `a()`, conditional
`b()`, `c()` must execute as `a, b, c`, never `b, a, c`. This applies to side
effects, panics, and error selection; preserving only the final last-writer-wins
bag is insufficient. Untaken conditional branches remain lazy.

### Escaping is byte-identical

An interposed static becomes a bag value sanitized at the leaf by key context
(nav-URL → `URLVal`, image → `URLImageVal`, srcset → `SrcsetVal`, `class`/`style`
→ aggregate, bool → `BoolAttr`, else `AttrValue`). Post-#79 these are the same
sinks the compile-time attribute path uses, so output bytes are identical. This
is pinned per context in the test plan (a mismatch is a bug, not an accepted
divergence).

### Void elements and children

Unchanged — `emitManualSpreadElement` already emits the tag/void/children after
the bag; the synthetic single-spread element carries the original children.

## Decisions

1. **Single-spread and lone cond-nested-spread paths are retained as
   optimisations, not exemptions.** Fold triggers on **≥2 spreads**; the 0- and
   1-spread paths stay — *but only after the differential test proves each
   byte-identical to the full fold*. The lone cond-nested-spread inline path is
   the one under active suspicion (O1); if the diff fails, that path is wrong and
   is replaced by the fold, accepting its golden churn. "Zero churn" is a welcome
   outcome of equivalence, never a reason to skip the check.

2. **Spread-inside-cond-branch is legalized on both sides.** For consistency, a
   spread inside a cond branch composes as
   `AttrsCond(c, →ConcatAttrs(branchStatics, x), →else)`. This removes the
   element diagnostic variants (b)/(c) and the component `unsupported-component-attr`
   "cond branches stay shallow" rejection. Elements already accept a lone one;
   components will now match.

3. **Interposed pipe-holes are supported** (not deferred) — hoist the hole's
   value, threading pipe errors, then store the temp as `Value`. This keeps the
   feature whole rather than shipping a partial rule with a follow-up owed.

4. **Precedence is source order, uniformly** — we do **not** carry forward the
   old "conditional overrides base" special case from the D2-era diagnostic.
   Whichever spread is written last wins; a conditional entry that is untaken
   contributes nothing (nil bag).

## Deleted / changed

- **Delete** `firstTwoSpreadAttrs` and its rejection branch (`emit.go:1664-1697`)
  plus the three diagnostic variants; delete the component "cond branches stay
  shallow" spread rejection.
- **Reclassify** these corpus cases from `diagnostics.golden` (rejection) to
  `render.golden` + `generated.x.go.golden` (accepted): `two_spreads_error`,
  `fallthrough/second_spread_rejected`, `fallthrough/byo_bag_two_spreads`,
  `fallthrough/cond_attr_nested_spread_rejected`, `spread-sanitize/cond_nested_two_spreads`,
  `spread-sanitize/cond_nested_spread_after_toplevel`, `jsattr/manual_multi_spread_rejected`,
  `components/component_cond_branch_spread_rejected`. Rename off the `_rejected`/`_error`
  suffix.
- **Rewrite** guide `docs/guide/syntax/composition.md:231-235` ("An element
  carries one forwarding spread…") to state the source-order merge rule
  concisely (behavior only; rationale stays in this spec).

## Open questions

- **O1 — lone cond-spread class merge.** The inline `if cond { Spread(bag, excluded=nil) }`
  path does not run the root class/style merge. If an element has a root
  `class="…"` *and* a lone cond-nested spread carrying `class`, does the bag's
  class merge with the root's, or emit a duplicate `class` attribute? Build a
  probe during planning; if it is a latent bug, fold the lone case through the
  new path too (accepting its golden churn) rather than preserving a broken fast
  path.
- **O2 — output order for a key present only in an earlier spread.** With
  `ConcatAttrs(p, q)` and a key in `p` only, it renders at `p`'s position; a key
  in both renders at `q`'s (last) position. Confirm this matches author
  expectation and pin it; it is deterministic either way.

## Performance

- Common case (adjacent spreads, no interposed statics/conditionals): one
  `ConcatAttrs` allocation for the merged bag, then the existing leaf. No shadow
  bags, no per-static guards, no dynamic drop slices.
- Interposed statics add a tiny `Attrs` literal each; conditionals add an
  `AttrsCond` call (already thunked). All confined to the span.
- No change to the 0-spread and 1-spread hot paths.

## Test plan (extensive)

Corpus is canonical — every case pins `input.gsx` + `generated.x.go.golden` +
`render.golden`. New cases under `internal/corpus/testdata/cases/multispread/`
(elements) and additions to `components/`:

**Merge semantics**
- adjacent 2 spreads; adjacent 3 spreads (source-order last-wins across all).
- overlapping keys across spreads (later wins); disjoint keys (union).
- empty/nil bag participants (skipped; all-empty → no attrs).

**Interposed statics — per context (byte-identical to compile-time)**
- nav URL (`href`), image URL (`src` on `<img>`), `srcset`, plain scalar,
  boolean attr, numeric attr, `class`, `style`. Each: a dangerous value
  (`javascript:`) proves leaf sanitization; a benign value proves byte-identity.
- interposed hole `k={ident}`; interposed pipe-hole `k={x |> f}` (error and
  non-error filter); pipe-hole that errors mid-render (site-correct return).

**Conditionals**
- conditional spread `{ if c { {x...} } }` alongside another spread (both
  branches); with `else { {y...} }`; nested `if/else-if`.
- conditional interposed static; conditional spread-in-branch on **component**
  (the flipped `component_cond_branch_spread_rejected`).
- untaken-branch non-evaluation (`{ if u != nil { {u.Attrs...} } }` with `u == nil`).

**Zone interaction**
- pre-span static (overridable) + span + post-span static (forced) — three-zone
  precedence in one element.
- post-span cond-attr (existing `planPostCond`) coexisting with a multi-spread span.
- root `class` + span carrying `class` (aggregate once, no duplicate).

**Components**
- `<Comp { a... } { b... }>`; `<Comp field="x" { a... } { b... }>` (field vs bag split);
  `<Comp { a... } attrs={{…}}/>` (ordered-attrs literal concatenated last, existing rule).

**Contexts / tags**
- nonce-injected `<script>`/`<style>` with 2 spreads (nonce inherited).
- void element (`<input>`) and non-void with children.

**Differential (fast-path ≡ full-fold) — enforces the governing principle**
- For a representative matrix (0/1/2/3 spreads × interposed static × conditional
  × class/style × URL context), assert the shipped codegen's rendered output is
  **byte-identical** to a naive reference that folds *every* attribute into one
  `ConcatAttrs(...)` and renders it. Any divergence fails — it means an
  optimisation (single-spread path, pre/post zone, lone cond-spread inline) does
  not match the reference semantics and must be fixed or dropped.

**Fuzz / property — "handle all cases" (two tiers; `packages.Load` cost forces
the split)**
- *Tier 1 — runtime leaf fuzz (`testing.F`, root `gsx` package, no codegen →
  cheap, high iteration).* Fuzz a random sequence of attribute contributors —
  spreads (`Attrs`), interposed statics `(key,value)`, and conditional entries
  (`AttrsCond`, taken/untaken) — in random source order, over a small key
  alphabet that forces frequent collisions plus `class`/`style` and a URL key
  (`href`/`src`). Build the composition two ways and assert byte-identical
  rendered output: (a) `Spread` over `ConcatAttrs(...)` of the contributors;
  (b) an independent reference that computes source-order last-wins (class/style
  aggregated) and renders it. This proves the *merge semantics* exhaustively,
  decoupled from codegen. Also assert the classic invariants: last-wins per key,
  class/style aggregation, untaken `AttrsCond` contributes nothing, empty/nil
  bags vanish.
- *Codegen evaluation order.* Generate contributors through functions that
  append stable markers before returning their bags. Assert mixed plain,
  conditional, and piped contributors record strict source order, that an
  earlier error prevents later contributors from running, and that an untaken
  conditional branch records no marker. These assertions target evaluation
  order separately from the existing byte-output differential.
- *Tier 2 — codegen dispatch differential (batched, bounded, CI).* A generator
  emits many element/component shapes (spanning E0–E7, C0–C2) into **one** `.gsx`
  package so `packages.Load`/build runs **once**, then renders every shape and
  diffs each against its Tier-1 reference render. This ties the codegen
  *dispatch* (which path each shape takes) to the proven runtime semantics —
  catching a fast path that compiles to something other than the fold. Not
  per-iteration `testing.F` (would reload packages each input); a fixed
  broad matrix regenerated in CI.
- *Parser fuzz (existing) unchanged* — multi-spread already parses, so the
  parser fuzz corpus needs no new seeds, but confirm it does not crash on
  dense multi-spread/cond-nested inputs.

**Regression / negative**
- confirm 0-spread and lone-spread goldens are **unchanged** (byte-diff the
  regenerated corpus for those cases) — an *expected* consequence of equivalence,
  verified, not assumed.
- remaining genuine errors still error (e.g. unsupported component attribute
  kinds unrelated to spreads).

**Formatter**
- `internal/gsxfmt/testdata/cases/`: pin layout of `{ a... } { b... }` and an
  interposed-static multi-spread element (multiple spreads already parse, so this
  is a layout-only case, but ship it per the fmt-corpus rule).

**Runtime unit tests**
- root `gsx` package: `ConcatAttrs` + `AttrsCond` composition edge cases already
  covered; add any missing last-occurrence-ordering assertion surfaced by O2.

## Non-goals

- No new runtime API (no `SpreadShadowed`, no `Attrs.Keys()`). Explicitly
  rejected in favor of the fold.
- No preservation of compile-time escaping for interposed statics (leaf escaping
  is byte-identical and simpler).
- No change to single-spread or zero-spread codegen.

## Sibling repos

Multiple spreads already **parse** (the old restriction was generate-time, not a
grammar rule), so `tree-sitter-gsx`, `vscode-gsx`, and the docs CodeMirror
grammar need **no** change. Only `docs/guide/syntax/composition.md` prose is
updated. `make lint` and the docs `v-pre` rule apply as usual.
