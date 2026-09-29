package codegen

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestAnalyzeFieldConditionalLiterals(t *testing.T) {
	cases := []struct {
		name          string
		syntax        fieldSyntax
		before, after string
		want          string
	}{
		{"plain call", syntaxExpr, "ok(", ")", ""},
		{"left of &&", syntaxExpr, "ok(", ") && a", ""},
		{"right of &&", syntaxExpr, "a && ok(", ")", logicalRHSErrRemedy},
		{"right of || nested", syntaxExpr, "(a || b) && g(c || ok(", "))", logicalRHSErrRemedy},
		{"left operand of a right side", syntaxExpr, "a || (ok(", ") && b)", logicalRHSErrRemedy},
		{"func literal", syntaxExpr, "call(func() bool { return ok(", ") })", funcLitErrRemedy},
		{"if header with init", syntaxIfHeader, "s := f(); ok(", ")", ""},
		{"if header rhs", syntaxIfHeader, "s := f(); s != \"\" && ok(", ")", logicalRHSErrRemedy},
		{"switch tag", syntaxSwitchHeader, "g(", ")", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lit := &ast.EmbeddedInterp{}
			shape, err := analyzeField([]ast.GoPart{ast.GoText{Src: c.before}, lit, ast.GoText{Src: c.after}}, c.syntax)
			if err != nil {
				t.Fatal(err)
			}
			if got := shape.conditional[lit]; got != c.want {
				t.Errorf("remedy = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnalyzeFieldHeaderInit(t *testing.T) {
	lit := &ast.EmbeddedInterp{}
	parts := []ast.GoPart{ast.GoText{Src: "s := wrap("}, lit, ast.GoText{Src: ");  ok(s)"}}
	shape, err := analyzeField(parts, syntaxIfHeader)
	if err != nil {
		t.Fatal(err)
	}
	m := shape.masked
	if got := m.text[:shape.initEnd]; got != `s := wrap("")` {
		t.Errorf("init = %q", got)
	}
	if got := m.text[shape.bodyStart:]; got != "ok(s)" {
		t.Errorf("cond = %q", got)
	}
	init := m.split(0, shape.initEnd)
	if len(init) != 3 || init[1] != ast.GoPart(lit) || init[2].(ast.GoText).Src != ")" {
		t.Errorf("init parts = %#v", init)
	}
}
