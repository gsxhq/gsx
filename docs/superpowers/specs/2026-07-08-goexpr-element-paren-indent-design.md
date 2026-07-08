# Multi-line element literals in Go-expression position: paren-wrap instead of column-align

> **Status: implemented and verified** (this revision documents what actually
> shipped, after two design pivots forced by running the real test suite —
> see "Two hazards found only by running it" below). An earlier revision of
> this doc proposed a printer-only fix; that design was unsound and never
> merged. Do not resurrect it without re-reading this document's "why."

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
cleanly, and scoped to `<tag>`/`</tag>` element and fragment values only (an
explicit user requirement — never an `f\`...\`` embedded-literal value, which
has no matching codegen convention to strip; see below).

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

`internal/printer/printer.go`'s `fmtGoExprParts` (used by `goWithElements`)
already builds a syntactically-complete substituted Go source for every
`GoWithElements` decl: each gsx value is stood in by a placeholder identifier
so `go/format` can lay out the surrounding Go text. Classification reuses this
same idea, parsing a substituted source with `go/parser` and, for each
placeholder's tracked byte range, finding its enclosing node — implemented in
a **new shared package, `internal/goexprshape`** (see below; it turned out
both the printer and codegen need this, for two independent decisions).

```go
type Shape int

const (
    Plain     Shape = iota // call arg, keyless composite-lit element, unrecognized, or unparseable
    ParenWrap               // assignment RHS / return operand / keyed composite-lit field value
)
```

- `ParenWrap`: the placeholder is `*ast.ValueSpec.Values[i]`,
  `*ast.AssignStmt.Rhs[i]`, `*ast.ReturnStmt.Results[i]`, or the `Value` of an
  `*ast.KeyValueExpr` (unwrapping any pre-existing `*ast.ParenExpr` layers
  first, so classification is stable whether or not a decorative paren is
  already present — see "Idempotence" below for why this matters).
- `Plain`: the placeholder is a `*ast.CallExpr.Args[i]`, a keyless
  `*ast.CompositeLit.Elts[i]`, anything else, or the substituted source fails
  to parse.

This is real semantic classification via the Go AST — not a heuristic over
surrounding text (which the project's engineering standard rules out; e.g.
"does the preceding GoText end in `(`" would misfire on a trailing comment,
nested parens, or — as discovered during implementation — a `var (...)`
group's own unrelated closing paren; see "Two hazards" below).

## The real-nesting-depth trap (verified, and how it's fixed)

A first draft of this design built the paren-wrap using only `pretty.Indent`/
`pretty.Group`'s own indent counter, on the theory that `Align` could simply be
deleted. **That's wrong, verified by actually running the construction**
(not just reasoning about it) against a value nested inside a `func` body —
the single most common shape in the corpus (`return.txtar`, `func-local.txtar`,
`method-receiver.txtar` are all "value inside a func body"):

```go
func f() {
	someLongName := (
	<div>              // wrong: should be one tab deeper than "someLongName"
		<p>hi</p>
	</div>
)                       // wrong: should align with "someLongName"
	_ = someLongName
}
```

The reason: a `GoWithElements` decl's own indent counter is **always 0**,
regardless of real Go nesting — the surrounding Go text (including the real
leading tab before `someLongName`) is emitted as literal bytes the pretty
engine never sees as "indent." This is exactly the problem PR #49's `Align`
solved, by reading back the actual current line's leading whitespace from the
output buffer at print time.

The fix is **not** to keep `Align` (with or without its space-padding) — the
real depth is knowable *statically*, without any print-time buffer read-back.
It's just the leading-tab count on the last line of the `GoText` immediately
preceding the value — a string already in hand while building the Doc:

```go
// realTabDepth returns the leading-tab count on the last line of a GoText
// immediately preceding a gsx value — the real Go indentation depth the
// value sits at, which the GoWithElements decl's own indent counter (always
// 0, since its surrounding Go text is literal bytes) cannot see.
func realTabDepth(precedingGoText string) int {
	line := precedingGoText
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	n := 0
	for n < len(line) && line[n] == '\t' {
		n++
	}
	return n
}
```

`goWithElements` wraps each value's Doc — paren-wrap or plain — in
`realTabDepth`-many ordinary `pretty.Indent` calls. Verified with a throwaway
probe directly against `internal/pretty`: output matches prettier's own shape
exactly — `<div>` one tab deeper than `someLongName`, `<p>` one deeper again,
`</div>` and the closing `)` both back at `someLongName`'s own depth. No new
`Doc` kind, no print-time bookkeeping — plain `Indent`, applied a computed
number of times, which is exactly what `Indent` is for. `pretty.Align` is
removed entirely (`internal/pretty/doc.go`, `print.go`, `align_test.go`);
`Print` reverts to a plain `strings.Builder`, a real simplification.

## The paren-wrap Doc

A naive `IfBreak(HardLine, Text(""))` inside a `Group` is a separate trap:
`Group` computes its own `forced` flag via `containsForcedBreak`, which
inspects an `IfBreak`'s *broken* branch unconditionally — so a bare
`HardLine` there would make the group report itself as always-forced,
wrapping even values that fit on one line. The existing element/children
printer avoids exactly this by using `SoftLine` (which never forces a break)
for the optional break points and reserving `IfBreak` only for literal tokens
whose branches are `Text`, never `Line`/`HardLine`. `parenWrapDoc` uses the
identical shape:

```go
func parenWrapDoc(doc pretty.Doc) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.IfBreak(pretty.Text("("), pretty.Text("")),
		pretty.Indent(pretty.Concat(pretty.SoftLine, doc)),
		pretty.SoftLine,
		pretty.IfBreak(pretty.Text(")"), pretty.Text("")),
	))
}
```

`goWithElements` applies `realTabDepth`-many `Indent`s around the *result* of
`parenWrapDoc` (for `ParenWrap` values) or around the bare `doc` (for `Plain`
values) — depth-correction is orthogonal to the paren decision.

## Two hazards found only by running it

Both of the following were caught by actually executing the full test suite
(`go test ./...`, then `go test ./internal/corpus -run TestCorpus -update`)
against a real implementation — not by reasoning about the design. Neither
would have been caught by unit tests scoped to `internal/printer` alone.

### Hazard 1: Go's automatic semicolon insertion breaks codegen

Every gsx element/fragment lowers, via codegen, into
`gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { ... })` — a
closure that **always ends in `})`**. Wrapping that in bare `(...)` in the
*generated* `.x.go`, with the closing paren on its own line, is invalid Go
**every time**, confirmed with a minimal, non-gsx repro:

```go
var n = (
	func() int {
		return 1
	}()
)
```
```
./main.go:6:5: syntax error: unexpected newline, expected )
```

Go's automatic semicolon insertion is a purely lexical rule — a newline after
a line ending in `)` (or an identifier, literal, etc.) always inserts an
implicit semicolon, regardless of bracket nesting. This is not a narrow edge
case; it breaks every single `ParenWrap` position once codegen substitutes the
real closure for the placeholder.

**The fix is not to avoid parens in the `.gsx` source** — it's to make them
purely cosmetic there, invisible to codegen. `internal/codegen`'s two emission
sites that splice a `GoWithElements`'s `GoText` verbatim —
`emit.go`'s `*ast.GoWithElements` case (the real `.x.go` output) and
`analyze.go`'s skeleton builder (a separate, type-checking-only Go file with
its *own*, pre-existing awareness of this exact hazard class, documented at
`emitSkeletonBlockLine`'s doc comment for the unrelated `Wrap(<Foo/>)` case) —
now each classify the `GoWithElements`'s parts (reusing `internal/goexprshape`)
and strip a decorative paren from the adjacent `GoText` **before** writing it,
so the spliced closure never has one next to it:

```go
// parenStripTrailing / parenWrappable — internal/codegen/parenstrip.go
src := p.Src
if i > 0 && parenWrappable(v.Parts[i-1], shapes, i-1) {
	src = goexprshape.StripLeadingParen(src)
}
if i < len(v.Parts)-1 && parenWrappable(v.Parts[i+1], shapes, i+1) {
	src = goexprshape.StripTrailingParen(src)
}
```

Printer and codegen make **independent** decisions from the same
classification: printer decides whether to *add* a decorative paren for
human readability; codegen decides whether to *strip* one before compiling.
Neither needs to know why the other exists.

### Hazard 2: the classifier's own substituted source hit the same ASI bug — twice

Building the classify-only substituted source by naively concatenating verbatim
`GoText` around a placeholder reproduces Hazard 1 **inside the classifier
itself**, on a re-parse of gsx fmt's own previous output (`Icon: (\n\tX\n)}`
puts the placeholder alone on its own line): `go/parser` fails to parse,
`Classify` degrades to all-`Plain`, and codegen never learns there's a paren
to strip.

The first fix attempt — replace every `\n` in the substituted source with a
space — was **also wrong**, caught by a printer regression test: it also
collapses *real, required* statement separators (`n := X\n_ = n` needs that
newline; Go's own ASI depends on it to parse two separate statements at all).

The actual fix distinguishes the two cases structurally: only collapse
whitespace touching a hole when it sits **directly inside an open bracket**
(the nearest non-whitespace byte before the hole is `(`/`[`/`{`, or after it is
`)`/`]`/`}`). A statement separator has no such bracket adjacent to it; a
decorative-or-real paren/bracket does. This is `Classify`'s
`collapseHoleWhitespace`, covered by dedicated regression tests for both
directions of the bug.

### Idempotence and the `Wrapped` field

Classifying `Shape` alone is not enough to decide whether to *strip* a paren:
a `var (...)` group's own closing paren can immediately follow an **unwrapped**
`ParenWrap`-eligible value with no relation to it at all —

```go
var (
	hello = "Hello, World!"

	xx = <p>{ hello }</p>
)
```

— and naively stripping "the `)` that happens to follow a `ParenWrap` value"
ate the group's real closing paren, corrupting the output (caught by an
existing printer test, `TestGoWithElementsEqAlignment`-style cases). `Classify`
therefore returns a `Result{Shape, Wrapped}` per hole: `Wrapped` reports
whether the value is *actually* sitting inside a real `*ast.ParenExpr` in the
given source (the same unwrap loop that stabilizes `Shape` also detects this).
Both printer and codegen gate their paren-stripping on `Wrapped`, never on
`Shape` alone. This is also what makes fmt idempotent: re-formatting
already-wrapped output strips the old paren before recomputing whether to add
a fresh one, rather than adding a second layer.

## `internal/goexprshape` — the shared package

```go
package goexprshape

type Shape int
const (
	Plain Shape = iota
	ParenWrap
)

type Hole struct{ Start, End int } // placeholder's byte range in src

type Result struct {
	Shape   Shape
	Wrapped bool // is there ACTUALLY a paren around this value right now
}

func Classify(src string, holes []Hole) []Result
func StripTrailingParen(src string) string // drops a decorative "(" + trailing ws
func StripLeadingParen(src string) string  // drops a decorative ")" + leading ws
```

Lives outside both `internal/printer` and `internal/codegen` because both need
it and neither imports the other. `internal/printer` builds its `Hole`s from
the same width-matched placeholders `fmtGoExprParts` already builds for
`go/format`; `internal/codegen` (`parenstrip.go`) builds its own
single-rune-placeholder substituted source, since it never needs gofmt-style
column alignment.

## The plain fallback

For `Plain` (call-arg, bare composite-lit elements, unclassified, unparseable):
the value's doc is emitted **unchanged except for the same
`realTabDepth`-many `Indent` wrapping** — no parens. Because the depth fix is
applied uniformly regardless of shape, this bucket does **not** reintroduce
the pre-PR-#49 bug: a multi-line call-arg element nested inside a func body
still gets its closing tag correctly aligned to the real block depth. It just
doesn't get the extra parens/bracket-reflow treatment prettier gives call
arguments and array elements — deferred, see Non-goals.

## Testing

- `internal/goexprshape/shape_test.go`: `Classify` against every recognized
  `Shape`/`Wrapped` combination, including the three regressions found during
  implementation: an already-paren-wrapped multi-line value (Hazard 2), a hole
  followed by a real statement separator (the newline-collapse overcorrection),
  and a `var (...)` group's own closing paren not being mistaken for the
  value's (the `Wrapped` field's reason to exist).
- `internal/printer/goexpr_test.go`: replaces the four tests pinning the old
  hanging-indent with tests pinning paren-wrap (top-level, width-forced,
  fits-flat regression guard, nested-in-func-body real-depth, rename-stability,
  keyed composite-lit field) and plain-indent (call-arg, top-level and nested)
  — plus `TestRealTabDepth` directly. `checkFormat`'s built-in idempotence
  check (format twice, compare) catches the double-wrap regression from
  Hazard 2/`Wrapped`.
- `internal/codegen`: existing tests exercise `emit.go`/`analyze.go` through
  real compilation (`go/packages`), which is what actually caught Hazard 1 —
  no new codegen-specific unit tests were needed beyond the corpus fixture
  updates below, since the corpus suite already compiles every case for real.
- Corpus (`internal/corpus`): **three existing fixtures needed updating** —
  `element-literals/struct-field.txtar`, `element-literals/text-go-keyword.txtar`,
  `fragment-literals/loop-list.txtar` — because their `input.gsx` contained a
  multi-line value in a now-`ParenWrap` position; their checked-in source is
  updated to the new canonical (paren-wrapped) form and `generated.x.go.golden`
  regenerated via `go test ./internal/corpus -run TestCorpus -update` (only
  `//line` numbers shift; the generated closure text itself is byte-identical
  to before, confirming Hazard 1's fix). `render.golden`/`diagnostics.golden`
  are unchanged. An earlier version of this doc claimed no corpus changes were
  needed — wrong; that claim didn't yet account for input.gsx's own canonical
  form changing.
- `make check` (build/vet/full test suite across the main module,
  `playground/server`, and `examples/tailwind-merge`, plus `golangci-lint`)
  is green.

## Non-goals (this pass)

- **Bracket-reflow for call-arg / bare composite-literal elements** — e.g. the
  `[]any{<div>...}` case from PR #49's own commit message, and any multi-line
  `Wrap(<Foo>...</Foo>)` call argument. These get correct real-depth
  indentation (via `realTabDepth`, same as every other position) but keep
  today's inline/bare shape — no parens, no trailing comma, no bracket moved
  onto its own line. Tracked as a `docs/ROADMAP.md` follow-up.
- **Reflowing an outer composite literal's own braces** when one of its keyed
  fields' values paren-wraps — same deferred territory, orthogonal to this fix.
- **`*ast.EmbeddedInterp` (`f\`...\`` literal) values** — never paren-wrapped,
  per explicit scope: codegen lowers these to a string expression, not a
  closure, so they have no ASI hazard and no matching strip convention: adding
  one would be pure scope creep with no bug to fix.
- **No syntax change.** The decorative paren is ordinary, pre-existing Go
  syntax (parens around an operand); `internal/goexprshape` and codegen's
  strip step are purely a formatting/codegen-hygiene concern. No
  `tree-sitter-gsx` / `vscode-gsx` / `gsxhq.github.io` grammar updates needed.

## Files touched

- `internal/goexprshape/` (new package) — `Shape`, `Hole`, `Result`,
  `Classify`, `StripLeadingParen`, `StripTrailingParen`, `shape_test.go`.
- `internal/printer/printer.go` — `fmtGoExprParts` returns
  `[]goexprshape.Result` alongside reformatted parts; `goWithElements`
  branches on `Shape`/`Wrapped` per part (strip-then-maybe-rewrap), applies
  `realTabDepth`-many `Indent`s uniformly; new `parenWrapDoc`, `indentN`,
  `realTabDepth` helpers.
- `internal/pretty/doc.go`, `internal/pretty/print.go` — `Align` removed.
- `internal/pretty/align_test.go` — deleted.
- `internal/printer/goexpr_test.go` — old hanging-indent tests replaced; new
  `TestRealTabDepth`.
- `internal/codegen/parenstrip.go` (new file) — `goWithElementsParenShapes`,
  `parenWrappable`.
- `internal/codegen/emit.go` — `*ast.GoWithElements` case strips decorative
  parens from adjacent `GoText` before splicing the lowered closure.
- `internal/codegen/analyze.go` — skeleton builder's `*gsxast.GoWithElements`
  loop does the same, for the same reason, independently (see Hazard 1).
- `internal/corpus/testdata/cases/element-literals/struct-field.txtar`,
  `text-go-keyword.txtar`, `fragment-literals/loop-list.txtar` — updated to
  new canonical `input.gsx` + regenerated `generated.x.go.golden`.
- `docs/ROADMAP.md` — new entry for deferred bracket-reflow work (call-arg /
  bare composite-lit elements).
