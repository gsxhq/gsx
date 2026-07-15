package codegen

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/packages"
)

type configuredPackageRequest struct {
	path  string
	where string
}

// configuredSourcePackages resolves configured function-provider packages in
// one declaration-only shipping universe. Every module-local path is rebuilt by
// sourceDeclResolver; only packages outside the authoritative source inventory
// use the cold external importer. The returned local map identifies configured
// packages that own GSX source (renderer locality needs that distinction).
func (m *Module) configuredSourcePackages(requests []configuredPackageRequest) (byPath map[string]*types.Package, local map[string]bool, err error) {
	byPath = make(map[string]*types.Package, len(requests))
	local = make(map[string]bool, len(requests))
	if len(requests) == 0 {
		return byPath, local, nil
	}
	external, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	resolver := newConfiguredSourceDeclResolver(m, external)
	for _, request := range requests {
		if request.path == "" {
			return nil, nil, fmt.Errorf("codegen: %s has an empty package path", request.where)
		}
		if _, done := byPath[request.path]; done {
			continue
		}
		if dir, ok := m.sourcePackageDir(request.path); ok {
			m.mu.Lock()
			gsxOwned := m.sourceGsxDirs[dir]
			inventoryReady := m.sourceInventoryReady
			// Register exact configured-source ownership before resolution starts.
			// A failed declaration check is memoized by its table consumer; edits to
			// this root or a dependency edge published during the failed check must
			// therefore clear that failure on the next analysis.
			m.configuredSourceDirs[dir] = true
			m.mu.Unlock()
			if !inventoryReady {
				gsxOwned = m.isGsxPackage(dir)
			}
			pkg, resolveErr := resolver.packageForDir(dir)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("codegen: %s type resolution failed: %w", request.where, resolveErr)
			}
			byPath[request.path] = pkg
			if gsxOwned {
				local[request.path] = true
			}
			continue
		}
		if localDeps := m.externalBackedgeFor(request.path); len(localDeps) != 0 {
			return nil, nil, fmt.Errorf("codegen: %s crosses the external-to-main-module semantic boundary: %w", request.where,
				&externalMainModuleBackedgeError{path: request.path, localDeps: localDeps})
		}

		m.mu.Lock()
		errs := append([]packages.Error(nil), m.extErrs[request.path]...)
		m.mu.Unlock()
		if len(errs) != 0 {
			return nil, nil, fmt.Errorf("codegen: %s type resolution failed: %s", request.where, errs[0])
		}
		pkg, importErr := resolver.Import(request.path)
		if importErr != nil {
			return nil, nil, fmt.Errorf("codegen: %s was not loaded: %w", request.where, importErr)
		}
		if pkg == nil {
			return nil, nil, fmt.Errorf("codegen: %s has no type information", request.where)
		}
		byPath[request.path] = pkg
	}
	return byPath, local, nil
}
