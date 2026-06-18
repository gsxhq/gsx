package parser

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseInterp(t *testing.T) {
	p := testParser(`{ user.Name }rest`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "user.Name" || n.Try {
		t.Fatalf("got %+v", n)
	}
	if p.src[p.i:] != "rest" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseInterpTry(t *testing.T) {
	p := testParser(`{ route.URL(ctx)? }`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "route.URL(ctx)" || !n.Try {
		t.Fatalf("got %+v", n)
	}
}

func TestParseText(t *testing.T) {
	p := testParser("hello world<div>")
	n := p.parseText()
	if n.Value != "hello world" {
		t.Fatalf("got %q", n.Value)
	}
	if p.peek() != '<' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseAttrs(t *testing.T) {
	p := testParser(`class="card" id={x} disabled {...rest} data-y={z?}>`)
	attrs, err := p.parseAttrs()
	if err != nil {
		t.Fatal(err)
	}
	if len(attrs) != 5 {
		t.Fatalf("got %d attrs: %#v", len(attrs), attrs)
	}
	if a, ok := attrs[0].(*ast.StaticAttr); !ok || a.Name != "class" || a.Value != "card" {
		t.Fatalf("attr0 = %#v", attrs[0])
	}
	if a, ok := attrs[1].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr1 = %#v", attrs[1])
	}
	if a, ok := attrs[2].(*ast.BoolAttr); !ok || a.Name != "disabled" {
		t.Fatalf("attr2 = %#v", attrs[2])
	}
	if a, ok := attrs[3].(*ast.SpreadAttr); !ok || a.Expr != "rest" {
		t.Fatalf("attr3 = %#v", attrs[3])
	}
	if a, ok := attrs[4].(*ast.ExprAttr); !ok || a.Name != "data-y" || !a.Try {
		t.Fatalf("attr4 = %#v", attrs[4])
	}
	if p.peek() != '>' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseSelfClosing(t *testing.T) {
	p := testParser(`<img src="x.png"/>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "img" || !el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}

func TestParseDottedComponentTag(t *testing.T) {
	p := testParser(`<ui.Button variant="primary"></ui.Button>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "ui.Button" || el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}

func TestParseChildrenNested(t *testing.T) {
	p := testParser(`<div class="card"><h2>{title}</h2>text</div>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	div := n.(*ast.Element)
	if len(div.Children) != 2 {
		t.Fatalf("got %d children: %#v", len(div.Children), div.Children)
	}
	h2 := div.Children[0].(*ast.Element)
	if h2.Tag != "h2" {
		t.Fatalf("child0 = %#v", h2)
	}
	if _, ok := h2.Children[0].(*ast.Interp); !ok {
		t.Fatalf("h2 child = %#v", h2.Children[0])
	}
	if txt := div.Children[1].(*ast.Text); txt.Value != "text" {
		t.Fatalf("child1 = %#v", div.Children[1])
	}
}

func TestParseMarkupAttr(t *testing.T) {
	p := testParser(`<Panel header={ <h1>Hi</h1> }></Panel>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	ma := el.Attrs[0].(*ast.MarkupAttr)
	if ma.Name != "header" || len(ma.Value) != 1 {
		t.Fatalf("got %#v", ma)
	}
	if ma.Value[0].(*ast.Element).Tag != "h1" {
		t.Fatalf("markup attr value = %#v", ma.Value[0])
	}
}

func TestParseChildrenMismatch(t *testing.T) {
	p := testParser(`<div>hi</span>`)
	_, err := p.parseElement()
	if err == nil {
		t.Fatalf("expected mismatched-close-tag error, got nil")
	}
	if !strings.Contains(err.Error(), "mismatched close tag") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseSpreadWhitespace(t *testing.T) {
	// {...expr}, { ...expr }, and {...  expr  } must all parse as one spread.
	for _, src := range []string{`{...rest}>`, `{ ...rest }>`, `{...  rest  }>`} {
		p := testParser(src)
		attrs, err := p.parseAttrs()
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if len(attrs) != 1 {
			t.Fatalf("%q: got %d attrs: %#v", src, len(attrs), attrs)
		}
		sa, ok := attrs[0].(*ast.SpreadAttr)
		if !ok || sa.Expr != "rest" {
			t.Fatalf("%q: got %#v", src, attrs[0])
		}
	}
}

func TestTagTrailingLineComment(t *testing.T) {
	// `<div id={x} // trailing\n class="y">` → div with exactly two attrs (ExprAttr id, StaticAttr class)
	p := testParser("<div id={x} // trailing\n class=\"y\"></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
	if a, ok := el.Attrs[1].(*ast.StaticAttr); !ok || a.Name != "class" || a.Value != "y" {
		t.Fatalf("attr1 = %#v, want StaticAttr{class, y}", el.Attrs[1])
	}
}

func TestTagOwnLineComment(t *testing.T) {
	// `<div\n // own line\n id={x}>` → div with one attr id
	p := testParser("<div\n // own line\n id={x}></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
}

func TestTagBlockComment(t *testing.T) {
	// `<div /* note */ id={x}>` → div with one attr id
	p := testParser("<div /* note */ id={x}></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
}

func TestUnterminatedTagBlockComment(t *testing.T) {
	// `<div /* oops>` → parseElement returns an error mentioning "unterminated block comment"
	p := testParser("<div /* oops>")
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for unterminated block comment, got nil")
	}
	if !strings.Contains(err.Error(), "unterminated block comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

var _ = ast.Text{}
