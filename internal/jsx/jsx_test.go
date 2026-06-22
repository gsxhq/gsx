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
		{"comment hole script-breakout", "const u = @{ id }; // x @{ \"</script>\" }\n"},
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

// setType appends (or replaces) a static `type` attribute on el.
func setType(el *ast.Element, val string) {
	el.Attrs = append(el.Attrs, &ast.StaticAttr{Name: "type", Value: val})
}

func TestResolveDataIsland(t *testing.T) {
	t.Run("bare value", func(t *testing.T) {
		f, el := parseScript(t, `@{ data }`)
		setType(el, "application/json")
		if err := ResolveScripts(f); err != nil {
			t.Fatalf("ResolveScripts: %v", err)
		}
		ins := interps(el)
		if len(ins) != 1 || ins[0].JSCtx != ast.JSCtxValue {
			t.Fatalf("got %d interps ctx %v; want 1 JSCtxValue", len(ins), ins[0].JSCtx)
		}
	})
	t.Run("whitespace padded", func(t *testing.T) {
		f, el := parseScript(t, `  @{ data }  `)
		setType(el, "application/json")
		if err := ResolveScripts(f); err != nil {
			t.Fatalf("ResolveScripts: %v", err)
		}
		ins := interps(el)
		if len(ins) != 1 || ins[0].JSCtx != ast.JSCtxValue {
			t.Fatalf("got %d interps ctx %v; want 1 JSCtxValue", len(ins), ins[0].JSCtx)
		}
	})
	t.Run("multiple holes rejected", func(t *testing.T) {
		f, el := parseScript(t, `@{ a } @{ b }`)
		setType(el, "application/json")
		if err := ResolveScripts(f); err == nil {
			t.Fatal("expected error for multiple holes, got nil")
		}
	})
	t.Run("literal text rejected", func(t *testing.T) {
		f, el := parseScript(t, `[@{ a }]`)
		setType(el, "application/json")
		if err := ResolveScripts(f); err == nil {
			t.Fatal("expected error for literal text, got nil")
		}
	})
	t.Run("module type stays JS path fails closed", func(t *testing.T) {
		// type="module" is executable JS: a bare @{ data } is a start-of-input
		// (non-value) position and must fail closed on the JS path, unchanged.
		f, el := parseScript(t, `@{ data }`)
		setType(el, "module")
		if err := ResolveScripts(f); err == nil {
			t.Fatal("expected fail-closed error for bare hole in type=module script, got nil")
		}
	})
}

// jsAttrSegs builds a JS-attribute Segments list from a template where "@" marks
// a hole: each "@" in parts[i] boundary becomes an *ast.Interp{Expr: exprs[i]}.
// Caller passes the literal text chunks and the hole exprs interleaved.
func jsAttrSegs(texts []string, exprs []string) []ast.Markup {
	var segs []ast.Markup
	for i, tx := range texts {
		if tx != "" {
			segs = append(segs, &ast.Text{Value: tx})
		}
		if i < len(exprs) {
			segs = append(segs, &ast.Interp{Expr: exprs[i]})
		}
	}
	return segs
}

func interpSegs(segs []ast.Markup) []*ast.Interp {
	var out []*ast.Interp
	for _, s := range segs {
		if in, ok := s.(*ast.Interp); ok {
			out = append(out, in)
		}
	}
	return out
}

func TestResolveJSAttrValueContext(t *testing.T) {
	// x-data="{ tab: @{ tab }, open: false }"
	segs := jsAttrSegs([]string{"{ tab: ", ", open: false }"}, []string{" tab "})
	if err := ResolveJSAttr("x-data", segs); err != nil {
		t.Fatalf("ResolveJSAttr: %v", err)
	}
	ins := interpSegs(segs)
	if len(ins) != 1 || ins[0].JSCtx != ast.JSCtxValue {
		t.Fatalf("got %d interps, ctx %v; want 1 JSCtxValue", len(ins), ins[0].JSCtx)
	}
}

func TestResolveJSAttrStringContext(t *testing.T) {
	// x-init="fetch('/x/@{ id }')"
	segs := jsAttrSegs([]string{"fetch('/x/", "')"}, []string{" id "})
	if err := ResolveJSAttr("x-init", segs); err != nil {
		t.Fatalf("ResolveJSAttr: %v", err)
	}
	ins := interpSegs(segs)
	if len(ins) != 1 || ins[0].JSCtx != ast.JSCtxString {
		t.Fatalf("ctx = %v; want JSCtxString", ins[0].JSCtx)
	}
}

func TestResolveJSAttrBindingRejected(t *testing.T) {
	// x-on:click="@{ stmt } = 1" — binding/identifier position
	segs := jsAttrSegs([]string{"", " = 1"}, []string{" stmt "})
	err := ResolveJSAttr("x-on:click", segs)
	if err == nil {
		t.Fatal("expected fail-closed error for binding position, got nil")
	}
	if !strings.Contains(err.Error(), "identifier/binding") {
		t.Fatalf("error = %q; want identifier/binding rejection", err)
	}
}

func TestResolveJSAttrCommentRejected(t *testing.T) {
	// onclick="/* @{ x } */ doThing()" — hole inside a JS comment
	segs := jsAttrSegs([]string{"/* ", " */ doThing()"}, []string{" x "})
	err := ResolveJSAttr("onclick", segs)
	if err == nil {
		t.Fatal("expected fail-closed error for comment-context hole, got nil")
	}
	if !strings.Contains(err.Error(), "JS comment") {
		t.Fatalf("error = %q; want JS comment rejection", err)
	}
}
