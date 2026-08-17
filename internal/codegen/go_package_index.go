package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	"go/types"
	"maps"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// goPackageAnalysis is the retained type-check of one Go-only main-module
// package: the package, the types.Info its identifiers resolved through, the
// exact companion sources it was built from, and whatever the check reported.
// Unlike the shipping type-only path, the analysis survives its own errors —
// navigation publishes whatever resolved — so both the recoverable errors and
// the identity-mapped index derived from it are retained alongside it.
type goPackageAnalysis struct {
	pkg   *types.Package
	info  *types.Info
	files []companionGoFile
	// typeErrs and sourceErr are the check's own verdict, retained so a cached
	// analysis answers a soundness-demanding caller (shippingGoPackageWith)
	// exactly as the original check did instead of silently promoting a broken
	// package to an importable one on the second call.
	typeErrs  []types.Error
	sourceErr error
	index     *sourceintel.Index // built lazily by GoPackageIndex
}

// goPackageAnalysisWith type-checks one Go-only main-module package inside the
// shipping declaration universe — retained cmd/go syntax + moduleImporter, no
// packages.Load — retaining a types.Info so the package's identifiers can join
// the symbol graph. Unlike shippingGoPackageWith's importer contract, the
// analysis is retained even when the package has type errors or an import
// failed (navigation wants whatever resolved); callers that need a sound
// *types.Package check typeErrs and sourceErr.
func (m *Module) goPackageAnalysisWith(dir string, mi *moduleImporter) (*goPackageAnalysis, error) {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	cached := m.goPkgAnalyses[dir]
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	mi.seen[dir] = true
	defer delete(mi.seen, dir)
	sourcePackage, found, ready := m.targetSourcePackage(dir)
	if !ready || !found {
		return nil, fmt.Errorf("codegen: shipping source inventory has no Go-only package for %s", dir)
	}
	files, importPaths, err := m.companionGoSources(dir, nil)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("codegen: shipping Go-only package %s has no retained compiled source", dir)
	}
	asts := make([]*goast.File, len(files))
	for i, f := range files {
		asts[i] = f.file
	}
	if err := m.rejectExternalBackedgeImports(asts); err != nil {
		return nil, err
	}
	typeEnvironment, err := m.typeCheckEnvironmentForDir(dir)
	if err != nil {
		return nil, err
	}

	// Publish the complete syntactic path before recursive checking. This keeps
	// invalidation correct even when an imported package currently has an error;
	// semantic package publication remains gated on the successful check in
	// shippingGoPackageWith.
	m.recordImports(dir, importPaths)
	// Exactly the three maps sourceintel.BuildIndex reads: Defs/Uses carry every
	// identifier occurrence, Types carries the expression spans hover needs.
	// Nothing consumes Selections, so the checker is not asked to record it.
	a := &goPackageAnalysis{files: files, info: &types.Info{
		Defs:  map[*goast.Ident]types.Object{},
		Uses:  map[*goast.Ident]types.Object{},
		Types: map[goast.Expr]types.TypeAndValue{},
	}}
	config := types.Config{
		Importer:  mi,
		Sizes:     typeEnvironment.sizes,
		GoVersion: typeEnvironment.goVersion,
		Error: func(err error) {
			if typeErr, ok := err.(types.Error); ok {
				a.typeErrs = append(a.typeErrs, typeErr)
			}
		},
	}
	a.pkg = types.NewPackage(sourcePackage.pkgPath, sourcePackage.name)
	_ = types.NewChecker(&config, m.fset, a.pkg, a.info).Files(asts)
	a.sourceErr = mi.sourceErr
	if mi.cycleErr != nil {
		// A cycle is a property of the importer stack that discovered it, not of
		// this package's source, so it is reported without caching a partial
		// analysis the next (differently rooted) check would disagree with.
		return nil, mi.cycleErr
	}

	m.mu.Lock()
	if m.goPkgAnalyses == nil {
		m.goPkgAnalyses = map[string]*goPackageAnalysis{}
	}
	m.goPkgAnalyses[dir] = a
	m.goPackageAnalyses++
	m.mu.Unlock()
	return a, nil
}

// goPackageAnalysisCount returns the number of Go-only package analyses this
// Module actually performed (test hook; a cache hit does not count).
func (m *Module) goPackageAnalysisCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.goPackageAnalyses
}

// GoPackageIndex returns the identity-mapped symbol index of one Go-only
// package (see goPackageAnalysisWith) together with the package it was checked
// into. Type errors are not fatal: whatever resolved is indexed.
func (m *Module) GoPackageIndex(dir string) (*sourceintel.Index, *types.Package, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	ext, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	a, err := m.goPackageAnalysisWith(dir, newModuleImporter(m, ext))
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	index := a.index
	m.mu.Unlock()
	if index != nil {
		return index, a.pkg, nil
	}
	var mapped []sourceintel.MappedFile
	for _, source := range a.files {
		mappedFile, ok, mapErr := m.identityMappedCompanionFile(source, m.fset)
		if mapErr != nil {
			return nil, nil, mapErr
		}
		if !ok {
			continue // fail closed: bytes and retained syntax disagree
		}
		mapped = append(mapped, mappedFile)
	}
	index = sourceintel.BuildIndex(a.info, mapped)
	m.mu.Lock()
	a.index = index
	m.mu.Unlock()
	return index, a.pkg, nil
}

// reverseDependencyGoDirs lists every Go-only main-module package whose
// transitive imports reach a gsx package. Edges come from the retained syntax
// (import specs), resolved through sourcePackageDir; the inventory must be
// ready (any Package/typesPackage call primes it).
func (m *Module) reverseDependencyGoDirs() ([]string, error) {
	m.mu.Lock()
	ready := m.sourceInventoryReady
	sourcePackages := make(map[string]projectSourcePackage, len(m.sourcePackages))
	maps.Copy(sourcePackages, m.sourcePackages)
	gsx := make(map[string]bool, len(m.sourceGsxDirs))
	maps.Copy(gsx, m.sourceGsxDirs)
	m.mu.Unlock()
	if !ready {
		return nil, errors.New("codegen: shipping source inventory is not ready")
	}
	// sourcePackageDir takes m.mu itself, so every graph walk below runs outside
	// the critical section above.
	importsOf := func(p projectSourcePackage) []string {
		var out []string
		for _, path := range p.compiledGoFiles {
			file := p.syntaxByFile[path].file
			if file == nil {
				continue
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					continue
				}
				if dir, ok := m.sourcePackageDir(importPath); ok {
					out = append(out, dir)
				}
			}
		}
		return out
	}
	memo := map[string]bool{}
	var reaches func(dir string, stack map[string]bool) bool
	reaches = func(dir string, stack map[string]bool) bool {
		if gsx[dir] {
			return true
		}
		if reached, ok := memo[dir]; ok {
			return reached
		}
		if stack[dir] {
			return false // an import cycle proves nothing on its own
		}
		stack[dir] = true
		reached := false
		if p, ok := sourcePackages[dir]; ok {
			for _, dep := range importsOf(p) {
				if reaches(dep, stack) {
					reached = true
					break
				}
			}
		}
		delete(stack, dir)
		memo[dir] = reached
		return reached
	}
	var out []string
	for dir := range sourcePackages {
		if !gsx[dir] && reaches(dir, map[string]bool{}) {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SymbolGraph merges the retained analysis of every listed gsx package dir
// (Package) with every reverse-dependency Go-only package (GoPackageIndex).
// Un-analyzable dirs are skipped (partial graph), matching find-references'
// historical tolerance. Returns an error only when nothing could be built.
func (m *Module) SymbolGraph(gsxDirs []string) (*sourceintel.SymbolGraph, error) {
	graph := sourceintel.NewSymbolGraph()
	added := 0
	var firstErr error
	for _, dir := range gsxDirs {
		result, err := m.Package(dir)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// A diagnostics-only PackageResult carries neither an index nor a checked
		// package; there is nothing keyable to merge.
		if result == nil || result.SourceIndex == nil || result.Types == nil {
			continue
		}
		graph.AddIndex(result.SourceIndex, sourceintel.NewKeyer(result.Types))
		added++
	}
	goDirs, err := m.reverseDependencyGoDirs()
	if err != nil && firstErr == nil {
		firstErr = err
	}
	for _, dir := range goDirs {
		index, pkg, err := m.GoPackageIndex(dir)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		graph.AddIndex(index, sourceintel.NewKeyer(pkg))
		added++
	}
	if added == 0 && firstErr != nil {
		return nil, firstErr
	}
	return graph, nil
}
