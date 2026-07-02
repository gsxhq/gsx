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

| Context | Where it applies | What gsx does | Opt-out (trusted only) |
|---------|-----------------|---------------|------------------------|
| **Text / attribute** | `{ x }` in body; `attr={ x }` unless the attr is URL-context by name | HTML / attribute escape — `<`, `>`, `&`, `"`, `'` are entity-encoded; NUL is replaced with U+FFFD | `gsx.Raw(s)` |
| **URL attribute** | `href`, `src`, `action`, `formaction`, `poster`, `cite`, `ping`, `data`, `background`, `manifest`, `xlink:href`; htmx method attrs `hx-get` / `hx-post` / `hx-put` / `hx-delete` / `hx-patch` | Scheme-sanitize: non-allowlisted schemes (e.g. `javascript:`) are replaced with `about:invalid#gsx`; value is then attribute-escaped | `gsx.RawURL(s)` |
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

When a dynamic value lands in a URL attribute, gsx checks the scheme. Safe schemes (`http`, `https`, `mailto`, `tel`, relative paths, and a small allowlist) pass through; anything else — including `javascript:`, `data:`, and `vbscript:` — is replaced with the blocked-URL sentinel `about:invalid#gsx`. The value is still attribute-escaped after the scheme check, so it cannot break out of the surrounding quotes.

`gsx.RawURL` skips the scheme check entirely. The string is still attribute-escaped (it cannot inject new attributes or break the quote context), but any scheme — including `javascript:` — is preserved verbatim. Use only for URLs you have already validated.

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

### CSS values

CSS values are filtered through `gw.CSS` (ported from `html/template`). The filter is conservative and whole-value: if the value contains **any** of `(`, `)`, `/`, `'`, `"`, `;`, `@`, `[`, `\`, `]`, `` ` ``, `{`, `}`, `<`, `>`, NUL, or a `--` run — or if it decodes to the strings `expression` or `mozbinding` — the **entire value** is replaced with the safe placeholder `ZgotmplZ`. It is not a per-token substitution. This means common CSS functions like `rgb(...)`, `calc(...)`, and `url(...)` (which contain parentheses) are blocked wholesale; even a single unsafe character in any part of the value triggers the replacement.

Use `gsx.RawCSS` for CSS values you trust — for example, a validated color string or a pre-built CSS expression:

```gsx
<div style={ gsx.RawCSS("color:rgb(0,128,0)") }>…</div>
```

A `` css`...` `` literal can also be one contribution inside a composable
`style={...}` list. The braces are required in this form because the literal is
part of the style list, not the entire attribute value.

## Opt-out helpers summary

All opt-out helpers are **for trusted values only**. They vouch that the string is safe for the target context and bypass the automatic safety check.

| Helper | Type | Skips |
|--------|------|-------|
| `gsx.Raw(s)` | `func(string) gsx.Node` | HTML escaping — emits string verbatim as a `gsx.Node` |
| `gsx.RawURL(s)` | `type RawURL string` | URL scheme check (still attribute-escaped) |
| `gsx.RawJS(s)` | `type RawJS string` | JSON-encoding in `<script>` `@{ }` and `` js`...` `` holes |
| `gsx.RawCSS(s)` | `type RawCSS string` | CSS value-filter in `<style>`, composed style values, and `` css`...` `` holes |

See the `security/`, `style/`, `jsattr/`, and `datajson/` [corpus cases](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases) for exhaustive examples of each escaping context.
