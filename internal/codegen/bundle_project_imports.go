package codegen

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type bundleProjectGsxTransitiveError struct {
	directPath string
	gsxPath    string
}

type bundleProjectImportCheck struct {
	epoch   uint64
	finding bundleProjectImportFinding
}

type bundleProjectImportFinding struct {
	gsxPath           string
	externalPath      string
	externalBackedges []string
}

func (e *bundleProjectGsxTransitiveError) Error() string {
	return fmt.Sprintf(
		"codegen: Bundle package %q transitively imports project GSX package %q through prebuilt Go types; use the normal module resolver for this source graph",
		e.directPath,
		e.gsxPath,
	)
}

// importWithBundleProjectBoundary delegates an import only after proving that a
// prebuilt module-local Go-only package cannot lead back into project GSX. Bundle
// has no authoritative compiled-source inventory for that intermediary, so
// accepting its transitive prebuilt GSX types would mix stale and source-owned
// declaration universes. Normal mode has a retained source graph and needs no
// such rejection.
func (m *Module) importWithBundleProjectBoundary(path string, external types.Importer) (*types.Package, error) {
	if external == nil {
		return nil, fmt.Errorf("codegen: no importer for %q", path)
	}
	if m.opts.SourceOnly {
		// SourceOnly has exactly one authored in-memory GSX package. Every import
		// is therefore prebuilt external state; there is no second source-owned
		// package or host module graph to classify, and consulting the filesystem
		// would violate the mode's core contract.
		return external.Import(path)
	}
	if m.opts.Bundle == nil {
		return external.Import(path)
	}
	dir, owned, err := m.bundleOwnedProjectDir(path)
	if err != nil {
		return nil, err
	}
	if !owned {
		pkg, err := external.Import(path)
		if err != nil {
			return nil, err
		}
		localDeps, err := m.bundleExternalBackedges(path, pkg)
		if err != nil {
			return nil, err
		}
		if len(localDeps) != 0 {
			return nil, &externalMainModuleBackedgeError{path: path, localDeps: localDeps}
		}
		return pkg, nil
	}
	if m.isGsxPackage(dir) {
		return external.Import(path)
	}
	pkg, err := external.Import(path)
	if err != nil {
		return nil, err
	}
	finding, err := m.bundleProjectImportFinding(path, pkg)
	if err != nil {
		return nil, err
	}
	if finding.externalPath != "" {
		return nil, &externalMainModuleBackedgeError{
			path:      finding.externalPath,
			localDeps: finding.externalBackedges,
		}
	}
	if finding.gsxPath != "" {
		return nil, &bundleProjectGsxTransitiveError{directPath: path, gsxPath: finding.gsxPath}
	}
	return pkg, nil
}

func (m *Module) bundleExternalBackedges(path string, pkg *types.Package) ([]string, error) {
	for {
		m.mu.Lock()
		epoch := m.sourceManifestEpoch
		if cached, ok := m.bundleProjectImportChecks[path]; ok && cached.epoch == epoch {
			localDeps := append([]string(nil), cached.finding.externalBackedges...)
			m.mu.Unlock()
			return localDeps, nil
		}
		m.mu.Unlock()

		localDeps, err := m.transitiveOwnedProjectImports(pkg)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.sourceManifestEpoch != epoch {
			m.mu.Unlock()
			continue
		}
		if m.bundleProjectImportChecks == nil {
			m.bundleProjectImportChecks = map[string]bundleProjectImportCheck{}
		}
		m.bundleProjectImportChecks[path] = bundleProjectImportCheck{
			epoch: epoch,
			finding: bundleProjectImportFinding{
				externalPath:      path,
				externalBackedges: append([]string(nil), localDeps...),
			},
		}
		m.mu.Unlock()
		return localDeps, nil
	}
}

// transitiveOwnedProjectImports walks the complete prebuilt external package
// graph until it reaches the configured module's actual ownership boundary.
// Nested modules that share the main module's lexical import-path prefix remain
// external and are traversed; an edge into any genuinely owned package is a
// boundary violation and is not followed further.
func (m *Module) transitiveOwnedProjectImports(root *types.Package) ([]string, error) {
	seen := map[*types.Package]bool{}
	localDeps := map[string]bool{}
	var visit func(*types.Package) error
	visit = func(pkg *types.Package) error {
		if pkg == nil || seen[pkg] {
			return nil
		}
		seen[pkg] = true
		for _, imported := range sortedPackageImports(pkg) {
			if imported == nil {
				continue
			}
			_, owned, err := m.bundleOwnedProjectDir(imported.Path())
			if err != nil {
				return err
			}
			if owned {
				localDeps[imported.Path()] = true
				continue
			}
			if err := visit(imported); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(localDeps))
	for path := range localDeps {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// bundleOwnedProjectDir distinguishes source owned by the configured module
// from a nested module whose import path happens to share the configured
// module's lexical prefix. A synthetic child path makes moduleOwnsPath inspect
// the package directory itself for a nested go.mod boundary.
func (m *Module) bundleOwnedProjectDir(importPath string) (string, bool, error) {
	dir, lexical := dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, importPath)
	if !lexical {
		return "", false, nil
	}
	owned, err := moduleOwnsPath(m.opts.ModuleRoot, filepath.Join(dir, ".gsx-bundle-ownership"))
	if err != nil {
		return "", false, fmt.Errorf("codegen: inspect Bundle module ownership for %q: %w", importPath, err)
	}
	return dir, owned, nil
}

func cloneBundleProjectImportFinding(finding bundleProjectImportFinding) bundleProjectImportFinding {
	finding.externalBackedges = append([]string(nil), finding.externalBackedges...)
	return finding
}

func (m *Module) bundleProjectImportFinding(path string, pkg *types.Package) (bundleProjectImportFinding, error) {
	for {
		m.mu.Lock()
		epoch := m.sourceManifestEpoch
		if cached, ok := m.bundleProjectImportChecks[path]; ok && cached.epoch == epoch {
			finding := cloneBundleProjectImportFinding(cached.finding)
			m.mu.Unlock()
			return finding, nil
		}
		m.mu.Unlock()

		finding, err := m.firstTransitiveProjectImportFinding(pkg)
		if err != nil {
			return bundleProjectImportFinding{}, err
		}
		m.mu.Lock()
		if m.sourceManifestEpoch != epoch {
			m.mu.Unlock()
			continue
		}
		if m.bundleProjectImportChecks == nil {
			m.bundleProjectImportChecks = map[string]bundleProjectImportCheck{}
		}
		m.bundleProjectImportChecks[path] = bundleProjectImportCheck{
			epoch:   epoch,
			finding: cloneBundleProjectImportFinding(finding),
		}
		m.mu.Unlock()
		return finding, nil
	}
}

func sortedPackageImports(pkg *types.Package) []*types.Package {
	if pkg == nil {
		return nil
	}
	imports := append([]*types.Package(nil), pkg.Imports()...)
	sort.Slice(imports, func(i, j int) bool {
		if imports[i] == nil {
			return imports[j] != nil
		}
		if imports[j] == nil {
			return false
		}
		return imports[i].Path() < imports[j].Path()
	})
	return imports
}

func (m *Module) firstTransitiveProjectImportFinding(root *types.Package) (bundleProjectImportFinding, error) {
	seen := map[*types.Package]bool{}
	var visit func(*types.Package) (bundleProjectImportFinding, error)
	visit = func(pkg *types.Package) (bundleProjectImportFinding, error) {
		if pkg == nil || seen[pkg] {
			return bundleProjectImportFinding{}, nil
		}
		seen[pkg] = true
		for _, imported := range sortedPackageImports(pkg) {
			if imported == nil {
				continue
			}
			dir, owned, err := m.bundleOwnedProjectDir(imported.Path())
			if err != nil {
				return bundleProjectImportFinding{}, err
			}
			if !owned {
				localDeps, err := m.bundleExternalBackedges(imported.Path(), imported)
				if err != nil {
					return bundleProjectImportFinding{}, err
				}
				if len(localDeps) != 0 {
					return bundleProjectImportFinding{
						externalPath:      imported.Path(),
						externalBackedges: localDeps,
					}, nil
				}
				// A nested-module or unrelated external graph that never re-enters
				// this module remains entirely prebuilt and needs no source check.
				continue
			}
			if m.isGsxPackage(dir) {
				return bundleProjectImportFinding{gsxPath: imported.Path()}, nil
			}
			finding, err := visit(imported)
			if err != nil {
				return bundleProjectImportFinding{}, err
			}
			if finding.gsxPath != "" || finding.externalPath != "" {
				return finding, nil
			}
		}
		return bundleProjectImportFinding{}, nil
	}
	return visit(root)
}

// validateBundleProjectImports runs before any semantic checker, including the
// all-unique/no-call-site fast paths. Importer-level rejection is still required
// as a backstop for every recursive semantic importer; this preflight gives the
// user one stable, positioned GSX diagnostic instead of a go/types import string.
func (m *Module) validateBundleProjectImports(files map[string]*gsxast.File, fset *token.FileSet) error {
	if m.opts.Bundle == nil || len(files) == 0 {
		return nil
	}
	var diagnostics []diag.Diagnostic
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := files[path]
		for _, spec := range fileImportSpecs(file, fset) {
			_, err := m.importWithBundleProjectBoundary(spec.path, m.opts.Bundle.importer())
			var transitive *bundleProjectGsxTransitiveError
			var backedge *externalMainModuleBackedgeError
			if !errors.As(err, &transitive) && !errors.As(err, &backedge) {
				// Ordinary missing/broken imports remain the normal checker's
				// positioned diagnostic responsibility.
				continue
			}
			position := token.Position{}
			if fset != nil && spec.pos.IsValid() {
				position = fset.Position(spec.pos)
			}
			code := "bundle-project-gsx-transitive"
			if backedge != nil {
				code = externalMainModuleBackedgeCode
			}
			diagnostics = append(diagnostics, diag.Diagnostic{
				Start:    position,
				End:      position,
				Severity: diag.Error,
				Code:     code,
				Message:  err.Error(),
				Source:   "codegen",
			})
		}
	}
	if len(diagnostics) == 0 {
		return nil
	}
	return sourceDiagnosticsError{diags: diagnostics}
}
