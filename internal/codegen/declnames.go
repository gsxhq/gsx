package codegen

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// packageDeclNames returns the set of package-level declared bare names for
// the package in dir: top-level func (methods excluded), var, type, and const
// names from hand-written .go files plus the package's .gsx files (receiver-
// less component decls, and decls inside GoChunk / GoWithElements Go source).
// Import names are never counted (imports are file-scoped, not declarations).
// Syntax-only (go/parser), build-tag-oblivious, skips _test.go and .x.go —
// same file-walk rules as packageTypeNames/packageNullaryFuncs (byo.go).
// This set is the resolution input for lowercase tags: see resolveComponentTags.
func packageDeclNames(dir string, files map[string]*gsxast.File) map[string]bool {
	out := map[string]bool{}
	collectGoDecls := func(f *goast.File) {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *goast.FuncDecl:
				if d.Recv == nil && d.Name != nil {
					out[d.Name.Name] = true
				}
			case *goast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *goast.TypeSpec:
						out[s.Name.Name] = true
					case *goast.ValueSpec: // var + const
						for _, n := range s.Names {
							out[n.Name] = true
						}
					}
				}
			}
		}
	}
	if dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			fset := token.NewFileSet()
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") ||
					strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".x.go") {
					continue
				}
				f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
				if perr != nil || f == nil {
					continue
				}
				collectGoDecls(f)
			}
		}
	}
	scanChunk := func(src string) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "", "package _gsxp\n"+src, 0)
		if f == nil && err != nil {
			return
		}
		collectGoDecls(f)
	}
	for _, file := range files {
		for _, d := range file.Decls {
			switch t := d.(type) {
			case *gsxast.Component:
				if t.Recv == "" {
					out[t.Name] = true
				}
			case *gsxast.GoChunk:
				scanChunk(t.Src)
			case *gsxast.GoWithElements:
				// Reconstruct parseable Go: element parts become `nil`
				// placeholders (offsets don't matter — names only).
				var b strings.Builder
				for _, p := range t.Parts {
					if gt, ok := p.(gsxast.GoText); ok {
						b.WriteString(gt.Src)
					} else {
						b.WriteString("nil")
					}
				}
				scanChunk(b.String())
			}
		}
	}
	return out
}
