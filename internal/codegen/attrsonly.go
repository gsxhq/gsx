package codegen

import (
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// shadowedQualifierType is the sentinel harvest stores in resolved[el] for a
// gated dotted attrs-only tag (<ui.Icon>) whose qualifier resolves — in the
// skeleton's Go scope — to a local/param that SHADOWS the like-named import,
// not to the package itself. The name-based gate (isAttrsOnlyCandidate →
// byo.isDepAlias) fires on the import alias, but Go scoping makes the probe's
// _gsxcompsig(ui.Icon) resolve through the param's struct field; without this
// signal the emitter would silently bag-call that field (FAIL 7: a region that
// is a hard build error on main). genChildComponent recognizes this exact
// sentinel (pointer identity) and emits a positioned attrsonly-shadowed-qualifier
// diagnostic instead. It is only ever read for the one element node harvest
// tagged, so a fabricated Named with an Invalid underlying is safe.
var shadowedQualifierType types.Type = types.NewNamed(
	types.NewTypeName(token.NoPos, nil, "_gsxShadowedQualifier", nil),
	types.Typ[types.Invalid], nil,
)

// isAttrsOnlyCandidate reports whether a component tag should be resolved as a
// potential attrs-only component value: a tag that is not component-declared,
// not byo, not a method, not a bare-call nullary candidate, and whose
// <Name>Props type does not exist anywhere in scope. That region is guaranteed
// to fail today (undefined: <Name>Props), so gating it onto the _gsxcompsig
// probe is a pure capability addition. emitProbes and genChildComponent both
// branch on this so emit ≡ probe.
//
// A dotted tag <alias.Name> is gated ONLY when alias is a known project-internal
// gsx import (byo.isDepAlias) — so <ui.HomeIcon> against an imported gsx package
// is eligible, while a local/receiver/field qualifier (<item.Icon>) is not a dep
// alias and is left untouched on its existing path (preserving today's
// behavior). The remaining checks then run against the alias-qualified facts
// (propFields["ui.HomeIconProps"], byo type/func facts republished under the
// alias by mergeQualified), identically in both passes since both receive the
// same file-scoped propFields + byo.
func isAttrsOnlyCandidate(el *gsxast.Element, propFields map[string]map[string]bool, byo *byoData, recvVar, recvTypeName string) bool {
	if !isComponentTag(el.Tag) || el.TypeArgs != "" {
		return false
	}
	if dot := strings.IndexByte(el.Tag, '.'); dot >= 0 {
		if !byo.isDepAlias(el.Tag[:dot]) {
			return false // local/receiver/field qualifier: never gated
		}
	}
	_, propsType, isMethod := childInvocation(el, byo, recvVar, recvTypeName)
	if isMethod {
		return false
	}
	if _, isByo := byo.isByoStruct(propsType); isByo {
		return false
	}
	if _, known := propFields[propsType]; known {
		return false // component-declared or enumerated props type
	}
	if byo.isNullaryFunc(el.Tag) {
		return false // bare-call candidate keeps its existing probe
	}
	return !byo.hasTypeName(propsType)
}

// attrsOnlySig reports whether t is an attrs-only component-value shape
// (spec: docs/superpowers/specs/2026-07-07-attrs-only-component-values-design.md):
// a func with exactly one parameter and one result, the result the named
// gsx.Node, and the parameter's underlying type (types.Unalias, then — for a
// defined param type — Named.Underlying(), which already resolves
// transitively through a chain of defined types) a *types.Slice whose element
// is exactly the named gsx.Attr — or, variadically, `...gsx.Attr`. The
// boundary is purely structural now: element and result identity are the only
// checks. This subsumes the earlier "assignable from gsx.Attrs" rule (which
// rejected any user-defined named slice type) — accepting a defined param
// type is made SOUND by a call-site conversion (attrsonly-bag emission wraps
// the bag: `F([]gsx.Attr(bag))`; []gsx.Attr assigns to any named type sharing
// that underlying, one side unnamed, same rule Go's assignability check
// applies), so there is no longer a compile-failure risk to guard against.
// needsConvert reports whether the emitter must insert that conversion: true
// exactly when the match is non-variadic AND the param is neither the named
// gsx.Attrs itself nor an unnamed []gsx.Attr (both already assign directly,
// so wrapping them would be redundant, and — for gsx.Attrs — would change
// nothing since it's already the bag's own type). The element check stays
// strict: a slice of a DEFINED type sharing gsx.Attr's underlying (e.g. type
// myAttr gsx.Attr) is rejected, because slice conversions in Go require
// identical element types, not merely identical underlying element types —
// []myAttr(bag) does not compile when bag is gsx.Attrs. Aliases are unwrapped
// first, so a userland `type Component = func(...gsx.Attr) gsx.Node` matches.
// Generic signatures never match.
func attrsOnlySig(t types.Type) (variadic, needsConvert, ok bool) {
	sig, isSig := types.Unalias(t).(*types.Signature)
	if !isSig {
		if n, isNamed := types.Unalias(t).(*types.Named); isNamed {
			sig, isSig = n.Underlying().(*types.Signature)
		}
		if !isSig {
			return false, false, false
		}
	}
	if sig.TypeParams().Len() != 0 || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false, false, false
	}
	if !isGsxNamed(sig.Results().At(0).Type(), "Node") {
		return false, false, false
	}
	p := sig.Params().At(0).Type()
	if sig.Variadic() {
		sl, isSlice := types.Unalias(p).(*types.Slice)
		if !isSlice || !isGsxNamed(sl.Elem(), "Attr") {
			return false, false, false
		}
		return true, false, true
	}
	if isGsxNamed(p, "Attrs") {
		return false, false, true
	}
	pu := types.Unalias(p)
	if sl, isSlice := pu.(*types.Slice); isSlice && isGsxNamed(sl.Elem(), "Attr") {
		return false, false, true
	}
	if n, isNamed := pu.(*types.Named); isNamed {
		if sl, isSlice := n.Underlying().(*types.Slice); isSlice && isGsxNamed(sl.Elem(), "Attr") {
			return false, true, true
		}
	}
	return false, false, false
}

// isGsxNamed reports whether t is the named type gsx.<name> (matched by the
// gsx module path so vendored/replaced copies still match, forks don't).
func isGsxNamed(t types.Type, name string) bool {
	n, isNamed := types.Unalias(t).(*types.Named)
	if !isNamed {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == gsxRuntimePath && obj.Name() == name
}
