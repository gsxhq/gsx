package lsp

import (
	"encoding/json"
	"strings"
)

// handleWorkspaceSymbol returns every module-wide symbol whose name contains the
// query (case-insensitive substring; empty query returns all). The module symbol
// list is built lazily via ModuleSymbols, cached, and invalidated on any document
// mutation. On ModuleSymbols error it replies with an empty list.
func (s *Server) handleWorkspaceSymbol(f frame) error {
	sources := s.sourceSnapshot()
	var p workspaceSymbolParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []SymbolInformation{})
	}
	if !s.diskViewValid {
		return s.reply(f.ID, []SymbolInformation{})
	}
	if !s.moduleSymsValid {
		syms, err := s.analyzer.ModuleSymbols(sources.anyOpenDir(), sources.openGSXOverrides())
		if err != nil {
			return s.reply(f.ID, []SymbolInformation{})
		}
		s.moduleSyms = syms
		s.moduleSymsValid = true
	}
	q := strings.ToLower(p.Query)
	out := make([]SymbolInformation, 0, len(s.moduleSyms))
	for _, sym := range s.moduleSyms {
		if q != "" && !strings.Contains(strings.ToLower(sym.Name), q) {
			continue
		}
		span, ok := authoredSpanForPosition(sym.NamePos, len(sym.Name))
		if !ok {
			continue
		}
		location, ok := sources.locationForSpan(span)
		if !ok {
			continue
		}
		out = append(out, SymbolInformation{
			Name:          sym.Name,
			Kind:          sym.Kind,
			ContainerName: sym.Container,
			Location:      location,
		})
	}
	return s.reply(f.ID, out)
}
