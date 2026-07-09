package gsxfmt

import (
	goformat "go/format"
	goparser "go/parser"
	"go/token"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/printer"
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
	out := make([]gsxast.Decl, 0, len(f.Decls))
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			out = append(out, d)
			continue
		}
		if rewritten, changed := deleteChunkImports(gc.Src, unused); changed {
			gc.Src = rewritten
			if strings.TrimSpace(gc.Src) == "" {
				continue // entirely-imports chunk, now empty → drop
			}
		}
		out = append(out, gc)
	}
	f.Decls = out
}

// deleteChunkImports parses one Go chunk (wrapped with a synthetic package
// clause), deletes the named imports via astutil, and reprints. Returns the
// rewritten chunk and whether anything changed. The chunk is gofmt'd here; the
// gsx printer also gofmt's chunks on output, so the result is stable.
func deleteChunkImports(src string, unused []ImportRef) (string, bool) {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", goChunkPkg+src, goparser.ParseComments)
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
	stripped, ok := printer.StripSyntheticPackage([]byte(b.String()))
	if !ok {
		return src, false
	}
	return preserveTrailing(src, stripped), true
}

// preserveTrailing reattaches orig's trailing whitespace to a rewritten chunk
// body. A GoChunk's trailing newlines are a layout fact, not slack: the printer
// reads them (endsWithBlankLine) to decide whether a blank line separates this
// chunk from the next declaration. TrimSpace-ing them away silently collapses
// that blank line.
func preserveTrailing(orig, body string) string {
	trimmed := strings.TrimRight(orig, " \t\n")
	return strings.TrimSpace(body) + orig[len(trimmed):]
}

// goChunkPkg is the synthetic package clause prepended to a GoChunk so that the
// chunk — which carries no package declaration of its own — parses as a
// standalone Go file. It is stripped from every reprinted result.
const goChunkPkg = "package _gsxp\n"

// chunkHasImports reports whether src declares at least one import. The decision
// is made on the parsed AST, never on a substring of the text: the word `import`
// can appear inside a string literal or a comment. A chunk that is not
// standalone-valid Go reports false, so it is left untouched downstream.
//
// goparser.ImportsOnly stops the parse after the import block, which is all this
// gate needs and keeps the common (import-free) chunk cheap.
func chunkHasImports(src string) bool {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", goChunkPkg+src, goparser.ImportsOnly)
	if err != nil {
		return false
	}
	return len(file.Imports) > 0
}

// reorderChunkImports runs goimports' formatter over one Go chunk: it merges
// every import declaration into a single block, drops duplicate specs, splits
// standard-library from third-party imports with a blank line, and sorts each
// group. Returns the rewritten chunk and whether anything changed.
//
// FormatOnly is essential. Without it goimports would also ADD and REMOVE
// imports based on what the chunk body references — and a gsx chunk body never
// references the template's imports, so plain goimports would strip every one of
// them. Unused-import removal is a separate, module-analysis-driven pass
// (removeImports); adding imports is impossible for gsx (a chunk body cannot
// tell us which package a template's identifier came from).
//
// Comments/TabIndent/TabWidth make FormatOnly's output match gofmt's tabbed
// chunk formatting; without them it emits spaces and the printer's own gofmt
// would fight it.
func reorderChunkImports(src string) (string, bool) {
	if !chunkHasImports(src) {
		return src, false
	}
	out, err := imports.Process("chunk.go", []byte(goChunkPkg+src), &imports.Options{
		FormatOnly: true,
		Comments:   true,
		TabIndent:  true,
		TabWidth:   8,
	})
	if err != nil {
		return src, false // not standalone-valid Go; leave it
	}
	stripped, ok := printer.StripSyntheticPackage(out)
	if !ok {
		return src, false
	}
	res := preserveTrailing(src, stripped)
	if res == src {
		return src, false
	}
	return res, true
}

// addImports inserts each ref into the file's import block, creating one when the
// file has none. Insertion is delegated to astutil.AddNamedImport (the same
// package that supplies DeleteNamedImport for removal): it places the spec in the
// right existing group, creates the declaration when there is none, and is a
// no-op when the path is already imported — so duplicates cost nothing.
//
// astutil will put a third-party import into a std-only block without opening a
// new group. That is fine: reorderImports (goimports FormatOnly) runs afterwards
// and splits std from everything else.
func addImports(f *gsxast.File, add []ImportRef) {
	if len(add) == 0 {
		return
	}
	gc := importTargetChunk(f)
	if gc == nil {
		return // no chunk could be created; leave the file alone
	}
	src, ok := addChunkImports(gc.Src, add)
	if ok {
		gc.Src = src
	}
}

// importTargetChunk returns the GoChunk that should hold the file's imports,
// creating one if necessary.
//
// Preference: the leading chunk that already declares imports, else the first
// GoChunk, else a fresh empty chunk inserted at Decls[0]. A GoWithElements or
// Component is never a target — astutil parses Go, and neither is standalone-valid
// Go. That last case is real: `package main` followed only by
// `var xx = <p>hi</p>` has no GoChunk at all.
func importTargetChunk(f *gsxast.File) *gsxast.GoChunk {
	var first *gsxast.GoChunk
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		if chunkHasImports(gc.Src) {
			return gc
		}
		if first == nil {
			first = gc
		}
	}
	if first != nil {
		return first
	}
	// A brand-new chunk has no author-written trailing whitespace for
	// preserveTrailing (in addChunkImports) to carry forward — Src is about to
	// go from "" to an import block, with nothing upstream to copy a separator
	// from. Every OTHER GoChunk's Src encodes "a blank line follows" as a
	// trailing "\n\n" (see endsWithBlankLine); a synthesized chunk must encode
	// the same default (file's own per-decl default is `blank := true`) or the
	// printer reads its bare, separator-less Src as "author wrote no gap" and
	// glues it to the next decl. Seeding Src with "\n\n" up front gives
	// preserveTrailing a real trailing run to preserve, so the chunk ends up
	// with the same blank-line marker any authored import block would have.
	gc := &gsxast.GoChunk{Src: "\n\n"}
	f.Decls = append([]gsxast.Decl{gc}, f.Decls...)
	return gc
}

// addChunkImports wraps one chunk in the synthetic package clause, runs
// astutil.AddNamedImport per ref, and reprints. Returns the rewritten chunk and
// whether anything changed.
//
// The clause is removed by PARSING (printer.StripSyntheticPackage), never by line
// index: go/printer hoists a //go:build comment above the clause, and a
// line-index strip would shear the constraint and splice `package _gsxp` into the
// user's source.
func addChunkImports(src string, add []ImportRef) (string, bool) {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", goChunkPkg+src, goparser.ParseComments)
	if err != nil {
		return src, false // not standalone-valid Go; leave it
	}
	changed := false
	for _, r := range add {
		if astutil.AddNamedImport(fset, file, r.Name, r.Path) {
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
	stripped, ok := printer.StripSyntheticPackage([]byte(b.String()))
	if !ok {
		return src, false
	}
	return preserveTrailing(src, stripped), true
}

// reorderImports rewrites the imports of every GoChunk in f, in place, to
// goimports' canonical form. Non-GoChunk decls are skipped: imports never live
// in a GoWithElements region, because the parser peels a leading import run into
// its own plain GoChunk before building the element region.
//
// Unlike removeImports this can never empty a chunk (FormatOnly deletes no
// specs), so no decl is dropped here.
func reorderImports(f *gsxast.File) {
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		if out, changed := reorderChunkImports(gc.Src); changed {
			gc.Src = out
		}
	}
}
