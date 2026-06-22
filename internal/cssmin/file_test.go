package cssmin

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func styleEl(children ...ast.Markup) *ast.Element {
	return &ast.Element{Tag: "style", Children: children}
}
func fileWith(el *ast.Element) *ast.File {
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
}
func styleChildren(f *ast.File) []ast.Markup {
	return f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
}

func TestMinifyFileHoleless(t *testing.T) {
	f := fileWith(styleEl(&ast.Text{Value: "  .a {\n  color: red;\n}  "}))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := styleChildren(f)
	if len(ch) != 1 {
		t.Fatalf("got %d children, want 1", len(ch))
	}
	if got := ch[0].(*ast.Text).Value; got != ".a{color: red}" {
		t.Fatalf("minified = %q", got)
	}
}

func TestMinifyFileHoleyPreservesInterpAndAdjacentSpace(t *testing.T) {
	in := &ast.Interp{Expr: "a"}
	in2 := &ast.Interp{Expr: "b"}
	// "margin:  " <a> " " <b> "  ;"  -> margin and one space between the two holes
	f := fileWith(styleEl(
		&ast.Text{Value: ".x{margin:  "}, in, &ast.Text{Value: " "}, in2, &ast.Text{Value: "  ;}"},
	))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := styleChildren(f)
	// The two interps survive (same pointers) with exactly one space between them.
	var interps []*ast.Interp
	var sb strings.Builder
	for _, c := range ch {
		switch v := c.(type) {
		case *ast.Interp:
			interps = append(interps, v)
			sb.WriteString("\x00")
		case *ast.Text:
			sb.WriteString(v.Value)
		}
	}
	if len(interps) != 2 || interps[0] != in || interps[1] != in2 {
		t.Fatalf("interp pointers not preserved: %#v", interps)
	}
	if got := sb.String(); got != ".x{margin: \x00 \x00}" {
		t.Fatalf("layout = %q, want %q", got, ".x{margin: \x00 \x00}")
	}
}

func TestMinifyFileExtHolelessOnly(t *testing.T) {
	ext := func(css string) (string, error) { return "EXT", nil }
	// Holeless -> ext is used.
	f := fileWith(styleEl(&ast.Text{Value: ".a{x:1}"}))
	if err := MinifyFile(f, ext); err != nil {
		t.Fatal(err)
	}
	if got := styleChildren(f)[0].(*ast.Text).Value; got != "EXT" {
		t.Fatalf("holeless ext = %q, want EXT", got)
	}
	// Holey -> ext is NOT used (built-in keeps the interp).
	in := &ast.Interp{Expr: "a"}
	f2 := fileWith(styleEl(&ast.Text{Value: ".a{x:"}, in, &ast.Text{Value: "}"}))
	if err := MinifyFile(f2, ext); err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, c := range styleChildren(f2) {
		if _, ok := c.(*ast.Interp); ok {
			saw = true
		}
		if t2, ok := c.(*ast.Text); ok && strings.Contains(t2.Value, "EXT") {
			t.Fatal("ext was applied to a holey block")
		}
	}
	if !saw {
		t.Fatal("holey block lost its interp")
	}
}

func TestMinifyFileNULBail(t *testing.T) {
	in := &ast.Interp{Expr: "a"}
	orig := []ast.Markup{&ast.Text{Value: ".a{ x:\x001 "}, in, &ast.Text{Value: " }"}}
	f := fileWith(styleEl(orig...))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := styleChildren(f)
	if len(ch) != 3 || ch[0].(*ast.Text).Value != ".a{ x:\x001 " || ch[1] != in {
		t.Fatalf("NUL bail did not return children verbatim: %#v", ch)
	}
}

func TestMinifyFileStyleInMarkupAttr(t *testing.T) {
	// <style> passed as a markup-attribute slot value must still be minified.
	deep := styleEl(&ast.Text{Value: "  .a {\n  x: 1;\n}  "})
	host := &ast.Element{Tag: "div", Attrs: []ast.Attr{&ast.MarkupAttr{Name: "header", Value: []ast.Markup{deep}}}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{host}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != ".a{x: 1}" {
		t.Fatalf("<style> in MarkupAttr slot not minified: %q", got)
	}
}

func TestMinifyFileNestedStyle(t *testing.T) {
	deepStyle := styleEl(&ast.Text{Value: "  .a {\n  x: 1;\n}  "})
	div := &ast.Element{Tag: "div", Children: []ast.Markup{deepStyle}}
	elseStyle := styleEl(&ast.Text{Value: "  .b {\n  y: 2;\n}  "})
	iff := &ast.IfMarkup{Cond: "ok", Then: nil, Else: []ast.Markup{elseStyle}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{div, iff}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deepStyle.Children[0].(*ast.Text).Value; got != ".a{x: 1}" {
		t.Fatalf("nested <style> in div not minified: %q", got)
	}
	if got := elseStyle.Children[0].(*ast.Text).Value; got != ".b{y: 2}" {
		t.Fatalf("<style> in IfMarkup.Else not minified: %q", got)
	}
}
