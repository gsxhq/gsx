package lsp

import (
	"go/types"
	"strings"
	"testing"
)

// TestGoBlockJSLiteralHoleDefinition asserts go-to-definition from an @{ }
// hole inside a js`…` literal assembled in a `{{ }}` Go block resolves to the
// enclosing component's parameter — the same resolution a body-position
// f-literal hole gets (interp_embedded_nav_test.go), now exercised through
// GoBlock.Embedded rather than Interp.Embedded.
func TestGoBlockJSLiteralHoleDefinition(t *testing.T) {
	src := "package page\n\ncomponent P(detail string) {\n\t{{ h := js`suggest(@{detail})` }}\n\t<div @click={h}>x</div>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}
	paramDetail := strings.Index(src, "detail string")

	off := strings.Index(src, "@{detail}") + len("@{")
	dp, ok := exprDefinitionAt(pkg, path, off)
	if !ok {
		t.Fatal("goblock js-literal hole did not resolve")
	}
	wantLine, wantCol := lineCol(paramDetail)
	if dp.Line != wantLine || dp.Column != wantCol {
		t.Errorf("hole detail resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
	}
}

// TestTopLevelVarJSLiteralHoleHover asserts hover over an @{ } hole inside a
// js`…` literal in a top-level var initializer (expression position, riding
// directly in a GoWithElements Part rather than any Embedded split) shows the
// hole's resolved Go type.
func TestTopLevelVarJSLiteralHoleHover(t *testing.T) {
	src := "package page\n\nvar id = \"abc\"\n\nvar handler = js`save(@{id})`\n\ncomponent P() {\n\t<button @click={handler}>Save</button>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	off := strings.Index(src, "@{id}") + len("@{")
	obj := hoverObjectAt(t, pkg, path, off)
	if obj == nil {
		t.Fatal("top-level var js-literal hole hover did not resolve")
	}
	if obj.Type().String() != "string" {
		t.Errorf("hole id hover type = %s, want string", obj.Type().String())
	}
	if _, ok := obj.Type().Underlying().(*types.Basic); !ok {
		t.Errorf("hole id hover type underlying = %T, want *types.Basic", obj.Type().Underlying())
	}
}

// TestExprPositionCSSLiteralHoleDefinition asserts go-to-definition from an
// @{ } hole inside a css`…` literal used in Go-expression position (a plain
// func's return statement, riding in the same top-level GoWithElements Part
// path as the var case above) resolves to the func parameter.
func TestExprPositionCSSLiteralHoleDefinition(t *testing.T) {
	src := "package page\n\nimport \"github.com/gsxhq/gsx\"\n\nfunc mk(color string) gsx.RawCSS {\n\treturn css`color:@{color}`\n}\n\ncomponent P() {\n\t<button style={mk(\"teal\")}>x</button>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}
	paramColor := strings.Index(src, "color string")

	off := strings.Index(src, "@{color}") + len("@{")
	dp, ok := exprDefinitionAt(pkg, path, off)
	if !ok {
		t.Fatal("expr-position css-literal hole did not resolve")
	}
	wantLine, wantCol := lineCol(paramColor)
	if dp.Line != wantLine || dp.Column != wantCol {
		t.Errorf("hole color resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
	}
}

// TestNestedLiteralHoleDefinition asserts go-to-definition from an @{ } hole
// belonging to a NESTED f-literal — one written inside another literal's own
// @{ } hole, e.g. f`a @{ f`b @{who}` }` — resolves to the enclosing
// component's parameter. inspectWithEmbedded re-descends every *Interp
// (including one seated inside a Interp.Embedded segment), so the nested
// hole's own Interp.Embedded should resolve exactly like a top-level one; this
// pins that W3c body-position nesting (Task 5) without requiring any
// production code change.
func TestNestedLiteralHoleDefinition(t *testing.T) {
	src := "package page\n\ncomponent P(who string) {\n\t<p>{f`a @{ f`b @{who}` }`}</p>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}
	paramWho := strings.Index(src, "who string")

	off := strings.Index(src, "@{who}") + len("@{")
	dp, ok := exprDefinitionAt(pkg, path, off)
	if !ok {
		t.Fatal("nested literal hole did not resolve")
	}
	wantLine, wantCol := lineCol(paramWho)
	if dp.Line != wantLine || dp.Column != wantCol {
		t.Errorf("nested hole who resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
	}
}

// TestNestedLiteralHoleHover is TestNestedLiteralHoleDefinition's hover
// counterpart: the same nested @{who} hole resolves to who's go/types Object
// via the ExprMap bridge.
func TestNestedLiteralHoleHover(t *testing.T) {
	src := "package page\n\ncomponent P(who string) {\n\t<p>{f`a @{ f`b @{who}` }`}</p>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	off := strings.Index(src, "@{who}") + len("@{")
	obj := hoverObjectAt(t, pkg, path, off)
	if obj == nil || obj.Name() != "who" {
		t.Fatalf("nested literal hole hover obj = %v, want who", obj)
	}
}

// TestComponentPropJSLiteralHoleDefinition is the BONUS row: go-to-definition
// from an @{ } hole inside a js`…` literal supplied as a braced component-prop
// value (`Handler={ js`open(@{id})` }`) resolves to the enclosing component's
// parameter.
func TestComponentPropJSLiteralHoleDefinition(t *testing.T) {
	src := "package page\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Button(handler gsx.RawJS) {\n\t<button @click={handler}>x</button>\n}\n\ncomponent P(id string) {\n\t<Button handler={ js`open(@{id})` }/>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}
	// The hole resolves against P's scope (the caller), not Button's.
	paramID := strings.LastIndex(src, "id string")

	off := strings.Index(src, "@{id}") + len("@{")
	dp, ok := exprDefinitionAt(pkg, path, off)
	if !ok {
		t.Fatal("component-prop js-literal hole did not resolve")
	}
	wantLine, wantCol := lineCol(paramID)
	if dp.Line != wantLine || dp.Column != wantCol {
		t.Errorf("hole id resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
	}
}
