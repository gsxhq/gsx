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

// TestElementCloseNamePos verifies the parser records the closing-tag name
// position on a non-void element (used by LSP go-to-definition from a closing
// tag), and leaves it NoPos for a self-closing/void element.
func TestElementCloseNamePos(t *testing.T) {
	src := "package pos\n\ncomponent Page() {\n\t<section><br/></section>\n}\n"
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "t.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var page *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok && c.Name == "Page" {
			page = c
		}
	}
	if page == nil || len(page.Body) == 0 {
		t.Fatal("Page/body not found")
	}
	section, ok := page.Body[0].(*ast.Element)
	if !ok || section.Tag != "section" {
		t.Fatalf("expected <section>, got %T", page.Body[0])
	}

	// Non-void <section>…</section>: CloseNamePos must point at the "section" in
	// the closing tag.
	if !section.CloseNamePos.IsValid() {
		t.Fatal("section.CloseNamePos is NoPos, want the </section> name position")
	}
	off := fset.Position(section.CloseNamePos).Offset
	if got := src[off : off+len("section")]; got != "section" {
		t.Errorf("CloseNamePos points at %q, want \"section\"", got)
	}
	// It must be the CLOSING name (after "</"), not the opening one.
	if off <= int(section.Pos()) {
		t.Errorf("CloseNamePos offset %d not after element start", off)
	}

	// Void <br/>: no closing tag → NoPos.
	var br *ast.Element
	for _, ch := range section.Children {
		if e, ok := ch.(*ast.Element); ok && e.Tag == "br" {
			br = e
		}
	}
	if br == nil {
		t.Fatal("<br/> not found")
	}
	if br.CloseNamePos.IsValid() {
		t.Errorf("void <br/> CloseNamePos = %v, want NoPos", br.CloseNamePos)
	}
}

// TestControlFlowClausePositions verifies that ForMarkup.ClausePos, IfMarkup.CondPos,
// and GoBlock.CodePos each point at the first character of the trimmed clause/cond/code.
func TestControlFlowClausePositions(t *testing.T) {
	src := "package x\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n\t{ if props.Featured { <b>f</b> } }\n\t{{ total := len(props.Posts) }}\n}\n"
	fset := token.NewFileSet()
	f, errs := ParseFileWithClassifier(fset, "p.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var forM *ast.ForMarkup
	var ifM *ast.IfMarkup
	var gb *ast.GoBlock
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ForMarkup:
			forM = v
		case *ast.IfMarkup:
			ifM = v
		case *ast.GoBlock:
			gb = v
		}
		return true
	})
	// Each position must point at the first char of the (trimmed) clause/cond/code.
	check := func(name string, pos token.Pos, text string) {
		if !pos.IsValid() {
			t.Fatalf("%s position invalid", name)
		}
		off := fset.Position(pos).Offset
		if got := src[off : off+len(text)]; got != text {
			t.Errorf("%s: src at pos = %q, want %q", name, got, text)
		}
	}
	check("ForMarkup.ClausePos", forM.ClausePos, "_, post := range props.Posts")
	check("IfMarkup.CondPos", ifM.CondPos, "props.Featured")
	check("GoBlock.CodePos", gb.CodePos, "total := len(props.Posts)")
}

// TestElementCloseNamePosEdgeCases covers close-tag name positions the LSP relies
// on for go-to-definition from a closing tag: whitespace before '>' (the parser
// skipSpaces, so </Card > is valid) must still point at the NAME, and nested
// same-name elements must each get their own correct close position.
func TestElementCloseNamePosEdgeCases(t *testing.T) {
	firstElement := func(src string) (*ast.File, *token.FileSet) {
		fset := token.NewFileSet()
		f, err := ParseFile(fset, "t.gsx", src, 0)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		return f, fset
	}
	nameAt := func(src string, fset *token.FileSet, pos token.Pos, n int) string {
		off := fset.Position(pos).Offset
		return src[off : off+n]
	}

	// Whitespace before '>' in the close tag: </section > — name pos still on "section".
	t.Run("whitespace_close", func(t *testing.T) {
		src := "package x\n\ncomponent P() {\n\t<section>hi</section >\n}\n"
		f, fset := firstElement(src)
		var sec *ast.Element
		ast.Inspect(f, func(n ast.Node) bool {
			if e, ok := n.(*ast.Element); ok && e.Tag == "section" {
				sec = e
			}
			return true
		})
		if sec == nil || !sec.CloseNamePos.IsValid() {
			t.Fatal("section/CloseNamePos missing")
		}
		if got := nameAt(src, fset, sec.CloseNamePos, len("section")); got != "section" {
			t.Errorf("whitespace close: CloseNamePos at %q, want \"section\"", got)
		}
	})

	// Nested same-name <div><div>x</div></div>: each div gets its OWN close pos,
	// and the outer close is after the inner close.
	t.Run("nested_same_name", func(t *testing.T) {
		src := "package x\n\ncomponent P() {\n\t<div><div>x</div></div>\n}\n"
		f, fset := firstElement(src)
		var divs []*ast.Element
		ast.Inspect(f, func(n ast.Node) bool {
			if e, ok := n.(*ast.Element); ok && e.Tag == "div" {
				divs = append(divs, e)
			}
			return true
		})
		if len(divs) != 2 {
			t.Fatalf("want 2 divs, got %d", len(divs))
		}
		for _, d := range divs {
			if !d.CloseNamePos.IsValid() {
				t.Fatal("div CloseNamePos missing")
			}
			if got := nameAt(src, fset, d.CloseNamePos, len("div")); got != "div" {
				t.Errorf("nested close: CloseNamePos at %q, want \"div\"", got)
			}
		}
		// divs[0] is the outer (visited first); its close tag is the LAST </div>,
		// so its CloseNamePos offset must exceed the inner div's.
		outer, inner := fset.Position(divs[0].CloseNamePos).Offset, fset.Position(divs[1].CloseNamePos).Offset
		if outer <= inner {
			t.Errorf("outer close offset %d should be after inner %d", outer, inner)
		}
	})
}
