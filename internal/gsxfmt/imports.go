package gsxfmt

import (
	goformat "go/format"
	goparser "go/parser"
	"go/token"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"

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
	stripped, ok := stripSyntheticPackage([]byte(b.String()))
	if !ok {
		return src, false
	}
	return preserveTrailing(src, normalizeStripped(stripped)), true
}

// stripSyntheticPackage removes the synthetic goChunkPkg clause from formatted —
// the output of a Go formatter fed goChunkPkg+chunk. It locates the clause by
// PARSING, never by assuming it is the first line: go/printer relocates
// build-constraint comments (//go:build) above the package clause, and a
// line-index strip would delete the constraint and leave `package _gsxp` spliced
// into the user's source.
//
// ok is false when formatted does not parse; the caller then leaves the chunk
// untouched.
func stripSyntheticPackage(formatted []byte) (string, bool) {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", formatted, goparser.PackageClauseOnly|goparser.ParseComments)
	if err != nil {
		return "", false
	}
	start := fset.Position(file.Package).Offset  // offset of the `package` keyword
	end := fset.Position(file.Name.End()).Offset // end of the package name
	for end < len(formatted) && formatted[end] != '\n' {
		end++
	}
	if end < len(formatted) {
		end++ // consume the newline terminating the clause
	}
	return string(formatted[:start]) + string(formatted[end:]), true
}

// normalizeStripped closes up the blank-line gap that stripSyntheticPackage can
// leave where the package clause sat: the clause always had a blank line on
// each side in formatted output, so deleting just the clause line merges those
// into a double blank line (three consecutive newlines).
//
// This is done by mechanical string collapsing, NOT by another go/format pass.
// go/format.Source contains the identical "insert `package p`, then strip a
// fixed byte range" logic this file's whole fix removes (see internal.go's
// parse/sourceAdj) — verified: feeding it a fragment led by a //go:build
// comment reproduces Finding 1 inside the stdlib helper itself (the comment
// gets hoisted above the injected clause, and the fixed-offset strip leaks
// "package p" into the output). A real Go formatter never emits 3+ consecutive
// newlines on its own, so collapsing them here only ever touches the gap this
// function created.
func normalizeStripped(stripped string) string {
	for strings.Contains(stripped, "\n\n\n") {
		stripped = strings.ReplaceAll(stripped, "\n\n\n", "\n\n")
	}
	return stripped
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
	stripped, ok := stripSyntheticPackage(out)
	if !ok {
		return src, false
	}
	res := preserveTrailing(src, normalizeStripped(stripped))
	if res == src {
		return src, false
	}
	return res, true
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
