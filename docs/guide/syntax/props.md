# Props

Choose the props model from the component's declaration shape. No annotations
or configuration are needed.

## Choose a props model

| Model | Declaration | Use it when |
|---|---|---|
| Bring your own | `component Button(p Props)` | A bare, same-package named struct should define the full contract. |
| Generated | `component Card(title string, count int)` | Inline params are the clearest API. |
| Nullary | `component Divider()` | The component needs no call-site data. |
| Attrs-only value | `func(gsx.Attrs) gsx.Node` or a related accepted shape | A factory-produced value, such as an icon, only needs an attribute bag. |

Method receivers do not count as props. A nullary component can still opt into
`{children}` or `attrs` by using them in its body.

## Bring your own struct

When a component's sole non-receiver param is a bare name for a same-package
struct, call-site attributes map directly to that struct's fields. Qualified
types such as `ui.Props` and pointer types such as `*Props` use generated props.

<!--@include: ./_generated/props/010-bring-your-own-props.md-->

- `variant` maps to `Variant`; kebab names such as `full-width` map to
  `FullWidth`.
- `Children gsx.Node` receives nested content.
- `Attrs gsx.Attrs` receives unmatched attributes, which the component places
  explicitly with a [spread](./attributes.md#spread-x-—-ordered).

Both `Children` and `Attrs` are opt-in fields on a bring-your-own struct.

## Generated props

Inline params select generated props. A single scalar param and a list of
params use the same model.

<!--@include: ./_generated/props/020-props-heuristic.md-->

`Greeting(name string)` and `Card(title string, n int)` expose those params as
tag attributes. A component that uses `{children}` or `attrs` also accepts that
content or fallthrough bag. See [Composition](./composition.md#children-children)
for children and [explicit attribute forwarding](./composition.md#explicit-attribute-forwarding)
for fallthrough attributes.

## Whole-struct splat

When a props value is already assembled, pass the whole struct with
`{ value... }`.

<!--@include: ./_generated/props/030-whole-struct-splat.md-->

Whole-struct splat works for top-level and method components. It is
all-or-nothing: do not mix it with field attributes or children on the same
call. A `gsx.Attrs` expression uses the same surface syntax for an attribute
[spread](./attributes.md#spread-x-—-ordered); its type makes the intent clear.

## Advanced case: attrs-only component values {#attrs-only-component-values}

A package-level function or value can be called as a component tag when it has
one of these public shapes:

```go
func(gsx.Attrs) gsx.Node
func([]gsx.Attr) gsx.Node
func(...gsx.Attr) gsx.Node
```

Every call-site attribute enters that one bag. These values do not accept
children; use a declared component with a `Children` slot when nested content
is required.

This is useful for JSX-style component factories:

```gsx
func namedIcon(name string) func(...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node {
		return <svg class="size-5" { attrs... }>{name}</svg>
	}
}

var HomeIcon = namedIcon("house")

component Toolbar() {
	<HomeIcon class="text-blue" aria-label="Home"/>
}
```

The element spread follows the same ordering and escaping rules as every other
bag. See [Attributes — Spread](./attributes.md#spread-x-—-ordered) and
[Escaping](./escaping.md).

## Reserved component variables {#reserved-variables}

gsx owns three names at the top level of a component body:

| Name | Available in |
|---|---|
| `ctx` | every component render |
| `children` | generated and nullary components that place [`{children}`](./composition.md#children-children) |
| `attrs` | generated and nullary components that freely use the [fallthrough bag](./composition.md#explicit-attribute-forwarding) |

A bring-your-own props component uses its explicit `p.Children` and `p.Attrs`
fields instead.

Do not declare these names as component parameters or receivers, or with a
top-level `:=`, `var`, or `const`. Nested scopes may shadow them like ordinary
Go—for example, a range variable or function-literal parameter.
