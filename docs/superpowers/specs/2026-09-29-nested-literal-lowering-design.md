# Nested literals in every Go-expression position

Status: approved for implementation 2026-09-29 (user delegated design judgement:
"reasonable and least unexpected"). Stacks on PR #213 (`gate-nested-literals`).

## Problem

A gsx construct nested inside a Go expression — a prefixed literal
(`` f`…@{x}…` ``, `` js`…` ``, `` css`…` ``, either delimiter) or an element
literal (`<b>{x}</b>`) — is lowered only in body `{ }` interpolations
(`Interp.Embedded`), `{{ }}` blocks (`GoBlock.Embedded`, prefixed literals only)
and top-level Go (`GoWithElements`). PR #213 turned every other position's raw
go/parser failure into a positioned `nested-literal` diagnostic. This design
removes the diagnostic by lowering those positions the same way body
interpolations are lowered.

## Goal and non-goals

**Goal.** In every position below, a nested construct means exactly what it
means in a body `{ wrap(f`…`) }`: same static type, same escaping, same
error propagation, and the surrounding Go evaluates in its authored order —
except that an error-carrying hole (see below) is evaluated before the rest of
the expression containing it, because its hoisted statement runs first.

| Family | Fields |
|---|---|
| Attribute values | `ExprAttr.Expr` (native, component input, attrs-bag contributor, cond-attr branch value), `SpreadAttr.Expr`, `OrderedPair.Value`, marker/PI name |
| class/style | `ComposedPart.Expr`, `ComposedPart.Cond`, `ValueArm.Expr`, `ValueIf.Cond`, `ValueSwitch.Tag`, `ValueSwitchCase.List` |
| Conditional attributes | `CondAttr.Cond`, `SwitchAttr.Tag`, `AttrCaseClause.List` |
| Control flow | `IfMarkup.Cond`, `ForMarkup.Clause`, `SwitchMarkup.Tag`, `CaseClause.List` |
| Blocks | element literals in `{{ }}` (`GoBlock.Embedded` element parts) |

**Non-goals.** Formatting inside these fields (they keep round-tripping
verbatim, as in the 2026-07-13 spec). Literals in pipe-stage arguments and
whole-literal pipes at Go positions keep their existing diagnostics. Element
literals inside another literal's `@{ }` hole keep theirs.

## Semantics

### Lowering in place

Every nested construct lowers **as an expression at its own position**:

- a literal with pure holes → a Go string / `gsx.RawJS` / `gsx.RawCSS`
  expression (what `emitGoExprEmbeddedInterp` produces today);
- an element or fragment literal → a `gsx.Node` closure expression
  (`emitElementValue` / `emitFragmentValue`, as `genInterp` does).

Nothing is evaluated ahead of its node, so conditional attribute branches,
value-form arms, `else if` conditions, case lists and `for` conditions keep
Go's evaluation order and laziness. A nested literal in a false branch or an
unmatched case never evaluates its holes. The one exception to order is an
error-carrying hole: its hoisted statement runs before the other operands of
the field it sits in (full left-to-right pinning is a ROADMAP deferral).

### Error-carrying holes

A hole whose value is `(T, error)` (or a fallible pipeline) needs a statement
to hoist `v, err := …; if err != nil { return err }` into. The rule, the same
capability rule the 07-13 spec established (`canHoist`):

| Position | Error hole |
|---|---|
| attribute value, spread, ordered pair, class/style part and guard, marker name | hoists before the attribute's own write/input statement — inside the enclosing cond-attr branch or `AttrsCond` thunk when there is one, never ahead of it |
| value-form arm | hoists inside its own arm (where `hoistValueCF` already places arm hoists) |
| `if` / `else if` condition (markup, cond-attr, value-form), switch tag (all three forms) | hoists immediately before the `if`/`switch`; an `else if` is emitted as `else { <hoist>; if … }` so the hoist runs only when reached |
| case list, `for` clause | **rejected** with the existing positioned `goexpr-literal-error` diagnostic: a case list is evaluated lazily case by case, and a `for` condition/post runs every iteration, so no single hoist point preserves semantics |
| `{{ }}` block | unchanged: rejected (the block's reconstruction has no hoist slot) |

`ctx`-taking filters and renderers inside holes follow the same `hasCtx` flag
as today: available everywhere inside a component body.

### Element literals in `{{ }}`

`{{ n := wrapN(<b>{x}</b>) }}` assigns a `gsx.Node` value, exactly like an
element literal in top-level Go. The GoBlock splice lowers element parts with
the same element emitter; the "not supported yet" diagnostic is removed.

### Whole literals

A braced literal that is the entire attribute value (`title={f`…`}`) keeps its
existing lowering; it is not nested.

## Architecture

### Data

Each field above gains a codegen-only overlay `…Embedded []ast.GoPart` beside
its text, like `Interp.Embedded`: nil when the text has no nested construct,
filled only by the analysis split, never by the parser. `OrderedPair` gains
`ValuePos token.Pos` (the split needs a byte-exact base). `ast/clone.go` deep-
copies every new overlay (the parse cache relies on clones being independent).

### Split

`materializeEmbeddedMarkup` replaces PR #213's gate calls with a split of every
field (`SplitGoExprElements`, same `maySplit` pre-gate). Split errors stay
positioned parser diagnostics. The gate file is deleted; its corpus cases turn
into rendering cases.

### Probe (type-check skeleton)

One helper, extracted from the `Interp.Embedded` case of `emitProbes`, writes a
field's parts into the skeleton: `GoText` through the mapped authored writer,
each literal as `probeEmbeddedInterpIIFE`, each element as the tagged
`_gsxelem(N)` IIFE. Value fields reach it through `writeSkeletonProbeExpr`;
header fields through the liveness/control-flow writers. Because nested
constructs use the existing tagged IIFEs, `collectExprs`/`harvestBody`
ordering and `harvestEmbeddedElements` need no change. Invariant: emit ≡ probe
(same static type per construct).

### Emit

One helper generalizes `assembleHoleSeed`: given a field's parts and its
capability flags (`canHoist`, `hasCtx`, the position's error return), it
returns the lowered expression, writing hoists to the caller's statement
buffer. Element parts use `emitElementValue`/`emitFragmentValue`. Every emit
site that splices one of the fields switches from the raw text to the helper;
sites where hoisting is disallowed pass `canHoist=false`.

### Walkers

Every walker that descends `Interp.Embedded`/`GoBlock.Embedded` gains the new
overlays: clone, rebase, JS/CSS minifiers, JSX resolution, reserved-identifier
scan, qualifier-root harvest, component-candidate collection, call-site
registry, LSP `inspectWithEmbedded`, and the `goPartSwitchFunctions` registry in
`ast/walk_exhaustive_test.go`.

### LSP

Hover, go-to-definition and references inside a nested hole or element work by
descending the overlays. For the plain-Go text around a construct, cursor ↔
skeleton mapping uses the per-`GoText` mapped segments (`writeSkeletonAuthoredAt`)
instead of `len(text)` offset arithmetic, as body interpolations already do.

## Testing

- Corpus, one case per family, each position with f, js, css and element
  variants where the position's type allows: renders pinned.
- Laziness: a nested literal whose hole dereferences nil, placed in a false
  cond-attr branch, a value-form arm, an `else if` after a true branch, and an
  unmatched case, renders without panicking.
- Error holes: hoisted and propagated per position; rejected with a positioned
  diagnostic in case lists and `for` clauses.
- `{{ n := <b/> }}` renders.
- LSP: go-to-definition on an identifier inside a nested hole, and on a plain
  Go identifier after a nested construct, per family.
- Emit ≡ probe is exercised by the corpus generate+build; the walk-exhaustive
  test covers walker registration.
- Before merge: `make ci`, `make lint`, and an independent adversarial review
  that builds probe programs.

## Delivery

One branch (`nested-literal-lowering`) stacked on #213, in phases that each
keep `make ci` green: shared helpers + value fields → class/style + value-form
→ conditional attributes + control flow → `{{ }}` elements → LSP → docs
(interpolation.md caveat removed; ROADMAP deferral closed; the gsx skill needs
no change).
