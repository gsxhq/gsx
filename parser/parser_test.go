package parser

import "testing"

func TestCursorBasics(t *testing.T) {
	p := newParser("  ab")
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
	pos := p.pos()
	if pos.Line != 1 || pos.Column != 3 {
		t.Fatalf("pos = %d:%d, want 1:3", pos.Line, pos.Column)
	}
	p.i = len(p.src)
	if !p.eof() {
		t.Fatalf("expected eof")
	}
}
