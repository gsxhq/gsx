package codegen

import (
	"bytes"
	"fmt"
	"go/types"
)

// RendererAlias is one [renderers] registration: the canonical registered type
// key ("pkgPath.TypeName", optionally *-prefixed for a pointer type) and the
// resolved Go target func. Duplicate TypeKeys are last-wins, like FilterAlias.
type RendererAlias struct {
	TypeKey  string
	PkgPath  string
	FuncName string
}

// rendererEntry is one harvested renderer. result is the func's first result
// type from the renderer package's type-check universe — classify() and the
// emitRender conversions are purely structural/syntactic, so a cross-universe
// types.Type is safe there; it is never compared with types.Identical.
type rendererEntry struct {
	funcName string
	alias    string
	pkgPath  string
	hasErr   bool
	// wantsCtx is true for func(ctx context.Context, T) R / (R, error); false
	// for the ctx-less func(T) R / (R, error) shapes. Harvested here but not
	// yet consumed at emission — applyRenderer threading ctx through the call
	// site is a follow-up (#87 pt. 2).
	wantsCtx bool
	result   types.Type
}

// rendererTable maps a canonical type key (rendererKey) to its renderer.
type rendererTable map[string]rendererEntry

// rendererKey returns the canonical registry key for t: "pkgPath.TypeName" for
// a named type, "*pkgPath.TypeName" for a pointer to one, "" for anything that
// can never match a registration (basic/unnamed types, generic instantiations,
// type params, universe-scope names). Aliases are unwrapped on both levels.
func rendererKey(t types.Type) string {
	t = types.Unalias(t)
	prefix := ""
	if p, ok := t.(*types.Pointer); ok {
		prefix = "*"
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok || n.TypeArgs().Len() > 0 || n.Obj().Pkg() == nil {
		return ""
	}
	return prefix + n.Obj().Pkg().Path() + "." + n.Obj().Name()
}

// harvestRenderers validates and harvests every registered renderer from
// already-loaded packages. It is called from the same seam as
// harvestFromTypes, so every resolver path (go list, warm Module, WASM
// bundle) agrees. Duplicate TypeKeys are last-wins (registration order).
func harvestRenderers(byPath map[string]*types.Package, renderers []RendererAlias, aliases map[string]string) (rendererTable, error) {
	if len(renderers) == 0 {
		return rendererTable{}, nil
	}
	table := rendererTable{}
	for _, r := range renderers {
		pkg, ok := byPath[r.PkgPath]
		if !ok || pkg == nil {
			return nil, fmt.Errorf("codegen: renderer for %q: package %q was not loaded", r.TypeKey, r.PkgPath)
		}
		obj := pkg.Scope().Lookup(r.FuncName)
		if obj == nil {
			return nil, fmt.Errorf("codegen: renderer for %q: func %q not found in package %q", r.TypeKey, r.FuncName, r.PkgPath)
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return nil, fmt.Errorf("codegen: renderer for %q: %q in package %q is not a function", r.TypeKey, r.FuncName, r.PkgPath)
		}
		sig := fn.Type().(*types.Signature)
		// The subject param is normally the only param (func(T) R); a leading
		// context.Context is also accepted, in which case the subject is the
		// param immediately after it (func(ctx context.Context, T) R). A ctx
		// param with nothing after it (no subject) or in any other position
		// is a contract violation, same as any other wrong arity/shape.
		params := sig.Params()
		firstIsCtx := params.Len() >= 1 && isContextContext(params.At(0).Type())
		wantsCtx := firstIsCtx && params.Len() == 2
		subjectOK := wantsCtx || (params.Len() == 1 && !firstIsCtx)
		if sig.Recv() != nil || sig.Variadic() || sig.TypeParams().Len() != 0 ||
			!subjectOK || sig.Results().Len() < 1 || sig.Results().Len() > 2 ||
			(sig.Results().Len() == 2 && !isErrorType(sig.Results().At(1).Type())) {
			return nil, fmt.Errorf("codegen: renderer %q for %q does not match the renderer contract func(T) R, func(T) (R, error), func(ctx context.Context, T) R, or func(ctx context.Context, T) (R, error)", r.FuncName, r.TypeKey)
		}
		subject := params.At(params.Len() - 1)
		if pk := rendererKey(subject.Type()); pk != r.TypeKey {
			return nil, fmt.Errorf("codegen: renderer %q takes %s; registered for %q", r.FuncName, subject.Type(), r.TypeKey)
		}
		res := sig.Results().At(0).Type()
		// The unrenderable-result rejection is deferred to the chain-check
		// pass below: whether res is "not a renderable type" or "chains to
		// another registered renderer" can only be told apart once every
		// registration has been read (last-wins means the full key set isn't
		// known here — a later entry may still register res's key).
		table[r.TypeKey] = rendererEntry{
			funcName: r.FuncName,
			alias:    aliases[r.PkgPath],
			pkgPath:  r.PkgPath,
			hasErr:   sig.Results().Len() == 2,
			wantsCtx: wantsCtx,
			result:   res,
		}
	}
	// Renderers apply exactly once: a result type that is itself registered
	// (including the renderer's own param type) would silently chain or loop,
	// so it is rejected here where all registrations are visible — BEFORE the
	// plain unrenderable check, so a chained-but-structurally-unsupported
	// result (e.g. a non-renderable wrapper struct that itself carries a
	// [renderers] registration — the common case) reports the chain message,
	// not the misleading "not a renderable type" (the type DOES have a
	// renderer). A result that is neither registered nor natively renderable
	// still gets the plain message.
	for key, e := range table {
		if rk := rendererKey(e.result); rk != "" {
			if _, chained := table[rk]; chained {
				if rk == key {
					return nil, fmt.Errorf("codegen: renderer %q for %q returns its own registered type; renderers apply once and never chain", e.funcName, key)
				}
				return nil, fmt.Errorf("codegen: renderer %q for %q returns %q, which has its own renderer; renderers apply once and never chain — return a natively renderable type", e.funcName, key, rk)
			}
		}
		if classify(e.result) == catUnsupported {
			return nil, fmt.Errorf("codegen: renderer %q for %q returns %s, which is not a renderable type", e.funcName, key, e.result)
		}
	}
	return table, nil
}

// effectiveRenderType returns the type a render boundary actually classifies
// for a value of type t: the registered renderer's result type when t's
// canonical key is in the registry, t itself otherwise. It is the type-only
// shadow of applyRenderer (below, which additionally rewrites the expression
// and hoists an error-returning renderer) — kept adjacent so the two can
// never disagree on the registry lookup. The _gsxnum scratch-declaration
// prescan (scopeUsesNumeric / attrsUseNumericScratch / resolvedTypeIsNumeric)
// uses it so a renderer returning int/uint/float triggers the `var _gsxnum
// [32]byte` declaration exactly when the emit path's post-applyRenderer
// classification takes the IntInto/UintInto/FloatInto arm.
func effectiveRenderType(t types.Type, table funcTables) types.Type {
	if e, ok := table.renderers[rendererKey(t)]; ok {
		return e.result
	}
	return t
}

// applyRenderer wraps expr in its registered renderer call when t's canonical
// key is registered, marking the renderer package as imported. An error
// renderer hoists through hoistTupleReturning with the caller's error-return
// statement (the same per-context shapes pipe filters use: "return _gsxerr"
// in a render closure, "return nil, _gsxerr" in an (Attrs, error) thunk).
// Returns the (possibly hoisted) expr and the type the boundary classifies;
// a registry miss returns the inputs unchanged. Renderers apply exactly once
// (harvest rejects chains), so this never recurses.
func applyRenderer(b *bytes.Buffer, expr string, t types.Type, table funcTables, imports map[string]bool, interpTemp *int, errReturn string) (string, types.Type) {
	e, ok := table.renderers[rendererKey(t)]
	if !ok {
		return expr, t
	}
	imports[e.pkgPath] = true
	call := e.alias + "." + e.funcName + "((" + expr + "))"
	if e.hasErr {
		return hoistTupleReturning(b, call, interpTemp, errReturn), e.result
	}
	return call, e.result
}
