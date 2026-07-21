package lsp

import (
	"path/filepath"
	"sort"
)

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

// tagCompletion answers a ctxTag cursor (`<▮` or `<qualifier.▮`): component
// candidates drawn from ComponentDecls, local and imported. See
// componentDeclPackage for the resolution fallback and componentTagItems for
// the candidate list itself. HTML tag names are not offered yet (Task 15
// merges them in); until then this list is components only.
func (s *Server) tagCompletion(cc completionContext, path, text string, off int, r repairResult) CompletionList {
	pkg := s.componentDeclPackage(filepath.Dir(path), path, r.src)
	if pkg == nil {
		return emptyCompletion()
	}
	start, end := completionTokenSpan(text, off, false)
	capitalizedPrefix := startsWithUpper(text[start:end])
	items := componentTagItems(pkg, cc.qualifier, capitalizedPrefix, text, start, end, s.enc)
	if len(items) == 0 {
		return emptyCompletion()
	}
	return CompletionList{IsIncomplete: false, Items: items}
}

// componentDeclPackage resolves the analyzed package whose ComponentDecls and
// Types back a ctxTag cursor. Same ephemeral-then-retained fallback as
// pipeFilters: an ephemeral analysis of the (possibly mid-edit) buffer src is
// preferred, so a component just added in this edit completes immediately;
// when that comes back a shell (analyzer error, or a diagnostics-only Package
// with both Info and ComponentDecls empty), the retained s.pkgs[dir] snapshot
// is served instead — component identities (ComponentDecls keys, import
// paths/names) are position-independent, so a stale retained package is a
// safe fallback. Both absent/empty yields nil, and tagCompletion turns that
// into an empty list — fail soft, never an error.
func (s *Server) componentDeclPackage(dir, path string, src []byte) *Package {
	eph, err := s.analyzer.AnalyzeEphemeral(dir, path, src)
	if err == nil && eph != nil && (eph.Info != nil || len(eph.ComponentDecls) > 0) {
		return eph
	}
	if pkg := s.pkgs[dir]; pkg != nil {
		return pkg
	}
	return nil
}

// componentTagItems enumerates component candidates for a tag cursor.
//
// qualifier == "": current-package components, plus one qualifier item per
// imported gsx package that declares any component (label = import name,
// insert = "name.", kind ciKindModule).
//
// qualifier != "": components of the import whose package NAME == qualifier.
//
// A component's ComponentKey (codegen's componentKey/componentObjectKey) is
// "."+Name for a plain function component — a LEADING dot, not a bare name —
// and RecvType+"."+Name for a receiver/method component. Every key therefore
// contains a dot; componentNameItems' exclusion rule keys off the dot's
// POSITION (index 0), not off dot-presence. Receiver components need a
// receiver expression before the tag can resolve and are excluded from v1
// (spec follow-up).
//
// capitalizedPrefix does not affect ranking yet — every item here is
// tierContext; it starts mattering once Task 15 merges HTML tag names into
// this same list (capitalization flips which list gets tierSecondary).
func componentTagItems(pkg *Package, qualifier string, capitalizedPrefix bool, text string, start, end int, enc encoding) []CompletionItem {
	if pkg == nil || pkg.Types == nil {
		return nil
	}
	if qualifier != "" {
		for _, imp := range pkg.Types.Imports() {
			if imp.Name() == qualifier {
				return componentNameItems(pkg, imp.Path(), text, start, end, enc)
			}
		}
		return nil
	}
	items := componentNameItems(pkg, pkg.Types.Path(), text, start, end, enc)
	items = append(items, importQualifierItems(pkg, text, start, end, enc)...)
	return items
}

// componentNameItems returns one item per plain (non-receiver) component
// declared in pkgPath, sorted by name for a deterministic list (ComponentDecls
// is a map; iteration order is not).
func componentNameItems(pkg *Package, pkgPath string, text string, start, end int, enc encoding) []CompletionItem {
	var names []string
	for key := range pkg.ComponentDecls {
		if key.PackagePath != pkgPath {
			continue
		}
		if name, ok := plainComponentName(key.ComponentKey); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	items := make([]CompletionItem, 0, len(names))
	for _, name := range names {
		items = append(items, newCompletionItem(text, start, end, enc, name, name, ciKindFunction, tierContext, "", nil))
	}
	return items
}

// plainComponentName extracts the bare name from a plain (non-receiver)
// ComponentKey — "."+Name, the leading-dot marker codegen.componentKey uses
// so a package-level function component can never collide with a method of
// the same name (see componentObjectKey/crossRefKeyForFunc). A receiver
// component key is RecvType+"."+Name (dot NOT at index 0) and is rejected.
func plainComponentName(key string) (string, bool) {
	if key == "" || key[0] != '.' {
		return "", false
	}
	return key[1:], true
}

// importQualifierItems returns one item per imported package that declares at
// least one component (any ComponentDeclKey.PackagePath == the import's
// path), sorted by import name for a deterministic list.
func importQualifierItems(pkg *Package, text string, start, end int, enc encoding) []CompletionItem {
	type qualifier struct{ name, path string }
	var quals []qualifier
	for _, imp := range pkg.Types.Imports() {
		if !packageHasComponentDecls(pkg, imp.Path()) {
			continue
		}
		quals = append(quals, qualifier{imp.Name(), imp.Path()})
	}
	sort.Slice(quals, func(i, j int) bool { return quals[i].name < quals[j].name })
	items := make([]CompletionItem, 0, len(quals))
	for _, q := range quals {
		items = append(items, newCompletionItem(text, start, end, enc, q.name, q.name+".", ciKindModule, tierContext, q.path, nil))
	}
	return items
}

// packageHasComponentDecls reports whether pkg.ComponentDecls contains any
// entry for pkgPath, regardless of whether that entry is a plain or receiver
// component — an import is worth offering as a qualifier as soon as it
// declares components, even if every one of them happens to be a receiver
// component excluded from the dot-free name list.
func packageHasComponentDecls(pkg *Package, pkgPath string) bool {
	for key := range pkg.ComponentDecls {
		if key.PackagePath == pkgPath {
			return true
		}
	}
	return false
}
