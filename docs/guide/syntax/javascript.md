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
code or a JavaScript expression, and a plain backtick literal — `` name=`…@{ x }…` ``,
no language prefix — when the value is ordinary text with holes rather than
code; see [Attributes — Interpolating attribute literals](./attributes#interpolating-attribute-literals).

````gsx
<button @click=js`toggle()`>Toggle</button>
````

<!--@include: ./_generated/javascript/030-attribute-local-js-handler.md-->

Alpine's directive values are JavaScript expressions. The explicit marker keeps
the intent local to the attribute instead of relying on names like `x-data`,
`@click`, or `:key`:

<!--@include: ./_generated/javascript/040-alpine-dropdown.md-->

For larger Alpine state objects, keep the JavaScript in a multiline `` js`...` ``
literal. Other embedded-language attributes can sit beside it, including
`` css`...` `` contributions inside composed `style={...}`:

<!--@include: ./_generated/javascript/050-complete-alpine-search.md-->

## JSON attribute values

Attributes like htmx's `hx-vals` expect a JSON object. Write them with the same
`` js`...` `` literal — JSON is a subset of JavaScript, and each `@{ }` hole is
encoded for the value position it occupies:

<!--@include: ./_generated/javascript/055-json-attribute-values.md-->

A Go value in a hole serializes to its JSON notation by default: a string
becomes a quoted JSON string, a number stays numeric, and a struct, map, or
slice is marshaled the way `encoding/json` would — so there is no need for a
manual "to JSON" helper on the Go side. The rendered attribute is HTML-escaped
as usual; once the browser un-escapes it, the consumer (htmx here) receives
valid JSON: `{"entity_type": "opportunity", "opts": {"page":"1"}}`.

Name the top-level JSON keys in the literal and put holes in value position.
A bare `` js`@{payload}` `` is rejected with `jsx-identifier-position` — the
escaper cannot prove a lone hole is a safe JavaScript value position — but a
whole map or struct works fine as the *value* of a named key, like `opts` in
the example above.

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

The full rendering — including combining an attribute-local JavaScript handler
and a data island in the same component — is shown below.

<!--@include: ./_generated/javascript/010-js-attributes-data-islands.md-->

The data island is inert HTML — browsers parse it as text, not script — and the
`id="cfg"` attribute lets client JavaScript retrieve it with
`document.getElementById`.
