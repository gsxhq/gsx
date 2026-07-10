package lsp

import (
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// gsxRuntimePath is the gsx runtime's import path. Mirrors
// codegen.gsxRuntimePath (internal/codegen/rtimports.go) — duplicated rather
// than imported, since internal/lsp deliberately does not depend on
// internal/codegen in production code (see Package's doc comment); the same
// pattern already duplicates isComponentTag/isSimpleComponentTag.
const gsxRuntimePath = "github.com/gsxhq/gsx"

// isGsxNamedType reports whether t is the named type gsx.<name>, matched by
// the gsx module's import path (so a vendored/replaced copy still matches).
// Mirrors codegen.isGsxNamed.
func isGsxNamedType(t types.Type, name string) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == gsxRuntimePath && obj.Name() == name
}

// isAttrsOnlyValueType reports whether t is one of the two attrs-only
// component-value shapes gated by codegen.isAttrsOnlyCandidate /
// codegen.attrsOnlySig at codegen time:
//
//	func(gsx.Attrs) gsx.Node
//	func(...gsx.Attr) gsx.Node
//
// Mirrors codegen.attrsOnlySig exactly (named-type identity, alias-unwrapped,
// no generics, the func([]gsx.Attr) gsx.Node assignability-accident spelling
// deliberately excluded) so go-to-definition recognizes precisely the tags
// codegen resolves as attrs-only values — never a same-named unrelated
// func/var.
func isAttrsOnlyValueType(t types.Type) bool {
	sig, ok := types.Unalias(t).(*types.Signature)
	if !ok {
		n, isNamed := types.Unalias(t).(*types.Named)
		if !isNamed {
			return false
		}
		sig, ok = n.Underlying().(*types.Signature)
		if !ok {
			return false
		}
	}
	if sig.TypeParams().Len() != 0 || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	if !isGsxNamedType(sig.Results().At(0).Type(), "Node") {
		return false
	}
	p := sig.Params().At(0).Type()
	if sig.Variadic() {
		sl, isSlice := types.Unalias(p).(*types.Slice)
		return isSlice && isGsxNamedType(sl.Elem(), "Attr")
	}
	return isGsxNamedType(p, "Attrs")
}

// attrsOnlyTagObject resolves a component tag name to its package-scope
// types.Object — the SAME lookup codegen's harvest performs via the
// `_gsxcompsig(tag)` probe (analyze.go's sigByName), just read directly off
// go/types instead of re-deriving it from a probe call: pkg.Types.Scope() for
// a same-package tag, or the qualifier's imported package scope for a dotted
// tag. Ambiguous-qualifier safety mirrors resolveCrossPkgComponent: two
// imports sharing the declared name bail rather than risk a wrong jump. Only
// a *types.Func or *types.Var is returned (never a type name, const, or
// package name) — an attrs-only component value is always one of those two.
// Returns nil when the tag doesn't resolve.
func attrsOnlyTagObject(pkg *Package, tag string) types.Object {
	var obj types.Object
	if qualifier, name, ok := splitDottedTag(tag); ok {
		var imp *types.Package
		for _, p := range pkg.Types.Imports() {
			if p.Name() == qualifier {
				if imp != nil {
					return nil // ambiguous qualifier; never risk a wrong jump
				}
				imp = p
			}
		}
		if imp == nil {
			return nil
		}
		obj = imp.Scope().Lookup(name)
	} else if isSimpleComponentTag(tag) {
		obj = pkg.Types.Scope().Lookup(tag)
	} else {
		return nil
	}
	switch obj.(type) {
	case *types.Func, *types.Var:
		return obj
	default:
		return nil
	}
}

// attrsOnlyTagDeclAt resolves a cursor on a component tag name that names an
// attrs-only component value — a package-level var/func of one of the two
// shapes isAttrsOnlyValueType recognizes (docs/superpowers/specs/2026-07-07-
// attrs-only-component-values-design.md) — rather than a `component`
// declaration. componentTagDeclAt/crossPkgTagDeclAt only recognize
// gsxast.Component decls (CrossIndex's compByKey, built from `component`
// syntax), so an attrs-only tag falls through both undetected: this is the
// parallel, additive lookup — it never touches CrossIndex. It resolves the
// tag directly through go/types (attrsOnlyTagObject) and returns the var/
// func's OWN declaration position: a real .go file position, or (for a
// GoChunk-declared value) the .gsx position its skeleton //line directive
// maps back to — exactly the resolution buildCrossNav already relies on for
// every other cross-index position. Returns ok=false when the cursor isn't on
// a tag name, the tag doesn't resolve to a package-scope func/var, that
// object's type isn't one of the two attrs-only shapes, or its position
// doesn't land in real source — so a typo'd/undefined tag or an unrelated
// same-named symbol never produces a false jump.
func attrsOnlyTagDeclAt(pkg *Package, path string, off int) (token.Position, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil || pkg.Types == nil || pkg.Fset == nil {
		return token.Position{}, false
	}
	f := pkg.Files[path]
	if f == nil {
		return token.Position{}, false
	}
	var result token.Position
	found := false
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if found {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok || !isComponentTag(el.Tag) {
			return true
		}
		tag := el.Tag
		elOff := pkg.GSXFset.Position(el.Pos()).Offset
		nameStart := elOff + 1 // skip '<'
		onOpen := off >= nameStart && off < nameStart+len(tag)
		onClose := false
		if el.CloseNamePos.IsValid() {
			closeStart := pkg.GSXFset.Position(el.CloseNamePos).Offset
			onClose = off >= closeStart && off < closeStart+len(tag)
		}
		if !onOpen && !onClose {
			return true
		}
		obj := attrsOnlyTagObject(pkg, tag)
		if obj == nil || !isAttrsOnlyValueType(obj.Type()) || !obj.Pos().IsValid() {
			return true
		}
		dp := pkg.Fset.Position(obj.Pos())
		if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
			return true
		}
		result = dp
		found = true
		return false
	})
	return result, found
}
