# Verbatim component signatures — the parameter list *is* the contract

**Status:** design settled — ready for implementation planning.
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
| Add a parameter | arity break | zero-filled (source-compatible) |
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
2. Only identifier-shaped attribute names can bind a param. `class`-style names
   that are not Go identifiers — `data-*`, `aria-*`, `hx-*`, any kebab name —
   **can never match a param** and always fall through.
3. Every unmatched attribute, `{bag...}` spread, conditional-attribute bag, and
   explicit `attrs` bag contributes to the `attrs` parameter in authored order.
4. **Strict:** an unmatched attribute with **no `attrs` param declared** is a
   **compile error** (`Card has no attrs param; unexpected attribute "class"`).
5. An **omitted** known prop **zero-fills** (optional; see §Zero-fill lowering).
   There is no `required` concept; presence checks are ordinary Go in the body.
6. **Parameter order is irrelevant to markup** (fill is by name/role). Order only
   matters to Go positional callers, who see the signature.

`class` / `style` are the author's choice: declare `class string` to take them as
plain props, or omit them and let them land in `attrs`, where a `{attrs...}`
spread on the root performs the existing class/style merge. **Caveat:** `class`
and `style` are *not yet* ordinary props for every syntax form — the parser
special-cases braced/composed/ordered forms today. Making them plain params needs
**target-aware** handling across the static, braced (`class={…}`), composed
(class parts), and ordered-attrs forms; this is called out as an implementation
task, not hand-waved as "just a prop."

### Zero-fill lowering — inline only, no generated helpers

An omitted prop needs its zero value at the call site, spelled **inline** — no
generated adapter or helper function. The zero is written directly: `""`, `0`,
`false`, `nil`, `T{}`, or the uniform `*new(T)` / a local `var _gsxz T` for the
general nameable case. Omitted trailing variadics pass nothing (`f(a)` for
`f(a, xs...)`). This keeps the principle intact: **gsx synthesizes no type and no
function**; call-site lowering uses only inline expressions and local temporaries.

**Not-nameable case → required attribute (fail closed).** Spelling a zero inline
requires naming the type. A direct call *cannot* name an **imported unexported
type**, so an omitted attribute whose param type is not nameable at the call site
is a **positioned diagnostic** ("attribute `x` is required here: its type is not
nameable in this package"), not a synthesized work-around. This is rare and
already-odd (a component exposing an unexported-typed param to cross-package
markup); the caller may still *provide* it via an expression of that type, but may
not omit it. A private generic call adapter (`_gsxcall…[A,B any](f, a)`) was
considered and **rejected**: replacing generated structs with generated call
helpers undercuts the whole motivation (honest ABI, no generated machinery,
simple output) and grows combinatorially with arity/omission shape.

**Inference-failure case.** When a type parameter appears *only* in omitted
arguments, Go cannot infer it (`<Box/>` with `item` omitted, `Box[T](item T)`).
Require explicit type arguments (`<Box[string]/>`); absent them, a **positioned
diagnostic**.

A legitimate but uncommon non-nameable case is an **opaque package value**:

```go
// package ui
type theme struct{ /* ... */ }
func DefaultTheme() theme { return theme{} }
func Widget(title string, theme theme) gsx.Node { return nil }
```

Another package can call `ui.Widget("title", ui.DefaultTheme())` — it can obtain
and pass `ui.theme` without naming the type — but cannot spell the omitted zero.
Markup therefore accepts `<ui.Widget title="title" theme={ui.DefaultTheme()}/>`
and rejects `<ui.Widget title="title"/>` with the required-attribute diagnostic.
This is the concrete cross-package use case to retain in the implementation
probes. If implementation finds another signature that cannot obey the
inline-only rule, it must stop with a minimal corpus reproducer and bring the
case back to the design; no helper, reflection, or guessed zero-value fallback.

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
Untyped constants and other context-dependent values stay inline so the final
parameter supplies their type context. The plan must also preserve **`(T, error)`
auto-unwrapping** — a naïve direct call could otherwise bind a returned `error`
into an adjacent component parameter.

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
- **`_`** (blank), and any fixed parameter markup cannot name/fill — the component
  is **tag-ineligible**: using it as a markup tag is a **compile error** (fail
  closed). It remains perfectly valid for direct Go and structpages calls
  (`fillMethodArgs` fills `_ *Store` by type). Rationale: silently zero-filling
  `nil` for an injected `_ *Store` on a markup call is a footgun; reject the
  markup invocation instead. gsx emits the param verbatim and binds no local.

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
`attrs={expr}` contributes an expression assignable to `gsx.Attrs`, exactly like
`{expr...}`; `attrs={{ "k": value }}` constructs and contributes a `gsx.Attrs`
literal. Separately, `name={{ "k": value }}` remains general attribute-value
syntax which produces a `gsx.Attrs` value for an **ordinary** named prop, so a
composite component can route that bag to one or several chosen descendants.

| Form | Meaning |
|---|---|
| `<C>body</C>` | fills `children` from the body |
| `<C children={n}/>` | **error** — `children` is body-populated; write `<C>{n}</C>` |
| `<C class="x"/>` (unmatched) + `attrs` param | `class` flows into `attrs` |
| `<C {bag...}/>` | contributes `bag` to `attrs` (fallthrough forwarding) |
| `<C attrs={a}/>` | contributes `a` to `attrs` at this authored position; `a` must be assignable to `gsx.Attrs` |
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
converts it for a defined slice, or expands it for the variadic form. The
component body sees the exact type the author declared.

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
  returning `gsx.Node`, bound method values (`x.Page` where `x` is a value), and
  named func types / aliases.
- **Ineligible (fail closed):** any callee whose signature does not statically
  resolve — including several field/local/interface-method/param shapes the
  current generator already rejects. **An unresolved signature is an error, not a
  guess.** The existing imported-props "warn-and-guess" fallback is unsound for
  positional calls (a guessed field set cannot produce a correct positional call)
  and must be replaced by fail-closed resolution here.

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

## Open implementation risks (design is settled; these are execution concerns)

1. **Cross-package invocation resolution.** `<pkg.Foo …/>` needs Foo's ordered
   signature — param names, types, and reserved-role classification — resolved at
   the call site. Today's cross-package facts are field-name sets; this is a
   different shape and leans on type resolution. `packages.Load` cost must be
   respected (see the packages.Load perf memory); prefer the existing
   skeleton/probe-based enumeration over new heavyweight loads. (Cutting
   struct-splat removed the need to also resolve a splat *source's* field set,
   which was the heavier half of this.)
2. **Codegen rewrite scope.** `emit.go` (`genComponent`), `analyze.go`
   (`componentPropFieldsFor`, skeleton/probe, `emitComponentSkeleton`), call-site
   lowering (`genChildComponent`), and the imported-props / attrs-only subsystems
   all key on the synthesized props type today and must re-home onto the
   verbatim-signature model. `ast.OrderedAttrsAttr` also has stale documentation
   saying it lowers to `gsx.OrderedAttrs`; the implementation already emits
   `gsx.Attrs`, and that comment must be corrected while this path moves.
3. **attrs subsystem re-home.** class/style merge, URL sinks, renderers, and
   spread hardening currently read the props struct's `Attrs` field; they move to
   the declared `attrs` parameter. Mechanically equivalent (still a `gsx.Attrs`
   value) but broad. The current `attrsLitIdx`/forced-last branch for reserved
   `attrs={{...}}` is replaced by the same authored-position contributor path as
   unmatched attrs, spreads, conditionals, and `attrs={expr}`; `name={{...}}`
   remains an ordinary `gsx.Attrs`-valued prop form when `name` is not reserved.
4. **LSP / fmt.** Nav, hover, unused-analysis, add-import must follow the
   signature change. `definition_attr.go` uses first-letter case-insensitive
   matching and must adopt codegen's **exact-name** rule. **Parameter-rename**
   support becomes much more valuable (gopls does not understand markup
   attributes, so a Go rename silently breaks every `<Tag attr=…>` call site).
5. **Build-tag variant collision.** `variantcollide.go:componentSignature`
   currently sorts props and treats reordered declarations as equivalent. It must
   compare **ordered** parameter names, types, variadic position, and reserved
   roles — reorder/rename are now contract changes.

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
  - omitting an **imported-unexported-typed** attribute → required-attribute
    diagnostic (no adapter); and **generic-inference failure** when a type param
    appears only in omitted args (→ explicit-type-arg diagnostic);
  - **authored order ≠ parameter order** (side effects, short-circuit) — proves
    once-only lexical evaluation;
  - **`(T, error)`** propagation through positional assembly;
  - **reserved-role collisions**, including authored-order composition of
    `attrs={}`, `attrs={{ }}`, ordinary fallthrough, and multiple explicit attrs
    contributors; rejection without a declared attrs role; rejection of
    non-bag attrs forms; and success of an ordinary `someAttrs={{ }}` prop;
  - **exact-case mismatch** and **parameter rename** (contract break);
  - **every supported callable kind** and each **rejected dynamic kind**
    (fail-closed);
  - the merged **attrs-bag signature family**, including defined-slice conversion,
    variadic expansion, alias handling, exact element identity, and the
    name-required diagnostic;
  - **signature-only** dependency/cache invalidation (a rename must bust caches);
  - a **direct-Go compile fixture** asserting the generated signature is exact.
- **structpages interop**: a differential test mounting a route tree and driving
  each page, asserting `Props() T → Page(x T)` wires (the regression that started
  this).
- **fmt corpus**: layout implications of declaring `children`/`attrs` params.
- **Sibling repos** (tree-sitter-gsx, vscode-gsx, gsxhq.github.io): reserved-name
  and any grammar implications.
