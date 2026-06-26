package lsp

import (
	"encoding/json"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// exprNodeAtOffset returns the gsx Interp/ExprAttr whose Go-expression span
// [ExprPos, ExprPos+len(Expr)) contains the byte offset, together with that
// node's ExprPos (so the caller need not re-discriminate the node type). Returns
// (nil, token.NoPos) when no expression node covers the offset. Interp/ExprAttr
// expressions are opaque strings that never nest, so at most one node matches an
// offset — the walk's last-write-wins is unambiguous.
func exprNodeAtOffset(pkg *Package, path string, off int) (gsxast.Node, token.Pos) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return nil, token.NoPos
	}
	var found gsxast.Node
	var foundPos token.Pos
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if n == nil {
			return false
		}
		var exprPos token.Pos
		var exprLen int
		var stages []gsxast.PipeStage
		switch e := n.(type) {
		case *gsxast.Interp:
			exprPos, exprLen = e.ExprPos, len(e.Expr)
			stages = e.Stages
		case *gsxast.ExprAttr:
			exprPos, exprLen = e.ExprPos, len(e.Expr)
			stages = e.Stages
		case *gsxast.ForMarkup:
			exprPos, exprLen = e.ClausePos, len(e.Clause)
		case *gsxast.IfMarkup:
			exprPos, exprLen = e.CondPos, len(e.Cond)
		case *gsxast.GoBlock:
			exprPos, exprLen = e.CodePos, len(e.Code)
		default:
			return true
		}
		if !exprPos.IsValid() {
			return true
		}
		start := pkg.GSXFset.Position(exprPos).Offset
		if off >= start && off < start+exprLen {
			found = n
			foundPos = exprPos
			return true
		}
		// Also match pipeline stage positions (filter name, filter args) so that
		// pipedTarget can be dispatched for those cursor positions too.
		for _, st := range stages {
			if st.NamePos.IsValid() {
				nameStart := pkg.GSXFset.Position(st.NamePos).Offset
				if off >= nameStart && off < nameStart+len(st.Name) {
					found = n
					foundPos = exprPos
					return true
				}
			}
			if st.HasArgs && st.ArgsPos.IsValid() {
				argsStart := pkg.GSXFset.Position(st.ArgsPos).Offset
				if off >= argsStart && off < argsStart+len(st.Args) {
					found = n
					foundPos = exprPos
					return true
				}
			}
		}
		return true
	})
	return found, foundPos
}

// hasPipeStages reports whether the gsx expression node carries pipeline stages
// (`{ x |> f }`). Such nodes lower to a wrapped call in the skeleton, breaking
// the byte-identical relative-offset bridge go-to-def relies on.
func hasPipeStages(n gsxast.Node) bool {
	switch e := n.(type) {
	case *gsxast.Interp:
		return len(e.Stages) > 0
	case *gsxast.ExprAttr:
		return len(e.Stages) > 0
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

	// D2: cursor on a component tag name in a .gsx file → jump to the component declaration.
	if decl, ok := componentTagDeclAt(pkg, path, off); ok {
		return s.reply(f.ID, s.locationForPos(decl))
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

	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		return s.reply(f.ID, nil)
	}
	if hasPipeStages(node) {
		if obj, _, ok := pipedTarget(pkg, node, exprPos, off); ok && obj.Pos().IsValid() {
			dp := pkg.Fset.Position(obj.Pos())
			if dp.Filename != "" && !strings.HasSuffix(dp.Filename, ".x.go") {
				return s.reply(f.ID, s.locationForPos(dp))
			}
		}
		return s.reply(f.ID, nil)
	}
	switch node.(type) {
	case *gsxast.ForMarkup, *gsxast.IfMarkup, *gsxast.GoBlock:
		cr, ok := pkg.CtrlMap[node]
		if !ok || cr.Node == nil {
			return s.reply(f.ID, nil)
		}
		clauseStart := pkg.GSXFset.Position(exprPos).Offset
		skelPos := cr.ClauseStart + token.Pos(off-clauseStart)
		id := innermostIdent(cr.Node, skelPos)
		if id == nil {
			return s.reply(f.ID, nil)
		}
		obj := pkg.Info.Uses[id]
		if obj == nil {
			obj = pkg.Info.Defs[id]
		}
		if obj == nil || !obj.Pos().IsValid() {
			return s.reply(f.ID, nil)
		}
		dp := pkg.Fset.Position(obj.Pos())
		if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
			return s.reply(f.ID, nil)
		}
		return s.reply(f.ID, s.locationForPos(dp))
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		return s.reply(f.ID, nil)
	}

	// Map the cursor into the skeleton expr by relative byte offset (the gsx and
	// skeleton expression texts are byte-identical).
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)

	id := innermostIdent(skel, skelPos)
	if id == nil {
		return s.reply(f.ID, nil)
	}
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	if obj == nil || !obj.Pos().IsValid() {
		return s.reply(f.ID, nil)
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
		return s.reply(f.ID, nil)
	}
	loc := s.locationFor(dp)
	return s.reply(f.ID, loc)
}

// locationFor builds an LSP Location from a resolved definition position. Alias
// of locationForPos (kept for the slice-2a .gsx-side call sites).
func (s *Server) locationFor(dp token.Position) Location {
	return s.locationForPos(dp)
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
	for i := 0; i < line; i++ {
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
	line := dp.Line - 1
	if line < 0 {
		line = 0
	}
	pos := Position{Line: line, Character: char}
	return Location{URI: pathToURI(dp.Filename), Range: Range{Start: pos, End: pos}}
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
// function-component key "." + tag, and returns its Decl position and true.
// Returns (zero, false) if the cursor is not on a component tag.
func componentTagDeclAt(pkg *Package, path string, off int) (token.Position, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return token.Position{}, false
	}
	f := pkg.Files[path]
	if f == nil {
		return token.Position{}, false
	}
	var result token.Position
	found := false
	gsxast.Inspect(f, func(n gsxast.Node) bool {
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
			// Cursor is on the tag name; look up in CrossIndex.
			key := "." + tag
			cr, ok := pkg.CrossIndex[key]
			if ok && cr.Decl.IsValid() {
				result = cr.Decl
				found = true
			}
		}
		return true
	})
	return result, found
}
