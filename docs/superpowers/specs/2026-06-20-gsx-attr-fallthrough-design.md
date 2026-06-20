# Design: Attribute fallthrough (Vue-style) — Phase 7

**Date:** 2026-06-20
**Status:** PROPOSED (needs approval before SDD)
**Design refs:** `2026-06-18-gsx-templating-design.md` §3.3 (children/attrs),
`examples/12_children_attrs.gsx`.

## The feature

A caller can put attributes a component does NOT declare as props on the
invocation, and they "fall through" onto the component's root element:

```go
component Button(variant string) {
    <button type="button" class={ "btn", variantClass(variant) }>{children}</button>
}
component Toolbar() {
    <Button variant="primary" class="w-full" data-test="save" hx-post="/save" @click="go()">Save</Button>
}
```
- `variant` → a prop (declared). `class`/`data-test`/`hx-post`/`@click` → NOT
  declared → fall through to `<button>`.
- `class` **merges** with the root's class → `btn btn-primary w-full`. The rest are
  **added** to `<button>`.
- No `{...attrs}`, no boilerplate. This is Vue's default `inheritAttrs`.

## Architecture — two sides, mediated by a synthesized `Attrs` field

### Call site (passing fallthrough)
Split the invocation's attrs into **declared props** vs **fallthrough**, by whether
`fieldName(name)` is a field of the child's props struct. Emit:
```go
Button(ButtonProps{Variant: "primary", Attrs: gsx.Attrs{
    "class": "w-full", "data-test": "save", "hx-post": "/save", "@click": "go()",
}})
```
- **The split needs the child's prop field names.** Get them from `go/types`: the
  child's `<Tag>Props` struct (same-package skeleton OR cross-package compiled) is
  in the type-checked package; enumerate `*types.Struct` fields. This is uniform
  same/cross-package. (We already run `go/types`; this is one more query captured
  during the resolution pass and threaded to emission.)
- A **non-identifier** attr (`data-test`, `@click`, `hx-post`) can never be a prop
  (not a Go field name) → always fallthrough. An **identifier** attr (`class`,
  `variant`) is a prop iff the struct has that field, else fallthrough.
- If the child is NOT fallthrough-eligible (no synthesized `Attrs` field) but the
  caller passes fallthrough attrs → assigning `Attrs:` → **Go "unknown field Attrs"
  error** via `//line`. That IS the ambiguity diagnostic (free, correct).

### Child side (receiving fallthrough)
A **fallthrough-eligible** component synthesizes an `Attrs gsx.Attrs` props field and
applies it at its single root element:
- **Eligible** = the body has exactly one root *HTML element* (ignoring pure-whitespace
  text and comments) AND does not opt out. Fragment / multiple roots / a control-flow
  root (`{ if … }`) / a child-component root → NOT eligible → no `Attrs` field.
- **Apply at the root**: merge `Attrs`' class into the root's class; spread the rest:
  ```
  <button type="button"  ← root's own static/bool attrs emit normally
    [class merged: gw.Class(rootParts…, gsx.Class(_gsxp.Attrs.Class()))]
    [gw.Spread(ctx, _gsxp.Attrs.Without("class","style"))]
  >
  ```

## Key decisions (recommendations — please confirm/redirect)

**D1. Split mechanism → `go/types` prop-field query (uniform same+cross-package).**
Alternative was same-package-only via the AST (simpler, but cross-package
`<ui.Button>` is common). Recommend the `go/types` query so fallthrough works
everywhere. *Cost:* capture each child invocation's prop-field set during resolution
and thread it to `genChildComponent`.

**D2. Scope → AUTO + MANUAL mode. (DECIDED.)** Auto: single HTML-element root,
class-merge + spread. Manual: a component whose body references `attrs` (a
`{...attrs}` spread, or `attrs` in an expr) takes over placement — auto root
injection is DISABLED, `attrs := _gsxp.Attrs` is bound as a local, and the author's
existing `{...attrs}` (Phase-2 element spread) renders it. **Style fallthrough is
still deferred** (style composition is fail-closed; `style` is dropped from the
auto-applied bag, documented, pending `|> css` — the bag still carries it for manual
`{...attrs}` where the author owns escaping via the trusted `Attrs` contract).

**D3. Ambiguity → Go "unknown field Attrs" error (v1), upgrade to a gsx diagnostic
later.** A caller passing fallthrough attrs to an ambiguous-root component gets a Go
compile error via `//line` (points at the `.gsx`). A *clean* gsx-level message
("component <X> has no single root; cannot accept fallthrough attributes") is nicer
but needs the call site to know the child's root shape (cross-package = unavailable
without more plumbing) — recommend **defer the pretty message**; the Go error is
correct and points at the right place.

**D4. The root class-merge "empty class" problem.** If the root has NO `class` and the
bag has none either, naive `gw.Class(gsx.Class(""))` emits `class=""`. Recommend a
small **runtime helper** `gw.ClassMerged(bagClass string, parts ...ClassPart)` (or
make the root emission conditional) that emits the `class` attribute only when the
merged tokens are non-empty — so a no-class root with no fallthrough class stays
clean. (Runtime is ours, stdlib-only; this is a 10-line addition.)

**D5. Conflict rule → ROOT WINS (caller dropped). (DECIDED.)** `class`/`style`
**merge** (root parts then caller's, via `ClassMerger`). For any OTHER attr the root
statically sets, the caller's same-named fallthrough value is **dropped** — the
component's explicit attr is authoritative (Vue semantics). Implementation: at the
root, spread `_gsxp.Attrs.Without(<root's own attr names> + "class","style")` so the
bag never duplicates a root attr; class merges separately. The root's own attr-name
set is known at emit time (the root element's AST).

## Proposed SDD task breakdown (after approval)

1. **Prop-field capture** — during resolution, for each child-component invocation
   record its props-struct field-name set (`go/types`); thread to emit. (Infra; no
   behavior change yet — child invocations still treat all attrs as props, but the
   field set is now available.)
2. **Call-site split** — `genChildComponent`/probe split attrs into props vs
   fallthrough using the field set; emit `Attrs: gsx.Attrs{…}` for fallthrough.
   Non-identifier attrs → fallthrough. (Child still ignores `Attrs` → no visible
   effect yet unless the child has the field — so pair with task 3, or gate behind
   a child that declares it.)
3. **Child eligibility + root application** — detect single-root eligibility;
   synthesize the `Attrs` field (genComponent + buildSkeleton); apply at the root
   (class-merge via the D4 runtime helper + `Spread` of the rest, with D5 de-dup).
4. **End-to-end + ambiguity** — example-12 Button/Toolbar render golden; ambiguity
   (fragment-root component + fallthrough attrs → error); cross-package split; the
   reserved `attrs`/`Attrs` name handling.

Each is an SDD slice with per-task + independent review, as usual. Tasks 1–2 are
order-invariant-neutral (fallthrough attrs feed the props literal, not `_gsxuse`);
task 3 touches the root emission and the props-struct synthesis (skeleton/emit
parity, like `{children}`).

## Risks
- **Prop-field capture timing** — the field set must be resolved before emission;
  validate `go/types` gives the (skeleton) `<Tag>Props` struct fields during the
  existing pass.
- **Root application is the subtle part** (D4/D5) — class-merge correctness, empty
  class, de-dup. The example-12 golden is the guard.
- **Reserved name** — `Attrs`/`attrs` as a user param must be rejected (it's now
  synthesized), like `children`.
