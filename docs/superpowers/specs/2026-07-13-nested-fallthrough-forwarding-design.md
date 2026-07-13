# Fallthrough forwarding through nested component calls

**Date:** 2026-07-13
**Status:** approved design, pre-implementation
**Prior art:** `2026-07-07-attrs-only-component-values-design.md` §"Alternative
considered" (names this feature and its open questions),
`2026-07-10-bag-spread-hardening-design.md` (leaf-resolution principle),
PR #103 (tracks the fallthrough-without-Attrs diagnostic debt this spec folds in).

## Problem

Inside a `component` body, the implicit `attrs` bag is only recognized when it
flows to a **plain element**. Any reference to `attrs` inside a *nested
component invocation* fails with `undefined: attrs`:

```gsx
component SearchIcon() {
    <dsicon.Icon name="search" class="w-5 h-5" { attrs... }/>   // error: undefined: attrs
}
```

This blocks the wrapper-component pattern: a component cannot forward its own
fallthrough bag into a callee's synthesized bag. The attrs-only spec called it
"a composability hole that reaches beyond icons" and deferred it here.

**Root cause** (probe-verified): `usesAttrs` (`internal/codegen/emit.go:4182`)
decides whether the enclosing component is *manual mode* (synthesized
`Attrs gsx.Attrs` prop + `attrs := _gsxp.Attrs` local). Its component-element
branch walks only markup-valued attrs (`walkMarkupAttrs`) and deliberately
skips the element's Go-fragment attrs — so a `{ attrs... }` spread (or any
other `attrs` reference) on a nested component tag never triggers the binding,
in both the emit output and the probe skeleton. The call-site bag assembly
itself (`childPropsLiteral`, `emit.go:5550`) already emits those expressions;
they just don't resolve.

The related debt (PR #103): fallthrough onto a generated component whose body
never uses `attrs` has no worded diagnostic — same-package callers get the raw
go/types `unknown field Attrs in struct literal`, and cross-package, generate
exits 0 and the failure surfaces only at `go build` of the emitted `.x.go`.
Forwarding makes that path common, so this spec closes it too.

## Design

### Language semantics

One rule, no new syntax: **inside a `component` body, the implicit `attrs`
identifier binds in every Go-fragment position of a nested component
invocation**, exactly as it already does in plain-element and body positions.
Any such reference makes the enclosing component manual mode.

This is not a new rule — it restores the existing one. Usage synthesizes the
prop: *any* `attrs` reference in a component's body means the component has a
fallthrough bag in its props (`{children}` placement ⇒ `Children gsx.Node`,
same model). Today's detection walk simply skips Go fragments in nested
component-tag attr positions, so references there error instead of
synthesizing; `children` has no equivalent gap because `{children}` placement
is markup, and markup positions of nested tags are already walked.

- **Forwarding.** `<Inner { attrs... }/>` concatenates the wrapper's bag into
  `Inner`'s synthesized bag **at the spread's source position**, alongside
  bare fallthrough attrs, other spreads, and conditional attrs — the existing
  source-ordered `ConcatAttrs` assembly; an `attrs={{ }}` ordered literal
  still concatenates last. Nothing merges at the call boundary: duplicates
  resolve at the callee's **leaf** as today (last-wins scalars, aggregating
  `class`/`style`, leaf URL sanitization, the module's `class_merger`). This
  answers the merge-order questions the attrs-only spec left open — by
  construction, not by new machinery.
- **Chains** compose transitively (A→B→C is one concat per hop; each hop's
  bag is that component's own synthesized field).
- **Derived bags** work unchanged: `<Inner { attrs.Without("id")... }/>` —
  the spread expression is arbitrary Go, same as `{ extra... }` today.
- **Callee kinds.** All participate through the one `childPropsLiteral`
  path: generated components (same-package and cross-package dotted), method
  components, byo structs (which require their explicit `Attrs gsx.Attrs`
  field — the existing `byo-missing-attrs` error otherwise), and attrs-only
  component values (every call-site attr is bag there, so `{ attrs... }`
  joins the same source-ordered assembly).
- **Uniform binding, not spread-only.** Non-spread references bind too:
  `title={ attrs.Get("t") }` (prop value), `{ if attrs.Has("x") { data-y } }`
  (conditional), `class={ attrs.Class() }` (class prop part),
  `attrs={{ "data-x": attrs.Get("x") }}` (ordered-literal value), pipeline
  args. `attrs` is an ordinary local; there is no position where it half-works.
- **Unchanged:** passing attrs to a nullary component stays the existing
  clean diagnostic; element-side forwarding semantics (Has-guarded
  caller-wins before the spread, forced after, class/style always-merge) are
  untouched; multi-spread source-order merge applies on component calls as it
  does on elements.

**Compatibility:** every shape this enables is a hard `undefined: attrs`
error today, so no existing valid component changes behavior or synthesized
signature.

### Implementation

**Binding.** Extend `usesAttrs`'s component-element branch
(`emit.go:4213-4227`) to scan the element's attr list with `attrsRefAttrs`
(`emit.go:4267`), which already covers `SpreadAttr`, `ExprAttr` + pipeline
args, `ClassAttr` parts, `CondAttr` recursion, and `EmbeddedAttr` holes.
`attrsRefAttrs` grows one missing case: `OrderedAttrsAttr` pair values (legal
only on component tags, which is why it was never needed). The existing
`walkMarkupAttrs` recursion into named-slot markup values stays.

`usesAttrs` has five consumers — `emit.go:633`, `analyze.go:113/1214/3984`,
`variantcollide.go:51` — all sharing the single predicate, so props-struct
synthesis, the probe skeleton's `attrs` binding, the emitted local, and
variant-signature comparison move in lockstep. Emit ≡ probe holds by
construction; no new probe shapes.

**No new lowering.** `childPropsLiteral` (shared by `genChildComponent` and
`emitProbes`) already emits spread/prop/cond expressions verbatim into the
source-ordered `ConcatAttrs`/props literal for both passes. Once the local
binds, forwarding compiles through paths corpus-pinned for `{ extra... }`
(`components/component_spread.txtar`, `attrsonly/merge_order.txtar`, …).

**Worded diagnostic (`component-missing-attrs`), closing PR #103's debt.** At
the bag-assembly guard (`emit.go:5915`) where `byo-missing-attrs` fires for
byo callees, add the generated-props twin: when fallthrough pairs/segments
target a **generated** component whose prop map has no synthesized `Attrs`
(its body never references `attrs`), return a positioned `attrError`:

> attribute on `<Inner>` matches no declared prop of component `Inner`, and
> `Inner`'s body does not reference `attrs` (no fallthrough bag) — reference
> `attrs` in `Inner`'s body to accept forwarded attributes

positioned at the **first fallthrough attr** (keeping PR #103's observation
that the raw error at least anchored at the attribute — not `el.Pos()`).
Cross-package callees are covered: import-alias-scoped prop discovery already
records whether the synthesized bag exists. A dependency on the
`imported-props-unavailable` fallback keeps current behavior (we cannot
validate what we cannot analyze). Implementation-time verification point:
confirm the guard can distinguish "generated component without bag" from the
prop-name map `childPropsLiteral` already holds; if not, thread one bit from
prop discovery.

**Perf.** The `usesAttrs` extension is a pure AST walk — no `packages.Load`,
no additional probes, no cache-key change.

**Order of operations.** Merge docs-only PR #103 first; this spec's ROADMAP
edit then rewrites that entry as folded into this feature.

## Testing

New corpus group `internal/corpus/testdata/cases/nestedforward/` — every case
pins `input.gsx` + `generated.x.go.golden` + `render.golden`:

- **By callee kind:** generated same-package; cross-package dotted
  (`<ui.Button { attrs... }/>`); attrs-only component value (the icon-wrapper
  shape end-to-end); byo with `Attrs` field; method component.
- **Merge semantics:** one invocation interleaving statics, `{ attrs... }`,
  a conditional attr, and an `attrs={{ }}` literal (render golden proves
  source-order last-wins + literal-last); a class-carrying forwarded bag
  merging once at the callee's leaf; a chain A→B→C render golden.
- **Uniform binding:** prop value `title={ attrs.Get("t") }`; conditional
  `{ if attrs.Has(...) { … } }`; class prop `class={ attrs.Class() }`;
  ordered-literal value; derived bag `{ attrs.Without("id")... }`;
  forwarding combined with children/named slots.
- **Security:** a forwarded bag carrying `href`/`src` sanitized at the
  callee's leaf (spread-sanitize-style render golden) — no sanitization gap
  opens at the call boundary.
- **Rejections:** `component-missing-attrs` same-package and cross-package;
  nullary-callee and `imported-props-unavailable` behavior unchanged.

Plus unit tests for the `usesAttrs`/`attrsRefAttrs` extension, and a
`coverage.golden` manifest bump. No fmt-corpus case (no layout change) and no
sibling-grammar work (spreads on component tags already parse; tree-sitter's
authoritative gate syncs new corpus cases automatically).

## Documentation

Concise, per standing feedback: `composition.md` gains a short "Forwarding
through components" subsection (the rule, one example, merge order in one
sentence); `props.md` and `attributes.md` get one-line cross-references;
ROADMAP moves the tracked debt to done and updates item 7/18 pointers.
Playground example for the icon-wrapper recipe.

## Process

Worktree branch; subagent-driven execution with per-task reviews; one
independent adversarial reviewer with live probe programs before merge — the
cond-attr thunk `errReturn` shape this touches has bitten three prior PRs.

## Alternatives considered

- **Dedicated call-boundary merge machinery** — recognize `{ attrs... }` on
  component tags specially and merge caller/callee bags at the boundary.
  Rejected: duplicates what source-ordered `ConcatAttrs` + leaf resolution
  already guarantee, and boundary-time merging is what the 2026-07-10 bag
  hardening deliberately moved away from.
- **Implicit auto-forwarding** (inject the wrapper's bag into a single-root
  nested component with no spread written). Rejected: the 2026-06-30
  explicit-forwarding decision removed auto single-root injection; forwarding
  stays visible in the source.
- **Spread-only binding** (bind `attrs` only for `{ attrs... }`, keep other
  positions failing). Rejected: leaves an inconsistent seam where a local
  binds in one attr position but not its neighbor; uniform binding is the
  smaller rule.

## Out of scope

- Any change to element-side forwarding precedence or the leaf fold.
- Renderer/`class_merger` semantics (leaf-resolved, unchanged).
- Locals/struct-field tags (`<item.Icon/>`) — still not tag-callable, per the
  attrs-only spec's scope boundary.
