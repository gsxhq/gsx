# Format & minify `js``/`css`` attribute-value literals

**Date:** 2026-07-14
**Status:** Approved (design), ready for planning

## Problem

Embedded-literal **attribute values** — `name=js`…``, `name=css`…``, `name=js"…"`,
`name=css"…"` — are the only place gsx knows an attribute value is JavaScript or
CSS, yet they are the one place that knowledge is never used by the tooling:

- **`gsx fmt`** emits their body **verbatim** (`internal/printer/printer.go:1118-1142`,
  `writeEmbeddedAttrSegments` at `printer.go:1185-1202`). A multi-line
  `x-data=js"{ … }"` blob keeps whatever indentation the author left, exactly as a
  plain `"…"` string would. `<script>`/`<style>` **element bodies**, by contrast,
  are re-indented during fmt (`printer.go:459-475`).
- **minify** never touches them. The codegen minify walkers
  (`internal/cssmin/file.go`, `internal/jsmin/file.go`) recurse into attributes
  via `minifyAttrs`, which handles only `*ast.MarkupAttr` and `*ast.CondAttr` —
  there is **no `*ast.EmbeddedAttr` case** (`cssmin/file.go:136-155`). So an
  `onclick=js`…`` / `style=css`…`` body is streamed to the client un-minified.

Motivating case: a large inline Alpine `x-data` object literal. Written as a
plain `"…"` string it is opaque (whitespace is literal value — fmt cannot and must
not touch it). Rewritten as `x-data=js"{ … }"` it becomes formattable *and*
minifiable — but today neither happens.

Non-problem for reference: a plain `"…"` static attribute is opaque HTML-ish text;
its interior whitespace is part of the value. It is out of scope by design (see
Non-goals).

## Scope decision (locked)

Formatting and minification apply **only** to `*ast.EmbeddedAttr` whose `Lang` is
`EmbeddedJS` or `EmbeddedCSS`. Excluded, unchanged, verbatim:

- Plain `"…"` static attribute values (`*ast.StaticAttr`) — opaque, whitespace is
  literal value.
- `f`…`` / `f"…"` literals (`Lang == EmbeddedText`) — generic interpolating
  literal, no sublanguage, nothing to format/minify.

The author opts in per-attribute by writing `js"…"` / `css"…"` (or the backtick
forms).

## Existing infrastructure reused (no new engines)

| Concern | Reused component | Notes |
|---|---|---|
| JS re-indent | `internal/jsfmt` | tdewolff JS **lexer** (no parser); indents by brace depth; **keeps every newline → ASI never altered**; strings/templates/regex/comments opaque. |
| CSS re-indent | `internal/cssfmt` | CSS tokenizer; indents by brace depth; changes nothing else. |
| placeholder/format/restore | `internal/rawfmt` | `Format(segments, holes, Formatter) (pretty.Doc, bool)`; hole → sentinel → format → restore → re-indent. Already powers `<script>`/`<style>` bodies. |
| JS minify (safe) | `internal/jsmin` | tdewolff lexer; strips comments+indentation, collapses intra-line whitespace, **keeps every newline (ASI-safe)**. |
| CSS minify (safe) | `internal/cssmin` | whitespace/comment reductions only, hole-aware, never value rewrites. |
| JS/CSS minify (full) | `internal/fullmin` | tdewolff **parser/AST** `js.Minify`/`css.Minify`; **removes newlines, ASI respected**; holeless blocks only. |
| minify level selection | `gen/minify.go` (`MinifyLevel`), config `[minify]`, `GSX_MINIFY` | precedence option > env > config; public default `MinifyNone`. |

The formatters (`jsfmt`/`cssfmt`) are bound in `FprintWith` as `p.jsFmt`/`p.cssFmt`
(`printer.go:61-67`); a `nil` formatter leaves the body verbatim.

## AST shape

`ast.EmbeddedAttr` (`ast/ast.go:416-436`):

- `Name string`
- `Lang EmbeddedLang` (`EmbeddedJS` / `EmbeddedCSS` / `EmbeddedText`)
- `Segments []Markup` — only `*ast.Text` (unescaped literal body) and `*ast.Interp`
  (`@{ }` holes)
- `Stages []PipeStage` — optional whole-literal `|>` pipeline
- `DoubleQuoted bool`, `Braced bool` — delimiter / brace round-trip facts

Critical fact confirmed from `writeEmbeddedLiteralText` (`printer.go:1221-1235`):
**the AST stores the body UNESCAPED (real JS/CSS).** The delimiter (`` ` `` or `"`)
and bare `@{` are re-escaped only on output. So the formatter/minifier operate on
real source; escaping is purely an emission concern.

---

## Phase 1 — formatting (`gsx fmt`)

### Seam

`attrDoc` (`internal/printer/printer.go:530`) currently sends `*ast.EmbeddedAttr`
to `default: return pretty.Text(attrInline(a))` (flat, single-line). Add an
`*ast.EmbeddedAttr` case (guard `Lang ∈ {JS,CSS}` and the relevant `p.jsFmt`/
`p.cssFmt != nil`).

- **Single-line value** (body contains no newline): unchanged — inline exactly as
  today (still normalizes hole whitespace via the existing path).
- **Already-multi-line value**: produce a structured `pretty.Doc`.

Re-indent only, **never force-wrap**: a single-line literal never becomes
multi-line, and token layout inside is never reflowed. This is what keeps ASI safe
and mirrors `<script>`/`<style>` body behavior.

### Pipeline (reuses `rawfmt` core + two additions)

1. **Extract** `Segments` → interleaved (`segments []string`, `holes []string`)
   with `len(segments) == len(holes)+1`. Each `*ast.Text` → its unescaped body;
   each `*ast.Interp` → its rendered `@{ expr }` string (expr formatted by the gsx
   printer). This is the attribute analogue of `nodesToBody` (`printer.go:1307`),
   which does the same for `<script>`/`<style>` children.
2. **Format** the placeholdered source with `p.jsFmt`/`p.cssFmt` (via
   `rawfmt`'s existing placeholder → format → restore machinery).
3. **Delimiter escaping (NEW)** — applied to the formatted text **before** hole
   restoration: escape the literal's own delimiter (`` ` `` for backtick forms,
   `"` for the `"` forms) and bare `@{`, using the existing
   `writeEmbeddedLiteralText` rule (odd-backslash-run aware). Safe because
   `rawfmt`'s sentinels (`__gsxhole_N_`, valid identifiers) contain no escapable
   character, so escaping the whole placeholdered string cannot corrupt a
   sentinel. Restored holes (`@{expr}`) are inserted *after* escaping, so they stay
   unescaped real holes.
4. **Attr-anchored re-indent (NEW variant of `rawfmt.reindent`)** — TWO layouts,
   chosen by the body's own structure (which the formatter preserves; the signal
   is whether the formatted body starts with a newline):
   - **Inline (content-adjacent)** — the body begins with content, e.g.
     `js"{ … }"` whose `{`/`}` hug the delimiters. The opening delimiter + first
     body line attach to the `name=js"` line (no leading `HardLine`), body lines
     indent one level under the attribute via the formatter's own brace-depth
     tabs (NO extra base `Indent`), and the closing delimiter attaches to the last
     body line.
   - **Block (body on its own lines)** — the body begins with a newline, e.g.
     `css`\n…\n``, and (for flat declaration lists) the formatter emits no
     brace-depth nesting. The delimiters stand alone on their own lines and the
     body is wrapped in one base `Indent` (body one level under the attribute) —
     the same shape as a `<script>`/`<style>` element body. Without this, a
     brace-less CSS body would sit flush with the attribute and the closing
     delimiter would glue to the last declaration.

   Target layouts (approved):
   ```
   	<form
   		x-data=js"{          ← inline: `{` attaches, body nested via braces
   			open: false,
   			active: -1,
   			items() {
   				return q('a:not([x])');
   			},
   		}"
   		class="c"
   	>
   	<div
   		style=css`           ← block: delimiters alone, body +1 (like <style>)
   			color: red;
   			margin: 0;
   		`
   	/>
   ```
5. **Whole-literal pipeline**: any `Stages` (`|> f`) append after the closing
   delimiter, as today.

### Failure / fallback

Any formatter error, panic, arity mismatch, or hole-restore mismatch →
`rawfmt.Format` returns `ok=false` → fall back to today's verbatim inline emission.
Identical safety valve to the body path (`printer.go:460-475`,
`rawfmt.go:69-83`).

### Config

On by default whenever `p.jsFmt`/`p.cssFmt` is set — the same gate that already
governs `<script>`/`<style>` body formatting. **No new config knob.**

### Idempotence

`fmt(fmt(x)) == fmt(x)` is the correctness gate. The re-indent variant must land on
a fixed point (blank lines emit as bare `Text("\n")` with no trailing tabs — the
same idempotence discipline the existing `rawfmt.reindent` already documents and
implements).

---

## Phase 2 — minify (generated output, codegen)

### Seam

`minifyAttrs` in `internal/cssmin/file.go:136-155` handles `*ast.MarkupAttr` and
`*ast.CondAttr`; `internal/jsmin/file.go` walks no attributes at all. Both gain an
`*ast.EmbeddedAttr` case (JS for jsmin, CSS for cssmin) that minifies the literal
body in place. No source-delimiter escaping is needed here (unlike Phase 1 fmt):
minify rewrites the `Segments` `*ast.Text` value with the actual minified
JS/CSS, and codegen emits that value as a Go string literal (which handles its own
escaping).

### Attribute values are FRAGMENTS, not programs (verified)

This is where "mirror `<script>`/`<style>` exactly" breaks down. A `<script>` body
is a program; a `js`…`` attribute value is a fragment — an object literal
(`x-data`), an event-handler statement, or a call expression. tdewolff's
`fullmin.JS` parses its input as a **program**, so a bare `{ … }` object literal
errors ("unexpected : in expression"). Audit of all 73 real `x-data` in
one-learning-gsx: 37 minify raw, 11 need a wrap, 25 are holey; **0 fail** once
wrapped. Wrapping `{ … }` as `( { … } )` makes it a valid expression that fully
minifies (line breaks + whitespace removed, locals mangled, **object keys and
globals preserved** — Alpine/HTMX reference those). Behaviorally verified: the
landing `x-data`'s methods (`moveActive`, `refreshHighlight`) execute with an
identical result trace before and after minify; 35% smaller. `(…)` is kept in the
output — a parenthesized expression is an equivalent `x-data` value.

### Policy

- **Full level** (`ext` = `fullmin.JS`, i.e. `MinifyFull`), JS attrs: cascade —
  `ext(text)` → on parse error `ext("(" + text + ")")` → on error the safe
  `minifyJS(text)` (never errors). This lives in the jsmin walker, using `ext` as
  the primitive, so it also degrades gracefully for an external minifier.
- **Holey JS attrs**: sentinel round-trip — replace each `*ast.Interp` with a
  collision-free VALID-IDENTIFIER sentinel (`__gsxhole_N__`; a free identifier,
  which tdewolff never mangles), run the cascade, split the minified string back
  into `Text`/`Interp`. Safe because attribute holes sit in expression value
  positions (object property values, call args, spreads) — the same technique
  `cssmin` already uses for holey `<style>` (`minifyStyleChildren` /
  `splitSentinels`), adapted to a JS-identifier sentinel.
- **Safe level** (`ext == nil`), JS attrs: `minifyJS` on a holeless body (lexer,
  keeps newlines / ASI-safe); a holey body is left un-minified (matches holey
  `<script>`). Marginal on its own, but strips the indentation Phase 1 adds.
- **CSS attrs**: reuse `minifyStyleChildren(v.Segments, ext)` unchanged — it
  already handles holeless (via `ext`/built-in) and holey (sentinel) CSS. `css`…``
  attribute values are declaration lists; `fullmin.CSS` tolerates them (no error).

### Config

Runs at codegen, gated by the existing `MinifyLevel` (`gen/minify.go`), config
`[minify]`, `GSX_MINIFY`. **No new config surface.** Precedence option > env >
config, as today.

---

## Testing

### Phase 1 (fmt) — `internal/gsxfmt/testdata/cases/*.txtar`

One case per combination, plus idempotence verified by the corpus harness
(`fmt.golden`, then re-run without `-update`):

- `js` × {backtick, `"`} — multi-line, re-indented under the attribute.
- `css` × {backtick, `"`} — multi-line.
- hole present (`@{expr}`) inside a multi-line body — hole preserved, expr
  formatted, surrounding JS re-indented.
- single-line value stays inline (no forced wrap).
- delimiter round-trip: a literal whose body contains its own delimiter and a bare
  `@{` — escaping survives format.
- fallback: a deliberately unparseable body degrades to verbatim (no crash).

### Phase 2 (minify) — semantic corpus `internal/corpus/testdata/cases/**`

`input.gsx` + `generated.x.go.golden` + `render.golden`:

- `js"…"` / `css"…"` attribute literal minified in output; rendered HTML still
  correct.
- full-pass case: newlines removed on a holeless literal; render correct.
- holey JS literal **left un-minified** (ASI safety) — pinned.
- delimiter-escaping preserved through minify.

### Unit

- The delimiter-escaping-before-restore helper: sentinel-safety property (escaping
  a placeholdered string never touches a sentinel).

---

## Non-goals

- Detecting/formatting JS inside plain `"…"` string attributes (gsx cannot know a
  string is JS; re-indenting would change literal value bytes).
- Reflowing / line-wrapping JS or CSS (re-indent only).
- Changing hole (`@{ }`) semantics or the whole-literal `|>` pipeline.
- Any new config knob for either phase.
- The tree-sitter multi-line-attribute fix (separate, already shipped) and any
  CodeMirror / vscode-gsx grammar follow-ups.

## Delivery

One spec, **two sequenced phases**: implement + review Phase 1 (formatting) first
(it unblocks readable source), then Phase 2 (minify). Each phase ships its own
tests; the semantic and fmt corpora stay separate per the repo convention.
