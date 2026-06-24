package lsp

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestByteOffsetForPosition(t *testing.T) {
	text := "line one\nhéllo world\n" // line 1 has a 2-byte 'é'
	// UTF-16: line 1 (0-based), char 6 → after "héllo " → byte offset of 'w'.
	off := byteOffsetForPosition(text, 1, 6, encUTF16)
	if text[off] != 'w' {
		t.Fatalf("utf16: byte %q at off %d, want 'w'", text[off], off)
	}
	// UTF-8: char counts bytes → char 7 lands on 'w' too ("héllo " is 7 bytes).
	off8 := byteOffsetForPosition(text, 1, 7, encUTF8)
	if text[off8] != 'w' {
		t.Fatalf("utf8: byte %q, want 'w'", text[off8])
	}
}

func TestInnermostIdent(t *testing.T) {
	// Parse a standalone expression; its node positions start at 1 (token.Pos).
	expr, err := parser.ParseExpr("user.Name")
	if err != nil {
		t.Fatal(err)
	}
	base := expr.Pos() // position of 'u'
	// offset 0 → "user"
	if id := innermostIdent(expr, base+token.Pos(0)); id == nil || id.Name != "user" {
		t.Fatalf("at 0 got %v, want user", id)
	}
	// offset 5 → 'N' of "Name" (u(0)s(1)e(2)r(3).(4)N(5))
	if id := innermostIdent(expr, base+token.Pos(5)); id == nil || id.Name != "Name" {
		t.Fatalf("at 5 got %v, want Name", id)
	}
	// offset 4 → the '.', no ident
	if id := innermostIdent(expr, base+token.Pos(4)); id != nil {
		t.Fatalf("at 4 got %v, want nil (on the dot)", id)
	}
}
