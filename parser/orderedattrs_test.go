package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func parseOneAttrElement(t *testing.T, src string) *ast.OrderedAttrsAttr {
	t.Helper()
	f, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	// Walk to the single OrderedAttrsAttr in the file.
	var found *ast.OrderedAttrsAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if oa, ok := n.(*ast.OrderedAttrsAttr); ok {
			found = oa
		}
		return true
	})
	if found == nil {
		t.Fatal("no OrderedAttrsAttr parsed")
	}
	return found
}

func TestParseOrderedAttrsLiteral(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<Card container-attrs={{ \"data-signals\": sig, \"hx-on:click\": h, \"data-show\": true }}/>\n}\n"
	oa := parseOneAttrElement(t, src)
	if oa.Name != "container-attrs" {
		t.Fatalf("name = %q", oa.Name)
	}
	want := []struct{ k, v string }{
		{"data-signals", "sig"},
		{"hx-on:click", "h"},
		{"data-show", "true"},
	}
	if len(oa.Pairs) != len(want) {
		t.Fatalf("got %d pairs, want %d: %+v", len(oa.Pairs), len(want), oa.Pairs)
	}
	for i, w := range want {
		if oa.Pairs[i].Key != w.k || oa.Pairs[i].Value != w.v {
			t.Errorf("pair %d = %q:%q, want %q:%q", i, oa.Pairs[i].Key, oa.Pairs[i].Value, w.k, w.v)
		}
	}
}

func TestParseOrderedAttrsNestedBracesInValue(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<Card x={{ \"data-m\": map[string]int{\"a\": 1} }}/>\n}\n"
	oa := parseOneAttrElement(t, src)
	if len(oa.Pairs) != 1 || oa.Pairs[0].Value != `map[string]int{"a": 1}` {
		t.Fatalf("nested-brace value mis-parsed: %+v", oa.Pairs)
	}
}

func TestParseOrderedAttrsErrors(t *testing.T) {
	cases := map[string]string{
		"bare key":          "package p\ncomponent C() {\n\t<Card x={{ \"data-x\" }}/>\n}\n",
		"unquoted key":      "package p\ncomponent C() {\n\t<Card x={{ data-x: 1 }}/>\n}\n",
		"standalone spread": "package p\ncomponent C() {\n\t<div {{ \"data-x\": 1 }}>y</div>\n}\n",
	}
	for name, src := range cases {
		if _, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0); err == nil {
			t.Errorf("%s: expected a parse error, got none", name)
		}
	}
}
