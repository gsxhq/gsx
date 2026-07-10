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
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent A(id string) {\n\t<div data-id={id}></div>\n}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	// Find the ExprAttr in the package's own parsed AST (Package re-parses).
	var attr *gsxast.ExprAttr
	for _, file := range pr.GSXFiles {
		gsxast.Inspect(file, func(n gsxast.Node) bool {
			if a, ok := n.(*gsxast.ExprAttr); ok {
				attr = a
			}
			return true
		})
	}
	if attr == nil {
		t.Fatal("no ExprAttr in AST")
	}
	goExpr, ok := pr.ExprMap[attr]
	if !ok || goExpr == nil {
		t.Fatalf("attr expr not mapped (ExprMap has %d entries)", len(pr.ExprMap))
	}
	tv := pr.Info.Types[goExpr]
	if tv.Type == nil {
		t.Fatal("attr expr type not resolved")
	}
	if b, ok := tv.Type.Underlying().(*types.Basic); !ok || b.Info()&types.IsString == 0 {
		t.Fatalf("attr expr type = %s, want string", tv.Type)
	}
}

// TestComponentPropFieldsFor checks the AST-derived prop-field map (the call-site
// split's source, built BEFORE type resolution): same-package function and method
// components are keyed by the BARE props-type name childInvocation produces, and
// carry their declared param fields plus the synthesized Children (when {children}
// is used) and Attrs (when attrs is referenced) fields — EXACTLY what the skeleton/emitter
// synthesize, so emit ≡ probe.
func TestComponentPropFieldsFor(t *testing.T) {
	t.Parallel()
	// Card: function component using {children} and attrs → CardProps has
	//   {Title, Featured, Children, Attrs}.
	// (p Pg) Grid: method component using attrs (no children) → PgGridProps has
	//   {Sort, Attrs}.
	src := "package views\n\n" +
		"type Pg struct{}\n\n" +
		"component Card(title string, featured bool) {\n\t<div { attrs... }>{title}{children}</div>\n}\n\n" +
		"component (p Pg) Grid(sort string) {\n\t<i { attrs... }>{sort}</i>\n}\n"

	file, err := gsxparser.ParseFile(token.NewFileSet(), "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	propFields, _, _, _, err := componentPropFieldsFor("", map[string]*gsxast.File{"views.gsx": file})
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

// TestIsGsxQualifiedType checks that isGsxQualifiedType recognises the runtime
// selector under the default "gsx" qualifier AND any aliased qualifier, and
// nothing else. This is the sole bag/node type predicate (param + byo struct
// field classification); an aliased `g.Attrs` MUST match so a forwarding spread
// stays sanitized.
func TestIsGsxQualifiedType(t *testing.T) {
	t.Parallel()
	quals := map[string]bool{"gsx": true, "g": true}
	cases := []struct {
		typ  string
		sel  string
		want bool
	}{
		{"gsx.Node", "Node", true},
		{" gsx.Node ", "Node", true},
		{"g.Node", "Node", true}, // aliased runtime import
		{"gsx.Attrs", "Attrs", true},
		{"g.Attrs", "Attrs", true}, // aliased runtime import
		{"string", "Node", false},
		{"int", "Node", false},
		{"[]gsx.Node", "Node", false}, // slice form has no bare selector
		{"gsx.Node2", "Node", false},
		{"other.Attrs", "Attrs", false}, // unrelated qualifier
		{"", "Node", false},
	}
	for _, tc := range cases {
		got := isGsxQualifiedType(tc.typ, quals, tc.sel)
		if got != tc.want {
			t.Errorf("isGsxQualifiedType(%q, quals, %q) = %v, want %v", tc.typ, tc.sel, got, tc.want)
		}
	}
}

// TestNodePropsSignal checks that componentPropFieldsFor derives the nodeProps
// signal: for component Card(title gsx.Node, n int), nodeProps["CardProps"] has
// Title:true and does NOT contain N.
// It also verifies that synthetic Children and Attrs fields (added to propFields
// when a component uses {children} and explicitly references attrs) are NOT promoted into
// nodeProps — only declared gsx.Node params should appear there.
func TestNodePropsSignal(t *testing.T) {
	t.Parallel()
	src := "package views\n\n" +
		"component Card(title gsx.Node, n int) {\n\t<div>{title}</div>\n}\n"

	file, err := gsxparser.ParseFile(token.NewFileSet(), "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, nodeProps, _, _, err := componentPropFieldsFor("", map[string]*gsxast.File{"views.gsx": file})
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

	// Box: {children} + explicit attrs placement → both fields are synthesized;
	// synthesized; nodeProps must NOT include either synthetic field.
	src2 := "package views\n\n" +
		"component Box(label gsx.Node) {\n\t<div { attrs... }>{children}</div>\n}\n"

	file2, err := gsxparser.ParseFile(token.NewFileSet(), "views2.gsx", []byte(src2), 0)
	if err != nil {
		t.Fatal(err)
	}
	propFields2, nodeProps2, _, _, err := componentPropFieldsFor("", map[string]*gsxast.File{"views2.gsx": file2})
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
	t.Parallel()
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
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(pkgDir, files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	table, err := loadFilterTable(pkgDir)
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}
	skel, _, _, _, _, _, err := buildSkeleton(file, table, propFields, nodeProps, attrsProps, nil, nil, byo, nil, fset, nil, nil, nil)
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

// TestSkeletonProbeMidStageErrFilter is the second user checkpoint for
// stage-aware lowerPipe (Task 3): the probe form of a mid-pipeline (R, error)
// filter must _gsxunwrap the failing stage's call so the skeleton stays a
// single expression while still harvesting the RIGHT types for both stages —
// exactly the shape TestLowerPipeMidStageErr pins for the raw lowering, now
// proven end-to-end through buildSkeleton (emit ≡ probe).
func TestSkeletonProbeMidStageErrFilter(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxskel\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	filtersDir := filepath.Join(tmp, "filters")
	if err := os.MkdirAll(filtersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filtersDir, "filters.go", `package filters

import (
	"errors"
	"strings"
)

// Parse splits a comma-separated list; fails on empty input.
func Parse(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("parse: empty input")
	}
	return strings.Split(s, ","), nil
}
`)

	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent Tags(csv string) {\n\t<p>{ csv |> parse |> join(\" \") }</p>\n}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, filepath.Join(pkgDir, "views.gsx"), []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*gsxast.File{filepath.Join(pkgDir, "views.gsx"): file}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(pkgDir, files)
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	table, err := loadFilterTableMulti(pkgDir, []string{stdImportPath, "gsxskel/filters"}, nil)
	if err != nil {
		t.Fatalf("loadFilterTableMulti: %v", err)
	}
	skel, _, _, _, _, _, err := buildSkeleton(file, table, propFields, nodeProps, attrsProps, nil, nil, byo, nil, fset, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}
	if !strings.Contains(skel, `_gsxuse(_gsxstd.Join(_gsxunwrap(_gsxf0.Parse((csv))), " "))`) {
		t.Errorf("skeleton missing mid-stage-err probe form; got:\n%s", skel)
	}
}

// TestResolveCondAttrBranchParts pins Task 2: branch positions inside a COMPONENT
// cond-attr group must join the probe type-harvest. A plain tuple-returning call
// used as an expr-attr value AND as a class part inside `{ if hot { … } }` on a
// component must each get a resolved entry (a *types.Tuple), enabling emit-time
// hoisting (Task 3 consumer). Before Task 2 the ExprAttr in the branch is never
// probed (the component ExprAttr collection is top-level only), so it has no
// resolved entry — RED.
//
// The harness drives the real analysis entry (m.Package) and reconstructs the
// harvested resolved map from ExprMap+Info: resolved[node] == Info.Types[
// ExprMap[node]].Type, exactly what harvest records. Package returns ExprMap even
// when the skeleton still has type errors (the un-lifted branch leaks Task 3
// fixes), and go/types resolves each probe argument's type best-effort regardless.
func TestResolveCondAttrBranchParts(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "helpers.go", "package views\n\nfunc cls(v string) (string, error) { return v, nil }\n")
	src := `package views

import "github.com/gsxhq/gsx"

component Card(title gsx.Node) { <div { attrs... }>{title}</div> }

component Page(hot bool, csv string) {
	<Card title="Hi" { if hot { class={ cls(csv) } data-x={ cls(csv) } } } />
}
`
	writeFile(t, pkgDir, "views.gsx", src)

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	var gotClassPart, gotExprAttr bool
	for node, expr := range pr.ExprMap {
		typ := pr.Info.Types[expr].Type
		if _, isTuple := typ.(*types.Tuple); !isTuple {
			continue
		}
		switch node.(type) {
		case *gsxast.ClassPart:
			gotClassPart = true
		case *gsxast.ExprAttr:
			gotExprAttr = true
		}
	}
	if !gotClassPart || !gotExprAttr {
		t.Fatalf("branch positions not harvested: classPart=%v exprAttr=%v", gotClassPart, gotExprAttr)
	}
}

// TestResolveEmbeddedWholeLiteralPipeType pins Task 3 (body-interp +
// whole-literal-pipe plan): a whole-literal pipeline on a body
// *ast.EmbeddedInterp ({`n=@{n}` |> upper}, n int) and on a braced-attr
// *ast.EmbeddedAttr (class={`c-@{v}` |> upper}, v int) must each resolve —
// via harvest, exactly like a plain *ast.Interp with Stages — to the
// pipeline's RESULT type (upper returns string), even though every hole in
// the assembled seed is a NON-string type. This is the "emit ≡ probe"
// invariant: analyze's seed-assembly must reach the same result type
// codegen's embeddedTextValueExpr-built seed would.
func TestResolveEmbeddedWholeLiteralPipeType(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent Row(n int) {\n" +
		"\t<p>{f`n=@{n}` |> upper}</p>\n" +
		"\t<p class={f`c-@{n}` |> upper}></p>\n" +
		"}\n"
	writeFile(t, pkgDir, "views.gsx", src)

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	isString := func(typ types.Type) bool {
		b, ok := typ.Underlying().(*types.Basic)
		return ok && b.Info()&types.IsString != 0
	}
	var gotInterp, gotAttr bool
	for node, expr := range pr.ExprMap {
		typ := pr.Info.Types[expr].Type
		if typ == nil {
			continue
		}
		switch node.(type) {
		case *gsxast.EmbeddedInterp:
			gotInterp = true
			if !isString(typ) {
				t.Errorf("EmbeddedInterp whole-pipe resolved type = %s, want string", typ)
			}
		case *gsxast.EmbeddedAttr:
			gotAttr = true
			if !isString(typ) {
				t.Errorf("EmbeddedAttr whole-pipe resolved type = %s, want string", typ)
			}
		}
	}
	if !gotInterp {
		t.Fatal("no *ast.EmbeddedInterp node resolved (whole-literal pipe not probed)")
	}
	if !gotAttr {
		t.Fatal("no *ast.EmbeddedAttr node resolved (whole-literal pipe not probed)")
	}
}
