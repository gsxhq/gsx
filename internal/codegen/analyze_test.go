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

	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, filepath.Join(pkgDir, "views.gsx"), []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file}
	propFields, err := componentPropFieldsFor(files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	resolved, _, err := resolveTypesPkg(pkgDir, files, propFields, fset)
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

// TestComponentPropFieldsFor checks the AST-derived prop-field map (the call-site
// split's source, built BEFORE type resolution): same-package function and method
// components are keyed by the BARE props-type name childInvocation produces, and
// carry their declared param fields plus the synthesized Children (when {children}
// is used) and Attrs (single-root) fields — EXACTLY what the skeleton/emitter
// synthesize, so emit ≡ probe.
func TestComponentPropFieldsFor(t *testing.T) {
	// Card: single-root function component using {children} → CardProps has
	//   {Title, Featured, Children, Attrs}.
	// (p Pg) Grid: single-root method component (no children) → PgGridProps has
	//   {Sort, Attrs}.
	src := "package views\n\n" +
		"type Pg struct{}\n\n" +
		"component Card(title string, featured bool) {\n\t<div>{title}{children}</div>\n}\n\n" +
		"component (p Pg) Grid(sort string) {\n\t<i>{sort}</i>\n}\n"

	file, err := gsxparser.ParseFile(token.NewFileSet(), "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	propFields, err := componentPropFieldsFor(map[string]*gsxast.File{"views.gsx": file})
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}

	card, ok := propFields["CardProps"]
	if !ok {
		t.Fatalf("propFields has no CardProps key (keys: %v)", keysOf(propFields))
	}
	for _, want := range []string{"Title", "Featured", "Children", "Attrs"} {
		if !card[want] {
			t.Errorf("CardProps missing field %q (have %v)", want, card)
		}
	}
	if card["Bogus"] {
		t.Errorf("CardProps unexpectedly has field Bogus")
	}

	grid, ok := propFields["PgGridProps"]
	if !ok {
		t.Fatalf("propFields has no PgGridProps key (keys: %v)", keysOf(propFields))
	}
	if !grid["Sort"] || !grid["Attrs"] {
		t.Errorf("PgGridProps want {Sort, Attrs} (have %v)", grid)
	}
	if grid["Children"] {
		t.Errorf("PgGridProps unexpectedly has Children (Grid does not use {children})")
	}
}

func keysOf(m map[string]map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
