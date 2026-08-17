package lsp

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pipeshape"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

type semanticDefinitionTarget struct {
	Authored sourceintel.Span
	Go       token.Position
}

func semanticDefinition(pkg *Package, path string, source []byte, offset int) (semanticDefinitionTarget, bool) {
	return semanticDefinitionFromSnapshot(pkg, path, source, offset, &requestSourceSnapshot{
		sources:   make(map[string]*capturedSource),
		openGSX:   make(map[string]struct{}),
		ownership: make(map[string]pairedGeneratedOwnership),
	})
}

func semanticDefinitionFromSnapshot(pkg *Package, path string, source []byte, offset int, sources *requestSourceSnapshot) (semanticDefinitionTarget, bool) {
	if pkg == nil || pkg.SourceIndex == nil || !pkg.SourceIndex.MatchesSource(path, source) {
		return semanticDefinitionTarget{}, false
	}
	occurrence, ok := pkg.SourceIndex.At(path, offset)
	if !ok || occurrence.Object == nil {
		return semanticDefinitionTarget{}, false
	}
	object := sourceintel.Origin(occurrence.Object)
	if authored, ok := pkg.SourceIndex.Definition(object); ok {
		return semanticDefinitionTarget{Authored: authored}, true
	}
	if pkg.Fset == nil || !object.Pos().IsValid() {
		return semanticDefinitionTarget{}, false
	}
	goPosition := pkg.objPosition(object.Pos())
	if goPosition.Filename == "" || !isNavigableTargetFile(goPosition.Filename) {
		return semanticDefinitionTarget{}, false
	}
	if sources == nil || sources.isPairedGeneratedOutput(goPosition.Filename) {
		return semanticDefinitionTarget{}, false
	}
	return semanticDefinitionTarget{Go: goPosition}, true
}

// navSpan is one navigable Go-fragment byte span of a gsx node: pos is the
// first byte of the (trimmed) fragment text in the .gsx, ln its byte length.
type navSpan struct {
	pos token.Pos
	ln  int
}

// nodeNavSpans returns a node's navigable Go-fragment spans plus its pipeline
// stages (whose name/args regions are matched separately). Every span's source
// bytes spell exactly the stored fragment text — the parser's byte-faithful
// invariant — so cursors bridge into the skeleton by relative offset. A node
// can carry more than one span (a ComposedPart's expr and its `: cond` guard);
// spans never overlap across nodes, so at most one (node, span) matches an
// offset. Nodes with no navigable fragment return nil.
func nodeNavSpans(n gsxast.Node) (spans []navSpan, stages []gsxast.PipeStage) {
	switch e := n.(type) {
	case *gsxast.Interp:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.ExprAttr:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.SpreadAttr:
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.OrderedPair:
		return []navSpan{{e.Pos(), len(e.Value)}}, nil
	case *gsxast.ComposedPart:
		if e.CF != nil {
			return nil, nil // the CF's own nodes carry the spans
		}
		if e.LiteralSegments == nil {
			spans = append(spans, navSpan{e.ExprPos, len(e.Expr)})
			stages = e.Stages
		}
		if e.Cond != "" {
			spans = append(spans, navSpan{e.CondPos, len(e.Cond)})
		}
		return spans, stages
	case *gsxast.ValueArm:
		if e.Segments != nil {
			return nil, nil // a literal arm's holes carry their own spans
		}
		return []navSpan{{e.ExprPos, len(e.Expr)}}, e.Stages
	case *gsxast.ValueIf:
		return []navSpan{{e.CondPos, len(e.Cond)}}, nil
	case *gsxast.ValueSwitch:
		return []navSpan{{e.TagPos, len(e.Tag)}}, nil
	case *gsxast.ValueSwitchCase:
		return []navSpan{{e.ListPos, len(e.List)}}, nil
	case *gsxast.CondAttr:
		return []navSpan{{e.CondPos, len(e.Cond)}}, nil
	case *gsxast.SwitchAttr:
		return []navSpan{{e.TagPos, len(e.Tag)}}, nil
	case *gsxast.AttrCaseClause:
		return []navSpan{{e.ListPos, len(e.List)}}, nil
	case *gsxast.SwitchMarkup:
		return []navSpan{{e.TagPos, len(e.Tag)}}, nil
	case *gsxast.CaseClause:
		return []navSpan{{e.ListPos, len(e.List)}}, nil
	case *gsxast.ForMarkup:
		return []navSpan{{e.ClausePos, len(e.Clause)}}, nil
	case *gsxast.IfMarkup:
		return []navSpan{{e.CondPos, len(e.Cond)}}, nil
	case *gsxast.GoBlock:
		return []navSpan{{e.CodePos, len(e.Code)}}, nil
	}
	return nil, nil
}

// exprNodeAtOffset returns the gsx node one of whose Go-fragment spans (see
// nodeNavSpans) contains the byte offset, together with the matched span's
// start position (so the caller can both bridge by relative offset and tell
// WHICH fragment of a multi-span node matched). Returns (nil, token.NoPos)
// when no node covers the offset. Fragment spans never nest across nodes, so
// at most one (node, span) matches — the walk's last-write-wins is unambiguous.
func exprNodeAtOffset(pkg *Package, path string, off int) (gsxast.Node, token.Pos) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return nil, token.NoPos
	}
	var found gsxast.Node
	var foundPos token.Pos
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if n == nil {
			return false
		}
		spans, stages := nodeNavSpans(n)
		if len(spans) == 0 {
			return true
		}
		for _, s := range spans {
			if !s.pos.IsValid() {
				continue
			}
			start := pkg.GSXFset.Position(s.pos).Offset
			if off >= start && off < start+s.ln {
				found = n
				foundPos = s.pos
				return true
			}
		}
		// Also match pipeline stage positions (filter name, filter args) so that
		// pipedTarget can be dispatched for those cursor positions too. The
		// reported position is the primary (seed) span's start, matching what
		// pipedTarget expects.
		if !spans[0].pos.IsValid() {
			return true
		}
		for _, st := range stages {
			if st.NamePos.IsValid() {
				nameStart := pkg.GSXFset.Position(st.NamePos).Offset
				if off >= nameStart && off < nameStart+len(st.Name) {
					found = n
					foundPos = spans[0].pos
					return true
				}
			}
			if st.HasArgs && st.ArgsPos.IsValid() {
				argsStart := pkg.GSXFset.Position(st.ArgsPos).Offset
				if off >= argsStart && off < argsStart+len(st.Args) {
					found = n
					foundPos = spans[0].pos
					return true
				}
			}
		}
		return true
	})
	return found, foundPos
}

// signatureTypeIdentAt resolves a cursor sitting on an identifier inside a
// component-signature TYPE — a parameter type (e.g. `store.Comment` in
// `component C(c []store.Comment)`) or a method receiver type — to that
// identifier's go/types object. It walks the file's components, finds the
// signature-type span covering off (via pkg.SigTypes), bridges the cursor into
// the type-checked skeleton type expression by relative byte offset (the
// skeleton copies the type verbatim, so the bytes align), and resolves the
// innermost identifier through go/types. gsxStart/idLen are the identifier's
// byte span back in the .gsx file (for the hover range). Returns ok=false when
// the cursor is not on a signature-type identifier or it does not resolve.
func signatureTypeIdentAt(pkg *Package, path string, off int) (obj types.Object, gsxStart, idLen int, ok bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil || pkg.Info == nil || pkg.SigTypes == nil {
		return nil, 0, 0, false
	}
	for _, d := range f.Decls {
		c, isComp := d.(*gsxast.Component)
		if !isComp {
			continue
		}
		for _, r := range pkg.SigTypes[c] {
			start := pkg.GSXFset.Position(r.GSXPos).Offset
			if off < start || off >= start+r.Len {
				continue
			}
			skelPos := r.SkelTyp.Pos() + token.Pos(off-start)
			id := innermostIdent(r.SkelTyp, skelPos)
			if id == nil {
				return nil, 0, 0, false
			}
			o := pkg.Info.Uses[id]
			if o == nil {
				o = pkg.Info.Defs[id]
			}
			if o == nil {
				return nil, 0, 0, false
			}
			// The identifier's .gsx span: its offset within the (verbatim) skeleton
			// type equals its offset within the .gsx type, so add it to the type start.
			gs := start + int(id.Pos()-r.SkelTyp.Pos())
			return o, gs, len(id.Name), true
		}
	}
	return nil, 0, 0, false
}

// signatureTypeDefinition builds the textDocument/definition reply for an
// identifier resolved inside a component-signature parameter type. A package
// qualifier (a *types.PkgName) jumps into the imported package — a list of the
// `package` clauses of its files, like gopls — rather than back to the import
// site. Any other object jumps to its single declaration. Returns nil (→ null)
// when there is no real source target.
func signatureTypeDefinition(sources *requestSourceSnapshot, pkg *Package, obj types.Object) any {
	if pn, ok := obj.(*types.PkgName); ok {
		if locs := packageLocations(sources, pn.Imported(), pkg.objPosition); len(locs) > 0 {
			return locs
		}
		return nil
	}
	if result, ok := objectDefinitionResult(sources, pkg, obj); ok {
		return result
	}
	return nil
}

func objectDefinitionResult(sources *requestSourceSnapshot, pkg *Package, obj types.Object) (any, bool) {
	if pkg == nil || obj == nil {
		return nil, false
	}
	object := sourceintel.Origin(obj)
	if pkg.SourceIndex != nil {
		if span, ok := pkg.SourceIndex.Definition(object); ok {
			text, available := sources.sourceText(span.Path)
			if !available || !pkg.SourceIndex.MatchesSource(span.Path, text) {
				return nil, false
			}
			return sources.locationForSpan(span)
		}
	}
	if object.Pkg() != nil {
		key := ComponentDeclKey{PackagePath: object.Pkg().Path(), ComponentKey: componentObjectKey(object)}
		if decls := pkg.ComponentDecls[key]; len(decls) > 0 {
			// This object is a component with tracked declaration spans; those
			// versioned spans are the authoritative, stale-validated answer. Return
			// them (failing closed when they no longer match the current buffer)
			// rather than falling through to resolve the raw object position against
			// possibly-stale source.
			return versionedDefinitionResult(sources, decls)
		}
	}
	// A plain package-level value/symbol (a component VALUE like `icon.X`, or a Go
	// helper) has no tracked component spans: resolve its declaring position —
	// authored .gsx when present, else the physical generated file.
	if location, ok := objectSourceLocation(sources, pkg, object); ok {
		return location, true
	}
	return nil, false
}

func componentObjectKey(object types.Object) string {
	function, ok := object.(*types.Func)
	if !ok {
		return ""
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return "." + function.Name()
	}
	receiver := types.Unalias(signature.Recv().Type())
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = types.Unalias(pointer.Elem())
	}
	if named, ok := receiver.(*types.Named); ok && named.Obj() != nil {
		return named.Obj().Name() + "." + function.Name()
	}
	return ""
}

// packageLocations returns the `package` clause location of every file in imp
// that declares a package-level object — gopls's answer for go-to-definition on
// a package name. Files are discovered from the package scope's objects (so a
// file declaring nothing package-level is not listed) and sorted for stable
// output. Returns nil when imp is nil or no source files can be located (e.g. a
// dependency available only as export data without file positions).
func packageLocations(sources *requestSourceSnapshot, imp *types.Package, resolve func(token.Pos) token.Position) []Location {
	if imp == nil || resolve == nil {
		return nil
	}
	files := map[string]bool{}
	scope := imp.Scope()
	for _, name := range scope.Names() {
		o := scope.Lookup(name)
		if o == nil || !o.Pos().IsValid() {
			continue
		}
		fn := resolve(o.Pos()).Filename
		if strings.HasSuffix(fn, ".go") {
			files[fn] = true
		}
	}
	sorted := make([]string, 0, len(files))
	for fn := range files {
		sorted = append(sorted, fn)
	}
	sort.Strings(sorted)
	var locs []Location
	for _, fn := range sorted {
		if loc, ok := packageClauseLocation(sources, fn); ok {
			locs = append(locs, loc)
		}
	}
	return locs
}

// packageClauseLocation returns the location of the package-name identifier in
// the `package X` clause of the Go file at filename (what go-to-definition on a
// package qualifier should land on). Returns ok=false if the file cannot be read
// or parsed.
func packageClauseLocation(sources *requestSourceSnapshot, filename string) (Location, bool) {
	data, ok := sources.sourceText(filename)
	if !ok {
		return Location{}, false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, data, parser.PackageClauseOnly)
	if err != nil || f.Name == nil {
		return Location{}, false
	}
	p := fset.Position(f.Name.Pos())
	return sources.locationForGoPosition(p, len(f.Name.Name))
}

// importDefAt answers go-to-definition for a cursor on an import statement in a
// .gsx file (the package path or its alias): it jumps into the imported package,
// returning the `package` clauses of its files — the same picker as a package
// qualifier in a parameter type. The second result reports whether the cursor
// was on an import spec at all (so the caller stops dispatching even when the
// import does not resolve). gsx imports live in top-level GoChunks, so the
// chunk under the cursor is re-parsed for its import specs.
func importDefAt(sources *requestSourceSnapshot, pkg *Package, path string, off int) (any, bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil || pkg.Types == nil {
		return nil, false
	}
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		start := pkg.GSXFset.Position(gc.Pos()).Offset
		if off < start || off >= start+len(gc.Src) {
			continue
		}
		impPath, found := importPathAtOffset(gc.Src, off-start)
		if !found {
			return nil, false
		}
		if tpkg := importedPackageByPath(pkg.Types, impPath); tpkg != nil {
			if locs := packageLocations(sources, tpkg, pkg.objPosition); len(locs) > 0 {
				return locs, true
			}
		}
		return nil, true // on an import spec, but it did not resolve to source
	}
	return nil, false
}

// importPathAtOffset re-parses a GoChunk's source (the verbatim Go after the
// .gsx package line) and returns the import path of the import spec covering the
// byte offset relOff within that source — matching either the path string or the
// alias. ok is false if relOff is not on an import spec or the chunk's imports
// do not parse.
func importPathAtOffset(src string, relOff int) (string, bool) {
	const prefix = "package _\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src, parser.ImportsOnly)
	if err != nil {
		return "", false
	}
	target := relOff + len(prefix)
	for _, imp := range f.Imports {
		lo := fset.Position(imp.Pos()).Offset
		hi := fset.Position(imp.End()).Offset
		if target >= lo && target < hi {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				return "", false
			}
			return p, true
		}
	}
	return "", false
}

// importedPackageByPath returns the direct import of p whose path is importPath,
// or nil. Used to resolve a .gsx import spec to the analyzed package's
// already-type-checked dependency.
func importedPackageByPath(p *types.Package, importPath string) *types.Package {
	for _, imp := range p.Imports() {
		if imp.Path() == importPath {
			return imp
		}
	}
	return nil
}

// hasPipeStages reports whether the gsx expression node carries pipeline stages
// (`{ x |> f }`). Such nodes lower to a wrapped call in the skeleton, breaking
// the byte-identical relative-offset bridge go-to-def relies on — they resolve
// through pipedTarget instead.
func hasPipeStages(n gsxast.Node) bool {
	return len(pipeshape.Stages(n)) > 0
}

// isCtrlSpan reports whether the matched span (see exprNodeAtOffset) resolves
// through the CtrlMap bridge — a control-flow clause emitted verbatim in
// statement position — rather than the ExprMap expression bridge. For a
// ComposedPart the two coexist: its `: cond` guard is a ctrl span while its expr
// is an ExprMap span, so the matched position discriminates.
func isCtrlSpan(node gsxast.Node, matched token.Pos) bool {
	switch e := node.(type) {
	case *gsxast.ForMarkup, *gsxast.IfMarkup, *gsxast.GoBlock,
		*gsxast.ValueIf, *gsxast.ValueSwitch, *gsxast.ValueSwitchCase,
		*gsxast.CondAttr, *gsxast.SwitchMarkup, *gsxast.CaseClause,
		*gsxast.SwitchAttr, *gsxast.AttrCaseClause:
		return true
	case *gsxast.ComposedPart:
		return e.CondPos.IsValid() && matched == e.CondPos
	}
	return false
}

func versionedDefinitionResult(sources *requestSourceSnapshot, spans []sourceintel.VersionedSpan) (any, bool) {
	if len(spans) == 0 {
		return nil, false
	}
	locations := make([]Location, 0, len(spans))
	for _, span := range spans {
		location, ok := sources.locationForVersionedSpan(span)
		if !ok {
			return nil, false
		}
		locations = append(locations, location)
	}
	if len(locations) == 1 {
		return locations[0], true
	}
	return locations, true
}

// objectSourceLocation resolves a go/types object's declaring position to a
// navigation target under the authored-first policy:
//
//   - The //line-adjusted position (pkg.Fset.Position) points at an authored .gsx
//     when the object is a component VALUE (or any top-level Go symbol) declared
//     in a Go chunk of a .gsx analyzed from sources — its own package, or a
//     dependency whose .gsx ships beside its generated .x.go (same module, a
//     nested module, or a module cache with sources). When that .gsx exists, jump
//     there.
//   - Otherwise fall back to the PHYSICAL position (Fset.PositionFor without
//     //line adjustment): the real .go / .x.go the object was type-checked from.
//     This covers an external dependency shipping only its generated .x.go (the
//     authored .gsx absent), where a generated-file location beats null.
//
// locationForExistingFile still rejects a paired generated .x.go while its
// authored .gsx exists, so navigation never lands in generated code when the
// source is available. Same-package authored objects are resolved by callers via
// SourceIndex before reaching here.
func objectSourceLocation(sources *requestSourceSnapshot, pkg *Package, object types.Object) (Location, bool) {
	if pkg == nil || pkg.Fset == nil || object == nil || !object.Pos().IsValid() {
		return Location{}, false
	}
	if adjusted := pkg.objPosition(object.Pos()); strings.HasSuffix(adjusted.Filename, ".gsx") {
		if location, ok := sources.locationForExistingFile(adjusted, len(object.Name())); ok {
			return location, true
		}
	}
	physical := pkg.objPositionPhysical(object.Pos())
	return sources.locationForExistingFile(physical, len(object.Name()))
}

// handleDefinition answers textDocument/definition for D1: a Go symbol under the
// cursor that resolves to a definition in a real .go file. When the retained
// package answers nothing — most often because the live buffer is mid-edit and
// unparseable, so analysis fell back to a diagnostics-only shell — it retries the
// same resolver cascade against one ephemeral analysis of the completion-repaired
// buffer (definitionFallback), healing go-to-definition on a half-typed tag.
func (s *Server) handleDefinition(f frame) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	if !s.diskViewValid {
		return s.reply(f.ID, nil)
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	sources := s.sourceSnapshot()
	if strings.HasSuffix(path, ".go") {
		return s.handleGoDefinition(f, path, sources)
	}
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, nil)
	}
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	pkg := s.pkgs[filepath.Dir(path)]
	if result, answered := s.definitionAnswerFromPkg(pkg, path, []byte(text), off, sources); answered {
		return s.reply(f.ID, result)
	}
	if result, answered := s.definitionFallback(path, text, off, sources); answered {
		return s.reply(f.ID, result)
	}
	return s.reply(f.ID, nil)
}

// definitionAnswerFromPkg runs the go-to-definition resolver cascade against one
// package. It is the shared body called first with the retained package (exactly
// the prior handler behavior, including every fail-closed guard) and again with
// an ephemeral package on a total miss. answered=true means a cursor branch
// matched and result is the reply to send (possibly nil when the branch matched
// but resolved nothing); answered=false means no branch matched, so the caller
// may try the fallback. source is the exact bytes pkg was analyzed from — live
// buffer for the retained pass, the repaired buffer for the ephemeral pass — so
// the SourceIndex staleness guards compare against the right text.
func (s *Server) definitionAnswerFromPkg(pkg *Package, path string, source []byte, off int, sources *requestSourceSnapshot) (any, bool) {
	if pkg == nil || pkg.Info == nil {
		return nil, false
	}

	// D2: exact call-target identity from codegen. For a GSX build-variant
	// family, retain the existing multi-location picker after the exact target
	// establishes that this authored element is a component call.
	if cursor, ok := componentTargetAtOffset(pkg, path, off); ok {
		if result, valid := versionedDefinitionResult(sources, cursor.fact.TargetDecls); valid {
			return result, true
		}
		location, ok := objectSourceLocation(sources, pkg, componentTargetObject(cursor.fact))
		if !ok {
			return nil, true
		}
		return location, true
	}

	// D2: cursor on a component tag name in a .gsx file → jump to the component
	// declaration(s). A single variant replies with a plain Location (unchanged
	// wire shape); multiple build-tag variants (Task 7) reply with a []Location
	// so the editor shows a picker — both are valid textDocument/definition results.
	// This is the branch that answers when the package's positional call planning
	// was skipped (any unrelated type error does that): the D1 branch above needs
	// a ComponentCalls fact, which planning is the only producer of, while the
	// tag→component index edge this branch reads is planning-independent.
	if decls, ok := componentTagDeclAt(pkg, path, source, off); ok {
		locs := appendSpanLocations(nil, sources, decls)
		if len(locs) == 0 {
			return nil, true
		}
		if len(locs) == 1 {
			return locs[0], true
		}
		return locs, true
	}

	// A/C: cursor on a component-invocation attribute name → the matching component
	// parameter (same-package function components and cross-package dotted tags).
	if cursor, ok := componentAttrAtOffset(pkg, path, off); ok {
		if result, valid := versionedDefinitionResult(sources, cursor.fact.ParamDecls[cursor.param.Ordinal]); valid {
			return result, true
		}
		param := cursor.param.Origin
		if param == nil {
			param = cursor.param.Var
		}
		location, ok := objectSourceLocation(sources, pkg, param)
		if !ok {
			return nil, true
		}
		return location, true
	}

	// E: cursor on an identifier inside a component-signature parameter TYPE
	// (e.g. `store.Comment` in `component C(c []store.Comment)`) → the Go
	// definition of that identifier: a type name jumps to its declaration; a
	// package qualifier jumps into the package (its files' `package` clauses,
	// gopls-style). A cursor on a signature type that does not resolve replies
	// null rather than falling through to expression resolution.
	if obj, _, _, ok := signatureTypeIdentAt(pkg, path, off); ok {
		return signatureTypeDefinition(sources, pkg, obj), true
	}

	// F: cursor on an import statement in the .gsx → into the imported package
	// (its files' `package` clauses), the same picker as a type qualifier.
	if res, ok := importDefAt(sources, pkg, path, off); ok {
		return res, true
	}

	if target, ok := exprDefinitionTargetAt(pkg, path, off); ok {
		result, ok := objectDefinitionResult(sources, pkg, target.object)
		if !ok && target.position.Filename != "" {
			result, ok = sources.locationForResolvedPosition(target.position, 0)
		}
		if !ok {
			return nil, true
		}
		return result, true
	}
	if target, ok := semanticDefinitionFromSnapshot(pkg, path, source, off, sources); ok {
		if target.Authored.Path != "" {
			if location, ok := sources.locationForSpan(target.Authored); ok {
				return location, true
			}
			return nil, true
		}
		location, ok := sources.locationForExistingFile(target.Go, 0)
		if !ok {
			return nil, true
		}
		return location, true
	}
	return nil, false
}

// definitionFallback answers go-to-definition from a completion-repaired,
// ephemerally analyzed buffer when the retained package matched nothing. It
// repairs the (mid-edit) buffer at the cursor with the same closed patch set
// completion uses, runs one warm uncached analysis, and re-runs the resolver
// cascade against it. Current-file byte spans the cascade returns are in repaired
// coordinates, so the snapshot is armed to map them back to the live buffer
// (setRepair). answered=false — reply null as before — whenever the buffer is
// unrepairable, analysis errors, or the result is a shell with neither type info
// nor a parse.
func (s *Server) definitionFallback(path, text string, off int, sources *requestSourceSnapshot) (any, bool) {
	r, insertOff, queryOff, ok := navRepair(path, text, off)
	if !ok {
		return nil, false
	}
	// Non-blocking: this fallback runs inline on the dispatch goroutine after the
	// retained-snapshot primary missed. Under contention (acquired=false) just
	// skip the ephemeral pass and reply null (answered=false), the pre-mid-edit-nav
	// behavior — never block the whole server behind a background analysis.
	eph, acquired, err := s.analyzer.AnalyzeEphemeralNonBlocking(filepath.Dir(path), path, r.src)
	if !acquired || err != nil || eph == nil || (eph.Info == nil && eph.Files == nil) {
		return nil, false
	}
	sources.setRepair(path, insertOff, len(r.patch))
	defer sources.clearRepair()
	return s.definitionAnswerFromPkg(eph, path, r.src, queryOff, sources)
}

type resolvedDefinitionTarget struct {
	object   types.Object
	position token.Position
}

func exprDefinitionTargetAt(pkg *Package, path string, off int) (resolvedDefinitionTarget, bool) {
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		return resolvedDefinitionTarget{}, false
	}
	if isCtrlSpan(node, exprPos) {
		obj, _, _, ok := ctrlObjectAt(pkg, node, exprPos, off)
		return resolvedTargetForObject(pkg, obj, ok)
	}
	if hasPipeStages(node) {
		obj, _, ok := pipedTarget(pkg, node, exprPos, off)
		return resolvedTargetForObject(pkg, obj, ok)
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		return resolvedDefinitionTarget{}, false
	}
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)
	id := innermostIdent(skel, skelPos)
	if id == nil {
		return resolvedDefinitionTarget{}, false
	}
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	return resolvedTargetForObject(pkg, obj, obj != nil)
}

func resolvedTargetForObject(pkg *Package, obj types.Object, ok bool) (resolvedDefinitionTarget, bool) {
	if !ok || obj == nil || !obj.Pos().IsValid() || pkg == nil || pkg.Fset == nil {
		return resolvedDefinitionTarget{}, false
	}
	position := pkg.objPosition(obj.Pos())
	if position.Filename == "" {
		return resolvedDefinitionTarget{}, false
	}
	return resolvedDefinitionTarget{object: obj, position: position}, true
}

// exprDefinitionAt answers go-to-definition for a cursor inside any Go-fragment
// span of a .gsx file (see nodeNavSpans): ctrl spans resolve through CtrlMap,
// pipelined expressions through pipedTarget, and plain expressions through the
// ExprMap byte-identical bridge. Ctrl spans are checked first: a ComposedPart's
// `: cond` guard must resolve via CtrlMap even when the part's EXPR carries a
// pipeline. ok=false when no span covers the offset or nothing resolves to a
// real source location.
func exprDefinitionAt(pkg *Package, path string, off int) (token.Position, bool) {
	target, ok := exprDefinitionTargetAt(pkg, path, off)
	if !ok {
		return token.Position{}, false
	}
	return target.position, true
}

// ctrlObjectAt resolves a cursor inside a CtrlMap-bridged span (see
// isCtrlSpan: control-flow clauses, switch tags and case lists, in-tag
// conditional-attribute conds, class guard conds, and value-form control
// expressions) to the go/types object of the identifier under the cursor,
// plus that identifier's byte span in the .gsx (for hover highlight ranges).
// It bridges the cursor into the skeleton via the node's CtrlMap entry — the
// clause bytes are emitted verbatim, so relative offsets align both ways.
// ok=false when the node has no CtrlMap entry or no identifier resolves.
func ctrlObjectAt(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (obj types.Object, identStart, identLen int, ok bool) {
	cr, found := pkg.CtrlMap[node]
	if !found || cr.Node == nil || pkg.Info == nil {
		return nil, 0, 0, false
	}
	clauseStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := cr.ClauseStart + token.Pos(off-clauseStart)
	id := innermostIdent(cr.Node, skelPos)
	if id == nil {
		return nil, 0, 0, false
	}
	obj = pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	if obj == nil {
		return nil, 0, 0, false
	}
	return obj, clauseStart + int(id.Pos()-cr.ClauseStart), len(id.Name), true
}

// ctrlDefinitionPos resolves a cursor inside a CtrlMap-bridged span to the
// defining object's source position. The request source snapshot performs the
// exact paired-generated-output ownership check before publishing a location.
func ctrlDefinitionPos(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (token.Position, bool) {
	obj, _, _, ok := ctrlObjectAt(pkg, node, exprPos, off)
	if !ok || !obj.Pos().IsValid() {
		return token.Position{}, false
	}
	dp := pkg.objPosition(obj.Pos())
	if dp.Filename == "" {
		return token.Position{}, false
	}
	return dp, true
}

// handleGoDefinition answers definition for a cursor in a .go file from the
// module symbol graph: any dir in the module, a Go-only package that imports a
// gsx package included. The graph answers with everything it knows — a .gsx
// declaration and a .go one alike; the editor merges this with gopls' answer.
// A build-tag variant family has one declaration per variant, so the reply is a
// []Location picker; a single declaration replies with a plain Location.
func (s *Server) handleGoDefinition(f frame, path string, sources *requestSourceSnapshot) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, nil)
	}
	source, _ := sources.sourceText(path) // same captured source, validated above
	dir := filepath.Dir(path)
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	graph := s.moduleSymbolGraph(sources, dir)
	key, found := symbolDefinitionAt(graph, path, source, off)
	if !found {
		graph = packageSymbolGraph(s.pkgs[dir])
		key, found = symbolDefinitionAt(graph, path, source, off)
	}
	if !found {
		return s.reply(f.ID, nil)
	}
	locations := appendSpanLocations(nil, sources, graph.Definitions(key))
	switch len(locations) {
	case 0:
		return s.reply(f.ID, nil)
	case 1:
		return s.reply(f.ID, locations[0])
	default:
		return s.reply(f.ID, locations)
	}
}

// componentTagDeclAt checks whether the byte offset off in the .gsx file at
// path sits on the name portion of a same-package component element tag (e.g.
// the "Card" in "<Card .../>" or in its closing "</Card>", or a lowercase
// "card" resolving to a package-level declaration — el.IsComponent is the
// codegen-stamped answer, not a syntactic capital-letter guess). Dotted tags
// are excluded here: a same-package build-tag variant family is what this
// branch's multi-span picker exists for, and a dotted tag names one imported
// declaration, which the index-backed semantic branch at the end of the cascade
// (semanticDefinitionFromSnapshot) already answers from the same occurrence.
// The declarations come from the package symbol graph, so a build-tag variant
// family yields one span per variant. source must be the exact bytes pkg was
// analyzed from: the index is offset-addressed, so stale bytes name nothing.
func componentTagDeclAt(pkg *Package, path string, source []byte, off int) ([]sourceintel.Span, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil || pkg.SourceIndex == nil {
		return nil, false
	}
	file := pkg.Files[path]
	if file == nil {
		return nil, false
	}
	nameStart, nameLen, ok := componentTagNameAt(pkg, file, off)
	if !ok {
		return nil, false
	}
	// The authored tag name is not Go, so the type checker never saw it: the
	// index carries the tag site as an explicit occurrence of the component's
	// object (codegen's gsxExtraOccurrences). Resolving the OPENING tag-name
	// offset serves a cursor on either tag, which is why the walk normalizes
	// closing tags onto it.
	graph := packageSymbolGraph(pkg)
	key, resolved := symbolAt(graph, path, source, nameStart)
	if !resolved {
		return nil, false
	}
	occurrence, found := pkg.SourceIndex.At(path, nameStart)
	if !found || occurrence.Span.Start != nameStart || occurrence.Span.End != nameStart+nameLen {
		return nil, false
	}
	if _, isFunc := occurrence.Object.(*types.Func); !isFunc {
		return nil, false // the offset names something other than the component
	}
	spans := graph.Definitions(key)
	if len(spans) == 0 {
		return nil, false
	}
	return spans, true
}

// componentTagNameAt reports the OPENING tag-name span of the same-package
// component element whose opening or closing tag name covers off.
func componentTagNameAt(pkg *Package, file *gsxast.File, off int) (nameStart, nameLen int, ok bool) {
	inspectWithEmbedded(file, func(n gsxast.Node) bool {
		if ok {
			return false
		}
		el, isElement := n.(*gsxast.Element)
		if !isElement || el.Tag == "" || strings.Contains(el.Tag, ".") || !el.IsComponent {
			return true
		}
		// The opening tag name starts right after the '<'.
		start := pkg.GSXFset.Position(el.Pos()).Offset + 1
		onOpen := off >= start && off < start+len(el.Tag)
		// The closing tag name (the "Card" in "</Card>") resolves the same way,
		// so go-to-definition works from either end of the element.
		onClose := false
		if el.CloseNamePos.IsValid() {
			closeStart := pkg.GSXFset.Position(el.CloseNamePos).Offset
			onClose = off >= closeStart && off < closeStart+len(el.Tag)
		}
		if onOpen || onClose {
			nameStart, nameLen, ok = start, len(el.Tag), true
		}
		return true
	})
	return nameStart, nameLen, ok
}
