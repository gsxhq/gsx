package lsp

import (
	"go/types"
	"path/filepath"
	"sort"

	gsxast "github.com/gsxhq/gsx/ast"
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
// imported gsx package that declares any component (label = the import's
// LOCAL name — its alias when the import is aliased — insert = "name.",
// kind ciKindModule).
//
// qualifier != "": components of the import whose LOCAL name == qualifier
// (alias-aware; see importQualifierCandidates).
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
		if path, ok := importQualifierCandidates(pkg)[qualifier]; ok {
			return componentNameItems(pkg, path, text, start, end, enc)
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
// path), sorted by the import's local name for a deterministic list.
func importQualifierItems(pkg *Package, text string, start, end int, enc encoding) []CompletionItem {
	type qualifier struct{ name, path string }
	var quals []qualifier
	for name, path := range importQualifierCandidates(pkg) {
		if !packageHasComponentDecls(pkg, path) {
			continue
		}
		quals = append(quals, qualifier{name, path})
	}
	sort.Slice(quals, func(i, j int) bool { return quals[i].name < quals[j].name })
	items := make([]CompletionItem, 0, len(quals))
	for _, q := range quals {
		items = append(items, newCompletionItem(text, start, end, enc, q.name, q.name+".", ciKindModule, tierContext, q.path, nil))
	}
	return items
}

// importQualifierCandidates resolves the imports visible at a `<qualifier.`
// cursor as local-name -> import-path, dedup'd by local name.
//
// When pkg.Info is available (the common analyzed-package case) resolution
// walks the file scopes exactly as Go itself resolves a qualified identifier:
// in go/types, Info.Scopes records one scope per *ast.File (see
// fileScopeSet), and every import declaration — aliased or not — is bound in
// that file's scope as a *types.PkgName whose Name() is the LOCAL name (the
// alias when the import is aliased, the package's declared name otherwise)
// and whose Imported() is the imported *types.Package. Reading
// pkg.Types.Imports()[i].Name() instead (the package's DECLARED name) is
// blind to aliases — `import myui "example.com/ui"` would resolve as "ui",
// never "myui".
//
// When pkg.Info is nil (a shell / type-error package with no scope info to
// walk) this falls back to pkg.Types.Imports(), which cannot see aliases —
// a documented best-effort degradation; the alternative is offering nothing
// at all for such a package.
func importQualifierCandidates(pkg *Package) map[string]string {
	out := map[string]string{}
	if pkg.Info != nil {
		for scope := range fileScopeSet(pkg) {
			for _, name := range scope.Names() {
				if pn, ok := scope.Lookup(name).(*types.PkgName); ok {
					out[pn.Name()] = pn.Imported().Path()
				}
			}
		}
		return out
	}
	for _, imp := range pkg.Types.Imports() {
		out[imp.Name()] = imp.Path()
	}
	return out
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

// attrNameCompletion answers a ctxAttrName cursor (inside an open tag, on an
// attribute name, or in the whitespace before one): component-attribute
// candidates when the enclosing element is a planned component call;
// otherwise nothing yet (HTML attribute names are Task 15's to add — this
// same branch point is where they land).
//
// cc.element belongs to r.parsed — repairAtCursor's own parse/FileSet, built
// by Task 7's classifier — whose elements are NEVER stamped with IsComponent
// (that stamp is codegen's, applied only to the elements inside an analyzed
// Package's Files). Reading it here would always see the zero value. The
// bridge is elementAtTagOffset: locate the SAME element, by TagPos byte-offset
// equality, in one ephemeral analysis of the (possibly mid-edit) buffer — eph
// and r.parsed are independent parses of the identical r.src bytes, so their
// offsets always line up even though their node pointers never do (see
// TestElementAtTagOffset).
//
// There is deliberately no retained-package (s.pkgs[dir]) fallback here,
// unlike pipeFilters/componentDeclPackage: ComponentCalls is keyed by
// *gsxast.Element pointer, and every parse produces distinct pointers over
// the same bytes, so a stale retained Package could never key-match this
// cursor's element even by coincidence — falling back to it would only ever
// yield "no fact", never a stale-but-useful one. A shell ephemeral result
// therefore answers empty, not stale; recorded as a follow-up in the task-13
// report rather than worked around here.
func (s *Server) attrNameCompletion(cc completionContext, path, text string, off int, r repairResult) CompletionList {
	if cc.element == nil || r.fset == nil {
		return emptyCompletion()
	}
	eph, err := s.analyzer.AnalyzeEphemeral(filepath.Dir(path), path, r.src)
	if err != nil || eph == nil {
		return emptyCompletion()
	}
	tagOff := r.fset.Position(cc.element.TagPos).Offset
	ephEl := elementAtTagOffset(eph, path, tagOff)
	if ephEl == nil || !ephEl.IsComponent {
		// HTML tags land here too (ephEl != nil, IsComponent false) until Task 15
		// adds the HTML attribute-name table.
		return emptyCompletion()
	}
	start, end := completionTokenSpan(text, off, false)
	items := componentAttrItems(eph, ephEl, text, start, end, s.enc)
	if len(items) == 0 {
		return emptyCompletion()
	}
	return CompletionList{IsIncomplete: false, Items: items}
}

// elementAtTagOffset locates, in eph.Files[path], the element whose TagPos
// resolves (via eph.GSXFset) to the byte offset tagOff. It is the bridge
// between a *gsxast.Element from one parse of a buffer and the semantically
// equivalent element from an independent second parse of the identical
// bytes — the two parses never share node pointers, but every offset
// computed against unmoved source bytes is stable across them.
func elementAtTagOffset(eph *Package, path string, tagOff int) *gsxast.Element {
	if eph == nil || eph.GSXFset == nil || eph.Files[path] == nil {
		return nil
	}
	var found *gsxast.Element
	inspectWithEmbedded(eph.Files[path], func(n gsxast.Node) bool {
		if found != nil {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok {
			return true
		}
		if el.TagPos.IsValid() && eph.GSXFset.Position(el.TagPos).Offset == tagOff {
			found = el
			return false
		}
		return true
	})
	return found
}

// reservedComponentAttrName reports whether name is one of the three
// component-parameter names that are never attribute candidates: the
// ambient render context, the children slot, and the raw forwarded-attrs
// bag (see the gsx reserved-identifiers rule — ctx/children/attrs share one
// exclusion regardless of the syntax position asking).
func reservedComponentAttrName(name string) bool {
	switch name {
	case "ctx", "children", "attrs":
		return true
	default:
		return false
	}
}

// componentAttrItems enumerates unbound signature parameters as
// attribute-name candidates for a cursor on a planned component call's open
// tag. el must already be bridged into pkg's own parse (see
// elementAtTagOffset); pkg.ComponentCalls[el] is codegen's retained semantic
// fact for that exact call.
//
// candidates = fact.Signature's parameters, MINUS the reserved names
// (reservedComponentAttrName), MINUS every OTHER already-authored attribute's
// bound parameter name (fact.Params values' .Name). "Other" matters: a bound
// attribute whose OWN name span contains [start,end) — the cursor is
// literally mid-typing that very attribute's name, e.g. `<Card title` with
// the cursor right after "title", which the planner has already bound to the
// "title" parameter — stays offered. Excluding it would hide the one
// candidate the user is in the middle of accepting; see
// TestComponentAttrItemsCursorOnBoundAttrStaysOffered.
//
// label = param name, kind = ciKindField, detail = the param's type via
// qualifierFor, tier = tierContext. newText is the plain name — no `={}`
// snippet in v1, per the task-13 spec.
//
// No fact for el (the call was never planned — a broken tag, an unresolved
// target, ...) returns nil: fail-soft, never a guess.
func componentAttrItems(pkg *Package, el *gsxast.Element, text string, start, end int, enc encoding) []CompletionItem {
	if pkg == nil || el == nil {
		return nil
	}
	fact, ok := pkg.ComponentCalls[el]
	if !ok || fact.Signature == nil {
		return nil
	}

	bound := map[string]bool{}
	for attr, param := range fact.Params {
		if attrNameSpanContains(pkg, attr, start, end) {
			continue // cursor is on this very attribute's own token; keep it offered
		}
		bound[param.Name] = true
	}

	sig := fact.Signature
	qf := qualifierFor(pkg)
	items := make([]CompletionItem, 0, sig.Params().Len())
	for p := range sig.Params().Variables() {
		name := p.Name()
		if reservedComponentAttrName(name) || bound[name] {
			continue
		}
		detail := types.TypeString(p.Type(), qf)
		items = append(items, newCompletionItem(text, start, end, enc, name, name, ciKindField, tierContext, detail, nil))
	}
	return items
}

// attrNameSpanContains reports whether attr's own name span — [pos,
// pos+len(name)) in pkg.GSXFset coordinates — contains the completion token
// span [start,end), i.e. the cursor sits on the very attribute being
// inspected rather than on some other already-authored one.
func attrNameSpanContains(pkg *Package, attr gsxast.Attr, start, end int) bool {
	if pkg.GSXFset == nil || !attr.Pos().IsValid() {
		return false
	}
	name, ok := attrName(attr)
	if !ok {
		return false
	}
	nameStart := pkg.GSXFset.Position(attr.Pos()).Offset
	nameEnd := nameStart + len(name)
	return nameStart <= start && end <= nameEnd
}
