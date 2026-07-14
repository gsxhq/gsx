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

1. a type-independent zero (`""`, `0`, `false`, or `nil`) when Go says the
   untyped value is assignable to the instantiated parameter type;
2. `*new(T)` when the exact type `T` is nameable in the caller;
3. `*new(U)` when an accessible unnamed type expression `U` is assignable to the
   parameter (for example, the spellable underlying struct/array shape of an
   otherwise unexported named type).

The analyzer validates candidate types and assignability with `go/types`; it does
not infer lowerability from exported spelling or an underlying-kind heuristic.
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
bindings are known, analysis installs a transient semantic `types.Func` in the
checker scope. Its `types.Signature` reuses the target's semantic type parameters,
constraints, and supplied parameter types, but omits every unsupplied parameter.
Because this carrier is assembled from `go/types` objects rather than copied Go
source, imported unexported constraints remain usable without being named. Its
instance supplies the inferred type arguments for the original signature. Only
then does lowering instantiate that signature, synthesize zeros, and validate the
complete call. The carrier is analysis scaffolding, never an emitted declaration.

Inference diagnostics follow Go's failure class, in this order: authored operand
parse/type errors retain their native positioned diagnostics; an incomplete
inference alone gets the explicit-type-argument hint (`<Box[string]/>`); an
inferred or explicit argument that violates a constraint gets the native
constraint diagnostic without claiming explicit arguments will fix it. Do not
decide inference by syntactic type-parameter occurrence: `Infer[T](*T)` cannot
infer `T` from `nil`, while constraints may infer a parameter not textually
present in a supplied parameter type.

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
- A **blank or unnamed fixed parameter** (`func(_ string) gsx.Node` or
  `func(string) gsx.Node`) —
  markup cannot name/fill it, so the callable is **tag-ineligible**: using it as a
  tag is a compile error (fail closed). It remains valid for direct Go and
  structpages calls (`fillMethodArgs` can fill `_ *Store` by type). Names come
  only from the callee's static signature; gsx never tries to recover a name from
  a function originally assigned to an unnamed func type. Rationale: silently
  zero-filling an injected fixed parameter is a footgun. A blank or unnamed
  ordinary variadic is Go-only and may be omitted, matching the general
  variadic rule.

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
compile error.

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
| `<C Attrs={{…}}/>` / `<C Children={…}/>` | when no exact ordinary capitalized param exists, emit a **migration diagnostic** pointing to lowercase `attrs`, bag spread, or body syntax; never silently make the legacy spelling an HTML fallthrough attribute |

**Settled: accept `attrs={expr}` and `attrs={{...}}`; reject only explicit
`children={...}`.** Rejecting the two attrs forms would add work and a surprising
exception even though `attrs` is an input role rather than an ordinary parameter.
Their capability overlaps `{expr...}` and individual attributes, but uniform
syntax is the stronger rule here.

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
ordinary named prop, filled by a **markup-valued attribute** — `header={<h1/>}`.
(This is the existing attribute-value grammar, *not* a distinct body-child slot
grammar; "named slots" are just node-typed props filled this way.)

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
  and must be replaced by fail-closed resolution here.

A signature alone is not enough; the discovery pass records provenance. Allowed
origins are a package-scope `types.Func`, a package-scope function-valued
`types.Var` (bare or through a package selector), or a concrete
`types.MethodVal`. Reject `types.MethodExpr`, struct fields, locals/parameters,
interface dispatch, and any other dynamic origin even when its current type is a
concrete signature. Named func types and aliases inherit the eligibility of the
object that supplies the value. Parameter names come only from that static
signature. A fixed parameter whose `types.Var.Name()` is empty or `_` triggers
the tag-ineligible rule above; missing export-data names are never guessed.

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

Arbitrary-name structural classification is retired with a migration diagnostic;
`func(extra ...gsx.Attr) gsx.Node` does not silently acquire the reserved role.
Useful icon/factory values remain supported by naming the bag `attrs`, and other
node-valued or attrs-bag-valued parameters remain ordinary exact-name props.

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
- **Reserved-identifier body scanning + shadow handling** (`reserved_scan.go`,
  `usesChildren`/`usesAttrs` free-use token scans, branch-env shadow reasoning) —
  gone. Whether a component uses children/attrs is now read off the *signature*
  (it declared the param or it didn't), and a local named `attrs`/`children` is
  ordinary Go scoping, not a shadow of a magic identifier. (`ctx` is still ambient
  and needs no scan.)
- **`WithFieldMatcher`** (`gen/options.go:355`) and fuzzy attr→field matching —
  obsolete under **exact-name** matching. Remove/deprecate deliberately across
  config, cache keys, manifest/info JSON, watch/LSP wiring, tests, docs, and the
  roadmap. (Attr names and lowercase params now match exactly; the first-letter
  case-fold that mapped `class`→`Class` fields is no longer needed.)

## Migration — atomic cutover

This is an **atomic** source / generated-code / manual-caller cutover: the old
generator *rejects* explicit `children`/`attrs` params, and the new generator
*deletes* the Props ABI — the two cannot coexist per-file. Given the early syntax
phase, embrace the atomic rewrite; do **not** build a dual-mode compatibility
layer. Scope (fresh one-learning scan): **841 component declarations across 113
files**, and **71 hand-written `RenderComponent` calls** outside generated files.

- Every component using children/attrs **declares** the reserved param.
- Plain-Go attrs-only factories retain their bag-shaped signatures but publish
  the reserved name in the static function type, for example
  `func named(string) func(attrs ...gsx.Attr) gsx.Node`.
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
When such a package is reachable during analysis, those compiled files are
source-type-checked through the same module importer, so any gsx package they
import resolves from the authoritative in-memory skeleton. Cache and reverse-
dependency edges include those intermediaries; a warm gsx-signature edit
rechecks the retained syntax without another load.

Bundle mode deliberately carries prebuilt types but no authoritative project
source/build inventory. It continues to support its existing single-gsx-package
plus external-dependencies contract. If analysis finds a project-local
`gsx -> Go-only -> gsx` path in Bundle mode, it fails closed with a diagnostic
directing the caller to the normal resolver; it never accepts the bundled stale
transitive ABI. Do not solve either mode by generation ordering, writing
dependencies early, reloading `packages.Load`, or emitting an ABI sentinel: a
single analysis run must not observe a mixture of old disk ABI and new skeleton
ABI. No emitted compatibility wrapper is introduced.

## Open implementation risks (design is settled; these are execution concerns)

1. **Stable call-site preprocessing and two-phase callable analysis.** Positional probe construction needs the
   callable signature, but today's single skeleton pass learns an arbitrary
   callable signature only after `_gsxcompsig` harvest. Before either phase,
   materialize every element embedded in an interpolation or Go block and stamp
   component classification exactly once. Assign call-site IDs to that shared,
   mutated AST and reuse the same tree for target discovery, positional
   validation, LSP facts, and final emission; neither phase may re-split or clone
   embedded markup. This does not add element literals inside `{{ }}` Go blocks:
   those nodes remain materialized only long enough to receive the existing
   single positioned `unsupported-node` diagnostic, and are excluded from target
   planning/emission. Supported interpolation and top-level `GoWithElements`
   sites retain their IDs through emission.

   Then use two in-memory `go/types` phases: a target-discovery skeleton records
   each site's origin generic signature/object provenance and
   `types.Selection.Kind`, tolerating only the registered expected diagnostic for
   an uninstantiated generic target. After authored operands are bound, the
   inference carrier described above records the completed instance; a positional
   skeleton validates the zero-filled call. Facts are keyed by call-site ID/AST
   identity, never tag text, so shadowed selectors, repeated tags, bound method
   values, and true method expressions cannot alias. Both phases reuse the
   existing importer; neither may call `packages.Load`. Benchmark the added pass
   and cache declaration facts at the existing package invalidation boundary.
2. **Cross-package invocation resolution.** `<pkg.Foo …/>` needs Foo's ordered
   signature — param names, types, and reserved-role classification — resolved at
   the call site. Today's cross-package facts are field-name sets; this is a
   different shape and leans on type resolution. `packages.Load` cost must be
   respected (see the packages.Load perf memory); prefer the existing
   skeleton/probe-based enumeration over new heavyweight loads. (Cutting
   struct-splat removed the need to also resolve a splat *source's* field set,
   which was the heavier half of this.)
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
6. **Build-tag variant collision.** `variantcollide.go:componentSignature`
   currently sorts props and treats reordered declarations as equivalent. It must
   compare **ordered** parameter names, types, variadic position, and reserved
   roles — reorder/rename are now contract changes. Grouped and ungrouped forms
   with the same logical ordered parameters are equivalent. Each receiver,
   constraint, and parameter type compares its normalized Go source spelling,
   not alias-resolved semantic identity, so `Alias` and its expansion differ.
   This is deliberately strict and avoids loading mutually exclusive variants
   merely to suppress a redeclaration diagnostic.

## Testing

- **Corpus** (`internal/corpus`), a case per fill context: plain prop, `attrs`
  fallthrough (matched + leftover + strict-error), whole-value forwarding
  (`param={value}`), `children` single, `children` variadic (static list),
  node-valued attribute (`header={<h1/>}`), ordinary named-bag literal
  (`inputAttrs={{...}}`), element attr-spread, attrs-forwarding into a component,
  every accepted `attrs` signature shape, generic component inference, bound
  method value, rejected true method expression, and free-function component.
  Pin `generated.x.go.golden` + `render.golden`.
- **Adversarial corpus** (the sharp edges the review surfaced):
  - zero-fill for opaque defined numeric/nilable types, an accessible unnamed
    underlying shape, and an **imported opaque struct with an unexported field**
    (the last → required-attribute diagnostic, no adapter);
  - Go-driven generic inference from authored operands only, including
    `Infer[T](*T)` with `nil`, constraint inference, and omission requiring an
    explicit-type-arg diagnostic;
  - **authored order ≠ parameter order** (side effects, short-circuit) — proves
    once-only lexical evaluation;
  - untyped call-valued constants such as `min(1, 2)` retain target context, and
    **`(T, error)`** propagation through every contributor kind completes before
    positional assembly;
  - duplicate ordinary fills get `duplicate-prop`, while repeated `attrs`
    contributors remain legal; blank/unnamed fixed params are tag-ineligible and
    grouped parameter declarations preserve logical order;
  - **reserved-role collisions**, including authored-order composition of
    `attrs={}`, `attrs={{ }}`, ordinary fallthrough, and multiple explicit attrs
    contributors; rejection without a declared attrs role; rejection of
    non-bag attrs forms; and success of an ordinary `someAttrs={{ }}` prop;
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
    variadic expansion, alias handling, exact element identity, and the
    name-required diagnostic;
  - **signature-only** dependency/cache invalidation (a rename must bust caches);
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
