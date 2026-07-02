package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseComponentSimple(t *testing.T) {
	src := `component Card(title string) {
		<section class="card">{title}</section>
	}`
	p := testParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "" || c.Name != "Card" || c.Params != "title string" {
		t.Fatalf("got %+v", c)
	}
	if len(c.Body) != 1 {
		t.Fatalf("body = %#v", c.Body)
	}
	if c.Body[0].(*ast.Element).Tag != "section" {
		t.Fatalf("body0 = %#v", c.Body[0])
	}
}

func TestParseComponentMethod(t *testing.T) {
	src := `component (p UsersPage) Content() {
		<div>{p.Title}</div>
	}`
	p := testParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "(p UsersPage)" || c.Name != "Content" || c.Params != "" {
		t.Fatalf("got %+v", c)
	}
}

func TestParseComponentTypeParams(t *testing.T) {
	src := `component EditCheckbox[T bool | pgtype.Bool](value T) {
		<input checked={value} />
	}`
	p := testParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "" || c.Name != "EditCheckbox" || c.TypeParams != "T bool | pgtype.Bool" || c.Params != "value T" {
		t.Fatalf("got %+v", c)
	}
}

func TestParseComponentTypeParamsNestedSlash(t *testing.T) {
	// '/' inside a nested bracket (array-length division — legal Go:
	// func F[T [8/4]byte]() compiles) must not trip the type-list stop set.
	src := `component Matrix[T [8/4]byte](v T) {
		<p>x</p>
	}`
	p := testParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "" || c.Name != "Matrix" || c.TypeParams != "T [8/4]byte" || c.Params != "v T" {
		t.Fatalf("got %+v", c)
	}
}

func TestParseMethodComponentTypeParams(t *testing.T) {
	src := `component (p Page) EditCheckbox[T bool | pgtype.Bool](value T) {
		<input checked={value} />
	}`
	p := testParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "(p Page)" || c.Name != "EditCheckbox" || c.TypeParams != "T bool | pgtype.Bool" || c.Params != "value T" {
		t.Fatalf("got %+v", c)
	}
}

func TestComponentBodyWithApostrophe(t *testing.T) {
	// C1: apostrophe in body markup on the same line as a later brace must parse.
	src := "package p\ncomponent C(n int) {\n\t<p>Today's items: {n}</p>\n}"
	file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	comp := file.Decls[0].(*ast.Component)
	p := comp.Body[0].(*ast.Element)
	if p.Tag != "p" {
		t.Fatalf("body[0] = %#v, want <p>", comp.Body[0])
	}
	// <p> children: Text "Today's items: " then Interp{n}
	var sawApostropheText, sawInterp bool
	for _, c := range p.Children {
		if txt, ok := c.(*ast.Text); ok && strings.Contains(txt.Value, "Today's") {
			sawApostropheText = true
		}
		if in, ok := c.(*ast.Interp); ok && in.Expr == "n" {
			sawInterp = true
		}
	}
	if !sawApostropheText || !sawInterp {
		t.Fatalf("children = %#v (apostropheText=%v interp=%v)", p.Children, sawApostropheText, sawInterp)
	}
}

func TestComponentBodyControlFlowWithApostrophe(t *testing.T) {
	// C1: apostrophe inside a control-flow body inside a component body.
	src := "package p\ncomponent C(c bool) {\n\t{ if c { <p>it's here</p> } }\n}"
	if _, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0); err != nil {
		t.Fatalf("parse error: %v", err)
	}
}

func TestComponentBodyUnterminated(t *testing.T) {
	// Negative: a body missing its closing brace fails cleanly (no panic/hang).
	src := "package p\ncomponent C() {\n\t<p>hi</p>"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err == nil {
		t.Fatal("expected unterminated-body error, got nil")
	}
	if !strings.Contains(err.Error(), "component body") {
		t.Fatalf("error = %v, want mention of `component body`", err)
	}
}
