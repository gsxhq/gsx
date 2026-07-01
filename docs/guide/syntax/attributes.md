# Attributes

HTML attributes in gsx accept static string values (`name="value"`), Go
expressions (`name={expr}`), and explicit embedded-language literals such as
`` name=js`...` `` or `` name=css`...` ``. The right-hand side is evaluated at
render time and escaped for its context automatically — no manual encoding
needed.

For `js` and `css` attribute literals, braces are optional: `` name=js`...` ``
and `` name={js`...`} `` are equivalent, as are `` name=css`...` `` and
`` name={css`...`} ``.

## Expression attributes

Write `name={expr}` to bind any Go expression to an attribute. The expression can be a variable, a field access, an arithmetic expression, a function call, or a literal value.

<!--@include: ./_generated/attributes/010-expression-attributes.md-->

`href={url}` is a URL-context attribute: gsx recognises `href`, `src`, `action`, and the htmx method attributes (`hx-get`, `hx-post`, etc.) as URL contexts and scheme-sanitises the value in addition to HTML-escaping it (see [Contextual escaping](#contextual-escaping) below).

`data-count={count}` is a plain attribute: the integer is converted to its decimal string representation and attribute-escaped. Any Go expression whose result converts to a string is valid here.

Quoted attributes are literal strings. gsx does not scan them for `@{}` holes,
so `x-data="{ open: @{open} }"` renders those characters as written.

## Boolean attributes

A bare attribute name with no value (`required`, `disabled`, `checked`) is always rendered as-is. When an attribute is bound to a `bool` expression (`disabled={on}`), gsx renders the attribute name with no value when `on` is `true` and **omits the attribute entirely** when `on` is `false`.

<!--@include: ./_generated/attributes/020-boolean-attributes.md-->

In this example `required` is always present because it has no expression binding. `disabled={on}` is present only when `on` is `true`; calling `Field(FieldProps{On: false})` produces `<input type="text" class="form-control" required/>` with no `disabled`.

## Conditional attributes

To add one or more attributes only when a condition holds, use `{ if cond { attr=… } }` inside the element's opening tag.

<!--@include: ./_generated/attributes/030-conditional-attributes.md-->

The `{ if … { … } }` block can contain any combination of attribute bindings. The braces wrap the entire `if` expression; the inner braces contain only attribute syntax, not Go statements. An `else` branch is also allowed: `{ if cond { class="a" } else { class="b" } }`.

## Spread `{ x… }` — ordered

To forward a bag of attributes in a passthrough component, declare a parameter of
type `gsx.Attrs` and spread it onto an element with `{ bag… }`.

<!--@include: ./_generated/attributes/040-spread-attributes.md-->

`gsx.Attrs` is `[]gsx.Attr` — an ordered slice and the only attribute-bag type accepted by templates. Pairs render in their declared or insertion order: whatever order the call site writes them is the order they appear in the HTML. The implicit fallthrough bag (unmatched call-site attributes collected into an `Attrs` prop) lands in call-site source order.

Boolean values in an `Attrs` slice follow the same rule as attribute-level booleans: `true` renders as the bare attribute name; `false` omits the attribute entirely.

`map[string]any` and `gsx.AttrMap` are not implicit template bag types. When starting from map-shaped data in Go, convert it explicitly before passing it to a template:

```go
attrs := gsx.AttrMap{"class": "card", "id": id}.ToAttrs()

// A bare map has no ToAttrs method; convert it to AttrMap first.
attrs = gsx.AttrMap(m).ToAttrs()
```

`ToAttrs` sorts keys ascending because maps do not preserve insertion order. When order matters, construct `gsx.Attrs` directly instead.

## Ordered-attrs literal `&#123;&#123; "k": v &#125;&#125;`

::: v-pre
When attribute order matters — for example, `data-*` directives consumed by Datastar where a signal must be declared before it is read — use the `&#123;&#123; "key": value &#125;&#125;` literal in a **component invocation** to pass an ordered attribute bag. The literal lowers to `gsx.Attrs` (an ordered slice), the same type as any declared `Attrs gsx.Attrs` prop and the `{ bag… }` spread.

Use `&#123;&#123; "k": v &#125;&#125;` any time key order matters: Datastar `data-*` directives, JSX-style overrides through duplicate scalar keys, or explicit ordering that a map would scramble.
:::

<!--@include: ./_generated/attributes/050-ordered-attributes.md-->

::: v-pre
`Counter` declares a `gsx.Attrs` prop and spreads it with `{ signals... }`. The caller passes `signals=&#123;&#123; "data-signals": …, "data-text": …, "data-on-click": … &#125;&#125;` — the attributes render in that exact order (source order in the literal). Because `gsx.Attrs` is an ordered slice, no sorting happens.

Key points:

- The `&#123;&#123; &#125;&#125;` literal is valid **only as the value of a component attribute** bound to a declared `gsx.Attrs` prop. There is no standalone-element form — `<div &#123;&#123; … &#125;&#125;>` is a parse error.
- Keys are quoted string literals (`"data-signals"`, not bare identifiers). This is required so that kebab and colon names such as `"hx-on:click"` round-trip safely.
- A bool value (`"data-show": true`) renders the bare attribute `data-show`; `false` omits it entirely.
- `"class"` or `"style"` pairs in an `Attrs` bag render verbatim in their slot position. At the element level, `class=` and `style=` use the bag's `Class()` / `Style()` aggregate methods for merging.
- A pair value that returns `(T, error)` — e.g. `&#123;&#123; "data-signals": sig(t) &#125;&#125;` where `sig` returns `(string, error)` — is auto-unwrapped: the error propagates from `Render`. See [auto-unwrap](./interpolation#functions-t-error-auto-unwrap).

`gsx.Attrs` tolerates duplicate keys — the `&#123;&#123; &#125;&#125;` literal can repeat a key. Scalar duplicates are last-wins when spread, matching JSX-style override order. `class` and `style` are special aggregate keys. Methods on `gsx.Attrs`:

| Method | Behavior |
|--------|----------|
| `Class() string` | Aggregates **all** `"class"` pairs (space-joined) — nothing dropped |
| `Style() string` | Aggregates **all** `"style"` pairs (`"; "`-joined) |
| `Get(key) (any, bool)` | Last occurrence wins |
| `Has(key) bool` | True if any pair has the key |
| `Without(keys…) Attrs` | Removes **all** matching pairs |
| `Take(key) (any, Attrs)` | Last value + `Without(key)` |
| `Merge(other Attrs) Attrs` | `class`/`style` concat in place on first match; other keys overwrite the last existing match or append |

A nil `Attrs` is an empty bag — safe to spread, merge, and call methods on.
:::

## Contextual escaping

For ordinary expression attributes, the only name-based special case is URL
classification. `href={href}`, `src={src}`, `action={action}`, and configured URL
attributes are scheme-sanitised and then attribute-escaped; other `attr={expr}`
values are ordinary attribute-escaped text.

<!--@include: ./_generated/attributes/060-attribute-contexts.md-->

In this example `href={href}` is a URL context. When the value is `"javascript:alert(1)"` — a dangerous scheme — gsx replaces the entire value with `about:invalid#gsx`, rendering a safe but inert link. A normal URL such as `"/search?q=go&page=2"` would be percent-encoded and HTML-attribute-escaped as usual.

JavaScript and CSS in attributes are explicit. Use `` js`...` `` for event
handlers, Alpine/HTMX expressions, or other JavaScript-valued attributes, and
`` css`...` `` for CSS-valued attributes:

````gsx
<button @click=js`save(@{id})`>Save</button>
<div style=css`color:@{color}`>...</div>
````

`@{expr}` holes inside those literals are escaped for their embedded-language
position. Plain `hx-on:*={expr}` or `@click={expr}` attributes do not switch to a
JavaScript context by name; use a `` js`...` `` literal when the attribute value is
JavaScript.

For a complete reference of escaping contexts and the opt-out helpers (`gsx.Raw`, `gsx.RawURL`, `gsx.RawJS`, `gsx.RawCSS`), see [Escaping](./escaping).
