package codegen

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// typeCheckFuncs type-checks src (a complete Go file) and returns its package
// scope, so classifyFilter / isContextContext tests can pull real
// *types.Signature and *types.Type values out of go/types rather than
// hand-building them.
func typeCheckFuncs(t *testing.T, src string) *types.Scope {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("x", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	return pkg.Scope()
}

func sigOf(t *testing.T, scope *types.Scope, name string) *types.Signature {
	t.Helper()
	obj := scope.Lookup(name)
	if obj == nil {
		t.Fatalf("func %q not found", name)
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		t.Fatalf("%q is not a func", name)
	}
	return sig
}

func TestClassifyFilter(t *testing.T) {
	t.Parallel()
	const src = `package x

import "context"

func Bare(s string) string { return s }
func WithArg(s string, n int) string { return s }
func WithErr(s string) (string, error) { return s, nil }
func Ctx(ctx context.Context, v any) (string, error) { return "", nil }
func CtxArgs(ctx context.Context, v any, k string, rest ...any) (string, error) { return "", nil }

// rejected shapes
func Curried(n int) func(string) string { return func(s string) string { return s } }
func ZeroParam() string { return "" }
func CtxOnly(ctx context.Context) string { return "" }
func TooManyResults(s string) (string, string) { return s, s }
`
	scope := typeCheckFuncs(t, src)

	cases := []struct {
		fn      string
		wantCtx bool
		wantOK  bool
	}{
		{"Bare", false, true},
		{"WithArg", false, true},
		{"WithErr", false, true},
		{"Ctx", true, true},
		{"CtxArgs", true, true},
		{"Curried", false, false},        // removed curried shape
		{"ZeroParam", false, false},      // no subject param
		{"CtxOnly", false, false},        // ctx but no subject after it
		{"TooManyResults", false, false}, // (R, non-error) is invalid
	}
	for _, c := range cases {
		t.Run(c.fn, func(t *testing.T) {
			gotCtx, gotOK := classifyFilter(sigOf(t, scope, c.fn))
			if gotOK != c.wantOK {
				t.Fatalf("classifyFilter(%s) ok = %v, want %v", c.fn, gotOK, c.wantOK)
			}
			if gotCtx != c.wantCtx {
				t.Fatalf("classifyFilter(%s) wantsCtx = %v, want %v", c.fn, gotCtx, c.wantCtx)
			}
		})
	}
}

// TestIsCurriedShape proves the curried-shape detector used by the WithFilter
// migration diagnostic distinguishes the removed shape from seed-first filters.
func TestIsCurriedShape(t *testing.T) {
	t.Parallel()
	const src = `package x

func Curried(n int) func(string) string { return func(s string) string { return s } }
func SeedFirst(s string, n int) string { return s }
func TwoResults(s string) (string, error) { return s, nil }
`
	scope := typeCheckFuncs(t, src)
	if !isCurriedShape(sigOf(t, scope, "Curried")) {
		t.Error("Curried should be detected as the curried shape")
	}
	if isCurriedShape(sigOf(t, scope, "SeedFirst")) {
		t.Error("SeedFirst should NOT be the curried shape")
	}
	if isCurriedShape(sigOf(t, scope, "TwoResults")) {
		t.Error("TwoResults should NOT be the curried shape")
	}
}

// TestIsContextContext proves the context.Context detector accepts only the
// real stdlib type and rejects look-alikes.
func TestIsContextContext(t *testing.T) {
	t.Parallel()
	const src = `package x

import (
	stdctx "context"
)

type Context struct{} // a same-named local type that is NOT context.Context

func A(ctx stdctx.Context) {}
func B(c Context) {}
func C(s string) {}
`
	scope := typeCheckFuncs(t, src)
	a := sigOf(t, scope, "A").Params().At(0).Type()
	if !isContextContext(a) {
		t.Error("A's first param should be context.Context")
	}
	b := sigOf(t, scope, "B").Params().At(0).Type()
	if isContextContext(b) {
		t.Error("B's first param (local Context) should NOT be context.Context")
	}
	c := sigOf(t, scope, "C").Params().At(0).Type()
	if isContextContext(c) {
		t.Error("C's first param (string) should NOT be context.Context")
	}
}

// typeParamOf type-checks src and returns the first type parameter of func F.
func typeParamOf(t *testing.T, src string) *types.TypeParam {
	t.Helper()
	scope := typeCheckFuncs(t, src)
	sig := sigOf(t, scope, "F")
	if sig.TypeParams().Len() == 0 {
		t.Fatal("F has no type params")
	}
	return sig.TypeParams().At(0)
}

func TestClassifyTypeParam(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want category
	}{
		{"mixed string|int", `package x
func F[T string | int](v T) {}`, catAnyMixed},
		{"uniform ~string", `package x
func F[T ~string](v T) {}`, catString},
		{"uniform int kinds", `package x
func F[T ~int | ~int64](v T) {}`, catInt},
		{"mixed with tilde", `package x
func F[T ~string | int](v T) {}`, catUnsupported},
		{"unrenderable term", `package x
func F[T string | []int](v T) {}`, catUnsupported},
		{"stringer constraint method", `package x
import "fmt"
func F[T fmt.Stringer](v T) {}`, catStringer},
		{"any", `package x
func F[T any](v T) {}`, catUnsupported},
		{"named non-tilde mixed", `package x
type Slug string
func F[T Slug | int](v T) {}`, catUnsupported},
		{"uniform named", `package x
type Slug string
func F[T Slug](v T) {}`, catString},
		{"named Stringer mixed with string", `package x
type S struct{}
func (S) String() string { return "" }
func F[T S | string](v T) {}`, catAnyMixed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(typeParamOf(t, c.src)); got != c.want {
				t.Fatalf("classify = %v, want %v", got, c.want)
			}
		})
	}
}
