package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// parseAttrList is a helper: parse the attribute list from the element in a
// minimal gsx file and return the attrs. The src fragment is the content between
// "<div " and ">…</div>".
func parseAttrList(t *testing.T, attrSrc string) []ast.Attr {
	t.Helper()
	// Wrap in a complete valid gsx source.
	full := "package p\ncomponent C() {\n\t<div " + attrSrc + ">x</div>\n}\n"
	f, err := ParseFile(token.NewFileSet(), "t.gsx", full, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, full)
	}
	var el *ast.Element
	ast.Inspect(f, func(n ast.Node) bool {
		if e, ok := n.(*ast.Element); ok && el == nil {
			el = e
		}
		return el == nil
	})
	if el == nil {
		t.Fatal("no element parsed")
	}
	return el.Attrs
}

// parseAttrListErr is like parseAttrList but expects a parse error and returns
// the error string.
func parseAttrListErr(t *testing.T, attrSrc string) string {
	t.Helper()
	full := "package p\ncomponent C() {\n\t<div " + attrSrc + ">x</div>\n}\n"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", full, 0)
	if err == nil {
		t.Fatalf("expected parse error for %q, got none", attrSrc)
	}
	return err.Error()
}

// --- Tests for whitespace around = ---

func TestAttrWS_ExprSpaceBoth(t *testing.T) {
	// data-x = {expr} — ExprAttr with space on both sides of '='.
	// We use "data-x" (not "class"/"style") since those route to ClassAttr.
	attrs := parseAttrList(t, "data-x = {expr}")
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	ea, ok := attrs[0].(*ast.ExprAttr)
	if !ok {
		t.Fatalf("got %T, want *ast.ExprAttr", attrs[0])
	}
	if ea.Name != "data-x" || ea.Expr != "expr" {
		t.Errorf("ExprAttr Name=%q Expr=%q, want data-x/expr", ea.Name, ea.Expr)
	}
}

func TestAttrWS_ExprSpaceBefore(t *testing.T) {
	// data-x ={expr} — ExprAttr with space before '=' only.
	attrs := parseAttrList(t, "data-x ={expr}")
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	ea, ok := attrs[0].(*ast.ExprAttr)
	if !ok {
		t.Fatalf("got %T, want *ast.ExprAttr", attrs[0])
	}
	if ea.Name != "data-x" || ea.Expr != "expr" {
		t.Errorf("ExprAttr Name=%q Expr=%q, want data-x/expr", ea.Name, ea.Expr)
	}
}

func TestAttrWS_ExprSpaceAfter(t *testing.T) {
	// data-x= {expr} — ExprAttr with space after '=' only.
	attrs := parseAttrList(t, "data-x= {expr}")
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	ea, ok := attrs[0].(*ast.ExprAttr)
	if !ok {
		t.Fatalf("got %T, want *ast.ExprAttr", attrs[0])
	}
	if ea.Name != "data-x" || ea.Expr != "expr" {
		t.Errorf("ExprAttr Name=%q Expr=%q, want data-x/expr", ea.Name, ea.Expr)
	}
}

func TestAttrWS_ClassAttrSpace(t *testing.T) {
	// class = {x} — spaces around '='; class routes to ClassAttr (composed).
	attrs := parseAttrList(t, "class = {x}")
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	ca, ok := attrs[0].(*ast.ClassAttr)
	if !ok {
		t.Fatalf("got %T, want *ast.ClassAttr", attrs[0])
	}
	if ca.Name != "class" {
		t.Errorf("ClassAttr Name=%q, want class", ca.Name)
	}
	if len(ca.Parts) != 1 || ca.Parts[0].Expr != "x" {
		t.Errorf("ClassAttr Parts=%+v, want one part with Expr=x", ca.Parts)
	}
}

func TestAttrWS_StaticSpace(t *testing.T) {
	// id = "hi" — static string with space on both sides
	attrs := parseAttrList(t, `id = "hi"`)
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	sa, ok := attrs[0].(*ast.StaticAttr)
	if !ok {
		t.Fatalf("got %T, want *ast.StaticAttr", attrs[0])
	}
	if sa.Name != "id" || sa.Value != "hi" {
		t.Errorf("StaticAttr Name=%q Value=%q, want id/hi", sa.Name, sa.Value)
	}
}

func TestAttrWS_OrderedAttrsSpace(t *testing.T) {
	// a = {{ "data-x": v }} — ordered-attrs literal with space on both sides
	attrs := parseAttrList(t, `a = {{ "data-x": v }}`)
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	oa, ok := attrs[0].(*ast.OrderedAttrsAttr)
	if !ok {
		t.Fatalf("got %T, want *ast.OrderedAttrsAttr", attrs[0])
	}
	if oa.Name != "a" {
		t.Errorf("OrderedAttrsAttr Name=%q, want a", oa.Name)
	}
	if len(oa.Pairs) != 1 {
		t.Fatalf("got %d pairs, want 1: %+v", len(oa.Pairs), oa.Pairs)
	}
	if oa.Pairs[0].Key != "data-x" || oa.Pairs[0].Value != "v" {
		t.Errorf("pair key=%q value=%q, want data-x/v", oa.Pairs[0].Key, oa.Pairs[0].Value)
	}
}

func TestAttrWS_NewlineAndBoolAttr(t *testing.T) {
	// Multi-line: data-x = {expr} on one line + newline whitespace, hidden bool attr on next.
	// Verifies that newlines count as whitespace around '=', AND that the
	// trailing bool attr is still parsed correctly (bool-attr ws-left-intact behavior).
	attrs := parseAttrList(t, "data-x = {expr}\n  hidden")
	if len(attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %+v", len(attrs), attrs)
	}
	ea, ok := attrs[0].(*ast.ExprAttr)
	if !ok {
		t.Fatalf("attrs[0] got %T, want *ast.ExprAttr", attrs[0])
	}
	if ea.Name != "data-x" || ea.Expr != "expr" {
		t.Errorf("ExprAttr Name=%q Expr=%q, want data-x/expr", ea.Name, ea.Expr)
	}
	ba, ok := attrs[1].(*ast.BoolAttr)
	if !ok {
		t.Fatalf("attrs[1] got %T, want *ast.BoolAttr", attrs[1])
	}
	if ba.Name != "hidden" {
		t.Errorf("BoolAttr Name=%q, want hidden", ba.Name)
	}
}

func TestAttrWS_BoolAttrDisambiguation(t *testing.T) {
	// <div foo bar> must produce TWO BoolAttrs, NOT treat foo=bar as a key=value.
	attrs := parseAttrList(t, "foo bar")
	if len(attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %+v", len(attrs), attrs)
	}
	for i, name := range []string{"foo", "bar"} {
		ba, ok := attrs[i].(*ast.BoolAttr)
		if !ok {
			t.Fatalf("attrs[%d] got %T, want *ast.BoolAttr", i, attrs[i])
		}
		if ba.Name != name {
			t.Errorf("attrs[%d].Name=%q, want %q", i, ba.Name, name)
		}
	}
}

func TestAttrWS_JSContextWithSpace(t *testing.T) {
	// onclick = "alert(1)" — JS-context attribute with space around =.
	// Should parse successfully; Name must be "onclick".
	attrs := parseAttrList(t, `onclick = "alert(1)"`)
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1", len(attrs))
	}
	switch a := attrs[0].(type) {
	case *ast.StaticAttr:
		if a.Name != "onclick" {
			t.Errorf("Name=%q, want onclick", a.Name)
		}
	default:
		t.Fatalf("expected StaticAttr for onclick, got %T", attrs[0])
	}
}

func TestAttrWS_ErrorEmptyValue(t *testing.T) {
	// <div x = > — no value after =; must be a parse error.
	errStr := parseAttrListErr(t, "x =")
	if errStr == "" {
		t.Error("expected a non-empty error")
	}
	// The error must NOT be the old "expected attribute name, got '='" message.
	if strings.Contains(errStr, "expected attribute name, got") {
		t.Errorf("wrong error (should be a value-missing error, not name error): %s", errStr)
	}
}

func TestAttrWS_ErrorUnquotedValue(t *testing.T) {
	// <div x = y> — unquoted value; must be a parse error.
	errStr := parseAttrListErr(t, "x = y")
	if errStr == "" {
		t.Error("expected a non-empty error")
	}
}
