package sourceintel

import (
	"maps"
	"sort"
)

type keyedOccurrence struct {
	span Span
	key  ObjectKey
	kind OccurrenceKind
}

// SymbolGraph is the module-wide, keyed union of per-package indexes: every
// definition and reference span of every keyable object, plus per-file
// occurrence tables so a cursor in any indexed file resolves to a key.
type SymbolGraph struct {
	occurrences map[string][]keyedOccurrence // path → sorted by start, then shortest
	definitions map[ObjectKey][]Span
	references  map[ObjectKey][]Span
	sources     map[string]SourceVersion
	finalized   bool
}

func NewSymbolGraph() *SymbolGraph {
	return &SymbolGraph{
		occurrences: map[string][]keyedOccurrence{},
		definitions: map[ObjectKey][]Span{},
		references:  map[ObjectKey][]Span{},
		sources:     map[string]SourceVersion{},
	}
}

// AddIndex merges one package's index. Occurrences without a keyable object
// (expressions, universe objects, foreign locals) are skipped. Call once per
// package — a repeated add double-counts occurrences.
func (g *SymbolGraph) AddIndex(index *Index, keyer *Keyer) {
	if index == nil || keyer == nil {
		return
	}
	maps.Copy(g.sources, index.sources)
	index.forEachOccurrence(func(path string, o Occurrence) {
		if o.Kind == Expression || o.Object == nil {
			return
		}
		key, ok := keyer.Key(o.Object)
		if !ok {
			return
		}
		g.occurrences[path] = append(g.occurrences[path], keyedOccurrence{span: o.Span, key: key, kind: o.Kind})
		if o.Kind == IdentifierDefinition {
			g.definitions[key] = append(g.definitions[key], o.Span)
		} else {
			g.references[key] = append(g.references[key], o.Span)
		}
	})
	g.finalized = false
}

func (g *SymbolGraph) finalize() {
	if g.finalized {
		return
	}
	for path, occ := range g.occurrences {
		sort.SliceStable(occ, func(a, b int) bool {
			if occ[a].span.Start != occ[b].span.Start {
				return occ[a].span.Start < occ[b].span.Start
			}
			la, lb := occ[a].span.End-occ[a].span.Start, occ[b].span.End-occ[b].span.Start
			if la != lb {
				return la < lb
			}
			return occ[a].kind < occ[b].kind // definitions before uses on identical spans
		})
		g.occurrences[path] = occ
	}
	for k, spans := range g.definitions {
		g.definitions[k] = sortDedupSpans(spans)
	}
	for k, spans := range g.references {
		g.references[k] = sortDedupSpans(spans)
	}
	g.finalized = true
}

func sortDedupSpans(spans []Span) []Span {
	sort.Slice(spans, func(a, b int) bool {
		if spans[a].Path != spans[b].Path {
			return spans[a].Path < spans[b].Path
		}
		if spans[a].Start != spans[b].Start {
			return spans[a].Start < spans[b].Start
		}
		return spans[a].End < spans[b].End
	})
	out := spans[:0]
	for i, s := range spans {
		if i == 0 || s != spans[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// At returns the key of the innermost identifier occurrence covering offset
// in path (definitions preferred over uses on identical spans).
func (g *SymbolGraph) At(path string, offset int) (ObjectKey, Span, bool) {
	return g.at(path, offset, false)
}

// UseAt is At restricted to reference occurrences: the innermost IdentifierUse
// covering offset, ignoring definitions entirely.
//
// An embedded struct field's ident (`type Pages struct { Home }`) is BOTH a
// field definition and a use of the embedded type, at one identical span. At
// answers the field — definitions win the tie — which makes go-to-definition a
// no-op jump onto the cursor itself. UseAt gives the definition handlers the
// type-side answer without moving the tie-break for hover, rename and
// references, which all read At and correctly want the field there.
func (g *SymbolGraph) UseAt(path string, offset int) (ObjectKey, Span, bool) {
	return g.at(path, offset, true)
}

func (g *SymbolGraph) at(path string, offset int, usesOnly bool) (ObjectKey, Span, bool) {
	g.finalize()
	occ := g.occurrences[path]
	best := -1
	for i, o := range occ {
		if o.span.Start > offset {
			break
		}
		if usesOnly && o.kind != IdentifierUse {
			continue
		}
		if offset < o.span.End || (o.span.Start == o.span.End && offset == o.span.Start) {
			if best < 0 || (o.span.End-o.span.Start) < (occ[best].span.End-occ[best].span.Start) {
				best = i
			}
		}
	}
	if best < 0 {
		return "", Span{}, false
	}
	return occ[best].key, occ[best].span, true
}

func (g *SymbolGraph) Definitions(key ObjectKey) []Span {
	g.finalize()
	return append([]Span(nil), g.definitions[key]...)
}

func (g *SymbolGraph) References(key ObjectKey) []Span {
	g.finalize()
	return append([]Span(nil), g.references[key]...)
}

func (g *SymbolGraph) MatchesSource(path string, source []byte) bool {
	v, ok := g.sources[path]
	return ok && v.Matches(source)
}

func (g *SymbolGraph) Stats() (files, keys, occurrences int) {
	for _, o := range g.occurrences {
		occurrences += len(o)
	}
	seen := map[ObjectKey]bool{}
	for k := range g.definitions {
		seen[k] = true
	}
	for k := range g.references {
		seen[k] = true
	}
	return len(g.sources), len(seen), occurrences
}
