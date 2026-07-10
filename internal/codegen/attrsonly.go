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

// attrsOnlySig reports whether t is one of the three attrs-only
// component-value shapes (spec:
// docs/superpowers/specs/2026-07-07-attrs-only-component-values-design.md):
//
//	func(gsx.Attrs) gsx.Node
//	func([]gsx.Attr) gsx.Node
//	func(...gsx.Attr) gsx.Node
//
// The non-variadic param must be ASSIGNABLE FROM gsx.Attrs: either the named
// gsx.Attrs itself, or the unnamed slice type []gsx.Attr (identical
// underlying, one side unnamed — the same rule Go's assignability check
// applies). A user-defined named type sharing that underlying (type MyAttrs
// []gsx.Attr) is NOT assignable from gsx.Attrs (two distinct named types) and
// stays rejected: accepting it would emit an F(bag) call that fails to
// compile. This used to exclude the unnamed-slice spelling too, as spelling
// discipline left over from an abandoned syntactic (regex-based) design; under
// go/types recognition only types exist, and the variadic form already hands
// the component body an unnamed []gsx.Attr, so that ergonomic argument was
// void. Aliases are unwrapped first, so a userland
// `type Component = func(...gsx.Attr) gsx.Node` matches. Generic signatures
// never match.
func attrsOnlySig(t types.Type) (variadic, ok bool) {
	sig, isSig := types.Unalias(t).(*types.Signature)
	if !isSig {
		if n, isNamed := types.Unalias(t).(*types.Named); isNamed {
			sig, isSig = n.Underlying().(*types.Signature)
		}
		if !isSig {
			return false, false
		}
	}
	if sig.TypeParams().Len() != 0 || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false, false
	}
	if !isGsxNamed(sig.Results().At(0).Type(), "Node") {
		return false, false
	}
	p := sig.Params().At(0).Type()
	if sig.Variadic() {
		sl, isSlice := types.Unalias(p).(*types.Slice)
		if !isSlice || !isGsxNamed(sl.Elem(), "Attr") {
			return false, false
		}
		return true, true
	}
	if isGsxNamed(p, "Attrs") {
		return false, true
	}
	if sl, isSlice := types.Unalias(p).(*types.Slice); isSlice && isGsxNamed(sl.Elem(), "Attr") {
		return false, true
	}
	return false, false
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
