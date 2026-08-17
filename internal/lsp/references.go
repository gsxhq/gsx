package lsp

import (
	"encoding/json"
	"path/filepath"
)

// handleReferences returns every reference to the gsx component under the cursor
// — .go call sites and .gsx <Card/> tags — from the module-wide symbol graph.
//
// TODO(module-symbol-graph): rewritten in the next commit. This handler used to
// resolve the cursor against a whole-module cross-reference list built from
// codegen.CrossRef/NavRef (deleted). AnalyzeModule now returns a
// *sourceintel.SymbolGraph instead; the graph is still refreshed and cached here
// so the lazy/invalidate-on-edit plumbing stays exercised, but cursor resolution
// against it (SymbolGraph.At + Definitions/References) is Task 10/11's job. Until
// then this always replies with an empty result.
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	if !s.diskViewValid {
		return s.reply(f.ID, []Location{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	sources := s.sourceSnapshot()
	if _, ok := sources.sourceString(path); !ok {
		return s.reply(f.ID, []Location{})
	}

	// Whole-module graph (lazy, cached, invalidated on edits). A successful
	// AnalyzeModule — even an empty graph — is cached; an error leaves the cache
	// invalid so the next request retries.
	if !s.moduleGraphValid {
		s.refreshModuleReferences(sources, filepath.Dir(path))
	}

	return s.reply(f.ID, []Location{})
}

func (s *Server) refreshModuleReferences(sources *requestSourceSnapshot, dir string) {
	if graph, err := s.analyzer.AnalyzeModule(dir, sources.openGSXOverrides()); err == nil {
		s.moduleGraph = graph
		s.moduleGraphValid = true
	}
}
