# Styling

Use composable `class` and `style` values when an element has a fixed base plus
optional or caller-supplied parts.

## Compose classes

Write a `class={...}` list with always-on strings and `"name": condition`
entries. Included parts keep source order.

<!--@include: ./_generated/styling/010-composable-class.md-->

Expressions can contribute classes too:

```gsx
<button class={ "btn", sizeClass, "btn-disabled": disabled }>Save</button>
```

## Compose inline styles

Each entry in a `style={...}` list is a complete CSS declaration. A condition
after `:` includes that declaration only when it is true.

```gsx
<div style={
	"display: block",
	"color: " + accent,
	"opacity: 0": hidden,
}>...</div>
```

Use a CSS literal when a dynamic declaration is clearer as CSS text:

````gsx
<div style={ "display: none": hidden, css`width:@{width}px` }>...</div>
````

Dynamic CSS values are filtered for their CSS context. See
[Escaping](./escaping.md#javascript-and-css-contexts) for the safety rules and
trusted-value boundary.

## Merge forwarded class and style {#class-style-merging}

Scalar attributes from a spread keep the spread's source position. Forwarded
`class` and `style` are the exception: the component root's local parts merge
first and the caller's parts merge last, even when `{ attrs... }` appears before
the local `class` or `style`. The default class merger keeps the last occurrence
of an exact duplicate token. For style, the later value for the same property
wins.

For example, `<div { attrs... } class="card">` still puts `card` before the
caller's class tokens.

<!--@include: ./_generated/styling/030-class-style-merging.md-->

The example keeps both class tokens, replaces `color: red` with the caller's
`color: blue`, and adds the caller's `margin`. See [Attributes](./attributes.md)
for general spread ordering and precedence.

An interpolated class literal is another mergeable class value:

<!--@include: ./_generated/styling/040-interpolated-class-literal-merges-with-a-spread-bag.md-->

See [Attributes](./attributes.md#interpolating-attribute-literals) for `f`
literal syntax.

### Tailwind-aware class merging

Exact-token deduplication does not resolve conflicting Tailwind utilities such
as `px-4 px-8`. Configure a Tailwind-aware merger when your project needs those
semantics:

```toml
class_merger = "myapp/twcfg.Merge"
```

The configured symbol has the signature `func([]string) string`. See
[Configuration](../config.md#class_merger-tailwind-aware-class-merge-strategy)
for the contract and a `tailwind-merge-go` wrapper.

## Choose one value with `if` or `switch`

Conditional entries are additive. Use a value-form `if` when one of two values
should contribute:

```gsx
<button class={ "btn", if open { "btn-open" } else { "btn-closed" } }>...</button>
```

Use `switch` for several choices:

```gsx
<span class={
	"badge",
	switch tone {
	case "success": "badge-success"
	case "warning": "badge-warning"
	default: "badge-neutral"
	},
}>...</span>
```

The selected arm contributes one string. With no matching arm and no `else` or
`default`, it contributes nothing. The same forms work in `style={...}` lists,
where each arm returns a complete declaration.

## `<style>` blocks

Use a `<style>` block for component CSS. Interpolate Go values with `@{...}`.

<!--@include: ./_generated/styling/040-style-blocks.md-->

Interpolated values are CSS-filtered. See
[Escaping](./escaping.md#javascript-and-css-contexts) before passing trusted CSS.
