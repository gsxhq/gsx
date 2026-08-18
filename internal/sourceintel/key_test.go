package sourceintel

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

const keySrc = `package p
type T struct{ F int; g string }
func (T) M() {}
func (t *T) pm() {}
type unexp int
func (unexp) um() {}
var V, w int
const C = 1
func F(a int) { var local int; _ = local; _ = a }
func use() { _ = w; F(1) }
type G[X any] struct{ Z X }
func (g G[X]) GM(x X) X { return x }
func inst() { var gg G[int]; gg.GM(1); _ = gg.Z }
`

func checkKeySrc(t *testing.T) (*types.Package, *types.Info, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", keySrc, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	return pkg, info, file
}

func defOf(t *testing.T, info *types.Info, file *ast.File, name string, ordinal int) types.Object {
	t.Helper()
	obj := info.Defs[findIdent(t, file, name, ordinal)]
	if obj == nil {
		t.Fatalf("no def for %s#%d", name, ordinal)
	}
	return obj
}

func TestKeyerKeys(t *testing.T) {
	pkg, info, file := checkKeySrc(t)
	k := NewKeyer(pkg)
	want := map[string]struct {
		name    string
		ordinal int
	}{
		"example.com/p T":        {"T", 0},
		"example.com/p T.UF0":    {"F", 0},
		"example.com/p T.UF1":    {"g", 0},
		"example.com/p T.M0":     {"M", 0},
		"example.com/p T.M1":     {"pm", 0},
		"example.com/p unexp":    {"unexp", 0},
		"example.com/p unexp.M0": {"um", 0},
		"example.com/p V":        {"V", 0},
		"example.com/p w":        {"w", 0}, // unexported package-level var: name fallback
		"example.com/p C":        {"C", 0},
		"example.com/p F":        {"F", 1},
		"example.com/p F.PA0":    {"a", 0},
		"example.com/p use":      {"use", 0}, // unexported func: name fallback
		"example.com/p G":        {"G", 0},
		"example.com/p G.UF0":    {"Z", 0},
		"example.com/p G.M0":     {"GM", 0},
	}
	for wantKey, ident := range want {
		got, ok := k.Key(defOf(t, info, file, ident.name, ident.ordinal))
		if !ok || string(got) != wantKey {
			t.Errorf("Key(%s#%d) = %q, %v; want %q", ident.name, ident.ordinal, got, ok, wantKey)
		}
	}
	// local var: per-package ordinal, stable across calls, distinct from other locals
	local := defOf(t, info, file, "local", 0)
	k1, ok1 := k.Key(local)
	k2, _ := k.Key(local)
	if !ok1 || k1 != k2 || string(k1) != "example.com/p #0" {
		t.Fatalf("local key = %q %q %v", k1, k2, ok1)
	}
	// generic instance use resolves to origin key
	var gmUse types.Object
	for id, obj := range info.Uses {
		if id.Name == "GM" {
			gmUse = obj
		}
	}
	if got, _ := k.Key(gmUse); string(got) != "example.com/p G.M0" {
		t.Fatalf("instantiated method use key = %q", got)
	}
	// universe objects are not keyable
	if _, ok := k.Key(types.Universe.Lookup("int")); ok {
		t.Fatal("universe object must not be keyable")
	}
}

func TestKeyerStableAcrossIndependentChecks(t *testing.T) {
	_, info1, file1 := checkKeySrc(t)
	_, info2, file2 := checkKeySrc(t)
	k1, k2 := NewKeyer(nil), NewKeyer(nil)
	for _, name := range []string{"T", "F", "M", "a", "Z", "GM", "unexp", "w"} {
		a, _ := k1.Key(defOf(t, info1, file1, name, 0))
		b, _ := k2.Key(defOf(t, info2, file2, name, 0))
		if a == "" || a != b {
			t.Errorf("%s: %q vs %q", name, a, b)
		}
	}
	// a foreign local (Keyer for another package) is not keyable
	if _, ok := k1.Key(defOf(t, info1, file1, "local", 0)); ok {
		t.Fatal("nil-package Keyer must not key locals")
	}
}
