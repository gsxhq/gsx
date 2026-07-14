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

func TestMinifyFileScriptInForMarkup(t *testing.T) {
	deep := scriptEl("function f() {\n\treturn 1;\n}")
	loop := &ast.ForMarkup{Clause: "_, x := range xs", Body: []ast.Markup{deep}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{loop}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != "function f() {\nreturn 1;\n}" {
		t.Fatalf("<script> in ForMarkup.Body not minified: %q", got)
	}
}

func TestMinifyFileScriptInSwitchMarkup(t *testing.T) {
	deep := scriptEl("function f() {\n\treturn 1;\n}")
	sw := &ast.SwitchMarkup{Tag: "v", Cases: []*ast.CaseClause{{List: "1", Body: []ast.Markup{deep}}}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{sw}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != "function f() {\nreturn 1;\n}" {
		t.Fatalf("<script> in SwitchMarkup case not minified: %q", got)
	}
}

func TestMinifyFileScriptInFragment(t *testing.T) {
	deep := scriptEl("function f() {\n\treturn 1;\n}")
	frag := &ast.Fragment{Children: []ast.Markup{deep}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{frag}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != "function f() {\nreturn 1;\n}" {
		t.Fatalf("<script> in Fragment not minified: %q", got)
	}
}

func TestMinifyFileSkipsDataIslandScript(t *testing.T) {
	// A holeless data-block <script type="application/json"> is NOT JavaScript;
	// MinifyFile must leave its body byte-for-byte unchanged.
	body := "{\n  \"a\": 1\n}"
	el := &ast.Element{
		Tag:      "script",
		Attrs:    []ast.Attr{&ast.StaticAttr{Name: "type", Value: "application/json"}},
		Children: []ast.Markup{&ast.Text{Value: body}},
	}
	f := fileWith(el)
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := el.Children[0].(*ast.Text).Value; got != body {
		t.Fatalf("data-island <script> was modified: %q (want %q)", got, body)
	}
}

func divAttr(a ast.Attr) *ast.Element { return &ast.Element{Tag: "div", Attrs: []ast.Attr{a}} }
func attrSegs(f *ast.File) []ast.Markup {
	return f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Attrs[0].(*ast.EmbeddedAttr).Segments
}

func TestMinifyJSAttrHolelessFull(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{&ast.Text{Value: "{\n  open: false,\n  active: -1,\n}"}}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	segs := attrSegs(f)
	if len(segs) != 1 {
		t.Fatalf("want 1 seg, got %d: %#v", len(segs), segs)
	}
	got := segs[0].(*ast.Text).Value
	if containsNL(got) {
		t.Fatalf("newlines not removed: %q", got)
	}
	if !has(got, "open") || !has(got, "active") {
		t.Fatalf("object keys lost: %q", got)
	}
}

func TestMinifyJSAttrSafeKeepsContent(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{&ast.Text{Value: "{\n  open: false,\n}"}}}))
	if err := jsminFileMinify(f, nil); err != nil {
		t.Fatal(err)
	}
	if !has(attrSegs(f)[0].(*ast.Text).Value, "open") {
		t.Fatalf("lost content")
	}
}

func TestMinifyJSAttrIgnoresCSSLang(t *testing.T) {
	orig := "color: red;\n"
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "style", Lang: ast.EmbeddedCSS,
		Segments: []ast.Markup{&ast.Text{Value: orig}}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	if attrSegs(f)[0].(*ast.Text).Value != orig {
		t.Fatalf("css attr touched by JS pass")
	}
}

func TestMinifyJSAttrHoleyFull(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{
			&ast.Text{Value: "{\n  id: "},
			&ast.Interp{Expr: "id"},
			&ast.Text{Value: ",\n  k: 1,\n}"},
		}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	segs := attrSegs(f)
	hasInterp := false
	text := ""
	for _, s := range segs {
		switch x := s.(type) {
		case *ast.Interp:
			hasInterp = true
			if x.Expr != "id" {
				t.Fatalf("hole expr changed: %q", x.Expr)
			}
		case *ast.Text:
			text += x.Value
		}
	}
	if !hasInterp {
		t.Fatalf("hole lost: %#v", segs)
	}
	if has(text, "gsxHole") {
		t.Fatalf("sentinel leaked: %q", text)
	}
	if containsNL(text) {
		t.Fatalf("not minified (newlines remain): %q", text)
	}
	if !has(text, "id") || !has(text, "k") {
		t.Fatalf("keys lost: %q", text)
	}
}

func TestMinifyJSAttrHoleySafeUnchanged(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS,
		Segments: []ast.Markup{&ast.Text{Value: "{ id: "}, &ast.Interp{Expr: "id"}, &ast.Text{Value: " }"}}}))
	if err := jsminFileMinify(f, nil); err != nil {
		t.Fatal(err)
	}
	if len(attrSegs(f)) != 3 {
		t.Fatalf("holey js under safe level must be unchanged, got %#v", attrSegs(f))
	}
}

// A SINGLE-property object literal is a valid *program* (labeled block), so a
// raw-first cascade would strip its braces and break it. It must stay an object.
func TestMinifyJSAttrSinglePropObject(t *testing.T) {
	for _, in := range []string{"{ open: false }", "{ count: 0 }", "{ items: ['a', 'b'] }"} {
		f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS, DoubleQuoted: true,
			Segments: []ast.Markup{&ast.Text{Value: in}}}))
		if err := jsminFileMinify(f, fullminJS); err != nil {
			t.Fatal(err)
		}
		got := attrSegs(f)[0].(*ast.Text).Value
		// must still be an object expression: start with `(` or `{`, contain `{`, and NOT be a bare `key:val` label statement.
		if !has(got, "{") || (got[0] != '(' && got[0] != '{') {
			t.Fatalf("%q → %q: object braces stripped (became a label statement)", in, got)
		}
	}
}

// A SINGLE-property HOLEY object must also keep its braces (sentinel text starts
// with `{` → wrap-first), else the hole ends up in a stripped label statement.
func TestMinifyJSAttrSinglePropHoley(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{&ast.Text{Value: "{ id: "}, &ast.Interp{Expr: "id"}, &ast.Text{Value: " }"}}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	segs := attrSegs(f)
	text, hasHole := "", false
	for _, s := range segs {
		switch x := s.(type) {
		case *ast.Text:
			text += x.Value
		case *ast.Interp:
			hasHole = true
		}
	}
	if !hasHole || !has(text, "{") || (text[0] != '(' && text[0] != '{') {
		t.Fatalf("single-prop holey object broke: %q (hole=%v)", text, hasHole)
	}
}
