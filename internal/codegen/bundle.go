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
// harvestFromTypes is THE filter harvest. Every path that builds a filter table
// funnels here: harvestFilters (go list) validates *packages.Package errors and
// then delegates; Module.filterTableFromExt harvests the external importer's
// already-loaded types; NewCachedResolverFromTypes serves the WASM Bundle.
//
// One implementation is not a tidiness preference. Precedence (whole-package
// paths in order, last-wins, then explicit aliases), signature classification,
// and `_gsxf<i>` alias assignment all feed the emitted .x.go, so two harvests
// that disagree produce different generated code from the same input. They did:
// only the go-list copy recognized the removed curried shape, so the same bad
// WithFilter got a migration hint or an unhelpful contract error depending on
// which path ran.
//
// aliases maps each package path to its reserved import alias (filterAliases).
func harvestFromTypes(byPath map[string]*types.Package, pkgPaths []string, explicitAliases []FilterAlias, aliases map[string]string) (map[string][]filterEntry, error) {
	harvested := map[string][]filterEntry{}
	for _, path := range pkgPaths {
		pkg, ok := byPath[path]
		if !ok || pkg == nil {
			return nil, fmt.Errorf("codegen: filter package %q was not loaded (add it to Options.FilterPkgs or Options.LoadPkgs)", path)
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
			return nil, fmt.Errorf("codegen: WithFilter %q: package %q was not loaded", a.Name, a.PkgPath)
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
			// The curried shape gets its own migration message: it was once valid,
			// so "does not match the contract" would not tell an author what to do.
			if isCurriedShape(sig) {
				return nil, fmt.Errorf("codegen: WithFilter %q: filter %q uses the removed curried shape func(args) func(T) R; rewrite as seed-first func([ctx,] subject, args...)", a.Name, a.FuncName)
			}
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
// packages (no subprocess), mirroring loadFilterTableMulti. renderers' package
// paths join aliasPaths (mirroring harvestFilters) so a renderer package
// shares its alias with a same-path filter package, and harvestRenderers runs
// after harvestFromTypes to produce the rendererTable returned alongside the
// filterTable.
func loadFilterTableFromTypes(byPath map[string]*types.Package, pkgPaths []string, explicitAliases []FilterAlias, renderers []RendererAlias) (filterTable, rendererTable, error) {
	if len(pkgPaths) == 0 && len(explicitAliases) == 0 && len(renderers) == 0 {
		return filterTable{}, rendererTable{}, nil
	}
	aliasPaths := pkgPaths
	for _, a := range explicitAliases {
		aliasPaths = append(aliasPaths, a.PkgPath)
	}
	for _, r := range renderers {
		aliasPaths = append(aliasPaths, r.PkgPath)
	}
	aliases := filterAliases(aliasPaths)
	harvested, err := harvestFromTypes(byPath, pkgPaths, explicitAliases, aliases)
	if err != nil {
		return nil, nil, err
	}
	table := filterTable{}
	for name, entries := range harvested {
		table[name] = entries[len(entries)-1] // last-wins
	}
	rt, err := harvestRenderers(byPath, renderers, aliases)
	if err != nil {
		return nil, nil, err
	}
	return table, rt, nil
}

// NewCachedResolverFromTypes builds a Bundle from already-loaded packages
// (e.g. reconstructed from a typebundle) with NO packages.Load and NO subprocess.
// pkgs maps import path -> *types.Package and MUST include the gsx runtime, every
// filterPkg, and every import a generated snippet references. Empty filterPkgs
// defaults to the built-in std filter package.
func NewCachedResolverFromTypes(pkgs map[string]*types.Package, filterPkgs []string, aliases []FilterAlias) (*Bundle, error) {
	filterPkgs = dedupFilterPkgs(filterPkgs)
	table, rt, err := loadFilterTableFromTypes(pkgs, filterPkgs, aliases, nil)
	if err != nil {
		return nil, err
	}
	return &Bundle{imp: mapImporter(pkgs), table: funcTables{filters: table, renderers: rt}}, nil
}
