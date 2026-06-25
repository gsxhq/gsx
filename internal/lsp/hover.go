package lsp

import (
	"encoding/json"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// handleHover answers textDocument/hover for a .gsx file: it shows the Go
// type/signature of the symbol or expression under the cursor. .go files are
// gopls's to hover (null).
func (s *Server) handleHover(f frame) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	path := uriToPath(p.TextDocument.URI)
	if strings.HasSuffix(path, ".go") {
		return s.reply(f.ID, nil) // gopls owns .go hover
	}
	text, ok := s.docs.text(p.TextDocument.URI)
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
		// A piped expr lowers to a wrapped call, so the byte-identical
		// relative-offset bridge does not hold and the cursor cannot be reliably
		// mapped (mirrors definition). Honest null.
		return s.reply(f.ID, nil)
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		return s.reply(f.ID, nil)
	}
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)
	qf := qualifierFor(pkg)

	// On an identifier → show the resolved object's signature.
	if id := innermostIdent(skel, skelPos); id != nil {
		obj := pkg.Info.Uses[id]
		if obj == nil {
			obj = pkg.Info.Defs[id]
		}
		if obj != nil {
			identStart := exprStart + int(id.Pos()-skel.Pos())
			rng := rangeForSpan(text, identStart, identStart+len(id.Name), s.enc)
			return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qf)), Range: &rng})
		}
	}
	// Otherwise → the whole expression's type.
	if tv, ok := pkg.Info.Types[skel]; ok && tv.Type != nil {
		rng := rangeForSpan(text, exprStart, exprStart+len(exprText(node)), s.enc)
		return s.reply(f.ID, Hover{Contents: markdownGo(types.TypeString(tv.Type, qf)), Range: &rng})
	}
	return s.reply(f.ID, nil)
}

// qualifierFor renders the analyzed package's own types unqualified and imported
// types by package name (gopls-style: `User`, `store.User`).
func qualifierFor(pkg *Package) types.Qualifier {
	return func(p *types.Package) string {
		if pkg.Types != nil && p == pkg.Types {
			return ""
		}
		return p.Name()
	}
}

// markdownGo wraps a Go signature/type string in a fenced go code block.
func markdownGo(s string) MarkupContent {
	return MarkupContent{Kind: "markdown", Value: "```go\n" + s + "\n```"}
}

// exprText returns the Go-expression source of an Interp / ExprAttr node.
func exprText(n gsxast.Node) string {
	switch e := n.(type) {
	case *gsxast.Interp:
		return e.Expr
	case *gsxast.ExprAttr:
		return e.Expr
	}
	return ""
}

// rangeForSpan converts a [startOff, endOff) byte span in text to an LSP Range
// (characters counted in the negotiated encoding).
func rangeForSpan(text string, startOff, endOff int, enc encoding) Range {
	return Range{
		Start: positionForByteOffset(text, startOff, enc),
		End:   positionForByteOffset(text, endOff, enc),
	}
}

// positionForByteOffset is the inverse of byteOffsetForPosition: a byte offset in
// text → a 0-based LSP position (character counted in enc).
func positionForByteOffset(text string, off int, enc encoding) Position {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	line := strings.Count(text[:off], "\n")
	lineStart := strings.LastIndexByte(text[:off], '\n') + 1 // 0 when no newline precedes
	char := charForByteCol(text[lineStart:off], (off-lineStart)+1, enc)
	return Position{Line: line, Character: char}
}
