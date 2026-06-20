package codegen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// TestResolveAttrExprType checks that an attribute expression's type is resolved
// (not just interpolations).
func TestResolveAttrExprType(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent A(id string) {\n\t<div data-id={id}></div>\n}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	file, err := gsxparser.ParseFile(token.NewFileSet(), filepath.Join(pkgDir, "views.gsx"), []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveTypesPkg(pkgDir, map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// find the ExprAttr node and assert its resolved type is string.
	var attr *gsxast.ExprAttr
	gsxast.Inspect(file, func(n gsxast.Node) bool {
		if a, ok := n.(*gsxast.ExprAttr); ok {
			attr = a
		}
		return true
	})
	if attr == nil {
		t.Fatal("no ExprAttr in AST")
	}
	got, ok := resolved[attr]
	if !ok || got == nil {
		t.Fatalf("attr expr type not resolved (resolved has %d entries)", len(resolved))
	}
	if b, ok := got.Underlying().(*types.Basic); !ok || b.Info()&types.IsString == 0 {
		t.Fatalf("attr expr type = %s, want string", got)
	}
}
