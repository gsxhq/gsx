package lsp

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// navSpan is one navigable Go-fragment byte span of a gsx node: pos is the
// first byte of the (trimmed) fragment text in the .gsx, ln its byte length.
type navSpan struct {
	pos token.Pos
	ln  int
}

// nodeNavSpans returns a node's navigable Go-fragment spans plus its pipeline
// stages (whose name/args regions are matched separately). Every span's source
// bytes spell exactly the stored fragment text — the parser's byte-faithful
// invariant — so cursors bridge into the skeleton by relative offset. A node
// can carry more than one span (a ClassPart's expr and its `: cond` guard);
// spans never overlap across nodes, so at most one (node, span) matches an
// offset. Nodes with no navigable fragment return nil.
func nodeNavSpans(n gsxast.Node) (spans []navSpan, stages []gsxast.PipeStage) {
	switch e := n.(type) {
	case *gsxast.Interp:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.ExprAttr:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.SpreadAttr:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.OrderedPair:
		return []navSpan{{e.Pos(), len(e.Value)}}, nil
	case *gsxast.ClassPart:
		if e.CF != nil {
			return nil, nil // the CF's own nodes carry the spans
		}
		if e.CSSSegments == nil {
			spans = append(spans, navSpan{e.ExprPos, len(e.Expr)})
			stages = e.Stages
		}
		if e.Cond != "" {
			spans = append(spans, navSpan{e.CondPos, len(e.Cond)})
		}
		return spans, stages
	case *gsxast.ValueArm:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.ValueIf:
		return []navSpan{{e.CondPos, len(e.Cond)}}, nil
	case *gsxast.ValueSwitch:
		return []navSpan{{e.TagPos, len(e.Tag)}}, nil
	case *gsxast.ValueSwitchCase:
		return []navSpan{{e.ListPos, len(e.List)}}, nil
	case *gsxast.CondAttr:
		return []navSpan{{e.CondPos, len(e.Cond)}}, nil
	case *gsxast.SwitchMarkup:
		return []navSpan{{e.TagPos, len(e.Tag)}}, nil
	case *gsxast.CaseClause:
		return []navSpan{{e.ListPos, len(e.List)}}, nil
	case *gsxast.ForMarkup:
		return []navSpan{{e.ClausePos, len(e.Clause)}}, nil
	case *gsxast.IfMarkup:
		return []navSpan{{e.CondPos, len(e.Cond)}}, nil
	case *gsxast.GoBlock:
		return []navSpan{{e.CodePos, len(e.Code)}}, nil
	}
	return nil, nil
}

// exprNodeAtOffset returns the gsx node one of whose Go-fragment spans (see
// nodeNavSpans) contains the byte offset, together with the matched span's
// start position (so the caller can both bridge by relative offset and tell
// WHICH fragment of a multi-span node matched). Returns (nil, token.NoPos)
// when no node covers the offset. Fragment spans never nest across nodes, so
// at most one (node, span) matches — the walk's last-write-wins is unambiguous.
func exprNodeAtOffset(pkg *Package, path string, off int) (gsxast.Node, token.Pos) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return nil, token.NoPos
	}
	var found gsxast.Node
	var foundPos token.Pos
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if n == nil {
			return false
		}
		spans, stages := nodeNavSpans(n)
		if len(spans) == 0 {
			return true
		}
		for _, s := range spans {
			if !s.pos.IsValid() {
				continue
			}
			start := pkg.GSXFset.Position(s.pos).Offset
			if off >= start && off < start+s.ln {
				found = n
				foundPos = s.pos
				return true
			}
		}
		// Also match pipeline stage positions (filter name, filter args) so that
		// pipedTarget can be dispatched for those cursor positions too. The
		// reported position is the primary (seed) span's start, matching what
		// pipedTarget expects.
		if !spans[0].pos.IsValid() {
			return true
		}
		for _, st := range stages {
			if st.NamePos.IsValid() {
				nameStart := pkg.GSXFset.Position(st.NamePos).Offset
				if off >= nameStart && off < nameStart+len(st.Name) {
					found = n
					foundPos = spans[0].pos
					return true
				}
			}
			if st.HasArgs && st.ArgsPos.IsValid() {
				argsStart := pkg.GSXFset.Position(st.ArgsPos).Offset
				if off >= argsStart && off < argsStart+len(st.Args) {
					found = n
					foundPos = spans[0].pos
					return true
				}
			}
		}
		return true
	})
	return found, foundPos
}

// signatureTypeIdentAt resolves a cursor sitting on an identifier inside a
// component-signature TYPE — a parameter type (e.g. `store.Comment` in
// `component C(c []store.Comment)`) or a method receiver type — to that
// identifier's go/types object. It walks the file's components, finds the
// signature-type span covering off (via pkg.SigTypes), bridges the cursor into
// the type-checked skeleton type expression by relative byte offset (the
// skeleton copies the type verbatim, so the bytes align), and resolves the
// innermost identifier through go/types. gsxStart/idLen are the identifier's
// byte span back in the .gsx file (for the hover range). Returns ok=false when
// the cursor is not on a signature-type identifier or it does not resolve.
func signatureTypeIdentAt(pkg *Package, path string, off int) (obj types.Object, gsxStart, idLen int, ok bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil || pkg.Info == nil || pkg.SigTypes == nil {
		return nil, 0, 0, false
	}
	for _, d := range f.Decls {
		c, isComp := d.(*gsxast.Component)
		if !isComp {
			continue
		}
		for _, r := range pkg.SigTypes[c] {
			start := pkg.GSXFset.Position(r.GSXPos).Offset
			if off < start || off >= start+r.Len {
				continue
			}
			skelPos := r.SkelTyp.Pos() + token.Pos(off-start)
			id := innermostIdent(r.SkelTyp, skelPos)
			if id == nil {
				return nil, 0, 0, false
			}
			o := pkg.Info.Uses[id]
			if o == nil {
				o = pkg.Info.Defs[id]
			}
			if o == nil {
				return nil, 0, 0, false
			}
			// The identifier's .gsx span: its offset within the (verbatim) skeleton
			// type equals its offset within the .gsx type, so add it to the type start.
			gs := start + int(id.Pos()-r.SkelTyp.Pos())
			return o, gs, len(id.Name), true
		}
	}
	return nil, 0, 0, false
}

// signatureTypeDefinition builds the textDocument/definition reply for an
// identifier resolved inside a component-signature parameter type. A package
// qualifier (a *types.PkgName) jumps into the imported package — a list of the
// `package` clauses of its files, like gopls — rather than back to the import
// site. Any other object jumps to its single declaration. Returns nil (→ null)
// when there is no real source target.
func (s *Server) signatureTypeDefinition(pkg *Package, obj types.Object) any {
	if pn, ok := obj.(*types.PkgName); ok {
		if locs := s.packageLocations(pn.Imported(), pkg.Fset); len(locs) > 0 {
			return locs
		}
		return nil
	}
	if !obj.Pos().IsValid() {
		return nil
	}
	dp := pkg.Fset.Position(obj.Pos())
	if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
		return nil
	}
	return s.locationForPos(dp)
}

// packageLocations returns the `package` clause location of every file in imp
// that declares a package-level object — gopls's answer for go-to-definition on
// a package name. Files are discovered from the package scope's objects (so a
// file declaring nothing package-level is not listed) and sorted for stable
// output. Returns nil when imp is nil or no source files can be located (e.g. a
// dependency available only as export data without file positions).
func (s *Server) packageLocations(imp *types.Package, fset *token.FileSet) []Location {
	if imp == nil || fset == nil {
		return nil
	}
	files := map[string]bool{}
	scope := imp.Scope()
	for _, name := range scope.Names() {
		o := scope.Lookup(name)
		if o == nil || !o.Pos().IsValid() {
			continue
		}
		fn := fset.Position(o.Pos()).Filename
		if strings.HasSuffix(fn, ".go") && !strings.HasSuffix(fn, ".x.go") {
			files[fn] = true
		}
	}
	sorted := make([]string, 0, len(files))
	for fn := range files {
		sorted = append(sorted, fn)
	}
	sort.Strings(sorted)
	var locs []Location
	for _, fn := range sorted {
		if loc, ok := packageClauseLocation(fn, s.enc); ok {
			locs = append(locs, loc)
		}
	}
	return locs
}

// packageClauseLocation returns the location of the package-name identifier in
// the `package X` clause of the Go file at filename (what go-to-definition on a
// package qualifier should land on). Returns ok=false if the file cannot be read
// or parsed.
func packageClauseLocation(filename string, enc encoding) (Location, bool) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Location{}, false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, data, parser.PackageClauseOnly)
	if err != nil || f.Name == nil {
		return Location{}, false
	}
	p := fset.Position(f.Name.Pos())
	line := max(p.Line-1, 0)
	char := charForByteCol(lineAtFunc(string(data))(p.Line), p.Column, enc)
	pos := Position{Line: line, Character: char}
	return Location{URI: pathToURI(filename), Range: Range{Start: pos, End: pos}}, true
}

// importDefAt answers go-to-definition for a cursor on an import statement in a
// .gsx file (the package path or its alias): it jumps into the imported package,
// returning the `package` clauses of its files — the same picker as a package
// qualifier in a parameter type. The second result reports whether the cursor
// was on an import spec at all (so the caller stops dispatching even when the
// import does not resolve). gsx imports live in top-level GoChunks, so the
// chunk under the cursor is re-parsed for its import specs.
func (s *Server) importDefAt(pkg *Package, path string, off int) (any, bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil || pkg.Types == nil {
		return nil, false
	}
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		start := pkg.GSXFset.Position(gc.Pos()).Offset
		if off < start || off >= start+len(gc.Src) {
			continue
		}
		impPath, found := importPathAtOffset(gc.Src, off-start)
		if !found {
			return nil, false
		}
		if tpkg := importedPackageByPath(pkg.Types, impPath); tpkg != nil {
			if locs := s.packageLocations(tpkg, pkg.Fset); len(locs) > 0 {
				return locs, true
			}
		}
		return nil, true // on an import spec, but it did not resolve to source
	}
	return nil, false
}

// importPathAtOffset re-parses a GoChunk's source (the verbatim Go after the
// .gsx package line) and returns the import path of the import spec covering the
// byte offset relOff within that source — matching either the path string or the
// alias. ok is false if relOff is not on an import spec or the chunk's imports
// do not parse.
func importPathAtOffset(src string, relOff int) (string, bool) {
	const prefix = "package _\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src, parser.ImportsOnly)
	if err != nil {
		return "", false
	}
	target := relOff + len(prefix)
	for _, imp := range f.Imports {
		lo := fset.Position(imp.Pos()).Offset
		hi := fset.Position(imp.End()).Offset
		if target >= lo && target < hi {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				return "", false
			}
			return p, true
		}
	}
	return "", false
}

// importedPackageByPath returns the direct import of p whose path is importPath,
// or nil. Used to resolve a .gsx import spec to the analyzed package's
// already-type-checked dependency.
func importedPackageByPath(p *types.Package, importPath string) *types.Package {
	for _, imp := range p.Imports() {
		if imp.Path() == importPath {
			return imp
		}
	}
	return nil
}

// hasPipeStages reports whether the gsx expression node carries pipeline stages
// (`{ x |> f }`). Such nodes lower to a wrapped call in the skeleton, breaking
// the byte-identical relative-offset bridge go-to-def relies on — they resolve
// through pipedTarget instead.
func hasPipeStages(n gsxast.Node) bool {
	switch e := n.(type) {
	case *gsxast.Interp:
		return len(e.Stages) > 0
	case *gsxast.ExprAttr:
		return len(e.Stages) > 0
	case *gsxast.SpreadAttr:
		return len(e.Stages) > 0
	case *gsxast.ClassPart:
		return len(e.Stages) > 0
	case *gsxast.ValueArm:
		return len(e.Stages) > 0
	}
	return false
}

// isCtrlSpan reports whether the matched span (see exprNodeAtOffset) resolves
// through the CtrlMap bridge — a control-flow clause emitted verbatim in
// statement position — rather than the ExprMap expression bridge. For a
// ClassPart the two coexist: its `: cond` guard is a ctrl span while its expr
// is an ExprMap span, so the matched position discriminates.
func isCtrlSpan(node gsxast.Node, matched token.Pos) bool {
	switch e := node.(type) {
	case *gsxast.ForMarkup, *gsxast.IfMarkup, *gsxast.GoBlock,
		*gsxast.ValueIf, *gsxast.ValueSwitch, *gsxast.ValueSwitchCase,
		*gsxast.CondAttr, *gsxast.SwitchMarkup, *gsxast.CaseClause:
		return true
	case *gsxast.ClassPart:
		return e.CondPos.IsValid() && matched == e.CondPos
	}
	return false
}

// handleDefinition answers textDocument/definition for D1: a Go symbol under the
// cursor that resolves to a definition in a real .go file.
func (s *Server) handleDefinition(f frame) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	if strings.HasSuffix(path, ".go") {
		return s.handleGoDefinition(f, uri, path)
	}
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || pkg.Info == nil {
		return s.reply(f.ID, nil)
	}

	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	// D2: cursor on a component tag name in a .gsx file → jump to the component
	// declaration(s). A single variant replies with a plain Location (unchanged
	// wire shape); multiple build-tag variants (Task 7) reply with a []Location
	// so the editor shows a picker — both are valid textDocument/definition results.
	if decls, ok := componentTagDeclAt(pkg, path, off); ok {
		if len(decls) == 1 {
			return s.reply(f.ID, s.locationForPos(decls[0]))
		}
		locs := make([]Location, 0, len(decls))
		for _, d := range decls {
			locs = append(locs, s.locationForPos(d))
		}
		return s.reply(f.ID, locs)
	}

	// B: cursor on a dotted/cross-package component tag → its declaration in the
	// imported package's .gsx.
	if dp, ok := crossPkgTagDeclAt(pkg, path, off); ok {
		return s.reply(f.ID, s.locationForPos(dp))
	}

	// A/C: cursor on a component-invocation attribute name → the matching component
	// parameter (same-package function components and cross-package dotted tags).
	if dp, ok := componentAttrParamAt(pkg, path, off); ok {
		return s.reply(f.ID, s.locationForPos(dp))
	}

	// E: cursor on an identifier inside a component-signature parameter TYPE
	// (e.g. `store.Comment` in `component C(c []store.Comment)`) → the Go
	// definition of that identifier: a type name jumps to its declaration; a
	// package qualifier jumps into the package (its files' `package` clauses,
	// gopls-style). A cursor on a signature type that does not resolve replies
	// null rather than falling through to expression resolution.
	if obj, _, _, ok := signatureTypeIdentAt(pkg, path, off); ok {
		return s.reply(f.ID, s.signatureTypeDefinition(pkg, obj))
	}

	// F: cursor on an import statement in the .gsx → into the imported package
	// (its files' `package` clauses), the same picker as a type qualifier.
	if res, ok := s.importDefAt(pkg, path, off); ok {
		return s.reply(f.ID, res)
	}

	if dp, ok := exprDefinitionAt(pkg, path, off); ok {
		return s.reply(f.ID, s.locationForPos(dp))
	}
	return s.reply(f.ID, nil)
}

// exprDefinitionAt answers go-to-definition for a cursor inside any Go-fragment
// span of a .gsx file (see nodeNavSpans): ctrl spans resolve through CtrlMap,
// pipelined expressions through pipedTarget, and plain expressions through the
// ExprMap byte-identical bridge. Ctrl spans are checked first: a ClassPart's
// `: cond` guard must resolve via CtrlMap even when the part's EXPR carries a
// pipeline. ok=false when no span covers the offset or nothing resolves to a
// real source location.
func exprDefinitionAt(pkg *Package, path string, off int) (token.Position, bool) {
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		return token.Position{}, false
	}
	if isCtrlSpan(node, exprPos) {
		return ctrlDefinitionPos(pkg, node, exprPos, off)
	}
	if hasPipeStages(node) {
		if obj, _, ok := pipedTarget(pkg, node, exprPos, off); ok && obj.Pos().IsValid() {
			dp := pkg.Fset.Position(obj.Pos())
			if dp.Filename != "" && !strings.HasSuffix(dp.Filename, ".x.go") {
				return dp, true
			}
		}
		return token.Position{}, false
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		return token.Position{}, false
	}

	// Map the cursor into the skeleton expr by relative byte offset (the gsx and
	// skeleton expression texts are byte-identical).
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)

	id := innermostIdent(skel, skelPos)
	if id == nil {
		return token.Position{}, false
	}
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	if obj == nil || !obj.Pos().IsValid() {
		return token.Position{}, false
	}
	dp := pkg.Fset.Position(obj.Pos())
	// Only surface real source locations. Params resolve back to .gsx via the
	// skeleton's param //line (D3); user Go symbols resolve to real .go files.
	// Anything still pointing at a bare skeleton overlay path (the in-memory
	// <base>.x.go) is a synthesized internal (e.g. the `ctx`/`_gsxp` bindings) —
	// return no definition rather than jump into generated code that may not even
	// exist on disk. (`.x.go` is gsx's reserved generated suffix; the only false
	// positive would be a hand-written dependency file literally named `*.x.go`.)
	if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
		return token.Position{}, false
	}
	return dp, true
}

// ctrlObjectAt resolves a cursor inside a CtrlMap-bridged span (see
// isCtrlSpan: control-flow clauses, switch tags and case lists, in-tag
// conditional-attribute conds, class guard conds, and value-form control
// expressions) to the go/types object of the identifier under the cursor,
// plus that identifier's byte span in the .gsx (for hover highlight ranges).
// It bridges the cursor into the skeleton via the node's CtrlMap entry — the
// clause bytes are emitted verbatim, so relative offsets align both ways.
// ok=false when the node has no CtrlMap entry or no identifier resolves.
func ctrlObjectAt(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (obj types.Object, identStart, identLen int, ok bool) {
	cr, found := pkg.CtrlMap[node]
	if !found || cr.Node == nil || pkg.Info == nil {
		return nil, 0, 0, false
	}
	clauseStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := cr.ClauseStart + token.Pos(off-clauseStart)
	id := innermostIdent(cr.Node, skelPos)
	if id == nil {
		return nil, 0, 0, false
	}
	obj = pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	if obj == nil {
		return nil, 0, 0, false
	}
	return obj, clauseStart + int(id.Pos()-cr.ClauseStart), len(id.Name), true
}

// ctrlDefinitionPos resolves a cursor inside a CtrlMap-bridged span to the
// defining object's source position, rejecting positions still inside
// generated .x.go overlays.
func ctrlDefinitionPos(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (token.Position, bool) {
	obj, _, _, ok := ctrlObjectAt(pkg, node, exprPos, off)
	if !ok || !obj.Pos().IsValid() {
		return token.Position{}, false
	}
	dp := pkg.Fset.Position(obj.Pos())
	if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
		return token.Position{}, false
	}
	return dp, true
}

// handleGoDefinition answers definition for a cursor in a .go file: if the
// cursor sits on a reference to a gsx component (per the cross-index), jump to
// that component's .gsx declaration. Otherwise null (gopls handles real Go).
func (s *Server) handleGoDefinition(f frame, uri, path string) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || (len(pkg.NavIndex) == 0 && len(pkg.CrossIndex) == 0) {
		return s.reply(f.ID, nil) // not a gsx package
	}
	curLine := p.Position.Line + 1 // token.Position is 1-based
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1 // 1-based byte column on the line
	for _, nr := range pkg.NavIndex {
		if nr.To.IsValid() && posCoversCursor(nr.From, path, curLine, curCol, len(nr.Name)) {
			return s.reply(f.ID, s.locationForPos(nr.To))
		}
	}
	return s.reply(f.ID, nil)
}

// lineStartOffset returns the byte offset of the start of the 0-based line.
func lineStartOffset(text string, line int) int {
	off := 0
	for range line {
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			return len(text)
		}
		off += nl + 1
	}
	return off
}

// locationForPos converts a resolved token.Position (a .gsx or .go file) to an
// LSP Location, encoding the column against the target file's own text.
func (s *Server) locationForPos(dp token.Position) Location {
	char := dp.Column - 1
	if data, err := os.ReadFile(dp.Filename); err == nil {
		char = charForByteCol(lineAtFunc(string(data))(dp.Line), dp.Column, s.enc)
	}
	line := max(dp.Line-1, 0)
	pos := Position{Line: line, Character: char}
	return Location{URI: pathToURI(dp.Filename), Range: Range{Start: pos, End: pos}}
}

// locationForNameSpan builds a Location covering the name (nameLen bytes) that
// begins at dp, encoding columns against the target file's on-disk text.
func (s *Server) locationForNameSpan(dp token.Position, nameLen int) Location {
	startChar, endChar := dp.Column-1, dp.Column-1+nameLen
	if data, err := os.ReadFile(dp.Filename); err == nil {
		lineText := lineAtFunc(string(data))(dp.Line)
		startChar = charForByteCol(lineText, dp.Column, s.enc)
		endChar = charForByteCol(lineText, dp.Column+nameLen, s.enc)
	}
	line := max(dp.Line-1, 0)
	return Location{URI: pathToURI(dp.Filename), Range: Range{
		Start: Position{Line: line, Character: startChar},
		End:   Position{Line: line, Character: endChar},
	}}
}

// posCoversCursor reports whether the token.Position r (a reference in a .go
// file) covers the given cursor (1-based line and byte column). The reference
// name has length nameLen bytes; the span is [r.Column, r.Column+nameLen).
// filepath.Base comparison is used so the cross-index path need not be absolute.
func posCoversCursor(r token.Position, path string, curLine, curCol, nameLen int) bool {
	if r.Line != curLine {
		return false
	}
	if filepath.Base(r.Filename) != filepath.Base(path) {
		return false
	}
	return curCol >= r.Column && curCol < r.Column+nameLen
}

// componentTagDeclAt checks whether the byte offset off in the .gsx file at
// path sits on the name portion of a component element tag (e.g. the "Card" in
// "<Card .../>"). If so, it looks the component up in pkg.CrossIndex by the
// function-component key "." + tag, and returns every build-tag variant's
// declaration position (Task 7) and true. Returns (nil, false) if the cursor
// is not on a component tag.
func componentTagDeclAt(pkg *Package, path string, off int) ([]token.Position, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return nil, false
	}
	f := pkg.Files[path]
	if f == nil {
		return nil, false
	}
	var result []token.Position
	found := false
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if found {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok {
			return true
		}
		tag := el.Tag
		if tag == "" || strings.Contains(tag, ".") || tag[0] < 'A' || tag[0] > 'Z' {
			// not a simple function component tag
			return true
		}
		// The opening tag name starts right after the '<': nameStart is the byte
		// offset of the first character of the tag name in the file.
		elOff := pkg.GSXFset.Position(el.Pos()).Offset
		nameStart := elOff + 1 // skip '<'
		onOpen := off >= nameStart && off < nameStart+len(tag)
		// The closing tag name (the "Card" in "</Card>") resolves the same way, so
		// go-to-definition works from either end of the element.
		onClose := false
		if el.CloseNamePos.IsValid() {
			closeStart := pkg.GSXFset.Position(el.CloseNamePos).Offset
			onClose = off >= closeStart && off < closeStart+len(tag)
		}
		if onOpen || onClose {
			// Cursor is on the tag name; look up in CrossIndex. Decls carries every
			// build-tag variant's declaration (Task 6); fall back to the single
			// primary Decl for CrossRef values that predate Decls (e.g. synthetic
			// test fixtures built before Task 7).
			key := "." + tag
			cr, ok := pkg.CrossIndex[key]
			if ok {
				decls := cr.Decls
				if len(decls) == 0 && cr.Decl.IsValid() {
					decls = []token.Position{cr.Decl}
				}
				if len(decls) > 0 {
					result = decls
					found = true
				}
			}
		}
		return true
	})
	return result, found
}
