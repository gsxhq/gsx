package codegen

import (
	"go/token"
	"slices"
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
		{"tagless switch with init", syntaxSwitchHeader, "n := g(", ");", ""},
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

// TestAnalyzeFieldEvals pins the operands fieldPins may pin: calls,
// receives, logical operations and literals with a hole, in source order,
// with their .gsx spans; nothing inside a func literal; a split keeps spans.
func TestAnalyzeFieldEvals(t *testing.T) {
	const base = token.Pos(100)
	before, litSrc, after := "join(<-ch, f(g()), func() { h() }, a() && b, f`x`, ", "f`@{v}`", ") + T(x)"
	src := before + litSrc + after
	text := func(pos, end token.Pos, s string) ast.GoPart {
		gt := ast.GoText{Src: s}
		ast.SetSpan(&gt, pos, end)
		return gt
	}
	static := &ast.EmbeddedInterp{}
	lit := &ast.EmbeddedInterp{Segments: []ast.Markup{&ast.Interp{Expr: "v"}}}
	staticAt := base + token.Pos(len("join(<-ch, f(g()), func() { h() }, a() && b, "))
	ast.SetSpan(static, staticAt, staticAt+token.Pos(len("f`x`")))
	litAt := base + token.Pos(len(before))
	ast.SetSpan(lit, litAt, litAt+token.Pos(len(litSrc)))
	parts := []ast.GoPart{
		text(base, staticAt, src[:staticAt-base]),
		static,
		text(static.End(), litAt, src[static.End()-base:litAt-base]),
		lit,
		text(lit.End(), base+token.Pos(len(src)), after),
	}
	shape, err := analyzeField(parts, syntaxExpr)
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		kind evalKind
		src  string
	}
	var evals []got
	for _, e := range shape.evals {
		evals = append(evals, got{e.kind, src[e.start-base : e.end-base]})
	}
	want := []got{
		{evalCall, src[:len(src)-len(" + T(x)")]},
		{evalRecv, "<-ch"},
		{evalCall, "f(g())"},
		{evalCall, "g()"},
		{evalLogical, "a() && b"},
		{evalCall, "a()"},
		{evalLiteral, litSrc},
		{evalCall, "T(x)"},
	}
	if !slices.Equal(evals, want) {
		t.Errorf("evals =\n%v\nwant\n%v", evals, want)
	}
	for _, part := range shape.masked.split(0, len("join(")) {
		if part.Pos() != base || part.End() != base+token.Pos(len("join(")) {
			t.Errorf("split span = [%d, %d), want [%d, %d)", part.Pos(), part.End(), base, base+5)
		}
	}
}
