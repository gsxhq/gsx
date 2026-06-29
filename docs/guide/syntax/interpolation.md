# Interpolation

Interpolation embeds a Go value into the output using single braces: `{ expr }`. The expression is evaluated at render time; the result is written to the output with the appropriate escaper applied automatically for the context it sits in (HTML text, attribute value, URL, etc.).

## Basic interpolation

`{ expr }` is the core form. It works anywhere a text node can appear — between elements, inside text content, mixed with static text — and accepts any Go expression: a variable, a method call, an arithmetic expression, a type conversion.

<!--@include: ./_generated/interpolation/010-interpolation-props.md-->

The `{ name }` and `{ count }` expressions are evaluated against the component's params. String values are HTML-escaped (angle brackets, ampersands, and quotes are encoded); numeric values (`int`, `float64`, etc.) are converted to their decimal string representation without escaping, because digits and a decimal point carry no HTML-special meaning.

Note that `{ expr }` is **interpolation** — it emits a value. It is not a Go statement block. To run a Go statement that produces no output, use `{{ stmt }}` (a GoBlock). See [Raw Go](./raw-go) for details.

## Fields & typed values

You can interpolate any Go expression, including field accesses on a struct passed as a param. The type does not need to be a string: any type that satisfies `fmt.Stringer` is formatted via its `String()` method, and numeric primitives are formatted directly.

<!--@include: ./_generated/interpolation/020-field-access.md-->

`{ user.Name }` and `{ user.Age }` access fields on the `User` struct param directly. There is no special accessor syntax — it is ordinary Go field access inside braces.

## Functions & `(T, error)` auto-unwrap

When an expression in `{ … }` is a function call returning `(T, error)`, the code generator automatically unwraps the tuple: it assigns the result to a temporary, checks the error, and if the error is non-nil, returns it from the enclosing `Render` call. No extra syntax is needed.

<!--@include: ./_generated/interpolation/030-function-t-error-unwrap.md-->

`lookup(key)` returns `(string, error)`. The generated code is equivalent to:

```go
_v, _err := lookup(key)
if _err != nil {
    return _err
}
// write _v to output
```

The caller of `Render` receives the error and can handle it (log, serve a 500, etc.). The same auto-unwrap applies in attribute values, `<script>` interpolation (`@{ expr }`), and `<style>` interpolation — anywhere `{ expr }` or `@{ expr }` is used.

## Numeric & string contexts

The escaper applied to `{ expr }` depends on **where** the interpolation appears, not on the type of the value:

- **Text content** (`<p>{ x }</p>`) — HTML-escapes the string form of `x`.
- **Attribute value** (`title={ x }`) — attribute-escapes the value.
- **URL attribute** (`href={ x }`, `src={ x }`, `action={ x }`, `hx-*`) — scheme-sanitizes and escapes.
- **`<script>` body** (`@{ x }`) — JSON-encodes the Go value to a safe JS literal.
- **`<style>` body** (`@{ x }`) — CSS value-filters the string.

Numeric values in text context (`{ count }` where `count` is `int`) are formatted as their decimal representation and do not require escaping — no HTML-special characters can appear in a plain integer string. This means numeric interpolation has no overhead from the escaper.

For a complete reference of escaping contexts and opt-out helpers (`gsx.Raw`, `gsx.RawURL`, `gsx.RawJS`, `gsx.RawCSS`), see [Escaping &amp; safe contexts](../syntax#escaping-safe-contexts).
