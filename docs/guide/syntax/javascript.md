# JavaScript

gsx integrates with JavaScript through attribute-local JavaScript literals,
`<script>` body interpolation, and JSON data islands. Dynamic values are escaped
for the JavaScript position they occupy, and `gsx.RawJS` remains the explicit
opt-in for trusted JavaScript inside interpolation holes.

## Attribute-local JavaScript

Use a `` js`...` `` literal when an attribute value is JavaScript:

````gsx
<button @click=js`open = !open`>Toggle</button>
<div x-data=js`{ open: false, initial: @{initial} }`>...</div>
````

`@{expr}` inside a `` js`...` `` literal inserts a Go value escaped for the
JavaScript position where the hole appears. A string becomes a quoted JavaScript
string literal, a number stays numeric, a struct or map becomes a JavaScript
object literal, and `gsx.RawJS` bypasses that encoding only for trusted values.

Quoted attributes remain literal strings. gsx does not scan quoted attributes for
`@{}` interpolation:

````gsx
<div x-data="{ open: false }">...</div>
````

For ordinary expression attributes, `attr={expr}` uses normal attribute escaping
unless the attribute is URL-context by name. Use `` js`...` `` when the value is
code or a JavaScript expression.

````gsx
<button @click=js`toggle()`>Toggle</button>
````

## `<script>` interpolation

Inside a `<script>` element, `@{ expr }` interpolates a Go value into the JavaScript body. The value is passed through the same JSON-encoding path as `jsValEscaper` from `html/template`: the result is a JSON literal that is also safe to embed inside an HTML `<script>` block — `</script>`, `<!--`, `-->`, and the Unicode line/paragraph separators U+2028/U+2029 are escaped so hostile input cannot terminate the script block or break JSON parsing.

A struct, map, slice, number, or boolean is marshaled to its JSON representation. A `gsx.RawJS` value bypasses marshaling and is emitted verbatim — useful when you have a pre-rendered JS expression you trust.

<!--@include: ./_generated/javascript/020-script-interpolation.md-->

`@{ state }` in the script body serializes the `AppState` struct as a JSON object. The resulting `const app = {"Tab":"settings","Open":true};` is valid JavaScript: the Go struct becomes a JS object literal that the script can read immediately without a separate JSON parse step.

## JSON data islands

A common pattern for passing server-side data to client-side JavaScript is a `<script type="application/json">` element. Because the MIME type is not `text/javascript`, browsers do not execute the content; client code reads it with `JSON.parse(document.getElementById("…").textContent)`. In gsx, `@{ expr }` inside such a block serializes the Go value as JSON, making the data island easy to write and safe to embed:

```gsx
<script type="application/json" id="cfg">@{ cfg }</script>
```

The data island is inert HTML — browsers parse it as text, not script — and the
`id="cfg"` attribute lets client JavaScript retrieve it with
`document.getElementById`.
