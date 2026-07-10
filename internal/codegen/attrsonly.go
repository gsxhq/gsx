package codegen

import (
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// isAttrsOnlyCandidate reports whether a component tag should be resolved as a
// potential attrs-only component value: a same-package (non-dotted, non-generic)
// tag that is not component-declared, not byo, not a method, not a bare-call
// nullary candidate, and whose <Name>Props type does not exist anywhere in the
// package. That region is guaranteed to fail today (undefined: <Name>Props), so
// gating it onto the _gsxcompsig probe is a pure capability addition. emitProbes
// and genChildComponent both branch on this so emit ≡ probe.
func isAttrsOnlyCandidate(el *gsxast.Element, propFields map[string]map[string]bool, byo *byoData, recvVar, recvTypeName string) bool {
	if !isComponentTag(el.Tag) || strings.Contains(el.Tag, ".") || el.TypeArgs != "" {
		return false
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

// attrsOnlySig reports whether t is exactly one of the two attrs-only
// component-value shapes (spec:
// docs/superpowers/specs/2026-07-07-attrs-only-component-values-design.md):
//
//	func(gsx.Attrs) gsx.Node
//	func(...gsx.Attr) gsx.Node
//
// Matching is by NAMED-type identity against the gsx package (path + name),
// never structural: the assignability-accident spelling
// func([]gsx.Attr) gsx.Node is deliberately excluded (spec §"The one shape
// deliberately excluded"). Aliases are unwrapped first, so a userland
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
	if !isGsxNamed(p, "Attrs") {
		return false, false
	}
	return false, true
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
