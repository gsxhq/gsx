package codegen

import (
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
		if sig.Recv() != nil || sig.Variadic() || sig.TypeParams().Len() != 0 ||
			sig.Params().Len() != 1 || sig.Results().Len() < 1 || sig.Results().Len() > 2 ||
			(sig.Results().Len() == 2 && sig.Results().At(1).Type().String() != "error") {
			return nil, fmt.Errorf("codegen: renderer %q for %q does not match the renderer contract func(T) R or func(T) (R, error)", r.FuncName, r.TypeKey)
		}
		if pk := rendererKey(sig.Params().At(0).Type()); pk != r.TypeKey {
			return nil, fmt.Errorf("codegen: renderer %q takes %s; registered for %q", r.FuncName, sig.Params().At(0).Type(), r.TypeKey)
		}
		res := sig.Results().At(0).Type()
		if classify(res) == catUnsupported {
			return nil, fmt.Errorf("codegen: renderer %q for %q returns %s, which is not a renderable type", r.FuncName, r.TypeKey, res)
		}
		table[r.TypeKey] = rendererEntry{
			funcName: r.FuncName,
			alias:    aliases[r.PkgPath],
			pkgPath:  r.PkgPath,
			hasErr:   sig.Results().Len() == 2,
			result:   res,
		}
	}
	// Renderers apply exactly once: a result type that is itself registered
	// (including the renderer's own param type) would silently chain or loop,
	// so it is rejected here where all registrations are visible.
	for key, e := range table {
		rk := rendererKey(e.result)
		if rk == "" {
			continue
		}
		if _, chained := table[rk]; chained {
			if rk == key {
				return nil, fmt.Errorf("codegen: renderer %q for %q returns its own registered type; renderers apply once and never chain", e.funcName, key)
			}
			return nil, fmt.Errorf("codegen: renderer %q for %q returns %q, which has its own renderer; renderers apply once and never chain — return a natively renderable type", e.funcName, key, rk)
		}
	}
	return table, nil
}
