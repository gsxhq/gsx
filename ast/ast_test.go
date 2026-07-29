// ast/ast_test.go
package ast

import (
	"fmt"
	"go/token"
	"reflect"
	"slices"
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
	var _ Attr = (*ComposedAttr)(nil)
	var _ Node = (*CaseClause)(nil)
}

func TestHTMLMarkupNodes(t *testing.T) {
	var _ Markup = (*Doctype)(nil)
	var _ Markup = (*HTMLComment)(nil)
	// Marker/MarkerRegion are the processing-instruction siblings of
	// Doctype/HTMLComment; unlike them, Marker/MarkerRegion are not leaves
	// (see TestInspectWalksMarkerRegion), so only the interface assertion is
	// shared here — the SetSpan and Inspect-leaf checks below stay Doctype/
	// HTMLComment-only.
	var _ Markup = (*Marker)(nil)
	var _ Markup = (*MarkerRegion)(nil)

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

// TestInspectWalksMarkerRegion proves MarkerRegion is not a leaf: Inspect must
// walk its Name attribute and Children, unlike the true-leaf Doctype/HTMLComment
// processing instructions above.
func TestInspectWalksMarkerRegion(t *testing.T) {
	region := &MarkerRegion{
		Name:     &StaticAttr{Name: "name", Value: "feed"},
		Children: []Markup{&Text{Value: "hi"}},
	}
	var seen []string
	Inspect(region, func(n Node) bool {
		switch n.(type) {
		case *MarkerRegion:
			seen = append(seen, "region")
		case *StaticAttr:
			seen = append(seen, "name")
		case *Text:
			seen = append(seen, "text")
		}
		return true
	})
	want := []string{"region", "name", "text"}
	if !slices.Equal(seen, want) {
		t.Fatalf("Inspect visited %v, want %v", seen, want)
	}
}

// TestCommentSetSpan pins a fix to SetSpan: *Comment (the content-comment
// node — braced `{// }`/`{/* */}` and, since the Bare field's introduction,
// bare line-start `//`) was missing from SetSpan's type switch, so every
// caller's ast.SetSpan(comment, start, end) was a silent no-op and
// comment.Pos()/End() always read token.NoPos. Nothing surfaced it until a
// diagnostic needed to position itself AT a content comment (the
// bare-comment-touches-text check in internal/codegen), because comment
// ordering/rendering never depended on the span. Modeled on
// TestHTMLMarkupNodes' Doctype/HTMLComment coverage above.
func TestCommentSetSpan(t *testing.T) {
	c := &Comment{Text: "hi", Bare: true}
	SetSpan(c, token.Pos(10), token.Pos(20))
	if c.Pos() != token.Pos(10) || c.End() != token.Pos(20) {
		t.Fatalf("Comment: SetSpan not applied: pos=%d end=%d", c.Pos(), c.End())
	}
}

func TestSetSpanPart2(t *testing.T) {
	nodes := []Node{
		&GoBlock{}, &IfMarkup{}, &ForMarkup{}, &SwitchMarkup{}, &CaseClause{}, &CondAttr{}, &ComposedAttr{},
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

// TestFragmentIsGoPart proves *Fragment is usable as a GoWithElements part
// (compile-time proof via the slice literal) and that Inspect recurses into
// its children through GoWithElements.
func TestFragmentIsGoPart(t *testing.T) {
	frag := &Fragment{Children: []Markup{&Text{Value: "hi"}}}
	we := &GoWithElements{Parts: []GoPart{
		GoText{Src: "var x = "},
		frag,
	}}

	var sawText bool
	Inspect(we, func(n Node) bool {
		if txt, ok := n.(*Text); ok && txt.Value == "hi" {
			sawText = true
		}
		return true
	})
	if !sawText {
		t.Fatal("Inspect did not reach the fragment's child text through GoWithElements")
	}
}

// TestInspectGoWithElements is the Task-2 review follow-up: Inspect must
// descend into a GoWithElements's Parts (GoText leaves, *Element recurses),
// or a tree-walker (tooling/codegen) never sees embedded elements at all.
func TestInspectGoWithElements(t *testing.T) {
	tree := &File{Decls: []Decl{
		&GoWithElements{Parts: []GoPart{
			GoText{Src: "var help = "},
			&Element{Tag: "a", Void: true},
			GoText{Src: ""},
		}},
	}}
	var kinds []string
	Inspect(tree, func(n Node) bool {
		if n != nil {
			kinds = append(kinds, fmt.Sprintf("%T", n))
		}
		return true
	})
	want := []string{
		"*ast.File",
		"*ast.GoWithElements",
		"ast.GoText",
		"*ast.Element",
		"ast.GoText",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("Inspect order:\n got %v\nwant %v", kinds, want)
	}
}

func TestClassPartExprEnd(t *testing.T) {
	var nilPart *ComposedPart
	if got := nilPart.ExprEnd(); got != token.NoPos {
		t.Fatalf("nil ExprEnd() = %d, want NoPos", got)
	}

	tests := []struct {
		name string
		part *ComposedPart
		want token.Pos
	}{
		{
			name: "invalid position",
			part: &ComposedPart{Expr: "button.Role()"},
			want: token.NoPos,
		},
		{
			name: "ASCII",
			part: &ComposedPart{Expr: "button.Role()", ExprPos: token.Pos(11)},
			want: token.Pos(11 + len("button.Role()")),
		},
		{
			name: "UTF-8 uses byte length",
			part: &ComposedPart{Expr: `"é"`, ExprPos: token.Pos(23)},
			want: token.Pos(23 + len(`"é"`)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.part.ExprEnd(); got != tt.want {
				t.Fatalf("ExprEnd() = %d, want %d", got, tt.want)
			}
		})
	}
}
