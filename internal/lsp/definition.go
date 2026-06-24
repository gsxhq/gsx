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
		switch e := n.(type) {
		case *gsxast.Interp:
			exprPos, exprLen = e.ExprPos, len(e.Expr)
		case *gsxast.ExprAttr:
			exprPos, exprLen = e.ExprPos, len(e.Expr)
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
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || pkg.Info == nil {
		return s.reply(f.ID, nil)
	}

	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		return s.reply(f.ID, nil)
	}
	if hasPipeStages(node) {
		// A piped expression (`{ x |> f }`) lowers to a wrapped call in the
		// skeleton, so ExprMap[node] is the lowered call, not the byte-identical
		// seed. The relative-offset bridge would then map the cursor into
		// generated code and resolve a wrong symbol. Return no definition until
		// seed-node mapping lands (follow-up): an honest null beats a wrong jump.
		return s.reply(f.ID, nil)
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

// locationFor builds an LSP Location from a resolved definition position,
// converting its 1-based byte column to the negotiated encoding using the target
// file's own line text (read from disk; the target is a real file).
func (s *Server) locationFor(dp token.Position) Location {
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

