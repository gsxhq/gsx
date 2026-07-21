package lsp

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// buildSyntheticPackage type-checks a plain Go source (no gsx involved) into an
// lsp.Package carrying Types, Info (with Scopes), and Fset — exactly the shape
// the scope-walk helpers consume. It returns the package and the *token.File so
// tests can turn byte offsets into token.Pos.
func buildSyntheticPackage(t *testing.T, src string) (*Package, *token.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types:  map[ast.Expr]types.TypeAndValue{},
		Defs:   map[*ast.Ident]types.Object{},
		Uses:   map[*ast.Ident]types.Object{},
		Scopes: map[ast.Node]*types.Scope{},
	}
	// Tolerate type errors (unresolved names in mid-edit fixtures) exactly as
	// the production skeleton typecheck does: go/types fills Info best-effort.
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	tpkg, _ := conf.Check("p", fset, []*ast.File{file}, info)
	pkg := &Package{Types: tpkg, Info: info, Fset: fset}
	return pkg, fset.File(file.Pos())
}

func TestScopeCandidates(t *testing.T) {
	src := `package p

import "strings"

var global = 1

func f(param int) {
	local := 2
	_ = local
	after := 3
	_ = after
}
`
	pkg, tf := buildSyntheticPackage(t, src)

	// POS: inside f's body after local and param are declared but strictly
	// before the `after := 3` declaration (Go's declaration-order rule excludes
	// only objects whose Pos is strictly greater than the cursor).
	markerOff := strings.Index(src, "_ = local") + len("_ = local")
	if markerOff < len("_ = local") {
		t.Fatal("marker not found")
	}
	pos := tf.Pos(markerOff)

	scope := innermostScopeAt(pkg, pos)
	if scope == nil {
		t.Fatal("innermostScopeAt returned nil")
	}
	cands := scopeCandidates(pkg, scope, pos)

	tier := map[string]int{}
	for _, c := range cands {
		tier[c.obj.Name()] = c.tier
	}

	for _, name := range []string{"local", "param", "global", "strings", "f"} {
		if _, ok := tier[name]; !ok {
			t.Errorf("candidate %q missing", name)
		}
	}
	if _, ok := tier["after"]; ok {
		t.Errorf("candidate %q present but is declared after the cursor", "after")
	}
	// Universe entries are visible everywhere.
	for _, name := range []string{"println", "error", "true"} {
		if _, ok := tier[name]; !ok {
			t.Errorf("universe candidate %q missing", name)
		}
	}

	wantTier := map[string]int{
		"strings": tierImported,
		"global":  tierPackage,
		"f":       tierPackage,
		"local":   tierLocal,
		"param":   tierLocal,
		"println": tierUniverse,
		"error":   tierUniverse,
		"true":    tierUniverse,
	}
	for name, want := range wantTier {
		if got := tier[name]; got != want {
			t.Errorf("tier[%q] = %d, want %d", name, got, want)
		}
	}
}

// TestScopeCandidatesShadowing verifies an inner declaration shadows an outer
// name of the same spelling: only the innermost binding is offered.
func TestScopeCandidatesShadowing(t *testing.T) {
	src := `package p

var x = 1

func g() {
	x := "inner"
	_ = x
	println(x)
}
`
	pkg, tf := buildSyntheticPackage(t, src)
	markerOff := strings.Index(src, "println(x)")
	pos := tf.Pos(markerOff)
	scope := innermostScopeAt(pkg, pos)
	cands := scopeCandidates(pkg, scope, pos)

	var xObjs []types.Object
	for _, c := range cands {
		if c.obj.Name() == "x" {
			xObjs = append(xObjs, c.obj)
		}
	}
	if len(xObjs) != 1 {
		t.Fatalf("expected exactly one x candidate, got %d", len(xObjs))
	}
	// The winner is the inner (local) x, not the package var.
	if _, isVar := xObjs[0].(*types.Var); !isVar {
		t.Fatalf("shadowed x is %T, want *types.Var (local)", xObjs[0])
	}
	if xObjs[0].Parent() == pkg.Types.Scope() {
		t.Fatal("shadowed x resolved to the package-scope var, want the local")
	}
}

func TestIsFileScope(t *testing.T) {
	src := `package p

import "strings"

var global = 1
`
	pkg, _ := buildSyntheticPackage(t, src)
	var fileScopeCount int
	for node, s := range pkg.Info.Scopes {
		if _, ok := node.(*ast.File); ok {
			if !isFileScope(pkg, s) {
				t.Errorf("file scope not recognized by isFileScope")
			}
			fileScopeCount++
		}
	}
	if fileScopeCount == 0 {
		t.Fatal("no *ast.File scope found in Info.Scopes")
	}
	// The package scope is not a file scope.
	if isFileScope(pkg, pkg.Types.Scope()) {
		t.Error("package scope wrongly classified as a file scope")
	}
}
