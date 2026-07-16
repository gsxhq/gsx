# Verbatim component signatures — the parameter list *is* the contract

**Status:** design settled — implementation in progress.
Early syntax phase; **atomic throwaway rewrite** accepted (no dual-mode layer).
**Date:** 2026-07-14

## Summary

A gsx component today compiles to a Go func/method whose real parameter is a
gsx-**synthesized** `<Recv><Name>Props` struct (except in the narrow BYO case).
The author writes `Page(h db.History)`; gsx emits `Page(_gsxp HistoryDiffPartialPageProps)`.

This design removes the synthesized struct entirely. **A component compiles to a
Go func/method whose parameter list is exactly what the author wrote — no public
wrapper type, no ABI-affecting parameter.** Markup invocation
(`<Comp attr={} …>children</Comp>`) is adapted to that signature at the call site
by the generator, which already owns every **markup** call site (using only inline
expressions and local temporaries — no generated type or helper; see §Zero-fill
lowering, §Evaluation order).

`children` and `attrs` become **reserved parameter names** the author declares,
not magic identifiers gsx injects.

## Motivation

The synthesized props struct breaks the Go ABI for every reflection-based or
manual caller, because it *replaces* the declared signature:

- **structpages regression.** structpages wires a page by type match (exact
  match first, then assignable): `Props() T` feeds `Page(x T)`
  (`parse.go:fillMethodArgs`).
  templ preserved the signature, so `Props() db.History → Page(h db.History)`
  matched. gsx wraps `Page` into `Page(_gsxp HistoryDiffPartialPageProps)`, so
  the match fails at runtime: *"method Page requires argument of type
  HistoryDiffPartialPageProps, but not found."* An audit of one-learning found
  **8** such latent pages (`ListAttachmentsPartial`, `HistoryDiffPartial`,
  `GroupNewModal`, `QCEditChecklistPreview`, `RulesList/Show/New/Edit`,
  `FeedbackNewPage`) — every one a single-param method whose `Props()` returns a
  type the wrapper hid.
- **Generic inference.** `Box[T](item T)` emits `BoxProps[T]{Item: T}` and forces
  inference through the wrapper (named-type holes have bitten generic dispatch
  twice). Verbatim `func Box[T](item T)` called `Box(x)` lets Go infer `T`
  directly.
- **Calling convention doesn't map to the signature.** Manual Go callers must
  construct a generated type name (`X{}.Page(XPageProps{…})`,
  `RenderComponent(X.Page, XProps{…})`), coupling hand-written code to generated
  names.
- **Freedom.** With no ceremony, any `func(…) gsx.Node` is already a component.

The BYO escape hatch (verbatim signature for a "bring your own Props" struct)
proves the two ABIs *can* coincide — but it triggers on an incidental property
(is the sole param a same-package named struct?), not on intent. `Page(h db.History)`
and `Page(h LocalHistory)` have identical intent and get opposite ABIs. This
design removes the heuristic by making verbatim the universal rule.

## The load-bearing principle

> **The public component signature is exactly, and only, the parameters the
> author wrote. gsx synthesizes no type and no function. Call-site lowering uses
> only inline expressions and local temporaries.**

The one narrowing (vs. "gsx does *nothing* at the call site") is that lowering may
introduce **local temporaries** — needed to preserve evaluation order and to spell
zero values inline (see §Zero-fill lowering, §Evaluation order). It never emits a
synthesized *type* or *helper function*; no additional declaration is exposed to
Go callers, structpages, or Go API tools. Local temporaries remain ordinary
generated implementation details. (A private generic call adapter for zero-fill
was considered and rejected — see §Zero-fill lowering.)

This prohibition applies to declarations in emitted `.x.go`. Transient,
in-memory type-check probes may synthesize private declarations as analysis
scaffolding, but they never appear in generated source and never surface as user
API through LSP or Go tooling.

`children` and `attrs` — the two inputs that come from the markup parent, not a
Go caller — must therefore be **declared by the author** if used; a synthesized
param would re-diverge the contract. This is consistent with the 2026-06-30
explicit-forwarding decision.

## Public contract vs Go ABI

"ABI" is imprecise: parameter *names* are not part of Go's function-type identity,
but they **are** the gsx markup API. This creates two overlapping public
contracts with different compatibility rules — the spec calls the combined thing
the **component contract**:

| Author change | Go callers | gsx markup callers |
|---|---|---|
| Rename a parameter | no Go type change | **breaking** (the attribute name changed) |
| Reorder same-typed params | may **silently swap** arguments | no effect (fill is by name) |
| Add a parameter | arity break | zero-filled when an inline zero is lowerable; otherwise the call gains a required-attribute diagnostic |
| Remove a parameter | arity break | unmatched-attribute error, or falls through |
| Change a parameter type | type error | type error |

**Consequence / guidance.** Generated Props gave exactly one real advantage:
keyed literals tolerated adding and reordering *fields*. Verbatim params trade
that for an honest Go ABI. So the documented rule is:

- **Loose parameters** for small and structpages-facing components (the common
  case) — the honest ABI and direct generic inference matter most there.
- **One author-owned options struct** for high-arity or long-lived Go APIs where
  add/reorder tolerance is worth it — this is now an ordinary single-struct-param
  component (`Foo(opts FooOptions)`), an explicit author choice, not a gsx-forced
  wrapper.

## The model

A component compiles to:

```
component (p T) Name(<params>) { … }   →   func (p T) Name(<params>) gsx.Node
component Name(<params>) { … }          →   func Name(<params>) gsx.Node
```

`<params>` is emitted verbatim. Role of each parameter:

| Parameter | Role | Fill source |
|---|---|---|
| `children` (reserved name) | body slot | the markup body |
| `attrs` (reserved name, attrs-bag signature family) | fallthrough bag | attributes not matching any param, spreads, and explicit attrs-bag contributors |
| any other param | ordinary named prop | `name={value}` matching the param name; else zero |

Only `children` and `attrs` are special. Other attrs-bag-shaped params
(`containerAttr`, `extraAttr`) and other `gsx.Node` params (`icon`, `header`) are
**ordinary named props** — this is what composite components need.

### Markup fill semantics

1. An attribute whose name **exactly equals** an ordinary parameter identifier
   fills that parameter. `children` is populated only by the body. The reserved
   name `attrs` is not an ordinary parameter fill: `attrs={expr}` and
   `attrs={{...}}` contribute bags to the fallthrough stream as defined below.
   An ordinary parameter may be explicitly filled at most once; a second
   exact-name attribute is a positioned `duplicate-prop` error. Repetition is
   allowed only for the reserved `attrs` contributor stream.
2. Only identifier-shaped attribute names can bind a param. `class`-style names
   that are not Go identifiers — `data-*`, `aria-*`, `hx-*`, any kebab name —
   **can never match a param** and always fall through.
3. Every unmatched attribute, `{bag...}` spread, conditional-attribute bag, and
   explicit `attrs` bag contributes to the `attrs` parameter in authored order.
   A conditional-attribute group is bag syntax: names inside its branches never
   conditionally fill ordinary parameters; they contribute to `attrs` or get the
   strict missing-attrs diagnostic.
4. **Strict:** an unmatched attribute with **no `attrs` param declared** is a
   **compile error** (`Card has no attrs param; unexpected attribute "class"`).
   This diagnostic is call-site-driven: signature validation never emits it from
   the type or name of some other parameter.
5. An **omitted** known prop **zero-fills** when its zero is lowerable (see
   §Zero-fill lowering). There is no author-declared `required` modifier; only an
   unlowerable semantic zero makes a prop required at a particular call site.
   Presence checks are ordinary Go in the body.
6. **Parameter order is irrelevant to markup** (fill is by name/role). Order only
   matters to Go positional callers, who see the signature.

`class` / `style` are the author's choice: declare an exact ordinary parameter to
take them as props, or omit it and let them land in `attrs`, where a
`{attrs...}` spread on the root performs the existing class/style merge. Target
classification happens **before** the parser-specific attribute kind is lowered:
when the exact parameter exists, static/braced/embedded/ordered/markup-valued
forms bind it using their ordinary value type, while composed `class={…}` lowers
to `gsx.ClassJoin(...)` and composed `style={…}` lowers to
`gsx.StyleString(...)`. The resulting value is checked against the declared
parameter type. Without the exact parameter, those forms retain their existing
fallthrough-bag behavior. Unsupported target/form combinations get a positioned
diagnostic; they are never silently re-routed.

### Zero-fill lowering — inline only, no generated helpers

An omitted prop needs a zero value at the call site, spelled **inline** — no
emitted adapter or helper function. Lowering chooses the first semantically valid
shape:

1. the type-independent literal that is the **actual semantic zero** of the
   instantiated parameter type (`""`, `0`, `false`, or `nil`), when one exists;
2. `*new(T)` when the exact type `T` is nameable in the caller;
3. `*new(U)` when an accessible unnamed type expression `U` is assignable to the
   parameter (for example, the spellable underlying struct/array shape of an
   otherwise unexported named type).

The analyzer does not try arbitrary literals and stop at the first assignable
one: assignability alone is insufficient (`""` is assignable to `any`, but the
zero of `any` is `nil`). It computes the real zero-literal category from the
semantic type and, for a remaining type parameter, its complete type set; it
then validates that single literal with `go/types`. `any` and non-empty
interfaces therefore lower to `nil`. If a type set has no one literal that is
the zero of every member, literal lowering is unavailable and analysis proceeds
to `*new(T)`. The analyzer does not infer lowerability from exported spelling or
an underlying-kind heuristic.
Omitted trailing variadics pass nothing (`f(a)` for `f(a, xs...)`).

**No lowerable zero → required attribute (fail closed).** A type is nameable when
the generator can emit a caller-package type expression which type-checks as
identical to the instantiated parameter type. Alias-preserving source expressions
count. Instantiated types with opaque arguments and anonymous structs/interfaces
containing foreign unexported members may remain unnameable. If neither a
type-independent zero nor an assignable nameable type expression exists, omission
gets a **positioned diagnostic** ("attribute `x` is required here: its zero value
cannot be expressed in this package"), not a synthesized work-around. The caller
may still provide an expression of that opaque type.

A private generic call adapter (`_gsxcall…[A,B any](f, a)`) was considered and
**rejected**: replacing generated structs with generated call helpers undercuts
the motivation (honest ABI, no emitted machinery, simple output) and grows
combinatorially with arity/omission shape.

**Inference-failure case.** Before synthesizing zeros, perform Go type inference
using only explicit type arguments and authored operands that survive into the
actual call lowering. Omitted zeros are never inference evidence. Discovery may
recover the origin generic object/signature even though using an uninstantiated
generic function as a value produces an expected probe diagnostic. Once authored
type arguments may be a prefix (`<Pair[int] ...>` for `Pair[A, B]`); discovery
retains that prefix even when the target alone cannot produce a complete
`types.Instance`. Once authored bindings are known, analysis installs a transient
semantic `types.Func` in the checker scope and calls it with that exact prefix plus
the authored operands. Its `types.Signature` reuses the target's semantic type
parameters, constraints, and supplied parameter types, but omits every unsupplied
value parameter.
Because this carrier is assembled from `go/types` objects rather than copied Go
source, imported unexported constraints remain usable without being named. Its
instance supplies the inferred type arguments for the original signature. Only
then does lowering instantiate that signature, synthesize zeros, and validate the
complete call. The carrier is analysis scaffolding, never an emitted declaration.

Inference diagnostics follow Go's failure class, in this order: authored operand
parse/type errors retain their native positioned diagnostics (target-only
type-argument/constraint errors are retained until this precedence can be
applied); an incomplete inference alone gets the explicit-type-argument hint
(`<Box[string]/>`); an inferred or explicit argument that violates a constraint
gets the native constraint diagnostic without claiming explicit arguments will
fix it. Do not decide inference by syntactic type-parameter occurrence:
`Infer[T](*T)` cannot infer `T` from `nil`, while constraints may infer a
parameter not textually present in a supplied parameter type.

A legitimate but uncommon non-nameable case is an **opaque package value**:

```go
// package ui
type theme struct{ id uint64 } // foreign unexported field makes the type opaque
func DefaultTheme() theme { return theme{id: 1} }
func Widget(title string, theme theme) gsx.Node { return nil }
```

Another package can call `ui.Widget("title", ui.DefaultTheme())` — it can obtain
and pass `ui.theme` without naming the type — but cannot spell the omitted zero.
Markup therefore accepts `<ui.Widget title="title" theme={ui.DefaultTheme()}/>`
and rejects `<ui.Widget title="title"/>` with the required-attribute diagnostic.
This is the concrete cross-package use case to retain in the implementation
probes. (An opaque defined numeric or nilable type may still accept untyped `0`
or `nil`; opacity alone is not rejection.) If implementation finds another
signature that cannot obey the inline-only rule, it must stop with a minimal
corpus reproducer and bring the case back to the design; no helper, reflection,
or guessed zero-value fallback.

### Evaluation order

Rule 6 ("param order is irrelevant to markup") is about *which slot* a value
fills — **not** about when its expression runs. `F(argA, argB)` evaluates in
signature order, so naively rearranging authored expressions into signature order
would reorder calls, receives, short-circuits, spreads, and fallthrough
evaluation. Invariant:

> **Each authored component expression is evaluated exactly once. Operations for
> which Go defines lexical ordering retain that authored order; only their
> already-evaluated values are rearranged into signature order for the call.**

This reuses the existing value-plan machinery of the component-value effect-order
design (`docs/superpowers/specs/2026-07-13-component-value-effect-order-design.md`):
build one source-ordered plan, but materialize **only** values whose movement
crosses Go-defined ordered work or which require statement-producing lowering.
Classify each expression with `go/types` before ordering analysis. A constant-
valued or nil expression must not be materialized with `:=`; it stays inline so
the final parameter supplies its context, or uses a target-typed local when that
type is nameable. Syntactic `CallExpr` classification never overrides contextual
typing (`min(1, 2)` may still be an untyped constant). The plan must also preserve
**`(T, error)` auto-unwrapping** — a naïve direct call could otherwise bind a
returned `error` into an adjacent component parameter. Auto-unwrapping applies
independently to ordinary props, `attrs={expr}`, bag spreads, and each ordered-
literal value; a tuple is consumed before positional assembly and can never
expand into adjacent component parameters.

**Implementation checkpoint.** The first positional-call corpus slice must prove
authored-order inversion, untyped constants bound to non-default types, and tuple
unwrapping together. If the implementation cannot preserve all three with the
semantic value plan, stop and surface the generated shape plus a minimal failing
case; do not replace the semantic Go-operation classifier with a broader guess.

### Reserved and edge-case parameter names

`checkReservedParams` today forbids `ctx`, `children`, `attrs`, and the `_gsx`
prefix. Under this design:

- **`ctx`** — stays reserved (ambient context).
- **`children` / `attrs`** — flip from *forbidden* to *reserved-with-role*, and
  are type-validated (`children`: `gsx.Node` or `...gsx.Node`; `attrs`: the
  attrs-bag signature family defined below).
- **`_gsx…`** — stays reserved (gsx's generated namespace) → error.

Leading underscore needs no new rule — it follows from name-matching:

- **`_foo`** (leading underscore, real name) — an ordinary named prop. Identifier-
  shaped, so markup fills it by name (`<Foo _foo={x}/>`) and the body references
  it. No special meaning (matching Go).
- **Every parameter must have a usable static name**, including variadics.
  `func(_ string) gsx.Node`, `func(string) gsx.Node`, and
  `func(...gsx.Attr) gsx.Node` cannot be used as components. A use as a tag gets
  `function parameters must be named to be used as a component; parameter N is
  unnamed` (or `parameter N is blank` for `_`). The functions remain valid for
  direct Go and structpages calls (`fillMethodArgs` can fill `_ *Store` by type).
  Names come only from the callee's static signature; gsx never tries to recover
  a name from a returned closure or another value once the static func type has
  erased it. This rule is independent of parameter type: it does not guess that
  an unnamed `gsx.Attrs`, `[]gsx.Attr`, or `...gsx.Attr` parameter was intended
  to be the component attrs bag.

**Deliberately not adopted:** a "private/markup-hidden param" meaning for named
leading-underscore params (`_foo`). No strong use case in gsx (structpages injects
by type; `ctx` is ambient), so it would be an invented convention with no customer
(YAGNI). The blank-`_` tag-ineligibility already covers type injection. Reconsider
only if a real injection case appears.

### children

The `children` parameter's type is author-chosen; gsx fills the same markup body
either way:

- **`children gsx.Node`** — the whole body as one node (a fragment when there are
  several top-level children; `nil` when empty, nil-safe render). The default for
  ~90% of components.
- **`children ...gsx.Node`** — each top-level body node as a list element; inside,
  `children` is `[]gsx.Node` (`len`/`range`/index). Go callers write
  `Tabs(a, b, c)`. Use when the component iterates its children.

Exactly these **two** forms — `gsx.Node` and `...gsx.Node`. `[]gsx.Node` is **not**
accepted (the variadic already gives `[]gsx.Node` inside, with better Go-call
ergonomics — the non-variadic slice form buys nothing). `children int` is an
error. A component with no `children` param rejects a body: `<C>…</C>` is a
compile error. Source comments are not body values: comment nodes are removed
from the static top-level child list, a comment-only body is empty, and comments
interspersed with real children do not consume variadic child positions.

### Reserved-input population and collisions

`children` remains body-only. `attrs` is different: it names the component's
ordered fallthrough **input role**, not a single ordinary prop slot. All syntax
that contributes an attribute bag therefore enters one source-ordered stream.
`attrs={expr}` accepts the full non-variadic attrs-bag slice family and normalizes
it to canonical `gsx.Attrs` at this authored position; `attrs={{ "k": value }}`
constructs and contributes a `gsx.Attrs` literal. Separately,
`name={{ "k": value }}` remains general attribute-value
syntax which produces a `gsx.Attrs` value for an **ordinary** named prop, so a
composite component can route that bag to one or several chosen descendants.

| Form | Meaning |
|---|---|
| `<C>body</C>` | fills `children` from the body |
| `<C children={n}/>` | **error** — `children` is body-populated; write `<C>{n}</C>` |
| `<C class="x"/>` (unmatched) + `attrs` param | `class` flows into `attrs` |
| `<C {bag...}/>` | contributes `bag` to `attrs` (fallthrough forwarding) |
| `<C attrs={a}/>` | contributes `a` to `attrs` at this authored position; `a` may be `gsx.Attrs`, `[]gsx.Attr`, or a defined/aliased slice with exact underlying `[]gsx.Attr` |
| `<C attrs={{ "role": "x" }}/>` | contributes the ordered literal to `attrs` at this authored position |
| `<C inputAttrs={{ "aria-describedby": id }}/>` | fills the ordinary `inputAttrs` prop with a `gsx.Attrs` value; the component may spread it on one or more chosen inner elements |
| `<C inputAttrs={computedBag}/>` | fills that same ordinary prop from a reusable/computed `gsx.Attrs` expression |
| `<C {a...} class="x"/>` | `a` then `class` merge into `attrs`, source order |
| `<C {b...}{c...}/>` | multiple spreads merge in source order (existing multi-spread merge) |
| `<C attrs={a} id="x" attrs={{ "id": "y" }}/>` | contributes `a`, then `id="x"`, then the literal; duplicates are retained and leaf semantics decide last-wins/aggregation |
| `<C attrs="x"/>` / `<C attrs/>` / `<C attrs={<i/>}/>` | **error** — the reserved attrs role accepts only bag expression/literal syntax |
| `<C Attrs={{…}}/>` / `<C Children={…}/>` | `Attrs` and `Children` have no reserved meaning: they fill exact ordinary capitalized params when declared; otherwise they follow the same unmatched/fallthrough and missing-`attrs` rules as any other ordinary name |

**Settled: accept `attrs={expr}` and `attrs={{...}}`; reject only explicit
`children={...}`.** Rejecting the two attrs forms would add work and a surprising
exception even though `attrs` is an input role rather than an ordinary parameter.
Their capability overlaps `{expr...}` and individual attributes, but uniform
syntax is the stronger rule here.

A conditional attribute group remains one top-level attrs-stream contributor at
its authored position, but its branches are not opaque. The planner retains a
recursive ordered tree of pair, spread, explicit-`attrs` contributor, and nested
conditional records. Each branch leaf is validated under the same reserved-role
rules: lowercase `children` is always rejected, valid `attrs={expr}` and
`attrs={{...}}` leaves contribute bags, invalid explicit `attrs` forms get
`reserved-input-form`, and every other branch name is a fallthrough pair rather
than a conditional ordinary-prop fill. Alternatives are never flattened into a
fictional global evaluation order. When no `attrs` role exists, each authored
leaf gets its own positioned `component-missing-attrs` diagnostic; diagnostics
are not coalesced at the outer conditional. A genuinely empty/comment-only
conditional is itself the diagnostic leaf because its condition is still an
authored operation; nested parents do not duplicate that error. A conditional
whose only leaves are malformed reserved forms keeps their precise
`reserved-input-form` diagnostics without a cascading missing-attrs error.

There is no legacy-capitalization branch. Only lowercase `attrs` and `children`
are reserved roles; exact ordinary parameters named `Attrs` or `Children` bind
normally, and an undeclared capitalized spelling receives only the ordinary
unmatched-input behavior.

The one-learning audit found five real `containerAttrs={{...}}` call sites and
computed bag spreads, but no reserved `attrs={{...}}` or `attrs={expr}` call site.
That evidence proves the value of separately named destination bags; it does not
justify rejecting intuitive syntax for the reserved input bag.

There is **no forced-last exception**. Ordinary unmatched attributes, conditional
bags, `{bag...}`, `attrs={expr}`, and `attrs={{...}}` concatenate at their authored
positions. More than one explicit `attrs` contributor is allowed. Bag construction
retains duplicates: scalar keys remain last-wins and `class`/`style` remain
aggregate-at-leaf under the existing `gsx.Attrs`/`Spread` rules. Each contributor
expression and each ordered-literal value participates in the same once-only
evaluation plan as every other component-call expression.

An explicit attrs contributor requires a declared `attrs` role; otherwise it gets
the same positioned `component has no attrs param` diagnostic as any other
fallthrough input. General rule: `children` is filled only by the body; `attrs`
collects the ordered fallthrough stream; every other param is filled only by its
exact name, never by body/fallthrough spread.

### attrs

The reserved `attrs` role accepts one **attrs-bag signature family**:

- `attrs gsx.Attrs`;
- `attrs []gsx.Attr`;
- `attrs MyAttrs`, where `MyAttrs` is a defined non-variadic slice whose
  underlying type is exactly `[]gsx.Attr`;
- `attrs ...gsx.Attr`.

Aliases of these forms are accepted. The element type remains exact after alias
resolution: `[]MyAttr` where `type MyAttr gsx.Attr` is not a bag shape. This is
one type-driven rule, not four calling-convention branches. Call-site lowering
builds the canonical `gsx.Attrs` bag, passes it directly where assignable,
passes `[]gsx.Attr(canonicalBag)` for a defined-slice target (Go assignability
then covers exported, unexported, aliased, and instantiated named slices without
naming the target), or expands it for the variadic form. An `attrs={expr}`
contributor from any non-variadic member of the same family normalizes as
`gsx.Attrs([]gsx.Attr(expr))`. The component body sees the exact type the author
declared.

Because Go requires a variadic parameter to be final, `attrs ...gsx.Attr` cannot
be followed by other parameters or coexist with variadic `children`; use the
non-variadic `attrs gsx.Attrs` form when the signature needs both roles.

The bag contains unmatched attributes, `{bag...}` spreads, conditional bags, and
explicit `attrs={expr}` / `attrs={{...}}` contributors, per the table. It feeds
`{attrs...}` spreads in the body, which route through the existing
sanitizing `Spread` (URL sinks, class/style merge, renderers). The bag re-homes
from a synthesized struct field onto a declared parameter; the security-sensitive
spread machinery is unchanged.

### Node-valued props

A non-`children` node-typed prop (`icon gsx.Node`, `header gsx.Node`) is an
ordinary named prop. It accepts a **markup-valued attribute** —
`header={<h1/>}` — and preserves the existing GSX component-boundary promotion
for renderable values:

| Authored operand for an exact `gsx.Node` parameter | Positional argument |
|---|---|
| markup or a value already assignable to `gsx.Node` | unchanged |
| static string attribute or `f`-literal | `gsx.Text(value)` |
| other supported scalar / string / `fmt.Stringer` expression | `gsx.Val(value)` |

This is the existing attribute-value grammar, *not* a distinct body-child slot
grammar; "named slots" are just node-typed props filled this way. Leaf HTML
emission keeps its direct-write fast path: this promotion exists only at a
component parameter boundary.

The destination test is semantic identity with the canonical runtime
`gsx.Node` type (including aliases through `types.Identical`), not “implements
Node.” A concrete type that implements `gsx.Node` remains an ordinary exact-type
parameter because neither `gsx.Text` nor `gsx.Val` produces that concrete type.

Planning records one adapter per supplied operand: identity, NodeText, or
NodeVal. Tuple/error unwrapping happens before adaptation. The adapted semantic
fact participates in generic inference, assignment validation, and final-call
validation; emission consumes the recorded decision after the operand's
once-only evaluation/materialization and never reclassifies it. This prevents an
emitter-only wrapper from disagreeing with inference or diagnostics and requires
no additional package load for imported components.

Known-scalar specialized Node wrappers may avoid `gsx.Val`'s `any` box in the
future, but are deliberately outside this recovery. They require a separately
benchmarked runtime/codegen design. This cutover restores the existing contract:
Text for static/`f` strings, Val for other promoted renderable values.

### Forwarding and spreads

There is **no component struct-splat** (`<C {structVal...}/>`). It was cut after
an audit: every real splat in one-learning is whole-value forwarding into a
single struct param of the same type (`<AddWellFormFields {props...}/>` where
`AddWellFormFields(props AddWellModalProps)`; `<AnnouncementTitleLink {item...}/>`
where `AnnouncementTitleLink(item Announcement)`). Under this model that is
simply an ordinary named-prop fill — the target's single param has a name:

```
<AddWellFormFields props={props}/>      // → AddWellFormFields(props)
<AnnouncementTitleLink item={item}/>    // → AnnouncementTitleLink(item)
```

The only thing struct-splat *uniquely* offered — field-destructure, spreading one
struct across several individual field-params (`<Card {ann...}/>` →
`Card(ann.Title, ann.Date)`) — has **zero customers** in the realistic codebase
(the style is "bundle into a struct, pass it whole"), and it was the sole reason
the call site needed the source struct's field set resolved cross-package.
Cutting it removes that resolution requirement and a whole codegen category
(field↔param name matching, case rules, strict-mismatch diagnostics). YAGNI;
reconsider only if a real field-destructure case appears.

Two `gsx.Attrs`-bag spreads remain (both bags, not structs — no field
resolution):

- **element attr-spread** `<div {bag...}>` — the heavily-used, security-sensitive
  one. Unchanged (sanitizing `Spread`).
- **attrs forwarding into a component** `<Comp {attrs...}/>` — the bag flows into
  `Comp`'s declared `attrs` param (fallthrough forwarding). Unchanged.

### Variadic

First-class. `...gsx.Node` named `children` is the list form above, filled from
the **static** top-level body nodes (`<Tabs><Tab/><Tab/></Tabs>` → two children).
A *dynamic* body (a `{ for … }` loop) is one node, so dynamic children use the
single-node `children gsx.Node` form (a fragment) or the component iterates
internally.

A **non-`children`, non-`attrs` `...T` variadic is not markup-bindable** —
Go-callable only; `<C xs={…}/>` for a variadic `xs` is a **compile error**. The
reserved `attrs ...gsx.Attr` form is the single exception: fallthrough builds one
bag and expands it at the call. There is no general slice-into-variadic markup
splat — YAGNI, no customer; pass the slice via a Go call if needed.

### The callable universe

Positional lowering with zero-fill and evaluation-order needs the callee's exact
static signature (ordered names, types, variadic position, reserved roles). So
tag eligibility is defined by **resolvability to a concrete `go/types.Signature`**:

- **Eligible:** declared gsx components (funcs, plus methods through a bound
  receiver), same-package and imported package-level Go funcs / func vars
  with exactly one result assignable to `gsx.Node`, bound method values (`x.Page`
  where `x` is a value), and named func types / aliases. Result assignability,
  not exact named-type identity, admits concrete node implementations while
  still rejecting zero-result and multi-result callables.
- **Ineligible (fail closed):** any callee whose signature does not statically
  resolve — including several field/local/interface-method/param shapes the
  current generator already rejects. **An unresolved signature is an error, not a
  guess.** The existing imported-props "warn-and-guess" fallback is unsound for
  positional calls (a guessed field set cannot produce a correct positional call)
  and must be replaced by fail-closed resolution here. A resolved signature must
  also give every parameter a non-empty, non-blank name; otherwise using the
  callable as a tag gets the general named-parameters diagnostic above.

`ast.Element.IsComponent` records the final semantic answer, not syntactic
component intent. Capital-first and dotted spellings nominate a target; they do
not make an invalid target a component. The package preprocessor sets the field
true only after proving allowed provenance and the one-result-assignable-to-
`gsx.Node` contract. A failed capital-first/dotted claim is a positioned error,
not an HTML fallback. A lowercase same-package name that definitively lacks the
result contract remains an ordinary leaf. Parameter-role validation happens
after this identity decision, so a callable with a valid result but malformed
`attrs` or `children` stays a component and receives the signature diagnostic.

A signature containing `types.Invalid` anywhere in a parameter or result type
graph, or an incomplete alias that unaliases to no type, is unresolved for this
purpose and therefore tag-ineligible. Validation walks semantic types with cycle
detection; it does not infer validity from printed type text or only inspect the
outermost type.

A signature alone is not enough; the discovery pass records provenance. Allowed
origins are a package-scope `types.Func`, a package-scope function-valued
`types.Var` (bare or through a package selector), or a concrete
`types.MethodVal`. Reject `types.MethodExpr`, struct fields, locals/parameters,
interface dispatch, and any other dynamic origin even when its current type is a
concrete signature. Named func types and aliases inherit the eligibility of the
object that supplies the value. Parameter names come only from that static
signature. Any parameter whose `types.Var.Name()` is empty or `_` triggers the
named-parameters rule above, including a variadic parameter; missing export-data
names are never guessed.

Factory-produced function values follow the same static rule. For
`func factory() func(name, label string) gsx.Node`, the signature of
`var X = factory()` retains `name` and `label`, so `<X name="foo" label="bar"/>`
is valid. Named func types and aliases retain the names written on their type
declaration. By contrast, `func factory() func(string, string) gsx.Node` is not
usable as a component even if the returned closure happens to name its parameters:
those closure-local names are not part of `X`'s static type.

Definition/hover locations use the parameter objects from that same static
signature. Source-loaded same-package and imported module packages therefore
point at the return func type, named func type, or alias declaration that owns the
name. Export data may preserve a name without a usable source position; that is
still sufficient for correct binding, while navigation is best-effort and may
have no target. The resolver does not inspect factory bodies or initializers to
manufacture either names or locations. Plain-Go signature parameters participate
in definition and hover, but remain outside GSX semantic rename because their
declarations and Go references are owned by Go tooling.

A true method expression (`T.Page`) is also tag-ineligible: its function value has
an explicit receiver argument, and markup has no receiver-fill mechanism. The
structpages/direct-Go shape `x.Page` is a **bound method value**, not a method
expression; the receiver is already captured.

### attrs-only ABI is merged into the signature model

There is no separate attrs-only calling convention. The same signature analyzer
used for every component recognizes the named `attrs` role and its bag family,
whether the callee is a `.gsx` declaration, Go func, func variable, named func
type, or alias. The current `attrsonly.go:attrsOnlySig` shapes therefore become
ordinary instances of the universal model rather than a parallel emitter path.

The parameter name remains part of the markup contract. A plain-Go factory should
publish it in the static function type:

```go
func named(name string) func(attrs ...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node { return nil }
}
```

Arbitrary-name structural classification is retired without a legacy diagnostic
branch. An unnamed `func(...gsx.Attr) gsx.Node` gets only the general
named-parameters diagnostic when used as a tag; its type does not trigger an
attrs-specific message. `func(extra ...gsx.Attr) gsx.Node` has a named parameter
and is therefore a valid component signature, but `extra` does not acquire the
reserved role: it is an ordinary non-reserved variadic, so markup cannot bind it
and may only omit it under the universal ordinary-variadic rule. Likewise,
`func(a myAttrs) gsx.Node` is valid and `a` is an ordinary exact-name prop, so
`<Badge a={{ "role": "status" }}/>` binds it normally. `someAttrs={{...}}` has
the same ordinary-prop meaning and can be forwarded to any chosen descendants.

The attrs-specific diagnostic is emitted only while binding an authored
fallthrough input: an unmatched attribute, spread, conditional bag, or explicit
`attrs` contributor found on a component with no parameter literally named
`attrs`. It is never emitted merely because a callable has an attrs-bag-shaped
parameter with another name. The migration ledger may identify unnamed factory
signatures or factories that intend fallthrough for source rewrites, but
production classification and diagnostics do not know that they were once
attrs-only.

## What is removed / subsumed

- **The synthesized `<Name>Props` struct** — gone for every component.
- **The BYO path** (`byo.go`, `soleParamTypeName`, `compStruct`, the
  emit/skeleton BYO branches) — subsumed. A single struct param is now just one
  named prop:
  - **Whole-value** use (structpages page, `<C p={val}/>`) — the natural page
    shape; `Props() T → Page(x T)` matches.
  - **Field-level** markup addressing (`<Card title={x} body={y}/>`) — the author
    declares individual params instead of a struct. (Previously BYO let
    `<Card title={x}/>` address struct fields; that now requires individual
    params. Whole-struct forwarding is a named-prop fill, `<Card props={p}/>`.)
- **Magic `{children}` / `attrs` identifiers** — replaced by declared reserved
  params. `ctx` remains ambient.
- **The separate attrs-only classifier/emitter path** — merged into ordinary
  signature role analysis; its sound bag shapes remain, but not its
  arbitrary-name structural convention.
- **Implicit-role free-use scanning + shadow handling** (`freeuse.go`,
  `usesChildren`/`usesAttrs`, and the `children`/`attrs` arms of
  `reserved_bindings.go`) — gone. Whether a component uses children/attrs is now
  read off the *signature* (it declared the param or it didn't), and a local named
  `attrs`/`children` is ordinary Go scoping, not a shadow of a magic identifier.
  Keep `reserved_scan.go`: its real Go-lexer pass enforces the still-required
  `_gsx...` namespace. Keep the `ctx` body-binding check because `ctx` remains the
  ambient render-closure parameter. Before deleting `freeuse.go`, move the small
  Go-AST statement-binding parser it shares with `reserved_bindings.go` into that
  file (or a dedicated `ctx` binding file), reduce it to `ctx`, and retain its
  focused scope tests; only the implicit-role branch/environment machinery is
  obsolete.

  Foundation commits before the atomic cutover do not widen these legacy
  syntax-only scanners over newly materialized expression parts. That cannot be
  made exact without semantic identity: `S{attrs: 1}` names a field while
  `map[any]int{attrs: 1}` evaluates the identifier, and lexical bindings live at
  the exact materialized marker position. A transitional typed body analyzer
  would duplicate the new analysis pipeline only to be deleted here. Keep the
  legacy behavior frozen (apart from excluding a preprocessor-annotated
  unsupported Go block), then prove materialized values, lexical shadowing, and
  map-key uses against real authored parameters in the cutover corpus.
- **`WithFieldMatcher`** (`gen/options.go:355`) and fuzzy attr→field matching —
  obsolete under **exact-name** matching. Remove it outright across config,
  cache keys, manifest/info JSON, watch/LSP wiring, tests, docs, and the roadmap;
  this early development phase does not carry a deprecated compatibility API.
  (Attr names and lowercase params now match exactly; the first-letter case-fold
  that mapped `class`→`Class` fields is no longer needed.)

## Migration — atomic cutover

This is an **atomic** source / generated-code / manual-caller cutover: the old
generator *rejects* explicit `children`/`attrs` params, and the new generator
*deletes* the Props ABI — the two cannot coexist per-file. Given the early syntax
phase, embrace the atomic rewrite; do **not** build a dual-mode compatibility
layer. Scope (fresh one-learning scan): **841 component declarations across 113
files**, and **71 hand-written `RenderComponent` calls** outside generated files.

- Every component using children/attrs **declares** the reserved param.
- Plain-Go factory-returned callable signatures name every parameter. Factories
  that intend fallthrough publish the reserved `attrs` name in the static
  function type, for example
  `func named(name string) func(attrs ...gsx.Attr) gsx.Node`; a differently named
  bag parameter remains an ordinary exact-name prop.
- Existing reserved `attrs={{...}}` call sites remain valid, but move from the
  source-position-independent merge-last rule to ordinary authored-position
  composition. `attrs={expr}` is pinned as its computed-bag counterpart.
- Single-param method pages: `Props()` return type now matches the verbatim
  `Page` param — the 8 audited bugs resolve at the source. Manual call sites
  (`attachments.go:204`, `RenderComponent(X.Page, XProps{…})`) pass the value
  directly.
- Corpus, examples, docs, and `gsx init` scaffold regenerate/migrate. New syntax
  valid in multiple contexts ships a corpus case per context (per CLAUDE.md).
- The plan must include: **stale-wrapper detection** (a poisoned/orphan `.x.go`
  carrying an old `Props` type), **direct-Go compile fixtures** proving the
  generated signature is exact, an explicit **rollback unit**, LSP/cache tests,
  and **generation benchmarks**.

Atomicity has two scopes. Within this repository, declaration emission, call
emission, owned sources, generated outputs, scaffolds, and playground consumers
switch in one rollback unit. Across repositories Git cannot provide one atomic
commit, so the core branch is explicitly **non-mergeable and non-releasable**
until the structpages and one-learning branches have both been migrated and
verified against the exact core feature commit. The release transaction is:

1. prepare the core, structpages, and one-learning branches and verify the two
   consumer branches through an ignored workspace pinned to the core commit;
2. freeze and record those commit IDs plus all mandatory gate output;
3. merge the already-consumer-verified core commit and publish its version;
4. replace the temporary workspace wiring with that released version, rerun the
   same consumer gates, then merge the prepared consumer branches.

Any failure stops the transaction before another repository advances. This is a
release gate, not a compatibility window or an optional post-release migration.

**Stale-wrapper boundary.** Detection is generation-time, not a promise made by
plain `go build` (the Go tool does not read `.gsx`). Signature discovery ignores a
paired disk `.x.go` when authoritative `.gsx` declaration facts exist; a normal
generation overwrites that paired output. `gsx generate` removes and reports an
orphan file with gsx's exact generated header and `.x.go` naming convention when
its source `.gsx` no longer exists, including directories with no remaining
`.gsx`; this preserves the existing ownership-gated orphan behavior.

The existing `gsx -> Go-only project package -> gsx` importer path is also part
of the cutover. It currently obtains the transitive gsx package from the external
importer's on-disk `.x.go`, so ignoring only directly paired output is
insufficient. In normal module mode, the existing single cold `packages.Load`
adds `NeedCompiledGoFiles` and `NeedSyntax` and retains each project-local Go-only
package's `CompiledGoFiles`/`Syntax` inventory in that load's exact build context.
Before that same load classifies packages, every on-disk `.x.go` paired with an
authoritative `.gsx` source is overlaid with a contradictory Go build constraint
and therefore excluded for every possible tag assignment. Stale wrong-package
clauses and syntax errors therefore cannot poison package selection, while the
Go command remains the authority for the active compiled-file set.
The same pre-load manifest adds one declaration-free synthetic Go sentinel per
valid GSX package, then filters that sentinel out of the retained companion
inventory. A GSX-only package is therefore represented as an explicit empty
compiled-file set rather than an error-text exception. Each owned GSX package is
also an exact load root, including packages below `testdata` or underscore-
prefixed directories that `./...` omits, and every import authored in GSX is an
explicit load root so deleting generated output cannot erase dependency
discovery. Nested modules and `vendor` trees are excluded before any overlay or
load root is created.
Retained package identity is accepted only when both the clean absolute directory
and `importPathForDir` match the loaded package, so a nested module cannot enter
the parent module's source inventory. That cold validation also publishes the
authoritative import-path-to-directory index used by every warm exact import and
dependency-edge update; warm analysis never repeats filesystem package
discovery. The retained syntax—not a later disk reparse—is source-checked by the
exact importer; cgo-transformed compiled files therefore follow the same
authoritative path.
When any retained module-local package is reachable during analysis, its
compiled files are source-type-checked through the exact importer rather than
reusing the cold load's best-effort `Types`. This includes Go-only intermediaries,
so a `gsx → Go-only → gsx` path resolves the final package from the authoritative
in-memory skeleton. Cache and reverse-dependency edges include every such
intermediary; a warm gsx-signature edit rechecks the retained syntax without
another load.

The external-import boundary is one-way. An external dependency imported by the
main module may not import any package owned by that main module, directly or
transitively. Such an `owned GSX/Go package → external package → owned package`
backedge is rejected at the authored importing source position with a stable
semantic-boundary diagnostic. Gsx does not re-type-check external source against
one of its internal declaration universes, suppress external bodies, or retain a
phase-specific external reconstruction cache. Supporting this topology later
would require a separate design for a coherent whole-graph source universe.

A `Module` freezes the process build environment used by its cold load. A caller
that changes build environment creates a new Module. Unsaved overrides and saved
disk sources feed the same authoritative source manifest. Once supplied or
published, that manifest is an immutable membership-and-byte snapshot consumed
by package parsing, analysis, and cache identity; those consumers never glob or
reread live disk independently. An explicit disk refresh atomically replaces the
snapshot while preserving current overrides. Before watch performs
warm invalidation for a saved `.gsx` create, delete, rename, or write, it refreshes
the affected disk-source facts and publishes that refresh atomically; `Invalidate`
alone is not a disk refresh. If a newly-created source claims a paired `.x.go`
after the inventory already exists, the next analysis atomically rebuilds the
FileSet, external importer, source inventory, and all position-bearing package
caches before proceeding; it never continues with stale file selection.

`gsx watch` and `gsx dev` remain alive when the requested roots currently contain
no GSX files. They watch the requested roots and their owning module trees rather
than only directories discovered from the initial GSX set, so new sibling
packages and module/source changes are observable. Directory-create events are
registered and their already-present contents scanned before filename filtering.
An `.x.go` event is classified as generated output only while an exact same-base
`.gsx` source exists; an unpaired authored `.x.go` remains a real Go-source event.

Observation scope and generation scope are distinct. A watch session observes
the owning module tree plus the exact reachable workspace/local-replacement roots
and provenance files published by the same frozen Go-command/source manifest; it
does not guess additional roots from path prefixes. It generates only the
requested source roots and their exact current GSX dependency closure, never
unrelated sibling packages merely because their events must be observable. A
manifest change atomically updates both that closure and the watched physical
roots.

Startup closes the edit gap in this order: resolve roots, arm every current watch,
snapshot the authoritative sources, generate from that snapshot, then consume
the queued events. A source transition observed during initial generation is
therefore regenerated rather than acknowledged as part of the earlier snapshot.
After any generation failure, `watch` and `dev` retain the dirty package/closure
state; the next relevant event retries the complete pending state. They publish a
clean state only after a successful generation and never drop a failed event to
make the session appear current. This transaction distinguishes authored
diagnostics from operational failure: a completed cycle that emits poison plus
positioned diagnostics commits its observed source snapshot, while either a
top-level regeneration error or any per-directory operational `Err` retains the
whole dirty set. A later event unions into that retained set before retry.

The LSP uses the same source-view boundary for unsaved `.go` and `.gsx` buffers.
Go buffers enter the one authoritative `go/packages` overlay before package
selection and syntax loading; a second parse or GSX-only approximation is not an
acceptable editor model. Every override transition returns the exact affected
directory closure computed by the same reverse-dependency/renderer/configured-
source invalidation primitive. The server evicts those cached LSP packages,
supersedes their workers, intersects the closure with currently open documents,
and schedules only that set, including reverse dependents in other directories.

Closing a buffer always ends buffer authority, even when the saved path is
unreadable. Saved source has three explicit states (present, absent, unreadable):
an unreadable state fails subsequent analysis closed and can never retain the
closed bytes. Moving an open path between module roots retires its prior owner
before resolving or attaching the new owner; failure returns the old affected
closure and removes stale navigation facts. Debounce events carry an epoch
assigned at the document mutation, not at worker launch. Set, cancel, close, and
root transfer advance that epoch; queued stale events and events for directories
with no open documents are ignored. Thus a pre-close callback cannot republish a
closed URI, and `didChange` supersedes an in-flight worker immediately rather
than after the debounce delay.

Package-path membership, package-clause changes, and the sorted GSX import
surface are versioned independently from body content. New/deleted packages and
package membership changes rebuild the manifest. Import additions rebuild when
the published cold importer cannot already supply the new path; removals and
already-published additions safely retain that exact known-path superset. A
body-only edit preserves the one-load warm path. An epoch check around refresh
and cold load prevents a concurrent override or disk event from publishing stale
facts.

Normal analysis and persistent generation-cache metadata consume this one
manifest implementation; there is no second plain-`go list` approximation of the
source graph. Its cache view includes owned GSX-only packages and their authored
imports, active companion Go selection, local replacement-module provenance and
`go.mod` inputs, and the paired-output exclusion map. A still-paired generated
`.x.go` never supplies dependency edges or source identity, even if its bytes are
poisoned; transitions between paired and orphan ownership remain tracked for
generation cleanup. Cache lookup and warm generation therefore cannot disagree
about which source universe a key represents.

The normal resolver owns one virtual source view for both Go package discovery
and syntax loading. It therefore rejects an effective `GOFLAGS=-overlay=...`
before the cold load. Go overlays can represent deletion while
`packages.Config.Overlay` can represent only replacement bytes; composing just
the non-deletion subset would make `go list` and `go/packages` parse different
files. The check uses the Go command's effective `GOFLAGS`, including a value
persisted by `go env -w` and cmd/go's last-flag-wins behavior, and does not
silently ignore, replace, or partially materialize a user overlay. First-class
interoperability requires a separate deletion-aware loader shared by package
discovery and parsing.

For the same reason, normal mode rejects an effective external
`GOPACKAGESDRIVER`, whether configured explicitly or discovered on the frozen
Open-time `PATH`.
External drivers have implementation-defined overlay support and cannot satisfy
this resolver's authoritative Go-command inventory contract implicitly. Only
after proving no configured or frozen-PATH driver was effective does gsx pin
`GOPACKAGESDRIVER=off` in the load environment, preventing x/tools from
re-evaluating a later live `PATH`; this does not hide configured driver state.
Bundle mode is unaffected by either boundary because it performs no
`packages.Load`.

Every manual `go/types` universe also receives the frozen target's
`TypesSizes` and module language version. Normal mode retains both from the
authoritative package load; a module with no `go` directive uses cmd/go's
specified `go1.16` language default. A browser type archive is self-describing:
its versioned envelope records compiler, GOOS, GOARCH, cgo, toolchain version,
snippet language version, and the observed build/tool/release tag sets from the
same immutable Go command environment and build flags used by `packages.Load`.
Archives are `gc`-only. The reader reconstructs sizes only from recorded
`gc`/GOARCH, rejects a producer toolchain newer than the repository's pinned
reader toolchain, and rejects legacy, incomplete, or unknown envelopes.
Toolchain and language versions are separate because the pinned server
toolchain may intentionally compile an older module language contract.
External package drivers cannot produce this exact provenance and are rejected
by the archive producer. The package export payload is opaque and owned by
`golang.org/x/tools/go/gcexportdata`; gsx's envelope owns its exact byte length,
SHA-256 digest, and canonical target metadata, but does not parse or rewrite the
private indexed-export format. Producer upgrades regenerate the archive and
must pass the pinned upstream decoder/universe checks before the reader ceiling
is advanced. The bundle is a trusted embedded build artifact: the digest detects
accidental byte/metadata drift but is not an authenticity mechanism for
hostile, re-signed input.

The playground server validates that exact archive target manifest at startup.
Its codegen resolver is built from the same embedded archive as the browser and
accepts a complete in-memory GSX source set; it is not constructed from the
first disposable server workspace and never reuses one workspace's module
universe for another.
Every child compile starts from the pinned target after removing inherited
`GOEXPERIMENT`, `GOAMD64`, and `GOTOOLCHAIN`; deployment ambience cannot alter the
engine contract. The independently deployed browser and server exchange exact
`engineID` and `targetManifestID` values on `/run`, and both values participate in
the server result/cache key. A mismatch returns HTTP 409 with a structured reload
handshake containing the server's current IDs, so the client reloads instead of
executing or caching a response from a different engine universe.

Bundle mode deliberately carries prebuilt types but no authoritative project
source/build inventory. It continues to support its existing single-gsx-package
plus external-dependencies contract. If analysis finds a project-local
`gsx -> Go-only -> gsx` path in Bundle mode, it fails closed with a diagnostic
directing the caller to the normal resolver; it never accepts the bundled stale
transitive ABI. Project ownership follows actual nested `go.mod`/vendor
boundaries, not a lexical module-path prefix. An external prebuilt graph reached
directly or through a project Go-only bridge is walked far enough to reject any
external-to-main-module backedge with the same semantic-boundary diagnostic as
normal mode, positioned on the authored import Bundle can observe.
`SourceOnly` is exactly one authored in-memory package plus prebuilt externals;
it delegates imports without consulting host ownership or source files. Do not
solve either mode by generation ordering, writing
dependencies early, reloading `packages.Load`, or emitting an ABI sentinel: a
single analysis run must not observe a mixture of old disk ABI and new skeleton
ABI. No emitted compatibility wrapper is introduced.

The archive-backed rootless API is a separate `*gen.BundledResolver`, returned
by `gen.NewBundledResolver`. It exposes only `GenerateSource` and
`GenerateSources`; it has no disk `Generate` method. Conversely,
`*gen.CachedResolver` exposes only its root-bound `Generate` method and cannot
be used as a source-only resolver. The two public types share private generation
internals without a runtime mode flag.

The public module-backed `gen.NewCachedResolver` is bound at construction to
the owning module's physical root identity and declared module path. Each
disk-based `Generate` resolves the target directory's nearest `go.mod` and
rejects a different root, a nested module, a replaced root inode, or a changed
module directive before cached types are used. Both the logical directory and
its resolved physical path must remain within that root; an in-root symlink may
not escape to outside source. Module discovery fully parses the nearest
`go.mod`, fails closed on an unreadable/malformed file or one without a module
directive, and never skips that boundary to fall through to an enclosing module.

Analysis-only variant declarations use package-unique, always-unexported
function and props-type names. The private props name is explicit plan data and
never derived from receiver spelling; public emission alone uses the authored
shipping declaration name. This keeps dot imports and receiver aliases from
creating exported analysis artifacts or changing allocation.

## Open implementation risks (design is settled; these are execution concerns)

1. **Stable call-site preprocessing and two-phase callable analysis.** Positional probe construction needs the
   callable signature, but today's single skeleton pass learns an arbitrary
   callable signature only after `_gsxcompsig` harvest. Before either phase,
   materialize every element embedded in an interpolation or Go block, validate
   every top-level `GoWithElements` exclusion mapping from the canonical GSX
   source-mapped expression reconstruction: apply the shared
   decorative-parenthesis rules and `goexprshape.Sanitize`; represent every
   non-text GSX part with a unique non-call value marker; and map every sanitizer
   and parser byte offset back to the authored `token.Pos`. Element/fragment
   emission is a `gsx.Func(...)` conversion value, not an invocation, so this
   preserves the final `go`/`defer` rejection for every GSX value. Reject any `go/parser`
   error or recovery AST, and reject any non-text part without one exact
   enclosing declaration. Parser failures retain `parse-error`/`parser` while
   structural declaration-mapping failures use
   `invalid-go-declaration`/`codegen`. Only then run the JavaScript safety
   resolver over that complete expanded tree and stamp component
   classification exactly once. Assign call-site IDs to that shared,
   mutated AST and reuse the same tree for target discovery, positional
   validation, LSP facts, and final emission; neither phase may re-split or clone
   embedded markup. This does not add element literals inside `{{ }}` Go blocks:
   the first direct element/fragment is recorded once on the expanded
   `GoBlock`, and every consumer uses that annotation. Those nodes remain
   materialized only long enough to be stamped and receive the existing single
   positioned `unsupported-node` diagnostic; their entire blocks are excluded
   from further expression materialization, secondary JSX/tag validation,
   target planning, and emission. A syntax
   or JavaScript failure produces no registry, so partial package facts cannot
   escape. Each fresh parse is owned by a private codegen package-lifecycle
   object; an atomic one-shot package transition is claimed before mutation,
   and any concurrent or repeated pass is rejected before it can duplicate
   diagnostics or consume partial state. Lifecycle state never lives on the
   public `ast.File`, where it would affect AST equality and remain bypassable by
   shallow copies. Every
   separately parsed production AST (including imported facts,
   renderer declaration resolution, and importer-free unused-import analysis)
   runs the same preprocessor before body-derived facts are read. Supported
   interpolation and top-level `GoWithElements`
   sites retain their IDs through emission.

   Package declaration-name discovery is part of this syntax boundary:
   `GoWithElements` names come from the same canonical reconstruction and a
   complete parser result, never an independent `nil` substitution or recovery
   AST. The pre-cutover legacy implicit-role/liveness scanners consume the
   unsupported-block annotation but otherwise remain frozen until their atomic
   deletion above; they are not a second interpretation of expanded markup.

   Then use two in-memory `go/types` phases. Target discovery has distinct
   exact-signature declaration and lexical-discovery skeleton modes; it never
   reads or alters the shipping pre-cutover Props skeleton. Before inserting a
   target expression, syntax-check that site alone and map it through exact
   parser-recorded tag/open-bracket/close-bracket positions. A malformed target
   emits a parse-safe inert binding plus one total fact carrying its deferred
   positioned diagnostic, rather than making the package skeleton unparseable.
   The target-discovery skeleton records
   each site's origin generic signature/object provenance and
   `types.Selection.Kind`. For a `MethodVal`, `Selection.Type()` is the raw call
   contract: its callable parameters omit/substitute the receiver even though
   `Signature.Recv()` metadata remains present. `MethodExpr` rejection remains
   unconditional even when go/types records a receiver-free explicit instance.
   Tolerate an incomplete generic target only when structure proves it: the
   supplier resolves to the raw generic function/method, the authored prefix is
   shorter than the target type-parameter arity, and the sole site-local error
   is at the exact raw target-expression start. Do not inspect diagnostic text;
   all other target errors remain deferred. After authored operands are bound, the
   inference carrier described above records the completed instance; a positional
   skeleton validates the zero-filled call. Facts are keyed by call-site ID/AST
   identity, never tag text, so shadowed selectors, repeated tags, bound method
   values, and true method expressions cannot alias. Both phases reuse the
   existing external importer; neither may call `packages.Load`. Imported project
   GSX targets resolve through a separate exact-declaration source graph/cache,
   not the current `pkgTypes` graph whose functions still expose the Props ABI.
   That phase-specific graph recursively uses current `.gsx` source, ignores
   paired disk `.x.go`, has its own cycle guard, and is cleared at the existing
   package/FileSet invalidation boundaries. Go/types may populate
   `Info.Instances` even when a constraint fails, so an explicit instance is
   successful only when that site has no target-check errors. Benchmark the added
   pass and cache declaration facts at the existing package invalidation boundary.
2. **Cross-package invocation resolution.** `<pkg.Foo …/>` needs Foo's ordered
   signature — param names, types, and reserved-role classification — resolved at
   the call site. Today's cross-package facts and `pkgTypes` are Props-shaped, so
   neither is authoritative for target discovery. Use the phase-specific
   exact-declaration graph above; do not read stale generated output, add a second
   cold load, or derive parameter names from export-data guesses. `packages.Load`
   cost must be respected (see the packages.Load perf memory). Cutting
   struct-splat removed the need to also resolve a splat *source's* field set,
   which was the heavier half of this.
3. **Codegen rewrite scope.** `emit.go` (`genComponent`), `analyze.go`
   (`componentPropFieldsFor`, skeleton/probe, `emitComponentSkeleton`), call-site
   lowering (`genChildComponent`), and the imported-props / attrs-only subsystems
   all key on the synthesized props type today and must re-home onto the
   verbatim-signature model. `ast.OrderedAttrsAttr` also has stale documentation
   saying it lowers to `gsx.OrderedAttrs`; the implementation already emits
   `gsx.Attrs`, and that comment must be corrected while this path moves.
4. **attrs subsystem re-home.** class/style merge, URL sinks, renderers, and
   spread hardening currently read the props struct's `Attrs` field; they move to
   the declared `attrs` parameter. Mechanically equivalent (still a `gsx.Attrs`
   value) but broad. The current `attrsLitIdx`/forced-last branch for reserved
   `attrs={{...}}` is replaced by the same authored-position contributor path as
   unmatched attrs, spreads, conditionals, and `attrs={expr}`; `name={{...}}`
   remains an ordinary `gsx.Attrs`-valued prop form when `name` is not reserved.
5. **LSP / fmt.** Nav, hover, unused-analysis, add-import must follow the
   signature change. `definition_attr.go` uses first-letter case-insensitive
   matching and must adopt codegen's **exact-name** rule. **Parameter-rename**
   support becomes much more valuable (gopls does not understand markup
   attributes, so a Go rename silently breaks every `<Tag attr=…>` call site).
   Rename is offered for parameters declared by gsx components. It follows
   semantic object identity, normalizing instantiated generic parameters through
   `types.Var.Origin()`, and updates exact call-site attrs.
   Renaming reserved `children` or `attrs` is rejected because it would change
   the parameter's language role rather than merely rename the markup contract;
   an ordinary parameter likewise cannot be renamed to `children`, `attrs`,
   `ctx`, `_`, or `_gsx...`; ordinary Go identifier/collision validation also
   applies. For a gsx build-tag variant set, rename updates the same parameter
   ordinal in every already-equivalent declaration and all of their call sites
   atomically; if equivalence cannot be proven, rename is rejected. Plain-Go
   callable parameters still participate in definition/hover and exact binding,
   but gsx does not offer rename for them: inactive Go build variants are owned by
   the Go tool/gopls and cannot be safely fan-out edited from gsx's all-source
   variant model.
6. **Component-only build variants.** A duplicate component forms a logical
   variant family only when every declaration's generated `.x.go` has an
   effective Go constraint: a valid `//go:build`, legacy `// +build`, or a
   recognized GOOS/GOARCH filename constraint. Same-file duplicates,
   unconstrained duplicates, and mixed constrained/unconstrained duplicates are
   hard errors. gsx does not try to prove constraints disjoint; the Go command
   remains responsible for rejecting a concrete build configuration that
   selects zero or multiple alternatives.

   Variant signatures are checked under unique in-memory names before one public
   representative is chosen. The contract requires an exact ordered value-
   parameter name/role vector, `types.Identical` function signatures, and
   separately `types.Identical` receiver types because Go signature identity
   ignores receivers and parameter names. Type-parameter names may be alpha-
   renamed. Import aliases and type spelling are not identity: two spellings of
   the same semantic type match, while the same spelling bound to different
   packages does not. Syntax-normalized declaration text remains only a
   deterministic parse/cache key, never an acceptance shortcut.

   Only component declarations receive logical variant treatment. Raw Go
   functions, methods, types, variables, and constants are never covered by a
   cross-file redeclaration suppressor; alternative implementations belong in
   ordinary constrained `.go` files behind one stable package API. Every GSX
   source, including inactive variants, is analyzed in one frozen Go universe,
   so a platform-only import used directly by an inactive `.gsx` file is an
   error. Supporting divergent platform import universes would require a
   separate multi-context analyzer, not skipped imports or textual inference.

## Testing

- **Corpus** (`internal/corpus`), a case per fill context: plain prop, `attrs`
  fallthrough (matched + leftover + strict-error), whole-value forwarding
  (`param={value}`), `children` single, `children` variadic (static list),
  node-valued attribute (`header={<h1/>}`), ordinary named-bag literal
  (`inputAttrs={{...}}`) and computed bag (`inputAttrs={computedBag}`), with each
  value spread onto two chosen inner elements, element attr-spread,
  attrs-forwarding into a component, every accepted `attrs` signature shape,
  generic component inference, bound method value, rejected true method
  expression, and free-function component. Pin `generated.x.go.golden` +
  `render.golden`.
- **Adversarial corpus** (the sharp edges the review surfaced):
  - zero-fill for opaque defined numeric/nilable types, an accessible unnamed
    underlying shape, and an **imported opaque struct with an unexported field**
    (the last → required-attribute diagnostic, no adapter);
  - Go-driven generic inference from authored operands only, including
    `Infer[T](*T)` with `nil`, constraint inference, and omission requiring an
    explicit-type-arg diagnostic;
  - **authored order ≠ parameter order** (side effects, short-circuit) — proves
    once-only lexical evaluation;
  - a final emitted **cross-package positional call** whose same-typed ordinary
    parameters appear out of call order and whose callee also declares `attrs`;
    generated and rendered goldens prove imported name/order/role facts survive
    resolution. Include a package variable declared through a named func-type
    alias, not only a direct named func type;
  - untyped call-valued constants such as `min(1, 2)` retain target context, and
    **`(T, error)`** propagation through every contributor kind completes before
    positional assembly;
  - exact-`gsx.Node` boundary promotion for static strings, `f`-literals,
    Stringers, scalars, existing Nodes, and tuple-unwrapped values; generic
    inference consumes the adapted fact; concrete Node implementations remain
    exact-type parameters; imported components and aliases are covered; and leaf
    `f`-literal direct-write behavior remains unchanged;
  - duplicate ordinary fills get `duplicate-prop`, while repeated `attrs`
    contributors remain legal; blank/unnamed parameters (fixed and variadic)
    receive the general named-parameters diagnostic and grouped parameter
    declarations preserve logical order;
  - **reserved-role collisions**, including authored-order composition of
    `attrs={}`, `attrs={{ }}`, ordinary fallthrough, and multiple explicit attrs
    contributors; rejection without a declared attrs role; rejection of
    non-bag attrs forms; success of ordinary `a={{ }}` and
    `someAttrs={{ }}` bag-valued props; and proof that attrs-specific diagnostics
    appear only for authored fallthrough with no literal `attrs` parameter;
  - exact `class`/`style` params across static, braced, composed, embedded,
    ordered, and markup-valued forms; conditional-attribute branches stay bag-
    only;
  - **exact-case mismatch** and **parameter rename** (contract break);
  - **every supported callable kind** and each **rejected dynamic kind**
    (fail-closed), including concrete result types assignable to `gsx.Node`,
    per-element provenance, bound concrete methods, interface methods, and true
    method expressions;
  - the merged **attrs-bag signature family**, including defined-slice conversion,
    an imported unexported defined-slice target, defined-slice contributors,
    variadic expansion, alias handling, exact element identity, the general
    named-parameters rejection for an unnamed bag variadic, and universal
    ordinary-variadic omission/fill rejection for a named non-reserved
    `...gsx.Attr` parameter;
  - factory-returned callable signatures with named and unnamed anonymous func
    types, named func types, and aliases; same-package and imported definition /
    hover positions; export-only no-position behavior; and a negative semantic-
    rename row for plain-Go parameters;
  - **signature-only** dependency/cache invalidation (a rename must bust caches);
  - saved-disk refresh before warm invalidation: create the first `.gsx` in a new
    package, add a previously unseen import, and change a package clause/file
    membership; each next generation sees one coherent refreshed manifest;
  - persistent-cache graph/key parity for a GSX-only dependency, a local
    replacement `go.mod` change, and poisoned/changed paired `.x.go` output; the
    cache and normal analyzer must select the same authoritative inputs and the
    paired output must never contribute a dependency edge;
  - `gsx -> Go-only project package -> gsx` resolution uses the current skeleton
    ABI on the first run even when the disk `.x.go` contains the old Props ABI;
    warm invalidation follows the Go-only intermediary without another
    `packages.Load`;
  - supported embedded component tags retain one call-site identity across
    discovery, validation, and emission; a direct element literal in a `{{ }}`
    Go block retains its existing single rejection and never reaches planning;
  - gsx build-tag parameter rename updates all equivalent variants by ordinal,
    rejects `_`/reserved-name/unprovable-variant moves, and plain-Go callable
    parameter rename is not offered;
  - a **direct-Go compile fixture** asserting the generated signature is exact
    and emitted `.x.go` contains no analysis helper/type declaration.
- **structpages interop**: a differential test mounting a route tree and driving
  each page, asserting `Props() T → Page(x T)` wires (the regression that started
  this).
- **fmt corpus**: layout implications of declaring `children`/`attrs` params.
- **Sibling repos** (tree-sitter-gsx, vscode-gsx, gsxhq.github.io): reserved-name
  and any grammar implications.
- **Performance:** benchmark cold/warm generation before and after the two-phase
  analysis; no new `packages.Load`, and warm cached generation must retain the
  existing dev-loop profile.
