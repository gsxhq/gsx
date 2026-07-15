# Go-expression embedded-literal parity (fmt reindent + minify) — design

**Goal:** A `js\`\``/`css\`\`` literal in **Go-expression position** (inside a
`{{ }}` block, a `{ expr }` hole, a top-level var initializer, or a body
`{ … }`) should get the same treatment element-attribute and `<script>`/`<style>`
literals already get: (C) the formatter re-indents its multi-line body, and
(B) codegen minifies its static text under `[minify]`.

Two facets, two subsystems (formatter, codegen-emit), one theme. Ship **C
first** (the daily formatting pain), then **B**.

## Background

Expression-valued `js\`\``/`css\`\`` literals (PR #106) let authors write
embedded JS/CSS inline in Go — e.g. an Alpine `@change` handler as a value in a
`{{ a := gsx.Attrs{{Key:"@change", Value: js\`…\`}} }}` block. But two passes that
already handle *element-attribute* and *script/style* literals never learned to
reach the Go-expression form:

- **Formatter:** `gsx fmt` leaves the JS/CSS body of a Go-block literal at
  column 0 (verbatim), instead of indenting it under `Value: js\``.
- **Minifier:** `[minify] js="full"` minifies element-attribute and `<script>`
  literals but not Go-expression ones — the generated `_gsxrt.RawJS("…\n…")`
  keeps its newlines/whitespace.

Both are coverage gaps, not intentional exclusions: the expression-valued
literal feature (PR #106) postdates minify Phase 2 (PR #115) and the
embedded-attr fmt work (PRs #114/#116).

## Where these literals live

`js\`\``/`css\`\`` in Go-expression position are `*ast.EmbeddedInterp` nodes
(`Lang`, `Segments []Markup` of Text+Interp, `Stages`, `DoubleQuoted`) — the same
shape as `*ast.EmbeddedAttr` minus name/braces. They appear:

- `GoBlock.Embedded []GoPart` — `{{ … }}` blocks (the common case: a
  `gsx.Attrs{}` value).
- `Interp.Embedded []GoPart` — `{ expr }` holes carrying a literal.
- Top-level `GoWithElements.Parts` / a var initializer.
- As a markup child directly: body-position `{ js\`…\` }` is an
  `*ast.EmbeddedInterp` in the markup tree.

**Scope of each facet (as implemented):** minify (B) reaches ALL of the above.
The fmt re-indent (C) is implemented for the **`{{ }}` block** path only
(`goBlock`); `{ expr }`-hole and top-level-var literals are emitted verbatim by
`goExprValue` and are not re-indented. This is deliberate — the `{{ }}` block is
the case authors hit (a `gsx.Attrs` value) — and it is render-safe either way:
because C adds no indent in those positions, there is nothing there for the
emit-side rebase to strip (and `rebaseEmbedded` correspondingly does not walk
top-level `GoWithElements`).

`Embedded` is populated by codegen's **analyze** pass
(`splitInterpEmbedded`, analyze.go:691) — present at emit/minify time, **nil**
during fmt. The formatter therefore re-derives the split **syntactically** with
`parser.SplitGoExprElements` (it already does: `splitGoBlockParts`,
printer.go:875), never depending on the analyze overlay.

---

## Facet C — formatter re-indents Go-block literal bodies

### Current behavior (the gap)

`goBlock` (printer.go:772) → `fmtGoBlockCode` (839) splits `Code` into
GoText/`*EmbeddedInterp` parts (`splitGoBlockParts`), gofmt-formats the Go, and
renders each literal **flat/verbatim** via `goExprValue`
(printer.go:855-863 — "any newlines it carries are literal content that
multiline reproduces *without re-indenting*"). It returns a single string `s`
that retains gofmt's *relative* indentation; `goBlock` then splits `s` outside
raw strings and prepends the block's **managed** indent (`pretty.HardLine`
inside `pretty.Indent`) per line. A raw-string (`js\`…\``) interior is kept in
one segment and emitted as one `pretty.Text` → its interior newlines print
verbatim at column 0. Hence the un-indented body.

The absolute column of a literal is thus **managed base (a pretty.Indent
level) + gofmt-relative tabs (baked into the Text)** — a hybrid the pretty
engine can't resolve for interior lines, because the gofmt-relative tabs are
not managed levels.

### Design

Make `goBlock` **parts-aware**: render from the structured parts (GoText +
`*EmbeddedInterp`) instead of flattening literals into `s`, so each multi-line
`js\`\``/`css\`\`` literal body is emitted line-by-line — inheriting the block's
managed indent via `HardLine` **and** carrying the literal's own indent baked as
explicit tabs.

Concretely, for the multi-statement (block-style) path:

1. `fmtGoBlockCode` keeps returning the gofmt-normalized structure, but instead
   of baking each literal flat, it exposes the interleaved parts *after*
   formatting: the GoText spans (with gofmt-relative indentation) and, for each
   `*EmbeddedInterp`, the literal node plus the **indent of the line it opens
   on** (`litIndent` = leading whitespace of that GoText's last line — e.g. the
   two tabs before `Value: js\``).
2. `goBlock` emits GoText statement lines exactly as today (`HardLine +
   Text(line)`; the managed base + the line's gofmt-relative tabs). When it
   reaches a literal:
   - Format the body into re-indented logical lines with the existing
     `embeddedAttrLines(Lang, segments, holes, escape)` machinery (shared with
     the attribute path; holes preserved, verbatim multi-line tokens intact).
   - Emit the opener (`…Value: js\``) attached to the current line, then each
     body line as `HardLine + Text(strings.Repeat("\t", litIndent+1) + line)`
     and the closer as `HardLine + Text(strings.Repeat("\t", litIndent) +
     delim + trailingGoText)`. `HardLine` supplies the managed base; the baked
     `litIndent (+1)` tabs supply the Go-structural depth. Absolute column =
     managed base + litIndent (+1) — matching `Value: js\``'s column + one
     level, exactly what the block-layout attribute body already produces.
   - Blank body lines emit as bare `"\n"` (no trailing tabs → idempotence),
     mirroring `embeddedAttrValueDoc`.

Layout selection (inline vs block) reuses the attribute rule: a body whose
formatted form leads with a newline (CSS decls, the author's `\n…\n` handler)
is **block** (delimiters alone, body one level under); a content-adjacent body
(`js\`{ … }\``) stays **inline** (delimiter hugs the first body line). This is
the same `embeddedAttrValueDoc` behavior, just anchored at `litIndent` instead
of the attribute column.

Single-statement inline blocks (`{{ x := 1 }}`) with a one-line literal are
unaffected (they take the `!strings.Contains(s, "\n")` fast path).

### Faithfulness / idempotence

- The formatter still works from `Code` (never the analyze overlay); the parts
  come from the syntactic `splitGoBlockParts`.
- Re-running fmt on its own output must be a fixed point. The body is formatted
  by the same `embeddedAttrLines` the attribute path uses (already idempotent in
  the fmt corpus), and the baked `litIndent` derives from gofmt's stable
  relative indentation — stable whether `Code` arrives verbatim from source or
  re-indented from a re-parsed block (the property `canonGo` relies on).
- Verbatim multi-line tokens inside the body (template literals, block
  comments) stay byte-exact via the existing logical-line threading
  (`ReindentLines`/`FormatLines`).

### Tests (fmt corpus — `internal/gsxfmt/testdata/cases/`)

Per context, `input.gsx` → `fmt.golden`, auto-checked for idempotence + reparse:

- `goblock_js_literal_reindent` — the `gsx.Attrs{}` `@change` shape (block
  layout, holes incl. a binding-position `@{gsx.RawJS(x)} = …`).
- `goblock_css_literal_reindent` — a `css\`\`` value (block layout, CSS decls).
- `goblock_js_literal_inline` — content-adjacent `js\`{ … }\`` stays inline.
- `goblock_literal_verbatim_token` — a template literal / block comment interior
  stays byte-exact under reindent.
- A `{ expr }`-hole / body-position `{ js\`…\` }` case if it routes through a
  different printer path than goBlock (verify during implementation).

---

## Facet B — codegen minifies Go-expression literals

### Current behavior (the gap)

`jsmin.MinifyFile` / `cssmin.MinifyFile` (called in `generateFile`, emit.go:60,
after analyze) walk the **markup tree** — `Element`, `<script>`/`<style>`,
`*ast.EmbeddedAttr` — and never visit `*ast.EmbeddedInterp` in Go-expression
position. The minify helpers (`minifyJSEmbedded`/`minifyJSEmbeddedHoley` and the
cssmin equivalents) already operate solely on a node's `Segments`.

### Design

1. Refactor the minify-embedded helpers to take `*[]ast.Markup` (the `Segments`
   slice) instead of `*ast.EmbeddedAttr`, so they serve both `EmbeddedAttr` and
   `EmbeddedInterp`. (`Stages` don't affect static-text minification.)
2. Extend the `MinifyFile` walk to reach Go-expression literals, using the
   analyze-populated `Embedded` (present at emit time):
   - `minifyMarkup` gains a `*ast.EmbeddedInterp` case (body-position literals).
   - It also descends `GoBlock.Embedded`, `Interp.Embedded`, and — from
     `MinifyFile` — top-level `GoChunk`/`GoWithElements` parts, minifying each
     `*EmbeddedInterp` part by `Lang` (JS in jsmin, CSS in cssmin).
3. Semantics are identical to the attribute path: holeless bodies
   cascade-minify (`cascadeJS` — fragment-aware); holey bodies use the
   free-identifier sentinel round-trip under the full minifier only, left
   unchanged under the safe level. The sentinel round-trip reuses the same
   `*ast.Interp` pointers, so the `resolved` type map (keyed on hole pointers)
   is untouched — including binding-position holes (`gsxHole0z = foundId`
   minifies and splits back cleanly).

### Safety / correctness

- Minify mutates `Segments` on nodes the **emit** reads
  (`embeddedJSValueExpr`), never the printer (which reads `Code`/`Expr`) — so
  fmt is unaffected; the two facets don't interact.
- A Go-expression `js\`\`` used as an attribute value (via `gsx.Attrs` spread)
  is a JS **fragment/handler**, exactly like a direct `@change` attribute →
  `cascadeJS` is the correct treatment (the direct form already minifies this
  way).

### Tests (semantic corpus — `internal/corpus/testdata/cases/`)

Minify runs behind `MinifyFull`; corpus cases pin the safe level and gen/render
goldens. Add cases (mirroring existing `*_attr` minify cases) for a Go-block
`js\`\`` and `css\`\`` value proving the generated `RawJS(...)`/`RawCSS(...)`
static text is minified (holeless), and a holey Go-block literal proving the
safe level leaves it unchanged. A full-cascade e2e (`GSX_MINIFY=full`) validates
on the real `common_edit_components.gsx`.

---

## Non-goals

- No change to element-attribute or `<script>`/`<style>` literal handling.
- No new syntax; `Embedded` overlay semantics unchanged.
- fmt stays parse-only (no analyze / packages.Load on the format path).
