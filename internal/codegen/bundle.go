package codegen

import (
	"fmt"
	"go/types"
)

// This file is the subprocess-free counterpart to the packages.Load-based
// resolver path: it builds a Bundle from already-loaded *types.Package
// values (e.g. reconstructed from an embedded typebundle), so the gsx transform
// can run in a WASM build with no `go list`.
//
// NOTE: harvestFromTypes duplicates the scope-iteration of harvestFilters
// (which is coupled to *packages.Package for its Errors/dir-specific messages).
// Worth DRYing once the WASM path is settled.

// harvestFromTypes harvests filters from already-type-checked packages keyed by
// import path — no packages.Load. aliases maps each package path to its reserved
// import alias (filterAliases). Mirrors harvestFilters' precedence: whole-package
// paths in order (last-wins), then explicit aliases.
func harvestFromTypes(byPath map[string]*types.Package, pkgPaths []string, explicitAliases []FilterAlias, aliases map[string]string) (map[string][]filterEntry, error) {
	harvested := map[string][]filterEntry{}
	for _, path := range pkgPaths {
		pkg, ok := byPath[path]
		if !ok || pkg == nil {
			return nil, fmt.Errorf("codegen: filter package %q has no type information in bundle", path)
		}
		alias := aliases[path]
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			fn, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok {
				continue
			}
			wantsCtx, ok := classifyFilter(sig)
			if !ok {
				continue // non-conforming func skipped during whole-package harvest
			}
			tname := lowerFirst(name)
			harvested[tname] = append(harvested[tname], filterEntry{
				funcName: name,
				wantsCtx: wantsCtx,
				hasErr:   sig.Results().Len() == 2,
				alias:    alias,
				pkgPath:  path,
			})
		}
	}

	for _, a := range explicitAliases {
		pkg, ok := byPath[a.PkgPath]
		if !ok || pkg == nil {
			return nil, fmt.Errorf("codegen: WithFilter %q: package %q has no type information in bundle", a.Name, a.PkgPath)
		}
		obj := pkg.Scope().Lookup(a.FuncName)
		if obj == nil {
			return nil, fmt.Errorf("codegen: WithFilter %q: func %q not found in package %q", a.Name, a.FuncName, a.PkgPath)
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return nil, fmt.Errorf("codegen: WithFilter %q: %q in package %q is not a function", a.Name, a.FuncName, a.PkgPath)
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			return nil, fmt.Errorf("codegen: WithFilter %q: %q in package %q has no signature", a.Name, a.FuncName, a.PkgPath)
		}
		wantsCtx, ok := classifyFilter(sig)
		if !ok {
			return nil, fmt.Errorf("codegen: WithFilter %q: func %q does not match the seed-first filter contract func([ctx,] subject, args...) (R[, error])", a.Name, a.FuncName)
		}
		harvested[a.Name] = append(harvested[a.Name], filterEntry{
			funcName: a.FuncName,
			wantsCtx: wantsCtx,
			hasErr:   sig.Results().Len() == 2,
			alias:    aliases[a.PkgPath],
			pkgPath:  a.PkgPath,
		})
	}
	return harvested, nil
}

// loadFilterTableFromTypes builds the winner-only filter table from pre-loaded
// packages (no subprocess), mirroring loadFilterTableMulti.
func loadFilterTableFromTypes(byPath map[string]*types.Package, pkgPaths []string, explicitAliases []FilterAlias) (filterTable, error) {
	if len(pkgPaths) == 0 && len(explicitAliases) == 0 {
		return filterTable{}, nil
	}
	aliasPaths := pkgPaths
	for _, a := range explicitAliases {
		aliasPaths = append(aliasPaths, a.PkgPath)
	}
	aliases := filterAliases(aliasPaths)
	harvested, err := harvestFromTypes(byPath, pkgPaths, explicitAliases, aliases)
	if err != nil {
		return nil, err
	}
	table := filterTable{}
	for name, entries := range harvested {
		table[name] = entries[len(entries)-1] // last-wins
	}
	return table, nil
}

// NewCachedResolverFromTypes builds a Bundle from already-loaded packages
// (e.g. reconstructed from a typebundle) with NO packages.Load and NO subprocess.
// pkgs maps import path -> *types.Package and MUST include the gsx runtime, every
// filterPkg, and every import a generated snippet references. Empty filterPkgs
// defaults to the built-in std filter package.
func NewCachedResolverFromTypes(pkgs map[string]*types.Package, filterPkgs []string, aliases []FilterAlias) (*Bundle, error) {
	filterPkgs = dedupFilterPkgs(filterPkgs)
	table, err := loadFilterTableFromTypes(pkgs, filterPkgs, aliases)
	if err != nil {
		return nil, err
	}
	return &Bundle{imp: mapImporter(pkgs), table: table}, nil
}
