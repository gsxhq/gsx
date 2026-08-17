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
	// object is not a stable identity to publish spans against.
	canonicalByKey map[string]types.Object
	exprMap        map[gsxast.Node]goast.Expr
	info           *types.Info
	gsxFset        *token.FileSet
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
	// 1. component tag sites and their attribute→parameter bindings.
	for element, call := range in.calls {
		if element == nil || call.Target == nil {
			continue
		}
		// A dotted tag (`<pkg.Card/>`) names the component only in its last
		// segment; the qualifier is the package, which has its own object.
		local := element.Tag
		if dot := strings.LastIndexByte(local, '.'); dot >= 0 {
			local = local[dot+1:]
		}
		if span, ok := spanAt(element.TagPos+token.Pos(len(element.Tag)-len(local)), len(local)); ok {
			out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: call.Target})
		}
		for attr, param := range call.Params {
			name, ok := componentInputAttrName(attr)
			if !ok || param.Origin == nil {
				continue
			}
			if span, ok := spanAt(attr.Pos(), len(name)); ok {
				out = append(out, sourceintel.Occurrence{Span: span, Kind: sourceintel.IdentifierUse, Object: param.Origin})
			}
		}
	}
	// 2. pipe stage names. The lowering emits `alias.Func(` whose bytes differ
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
	// 3. every variant's authored declaration span, on the canonical object.
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
