package parser

import (
	"go/token"
	"strings"
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

// Fix 2: bare-key error should use %s (not %q on the segment) and position
// the caret at the first non-whitespace byte of the segment.

func TestOrderedAttrsErrorBareKeyMessage(t *testing.T) {
	// Input: x={{ "data-x" }} — bare key, no ": value".
	src := "package p\ncomponent C() {\n\t<Card x={{ \"data-x\" }}/>\n}\n"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err == nil {
		t.Fatal("expected parse error, got none")
	}
	got := err.Error()
	// Old (buggy): ordered-attrs pair missing value (bare key): "\"data-x\""
	// New (correct): ordered-attrs pair "data-x" is missing a ": value"
	if strings.Contains(got, `\"`) {
		t.Errorf("error message has double-escaped quotes (%%q bug): %s", got)
	}
	if !strings.Contains(got, `"data-x"`) {
		t.Errorf("error message should contain the segment text \"data-x\": %s", got)
	}
	if !strings.Contains(got, `is missing a ": value"`) {
		t.Errorf("error message should say 'is missing a \": value\"': %s", got)
	}
}

func TestOrderedAttrsErrorMissingValueMessage(t *testing.T) {
	// Input: x={{ "data-x": }} — key with empty value.
	src := "package p\ncomponent C() {\n\t<Card x={{ \"data-x\": }}/>\n}\n"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err == nil {
		t.Fatal("expected parse error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "missing value for key") {
		t.Errorf("error message should say 'missing value for key': %s", got)
	}
}

// Fix 3: leading / interior empty segments must error; trailing comma is legal.

func TestOrderedAttrsTrailingCommaLegal(t *testing.T) {
	// Trailing comma is legal: {{ "k": v, }}
	src := "package p\ncomponent C() {\n\t<Card x={{ \"data-x\": 1, }}/>\n}\n"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("trailing comma should be legal, got error: %v", err)
	}
}

func TestOrderedAttrsLeadingCommaError(t *testing.T) {
	// Leading comma is illegal: {{ , "k": v }}
	src := "package p\ncomponent C() {\n\t<Card x={{ , \"data-x\": 1 }}/>\n}\n"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err == nil {
		t.Fatal("leading comma should be a parse error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "stray comma") {
		t.Errorf("error should mention 'stray comma': %s", got)
	}
}

func TestOrderedAttrsDoubleCommaError(t *testing.T) {
	// Interior double comma: {{ "k": v,, "j": w }}
	src := "package p\ncomponent C() {\n\t<Card x={{ \"k\": 1,, \"j\": 2 }}/>\n}\n"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err == nil {
		t.Fatal("double interior comma should be a parse error, got none")
	}
	got := err.Error()
	if !strings.Contains(got, "stray comma") {
		t.Errorf("error should mention 'stray comma': %s", got)
	}
}
