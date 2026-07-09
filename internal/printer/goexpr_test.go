package printer

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// fmtSourceWidth parses, normalizes, and prints src at an EXPLICIT print width.
// The corpus and the rest of this suite format at width 80; some idempotency
// bugs only manifest at a width where a packed composite-literal item straddles
// the budget, and structurally cannot be seen at 80 (the item breaks on both
// passes there). See TestGoExprCompositeLitMultilineElementIdempotentAt120.
func fmtSourceWidth(t *testing.T, src string, width int) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, src)
	}
	wsnorm.Normalize(f)
	var b strings.Builder
	if err := Fprint(&b, f, width, pretty.DefaultTabWidth); err != nil {
		t.Fatalf("Fprint error: %v", err)
	}
	return b.String()
}

// A multi-line element literal in an assignment/return/keyed-composite-lit-field
// position — a "prefix: value" shape — wraps in (...) when it breaks, mirroring
// prettier's JSX formatting (verified against prettier@3.9.4). The real Go
// nesting depth (which the GoWithElements decl's own indent counter can't see,
// since its surrounding Go text is literal bytes) comes from realTabDepth
// reading the leading tabs off the preceding GoText, not from a column anchor —
// so renaming the value does not reindent it. The parens themselves are purely
// cosmetic: internal/codegen strips them back out before splicing the element's
// lowered closure into the generated .x.go (see codegen's own tests), so they
// never risk Go's automatic semicolon insertion.

func TestGoExprElementParenWrapsAtTopLevel(t *testing.T) {
	src := "package main\n\nvar n = <div>\n<p>hi</p>\n</div>\n"
	want := "package main\n\nvar n = (\n\t<div>\n\t\t<p>hi</p>\n\t</div>\n)\n"
	checkFormat(t, src, want)
}

// The element is 12 characters and fits anywhere. The line is 103 columns
// because of the Go fields around it. Breaking the element's parens does not
// address that; breaking the literal's fields does.
func TestGoExprWideLiteralBreaksFieldsNotElement(t *testing.T) {
	src := "package main\n\nvar nav = []item{\n\t{label: \"Team View\", icon: <UsersIcon/>, page: TeamViewPage{}, pathMatch: \"/team\", nonVendor: true},\n}\n"
	want := "package main\n\nvar nav = []item{\n\t{\n\t\tlabel:     \"Team View\",\n\t\ticon:      <UsersIcon/>,\n\t\tpage:      TeamViewPage{},\n\t\tpathMatch: \"/team\",\n\t\tnonVendor: true,\n\t},\n}\n"
	checkFormat(t, src, want)
}

// A flat element on a wide line never gets DECORATIVE PARENS — those are for a
// genuinely multi-line element (a block child or an author line break) only. When
// there are no fields around it to break (a bare `var name = <div>x</div>`), the
// only way to respect the width is for the element to break its OWN content,
// exactly as it would anywhere else in the document. So at 81 columns the
// single-child `<div>` breaks its child, and no `(`/`)` appears — the removed
// goExprFlatText short-circuit used to render it as fixed text that silently
// overflowed the line instead.
func TestGoExprElementStaysFlatOnAWideLine(t *testing.T) {
	name := strings.Repeat("x", 62)
	src := "package main\n\nvar " + name + " = <div>x</div>\n" // 81 columns flat
	// Unchanged. The author did not ask for a break, so they do not get one, and
	// the line is 81 columns because that is how they wrote it. gofmt leaves an
	// 81-column expression alone too.
	checkFormat(t, src, src)
	if strings.ContainsAny(fmtSource(t, src), "()") {
		t.Error("no decorative parens: the author did not request a break")
	}
}

func TestGoExprElementStaysFlatWhenItFits(t *testing.T) {
	// Regression guard: a short value on a short line never gets parens.
	src := "package main\n\nvar help = <a href=\"/help\" class=\"text-blue-600\">?</a>\n"
	checkFormat(t, src, src)
}

func TestGoExprElementParenWrapsNestedInFuncBody(t *testing.T) {
	// The real-depth case: the closing paren must land at "someLongName"'s own
	// depth (one tab), children one tab deeper — not at column 0 (the decl's
	// own, always-zero baseline).
	src := "package main\n\nfunc f() {\nsomeLongName := <div>\n<p>hi</p>\n</div>\n_ = someLongName\n}\n"
	want := "package main\n\nfunc f() {\n\tsomeLongName := (\n\t\t<div>\n\t\t\t<p>hi</p>\n\t\t</div>\n\t)\n\t_ = someLongName\n}\n"
	checkFormat(t, src, want)
}

// Renaming the value does not reindent it — the anchor is real block nesting
// depth (realTabDepth), not the opening tag's column. This replaces the old
// TestGoExprElementRenameShiftsHangingIndent, which pinned the opposite
// (pre-existing) property.
func TestGoExprElementRenameDoesNotShiftIndent(t *testing.T) {
	src := "package main\n\nfunc f() {\nn := <div>\n<p>hi</p>\n</div>\n_ = n\n}\n"
	want := "package main\n\nfunc f() {\n\tn := (\n\t\t<div>\n\t\t\t<p>hi</p>\n\t\t</div>\n\t)\n\t_ = n\n}\n"
	checkFormat(t, src, want)
}

// A multi-line element in a keyed composite-literal field paren-wraps AND forces
// the literal's other fields one-per-line: its true width is unknowable and it can
// never be a one-liner, so breakWideLiterals (fed the multi-line placeholder as
// its forceMarker) treats the field's line as over budget without measuring it.
func TestGoExprElementParenWrapsForKeyedCompositeLitField(t *testing.T) {
	src := "package main\n\nvar item = NavItem{Label: \"Home\", Icon: <svg class=\"w-5 h-5\">\n<path d=\"M0 0\"/>\n</svg>}\n"
	want := "package main\n\nvar item = NavItem{\n\tLabel: \"Home\",\n\tIcon:  (\n\t\t<svg class=\"w-5 h-5\">\n\t\t\t<path d=\"M0 0\"/>\n\t\t</svg>\n\t),\n}\n"
	checkFormat(t, src, want)
}

// Call arguments and bare composite-literal elements are the deferred
// bracket-reflow bucket (see docs/ROADMAP.md): no parens, no trailing comma,
// no bracket moved onto its own line — but the real-depth fix still applies
// uniformly, so nesting inside a func body no longer regresses to column 0
// the way it did before PR #49.

func TestGoExprElementPlainIndentForCallArgAtTopLevel(t *testing.T) {
	src := "package main\n\nvar wrapped = Wrap(<div>\n<p>hi</p>\n</div>)\n"
	want := "package main\n\nvar wrapped = Wrap(<div>\n\t<p>hi</p>\n</div>)\n"
	checkFormat(t, src, want)
}

func TestGoExprElementPlainIndentForCallArgNestedInFuncBody(t *testing.T) {
	src := "package main\n\nfunc f() {\nwrapped := Wrap(<div>\n<p>hi</p>\n</div>)\n_ = wrapped\n}\n"
	want := "package main\n\nfunc f() {\n\twrapped := Wrap(<div>\n\t\t<p>hi</p>\n\t</div>)\n\t_ = wrapped\n}\n"
	checkFormat(t, src, want)
}

func TestRealTabDepth(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"empty", "", 0},
		{"no preceding newline, no indent", "var help = ", 0},
		{"no preceding newline, with indent (defensive)", "\t\tvar help = ", 2},
		{"one preceding line, no indent", "package main\n\nvar help = ", 0},
		{"one tab of real Go nesting", "package main\n\nfunc f() {\n\tsomeLongName := ", 1},
		{"two tabs of real Go nesting", "package main\n\nfunc f() {\n\tif true {\n\t\tx := ", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := realTabDepth(tt.src); got != tt.want {
				t.Errorf("realTabDepth(%q) = %d, want %d", tt.src, got, tt.want)
			}
		})
	}
}

// A GoWithElements decl's Go text is real, complete Go once each embedded
// element literal is stood in for by an ordinary Go operand. These tests pin
// that it is gofmt'd like any other top-level Go, rather than relayed verbatim
// because one part of it happens to be a gsx element.

// TestGoWithElementsVarGroupFormatted is the reported bug: an element literal
// inside a `var (…)` group left the whole group — including the sibling
// declaration `hello` — unindented, and `gsx fmt -l` called the file clean.
func TestGoWithElementsVarGroupFormatted(t *testing.T) {
	src := "package main\n\nvar (\nhello = \"Hello, World!\"\n\nxx = <p>{ hello }</p>\n)\n"
	want := "package main\n\nvar (\n\thello = \"Hello, World!\"\n\n\txx = <p>{ hello }</p>\n)\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsSiblingDeclsFormatted pins that complete declarations on
// either side of an element literal are gofmt'd. Before the fix a single
// element literal anywhere in the file's Go region relayed the entire region
// — both funcs here — byte-for-byte.
func TestGoWithElementsSiblingDeclsFormatted(t *testing.T) {
	src := "package main\n\nfunc a() {\nx := 1\n_ = x\n}\n\nvar xx = <p>hi</p>\n\nfunc b() {\ny := 2\n_ = y\n}\n"
	want := "package main\n\nfunc a() {\n\tx := 1\n\t_ = x\n}\n\nvar xx = <p>hi</p>\n\nfunc b() {\n\ty := 2\n\t_ = y\n}\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsEqAlignment pins that gofmt's `=` alignment (which keys on
// name width) is applied across a group containing an element literal.
func TestGoWithElementsEqAlignment(t *testing.T) {
	src := "package main\n\nvar (\na = 1\nxx = <p/>\n)\n"
	want := "package main\n\nvar (\n\ta  = 1\n\txx = <p/>\n)\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsMultipleElements pins that a decl holding two element
// literals still formats, and that each element's own printer output is
// spliced back in source order.
func TestGoWithElementsMultipleElements(t *testing.T) {
	src := "package main\n\nvar ns = []any{<a/>,<b/>}\n"
	want := "package main\n\nvar ns = []any{<a/>, <b/>}\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsInsideFuncBody pins an element literal in a short var decl
// inside a func body: the body's statements gofmt around it.
func TestGoWithElementsInsideFuncBody(t *testing.T) {
	src := "package main\n\nfunc f() {\nn := <p>hi</p>\n_ = n\n}\n"
	want := "package main\n\nfunc f() {\n\tn := <p>hi</p>\n\t_ = n\n}\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsTrailingCommentAlignment pins that gofmt's end-of-line
// comment columns — which it computes from each value's rendered width — are
// correct across a group holding an element literal. This only holds because the
// placeholder handed to gofmt is exactly as many runes wide as the element it
// stands for; a fixed-width placeholder aligns `// one`/`// three` to itself and
// leaves `// two` stranded.
func TestGoWithElementsTrailingCommentAlignment(t *testing.T) {
	src := "package main\n\nvar (\na = 1 // one\nxx = <p>{ hello }</p> // two\nb = 3 // three\n)\n"
	want := "package main\n\nvar (\n\ta  = 1                // one\n\txx = <p>{ hello }</p> // two\n\tb  = 3                // three\n)\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsNarrowElementCommentAlignment pins the same for an element
// narrower than any `_gsx`-prefixed placeholder name could be (4 runes), which
// is why the placeholder is a repeated rune rather than a prefixed identifier.
func TestGoWithElementsNarrowElementCommentAlignment(t *testing.T) {
	src := "package main\n\nvar (\na = 1 // one\nxx = <a/> // two\n)\n"
	want := "package main\n\nvar (\n\ta  = 1    // one\n\txx = <a/> // two\n)\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsMultilineElementStillFormats pins that a value with a forced
// break (a block element, which has no single rendered width) does not stop the
// surrounding Go from being formatted: indentation and `=` alignment do not
// depend on value width.
func TestGoWithElementsMultilineElementStillFormats(t *testing.T) {
	src := "package main\n\nfunc f() {\nx := 1\nn := <div>\n<p>hi</p>\n</div>\n_, _ = x, n\n}\n"
	got := fmtSource(t, src)
	if !contains(got, "\tx := 1\n") {
		t.Errorf("surrounding Go not formatted:\n%s", got)
	}
	again := fmtSource(t, got)
	if again != got {
		t.Errorf("not idempotent:\n--- 1 ---\n%s\n--- 2 ---\n%s", got, again)
	}
}

// TestGoWithElementsHoleRuneInSourceFallsBack pins that a source legitimately
// containing the first placeholder rune does not corrupt the re-split: another
// candidate rune is chosen, and the Go still formats.
func TestGoWithElementsHoleRuneInSourceFallsBack(t *testing.T) {
	src := "package main\n\nvar (\ns = \"ᴳ\"\nxx = <p/>\n)\n"
	want := "package main\n\nvar (\n\ts  = \"ᴳ\"\n\txx = <p/>\n)\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsInvalidGoLeftVerbatim pins the graceful degrade: Go that
// gsx parsed but go/format rejects must fall back to the verbatim relay rather
// than dropping source on the floor.
func TestGoWithElementsInvalidGoLeftVerbatim(t *testing.T) {
	// `func f() {` never closes at the gap's end -> format.Source errors.
	src := "package main\n\nfunc f( {\nn := <p>hi</p>\n}\n"
	got := fmtSource(t, src)
	if !contains(got, "<p>hi</p>") || !contains(got, "func f( {") {
		t.Errorf("invalid Go should relay verbatim, got:\n%s", got)
	}
}

// TestGoWithElementsPreservesBuildComment guards the twin of the fmtGoChunk
// corruption bug (see TestFmtGoChunkPreservesBuildComment): fmtGoExprParts
// wraps a GoWithElements region's Go text in the same goExprWrapper synthetic
// package clause, and go/printer hoists a //go:build comment ABOVE that
// clause. The old strip assumed the clause was always the first line of
// gofmt's output, so it deleted the hoisted build comment instead and spliced
// `package _gsxfmt` into the user's source — silently, with exit 0. The strip
// must locate the clause by parsing (StripSyntheticPackage) so the comment
// survives and nothing leaks.
func TestGoWithElementsPreservesBuildComment(t *testing.T) {
	src := "package probe\n\n//go:build linux\nvar greeting = <a href=\"x\">hi</a>\n"
	want := "package probe\n\n//go:build linux\n\nvar greeting = <a href=\"x\">hi</a>\n"
	checkFormat(t, src, want)
}

// TestGoWithElementsNoBuildCommentUnchanged pins that an ordinary
// GoWithElements region (no hoisted build comment) is unaffected by routing
// the clause-strip through StripSyntheticPackage: it removes the clause line
// AND the following blank line, one more byte than the old first-line strip
// removed, but goWithElements' own trimGoTextEdges already trims the first
// part's leading whitespace regardless — so the extra removed byte is
// invisible here. This guards against a regression in that assumption.
func TestGoWithElementsNoBuildCommentUnchanged(t *testing.T) {
	src := "package main\n\nvar greeting = <a href=\"x\">hi</a>\n"
	checkFormat(t, src, src)
}

// TestGoExprCompositeLitMultilineElementIdempotentAt120 is the width-120
// regression the corpus (which formats only at 80) structurally cannot see. A
// composite-literal item packing a wide label next to a MULTI-LINE element used
// to break asymmetrically: pass 1 measured the element as a single rune, so the
// packed item fit under 120 and stayed packed (the element decorative-paren-
// wrapped); pass 2 saw those literal `(`/`)` as ordinary text, crossed 120, and
// exploded every field. fmt(x) != fmt(fmt(x)).
//
// The fix hands breakWideLiterals the multi-line placeholder as a forceMarker, so
// a line holding a multi-line value is over budget without measuring — the value
// forces a break and can never be a one-liner. Both passes now break the fields.
func TestGoExprCompositeLitMultilineElementIdempotentAt120(t *testing.T) {
	label := "T" + strings.Repeat("x", 84)
	src := "package main\n\nvar nav = []item{\n\t{label: \"" + label + "\", icon: <UsersIcon><title>u</title></UsersIcon>, page: P{}},\n}\n"
	want := "package main\n\nvar nav = []item{\n\t{\n\t\tlabel: \"" + label + "\",\n\t\ticon:  (\n\t\t\t<UsersIcon>\n\t\t\t\t<title>u</title>\n\t\t\t</UsersIcon>\n\t\t),\n\t\tpage:  P{},\n\t},\n}\n"
	got := fmtSourceWidth(t, src, 120)
	if got != want {
		t.Errorf("format mismatch at width 120:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if again := fmtSourceWidth(t, got, 120); again != got {
		t.Errorf("not idempotent at width 120:\n--- pass1 ---\n%s\n--- pass2 ---\n%s", got, again)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && indexOf(hay, needle) >= 0
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Feeding gsx fmt its OWN output back in must still gofmt the region. A
// decorative paren puts the element alone on its line inside a bracket —
// exactly the shape Go's automatic semicolon insertion rejects once the
// element is replaced by a placeholder identifier. If the placeholder-
// substituted source is not sanitized before go/format sees it, format.Source
// fails, fmtGoExprParts falls back to relaying the region verbatim, and the
// misindented sibling below never gets fixed.
func TestGoWithElementsReformatsItsOwnParenWrappedOutput(t *testing.T) {
	src := "package main\n\nvar items = []T{\n\t{label: \"a\", icon: (\n\t\t<Icon/>\n\t), page: P{}},\n{label: \"b\"},\n}\n"
	// The paren is the author's break request, so it survives and the element
	// stays broken. What this test is really about is the SIBLING: `{label: "b"}`
	// starts at column 0 in the input, and only a region that actually reached
	// gofmt gets it indented.
	want := "package main\n\nvar items = []T{\n\t{label: \"a\", icon: (\n\t\t<Icon/>\n\t), page: P{}},\n\t{label: \"b\"},\n}\n"
	checkFormat(t, src, want)
}

// Sanitize collapses the whitespace between a hole and an ADJACENT CLOSING
// bracket, because a placeholder identifier ending a line makes Go's automatic
// semicolon insertion break the parse. The whitespace between an OPENING
// bracket and a hole must survive: an opening bracket never ends a line in a
// way that triggers ASI, and go/printer reads the `{`-to-first-element line
// break to decide whether the literal prints on one line. Collapsing it drags
// the first element up onto the brace line.
func TestGoWithElementsKeepsBreakAfterOpeningBrace(t *testing.T) {
	src := "package main\n\nvar icons = []gsx.Node{\n\t<a/>,\n\t<b/>,\n}\n"
	checkFormat(t, src, src)
}

// A paren the author wrote IS the break request — the same signal a newline
// after `>` is for markup. It breaks even though the value would fit, and the
// parens are never silently deleted: deleting them would erase the request and
// let the next pass re-derive a different answer.
func TestGoExprElementBreaksWhenAuthorParenthesizes(t *testing.T) {
	src := "package main\n\nvar n = (<div>x</div>)\n"
	want := "package main\n\nvar n = (\n\t<div>x</div>\n)\n"
	checkFormat(t, src, want)
}
