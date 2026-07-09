# Raw HTML

By default, any Go value interpolated with `{ expr }` is HTML-escaped before it reaches the output. This keeps the template safe against injection: a string like `<img src=x onerror=alert(1)>` becomes `&lt;img src=x onerror=alert(1)&gt;` in the rendered page.

`gsx.Raw(s)` is the deliberate opt-out. It wraps a `string` in a `gsx.Node` that writes the bytes verbatim — no entity encoding, no escaping of any kind.

## Rendering trusted HTML

Use `{ gsx.Raw(html) }` when the string is already safe HTML that must be rendered as markup, not as escaped text. A typical source is a Markdown converter or a CMS that runs its own sanitization pass before handing you the HTML.

<!--@include: ./_generated/raw-html/010-raw-html.md-->

`{ gsx.Raw(html) }` emits the string as-is inside the `<article>` element. The `<em>` and `<strong>` tags appear as real elements in the rendered output, not as their escaped counterparts `&lt;em&gt;`.

## Security

`gsx.Raw` is the **escape-by-construction opt-out**, analogous to `templ.Raw`. The string bypasses all of gsx's safety machinery. Only call it on strings you control or have already validated and sanitized:

- Pre-rendered Markdown from a library that sanitizes its output
- HTML from a trusted CMS field with a fixed allow-list
- Static strings written directly in your own code

Never pass unvalidated user input to `gsx.Raw`. A hostile string like `<script>stealCookies()</script>` would be written verbatim into the page.

If the trusted value is a URL rather than HTML, use `gsx.RawURL`. For trusted JavaScript, use `gsx.RawJS`. For trusted CSS, use `gsx.RawCSS`. See [Escaping](./escaping.md) for the full context-aware escaping reference.
