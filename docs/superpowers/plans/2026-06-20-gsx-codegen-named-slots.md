# Plan: Codegen named slots (markup attributes) (Phase 5)

**Date:** 2026-06-20
**Branch:** `feat/codegen-named-slots` off `main`
**Design:** `specs/2026-06-18-gsx-templating-design.md` §3.3 (named slots are
`gsx.Node` params placed explicitly).
**Status:** ready for SDD

## Goal

Support a **markup attribute** on a component invocation:
`<Panel header={ <h1>Title</h1> }>kids</Panel>` → the `header={…markup…}` lowers to
a `gsx.Func` closure assigned to the `Header` prop field. The component declares
`header gsx.Node` and places it via `{header}` (already works — a `gsx.Node` param
renders via the existing catNode path). Today `childPropsFields` errors on
`*ast.MarkupAttr`.

A named slot is just a `{children}`-style slot bound to a NAMED, DECLARED prop —
so it reuses the Phase-3 slot-closure machinery. No component-side changes; no
field synthesis (the field already exists, declared as a `gsx.Node` param).

## Key facts (grounded)

- `ast.MarkupAttr{Name string, Value []Markup}` — `header={ <h1/> }` → Value is the
  markup node list. Parser already produces it (parser/markup.go:519).
- `childPropsFields(el)` (emit.go) is the SHARED builder for both genChildComponent
  (emit) and emitProbes (probe). It currently errors on MarkupAttr (emit.go:756).
- `{children}` precedent (Phase 3): the slot VALUE differs between emit (real
  `gsx.Func` closure) and probe (`_gsxrt.Node(nil)` typed-nil); the Children field
  is appended by each caller OUTSIDE childPropsFields. Slot CONTENT is probed in the
  PARENT scope by recursing the markup in collectExprs + emitProbes.
- Slot closure shape: `gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {
  _gsxgw := gsx.W(_gsxw); <genNode each markup node>; return _gsxgw.Err() })`.
- Order invariant: collectExprs ≡ emitProbes ≡ harvest order. For a component
  element today: skip attrs, recurse el.Children. Markup-attr VALUES also render in
  the parent and carry interps → they must be collected/probed too.

## The order invariant extension (load-bearing)

For a component-tag element, define the canonical type-needing-node order as:
**[each MarkupAttr.Value, in el.Attrs order] then [el.Children]**. (Attrs precede
children textually, matching the existing "attr-exprs before children" rule.) BOTH
collectExprs and emitProbes must walk this identically:
- collectExprs (component case): for each `*MarkupAttr` in el.Attrs (in order),
  `collectExprs(ma.Value, out)`; then `collectExprs(el.Children, out)`. Still skip
  the simple/prop attrs (static/expr/bool — type-checked via the props literal, not
  `_gsxuse`).
- emitProbes (component case): emit the props literal `_ = Tag(TagProps{… simple
  fields …, Header: _gsxrt.Node(nil), Children: _gsxrt.Node(nil) …})` (NO `_gsxuse`),
  THEN for each `*MarkupAttr.Value` (in attr order) `emitProbes(ma.Value)`, THEN
  `emitProbes(el.Children)`. The markup interps become the `_gsxuse` sequence, in the
  same order collectExprs collected them.
- harvest unchanged (maps k-th `_gsxuse` to k-th collectExprs node).

## Task: Named slots (markup attributes on components)

Single SDD slice (the machinery exists; the change is contained but
order-invariant-sensitive).

- **childPropsFields decomposition** (emit.go): MarkupAttr (like Children) cannot be
  a plain field in the shared builder because its VALUE differs emit vs probe. Pick
  the cleanest of:
  (a) childPropsFields collects simple fields AND returns the list of MarkupAttrs
      separately, so each caller renders the markup field value; OR
  (b) childPropsFields takes a `markupValue func(*ast.MarkupAttr) string` callback
      that the caller supplies (emit → closure; probe → `_gsxrt.Node(nil)`).
  Mirror how Phase-3 Children is composed (Children is appended outside the shared
  builder). Keep emit and probe agreeing on WHICH fields exist (a markup field is
  present for each MarkupAttr); only the VALUE differs. Validate `fieldName(ma.Name)`
  is a valid field (checkComponentAttrName — markup-attr names must be identifiers).
- **genChildComponent (emit)**: for each `*MarkupAttr`, emit field
  `<fieldName(Name)>: gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {
  _gsxgw := gsx.W(_gsxw); <genNode each ma.Value node, threading recvVar/recvTypeName/
  resolved/table/imports/fset>; return _gsxgw.Err() })`. Reuse the exact slot-closure
  emission Phase 3 uses for Children (factor a shared `emitSlotClosure(b, nodes, …)`
  helper if Children doesn't already have one — DRY the two).
- **emitProbes (probe)**: for each `*MarkupAttr`, the props-literal field is
  `<fieldName(Name)>: _gsxrt.Node(nil)` (typed-nil), and the value's markup is probed
  in parent scope via the order-invariant recursion above.
- **collectExprs (analyze.go)**: component case recurses each `*MarkupAttr.Value`
  (attr order) then el.Children — see the order section. (Today it only recurses
  el.Children.)
- **usedParams**: a param/loop-var referenced ONLY inside a markup-attr value must be
  bound (the closure renders in the parent scope). The markup interps flow through
  componentExprs/collectExprs (now recursing markup-attr values) → bound. ALSO the
  four Phase-3 sibling binding walks (collectClauseSrc, collectAttrExprSrc,
  collectChildPropExprSrc, bodyHasPipeline) must recurse markup-attr values so a
  clause/class/pipeline/nested-child-prop inside a named slot binds/detects. Confirm
  each reaches MarkupAttr.Value (they recurse component children for the {children}
  slot; markup-attr values are the analogous case — extend them).

Tests (e2e_test.go):
- `component Panel(header gsx.Node) { <div class="panel"><div class="hd">{header}</div></div> }`
  + parent `<Panel header={ <h1>Title</h1> }/>` → `<div class="panel"><div class="hd"><h1>Title</h1></div></div>`.
- Named slot + `{children}` together: `component Panel(header gsx.Node) { <div>{header}{children}</div> }`,
  `<Panel header={ <h1>T</h1> }><p>body</p></Panel>`.
- Markup-attr value referencing a PARENT param / loop var (binding).
- ORDER-INVARIANT test: a component invocation with a markup attr whose Value
  contains a typed interp of type X, plus a sibling typed interp of type Y before
  and Z after, plus children with type W — assert each renders with its own type.
- Multiple named slots on one component (`header={…} footer={…}`) — both placed.
- A `gsx.Node` param works standalone (regression: declared `header gsx.Node`,
  placed via `{header}`, passed a markup value).
- Error: a markup attr with a non-identifier name (`data-x={ <m/> }`) → clear error.
- A markup attr on an ELEMENT (not a component) — confirm current behavior (likely
  an error or unsupported; markup attrs are a component-slot concept). Check the
  parser/emit path and keep it erroring cleanly if not meaningful for elements.

Commit: `codegen: named slots (markup attributes on component invocations)`.

## After tasks
- Final whole-feature review (adversarial: order invariant with markup-attr values
  interleaved among children and sibling interps; emit ≡ probe field set; the slot
  closure scoping/binding; multiple slots; named slot + children together).
- Independent adversarial review with live probing (merge gate).
- Merge `--no-ff`; update ROADMAP (named slots done; auto-fallthrough remains #7).

## Scope cuts (deferred, clear errors)
- Markup attrs on non-component ELEMENTS (not a slot concept) — error cleanly.
- A markup attr whose name doesn't match a declared `gsx.Node` prop → Go
  unknown-field / type error via //line (acceptable; same as a mistyped prop).
- Pipeline/`?` on a markup attr — N/A (markup attrs carry markup, not a piped expr).

## Risks
- **Order invariant.** Markup-attr values move component-element traversal from
  "children only" to "markup-attr values then children" in collectExprs AND
  emitProbes — they must change together. The interleaved test is the guard.
- **emit ≡ probe field set.** Every MarkupAttr must appear as a field in BOTH the
  emitted props literal (closure) and the probe props literal (typed-nil), or the
  type-check disagrees with emission. Drive both from the same attr iteration.
- **Slot-closure DRY.** Children and named slots should share one closure emitter to
  avoid drift.
