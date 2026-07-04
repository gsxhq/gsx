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

## URL-context safety (scheme-integrity, gsx-consistent)

Plain attribute-escaping is **not** safe for URL attributes: `` href=`@{u}` `` with
`u = "javascript:…"` would pass through as a live XSS, because attr-escaping does not stop a
dangerous scheme.

**Correction to the earlier framing:** gsx's URL model is deliberately **scheme-sanitize +
HTML-entity-escape only** — `_gsxgw.URL` = `urlSanitize` (scheme allow-list →
`about:invalid#gsx`) then `writeHTML`. There is **no percent-encoder**, and
[`2026-07-02-url-hardening-refresh-base-design.md`](./2026-07-02-url-hardening-refresh-base-design.md)
explicitly lists "no `urlPart` sub-context state, no per-region escaper" as an *intentional*
non-goal. A "faithful `html/template` port" with a percent-encoding `URLPart` helper would
**contradict that deliberate decision**. So we stay gsx-consistent: no percent-encoding, no new
escaper. The only thing we must protect is **scheme integrity**, and the scheme can straddle the
static/dynamic seam.

**The analysis is entirely compile-time** (codegen scans the literal's known static prefix);
what's emitted per hole is a plain `_gsxgw.URL(…)` or `_gsxgw.AttrValue(…)` call — **identical
runtime cost to today's `href={expr}`.** No runtime region detection, no state machine.

For URL-context attrs (`href`, `src`, `action`, `formaction`, `poster`, … via the existing
`internal/attrclass` `Classifier` — `Context(name) == CtxURL`, respecting `gsx.RawURL`
bypass), each hole is **regioned by scanning the static prefix before it**:

| Hole region (determined at codegen) | Emitted call | Why safe |
|---|---|---|
| **pre-scheme** — no `/`, `?`, `#`, or static `scheme:` before the hole | `_gsxgw.URL(…)` (scheme-sanitize) | the hole itself carries/determines the scheme; sanitizer rejects `javascript:` etc. (`` href=`@{base}/x` ``) |
| **post-scheme** — a `/`, `?`, `#`, or static `scheme:` appears in the static prefix | `_gsxgw.AttrValue(…)` (entity-escape) | scheme already committed by static text; matches how gsx treats URL parts today (`` href=`/u/@{id}` ``) |

**Seam guard (compile error):** a **pre-scheme hole immediately followed by a static `:`**
(e.g. `` href=`@{x}://y` ``) is rejected at codegen with a clear diagnostic — the scheme would
be forged from `hole + static ":"`, which per-segment sanitizing cannot catch. Directs the user
to `href={ url }` (single expression → whole-value scheme-sanitized). This is the only unsafe
gap, and it's closed by refusing to compile, never by a runtime check. Rare and precise.

Non-URL attrs are unaffected (holes always entity-escape). `srcset` / meta-refresh `content`
keep their existing special-casing (`RefreshContent`); confirm interop or correct exclusion.
CSS `url()` inside a `style` attribute stays a CSS context (use `` css`…` ``), out of scope.

### Fuzzing the scheme-integrity invariant

Because this is security-critical, add a **fuzz target** (alongside the existing codegen fuzz
harness) asserting the property: *for any static-prefix shape and any hole values, a rendered
URL-context backtick literal never yields a dangerous effective scheme.* The fuzzer generates
literal templates (random static segments + hole positions) and hole values (including
`javascript:`, `data:`, `vbscript:`, whitespace/control-char obfuscations, seam attempts),
compiles+renders, and fails if the output attribute resolves to a blocked scheme that isn't
`about:invalid#gsx`. Seam-shaped inputs must be rejected at compile time (no output to check).

## class / style composable attributes

`` class=`btn @{v}` `` produces a class-**string value** that flows into the normal class-merge
path (composes with a spread `class`, dedups, etc.) — the value-form analogue of
`class="btn other"`, **not** a `{ if … }` class arm. `style` is analogous, routing through the
existing style-value handling (`StyleValue`). No new class/style syntax; the literal is just a
new way to *produce* the value.

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
- URL attr: **pre-scheme** hole (`` href=`@{base}/x` `` → `_gsxgw.URL`) · **post-scheme**
  path hole (`` href=`/u/@{id}` `` → `AttrValue`) · post-scheme query hole
- URL **seam** rejected at compile time (`` href=`@{x}://y` `` → diagnostic)
- class attr (merge interaction) · style attr
- per-hole pipeline · whole-literal pipeline
- escaping: `\@{` · `` \` ``
- type variety: string · int · `Stringer` · mixed type-param
- error cases: unterminated literal, malformed hole

Plus:

- **Fuzz (security-critical):** scheme-integrity invariant for URL-context literals (see
  the fuzzing subsection above) — no hole-value combination yields a dangerous effective scheme.
- **Parser unit:** dispatch, `\@{` unescape (including js/css inheriting it).
- **Codegen unit:** URL region classification (pre/post-scheme) + seam-rejection diagnostic.
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
  interpolation; runnable example.
- `docs/ROADMAP.md` — reflect shipped feature.

## Open sub-decisions (confirm on review)

1. `EmbeddedText` as the lang name.
2. Include the braced `` {`…`} `` form (recommended: yes).

*(Resolved during planning: URL handling stays gsx-consistent — scheme-sanitize +
entity-escape, no `URLPart` percent-encoder; region-aware pre/post-scheme routing with a
compile-time seam guard; fuzzed. See the URL-context section.)*
