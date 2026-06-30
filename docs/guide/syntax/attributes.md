# Attributes

HTML attributes in gsx accept static string values (`name="value"`) or Go expressions (`name={expr}`). The right-hand side is evaluated at render time and escaped for the attribute's context automatically — no manual encoding needed.

## Expression attributes

Write `name={expr}` to bind any Go expression to an attribute. The expression can be a variable, a field access, an arithmetic expression, a function call, or a literal value.

<!--@include: ./_generated/attributes/010-expression-attributes.md-->

`href={url}` is a URL-context attribute: gsx recognises `href`, `src`, `action`, and the htmx method attributes (`hx-get`, `hx-post`, etc.) as URL contexts and scheme-sanitises the value in addition to HTML-escaping it (see [Contextual escaping](#contextual-escaping) below).

`data-count={count}` is a plain attribute: the integer is converted to its decimal string representation and attribute-escaped. Any Go expression whose result converts to a string is valid here.

## Boolean attributes

A bare attribute name with no value (`required`, `disabled`, `checked`) is always rendered as-is. When an attribute is bound to a `bool` expression (`disabled={on}`), gsx renders the attribute name with no value when `on` is `true` and **omits the attribute entirely** when `on` is `false`.

<!--@include: ./_generated/attributes/020-boolean-attributes.md-->

In this example `required` is always present because it has no expression binding. `disabled={on}` is present only when `on` is `true`; calling `Field(FieldProps{On: false})` produces `<input type="text" class="form-control" required/>` with no `disabled`.

## Conditional attributes

To add one or more attributes only when a condition holds, use `{ if cond { attr=… } }` inside the element's opening tag.

<!--@include: ./_generated/attributes/030-conditional-attributes.md-->

The `{ if … { … } }` block can contain any combination of attribute bindings. The braces wrap the entire `if` expression; the inner braces contain only attribute syntax, not Go statements. An `else` branch is also allowed: `{ if cond { class="a" } else { class="b" } }`.

## Spread `{ x… }` — sorted

To forward a bag of attributes — commonly used for passthrough components — declare a parameter of type `gsx.Attrs` and spread it onto an element with `{ bag… }`.

<!--@include: ./_generated/attributes/040-spread-attributes.md-->

`gsx.Attrs` is `map[string]any`. Because map iteration order in Go is undefined, gsx **sorts the keys alphabetically** before rendering to produce deterministic output. In the example above, `data-active` sorts before `id`, so they appear in that order in the HTML regardless of the order they were inserted into the map.

Boolean values in an `Attrs` bag follow the same rule as attribute-level booleans: `"disabled": true` renders as the bare attribute `disabled`; `"disabled": false` omits it.

## Ordered-attrs literal → `gsx.OrderedAttrs` {#ordered--→-gsxorderedattrs}

::: v-pre
When attribute order matters — for example, `data-*` directives consumed by Datastar where a signal must be declared before it is read — use the `{{ "key": value }}` literal in a **component invocation** to pass an ordered attribute bag. The literal is valid only in attribute-value position at a component call, bound (via the kebab field-matcher) to a prop declared as `gsx.OrderedAttrs`. The component then spreads that prop onto an element with `{ prop... }`, and the attributes render in the exact order they were written in the literal — no sorting.

Unlike `{ bag… }` spread (which sorts keys alphabetically), `{ signals… }` spread on a `gsx.OrderedAttrs` prop calls `SpreadOrdered` and preserves insertion order end to end.
:::

<!--@include: ./_generated/attributes/050-ordered-attributes.md-->

::: v-pre
`Counter` declares `signals gsx.OrderedAttrs` and spreads it with `{ signals... }`. The caller passes `signals={{ "data-signals": …, "data-text": …, "data-on-click": … }}` — the attributes render in that exact order. Contrast this with a `gsx.Attrs` (map) bag: the same three keys would render alphabetically as `data-on-click`, `data-signals`, `data-text`.

Key points:

- The `{{ }}` literal is valid **only as the value of a component attribute** whose matching prop is typed `gsx.OrderedAttrs`. There is no standalone-element form — `<div {{ … }}>` is a parse error.
- Keys are quoted string literals (`"data-signals"`, not bare identifiers). This is required so that kebab and colon names such as `"hx-on:click"` round-trip safely.
- A bool value (`"data-show": true`) renders the bare attribute `data-show`; `false` omits it entirely — the same rule as `gsx.Attrs`.
- `gsx.OrderedAttrs` does **not** participate in `class`/`style` merge. Any `"class"` or `"style"` pair in an ordered bag renders verbatim in its slot position; use element-level `class=` or `style=` for merging.
- A pair value that returns `(T, error)` — e.g. `{{ "data-signals": sig(t) }}` where `sig` returns `(string, error)` — is auto-unwrapped: the error propagates from `Render`. See [auto-unwrap](./interpolation#functions-t-error-auto-unwrap).
:::

## Contextual escaping

The escaper applied to an attribute value depends on the attribute name, not on the Go type of the expression. gsx knows which attributes are URL contexts, which are JavaScript event handlers, and which are plain text contexts, and applies the appropriate sanitiser automatically.

<!--@include: ./_generated/attributes/060-attribute-contexts.md-->

In this example `href={href}` is a URL context. When the value is `"javascript:alert(1)"` — a dangerous scheme — gsx replaces the entire value with `about:invalid#gsx`, rendering a safe but inert link. A normal URL such as `"/search?q=go&page=2"` would be percent-encoded and HTML-attribute-escaped as usual.

For a complete reference of escaping contexts and the opt-out helpers (`gsx.Raw`, `gsx.RawURL`, `gsx.RawJS`, `gsx.RawCSS`), see [Escaping](./escaping).
