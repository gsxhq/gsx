package printer

import (
	"strings"
	"testing"
)

// A multi-line element literal in Go-expression position hangs off its opening
// tag: children indent one level deeper than `<`, and the closing tag lines up
// under it. The prefix is the line's leading whitespace verbatim plus spaces, so
// the alignment survives any tab width.

func TestGoExprElementHangsOffOpeningTag(t *testing.T) {
	// "\tsomeLongName := " -> prefix is one tab then 16 spaces.
	pfx := "\t" + strings.Repeat(" ", len("someLongName := "))
	src := "package main\n\nfunc f() {\nsomeLongName := <div>\n<p>hi</p>\n</div>\n_ = someLongName\n}\n"
	want := "package main\n\nfunc f() {\n" +
		"\tsomeLongName := <div>\n" +
		pfx + "\t<p>hi</p>\n" +
		pfx + "</div>\n" +
		"\t_ = someLongName\n}\n"
	checkFormat(t, src, want)
}

func TestGoExprElementHangsOffOpeningTagAtTopLevel(t *testing.T) {
	// "var n = " has no leading whitespace -> prefix is 8 spaces.
	pfx := strings.Repeat(" ", len("var n = "))
	src := "package main\n\nvar n = <div>\n<p>hi</p>\n</div>\n"
	want := "package main\n\nvar n = <div>\n" + pfx + "\t<p>hi</p>\n" + pfx + "</div>\n"
	checkFormat(t, src, want)
}

func TestGoExprElementHangsOffOpeningTagInVarGroup(t *testing.T) {
	// "\tn = " -> one tab (verbatim) then 4 spaces.
	pfx := "\t" + strings.Repeat(" ", len("n = "))
	src := "package main\n\nvar (\nn = <div>\n<p>hi</p>\n</div>\n)\n"
	want := "package main\n\nvar (\n\tn = <div>\n" + pfx + "\t<p>hi</p>\n" + pfx + "</div>\n)\n"
	checkFormat(t, src, want)
}

// Renaming the variable re-indents the element, since the hanging indent is
// anchored to the opening tag's column. Pinned so the trade-off is explicit.
func TestGoExprElementRenameShiftsHangingIndent(t *testing.T) {
	pfx := "\t" + strings.Repeat(" ", len("n := "))
	src := "package main\n\nfunc f() {\nn := <div>\n<p>hi</p>\n</div>\n_ = n\n}\n"
	want := "package main\n\nfunc f() {\n\tn := <div>\n" + pfx + "\t<p>hi</p>\n" + pfx + "</div>\n\t_ = n\n}\n"
	checkFormat(t, src, want)
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
