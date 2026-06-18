package ast

import (
	"go/token"
	"testing"
)

func TestNodesImplementInterfaces(t *testing.T) {
	var _ Decl = (*GoChunk)(nil)
	var _ Decl = (*Component)(nil)
	var _ Node = (*Element)(nil)
	var _ Node = (*Fragment)(nil)
	var _ Node = (*Text)(nil)
	var _ Node = (*Interp)(nil)
	var _ Attr = (*StaticAttr)(nil)
	var _ Attr = (*ExprAttr)(nil)
	var _ Attr = (*BoolAttr)(nil)
	var _ Attr = (*SpreadAttr)(nil)
	var _ Attr = (*MarkupAttr)(nil)

	f := File{Package: "views", Decls: []Decl{
		&Component{Name: "Card", Body: []Node{
			&Element{Tag: "div", Children: []Node{&Text{Value: "hi"}}},
		}, Pos: token.Position{Line: 1}},
	}}
	if f.Decls[0].(*Component).Name != "Card" {
		t.Fatalf("unexpected name")
	}
}
