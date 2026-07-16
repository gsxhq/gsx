# Component signatures

A component's authored parameter list is its Go signature and its markup
contract. gsx emits no props wrapper type.

## Choose a signature shape

| Shape | Declaration | Use it when |
|---|---|---|
| Loose parameters | `component Card(title string, count int)` | The component has a small, direct API. |
| Options value | `component Panel(opts PanelOptions)` | A long-lived or high-arity Go API benefits from an author-owned struct. |
| Nullary | `component Divider()` | The component needs no call-site data. |
| Callable value | `func(attrs gsx.Attrs) gsx.Node` | A package function or factory-produced value should be tag-callable. |

Method receivers are not component inputs. Parameters keep their authored order
for direct Go callers; markup binds ordinary parameters by exact name.

<!--@include: ./_generated/props/010-verbatim-component-signature.md-->

`Button` is emitted as `func Button(variant string, children gsx.Node, attrs
gsx.Attrs) gsx.Node`. Go callers pass those values directly.

## Exact-name inputs

An ordinary markup attribute fills a parameter only when its name exactly
matches the authored identifier. Matching is case-sensitive and does not
convert kebab names or struct fields.

<!--@include: ./_generated/props/020-signature-shapes.md-->

- `<Greeting name="Ann"/>` fills `name`.
- `<Panel p={Props{Title: "P"}}/>` passes one ordinary composite value.
- An ordinary parameter can be filled at most once on a call.
- `title` does not address a field inside `p`; declare `title` as a separate
  parameter if callers should fill it separately.
- `data-*`, `aria-*`, `hx-*`, and other non-identifier names always contribute
  to `attrs`.

Omitted ordinary parameters receive their Go zero value when gsx can express it
at the call site. An opaque cross-package type whose zero cannot be named must be
supplied explicitly; gsx reports a positioned required-input diagnostic.

## Author-owned options structs

Use an ordinary struct parameter when keyed Go construction is valuable:

```gsx
type CardOptions struct {
	Title    string
	Featured bool
}

component Card(opts CardOptions) {
	<article class={ "featured": opts.Featured }>{opts.Title}</article>
}

component Page() {
	<Card opts={CardOptions{Title: "News", Featured: true}}/>
}
```

The struct is entirely application-owned. gsx does not inspect its fields or
derive a second markup API from them.

## Forward values explicitly

Pass an assembled value through the target parameter's exact name.

<!--@include: ./_generated/props/030-explicit-parameter-forwarding.md-->

`<p.Content pd={pd}/>` is ordinary named input binding. `{bag...}` on a
component always means an [attrs contributor](./attributes.md#spread-x-—-ordered),
never struct destructuring.

## Tag-callable Go values

A package-level function or value is tag-callable when gsx can resolve a
concrete function signature returning `gsx.Node`. Parameter names are part of
the markup contract, including the reserved `attrs` role:

```gsx
package views

import "github.com/gsxhq/gsx"

component icon(name string, attrs gsx.Attrs) {
	<span class="icon" data-name={name} {attrs...}>i</span>
}

func namedIcon(name string) func(attrs ...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node {
		return icon(name, gsx.Attrs(attrs))
	}
}

var HomeIcon = namedIcon("house")

component Toolbar() {
	<HomeIcon class="text-blue" aria-label="Home"/>
}
```

The supported `attrs` types are `gsx.Attrs`, `[]gsx.Attr`, a defined slice with
underlying type `[]gsx.Attr`, and `...gsx.Attr`. A differently named attrs-shaped
parameter is an ordinary input; a non-reserved variadic is Go-callable but not
markup-bindable. Since Go allows only one final variadic parameter, use
non-variadic `attrs gsx.Attrs` when a signature also has variadic `children`.

## Reserved component inputs {#reserved-variables}

| Name | Role |
|---|---|
| `ctx` | Ambient render context; it is not declared as a component parameter. |
| `children` | Body input; declare `children gsx.Node` or `children ...gsx.Node`. |
| `attrs` | Ordered fallthrough input; declare one of the supported attrs-bag types. |

`children` and `attrs` are special only in lowercase. Parameters named `Children`
or `Attrs` are ordinary exact-name inputs. A component without `children` rejects
a non-empty body, and a component without `attrs` rejects every unmatched
attribute or attrs contributor.

Names beginning `_gsx` are reserved for generated implementation details.
