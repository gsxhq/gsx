package tagcallable

import (
	"go/types"
	"testing"
)

// nodeInterface builds a minimal `interface{ Render() }`-shaped interface
// type standing in for gsx.Node, and a defined type implementing it — enough
// to exercise types.AssignableTo without importing the real gsx package.
func nodeInterface() (*types.Interface, *types.Named) {
	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	method := types.NewFunc(0, nil, "Render", sig)
	iface := types.NewInterfaceType([]*types.Func{method}, nil).Complete()

	pkg := types.NewPackage("example.com/node", "node")
	named := types.NewNamed(types.NewTypeName(0, pkg, "Element", nil), types.NewStruct(nil, nil), nil)
	// Element implements Render via a pointer receiver method set below.
	recv := types.NewVar(0, pkg, "e", types.NewPointer(named))
	rendersig := types.NewSignatureType(recv, nil, nil, nil, nil, false)
	rendermethod := types.NewFunc(0, pkg, "Render", rendersig)
	named.AddMethod(rendermethod)
	return iface, named
}

func namedParam(pkg *types.Package, name string, typ types.Type) *types.Var {
	return types.NewParam(0, pkg, name, typ)
}

func TestSignature(t *testing.T) {
	iface, node := nodeInterface()
	nodePtr := types.NewPointer(node)
	pkg := types.NewPackage("example.com/p", "p")

	funcSig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(namedParam(pkg, "name", types.Typ[types.String])),
		types.NewTuple(types.NewVar(0, pkg, "", nodePtr)), false)

	t.Run("bare signature type", func(t *testing.T) {
		got := Signature(funcSig)
		if got != funcSig {
			t.Fatalf("Signature(bare sig) = %v, want the same signature back", got)
		}
	})

	t.Run("named function type unwraps to its underlying signature", func(t *testing.T) {
		defined := types.NewNamed(types.NewTypeName(0, pkg, "Factory", nil), funcSig, nil)
		got := Signature(defined)
		if got == nil || !types.Identical(got, funcSig) {
			t.Fatalf("Signature(named func type) = %v, want a signature identical to %v", got, funcSig)
		}
	})

	t.Run("non-callable type yields nil", func(t *testing.T) {
		if got := Signature(types.Typ[types.String]); got != nil {
			t.Fatalf("Signature(string) = %v, want nil", got)
		}
	})

	t.Run("nil type yields nil", func(t *testing.T) {
		if got := Signature(nil); got != nil {
			t.Fatalf("Signature(nil) = %v, want nil", got)
		}
	})

	_ = iface
}

func TestIsResult(t *testing.T) {
	iface, node := nodeInterface()
	nodeType := types.Type(iface)
	nodePtr := types.NewPointer(node)
	pkg := types.NewPackage("example.com/p", "p")

	t.Run("one result assignable to node is accepted", func(t *testing.T) {
		sig := types.NewSignatureType(nil, nil, nil, nil,
			types.NewTuple(types.NewVar(0, pkg, "", nodePtr)), false)
		if !IsResult(sig, nodeType) {
			t.Fatalf("IsResult(one Node-assignable result) = false, want true")
		}
	})

	t.Run("(Node, error) two results is rejected", func(t *testing.T) {
		sig := types.NewSignatureType(nil, nil, nil, nil,
			types.NewTuple(
				types.NewVar(0, pkg, "", nodePtr),
				types.NewVar(0, pkg, "", types.Universe.Lookup("error").Type()),
			), false)
		if IsResult(sig, nodeType) {
			t.Fatalf("IsResult((Node, error)) = true, want false (exactly one result required)")
		}
	})

	t.Run("zero results is rejected", func(t *testing.T) {
		sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
		if IsResult(sig, nodeType) {
			t.Fatalf("IsResult(zero results) = true, want false")
		}
	})

	t.Run("result not assignable to node is rejected", func(t *testing.T) {
		sig := types.NewSignatureType(nil, nil, nil, nil,
			types.NewTuple(types.NewVar(0, pkg, "", types.Typ[types.Int])), false)
		if IsResult(sig, nodeType) {
			t.Fatalf("IsResult(int result) = true, want false")
		}
	})

	t.Run("nil signature is rejected", func(t *testing.T) {
		if IsResult(nil, nodeType) {
			t.Fatalf("IsResult(nil sig) = true, want false")
		}
	})

	t.Run("nil node is rejected", func(t *testing.T) {
		sig := types.NewSignatureType(nil, nil, nil, nil,
			types.NewTuple(types.NewVar(0, pkg, "", nodePtr)), false)
		if IsResult(sig, nil) {
			t.Fatalf("IsResult(sig, nil node) = true, want false")
		}
	})
}
