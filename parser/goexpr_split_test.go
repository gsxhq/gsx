package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// splitAt seats a fresh parser over src (via the package's testParser helper)
// and calls splitGoElements with base = the position of src[0] in that
// parser's file — mirroring how parser/file.go seats a GoChunk region.
func splitAt(src string) (*parser, ast.Decl) {
	p := testParser(src)
	return p, p.splitGoElements(src, p.file.Pos(0))
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
	part := p.splitGoElements(src, f.Pos(goRegionStart))
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
