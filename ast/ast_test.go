// ast/ast_test.go
package ast

import (
	"fmt"
	"go/token"
	"reflect"
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

func TestPart2NodesImplementInterfaces(t *testing.T) {
	var _ Markup = (*GoBlock)(nil)
	var _ Markup = (*IfMarkup)(nil)
	var _ Markup = (*ForMarkup)(nil)
	var _ Markup = (*SwitchMarkup)(nil)
	var _ Attr = (*CondAttr)(nil)
	var _ Attr = (*ClassAttr)(nil)
	var _ Node = (*CaseClause)(nil)
}

func TestHTMLMarkupNodes(t *testing.T) {
	var _ Markup = (*Doctype)(nil)
	var _ Markup = (*HTMLComment)(nil)

	for _, n := range []Node{&Doctype{}, &HTMLComment{}} {
		SetSpan(n, token.Pos(10), token.Pos(20))
		if n.Pos() != token.Pos(10) || n.End() != token.Pos(20) {
			t.Fatalf("%T: SetSpan not applied: pos=%d end=%d", n, n.Pos(), n.End())
		}
	}

	// Doctype and HTMLComment are leaves: Inspect visits them but not "into" them.
	tree := &Element{Tag: "html", Children: []Markup{
		&Doctype{Text: "<!DOCTYPE html>"},
		&HTMLComment{Text: " hi "},
	}}
	var kinds []string
	Inspect(tree, func(n Node) bool {
		if n != nil {
			kinds = append(kinds, fmt.Sprintf("%T", n))
		}
		return true
	})
	want := []string{"*ast.Element", "*ast.Doctype", "*ast.HTMLComment"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("Inspect order:\n got %v\nwant %v", kinds, want)
	}
}

func TestSetSpanPart2(t *testing.T) {
	nodes := []Node{
		&GoBlock{}, &IfMarkup{}, &ForMarkup{}, &SwitchMarkup{}, &CaseClause{}, &CondAttr{}, &ClassAttr{},
	}
	for _, n := range nodes {
		SetSpan(n, token.Pos(10), token.Pos(20))
		if n.Pos() != token.Pos(10) || n.End() != token.Pos(20) {
			t.Fatalf("%T: SetSpan not applied: pos=%d end=%d", n, n.Pos(), n.End())
		}
	}
}

func TestGoWithElementsShape(t *testing.T) {
	n := GoWithElements{Parts: []GoPart{
		GoText{Src: "x = "},
		&Element{Tag: "div", Void: true},
	}}
	if len(n.Parts) != 2 {
		t.Fatalf("want 2 parts, got %d", len(n.Parts))
	}
	if _, ok := n.Parts[0].(GoText); !ok {
		t.Fatalf("part 0 not GoText")
	}
	if _, ok := n.Parts[1].(*Element); !ok {
		t.Fatalf("part 1 not *Element")
	}
}

func TestInspectPart2(t *testing.T) {
	// if (then: Text) else (Interp); for (Text); switch (case: Element); cond attr; class attr
	tree := &Component{Body: []Markup{
		&IfMarkup{Cond: "x", Then: []Markup{&Text{Value: "t"}}, Else: []Markup{&Interp{Expr: "y"}}},
		&ForMarkup{Clause: "i := range xs", Body: []Markup{&Text{Value: "b"}}},
		&SwitchMarkup{Tag: "k", Cases: []*CaseClause{{List: `"a"`, Body: []Markup{&Element{Tag: "span"}}}}},
		&GoBlock{Code: "z := 1"},
	}}
	var kinds []string
	Inspect(tree, func(n Node) bool {
		if n != nil {
			kinds = append(kinds, fmt.Sprintf("%T", n))
		}
		return true
	})
	// Must visit the IfMarkup, its Then Text, its Else Interp, ForMarkup+Text,
	// SwitchMarkup+CaseClause+Element, GoBlock.
	want := []string{
		"*ast.Component",
		"*ast.IfMarkup", "*ast.Text", "*ast.Interp",
		"*ast.ForMarkup", "*ast.Text",
		"*ast.SwitchMarkup", "*ast.CaseClause", "*ast.Element",
		"*ast.GoBlock",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("Inspect order:\n got %v\nwant %v", kinds, want)
	}
}
