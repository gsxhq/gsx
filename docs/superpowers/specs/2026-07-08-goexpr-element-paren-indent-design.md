# Multi-line element literals in Go-expression position: paren-wrap instead of column-align

## Problem

PR #49 (`fix(fmt): hang multi-line element literals off their opening tag`)
fixed a real bug — a multi-line element literal in Go-expression position
(`n := <div>...`) broke its lines to column 0 regardless of surrounding
indentation. The fix added `pretty.Align`: a hanging indent that reproduces the
current line's leading whitespace verbatim, then pads the rest with **one
space per rune** so continuation lines land under the value's opening column:

```go
someLongName := <div>
                     <p>hi</p>
                </div>
```

This works, but has two real problems:

1. **Space-padded indentation.** Everything after the verbatim leading
   whitespace is spaces, not tabs — editors/diffs that assume tab-indentation
   throughout a Go file see an odd mix, and reindent-on-save can visibly break
   the alignment.
2. **Rename instability**, already called out and pinned by
   `TestGoExprElementRenameShiftsHangingIndent`: renaming the variable shifts
   every continuation line, since the anchor is the value's column, not its
   block nesting depth.

Prettier's JSX formatter solves the equivalent problem differently: it never
aligns to a column. When a JSX value would render multi-line, prettier wraps it
in `(...)` on their own lines and indents by one **fixed** level from the
enclosing block — verified empirically against `prettier@3.9.4`:

```js
someLongName = (
  <p>
    <span>inline JSX</span>
  </p>
);
```

Renaming `someLongName` to anything does not change the indentation of the
body. This design ports that approach to gsx, scoped to where it applies
cleanly.

## Not every position gets the same treatment

Testing prettier against more contexts shows the paren-wrap is not universal.
For an assignment RHS, a `return` operand, or an object-property value, it
wraps in parens. For a call argument or an array element, it does **not** —
the *existing* enclosing bracket reflows instead (own line per element,
trailing comma):

```js
Wrap(<div>              Wrap(
  <p>a</p>       →        <div>
</div>);                    <p>a</p>
                           </div>,
                         );

icon: <svg>...     →    icon: (
                           <svg>...
                         ),
```

The distinguishing shape is: is the value the **sole thing on its
conceptual line** (prefix, then value — assignment, return, keyed
struct-literal field), or is it **one element of an existing bracketed
list** (call args, bare composite-literal elements)? The first shape gets
paren-wrap. The second needs the enclosing bracket itself restructured
(move the bracket, add a trailing comma) — a strictly bigger change, and
**out of scope for this pass** (see Non-goals).

## Classifying the shape: real AST, not text-sniffing

`internal/printer/printer.go`'s `fmtGoExprParts` (used by `goWithElements`,
printer.go:1256) already builds a syntactically-complete substituted Go
source for every `GoWithElements` decl: each gsx value (element, fragment,
`f\`...\`` literal) is stood in by a placeholder identifier so `go/format` can
lay out the surrounding Go text. This design adds a sibling classification step
that reuses that exact substituted source.

Parse it with `go/parser.ParseFile` and, for each placeholder's tracked byte
offset, find its enclosing node:

```go
type goExprShape int

const (
    shapePlain     goExprShape = iota // plain, unchanged doc (fallback/default)
    shapeParenWrap                     // "prefix: value" — wrap in (...) when it breaks
)
```

- `shapeParenWrap`: the placeholder is `*ast.ValueSpec.Values[i]`,
  `*ast.AssignStmt.Rhs[i]`, `*ast.ReturnStmt.Results[i]`, or the `Value` of an
  `*ast.KeyValueExpr`.
- `shapePlain`: the placeholder is a `*ast.CallExpr.Args[i]`, a keyless
  `*ast.CompositeLit.Elts[i]`, anything else, or the substituted source fails
  to parse.

This is real semantic classification via the Go AST already being built for
formatting purposes — not a heuristic over surrounding text (which the
project's engineering standard rules out; e.g. "does the preceding GoText end
in `(`" would misfire on a trailing comment or nested parens). No new parse
pass beyond what already exists to reformat the surrounding Go text; the
classification and the `go/format.Source` call both run over the same
substituted source built once.

`goWithElements` receives the per-part shape alongside the (possibly
reformatted) parts and picks the Doc construction accordingly. This applies
uniformly to every `goExprValue` kind — `*ast.Element`, `*ast.Fragment`, and
`*ast.EmbeddedInterp` (an `f\`...\`` literal in value position) all go through
the same classify-then-wrap path, since `fmtGoExprParts` already treats them
identically when building the substituted source. A multi-line `f\`...\``
literal's width is measured by `fits()`'s existing rune-count-of-`Text`
handling, which does not special-case embedded `\n` — a pre-existing
characteristic of `internal/pretty`, not something this design changes;
`Group.forced` still catches it correctly whenever an *element* is the value,
since block elements signal their own forced break via `BreakParent`/`HardLine`
independent of this Text quirk.

## The paren-wrap Doc

A naive `IfBreak(HardLine, Text(""))` inside a `Group` is a trap: `Group`
computes its own `forced` flag via `containsForcedBreak`, which inspects an
`IfBreak`'s *broken* branch unconditionally — so a bare `HardLine` there would
make the group report itself as always-forced, wrapping even values that fit
on one line. The existing element/children printer avoids exactly this by
using `SoftLine` (which never forces a break) for the optional break points
and reserving `IfBreak` only for literal tokens whose branches are `Text`,
never `Line`/`HardLine` (see printer.go:353-356, the opening-tag/children
wrap). This design uses the identical shape:

```go
pretty.Group(pretty.Concat(
    pretty.IfBreak(pretty.Text("("), pretty.Text("")),
    pretty.Indent(pretty.Concat(pretty.SoftLine, doc)),
    pretty.SoftLine,
    pretty.IfBreak(pretty.Text(")"), pretty.Text("")),
))
```

`containsForcedBreak` on this Concat reduces to whatever `doc` itself carries
(author-written multi-line content propagates a real forced break; `SoftLine`
and `Text` never do) — so the group is forced only when the value genuinely
must be multi-line, falls through to `Print`'s width `fits()` check otherwise
(matching prettier's own width-triggered wrapping, verified empirically: a
short single-line JSX value still gets paren-wrapped if the assignment line
itself overflows 80 columns), and renders completely flat — parens as `""` —
when the value fits. No change to any currently-single-line var/return/
struct-field output.

Example — `struct-field.txtar`'s `Icon: <svg class="w-5 h-5"><path .../></svg>`
(already multi-line in its author source) becomes:

```go
var item = NavItem{Label: "Home", Icon: (
	<svg class="w-5 h-5">
		<path d="M0 0"/>
	</svg>
)}
```

Note the outer `NavItem{...}` braces are **not** reflowed (no trailing comma,
no brace-per-line) — that's the same bracket-reflow territory deferred for
call-arg/bare-composite-lit elements, just visible one level out. Only the
field's own value gets paren-wrapped.

## The plain fallback

For `shapePlain` (call-arg, bare composite-lit elements, unclassified,
unparseable): the value's doc is emitted **unchanged** — no `Align`, no extra
`Indent`. This is a deliberate, informed reversion to the pre-PR-#49 behavior
for this bucket specifically: the closing tag lands at column 0 regardless of
real nesting depth, exactly the bug PR #49 fixed. There is no principled
partial fix available here without tracking the surrounding Go text's true
indent depth through `goWithElements` (which the current architecture doesn't
carry — the enclosing Go text is emitted as literal bytes, and the decl's own
indent level is always 0), which is exactly the bracket-reflow work being
deferred. Guessing an indent count instead of doing that properly would be the
kind of heuristic the project's engineering standard rules out — so this pass
takes the explicit, honest trade instead: zero space-based indentation
anywhere, at the cost of leaving this one bucket exactly where it was before
PR #49.

## Retiring `pretty.Align`

With every position resolved to either paren-wrap or bare/unchanged, nothing
uses the column-hanging behavior `Align` provided. Remove it entirely:

- `internal/pretty/doc.go` — remove `kindAlign`, `Align()`, and its
  `containsForcedBreak` case (reverts to `case kindIndent:` alone).
- `internal/pretty/print.go` — remove `cmd.align`/`cmd.alignCol`, the
  `kindAlign` case, and `alignPrefix()`. The `[]byte` + `lineStart` bookkeeping
  in `Print` existed solely so `Align` could read back the current line's
  leading whitespace; with `Align` gone, `Print` reverts to a plain
  `strings.Builder` — a real simplification, not just a swap.
- Delete `internal/pretty/align_test.go` (4 tests, all `Align`-specific).

## Testing

- `internal/printer/goexpr_test.go`: replace the four tests pinning the old
  hanging-indent (`TestGoExprElementHangsOffOpeningTag`, `…AtTopLevel`,
  `…InVarGroup`, `TestGoExprElementRenameShiftsHangingIndent`) with tests
  pinning the new behavior:
  - paren-wrap for `var`, `return`, and a keyed composite-literal field, each
    covering: author-forced multi-line, width-forced-but-source-single-line,
    and fits-on-one-line (stays flat, no parens — regression guard).
  - plain/unchanged output for a multi-line call-arg element, with a comment
    cross-referencing the ROADMAP entry for the deferred bracket-reflow work.
  - a rename of the `var`/`return` case no longer changes the body's
    indentation (replaces the intent of the deleted
    `TestGoExprElementRenameShiftsHangingIndent`, now proving the opposite
    property).
- `internal/pretty`: no new tests needed beyond what already exercises
  `Group`/`IfBreak`/`SoftLine` composition; `align_test.go` is deleted, not
  replaced (the behavior it pinned no longer exists).
- Corpus (`internal/corpus`): no changes needed. `generated.x.go.golden` /
  `render.golden` don't encode whitespace, and
  `internal/printer.TestCorpusIdempotence` only checks
  `fmt(fmt(x)) == fmt(x)`, not a byte-for-byte stored golden — it exercises
  `struct-field.txtar`'s existing multi-line input against the new logic
  automatically.
- `make check` / `make ci` must stay green (gofmt + `gsx fmt` drift checks
  included).

## Non-goals (this pass)

- **Bracket-reflow for call-arg / bare composite-literal elements** — e.g. the
  `[]any{<div>...}` case from PR #49's own commit message, and any multi-line
  `Wrap(<Foo>...</Foo>)` call argument. These keep today's plain/bare
  behavior (column-0 closing tag). Tracked as a `docs/ROADMAP.md` follow-up:
  reflowing the enclosing bracket (moving `(`/`[`/`{`, adding a trailing
  comma) the way prettier does for call arguments and array elements.
- **Reflowing an outer composite literal's own braces** when one of its keyed
  fields' values paren-wraps (the `NavItem{...}` example above) — same
  deferred territory, orthogonal to this fix.
- **No syntax change.** This is a whitespace/text-shape formatting change
  only; parens around a Go operand are already valid, unremarkable Go. No
  `tree-sitter-gsx` / `vscode-gsx` / `gsxhq.github.io` grammar updates needed.

## Files touched

- `internal/printer/printer.go` — `fmtGoExprParts` gains AST-based shape
  classification; `goWithElements` branches on shape per part.
- `internal/pretty/doc.go`, `internal/pretty/print.go` — remove `Align`.
- `internal/pretty/align_test.go` — deleted.
- `internal/printer/goexpr_test.go` — four tests replaced.
- `docs/ROADMAP.md` — new entry for deferred bracket-reflow work.
