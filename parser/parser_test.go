package parser

import (
	"go/token"
	"testing"
)

// testParser creates a parser over src backed by a fresh FileSet — for use in unit tests.
func testParser(src string) *parser {
	fset := token.NewFileSet()
	f := fset.AddFile("t.gsx", fset.Base(), len(src))
	return newParser(f, src)
}

func TestCursorBasics(t *testing.T) {
	fset := token.NewFileSet()
	src := "  ab"
	f := fset.AddFile("t.gsx", fset.Base(), len(src))
	p := newParser(f, src)
	p.skipSpace()
	if p.peek() != 'a' {
		t.Fatalf("peek = %q, want 'a'", p.peek())
	}
	if !p.at("ab") {
		t.Fatalf("expected at('ab')")
	}
	if p.at("xy") {
		t.Fatalf("did not expect at('xy')")
	}
	resolvedPos := f.Position(p.pos())
	if resolvedPos.Line != 1 || resolvedPos.Column != 3 {
		t.Fatalf("pos = %d:%d, want 1:3", resolvedPos.Line, resolvedPos.Column)
	}
	p.i = len(p.src)
	if !p.eof() {
		t.Fatalf("expected eof")
	}
}
