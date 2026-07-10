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
	otherAttrs := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "Attrs", nil), types.NewSlice(attr), nil)

	cases := []struct {
		name     string
		typ      types.Type
		variadic bool
		ok       bool
	}{
		{"named-attrs", sig(false, attrs, node), false, true},
		{"variadic-attr", sig(true, types.NewSlice(attr), node), true, true},
		{"unnamed-slice", sig(false, types.NewSlice(attr), node), false, false},
		{"extra-error", sig(false, attrs, node, types.Universe.Lookup("error").Type()), false, false},
		{"wrong-result", sig(false, attrs, types.Typ[types.String]), false, false},
		{"wrong-pkg-attrs", sig(false, otherAttrs, node), false, false},
		{"non-signature", types.Typ[types.Int], false, false},
		{"alias-of-match", types.NewAlias(types.NewTypeName(token.NoPos, otherPkg, "Component", nil), sig(true, types.NewSlice(attr), node)), true, true},
		// Additional cases
		{"zero-param", sigCustom(false, []*types.Var{}, node), false, false},
		{"two-param", sigCustom(false, []*types.Var{types.NewVar(token.NoPos, nil, "a", attrs), types.NewVar(token.NoPos, nil, "b", attrs)}, node), false, false},
		// Defined (non-alias) named func type that matches
		{"named-sig-underlying", types.NewNamed(types.NewTypeName(token.NoPos, types.NewPackage("github.com/gsxhq/gsx", "gsx"), "Component", nil), sig(true, types.NewSlice(attr), node), nil), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			variadic, ok := attrsOnlySig(c.typ)
			if ok != c.ok || variadic != c.variadic {
				t.Fatalf("attrsOnlySig(%s) = (variadic=%v, ok=%v), want (%v, %v)", c.typ, variadic, ok, c.variadic, c.ok)
			}
		})
	}
}
