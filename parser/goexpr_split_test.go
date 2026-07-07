package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// splitAt seats a fresh parser over src (via the package's testParser helper)
// and calls splitGoElements with base = the position of src[0] in that
// parser's file — mirroring how parser/file.go seats a GoChunk region.
func splitAt(src string) (*parser, ast.Decl) {
	p := testParser(src)
	decls := p.splitGoElements(src, p.file.Pos(0))
	// These fixtures have no leading imports, so no import-peel GoChunk is split
	// off; splitGoElements returns exactly one decl. The import-peel path has its
	// own test (TestSplitGoElementsPeelsLeadingImports).
	return p, decls[len(decls)-1]
}

func TestSplitGoElements(t *testing.T) {
	src := `var help = <a href="/help">?</a>`
	_, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	if len(we.Parts) != 3 {
		t.Fatalf("parts=%d want 3: %#v", len(we.Parts), we.Parts)
	}
	lead, ok := we.Parts[0].(ast.GoText)
	if !ok {
		t.Fatalf("part 0 not GoText, got %T", we.Parts[0])
	}
	if lead.Src != "var help = " {
		t.Fatalf("lead text=%q", lead.Src)
	}
	el, ok := we.Parts[1].(*ast.Element)
	if !ok {
		t.Fatalf("part 1 not *ast.Element, got %T", we.Parts[1])
	}
	if el.Tag != "a" {
		t.Fatalf("tag=%q", el.Tag)
	}
	trail, ok := we.Parts[2].(ast.GoText)
	if !ok {
		t.Fatalf("part 2 not GoText, got %T", we.Parts[2])
	}
	if trail.Src != "" {
		t.Fatalf("trailing text=%q, want empty", trail.Src)
	}
}

// A region whose embedded-element func is preceded by import declarations must
// split those imports into their own leading GoChunk, so both the skeleton and
// the emitted output hoist them into the file's import block (rather than
// splicing them after a declaration → "imports must appear before other
// declarations"). The remainder becomes the GoWithElements.
func TestSplitGoElementsPeelsLeadingImports(t *testing.T) {
	src := "import \"a\"\nimport b \"c\"\n\nfunc render() Node {\n\treturn <div/>\n}\n"
	p := testParser(src)
	decls := p.splitGoElements(src, p.file.Pos(0))
	if len(decls) != 2 {
		t.Fatalf("decls=%d, want 2 (import GoChunk + GoWithElements): %#v", len(decls), decls)
	}
	gc, ok := decls[0].(*ast.GoChunk)
	if !ok {
		t.Fatalf("decl 0 not *ast.GoChunk, got %T", decls[0])
	}
	// The import chunk absorbs the trailing blank line so the printer keeps the
	// blank line between the imports and the func (see printer.endsWithBlankLine).
	if gc.Src != "import \"a\"\nimport b \"c\"\n\n" {
		t.Fatalf("import chunk Src=%q", gc.Src)
	}
	we, ok := decls[1].(*ast.GoWithElements)
	if !ok {
		t.Fatalf("decl 1 not *ast.GoWithElements, got %T", decls[1])
	}
	// The GoWithElements must carry the func region verbatim, with no import in it.
	lead := we.Parts[0].(ast.GoText)
	if !strings.HasPrefix(strings.TrimSpace(lead.Src), "func render()") {
		t.Fatalf("GoWithElements lead=%q, want to start at func render", lead.Src)
	}
	// Round-trip: chunk + parts must reproduce src byte-for-byte.
	got := gc.Src
	for _, part := range we.Parts {
		switch v := part.(type) {
		case ast.GoText:
			got += v.Src
		case *ast.Element:
			got += src[p.file.Offset(v.Pos()):p.file.Offset(v.End())]
		}
	}
	if got != src {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, src)
	}
}

// A `<div/>` at operand-start with NO preceding import is unaffected: single
// GoWithElements, no spurious empty import chunk.
func TestSplitGoElementsNoLeadingImportNoPeel(t *testing.T) {
	src := "var x = <div/>"
	p := testParser(src)
	decls := p.splitGoElements(src, p.file.Pos(0))
	if len(decls) != 1 {
		t.Fatalf("decls=%d, want 1 (no peel): %#v", len(decls), decls)
	}
	if _, ok := decls[0].(*ast.GoWithElements); !ok {
		t.Fatalf("decl 0 not *ast.GoWithElements, got %T", decls[0])
	}
}

// A stray import that FOLLOWS the embedded-element func stays inside the
// GoWithElements (it is invalid Go and must remain a reported error); only the
// leading run is peeled.
func TestSplitGoElementsDoesNotPeelTrailingImport(t *testing.T) {
	src := "import \"a\"\n\nfunc render() Node { return <div/> }\n\nimport \"late\"\n"
	p := testParser(src)
	decls := p.splitGoElements(src, p.file.Pos(0))
	if len(decls) != 2 {
		t.Fatalf("decls=%d, want 2: %#v", len(decls), decls)
	}
	we := decls[1].(*ast.GoWithElements)
	var joined string
	for _, part := range we.Parts {
		if gt, ok := part.(ast.GoText); ok {
			joined += gt.Src
		}
	}
	if !strings.Contains(joined, `import "late"`) {
		t.Fatalf("trailing import should remain inside GoWithElements; parts text=%q", joined)
	}
}

func TestSplitGoElementsNoMarksFastPath(t *testing.T) {
	src := `var x = 1 + 2`
	_, part := splitAt(src)
	gc, ok := part.(*ast.GoChunk)
	if !ok {
		t.Fatalf("want *ast.GoChunk (fast path), got %T", part)
	}
	if gc.Src != src {
		t.Fatalf("Src=%q, want unchanged %q", gc.Src, src)
	}
}

func TestSplitGoElementsTwoSiblings(t *testing.T) {
	src := `f(<a/>, <b/>)`
	p, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	// ["f(", <a/>, ", ", <b/>, ")"]
	if len(we.Parts) != 5 {
		t.Fatalf("parts=%d want 5: %#v", len(we.Parts), we.Parts)
	}
	want := []struct {
		isText bool
		text   string
		tag    string
	}{
		{true, "f(", ""},
		{false, "", "a"},
		{true, ", ", ""},
		{false, "", "b"},
		{true, ")", ""},
	}
	for i, w := range want {
		if w.isText {
			gt, ok := we.Parts[i].(ast.GoText)
			if !ok {
				t.Fatalf("part %d not GoText, got %T", i, we.Parts[i])
			}
			if gt.Src != w.text {
				t.Fatalf("part %d text=%q want %q", i, gt.Src, w.text)
			}
		} else {
			el, ok := we.Parts[i].(*ast.Element)
			if !ok {
				t.Fatalf("part %d not *ast.Element, got %T", i, we.Parts[i])
			}
			if el.Tag != w.tag {
				t.Fatalf("part %d tag=%q want %q", i, el.Tag, w.tag)
			}
		}
	}

	// Reconstruct src by concatenating each part's own source span
	// (GoText.Src verbatim, an *Element's own [Pos, End) slice) and confirm
	// it reproduces src exactly, per GoWithElements's documented invariant.
	var got string
	for _, part := range we.Parts {
		switch v := part.(type) {
		case ast.GoText:
			got += v.Src
		case *ast.Element:
			start := p.file.Position(v.Pos()).Offset
			end := p.file.Position(v.End()).Offset
			got += src[start:end]
		}
	}
	if got != src {
		t.Fatalf("reconstructed src = %q, want %q", got, src)
	}
}

func TestSplitGoElementsPositions(t *testing.T) {
	src := `var help = <a href="/help">?</a>`
	p, part := splitAt(src)
	we := part.(*ast.GoWithElements)

	if got, want := p.file.Position(we.Pos()).Offset, 0; got != want {
		t.Fatalf("GoWithElements.Pos() offset = %d, want %d", got, want)
	}
	if got, want := p.file.Position(we.End()).Offset, len(src); got != want {
		t.Fatalf("GoWithElements.End() offset = %d, want %d", got, want)
	}

	lead := we.Parts[0].(ast.GoText)
	if got, want := p.file.Position(lead.Pos()).Offset, 0; got != want {
		t.Fatalf("lead GoText.Pos() offset = %d, want %d", got, want)
	}
	if got, want := p.file.Position(lead.End()).Offset, len("var help = "); got != want {
		t.Fatalf("lead GoText.End() offset = %d, want %d", got, want)
	}

	el := we.Parts[1].(*ast.Element)
	wantElStart := len("var help = ")
	if got, want := p.file.Position(el.Pos()).Offset, wantElStart; got != want {
		t.Fatalf("Element.Pos() offset = %d, want %d", got, want)
	}
	wantElEnd := len(src) // the element runs to the very end of src here
	if got, want := p.file.Position(el.End()).Offset, wantElEnd; got != want {
		t.Fatalf("Element.End() offset = %d, want %d", got, want)
	}

	trail := we.Parts[2].(ast.GoText)
	if got, want := p.file.Position(trail.Pos()).Offset, len(src); got != want {
		t.Fatalf("trailing GoText.Pos() offset = %d, want %d", got, want)
	}
}

// TestSplitGoElementsMalformedForwardProgress verifies that a mark whose
// element fails to parse (parseElement returns an error) doesn't panic or
// drop bytes: the error is recorded in p.errs, and the rest of src (starting
// at the failed element's own '<') is folded back in verbatim as a trailing
// GoText part.
func TestSplitGoElementsMalformedForwardProgress(t *testing.T) {
	src := `var x = <div`
	p, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	if len(p.errs) == 0 {
		t.Fatalf("expected a recorded parse error for the unterminated tag")
	}
	if len(we.Parts) != 2 {
		t.Fatalf("parts=%d want 2: %#v", len(we.Parts), we.Parts)
	}
	lead := we.Parts[0].(ast.GoText)
	if lead.Src != "var x = " {
		t.Fatalf("lead=%q", lead.Src)
	}
	tail := we.Parts[1].(ast.GoText)
	if tail.Src != "<div" {
		t.Fatalf("tail=%q, want the unparsed remainder verbatim", tail.Src)
	}
}

// fragmentPart finds the single *ast.Fragment among we.Parts, failing the
// test if none is found.
func fragmentPart(t *testing.T, we *ast.GoWithElements) *ast.Fragment {
	t.Helper()
	for _, part := range we.Parts {
		if frag, ok := part.(*ast.Fragment); ok {
			return frag
		}
	}
	t.Fatalf("no *ast.Fragment part found; parts=%#v", we.Parts)
	return nil
}

// TestSplitGoElementsFragmentAdmitted verifies that a non-empty fragment
// (`<>…</>`) in Go-expression position is admitted as a *ast.Fragment part —
// no diagnostic, and its children are parsed — and that reconstructing src
// from each part's own source span (GoText verbatim, *Element/*Fragment via
// their own [Pos,End) slice) still reproduces src exactly.
func TestSplitGoElementsFragmentAdmitted(t *testing.T) {
	src := `f(<>hi</>, 1)`
	p, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	if len(p.errs) != 0 {
		t.Fatalf("unexpected errors: %v", p.errs)
	}
	frag := fragmentPart(t, we)
	if len(frag.Children) != 1 {
		t.Fatalf("fragment children = %d, want 1: %#v", len(frag.Children), frag.Children)
	}

	// Reconstruct src: GoText verbatim, *Element/*Fragment via their own
	// [Pos,End) span.
	var got string
	for _, part := range we.Parts {
		switch v := part.(type) {
		case ast.GoText:
			got += v.Src
		case *ast.Element:
			start := p.file.Position(v.Pos()).Offset
			end := p.file.Position(v.End()).Offset
			got += src[start:end]
		case *ast.Fragment:
			start := p.file.Position(v.Pos()).Offset
			end := p.file.Position(v.End()).Offset
			got += src[start:end]
		}
	}
	if got != src {
		t.Fatalf("reconstructed src = %q, want %q", got, src)
	}
}

// TestSplitGoElementsFragmentInExpressionPosition verifies a non-empty
// fragment as a var initializer: one GoWithElements decl whose parts include
// an *ast.Fragment with two element children. No diagnostics.
func TestSplitGoElementsFragmentInExpressionPosition(t *testing.T) {
	src := `var list = <><a/><b/></>`
	p, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	if len(p.errs) != 0 {
		t.Fatalf("unexpected errors: %v", p.errs)
	}
	frag := fragmentPart(t, we)
	if got := len(frag.Children); got != 2 {
		t.Fatalf("fragment children = %d, want 2", got)
	}
}

// TestSplitGoElementsEmptyFragmentInExpressionPosition verifies that `<></>`
// is legal and yields a zero-child fragment (the nop). No diagnostics.
func TestSplitGoElementsEmptyFragmentInExpressionPosition(t *testing.T) {
	src := `var nop = <></>`
	p, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	if len(p.errs) != 0 {
		t.Fatalf("unexpected errors: %v", p.errs)
	}
	frag := fragmentPart(t, we)
	if len(frag.Children) != 0 {
		t.Fatalf("empty fragment has %d children, want 0", len(frag.Children))
	}
}

// TestSplitGoElementsBareAdjacentNotAdmittedAsTwoParts documents the JSX
// rule: a bare adjacent sequence of elements in expression position is NOT
// admitted as two element parts. scanGoElementMarks only flags a '<' at an
// operand-start (prefix) position; immediately after consuming an element the
// scanner sits at an operand-consumed (operator/infix) position, so the
// second `<B/>` is never flagged as a mark and is never separately parsed —
// it rides along as part of the trailing GoText, verbatim.
func TestSplitGoElementsBareAdjacentNotAdmittedAsTwoParts(t *testing.T) {
	src := `var x = <A/><B/>`
	_, part := splitAt(src)
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	var elCount int
	for _, p := range we.Parts {
		if _, ok := p.(*ast.Element); ok {
			elCount++
		}
	}
	if elCount != 1 {
		t.Fatalf("element parts = %d, want 1 (bare-adjacent sequences aren't admitted as multiple element parts): %#v", elCount, we.Parts)
	}
	trail, ok := we.Parts[len(we.Parts)-1].(ast.GoText)
	if !ok || trail.Src != "<B/>" {
		t.Fatalf("trailing part = %#v, want GoText %q (unparsed second element, verbatim)", we.Parts[len(we.Parts)-1], "<B/>")
	}
}

// TestSplitGoElementsAbsoluteBase verifies positions are correct when the Go
// region does not start at file offset 0 — i.e. base reflects a real
// mid-file position, as it would when parser/file.go hands splitGoElements
// the tail of a larger source string.
func TestSplitGoElementsAbsoluteBase(t *testing.T) {
	full := `package p

component Foo() { <div/> }

var help = <a href="/help">?</a>
`
	fset := token.NewFileSet()
	f := fset.AddFile("t.gsx", fset.Base(), len(full))
	p := newParser(f, full)

	goRegionStart := len(`package p

component Foo() { <div/> }

`)
	src := full[goRegionStart:]
	decls := p.splitGoElements(src, f.Pos(goRegionStart))
	part := decls[len(decls)-1]
	we, ok := part.(*ast.GoWithElements)
	if !ok {
		t.Fatalf("want *ast.GoWithElements, got %T", part)
	}
	el := we.Parts[1].(*ast.Element)
	gotOff := f.Position(el.Pos()).Offset
	wantOff := goRegionStart + len("var help = ")
	if gotOff != wantOff {
		t.Fatalf("Element.Pos() offset = %d, want %d", gotOff, wantOff)
	}
	if el.Tag != "a" {
		t.Fatalf("tag=%q", el.Tag)
	}
}
