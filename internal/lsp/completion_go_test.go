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

// TestMemberCandidates exercises the method-set + embedded-field BFS over a
// synthetic type, asserting promotion depth and the unexported-visibility gate.
func TestMemberCandidates(t *testing.T) {
	src := `package p

type Base struct{ Shared, base int }
type T struct {
	Base
	Name string
	priv int
}

func (T) M()  {}
func (*T) PM() {}
`
	pkg, _ := buildSyntheticPackage(t, src)
	tObj := pkg.Types.Scope().Lookup("T")
	if tObj == nil {
		t.Fatal("type T not found")
	}
	T := tObj.Type()

	collect := func(samePkg *types.Package) map[string]int {
		m := map[string]int{}
		for _, c := range memberCandidates(T, samePkg) {
			m[c.obj.Name()] = c.depth
		}
		return m
	}

	// Same package: every member is visible, including unexported ones.
	same := collect(pkg.Types)
	for _, name := range []string{"Name", "priv", "Base", "Shared", "base", "M", "PM"} {
		if _, ok := same[name]; !ok {
			t.Errorf("same-package member %q missing; got %v", name, same)
		}
	}
	if same["Shared"] != 1 {
		t.Errorf("Shared depth = %d, want 1", same["Shared"])
	}
	if same["base"] != 1 {
		t.Errorf("base depth = %d, want 1", same["base"])
	}
	if same["Name"] != 0 {
		t.Errorf("Name depth = %d, want 0", same["Name"])
	}
	if same["Base"] != 0 {
		t.Errorf("Base depth = %d, want 0", same["Base"])
	}

	// Other package (samePkg=nil): unexported members are hidden.
	other := collect(nil)
	for _, name := range []string{"Name", "Base", "Shared", "M", "PM"} {
		if _, ok := other[name]; !ok {
			t.Errorf("other-package member %q missing; got %v", name, other)
		}
	}
	for _, name := range []string{"priv", "base"} {
		if _, ok := other[name]; ok {
			t.Errorf("other-package member %q leaked (unexported); got %v", name, other)
		}
	}
}

// TestMemberCandidatesShadowing verifies a shallower field shadows a deeper
// promoted field of the same name (BFS dedup by name).
func TestMemberCandidatesShadowing(t *testing.T) {
	src := `package p

type Inner struct{ X int }
type Outer struct {
	Inner
	X string
}
`
	pkg, _ := buildSyntheticPackage(t, src)
	T := pkg.Types.Scope().Lookup("Outer").Type()
	var xDepth = -1
	var xCount int
	for _, c := range memberCandidates(T, pkg.Types) {
		if c.obj.Name() == "X" {
			xCount++
			xDepth = c.depth
		}
	}
	if xCount != 1 {
		t.Fatalf("expected exactly one X candidate (shallow shadows deep), got %d", xCount)
	}
	if xDepth != 0 {
		t.Errorf("winning X depth = %d, want 0 (Outer's own field shadows Inner's)", xDepth)
	}
}

// TestMemberDispatch asserts the dispatch decision: a cursor on the Sel of an
// enclosing selector takes the member path (enclosingSelector finds it), while a
// plain identifier takes the scope path (no enclosing selector).
func TestMemberDispatch(t *testing.T) {
	// Member position: `u.Na` — the cursor sits on `Na`, the Sel of the selector.
	selExpr, err := parser.ParseExpr("u.Na")
	if err != nil {
		t.Fatal(err)
	}
	sel := selExpr.(*ast.SelectorExpr)
	id := innermostIdent(sel, sel.Sel.Pos())
	if id != sel.Sel {
		t.Fatalf("innermostIdent on selector Sel = %v, want the Sel ident", id)
	}
	if got := enclosingSelector(sel, id); got != sel {
		t.Fatalf("enclosingSelector(sel, Sel) = %v, want the selector itself", got)
	}

	// Scope position: a bare identifier is not the Sel of any selector.
	plain, err := parser.ParseExpr("x")
	if err != nil {
		t.Fatal(err)
	}
	pid := plain.(*ast.Ident)
	if got := enclosingSelector(plain, pid); got != nil {
		t.Fatalf("enclosingSelector on a bare ident = %v, want nil (scope path)", got)
	}

	// The X of a selector is NOT its Sel: a cursor on `u` in `u.Na` completes as
	// a scope identifier, not a member, so enclosingSelector must not match it.
	xid := sel.X.(*ast.Ident)
	if got := enclosingSelector(sel, xid); got != nil {
		t.Fatalf("enclosingSelector on selector X = %v, want nil (X is a scope ident)", got)
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
