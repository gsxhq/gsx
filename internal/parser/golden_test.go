// internal/parser/golden_test.go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

const goldenSrc = `package examples

import "github.com/gsxhq/gsx"

component Card(title string, featured bool) {
	<section class="card">
		<h2>{title}</h2>
		{children}
	</section>
}

component Panel(header gsx.Node) {
	<div class="panel">
		<div class="head">{header}</div>
		{children}
	</div>
}
`

func TestGoldenCore(t *testing.T) {
	f, err := Parse(goldenSrc)
	if err != nil {
		t.Fatal(err)
	}
	if f.Package != "examples" {
		t.Fatalf("package = %q", f.Package)
	}

	var card, panel *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			switch c.Name {
			case "Card":
				card = c
			case "Panel":
				panel = c
			}
		}
	}
	if card == nil || panel == nil {
		t.Fatalf("missing components: %#v", f.Decls)
	}
	if card.Params != "title string, featured bool" {
		t.Fatalf("card params = %q", card.Params)
	}
	section := card.Body[0].(*ast.Element)
	if section.Tag != "section" {
		t.Fatalf("card root = %#v", section)
	}
	if a := section.Attrs[0].(*ast.StaticAttr); a.Name != "class" || a.Value != "card" {
		t.Fatalf("section attr = %#v", section.Attrs[0])
	}
	// section children: <h2>…</h2>, {children}  (whitespace text nodes also present)
	var sawH2, sawChildren bool
	for _, ch := range section.Children {
		switch v := ch.(type) {
		case *ast.Element:
			if v.Tag == "h2" {
				sawH2 = true
				if _, ok := v.Children[0].(*ast.Interp); !ok {
					t.Fatalf("h2 child = %#v", v.Children[0])
				}
			}
		case *ast.Interp:
			if v.Expr == "children" {
				sawChildren = true
			}
		}
	}
	if !sawH2 || !sawChildren {
		t.Fatalf("section children = %#v", section.Children)
	}
}
