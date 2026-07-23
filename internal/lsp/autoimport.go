package lsp

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// SymbolKind is the LSP mirror of codegen.SymbolKind: a coarse classification of
// an unimported package's exported symbol, carried across the Analyzer seam by
// value so no *types.Object crosses the analysis lock. ciKind maps it to an LSP
// CompletionItemKind for the completion item.
type SymbolKind int

const (
	SymbolFunc SymbolKind = iota
	SymbolVar
	SymbolConst
	SymbolTypeStruct
	SymbolTypeInterface
	SymbolTypeOther
)

// ciKind maps a SymbolKind to its LSP CompletionItemKind. It mirrors
// goObjectPresentation's kind choices for the categories a package's top-level
// exported scope holds.
func (k SymbolKind) ciKind() int {
	switch k {
	case SymbolFunc:
		return ciKindFunction
	case SymbolVar:
		return ciKindVariable
	case SymbolConst:
		return ciKindConstant
	case SymbolTypeStruct:
		return ciKindStruct
	case SymbolTypeInterface:
		return ciKindInterface
	default:
		return ciKindClass
	}
}

// ImportSymbol is one exported symbol of an unimported package, offered as an
// auto-import completion candidate. Kind/Detail feed the item's icon and
// signature; the import path is supplied separately by the resolving caller
// (one package's symbols share one path).
type ImportSymbol struct {
	Name   string
	Kind   SymbolKind
	Detail string
}

// ImportablePackage is one package a file could import: its declared name (the
// qualifier the file would use) and its import path.
type ImportablePackage struct {
	Name string
	Path string
}

// isIdentByte reports whether b can appear in a Go identifier. Used to walk an
// authored-text qualifier backward from a member dot.
func isIdentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_':
		return true
	case b >= 0x80: // any multibyte rune byte: be permissive, go/types is the arbiter
		return true
	default:
		return false
	}
}

// qualifierBeforeDot returns the bare identifier immediately before the member
// dot at text[start-1], or ok=false when start-1 is not a dot or the receiver
// is not a SIMPLE identifier (a complex receiver like `foo().` or a chained
// `a.b.` is never an auto-import qualifier). nameEnd is the byte offset one past
// the qualifier's last byte (its last identifier byte + 1), the position the
// receiver's resolution is probed at.
//
// This is the text-level member-position test shared by expression and
// statement cursors: for `fmt.Sprin▮`, start is the `Sprin` token start and
// text[start-1] is the dot; for a trailing-dot cursor `fmt.▮`, start == the
// cursor and text[start-1] is the dot. Both resolve the same qualifier `fmt`.
func qualifierBeforeDot(text string, start int) (name string, nameEnd int, ok bool) {
	if start <= 0 || start > len(text) || text[start-1] != '.' {
		return "", 0, false
	}
	i := start - 2 // byte before the dot
	for i >= 0 && isGoSpaceByte(text[i]) {
		i--
	}
	end := i + 1 // one past the qualifier's last byte
	for i >= 0 && isIdentByte(text[i]) {
		i--
	}
	nameStart := i + 1
	if nameStart >= end {
		return "", 0, false // no identifier before the dot
	}
	// A leading digit means this is not an identifier (a numeric literal member
	// access is not a package qualifier).
	if c := text[nameStart]; c >= '0' && c <= '9' {
		return "", 0, false
	}
	// The receiver must be a bare qualifier: the byte before it may not continue
	// an expression (a dot, an identifier byte, or a closing bracket would make
	// it a field/chain/complex receiver, not an unimported package name).
	if nameStart > 0 {
		switch c := text[nameStart-1]; {
		case c == '.' || c == ')' || c == ']' || isIdentByte(c):
			return "", 0, false
		}
	}
	return text[nameStart:end], end, true
}

// undefinedQualifier reports whether the qualifier ending at byte nameEnd in
// path resolves to nothing in eph — the precedence gate for auto-import: an
// in-scope binding (a variable, a field) OR an imported package name both record
// an occurrence in the source index, so a hit means the ordinary member path
// already handles this receiver and auto-import must NOT fire. Only a total
// resolution miss (an unimported package, or a typo) is an auto-import
// candidate.
func undefinedQualifier(eph *Package, path string, nameEnd int) bool {
	if eph == nil || eph.SourceIndex == nil {
		return false
	}
	// The index keys occurrences by the identifier's last byte; nameEnd is one
	// past it.
	_, resolved := eph.SourceIndex.At(path, nameEnd-1)
	return !resolved
}

// importEditPrep is the buffer-level state a candidate import path's edit is
// built from: the parse and the import-chunk region depend only on text, not
// on which path is being added. prepareImportEdit computes it ONCE per
// completion request; apply then does the cheap per-path work
// (AddChunkImports + the prefix/suffix diff) for each candidate, so a request
// offering N unimported packages/paths re-parses the buffer once instead of N
// times.
type importEditPrep struct {
	text       string
	chunkStart int
	oldSrc     string
	enc        encoding
}

// prepareImportEdit parses text and locates its import-chunk region — see
// importEditFor's doc for the overall contract this feeds. ok=false means no
// candidate path in the caller's loop can produce an edit (the buffer does not
// parse, or there is no place to put imports), so the loop can be skipped
// entirely.
func prepareImportEdit(text string, enc encoding) (importEditPrep, bool) {
	fset := token.NewFileSet()
	file, err := gsxparser.ParseFile(fset, "buffer.gsx", text, 0)
	if file == nil || err != nil {
		return importEditPrep{}, false
	}
	chunkStart, oldSrc, ok := importChunkRegion(file, fset, text)
	if !ok {
		return importEditPrep{}, false
	}
	return importEditPrep{text: text, chunkStart: chunkStart, oldSrc: oldSrc, enc: enc}, true
}

// apply builds importPath's NARROW TextEdit against the state prepareImportEdit
// already computed, in ORIGINAL-document coordinates, for a completion item's
// AdditionalTextEdits. It reuses the gsxfmt import primitives (importTargetChunk
// + addChunkImports) but — unlike the whole-document code-action edit, which
// would illegally overlap the completion's own textEdit — emits only the minimal
// changed byte range (a common-prefix/suffix diff of the target chunk), which
// always lies at or above the import region and strictly before the cursor.
//
// ok=false means no edit is needed or possible: importPath is already imported
// (AddChunkImports is a no-op), or the computed edit would reach at or past
// cursorStart (a safety net against overlapping the completion edit — never
// expected, since the import region is above the cursor, but enforced rather
// than assumed).
func (p importEditPrep) apply(importPath string, cursorStart int) (TextEdit, bool) {
	newSrc, changed := gsxfmt.AddChunkImports(p.oldSrc, importPath)
	if !changed {
		return TextEdit{}, false
	}
	// AddChunkImports runs the chunk through gofmt, which normalizes away the
	// leading blank-line separator the chunk's Src carries (the gsx printer
	// re-adds it from a blank-before flag in the normal formatter flow — a step
	// this direct call bypasses). Restore the chunk's original leading whitespace
	// so the common-prefix diff below keeps the package-clause separator intact.
	newSrc = restoreLeadingWhitespace(p.oldSrc, newSrc)
	// Narrow to the minimal changed span: shared prefix/suffix bytes are
	// untouched, so the edit replaces only the differing middle.
	pfx := commonPrefixLen(p.oldSrc, newSrc)
	sfx := commonSuffixLen(p.oldSrc[pfx:], newSrc[pfx:])
	editStart := p.chunkStart + pfx
	editEnd := p.chunkStart + len(p.oldSrc) - sfx
	if editEnd > cursorStart {
		return TextEdit{}, false // would overlap the completion edit; refuse
	}
	return TextEdit{
		Range:   rangeForSpan(p.text, editStart, editEnd, p.enc),
		NewText: newSrc[pfx : len(newSrc)-sfx],
	}, true
}

// importEditFor is the single-path convenience wrapper over
// prepareImportEdit+apply: parse once, apply once. Candidate LOOPS (Option
// 1's per-path paths, Option 2's per-package paths) call prepareImportEdit
// once and apply per candidate instead, so the parse is not repeated.
func importEditFor(text, importPath string, cursorStart int, enc encoding) (TextEdit, bool) {
	p, ok := prepareImportEdit(text, enc)
	if !ok {
		return TextEdit{}, false
	}
	return p.apply(importPath, cursorStart)
}

// importChunkRegion locates the byte region of text that holds (or should hold)
// the file's imports, returning its start offset and current source. It mirrors
// gsxfmt.importTargetChunk's preference — the leading chunk that already
// declares imports, else the first GoChunk — but for the no-GoChunk case
// (`package x` directly followed by an element decl, no Go region at all) it
// targets the whitespace between the package clause and the first declaration,
// so an inserted import block lands exactly where a synthesized chunk would
// print.
func importChunkRegion(file *gsxast.File, fset *token.FileSet, text string) (start int, src string, ok bool) {
	var first *gsxast.GoChunk
	for _, d := range file.Decls {
		gc, isChunk := d.(*gsxast.GoChunk)
		if !isChunk {
			continue
		}
		if gsxfmt.ChunkHasImports(gc.Src) {
			s := fset.Position(gc.Pos()).Offset
			return s, gc.Src, true
		}
		if first == nil {
			first = gc
		}
	}
	if first != nil {
		return fset.Position(first.Pos()).Offset, first.Src, true
	}
	// No Go chunk: insert after the package clause. The whitespace between the
	// package clause and the first decl is the region a synthesized import chunk
	// occupies; replacing it preserves the author's blank-line spacing.
	if len(file.Decls) == 0 {
		return 0, "", false
	}
	declStart := fset.Position(file.Decls[0].Pos()).Offset
	ws := declStart
	for ws > 0 && isGoSpaceByte(text[ws-1]) {
		ws--
	}
	if ws == 0 {
		return 0, "", false // no package clause preceding — refuse rather than guess
	}
	return ws, text[ws:declStart], true
}

// restoreLeadingWhitespace re-prepends old's leading whitespace run to new when
// gofmt stripped it (new no longer starts with old's separator). Idempotent when
// new already carries the separator.
func restoreLeadingWhitespace(old, new string) string {
	lead := old[:len(old)-len(strings.TrimLeft(old, " \t\n"))]
	if lead == "" || strings.HasPrefix(new, lead) {
		return new
	}
	return lead + strings.TrimLeft(new, " \t\n")
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func commonSuffixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// unimportedQualifierItems is Option 1: an undefined qualifier in member
// position (`fmt.▮`, `strings.ToUp▮`) resolves to candidate import path(s), and
// each path's exported symbols become completion items whose accept inserts both
// the symbol (the TextEdit over [start,end)) and the import (AdditionalTextEdits).
//
// The resolve is symbol-less — the qualifier alone — matching how member
// completion offers a package's whole exported scope and lets the client filter
// by the typed prefix. A qualifier that maps to several packages (an ambiguous
// name) yields one item per surviving symbol per path, each labelled with its
// own path (§5 of the design): honest, no relevance guessing. A candidate whose
// import edit cannot be synthesized (already imported, unparseable buffer) is
// skipped rather than offered without its import.
func (s *Server) unimportedQualifierItems(dir, path, text, qualifier string, start, end int) []CompletionItem {
	paths := s.analyzer.ResolveImport(dir, qualifier, "")
	if len(paths) == 0 {
		return nil
	}
	// Hoisted: the parse + import-chunk lookup don't depend on which path is
	// being added, so they run ONCE for the whole candidate loop below instead
	// of once per path.
	prep, ok := prepareImportEdit(text, s.enc)
	if !ok {
		return nil
	}
	var items []CompletionItem
	for _, importPath := range paths {
		edit, ok := prep.apply(importPath, start)
		if !ok {
			continue
		}
		for _, sym := range s.analyzer.ExportedSymbols(dir, importPath) {
			if isReservedGsxInternal(sym.Name) {
				continue
			}
			item := newCompletionItem(text, start, end, s.enc, sym.Name, sym.Name, sym.Kind.ciKind(), tierImported, sym.Detail, nil)
			item.AdditionalTextEdits = []TextEdit{edit}
			s.applyImportPathLabel(&item, sym.Detail, importPath)
			items = append(items, item)
		}
	}
	return items
}

// packageNameItems is Option 2: at a bare-identifier position, unimported
// package names whose name has the typed prefix, each accepting to insert the
// package identifier plus its import (AdditionalTextEdits). Ranked at
// tierUnimported — strictly below every in-scope name, member, and keyword.
//
// A prefix is required: flooding an empty cursor with ~1000 package names is
// pure noise, so package names appear only once the user commits at least one
// character (the design's "cap noise" — a server-side narrowing that is safe
// regardless of whether the client also filters). The path a candidate would
// import is shown as its detail/labelDetails.
func (s *Server) packageNameItems(dir, text, prefix string, start, end int) []CompletionItem {
	if prefix == "" {
		return nil
	}
	// Hoisted: the parse + import-chunk lookup don't depend on which package is
	// being added, so they run ONCE for the whole candidate loop below instead
	// of once per package (the loop can range over ~1000 std packages before
	// the prefix filter narrows it).
	prep, ok := prepareImportEdit(text, s.enc)
	if !ok {
		return nil
	}
	var items []CompletionItem
	for _, pkg := range s.analyzer.ImportablePackages(dir) {
		if !strings.HasPrefix(pkg.Name, prefix) {
			continue
		}
		edit, ok := prep.apply(pkg.Path, start)
		if !ok {
			continue
		}
		item := newCompletionItem(text, start, end, s.enc, pkg.Name, pkg.Name, ciKindModule, tierUnimported, pkg.Path, nil)
		item.AdditionalTextEdits = []TextEdit{edit}
		s.applyImportPathLabel(&item, pkg.Path, pkg.Path)
		items = append(items, item)
	}
	return items
}

// applyImportPathLabel places the import path on an auto-import item. A client
// that renders labelDetails gets the path in labelDetails.description (with the
// type signature kept in detail); one that does not falls back to the plain
// detail string carrying the path — the path being the disambiguator worth the
// single slot.
func (s *Server) applyImportPathLabel(item *CompletionItem, typeSig, importPath string) {
	if s.labelDetailsSupport {
		item.Detail = typeSig
		item.LabelDetails = &CompletionItemLabelDetails{Description: importPath}
		return
	}
	item.Detail = importPath
}
