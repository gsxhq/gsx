# Escaping

gsx uses **escape-by-construction**: the code generator knows *where* every value appears (text node, attribute value, URL attribute, JS-context attribute, `<script>` body, `<style>` body) and emits the correct escaper automatically. You never call an escape function on your data; the template structure itself is the guarantee.

## Escape by construction

A plain `{ expr }` in body text or an attribute value is HTML-escaped. No XSS is possible through the normal interpolation path — hostile HTML in a user-supplied string is neutralized at render time.

<!--@include: ./_generated/escaping/010-auto-escaping-safe-raw.md-->

The `<img src=x onerror=alert(1)>` string is rendered as `&lt;img src=x onerror=alert(1)&gt;` — a harmless text node. The browser sees the literal characters, not an `<img>` element.

## Context-aware escaping

gsx applies a different escaper depending on the context the value sits in. Each context is safe by default; the opt-out helpers are for explicitly trusted values only.

| Context | Where it applies | What gsx does | Opt-out (trusted only) |
|---------|-----------------|---------------|------------------------|
| **Text / attribute** | `{ x }` in body; `attr={ x }` | HTML / attribute escape — `<`, `>`, `&`, `"` are entity-encoded | `gsx.Raw(s)` |
| **URL attribute** | `href`, `src`, `action`, `formaction`, `xlink:href`; htmx method attrs `hx-get` / `hx-post` / `hx-put` / `hx-delete` / `hx-patch` | Scheme-sanitize: non-allowlisted schemes (e.g. `javascript:`) are replaced with `about:invalid#gsx`; value is then attribute-escaped | `gsx.RawURL(s)` |
| **JS-context attribute** | `onclick`, `@click`, `hx-on*`, `x-data`, `x-init`, `x-show`, `x-if`, `x-effect`, `x-on:*`, `:*`; whole-value form `attr={ expr }` | JSON-encode the Go value to a safe JS literal — hostile input is neutralized, not blocked | `gsx.RawJS(s)` |
| **`<script>` body** | `@{ expr }` inside a `<script>` element | JSON-encode; also escapes `</script>`, `<!--`, U+2028/U+2029 so the value cannot terminate the script block | `gsx.RawJS(s)` |
| **JSON data island** | `@{ expr }` inside `<script type="application/json">` | JSON-encode the whole value | — |
| **CSS value** | `<style>` body; `style=` attr; composable `style={ … }` | CSS value-filter (`gw.CSS`): tokens containing `(` or `/` collapse to `ZgotmplZ` | `gsx.RawCSS(s)` |

### URL attributes

When a dynamic value lands in a URL attribute, gsx checks the scheme. Safe schemes (`http`, `https`, `mailto`, `tel`, relative paths, and a small allowlist) pass through; anything else — including `javascript:`, `data:`, and `vbscript:` — is replaced with the blocked-URL sentinel `about:invalid#gsx`. The value is still attribute-escaped after the scheme check, so it cannot break out of the surrounding quotes.

`gsx.RawURL` skips the scheme check entirely. The string is still attribute-escaped (it cannot inject new attributes or break the quote context), but any scheme — including `javascript:` — is preserved verbatim. Use only for URLs you have already validated.

### JS-context attributes

Attribute names that carry JavaScript — `onclick`, `@click` (Alpine shorthand), `hx-on*` (HTMX), `x-data`, `x-show`, and others listed in the table above — use `JSValAttr` to JSON-encode the value when written in whole-value form (`attr={ expr }`). This neutralizes hostile input: an XSS payload in a string becomes a JSON string literal, which the browser evaluates as a harmless value expression.

Because JSON-encoding a string like `"openMenu()"` produces `"openMenu()"` (a JS string, not a call), event handler values that must execute as code need `gsx.RawJS`:

```gsx
<button @click={ gsx.RawJS("openMenu()") }>Open</button>
```

The **only** case that is a compile error is an `@{ }` interpolation hole that lands in an **identifier or binding position** inside a JS-attribute string literal — for example, `x-on:click="@{ stmt } = 1"`. gsx rejects this because the hole is in a position that can only be an identifier, not a safe value. See the `jsattr/jsattr_identifier_rejected` corpus case.

### CSS values

CSS values are filtered through `gw.CSS` (ported from `html/template`). Any token containing `(` or `/` — as in `rgb(...)`, `calc(...)`, `url(...)` — collapses to the safe placeholder `ZgotmplZ`. This means a dynamically-built CSS value like `"color:red;background:url(javascript:alert(1))"` is replaced wholesale.

Use `gsx.RawCSS` for CSS values you trust — for example, a validated color string or a pre-built CSS expression:

```gsx
<div style={ gsx.RawCSS("color:rgb(0,128,0)") }>…</div>
```

## Opt-out helpers summary

All opt-out helpers are **for trusted values only**. They vouch that the string is safe for the target context and bypass the automatic safety check.

| Helper | Type | Skips |
|--------|------|-------|
| `gsx.Raw(s)` | `func(string) gsx.Node` | HTML escaping — emits string verbatim as a `gsx.Node` |
| `gsx.RawURL(s)` | `type RawURL string` | URL scheme check (still attribute-escaped) |
| `gsx.RawJS(s)` | `type RawJS string` | JSON-encoding in JS-context attrs and `<script>` `@{ }` |
| `gsx.RawCSS(s)` | `type RawCSS string` | CSS value-filter in `<style>` and CSS-context attrs |

See the `security/`, `style/`, `jsattr/`, and `datajson/` [corpus cases](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases) for exhaustive examples of each escaping context.
