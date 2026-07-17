package jsmin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func goBlockLit(lit *ast.EmbeddedInterp) *ast.File {
	gb := &ast.GoBlock{Embedded: []ast.GoPart{ast.GoText{Src: "a := "}, lit, ast.GoText{Src: ""}}}
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{gb}}}}
}

func litText(lit *ast.EmbeddedInterp) string {
	var b strings.Builder
	for _, s := range lit.Segments {
		if t, ok := s.(*ast.Text); ok {
			b.WriteString(t.Value)
		} else if i, ok := s.(*ast.Interp); ok {
			b.WriteString("@{" + i.Expr + "}")
		}
	}
	return b.String()
}

// A holeless js` literal in a {{ }} block (carried in GoBlock.Embedded) is
// minified by the same walk as an attribute literal — the safe pass reduces
// whitespace. This is the walk the corpus exercises end-to-end; here in isolation.
func TestMinifyFileGoBlockLiteral(t *testing.T) {
	lit := &ast.EmbeddedInterp{Lang: ast.EmbeddedJS, Segments: []ast.Markup{
		&ast.Text{Value: "const x =   1 ;\nfoo(  ) ;"},
	}}
	f := goBlockLit(lit)
	if err := MinifyFile(f, Minifiers{}); err != nil {
		t.Fatal(err)
	}
	if got := litText(lit); strings.Contains(got, "  ") {
		t.Fatalf("go-block js literal not minified: %q", got)
	}
}

// The holey path is the real MinifyFull behavior for a Go-block js` literal
// (tdewolff cannot process @{ } holes): each hole is sentinel-substituted, the
// full ext minifies, then the sentinels split back to the ORIGINAL *ast.Interp
// pointers. Here a fake full ext stands in for tdewolff.
func TestMinifyFileGoBlockHoleyRoundTrip(t *testing.T) {
	ext := func(js string) (string, error) { return strings.ReplaceAll(js, " ", ""), nil }
	interp := &ast.Interp{Expr: "gsx.RawJS(x)"}
	lit := &ast.EmbeddedInterp{Lang: ast.EmbeddedJS, Segments: []ast.Markup{
		&ast.Text{Value: "const v = 1 ; "},
		interp,
		&ast.Text{Value: " = v ;"},
	}}
	f := goBlockLit(lit)
	if err := MinifyFile(f, Minifiers{JS: ext}); err != nil {
		t.Fatal(err)
	}
	sawInterp := false
	var joined strings.Builder
	for _, s := range lit.Segments {
		switch v := s.(type) {
		case *ast.Interp:
			if v == interp {
				sawInterp = true
			}
		case *ast.Text:
			joined.WriteString(v.Value)
		}
	}
	if !sawInterp {
		t.Fatalf("hole (*ast.Interp) lost in round-trip: %#v", lit.Segments)
	}
	if strings.Contains(joined.String(), " ") {
		t.Fatalf("text not minified around the hole: %q", joined.String())
	}
}

// A js` literal embedded in a { expr } hole (Interp.Embedded) is reached too.
func TestMinifyFileInterpEmbeddedLiteral(t *testing.T) {
	lit := &ast.EmbeddedInterp{Lang: ast.EmbeddedJS, Segments: []ast.Markup{
		&ast.Text{Value: "run(   1   )"},
	}}
	in := &ast.Interp{Expr: "wrap(...)", Embedded: []ast.GoPart{ast.GoText{Src: "wrap("}, lit, ast.GoText{Src: ")"}}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{in}}}}
	if err := MinifyFile(f, Minifiers{}); err != nil {
		t.Fatal(err)
	}
	if got := litText(lit); strings.Contains(got, "   ") {
		t.Fatalf("interp-embedded js literal not minified: %q", got)
	}
}

func scriptEl(text string) *ast.Element {
	return &ast.Element{Tag: "script", Children: []ast.Markup{&ast.Text{Value: text}}}
}
func fileWith(el *ast.Element) *ast.File {
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
}

func TestMinifyFileScript(t *testing.T) {
	f := fileWith(scriptEl("function f() {\n\treturn 1;\n}"))
	if err := MinifyFile(f, Minifiers{}); err != nil {
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
	if err := MinifyFile(f, Minifiers{JS: ext}); err != nil {
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
	if err := MinifyFile(f, Minifiers{}); err != nil {
		t.Fatal(err)
	}
	ch := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Children
	if len(ch) != 3 || ch[0] != ast.Markup(text1) || ch[1] != ast.Markup(interp) || ch[2] != ast.Markup(text2) {
		t.Fatalf("holey <script> must be left unchanged, got %#v", ch)
	}
}

func TestMinifyFileLeavesStyleAlone(t *testing.T) {
	f := fileWith(&ast.Element{Tag: "style", Children: []ast.Markup{&ast.Text{Value: "  .a { x: 1 }  "}}})
	if err := MinifyFile(f, Minifiers{}); err != nil {
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
	if err := MinifyFile(f, Minifiers{}); err != nil {
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
	if err := MinifyFile(f, Minifiers{}); err != nil {
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
	if err := MinifyFile(f, Minifiers{}); err != nil {
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
	if err := MinifyFile(f, Minifiers{}); err != nil {
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

// TestMinifyJSAttrJSONShapedValueStaysValidJSON is a FAILING reproduction of the
// hx-vals minify bug. A quoted-key object literal — valid JSON, as carried by htmx's
// hx-vals/hx-headers/hx-vars, which htmx parses with JSON.parse — must survive
// minification as valid JSON. Today the full JS pass unquotes the key
// (`"exclude"`→`exclude`) and wraps the object in `(…)` to minify it as an expression,
// yielding valid JavaScript but INVALID JSON (`({exclude:"SELF-1"})`); htmx's
// JSON.parse then rejects it and silently drops the params. The `js` literal prefix
// cannot distinguish an Alpine JS expression (x-data) from a JSON payload (hx-vals) —
// both are `{`-leading js` values. This test asserts the intended behavior and fails
// until the JSON-aware minify fix lands.
func TestMinifyJSAttrJSONShapedValueStaysValidJSON(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "hx-vals", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{&ast.Text{Value: `{ "exclude": "SELF-1" }`}}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	got := attrSegs(f)[0].(*ast.Text).Value
	if !json.Valid([]byte(got)) {
		t.Fatalf("minified JSON-shaped attribute value is not valid JSON: %s", got)
	}
	if got != `{"exclude":"SELF-1"}` {
		t.Fatalf("want compact JSON {\"exclude\":\"SELF-1\"}, got %q", got)
	}
}

// TestMinifyJSAttrClassification pins the routing split: a holeless `{`/`[`-
// leading value that json.Valid accepts goes through the JSON minifier
// (compact, quoted keys); everything else — including near-JSON shapes that
// are NOT valid JSON (unquoted keys, single quotes, trailing commas) — keeps
// going through the UNCHANGED tdewolff JS-expression cascade.
func TestMinifyJSAttrClassification(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"json_object", `{ "a": 1, "b": "x" }`, `{"a":1,"b":"x"}`},
		{"json_array", `[ 1, 2, 3 ]`, `[1,2,3]`},
		{"json_nested", `{ "a": { "b": [1, 2] } }`, `{"a":{"b":[1,2]}}`},
		{"js_unquoted_key_stays_js", `{ open: false }`, `({open:!1})`},
		{"js_single_quoted_key_stays_js", `{ 'a': 1 }`, `({a:1})`},
		{"js_trailing_comma_stays_js", `{ "a": 1, }`, `({a:1})`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "hx-vals", Lang: ast.EmbeddedJS,
				DoubleQuoted: true, Segments: []ast.Markup{&ast.Text{Value: c.in}}}))
			if err := jsminFileMinify(f, fullminJS); err != nil {
				t.Fatal(err)
			}
			if got := attrSegs(f)[0].(*ast.Text).Value; got != c.want {
				t.Fatalf("in=%q got=%q want=%q", c.in, got, c.want)
			}
		})
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

// TestMinifyJSAttrHoleyJSONStaysValid is a holey counterpart to
// TestMinifyJSAttrJSONShapedValueStaysValidJSON: a JSON-shaped hx-vals value
// with a quoted key and an @{ } hole in value position must minify to compact
// JSON with the hole preserved (`{"exclude":@{selfID}}`), not the identifier-
// sentinel JS cascade's paren-wrapped, unquoted-key shape.
func TestMinifyJSAttrHoleyJSONStaysValid(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "hx-vals", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{
			&ast.Text{Value: `{ "exclude": `},
			&ast.Interp{Expr: "selfID"},
			&ast.Text{Value: ` }`},
		}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	segs := attrSegs(f)
	var text string
	sawHole := false
	for _, s := range segs {
		switch x := s.(type) {
		case *ast.Interp:
			sawHole = true
			if x.Expr != "selfID" {
				t.Fatalf("hole expr changed: %q", x.Expr)
			}
		case *ast.Text:
			text += x.Value
		}
	}
	if !sawHole {
		t.Fatalf("hole lost: %#v", segs)
	}
	// Structural checks: quoted key kept, no paren-wrap, whitespace gone.
	if strings.Contains(text, "(") || strings.Contains(text, "exclude:") {
		t.Fatalf("JSON structure broken: %q", text)
	}
	if !strings.Contains(text, `"exclude":`) {
		t.Fatalf("quoted key lost: %q", text)
	}
	if strings.Contains(text, " ") {
		t.Fatalf("whitespace not stripped: %q", text)
	}
	if strings.Contains(text, "gsxHole") || strings.Contains(text, "900000000") {
		t.Fatalf("sentinel leaked: %q", text)
	}
}

// A single-line holey value has nothing to reindent, so the safe level is a no-op.
func TestMinifyJSAttrHoleySafeSingleLineNoop(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "x-data", Lang: ast.EmbeddedJS,
		Segments: []ast.Markup{&ast.Text{Value: "{ id: "}, &ast.Interp{Expr: "id"}, &ast.Text{Value: " }"}}}))
	if err := jsminFileMinify(f, nil); err != nil {
		t.Fatal(err)
	}
	segs := attrSegs(f)
	if len(segs) != 3 || segs[0].(*ast.Text).Value != "{ id: " || segs[2].(*ast.Text).Value != " }" {
		t.Fatalf("single-line holey js under safe level must be unchanged, got %#v", segs)
	}
}

// A MULTI-LINE indented holey value is NOT minified at the safe level (minifying
// around holes is deferred) but IS reindented: leading indentation is stripped
// while the hole, the newlines, and intra-line spacing survive verbatim. This is
// what makes a holey attribute render consistently with its de-indented holeless
// siblings instead of carrying the source's markup-level tabs.
func TestMinifyJSAttrHoleySafeReindents(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "@change", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{
			&ast.Text{Value: "\n\t\t\tconst v = 1;\n\t\t\t"},
			&ast.Interp{Expr: "x"},
			&ast.Text{Value: " = v;\n\t\t"},
		}}))
	if err := jsminFileMinify(f, nil); err != nil {
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
			if x.Expr != "x" {
				t.Fatalf("hole expr changed: %q", x.Expr)
			}
		}
	}
	if !hasHole {
		t.Fatalf("hole lost in reindent round-trip: %#v", segs)
	}
	if has(text, "\t") {
		t.Fatalf("leading tabs not stripped (not reindented): %q", text)
	}
	if !has(text, "const v = 1;") {
		t.Fatalf("body was altered — safe level must not minify a holey value: %q", text)
	}
	if !containsNL(text) {
		t.Fatalf("newlines dropped (ASI-unsafe): %q", text)
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
