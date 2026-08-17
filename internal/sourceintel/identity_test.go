package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestIdentitySourceMapMapsGoFileOntoItself(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc use(t T) {}\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "helper.go", src, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	if _, err := new(types.Config).Check("example.com/p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	sm, err := IdentitySourceMap("helper.go", len(src))
	if err != nil {
		t.Fatal(err)
	}
	mapped := MappedFile{AST: file, TokenFile: fset.File(file.Pos()), SourceMap: sm,
		SourceVersion: SourceVersion{Size: len(src), SHA256: sha256.Sum256([]byte(src))}}
	index := BuildIndex(info, []MappedFile{mapped})

	useT := strings.LastIndex(src, "T)")
	occ, ok := index.At("helper.go", useT)
	if !ok || occ.Kind != IdentifierUse || occ.Object == nil || occ.Object.Name() != "T" {
		t.Fatalf("At(use T) = %+v, %v", occ, ok)
	}
	def, ok := index.Definition(occ.Object)
	want := Span{Path: "helper.go", Start: strings.Index(src, "T struct"), End: strings.Index(src, "T struct") + 1}
	if !ok || def != want {
		t.Fatalf("Definition = %+v, %v; want %+v", def, ok, want)
	}
	if !index.MatchesSource("helper.go", []byte(src)) {
		t.Fatal("MatchesSource must accept the identity-mapped bytes")
	}
	decls := index.Declarations("helper.go")
	if len(decls) != 2 {
		t.Fatalf("Declarations = %d, want 2 (T, use)", len(decls))
	}
}
