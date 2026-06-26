# gsx formatter: width-aware layout on a reusable Doc IR

**Date:** 2026-06-26
**Status:** Approved (design)
**Topic:** Replace the gsx printer's purely-structural, render-direct layout with a
width-aware pretty-printer built on a reusable, language-agnostic document IR.

## Problem

The gsx formatter eliminates line breaks too aggressively. Given:

```gsx
<p class="text-sm text-slate-500">
    by {props.Author.Username}
    { if props.Category.Slug != "" {
        · <a class="hover:underline" href={ categoryPage{} |> url("slug", props.Category.Slug) }>{props.Category.Name}</a>
    } }
</p>
```

the formatter collapses the whole `<p>` onto a single line, destroying readability.

### Root cause

`internal/printer/printer.go` decides layout structurally, by node type, and writes
strings to a buffer as it goes — it never measures whether a subtree fits. Its
`isBlockList` (printer.go:127-138) forces a list **inline** whenever it contains *any*
surviving `Text` node, on the rationale that "breaking around it would alter rendering."

That rationale is overly conservative. gsx already runs a JSX-style whitespace pass
(`internal/wsnorm`): within a non-preserved children list it **drops** any
newline-containing whitespace run and **collapses** every other run to a single space.
Therefore a line break inserted at the right boundary is render-faithful — it
normalizes away on re-parse, yielding the identical AST. The printer is refusing
breaks that are in fact safe.

### Scope is bigger than children

The real target is the `his-project` design system (`ds/`), whose `.templ` components
the gsx formatter must format at least as well as templ does ("do better, not less").
Those components rely on layout the current gsx printer cannot produce:

- **Attribute-list wrapping.** An opening tag with conditional attributes or many
  attributes breaks one-attr-per-line with `>` on its own line:
  ```gsx
  <a
      if p.ID != "" {
          id={ p.ID }
      }
      href={ p.Href }
      class={ buttonClass(p) }
      { p.Attributes... }
  >
  ```
  gsx currently prints conditional attributes inline (`condAttrChain` →
  `{ if c { a } }` on the tag line).
- **Multi-line attribute values.** A long `class={ … }` wraps as gofmt'd Go:
  ```gsx
  class={
      utils.TwMerge(
          "text-[0.8rem] font-medium",
          messageVariantClass(p.Variant),
          p.Class,
      ),
  }
  ```
  gsx currently collapses every attribute value to one line via `fmtExpr`.
- **Go-comment fidelity.** `ds/dialog` has a multi-line comment *inside* a
  `class={ utils.TwMerge( /* … */ ) }` expression. gsx's `fmtExpr` formats a bare
  expression node with `go/format.Node`, which **drops comments** (they attach to the
  FileSet, not the node). "Do better" requires preserving them.

## Goals

1. Fix the reported over-collapsing bug: mixed text + interpolation + control-flow +
   element content lays out readably, breaking only at whitespace-safe boundaries.
2. Make the formatter **width-aware**: a list lays out flat if it fits within a
   configurable print width, else it breaks.
3. Support **attribute-list wrapping** and **multi-line attribute values**, preserving
   Go comments inside expressions.
4. Build the layout engine as a **reusable, language-agnostic Doc IR** — the foundation
   the in-progress JS and CSS formatters will share.
5. Preserve the existing contracts: **render-faithfulness** (re-parse + `wsnorm` yields
   the same AST) and **idempotence** (printing an already-formatted file is a no-op).

## Non-goals

- No greedy word-wrap (`Fill`) for gsx children — layout is **all-or-nothing per list**
  (each list fits on one line, or every safe boundary in it breaks). `Fill` is still
  *implemented* in the IR because the CSS/JS formatters need it; gsx's printer just
  does not use it for children.
- No reflow of `pre`/`textarea`/`script`/`style` bodies — they stay verbatim, exactly
  as today.
- No new config beyond `printWidth`.

## Design decisions (resolved during brainstorming)

| Decision | Choice |
| --- | --- |
| Break policy | Width-aware fill (wrap when a line exceeds the print width). |
| Break discipline | All-or-nothing per list (`Group` + `SoftLine`), not greedy `Fill`. |
| Print width | Configurable via `gsx.toml` `printWidth`, default **80**. |
| Tab measurement | Each indent level counts as **4 columns** for the fit check (fixed, documented). |
| Attribute wrapping | **In scope** for this deliverable. |
| Conditional attrs | Always break the opening tag (templ-style). |
| Go-comment fidelity | **In scope**; replace bare `format.Node` with a comment-preserving path. |
| IR home | New `internal/pretty` package, language-agnostic, separately unit-tested. |

## Architecture

Three layers, bottom-up.

### Layer 1 — `internal/pretty`: the Doc IR (reusable foundation)

A standalone Wadler/Prettier-style document model. No dependency on `ast`, `gen`, or
any gsx specifics — so the JS and CSS formatters can import it unchanged.

**Primitives (constructors returning an opaque `Doc`):**

- `Text(s string)` — a literal fragment containing no newline. Multi-line content is
  expressed with `HardLine`, never as raw `"\n"` inside `Text`.
- `Concat(docs ...Doc)` — ordered sequence.
- `Indent(d Doc)` — render `d` with the indent level increased by one.
- `Line` — flat → a single space; break → newline + current indent.
- `SoftLine` — flat → nothing; break → newline + current indent.
- `HardLine` — always a newline + indent; propagates "must break" to every enclosing
  `Group` (implicit `BreakParent`).
- `Group(d Doc)` — the decision unit: render `d` flat if it fits the remaining width on
  the current line, else render it broken.
- `Fill(docs ...Doc)` — greedy per-element wrap (provided for CSS/JS; unused by gsx
  children).
- `IfBreak(broken, flat Doc)` — emit `broken` when the enclosing group breaks, else
  `flat` (e.g. trailing commas / separators).
- `BreakParent` — force the nearest enclosing `Group` to break.

**Engine (the standard two-function algorithm):**

- `fits(remaining int, cmds []cmd) bool` — lookahead that walks pending commands in flat
  mode until a forced break or width exhaustion. Because it consumes *pending* commands,
  trailing same-line content (a closing `</p>`, a ` }` after a control-flow body) is
  correctly counted toward the fit decision.
- `best(width, indentCols int, doc Doc) string` — produces the final layout, choosing
  flat/break per `Group` via `fits`.

**Measurement:** column position = `indentLevel * tabWidth + charsSinceLineStart`, with
`tabWidth = 4`. Indentation is emitted as literal tabs (matching today's printer) but
measured at 4 columns.

**Determinism / idempotence:** `best` is a pure function of `(Doc, width)`. The Doc is a
pure function of the AST. So formatting is deterministic, and since re-parsing discards
cosmetic whitespace and reproduces the same AST, the second format makes identical
decisions → byte-identical output.

**Testing:** `internal/pretty` has its own unit tests (flat-fits, overflow-breaks,
nested groups, hardline propagation, `IfBreak`, `Fill`, tab-width measurement),
independent of gsx.

### Layer 2 — `internal/printer`: rebuild on the IR

Every `printer` method changes from "append to `bytes.Buffer`" to "return a
`pretty.Doc`". `Fprint` builds the document for the file and calls
`pretty.best(width, 0, doc)`. `Fprint` gains a width parameter (see Layer 3).

#### Children & control-flow layout

A children list becomes a `Group` of **segments** joined by `SoftLine` at
**whitespace-safe boundaries only**.

- **Segment** = a maximal run of adjacent children that must stay on one line because a
  *significant space* glues them. A significant space exists at boundary `(i, i+1)` iff
  the left child is a `Text` whose value ends in `' '`, or the right child is a `Text`
  whose value starts with `' '`. (Significant spaces live only inside `Text` values;
  `Interp`/`Element`/control-flow never carry surrounding spaces — `wsnorm` guarantees
  this.) Within a segment the space is literal text and never breaks.
- **Safe boundary** = a boundary with no significant space on either side. These become
  the `SoftLine` separators. A `SoftLine` here is faithful: flat → nothing (matching the
  AST's direct adjacency, e.g. `{author}{ if … }`); broken → newline + indent, which is
  an all-whitespace newline run that `wsnorm` drops on re-parse → adjacency preserved.
- **All-or-nothing:** the list is one `Group`. Fits flat → single line. Else every
  `SoftLine` breaks; each segment sits on its own line at `parentDepth+1`; the closer is
  re-indented to `parentDepth`.
- **Edge guard (faithfulness):** the leading/closing block breaks add whitespace right
  after the opener and right before the closer. If the **first** segment begins with a
  significant leading space, or the **last** segment ends with a significant trailing
  space, that added whitespace would be absorbed (newline run → dropped), changing the
  AST. Such a list is therefore forced **inline** (never broken). This is rare and
  preserves today's correctness.

Block-driving constructs:

- `if` / `for` bodies become `Group`s: short → flat `{ if c { · <a>…</a> } }`; long →
  block. Inline form keeps the `{ … }` single-space padding (so an inline body that
  begins/ends with an `Interp` does not produce `{{`/`}}`), exactly as `cfBody` does
  today.
- `switch` stays **hard-broken** (cases always on their own lines) via `HardLine`.
- A multi-statement `{{ … }}` Go block emits content containing `HardLine`, forcing its
  enclosing group to break.

#### Attribute-list layout

The opening tag `<tag …attrs…>` is a `Group`:

- Flat → `<tag a b c>`.
- Broken → `<tag` then each attribute on its own `Indent`ed line, then `SoftLine` + `>`
  on its own line at the tag's depth.
- **Forced broken** (`BreakParent`) when the attribute list contains any `CondAttr`
  (conditional attribute) — matches templ and reads better. Otherwise broken only on
  width overflow.
- **A broken opening tag forces the children to break too.** (Avoids the jammed
  `>{ children… }</div>`; matches templ.) Implementation: when the opening-tag group
  breaks, propagate a break into the children group (e.g. the element emits a
  `BreakParent` into the children group, or the element shares one enclosing group whose
  break covers both — chosen during implementation, but the observable rule is: tag
  broken ⟹ children broken).

#### Attribute values

`ExprAttr` / `ClassAttr` / `MarkupAttr` values may render **multi-line**:

- The Go expression is formatted with comments preserved (see below). A multi-line
  result is `Indent`ed under the attribute and forces the attribute's own line to break
  (`class={` / newline-indented value / `}`).
- A single-line result stays inline (`class={ … }`), as today.

#### Go-fragment formatting with comment fidelity

Replace the bare-`format.Node` path used by `fmtExpr` (and friends) where comments can
appear, with a **comment-preserving** approach: wrap the fragment in a complete Go file,
run `format.Source` (which preserves comments through the FileSet), then extract the
formatted span. The extraction must handle multi-line results (the value may span
several lines with interior comments). Helpers that format constructs which cannot carry
comments (param lists, for-clauses, case lists) may keep the existing node-based path.

#### Preserved-tag and raw-hole paths

`pre`/`textarea`/`script`/`style` (`isPreserveTag`) and the `@{ }` raw-hole printing
(`rawHoleChildren`, `JSAttr`) keep their current verbatim behavior, re-expressed as
`Text` + `HardLine` docs so they pass through the engine unchanged. The JS/CSS formatter
work (separate effort) may later replace the verbatim `script`/`style` bodies with real
formatting on this same IR; this spec leaves them verbatim.

### Layer 3 — Config & integration

`printWidth` is discovered and loaded with the rest of `gsx.toml`, then **threaded down**
as a plain `int`. The printer and `gsxfmt` must not import `gen` (layering / cycle
avoidance): they receive the width as a parameter.

- `gen/configfile.go`: add `PrintWidth int \`toml:"printWidth"\`` to `tomlConfig`; carry
  it into the internal `config`; default to 80 when unset/zero; merge semantics
  consistent with `mergeConfig` (a programmatic override wins over the file value).
- `internal/printer`: `Fprint(w, f, width)` — width parameter (0 ⇒ default 80 inside the
  printer as a safety net).
- `internal/gsxfmt`: `Format` / `FormatRemovingImports` gain a width parameter (or a
  small options struct) and pass it to `Fprint`.
- Callers:
  - `gsx fmt` CLI — discovers `gsx.toml` (existing discovery), passes `printWidth`.
  - LSP `internal/lsp/format.go` (and the `gen/lsp*.go` config plumbing) — passes the
    workspace's loaded `printWidth`.
- A file with no `gsx.toml` (or no `printWidth`) formats at width 80 with no behavior
  regression beyond the intended layout changes.

## Faithfulness & idempotence argument (summary)

- Breaks are inserted **only** at safe boundaries and at block edges that pass the edge
  guard. Each inserted break is an all-whitespace, newline-containing run, which
  `wsnorm` drops on re-parse → the re-parsed+normalized AST equals the original.
- Significant spaces are never crossed by a break — they remain literal inside `Text`.
- Layout is a deterministic pure function of the AST + width, so re-formatting is a
  fixed point.

These are the two existing corpus contracts (`TestCorpusIdempotence`,
`TestCorpusFaithfulness`); they must stay green.

## Testing plan (TDD)

Per the project convention, every syntax/codegen/format change ships txtar corpus +
unit coverage.

**`internal/pretty` unit tests:** flat-fits-on-one-line; overflow-forces-break; nested
groups (inner fits while outer breaks); `HardLine` propagation to enclosing groups;
`IfBreak` both modes; `Fill` greedy wrap; tab-width=4 measurement; empty/edge docs.

**Printer corpus cases (new):**
- The reported middot `<p>` example → the readable multi-line target.
- "Fits flat" inline case (`<span>Hello <a href="/x">world</a></span>` stays one line).
- Conditional-attribute wrapping (`ds/button`, `ds/form`, `ds/dialog` patterns).
- Multi-line `class={ TwMerge( … ) }`, including one with a Go comment inside the call.
- `switch` hard-break; multi-statement `{{ }}` forcing a parent break.
- Edge-space fallback (leading/trailing significant space ⇒ forced inline).
- Broken-opening-tag ⇒ broken children.
- `printWidth` sensitivity: the same input at width 80 vs a wider width.

**Regression:** existing idempotence + faithfulness corpus stays green; update
`TestElementInlineTextAndElement` and any other unit test that encodes the old
"any text ⇒ inline" rule (these assert the behavior we are deliberately changing).

**Acceptance:** formatting representative `ds/` components (translated to gsx, or the
equivalent gsx fixtures) produces stable, readable, idempotent output at least on par
with templ.

## Risks & open items

- **Go-comment extraction.** `format.Source`-then-extract for multi-line expressions is
  fiddlier than `format.Node`. Mitigation: keep the node-based path for comment-free
  fragments; use the source-based path only where comments can appear (attribute value
  expressions). Verify against the `ds/dialog` comment-in-`TwMerge` case.
- **Broken-tag ⇒ broken-children coupling.** Needs a clean encoding in the IR (shared
  group vs. injected `BreakParent`); decided in implementation, but the observable rule
  is fixed here.
- **Scope size.** The printer rewrite touches every node kind. Mitigated by TDD and the
  two standing corpus contracts, which catch faithfulness/idempotence regressions.
- **Tab width = 4 is a fixed assumption.** If it proves wrong for real files it can be
  promoted to config later; out of scope now.

## Out of scope (future, separate specs)

- Real JS/CSS formatting of `script`/`style` bodies on this IR (separate agent/effort).
- Greedy `Fill`-based layout for gsx (only the IR primitive is built now).
- Making `tabWidth` configurable.
