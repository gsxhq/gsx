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
