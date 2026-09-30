package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// A control-flow header that nests a gsx literal or element literal still
// delimits at its real body brace: the literal's @{ } holes and the element's
// children carry braces that must not be taken for the body.
func TestScanToBlockBraceSkipsEmbeddedConstructs(t *testing.T) {
	cases := []struct {
		keyword, header string
	}{
		{"if", ` s := wrap(f` + "`q-@{id}`" + `); s != "" `},
		{"if", ` ok(f"a @{ "}" } b") `},
		{"if", ` ok(js` + "`go(@{ map[string]int{\"a\": 1} })`" + `) `},
		{"for", ` _, s := range []string{wrap(f` + "`r-@{id}`" + `)} `},
		{"switch", ` wrap(css` + "`a:@{id}`" + `) `},
		{"switch", ` x := wrapN(<b>{id}</b>); x `},
		// Regressions: composite literals and plain raw strings still delimit.
		{"for", ` _, v := range []int{1, 2} `},
		{"if", " x := `{`; x != \"\" "},
	}
	for _, c := range cases {
		src := c.keyword + c.header + "{ body }"
		got, ok := scanToBlockBrace(src, len(c.keyword), c.keyword)
		want := len(c.keyword) + len(c.header)
		if !ok || got != want {
			t.Errorf("scanToBlockBrace(%q) = %d, %v; want %d, true", src, got, ok, want)
		}
	}
}

func TestEmbeddedConstructs(t *testing.T) {
	src := "wrap(f`a-@{id}`, js\"x(@{ \"y\" })\", wrapN(<b>{id}</b>)) + `raw`"
	got := EmbeddedConstructs(src)
	want := []struct {
		text      string
		isElement bool
	}{
		{"f`a-@{id}`", false},
		{`js"x(@{ "y" })"`, false},
		{"<b>{id}</b>", true},
	}
	if len(got) != len(want) {
		t.Fatalf("EmbeddedConstructs(%q) = %d constructs, want %d: %+v", src, len(got), len(want), got)
	}
	for i, w := range want {
		if text := src[got[i].Off:got[i].End]; text != w.text || got[i].IsElement != w.isElement {
			t.Errorf("construct %d = %q (element %v), want %q (element %v)", i, text, got[i].IsElement, w.text, w.isElement)
		}
	}
	masked := MaskEmbeddedConstructs(src)
	if len(masked) != len(src) {
		t.Fatalf("mask changed length: %d -> %d", len(src), len(masked))
	}
	if err := validateGoExpr(src); err != nil {
		t.Errorf("validateGoExpr on masked %q: %v", masked, err)
	}
	if strings.Contains(masked, "@{") || strings.Contains(masked, "<b>") {
		t.Errorf("mask left gsx syntax behind: %q", masked)
	}
}

// Nested constructs in class parts and control-flow headers parse; whether the
// position supports them is codegen's diagnostic, not a parse error.
func TestNestedConstructHeadersParse(t *testing.T) {
	src := "package v\n\ncomponent A(id int) {\n" +
		"\t<i class={ \"x\", wrap(f`c-@{id}`), \"on\": ok(f`d-@{id}`) }></i>\n" +
		"\t{ if s := wrap(f`q-@{id}`); s != \"\" { <b>{s}</b> } }\n" +
		"}\n"
	f, errs := ParseFile(token.NewFileSet(), "x.gsx", src, 0)
	if errs != nil {
		t.Fatalf("ParseFile: %v", errs)
	}
	var cond string
	ast.Inspect(f, func(n ast.Node) bool {
		if im, ok := n.(*ast.IfMarkup); ok {
			cond = im.Cond
		}
		return true
	})
	if want := "s := wrap(f`q-@{id}`); s != \"\""; cond != want {
		t.Errorf("if cond = %q, want %q", cond, want)
	}
}

// OrderedPair.ValuePos is the byte-exact base of Value, which the codegen
// split of a nested literal in an ordered-attrs value needs; a value starting
// on the line after its colon must still anchor at its first byte.
func TestOrderedPairValuePos(t *testing.T) {
	src := "package v\n\ncomponent A() {\n" +
		"\t<C attrs={{ \"k\": wrap(f`a`), \"n\":\n\t\twrapN(<b/>) }}/>\n" +
		"}\n"
	fset := token.NewFileSet()
	f, errs := ParseFile(fset, "x.gsx", src, 0)
	if errs != nil {
		t.Fatalf("ParseFile: %v", errs)
	}
	var pairs []ast.OrderedPair
	ast.Inspect(f, func(n ast.Node) bool {
		if oa, ok := n.(*ast.OrderedAttrsAttr); ok {
			pairs = oa.Pairs
		}
		return true
	})
	want := []string{"wrap(", "wrapN("}
	if len(pairs) != len(want) {
		t.Fatalf("got %d pairs, want %d", len(pairs), len(want))
	}
	for i, pr := range pairs {
		if !pr.ValuePos.IsValid() {
			t.Fatalf("pair %d: ValuePos is NoPos", i)
		}
		off := fset.Position(pr.ValuePos).Offset
		if !strings.HasPrefix(src[off:], want[i]) {
			t.Errorf("pair %d: src[ValuePos:] = %q, want prefix %q", i, src[off:min(off+10, len(src))], want[i])
		}
		if got := src[off : off+len(pr.Value)]; got != pr.Value {
			t.Errorf("pair %d: src[ValuePos:+len(Value)] = %q, want %q", i, got, pr.Value)
		}
	}
}
