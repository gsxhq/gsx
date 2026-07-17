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

The attribute **name** decides how a `bool` value renders:

- On an HTML boolean attribute (`checked`, `required`, `disabled`, `hidden`, …),
  `true` renders a bare attribute and `false` omits it — presence is the state.
- On any other name (`aria-*`, `data-*`, `contenteditable`, …), a `bool`
  renders `="true"`/`="false"` — there the string is the state.

A bare attribute (`<input required>`) is always present. A **string** value is
never affected by the name, so `required="foo"` renders verbatim.

For a name gsx cannot know is a toggle — a web component, a Datastar directive —
wrap the value in `gsx.Toggle`, which forces presence anywhere:
`active={ gsx.Toggle(open) }`.

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
- A `bool` on an HTML boolean attribute toggles (`true` bare, `false` omitted); on any other name it renders `="true"`/`="false"`. `gsx.Toggle(b)` forces presence on any name.
- Spread values are escaped for the destination attribute; URL and `srcset`
  destinations also run scheme sanitization.
- `gsx.AttrMap.ToAttrs()` sorts map keys.

**Treat keys as trusted code.** Build `gsx.Attr` and `gsx.Attrs` keys only from
developer-controlled strings; never use user input as a key. Values are still
escaped or URL-sanitized for their destination. For dynamic JavaScript or CSS,
use a typed literal on the final native element; see
[JavaScript and CSS contexts](./escaping.md#javascript-and-css-contexts).

<span id="ordered-attrs-literal-k-v"></span>

## Ordered-attrs literal <code v-pre>{{ "k": v }}</code> {#ordered-attrs-literal}

::: v-pre
Use `{{ "k": v }}` as the value of a component's attrs-bag parameter when the
call site must declare the bag in source order.
:::

<!--@include: ./_generated/attributes/050-ordered-attributes.md-->

::: v-pre
- The literal is valid only as a component attribute value.
- Keys must be quoted strings.
- A `bool` on an HTML boolean attribute toggles (`true` bare, `false` omitted); on any other name it renders `="true"`/`="false"`. `gsx.Toggle(b)` forces presence on any name.
- The last scalar value for a key wins.
- All `class` and `style` values compose.
:::

### Contributing to the declared attrs input

::: v-pre
```gsx
<Panel id="profile" attrs={computed} { defaults... } attrs={{ "role": "region" }}/>
```

- Ordinary unmatched attributes, conditional attributes, spreads,
  `attrs={expr}`, and `attrs={{...}}` compose in authored order.
- Names inside a conditional-attribute group are bag contributors; they do not
  conditionally fill ordinary component parameters.
- `attrs={expr}` accepts a computed attrs-bag value.
- Repeated explicit `attrs` contributors are allowed; they do not fill an
  ordinary parameter slot.
- The component must declare the reserved `attrs` parameter.
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

A matching `string` parameter receives the assembled string. A matching `gsx.Node`
parameter receives an escaped text node. An unmatched name falls through to the
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
