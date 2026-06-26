# gsx Embedded-Language Re-indent Formatter (Design)

**Date:** 2026-06-26
**Status:** Approved (brainstorm), pending implementation plan
**Predecessors:**
- `2026-06-26-gsx-css-formatting-infra-design.md` — shipped the `rawfmt` embed layer + a parse-based `cssfmt`. This spec **revises** `cssfmt` and **adds** JS.
- `2026-06-26-gsx-formatter-doc-ir-design.md` — the `internal/pretty` Doc IR.

## Goal

Format the bodies of `<style>` AND `<script>` in `.gsx` source during `gsx fmt`
by **normalizing indentation only** — a conservative token-pass re-indenter
that fixes the tab/space mess without otherwise altering the author's code.
Unify all four embedded-language passes (CSS/JS × minify/format) under one
philosophy, and **simplify the shipped `cssfmt`** from a parse-and-reflow
formatter to this lighter re-indenter.

## Motivation

The real-world pain (confirmed against `one-learning/ui/*.templ`,
`design-system/ds/**/*.templ`) is **inconsistent indentation** — embedded JS/CSS
authored with mixed tabs/spaces inside a tab-indented document. The fix users
want is "make the indentation consistent," not "reflow my code."

The codebase already has two **conservative token-pass minifiers**:
- `internal/cssmin` (133 lines): collapse whitespace/comments, hole-aware, never
  rewrites values.
- `internal/jsmin` (179 lines): tdewolff-lexer-driven, strips comments/indent,
  **keeps every newline** so ASI is never altered, never rewrites values.

The shipped `cssfmt` (≈480 lines) broke that pattern: it parses CSS into rules
and **reflows** (one declaration per line, blank lines between rules). That is
heavier than the pain requires and invents structure the author did not write
(e.g. blank lines). The coherent design is: **a formatter is the inverse of its
minifier** — a token pass that conservatively *normalizes leading indentation*
instead of *stripping* whitespace.

## Philosophy (the contract)

A re-indenter:
1. **Re-indents each line** to its block-nesting depth, emitting tabs
   (consistent with gsx's gofmt-style markup indentation).
2. **Preserves the author's line structure exactly** — never adds or removes a
   line break, never adds or removes a blank line, never reflows. A minified
   one-liner stays one line (re-indented to the tag depth); multi-line code gets
   consistent tabs.
3. **Treats strings, template literals, regex literals, and comments as opaque**
   — their interiors (including any internal newlines, e.g. a multi-line
   template literal or block comment) pass through verbatim and are never
   re-indented.
4. **Changes only leading whitespace.** Intra-line spacing is left as the author
   wrote it. (Spacing normalization is explicitly out of scope — it belongs to a
   reflow formatter, which we are not building.)
5. Is **idempotent** and **faithful** by construction (only leading whitespace
   changes; the token stream is otherwise untouched).

## Architecture

```
internal/reindent   shared, language-agnostic re-indent core (the algorithm)
                    + a small Adapter interface each language implements

internal/cssfmt     REVISED: keep token.go (tokenizer); DELETE the parser+layout;
                    add a cssAdapter feeding the shared core. Format(src,width).

internal/jsfmt      NEW: reuse jsmin's tdewolff lexer + regexPosition via a
                    jsAdapter feeding the shared core. Format(src,width).

rawfmt              UNCHANGED: hole-sentinel round-trip + re-indent to tag depth.
                    Both formatters are rawfmt.Formatter values.

printer + gen       <script> routed through rawfmt with the JS formatter
                    (mirrors <style>); gen.WithJSFormatter plug point.
```

The `width` parameter is retained on the `Format(src, width)` signatures for
interface symmetry with the shipped CSS formatter and the `defaultCSSFormatter`
closure, but a pure re-indenter does not wrap on width, so it is currently
unused by the algorithm. (Kept rather than removed to avoid churning the
`rawfmt.Formatter`/printer wiring; documented as intentionally unused.)

## The shared re-indenter (`internal/reindent`)

### Adapter interface

Each language provides an adapter that turns its source into a flat stream of
classified tokens. The core never imports a language tokenizer directly.

```go
package reindent

// Class is how the core treats one token for indentation purposes.
type Class uint8

const (
	Other   Class = iota // ordinary token: emit verbatim, no structural effect
	Open                 // increases block depth AFTER this line (e.g. "{")
	Close                // decreases block depth; a line STARTING with it dedents
	Newline              // a real line break OUTSIDE any literal/comment
	Space                // inter-token / leading whitespace (NOT inside a literal)
	Opaque               // string/template/regex/comment: emit verbatim, may span lines
)

// Token is one classified lexical token.
type Token struct {
	Class Class
	Text  string // exact source text (verbatim for Opaque, incl. internal newlines)
}

// Tokenize lexes src into the classified stream. It MUST be total for the
// re-indenter's purposes: on a lex error it returns ok=false and the core
// falls back (caller renders verbatim).
type Adapter interface {
	Tokenize(src []byte) (toks []Token, ok bool)
}
```

### Algorithm

```go
// Reindent re-indents src using the adapter, emitting `indentUnit` (a tab) per
// depth level. It returns (formatted, true) or (_, false) on a lex failure.
func Reindent(src []byte, a Adapter) (string, bool)
```

Walk the token stream maintaining `depth`:
- **`Open`**: emit the token; increment a `pendingDepth` that takes effect on the
  next line (so the `{`'s own line is not yet indented deeper).
- **`Close`**: decrement depth *before* emitting if it is the first non-space
  token on the current line (so `}` dedents to its block's level); emit the token.
- **`Newline`**: end the current line; emit `\n`. Mark "at line start" so the
  next line's leading `Space` tokens are dropped and `depth` tabs are emitted
  before the first non-space token. A line with no content before its `Newline`
  is a **blank line** — emit just `\n` (no tabs → no trailing whitespace; blank
  lines are preserved, never invented or stripped).
- **`Space`**: at line start, **drop** it (replaced by the computed indent);
  mid-line, emit verbatim (intra-line spacing preserved).
- **`Opaque`** / **`Other`**: emit `Text` verbatim. `Opaque` text may contain
  internal newlines (multi-line template/comment) — those are NOT treated as
  `Newline` tokens and are not re-indented.

Indentation is applied lazily: the indent for a line is written immediately
before its first non-space, non-`Close`-adjusted token, using the depth in
effect at that point. This guarantees idempotence (re-indenting correctly
indented input reproduces it) and blank-line cleanliness (no tabs on empty
lines).

**Only braces `{}` drive indentation — NOT parens or brackets.** (Revised from
the original "count parens/brackets too" after real-world testing.) The reason:
the ubiquitous callback pattern `call(args, (e) => {…})` has an *unclosed* `(`
**and** an opening `{` on the same line; counting both indents the body two
levels — visibly wrong, and it *changes* correctly-formatted code. Real-world
JS/CSS indents block scope (`{}`) only, so a brace-only rule reproduces
hand/editor-formatted code (it is idempotent on it). Measured across the sampled
projects: 179 brace-opening continuation lines vs **0** bare-paren/bracket
continuations — so the only cost (a bare multi-line `( … )` / `[ … ]` staying
flat) never occurs in practice. CSS parens are always single-line
(`@media (...)`, `calc(...)`, `url(...)`), so the same rule is correct there. A
single, uniform brace-depth model; no per-language heuristics.

## CSS — revise `internal/cssfmt`

- **Keep** `token.go` (the tokenizer: `tWS`, `tComment`, `tString`, `tLBrace`,
  `tRBrace`, `tLParen`, `tRParen`, …). It already classifies strings/comments as
  atomic and is hole-sentinel-safe.
- **Delete** the parser + layout in `cssfmt.go` (`parser`, `parseItems`,
  `parseStatement`, `layoutTopLevel`, `layoutItems`, `layoutItem`,
  `layoutPrelude`, `layoutDecl`, `renderInline`, `splitTopLevel`, `splitFirst`,
  and the `pretty`-IR construction). **Keep** `TokenSignature` — it is
  whitespace-agnostic and independent of the deleted layout, so it remains the
  CSS faithfulness oracle (see Faithfulness).
- **Add** a `cssAdapter` mapping CSS tokens → `reindent.Token`:
  - `tLBrace` → `Open`; `tRBrace` → `Close`; `tLParen` → `Open`; `tRParen` →
    `Close`. (CSS `[`/`]` are `tDelim` in `token.go` — left as `Other`; attribute
    selectors are single-line, no indent effect needed.)
  - `tComment` (`/* … */`, may be multi-line) and `tString` → `Opaque`.
  - `tWS`: a whitespace run that may contain newlines — split it so each `\n`
    becomes a `Newline` token and the rest becomes `Space`. (CSS strings never
    span lines, so newlines only appear in `tWS` and `tComment`.)
  - everything else → `Other`.
- `Format(src, width)` = `reindent.Reindent(src, cssAdapter{})`, returning an
  error only on the tokenizer's existing error conditions (unterminated
  string/comment) so `rawfmt` falls back to verbatim.

Result: `.a{color:red;background:blue}` stays one line (re-indented to tag
depth); a multi-line rule gets consistent tabs; **no blank lines invented, no
declarations reflowed**.

## JS — new `internal/jsfmt`

- Reuse `jsmin`'s tdewolff lexer driver (`js.NewLexer`) and its `regexPosition`
  helper (factor `regexPosition` into a shared spot or duplicate as `jsmin`/`jsx`
  already each keep a copy — the plan decides; do not add a second JS parser).
- `jsAdapter` maps tdewolff JS tokens → `reindent.Token`:
  - `PunctuatorToken` `{` → `Open`, `}` → `Close`; `(`/`[` → `Open`, `)`/`]` →
    `Close`.
  - `LineTerminatorToken` (and the newline within `CommentLineTerminatorToken`)
    → `Newline`.
  - `WhitespaceToken` → `Space`.
  - `StringToken`, `TemplateToken` (and template middle/tail parts),
    `RegExpToken`, `CommentToken` → `Opaque` (a block comment or template literal
    may span lines — emitted verbatim). Regex is disambiguated from division via
    `regexPosition` exactly as `jsmin` does.
  - everything else → `Other`.
- ASI safety: the re-indenter **keeps every newline** (it never moves a
  statement across a line boundary), so ASI is never altered — identical
  guarantee to `jsmin`. No AST parse is required.
- `Format(src, width)` = `reindent.Reindent(src, jsAdapter{})`, returning an
  error only on a lexer error (so `rawfmt` falls back to verbatim).

## Wiring (printer + gen)

- `printer` gains a `jsFmt rawfmt.Formatter` field alongside `cssFmt`. The
  `<script>` branch (currently `printer.go:196` → verbatim `rawHoleChildren`)
  routes through `rawfmt.Format(nodesToBody(children), p.jsFmt)` when `jsFmt !=
  nil`, with the verbatim fallback on `ok=false` — mirroring `<style>` exactly.
  `nodesToBody`/`renderHole` are reused unchanged.
- `Fprint`/`FprintWith` default `jsFmt` to a `jsfmt.Format` closure (mirror of
  `defaultCSSFormatter`). `FprintWith` gains the `jsFmt` parameter (or an
  options struct — the plan decides; keep the no-override `Fprint` ergonomic).
- `gen`: add `config.jsFmt`, `gen.WithJSFormatter(f rawfmt.Formatter) Option`,
  and the `mergeConfig` rule (mirror of `cssFmt`). `runFmt` threads `cfg.jsFmt`
  the same way it threads `cfg.cssFmt` (config-agnostic; no `resolveConfig`).
- Data-island scripts (`<script type="application/json">` etc.) are **not**
  JavaScript — leave them verbatim (do not run the JS re-indenter). `internal/jsx`
  already has `isDataIslandScript`; reuse that classification so only executable
  `<script>` is formatted.

## Faithfulness & idempotence

The Task-7 `<style>` faithfulness machinery (`cssfmt.TokenSignature` +
`canonStyleBodies` in the printer property tests) is **retained and extended to
`<script>`**:
- A re-indenter changes only leading whitespace, so CSS/JS token-equivalence,
  hole-sequence preservation, and idempotence all hold trivially — strictly
  easier to satisfy than the old reflow `cssfmt`.
- `cssfmt.TokenSignature` stays as the CSS oracle (it already ignores
  whitespace). Add a `jsfmt.TokenSignature` (lex the JS, drop
  whitespace/comments/line-terminators, join significant tokens) for the
  `<script>` side; extend the property-test canonicalizer to cover `<script>`
  bodies the same way `canonStyleBodies` covers `<style>`.

## Testing

- `internal/reindent`: unit tests for the core via a trivial fake adapter —
  depth in/out, `Close`-at-line-start dedent, leading-space replacement,
  intra-line spacing preserved, blank line stays blank with no trailing tabs,
  `Opaque` multi-line token passes through un-re-indented, lex-failure → `ok=false`.
- `internal/cssfmt`: **rewrite** the existing tests — remove reflow/one-decl-
  per-line/blank-line-between-rules assertions; assert: messy indentation fixed
  to tabs; one-liner stays one line; existing blank lines preserved and none
  invented; multi-line `/* */` comment interior untouched; idempotent.
- `internal/jsfmt`: messy JS re-indented to tabs; newlines preserved (ASI);
  template literal interior untouched; regex-vs-division (`a / b` vs `/re/g`)
  not mislexed; comment interior untouched; idempotent; lex error → error.
- `printer`: `<script>` with/without holes formatted; data-island script left
  verbatim; verbatim fallback on lex failure; idempotence; `<script>`-aware
  faithfulness in the property tests.
- `gen`: `WithJSFormatter` sets the field; `mergeConfig` opts-win/base-fallback;
  `runFmt` stays config-agnostic (malformed `gsx.toml` does not break `fmt`).

## Out of scope

- **Alpine attribute JS** (`x-data`, `:class`, `x-show`, `@click`, …) — these are
  single-line expressions inside attribute values; no indentation problem, and
  reflowing JS inside an attribute (quote-safety, attribute layout) is a separate
  surface. Left verbatim.
- **Any reflow / line-break insertion / blank-line insertion** — explicitly not a
  goal; the user does not want invented structure.
- **Intra-line spacing normalization** (`x=1` → `x = 1`) — belongs to a reflow
  formatter, not this re-indenter.
- **A second JS parser / esbuild** — reuse the existing tdewolff lexer; add no
  new parser dependency.

## File-level summary

| File | Change |
|------|--------|
| `internal/reindent/reindent.go` | **new** — `Class`, `Token`, `Adapter`, `Reindent` core |
| `internal/reindent/reindent_test.go` | **new** — core tests via a fake adapter |
| `internal/cssfmt/cssfmt.go` | **rewrite** — delete parser/layout; add `cssAdapter`; `Format` = `Reindent` |
| `internal/cssfmt/cssfmt_test.go` | **rewrite** — re-indent assertions (no reflow/blank-line) |
| `internal/cssfmt/token.go` | unchanged (reused as the CSS tokenizer) |
| `internal/jsfmt/jsfmt.go` | **new** — `jsAdapter` over jsmin's lexer + `Format`/`TokenSignature` |
| `internal/jsfmt/jsfmt_test.go` | **new** |
| `internal/printer/printer.go` | `<script>` → `rawfmt` with `jsFmt`; `printer.jsFmt`; data-island guard |
| `internal/printer/corpus_test.go` | extend the faithfulness canonicalizer to `<script>` |
| `internal/printer/script_test.go` / `script_property_test.go` | **new** |
| `internal/gsxfmt/gsxfmt.go` | thread `jsFmt` (mirror of `cssFmt`) |
| `gen/main.go`, `gen/options.go`, `gen/configfile.go`, `gen/fmt.go` | `config.jsFmt`, `WithJSFormatter`, merge, `runFmt` threading |
| `docs/guide/extensions.md` | drop the CSS "in development" caveat (now shipped); note JS re-indenter + `WithJSFormatter` landed |
