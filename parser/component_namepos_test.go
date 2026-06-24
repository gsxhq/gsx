package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestComponentNamePos(t *testing.T) {
	src := []byte("package p\n\ncomponent Card(title string) {\n\t<div/>\n}\n")
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var c *ast.Component
	ast.Inspect(f, func(n ast.Node) bool {
		if comp, ok := n.(*ast.Component); ok {
			c = comp
			return false
		}
		return true
	})
	if c == nil {
		t.Fatal("no component parsed")
	}
	p := fset.Position(c.NamePos)
	if src[p.Offset] != 'C' { // the 'C' of "Card"
		t.Fatalf("NamePos at byte %q, want 'C' (start of `Card`)", src[p.Offset])
	}
}
