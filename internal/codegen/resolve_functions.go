package codegen

import "sort"

// ResolveFunctions resolves the configured filter and renderer info through a
// Module. Unlike ResolveFilters, this entry point understands module-local GSX
// renderer packages and therefore does not require generated .x.go declarations
// to exist. The Module's external importer is loaded once and reused by both the
// filter harvest and local renderer declaration resolver.
func ResolveFunctions(opts Options) ([]FilterInfo, []RendererInfo, error) {
	m, err := Open(opts)
	if err != nil {
		return nil, nil, err
	}
	return m.resolveFunctions()
}

// resolveFunctions is the reusable Module-level implementation behind
// ResolveFunctions. Keeping it on Module lets tests and future warm callers
// prove repeated introspection reuses the same external importer and completed
// renderer table.
func (m *Module) resolveFunctions() ([]FilterInfo, []RendererInfo, error) {
	if _, err := m.externalImporter(); err != nil {
		return nil, nil, err
	}
	filterPkgs := dedupFilterPkgs(m.opts.FilterPkgs)
	// Reuse the Module's external-types validation path so info reports the same
	// missing/broken filter diagnostics as generation. This performs no load: the
	// external importer above already populated extPkgs and extErrs.
	if _, err := m.filterTableFromExt(filterPkgs); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	extPkgs := m.extPkgs
	m.mu.Unlock()
	aliasPaths := append([]string{}, filterPkgs...)
	for _, a := range m.opts.Aliases {
		aliasPaths = append(aliasPaths, a.PkgPath)
	}
	for _, r := range finalRendererAliases(m.opts.Renderers) {
		aliasPaths = append(aliasPaths, r.PkgPath)
	}
	harvested, err := harvestFromTypes(extPkgs, filterPkgs, m.opts.Aliases, filterAliases(aliasPaths))
	if err != nil {
		return nil, nil, err
	}
	renderers, err := m.rendererBaseTable()
	if err != nil {
		return nil, nil, err
	}
	return resolvedFunctionInfos(harvested, renderers)
}

// resolvedFunctionInfos converts harvested tables into the stable, sorted
// public shapes used by both the external-only and module-aware resolvers.
func resolvedFunctionInfos(harvested map[string][]filterEntry, renderers rendererTable) ([]FilterInfo, []RendererInfo, error) {
	infos := make([]FilterInfo, 0, len(harvested))
	for name, entries := range harvested {
		winner := entries[len(entries)-1]
		var shadows []string
		for _, e := range entries[:len(entries)-1] {
			shadows = append(shadows, e.pkgPath)
		}
		infos = append(infos, FilterInfo{
			Name:    name,
			Pkg:     winner.pkgPath,
			Func:    winner.funcName,
			Ctx:     winner.wantsCtx,
			Shadows: shadows,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })

	rinfos := make([]RendererInfo, 0, len(renderers))
	for key, e := range renderers {
		rinfos = append(rinfos, RendererInfo{TypeKey: key, Pkg: e.pkgPath, Func: e.funcName, HasErr: e.hasErr})
	}
	sort.Slice(rinfos, func(i, j int) bool { return rinfos[i].TypeKey < rinfos[j].TypeKey })
	return infos, rinfos, nil
}
