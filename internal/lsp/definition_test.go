package lsp

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseOnlyPackage parses src as a .gsx file into a Package with only GSXFset
// and Files set (no types). Sufficient for testing exprNodeAtOffset.
func parseOnlyPackage(t *testing.T, name, src string) (*Package, string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, name, []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	return &Package{GSXFset: fset, Files: map[string]*gsxast.File{name: f}}, name
}

func TestExprNodeAtOffset(t *testing.T) {
	// Build a Package with one .gsx file parsed by the gsx parser, no types.
	src := "package x\n\ncomponent C(u User) {\n\t<div>{ u.Name }</div>\n}\n"
	pkg, path := parseOnlyPackage(t, "c.gsx", src)
	// offset of 'N' in "u.Name"
	off := strings.Index(src, "u.Name") + 2
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		t.Fatalf("no expr node found at offset %d", off)
	}
	if _, ok := node.(*gsxast.Interp); !ok {
		t.Fatalf("node = %T, want *gsxast.Interp", node)
	}
	if !exprPos.IsValid() {
		t.Fatalf("exprNodeAtOffset returned invalid ExprPos")
	}
}

func TestHasPipeStages(t *testing.T) {
	firstInterp := func(src string) *gsxast.Interp {
		fset := token.NewFileSet()
		f, err := gsxparser.ParseFile(fset, "c.gsx", []byte(src), 0)
		if err != nil {
			t.Fatal(err)
		}
		var in *gsxast.Interp
		gsxast.Inspect(f, func(n gsxast.Node) bool {
			if in != nil {
				return false
			}
			if i, ok := n.(*gsxast.Interp); ok {
				in = i
				return false
			}
			return true
		})
		if in == nil {
			t.Fatalf("no Interp parsed from %q", src)
		}
		return in
	}

	plain := firstInterp("package x\n\ncomponent C(u User) {\n\t<div>{ u.Name }</div>\n}\n")
	if hasPipeStages(plain) {
		t.Fatalf("plain interp reported as piped")
	}
	piped := firstInterp("package x\n\ncomponent C(u User) {\n\t<div>{ u.Name |> upper }</div>\n}\n")
	if !hasPipeStages(piped) {
		t.Fatalf("piped interp not reported as piped")
	}
}
