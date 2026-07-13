# Attributes

Use quoted strings for static values, braces for Go expressions, and typed
literals when an attribute contains interpolated text, JavaScript, or CSS.

```gsx
<input name="email" value={value} required={required}/>
<a href=f`/users/@{id}`>Profile</a>
<button @click=js`save(@{id})`>Save</button>
<div style=css`color:@{color}`>...</div>
```

## Expression attributes

Use `name={expr}` to bind a Go value.

<!--@include: ./_generated/attributes/010-expression-attributes.md-->

`data-count={count}` formats a numeric value as attribute text. Quoted values
are literal: `title="Item @{id}"` does not scan for `@{}` holes.

## Boolean attributes

A bare boolean attribute is always present. A boolean expression renders the
attribute when true and omits it when false.

<!--@include: ./_generated/attributes/020-boolean-attributes.md-->

## Conditional attributes

Use `{ if cond { ... } }` inside an opening tag when a condition contributes
one or more attributes.

<!--@include: ./_generated/attributes/030-conditional-attributes.md-->

An `else` branch works too:
`{ if active { class="active" } else { class="idle" } }`.

## Spread `{ x… }` — ordered

Spread a `gsx.Attrs` bag with `{ bag... }`; entries render in slice order.

<!--@include: ./_generated/attributes/040-spread-attributes.md-->

- Source order is kept.
- Boolean `true` renders a bare attribute; `false` omits it.
- Spread values are escaped for the destination attribute; URL and `srcset`
  destinations also run scheme sanitization.
- `gsx.AttrMap.ToAttrs()` sorts map keys.

<span id="ordered-attrs-literal-k-v"></span>

## Ordered-attrs literal <code v-pre>{{ "k": v }}</code> {#ordered-attrs-literal}

::: v-pre
Use `{{ "k": v }}` as the value of a component's `gsx.Attrs` prop when the
call site must declare the bag in source order.
:::

<!--@include: ./_generated/attributes/050-ordered-attributes.md-->

::: v-pre
- The literal is valid only as a component attribute value.
- Keys must be quoted strings.
- Boolean `true` renders a bare attribute; `false` omits it.
- The last scalar value for a key wins.
- All `class` and `style` values compose.
:::

### Targeting the synthesized attrs bag

::: v-pre
```gsx
<Panel id="profile" { defaults... } attrs={{ "role": "region" }}/>
```

- Ordinary attributes, conditional attributes, and spreads compose in source
  order.
- `attrs={{...}}` is applied last.
- A call site may contain only one ordered literal.
:::

## Contextual escaping

Ordinary values are attribute-escaped. Dynamic URL expressions, interpolated
URL literals, and forwarded URL values also reject dangerous schemes. A quoted
URL authored directly on a native element is trusted author text and is emitted
without scheme validation.

<!--@include: ./_generated/attributes/060-attribute-contexts.md-->

Use typed literals for JavaScript- and CSS-valued attributes:

```gsx
<button @click=js`save(@{id})`>Save</button>
<div style=css`color:@{color}`>...</div>
```

See [Escaping](./escaping.md) and [JavaScript](./javascript.md) for the complete
context rules.

## Interpolating attribute literals

Use `f` literals to combine static text with typed `@{expr}` holes.

<!--@include: ./_generated/attributes/070-interpolating-attribute-literals.md-->

- Use `f"..."` when the content contains a backtick.
- Inside a backtick-delimited literal, `` \` `` writes a literal backtick.
- `\@{` writes a literal `@{` instead of opening a hole.

### On a component tag

```gsx
<PageHeader title="Tickets" subtitle=f`@{count} tickets`/>
```

A matching `string` prop receives the assembled string. A matching `gsx.Node`
prop receives an escaped text node. An unmatched name falls through to the
component's attrs bag.

### URL attributes sanitize the whole value

Static text and holes in an interpolated URL literal are assembled before the
URL scheme check.

<!--@include: ./_generated/attributes/080-url-attribute-literals-are-sanitized-whole.md-->

### `data:image` literals

```gsx
<img src=f`data:image/png;base64,@{b64}`/>
<img src={imageBytes |> dataURL("image/png")}/>
```

A `data:` literal is rejected on strict navigation sinks such as `href`. See
[Escaping](./escaping.md) for image and navigation sink rules.

### `class` and `style` are merge targets

Interpolated `class` and `style` values merge with a forwarded attrs bag.

<!--@include: ./_generated/styling/040-interpolated-class-literal-merges-with-a-spread-bag.md-->

See [Styling](./styling.md#class-style-merging) for precedence.
