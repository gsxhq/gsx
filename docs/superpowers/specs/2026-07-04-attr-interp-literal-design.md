# Interpolating attribute-value literals (`` name=`…@{ expr }…` ``)

**Status:** design / awaiting review
**Date:** 2026-07-04
**Branch:** `worktree-attr-interp-literal`

## Motivation

gsx's actual interpolation rule is *"you interleave static text with typed, auto-escaped
holes in text-bearing positions."* That rule holds **everywhere except one place**:

| Position | Interleave static + holes? | Hole syntax |
|---|---|---|
| Element body | ✅ `<div>item-{id}-{n}</div>` | `{ }` |
| `<script>` raw text | ✅ | `@{ }` |
| `<style>` raw text | ✅ | `@{ }` |
| `` js`…` `` / `` css`…` `` attr | ✅ | `@{ }` |
| **plain attribute value** | ❌ single `{ expr }` or fully static | — |

A user who writes `<div>item-{id}-{n}</div>` and reaches for the "same thing" in
`class="item-{id}-{n}"` hits a wall for no reason they can perceive. This feature
**removes that arbitrary exception** so the one rule becomes true without asterisks.

This is a language-completeness change, not a performance feature. Two consequences fall
out for free:

1. **Correctness.** Today's workaround is `class={ fmt.Sprintf("btn %s %s", v, size) }` or
   `+`-concatenation. `Sprintf` yields `%v` with *no context-aware escaping* and no
   compile-time type-awareness; concatenation forces manual `strconv` and manual escaping
   judgement. An interpolating literal escapes and formats **each hole by its type and
   position** automatically — strictly safer than the workaround.
2. **Zero allocation.** Because holes lower to direct, per-segment writer calls (numbers via
   the reused scratch buffer, strings via the escaping writer), the intermediate `Sprintf`
   string disappears. This resolves the class of unnecessary allocations raised in
   [templ #1333](https://github.com/a-h/templ/issues/1333) — but it is a side effect of the
   design, not its justification.

If the uniformity/correctness argument did not hold on its own, we would not add the syntax.

## Surface syntax

A new **backtick string literal** usable in **attribute-value position only**, mirroring the
two existing `` js`…` ``/`` css`…` `` surface forms:

```gsx
// direct RHS
<span class=`btn btn-@{variant} @{size}`>…</span>
<a href=`/user/@{id}/edit`>…</a>

// braced ("as-expression" entry; takes a trailing pipeline)
<span class={`btn-@{v}`}>…</span>
```

- **Holes are `@{ expr }`** — family-consistent with js/css, and reuses the existing
  `parseEmbeddedSegments` scanner verbatim.
- Existing forms are **unchanged**: `"…"` stays purely static; `{ expr }` stays a single
  Go expression.
- **Two independent pipeline levels**, both inherited from existing machinery:
  - per-hole: `` @{ v |> upper } `` — filters the hole value, stays zero-alloc.
  - whole-literal: `` `…` |> upper `` — materializes the assembled string, then filters
    (you opt into the allocation by composing).

### Scope boundaries (v1)

- Valid **only** in attribute-value position. Not in body (`{ }` already covers it), not as a
  general Go-expression-position literal.
- No native `fmt`-verb form (`%.2f`). When you need verbs, use
  `` @{ x |> format("%.2f") } `` (that hole allocates via `std.Format` — acknowledged).
- JSON / JS-context attribute values keep using `` js`…` `` (already correct, already escapes).

## AST & parser

- New `ast.EmbeddedLang` value **`EmbeddedText`** *(DECISION — alternatives `EmbeddedPlain`,
  `EmbeddedHTML`; `EmbeddedText` chosen: the value is plain text, HTML-attribute-escaped)*.
  Reuses `ast.EmbeddedAttr` (`Name`, `Lang`, `Segments []Markup`) unchanged.
- **Parser dispatch** (`parser/attrs.go`): add bare `` p.at("`") `` → `EmbeddedText` literal
  and the braced `` {`…`} `` form, alongside the current `js`` / `css`` dispatch. Reuses
  `parseEmbeddedAttrLiteral` + `parseEmbeddedSegments`; the scanner's terminal `switch`
  already has a `default:` arm anticipating a third lang.
- Holes are ordinary `*ast.Interp` (expr + optional per-hole `Stages`), produced by the same
  `parseInterp` call — so brace-balancing inside the hole expression is already handled.

### Escaping in the shared scanner

Extend the existing backslash convention; do **not** introduce a second mechanism:

- `` \` `` → literal backtick (already implemented via `embeddedBacktickEscaped`).
- **New: `\@{` → literal `@{`.** Added to the shared scanner and to
  `unescapeEmbeddedBackticks`, so `` js`` ``/`` css`` `` inherit the fix (they currently have
  *no* way to write a literal `@{`).
- `{{`-doubling is **rejected** as an escape — gsx already means ordered-attrs
  (`OrderedAttrsAttr`) by `{{ }}` in attribute position; reusing doubling would misread.
- `}` never needs escaping: only openers trigger; a bare `}` outside a hole is literal, and
  inside a hole the Go-expression parser balances braces itself.

## Codegen — type-aware, zero-alloc holes

Each hole routes **exactly like the `id={expr}` path** (`emitAttrValue` in
`internal/codegen/emit.go`):

| Hole type | Writer call | Alloc |
|---|---|---|
| `string` / `[]byte` | `_gsxgw.AttrValue(string(x))` | escapes; no alloc if already string |
| `int` / `uint` / `float` | `_gsxgw.IntInto/UintInto/FloatInto(_gsxnum[:], …)` | zero (reused scratch) |
| `fmt.Stringer` | `_gsxgw.AttrValue((x).String())` | per `String()` |
| mixed type-param | `_gsxgw.AttrAny(x)` | dynamic dispatch |

Static segments are HTML-attribute-escaped **at codegen time**
(`strconv.Quote(htmlAttrEscape(...))`) and merged into a single `_gsxgw.S("…")` run. The whole
value is wrapped `_gsxgw.S(" name=\"")` … segments … `_gsxgw.S("\"")`. No intermediate string
is ever built — the zero-alloc property is a direct consequence.

The **whole-literal pipeline** (`` `…` |> f ``) is the one composition point that materializes:
it lowers to `f(<assembled string>)`, where the assembled string is built once via a small
helper (still one alloc, vs `Sprintf`'s several) and then flows through the normal filtered
attr-value emit. Per-hole pipelines never materialize the whole value.

## URL-context safety — whole-value sanitization (the robust design)

Plain attribute-escaping is **not** safe for URL attributes: `` href=`@{u}` `` with
`u = "javascript:…"` would pass through as a live XSS, because attr-escaping does not stop a
dangerous scheme.

**Why whole-value, not per-hole (lesson learned):** an earlier iteration tried to classify each
hole at compile time (pre-scheme → `URL()`, post-scheme → `AttrValue()`, with a seam guard).
Adversarial review found **five** distinct browser-confirmed XSS bypasses in that approach —
multi-hole scheme commitment, split-scheme seams, tab/CR/LF and leading-space obfuscations
(browsers strip these before scheme parsing), and non-allowlisted static schemes. The root cause
is fundamental: reconstructing *which part of a split static/dynamic value is the scheme* at
compile time requires modeling every quirk of browser URL normalization, and that is a losing
game. **We abandoned per-hole classification entirely.**

**The design:** for a URL-context attribute (`href`, `src`, `action`, `formaction`, `poster`, …
via `attrclass.Classifier.Context(name) == CtxURL`), gsx **assembles the whole value** — static
segments concatenated with the type-converted hole values — into one string at runtime and passes
it through **`_gsxgw.URL`**, the exact same `urlSanitize` (+ entity-escape) that `href={ expr }`
already uses:

```go
// href=`/u/@{id}/edit`  (id string)  →
_gsxgw.S(" href=\"")
_gsxgw.URL("/u/" + string(id) + "/edit")
_gsxgw.S("\"")
```

**Why this is provably safe:** `urlSanitize` is **fail-closed** — it reads the scheme (text
before the first `:`), lowercases it, and blocks anything not in `{http, https, mailto, tel}` to
`about:invalid#gsx`. Because the *entire assembled string* is sanitized as one URL, every
bypass class collapses to "blocked":

- split across holes (`` `@{a}@{b}` `` a=`javascript` b=`:x`) → assembled `javascript:x` → blocked.
- obfuscation (`` `java⇥script:@{x}` ``, ` javascript:@{x}`) → the scheme string contains a tab /
  space → doesn't match the allowlist → blocked (fail-closed doesn't need to model browser
  stripping; any deviation from a clean allowlisted scheme is rejected).
- author-written dangerous static scheme (`` `javascript:@{x}` ``, `` `data:text/html,@{x}` ``) →
  scheme not allowlisted → blocked. Identical to `href={ "javascript:" + x }`.
- a *safe* dynamic scheme now simply **works**: `` href=`@{scheme}://ex.com` `` with
  `scheme="https"` → `https://ex.com` (allowed); with `scheme="javascript"` → blocked. No compile
  error needed — sanitization handles it gracefully.

This is **exactly as safe as `href={ expr }`** and requires *no* new machinery: no
`urlHoleRegion`, no `schemeSeamAfter`, no scheme detection, no compile-time seam rejection.

**Cost:** a URL-context literal builds one concatenated string (like `href={ expr }` already
does) rather than per-segment zero-alloc writes. Acceptable — URL attributes are not the hot path
Roman's allocation concern targets, and the zero-alloc per-segment path is **retained for every
non-URL attribute** (`class`, `data-*`, `id`, `title`, …), which is where it matters.

**RawURL / raw URLs:** a backtick URL literal is **always** sanitized. To emit an un-sanitized
URL (e.g. a `data:image/...;base64,…` inline image), use the single-expression form
`` src={ gsx.RawURL("data:image/png;base64," + b64) } `` — the existing author-vouch path.
(A `data:image` allowance for resource-URL contexts is tracked separately in `docs/ROADMAP.md`
as part of the navigational-vs-resource URL split; out of scope here.)

**Hole types in a URL literal:** each hole is converted to a string for concatenation via the
same type dispatch as `emitAttrValue` (string/[]byte → `string(x)`, int/uint/float →
`strconv.Format…`, `Stringer` → `(x).String()`); pipelines and `(T, error)` unwrap as elsewhere.
A hole whose type can't convert to string (e.g. a `Node`) is a codegen error. Non-URL attrs and
`srcset`/meta-refresh `content` keep their existing handling; CSS `url()` in `style` stays a CSS
context (use `` css`…` ``).

### Fuzzing the scheme-safety invariant

Because this is security-critical, add a **fuzz target** (alongside the existing codegen fuzz
harness) asserting: *for any static-segment shape and any hole values, a rendered URL-context
backtick literal never yields a dangerous effective scheme.* The fuzzer generates literal
templates (random static segments + hole positions) and hole values (including `javascript:`,
`data:`, `vbscript:`, and whitespace/control-char/split-scheme obfuscations), compiles + renders,
computes the **browser-effective scheme** (strip ASCII tab/LF/CR, strip leading C0/space,
lowercase, take text before the first `:`), and fails if that scheme is dangerous and the value
is not `about:invalid#gsx`. This guards the invariant permanently against any future regression.

## class / style composable attributes (v1 = full merge support)

`` class=`btn btn-@{v}` `` emits the interpolated value as the element's ` class="…"` (the
value-form analogue of `class="btn other"`), **not** a `{ if … }` class arm. `style` is
analogous.

**v1 scope decision:** the `EmbeddedText` class/style literal is a **first-class merge target**,
exactly like a static `class="…"`. The fallthrough machinery (`emitFallthroughAttrs`'s merge-site
finder, `emitRootStaticClass`/`emitSpread`) currently scans only for `*ast.StaticAttr` and
`*ast.ClassAttr` named `class`/`style`; we extend it to also recognize an `*ast.EmbeddedAttr`
(`Lang == EmbeddedText`, name `class`/`style`) as the merge site. A spread bag's `class` then
merges **caller-last** into the interpolated value, and its `style` merges over the interpolated
declarations — **no duplicate attribute**. Mirrors `emitRootStaticClass`, but the "own" class
string is emitted as interpolated segments (`S("badge-")` + hole) rather than a single static
literal, with the bag's class token(s) appended after.

Example: `` <span class=`badge-@{variant}` { attrs... }> `` with bag `class:"hl", id:"a"` →
`<span class="badge-x hl" id="a">`.

This is the one place v1 touches the intricate fallthrough/forwarding code, so it gets its own
task with dedicated corpus coverage (own-only, own+bag-class, own+bag-style, own+bag-scalar).

## LSP — go-to-definition & hover inside holes

Holes are ordinary `*ast.Interp`, so they **inherit** the PR-#28 nav wiring for Go-fragment
positions. Expected to be mostly free. Work:

- Add the new position to the **LSP nav-matrix test**: hole identifier, per-hole pipeline stage
  name, whole-literal pipeline stage name.
- Confirm gd + hover resolve for each; if a gap surfaces, close it with the established
  two-bridge recipe (see `gsx-lsp-nav-matrix` memory).

## Formatter

`gsx fmt` formats the literal **idempotently**, normalizing hole spacing to the js/css
convention (`@{ expr }`). Reuses the embedded-literal print path; no new formatting rules
beyond recognizing `EmbeddedText`.

## Testing

Per project convention, **every context ships a corpus case** (`input.gsx` +
`generated.x.go.golden` + `render.golden`) under `internal/corpus/testdata/cases/`:

- plain attr (string + int holes)
- URL attr (whole-value `_gsxgw.URL(…)`): path/join (`` href=`/u/@{id}/edit` ``,
  `` href=`@{base}/edit` ``) · safe dynamic scheme works (`` href=`@{scheme}://ex.com` ``,
  `scheme="https"`) · dangerous scheme **blocked** to `about:invalid#gsx`
  (`` href=`javascript:@{x}` ``, split `` `@{a}@{b}` `` a=`javascript` b=`:x`)
- class attr (merge interaction) · style attr
- per-hole pipeline · whole-literal pipeline
- escaping: `\@{` · `` \` ``
- type variety: string · int · `Stringer` · mixed type-param
- error cases: unterminated literal, malformed hole

Plus:

- **Fuzz (security-critical):** scheme-safety invariant for URL-context literals (see
  the fuzzing subsection above) — no hole-value combination yields a dangerous browser-effective
  scheme; dangerous values resolve to `about:invalid#gsx`.
- **Parser unit:** dispatch, `\@{` unescape (including js/css inheriting it).
- **LSP:** nav-matrix additions.
- **Formatter:** idempotence.

Regenerate goldens with `go test ./internal/corpus -run TestCorpus -update` (also rewrites
`coverage.golden`), then verify without `-update`. Gate the branch with `make ci`.

## Sibling-repo follow-ups (per CLAUDE.md)

Tracked as post-merge tasks; not blocking the core:

- `../tree-sitter-gsx` — grammar: backtick string + `@{ }` holes in attribute position, no
  lang prefix.
- `../vscode-gsx` — highlighting (via tree-sitter / TextMate).
- `../gsxhq.github.io` — CodeMirror + VitePress syntax; a docs section under attribute syntax /
  interpolation.
- **Playground examples** (`gsxhq.github.io/playground`) — add runnable snippets that make the
  feature tangible: (1) headline `` class=`btn btn-@{variant}` `` interpolation, (2) a URL path
  `` href=`/u/@{id}` ``, (3) the class + spread-bag merge, (4) a "why it's zero-alloc" before/after
  vs `fmt.Sprintf`. Each should render live in the playground so readers can edit holes and see
  output. Verify against the playground's WASM-staleness gotcha (rebuild + cache-bust `gsx.wasm`).
- `docs/ROADMAP.md` — reflect shipped feature.

## Open sub-decisions (confirm on review)

1. `EmbeddedText` as the lang name.
2. Include the braced `` {`…`} `` form (recommended: yes).

*(Resolved during planning: URL handling stays gsx-consistent — scheme-sanitize +
entity-escape, no `URLPart` percent-encoder; region-aware pre/post-scheme routing with a
compile-time seam guard; fuzzed. See the URL-context section.)*
