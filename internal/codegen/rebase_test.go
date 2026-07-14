package codegen

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func jsAttrEl(name, body string) *ast.File {
	el := &ast.Element{Tag: "div", Attrs: []ast.Attr{
		&ast.EmbeddedAttr{Name: name, Lang: ast.EmbeddedJS, Segments: []ast.Markup{&ast.Text{Value: body}}},
	}}
	return &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
}

func embAttrText(f *ast.File) string {
	segs := f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Attrs[0].(*ast.EmbeddedAttr).Segments
	var sb strings.Builder
	for _, s := range segs {
		if t, ok := s.(*ast.Text); ok {
			sb.WriteString(t.Value)
		} else if i, ok := s.(*ast.Interp); ok {
			sb.WriteString("@{" + i.Expr + "}")
		}
	}
	return sb.String()
}

// A js attribute value whose body carries the source markup-depth base (2 tabs)
// is re-based to zero, keeping the relative structure ({ open } object body at 1).
func TestRebaseHolelessStripsCommonIndent(t *testing.T) {
	// `{` attaches at col 0; body at 2 tabs, `}` at 1 tab (the markup base).
	f := jsAttrEl("x-data", "{\n\t\topen: false,\n\t\ttoggle() {\n\t\t\tthis.open = !this.open;\n\t\t},\n\t}")
	rebaseEmbedded(f, true, true)
	want := "{\n\topen: false,\n\ttoggle() {\n\t\tthis.open = !this.open;\n\t},\n}"
	if got := embAttrText(f); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// A holey js body re-bases and preserves its @{ } holes (via the sentinel
// round-trip) with no sentinel leaking into the output.
func TestRebaseHoleyPreservesHoles(t *testing.T) {
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{
		&ast.Element{Tag: "div", Attrs: []ast.Attr{&ast.EmbeddedAttr{
			Name: "x-data", Lang: ast.EmbeddedJS,
			Segments: []ast.Markup{
				&ast.Text{Value: "{\n\t\tid: "},
				&ast.Interp{Expr: "id"},
				&ast.Text{Value: ",\n\t\tk: 1,\n\t}"},
			},
		}}},
	}}}}
	rebaseEmbedded(f, true, true)
	got := embAttrText(f)
	if strings.Contains(got, "gsxRebase") {
		t.Fatalf("sentinel leaked: %q", got)
	}
	if !strings.Contains(got, "@{id}") {
		t.Fatalf("hole lost: %q", got)
	}
	if strings.Contains(got, "\t\tid:") {
		t.Fatalf("not re-based (markup base remains): %q", got)
	}
	if !strings.Contains(got, "\tid: @{id}") {
		t.Fatalf("relative indent not preserved: %q", got)
	}
}

// Under a minified language (doJS=false), rebase is a no-op — the minifier owns
// whitespace.
func TestRebaseSkipsMinifiedLanguage(t *testing.T) {
	body := "{\n\t\topen: false,\n\t}"
	f := jsAttrEl("x-data", body)
	rebaseEmbedded(f, false, false)
	if got := embAttrText(f); got != body {
		t.Fatalf("rebase must not touch a minified language: got %q", got)
	}
}

// A css attribute value is re-based the same way; a JS pass must not touch it.
func TestRebaseCSSAndLangIsolation(t *testing.T) {
	el := &ast.Element{Tag: "div", Attrs: []ast.Attr{
		&ast.EmbeddedAttr{Name: "style", Lang: ast.EmbeddedCSS, Segments: []ast.Markup{&ast.Text{Value: "\t\tcolor: red;\n\t\tmargin: 0;"}}},
	}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{el}}}}
	// JS-only rebase leaves the css attr alone.
	rebaseEmbedded(f, true, false)
	if got := el.Attrs[0].(*ast.EmbeddedAttr).Segments[0].(*ast.Text).Value; got != "\t\tcolor: red;\n\t\tmargin: 0;" {
		t.Fatalf("css attr touched by JS-only rebase: %q", got)
	}
	// CSS rebase re-bases it.
	rebaseEmbedded(f, false, true)
	if got := el.Attrs[0].(*ast.EmbeddedAttr).Segments[0].(*ast.Text).Value; got != "color: red;\nmargin: 0;" {
		t.Fatalf("css attr not re-based: %q", got)
	}
}
