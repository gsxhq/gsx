# Styling

gsx provides first-class support for composable `class` and `style` attributes. Both follow the same conditional-list pattern: a `{ }` literal whose entries are either always-on strings or `"value": cond` toggles. Caller-supplied class and style attributes are automatically merged into a component's root element — no extra wiring required.

## Composable class

The `class` attribute accepts a composable list instead of a plain string. Each entry is either an always-on class string or a `"name": cond` pair that is included only when `cond` is true.

```gsx
class={ "base", "modifier": condition }
```

This is gsx's built-in equivalent of `clsx` / Vue `:class`. The entries are evaluated at render time; on-entries are collected and merged into the final `class` value. The default merge strategy (`DefaultClassMerge`) deduplicates tokens keeping the last occurrence of each, so if the same token appears more than once across all contributing sources it is included exactly once.

<!--@include: ./_generated/styling/010-composable-class.md-->

Multiple always-on strings and any number of conditional pairs can appear in the same list. The list renders to a single `class="…"` attribute containing only the tokens whose conditions were true.

## Inline style composition

The `style` attribute has a parallel composable form. Each entry is a CSS declaration string — optionally conditional:

```gsx
style={ "color: red", "color: " + accent, "display: none": hide }
```

On-parts are joined with `"; "` into a single `style="…"` attribute value. String literal entries are trusted as-is; entries containing Go expressions are CSS-sanitized at render time: values that carry risky tokens (such as `(` or `/`) collapse to the `ZgotmplZ` placeholder rather than being injected into the page. To opt out of sanitization for a value you control, cast it to `gsx.RawCSS`:

```gsx
style={ "color: " + gsx.RawCSS(trustedColor) }
```

When a caller also supplies a `style` attribute, the component's composed style and the caller's style are merged per CSS property — the full story is in the [Class & style merging](#class-style-merging) section below.

## Class & style merging {#class-style-merging}

Every gsx component automatically receives an `Attrs` bag that callers can populate with extra attributes. When the bag contains `class` or `style`, gsx merges them into the component's root element rather than blindly overwriting the element's own attributes.

**Class merge — token-deduped, caller-wins.** The component's class sources (static string, composable list entries) and the caller's `class` string are all collected in source order, with the caller's contribution last. The merge function deduplicates tokens keeping the last occurrence of each — so if both the component and the caller supply the same token, the caller's copy survives (and the component's earlier copy is dropped), while tokens that only one side provides are always kept.

**Style merge — per CSS property, caller-wins.** The component's style declarations and the caller's style declarations are concatenated in the same order (component first, caller last). Property names are compared case-insensitively; when the same property appears on both sides the caller's declaration survives (property-level last-wins). The split is property-aware and will not break on `;` characters inside `url(…)` or quoted strings.

<!--@include: ./_generated/styling/030-class-style-merging.md-->

In the example above the component declares `class="card"` and `style="color: red"`. The caller adds `class="featured"` and `style="color: blue; margin: 0"`. The merged result is `class="card featured"` (no common tokens, so both survive) and `style="color: blue; margin: 0"` (the caller's `color` wins, the component's `color: red` is dropped, and the caller's `margin` is new so it is added).

### Tailwind-aware class merging

The default merge strategy (`DefaultClassMerge`) is correct for vanilla CSS but not for Tailwind, where conflicting utility pairs like `px-4 px-8` must collapse to `px-8` rather than both surviving. gsx provides a `class_merger` configuration seam for exactly this case.

Set `class_merger` in `gsx.toml` to the fully-qualified name of an exported `func([]string) string` that implements Tailwind-aware merging:

```toml
# gsx.toml
class_merger = "myapp/twcfg.Merge"
```

A working example that wires `tailwind-merge-go` lives in [`examples/tailwind-merge/`](https://github.com/gsxhq/gsx/tree/main/examples/tailwind-merge). Full configuration reference — including the signature contract and the option-based route (`gen.WithClassMerger`) — is in [Configuration → `class_merger`](../config#class_merger-tailwind-aware-class-merge-strategy).

## `<style>` blocks

A `<style>` element in gsx source is a raw-text element: its content is written verbatim to the output without HTML escaping, and nested tags are not parsed. Dynamic values are interpolated with `@{ expr }` inside the block; each interpolated value is CSS-sanitized by the same `cssValueFilter` that guards the `style=` attribute — risky tokens produce the `ZgotmplZ` placeholder.

<!--@include: ./_generated/styling/040-style-blocks.md-->

The component writes a scoped `<style>` block whose declaration values are filled from Go variables at render time. `@{ w }` (an `int`) and `@{ userColor }` (a `string`) are both CSS-safe values so they pass through the filter unchanged and appear in the output directly.

For values you have already validated and want to bypass CSS sanitization, cast to `gsx.RawCSS` before interpolating: `@{ gsx.RawCSS(trustedValue) }`.
