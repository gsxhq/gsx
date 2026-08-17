package lsp

import (
	"encoding/json"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// handleReferences answers textDocument/references from the module symbol
// graph: the occurrence under the cursor — in any indexed .gsx or .go file,
// including a component tag name, an `attr=` binding and a `|> pipe` stage —
// names an ObjectKey, and the reply is every reference span of that key
// module-wide, plus its definition spans when includeDeclaration is set. When
// the module graph cannot be built (or does not cover the cursor's file), the
// retained per-package index answers same-package requests.
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	if !s.diskViewValid {
		return s.reply(f.ID, []Location{})
	}
	path := uriToPath(p.TextDocument.URI)
	sources := s.sourceSnapshot()
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, []Location{})
	}
	source, _ := sources.sourceText(path) // same captured source, validated above
	dir := filepath.Dir(path)
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	graph := s.moduleSymbolGraph(sources, dir)
	key, found := symbolAt(graph, path, source, off)
	if !found {
		graph = packageSymbolGraph(s.pkgs[dir])
		key, found = symbolAt(graph, path, source, off)
	}
	if !found {
		return s.reply(f.ID, []Location{})
	}

	locations := make([]Location, 0)
	if p.Context.IncludeDeclaration {
		locations = appendSpanLocations(locations, sources, graph.Definitions(key))
	}
	locations = appendSpanLocations(locations, sources, graph.References(key))
	return s.reply(f.ID, locations)
}

func appendSpanLocations(locations []Location, sources *requestSourceSnapshot, spans []sourceintel.Span) []Location {
	for _, span := range spans {
		if location, ok := sources.locationForSpan(span); ok {
			locations = append(locations, location)
		}
	}
	return locations
}

// moduleSymbolGraph returns the whole-module graph, analyzing it lazily. A
// successful analysis — even an empty graph — is cached until the next document
// mutation or watched change; an error leaves the cache invalid so the next
// request retries.
func (s *Server) moduleSymbolGraph(sources *requestSourceSnapshot, dir string) *sourceintel.SymbolGraph {
	if !s.moduleGraphValid {
		s.refreshModuleGraph(sources, dir)
	}
	return s.moduleGraph
}

func (s *Server) refreshModuleGraph(sources *requestSourceSnapshot, dir string) {
	if graph, err := s.analyzer.AnalyzeModule(dir, sources.openGSXOverrides()); err == nil {
		s.moduleGraph = graph
		s.moduleGraphValid = true
	}
}

// packageSymbolGraph is the degraded, same-package answer for when the module
// graph does not cover the cursor, and the decl oracle for a same-package
// component tag: one package's retained index, keyed on its own types.
//
// It is limited to packages this server owns — ones that actually author .gsx
// files. A package with no .gsx is gopls' alone (a .go cursor there is answered
// by the module graph only when the package imports a gsx package, which is the
// coverage the graph is built for); answering it from the retained index would
// duplicate gopls in packages gsx has no business in.
//
// Building the graph costs ~0.26µs per indexed occurrence (measured), so a
// per-request build is affordable and no cache has to be kept coherent with
// re-analysis.
func packageSymbolGraph(pkg *Package) *sourceintel.SymbolGraph {
	if pkg == nil || pkg.SourceIndex == nil || len(pkg.Files) == 0 {
		return nil
	}
	graph := sourceintel.NewSymbolGraph()
	graph.AddIndex(pkg.SourceIndex, sourceintel.NewKeyer(pkg.Types))
	return graph
}

// symbolAt resolves a cursor to the key of the symbol it names. It is
// fail-closed on staleness: the graph must have been built from exactly the
// bytes the request is resolving against, or the offset means nothing.
func symbolAt(graph *sourceintel.SymbolGraph, path string, source []byte, offset int) (sourceintel.ObjectKey, bool) {
	if graph == nil || !graph.MatchesSource(path, source) {
		return "", false
	}
	key, _, ok := graph.At(path, offset)
	return key, ok
}

// symbolDefinitionAt is symbolAt for go-to-definition: it prefers the USE at
// the cursor over a definition at the same span. The only ident that is both is
// an embedded struct field, where the use (the embedded type) is what F12
// should travel to; every other cursor has at most one of the two, so the
// preference is a no-op. References and hover keep symbolAt's tie-break.
func symbolDefinitionAt(graph *sourceintel.SymbolGraph, path string, source []byte, offset int) (sourceintel.ObjectKey, bool) {
	if graph == nil || !graph.MatchesSource(path, source) {
		return "", false
	}
	if key, _, ok := graph.UseAt(path, offset); ok {
		return key, true
	}
	key, _, ok := graph.At(path, offset)
	return key, ok
}
