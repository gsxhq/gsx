# JavaScript

gsx integrates with JavaScript through three mechanisms: event-handler attributes, `<script>` body interpolation, and JSON data islands. All three share the same safety model — values are JSON-encoded by default and `gsx.RawJS` is the explicit opt-in for trusted, verbatim JavaScript.

## Event handler attributes & `gsx.RawJS`

Attribute names that carry JavaScript — `onclick`, `@click` (Alpine shorthand), `hx-on:*` (HTMX), `x-data`, `x-show`, and others — are classified as **JS-context** attributes. In whole-value form (`attr={ expr }`), the value is JSON-encoded and HTML-attribute-escaped before being written to the output. This is the right behavior for data-binding attributes like `x-data={ someStruct }` or `x-show={ open }`, where the value is data consumed by a framework, not code to execute.

Event handlers (`onclick`, `@click`, `hx-on:*`) are different: their value is JavaScript code, not data. JSON-encoding a string like `"openMenu()"` produces `"openMenu()"` (with quotes), which a JS engine evaluates as a string expression — harmless, but inert. To emit a value verbatim as JavaScript, wrap it in `gsx.RawJS`:

```gsx
@click={ gsx.RawJS("openMenu()") }
```

`gsx.RawJS` is a named string type — the `type RawJS string` declaration in the runtime. Passing it to a JS-context attribute (or `@{ }` in a `<script>` body) bypasses JSON encoding and emits the string as-is. It is the author's explicit vouch that the string is trusted JavaScript; never wrap untrusted user input.

<!--@include: ./_generated/javascript/030-rawjs-event-handler.md-->

The `@click` attribute carries `openMenu()` verbatim in the output. Without `gsx.RawJS`, the string would be JSON-encoded to `"openMenu()"` — a valid JSON string but not a callable expression.

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

The full rendering — including combining an event handler and a data island in the same component — is shown below.

<!--@include: ./_generated/javascript/010-js-attributes-data-islands.md-->

`Widget` combines both mechanisms: `@click={ gsx.RawJS("toggle()") }` emits the click handler verbatim, and `@{ cfg }` in the `<script type="application/json">` body serializes the `Config` struct as `{"Env":"prod","Beta":true}`. The data island is inert HTML — browsers parse it as text, not script — and the `id="cfg"` attribute lets client JavaScript retrieve it with `document.getElementById`.
