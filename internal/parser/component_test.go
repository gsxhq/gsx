package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

func TestParseComponentSimple(t *testing.T) {
	src := `component Card(title string) {
		<section class="card">{title}</section>
	}`
	p := newParser(src)
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
	p := newParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "(p UsersPage)" || c.Name != "Content" || c.Params != "" {
		t.Fatalf("got %+v", c)
	}
}
