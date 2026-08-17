package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// two packages: dep declares T and F; app uses dep.T and dep.F, plus a local.
func buildTwoPackageGraph(t *testing.T) (*SymbolGraph, string, string) {
	t.Helper()
	const depSrc = "package dep\n\ntype T struct{ X int }\n\nfunc F(t T) int { return t.X }\n"
	const appSrc = "package app\n\nimport \"example.com/dep\"\n\nfunc use() int {\n\tvar v dep.T\n\tv.X = 1\n\treturn dep.F(v)\n}\n"
	fset := token.NewFileSet()
	depFile, _ := parser.ParseFile(fset, "dep.go", depSrc, 0)
	appFile, _ := parser.ParseFile(fset, "app.go", appSrc, 0)
	newInfo := func() *types.Info {
		return &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	}
	depInfo := newInfo()
	depPkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/dep", fset, []*ast.File{depFile}, depInfo)
	if err != nil {
		t.Fatal(err)
	}
	// SECOND, independent check of dep — app imports this one, so object
	// pointers differ from depPkg's; keys must still agree.
	depInfo2 := newInfo()
	depPkg2, _ := (&types.Config{Importer: importer.Default()}).Check("example.com/dep", fset, []*ast.File{depFile}, depInfo2)
	appInfo := newInfo()
	imp := importerFunc(func(path string) (*types.Package, error) {
		if path == "example.com/dep" {
			return depPkg2, nil
		}
		return importer.Default().Import(path)
	})
	appPkg, err := (&types.Config{Importer: imp}).Check("example.com/app", fset, []*ast.File{appFile}, appInfo)
	if err != nil {
		t.Fatal(err)
	}
	mapped := func(file *ast.File, path, src string) MappedFile {
		sm, _ := IdentitySourceMap(path, len(src))
		return MappedFile{AST: file, TokenFile: fset.File(file.Pos()), SourceMap: sm, SourceVersion: SourceVersion{Size: len(src), SHA256: sha256.Sum256([]byte(src))}}
	}
	g := NewSymbolGraph()
	g.AddIndex(BuildIndex(depInfo, []MappedFile{mapped(depFile, "dep.go", depSrc)}), NewKeyer(depPkg))
	g.AddIndex(BuildIndex(appInfo, []MappedFile{mapped(appFile, "app.go", appSrc)}), NewKeyer(appPkg))
	return g, depSrc, appSrc
}

type importerFunc func(string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func TestSymbolGraphMergesCrossPackageByKey(t *testing.T) {
	g, depSrc, appSrc := buildTwoPackageGraph(t)
	key, span, ok := g.At("app.go", strings.Index(appSrc, "dep.T")+4)
	if !ok || string(key) != "example.com/dep T" || span.Path != "app.go" {
		t.Fatalf("At(app dep.T) = %q %+v %v", key, span, ok)
	}
	defs := g.Definitions(key)
	if len(defs) != 1 || defs[0] != (Span{Path: "dep.go", Start: strings.Index(depSrc, "T struct"), End: strings.Index(depSrc, "T struct") + 1}) {
		t.Fatalf("Definitions(T) = %+v", defs)
	}
	refs := g.References(key)
	if len(refs) != 2 { // dep.go "F(t T)" and app.go "dep.T"
		t.Fatalf("References(T) = %+v, want 2", refs)
	}
	xKey, _, _ := g.At("app.go", strings.Index(appSrc, "v.X")+2)
	if string(xKey) != "example.com/dep T.UF0" || len(g.References(xKey)) != 2 || len(g.Definitions(xKey)) != 1 {
		t.Fatalf("field X: key=%q refs=%+v defs=%+v", xKey, g.References(xKey), g.Definitions(xKey))
	}
	if !g.MatchesSource("app.go", []byte(appSrc)) || g.MatchesSource("app.go", []byte(appSrc+" ")) {
		t.Fatal("MatchesSource must gate on the exact indexed bytes")
	}
	if _, _, ok := g.At("nope.go", 0); ok {
		t.Fatal("unknown path must miss")
	}
	// local var v: keyed, references within app.go only
	vKey, _, ok := g.At("app.go", strings.Index(appSrc, "var v")+4)
	if !ok || !strings.HasPrefix(string(vKey), "example.com/app #") || len(g.References(vKey)) != 2 {
		t.Fatalf("local v: %q %v refs=%+v", vKey, ok, g.References(vKey))
	}
}

// TestSymbolGraphUseAtEmbeddedField pins the one ident that carries a
// definition AND a use at a single span: an embedded struct field. At must keep
// answering the field (hover/rename/references want it); UseAt must answer the
// embedded type, which is where go-to-definition has to travel.
func TestSymbolGraphUseAtEmbeddedField(t *testing.T) {
	const src = "package emb\n\ntype A struct{}\n\ntype B struct{ A }\n\nfunc use(b B) A { return b.A }\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "emb.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/emb", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	sm, _ := IdentitySourceMap("emb.go", len(src))
	g := NewSymbolGraph()
	g.AddIndex(BuildIndex(info, []MappedFile{{
		AST: file, TokenFile: fset.File(file.Pos()), SourceMap: sm,
		SourceVersion: SourceVersion{Size: len(src), SHA256: sha256.Sum256([]byte(src))},
	}}), NewKeyer(pkg))

	embedded := strings.Index(src, "struct{ A }") + len("struct{ ")
	key, span, ok := g.At("emb.go", embedded)
	if !ok || string(key) != "example.com/emb B.UF0" { // objectpath spelling of B's field 0
		t.Fatalf("At(embedded field) = %q %+v %v, want the field key", key, span, ok)
	}
	useKey, useSpan, ok := g.UseAt("emb.go", embedded)
	if !ok || string(useKey) != "example.com/emb A" {
		t.Fatalf("UseAt(embedded field) = %q %+v %v, want the embedded type key", useKey, useSpan, ok)
	}
	if useSpan != span {
		t.Fatalf("UseAt span %+v != At span %+v; both name the same ident", useSpan, span)
	}
	defs := g.Definitions(useKey)
	if len(defs) != 1 || defs[0].Start != strings.Index(src, "A struct{}") {
		t.Fatalf("Definitions(embedded type) = %+v, want the `type A` declaration", defs)
	}
	// A plain definition ident has no use at its span: UseAt must miss there
	// rather than drifting onto some enclosing occurrence.
	if _, _, ok := g.UseAt("emb.go", strings.Index(src, "A struct{}")); ok {
		t.Fatal("UseAt resolved a plain type-declaration ident; want no use")
	}
	// A plain use resolves identically through both.
	plain := strings.Index(src, "b.A }") + 2
	atKey, _, atOK := g.At("emb.go", plain)
	useOnlyKey, _, useOK := g.UseAt("emb.go", plain)
	if !atOK || !useOK || atKey != useOnlyKey {
		t.Fatalf("plain use: At=%q(%v) UseAt=%q(%v); want the same key", atKey, atOK, useOnlyKey, useOK)
	}
}
