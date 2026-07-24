# Escaping

gsx escapes dynamic values for the place where they appear. Use ordinary Go
values by default, and use a trusted-value helper only when you have already
validated the value for that context.

## Escape by default

Plain `{ expr }` values in text and ordinary attributes are HTML-escaped, so
user input renders as text instead of markup.

<!--@include: ./_generated/escaping/010-auto-escaping-safe-raw.md-->

## Contexts at a glance

| Context | Write | Safety rule |
|---|---|---|
| Text or ordinary attribute | `{ value }`, `name={value}` | HTML-escape the value |
| URL attribute | `href={url}`, `src={url}` | Check the URL scheme, then HTML-escape it |
| JavaScript | `` name=js`...@{ value }...` ``, `@{ value }` in `<script>` | Encode each hole for its JavaScript position |
| CSS | `` name=css`...@{ value }...` ``, dynamic `<style>` or `style` values | Filter each value for its CSS position |

HTML escaping still applies around JavaScript, CSS, and URL attribute values so
a dynamic value cannot break out of the quoted attribute.

## URL attributes

URL attributes include `href`, `src`, `action`, `formaction`, `poster`, `cite`,
`ping`, `data`, `background`, `manifest`, `xlink:href`, `srcset`, and
`imagesrcset`. The htmx method attributes `hx-get`, `hx-post`, `hx-put`,
`hx-delete`, and `hx-patch` join this set when you enable the `htmx` URL preset;
see [Config](../config.md#url_presets-named-opt-in-rulesets).

Relative URLs and the `http`, `https`, `mailto`, and `tel` schemes pass through.
Other schemes, including `javascript:` and `vbscript:`, become
`about:invalid#gsx`.

Scheme checks apply to dynamic expressions, interpolated literals, and values
that arrive through a spread. A quoted URL authored directly on a native
element is trusted author text and is emitted without scheme validation.

### Resource and navigation URLs {#resource-vs-navigational-url-sinks}

`data:` URLs are blocked except in these image-rendering positions:

- `src` on `<img>`, `<source>`, and `<input>`
- `poster` on `<video>`
- `background` on any element

Those positions accept `data:` URLs for `image/png`, `image/jpeg`, `image/gif`,
`image/webp`, `image/avif`, and `image/svg+xml`, in either encoding:

- base64: `data:image/png;base64,…`
- plain-text: `data:image/svg+xml,%3Csvg…%3E`, optionally with a
  `;charset=utf-8` parameter. Every payload byte must be printable ASCII, with
  `%` allowed only as a two-hex-digit escape — percent-encoding is optional
  for other characters, so `data:image/svg+xml,<svg/>` is also accepted.

Other MIME types, other parameters, and payloads that fail these checks are
blocked. Navigation and active-content positions, such as `<a href>`,
`<iframe src>`, `<object data>`, and `<script src>`, always block `data:` URLs.

When a URL attribute value is a compile-time constant that its position always
blocks, `gsx generate` emits a warning naming the value and the fix, so the
failure surfaces before a browser renders a broken resource.

Use `gsx.RawURL` only for a URL you have validated yourself — including a
`data:` URL outside the rules above. It skips the scheme check but still
receives attribute escaping.

### `srcset` and `imagesrcset`

These attributes contain a list of image candidates rather than one URL. gsx
checks every candidate as an image URL. A disallowed candidate becomes
`about:invalid#gsx` without discarding the safe candidates; image `data:` URLs
and descriptors such as `1.5x` remain intact. `gsx.RawURL` vouches for the
entire candidate list.

### Meta refresh

Keep `http-equiv="refresh"` literal, or use a compile-time constant expression,
when `content` contains a dynamic refresh URL:

```gsx
<meta http-equiv="refresh" content={"0;url=" + next}/>
```

A runtime-dynamic or spread `http-equiv` value is not classified as refresh, so
do not combine it with an untrusted URL in `content`.

## JavaScript and CSS contexts

Embedded languages are explicit in attributes. Use a `` js`...` `` literal for
JavaScript and a `` css`...` `` literal for CSS; `@{ expr }` marks a dynamic
hole in either form. A plain quoted attribute stays literal, and an ordinary
`name={expr}` does not become JavaScript because of its name.

In `<script>`, gsx encodes interpolated Go values as JavaScript values and
escapes holes that appear in JavaScript strings or regular expressions for
those positions. In CSS contexts, unsafe dynamic values become the inert
`ZgotmplZ` value. Use `gsx.RawJS` or `gsx.RawCSS` only for code or CSS you trust.

See [JavaScript](./javascript.md) for handlers, Alpine, htmx, scripts, and JSON,
and [Styling](./styling.md) for CSS composition.

## CSP nonces

Put a per-request nonce on the render context with `gsx.WithNonce`. Every native
`<script>` and `<style>` tag rendered with that context receives the same
attribute, including external scripts and JSON data islands:

```go
nonce := newNonce() // Generate this with a cryptographically secure source.
w.Header().Set("Content-Security-Policy",
	"script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'")
page.Render(gsx.WithNonce(r.Context(), nonce), w)
```

An authored `nonce` attribute wins. That includes a conditional `nonce` when
its branch is active and a key spelled exactly `"nonce"` in a spread
`gsx.Attrs` bag. An empty or absent context nonce emits no attribute. Nonce
values are attribute-escaped, and `gsx.NonceFromContext` retrieves the current
value when markup outside gsx needs it.

Your server remains responsible for generating the nonce and sending the CSP
header.

## Trusted-value helpers

Each helper crosses a security boundary. Pass it only content you control or
have already validated for the named context.

| Helper | Trust granted |
|---|---|
| `gsx.Raw(html)` | Render trusted HTML without HTML escaping |
| `gsx.RawURL(url)` | Skip URL scheme checks; attribute escaping still applies |
| `gsx.RawJS(code)` | Emit trusted JavaScript without JavaScript encoding |
| `gsx.RawCSS(css)` | Emit trusted CSS without CSS value filtering |
