# Attributes

HTML attributes in gsx accept static string values (`name="value"`), Go
expressions (`name={expr}`), and explicit embedded-language literals such as
`` name=js`...` `` or `` name=css`...` ``. The right-hand side is evaluated at
render time and escaped for its context automatically — no manual encoding
needed.

For `js` and `css` attribute literals, braces are optional: `` name=js`...` ``
and `` name={js`...`} `` are equivalent, as are `` name=css`...` `` and
`` name={css`...`} ``.

When a CSS literal is one item inside a composed style list, keep the list
braces: `` style={ "display:none": hidden, css`color:@{color}` } ``.

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

## Ordered-attrs literal <code v-pre>{{ "k": v }}</code>

::: v-pre
When attribute order matters — for example, `data-*` directives consumed by Datastar where a signal must be declared before it is read — use the `{{ "key": value }}` literal in a **component invocation** to pass an ordered attribute bag. The literal lowers to `gsx.Attrs` (an ordered slice), the same type as any declared `Attrs gsx.Attrs` prop and the `{ bag… }` spread.

Use `{{ "k": v }}` any time key order matters: Datastar `data-*` directives, JSX-style overrides through duplicate scalar keys, or explicit ordering that a map would scramble.
:::

<!--@include: ./_generated/attributes/050-ordered-attributes.md-->

::: v-pre
`Counter` declares a `gsx.Attrs` prop and spreads it with `{ signals... }`. The caller passes `signals={{ "data-signals": …, "data-text": …, "data-on-click": … }}` — the attributes render in that exact order (source order in the literal). Because `gsx.Attrs` is an ordered slice, no sorting happens.

Key points:

- The `{{ }}` literal is valid **only as the value of a component attribute** bound to a declared `gsx.Attrs` prop. There is no standalone-element form — `<div {{ … }}>` is a parse error.
- Keys are quoted string literals (`"data-signals"`, not bare identifiers). This is required so that kebab and colon names such as `"hx-on:click"` round-trip safely.
- A bool value (`"data-show": true`) renders the bare attribute `data-show`; `false` omits it entirely.
- `"class"` or `"style"` pairs in an `Attrs` bag render verbatim in their slot position. At the element level, `class=` and `style=` use the bag's `Class()` / `Style()` aggregate methods for merging.
- A pair value that returns `(T, error)` — e.g. `{{ "data-signals": sig(t) }}` where `sig` returns `(string, error)` — is auto-unwrapped: the error propagates from `Render`. See [auto-unwrap](./interpolation#functions-t-error-auto-unwrap).

`gsx.Attrs` tolerates duplicate keys — the `{{ }}` literal can repeat a key. Scalar duplicates are last-wins when spread, matching JSX-style override order. `class` and `style` are special aggregate keys. Methods on `gsx.Attrs`:

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

### Targeting the synthesized attrs bag

Every component that spreads `{ attrs… }` gets a generated `Attrs gsx.Attrs`
prop for the unmatched-attribute fallthrough bag. `attrs={{ "key": value }}`
targets that field explicitly — the same destination as writing the attrs
individually or letting them fall through. Lowercase `attrs` is the canonical
spelling; capitalize-first field matching also accepts `Attrs={{ … }}` (the
two spell the same target and render identically).

When `attrs={{ … }}` appears alongside other bag contributors on the same
call site — bare fallthrough attrs, `{ expr… }` spreads, conditional attrs —
they compose instead of colliding. Bare/fallthrough attrs form the base bag,
then spreads and conditional attrs merge in source order, then the
`attrs={{ … }}` literal merges last via `Merge`, regardless of where it
appears among the other attrs in source. A second `attrs={{ … }}` literal on
the same element is a clean error (`ordered-attrs-duplicate`) — combine the
pairs into one literal instead.

Imported components from the same module get this treatment automatically:
gsx discovers their declared props — including the synthesized `Attrs`
field — during module analysis, so bare-attr fallthrough and
`attrs={{ … }}` behave exactly as they do for same-package components. See
[Composition — cross-file & cross-package](./composition) for what happens
when a dependency's props cannot be discovered.
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

Inside `` js`...` `` or `` css`...` ``, write `` \` `` for a literal backtick.
The backslash escapes the gsx delimiter and is not part of the embedded
JavaScript or CSS source.

## Interpolating attribute literals

An `f`-prefixed backtick literal in attribute-value position —
`` name=f`…@{ expr }…` `` — mixes static text with typed, auto-escaped holes, the
same interleaving `{ expr }` already gives you in element bodies. It closes the
gap for ordinary (non-JS, non-CSS) attribute values: without it, interleaving
static text and a dynamic value in one attribute means falling back to string
concatenation in Go. Interpolation is opt-in behind the `f` prefix — a bare
`` name=`…` `` value is a plain Go raw string, and a `@{` inside it is literal
text.

<!--@include: ./_generated/attributes/070-interpolating-attribute-literals.md-->

Each `@{ expr }` hole is escaped by the Go type of `expr`: a string is
attribute-escaped, an integer or other numeric type is formatted to its
decimal string, and a `fmt.Stringer` is rendered via `String()` — the same
type-aware rules `{ expr }` interpolation uses elsewhere. A hole also accepts a
pipeline, evaluated before escaping: `` title=f`Item @{ id |> upper }` ``.

Two characters need escaping inside the literal: `` \` `` for a literal
backtick, and `\@{` for a literal `@{` that should not be read as a hole.
Anywhere else the text is copied through verbatim, exactly like a `` js`...` ``
or `` css`...` `` literal.

::: v-pre
To pipe the **whole** assembled value through a filter, wrap the literal in
braces and append a pipeline: `` class={f`btn-@{v}` |> upper} ``. The static text
and holes are interpolated into one string, then that whole value flows through
the pipeline before the attribute escaper runs — see [Pipelines — whole-literal
pipelines](./pipelines#whole-literal-pipelines). The pipe is only available in
the braced form; the direct (unbraced) `` class=f`…` `` literal does not take
a trailing `|>`.
:::

### URL attributes sanitize the whole value

When the attribute is a URL context (`href`, `src`, `action`, and the rest of
the URL-attribute table — see [Contextual escaping](#contextual-escaping)
above), the literal's static text and holes are assembled into one string
first, and *that whole string* is scheme-sanitized — not each hole
individually. A dangerous scheme is blocked to `about:invalid#gsx` even when
it is split across a hole boundary, because there is never a partial string to
sneak a scheme past the check:

<!--@include: ./_generated/attributes/080-url-attribute-literals-are-sanitized-whole.md-->

A safe dynamic scheme still works — `` href=f`@{scheme}://example.com` `` with
`scheme = "https"` renders `href="https://example.com"` unchanged. For a
value you have already validated and want to bypass the scheme check
entirely, interpolate `gsx.RawURL(...)` instead of writing the URL as an
`f` literal: `` href={ gsx.RawURL(trustedURL) } ``.

### `data:image` literals

An `f` literal is also how you write a `data:` URL directly, on an
[image sink](./escaping#resource-vs-navigational-url-sinks) — `<img src>`,
`<source src>`, `<input src>`, `<video poster>`, or `background`:

```gsx
<img src=f`data:image/png;base64,@{b64}` />
```

The scheme, MIME type, and `;base64,` marker are static author text; the hole
is a plain `string` interpolation — a value the author has **already
base64-encoded** — assembled with the surrounding static text into one string
and passed through unchanged (like any other `string` hole in a URL-context
literal; see [URL attributes sanitize the whole
value](#url-attributes-sanitize-the-whole-value) above).

If you're starting from raw `[]byte` image bytes, base64-encode them first —
either with `encoding/base64` in a Go interpolation, or with the built-in
`dataURL` filter, which does both the encoding and the `data:` URL assembly in
one step:

```gsx
<img src={ imageBytes |> dataURL("image/png") } />
```

See [Pipelines — `dataURL` grants no privilege](./pipelines#dataurl-grants-no-privilege)
for what that filter does and does not vouch for.

Writing a `data:` literal on a **strict** sink (`href` and the rest of the
[strict-sink table](./escaping#resource-vs-navigational-url-sinks)) is a
compile-time error (`data-url-strict-sink`): a static `data:` prefix has no
safe navigational or script use, so gsx rejects the literal instead of
falling back to the runtime sentinel. Use an image sink instead, or
`gsx.RawURL` for a value you have already validated.

### `class` and `style` are merge targets

A `class` or `style` backtick literal composes with a forwarded `{ attrs... }`
bag exactly like a static or composable `class`/`style` value does: the bag's
class or style merges in caller-last, producing a single merged attribute
instead of two competing ones. See [Class & style merging](./styling#class-style-merging)
for the full merge story — the interpolated case is documented alongside it:

<!--@include: ./_generated/styling/040-interpolated-class-literal-merges-with-a-spread-bag.md-->

An interpolated literal with no spread on the element skips the merge
machinery entirely and emits the assembled value directly — no `gsx.Attrs`
prop is synthesized unless the component body references `attrs` elsewhere.

`""` (a quoted string) stays a purely static value — gsx does not scan quoted
attributes for `@{ }` holes, matching quoted `` js`` ``/`` css`` `` attributes.
`{ expr }` remains a single Go expression; reach for the backtick literal only
when an attribute value needs static text interleaved with one or more holes.

For a complete reference of escaping contexts and the opt-out helpers (`gsx.Raw`, `gsx.RawURL`, `gsx.RawJS`, `gsx.RawCSS`), see [Escaping](./escaping).
