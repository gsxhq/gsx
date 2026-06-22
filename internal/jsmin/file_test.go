package jsmin

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func scriptEl(text string) *ast.Element {
	return &ast.Element{Tag: "script", Children: []ast.Markup{&ast.Text{Value: text}}}
}
func fileWith(el *ast.Element) *ast.File {
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
}

func TestMinifyFileScript(t *testing.T) {
	f := fileWith(scriptEl("function f() {\n\treturn 1;\n}"))
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if len(ch) != 1 || ch[0].(*ast.Text).Value != "function f() {\nreturn 1;\n}" {
		t.Fatalf("minified = %#v", ch)
	}
}

func TestMinifyFileExt(t *testing.T) {
	ext := func(js string) (string, error) { return "EXT", nil }
	f := fileWith(scriptEl("var x=1"))
	if err := MinifyFile(f, ext); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if ch[0].(*ast.Text).Value != "EXT" {
		t.Fatalf("ext not applied: %#v", ch)
	}
}

// TestMinifyFileSkipsHoleyScript asserts a <script> carrying an @{ } hole (an
// *ast.Interp child) is left UNCHANGED — minifying Text runs around a hole is
// unsafe (ASI / hole-boundary whitespace). All-Text scripts are still minified.
func TestMinifyFileSkipsHoleyScript(t *testing.T) {
	text1 := &ast.Text{Value: "const data = "}
	interp := &ast.Interp{Expr: "cfg"}
	text2 := &ast.Text{Value: ";\n\treturn data;"}
	el := &ast.Element{Tag: "script", Children: []ast.Markup{text1, interp, text2}}
	f := fileWith(el)
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if len(ch) != 3 || ch[0] != ast.Markup(text1) || ch[1] != ast.Markup(interp) || ch[2] != ast.Markup(text2) {
		t.Fatalf("holey <script> must be left unchanged, got %#v", ch)
	}
}

func TestMinifyFileLeavesStyleAlone(t *testing.T) {
	f := fileWith(&ast.Element{Tag: "style", Children: []ast.Markup{&ast.Text{Value: "  .a { x: 1 }  "}}})
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if ch[0].(*ast.Text).Value != "  .a { x: 1 }  " {
		t.Fatalf("jsmin must not touch <style>: %q", ch[0].(*ast.Text).Value)
	}
}
