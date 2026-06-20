# Plan: Codegen child-component props + `{children}` (Phase 3)

**Date:** 2026-06-20
**Branch:** `feat/codegen-child-props` off `main`
**Design:** `specs/2026-06-18-gsx-templating-design.md` §3.3 (children/attrs),
`specs/2026-06-19-gsx-codegen-design.md` (Child components).
**Status:** ready for SDD

## Goal

A parent component can pass props and children to a child component:
`<Card title={x} featured>kids</Card>` →
`gw.Node(ctx, Card(CardProps{Title: x, Featured: true, Children: <kids closure>}))`.
Today `genChildComponent` errors on any attrs or children.

## Scope

**IN:**
- **Child props from attrs**: static/expr/bool attrs map to props struct fields by
  name (`fieldName`: first-letter-upper). `title="hi"` → `Title: "hi"`;
  `count={n}` → `Count: n`; `featured` → `Featured: true`. Works for local
  (`<Card …>`) and dotted/cross-package (`<ui.Button …>`) child tags.
- **`{children}` placement**: a component whose body references `{children}` (a
  bare `children` interpolation) gets a synthesized `Children gsx.Node` props field
  and a `children := _gsxp.Children` local binding; `{children}` then renders via
  the existing type-aware interp path (`gsx.Node` → catNode). A parent's
  `<Card>kids</Card>` passes the kids as a `gsx.Func` closure assigned to
  `Children`. Passing children to a component that has no `Children` field is a Go
  compile error (unknown field, via `//line` maps) — acceptable for v1.

**OUT (deferred — clear errors, not silent):**
- **Named slots via markup attrs** (`header={ <h1>…</h1> }`, `*ast.MarkupAttr`) →
  error "not supported yet"; separate slice.
- **Auto-fallthrough** (undeclared attrs → single root) and the **`Attrs` prop /
  `{...attrs}`** on a component → Phase 7. A spread or a hyphenated/non-prop attr
  on a component stays an error (a hyphenated name produces an invalid Go field —
  guard with a clear codegen error rather than emitting broken code).
- **class/spread/conditional/pipeline attrs ON a child component** → error
  "not supported on components yet" (child attrs are simple value mappings here).
- Compile-time "children passed but not placed" gsx-level diagnostic (rely on Go's
  unknown-field error for now).

## Key facts (grounded)

- `genChildComponent(b, el)` emits `_gsxgw.Node(ctx, <Tag>(<Tag>Props{}))`. For a
  dotted tag `ui.Button`, `el.Tag + "Props"` = `ui.ButtonProps` and the call is
  `ui.Button(ui.ButtonProps{})` — string concat already handles both forms.
- `emitProbes` component-tag case emits `_ = <Tag>(<Tag>Props{})`.
- `collectExprs` SKIPS component-tag elements entirely (attrs AND children).
- Render closure shape (mirror for the children slot):
  `gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { _gsxgw := gsx.W(_gsxw); …; return _gsxgw.Err() })`.
- `fieldName(p)` = first-letter-upper. Props struct built from params in
  `genComponent`/`buildSkeleton`.
- Runtime referenced as `gsx`; writer `_gsxgw`; `ctx` ambient; reserved `_gsx*`.

## Tasks

### Task 1: Child-component props from attributes

Order-invariant-NEUTRAL (child attrs feed the props literal, not the `_gsxuse`
sequence; `collectExprs` keeps skipping component elements).

- **`genChildComponent`** (emit.go): build the props literal from `el.Attrs`:
  - `*StaticAttr` → `fieldName(Name): strconv.Quote(Value)`.
  - `*ExprAttr` → `fieldName(Name): strings.TrimSpace(Expr)`. Reject `Try` and
    non-empty `Stages` (pipeline on a child prop) with a clear error (deferred).
  - `*BoolAttr` → `fieldName(Name): true`.
  - `*ClassAttr` / `*SpreadAttr` / `*CondAttr` / `*MarkupAttr` → error
    "<kind> attribute on a component not supported yet".
  - Validate `Name` is a Go identifier (no `-`); else error "non-identifier
    attribute %q on component %s (attribute fallthrough not supported yet)".
  - Emit `_gsxgw.Node(ctx, <Tag>(<Tag>Props{Field1: v1, Field2: v2}))`. Keep
    erroring on `el.Children` (deferred to Task 2).
- **`emitProbes`** (analyze.go) component-tag case: emit the SAME props literal —
  `_ = <Tag>(<Tag>Props{Field1: v1, …})` — so the assignment type-checks each expr
  against the child's real prop field type (cross-package props resolve via the
  already-loaded go/packages deps). Factor the props-literal string builder into a
  shared helper used by BOTH genChildComponent and emitProbes (one builder, no
  drift), OR keep them trivially identical and tested. (Static values must be
  `strconv.Quote`d identically in both.)
- **usedParams** (analyze.go): bind params referenced in a child component's attr
  exprs (the props-literal exprs reference parent params/locals; they must be bound
  in the skeleton + render func). `collectAttrSrc` currently skips component tags —
  add child-component attr-expr ident collection (Expr of each `*ExprAttr`). Do NOT
  add them to `collectExprs`/the `_gsxuse` node sequence (they're not probed via
  `_gsxuse`).

Tests (e2e): `<Card title={x} featured count={n}/>` with
`component Card(title string, featured bool, count int)` in the same file — render
and assert the child output reflects the passed props (true/false featured branch,
the title, the count). A static-attr child prop (`title="hi"`). A param used ONLY
in a child prop expr is bound (no `undefined`). Cross-component in one file.
Error tests: a pipeline/`?` on a child prop; a class/spread/cond attr on a
component; a hyphenated attr on a component.

Commit: `codegen: child-component props from attributes`.

### Task 2: `{children}` placement

Order-invariant-SENSITIVE: a child element's slot content (its `Children` markup)
renders in the PARENT scope, so those interps/exprs must be collected/probed in
order.

- **Detect children usage**: a helper `usesChildren(body []ast.Markup) bool` that
  walks the body (recursing control flow + non-component element children + frag)
  and reports whether any `*Interp` has `strings.TrimSpace(Expr) == "children"`.
  (Mirror the markup walk used elsewhere.) A `children` reference inside a nested
  element still counts.
- **Synthesize the field + binding** (genComponent + buildSkeleton): when
  `usesChildren(c.Body)`, append `Children gsx.Node` to the props struct AND bind
  `children := _gsxp.Children` as a local (alongside used params). Then a bare
  `{children}` interp resolves: `children` is `gsx.Node` → existing emitRender
  catNode path writes it. (The `{children}` interp is already in collectExprs as a
  normal `*Interp`; the local binding makes it type-check.)
- **Parent passes the slot** (genChildComponent): when `len(el.Children) > 0`,
  add `Children: gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {
  _gsxgw := gsx.W(_gsxw); <genNode each child>; return _gsxgw.Err() })` to the
  props literal. (A child element with children but whose props type lacks
  `Children` → Go unknown-field error, acceptable.) Stop erroring on
  `el.Children`.
- **Probe the slot content in parent scope** (collectExprs + emitProbes): a
  component-tag element must now RECURSE INTO ITS CHILDREN (slot content rendered
  in the parent), while still SKIPPING its attrs (props, type-checked via the props
  literal, not `_gsxuse`). Change the component-tag case in BOTH:
  - `collectExprs`: for a component element, `collectExprs(t.Children, out)` (do
    NOT walk its attrs). This appends the slot's interps/exprs in source order.
  - `emitProbes`: emit the props literal `_ = Tag(TagProps{…})` (Task 1), THEN
    `emitProbes(t.Children)` for the slot content. The slot interps become
    `_gsxuse(...)` in the same order collectExprs collected them. (The props-literal
    exprs are NOT `_gsxuse`, so they don't perturb the k-th alignment.)
  - Keep the two walks identical for the children recursion — the k-th `_gsxuse`
    must map to the k-th collectExprs node. Add an interleaved test (a child
    component with slot content containing a typed interp, between sibling typed
    interps) to guard alignment.
- **usedParams**: the slot content renders in the parent, so its interps already
  flow through `componentExprs`/`collectExprs` (now recursing component children) →
  bound. Confirm a param used ONLY inside slot content is bound.

Tests (e2e): `component Card(title string) { <section><h2>{title}</h2>{children}</section> }`
and a parent `<Card title="Hi"><p>body</p></Card>` → assert nested output with the
slot placed. A `{children}` referenced but parent passes none (empty Children →
renders nothing / nil-safe — confirm gsx.Node nil renders empty). Slot content
referencing a parent param/loop var. Interleaved order-invariant test. (Optional:
children-not-placed → Go unknown-field error, document.)

Commit: `codegen: {children} slot placement for child components`.

## After tasks
- Final whole-feature review (adversarial: order invariant with slot content
  interleaved; cross-package child props type-check; static-value quoting; reserved
  identifier hygiene in the slot closure; nil children).
- Independent adversarial review with live probing (merge gate).
- Merge `--no-ff`; update ROADMAP (phase-2 #5 child props + children done; named
  slots + auto-fallthrough + Attrs prop still pending).

## Risks
- **Order invariant (Task 2).** Component-element children move from "skipped" to
  "recursed" in collectExprs AND emitProbes — they must change together and stay
  ordered. The interleaved test is the guard.
- **Children closure scoping.** The slot closure shadows `_gsxgw`/`ctx` — fine in
  Go (nested closure), inner genNode uses the inner `_gsxgw`. Confirm the slot
  renders the parent's markup with parent params in scope (they are — the closure
  is emitted inline in the parent render func body, capturing its locals).
- **`children` as a reserved-ish name.** Binding a `children` local could collide
  with a user param named `children`. `checkReservedParams` should reject a param
  named `children` (it's now synthesized) — add it to the reserved set if not
  present, or document the collision. Verify.
