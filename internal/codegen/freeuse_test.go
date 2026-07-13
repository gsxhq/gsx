package codegen

import (
	"go/token"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseComponentBody parses a single component body (the markup between
// `component T() { … }`) from a synthetic .gsx source string and returns its
// Body. Package-level helpers (`bags`, `opt`) referenced by test rows are
// declared so the whole file parses; freeUseAttrs is purely syntactic, so the
// helpers need not type-check.
func parseComponentBody(t *testing.T, body string) []gsxast.Markup {
	t.Helper()
	src := "package v\n\n" +
		"func bags() []any { return nil }\n" +
		"type opt struct{ attrs int }\n\n" +
		"component T() {\n" + body + "\n}\n"
	fset := token.NewFileSet()
	file, errs := gsxparser.ParseFileWithClassifier(fset, "p.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse body %q: %v", body, errs)
	}
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			return c.Body
		}
	}
	t.Fatalf("no component decl parsed from body %q", body)
	return nil
}

var freeUseCases = []struct {
	name string
	body string
	want bool
}{
	// free uses — trigger
	{"spread", `<div { attrs... }>x</div>`, true},
	{"method_in_goblock", `{{ d := attrs.Has("x") }}<div data-d={ d }>y</div>`, true},
	{"closure_over_bag", "{{ f := func() string { return attrs.Class() } }}<div class={ f() }>y</div>", true},
	{"reassign_is_use", `{{ attrs = attrs.Without("id") }}<div { attrs... }>x</div>`, true},
	{"nested_decl_rhs_free", `{ if true { <div data-x={ func() string { attrs := attrs.Class(); return attrs }() }>x</div> } }`, true}, // := RHS evaluates before the new name binds
	{"mixed_shadow_and_free", `<div { attrs... }>{ for _, attrs := range bags() { <span { attrs... }>i</span> } }</div>`, true},
	// bound / not-occurrences — no trigger
	{"range_shadow_only", `{ for _, attrs := range bags() { <span { attrs... }>i</span> } }`, false},
	{"funclit_param_shadow", `{{ f := func(attrs []string) int { return len(attrs) } }}<div data-n={ f(nil) }>x</div>`, false},
	{"struct_key", `{{ o := opt{attrs: 1} }}<div data-n={ o.attrs }>x</div>`, false},
	{"selector_only", `<div data-n={ o.attrs }>x</div>`, false},
	{"string_and_comment", `{{ s := "attrs" /* attrs */ }}<div data-s={ s }>x</div>`, false},
	{"longer_ident", `<div data-n={ attrsList }>x</div>`, false},
	{"label_stmt", `{{ attrs: for { break attrs } }}<div>x</div>`, false},
	{"if_init_shadow", `{ if attrs := bags(); len(attrs) > 0 { <span data-n={ len(attrs) }>i</span> } }`, false},
	{"goblock_toplevel_bind_then_use", `{{ attrs := 1 }}<div data-n={ attrs }>x</div>`, false}, // body-scope bind; walker treats later uses as bound
	// fallback
	{"unparseable_fragment_falls_back_to_token", `{{ attrs ++!garbage }}<div>x</div>`, true},
}

func TestFreeUseAttrs(t *testing.T) {
	for _, tc := range freeUseCases {
		t.Run(tc.name, func(t *testing.T) {
			body := parseComponentBody(t, tc.body)
			if got := freeUseAttrs(body); got != tc.want {
				t.Errorf("freeUseAttrs(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestFragmentBindings(t *testing.T) {
	cases := []struct {
		name string
		src  string
		kind fragKind
		want []boundIdent
	}{
		{"shortvar", "attrs := 1", fragStmts, []boundIdent{{name: "attrs", off: 0}}},
		{"tuple", "attrs, ok := f()", fragStmts, []boundIdent{{name: "attrs", off: 0}}},
		{"const", `const attrs = "x"`, fragStmts, []boundIdent{{name: "attrs", off: 6}}},
		{"range_clause", "for _, attrs := range bags()", fragClause, []boundIdent{{name: "attrs", off: 7}}},
		{"nonreserved", "x := 1", fragStmts, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fragmentBindings(tc.src, tc.kind)
			if len(got) != len(tc.want) {
				t.Fatalf("fragmentBindings(%q) = %+v, want %+v", tc.src, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("fragmentBindings(%q)[%d] = %+v, want %+v", tc.src, i, got[i], tc.want[i])
				}
			}
		})
	}
}
