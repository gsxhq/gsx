# Uniform `if`/`switch` branching in attribute and class/style value position

## Problem

gsx has four branching positions around attributes. Two work, two do not:

| Position | `if` | `switch` |
| --- | --- | --- |
| Attribute: `<div { if c { a=1 } }>` | yes | **no** — `expected \`...\` trailing spread inside \`{ }\` attribute` |
| Value: `class={ if c { "a" } else { "b" } }` | yes | yes |
| Value arm holding a literal: `style={ if c { css\`…\` } }` | **no** | **no** — `missing ',' in argument list` |

Two independent gaps:

- **B — no `switch` in attribute position.** `parseSingleAttr` (`parser/attrs.go:265`)
  dispatches on `braceKeyword() == "if"` and sends everything else to
  `parseSpreadAttr`, so `{ switch … }` reports the spread error.
- **A — value-form arms reject `f\`/js\`/css\`` literals.** `ast.ComposedPart`
  (`ast/ast.go:629`) carries `CSSSegments []Markup` for a literal part, but
  `ast.ValueArm` (`ast/ast.go:670`) carries only `Expr` + `Stages`, and
  `parseValueArm` (`parser/valueform.go:101`) routes arm text straight to
  `parsePipe`. A literal is therefore legal as a whole attribute value and as a
  composed part, but not as an arm — the only value position that rejects it.
  All eight combinations (`class`/`style` × `if`/`switch` × `f`/`js`/`css`) fail.

The driving case is a component that must split a CSS value so its separator is
static template text — e.g. `aspect-ratio: <w> / <h>`, where the CSS value
filter rejects an interpolated `/`. Branching selects the declaration shape;
the arm must be able to hold the `css\`\`` literal that does the splitting.

## Decision 1 — attribute `switch` is a real node, not a desugar

Rejected: lowering `{ switch tag { case A: … } }` to a nested `CondAttr` chain
(`tag == A`) in the parser. It touches only two files, but Go's `switch`
evaluates its tag exactly once and an `==` chain re-evaluates it per case. For
a tag that is a call, that is a behaviour change, not a formatting one. It also
forecloses type switches, which the value-form switch already supports
(`class/value_switch_type_switch.txtar`).

So: new `*ast.SwitchAttr` + `*ast.AttrCaseClause`, emitting a real Go `switch`
in the same statement position where `CondAttr` emits a real Go `if`
(`internal/codegen/emit.go:3044` — attribute emission is a sequence of writer
calls between `<tag` and `>`, so a Go control construct wrapping the branch's
attr emits is valid).

**Cost, stated plainly:** `CondAttr` has 30 type-switch dispatch sites across 14
files (9 `emit.go`, 7 `analyze.go`, 2 each `printer.go` / `component_call_plan.go`,
1 each in `wsnorm`, `lsp/definition`, `jsx`, `jsmin`, `cssmin`, `reserved_scan`,
`rebase`, `component_target`, `component_positional_plan`, `component_lsp_facts`),
plus `ast` walk/clone/print. `SwitchAttr` must reach every one that recurses
into branch attrs. This is the failure mode that shipped broken once already
(processing instructions: 11 of ~32 markup walks missed, components emitted
raw), so the exhaustiveness gate below is not optional.

## Decision 2 — value arms reuse the part payload

`ValueArm` gains the literal payload `ComposedPart` already has
(`CSSSegments []Markup`, `CSSDoubleQuoted bool`). `parseValueArm` probes for a
prefixed backtick literal with the same `parseEmbeddedInterpPart` that
`splitComposed` uses (`parser/attrs.go:492`), falling back to `parsePipe`. One
literal-scanning path, two container sites, so they cannot diverge.

`ValueArm` stops being a walk leaf: it gains children.

## Semantics

- Attribute `switch` accepts the same shapes as markup `switch`
  (`ast.SwitchMarkup`): tagged, tagless (`switch { case cond: … }`), multi-value
  case lists, and `default`. Arms are attribute lists, exactly like
  `CondAttr.Then`/`Else`.
- No `fallthrough`. An attribute list is not a statement list; falling through
  would emit two arms' attributes and silently duplicate names.
- A case arm may hold any attribute a `CondAttr` branch may hold, including a
  nested `{ if }` or `{ switch }`.
- Literal arms render identically to the same literal as a composed part: each
  `@{…}` hole is filtered independently by the context's sanitizer. Branching
  never widens what a hole may contribute.

## Exhaustiveness gate

`ast/walk_exhaustive_test.go` already asserts every node kind is reachable from
`Inspect`. Extend the same technique to the attribute walks: a test that
constructs a `SwitchAttr` carrying a marker attribute in every case arm, runs it
through parse → analyze → generate → render, and fails if the marker does not
survive. Add the equivalent for a literal-bearing `ValueArm`.

## Test matrix

Per `CLAUDE.md`, new syntax valid in multiple contexts needs a corpus case per
context.

- Attribute `switch`: tagged, tagless, multi-value case, `default`, no-default
  (no arm matches), nested inside `{ if }`, containing a spread, containing a
  composed `class=`/`style=`, on a component tag (fallthrough/bag routing), and
  a `fallthrough`-rejected diagnostic case.
- Value arms: `class` and `style` × `if` and `switch` × `f`, `js`, `css`.
- Formatter: a fmt-corpus case per new shape (`internal/gsxfmt/testdata/cases`),
  pinning that a `switch` written by the author is printed back as a `switch`.

## Sibling repos

Grammar and highlighting must follow before merge: `../tree-sitter-gsx`,
`../vscode-gsx`, `../gsxhq.github.io` (CodeMirror + VitePress).

### Pre-existing tree-sitter bug to fix in the same pass

Found 2026-07-30 while writing the new `AspectRatio`, and NOT caused by this
change — it reproduces on `{ if … }`, which already shipped.

A conditional attribute whose branches each hold a `style=css\`…\`` literal
parses with a spurious `(stylesheet (ERROR …))` node hanging off
`conditional_attribute`, spanning from the FIRST literal's `embedded_text`
through the LAST one — i.e. across the intervening `} else if … {` gsx syntax:

```gsx
<div
    { if form == aspectRatioPair {
        style=css`aspect-ratio: @{w} / @{h}`
    } else if form == aspectRatioAutoPair {
        style=css`aspect-ratio: auto @{w} / @{h}`
    } else {
        style=css`aspect-ratio: @{ratio}`
    } }
>
```

```
(conditional_attribute
  (stylesheet                 ; <- spurious, spans all three literals
    (ERROR (tag_name) (ERROR) (ERROR) (ERROR) (ERROR) (ERROR)))
  (keyword)
  condition: (binary_expression …)
  (attribute (embedded_attribute value: (embedded_css_literal …)))   ; these are correct
  (attribute_else_clause …))
```

Each `embedded_css_literal` on its own is correct — `embedded_open`,
`embedded_text`, `at_hole`, all well-formed. Only the extra combined
`stylesheet` is wrong.

Cause, confirmed in `tree-sitter-gsx/queries/injections.scm`: all four
`embedded_js_literal` / `embedded_css_literal` patterns (lines 29–46) carry
`#set! injection.combined`. That directive concatenates *every* match of the
pattern in the region into ONE injected document, so three sibling `css\`…\``
literals become a single CSS parse of their concatenated bodies — which is not
valid CSS, hence the ERROR nodes. `injection.combined` is correct for the
`raw_element` patterns above it (a `<style>` element's chunks really are one
stylesheet) and wrong for the literal patterns, where each literal is its own
independent fragment.

Fix: drop `injection.combined` from the four literal patterns (lines 29–46),
keep it on the two `raw_element` patterns. Add a grammar corpus case with two
or more `css\`…\`` literals in one element — the bug needs no conditional at
all to reproduce, the conditional just made it visible.

Note the same latent bug applies to sibling `js\`…\`` literals; the fix covers
both. Verify against `../vscode-gsx` and the CodeMirror grammar in
`../gsxhq.github.io`, which consume the same queries.

## Decision 3 — one literal language per composable attribute, everywhere

Adding literal arms exposed that gsx had no rule at all about WHICH literal a
composable attribute accepts. Measured behaviour before this change:

| written | rendered with a hostile hole | verdict |
| --- | --- | --- |
| `class=f\`…\`` | HTML-escaped | correct |
| `style=css\`…\`` | CSS-value-filtered | correct |
| `style=f\`…\`` | `style="color: red;background:url(javascript:alert(1))"` | **injection** |
| `class=css\`…\`` | `class="color: ZgotmplZ"` | mangles valid classes (`bg-primary/20`) |

The literal's language is what selects the sanitizer applied to its `@{ }`
holes, so a mismatched pairing silently applies the wrong escaper.
`style=f\`…\`` emitted `AttrValue(string(v))` — HTML escaping only, never
`StyleValue` — which is a pre-existing gap on main, not one this branch
introduced (`emitEmbeddedTextAttr` is untouched by this diff).

So the rule is: **`class` takes `f`, `style` takes `css`, and nothing else** —
enforced identically in all three positions (whole value, list entry,
value-form arm) by `checkComposableLiteralLang`. Attributes that compose
nothing (`data-*`, `onclick`, …) stay unrestricted.

Rejected alternative: normalising by sink instead (let the attribute decide the
escaper regardless of the literal's prefix). That would keep every spelling
working, but leaves two ways to write one thing and makes `class=css\`…\`` mean
"CSS-highlighted text that is not CSS" — strictness is the better contract.

**This is a breaking change.** `style=f\`…\`` compiles today and stops
compiling; one corpus case (`textattr/style_spread_merge`) used exactly that
form and moved to `css`. Any downstream use is by definition an unfiltered CSS
sink, so the break is the point.
