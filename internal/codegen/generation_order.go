package codegen

import (
	"path/filepath"
	"sort"
)

// GenerationOrder returns dirs reordered so that each dir follows every other
// dir in dirs that it imports, directly or through module-local packages
// outside dirs. Generate caches the package it analyzes, so generating a
// package before its importers lets them reuse it; in the reverse order the
// importer type-checks it on import and Generate checks it again, which also
// re-checks every importer of the first copy (#242).
//
// The order changes only how much work Generate does, never its output. Edges
// come from the same source analyze reads, through the shared parse cache, so
// the pass does no work Generate would not repeat. A dir whose imports cannot
// be read contributes no edges; its analysis reports the error. Import cycles
// are cut where the walk meets them. Ties keep the input order, and every
// input spelling of a dir is returned as given.
func (m *Module) GenerationOrder(dirs []string) []string {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()

	// Callers key results by the spelling they passed, so each clean dir maps
	// back to every input spelling of it.
	spellings := make(map[string][]string, len(dirs))
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		spellings[clean] = append(spellings[clean], dir)
	}
	order := make([]string, 0, len(dirs))
	visited := map[string]bool{}
	var visit func(string)
	visit = func(dir string) {
		if visited[dir] {
			return
		}
		visited[dir] = true
		for _, dep := range m.shippingImportDirs(dir) {
			visit(dep)
		}
		order = append(order, spellings[dir]...)
	}
	for _, dir := range dirs {
		visit(filepath.Clean(dir))
	}
	return order
}

// shippingImportDirs returns the sorted module-local dirs dir imports in the
// shipping universe, choosing the GSX or Go-only source exactly as
// typesPackageWith does. Assumes analysisMu.
func (m *Module) shippingImportDirs(dir string) []string {
	m.mu.Lock()
	ready := m.sourceInventoryReady
	source, found := m.sourcePackages[dir]
	gsxSource := m.sourceGsxDirs[dir]
	m.mu.Unlock()

	var paths []string
	switch {
	case ready && !found:
		return nil
	case ready && !gsxSource:
		for _, retained := range source.syntaxByFile {
			filePaths, ok := parsedImportPaths(retained.file)
			if ok {
				paths = append(paths, filePaths...)
			}
		}
	default:
		parsed, err := m.parsePackageWithFset(dir, m.fset)
		if err != nil {
			return nil
		}
		for _, f := range parsed.files {
			for _, spec := range fileImportSpecs(f, nil) {
				paths = append(paths, spec.path)
			}
		}
		_, goImportPaths, err := m.companionGoSources(dir, parsed.files)
		if err == nil {
			paths = append(paths, goImportPaths...)
		}
	}

	seen := map[string]bool{}
	var deps []string
	for _, path := range paths {
		dep, ok := m.sourcePackageDir(path)
		if !ok || dep == dir || seen[dep] {
			continue
		}
		seen[dep] = true
		deps = append(deps, dep)
	}
	sort.Strings(deps)
	return deps
}
