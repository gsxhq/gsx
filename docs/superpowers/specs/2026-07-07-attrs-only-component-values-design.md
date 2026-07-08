# Attrs-only component values — un-deferring `gsx.Component`

**Status:** design
**Date:** 2026-07-07
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

The 65 wrappers are boilerplate for exactly one reason (documented in the
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

## Proposal

Add a **4th props model** alongside byo / generated / nullary
(`docs/guide/syntax/props.md`'s three-row table): **attrs-only func value**. A
package-level identifier — `var` or `func`, doesn't matter which, since
resolution is by static type via `go/types`, same as the rest of tag
resolution (`composition.md`: "the generator resolves the tag through the Go
type system") — is tag-callable if its type is exactly one of:

```go
func(gsx.Attrs) gsx.Node
func(...gsx.Attr) gsx.Node
```

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
without `...`). One `types.Signature.Variadic()` check picks the emission
form — cheap.

The two shapes earn their keep on different axes:
- `func(gsx.Attrs) gsx.Node` matches the *only* documented attrs-bag type and
  gives the function body method access (`attrs.Has(...)`, `attrs.Merge(...)`)
  with no conversion.
- `func(...gsx.Attr) gsx.Node` gives the zero-attrs direct-call case
  (`HomeIcon()`) for free. The non-variadic form can't be called with zero
  arguments (`HomeIcon(nil)` is required), which matters because most icon
  call sites — both tag call sites and any plain-Go call sites outside
  `.gsx` files — pass no attrs at all.

### The one shape deliberately excluded

Bare unnamed `func([]gsx.Attr) gsx.Node` (non-variadic, unnamed slice) is
assignable to `gsx.Attrs` by the same rule and would be swept in "for free" by
a naive underlying-type check. Exclude it explicitly: require the *named* type
`gsx.Attrs` for the non-variadic form. It buys nothing over `gsx.Attrs` (same
data, worse readability, no method access without a conversion either) and
existing only as an assignability accident, not a deliberate spelling. A corpus
case should pin this as a rejection (falls through to the existing "assumed
prop fields" convention path, which then correctly fails to find a
`FooProps` — the identical failure mode as today, not a new error class).

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
(`depPropFacts`/`importedPropFacts`, `module_importer.go:324-391`). Exact hook
point (same-package sibling-`.go` resolution vs. the cross-package importer
path, and whether both need the check or just one shared point they both call
through) needs confirming during implementation — this spec fixes the
*signature contract and semantics*, not the precise call graph.

`internal/codegen/fieldmatch.go`'s `FieldMatcher`/`matchField` do not apply to
this model at all — there are no declared fields to match attrs against, by
design.

## Example: collapsing `ui/icons.gsx`

**Principle: this model never introduces a new way to *apply* attrs to
markup — only a new way to make an identifier *tag-callable* at the use
site.** Attrs still reach the `<svg>` element exactly the way they do today:
via `{ x... }` spread inside a real `component`-declared byo struct. Nothing
about class/style merge order, boolean-attr rendering, escaping, or
conditional-attr composition changes, because none of that code is touched —
it's the existing byo path, unmodified. An earlier draft of this example
hand-rolled the `<svg>` via raw `gsx.W(w)` writer calls; that was wrong for
exactly this reason (it also referenced a `Writer.Attrs(...)` method that
doesn't exist — `writer.go` has no bulk-attrs emit method, only
per-value primitives like `AttrValue`/`BoolAttr`). Corrected version below:

```gsx
// ui/icons.gsx — the one real component, unchanged shape from today's 65
component renderNamedIcon(name string, attrs gsx.Attrs) {
	<svg
		xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"
		fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
		aria-hidden="true"
		{ attrs... }
	>
		{ dsicon.RawIcon(name) }
	</svg>
}
```

```go
// ui/icons.go — the only new thing: a thin adapter making each icon tag-callable
func namedIcon(name string) func(gsx.Attrs) gsx.Node {
	return func(attrs gsx.Attrs) gsx.Node {
		return renderNamedIcon(name, attrs)
	}
}

var (
	HomeIcon   = namedIcon("house")
	SearchIcon = namedIcon("search")
	// ...63 more one-liners
)
```

`renderNamedIcon` is an ordinary byo component — single named-struct param,
`iconProps` declared in the same package — so it works with **zero compiler
changes**, today, exactly per `docs/guide/syntax/props.md`'s existing byo
row. The *only* capability this spec actually adds is making `HomeIcon`
(a `var` of type `func(gsx.Attrs) gsx.Node`) resolvable as a tag callee at
`<HomeIcon class="w-5 h-5"/>` call sites — everything downstream of that
(attrs merging, escaping, the `<svg>` markup) is already-shipped byo/spread
behavior. This also shrinks the scope of what "Where this hooks in" needs to
touch: no writer-level or attrs-merge code changes at all, purely the
tag-callee discriminator.

Collapses ~1450 lines (65 × ~22-line `component` bodies) to one shared
`iconProps`/`renderNamedIcon` pair plus 65 one-line `var` declarations. A
future tweak to the shared `<svg>` wrapper touches one place instead of 65.

## Scope boundaries

- Does not change the existing byo / generated / nullary discriminator or any
  existing corpus case for `component`-declared identifiers.
- Exactly two accepted signatures — `func(gsx.Attrs) gsx.Node` and
  `func(...gsx.Attr) gsx.Node` — not a general "anything assignable to
  `[]gsx.Attr`" rule, and no `(gsx.Node, error)` variants (see "Proposal"
  above). The bare unnamed `[]gsx.Attr` exclusion is deliberate and
  corpus-pinned.
- No `{children}` support for this model.
- No codegen-inserted conversion inside the callee body — `gsx.Attrs(attrs)`
  is an authoring idiom, documented, not synthesized.
- Supersedes the "Rejected" list in the deferred note only by adding the
  `gsx.Attrs` (named, non-variadic) shape as a second accepted spelling
  alongside the originally-proposed `type Component = func(...gsx.Attr)
  gsx.Node`; it does not revisit `Component any` or `Component[T]`, both still
  rejected for the reasons already recorded there.

## Testing

- **Corpus** (`internal/corpus/testdata/cases/attrs-only-components/` or
  similar): same-package `var Foo = factory(...)` for both accepted shapes; a
  cross-package/imported version to exercise the module-importer resolution
  path; the bare unnamed `[]gsx.Attr` rejection case; a
  children-supplied-but-unsupported error case; an attrs-merge-order case
  (bare + spread + conditional + ordered-literal together) pinning that they
  land in one bag in source order.
- **`one-learning-gsx` real-world check**: once implemented, collapse
  `ui/icons.gsx` per the example above and confirm `go build ./...` and
  `go test ./ui/...` are unaffected (mirrors the validation approach in
  `2026-07-06-byo-struct-fields-syntactic-design.md`).
