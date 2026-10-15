# Shipped `.x.go` hole line anchors

## Problem

Tools that read the generated `.x.go` — `go build`, `go vet`, structpages-lint,
panic stack traces — map positions back to `.gsx` through `//line` directives.
The emitter anchors each child node (`genNode` → `emitLine`), but nothing inside
an element's attribute list, a component tag's props, or a backtick literal's
later lines. Every Go hole there reports the element's line plus however many
`.x.go` lines precede it: `<a href={"Ghost" |> url}>` on line 22 reported at 29.

The analysis skeleton is unaffected (it anchors each probe itself), which is why
gsx's own diagnostics were right and nothing caught this.

## Rule

1. **Every verbatim run of authored Go text spliced into shipped output is
   preceded by a block-form `/*line file:L:C*/` at that run's own source
   position** — the rule the skeleton and `GoWithElements` already follow. It
   is honoured mid-line, so it is correct where several holes share one emitted
   statement (class parts, `f`/`js`/`css` literal holes, pipe stage args) and
   inside multi-line literals.
2. **Each filter call is anchored at its authored stage name** (`|> url`), so
   the synthesized `URLFor(ctx, …)` call — and a panic's caller frame inside
   it — maps to the stage. A per-attribute `//line` was rejected: placed before
   the attribute's static `S(" name=\"")` write it maps the writer on the next
   line one line late, and it splits the static-write coalescing run.

The anchor goes after a run's leading whitespace (never replacing it — a
newline there can be a statement separator inside a func literal), directly
before the first token.

## Precision

Lines are exact for authored tokens. Columns are approximate: `format.Source`
inserts a space after a block comment and respaces authored expressions, so no
directive can keep columns exact in gofmt-clean output. Synthesized statements
after the first in a hole continue counting from the nearest anchor, as child
interpolations already do.

## Sites

All authored-text entry points in emit:

- `lowerCtx.field` / `header` (bare text: anchored at the field's `*Pos`;
  `NoPos` = text not byte-identical to source → no anchor),
- `fieldPins.raw` / `String` (overlay `GoText` runs, anchored per run/slice),
- `assembleHoleSeed` (backtick-literal `@{}` holes; needs `fset` threaded),
- `rawTextHoleExpr` (`<script>`/`<style>` holes),
- `lowerPipe` stage calls (`NamePos`) and args (`ArgsPos`) — emit only; the
  skeleton probe passes a nil fset.

Plain child holes (`genInterp`) keep their existing `//line` at `{`: already
line-exact, so they are unchanged.

Diagnostics that quoted the lowered expression now quote authored source
(`pipeSourceText`), so no message carries an anchor or gsx-internal code.

## Compatibility

Comment-only change to generated code: runtime behaviour is identical, but most
`.x.go` with dynamic attributes change, so committed files fail drift gates
until regenerated. Ships in v0.4.0 with an "Upgrading: regenerate" note.

## Tests

- `TestShippedHoleLineAnchors` (one Module): a uniquely named probe func per
  hole kind (attr, piped attr with ctx filter + stage args, `(T, error)`,
  class/style parts, multi-line f/js/css literal holes, spread, cond/switch
  attr headers and branches, component props, multi-line tag, both spread-fold
  element paths, children); every probe ident in the `.x.go` must map to its
  authored line.
- Corpus goldens regenerated (comment-only diffs).
- Shape tests that substring-match generated code compare `codeShape(src)`
  (reprinted without comments).
