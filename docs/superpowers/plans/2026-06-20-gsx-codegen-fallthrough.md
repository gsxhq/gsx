# Plan: Codegen attribute fallthrough (Phase 7)

> **Superseded implementation plan:** Retained as history. Current execution is
> tracked by `2026-07-14-verbatim-component-signatures.md`.

**Date:** 2026-06-20
**Branch:** `feat/codegen-fallthrough` off `main`
**Design:** `specs/2026-06-20-gsx-attr-fallthrough-design.md` (approved; D2 = auto+manual,
D5 = root-wins).
**Status:** ready for SDD

## Goal

Undeclared attributes on a component invocation fall through to the component's root
(auto), or to wherever the component places `{...attrs}` (manual). class/style merge;
other attrs add; the root's explicitly-set attrs win over the caller's. Example 12
(`Button`/`Toolbar`) renders.

## Architecture (recap)

- **Child side:** a fallthrough-eligible component has a synthesized `Attrs gsx.Attrs`
  props field. **Auto** (single HTML-element root, no `attrs` reference): the root
  merges `Attrs`' class and spreads the rest (minus the root's own attr names, class,
  style). **Manual** (body references `attrs`): bind `attrs := _gsxp.Attrs`; the
  author's `{...attrs}` (Phase-2 element spread) places it; no auto root injection.
- **Call site:** split invocation attrs into declared props (props struct fields, via
  `go/types`) vs fallthrough (everything else, incl. all non-identifier names) →
  `Attrs: gsx.Attrs{…}`. A non-eligible child has no `Attrs` field → Go unknown-field
  error (the ambiguity diagnostic).

## Task ordering (each stays green)

Child-side synthesis lands FIRST so the `Attrs` field exists before any call site
populates it. Before task 3, no call site emits `Attrs:`, so the field is always nil
→ empty-bag merge/spread is a no-op → existing render goldens unchanged (source
golden `greeting.x.go.golden` updates for the new field + root emission).

### Task 1: Child auto-fallthrough — `Attrs` field + root application + runtime helper

- **Runtime** (root repo, stdlib-only): add `(gw *Writer) ClassMerged(extra string,
  parts ...ClassPart)` — composes `parts` + (when non-empty) `extra` and writes the
  `class` attribute ONLY when the merged token set is non-empty (so a no-class root
  with an empty bag stays clean). Unit-test it. (D4.)
- **Eligibility** (codegen): `singleRoot(c.Body) (*ast.Element, bool)` — exactly one
  top-level node that is a non-component `*ast.Element`, ignoring pure-whitespace
  `*ast.Text` and comments. Multiple roots / fragment / control-flow root /
  component-tag root → not single-root.
- **Synthesize the field** (genComponent + buildSkeleton, in lockstep): when
  eligible (this task: `singleRoot` true — manual trigger added in task 4), append
  `Attrs gsx.Attrs` (skeleton: `_gsxrt.Attrs`) to the props struct. Mirror the
  `Children` synthesis pattern exactly (skeleton/emit parity).
- **Apply at the root** (genNode/emitAttr for the single root element only — thread a
  flag identifying the root, or special-case it in genComponent's body emission):
  - **class:** route the root's class through `ClassMerged`/`Class` so the bag's
    class merges. Root has composed class `{ … }` → `gw.Class(parts…,
    gsx.Class(_gsxp.Attrs.Class()))`; static `class="x"` → `gw.Class(gsx.Class("x"),
    gsx.Class(_gsxp.Attrs.Class()))`; NO class attr → `gw.ClassMerged(
    _gsxp.Attrs.Class())` emitted in attr position. (style unchanged / fail-closed.)
  - **other attrs:** after the root's own attrs, emit `gw.Spread(ctx,
    _gsxp.Attrs.Without(<root's own static/bool/expr attr names>, "class", "style"))`
    — D5 root-wins (drop bag keys the root sets) + style deferred.
  - The bag is nil for not-yet-fallthrough callers → all no-ops (Class("")=no tokens,
    Spread(empty)=nothing). Existing HTML goldens stay identical.
- **Reserved name:** `checkReservedParams` rejects a param named `attrs` (now
  synthesized), like `children`.
- Tests: a single-root component constructed directly with an `Attrs` bag (from the
  harness: `p.Button(p.ButtonProps{Variant:"primary", Attrs: gsx.Attrs{"class":
  "w-full", "data-test":"x"}})`) → root has merged class + the extra attr; an empty
  bag → unchanged; a multi-root / fragment component does NOT get the field; a static
  class root merges; a no-class root with a bag class emits class. Reserved-`attrs`
  error. Update `greeting.x.go.golden`.

Commit: `codegen: child auto-fallthrough (Attrs field + root class-merge/spread)`.

### Task 2: Prop-field capture (go/types)

Infra to support the call-site split — no behavior change yet.

- During `resolveTypesPkg`, for each child-component invocation (a component-tag
  `*ast.Element`), resolve its `<Tag>Props` struct type and record the set of field
  NAMES. Return a `map[ast.Node]map[string]bool` (element → prop field names)
  alongside `resolved`. The `<Tag>Props` type is in the type-checked package
  (same-package skeleton OR cross-package compiled). Enumerate via
  `types.Struct.NumFields()/Field(i).Name()`. For a nullary method call (no props
  struct) the set is empty/absent. Thread it to `generateFile`/`genChildComponent`.
- Test (white-box): the captured field set for a same-file `<Card .../>` includes its
  declared props + `Attrs`/`Children` (synthesized); a cross-package child resolves
  too (or note same-package coverage and defer cross-package to task 4 if the harness
  makes a 2nd package hard).

Commit: `codegen: capture child props-struct field names (go/types) for split`.

### Task 3: Call-site split — props vs fallthrough

- `childPropsLiteral` / `genChildComponent` / the probe: for each invocation attr,
  classify via the task-2 field set: `fieldName(name)` is a declared prop field
  (NOT `Attrs`) → a props field (today's behavior); else → FALLTHROUGH. A
  non-identifier name (`data-test`, `@click`, `hx-post`) → always fallthrough. Collect
  fallthrough attrs into an `Attrs: gsx.Attrs{<name>: <value>, …}` field (static →
  string literal; bool → `true`; expr → the expr; class composed/cond on a component
  → still error or route as a bag entry? keep erroring for now). Names are the RAW
  attr names (`"data-test"`, `"@click"`) as bag keys.
- Probe mirrors (emit ≡ probe): the probe props literal includes the same `Attrs`
  field so the assignment type-checks (a non-eligible child → unknown-field error
  surfaces in BOTH probe and emit consistently).
- Order invariant untouched (fallthrough attrs feed the props literal / bag, not
  `_gsxuse`).
- Tests: example-12-style — `component Button(variant string) { <button type="button"
  class={ "btn", variantClass(variant) }>{children}</button> }` + `<Button
  variant="primary" class="w-full" data-test="save" hx-post="/save">Save</Button>` →
  `<button type="button" class="btn btn-primary w-full" data-test="save"
  hx-post="/save">Save</button>` (declared `variant` is a prop; rest fall through;
  class merges; root's `type` wins). A fallthrough attr to a NON-eligible (multi-root)
  component → clean error.

Commit: `codegen: call-site attribute split (props vs fallthrough → Attrs bag)`.

### Task 4: Manual mode + ambiguity polish + cross-package + goldens

- **Manual mode**: `usesAttrs(c.Body) bool` — the body references the identifier
  `attrs` (a `{...attrs}` SpreadAttr with Expr `attrs`, or `attrs` in any interp/expr
  via the ident walks). When true: the component is eligible (synthesize `Attrs`
  field) but auto root injection is DISABLED; bind `attrs := _gsxp.Attrs` as a local
  (alongside used params / `children`); the author's existing `{...attrs}` element
  spread renders it (Phase-2). Eligibility becomes `singleRoot || usesAttrs`.
- **Cross-package** split (if not already covered in task 2): a `<ui.Button …>`
  invocation splits via the imported `ui.ButtonProps` fields. Add a 2-package harness
  test.
- **Goldens / ambiguity**: graduate `examples/12_children_attrs.gsx` toward a render
  golden if feasible; an ambiguous-root component receiving fallthrough → the Go
  unknown-field error (assert it errors). A manual-mode `<Widget id="x" {...}>` test.
- Update ROADMAP (#7 done; note style-fallthrough + pretty-ambiguity-diagnostic
  deferred).

Commit: `codegen: manual-mode fallthrough ({...attrs}) + cross-package + goldens`.

## After tasks
- Final whole-feature review; independent adversarial review with live probing
  (merge gate — focus: class-merge correctness, root-wins de-dup, empty-bag no-op,
  eligibility detection, manual/auto switch, cross-package split, ambiguity error).
- Merge `--no-ff`; ROADMAP update.

## Risks
- **Root application is the subtle core** — class-merge (static vs composed vs none),
  the empty-class guard (`ClassMerged`), root-wins de-dup (`Without` the root's attr
  names). The example-12 golden + targeted unit tests are the guard.
- **Unconditional `Attrs` on every single-root component** — bloats every props
  struct + adds a root Spread/Class. Empty-bag path is a no-op (cheap); accepted per
  the Vue model (a component can't know its callers). Source golden churn expected.
- **Skeleton/emit parity** for the synthesized field + root application (like
  `{children}` / method components) — drive both from shared helpers.
- **`attrs`/`Attrs` reserved name** — reject as a user param.
