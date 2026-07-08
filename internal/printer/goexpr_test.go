package printer

import (
	"strings"
	"testing"
)

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

func TestGoExprElementParenWrapsOnWidthOverflow(t *testing.T) {
	// Fits on one line syntactically, but the assignment line alone is 81
	// columns — prettier wraps here too (verified empirically), not just on a
	// forced multi-line source.
	name := strings.Repeat("x", 62)
	src := "package main\n\nvar " + name + " = <div>x</div>\n"
	want := "package main\n\nvar " + name + " = (\n\t<div>x</div>\n)\n"
	checkFormat(t, src, want)
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

func TestGoExprElementParenWrapsForKeyedCompositeLitField(t *testing.T) {
	src := "package main\n\nvar item = NavItem{Label: \"Home\", Icon: <svg class=\"w-5 h-5\">\n<path d=\"M0 0\"/>\n</svg>}\n"
	want := "package main\n\nvar item = NavItem{Label: \"Home\", Icon: (\n\t<svg class=\"w-5 h-5\">\n\t\t<path d=\"M0 0\"/>\n\t</svg>\n)}\n"
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
