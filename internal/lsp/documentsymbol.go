package lsp

import (
	"encoding/json"
	"go/token"
	"path/filepath"
)

// handleDocumentSymbol returns the component and top-level Go declarations in
// the requested .gsx file as a flat DocumentSymbol list (gsx decls do not
// nest). It reads from the already-analyzed package (s.pkgs); an unknown or
// unanalyzed file replies with an empty list.
func (s *Server) handleDocumentSymbol(f frame) error {
	var p documentSymbolParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	if !s.diskViewValid {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || pkg.Files == nil || pkg.GSXFset == nil {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	file := pkg.Files[path]
	if file == nil {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	text, _ := s.docs.text(uri) // "" if not open; positions still map best-effort
	lineAt := lineAtFunc(text)

	syms := FileSymbols(path, file, pkg.GSXFset)
	out := make([]DocumentSymbol, 0, len(syms))
	for _, sym := range syms {
		out = append(out, DocumentSymbol{
			Name:           sym.Name,
			Kind:           sym.Kind,
			Range:          Range{Start: convertPos(sym.DeclStart, lineAt, s.enc), End: convertPos(sym.DeclEnd, lineAt, s.enc)},
			SelectionRange: nameSelectionRange(sym, lineAt, s.enc),
		})
	}
	return s.reply(f.ID, out)
}

// nameSelectionRange builds the range covering just the symbol's name on its
// declaration line.
func nameSelectionRange(sym Symbol, lineAt func(int) string, enc encoding) Range {
	start := convertPos(sym.NamePos, lineAt, enc)
	endCol := sym.NamePos.Column + len(sym.Name)
	end := convertPos(tokenPosAtColumn(sym.NamePos, endCol), lineAt, enc)
	return Range{Start: start, End: end}
}

// tokenPosAtColumn returns a copy of p with the given 1-based byte column.
func tokenPosAtColumn(p token.Position, col int) token.Position {
	p.Column = col
	return p
}
