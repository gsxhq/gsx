// ast/ast_test.go
package ast

import (
	"go/token"
	"testing"
)

func TestSpanImplementsNode(t *testing.T) {
	s := Span{Start: token.Pos(1), Finish: token.Pos(5)}
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

	f := File{
		Span:    Span{Start: 1, Finish: 100},
		Package: "views",
		Decls: []Decl{
			&Component{
				Span: Span{Start: 10, Finish: 90},
				Name: "Card",
				Body: []Markup{
					&Element{
						Span:     Span{Start: 20, Finish: 80},
						Tag:      "div",
						Children: []Markup{&Text{Span: Span{Start: 25, Finish: 27}, Value: "hi"}},
					},
				},
			},
		},
	}
	comp := f.Decls[0].(*Component)
	if comp.Name != "Card" {
		t.Fatalf("unexpected name: %s", comp.Name)
	}
	if comp.Pos() != 10 {
		t.Fatalf("Pos() = %v, want 10", comp.Pos())
	}
	if comp.End() != 90 {
		t.Fatalf("End() = %v, want 90", comp.End())
	}
}
