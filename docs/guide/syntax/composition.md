# Composition

Components compose with JSX-like tags while keeping Go values and package
boundaries.

## Calling components

Call a component with its name and pass declared props as attributes. Use a
self-closing tag when it has no nested content.

<!--@include: ./_generated/composition/010-components-props.md-->

Here `Page` passes strings, numbers, and the bare boolean prop `featured` to
`Card`. Package-qualified calls use the same form: `<ui.Button label="Save"/>`.

## Generic components

Declare Go type parameters after the component name. gsx infers type arguments
from the props supplied at each call site.

<!--@include: ./_generated/composition/070-generic-components.md-->

Both `Badge` calls infer `T` from `value`, so string and integer instances can
appear together in the same component body.

Directly interpolating `T` requires a renderable constraint; with `T any`,
convert or format it to a supported type, such as `{ value |> printf("%v") }`.

### Explicit type arguments

Use Go-style brackets when inference is ambiguous or when the call should state
the intended type directly.

<!--@include: ./_generated/composition/080-explicit-type-arguments.md-->

`<Price[float64] amount={4} currency="£"/>` keeps the amount in the `float64`
case even though an untyped `4` would otherwise infer `int`.

## Children `{children}`

Write `{children}` where a component should render the content between its
opening and closing tags.

<!--@include: ./_generated/composition/020-children.md-->

`Card` owns the surrounding markup and chooses the exact placement of the
caller's `<em>composed</em>` node. A bring-your-own props struct opts into nested
content with an explicit `Children gsx.Node` field; see
[Props](./props.md#bring-your-own-struct).

`children` — like `attrs` below and `ctx` — is reserved at the top level of a
component body. See [Reserved component variables](./props.md#reserved-variables).

## Named slots

Use `gsx.Node` props for additional content positions such as a header or
footer. Pass markup inline in the matching attribute.

<!--@include: ./_generated/composition/030-named-slots.md-->

Named slots and `{children}` can be used together: named slots receive explicit
attributes, while `{children}` receives the content inside the tag.

## Cross-file and cross-package calls

Components in different `.gsx` files of the same package call each other by
name. Imported components use their Go package qualifier.

<!--@include: ./_generated/composition/040-template-composition.md-->

Normal Go visibility applies: unexported components stay within their package,
and exported components can be called as `<ui.Button .../>` after importing
`ui`.

## Explicit attribute forwarding

Undeclared component attributes are accepted only when the component uses the
`attrs` bag. Put `{ attrs... }` on the element that should receive them.

<!--@include: ./_generated/composition/050-explicit-attribute-forwarding.md-->

The component can forward the bag, split selected values across elements, or
omit it entirely. See [Attributes — Spread](./attributes.md#spread-x-—-ordered)
for bag syntax and [Escaping](./escaping.md) for the trust rules applied at the
destination.

For JavaScript or CSS attributes with dynamic holes, accept ordinary wrapper
props and build the contextual literal on the final native element; see
[JavaScript](./javascript.md#attribute-local-javascript). A component that
intentionally accepts trusted code may instead declare a `gsx.RawJS` or
`gsx.RawCSS` prop and receive a
[braced contextual value](./javascript.md#contextual-literals-as-go-values).

### Precedence

The spread's source position controls scalar attributes:

```gsx
<button type="button" { attrs... } disabled class="button">Save</button>
```

- Before the spread, `type` is a default that the bag can override.
- After the spread, `disabled` is forced by the component.
- `class` and `style` compose instead of replacing one another.

### Derived bags

The spread can use any expression that produces `gsx.Attrs`:

```gsx
<input { attrs.Without("type")... }/>
<div { attrs.Merge(extra)... }>...</div>
<span { p.Attrs.Without("id")... }>Label</span>
```

## Forwarding through components

A component can pass its fallthrough bag into another component call. Each
component in the chain still chooses where the attributes finally land.

<!--@include: ./_generated/composition/090-forwarding-through-components.md-->

`SearchIcon` adds its default class, then forwards the outer caller's class and
ARIA label through `Icon` to the final `<span>`. To forward into a bring-your-own
component, put the bag in its explicit `Attrs` field and pass the props with a
[whole-struct splat](./props.md#whole-struct-splat).

## Method components

Declare a component as a method when several views share state held by a named
receiver. Inline params remain the per-call data.

<!--@include: ./_generated/composition/060-method-components.md-->

Call another method component through the receiver, as in
`<p.Grid sort={p.Sort}/>`. This keeps page-level state on `p` without threading
it through every component prop.
