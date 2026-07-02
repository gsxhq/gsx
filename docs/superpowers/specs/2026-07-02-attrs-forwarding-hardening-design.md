# Design: Attribute-forwarding hardening (caller-wins gaps)

**Date:** 2026-07-02
**Status:** DECIDED
**Extends:** `2026-06-30-explicit-attribute-forwarding-design.md`

## Problem

The explicit-forwarding contract — attrs before `{ attrs... }` are
caller-overridable, attrs after it are component-forced, class/style always
merge — has three shapes that silently break it (verified by live probes
against `4ea662e`):

1. **Cond-attrs defeat caller-wins.**
   `<button { if active { id="mine" } } { attrs... }>` called with `id="caller"`
   renders `<button id="mine" id="caller">` — duplicate attribute, and the
   component wins despite the pre-spread position (browsers take the first
   occurrence).
2. **A second spread duplicates keys and renders out of source order.**
   `<div { attrs... } { extra... }>` renders `extra` *before* the bag (inline
   emit vs. phase-3 spread) with no key resolution between the two.
3. **Derived-bag spreads lose all forwarding semantics.**
   `{ attrs.Without("id")... }` is not recognized as the forwarding position
   (detection is `Expr == "attrs"` exactly), so it takes the plain inline
   `gw.Spread` path: no class/style merge, no caller-wins guards —
   `<aside class="base" …>` called with `class="extra"` renders
   `class="base" class="extra"`.

Additionally the positional exemption for class/style (a post-spread
`class="forced"` merges rather than forces) is implemented and spec'd but
pinned by no corpus case and absent from the guide.

## Decisions

### D1 — Any `attrs`-referencing spread is the forwarding position

A `SpreadAttr` whose expression references the bare identifier `attrs`
(token-based, the same `valueIdents` detector as `usesAttrs`) is THE
forwarding spread of its element. `{ attrs.Without("id")... }`,
`{ attrs.Merge(extra)... }`, etc. get the full machinery: class/style merge,
caller-wins guards on pre-spread attrs, forced-name exclusion for post-spread
attrs.

The expression is evaluated **once**, hoisted to a temp
(`_gsxbag<N> := attrs.Without("id")`) emitted before the attribute section;
guards (`.Has`), class/style merge (`.Class()` / `.Style()`), and the spread
(`.Without(...)`) all read the temp. The bare `attrs` spread keeps using the
existing local directly (no temp).

Rationale: the derived spread *looks* like forwarding-with-exclusion and that
is always the author's intent; the machinery is already parameterized on
`bagExpr`, so this is the real implementation, not a special case. It also
makes "component keeps final say on class" expressible:
`{ attrs.Without("class")... }` → the root's own class stands, the caller's
class is dropped.

A non-`attrs` spread (e.g. a `gsx.Attrs`-typed prop: `{ extra... }`) on an
element **without** a forwarding spread keeps today's inline behavior.

### D2 — One spread per forwarding element

On an element that carries a forwarding spread, any **other** `SpreadAttr`
(second `attrs`-derived spread or a non-`attrs` spread) is a diagnostic:

> more than one spread on an element with { attrs... }; merge them into one
> spread ({ attrs.Merge(x)... } or { x.Merge(attrs)... }) so precedence is
> explicit

Rationale: expressing three-way key layering (scalar between two spreads)
statically would require lowering the whole attribute section to a runtime
bag, which loses per-context escaping (gw.URL etc. — a security regression)
and allocates on every render. `Attrs.Merge` already implements JS-spread
last-wins composition; requiring it makes precedence visible in source.
Elements without a forwarding spread are out of scope (multiple inline
spreads keep today's behavior).

### D3 — Cond-attrs participate in caller-wins

- **Pre-spread** `CondAttr`: every branch attr with a static name
  (Static/Bool/Expr/Embedded) emits under `if !<bag>.Has(name)` inside its
  branch. Caller override works; no duplicate emission.
- **Post-spread** `CondAttr` (forced): the branch selection is evaluated
  exactly once into an int temp (`_gsxbr<N>`) *before* the spread; the taken
  branch's attr names are appended to a dynamic drop slice
  (`_gsxdrop<N> := []string{"class","style",<static forced>…}`), the spread
  becomes `.Without(_gsxdrop<N>...)`, and the branch attrs emit after the
  spread by switching on the temp. Single evaluation preserves else-if
  short-circuit semantics for conditions with side effects.
- **class/style (or a nested spread) inside a cond branch on a forwarding
  element** is a diagnostic: the root's class/style merge is emitted once at a
  static site and cannot account for a conditional contribution; the
  composable form (`class={ "badge": cond }`) already expresses this.
  On non-forwarding elements branch class/style stay supported (pinned by
  `attrs/cond_attr_bool_on`).

### D4 — Pin and document the class/style positional exemption

- Corpus case: `<div { attrs... } class="forced">` + caller class → merged
  (`class="forced w-full"`), i.e. class is never "forced" positionally.
- Corpus case: `{ attrs.Without("class")... }` → root class wins outright
  (the new idiom enabled by D1).
- Guide (`composition.md`): state the positional rule AND its class/style
  exemption in the forwarding section, with the `Without("class")` idiom.

### D5 — Structural cleanup (behavior-preserving)

`emitFallthroughAttrs` collects post-spread forced names by *emitting* into a
side buffer through a reassigned closure-captured parameter
(`saved := b; b = &forcedBuf`). Forced names are pure AST facts
(`rootAttrName` over `attrs[splitIdx+1:]`); compute them in a pre-pass and
emit in one ordered walk with no buffering. Also: fix the stale
"auto single-root" comment (AUTO mode was removed 2026-06-30), inline the
`hasFallthrough := manual` alias, and correct comment spelling of the spread
(`{...attrs}` → `{ attrs... }`) across emit.go/analyze.go.

## Non-goals

- Renaming the internal "fallthrough" vocabulary or diagnostic codes
  (`attr-fallthrough`) to "forwarding" — churn deferred; this spec is the
  pointer.
- Multiple inline spreads on non-forwarding elements (unchanged).
- Runtime `Attrs`/`Merge`/`Spread` semantics (unchanged; the class-at-first
  vs class-at-last position asymmetry between Merge and Spread is cosmetic
  and out of scope).
