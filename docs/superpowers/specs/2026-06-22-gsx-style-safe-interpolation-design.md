# Design: safe interpolation in `<style>` (and `style=`)

**Date:** 2026-06-22
**Status:** approved (brainstorm) — ready for writing-plans
**Scope:** `<style>` block interpolation + `style=` attribute interpolation, via one
shared CSS-safe primitive. `<script>` / `|> js` are explicitly out of scope.

## Goal

Let templates interpolate dynamic values into CSS — both inside `<style>` blocks and
in the `style=` attribute — **safely by default, with no ceremony for the common
case**. Today both are dead ends: `<style>` bodies are consumed verbatim (no
interpolation at all), and an interpolated `style=` value is a hard compile error
("needs a safe type via `|> css` (not available yet)").

## Threat model & principles

gsx's standing model: **the template author is trusted** (they write the CSS
structure), **the interpolated data is not**. Two principles, in order:

1. **Safe by default.** An untrusted value placed in CSS can never break out of its
   context (close the value/rule/`<style>`, inject `expression(...)`/`url(javascript:)`,
   etc.).
2. **Convenience where possible.** Where a value is safe by construction (numbers) or
   can be made safe automatically (CSS value filtering), require no extra syntax —
   exactly as gsx already auto-sanitizes URLs.

This supersedes the earlier "CSS fails closed, route through `|> css`" stance, which
existed only because no safe CSS filter had been built. We now build a real one and
make CSS behave like URLs.

## Surface (what the author writes)

**Delimiter — `#{ … }` (SCSS-style).** Inside `<style>`, `{` already means a CSS rule
block, so gsx's universal `{ expr }` cannot be reused without parsing CSS. `#{` never
occurs in plain CSS (hex colors are `#fff`, IDs `#id` — never `#` directly before `{`),
so it is unambiguous and instantly familiar to anyone who has used Sass. Whitespace is
flexible (`#{x}` ≡ `#{ x }`).

**Auto-sanitize (matches URL handling).** Every interpolation in a **CSS context** —
the `style=` attribute **or** anywhere inside a `<style>` block — is automatically run
through the CSS value-filter. No filter syntax, no annotation.

**`gsx.SafeCSS(x)` — the opt-out.** Wrapping a value in `gsx.SafeCSS` tells gsx the
author vouches for it as arbitrary CSS; it is emitted raw (the filter is skipped). This
is the CSS analogue of the (planned) `gsx.SafeURL`.

**Numbers** are safe by construction and emitted via `strconv` directly.

**JS/event context stays fail-closed.** `onclick=`, `@click`, `hx-on*` keep their
existing hard error — there is no safe auto-escaper for JS, and `|> js` is a separate
chapter.

```gsx
component Card(w int, userColor string, raw gsx.SafeCSS) {
	<style>
		.card {
			width:  #{ w }px;               /* int  → "123"            (strconv)   */
			color:  #{ userColor };          /* string → gw.CSS value-filter        */
			border: #{ gsx.SafeCSS("1px solid #000") };  /* author opt-out: raw     */
			margin: #{ raw };                /* SafeCSS param: raw                  */
		}
	</style>
	<div style={ "color: " + userColor }>…</div>   {/* style attr: auto-filtered  */}
}
```

`#{ userColor }` where `userColor = "red; } body { display:none"` → the value-filter
neutralizes it (the breakout `;`/`}` make it fail to the safe placeholder); the rule
cannot escape `.card { … }`.

## Component 1 — Parser: `#{ }` inside `<style>` only

`parser/markup.go` `parseRawTextBody` currently consumes a raw-text element's body
verbatim into a single `*ast.Text`. Change, **for `<style>` only** (`<script>` stays
fully verbatim):

- Scan the body, accumulating raw bytes into a pending `Text`. On `#{`:
  - flush the pending `Text` (if non-empty);
  - parse a `#{ … }` interpolation by reusing the existing interpolation
    expression-scanner (the same Go-string-aware, brace-depth-aware scan
    `parseInterp` uses, plus optional `|> …` pipeline stages) and emit an `*ast.Interp`;
  - resume raw accumulation after the matching `}`.
  - Continue until the matching case-insensitive `</style>`.
- `#{` is the **sole** interpolation trigger; every other `{`, `}`, `#`, `${`, `/* */`
  stays raw CSS verbatim.
- Because the expression scanner respects Go string literals, an interpolated
  `#{ "</style>" }` does not terminate the raw-text element (the `</style>` lives
  inside a Go string); raw-text termination logic is unchanged.

**No new AST node and no AST context flag.** A `<style>` body becomes the normal
`[]ast.Markup` of `Text` + `Interp`. "This interpolation is in CSS context" is
**positional** — derived from the enclosing `<style>` element by codegen and the
printer — mirroring how `style=` context is derived from the attribute name.

**Errors:** an unterminated `#{ … ` before `</style>` is a parse error with the
`#{`'s line:col; the existing unterminated-`<style>` error is unchanged.

## Component 2 — Runtime: `gsx.SafeCSS` + the CSS value-filter

Stdlib-only (runtime constraint is sacred). Two additions to the root `gsx` package:

- **`type SafeCSS string`** (new `safe.go`, or alongside `Raw` in `node.go`). The
  author-vouched safe-CSS string type. This establishes the safe-opt-out-string-type
  pattern; a parallel `SafeURL` (already referenced by example 02, not yet
  implemented) is left to a future slice.
- **The value-filter.** A pure `escape.go` function `cssValueFilter(s string) string`
  (sibling of `writeURL`'s helper). It is a **faithful port of the standard library's
  `html/template/css.go` `cssValueFilter`** and its helpers (`decodeCSS`,
  `isCSSNmchar`, `skipCSSSpace`, the `filterFailsafe` placeholder). That algorithm is
  the authority — we do not invent CSS-safety logic. Behavior: decode CSS escapes,
  then pass genuinely-safe value tokens (`10px`, `#fff`, `rgb(1,2,3)`, `color: red`)
  verbatim and replace anything carrying breakout potential
  (`} { ; < > " ' ( ) \  url( expression /* …`) with the safe placeholder.

**Two CSS contexts ⇒ two `*Writer` methods over the one filter**, mirroring how
`html/template` chains `cssValueFilter` with (only in attributes) `htmlEscaper`:
- **`func (gw *Writer) CSS(s string)`** — `<style>` raw-text block. Writes
  `cssValueFilter(s)` as-is. The filter already rejects `<` (so `</style>` can never
  appear), and the body is raw text, so **no** HTML escaping is applied (HTML-escaping
  would corrupt legitimate CSS).
- **`func (gw *Writer) CSSAttr(s string)`** — `style="…"` attribute. Writes
  `attrEscape(cssValueFilter(s))` — the filtered value, then the existing
  attribute-value escaping, so the result is also safe inside the double-quoted
  attribute (covers any residual `&`; the filter already fails on `"`).

## Component 3 — Codegen: one CSS-context emit path

`internal/codegen/emit.go`. `attrContext("style")` already returns `ctxCSS`. Add the
positional case: when emitting the children of a `<style>` element, those `Interp`
nodes are in CSS context (thread a small context flag through child emission).

Replace the current `ctxCSS → reject` with the **URL-context-style dispatch**, shared
by `style=` and `<style>` interps:

The two sub-contexts differ in their **outer** escaping, independent of the value's
trust: a `<style>` block is raw text (no HTML escaping), while a `style="…"` value is
always HTML-attr-escaped so it can never break the quote (CSS survives HTML-decoding).
The CSS **value-filter** applies only to untrusted data, never to vouched `SafeCSS`.
Per resolved type:

| value | inside `<style>` block | inside `style="…"` |
|---|---|---|
| `isSafeCSS(t)` (vouched) | raw (`string(expr)`) | `_gsxgw.AttrValue(string(expr))` — attr-escape only, no filter |
| numeric (`catInt/Uint/Float`) | `strconv`, raw | `strconv` via `_gsxgw.AttrValue` |
| string-like (`catString/Bytes/Stringer`) | `_gsxgw.CSS(…)` (filter) | `_gsxgw.CSSAttr(…)` (filter + attr-escape) |
| otherwise | compile error | compile error |

`isSafeCSS(t)` is a type-identity check (a `*types.Named` whose object is
`github.com/gsxhq/gsx.SafeCSS`).

`isSafeCSS` is a localized identity check (not a new global `classify` category), so a
`SafeCSS` value used outside CSS context keeps its ordinary string behavior. `ctxJS`
fail-closed is unchanged. The whole-attribute `?` try-marker remains unsupported in
this slice.

## Component 4 — Formatter / whitespace

`<style>` remains a **preserve / verbatim** context, but now holds mixed
`Text`+`Interp`:
- **`internal/wsnorm`**: preserve elements skip text normalization; confirm the
  preserve walk passes `Interp` children through untouched (it iterates children;
  Interps already pass through — add a guard/test so a future change can't normalize
  inside `<style>`).
- **`internal/printer`**: the preserve path emits `Text` verbatim and renders an
  `Interp` with the **`#{ expr }`** delimiter (positional: inside a `<style>`),
  versus the normal `{ expr }` elsewhere. Pipeline stages (`|> …`), if ever present,
  print as usual inside `#{ }`.
- The faithfulness + idempotence property tests (`render(fmt(S)) ≡ render(S)`,
  `fmt(fmt(S)) == fmt(S)`) extend to cover `<style>` interpolation via the corpus.

## Error handling (summary)

| Situation | Result |
|---|---|
| `#{ … ` unterminated before `</style>` | parse error, `#{` position |
| CSS-context value of non-renderable type | codegen error |
| Bare interpolation of untrusted string in CSS | **allowed** — auto-filtered (safe) |
| Interpolation in `onclick=`/`@`/`hx-on*` | unchanged hard error (fail-closed) |
| `<style>` unterminated | unchanged existing error |

## Testing

- **Parser** (`parser/markup_test.go`): `#{x}` adjacent to CSS braces
  (`.a{width:#{w}}`), multiple interps in one body, whitespace variants, unterminated
  `#{`, and negative cases proving `<script>` bodies, bare `{`/`}`/`#`, and `${` stay
  raw.
- **Runtime** (`escape_test.go`): port representative cases from the stdlib
  `html/template` css tests — safe tokens pass (`10px`, `#fff`, `rgb(1,2,3)`,
  `color:red`), breakouts neutralized (`}`, `;`, `url(javascript:…)`, `expression(`,
  `</style>`, escaped `\3c`/`\00003c`), idempotence of the placeholder.
- **Codegen** (corpus + unit): golden for `<style>` auto-sanitize (string→`gw.CSS`,
  numeric→`strconv`, `SafeCSS`→raw), `style={…}` now auto-sanitizes (update/replace any
  corpus/unit case that asserted the old rejection), `onclick={…}` still fails closed.
- **Formatter**: round-trip `<style>` with `#{ }` (faithfulness + idempotence over the
  corpus); a dedicated corpus case locks `#{ }` printing.

## Non-goals / future

- `<script>` interpolation and `|> js` (JS context is a harder safety problem — own
  design).
- `gsx.SafeURL` parity (same opt-out-type pattern; referenced by example 02, still
  unimplemented).
- Named CSS-value convenience types (e.g. a `Color`/`Length` type safe bare).
- A literal-`#{` escape inside `<style>` (does not occur in real CSS; add only if a
  concrete need appears).
- Auto-applying the filter to `style=` for non-string interpolations beyond the
  numeric/SafeCSS cases.

## Risks

- **`cssValueFilter` port fidelity** — the safety rests entirely on faithfully porting
  the stdlib algorithm. Mitigation: port `html/template/css.go` directly (helpers and
  all), and lift its test vectors. Do **not** approximate.
- **Raw-text scanner regressions** — adding `#{` handling must not change `<script>` or
  non-interpolated `<style>` behavior. Mitigation: `<style>`-only branch + negative
  tests; `<script>` path untouched.
- **Formatter faithfulness on preserve + interp** — the printer must emit `#{ }` in
  preserve context without re-indenting the surrounding raw CSS. Mitigation: the
  corpus-wide property tests are the guard.
