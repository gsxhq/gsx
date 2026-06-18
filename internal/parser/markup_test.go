package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

func TestParseInterp(t *testing.T) {
	p := newParser(`{ user.Name }rest`)
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
	p := newParser(`{ route.URL(ctx)? }`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "route.URL(ctx)" || !n.Try {
		t.Fatalf("got %+v", n)
	}
}

func TestParseText(t *testing.T) {
	p := newParser("hello world<div>")
	n := p.parseText()
	if n.Value != "hello world" {
		t.Fatalf("got %q", n.Value)
	}
	if p.peek() != '<' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseAttrs(t *testing.T) {
	p := newParser(`class="card" id={x} disabled {...rest} data-y={z?}>`)
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
	p := newParser(`<img src="x.png"/>`)
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
	p := newParser(`<ui.Button variant="primary"></ui.Button>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "ui.Button" || el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}

var _ = ast.Text{}
