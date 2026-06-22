# Design: safe interpolation in `<style>` (and `style=`)

**Date:** 2026-06-22
**Status:** approved (brainstorm) — ready for writing-plans
**Scope:** `<style>` block interpolation + `style=` attribute interpolation (via one
shared CSS-safe primitive), plus codegen-time minification of `<style>` **and**
`<script>` static content — each with a robust built-in default + a pluggable extension
point. `<script>` *interpolation* and the `|> js` value pipeline are out of scope.

**Implementation slices:** (1) the interpolation + safety core (Components 1–4);
(2) CSS minification + the shared minifier-option plumbing (Component 5); (3) JS
minification (Component 5), independent of (2). Slices build on the `<style>`/`<script>`
emit path.

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

**Delimiter — `${ … }` (JS-template-literal style).** Inside `<style>`, `{` already
means a CSS rule block, so gsx's universal `{ expr }` cannot be reused without parsing
CSS. `${` never occurs in plain CSS — `$` appears only in the `[attr$="x"]`
ends-with attribute selector, which is `$=`, never `$` directly before `{` — so `${` is
unambiguous, and it is the interpolation form JS/shell authors already know. Whitespace
is flexible (`${x}` ≡ `${ x }`).

**Migrated to `@{ }`** (2026-06-23, slice A) to avoid the JS template-literal `${}` collision — see `2026-06-23-gsx-js-interpolation-design.md`.

**Auto-sanitize (matches URL handling).** Every interpolation in a **CSS context** —
the `style=` attribute **or** anywhere inside a `<style>` block — is automatically run
through the CSS value-filter. No filter syntax, no annotation.

**`gsx.RawCSS(x)` — the opt-out.** Wrapping a value in `gsx.RawCSS` tells gsx the
author vouches for it as arbitrary CSS; it is emitted raw (the filter is skipped). This
is the CSS analogue of the (planned) `gsx.RawURL`.

**Numbers** are safe by construction and emitted via `strconv` directly.

**JS/event context stays fail-closed.** `onclick=`, `@click`, `hx-on*` keep their
existing hard error — there is no safe auto-escaper for JS, and `|> js` is a separate
chapter.

```gsx
component Card(w int, userColor string, raw gsx.RawCSS) {
	<style>
		.card {
			width:  ${ w }px;               /* int  → "123"            (strconv)   */
			color:  ${ userColor };          /* string → gw.CSS value-filter        */
			border: ${ gsx.RawCSS("1px solid #000") };  /* author opt-out: raw     */
			margin: ${ raw };                /* RawCSS param: raw                  */
		}
	</style>
	<div style={ "color: " + userColor }>…</div>   {/* style attr: auto-filtered  */}
}
```

`${ userColor }` where `userColor = "red; } body { display:none"` → the value-filter
neutralizes it (the breakout `;`/`}` make it fail to the safe placeholder); the rule
cannot escape `.card { … }`.

## Component 1 — Parser: `${ }` inside `<style>` only

`parser/markup.go` `parseRawTextBody` currently consumes a raw-text element's body
verbatim into a single `*ast.Text`. Change, **for `<style>` only** (`<script>` stays
fully verbatim):

- Scan the body, accumulating raw bytes into a pending `Text`. On `${`:
  - flush the pending `Text` (if non-empty);
  - parse a `${ … }` interpolation by reusing the existing interpolation
    expression-scanner (the same Go-string-aware, brace-depth-aware scan
    `parseInterp` uses, plus optional `|> …` pipeline stages) and emit an `*ast.Interp`;
  - resume raw accumulation after the matching `}`.
  - Continue until the matching case-insensitive `</style>`.
- `${` is the **sole** interpolation trigger; every other `{`, `}`, `#`, bare `$`,
  `#{`, `/* */` stays raw CSS verbatim.
- Because the expression scanner respects Go string literals, an interpolated
  `${ "</style>" }` does not terminate the raw-text element (the `</style>` lives
  inside a Go string); raw-text termination logic is unchanged.

**No new AST node and no AST context flag.** A `<style>` body becomes the normal
`[]ast.Markup` of `Text` + `Interp`. "This interpolation is in CSS context" is
**positional** — derived from the enclosing `<style>` element by codegen and the
printer — mirroring how `style=` context is derived from the attribute name.

**Errors:** an unterminated `${ … ` before `</style>` is a parse error with the
`${`'s line:col; the existing unterminated-`<style>` error is unchanged.

## Component 2 — Runtime: `gsx.RawCSS` + the CSS value-filter

Stdlib-only (runtime constraint is sacred). Two additions to the root `gsx` package:

- **`type RawCSS string`** (new `safe.go`, or alongside `Raw` in `node.go`). The
  author-vouched safe-CSS string type. This establishes the safe-opt-out-string-type
  pattern; a parallel `RawURL` (already referenced by example 02, not yet
  implemented) is left to a future slice.
- **The value-filter.** A pure `escape.go` function `cssValueFilter(s string) string`
  (sibling of `writeURL`'s helper). It is a **faithful port of the standard library's
  `html/template/css.go` `cssValueFilter`** and its helpers (`decodeCSS`,
  `isCSSNmchar`, `skipCSSSpace`, `hexDecode`/`isHex`, the `ZgotmplZ` failsafe). That
  algorithm is the authority — we do not invent CSS-safety logic. Behavior: decode CSS
  escapes, then pass genuinely-safe value tokens (`10px`, `#fff`, `#123456`, `100%`,
  `-moz-corner-radius`, `color: red`) verbatim and replace the whole value with the
  `ZgotmplZ` failsafe if it contains ANY of `0x00 " ' ( ) / ; @ [ \ ] \` { } < >`, a
  `--`/`<!--`/`-->` run, or (after escape-decoding + lowercasing) `expression` or
  `mozbinding`. **Note the conservatism:** parenthesized values like `rgb(1,2,3)` and
  slash values like `12px/1.5` are *rejected* by this filter (they contain `(`/`/`); for
  those, the author uses `gsx.RawCSS`. gsx's `gw.CSS` drops html/template's
  `stringify`/`contentTypeCSS` typed-value machinery — it always receives a plain
  untrusted `string` (the `RawCSS` opt-out is decided at codegen, never reaching the
  filter).

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
The CSS **value-filter** applies only to untrusted data, never to vouched `RawCSS`.
Per resolved type:

| value | inside `<style>` block | inside `style="…"` |
|---|---|---|
| `isRawCSS(t)` (vouched) | raw (`string(expr)`) | `_gsxgw.AttrValue(string(expr))` — attr-escape only, no filter |
| numeric (`catInt/Uint/Float`) | `strconv`, raw | `strconv` via `_gsxgw.AttrValue` |
| string-like (`catString/Bytes/Stringer`) | `_gsxgw.CSS(…)` (filter) | `_gsxgw.CSSAttr(…)` (filter + attr-escape) |
| otherwise | compile error | compile error |

`isRawCSS(t)` is a type-identity check (a `*types.Named` whose object is
`github.com/gsxhq/gsx.RawCSS`).

`isRawCSS` is a localized identity check (not a new global `classify` category), so a
`RawCSS` value used outside CSS context keeps its ordinary string behavior. `ctxJS`
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
  `Interp` with the **`${ expr }`** delimiter (positional: inside a `<style>`),
  versus the normal `{ expr }` elsewhere. Pipeline stages (`|> …`), if ever present,
  print as usual inside `${ }`.
- The faithfulness + idempotence property tests (`render(fmt(S)) ≡ render(S)`,
  `fmt(fmt(S)) == fmt(S)`) extend to cover `<style>` interpolation via the corpus.

## Component 5 — CSS & JS minification (built-in safe default + extension point)

`<style>` and `<script>` content is emitted verbatim today (a `Text` child →
`_gsxgw.S(quoted)`), so the source CSS/JS — indentation, comments, blank lines and all —
ships into every rendered page. This component minifies that **static** content at
codegen time. Both languages follow the **same shape**: a built-in robust/stable safe
minifier on by default, plus a pluggable extension point for an aggressive
(value-rewriting / "obfuscating") minifier. It is a **separable implementation slice**
(the interpolation core, Components 1–4, ships first), and CSS and JS are themselves
independent sub-slices.

**Asymmetry to keep in mind:** CSS gains interpolation in this design, so a `<style>`
body can be *holey* (`Text`+`Interp`) and its minifier must be hole-aware. `<script>`
interpolation is deferred, so static JS is always **holeless** — the JS minifier needs
no hole/opaque-token logic (until a future `<script>`-interpolation chapter adds it).

### The robust/stable CSS default — `internal/cssmin`

A new codegen-time package (the minifier runs in the generator, not the stdlib-only
runtime — though it is itself written stdlib-only). It is a **real CSS tokenizer**, never
regex. It performs only the transformations that are *guaranteed* not to change
rendering — the cross-tool "safe" set (tdewolff "no structural changes",
clean-css Level 1 minus value rewrites, esbuild whitespace-only):

- strip comments **except** `/*! … */` (and preserve the legacy `>/**/` and IE5/Mac
  adjacency hacks);
- collapse insignificant whitespace runs to a single space, and remove whitespace
  adjacent to `, : ; { } ( )` and around the `>`/`+`/`~` combinators and `*`/`/` in
  math functions;
- drop the redundant trailing `;` before `}` (the "queued semicolon" technique);
- trim leading/trailing whitespace of declaration values; unquote `url()` when safe.

It performs **no value rewrites** — explicitly **not** `0px`→`0` (breaks `@keyframes`
`%`, `<time>`, `flex-basis`), color shortening, longhand→shorthand, dedup, or any
rule merging/reordering. Those are the aggressive tier, available only via the
extension point.

**Tokenizer must preserve significant whitespace:** inside string literals
(`content: "  "`), inside/around `url(...)`, the **descendant combinator** (`a b`,
≥1 space — never zero), between adjacent ident/number/dimension tokens
(`margin: 1px 2px`), **≥1 space around binary `+`/`-` in `calc()`/`min`/`max`/`clamp`**
(`calc(50% - 8px)`), and all interior whitespace of custom-property (`--*`) values
(including the valid empty value `--x: ;`; note that a whitespace-only custom-property value collapses to `--x:` — rendering-equivalent, since CSS trims leading/trailing whitespace before `var()` substitution).

**Hole-aware.** The minifier operates on the `<style>` child list (`Text`+`Interp`),
treating each `${ expr }` `Interp` as a single **opaque token**: whitespace immediately
adjacent to a hole is never collapsed or trimmed, and no token/merge reasoning crosses a
hole. This guarantees `margin: ${a} ${b}` (two values), `color: ${c}`, and
`width: calc(${a} - ${b})` stay correct regardless of the runtime value.

### The robust/stable JS default — `internal/jsmin`

A parallel codegen-time package (stdlib-only) for `<script>` static content. A **real JS
tokenizer**, never regex — JS minification is *harder* to make stable than CSS, so the
tokenizer must correctly handle: **automatic semicolon insertion (ASI)** — never remove
a newline that terminates a statement; **regex literals vs the `/` divide operator**
(disambiguated by preceding-token context); **string and template literals** (incl.
`${…}` *inside* a JS template literal — which is JS syntax, unrelated to gsx's `<style>`
delimiter — and nested templates); and line comments whose terminating newline may be
ASI-significant. The safe set: strip comments (keep `/*! … */`), collapse insignificant
whitespace, but **preserve every newline that could trigger ASI** and all literal
interiors. **No** identifier mangling, no value rewriting, no statement reordering —
those are the aggressive ("obfuscation") tier behind the extension point. Static JS is
always holeless (no `<script>` interpolation yet), so no opaque-token logic is needed.

### The extension points — `gen.WithCSSMinifier` / `gen.WithJSMinifier`

Two parallel functional options mirroring `gen.WithFilters`:

```go
gen.Main(
	gen.WithCSSMinifier(func(css string) (string, error) { … }),  // e.g. wrap tdewolff
	gen.WithJSMinifier(func(js string) (string, error) { … }),     // e.g. wrap esbuild
)
```

Both use the universal minifier shape (`func(src string) (string, error)`) so any
whole-buffer minifier (tdewolff, Lightning CSS, esbuild, …) drops in via a small adapter.
Each defaults to its built-in safe minifier. The option values thread from `gen` through
to codegen alongside the filter table.

**Boundary (load-bearing):** a pluggable minifier is invoked **only on fully-static
(holeless) blocks**, where it receives complete, syntactically valid source. **Holey
`<style>` blocks always use the built-in hole-aware CSS minifier** — an external
string→string minifier cannot reason across holes safely, so hole structure never
crosses the public boundary. (`<script>` is always holeless, so its extension point has
no such restriction yet.) Aggressive minification is thus available for static
stylesheets/scripts; interpolated CSS gets the safe pass. Minification is **on by
default** for both languages (per the robust-default principle); a no-op minifier option
expresses "off".

### Orthogonality

Minification is a **codegen-output** transform only. `gsx fmt` and the source `.gsx`
are untouched — the author keeps readable, indented CSS/JS; the *generated* `.x.go`
carries the minified form. Existing `<style>`/`<script>` codegen goldens shrink
accordingly.

## Error handling (summary)

| Situation | Result |
|---|---|
| `${ … ` unterminated before `</style>` | parse error, `${` position |
| CSS-context value of non-renderable type | codegen error |
| Bare interpolation of untrusted string in CSS | **allowed** — auto-filtered (safe) |
| Interpolation in `onclick=`/`@`/`hx-on*` | unchanged hard error (fail-closed) |
| `<style>` unterminated | unchanged existing error |

## Testing

- **Parser** (`parser/markup_test.go`): `${x}` adjacent to CSS braces
  (`.a{width:${w}}`), multiple interps in one body, whitespace variants, unterminated
  `${`, and negative cases proving `<script>` bodies, bare `{`/`}`/`#`/`$`, and `#{` stay
  raw.
- **Runtime** (`escape_test.go`): port the stdlib `html/template` `TestCSSValueFilter`
  vectors — safe tokens pass (`10px`, `#fff`, `#123456`, `100%`, `+.33em`,
  `-moz-corner-radius`, `color: red`, `U+00-FF, U+980-9FF`), breakouts → `ZgotmplZ`
  (`<!--`, `-->`, `</style`, `"`, `'`, `` ` ``, `\x00`, `/* */`, `//`, `(`/`)`, `;`,
  `expression(…)`, `-moz-binding`, escaped `-express\69on`, `@import url evil.css`,
  `<`, `>`). Confirm a `RawCSS`-vouched value bypasses the filter at the codegen level.
- **Codegen** (corpus + unit): golden for `<style>` auto-sanitize (string→`gw.CSS`,
  numeric→`strconv`, `RawCSS`→raw), `style={…}` now auto-sanitizes (update/replace any
  corpus/unit case that asserted the old rejection), `onclick={…}` still fails closed.
- **Formatter**: round-trip `<style>` with `${ }` (faithfulness + idempotence over the
  corpus); a dedicated corpus case locks `${ }` printing.
- **Minifier** (`internal/cssmin` unit tests): a golden corpus of the historical
  naive-minifier breakages that the safe pass must NOT change semantics on — `calc(50%
  - 8px)` spacing, empty custom property `--x: ;` (acceptable to collapse to `--x:` — rendering-equivalent), data-URI with spaces, `url() format()`
  spacing, `@media … and (…)`, `grid-template-areas` rows, IE `*`/`_` hacks, `/*!`/`>/**/`
  comment hacks, descendant combinator vs `.a.b`, `content:"  "` strings, `An+B`
  (`2n + 1`), `unicode-range` — plus gsx hole cases (`margin: ${a} ${b}` keeps both
  spaces, `width: calc(${a} - ${b})` no cross-hole merge, `${sel} { … }`). Verify
  idempotence (`min(min(x)) == min(x)`) and that `WithCSSMinifier` is invoked only for
  holeless blocks (and never sees a hole).
- **JS minifier** (`internal/jsmin` unit tests): a golden corpus of ASI and
  literal-sensitive cases the safe pass must NOT break — `return\n a+b` (newline kept,
  ASI), `a = b\n/foo/.test(c)` (regex vs divide), `let x = `a ${b} c`` (template-literal
  interior + JS `${}`), nested templates, `i++\n++j` (ASI), a `//` line comment whose
  newline is ASI-significant, `/*! banner */` kept, and a div-vs-regex case
  (`a / b / c`). Idempotence, and `WithJSMinifier` invoked on the holeless static script.

## Non-goals / future

- `<script>` *interpolation* and the `|> js` value pipeline (JS *escaping* in dynamic
  positions is a harder safety problem — own design). Note JS *minification* of static
  `<script>` IS in scope here.
- An aggressive *built-in* tier for either language (value rewrites, identifier
  mangling, structural transforms) — those belong to a user-supplied
  `WithCSSMinifier`/`WithJSMinifier`, not gsx core.
- Exposing interpolation holes to the pluggable minifier (a structured chunk API) —
  holey blocks stay on the built-in safe pass.
- `gsx.RawURL` parity (same opt-out-type pattern; referenced by example 02, still
  unimplemented).
- Named CSS-value convenience types (e.g. a `Color`/`Length` type safe bare).
- A literal-`${` escape inside `<style>` (does not occur in real CSS; add only if a
  concrete need appears).
- Auto-applying the filter to `style=` for non-string interpolations beyond the
  numeric/RawCSS cases.

## Risks

- **`cssValueFilter` port fidelity** — the safety rests entirely on faithfully porting
  the stdlib algorithm. Mitigation: port `html/template/css.go` directly (helpers and
  all), and lift its test vectors. Do **not** approximate.
- **Raw-text scanner regressions** — adding `${` handling must not change `<script>` or
  non-interpolated `<style>` behavior. Mitigation: `<style>`-only branch + negative
  tests; `<script>` path untouched.
- **Minifier correctness** — a too-clever default that rewrites values can silently
  break rendering (`0px`→`0`, color/shorthand). Mitigation: the default is whitespace +
  comments only, tokenizer-based (never regex), with the historical-breakage golden
  corpus as the guard; all value/structural transforms are pushed to the opt-in
  extension point.
- **Hole-adjacency in the minifier** — collapsing whitespace next to a `${ }` could
  change a multi-value declaration. Mitigation: holes are opaque tokens with
  never-touched adjacent whitespace; covered by the gsx hole golden cases.
- **JS minification stability (ASI / regex / templates)** — JS is materially harder to
  minify safely than CSS: dropping an ASI-significant newline, or mis-classifying `/` as
  divide vs regex, silently changes behavior. Mitigation: a real JS tokenizer (never
  regex) that preserves ASI-significant newlines and literal interiors, the ASI/regex/
  template golden corpus, and pushing all aggressive transforms to `WithJSMinifier`. If
  a robust default proves too risky in build, fall back to JS-extension-only without
  blocking the CSS slices.
- **Formatter faithfulness on preserve + interp** — the printer must emit `${ }` in
  preserve context without re-indenting the surrounding raw CSS. Mitigation: the
  corpus-wide property tests are the guard.
