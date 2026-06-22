package jsx

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

// parseScript wraps a <script> body in a minimal valid component and returns the
// <script> element. The parser splits the body on @{ … } into Text+Interp
// children (see parser TestScriptAtBraceTriggers).
func parseScript(t *testing.T, body string) (*ast.File, *ast.Element) {
	t.Helper()
	src := "package p\ncomponent C() {\n\t<script>" + body + "</script>\n}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	comp := f.Decls[0].(*ast.Component)
	el := findScript(comp.Body)
	if el == nil {
		t.Fatalf("no <script> element found for %q", body)
	}
	return f, el
}

func findScript(nodes []ast.Markup) *ast.Element {
	for _, n := range nodes {
		if el, ok := n.(*ast.Element); ok && strings.EqualFold(el.Tag, "script") {
			return el
		}
	}
	return nil
}

// interps returns the Interp children of el in order.
func interps(el *ast.Element) []*ast.Interp {
	var out []*ast.Interp
	for _, c := range el.Children {
		if in, ok := c.(*ast.Interp); ok {
			out = append(out, in)
		}
	}
	return out
}

func TestResolveScriptsContexts(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []ast.JSCtx // expected JSCtx per Interp, in order
	}{
		{"assign value", `let x = @{ a }`, []ast.JSCtx{ast.JSCtxValue}},
		{"call arg value", `f(@{ a })`, []ast.JSCtx{ast.JSCtxValue}},
		{"array elements", `let a = [@{ x }, @{ y }]`, []ast.JSCtx{ast.JSCtxValue, ast.JSCtxValue}},
		{"binary operand", `let z = 1 + @{ a }`, []ast.JSCtx{ast.JSCtxValue}},
		{"return value", `function g(){ return @{ a } }`, []ast.JSCtx{ast.JSCtxValue}},
		{"string", `let s = "hi @{ n }!"`, []ast.JSCtx{ast.JSCtxString}},
		{"template text", "let s = `t ${js} @{ g }`", []ast.JSCtx{ast.JSCtxTemplate}},
		{"template expr", "let e = `${ @{ v } }`", []ast.JSCtx{ast.JSCtxValue}},
		{"regex", `let r = /a@{ p }b/g`, []ast.JSCtx{ast.JSCtxRegexp}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, el := parseScript(t, tc.body)
			if err := ResolveScripts(f); err != nil {
				t.Fatalf("ResolveScripts: %v", err)
			}
			got := interps(el)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d interps, want %d: %#v", len(got), len(tc.want), el.Children)
			}
			for i, in := range got {
				if in.JSCtx != tc.want[i] {
					t.Errorf("interp[%d] JSCtx = %d, want %d", i, in.JSCtx, tc.want[i])
				}
			}
		})
	}
}

func TestResolveScriptsTemplateExprUntouchedDollar(t *testing.T) {
	// The JS ${js} in template text is NOT a gsx hole (gsx holes are @{ }).
	// Only @{ g } is an Interp; ${js} stays literal in the Text node.
	f, el := parseScript(t, "let s = `t ${js} @{ g }`")
	if err := ResolveScripts(f); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, c := range el.Children {
		if txt, ok := c.(*ast.Text); ok {
			joined.WriteString(txt.Value)
		}
	}
	if !strings.Contains(joined.String(), "${js}") {
		t.Fatalf("expected ${js} preserved verbatim in text, got %q", joined.String())
	}
	if len(interps(el)) != 1 {
		t.Fatalf("expected exactly one gsx hole, got %d", len(interps(el)))
	}
}

func TestResolveScriptsCommentUnsplit(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // literal substring expected in resulting text
	}{
		{"line comment", "// note @{ c }\n", "@{ c }"},
		{"block comment", "/* @{ c } */ let x=1", "@{ c }"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, el := parseScript(t, tc.body)
			if err := ResolveScripts(f); err != nil {
				t.Fatalf("ResolveScripts: %v", err)
			}
			if n := len(interps(el)); n != 0 {
				t.Fatalf("expected no Interp left (un-split), got %d", n)
			}
			var joined strings.Builder
			for _, c := range el.Children {
				if txt, ok := c.(*ast.Text); ok {
					joined.WriteString(txt.Value)
				}
			}
			if !strings.Contains(joined.String(), tc.want) {
				t.Fatalf("expected literal %q in %q", tc.want, joined.String())
			}
		})
	}
}

func TestResolveScriptsErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"binding position", `let @{ x } = 1`},
		{"member name", `obj.@{ p }`},
		{"optional chain member name", `obj?.@{ p }`},
		{"statement position", `var x = 1; @{ y }`},
		{"placeholder collision", `let _GSXJSHOLE_0 = @{ x }`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := parseScript(t, tc.body)
			if err := ResolveScripts(f); err == nil {
				t.Fatalf("expected error for %q, got nil", tc.body)
			}
		})
	}
}

func TestResolveScriptsIgnoresStyle(t *testing.T) {
	// A <style> interp must be left JSCtxNone (ResolveScripts ignores non-script).
	src := "package p\ncomponent C() {\n\t<style>.a{width:@{w}px}</style>\n}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ResolveScripts(f); err != nil {
		t.Fatal(err)
	}
	comp := f.Decls[0].(*ast.Component)
	var styleInterp *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if in, ok := n.(*ast.Interp); ok {
			styleInterp = in
		}
		return true
	})
	_ = comp
	if styleInterp == nil {
		t.Fatal("no style interp found")
	}
	if styleInterp.JSCtx != ast.JSCtxNone {
		t.Fatalf("style interp JSCtx = %d, want JSCtxNone", styleInterp.JSCtx)
	}
}

func TestResolveScriptsHolelessUnchanged(t *testing.T) {
	f, el := parseScript(t, `var x = 1;`)
	before := len(el.Children)
	if err := ResolveScripts(f); err != nil {
		t.Fatal(err)
	}
	if len(el.Children) != before {
		t.Fatalf("holeless <script> children changed: %d -> %d", before, len(el.Children))
	}
	if len(interps(el)) != 0 {
		t.Fatal("unexpected interp in holeless script")
	}
}
