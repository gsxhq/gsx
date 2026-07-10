# Attrs-only component values — un-deferring `gsx.Component`

**Status:** design
**Date:** 2026-07-07 (revised 2026-07-10 after probe review: recognition made
probe-based on the existing `_gsxcompsig` emit≡probe loop, worked example
corrected, class-merge fidelity addressed, forwarding alternative recorded;
further revised 2026-07-10: the unnamed `[]gsx.Attr` shape un-excluded and
accepted, with a user-defined named slice type still rejected — see "The
unnamed-slice shape — initially excluded, then accepted")
**Follow-up to:** `2026-07-06-element-literals-design.md`, "Deferred: component values (`gsx.Component`)"

## Why this was parked, and why that reasoning breaks down

The element-literals spec parked component values with this reasoning: "the
nav-icon driver needs only a *constant* class, so a baked element literal
(`icon: <HomeIcon class="w-5 h-5"/>`) already covers it. Component values only
earn their keep when the attrs must vary per render site, which is rare."

`one-learning-gsx/ui/icons.gsx` is the counter-example. It has ~65 near-identical
`component XIcon() { <svg xmlns="..." ... { attrs... }> {dsicon.RawIcon("x")}
</svg> }` blocks — one per Lucide icon — that exist *specifically* because attrs
(mainly `class`, for per-site sizing: `class="w-5 h-5"` in a nav rail,
`class="h-3 w-3"` inline in a table cell, `class="animate-spin h-4 w-4"` on a
loading indicator) vary at essentially every call site. This is the case the
deferral called rare. A baked element literal can't serve it: attrs are fixed at
the literal, so `icon: <HomeIcon class="w-5 h-5"/>` stored once can never be
re-rendered at a different size elsewhere.

The wrappers (62 on today's tree, 1191 lines) are boilerplate for exactly one reason (documented in the
`icons.gsx` file header): `{ attrs... }` fallthrough is only recognized on a
plain element, not when spread onto a *nested component call* — so each wrapper
inlines the whole `<svg>` rather than delegating to the shared
`ds/icon.Icon(name string)` component. Component values remove the need for the
wrapper to be a `component` declaration at all.

## Empirical correction to the deferred note

The deferred section claimed: "Interim, zero-compiler-change: the binding
closure + a userland `type Component = func(...gsx.Attr) gsx.Node` alias already
solve the nav case today." This is not true as of `gsx generate` on today's
`main` (verified in an isolated worktree, not the live `one-learning-gsx` tree):

```go
type zzComponent = func(...gsx.Attr) gsx.Node

func zzNamedThing(label string) zzComponent {
	return func(attrs ...gsx.Attr) gsx.Node { /* ... */ }
}

var ZZThing = zzNamedThing("hi")
```
```gsx
component ZZProbe() {
	<ZZThing class="w-5 h-5"/>
}
```
fails with `error: undefined: ZZThingProps`. The convention-component resolver
unconditionally expects a `<Name>Props` struct for any tag callee it doesn't
recognize as `component`-declared; it does not special-case a bare-attrs func
signature. This is real, currently-unimplemented codegen work — not a doc gap.
(Under the probe-based recognition rule below, this exact alias spelling
*becomes* valid — `go/types` resolves through the alias — so the deferred
note's claim ends up true once this spec ships, rather than being permanently
superseded.)

## Proposal

Add a **4th props model** alongside byo / generated / nullary
(`docs/guide/syntax/props.md`'s three-row table): **attrs-only func value**. A
package-level identifier — `var` or `func` — is tag-callable if its type is
one of:

```go
func(gsx.Attrs) gsx.Node
func([]gsx.Attr) gsx.Node
func(...gsx.Attr) gsx.Node
```

(The middle spelling — unnamed `[]gsx.Attr`, non-variadic — was initially
excluded and later accepted; see "The unnamed-slice shape — initially
excluded, then accepted" below for why.)

### Recognition rule — probe-based, real `go/types` resolution

An earlier draft of this revision proposed a syntactic closed list of
declaration shapes, on the grounds that the call-shape decision is made by
the syntactic prop-facts layer and `go/types` "runs too late" to inform it.
That was wrong about the codebase: gsx already has an **emit ≡ probe**
feedback loop for exactly this problem. The analysis pass emits
`_gsxcompsig(F)` probes into the type-checked skeleton
(`analyze.go:1276-1295`), the harvest maps each probe's real `types.Type`
back onto the tag elements (`analyze.go:2049`, `sigByName` →
`resolved[el]`), and the emitter branches on the harvested
`*types.Signature` (`emit.go:3741`). Today this resolves hand-written
same-package **nullary** funcs (`isBareCallCandidate`). This model extends
the same loop; recognition really is "the identifier's static type is one of
the accepted signatures", checked by `go/types` on the skeleton — no new
syntactic machinery, no `packages.Load`, no closed list.

**Gate (who gets probed).** The probe pass must decide *before* type-check
whether to emit the assumed-`<Name>Props` literal probe (the convention
path's generate-time attr checking, which must not regress) or a
`_gsxcompsig` probe. The gate is: a component tag that is not
`component`-declared, not byo, not a method, not a bare-call nullary
candidate, **and whose `<Name>Props` struct is not discoverable** in the
callee package's already-parsed facts (same-package: `gsxStructDecls` +
sibling-`.go` enumeration; dotted: `depPropFacts`). That region is
*guaranteed to fail* today (`undefined: <Name>Props`), so gating it onto the
probe is a pure capability addition — no existing working code changes
behavior, and convention components with real props structs keep their
literal probes and generate-time diagnostics untouched.

**Match (what the harvest accepts).** The harvested type's underlying
`*types.Signature` must have exactly one parameter and one result, the
result the named `gsx.Node`, and the parameter either:

- variadic `...gsx.Attr` (`sig.Variadic()` with element the named `gsx.Attr`), or
- non-variadic and **assignable from `gsx.Attrs`** — either the named
  `gsx.Attrs` itself, or the unnamed slice `[]gsx.Attr` (identical
  underlying type, one side unnamed — the same rule Go's assignability
  check applies).

The boundary is assignability, not a fixed enumeration: a user-defined named
type sharing that underlying (`type MyAttrs []gsx.Attr`) is deliberately
**not** matched, because `gsx.Attrs` is not assignable to a second distinct
named type (two named types are never mutually assignable regardless of a
shared underlying type) — matching it would emit an `F(bag)` call that
fails to compile. Named-type identity (`gsx.Attrs`/`gsx.Attr`/`gsx.Node`) is
checked against the gsx package path throughout, so:

- any initializer works — `var HomeIcon = namedIcon("house")`, chained
  factories, conditionals — because `go/types` infers the var's type;
- **type aliases work for free** (`type Component = func(...gsx.Attr)
  gsx.Node`; aliases are transparent to `go/types`), un-rejecting the
  deferred note's original spelling; an alias *of* `[]gsx.Attr` is likewise
  the unnamed slice and is accepted;
- a user-defined **named** slice type is excluded precisely (it is not
  assignable from `gsx.Attrs`), with no spelling analysis — the underlying
  shape is irrelevant, only the named-vs-unnamed assignability fact is.

**Probed-but-no-match is a new, required diagnostic.** For gated tags the
`_gsxcompsig` probe *replaces* the assumed-props literal probe, so the old
generate-time `undefined: <Name>Props` no longer surfaces from the skeleton.
A probed identifier whose type matches none of the accepted shapes therefore
gets a clean positioned diagnostic at generate time ("`<X>` is not
tag-callable: its type is `T`, not `func(gsx.Attrs) gsx.Node`,
`func([]gsx.Attr) gsx.Node`, or `func(...gsx.Attr) gsx.Node`, and no
`XProps` struct was found") — strictly better than
today's raw `undefined: XProps`, and required so diagnostics don't regress
to `go build` time. A gated identifier that doesn't exist at all still
surfaces as `undefined: X` from the probe itself, positioned at the tag.

No `(gsx.Node, error)` variants. `component`-declared functions never return
an error — always single-return `gsx.Node` — so this would be introducing a
capability with no other consumer. It's also redundant: `gsx.Node` is already
`gsx.Func(func(ctx, w io.Writer) error {...})`, i.e. rendering itself is
already an error-carrying operation. A fallible step inside the icon's
rendering (e.g. `RawIcon(name)` on a bad name) is already handled today,
ordinarily, by gsx's existing `(T, error)` interpolation auto-unwrap —
`{ dsicon.RawIcon(p.Name) }` inside the real component in the worked example
below — not by the outer `func(gsx.Attrs) gsx.Node` adapter, which never even
sees that error. The only case
`(gsx.Node, error)` would justify is failing *before* the Node is even
constructed, synchronously — nothing here needs that, and there's no existing
plumbing for propagating an error returned by a *tag callee itself* (as
opposed to an *attribute expression value*, which is what `props.md`'s
auto-unwrap paragraph actually covers) up through `Render`. Building that for
zero driving use cases is exactly the unforced complexity this model should
avoid.

At the tag site, **every** call-site attribute — bare fallthrough, `{ x... }`
spreads, conditional attrs, the `attrs={{ ... }}` ordered literal — merges into
one `gsx.Attrs` bag in source order, using the *existing* bag-assembly code
(the same machinery that already builds the synthesized `Attrs gsx.Attrs` field
for byo/generated components — see `docs/guide/syntax/attributes.md` §"Spread
`{ x... }`" and §"Targeting the synthesized attrs bag"). There is no field
matching step at all for this model — no struct, so nothing to match against;
every attribute is fallthrough. `<HomeIcon class="w-5 h-5"/>` compiles to
`HomeIcon(gsx.Attrs{{Key: "class", Value: "w-5 h-5"}})` (non-variadic) or
`HomeIcon(gsx.Attrs{{Key: "class", Value: "w-5 h-5"}}...)` (variadic).

### Why both shapes, not one

`gsx.Attrs` is `[]gsx.Attr` (`attrs.go:14,30`) and a variadic parameter
collapses to the unnamed `[]gsx.Attr` — mutually assignable to `gsx.Attrs`
under Go's assignability rule (identical underlying type, one side unnamed), so
detecting either shape is one check on the underlying element type, not two
disjoint code paths. What *does* differ is call-site emission: a
variadic-declared identifier needs `Ident(bag...)`; a `gsx.Attrs`-declared one
needs `Ident(bag)` (Go does not allow passing a slice to a variadic parameter
without `...`). One `types.Signature.Variadic()` check on the harvested
signature picks the emission form — cheap.

The two shapes earn their keep on different axes:
- `func(gsx.Attrs) gsx.Node` matches the *only* documented attrs-bag type and
  gives the function body method access (`attrs.Has(...)`, `attrs.Merge(...)`)
  with no conversion.
- `func(...gsx.Attr) gsx.Node` gives the zero-attrs direct-call case
  (`HomeIcon()`) for free. The non-variadic form can't be called with zero
  arguments (`HomeIcon(nil)` is required), which matters because most icon
  call sites — both tag call sites and any plain-Go call sites outside
  `.gsx` files — pass no attrs at all.

### The unnamed-slice shape — initially excluded, then accepted

Bare unnamed `func([]gsx.Attr) gsx.Node` (non-variadic, unnamed slice) is
assignable to `gsx.Attrs` by the same rule the variadic shape already relies
on ("Why both shapes, not one" above). An earlier revision of this spec
excluded it explicitly, requiring the harvest's non-variadic match to be the
*named* type `gsx.Attrs` specifically (a `*types.Named` whose object lives in
the gsx package), never a structural underlying-type comparison — reasoning
that it "buys nothing over `gsx.Attrs`" and "exists only as an
assignability accident, not a deliberate spelling."

That exclusion was a holdover from an earlier, abandoned design for this
same feature: a *syntactic* closed list of declaration shapes, where
"spelling discipline" was a meaningful concept because the matcher worked on
source text. That design was rejected in favor of the probe-based
`go/types` recognition rule actually shipped above ("Recognition rule —
probe-based, real `go/types` resolution") — and under real type-checking,
spellings don't exist, only types do. The variadic shape already hands the
component body an *unnamed* `[]gsx.Attr` (the parameter collapses; see "Why
both shapes, not one"), so "the unnamed spelling is worse ergonomically"
was never true of the feature as shipped — it was only ever true of the
named non-variadic declaration, which coexists with the unnamed one anyway.
Once the actual boundary is stated as an assignability rule instead of a
named-vs-unnamed spelling preference, excluding the unnamed slice took *more*
code than accepting it (an extra special case carved out of the general
rule), for a benefit ("discourage a worse spelling") the type system already
provides for free, since nothing stops an author preferring `gsx.Attrs` for
its method access regardless of what the matcher accepts.

**The real, still-enforced boundary:** the non-variadic parameter must be
*assignable from* `gsx.Attrs` — the named `gsx.Attrs` itself, or the unnamed
`[]gsx.Attr` (one side unnamed, identical underlying — ordinary Go
assignability). A **user-defined named** type sharing that underlying
(`type MyAttrs []gsx.Attr`) remains rejected, and this is not a stylistic
choice: `gsx.Attrs` is not assignable to a second, distinct named type (Go
never treats two named types as mutually assignable no matter how their
underlying types relate), so matching `MyAttrs` would make the emitter emit
`F(bag)` where `bag` has type `gsx.Attrs` against a parameter of type
`MyAttrs` — a call that fails to compile. This is corpus-pinned: an
acceptance case for the unnamed slice
(`internal/corpus/testdata/cases/attrsonly/unnamed_slice.txtar`) and a
rejection case for the user-named slice type
(`internal/corpus/testdata/cases/attrsonly/reject_named_slice_type.txtar`),
the latter still gated onto the probe (no `FooProps` struct exists) and
still hitting the "not tag-callable" diagnostic with the offending type
spelled out.

### `{children}` is not supported

`gsx.Attrs` has no field to receive child content the way a synthesized props
struct has a `Children gsx.Node` field. A tag using this model with content
between open/close tags is a compile-time error with a clear diagnostic
("component values do not support children — declare a Children slot on a
named-struct component instead"). Fine for the icon use case; every icon tag
is self-closing.

### Function-body ergonomics: the variadic conversion

A variadic-declared function receives `attrs []gsx.Attr` (the *unnamed*
collapse), not `gsx.Attrs` — so `.Has()` / `.Merge()` / other `gsx.Attrs`
methods aren't directly available. Convention: the author writes

```go
func(attrs ...gsx.Attr) gsx.Node {
	attrs := gsx.Attrs(attrs)
	// ... .Has, .Merge, etc. now available
}
```

as the first line. This is a named-type conversion between two types sharing
an underlying slice type — the runtime representation (pointer/len/cap) is
identical, so this is a header copy at worst (already what happens whenever a
slice is passed anywhere), not a data copy or allocation; there's no
"optimizing away" required because there's nothing to eliminate. No benchmark
or validation gate needed for this specific decision. This is a userland
authoring idiom, not something gsx codegen inserts — gsx only compiles the
*call site* into a function call; the callee's body is ordinary
author-controlled Go, untouched by gsx.

### `ordered-attrs` literal and merge order

`attrs={{ "data-x": "1" }}` continues to merge last regardless of source
position, same as the existing rule (`attributes.md` §"Targeting the
synthesized attrs bag"). Since every attribute on this model's tag is
fallthrough (no declared fields to conflict with), there's no special case
here beyond reusing the existing merge-order logic unchanged.

## Where this hooks in

The per-package props discriminator (`internal/codegen/analyze.go:70`
`componentPropFieldsFor`) decides byo / generated / nullary for
`component`-declared identifiers. This model is orthogonal to that — it's a
new outcome of *tag-callee resolution* for identifiers that are **not**
`component`-declared, i.e. it extends the same fallback path documented in
`composition.md` under "For components gsx cannot analyze... call-site
identifier attrs are assumed to be prop fields" and the cross-package
resolution machinery in `internal/codegen/module_importer.go`
(`depPropFacts`/`importedPropFacts`, `module_importer.go:324-391`). Concretely
(see "Recognition rule"): the *gate* consults the already-parsed struct facts
(same-package and `depPropFacts`) — no new file reads, no `packages.Load`;
the *probe* rides the existing `_gsxcompsig` emission at
`analyze.go:1276-1295`; the *harvest* extends `sigByName` at
`analyze.go:2049` (which today only accepts `*ast.Ident` probe args and must
also accept `*ast.SelectorExpr` for dotted tags, keyed by the full tag
string); the *emitter* adds a branch beside the existing
`resolved[el].(*types.Signature)` nullary check at `emit.go:3741`. The bag
expression reuses `childPropsLiteral`'s fallthrough assembly (`bag` /
`mergeChain` / `attrsLitIdx`) — every attr unmatched — rather than new merge
code.

`internal/codegen/fieldmatch.go`'s `FieldMatcher`/`matchField` do not apply to
this model at all — there are no declared fields to match attrs against, by
design.

## Example: collapsing `ui/icons.gsx`

**Principle: this model never introduces a new way to *apply* attrs to
markup — only a new way to make an identifier *tag-callable* at the use
site.** Attrs still reach the `<svg>` element exactly the way they do today:
via `{ x... }` spread inside a real `component`-declared byo struct. Nothing
about boolean-attr rendering, escaping, or conditional-attr composition
changes, because none of that code is touched — it's the existing byo path,
unmodified. An earlier draft of this example hand-rolled the `<svg>` via raw
`gsx.W(w)` writer calls; that was wrong for exactly this reason (it also
referenced a `Writer.Attrs(...)` method that doesn't exist — `writer.go` has
no bulk-attrs emit method, only per-value primitives like
`AttrValue`/`BoolAttr`). A second earlier draft wrote
`component renderNamedIcon(name string, attrs gsx.Attrs)` and called it byo —
two inline params are the *generated* model (`props.md`'s discriminator),
whose emitted signature is `func renderNamedIcon(p renderNamedIconProps)
gsx.Node`, so the adapter's plain-Go call `renderNamedIcon(name, attrs)`
would not have compiled. The byo form with an explicit `iconProps` is
required precisely because the adapter is plain Go calling the component
function directly. Corrected version:

```gsx
// ui/icons.gsx — the one real component, replacing today's 65
type iconProps struct {
	Name  string
	Attrs gsx.Attrs
}

component renderNamedIcon(p iconProps) {
	<svg
		xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"
		fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
		aria-hidden="true"
		{ gsx.Attrs{{Key: "class", Value: "w-5 h-5"}}.Merge(p.Attrs)... }
	>
		{ dsicon.RawIcon(p.Name) }
	</svg>
}
```

```go
// ui/icons.go — the only new thing: a thin adapter making each icon tag-callable
func namedIcon(name string) func(gsx.Attrs) gsx.Node {
	return func(attrs gsx.Attrs) gsx.Node {
		return renderNamedIcon(iconProps{Name: name, Attrs: attrs})
	}
}

var (
	HomeIcon   = namedIcon("house")
	SearchIcon = namedIcon("search")
	// ...60 more one-liners
)
```

`renderNamedIcon` is an ordinary byo component — single named-struct param,
`iconProps` declared in the same package — so it works with **zero compiler
changes**, today, exactly per `docs/guide/syntax/props.md`'s existing byo
row. The *only* capability this spec actually adds is making `HomeIcon`
(recognized via shape 3 of the recognition rule) resolvable as a tag callee
at `<HomeIcon class="w-5 h-5"/>` call sites — everything downstream of that
(attrs merging, escaping, the `<svg>` markup) is already-shipped byo/spread
behavior. This also shrinks the scope of what "Where this hooks in" needs to
touch: no writer-level or attrs-merge code changes at all, purely the
tag-callee discriminator.

### Class-merge fidelity — why the default class moved into the bag

Probe-verified on today's `main`: the byo path does **not** give `{ p.Attrs... }`
the caller-wins/class-merge machinery that the implicit `{ attrs... }` bag gets
on the generated/nullary path. A byo body cannot even reference the implicit
`attrs` identifier (it is unbound — `error: undefined: attrs`); it must spread
the declared field, `{ p.Attrs... }`, and that spread is emitted inline
(`bagSpreadIndex` only recognizes expressions referencing the bound `attrs`
token). Consequence: a static `class="w-5 h-5"` on the `<svg>` alongside a
caller class arriving via `{ p.Attrs... }` renders a **duplicate `class`
attribute** — `<svg class="w-5 h-5" class="h-3 w-3">` — and HTML parsers keep
only the first, silently dropping the caller's override. That would regress
the exact per-site-sizing behavior this feature exists to serve.

Hence the example keeps no static `class` on the element and instead merges
the default *inside the bag*: `gsx.Attrs{{Key: "class", Value: "w-5
h-5"}}.Merge(p.Attrs)`. `Attrs.Merge` is class/style-aware (`attrs.go:126`:
class/style values concatenate onto the first such pair; scalars are
last-wins), so exactly one `class` attribute is emitted, default tokens
first, caller tokens last.

One remaining fidelity delta, stated rather than papered over: `Attrs.Merge`
*concatenates*; it does not apply the project's configured `class_merger`
(that runs only at codegen-emitted implicit-bag class sites).
`one-learning-gsx` configures `class_merger =
"…/ds/twcfg.Merge"` (Tailwind-aware), so today `w-5 h-5` + `h-3 w-3`
collapses to the caller's sizing; under plain concatenation both token sets
are present and stylesheet order decides. For Tailwind sizing conflicts that
is not equivalent. The collapse therefore applies the merger explicitly in
the one shared body where fidelity matters —
`class={twcfg.Merge([]string{"w-5 h-5", p.Attrs.Class()})}` plus
`{ p.Attrs.Without("class")... }` — one place instead of 65, still zero
compiler changes. (Whether byo `{ p.Attrs... }` spreads should get
forwarding-position merge treatment like derived `attrs` bags do is a real
question, but a separate one: it changes existing byo semantics and deserves
its own spec. This one neither depends on nor blocks it.)

Validated against the real tree (2026-07-10, throwaway worktree, full
`gsx generate` + `go build` + `go test ./ui/...` + a 16-variant render-
equivalence harness — all pass): two refinements over the sketch above.
The per-icon defaults are NOT uniform (four default-class shapes plus four
class-less icons), so the adapter takes the default as a parameter
(`namedIconClass(name, class string)`); and an in-body dynamic
`class={twcfg.Merge(…)}` renders `class=""` for the class-less icons (gsx
does not omit an empty dynamic class attribute), so the merge moves into
the adapter and the body guards with
`{ if p.Class != "" { class={p.Class} } }`. Fidelity is then exact:
16/16 rendered variants byte-equivalent modulo attribute order. Measured
collapse: 1191 lines → 134 (−88.8%).

Collapses ~1190 lines (62 × ~19-line `component` bodies, measured on the live
file) to one shared `iconProps`/`renderNamedIcon` pair plus 62 one-line `var`
declarations. A future tweak to the shared `<svg>` wrapper touches one place
instead of 62.

## Alternative considered: fallthrough forwarding through component calls

The `icons.gsx` file header names the root cause of the boilerplate:
`{ attrs... }` fallthrough is only recognized on plain elements, not when
spread onto a *nested component call*. Probe-verified still true on today's
`main`: `<Inner { attrs... }/>` inside a component body fails with
`error: undefined: attrs`. Fixing THAT — letting a component forward its
implicit bag into a callee's synthesized bag — is the competing design for
the icon case; each wrapper would collapse to

```gsx
component SearchIcon() { <dsicon.Icon name="search" class="w-5 h-5" { attrs... }/> }
```

Why this spec chooses component values rather than forwarding:

- Forwarding still leaves 62 `component` declarations (~3-4 lines each);
  component values leave 62 one-line `var`s and one shared component.
- Component values are callable from plain Go (`HomeIcon()` /
  `HomeIcon(nil)`), not just from tags — icons get used outside `.gsx` files.
- Forwarding-through-calls is its own semantic feature with its own
  merge-order questions (where the caller's bag joins the callee's, whose
  `class_merger` site applies, how cond-attrs interleave) and deserves its
  own spec.

The two are complementary, not exclusive: forwarding closes a genuine
composability hole that will bite beyond icons, and stays open as follow-up
work. Nothing in this spec blocks or depends on it.

## Scope boundaries

- Does not change the existing byo / generated / nullary discriminator or any
  existing corpus case for `component`-declared identifiers.
- Three accepted param spellings — named `gsx.Attrs`, unnamed `[]gsx.Attr`,
  and variadic `...gsx.Attr` — governed by one rule, "assignable from
  `gsx.Attrs`" (plus variadic), not a fixed enumeration and not a general
  "anything with the same underlying type" rule: a user-defined named slice
  type (`type MyAttrs []gsx.Attr`) stays rejected because `gsx.Attrs` is not
  assignable to it (see "The unnamed-slice shape — initially excluded, then
  accepted" above). No `(gsx.Node, error)` variants (see "Proposal" above).
  Both the acceptance (unnamed slice) and the rejection (user-named slice
  type) are corpus-pinned.
- **Package-level identifiers only**, plain (`<HomeIcon/>`) or
  package-qualified (`<ui.HomeIcon/>`). Struct fields, locals, and params
  (`<item.Icon/>` where `Icon` is a field of type `func(gsx.Attrs) gsx.Node`)
  are NOT tag-callable in this model: a dotted tag's gate resolves the
  qualifier as a package import, so `item.Icon` never reaches the probe —
  corpus-pinned rejection. The element-literals spec's nav-config-struct case
  stays answered by baked element literals, not by this.
- Recognition is the probe-based rule above. Because `go/types` decides, the
  deferred note's `type Component = func(...gsx.Attr) gsx.Node` alias
  spelling is **recognized** (aliases are transparent); `Component any` and
  `Component[T]` remain rejected for the reasons already recorded there.
  Clarification: an *instantiated* generic named type used as a value — e.g.
  `type C[T any] func(...gsx.Attr) gsx.Node` with `var Y C[int] = …` — IS
  recognized, because its static type's substituted underlying is exactly the
  accepted shape (the `Component[T]` rejection concerns a generic API design,
  not concrete values); an *uninstantiated* generic is not expressible as a
  value and so stays rejected naturally.
- No `{children}` support for this model.
- No codegen-inserted conversion inside the callee body — `gsx.Attrs(attrs)`
  is an authoring idiom, documented, not synthesized.
- The "not tag-callable" diagnostic for a probed identifier whose type
  matches neither signature is **part of v1** (see "Recognition rule" — it
  replaces the skeleton's `undefined: <Name>Props` for gated tags, so
  omitting it would regress generate-time diagnostics to `go build` time).

## Deliverables beyond codegen

- **Docs:** `props.md` three-row table grows a fourth row; `composition.md`
  gains the tag-callable-value paragraph; `attributes.md` cross-references
  the everything-is-fallthrough merge behavior.
- **LSP:** go-to-definition + hover for the new tag-callee kind (the
  two-bridge wiring recipe from the nav-matrix work, PR #28). If deferred,
  it goes in `docs/ROADMAP.md`'s known-gaps list, not silently.
- **No surface syntax changes** — capitalized/qualified tags already parse —
  so `tree-sitter-gsx`, `vscode-gsx`, the website CodeMirror grammar, and
  `gsx fmt` are unaffected. State this in the PR rather than leaving it
  implicit.

## Testing

- **Corpus** (`internal/corpus/testdata/cases/attrs-only-components/` or
  similar), acceptance cases: direct func decl, factory-initialized `var`
  (`var HomeIcon = namedIcon("house")`), and type-alias spelling
  (`type Component = func(...gsx.Attr) gsx.Node`), covering both accepted
  signatures; a cross-package/imported version to exercise the dotted-tag
  probe + `SelectorExpr` harvest; zero-attr tag call for both signatures
  (pinning `Ident(nil)`/`Ident()` emission); an attrs-merge-order case
  (bare + spread + conditional + ordered-literal together) pinning that they
  land in one bag in source order.
- **Corpus rejection cases** (each pinned with its diagnostic): a
  user-defined named slice type (`type MyAttrs []gsx.Attr`)-typed var and an
  extra-`error`-result func — both gated onto the probe, both hitting the
  new "not tag-callable" diagnostic; a non-package-level callee
  (`<item.Icon/>` struct field, never gated); a children-supplied-but-
  unsupported error case with its diagnostic; an undefined-identifier tag
  pinning the positioned `undefined: X` from the probe. The bare unnamed
  `func([]gsx.Attr) gsx.Node`-typed var moved to an **acceptance** case (it
  is now a matched shape, not a rejection).
- **Worked-example fidelity case**: a byo component with a
  default-class-in-bag `Merge` spread (the corrected `renderNamedIcon`
  pattern) pinning that exactly ONE `class` attribute renders, default
  tokens first — the duplicate-class regression this spec's review caught.
- **`one-learning-gsx` real-world check**: once implemented, collapse
  `ui/icons.gsx` per the example above (including the explicit `twcfg.Merge`
  class site) and confirm `go build ./...` and `go test ./ui/...` are
  unaffected, plus a visual spot-check that per-site sizing overrides still
  win (mirrors the validation approach in
  `2026-07-06-byo-struct-fields-syntactic-design.md`).
