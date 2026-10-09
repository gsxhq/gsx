package codegen

import (
	"path/filepath"
	"sort"
)

// DependencyOrder returns dirs reordered so that each dir follows every other
// dir in dirs that it imports, directly or through module-local packages
// outside dirs. Generate and Package cache the package they analyze, so
// visiting a package before its importers lets them reuse it; in the reverse
// order the importer type-checks it on import and the later visit checks it
// again.
//
// The order changes only how much work is done, never any result. Edges
// come from the same source analyze reads, through the shared parse cache, so
// the pass does no work analysis would not repeat. A dir whose imports cannot
// be read contributes no edges; its analysis reports the error, as does the
// first analysis when the module cannot load, in which case dirs come back in
// input order. The walk stops at cached packages, so a warm call costs only
// the uncached part of the closure. Import cycles
// are cut where the walk meets them. Ties keep the input order, and every
// input spelling of a dir is returned as given.
func (m *Module) DependencyOrder(dirs []string) []string {
	// Callers key results by the spelling they passed, so each clean dir maps
	// back to every input spelling of it.
	spellings := make(map[string][]string, len(dirs))
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		spellings[clean] = append(spellings[clean], dir)
	}
	if len(spellings) <= 1 {
		return append([]string(nil), dirs...)
	}

	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	if m.opts.Bundle == nil && !m.opts.SourceOnly {
		// Companion imports need the source inventory. If it cannot load, the
		// first analysis reports that; one failed load here is enough.
		if _, err := m.externalImporter(); err != nil {
			return append([]string(nil), dirs...)
		}
	}

	order := make([]string, 0, len(dirs))
	visited := map[string]bool{}
	var visit func(string)
	visit = func(dir string) {
		if visited[dir] {
			return
		}
		visited[dir] = true
		// A cached package needs no import-time check, and neither do its
		// dependencies: invalidating a dependency drops its importers too, so
		// they are all cached.
		m.mu.Lock()
		_, cached := m.pkgTypes[dir]
		m.mu.Unlock()
		if !cached {
			for _, dep := range m.shippingImportDirs(dir) {
				visit(dep)
			}
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
