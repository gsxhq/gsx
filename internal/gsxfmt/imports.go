package gsxfmt

import (
	goformat "go/format"
	goparser "go/parser"
	"go/token"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	gsxast "github.com/gsxhq/gsx/ast"
)

// ImportRef identifies an import to remove from a .gsx file's pass-through Go
// chunk. Name is "" for a default import (e.g. `import "strings"`), or the
// explicit alias for an aliased import (e.g. `import sx "strings"`).
type ImportRef struct {
	Name string
	Path string
}

// removeImports drops every import named in `unused` from the file's GoChunks,
// in place. A chunk that does not parse on its own, or holds none of the unused
// imports, is left untouched. GoChunks that become empty after import removal
// are removed from the decl list entirely.
func removeImports(f *gsxast.File, unused []ImportRef) {
	if len(unused) == 0 {
		return
	}
	out := f.Decls[:0]
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			out = append(out, d)
			continue
		}
		if rewritten, changed := deleteChunkImports(gc.Src, unused); changed {
			gc.Src = rewritten
		}
		if strings.TrimSpace(gc.Src) != "" {
			out = append(out, gc)
		}
	}
	f.Decls = out
}

// deleteChunkImports parses one Go chunk (wrapped with a synthetic package
// clause), deletes the named imports via astutil, and reprints. Returns the
// rewritten chunk and whether anything changed. The chunk is gofmt'd here; the
// gsx printer also gofmt's chunks on output, so the result is stable.
func deleteChunkImports(src string, unused []ImportRef) (string, bool) {
	const pkg = "package _gsxp\n"
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", pkg+src, goparser.ParseComments)
	if err != nil {
		return src, false // not standalone-valid Go; leave it
	}
	changed := false
	for _, u := range unused {
		if astutil.DeleteNamedImport(fset, file, u.Name, u.Path) {
			changed = true
		}
	}
	if !changed {
		return src, false
	}
	var b strings.Builder
	if err := goformat.Node(&b, fset, file); err != nil {
		return src, false
	}
	out := b.String()
	// Drop the synthetic "package _gsxp" line we prepended.
	if nl := strings.IndexByte(out, '\n'); nl >= 0 {
		out = out[nl+1:]
	}
	return strings.TrimSpace(out), true
}
