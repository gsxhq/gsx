# Component Value Effect Order

## Problem

Component attributes are collected into declared props and a synthesized
`Attrs` field. The final Go struct literal follows field layout rather than
authored attribute order, so values can move across one another. The current
ordered-value pass detects only expressions containing a Go `CallExpr`.
Effectful non-call expressions such as channel receives can therefore be
evaluated in the wrong order and feed different values to declared props and
fallthrough attributes.

For example, the first receive below must populate `data-x` and the second must
populate `label`, even though the generated `Attrs` field follows `Label`:

```gsx
<Field data-x=f`@{<-ch}` label={<-ch}/>
```

HTML attribute serialization order is not the concern. The required behavior
is Go expression evaluation in authored component-attribute order.

## Requirements

- Preserve authored evaluation order for potentially effectful component
  values across declared props, fallthrough values, ordered-attrs pairs,
  spreads, conditionals, composable class values, and attrs-only components.
- Cover non-call expression forms including receives, indexing, slicing,
  dereferences, type assertions, and other nontrivial typed expressions.
- Preserve contextual typing for untyped constants and `nil`; do not hoist them
  through `:=` and accidentally fix them to a default type.
- Keep component values that are provably inert inline when no ordered pass is
  otherwise required, preserving the existing fast generated shape.
- Leave leaf-tag direct writes, including numeric buffer appends, unchanged.
- Add no runtime dependency or exported API.

## Design

Introduce one component-value evaluation classifier shared by plan activation,
later-effect tracking, and source-position capture.

The classifier parses the lowered Go expression structurally. A value is safe
to leave unordered only when it is provably inert:

- its resolved type is untyped, which means a successfully type-checked value
  expression is a constant or `nil`; or
- after removing parentheses, it is a basic literal or identifier.

Every other typed expression requires ordered evaluation. Parse failure is
conservative and also requires ordered evaluation. This is an allow-list of
provably inert forms rather than a pattern heuristic over source text, so new or
unhandled Go expression forms cannot become false negatives.

`componentValuePlanNeeded` uses this classifier when comparing authored and
final positions. Once the plan is active, `componentValueHasEffect` uses the
same result to compute whether later work exists, and
`materializeComponentValuePlan` captures the expression at its authored
position. Existing tuple, renderer, embedded-literal, ordered-pair, and bag
rebuild handling remains authoritative after capture.

Post-renderer capture can retain its call-specific helper because renderer
dispatch either leaves the already-captured value unchanged or introduces a
renderer call.

## Alternatives

### Ordered props-builder closure

Build a typed props value through sequential field assignments in an
immediately invoked closure. This preserves contextual typing exactly, but it
substantially rewrites generated code, bag assembly, generic props handling,
and attrs-only lowering. The larger performance and compatibility surface is
not justified for this defect.

### Preserve call order only

Document the current limitation. This would leave observable Go evaluation
semantics dependent on field reconstruction and contradict the existing
authored-order contract.

## Validation

- Keep the real render-level failing test where an unmatched component
  `f`-literal receive precedes a matched prop receive.
- Add focused classifier coverage for inert untyped values and representative
  non-call effectful forms.
- Run the focused codegen tests first, then `make ci`, `make lint`, and
  `git diff --check`.
- Inspect any generated-shape or corpus changes rather than updating goldens
  blindly; unexpected churn indicates the classifier or capture boundary is
  too broad.
