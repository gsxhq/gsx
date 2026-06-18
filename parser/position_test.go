// parser/position_test.go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// TestPositionCorrectness verifies that a nested element inside a component body
// (which uses a newSub parser with a non-zero base offset) reports the correct
// absolute line and column via the FileSet. This is the regression guard for
// base-offset threading through newSub.
//
// Source layout (1-indexed):
//
//	line 1: package pos
//	line 2: (blank)
//	line 3: component Card(title string) {
//	line 4: 	<section>
//	line 5: 		<h2>{title}</h2>    ← <h2> starts at column 3 (after \t\t)
//	line 6: 	</section>
//	line 7: }
func TestPositionCorrectness(t *testing.T) {
	src := "package pos\n\ncomponent Card(title string) {\n\t<section>\n\t\t<h2>{title}</h2>\n\t</section>\n}\n"

	fset := token.NewFileSet()
	f, err := ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Navigate to the <h2> element: File → Component "Card" → Body[0] (<section>) → Children[?] (<h2>)
	var card *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok && c.Name == "Card" {
			card = c
			break
		}
	}
	if card == nil {
		t.Fatal("Card component not found")
	}
	if len(card.Body) == 0 {
		t.Fatal("Card body is empty")
	}
	section, ok := card.Body[0].(*ast.Element)
	if !ok || section.Tag != "section" {
		t.Fatalf("expected <section>, got %T %v", card.Body[0], card.Body[0])
	}

	// Find the <h2> child (skip any text nodes with whitespace)
	var h2 *ast.Element
	for _, ch := range section.Children {
		if el, ok := ch.(*ast.Element); ok && el.Tag == "h2" {
			h2 = el
			break
		}
	}
	if h2 == nil {
		t.Fatal("<h2> not found in section children")
	}

	pos := fset.Position(h2.Pos())
	// <h2> is on line 5, column 3 (two tabs = columns 1,2 consumed; h2 starts at col 3)
	if pos.Line != 5 {
		t.Errorf("<h2> Pos().Line = %d, want 5", pos.Line)
	}
	if pos.Column != 3 {
		t.Errorf("<h2> Pos().Column = %d, want 3 (after \\t\\t)", pos.Column)
	}
}

// TestEmptyComponentBody is a regression guard ensuring that a component with an
// empty body parses without error and has a sane span (End >= Pos, no panic).
func TestEmptyComponentBody(t *testing.T) {
	src := "package pos\n\ncomponent X() {}\n"

	fset := token.NewFileSet()
	f, err := ParseFile(fset, "empty.gsx", src, 0)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}

	var x *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok && c.Name == "X" {
			x = c
			break
		}
	}
	if x == nil {
		t.Fatal("component X not found")
	}
	if len(x.Body) != 0 {
		t.Errorf("expected empty body, got %d nodes", len(x.Body))
	}
	if x.End() < x.Pos() {
		t.Errorf("End() %v < Pos() %v, span is inverted", x.End(), x.Pos())
	}
	// Pos() and End() must both be valid (non-zero)
	if !x.Pos().IsValid() {
		t.Error("Pos() is not valid")
	}
	if !x.End().IsValid() {
		t.Error("End() is not valid")
	}
}

// TestTrailingBoundaryPosition verifies that the last attribute of an element
// (which sits at the trailing boundary of the element's tag) reports the
// correct absolute line and column via the FileSet.
//
// Source layout (1-indexed):
//
//	line 1: package pos
//	line 2: (blank)
//	line 3: component Z() {
//	line 4: <img src="x"/>
//	line 5: }
//
// The StaticAttr `src="x"` starts at column 6 on line 4 (after `<img `).
// Its End() points to the `/` following the closing `"`, i.e. column 13 on line 4.
func TestTrailingBoundaryPosition(t *testing.T) {
	src := "package pos\n\ncomponent Z() {\n<img src=\"x\"/>\n}\n"

	fset := token.NewFileSet()
	f, err := ParseFile(fset, "trailing.gsx", src, 0)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}

	var z *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok && c.Name == "Z" {
			z = c
			break
		}
	}
	if z == nil {
		t.Fatal("component Z not found")
	}
	if len(z.Body) == 0 {
		t.Fatal("component Z has empty body")
	}
	img, ok := z.Body[0].(*ast.Element)
	if !ok || img.Tag != "img" {
		t.Fatalf("expected <img>, got %T", z.Body[0])
	}
	if len(img.Attrs) == 0 {
		t.Fatal("<img> has no attributes")
	}
	srcAttr, ok := img.Attrs[0].(*ast.StaticAttr)
	if !ok {
		t.Fatalf("expected StaticAttr, got %T", img.Attrs[0])
	}
	if srcAttr.Name != "src" {
		t.Fatalf("expected attr name %q, got %q", "src", srcAttr.Name)
	}

	// Pos(): src attr starts at line 4, column 6 (after "<img ")
	attrPos := fset.Position(srcAttr.Pos())
	if attrPos.Line != 4 {
		t.Errorf("src attr Pos().Line = %d, want 4", attrPos.Line)
	}
	if attrPos.Column != 6 {
		t.Errorf("src attr Pos().Column = %d, want 6", attrPos.Column)
	}

	// End(): points to the character after the closing `"` of src="x".
	// Line 4 layout: <img src="x"/>
	//   col 1: <, col 6: s (start of src), col 12: closing ", col 13: /
	// After advancing past the closing `"`, End() resolves to col 13 on line 4.
	attrEnd := fset.Position(srcAttr.End())
	if attrEnd.Line != 4 {
		t.Errorf("src attr End().Line = %d, want 4", attrEnd.Line)
	}
	if attrEnd.Column != 13 {
		t.Errorf("src attr End().Column = %d, want 13", attrEnd.Column)
	}
}
