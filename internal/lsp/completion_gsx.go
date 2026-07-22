package lsp

import (
	"go/types"
	"path/filepath"
	"slices"
	"sort"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/tagcallable"
)

// filterItems builds one CompletionItem per resolved filter candidate for a
// ctxPipeStage cursor. filters is a package's resolved filter table
// (Package.Filters, already sorted by name); order is preserved verbatim —
// this function does no sorting or prefix filtering of its own (the client
// matches against label/filterText as the user types). Detail renders as
// "Pkg.Func", with a " (ctx)" suffix when the filter's leading argument is a
// context.Context (WantsCtx).
//
// kind is ciKindOperator, not ciKindFunction: editors (blink.cmp observed)
// auto-append "()" on accepting a Function/Method-kind item, but a bare pipe
// stage (`f`) and a parameterized one (`f()`) are different stages with
// different semantics — auto-inserting "()" the user did not ask for silently
// changes which stage gets authored.
func filterItems(filters []FilterCandidate, text string, start, end int, enc encoding) []CompletionItem {
	items := make([]CompletionItem, 0, len(filters))
	for _, f := range filters {
		detail := f.Pkg + "." + f.Func
		if f.WantsCtx {
			detail += " (ctx)"
		}
		items = append(items, newCompletionItem(text, start, end, enc, f.Name, f.Name, ciKindOperator, tierContext, detail, nil))
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

// tagCompletion answers a ctxTag cursor (`<▮` or `<qualifier.▮`): the merged
// component + HTML tag list. Components come from ComponentDecls (local and
// imported — see componentDeclPackage for the ephemeral→retained fallback and
// componentTagItems for the list); HTML tag names come from the vendored
// dataset (htmlTagItems).
//
// Merge rule: components always sort at tierContext; HTML tags sort at
// tierContext for a lowercase/empty prefix but at tierSecondary for a
// capitalized prefix (a capitalized prefix means the user is reaching for a
// PascalCase component, so HTML falls below). Only htmlTagItems' tier varies —
// hence componentTagItems is left at its hardcoded tierContext.
//
// HTML tags are dataset facts, not codegen facts, so they are offered even when
// componentDeclPackage comes back nil (analysis is a shell): only the COMPONENT
// half depends on the package. A qualified `<pkg.▮` cursor is component-only —
// HTML tags have no qualifier — so htmlTagItems is skipped there.
func (s *Server) tagCompletion(cc completionContext, path, text string, off int, r repairResult) CompletionList {
	start, end := completionTokenSpan(text, off, false)
	capitalizedPrefix := startsWithUpper(text[start:end])

	var items []CompletionItem
	if pkg := s.componentDeclPackage(filepath.Dir(path), path, r.src); pkg != nil {
		items = componentTagItems(pkg, cc.qualifier, capitalizedPrefix, text, start, end, s.enc)
	}
	if cc.qualifier == "" {
		items = append(items, htmlTagItems(capitalizedPrefix, text, start, end, s.enc)...)
	}
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
// Both branches additionally scan for component VALUES — package-scope
// vars/funcs (like `var X = named("x")` in a plain Go sibling package) that
// are never a `component`-keyword decl and so never appear in ComponentDecls,
// but are still a legal tag target: codegen's callable-universe shape — any
// package-scope types.Func or function-valued types.Var whose signature has
// one result assignable to gsx.Node, see internal/tagcallable's package doc —
// is the SAME predicate componentValueNameItems applies here, reimplemented
// as a package-scope scan (there is no enumerable table for it; codegen only
// probes a specific already-authored call site, via
// component_identity.go's componentResultType). componentValueNameItems
// additionally requires every parameter named, a completion-only exclusion
// layered on top — see tagCallableValueNames' doc for why. Without this scan,
// a pure-Go design-system package (icons, wrapped factories, ...) with zero
// `component`-keyword declarations completes to nothing at its tag/qualifier
// cursor even though its values compile fine as tags.
//
// capitalizedPrefix does not affect the tier of component items — every
// candidate here is tierContext regardless. Per the tag-merge rule (see
// tagCompletion), components always lead at tierContext; only the merged-in HTML
// tag list's tier flips with capitalization, and that decision lives in the
// caller / htmlTagItems, not here. The parameter is retained so the signature
// documents the merge contract and the tests pin that components stay tierContext
// either way.
func componentTagItems(pkg *Package, qualifier string, capitalizedPrefix bool, text string, start, end int, enc encoding) []CompletionItem {
	if pkg == nil || pkg.Types == nil {
		return nil
	}
	if qualifier != "" {
		path, ok := importQualifierCandidates(pkg)[qualifier]
		if !ok {
			return nil
		}
		items := componentNameItems(pkg, path, text, start, end, enc)
		if target := importedPackageAt(pkg, path); target != nil {
			items = append(items, componentValueNameItems(target, offeredNames(items), true, text, start, end, enc)...)
		}
		return items
	}
	items := componentNameItems(pkg, pkg.Types.Path(), text, start, end, enc)
	items = append(items, componentValueNameItems(pkg.Types, offeredNames(items), false, text, start, end, enc)...)
	items = append(items, importQualifierItems(pkg, text, start, end, enc)...)
	return items
}

// offeredNames extracts the label set already produced by componentNameItems,
// so componentValueNameItems can skip a name it would otherwise duplicate — a
// `component`-keyword decl's underlying Go func trivially also satisfies the
// callable-universe signature shape, so without this every such component
// would be offered twice.
func offeredNames(items []CompletionItem) map[string]bool {
	names := make(map[string]bool, len(items))
	for _, it := range items {
		names[it.Label] = true
	}
	return names
}

// importedPackageAt resolves the *types.Package object for one of pkg.Types'
// direct imports whose Path() == path. pkg.Types.Imports() only ever needs a
// linear scan (a file's import list is small), and — unlike
// importQualifierCandidates, which resolves local-name -> path — this needs
// the resolved package OBJECT to scan its Scope() below.
func importedPackageAt(pkg *Package, path string) *types.Package {
	if pkg.Types == nil {
		return nil
	}
	for _, imp := range pkg.Types.Imports() {
		if imp.Path() == path {
			return imp
		}
	}
	return nil
}

// componentValueNameItems enumerates target's package-scope identifiers that
// satisfy codegen's callable-universe tag shape (see componentTagItems' doc
// comment) and are not already in skip, as completion items.
func componentValueNameItems(target *types.Package, skip map[string]bool, exportedOnly bool, text string, start, end int, enc encoding) []CompletionItem {
	names := tagCallableValueNames(target, skip, exportedOnly)
	items := make([]CompletionItem, 0, len(names))
	for _, name := range names {
		items = append(items, newCompletionItem(text, start, end, enc, name, name, ciKindClass, tierContext, "", nil))
	}
	return items
}

// tagCallableValueNames returns target's package-scope identifiers — sorted,
// minus skip — that satisfy codegen's callable-universe tag shape: a
// types.Func or function-valued types.Var whose signature has one result
// assignable to gsx.Node (single-sourced from internal/tagcallable — see its
// package doc) AND every parameter named.
//
// The named-parameter half is NOT part of the shared tagcallable predicate:
// it is a deliberate, conservative choice specific to offering completion
// candidates, not a copy of some single codegen acceptance function. codegen
// itself only requires named parameters at the point it plans how markup
// attributes bind to a resolved call target — component_signature.go's
// analyzeComponentSignature (the "component-parameter-name" check), reached
// from component_positional_plan.go's operand planning
// (planComponentPositionalCalls) — which runs on one already-authored,
// already-resolved call site, never on a package-scope scan like this one.
// An unnamed parameter could never receive a markup attribute that way, so
// completion excludes it up front rather than offering a candidate codegen
// would reject at the call site; but nothing stops a future codegen change
// from accepting unnamed parameters through some other binding path this
// scan does not know about, without this file needing to track it — hence
// "deliberate choice", not "mirrored rule".
//
// exportedOnly gates on obj.Exported() — required when target is a DIFFERENT
// package than the one completion is running in (Go visibility), and false
// for target's own package (every package-scope identifier, exported or
// not, is a legal same-package tag).
//
// gsxNodeInterface resolving to nil (target does not import gsx.Node at all,
// directly or — for the tests' synthetic packages — because it has no
// imports set up) is a silent, fail-soft "no value candidates", not an error:
// most packages never define a component-shaped value.
func tagCallableValueNames(target *types.Package, skip map[string]bool, exportedOnly bool) []string {
	iface := gsxNodeInterface(target)
	if iface == nil {
		return nil
	}
	scope := target.Scope()
	var names []string
	for _, name := range scope.Names() {
		if skip[name] {
			continue
		}
		obj := scope.Lookup(name)
		if exportedOnly && !obj.Exported() {
			continue
		}
		var sig *types.Signature
		switch o := obj.(type) {
		case *types.Func:
			sig, _ = o.Type().(*types.Signature)
		case *types.Var:
			sig = tagcallable.Signature(o.Type())
		default:
			continue
		}
		if sig == nil || !isTagCallableSignature(sig, iface) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// isTagCallableSignature reports whether sig matches codegen's
// callable-universe shape — tagcallable.IsResult, single-sourced with
// codegen's component_identity.go — AND every parameter is named. See
// tagCallableValueNames' doc for why the named-parameter half stays a
// completion-local, deliberately conservative addition rather than part of
// the shared predicate.
func isTagCallableSignature(sig *types.Signature, node *types.Interface) bool {
	if !tagcallable.IsResult(sig, node) {
		return false
	}
	for param := range sig.Params().Variables() {
		if param.Name() == "" {
			return false
		}
	}
	return true
}

// gsxNodeInterface locates the gsx.Node interface type within target's OWN
// direct imports. target must import "github.com/gsxhq/gsx" itself for any
// of its declarations to type-check against gsx.Node in the first place, so
// this never needs to search transitively or reach into a different
// package's import graph (in particular, the LSP's own analyzed root
// package's imports are irrelevant — target is scanned as an independent
// package, per the go/types identity rule that every *types.Package in one
// checked build shares one canonical object per imported package). Returns
// nil (fail-soft) when target does not import gsx at all.
//
// This duplicates a lookup codegen also performs — component_signature.go's
// runtimeContractFromAnalysisPackage is the authority there, and its
// runtimeContract.node is exactly this same gsx.Node identity — but that
// helper is not reachable from here without either a layering violation
// (internal/lsp importing internal/codegen) or pulling in invariants this
// scan does not need: runtimeContractFromAnalysisPackage resolves Node,
// Attr, AND Attrs together for one fixed "the analysis package" and errors
// if any is missing or inconsistent, whereas this needs only Node, resolved
// against an arbitrary imported target package that is never itself "the
// analysis package". Folding the two would mean either exporting a
// narrower, Node-only codegen entry point (not part of this pass; a
// follow-up if the duplication proves troublesome) or accepting the
// unrelated Attr/Attrs requirement here. Kept local for now, with codegen's
// runtimeContract as the documented authority for the identity this mirrors.
func gsxNodeInterface(target *types.Package) *types.Interface {
	if target == nil {
		return nil
	}
	for _, imp := range target.Imports() {
		if imp.Path() != "github.com/gsxhq/gsx" {
			continue
		}
		tn, ok := imp.Scope().Lookup("Node").(*types.TypeName)
		if !ok {
			return nil
		}
		iface, ok := types.Unalias(tn.Type()).Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		return iface
	}
	return nil
}

// componentNameItems returns one item per plain (non-receiver) component
// declared in pkgPath, sorted by name for a deterministic list (ComponentDecls
// is a map; iteration order is not).
//
// kind is ciKindClass, not ciKindFunction: these items are offered in TAG
// position (`<▮`), read by the author as a tag/type name, not a call — and
// editors (blink.cmp observed) auto-append "()" on accepting a Function-kind
// item, which is wrong for a tag (`<Card()` is not valid markup).
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
		items = append(items, newCompletionItem(text, start, end, enc, name, name, ciKindClass, tierContext, "", nil))
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
// least one component (any ComponentDeclKey.PackagePath == the import's path)
// OR at least one tag-callable component VALUE (tagCallableValueNames — a
// pure-Go sibling package like an icon set, with zero `component`-keyword
// decls, still deserves a qualifier item), sorted by the import's local name
// for a deterministic list.
func importQualifierItems(pkg *Package, text string, start, end int, enc encoding) []CompletionItem {
	type qualifier struct{ name, path string }
	var quals []qualifier
	for name, path := range importQualifierCandidates(pkg) {
		if !packageHasComponentDecls(pkg, path) && !packageHasTagCallableValue(pkg, path) {
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
				// Skip the generated runtime imports (_gsxrt/_gsxctx): they are
				// bound in the file scope as PkgNames but are reserved internals,
				// never author-visible qualifiers (see isReservedGsxInternal).
				if isReservedGsxInternal(name) {
					continue
				}
				if pn, ok := scope.Lookup(name).(*types.PkgName); ok {
					out[pn.Name()] = pn.Imported().Path()
				}
			}
		}
		return out
	}
	for _, imp := range pkg.Types.Imports() {
		if isReservedGsxInternal(imp.Name()) {
			continue
		}
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

// packageHasTagCallableValue reports whether the import at pkgPath exports at
// least one package-scope identifier matching codegen's callable-universe tag
// shape (tagCallableValueNames) — the value-component counterpart of
// packageHasComponentDecls, for a pure-Go sibling package with zero
// `component`-keyword decls.
func packageHasTagCallableValue(pkg *Package, pkgPath string) bool {
	target := importedPackageAt(pkg, pkgPath)
	if target == nil {
		return false
	}
	return len(tagCallableValueNames(target, nil, true)) > 0
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
	if cc.element == nil {
		return emptyCompletion()
	}
	dir := filepath.Dir(path)
	// Best-effort ephemeral bridge. It serves two roles, both optional: (a)
	// confirming el is a planned COMPONENT call — the IsComponent stamp lives only
	// on codegen's own parse, never on the classification parse cc.element belongs
	// to — and (b) reading the dir's url-presets. A shell result (analyzer error,
	// or no matching element) simply routes to the HTML path, which needs neither.
	eph, err := s.analyzer.AnalyzeEphemeral(dir, path, r.src)
	if err != nil {
		eph = nil
	}
	var ephEl *gsxast.Element
	if eph != nil && r.fset != nil && cc.element.TagPos.IsValid() {
		tagOff := r.fset.Position(cc.element.TagPos).Offset
		ephEl = elementAtTagOffset(eph, path, tagOff)
	}

	// allowDash=true: hx-*, data-*, and aria-* attribute names carry '-', so the
	// token span must include it or the completion would replace only the tail
	// after the last dash. Component parameter names are Go identifiers with no
	// dash, so this is a no-op for the component path.
	start, end := completionTokenSpan(text, off, true)

	// Computed up front: both branches below need it — the component branch
	// to gate the forwarded-attrs-catch-all's hx-* candidates the same way
	// the plain HTML branch gates its own.
	htmxEnabled := slices.Contains(s.dirURLPresets(dir, eph), "htmx")

	if ephEl != nil && ephEl.IsComponent {
		items := componentAttrItems(eph, ephEl, htmxEnabled, text, start, end, s.enc)
		if len(items) == 0 {
			return emptyCompletion()
		}
		return CompletionList{IsIncomplete: false, Items: items}
	}

	// HTML element (confirmed non-component, or analysis is a shell): offer HTML
	// attribute names computed purely from the classification element — no codegen
	// facts needed, so this works even when the ephemeral analysis failed.
	items := htmlAttrItems(cc.element, cc.element.Tag, htmxEnabled, tierContext, text, start, end, s.enc)
	if len(items) == 0 {
		return emptyCompletion()
	}
	return CompletionList{IsIncomplete: false, Items: items}
}

// dirURLPresets resolves the url-attribute preset names in effect for dir. It
// prefers the already-computed ephemeral analysis (its URLPresets reflect the
// buffer's live config), falling back to the retained s.pkgs[dir] snapshot when
// the ephemeral came back a shell — preset names are a position-independent,
// package-wide config fact, so a stale retained list is a safe fallback. Both
// absent yields nil (htmx off).
func (s *Server) dirURLPresets(dir string, eph *Package) []string {
	if eph != nil && (eph.Info != nil || len(eph.URLPresets) > 0) {
		return eph.URLPresets
	}
	if pkg := s.pkgs[dir]; pkg != nil {
		return pkg.URLPresets
	}
	return nil
}

// attrValueCompletion answers a ctxAttrValue cursor (inside a StaticAttr's
// string value): the enumerated values allowed for this (tag, attribute) pair
// from the vendored dataset. This is a pure dataset lookup keyed on the
// classification element's tag and the attribute's name — no analyzer, no
// codegen facts — so it always works and always fails soft. A freeform
// attribute (no enumerated value set) yields an empty list.
//
// The Task 7 phantom heals (a `class=` empty value, an unclosed `"/>` patch)
// both surface as an empty Value with the cursor at the value start;
// completionTokenSpan then returns a zero-width span, so nothing is typed to
// filter on and every enumerated value is offered.
func (s *Server) attrValueCompletion(cc completionContext, text string, off int) CompletionList {
	if cc.element == nil || cc.attr == nil {
		return emptyCompletion()
	}
	name, ok := attrName(cc.attr)
	if !ok {
		return emptyCompletion()
	}
	start, end := completionTokenSpan(text, off, true)
	items := htmlValueItems(cc.element.Tag, name, text, start, end, s.enc)
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
// (reservedComponentAttrName), MINUS the Go-only variadic last parameter (if
// the signature is variadic — see the loop below), MINUS every OTHER
// already-authored attribute's bound parameter name (fact.Params values'
// .Name). "Other" matters: a bound
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
// When the signature also declares an "attrs" catch-all parameter
// (signatureHasAttrsCatchAll — component_signature.go's roleAttrs, e.g.
// `func(attrs ...gsx.Attr) gsx.Node`), the component forwards arbitrary
// attributes to whatever element it ultimately renders, so the candidate set
// is extended with the HTML GLOBAL attribute set (htmldata.GlobalAttributes,
// plus hx-* when htmxEnabled) via htmlAttrItems — the SAME boolean/insert/
// present-attr/own-token logic the plain-HTML attrTagName path uses, single-
// sourced rather than reimplemented here. There is no per-tag contribution:
// which concrete element receives the forwarded bag is unknowable from the
// call site (tagName ""), so only globals are offered — see htmlAttrItems'
// doc for how an unmatched tagName collapses to globals-only. Forwarded items
// sort at tierSecondary so the component's own named params (tierContext)
// lead; a name collision with an already-offered named param (e.g. a
// component that happens to declare a "class" prop) is skipped so the same
// label never appears twice with two different insert behaviors.
//
// No signature-with-attrs → unchanged: named params only. An unknown attr
// would be rejected by the planner at build time, so offering HTML attrs
// there would suggest invalid input.
//
// No fact for el (the call was never planned — a broken tag, an unresolved
// target, ...) returns nil: fail-soft, never a guess.
func componentAttrItems(pkg *Package, el *gsxast.Element, htmxEnabled bool, text string, start, end int, enc encoding) []CompletionItem {
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
	n := sig.Params().Len()
	items := make([]CompletionItem, 0, n)
	for i := range n {
		p := sig.Params().At(i)
		name := p.Name()
		if reservedComponentAttrName(name) || bound[name] {
			continue
		}
		// A variadic signature's last parameter is never markup-bindable.
		// Per analyzeComponentSignature in component_signature.go
		// (~L346-366), a variadic component param has exactly one of three
		// roles: attrs (reserved exact name "attrs"), children (reserved
		// exact name "children"), or Go-only variadic — everything else.
		// reservedComponentAttrName above already excludes "attrs" and
		// "children", so any variadic last param that survives to here is
		// necessarily Go-only, and the planner rejects markup binding to it
		// (component_positional_plan.go ~L303-304: "Go-only variadic
		// parameter %d was populated from markup"). Skip it rather than
		// offer a candidate guaranteed to break the call.
		if sig.Variadic() && i == n-1 {
			continue
		}
		detail := types.TypeString(p.Type(), qf)
		items = append(items, newCompletionItem(text, start, end, enc, name, name, ciKindField, tierContext, detail, nil))
	}

	if signatureHasAttrsCatchAll(sig) {
		named := offeredNames(items)
		for _, g := range htmlAttrItems(el, "", htmxEnabled, tierSecondary, text, start, end, enc) {
			if named[g.Label] {
				continue
			}
			items = append(items, g)
		}
	}
	return items
}

// signatureHasAttrsCatchAll reports whether sig declares a parameter named
// "attrs" — codegen's reserved forwarded-attrs-bag role
// (component_signature.go's roleAttrs). ComponentCalls only ever holds
// successfully PLANNED calls, so any signature reaching componentAttrItems
// already passed classifyAttrsParam's shape check (variadic `...gsx.Attr`,
// or a non-variadic `[]gsx.Attr`/defined-slice-of-Attr parameter — codegen's
// "underlying []gsx.Attr" structural rule); completion does not need to
// re-verify the type, only detect the reserved name, exactly as
// reservedComponentAttrName already treats "attrs" as reserved without
// inspecting its type.
func signatureHasAttrsCatchAll(sig *types.Signature) bool {
	for param := range sig.Params().Variables() {
		if param.Name() == "attrs" {
			return true
		}
	}
	return false
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
