// Package tagcallable classifies whether a Go value — a func, or a
// function-typed var — has the shape a gsx tag can call: a signature with
// exactly one result assignable to gsx.Node.
//
// It is the single source for that shape check, shared by internal/codegen
// (which enforces it, via component_identity.go's componentResultType, at a
// real syntactic component call site — one already-authored target the
// analyzer resolved) and internal/lsp (which mirrors it in
// completion_gsx.go's tagCallableValueNames to scan an entire imported
// package's scope for candidate tag VALUES completion can offer — there is
// no call site to probe there, only a package to enumerate). Both consumers
// import this package directly (the same arrangement internal/goexprshape
// uses between internal/printer and internal/codegen); internal/lsp must not
// import internal/codegen, so this leaf package is the only legal place for
// the shared rule to live.
//
// This package intentionally does NOT decide the "every parameter must be
// named" restriction some callers additionally apply — that is not part of
// the callable-universe shape itself (see each consumer's own doc for where
// and why it layers that on).
package tagcallable

import "go/types"

// Signature returns typ's callable *types.Signature — unwrapping a defined
// (named) function type's underlying type — or nil when typ is not callable
// at all. This unwrap is needed for a `type Factory func(...) gsx.Node`-shaped
// package var, not just a bare `func(...) gsx.Node` one.
func Signature(typ types.Type) *types.Signature {
	if typ == nil {
		return nil
	}
	unaliased := types.Unalias(typ)
	if sig, ok := unaliased.(*types.Signature); ok {
		return sig
	}
	sig, _ := unaliased.Underlying().(*types.Signature)
	return sig
}

// IsResult reports whether sig has exactly one result assignable to node —
// the result half of the callable-universe tag shape. types.AssignableTo,
// not types.Implements: node is gsx.Node's own defined type identity in the
// relevant build, and assignability (not just interface satisfaction) is the
// exact rule a real component call site is checked against, so completion
// candidates must be held to the identical standard or the two would accept
// different value sets.
func IsResult(sig *types.Signature, node types.Type) bool {
	if sig == nil || node == nil {
		return false
	}
	results := sig.Results()
	return results.Len() == 1 && types.AssignableTo(results.At(0).Type(), node)
}
