package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func firstInterp(f *ast.File) *ast.Interp {
	var got *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if got != nil {
			return false
		}
		if in, ok := n.(*ast.Interp); ok {
			got = in
			return false
		}
		return true
	})
	return got
}

func TestInterpExprPos(t *testing.T) {
	src := []byte("package p\n\ncomponent C() {\n\t<div>{ user.Name }</div>\n}\n")
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	in := firstInterp(f)
	if in == nil {
		t.Fatal("no Interp parsed")
	}
	p := fset.Position(in.ExprPos)
	if src[p.Offset] != 'u' {
		t.Fatalf("ExprPos at offset %d byte %q, want 'u' (start of `user.Name`)", p.Offset, src[p.Offset])
	}
}

func TestExprAttrExprPos(t *testing.T) {
	src := []byte("package p\n\ncomponent C() {\n\t<a href={ dest }>x</a>\n}\n")
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ea *ast.ExprAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if e, ok := n.(*ast.ExprAttr); ok {
			ea = e
			return false
		}
		return true
	})
	if ea == nil {
		t.Fatal("no ExprAttr parsed")
	}
	p := fset.Position(ea.ExprPos)
	if src[p.Offset] != 'd' {
		t.Fatalf("ExprAttr.ExprPos at byte %q, want 'd' (start of `dest`)", src[p.Offset])
	}
}
