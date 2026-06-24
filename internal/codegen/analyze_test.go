package codegen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
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
	propFields, nodeProps, byo, err := componentPropFieldsFor(pkgDir, files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	resolved, _, err := resolveTypesPkg(pkgDir, files, propFields, nodeProps, byo, fset)
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
	propFields, _, _, err := componentPropFieldsFor("", map[string]*gsxast.File{"views.gsx": file})
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

// TestIsGsxNodeType checks that isGsxNodeType recognises exactly "gsx.Node"
// (with optional surrounding whitespace) and nothing else.
func TestIsGsxNodeType(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"gsx.Node", true},
		{" gsx.Node ", true},
		{"string", false},
		{"int", false},
		{"[]gsx.Node", false},
		{"gsx.Node2", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isGsxNodeType(tc.typ)
		if got != tc.want {
			t.Errorf("isGsxNodeType(%q) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

// TestNodePropsSignal checks that componentPropFieldsFor derives the nodeProps
// signal: for component Card(title gsx.Node, n int), nodeProps["CardProps"] has
// Title:true and does NOT contain N.
// It also verifies that synthetic Children and Attrs fields (added to propFields
// when a component uses {children} and has a single root) are NOT promoted into
// nodeProps — only declared gsx.Node params should appear there.
func TestNodePropsSignal(t *testing.T) {
	src := "package views\n\n" +
		"component Card(title gsx.Node, n int) {\n\t<div>{title}</div>\n}\n"

	file, err := gsxparser.ParseFile(token.NewFileSet(), "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, nodeProps, _, err := componentPropFieldsFor("", map[string]*gsxast.File{"views.gsx": file})
	if err != nil {
		t.Fatalf("componentPropFieldsFor: %v", err)
	}

	card, ok := nodeProps["CardProps"]
	if !ok {
		t.Fatalf("nodeProps has no CardProps key (keys: %v)", keysOf(nodeProps))
	}
	if !card["Title"] {
		t.Errorf("nodeProps[CardProps] missing Title (have %v)", card)
	}
	if card["N"] {
		t.Errorf("nodeProps[CardProps] unexpectedly has N (int param should not be a node prop)")
	}

	// Box: single-root + {children} → propFields gets both Children and Attrs
	// synthesized; nodeProps must NOT include either synthetic field.
	src2 := "package views\n\n" +
		"component Box(label gsx.Node) {\n\t<div>{children}</div>\n}\n"

	file2, err := gsxparser.ParseFile(token.NewFileSet(), "views2.gsx", []byte(src2), 0)
	if err != nil {
		t.Fatal(err)
	}
	propFields2, nodeProps2, _, err := componentPropFieldsFor("", map[string]*gsxast.File{"views2.gsx": file2})
	if err != nil {
		t.Fatalf("componentPropFieldsFor (Box): %v", err)
	}

	// Confirm the fixture actually triggered both syntheses (precondition).
	box := propFields2["BoxProps"]
	if !box["Children"] {
		t.Fatalf("precondition: BoxProps should have synthetic Children field (have %v)", box)
	}
	if !box["Attrs"] {
		t.Fatalf("precondition: BoxProps should have synthetic Attrs field (have %v)", box)
	}

	boxNode, ok := nodeProps2["BoxProps"]
	if !ok {
		t.Fatalf("nodeProps has no BoxProps key (keys: %v)", keysOf(nodeProps2))
	}
	if !boxNode["Label"] {
		t.Errorf("nodeProps[BoxProps] missing Label (declared gsx.Node param) (have %v)", boxNode)
	}
	if boxNode["Children"] {
		t.Errorf("nodeProps[BoxProps] unexpectedly contains synthetic Children field")
	}
	if boxNode["Attrs"] {
		t.Errorf("nodeProps[BoxProps] unexpectedly contains synthetic Attrs field")
	}
}

// TestChildPropPipelineSkeletonImportsStd verifies the emit≡probe import
// plumbing for a child-component prop pipeline: childPropsLiteral surfaces the
// filter packages a lowered prop pipeline references, so buildSkeleton imports
// the std filter package under its reserved _gsxstd alias and the lowered
// _gsxstd.Upper(...) call resolves. Without the threading the skeleton would
// not import std and type resolution would fail.
func TestChildPropPipelineSkeletonImportsStd(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(title gsx.Node) { <div class=\"card\">{title}</div> }\n\ncomponent Page(name string) {\n\t<Card title={ name |> upper } />\n}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, filepath.Join(pkgDir, "views.gsx"), []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file}
	propFields, nodeProps, byo, err := componentPropFieldsFor(pkgDir, files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	table, err := loadFilterTable(pkgDir)
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}
	skel, _, err := buildSkeleton(file, table, propFields, nodeProps, byo, fset)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}
	if !strings.Contains(skel, `import _gsxstd "github.com/gsxhq/gsx/std"`) {
		t.Errorf("skeleton missing std filter import; got:\n%s", skel)
	}
	if !strings.Contains(skel, "_gsxstd.Upper((name))") {
		t.Errorf("skeleton missing lowered pipeline call; got:\n%s", skel)
	}
}
