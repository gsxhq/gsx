# Escaping

gsx escapes by position: the code generator knows *where* every value appears
(text node, attribute value, URL attribute, attribute-local JavaScript/CSS
literal, `<script>` body, `<style>` body) and emits the matching escaper
automatically.

## Escape by default

A plain `{ expr }` in body text or an attribute value is HTML-escaped. Hostile
HTML in a user-supplied string renders as text rather than elements.

<!--@include: ./_generated/escaping/010-auto-escaping-safe-raw.md-->

The `<img src=x onerror=alert(1)>` string is rendered as `&lt;img src=x onerror=alert(1)&gt;` — a harmless text node. The browser sees the literal characters, not an `<img>` element.

## Context-aware escaping

gsx applies a different escaper depending on the context the value sits in. Each context is safe by default; the opt-out helpers are for explicitly trusted values only.

A `[renderers]`-registered type ([Config](../config.md#renderers-type-directed-value-rendering)) is resolved to a renderable value *before* any of the escaping below runs — the renderer only decides what string/number a wrapper type like `pgtype.Text` renders as; the context table and its sanitization are unchanged.

| Context | Where it applies | What gsx does | Opt-out (trusted only) |
|---------|-----------------|---------------|------------------------|
| **Text / attribute** | `{ x }` in body; `attr={ x }` unless the attr is URL-context by name | HTML / attribute escape — `<`, `>`, `&`, `"`, `'` are entity-encoded; NUL is replaced with U+FFFD | `gsx.Raw(s)` |
| **Interpolating attribute literal** | `` attr=f`…@{ x }…` `` for a non-URL attribute; each hole | Same type-aware attribute escaping as `attr={ x }` (string/number/`fmt.Stringer`), applied per hole | — |
| **URL attribute** | `href`, `src`, `action`, `formaction`, `poster`, `cite`, `ping`, `data`, `background`, `manifest`, `xlink:href` (built-in; the htmx method attrs `hx-get`/`hx-post`/`hx-put`/`hx-delete`/`hx-patch` join this set only with `url_presets = ["htmx"]`, see [Config](../config.md#url_presets-named-opt-in-rulesets)) | Scheme-sanitize: non-allowlisted schemes (e.g. `javascript:`) are replaced with `about:invalid#gsx`; value is then attribute-escaped | `gsx.RawURL(s)` |
| **Attribute-local JavaScript** | `` attr=js`...` `` or `` attr={js`...`} ``; `@{ expr }` holes inside the literal | Preserve the surrounding JavaScript and escape each hole for its JavaScript position | `gsx.RawJS(s)` in a hole |
| **Attribute-local CSS** | `` attr=css`...` ``, `` attr={css`...`} ``, or a `` css`...` `` contribution inside `style={...}`; `@{ expr }` holes inside the literal | Preserve the surrounding CSS and filter each hole as a CSS value before attribute-escaping the result | `gsx.RawCSS(s)` in a hole |
| **`<script>` body** | `@{ expr }` inside a `<script>` element | JSON-encode; also escapes `</script>`, `<!--`, U+2028/U+2029 so the value cannot terminate the script block | `gsx.RawJS(s)` |
| **JSON data island** | `@{ expr }` inside `<script type="application/json">` | JSON-encode the whole value | — |
| **CSS value** | `<style>` body; composable `style={ … }` | Conservative CSS value-filter: replaces the **entire value** with `ZgotmplZ` if it contains `(`, `/`, `'`, `"`, `;`, `\`, `<`, `>`, or other unsafe chars, a `--` run, or the strings `expression`/`mozbinding` | `gsx.RawCSS(s)` |

`attr={expr}` is ordinary attribute escaping unless the attribute is URL-context
by name. JavaScript and CSS attribute values are explicit: use `` js`...` `` or
`` css`...` `` when the value should be parsed as embedded JavaScript or CSS.
Inside either literal, write `` \` `` for a literal backtick. The backslash only
escapes the gsx delimiter; the embedded JavaScript or CSS receives a plain
backtick.

### URL attributes

When a dynamic value lands in a URL attribute, gsx checks the scheme. Safe schemes (`http`, `https`, `mailto`, `tel`, relative paths, and a small allowlist) pass through; anything else — including `javascript:` and `vbscript:` — is replaced with the blocked-URL sentinel `about:invalid#gsx`. The value is still attribute-escaped after the scheme check, so it cannot break out of the surrounding quotes. `data:` is blocked here too, **except** on the narrow set of image-rendering sinks described in [Resource vs navigational URL sinks](#resource-vs-navigational-url-sinks) below.

`gsx.RawURL` skips the scheme check entirely. The string is still attribute-escaped (it cannot inject new attributes or break the quote context), but any scheme — including `javascript:` — is preserved verbatim. Use only for URLs you have already validated.

### Resource vs navigational URL sinks

Not every URL attribute carries the same risk, so gsx splits URL attributes into two sink tiers and applies a different allow-list to each:

| Sink | Attributes | `data:` allowed? |
|------|-----------|-------------------|
| **Image sink** | `src` on `<img>`, `<source>`, `<input>`; `poster` on `<video>`; `background` (any tag) | `data:image/*`, narrowed to an allow-list (below) |
| **Strict sink** (everything else) | `href`, `action`, `formaction`, `ping`, `cite`, `data` (`<object>`), `manifest`, `xlink:href`, and `src`/`poster` on any other tag (`<script src>`, `<iframe src>`, `<embed src>`, `<video src>`, `<audio src>`, …) — plus the htmx method attributes (`hx-get`, `hx-post`, `hx-put`, `hx-delete`, `hx-patch`) when `url_presets = ["htmx"]` is opted in | never — blocked to `about:invalid#gsx`, same as `html/template` |

On an image sink, gsx additionally accepts a `data:` URL whose MIME type is one of `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `image/avif`, or `image/svg+xml` (matched case-insensitively) **and** which carries the `;base64,` marker. The `;base64,` requirement isn't cosmetic: it constrains the payload to the base64 character set (`[A-Za-z0-9+/=]`), which cannot encode a scheme-breaking character. Any other `data:` MIME, or a `data:` URL missing the marker, is blocked to `about:invalid#gsx` on an image sink exactly as it would be anywhere else.

Why the split is safe: `<img>`/`<source>`/`background` render their target as an inert raster — or, for `image/svg+xml`, as an SVG document in the browser's restricted image mode (no script execution, no external subresource fetches). `<iframe>`, `<object>`, and `<embed>` are different: they can load a live, scriptable document, so `data:` — including `data:image/svg+xml` — stays blocked on those strict sinks. This is also why `<video poster>` (a still-image preview, image sink) and `<video src>` (a fetched media resource, strict sink) get different treatment on the same element.

This is a deliberate divergence from `html/template`, which blocks `data:` on every URL attribute uniformly with no resource/navigational distinction. gsx narrows the exception to tag+attribute combinations that are provably inert, and to an explicit image-MIME allow-list, rather than opening `data:` up everywhere. See [Attributes — `data:image` literals](./attributes.md#data-image-literals) for the author-facing syntax, and [Pipelines](./pipelines.md) for the `dataURL` filter.

For anything the allow-list refuses — an exotic MIME on an image sink, or a `data:` URL you have separately validated for a strict sink — `gsx.RawURL` remains the escape hatch: it skips the scheme check entirely (the value is still attribute-escaped) and is the author's explicit vouch that the URL is safe.

### Interpolating attribute literals

An `f`-prefixed literal in attribute-value position —
`` name=f`…@{ expr }…` `` or `name=f"…@{ expr }…"` — mixes static text with
`@{ expr }` holes; see [Attributes — Interpolating
attribute literals](./attributes.md#interpolating-attribute-literals) for the full
syntax and examples. Two characters need escaping inside the literal: the
active delimiter (`` \` `` or `\"`) for a literal delimiter character, and
`\@{` for a literal `@{` that should not open a hole. Both mirror the
escaping rules for `` js`...` ``/`` css`...` `` literals below. The
`` f`…` `` and `f"…"` forms behave identically; the `"` form is the
escape-hatch for content that already contains a backtick.

When the attribute is URL-context by name, the literal's static text and every
hole are assembled into one string *before* the scheme check runs, so a
dangerous scheme cannot be smuggled in by splitting it across a hole boundary
— the whole value is sanitized once, the same way `href={ expr }` sanitizes
its single expression. `gsx.RawURL` is not usable inside a hole; write the
value as an ordinary expression attribute — `` href={ gsx.RawURL(trustedURL) } ``
— when you need to bypass the scheme check for a value you have already
validated.

### Attribute-local JavaScript

JavaScript in attributes is opt-in with a `` js`...` `` literal:

````gsx
<button @click=js`openMenu(@{id})`>Open</button>
````

Each `@{ }` hole is escaped for its JavaScript position. `gsx.RawJS` can be used
inside a hole to emit trusted JavaScript verbatim; never wrap untrusted input in
it. Quoted attributes are literal strings, so `x-data="{ open: @{open} }"` emits
the characters `@{open}` instead of interpolating.

Write `` \` `` when the JavaScript itself needs a backtick:

````gsx
<button @click=js`save(\`draft @{id}\`)`>Save</button>
````

Or reach for the `"`-delimited form instead — the escape-hatch for content
that already contains a backtick, which is common for JS template literals:

```gsx
component Button(x string) {
	<button @click=js"const t = `hi @{x}`; send(t)">Save</button>
}
```

renders `` <button @click="const t = `hi abc`; send(t)">Save</button> `` for
`x = "abc"` — the backtick passes through literally and `@{x}` is still the
gsx hole. `` css`...` `` accepts the same `"`-delimited form.

### CSS values

CSS values are filtered through `gw.CSS` (ported from `html/template`). The filter is conservative and whole-value: if the value contains **any** of `(`, `)`, `/`, `'`, `"`, `;`, `@`, `[`, `\`, `]`, `` ` ``, `{`, `}`, `<`, `>`, NUL, or a `--` run — or if it decodes to the strings `expression` or `mozbinding` — the **entire value** is replaced with the safe placeholder `ZgotmplZ`. It is not a per-token substitution. This means common CSS functions like `rgb(...)`, `calc(...)`, and `url(...)` (which contain parentheses) are blocked wholesale; even a single unsafe character in any part of the value triggers the replacement.

Use `gsx.RawCSS` for CSS values you trust — for example, a validated color string or a pre-built CSS expression:

```gsx
<div style={ gsx.RawCSS("color:rgb(0,128,0)") }>…</div>
```

A `` css`...` `` literal can also be one contribution inside a composable
`style={...}` list. The braces are required in this form because the literal is
part of the style list, not the entire attribute value.

### CSP nonces

Store the per-request CSP nonce on the render context with `gsx.WithNonce`.
Every `<script>` and `<style>` tag gsx renders with that context automatically
carries the matching `nonce` attribute:

```go
func withCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := newNonce() // yours: e.g. 128-bit crypto/rand, base64
		w.Header().Set("Content-Security-Policy",
			"script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'")
		next.ServeHTTP(w, r.WithContext(gsx.WithNonce(r.Context(), nonce)))
	})
}
```

```gsx
component Page() {
	<script>
		init()
	</script>
}
```

renders as `<script nonce="…">…</script>` — no template changes needed.

The rules:

- Every native `<script>` and `<style>` tag qualifies: inline, external
  (`src=…`), and JSON data islands alike. Adding a nonce where CSP ignores it
  is harmless, and uniformity keeps the rule predictable.
- **An author-written `nonce` always wins.** Writing `nonce={expr}` — or a
  conditional `{ if c { nonce="…" } }` — anywhere on the tag turns
  auto-injection off for that tag entirely.
- **A spread bag carrying a `"nonce"` key wins too**: `<script { attrs... }>`
  is only auto-decorated when the bag has no `nonce` entry. The guard matches
  the canonical lowercase key exactly — bag keys are trusted developer input
  (see the `gsx.Attrs` contract), so write the key as lowercase `nonce`.
- The value is attribute-escaped like any quoted attribute; an absent or empty
  context nonce emits nothing (output is byte-identical to not using the
  feature).
- `gsx.NonceFromContext(ctx)` reads the nonce back when you need it by hand
  (e.g. for markup gsx does not own, like `gsx.Raw`).

gsx does not generate nonce values and does not build the
`Content-Security-Policy` header — both stay in your server, as in the
middleware above.

## Opt-out helpers summary

All opt-out helpers are **for trusted values only**. They vouch that the string is safe for the target context and bypass the automatic safety check.

| Helper | Type | Skips |
|--------|------|-------|
| `gsx.Raw(s)` | `func(string) gsx.Node` | HTML escaping — emits string verbatim as a `gsx.Node` |
| `gsx.RawURL(s)` | `type RawURL string` | URL scheme check (still attribute-escaped) |
| `gsx.RawJS(s)` | `type RawJS string` | JSON-encoding in `<script>` `@{ }` and `` js`...` `` holes |
| `gsx.RawCSS(s)` | `type RawCSS string` | CSS value-filter in `<style>`, composed style values, and `` css`...` `` holes |

See the `security/`, `style/`, `jsattr/`, and `datajson/` [corpus cases](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases) for exhaustive examples of each escaping context.
