package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pipeshape"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

// componentCanonicalizer maps a split component's analysis-only body func — and
// its parameters, receiver and type parameters, positionally — onto the
// authored declaration's objects, so tag sites, attr bindings, Go callers and
// body uses all key to one symbol.
//
// publicByKey holds, per logical component key, the object of the declaration
// named exactly as the component is authored (analyze's publicComponentObj).
// nil is returned when nothing needs aliasing, which keeps the index's
// canonical hook at its identity fast path.
func componentCanonicalizer(objKey map[types.Object]string, publicByKey map[string]types.Object) func(types.Object) types.Object {
	alias := map[types.Object]types.Object{}
	for object, key := range objKey {
		public := publicByKey[key]
		if public == nil || public == object {
			continue
		}
		alias[object] = public
		bodySignature, bodyOK := object.Type().(*types.Signature)
		publicSignature, publicOK := public.Type().(*types.Signature)
		if !bodyOK || !publicOK {
			continue
		}
		aliasTuple(alias, bodySignature.Params(), publicSignature.Params())
		aliasTuple(alias, bodySignature.Results(), publicSignature.Results())
		if bodySignature.Recv() != nil && publicSignature.Recv() != nil {
			alias[bodySignature.Recv()] = publicSignature.Recv()
		}
		bodyTypeParams, publicTypeParams := bodySignature.TypeParams(), publicSignature.TypeParams()
		for i := 0; i < bodyTypeParams.Len() && i < publicTypeParams.Len(); i++ {
			alias[bodyTypeParams.At(i).Obj()] = publicTypeParams.At(i).Obj()
		}
	}
	if len(alias) == 0 {
		return nil
	}
	return func(object types.Object) types.Object {
		if canonical, ok := alias[sourceintel.Origin(object)]; ok {
			return canonical
		}
		return object
	}
}

func aliasTuple(alias map[types.Object]types.Object, from, to *types.Tuple) {
	for i := 0; i < from.Len() && i < to.Len(); i++ {
		alias[from.At(i)] = to.At(i)
	}
}

// gsxExtraInput carries the already-computed analysis facts the gsx-only index
// edges are derived from. Every field is a value analyze holds when the index
// is built; nothing here re-derives or re-walks.
type gsxExtraInput struct {
	calls          map[*gsxast.Element]ComponentCallFact
	componentDecls map[ComponentDeclKey][]sourceintel.VersionedSpan
	pkgPath        string
	// canonicalByKey maps a logical component key to the object every edge of
	// that component attaches to (the authored declaration's object). A key
	// without one contributes no declaration occurrence: an analysis-only body
	// object is not a stable identity to publish spans against. Keys reach that
	// branch for real — a variant family rejected as invalid (mismatched
	// membership or signature) is republished body-only under isolated keys
	// (component_variant_semantic.go's publishIsolated with private=true), so
	// none of its declarations is public.
	canonicalByKey map[string]types.Object
	exprMap        map[gsxast.Node]goast.Expr
	info           *types.Info
	gsxFset        *token.FileSet
	// gsxFiles, pkgScope, objKey and fileScopes are the tag→component edge's
	// PLANNING-INDEPENDENT inputs: the authored elements, this package's scope,
	// the set of objects that are components, and per-gsx-file the skeleton's
	// file scope (its imports). Positional planning is skipped whenever the
	// package has a type error anywhere (module_importer's targetPlanningReady),
	// which is the normal mid-edit state, so the tag edge must not be derived
	// from it — see componentTagOccurrences.
	gsxFiles   map[string]*gsxast.File
	pkgScope   *types.Scope
	objKey     map[types.Object]string
	fileScopes map[string]*types.Scope // gsx path → skeleton file scope
}

// componentTagOccurrences publishes one IdentifierUse occurrence per component
// tag, on the component's own object, at the tag's local-name segment.
//
// The tag name is not Go, so no source map carries this edge. It is resolved by
// the same NAME RESOLUTION the tag itself means — this package's scope for a
// bare `<Card/>`, the file's imports for a dotted `<ui.Card/>` — and
// deliberately NOT from the positional call plan: planning is skipped for the
// WHOLE package whenever a type error sits anywhere in it (analyze's
// targetPlanningReady), which is the ordinary state of a file being typed.
// Sourcing the edge from the plan took go-to-definition and find-references on
// every tag in the package down with one unrelated typo, while the declaration
// side (componentTargetDeclarationProvenances) stayed deliberately error-tolerant.
//
// A planned call fact, where there is one, is the exact callable codegen chose
// and wins: it is also the only route to the one tag shape name resolution
// cannot see — a method-receiver tag (`<p.Content/>`), whose qualifier is a
// local variable, not an import.
func componentTagOccurrences(in gsxExtraInput, spanAt func(token.Pos, int) (sourceintel.Span, bool)) []sourceintel.Occurrence {
	var out []sourceintel.Occurrence
	for path, file := range in.gsxFiles {
		fileScope := in.fileScopes[path]
		inspectMarkupWithEmbedded(file, func(node gsxast.Node) bool {
			element, isElement := node.(*gsxast.Element)
			if !isElement || !element.IsComponent || element.Tag == "" {
				return true
			}
			object, ok := in.componentTagObject(element, fileScope)
			if !ok {
				return true
			}
			// A dotted tag (`<pkg.Card/>`) names the component only in its last
			// segment; the qualifier is the package, which has its own object.
			local := componentTagLocalName(element.Tag)
			if span, ok := spanAt(element.TagPos+token.Pos(len(element.Tag)-len(local)), len(local)); ok {
				out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: object})
			}
			return true
		})
	}
	return out
}

func componentTagLocalName(tag string) string {
	if dot := strings.LastIndexByte(tag, '.'); dot >= 0 {
		return tag[dot+1:]
	}
	return tag
}

// componentTagObject resolves one component tag to the object it names, by name
// resolution alone. fileScope is the tag's own file's skeleton scope, which
// holds exactly that file's imports. The planned call fact is consulted only
// where name resolution reaches nothing — see componentTagOccurrences.
func (in gsxExtraInput) componentTagObject(element *gsxast.Element, fileScope *types.Scope) (types.Object, bool) {
	if object, ok := in.resolvedComponentTagObject(element.Tag, fileScope); ok {
		return object, true
	}
	if call, planned := in.calls[element]; planned && call.Target != nil {
		return call.Target, true
	}
	return nil, false
}

func (in gsxExtraInput) resolvedComponentTagObject(tag string, fileScope *types.Scope) (types.Object, bool) {
	local := componentTagLocalName(tag)
	if local == tag {
		// Bare tag: this package's scope. objKey membership is what makes the
		// resolved object a COMPONENT — a same-named plain func, var or type is
		// not what `<Card/>` lowers to, and must not receive the edge.
		if in.pkgScope == nil {
			return nil, false
		}
		object := in.pkgScope.Lookup(local)
		if _, isComponent := in.objKey[object]; !isComponent {
			return nil, false
		}
		return object, true
	}
	qualifier := tag[:len(tag)-len(local)-1]
	if fileScope == nil || strings.ContainsRune(qualifier, '.') {
		return nil, false
	}
	pkgName, _ := fileScope.Lookup(qualifier).(*types.PkgName)
	if pkgName == nil || pkgName.Imported() == nil {
		return nil, false
	}
	imported := pkgName.Imported()
	// The imported package must really declare a component of that name.
	// componentDecls carries every imported package's declaration provenances,
	// so this is an exact check rather than a name-shape guess: it stops a
	// method-receiver tag whose receiver happens to be spelled like an import
	// from claiming an unrelated exported func in that package.
	if len(in.componentDecls[ComponentDeclKey{PackagePath: imported.Path(), ComponentKey: packageComponentKey(local)}]) == 0 {
		return nil, false
	}
	object, isFunc := imported.Scope().Lookup(local).(*types.Func)
	if !isFunc {
		return nil, false
	}
	return object, true
}

// inspectMarkupWithEmbedded is gsxast.Inspect extended over the two embedded
// part lists Inspect does not descend into (Interp.Embedded and
// GoBlock.Embedded), so a component tag inside an interpolated or block-level
// Go expression is walked too. Mirrors the LSP's inspectWithEmbedded, which
// every element-gated LSP lookup uses.
func inspectMarkupWithEmbedded(node gsxast.Node, f func(gsxast.Node) bool) {
	var visit func(gsxast.Node) bool
	visit = func(n gsxast.Node) bool {
		if !f(n) {
			return false
		}
		switch t := n.(type) {
		case *gsxast.Interp:
			for _, part := range t.Embedded {
				gsxast.Inspect(part, visit)
			}
		case *gsxast.GoBlock:
			for _, part := range t.Embedded {
				gsxast.Inspect(part, visit)
			}
		}
		return true
	}
	gsxast.Inspect(node, visit)
}

// gsxExtraOccurrences produces the reference and definition occurrences the Go
// type checker cannot see, because the authored bytes carrying them are not Go:
// component tag sites, attribute→parameter bindings, pipe stage names, and
// every build-tag variant's declaration span (a variant "loser" declaration is
// dropped by go/types as a redeclaration, so it has no object of its own).
func gsxExtraOccurrences(in gsxExtraInput) []sourceintel.Occurrence {
	if in.gsxFset == nil {
		return nil
	}
	var out []sourceintel.Occurrence
	spanAt := func(pos token.Pos, length int) (sourceintel.Span, bool) {
		if !pos.IsValid() || length <= 0 {
			return sourceintel.Span{}, false
		}
		position := in.gsxFset.Position(pos)
		return sourceintel.Span{Path: position.Filename, Start: position.Offset, End: position.Offset + length}, true
	}
	// 1. component tag sites.
	out = append(out, componentTagOccurrences(in, spanAt)...)
	// 2. attribute→parameter bindings. These stay on the positional plan: which
	// parameter an attribute binds IS the plan's answer, and there is no
	// planning-independent way to ask it.
	for element, call := range in.calls {
		if element == nil || call.Target == nil {
			continue
		}
		// Only a NAMED binding — the attribute spelling the parameter it binds —
		// is an authored reference to that parameter. An attrs-bag contributor
		// binds a forwarded attribute to the bag parameter, which is data flow,
		// not a mention of the parameter's name; publishing those would make
		// `gr` on an attrs parameter return every forwarded attribute in the
		// module. This is the same guard componentParamReferenceFacts (the
		// rename surface) applies, and for the same reason.
		for attr, param := range call.Params {
			name, ok := componentInputAttrName(attr)
			if !ok || param.Origin == nil || name != param.Name {
				continue
			}
			if span, ok := spanAt(attr.Pos(), len(name)); ok {
				out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: param.Origin})
			}
		}
	}
	// 3. pipe stage names. The lowering emits `alias.Func(` whose bytes differ
	// from the authored `|> name`, so no source map can carry this edge: the
	// stage's object is found structurally, by peeling the lowered call chain
	// exactly as the LSP's pipe navigation does.
	if in.info != nil {
		for node, skeleton := range in.exprMap {
			stages := pipeshape.Stages(node)
			if len(stages) == 0 {
				continue
			}
			selectors, _, _, ok := pipeshape.Walk(skeleton, len(stages))
			if !ok {
				continue
			}
			for i, stage := range stages {
				selector := selectors[i]
				if selector == nil {
					continue
				}
				object := in.info.Uses[selector]
				if object == nil {
					object = in.info.Defs[selector]
				}
				if object == nil {
					continue
				}
				if span, ok := spanAt(stage.NamePos, len(stage.Name)); ok {
					out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: object})
				}
			}
		}
	}
	// 4. every variant's authored declaration span, on the canonical object.
	for key, spans := range in.componentDecls {
		if key.PackagePath != in.pkgPath {
			continue // imported components are defined by their own package's index
		}
		object := in.canonicalByKey[key.ComponentKey]
		if object == nil {
			continue
		}
		for _, versioned := range spans {
			out = append(out, sourceintel.Occurrence{Span: versioned.Span, Kind: sourceintel.IdentifierDefinition, Object: object})
		}
	}
	return out
}
