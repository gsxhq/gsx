# Interpolation

Interpolation embeds a Go value into the output using single braces: `{ expr }`. The expression is evaluated at render time; the result is written to the output with the appropriate escaper applied automatically for the context it sits in (HTML text, attribute value, URL, etc.).

## Basic interpolation

`{ expr }` is the core form. It works anywhere a text node can appear — between elements, inside text content, mixed with static text — and accepts any Go expression: a variable, a method call, an arithmetic expression, a type conversion.

<!--@include: ./_generated/interpolation/010-interpolation-props.md-->

The `{ name }` and `{ count }` expressions are evaluated against the component's params. String values are HTML-escaped (angle brackets, ampersands, and quotes are encoded); numeric values (`int`, `float64`, etc.) are converted to their decimal string representation without escaping, because digits and a decimal point carry no HTML-special meaning.

::: v-pre
Note that `{ expr }` is **interpolation** — it emits a value. It is not a Go statement block. To run a Go statement that produces no output, use `{{ stmt }}` (a GoBlock). See [Raw Go](./raw-go) for details.
:::

## Body interpolating literals

An `f`-prefixed backtick literal inside body braces — `` {f`…@{ expr }…`} `` —
interpolates static text and typed `@{ }` holes directly in element-body
position, the mirror image of the
[interpolating attribute literal](./attributes#interpolating-attribute-literals).
It saves you from concatenating a Go string when you want a single run of text
that interleaves literals and dynamic values.

<!--@include: ./_generated/interpolation/040-body-backtick-literals.md-->

Each `@{ expr }` hole is escaped the same way a bare `{ expr }` hole would be in
text context: a string is HTML-escaped, a numeric type is formatted to its
decimal string (via the zero-alloc integer path — no escaper overhead), and a
`fmt.Stringer` renders via `String()`. The static text between holes is trusted
source content and is emitted verbatim. There is **no** materialized concat
string: the literal lowers to the exact per-segment writes a hand-written mix of
static text and `{ expr }` holes would produce.

Two characters need escaping inside the literal: `` \` `` for a literal backtick,
and `\@{` for a literal `@{` that should not be read as a hole.

::: v-pre
Interpolation is **opt-in behind the `f` prefix**. A bare (unprefixed) backtick
in braces is always an ordinary Go raw string with **no** `@{ }` hole processing —
`` {`a` + x} ``, `` {strings.Repeat(`ab`, n)} ``, `` {`plain @{not-a-hole}`} `` —
so a `@{` inside a bare literal is literal text. Only `` f`…` `` interpolates,
and it must be the lone child of the braces (optionally followed by a
whole-literal pipeline — `` {f`…` |> upper} ``). This keeps every existing use of
raw strings in braces working unchanged.
:::

### Two delimiter forms

`` f`…` `` and `f"…"` are the same literal, just spelled with a different
delimiter — pick whichever quote your content doesn't contain. The `"` form
is the escape-hatch for text that itself carries a backtick:

```gsx
component Row(id string, n int) {
	<p>{f"row-`@{id}`-@{n}"}</p>
}
```

`Row(RowProps{Id: "a&b", N: 5})` renders `` <p>row-`a&amp;b`-5</p> `` — the same
per-segment, type-aware escaping as the backtick form, just with a literal
backtick in the static text instead of an escaped one.

### As a first-class Go value

An `f` literal isn't limited to body braces — it's an ordinary `string`
expression, so it can be assigned to a `var`, passed as a call argument, or
referenced by name from inside `{ }`:

```gsx
package demo

var name = "world"

var greeting = f`hello @{name}`

component Uses() {
	<p>{ greeting }</p>
}
```

`greeting` is a plain `string`; `{ greeting }` interpolates it like any other
Go expression, rendering `<p>hello world</p>`. It works as a call argument
too — `` { emphasize(f`@{label}!`) } `` passes the assembled string straight into
a function. See [Elements — Elements as values](./elements#elements-as-values)
and [Raw Go — the reverse direction](./raw-go#the-reverse-direction-elements-in-go-expression-position)
for the full set of Go-expression positions this and `<tag>`/`<>` literals
share. `` js`...` `` and `` css`...` `` stay attribute-context only — they are
not valid as standalone Go values.

## Fields & typed values

You can interpolate any Go expression, including field accesses on a struct passed as a param. The type does not need to be a string: any type that satisfies `fmt.Stringer` is formatted via its `String()` method, and numeric primitives are formatted directly.

<!--@include: ./_generated/interpolation/020-field-access.md-->

`{ user.Name }` and `{ user.Age }` access fields on the `User` struct param directly. There is no special accessor syntax — it is ordinary Go field access inside braces.

## Functions & `(T, error)` auto-unwrap

When an expression is a function call returning `(T, error)`, the code generator automatically unwraps the tuple: it assigns the result to a temporary, checks the error, and if the error is non-nil, returns it from the enclosing `Render` call. No extra syntax is needed.

<!--@include: ./_generated/interpolation/030-function-t-error-unwrap.md-->

`lookup(key)` returns `(string, error)`. The generated code is equivalent to:

```go
_v, _err := lookup(key)
if _err != nil {
    return _err
}
// write _v to output
```

The caller of `Render` receives the error and can handle it (log, serve a 500, etc.).

The auto-unwrap is **uniform across all expression positions** — not limited to text interpolation:

::: v-pre
| Position | Example |
|---|---|
| Text interpolation | `{ f() }` |
| Attribute value | `title={ f() }` |
| Child-component prop value | `<Row label={lookup(k)}/>` |
| Ordered-attrs pair value | `<Card bag={{ "k": f(t) }}/>` |
| Pipeline stage | each `\|>` stage whose return is `(T, error)` |
| Children / slot body | `<Wrap>{ f(t) }</Wrap>` |
:::

When a child-component prop value returns `(T, error)`, gsx hoists the call to a temporary before building the component literal. When multiple props on the same component call each return a tuple, all are hoisted in source order:

<!--@include: ./_generated/interpolation/035-error-unwrap-childprop.md-->

**Rules:**

- **Only `(T, error)`.** Exactly two return values, with the second typed `error`. Any other multi-value shape — `(int, string)`, three values, etc. — is a **compile-time gsx error**: `only (T, error) is supported`.
- **Automatic — no marker or opt-out.** The unwrap is always applied; there is no annotation to add or remove.
- **`?` is rejected (a gsx error).** A `?` try-marker suffix (e.g. `upper?` in a pipeline stage) is reported as not supported, because gsx already auto-unwraps `(T, error)` values.

## Numeric & string contexts

The escaper applied to `{ expr }` depends on **where** the interpolation appears, not on the type of the value:

- **Text content** (`<p>{ x }</p>`) — HTML-escapes the string form of `x`.
- **Attribute value** (`title={ x }`) — attribute-escapes the value.
- **Interpolating attribute literal** (`` title=f`Item @{ x }` ``) — an `f`-prefixed backtick literal mixing static text and `@{ }` holes in attribute-value position; each hole is escaped the same way its surrounding attribute would be (attribute-escape, or scheme-sanitize the whole assembled value for a URL attribute). See [Attributes — Interpolating attribute literals](./attributes#interpolating-attribute-literals).
- **URL attribute** (`href={ x }`, `src={ x }`, `action={ x }`, and htmx method attrs `hx-get`/`hx-post`/`hx-put`/`hx-delete`/`hx-patch`) — scheme-sanitizes and escapes. URL attributes are the only ordinary `attr={ x }` name-based special case; other attributes, including `hx-on*`, are plain attribute text unless written with an explicit embedded-language literal.
- **Attribute-local JavaScript/CSS** (`` @click=js`save(@{x})` ``, `` style=css`color:@{x}` ``, `` style={ css`color:@{x}` } ``) — escapes each hole for its embedded JavaScript or CSS position.
- **`<script>` body** (`@{ x }`) — JSON-encodes the Go value to a safe JS literal.
- **`<style>` body** (`@{ x }`) — CSS value-filters the string.

Numeric values in text context (`{ count }` where `count` is `int`) are formatted as their decimal representation and do not require escaping — no HTML-special characters can appear in a plain integer string. This means numeric interpolation has no overhead from the escaper.

For a complete reference of escaping contexts and opt-out helpers (`gsx.Raw`, `gsx.RawURL`, `gsx.RawJS`, `gsx.RawCSS`), see [Escaping](./escaping).

## Markup or Go in braces

In **attribute-value position** (`name={…}`), `{…}` can hold either a Go expression or markup. gsx resolves the ambiguity positionally — the Babel rule: if the first non-space character after `{` is `<` followed by a tag-name character, the content is parsed as markup; otherwise it is a Go expression. So `header={ <h1>Title</h1> }` is a markup-valued attribute (see [Composition — named slots](./composition#named-slots)), while `disabled={ a < b }` is a boolean expression where `<` is the less-than operator.

::: v-pre
In body and text context the ambiguity does not arise: markup is written as bare elements (`<span>…</span>`), and `{…}` holds interpolation, a GoBlock (`{{ }}`), or a control-flow construct (`{ if … }`, `{ for … }`, `{ switch … }`) — the latter dispatched by keyword, not by `<`.
:::

For parser corner cases, see the [`parser/` corpus](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases/parser).
