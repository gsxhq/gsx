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
// The package exposes TWO layers, deliberately kept apart:
//
//   - Signature/IsResult are the callable-universe shape itself, and nothing
//     more. They do NOT decide the "every parameter must be named"
//     restriction: a call site codegen resolves is held only to the shape.
//   - IsCandidate is the COMPLETION-grade predicate layered on top — the
//     shape plus every parameter named — used when scanning a whole package
//     scope for names to OFFER in tag position. An unnamed parameter could
//     never receive a markup attribute (component_signature.go's
//     "component-parameter-name" check rejects it at the point a resolved
//     call site is planned), so a package-scope scan excludes it up front
//     rather than offering a candidate codegen would later reject. That is a
//     deliberate, conservative completion choice, not a mirror of some single
//     codegen acceptance function — but it must be identical across every
//     completion surface that offers tags, which is why it lives here rather
//     than in one of them.
//
// NodeInterface resolves the gsx.Node identity a scan needs, from the scanned
// package's OWN direct imports.
package tagcallable

import "go/types"

// gsxRuntimeImportPath is the import path of the gsx runtime package, whose Node
// interface every tag-callable result must be assignable to.
const gsxRuntimeImportPath = "github.com/gsxhq/gsx"

// NodeInterface locates the gsx.Node interface type within pkg's OWN direct
// imports. pkg must import the gsx runtime itself for any of its declarations
// to type-check against gsx.Node in the first place, so this never needs to
// search transitively or reach into a different package's import graph (in particular
// the import graph of whichever package is doing the ASKING is irrelevant: pkg
// is scanned as an independent package, per the go/types identity rule that
// every *types.Package in one checked build shares one canonical object per
// imported package).
//
// Returns nil (fail-soft, never an error) when pkg is nil, does not import gsx
// at all, or its gsx does not declare a Node interface: most packages never
// define a component-shaped value, and a scan that finds no gsx.Node simply has
// no candidates to offer.
//
// codegen's component_signature.go runtimeContractFromAnalysisPackage is the
// authority for this same identity when it resolves Node, Attr, and Attrs
// TOGETHER for one fixed "the analysis package", erroring if any is missing or
// inconsistent. This narrower lookup answers the different question a scan asks
// — Node alone, for an arbitrary target package that is never itself the
// analysis package — and both now read the identity the same way.
func NodeInterface(pkg *types.Package) *types.Interface {
	if pkg == nil {
		return nil
	}
	for _, imp := range pkg.Imports() {
		if imp.Path() != gsxRuntimeImportPath {
			continue
		}
		tn, ok := imp.Scope().Lookup("Node").(*types.TypeName)
		if !ok {
			return nil
		}
		iface, ok := types.Unalias(tn.Type()).Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		return iface
	}
	return nil
}

// IsCandidate reports whether the package-scope object obj may be OFFERED as a
// tag name completing against node: a *types.Func, or a *types.Var whose type
// unwraps to a callable signature, whose signature satisfies IsResult AND names
// every parameter. See the package doc for why the named-parameter rule is part
// of this predicate but not of IsResult.
//
// Visibility (obj.Exported()) is NOT decided here: a same-package scan legally
// offers unexported names while a cross-package one may not, and only the
// caller knows which it is doing.
func IsCandidate(obj types.Object, node types.Type) bool {
	var sig *types.Signature
	switch o := obj.(type) {
	case *types.Func:
		sig, _ = o.Type().(*types.Signature)
	case *types.Var:
		sig = Signature(o.Type())
	default:
		return false
	}
	if !IsResult(sig, node) {
		return false
	}
	for param := range sig.Params().Variables() {
		if param.Name() == "" {
			return false
		}
	}
	return true
}

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
