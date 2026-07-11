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
// internal/codegen in production code (see Package's doc comment).
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

// isAttrsOnlyValueType reports whether t is an attrs-only component-value
// shape: a func with exactly one parameter and one result, the result the
// named gsx.Node, and the parameter's underlying type a *types.Slice whose
// one result, the result the named gsx.Node, and the parameter's underlying
// type a *types.Slice whose element is exactly the named gsx.Attr (variadic
// `...gsx.Attr`, or non-variadic — including a user-defined named slice type
// sharing that underlying, e.g. `type MyAttrs []gsx.Attr`; codegen makes that
// spelling sound with a call-site conversion, so go-to-definition must
// recognize it too, not just gsx.Attrs/[]gsx.Attr).
//
// Mirrors codegen.attrsOnlySig exactly, structurally — this function only
// needs the ok signal (codegen's needsConvert only matters for emission,
// never for whether a tag resolves as an attrs-only value at all). The
// element check stays strict: a slice of a distinct defined type merely
// sharing gsx.Attr's underlying is rejected. Alias-unwrapped, no generics, so
// go-to-definition recognizes precisely the tags codegen resolves as
// attrs-only values, never a same-named unrelated func/var.
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
	if isGsxNamedType(p, "Attrs") {
		return true
	}
	pu := types.Unalias(p)
	if sl, isSlice := pu.(*types.Slice); isSlice && isGsxNamedType(sl.Elem(), "Attr") {
		return true
	}
	if n, isNamed := pu.(*types.Named); isNamed {
		if sl, isSlice := n.Underlying().(*types.Slice); isSlice && isGsxNamedType(sl.Elem(), "Attr") {
			return true
		}
	}
	return false
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
//
// The non-dotted branch does not re-derive component-ness from the tag's
// case (that would miss a lowercase tag resolving to a package-level
// var/func under the lowercase-tag-resolution rule): its sole caller,
// attrsOnlyTagDeclAt, already gated on el.IsComponent before calling here, so
// any non-dotted tag reaching this point is a resolved same-package
// component name — capital or lowercase — and Scope().Lookup naturally
// returns nil for anything that isn't actually declared.
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
	} else if tag != "" && !strings.Contains(tag, ".") {
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

// attrsOnlyTagNameAt locates the component tag whose name span (open tag, or
// close tag for a paired element) contains the cursor offset off, and returns
// its tag string plus the name span's byte offset and length. Shared by
// attrsOnlyTagDeclAt (go-to-definition) and attrsOnlyTagAt (hover) so both
// features match the SAME tag position — only what each does with the
// resolved tag differs (jump to the value's own declaration vs. show its
// type/signature). Returns found=false when off isn't on any component tag's
// name.
func attrsOnlyTagNameAt(pkg *Package, path string, off int) (tag string, nameStart, nameLen int, found bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return "", 0, 0, false
	}
	f := pkg.Files[path]
	if f == nil {
		return "", 0, 0, false
	}
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if found {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok || !el.IsComponent {
			return true
		}
		t := el.Tag
		elOff := pkg.GSXFset.Position(el.Pos()).Offset
		start := elOff + 1 // skip '<'
		onOpen := off >= start && off < start+len(t)
		onClose := false
		closeStart := 0
		if el.CloseNamePos.IsValid() {
			closeStart = pkg.GSXFset.Position(el.CloseNamePos).Offset
			onClose = off >= closeStart && off < closeStart+len(t)
		}
		if !onOpen && !onClose {
			return true
		}
		tag, nameLen, found = t, len(t), true
		if onOpen {
			nameStart = start
		} else {
			nameStart = closeStart
		}
		return false
	})
	return tag, nameStart, nameLen, found
}

// attrsOnlyTagDeclAt resolves a cursor on a component tag name that names an
// attrs-only component value — a package-level var/func of one of the shapes
// isAttrsOnlyValueType recognizes (docs/superpowers/specs/2026-07-07-
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
// object's type isn't an attrs-only shape, or its position
// doesn't land in real source — so a typo'd/undefined tag or an unrelated
// same-named symbol never produces a false jump.
func attrsOnlyTagDeclAt(pkg *Package, path string, off int) (token.Position, bool) {
	if pkg == nil || pkg.Types == nil || pkg.Fset == nil {
		return token.Position{}, false
	}
	tag, _, _, found := attrsOnlyTagNameAt(pkg, path, off)
	if !found {
		return token.Position{}, false
	}
	obj := attrsOnlyTagObject(pkg, tag)
	if obj == nil || !isAttrsOnlyValueType(obj.Type()) || !obj.Pos().IsValid() {
		return token.Position{}, false
	}
	dp := pkg.Fset.Position(obj.Pos())
	if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
		return token.Position{}, false
	}
	return dp, true
}

// attrsOnlyTagAt resolves a cursor on an attrs-only component-value tag name
// to the value's own types.Object (a *types.Func or *types.Var) plus the
// byte offset/length of the tag-name span — hover's counterpart to
// attrsOnlyTagDeclAt. It reuses the exact same tag-position match
// (attrsOnlyTagNameAt) and resolution (attrsOnlyTagObject +
// isAttrsOnlyValueType) go-to-definition uses; only the returned shape
// differs, since hover renders the object's own signature (via
// types.ObjectString) rather than jumping to its declaration position.
// Returns ok=false under exactly the same conditions attrsOnlyTagDeclAt
// declines to resolve (tag not found, doesn't resolve to a package-scope
// func/var, or that object's type isn't an attrs-only shape) — a
// declaration-position check is unnecessary here since hover never needs a
// jump target.
func attrsOnlyTagAt(pkg *Package, path string, off int) (obj types.Object, nameStart, nameLen int, ok bool) {
	if pkg == nil || pkg.Types == nil {
		return nil, 0, 0, false
	}
	tag, nameStart, nameLen, found := attrsOnlyTagNameAt(pkg, path, off)
	if !found {
		return nil, 0, 0, false
	}
	o := attrsOnlyTagObject(pkg, tag)
	if o == nil || !isAttrsOnlyValueType(o.Type()) {
		return nil, 0, 0, false
	}
	return o, nameStart, nameLen, true
}
