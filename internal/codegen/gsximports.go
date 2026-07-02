package codegen

import (
	"go/token"
	"os"
	"path/filepath"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// GsxHoistedImportPaths returns the import paths hoisted from the GoChunks of
// every .gsx file in dir (disk content only — no Module overrides). It exists
// for gen's incremental cache key: an importer's generated output depends on
// its dep's .gsx-declared component props, but `go list` cannot see a dep
// whose only edge is a .gsx import with no .x.go on disk yet. Unparseable
// files are skipped: a .gsx that cannot parse cannot generate output, so a
// missed edge cannot serve stale output for it.
func GsxHoistedImportPaths(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	fset := token.NewFileSet()
	var out []string
	for _, p := range matches {
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, perrs := gsxparser.ParseFileWithClassifier(fset, p, src, 0, nil)
		if len(perrs) > 0 {
			continue
		}
		for _, d := range f.Decls {
			gc, ok := d.(*gsxast.GoChunk)
			if !ok {
				continue
			}
			imps, _, _, err := splitChunk(gc.Src)
			if err != nil {
				continue
			}
			for _, s := range imps {
				out = append(out, s.path)
			}
		}
	}
	return out
}
