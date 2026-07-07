package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// handleWorkspaceSymbol returns every module-wide symbol whose name contains the
// query (case-insensitive substring; empty query returns all). The module symbol
// list is built lazily via ModuleSymbols, cached, and invalidated on any document
// mutation. On ModuleSymbols error it replies with an empty list.
func (s *Server) handleWorkspaceSymbol(f frame) error {
	var p workspaceSymbolParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []SymbolInformation{})
	}
	if !s.moduleSymsValid {
		dir := s.anyOpenDir()
		syms, err := s.analyzer.ModuleSymbols(dir, s.docs.allOpenGSX())
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
		out = append(out, SymbolInformation{
			Name:          sym.Name,
			Kind:          sym.Kind,
			ContainerName: sym.Container,
			Location:      s.locationForNameSpan(sym.NamePos, len(sym.Name)),
		})
	}
	return s.reply(f.ID, out)
}

// anyOpenDir returns the directory of some open document (any is fine — the
// module root is derived from it). Falls back to "." when nothing is open.
func (s *Server) anyOpenDir() string {
	for path := range s.docs.allOpenGSX() {
		return filepath.Dir(path)
	}
	return "."
}
