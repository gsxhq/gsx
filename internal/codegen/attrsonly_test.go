package codegen

import (
	"go/token"
	"go/types"
	"testing"
)

// fabricated gsx package: named types with the right package path + underlying
// shapes, sufficient for identity checks (attrsOnlySig matches by package path
// and type name, never structurally).
func fakeGsx(t *testing.T) (pkg *types.Package, attr, attrs, node types.Type) {
	t.Helper()
	pkg = types.NewPackage("github.com/gsxhq/gsx", "gsx")
	attrN := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Attr", nil), types.NewStruct(nil, nil), nil)
	attrsN := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Attrs", nil), types.NewSlice(attrN), nil)
	nodeN := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Node", nil), types.NewInterfaceType(nil, nil), nil)
	return pkg, attrN, attrsN, nodeN
}

func sig(variadic bool, param, result types.Type, extraResults ...types.Type) *types.Signature {
	params := types.NewTuple(types.NewVar(token.NoPos, nil, "attrs", param))
	rs := []*types.Var{types.NewVar(token.NoPos, nil, "", result)}
	for _, r := range extraResults {
		rs = append(rs, types.NewVar(token.NoPos, nil, "", r))
	}
	return types.NewSignatureType(nil, nil, nil, params, types.NewTuple(rs...), variadic)
}

func sigCustom(variadic bool, params []*types.Var, result types.Type, extraResults ...types.Type) *types.Signature {
	rs := []*types.Var{types.NewVar(token.NoPos, nil, "", result)}
	for _, r := range extraResults {
		rs = append(rs, types.NewVar(token.NoPos, nil, "", r))
	}
	return types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), types.NewTuple(rs...), variadic)
}

func TestAttrsOnlySig(t *testing.T) {
	_, attr, attrs, node := fakeGsx(t)
	otherPkg := types.NewPackage("example.com/other", "other")
	// A type named "Attrs" but declared OUTSIDE the gsx package, elem the real
	// gsx.Attr: isGsxNamed(p, "Attrs") correctly rejects it on package path
	// (never fooled by the name alone), but it's still a legitimate
	// user-defined named slice of real gsx.Attr elements under the new rule —
	// accepted, needsConvert=true, same as any other named-slice-type case.
	otherPkgAttrsLookalike := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "Attrs", nil), types.NewSlice(attr), nil)
	userNamedSlice := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "MyAttrs", nil), types.NewSlice(attr), nil)
	// type M gsx.Attrs — a named type whose underlying is gsx.Attrs's OWN
	// underlying (a defined type's Underlying() resolves transitively through
	// a chain of named types, so this mirrors what go/types itself produces).
	namedOfNamed := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "M", nil), attrs.Underlying(), nil)
	// type myAttr gsx.Attr; a slice of it — the ELEMENT is a distinct defined
	// type sharing gsx.Attr's underlying, not gsx.Attr itself.
	definedElem := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "myAttr", nil), attr.Underlying(), nil)
	namedSliceOfDefinedElem := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "MyAttrs2", nil), types.NewSlice(definedElem), nil)
	// A slice element literally named "Attr" but declared OUTSIDE the gsx
	// package: isGsxNamed's package-path check must reject it just as
	// strictly on the element as it does on the container (otherPkgAttrsLookalike
	// above) — a same-named foreign Attr is not the real gsx.Attr.
	otherPkgAttrElemLookalike := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "Attr", nil), types.NewStruct(nil, nil), nil)
	namedSliceOfForeignAttrElem := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "MyAttrs3", nil), types.NewSlice(otherPkgAttrElemLookalike), nil)

	cases := []struct {
		name         string
		typ          types.Type
		variadic     bool
		needsConvert bool
		ok           bool
	}{
		{"named-attrs", sig(false, attrs, node), false, false, true},
		{"variadic-attr", sig(true, types.NewSlice(attr), node), true, false, true},
		{"unnamed-slice", sig(false, types.NewSlice(attr), node), false, false, true},
		{"user-named-slice", sig(false, userNamedSlice, node), false, true, true},
		{"named-of-named", sig(false, namedOfNamed, node), false, true, true},
		{"named-slice-defined-elem", sig(false, namedSliceOfDefinedElem, node), false, false, false},
		{"named-slice-foreign-attr-elem", sig(false, namedSliceOfForeignAttrElem, node), false, false, false},
		{"extra-error", sig(false, attrs, node, types.Universe.Lookup("error").Type()), false, false, false},
		{"wrong-result", sig(false, attrs, types.Typ[types.String]), false, false, false},
		{"other-pkg-attrs-lookalike", sig(false, otherPkgAttrsLookalike, node), false, true, true},
		{"non-signature", types.Typ[types.Int], false, false, false},
		{"alias-of-match", types.NewAlias(types.NewTypeName(token.NoPos, otherPkg, "Component", nil), sig(true, types.NewSlice(attr), node)), true, false, true},
		// Additional cases
		{"zero-param", sigCustom(false, []*types.Var{}, node), false, false, false},
		{"two-param", sigCustom(false, []*types.Var{types.NewVar(token.NoPos, nil, "a", attrs), types.NewVar(token.NoPos, nil, "b", attrs)}, node), false, false, false},
		// Defined (non-alias) named func type that matches
		{"named-sig-underlying", types.NewNamed(types.NewTypeName(token.NoPos, types.NewPackage("github.com/gsxhq/gsx", "gsx"), "Component", nil), sig(true, types.NewSlice(attr), node), nil), true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			variadic, needsConvert, ok := attrsOnlySig(c.typ)
			if ok != c.ok || variadic != c.variadic || needsConvert != c.needsConvert {
				t.Fatalf("attrsOnlySig(%s) = (variadic=%v, needsConvert=%v, ok=%v), want (%v, %v, %v)", c.typ, variadic, needsConvert, ok, c.variadic, c.needsConvert, c.ok)
			}
		})
	}
}
