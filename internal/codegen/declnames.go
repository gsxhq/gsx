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
// This set is the resolution input for lowercase tags in
// preprocessComponentCallSites.
func packageDeclNames(dir string, files map[string]*gsxast.File) map[string]bool {
	return packageDeclNamesFromFiles(parseHandwrittenGoFiles(dir), files)
}

func parseHandwrittenGoFiles(dir string) []*goast.File {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	files := make([]*goast.File, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".x.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr == nil && file != nil {
			files = append(files, file)
		}
	}
	return files
}

// packageDeclNamesFromFiles derives package declarations from a caller-owned
// companion syntax selection. Module semantic resolvers pass retained active
// CompiledGoFiles ASTs; the dir wrapper above preserves the standalone,
// build-oblivious syntactic behavior.
func packageDeclNamesFromFiles(goFiles []*goast.File, files map[string]*gsxast.File) map[string]bool {
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
	scanChunk := func(src string) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "", "package _gsxdecl\n"+src, 0)
		if err != nil || f == nil {
			return
		}
		collectGoDecls(f)
	}
	scanGoWithElements := func(g *gsxast.GoWithElements) {
		reconstructed, err := reconstructGoWithElements(g)
		if err != nil {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "", reconstructed.source, 0)
		if err != nil || f == nil {
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
				scanGoWithElements(t)
			}
		}
	}
	for _, file := range goFiles {
		collectGoDecls(file)
	}
	return out
}
