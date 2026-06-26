# gsx JS/CSS Formatting Infrastructure — CSS First (Design)

**Date:** 2026-06-26
**Status:** Approved (brainstorm), pending implementation plan
**Predecessor:** `2026-06-26-gsx-formatter-doc-ir-design.md` (the `internal/pretty` Doc IR this builds on)

## Goal

Format the body of `<style>` elements in `.gsx` source as part of `gsx fmt`,
via a small, reusable, language-agnostic embedding layer plus a built-in
minimal CSS formatter — with an in-process extension point so a fuller
formatter (e.g. a prettier shell-out) can replace the built-in one. JS gets the
same infrastructure in a follow-up spec; `<script>` stays verbatim until then.

## Motivation

Today `<style>`/`<script>` bodies are emitted verbatim by the printer
(`rawHoleChildren`: `Text` concatenated as-is, `@{ … }` holes rendered with the
`@{ }` delimiter). Authors get no formatting inside styles, and the
investment in the `internal/pretty` Doc IR (including the unused `Fill`
primitive, added precisely for this) is not yet realized for embedded
languages.

This effort establishes the embedding infrastructure and lands CSS as the first
real consumer. CSS is chosen first because it is far more regular and
whitespace-tolerant than JS; a minimal JS reflow risks subtly breaking code
(ASI, regex-vs-division ambiguity, template literals) and is deferred to its own
spec where those hazards can be designed for carefully.

## Decisions (from brainstorming)

1. **Default behavior:** a built-in **minimal real CSS formatter** (not no-op,
   not full-fidelity). Replaceable by a plugin.
2. **Extension point:** an **in-process Go func**,
   `func(src []byte) ([]byte, error)`, registered via `gen.WithCSSFormatter(...)`
   — mirroring the existing `cssMin`/`jsMin` minifier options. Shelling out to
   prettier/biome is a user-written wrapper, not core plumbing.
3. **Scope:** **infra + CSS now, JS later.** This spec ships the shared
   language-agnostic infra and a minimal CSS formatter + `WithCSSFormatter`.
   `internal/jsfmt` and `WithJSFormatter` are a follow-up spec.
4. **Interpolation holes:** **placeholder round-trip.** Each `@{ … }` hole is
   replaced by a collision-free sentinel token, the body is formatted, then
   holes are substituted back by matching sentinels.

## Load-bearing invariant: raw-text bodies are Text + Interp only

`parser/markup.go:parseRawTextBody` parses `<style>`/`<script>` bodies into
**only** `*ast.Text` and `@{ … }` `*ast.Interp` nodes. Markup control flow
(`{ if }`, `{ for }`, `{ switch }`) is **not** parsed inside raw-text bodies —
those braces are literal text. The only dynamic construct is a `@{ expr }` hole,
where `expr` is a Go expression (it may call a function but is not block
structure).

Consequence: every hole is a **single inline token**. A sentinel never has to
represent a block node spanning multiple CSS rules; it always sits in a
value / selector / property / at-rule position. The placeholder round-trip
depends on this. If control flow inside `<style>` is ever added, the sentinel
scheme must be reconsidered (a block hole is not substitutable by an inline
identifier token).

## Architecture

Three units with clear seams:

```
internal/rawfmt   language-agnostic embed infra:
                  holes → sentinels → Formatter → restore → re-indent → pretty.Doc
                  (+ verbatim fallback on any failure)

internal/cssfmt   built-in minimal CSS formatter:
                  tokenize CSS → build pretty.Doc → Print → []byte
                  (the first rawfmt.Formatter; uses the pretty IR incl. Fill)

printer + gen     wiring: <style> calls rawfmt with the configured Formatter;
                  gen.WithCSSFormatter plug point; default = cssfmt.Format
```

`<script>` continues through the existing `rawHoleChildren` path unchanged (JS
deferred). Only `<style>` is routed through `rawfmt` in this spec.

### Why two layers (not one CSS-only package)

Hole substitution, restore-with-verification, re-indentation, and the verbatim
fallback are **inherently language-agnostic** — the future JS formatter needs
them identically. They are also the safety-critical part (mis-restoring a hole
corrupts user code). Sharing them from the start avoids re-extracting
safety-critical logic later. The CSS-specific knowledge (tokenizing, layout)
lives entirely in `cssfmt`.

## `internal/rawfmt` — the infrastructure

### Public interface

```go
package rawfmt

// Formatter formats a self-contained source string of an embedded language.
// It returns the formatted bytes, or an error if the input cannot be formatted
// (malformed input, unsupported construct). Returning an error is not fatal:
// the caller falls back to verbatim rendering.
type Formatter func(src []byte) ([]byte, error)

// Format renders a preserve-tag body (a slice of *ast.Text and *ast.Interp
// nodes) by substituting holes with sentinels, running f, restoring holes, and
// re-indenting. It returns (doc, true) on success, where doc is the body's
// content to place between the open and close tags (it begins with an indented
// break and ends positioned for the close tag at the tag's own depth).
//
// It returns (zero, false) when the caller must fall back to verbatim
// rendering — on any Formatter error, recovered panic, or hole-restoration
// mismatch. Format never itself fails fmt on parseable gsx.
func Format(nodes []ast.Markup, f Formatter) (doc pretty.Doc, ok bool)
```

`rawfmt.Format` is width-agnostic: it does substitution, dispatch, restore, and
re-indent, none of which need a column budget. The print width (from
`gsx.toml printWidth`, default 80) is the concern of the concrete `Formatter`
only. The built-in is a closure that binds width —
`func(s []byte) ([]byte, error) { return cssfmt.Format(s, width) }` — so the
`Formatter` signature stays `func([]byte) ([]byte, error)`, matching the
`cssMin`/`jsMin` minifier options exactly.

### Pipeline

1. **Substitute.** Walk `nodes` in order. For `*ast.Text`, write `Value`
   verbatim into a buffer. For `*ast.Interp`, write a sentinel
   `<PREFIX>N<SUFFIX>` where `N` is the hole's 0-based index, and append the
   hole's rendered form to a parallel `holes []string` slice (rendered exactly
   as `rawHoleChildren` does today: `@{ ` + `fmtExpr(expr)` + ` |> stage`… + ` }`).

   The sentinel **prefix/suffix are chosen to be collision-free,
   deterministically**: start from `__GSX_HOLE_` / `__`. If the assembled
   placeholdered source would contain that literal already (scan the
   concatenated `Text` values), extend the prefix's trailing underscores
   (`__GSX_HOLE__`, `___GSX_HOLE___`, …) until neither prefix nor suffix occurs
   in the source. Deterministic input → deterministic sentinel → idempotent
   output. (Not a heuristic: a finite scan that provably yields an absent
   token.)

   The sentinel form `__GSX_HOLE_0__` is a valid CSS identifier token, so it
   survives `cssfmt` tokenization as an ordinary ident in value, selector,
   property, and at-rule-prelude positions.

2. **Dispatch.** Call `f(placeholdered)`. Wrap in a `recover()` so a Formatter
   panic becomes `ok=false`, not a crash. On error or panic → return
   `(_, false)`.

3. **Restore.** Replace every sentinel occurrence in the formatted bytes with
   `holes[N]`. **Verify** that each index `0 .. len(holes)-1` is matched
   **exactly once** (count occurrences while scanning). Any index missing or
   duplicated → return `(_, false)`. Matching is by parsed index, so a formatter
   that reorders rules (and thus holes) still restores correctly; only
   drop/duplicate is rejected.

4. **Re-indent.** Convert the restored multi-line bytes into a `pretty.Doc`:
   split on `\n`; emit each line as `pretty.Text(line)` separated by
   `pretty.HardLine`; a blank line emits a bare `HardLine` with no `Text` (no
   trailing-space pollution). Wrap so the printer places the body one indent
   level under the tag and the close tag back at the tag's depth:

   ```
   Concat(
     Indent(Concat(HardLine, <body lines>)),
     HardLine,   // before the close tag, at tag depth
   )
   ```

   This reuses the existing `multiline` pattern (printer.go:481) but lives in
   `rawfmt` (or a shared helper) so `rawfmt` does not import `printer`.

### Safety philosophy

`ok=false` → caller renders the body verbatim via the unchanged
`rawHoleChildren`. So a malformed CSS body, an exotic hole position the
formatter mangles, or a restoration mismatch degrades gracefully to today's
behavior. `gsx fmt` never breaks on parseable gsx — identical to the existing
Go-fragment fallback philosophy in the printer.

## `internal/cssfmt` — the built-in minimal CSS formatter

```go
package cssfmt

// Format formats a self-contained CSS source string. Returns an error if the
// CSS cannot be tokenized/parsed into the constructs it supports.
func Format(src []byte, width int) ([]byte, error)
```

(The configured `width` is bound into a `rawfmt.Formatter` closure at wiring
time: `func(s []byte) ([]byte, error) { return cssfmt.Format(s, width) }`.)

### Approach

Tokenize → build `pretty.Doc` → `pretty.Print(doc, width)`.

**Tokenizer** recognizes: whitespace, comments (`/* … */`), strings (`'…'`,
`"…"`), identifiers/idents (including sentinel tokens and hyphenated props),
numbers/dimensions, at-keywords (`@media`, `@import`, …), and the structural
punctuation `{ } ( ) ; : ,`. Sentinels need no special handling — they tokenize
as ordinary idents.

**Layout** (the `pretty.Doc` the formatter builds):
- One declaration per line: `property: value;`, single space after `:`,
  no space before `;`.
- Rule: `selector-list {` then `Indent` of declarations on their own lines,
  then `}` on its own line.
- Nested at-rules (`@media (...) { … }`) recurse: prelude, `{`, indented inner
  rules, `}`.
- One blank line between top-level rules / at-rules.
- `Fill` (the greedy per-element primitive) for long **selector lists**
  (`a, b, c, …` wraps only when it overflows `width`) and long
  comma-separated **value lists** (e.g. `font-family`, `grid-template`,
  multi-value `transition`).
- Comments preserved; a comment on its own line stays on its own line, a
  trailing comment stays trailing.

**Errors:** any construct the tokenizer/parser cannot represent (unbalanced
braces, truncated string, a token sequence it does not model) → `error`.
`rawfmt` then falls back to verbatim. `cssfmt` is deliberately minimal:
correctness-or-verbatim, never best-effort-mangle.

`cssfmt` has **no knowledge of HTML nesting** — it always formats from column 0.
`rawfmt` owns the outer indentation.

### Width handling (and its accepted limitation)

`cssfmt.Format` prints at `width` assuming column 0. `rawfmt` then re-indents
the whole block to the tag's depth. A deeply nested `<style>` therefore has its
effective budget reduced by the indent that `rawfmt` adds afterward, so a line
that just fit at column 0 may slightly overflow `width` once indented. This is
accepted for the minimal formatter (documented, not a bug). Passing a
depth-adjusted width *into* the Formatter is explicitly out of scope (it would
change the `Formatter` signature and complicates the plugin contract); a
fuller plugin can choose its own strategy.

## Wiring (printer + gen)

### printer

- `printer` gains a field `cssFmt rawfmt.Formatter`. When nil, it defaults to a
  closure over `cssfmt.Format` at the configured width.
- `Fprint` gains the formatter via the existing width-threading path. Concretely
  `gsxfmt.Format`/`FormatRemovingImports` already carry `width`; they construct
  the default `cssFmt` closure and pass it to the printer. A new
  `gsxfmt.FormatWith(..., cssFmt rawfmt.Formatter)` (or an options struct) lets
  `gen` supply an override; the existing `Format`/`FormatRemovingImports`
  signatures keep working with the default. (Exact signature is a plan
  decision; the constraint is: CLI/LSP use the default, `gen` can override.)
- `element()` `<style>` branch: instead of
  `pretty.Concat(openTag, p.rawHoleChildren(...), close)`, call
  `rawfmt.Format(e.Children, p.cssFmt, width)`. On `ok`, wrap as
  `Concat(openTag, doc, close)`. On `!ok`, use the unchanged
  `rawHoleChildren` path. `<script>` is untouched.

### gen

- `config` gains `cssFmt rawfmt.Formatter` (func-valued, code-only — like
  `cssMin`/`jsMin`).
- `gen.WithCSSFormatter(f rawfmt.Formatter) Option` sets it.
- `mergeConfig`: `merged.cssFmt = base.cssFmt; if opts.cssFmt != nil { merged.cssFmt = opts.cssFmt }`
  — identical to the `cssMin`/`jsMin` merge rule.
- A nil `cssFmt` everywhere means the built-in default applies. `gsx.toml`
  cannot set it (func-valued, like the minifiers) — declarative config stays
  declarative.

## Faithfulness & idempotence contract

Reformatting a `<style>` body **changes the rendered output bytes**: `<style>`
is a preserve context, so its text is emitted verbatim to generated output.
Reflowing the CSS changes whitespace between tokens. CSS is whitespace-
insensitive (outside strings), so semantics are preserved, but the byte-
identical faithfulness check the formatter uses elsewhere no longer holds for
`<style>` bodies. The contract is therefore redefined for them:

- **CSS token equivalence.** Tokenize `src`'s `<style>` body and the formatted
  body with `cssfmt`'s tokenizer; the sequences of **significant tokens**
  (whitespace and inter-token comment-trivia ignored) must be equal. This is a
  real semantic-preservation check and reuses the tokenizer already written.
- **Hole-sequence equality.** The ordered list of `Interp` holes
  (expr + stages, rendered) must be unchanged by formatting. (Combined with the
  index-based restore, this guarantees no hole is dropped, duplicated, or
  altered.)
- **Idempotence.** `fmt(fmt(x)) == fmt(x)` holds for whole files, `<style>`
  bodies included — unchanged from the existing global guarantee.

`<script>` bodies remain verbatim, so their faithfulness is byte-identical as
today; the existing `corpus_property_test.go` check is unchanged for them and
for all non-preserve markup. The property test is extended to apply the
token-equivalence + hole-sequence checks specifically to `<style>` bodies.

## Testing

- **`cssfmt` unit tests:** simple rules; multiple declarations; selector lists
  (short inline, long wrapped via `Fill`); value lists wrapped via `Fill`;
  `@media`/nested at-rules; `@import`/`@charset` simple at-rules; comments
  (own-line and trailing); strings containing braces/semicolons; sentinel
  tokens in value/selector/property positions round-trip untouched; malformed
  inputs (unbalanced brace, truncated string) → `error`.
- **`rawfmt` unit tests:** sentinel substitution + restore round-trip;
  collision-prefix extension when `__GSX_HOLE_` appears in source; restore
  verification rejects a dropped sentinel (→ `ok=false`) and a duplicated
  sentinel (→ `ok=false`); recovered Formatter panic → `ok=false`; re-indent
  produces correct depth and clean blank lines; an `ok=false` path leaves the
  body byte-identical to `rawHoleChildren`.
- **printer integration tests:** `<style>` with no holes; with holes in value
  and selector positions; nested at depth; idempotence; that a `cssfmt` error
  falls back to verbatim.
- **property test:** extend `corpus_property_test.go` with the CSS
  token-equivalence + hole-sequence checks for `<style>` bodies, and add
  `<style>` corpus inputs (minified, multi-rule, interpolated,
  malformed-but-parseable-gsx).

## Out of scope (follow-up specs)

- **JS formatter** (`internal/jsfmt`) and **`gen.WithJSFormatter`** — the same
  `rawfmt` infra, a separate spec for the JS-specific hazards. `<script>`
  stays verbatim until then.
- **Subprocess / CLI plug adapter** (a built-in `CmdFormatter` helper that
  shells out). Users can write the wrapper today; a first-class helper is
  deferred.
- **`<script>` formatting** (waits on `jsfmt`).
- **Passing a depth-adjusted width into the Formatter** (accepted minor
  overflow for deeply-nested styles; see §width handling).
- **Markup control flow inside `<style>`/`<script>`** — not supported by the
  parser today; not added here (and would require revisiting the sentinel
  scheme — see the load-bearing invariant).

## File-level summary

| File | Change |
|------|--------|
| `internal/rawfmt/rawfmt.go` | **new** — `Formatter` type, `Format`, sentinel substitution/restore, re-indent, fallback |
| `internal/rawfmt/rawfmt_test.go` | **new** — infra unit tests |
| `internal/cssfmt/cssfmt.go` | **new** — tokenizer + `pretty.Doc` layout + `Format(src, width)` |
| `internal/cssfmt/cssfmt_test.go` | **new** — CSS formatter unit tests |
| `internal/printer/printer.go` | `<style>` branch routes through `rawfmt`; `printer.cssFmt` field; fallback preserved |
| `internal/gsxfmt/gsxfmt.go` | thread the configured/default `cssFmt` to the printer |
| `gen/configfile.go` / gen options | `config.cssFmt`, `WithCSSFormatter`, `mergeConfig` rule |
| `internal/printer/corpus_property_test.go` | `<style>` token-equivalence + hole-sequence checks; new corpus inputs |
