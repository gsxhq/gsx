# Interpolation

Interpolation embeds a Go value into the output using single braces: `{ expr }`. The expression is evaluated at render time; the result is written to the output with the appropriate escaper applied automatically for the context it sits in (HTML text, attribute value, URL, etc.).

## Basic interpolation

`{ expr }` is the core form. It works anywhere a text node can appear — between elements, inside text content, mixed with static text — and accepts any Go expression: a variable, a method call, an arithmetic expression, a type conversion.

<!--@include: ./_generated/interpolation/010-interpolation-props.md-->

The `{ name }` and `{ count }` expressions are evaluated against the component's params. String values are HTML-escaped (angle brackets, ampersands, and quotes are encoded); numeric values (`int`, `float64`, etc.) are converted to their decimal string representation without escaping, because digits and a decimal point carry no HTML-special meaning.

::: v-pre
Note that `{ expr }` is **interpolation** — it emits a value. It is not a Go statement block. To run a Go statement that produces no output, use `{{ stmt }}` (a GoBlock). See [Raw Go](./raw-go) for details.
:::

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
| Pipeline stage | each `|>` stage whose return is `(T, error)` |
| Children / slot body | `<Wrap>{ f(t) }</Wrap>` |
:::

When a child-component prop value returns `(T, error)`, gsx hoists the call to a temporary before building the component literal. When multiple props on the same component call each return a tuple, all are hoisted in source order:

<!--@include: ./_generated/interpolation/035-error-unwrap-childprop.md-->

**Rules:**

- **Only `(T, error)`.** Exactly two return values, with the second typed `error`. Any other multi-value shape — `(int, string)`, three values, etc. — is a **compile-time gsx error**: `only (T, error) is supported`.
- **Automatic — no marker or opt-out.** The unwrap is always applied; there is no annotation to add or remove.
- **`?` is a parse error.** A `?` try-marker suffix (e.g. `upper?` in a pipeline stage) is rejected: gsx reports that the `?` try-marker is not supported, because gsx already auto-unwraps `(T, error)` values.

## Numeric & string contexts

The escaper applied to `{ expr }` depends on **where** the interpolation appears, not on the type of the value:

- **Text content** (`<p>{ x }</p>`) — HTML-escapes the string form of `x`.
- **Attribute value** (`title={ x }`) — attribute-escapes the value.
- **URL attribute** (`href={ x }`, `src={ x }`, `action={ x }`, and htmx method attrs `hx-get`/`hx-post`/`hx-put`/`hx-delete`/`hx-patch`) — scheme-sanitizes and escapes. (`hx-on*` is a JS context; other `hx-*` attrs are plain text.)
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
