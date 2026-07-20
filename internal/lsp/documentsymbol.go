package lsp

import (
	"encoding/json"
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
	sources := s.sourceSnapshot()
	text, ok := sources.sourceText(path)
	if !ok {
		return s.reply(f.ID, []DocumentSymbol{})
	}

	syms := FileSymbols(path, text, file, pkg.GSXFset, pkg.SourceIndex)
	out := make([]DocumentSymbol, 0, len(syms))
	for _, sym := range syms {
		declarationRange, ok := sources.rangeForAuthoredPositions(sym.DeclStart, sym.DeclEnd)
		if !ok {
			continue
		}
		nameSpan, ok := authoredSpanForPosition(sym.NamePos, len(sym.Name))
		if !ok {
			continue
		}
		selectionRange, ok := sources.rangeForSpan(nameSpan)
		if !ok {
			continue
		}
		out = append(out, DocumentSymbol{
			Name:           sym.Name,
			Kind:           sym.Kind,
			Range:          declarationRange,
			SelectionRange: selectionRange,
		})
	}
	return s.reply(f.ID, out)
}
