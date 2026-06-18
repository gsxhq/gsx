// ast/ast_test.go
package ast

import (
	"go/token"
	"testing"
)

func TestSpanImplementsNode(t *testing.T) {
	var s span
	s.start = token.Pos(1)
	s.end = token.Pos(5)
	if s.Pos() != 1 {
		t.Fatalf("Pos() = %v, want 1", s.Pos())
	}
	if s.End() != 5 {
		t.Fatalf("End() = %v, want 5", s.End())
	}
}

func TestNodesImplementInterfaces(t *testing.T) {
	// Node (universal)
	var _ Node = (*GoChunk)(nil)
	var _ Node = (*Component)(nil)
	var _ Node = (*Element)(nil)
	var _ Node = (*Fragment)(nil)
	var _ Node = (*Text)(nil)
	var _ Node = (*Interp)(nil)
	var _ Node = (*StaticAttr)(nil)
	var _ Node = (*ExprAttr)(nil)
	var _ Node = (*BoolAttr)(nil)
	var _ Node = (*SpreadAttr)(nil)
	var _ Node = (*MarkupAttr)(nil)

	// Decl
	var _ Decl = (*GoChunk)(nil)
	var _ Decl = (*Component)(nil)

	// Markup (replaces old Node)
	var _ Markup = (*Element)(nil)
	var _ Markup = (*Fragment)(nil)
	var _ Markup = (*Text)(nil)
	var _ Markup = (*Interp)(nil)

	// Attr
	var _ Attr = (*StaticAttr)(nil)
	var _ Attr = (*ExprAttr)(nil)
	var _ Attr = (*BoolAttr)(nil)
	var _ Attr = (*SpreadAttr)(nil)
	var _ Attr = (*MarkupAttr)(nil)

	text := &Text{Value: "hi"}
	SetSpan(text, 25, 27)

	el := &Element{Tag: "div", Children: []Markup{text}}
	SetSpan(el, 20, 80)

	comp := &Component{Name: "Card", Body: []Markup{el}}
	SetSpan(comp, 10, 90)

	f := File{
		Package: "views",
		Decls:   []Decl{comp},
	}
	SetSpan(&f, 1, 100)

	c := f.Decls[0].(*Component)
	if c.Name != "Card" {
		t.Fatalf("unexpected name: %s", c.Name)
	}
	if c.Pos() != 10 {
		t.Fatalf("Pos() = %v, want 10", c.Pos())
	}
	if c.End() != 90 {
		t.Fatalf("End() = %v, want 90", c.End())
	}
}
