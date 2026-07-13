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
is preserving the lexical ordering guarantees Go applies to operations inside
authored component-attribute expressions.

## Requirements

- Preserve the operations Go defines as lexically left-to-right—function and
  method calls, receive operations, and binary logical operations—when
  component values move between declared fields and synthesized bags.
- Apply the same operation detection across declared props, fallthrough values,
  ordered-attrs pairs, spreads, conditionals, composable class values, and
  attrs-only components.
- Do not impose ordering on operand evaluation Go explicitly leaves
  unspecified, including ordinary variable reads and indexing.
- Keep values with no Go-ordered operation inline, preserving contextual typing
  for untyped constants and the existing fast generated shape.
- Leave leaf-tag direct writes, including numeric buffer appends, unchanged.
- Add no runtime dependency or exported API.

## Design

Introduce `exprHasOrderedOperation`, a structural Go AST walk shared by plan
activation, later-effect tracking, and source-position capture. It reports the
operations named by Go's evaluation-order rule:

- `CallExpr` for function and method calls;
- unary `<-` for receive operations; and
- binary `&&` / `||` for logical operations.

Go's AST also represents conversions and some builtins as `CallExpr`, so call
detection remains the same conservative syntactic superset used by the existing
component-ordering implementation. That can retain an unnecessary temp but
does not change typing or evaluation semantics.

The walk does not descend into function-literal bodies because evaluating a
function literal does not execute its body. A call of the literal is still
detected at the enclosing `CallExpr`. Parse failure reports no ordered
operation; invalid Go is diagnosed by the existing analysis path rather than
being rewritten into a different invalid shape.

`componentValuePlanNeeded` uses this predicate when comparing authored and
final positions. Once the plan is active, `componentValueHasOrderedWork` uses
the same predicate to compute whether later work exists, and
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

### Broad effect classifier

Treat indexing, selectors, dereferences, assertions, composite literals, and
ordinary reads as ordered effects. This creates extra temps and generated
shape churn while promising semantics Go itself explicitly leaves unspecified.
It also reintroduces contextual-typing hazards if untyped expressions are
captured. The broader model is unnecessary for the receive gap.

## Validation

- Keep the real render-level failing test where an unmatched component
  `f`-literal receive precedes a matched prop receive.
- Add focused AST coverage for calls, receives, logical operations, nested
  composite values, skipped function-literal bodies, and parse failure.
- Run the focused codegen tests first, then `make ci`, `make lint`, and
  `git diff --check`.
- Inspect any generated-shape or corpus changes rather than updating goldens
  blindly; unexpected churn indicates the classifier or capture boundary is
  too broad.
