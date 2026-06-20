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
	resolved, _, _, err := resolveTypesPkg(pkgDir, map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file})
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

// TestHarvestStructFields checks the props-struct field lookup resolveTypesPkg
// returns: same-package function and method components' synthesized props structs
// are keyed by the BARE name childInvocation produces, and carry their declared
// param fields plus the Task-1 synthesized Attrs field (single-root component).
func TestHarvestStructFields(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxsf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Card: single-root function component → CardProps{Title, Featured, Attrs}.
	// (p Pg) Grid: single-root method component → PgGridProps{Sort, Attrs}.
	src := "package views\n\n" +
		"type Pg struct{}\n\n" +
		"component Card(title string, featured bool) {\n\t<div>{title}</div>\n}\n\n" +
		"component (p Pg) Grid(sort string) {\n\t<i>{sort}</i>\n}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	file, err := gsxparser.ParseFile(token.NewFileSet(), filepath.Join(pkgDir, "views.gsx"), []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, structFields, err := resolveTypesPkg(pkgDir, map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	card, ok := structFields["CardProps"]
	if !ok {
		t.Fatalf("structFields has no CardProps key (keys: %v)", keysOf(structFields))
	}
	for _, want := range []string{"Title", "Featured", "Attrs"} {
		if !card[want] {
			t.Errorf("CardProps missing field %q (have %v)", want, card)
		}
	}
	if card["Bogus"] {
		t.Errorf("CardProps unexpectedly has field Bogus")
	}

	grid, ok := structFields["PgGridProps"]
	if !ok {
		t.Fatalf("structFields has no PgGridProps key (keys: %v)", keysOf(structFields))
	}
	if !grid["Sort"] {
		t.Errorf("PgGridProps missing field Sort (have %v)", grid)
	}
}

func keysOf(m map[string]map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
