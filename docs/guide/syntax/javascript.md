# JavaScript

gsx supports JavaScript-valued attributes, script interpolation, and JSON data
islands. Use explicit JavaScript literals in attributes so code is distinguishable
from ordinary text.

## JavaScript-valued attributes {#attribute-local-javascript}

Use a `` js`...` `` literal for a handler or JavaScript expression:

<!--@include: ./_generated/javascript/030-attribute-local-js-handler.md-->

Inside the literal, `@{ expr }` inserts a Go value at a JavaScript value,
string, or regular-expression position. Use `js"..."` when the JavaScript itself
contains backticks. See [Attributes](./attributes.md#contextual-escaping)
for literal syntax and [Escaping](./escaping.md#javascript-and-css-contexts) for
the trust boundary.

Keep a `js` or `css` literal with `@{}` holes on the native element that
consumes it. A wrapper should accept ordinary props and build the contextual
literal at that destination:

```gsx
component SaveButton(id string) {
	<button @click=js`save(@{id})`>Save</button>
}

<SaveButton id={id}/>
```

On a component tag, a hole-free contextual literal may fall through as authored
text; a literal with holes is rejected.

## Alpine and htmx directives

Alpine directive values are JavaScript expressions, so mark `x-data`, `x-model`,
`x-for`, `x-text`, `@click`, and `:key` values with `js`. The same form works for
htmx attributes that contain JavaScript.

<!--@include: ./_generated/javascript/050-complete-alpine-search.md-->

## JSON attribute values

JSON is a subset of JavaScript, so attributes such as htmx's `hx-vals` use the
same literal:

<!--@include: ./_generated/javascript/055-json-attribute-values.md-->

Go strings, numbers, structs, maps, and slices in value-position holes are
encoded as JSON values. The rendered attribute is then HTML-escaped; the browser
restores the JSON text before the consumer reads it. Name keys in the literal
and place dynamic holes in value positions.

## `<script>` interpolation

Inside `<script>`, `@{ expr }` inserts a Go value in the surrounding JavaScript
context:

<!--@include: ./_generated/javascript/020-script-interpolation.md-->

Value-position interpolation produces JSON notation. String and
regular-expression positions receive their matching escapes, including escapes
that prevent input from ending the `<script>` element. `gsx.RawJS` bypasses
those protections and is only for JavaScript you trust; see
[Escaping](./escaping.md#trusted-value-helpers).

## JSON data islands

Use `<script type="application/json">` to expose server data without executing
it. Interpolation encodes the Go value as JSON, and client code can read the
element's text content and pass it to `JSON.parse`.

<!--@include: ./_generated/javascript/010-js-attributes-data-islands.md-->
