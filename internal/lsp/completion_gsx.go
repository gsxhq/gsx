package lsp

import "path/filepath"

// filterItems builds one CompletionItem per resolved filter candidate for a
// ctxPipeStage cursor. filters is a package's resolved filter table
// (Package.Filters, already sorted by name); order is preserved verbatim —
// this function does no sorting or prefix filtering of its own (the client
// matches against label/filterText as the user types). Detail renders as
// "Pkg.Func", with a " (ctx)" suffix when the filter's leading argument is a
// context.Context (WantsCtx).
func filterItems(filters []FilterCandidate, text string, start, end int, enc encoding) []CompletionItem {
	items := make([]CompletionItem, 0, len(filters))
	for _, f := range filters {
		detail := f.Pkg + "." + f.Func
		if f.WantsCtx {
			detail += " (ctx)"
		}
		items = append(items, newCompletionItem(text, start, end, enc, f.Name, f.Name, ciKindFunction, tierContext, detail, nil))
	}
	return items
}

// pipeStageCompletion answers a ctxPipeStage cursor (after `|>` inside a
// pipeline): one item per resolved filter-table candidate. A repair-phantom
// `_` stage (Task 7's healed `|> ` empty stage) never reaches filterItems as
// typed text: completionTokenSpan scans the ORIGINAL, unpatched buffer text
// (not r.src), so an empty stage naturally yields a zero-width [off,off) span
// with nothing typed to filter on or leak into the edit.
func (s *Server) pipeStageCompletion(cc completionContext, path, text string, off int, r repairResult) CompletionList {
	filters := s.pipeFilters(filepath.Dir(path), path, r.src)
	if len(filters) == 0 {
		return emptyCompletion()
	}
	start, end := completionTokenSpan(text, off, false)
	items := filterItems(filters, text, start, end, s.enc)
	if len(items) == 0 {
		return emptyCompletion()
	}
	return CompletionList{IsIncomplete: false, Items: items}
}

// pipeFilters resolves the pipe-filter table for dir. It prefers one
// ephemeral analysis of the (possibly mid-edit) buffer src, so a filter
// registered by an edit still only in the buffer completes immediately. When
// that comes back a shell — the analyzer errored, or returned a
// diagnostics-only Package with both Info and Filters empty (parse/analyze
// failure) — it falls back to the retained s.pkgs[dir] snapshot: filter NAMES
// are position-independent, so serving a stale retained list under staleness
// is safe. Both empty (or absent) yields nil, and pipeStageCompletion turns
// that into an empty list — fail soft, never an error.
func (s *Server) pipeFilters(dir, path string, src []byte) []FilterCandidate {
	eph, err := s.analyzer.AnalyzeEphemeral(dir, path, src)
	if err == nil && eph != nil && (eph.Info != nil || len(eph.Filters) > 0) {
		return eph.Filters
	}
	if pkg := s.pkgs[dir]; pkg != nil {
		return pkg.Filters
	}
	return nil
}
