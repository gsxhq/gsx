# Styling

gsx supports composable `class` and `style` attributes. Both use a `{ }` list
whose entries are always-on strings or `"value": cond` toggles. When a component
places `{ attrs... }`, caller-supplied class and style merge at that position.

## Composable class

The `class` attribute accepts a composable list instead of a plain string. Each entry is either an always-on class string or a `"name": cond` pair that is included only when `cond` is true.

```gsx
class={ "base", "modifier": condition }
```

Entries are evaluated at render time. On-entries are collected into the final
`class` value. When multiple class sources are merged, the default merge strategy
(`DefaultClassMerge`) deduplicates tokens and keeps the last occurrence.

<!--@include: ./_generated/styling/010-composable-class.md-->

Multiple always-on strings and any number of conditional pairs can appear in the same list. The list renders to a single `class="…"` attribute containing only the tokens whose conditions were true.

## Inline style composition

The `style` attribute has a parallel composable form. Each entry is a complete
CSS declaration string — optionally conditional. Static declarations, dynamic
declarations, and independent guards can be mixed:

```gsx
style={
	"display: block",
	"color: " + accent,
	"opacity: 0": hidden,
}
```

Parts evaluate strictly from left to right. On-parts are joined with `"; "` into
a single `style="…"` attribute value. String literal entries are trusted as-is;
entries containing Go expressions are CSS-sanitized at render time: values that
carry risky tokens (such as `(` or `/`) collapse to the `ZgotmplZ` placeholder
rather than being injected into the page. To opt out of sanitization for a value
you control, cast it to `gsx.RawCSS`:

```gsx
style={ "color: " + gsx.RawCSS(trustedColor) }
```

A `` css`...` `` literal can be one contribution in the same list when a
declaration is easier to author as CSS text:

````gsx
style={
	"display: none": hidden,
	css`color:@{accent};width:@{width}px`,
}
````

`@{...}` holes inside that contribution are CSS-value filtered, then the whole
style attribute is still merged and attribute-escaped like any other composed
style. The braces are required here because the literal is part of a larger
`style={...}` composition.

When a caller also supplies a `style` attribute, the component's composed style and the caller's style are merged per CSS property — the full story is in the [Class & style merging](#class-style-merging) section below.

## Class & style merging {#class-style-merging}

A component receives an `Attrs` bag only when its body references `attrs`. When
`{ attrs... }` places a bag containing `class` or `style`, gsx merges them with
the element's own attributes.

- **Class:** component classes and caller classes are collected in source order.
  Caller classes come last. Duplicate tokens keep the last occurrence.
- **Style:** component declarations and caller declarations are collected in the
  same order. Property names compare case-insensitively. Duplicate properties
  keep the caller declaration.
- **Parsing:** style splitting is property-aware and does not split on `;`
  inside `url(...)` or quoted strings.

<!--@include: ./_generated/styling/030-class-style-merging.md-->

In the example above the component declares `class="card"` and `style="color: red"`. The caller adds `class="featured"` and `style="color: blue; margin: 0"`. The merged result is `class="card featured"` (no common tokens, so both survive) and `style="color: blue; margin: 0"` (the caller's `color` wins, the component's `color: red` is dropped, and the caller's `margin` is new so it is added).

### Tailwind-aware class merging

The default merge strategy (`DefaultClassMerge`) deduplicates exact tokens.
Tailwind needs conflict-aware merging, where utility pairs like `px-4 px-8`
collapse to `px-8`. Configure `class_merger` for that case.

Set `class_merger` in `gsx.toml` to the fully-qualified name of an exported `func([]string) string` that implements Tailwind-aware merging:

```toml
# gsx.toml
class_merger = "myapp/twcfg.Merge"
```

A working example that wires `tailwind-merge-go` lives in [`examples/tailwind-merge/`](https://github.com/gsxhq/gsx/tree/main/examples/tailwind-merge). Full configuration reference — including the signature contract and the option-based route (`gen.WithClassMerger`) — is in [Configuration](../config).

## Exclusive selection — value-form `if` / `switch`

The composable `class={...}` / `style={...}` list is additive: every true guard
contributes. Use value-form `if` or `switch` when exactly one string should be
selected.

Use a value-form `if` for a binary toggle:

```gsx
class={ "btn", if open { "btn-open" } else { "btn-closed" } }
```

For styles, each selected arm still produces one complete declaration:

```gsx
style={
	"display: block",
	if active {
		"color: green"
	} else {
		"color: gray"
	},
}
```

Use a value-form `switch` to select among several alternatives:

```gsx
class={
	"inline-flex items-center rounded-md px-2 py-1 text-xs font-medium ring-1 ring-inset",
	switch variant {
	case Green:
		"bg-green-50 text-green-700 ring-green-600/20"
	case Yellow:
		"bg-yellow-50 text-yellow-700 ring-yellow-600/20"
	case Red:
		"bg-red-50 text-red-700 ring-red-600/20"
	default:
		"bg-gray-50 text-gray-700 ring-gray-600/20"
	},
}
```

Rules:

- Exactly one matched arm contributes a string.
- If no arm matches and there is no `default` or `else`, nothing is added.
- All arms must be strings.
- This form only works inside composed `class={...}` and `style={...}` lists.
- A pipe stage on the value-form result is not supported.

## `<style>` blocks

A `<style>` element in gsx source is a raw-text element: its content is written verbatim to the output without HTML escaping, and nested tags are not parsed. Dynamic values are interpolated with `@{ expr }` inside the block; each interpolated value is CSS-sanitized by the same `cssValueFilter` that guards the `style=` attribute — risky tokens produce the `ZgotmplZ` placeholder.

<!--@include: ./_generated/styling/040-style-blocks.md-->

The component writes a scoped `<style>` block whose declaration values are filled from Go variables at render time. `@{ w }` (an `int`) and `@{ userColor }` (a `string`) are both CSS-safe values so they pass through the filter unchanged and appear in the output directly.

For values you have already validated and want to bypass CSS sanitization, cast to `gsx.RawCSS` before interpolating: `@{ gsx.RawCSS(trustedValue) }`.
