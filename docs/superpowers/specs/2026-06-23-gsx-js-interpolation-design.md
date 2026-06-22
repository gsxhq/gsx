# Design: safe JS interpolation in gsx (`@{ }` in `<script>` + JS-context attributes)

**Date:** 2026-06-23
**Status:** approved (brainstorm) — ready for writing-plans
**Supersedes (token):** the `${ }` delimiter chosen in `2026-06-22-gsx-style-safe-interpolation-design.md` (slice 1) is migrated to `@{ }`.

## Goal

Let templates inject Go data into **JavaScript** safely and ergonomically — inside
`<script>` blocks **and** JS-context attributes (`x-data`, `@click`, `onclick`,
`:class`, `hx-on:*`…) — so authors stop hand-building JS strings with `fmt.Sprintf`
and stop using the `<script type="application/json">` + `JSON.parse` workaround.

Real-world grounding (the user's repos `one-learning`, `his-project`): the dominant
pattern is Alpine state built in Go and spliced into `x-data`, e.g.
`x-data={ fmt.Sprintf("{ activeTab: '%s', hasData: %t }", tab, ok) }` — untrusted-ish
data hand-concatenated into JS, exactly the unsafe thing gsx currently fail-closes on.
The headline ergonomic win is replacing that with
`x-data="{ activeTab: @{ tab }, hasData: @{ ok }, open: false }"`.

## Threat model & principles

gsx's standing model: the **template author is trusted** (writes the JS structure), the
**interpolated data is not**. Two principles in order: (1) **safe by default** — an
injected value can never break out of its JS context (close `</script>`, escape a
string/template/regex, inject code); (2) **convenience where possible** — the common
case (a Go value in a JS value position) needs no ceremony. This mirrors how slice 1
made CSS auto-sanitize and is modeled on `html/template`'s JS context engine (the
proven design and our test oracle).

## Dependency decision: `tdewolff/parse/v2` (researched)

A safe JS engine needs to **classify each interpolation hole's JS context** and to
lex JS correctly (regex-vs-divide, template literals, ASI, ES2020+). Research
(`docs/superpowers/specs/` research notes; see commit log) concluded:

- **`github.com/tdewolff/parse/v2`** (`/js` + `/css`) — **MIT, stdlib-only** (its
  `js`/`css` packages pull in zero third-party deps), ES2020-capable, maintained.
  Exposes an **importable lexer** (`js.NewLexer`/`Next`, with `parse.Input.Offset()`
  for byte positions). **This is the core dependency.** Used at the **token-stream**
  level (its AST carries no positions — but the correct design, like `html/template`,
  is token/state-machine-driven anyway, not AST-driven).
- **esbuild** — its parser/AST are `internal/`; only `Transform`/`Build` are public, so
  it **cannot** drive a context engine. It stays an *optional* aggressive minifier
  behind `WithJSMinifier`, **never a core dep**.
- **goja** — has an AST with positions but a heavy dependency graph (regexp2,
  sourcemap, yaml, pprof…) that violates gsx's lean posture. Rejected.

Net new core dependency = **`tdewolff/parse/v2` only** (codegen-time; the runtime stays
stdlib-only and untouched). It powers: the JS context engine, a safe built-in JS
minifier, and (optionally) a CSS context layer.

## Component 1 — the context engine (`internal/jsx`)

A new codegen-time package. For a `<script>` body (or JS-context attribute value)
containing `@{ goExpr }` holes:

1. **gsx-level hole scan.** Scan for `@{ goExpr }` exactly as slice 1 scans `${ }` —
   `goExprEnd` finds the matching `}` respecting Go strings/braces. Record each hole's
   Go expression + index. (`<script>` stays a single raw `Text` in the *parser* AST;
   the engine does all JS lexing at codegen — so a hole inside a JS comment never even
   becomes an AST node.)
2. **Placeholder substitution.** Replace each hole with a **reserved sentinel that lexes
   as one JS identifier** — a fixed ASCII-safe prefix plus the hole index (e.g.
   `_GSXJSHOLE_` + decimal index: `_GSXJSHOLE_0`, `_GSXJSHOLE_1`, …). The prefix is a
   valid JS `IdentifierStart`, so each sentinel is a single identifier token. Verify the
   source does not already contain the sentinel prefix; if it does, **fail closed**. The
   substituted string is valid, parseable JS.
3. **Lex with `tdewolff/parse/v2/js`,** walking tokens and carrying the byte offset via
   `parse.Input.Offset()`. tdewolff groups each string/template/regex/comment into a
   single typed token and resolves regex-vs-divide via its `DivToken` + `RegExp()`
   two-step (driven by `html/template`'s preceding-token `jsCtx` rule + its
   `regexpPrecederKeywords` set).
4. **Classify each hole by the token it lands in** (Component 2), then **emit the
   per-context escaper** at codegen, writing the escaped runtime value where the
   sentinel was (or, for comments, leaving the literal text verbatim).

The runtime escapers (`gw.JS*`, stdlib-only) are **faithful ports of `html/template`'s**
`jsValEscaper` / `jsStrEscaper` / `jsRegexpEscaper` / `jsTmplLitEscaper` — same logic,
same `</script>` / `U+2028` / `U+2029` / token-fusion defenses, so the differential
oracle test (Testing) is exact.

### Position tracking (no AST coordinates)

The **sentinel is the position.** Tracking reduces to *which token does each sentinel
land in*:

| Sentinel appears as… | Context |
|---|---|
| a standalone `IdentifierToken` | **value** — or **binding** (disambiguated by the preceding token: `let`/`const`/`var`/`function`/`class`/`.`/object-key → binding) |
| bytes inside a `StringToken` | **string** |
| bytes inside a `TemplateToken` | **template** |
| bytes inside a `RegExpToken` | **regex** |
| bytes inside a `CommentToken` | **comment → leave literal** |

`Input.Offset()` is used only to walk tokens; holes are located by scanning each
relevant token's bytes for the sentinel pattern.

## Component 2 — context taxonomy & escapers

| Context (where the hole lands) | Behavior | Escaper |
|---|---|---|
| **value / expression** (`let d = @{ x }`, `()=>(@{ s })`, `k: @{ v }`) | escape | **JSON-encode** (`json.Marshal`, HTML-safe; numeric token-fusion padding) — Go value → JS literal |
| **string-literal interior** (`"…@{ x }…"`, `'…'`) | escape | **JS-string-escape** (quotes, `\`, `< > &`, line terminators) |
| **template-literal text** (`` `…@{ x }…` ``) | escape | **template-escape** (neutralize `` ` ``, `$`, `{`, `\`) |
| **regex literal** (`/…@{ x }…/`) | escape | **regex-escape** (`jsRegexpEscaper` port) |
| **comment** (`// …`, `/* … */`) | **left literal** | — (not interpolated; the sentinel is never substituted back) |
| **identifier / binding** (`let @{x}=1`, `function @{x}()`) | **fail closed** | — (no safe escaper; injecting an identifier *is* code) |
| **unknown** (context not statically single-valued) | **fail closed** | — |

**`gsx.RawJS(s string)`** — the opt-out (matching `RawCSS`/`RawURL`/`Raw`):
author-vouched JS, emitted raw in a **value position** (Alpine method bodies, arrow
functions, `() => fetch(...)`). Outside a value position it is treated as its string and
escaped by that context.

## Component 3 — the author-facing surface

One engine, three entry points:

**(1) `<script>` blocks** — write JS, drop `@{ goExpr }` holes:
```js
<script>
  const chart = new Chart(ctx, @{ chartConfig });   // value  → JSON
  const url   = "/api/users/@{ userID }";           // string → escaped
  Alpine.data('panel', () => (@{ initialState }));   // value  → JSON
</script>
```

**(2) JS-context attributes** — the `fmt.Sprintf` killer:
- **2a — literal JS with holes** (headline): `<div x-data="{ activeTab: @{ tab }, open: false }">` →
  `x-data='{ activeTab: "opp", open: false }'`. Requires `@{ }` to be recognized inside
  **JS-context attribute string values** (a parser extension beyond raw-text blocks;
  regular attributes keep `{ goExpr }` unchanged).
- **2b — whole-value**: `<div x-data={ stateStruct }>` → JSON-encoded object;
  `<div @click={ gsx.RawJS("toggle()") }>` → vouched JS.

The **JS-context attribute set** (`attrContext`'s `ctxJS`) expands beyond today's
`on*`/`@*`/`hx-on*` to include Alpine directives — `x-data`, `x-init`, `x-show`,
`x-if`, `x-on:*`, `:*` (binding shorthand) — and HTMX `hx-on:*`.

**(3) Data island** (the `templ.JSONScript` replacement):
```html
<script type="application/json" id="cfg">@{ data }</script>   <!-- whole body = one JSON value -->
```

## Component 4 — minification (one tokenizer, both jobs)

The same `tdewolff/parse/v2/js` lexer serves interpolation and minification, so they
don't fight:

- **Holeless `<script>`** → built-in **safe JS minify**: strip comments (keep `/*!`),
  drop indentation, collapse intra-line whitespace, **keep ASI-significant newlines**.
  Correct regex/template handling comes from the lexer (no hand-rolled heuristic). On by
  default.
- **Interpolated `<script>`** → the engine already tokenizes it, so it minifies the
  static spans and splices the runtime-escaped hole values in the same pass —
  hole-aware by construction.
- **CSS:** holeless `<style>` keeps the slice-2 `cssmin` built-in (or `WithCSSMinifier`);
  interpolated `<style>` keeps `cssmin` (hole-aware). Unchanged.
- **Aggressive** minify for either language stays opt-in behind `WithJSMinifier` /
  `WithCSSMinifier` (esbuild's `MinifyWhitespace`, tdewolff-minify). The cache is
  bypassed when a custom minifier is set (as slice 2 established); `codegen.Version()`
  is bumped when JS minification lands.

## Component 5 — token migration (`${}` → `@{}`)

`@{ }` becomes the raw-text-block interpolation delimiter for **both** `<style>` and
`<script>`. Rationale: `${ }` collides with JS template literals (`` `${x}` ``), so it
cannot be used inside `<script>`; `@{ }` (LESS/templ-style) is invalid in both CSS
(at-rules are `@name`, never `@{`) and JS (decorators are `@name`, never `@{`), so it
coexists with users' JS `${}` template literals and `@decorators`. Migrate slice 1's
`${}` → `@{}` across parser/printer/examples/tests/spec. (`SafeCSS → RawCSS` is already
merged on `main`.)

## Error handling — all at compile time, with the `@{ }`'s `line:col`

| Situation | Result |
|---|---|
| hole in identifier/binding or unknown context | fail-closed compile error |
| `<script>` body that does not lex as JS | compile error (catches JS typos at build) |
| source already contains the reserved sentinel prefix | fail-closed |
| `@{ }` in a comment | left literal (no error, no interpolation) |
| JS-context attribute hole, same engine | same classification + errors |

## Slicing (one spec → multiple implementation plans)

- **Slice A — token migration** `${} → @{}` in `<style>` (parser/printer/examples/tests).
  Mechanical; ships first; unblocks `<script>`.
- **Slice B — `tdewolff/parse` dep + safe built-in JS minify + `WithJSMinifier` seam**
  (holeless `<script>`). Brings the dependency in; smallest real-code step; bumps
  `Version()`.
- **Slice C — JS interpolation core:**
  - **C1** — `internal/jsx` context engine + `<script>` `@{ }` interpolation + ported
    `gw.JS*` escapers + `gsx.RawJS`.
  - **C2** — JS-context attributes (forms 2a/2b + the `ctxJS` set expansion).
  - **C3** — data-island sugar.

## Testing — a brief for the `testing-foundation` worktree

This feature is the ideal customer for that worktree's **highest-leverage gap (G1): no
differential/oracle test for the escaper.** Because we *port* `html/template`'s
escapers, the oracle test is exact. The spec's testing requirements (for that worktree
to implement against the shared `internal/corpus` harness):

- **Differential oracle:** each escaper (`value`/`string`/`regex`/`template`) compared
  input-for-input against `html/template`'s `jsValEscaper` / `jsStrEscaper` /
  `jsRegexpEscaper` / `jsTmplLitEscaper`. Outputs must match.
- **Security corpus:** breakout attempts per context — `</script>`, `*/`, newline in a
  `//` comment, `` ` `` / `${` in a template, `/` in a regex, `U+2028/2029` — verify
  neutralized end-to-end (rendered output).
- **Context-classification corpus:** a table of `(JS snippet with one hole → expected
  context)`, including the binding-vs-value and comment-literal cases.
- **Fuzz:** random JS + holes → no panic, no breakout, stable classification.
- **Coordination:** feature code lives in `parser`/`internal/codegen`/`internal/jsx`/
  runtime; the test *infrastructure* (oracle harness, fuzz infra, the codegen coverage
  fix) lives in `testing-foundation`. The shared touch-point is the corpus harness —
  the feature adds cases, the worktree owns the harness, so changes there are
  coordinated, not concurrent.

## Non-goals / future

- gsx `{ if }`/`{ for }` control-flow *inside* a `<script>` body (would reintroduce the
  branch-merge / `jsCtxUnknown` hazard). MVP `<script>` bodies are straight-line; a hole
  whose context differs across branches fails closed.
- TypeScript / JSX in `<script>` (esbuild territory; out of scope — `RawJS` + a future
  raw `<script>` escape hatch cover non-JS bodies).
- Aggressive built-in minification (value rewrites, identifier mangling) — extension
  only.
- A CSS context engine on `tdewolff/parse/css` — `cssmin`'s tokenizer + slice-1
  `cssValueFilter` already cover CSS; revisit only if CSS interpolation needs
  position-awareness beyond value context.

## Risks

- **Novel ground.** No one has built a context-aware JS templating escaper on a
  third-party Go parser (templ punted to string+JSON with unsafe hatches;
  `html/template` hand-rolls everything). The two historical-CVE hazards —
  **template-literal `${}` brace-depth** (CVE-2023-24538) and **context-merge across
  branches** (CVE-2026-32289) — are designed in from day one (brace-depth tracked in
  the template-literal escaper; non-single-valued context → fail-closed).
- **tdewolff regex-vs-divide is a two-step** (`DivToken` + `RegExp()`): we re-implement
  `html/template`'s `jsCtx` precondition. Mitigation: lift its `regexpPrecederKeywords`
  set + `nextJSCtx` heuristic verbatim and fuzz it.
- **Escaper port fidelity** is the security crux. Mitigation: port `html/template`'s
  `js.go` escapers directly and gate with the differential oracle test (G1).
- **Sentinel collision / multi-byte identifiers** (BigInt etc.): verify against
  tdewolff's actual `TokenType` set; bail to fail-closed on any sentinel-prefix
  presence in source.
