# Interpolation

Use `{ expr }` to write the result of a Go expression into markup. gsx applies
the safety rules for the surrounding text, attribute, URL, JavaScript, or CSS
context; see [Escaping](./escaping.md).

## Go expressions

Braces accept ordinary Go expressions: names, calls, arithmetic, conversions,
method calls, and element values.

<!--@include: ./_generated/interpolation/010-interpolation-props.md-->

`{ name }` writes a string and `{ count }` writes a number. A `gsx.Node` renders
as markup; other values are formatted according to their Go type.

## Interpolating body literals

Use an `f` literal inside braces to mix authored text with `@{ expr }` holes.
Each hole behaves like a separate `{ expr }` interpolation in that body
position.

<!--@include: ./_generated/interpolation/040-body-backtick-literals.md-->

The `f` prefix is required. An unprefixed Go raw string such as
`` {`plain @{text}`} `` contains no holes.

### Delimiters and literal `@{`

Both `` f`...` `` and `f"..."` create an interpolating literal. Choose the
delimiter that makes the content easiest to read.

- Inside a backtick-delimited literal, `` \` `` writes a literal backtick.
- `\@{` writes a literal `@{` instead of opening a hole.

### Using a literal as a Go value {#as-a-first-class-go-value}

An `f` literal is also a string expression, so it can initialize a variable or
be passed to a function:

```gsx
var name = "world"
var greeting = f`Hello, @{name}`

component Message() {
	<p>{ greeting }</p>
}
```

In direct body position, the surrounding text is authored markup and each hole
is handled separately. As a Go value, the literal produces one string; if that
string is later interpolated, the whole value follows the rules of its new
context.

## Fields and typed values

Field access uses ordinary Go syntax. Strings, booleans, numeric primitives,
`fmt.Stringer` values, and renderable nodes keep their Go types until they are
written.

<!--@include: ./_generated/interpolation/020-field-access.md-->

## Functions returning `(T, error)`

A call returning `(T, error)` needs no special marker. gsx writes the `T` value
when the error is nil; otherwise the component's `Render` returns that error.

<!--@include: ./_generated/interpolation/030-function-t-error-unwrap.md-->

Only the two-value `(T, error)` shape is supported. Other multi-value results
are reported as errors. To handle an error in the component instead of
returning it, use an explicit Go `if` statement; see
[Control flow](./control-flow.md#init-statements). The automatic rule applies
in every expression position; a pipeline applies it at any stage.

### Component props

The same rule applies when a function call supplies a component prop.

<!--@include: ./_generated/interpolation/035-error-unwrap-childprop.md-->

Multiple prop expressions are evaluated in source order, and the first
non-nil error stops rendering.

## Choosing braces

::: v-pre
| Task | Form |
|---|---|
| Write a Go value | `{ expr }` |
| Render an element or fragment | `<tag>...</tag>` or `<>...</>` |
| Run a Go statement without output | `{{ stmt }}` |
:::

Element literals can also appear inside Go expressions; see
[Elements as values](./elements.md#elements-as-values). For attribute values,
see [Attributes](./attributes.md). For statement blocks and their scopes, see
[Raw Go](./raw-go.md).
