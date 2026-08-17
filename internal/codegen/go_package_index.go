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
// package: the package, the exact companion sources it was built from, and
// whatever the check reported. Unlike the shipping type-only path, the analysis
// survives its own errors — navigation publishes whatever resolved.
type goPackageAnalysis struct {
	pkg *types.Package
	// info is populated only for the symbol-graph caller (GoPackageIndex): the
	// importer path re-checks Go-only intermediaries on the generate/watch hot
	// path and has no consumer for the maps, so it pays neither their heap nor
	// the checker's recording cost. A cached analysis that was never checked
	// with info (checkedWithInfo false) is re-checked in place when the graph
	// later needs it.
	//
	// info is DROPPED once index is built: the identity index is everything the
	// graph reads, and Defs+Uses over a whole package is the larger allocation
	// by far. checkedWithInfo — not a nil info — is therefore the cache-hit
	// predicate; it holds the invariant "info != nil || index != nil".
	info            *types.Info
	checkedWithInfo bool
	files           []companionGoFile
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
// packages.Load. withInfo asks for the types.Info the symbol graph indexes;
// the importer path leaves it off (see goPackageAnalysis.info), and an analysis
// cached by that path is re-checked when a later withInfo caller needs the maps.
// Unlike shippingGoPackageWith's importer contract, the analysis is retained
// even when the package has type errors or an import failed (navigation wants
// whatever resolved); callers that need a sound *types.Package check typeErrs
// and sourceErr.
func (m *Module) goPackageAnalysisWith(dir string, mi *moduleImporter, withInfo bool) (*goPackageAnalysis, error) {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	cached := m.goPkgAnalyses[dir]
	gsxSource := m.sourceGsxDirs[dir]
	m.mu.Unlock()
	if cached != nil && (!withInfo || cached.checkedWithInfo) {
		return cached, nil
	}
	if gsxSource {
		// The exported entry point (GoPackageIndex) accepts any dir. A gsx package
		// must never be reconstructed from companionGoSources: with no gsxFiles to
		// pair against, its own generated .x.go output would enter as ordinary
		// companion source and be cached as this package's authoritative syntax.
		// Checked here, once, for every caller.
		return nil, fmt.Errorf("codegen: %s is a gsx package; use Package for its symbol index", dir)
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
	a := &goPackageAnalysis{files: files, checkedWithInfo: withInfo}
	if withInfo {
		// Exactly the two maps the symbol graph reads through
		// sourceintel.BuildIndex: Defs and Uses carry every identifier occurrence.
		// Types would only add Expression occurrences, which the graph drops and
		// which exist for hover on .go buffers — a spec non-goal, gopls owns it.
		a.info = &types.Info{
			Defs: map[*goast.Ident]types.Object{},
			Uses: map[*goast.Ident]types.Object{},
		}
	}
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
	// mi is shared with everything else the current walk imports, and it LATCHES
	// the first source error it sees (moduleImporter.Import). So a non-nil
	// sourceErr on entry belongs to some earlier package, and this check cannot
	// have contributed to it; attributing it here would cache a poisoned
	// analysis that no invalidation edge of THIS package can ever clear. Only an
	// error that appears across this check is ours. (The errors are compared to
	// nil, never to each other: sourceDiagnosticsError is uncomparable.)
	//
	// The converse — our own import failing while the latch already holds someone
	// else's error, so sourceErr stays unattributed — is caught by typeErrs: a
	// failed import makes the checker report "could not import …" here, and
	// shippingGoPackageWith refuses to publish a package with type errors.
	sourceErrLatched := mi.sourceErr != nil
	_ = types.NewChecker(&config, m.fset, a.pkg, a.info).Files(asts)
	if !sourceErrLatched {
		a.sourceErr = mi.sourceErr
	}
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
	// Replaces an info-less entry checked earlier by the importer path; both were
	// built from the same retained sources, so the richer one wins.
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
	a, err := m.goPackageAnalysisWith(dir, newModuleImporter(m, ext), true)
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
	// The index is the graph's only consumer of these maps, and it holds direct
	// references to every object it needs: retaining Defs+Uses as well would
	// keep a second, larger copy of the package's identifier table alive for the
	// life of the Module. checkedWithInfo keeps the cache honest about why info
	// is nil, so a later withInfo caller is not made to re-check this package.
	a.info = nil
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
	// reaches answers "does dir transitively import a gsx package", and also
	// reports whether the answer leaned on the cycle guard below. cmd/go rejects
	// import cycles, so a loaded inventory cannot contain one — but this graph is
	// derived from retained syntax that unpublished overrides may have edited
	// into a transient cycle, so the guard stays. A provisional false (one that
	// depends on an edge suppressed because it re-entered the current stack) is
	// only valid for THAT stack, so it is not memoized; without that rule a root
	// visited mid-cycle would poison every later root's answer.
	memo := map[string]bool{}
	var reaches func(dir string, stack map[string]bool) (bool, bool)
	reaches = func(dir string, stack map[string]bool) (bool, bool) {
		if gsx[dir] {
			return true, false
		}
		if reached, ok := memo[dir]; ok {
			return reached, false
		}
		if stack[dir] {
			return false, true
		}
		stack[dir] = true
		reached, provisional := false, false
		if p, ok := sourcePackages[dir]; ok {
			for _, dep := range importsOf(p) {
				depReached, depProvisional := reaches(dep, stack)
				if depReached {
					reached, provisional = true, false
					break
				}
				provisional = provisional || depProvisional
			}
		}
		delete(stack, dir)
		if !provisional {
			memo[dir] = reached
		}
		return reached, provisional
	}
	var out []string
	for dir := range sourcePackages {
		if gsx[dir] {
			continue
		}
		if reached, _ := reaches(dir, map[string]bool{}); reached {
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
//
// Threading: SymbolGraph holds no lock of its own; each Package/GoPackageIndex
// call takes analysisMu independently, so a SetOverride landing between two of
// them yields a graph whose packages were analyzed at different source epochs.
// That is deliberate — holding analysisMu across the whole merge would block
// the editor for the length of a module-wide analysis — and it is detectable:
// every indexed file carries its SourceVersion, so a consumer answering against
// a buffer checks MatchesSource before publishing a span, exactly as it must
// for a graph that has simply gone stale since it was built.
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
