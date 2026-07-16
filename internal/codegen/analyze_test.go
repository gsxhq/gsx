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
	table, err := loadFilterTable(pkgDir)
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}
	skel, _, _, _, _, err := buildSkeleton(file, funcTables{filters: table}, fset, nil, nil, skeletonFull)
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
	table, _, err := loadFilterTableMulti(pkgDir, []string{stdImportPath, "gsxskel/filters"}, nil, nil)
	if err != nil {
		t.Fatalf("loadFilterTableMulti: %v", err)
	}
	skel, _, _, _, _, err := buildSkeleton(file, funcTables{filters: table}, fset, nil, nil, skeletonFull)
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
