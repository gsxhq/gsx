package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// skeletonFixture is a minimal pre-built skeleton overlay file (equivalent to
// what buildSkeleton would produce) for a component:
//
//	component Greeting(name string, count int) { {name} {count} }
//
// Line layout (1-indexed):
//
//	1:  package views
//	2:  import _gsxrt "github.com/gsxhq/gsx"
//	3:  import _gsxctx "context"
//	4:  var _ _gsxrt.Node
//	5:  var _ _gsxctx.Context
//	6:  func _gsxuse(...any) {}
//	7:  type GreetingProps struct {
//	8:    Name  string
//	9:    Count int
//
// 10:  }
// 11:  func Greeting(_gsxp GreetingProps) _gsxrt.Node {
// 12:    var ctx _gsxctx.Context
// 13:    _ = ctx
// 14:    name := _gsxp.Name
// 15:    _ = name
// 16:    count := _gsxp.Count
// 17:    _ = count
// 18:    _gsxuse(name)   ← name is the arg; the ident "name" starts at col 10
// 19:    _gsxuse(count)  ← count is the arg
// 20:    return nil
// 21:  }
const skeletonFixture = `package views
import _gsxrt "github.com/gsxhq/gsx"
import _gsxctx "context"
var _ _gsxrt.Node
var _ _gsxctx.Context
func _gsxuse(...any) {}
type GreetingProps struct {
	Name  string
	Count int
}
func Greeting(_gsxp GreetingProps) _gsxrt.Node {
	var ctx _gsxctx.Context
	_ = ctx
	name := _gsxp.Name
	_ = name
	count := _gsxp.Count
	_ = count
	_gsxuse(name)
	_gsxuse(count)
	return nil
}
`

// allowImportsFixture is the set of extra import paths the cached resolver must
// have loaded (context is needed for the _gsxctx alias in the skeleton).
var allowImportsFixture = []string{"context"}

// harvestUseTypes walks all _gsxuse call arguments in the parsed files and
// returns a map of "ident@line" -> type string. The line comes from fset.
// This mirrors the harvest logic but only for _gsxuse calls, and keys by the
// argument identifier name + source line.
func harvestUseTypes(files []*goast.File, info *types.Info, fset *token.FileSet) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		goast.Inspect(f, func(n goast.Node) bool {
			call, ok := n.(*goast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxuse" || len(call.Args) != 1 {
				return true
			}
			arg := call.Args[0]
			tv := info.Types[arg]
			if tv.Type == nil {
				return true
			}
			pos := fset.Position(arg.Pos())
			var name string
			if ident, ok := arg.(*goast.Ident); ok {
				name = ident.Name
			} else {
				name = tv.Type.String()
			}
			key := name + "@" + itoa(pos.Line)
			out[key] = tv.Type.String()
			return true
		})
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestCachedResolverMatchesPackagesLoad verifies that checkSkeletonPackage + bundle.importer()
// produce the expected expression types for the skeleton fixture.
// The fixture has _gsxuse(name) and _gsxuse(count); name should resolve to
// string and count to int — confirming the cached importer correctly threads
// the github.com/gsxhq/gsx package (required for _gsxrt.Node) and context.
func TestCachedResolverMatchesPackagesLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root := repoRoot(t)
	bundle, err := newCachedResolver(root, []string{stdImportPath}, nil, allowImportsFixture)
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	gf, err := goparser.ParseFile(fset, dir+"/comp.x.go", []byte(skeletonFixture), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	files := []*goast.File{gf}
	_, info, errs := checkSkeletonPackage(dir, "views", files, fset, bundle.importer(), testTypeCheckEnvironment())
	if len(errs) != 0 {
		t.Fatalf("unexpected type errors: %v", errs)
	}

	got := harvestUseTypes(files, info, fset)
	want := map[string]string{
		"name@18":  "string",
		"count@19": "int",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("type mismatch %s: cached=%q want %q", k, got[k], v)
		}
	}
	if t.Failed() {
		t.Logf("full harvest: %v", got)
	}
}

func TestCachedResolverAcceptsModuleRootWithoutPackage(t *testing.T) {
	root := t.TempDir()
	repo := repoRoot(t)
	writeFile(t, root, "go.mod", "module example.com/empty\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repo+"\n")

	bundle, err := newCachedResolver(root, []string{stdImportPath}, nil, []string{"context"})
	if err != nil {
		t.Fatalf("newCachedResolver on package-less module root: %v", err)
	}
	if bundle.goVersion != "go1.26.1" {
		t.Fatalf("Bundle language version = %q, want go1.26.1", bundle.goVersion)
	}
}

func TestModuleLanguageVersionDefaultsNoDirectiveToGo116(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/no-go-line\n")
	version, err := moduleLanguageVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	if version != "go1.16" {
		t.Fatalf("moduleLanguageVersion = %q, want cmd/go default go1.16", version)
	}

	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, filepath.Join(root, "generic.go"), "package p\ntype Box[T any] struct{}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, typeErrs := checkSkeletonPackage("example.com/no-go-line", "p", []*goast.File{file}, fset, nil, typeCheckEnvironment{
		sizes: types.SizesFor("gc", "amd64"), goVersion: version,
	})
	if len(typeErrs) == 0 || !strings.Contains(typeErrs[0].Msg, "go1.18") {
		t.Fatalf("generic declaration errors = %v, want post-go1.16 language rejection", typeErrs)
	}
}

func TestCachedResolverRejectsPartialBrokenFilterTypes(t *testing.T) {
	root := t.TempDir()
	repo := repoRoot(t)
	writeFile(t, root, "go.mod", "module example.com/broken\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repo+"\n")
	writeFile(t, filepath.Join(root, "broken"), "filter.go", "package broken\nfunc Upper(value string) string { return missing(value) }\n")

	_, err := newCachedResolver(root, []string{"example.com/broken/broken"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `filter package "example.com/broken/broken" type resolution failed`) {
		t.Fatalf("newCachedResolver broken filter error = %v, want framed load failure", err)
	}
}
