package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/modpath"
	"github.com/gsxhq/gsx/internal/sourceintel"
	"github.com/gsxhq/gsx/internal/wsnorm"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// importPathForDir maps an absolute package dir under moduleRoot to its Go
// import path. ok is false when dir is not within moduleRoot.
func importPathForDir(moduleRoot, modulePath, dir string) (string, bool) {
	rel, err := filepath.Rel(moduleRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return modulePath, true
	}
	return modulePath + "/" + filepath.ToSlash(rel), true
}

// dirForImportPath is the inverse of importPathForDir. ok is false when
// importPath is not under modulePath (e.g. stdlib or third-party).
func dirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	return modpath.DirForImportPath(moduleRoot, modulePath, importPath)
}

// checkSkeletonPackage type-checks already-parsed package files against imp and
// returns the resulting *types.Package + *types.Info. Type errors are collected
// (not fatal): go/types fills Info best-effort even when some files don't check,
// matching the existing CachedResolver.check behaviour.
func checkSkeletonPackage(dir, pkgName string, files []*goast.File, fset *token.FileSet, imp types.Importer, typeEnvironment typeCheckEnvironment) (*types.Package, *types.Info, []types.Error) {
	info := &types.Info{
		Types:     map[goast.Expr]types.TypeAndValue{},
		Defs:      map[*goast.Ident]types.Object{},
		Uses:      map[*goast.Ident]types.Object{},
		Implicits: map[goast.Node]types.Object{},
	}
	var errs []types.Error
	conf := types.Config{
		Importer:  imp,
		Sizes:     typeEnvironment.sizes,
		GoVersion: typeEnvironment.goVersion,
		Error: func(e error) {
			if te, ok := e.(types.Error); ok {
				errs = append(errs, te)
			}
		},
	}
	pkg := types.NewPackage(dir, pkgName)
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files)
	return pkg, info, errs
}

// moduleImporter owns one coherent shipping declaration universe for every
// authoritative module-local package. GSX directories are rebuilt from shipping
// skeletons; Go-only directories are rebuilt from the retained compiled syntax
// selected by the cold source inventory. Only packages outside that source
// inventory are delegated to external. seen breaks recursion on import cycles;
// cycleErr records the first cycle detected so typesPackageWith can propagate it.
type moduleImporter struct {
	m         *Module
	external  types.Importer
	seen      map[string]bool
	cycleErr  error
	sourceErr error
}

func newModuleImporter(m *Module, external types.Importer) *moduleImporter {
	return &moduleImporter{
		m:        m,
		external: external,
		seen:     map[string]bool{},
	}
}

type sourceDiagnosticsError struct {
	diags []diag.Diagnostic
}

func (e sourceDiagnosticsError) Error() string {
	if len(e.diags) == 0 {
		return "source error"
	}
	d := e.diags[0]
	kind := d.Source + " error"
	if d.Code == "parse-error" {
		kind = "parse error"
	} else if d.Source == "" {
		kind = "source error"
	}
	if d.Start.IsValid() {
		return fmt.Sprintf("%s in %s:%d:%d: %s", kind, d.Start.Filename, d.Start.Line, d.Start.Column, d.Message)
	}
	return kind + ": " + d.Message
}

func diagnosticsFromSourceError(err error) ([]diag.Diagnostic, bool) {
	var perr sourceDiagnosticsError
	if !errors.As(err, &perr) {
		return nil, false
	}
	return append([]diag.Diagnostic(nil), perr.diags...), true
}

// componentPreprocessFailure converts a fresh parse's preprocessing diagnostics
// into the same structured error channel used by parser and skeleton failures.
// A result that is not analysis-ready without a diagnostic is an internal hard
// error: callers must never continue with a partially materialized AST.
func componentPreprocessFailure(dir string, result callSitePreprocessResult, bag *diag.Bag) error {
	if bag.HasErrors() {
		var errorsOnly []diag.Diagnostic
		for _, d := range bag.Sorted() {
			if d.Severity == diag.Error {
				errorsOnly = append(errorsOnly, d)
			}
		}
		return sourceDiagnosticsError{diags: errorsOnly}
	}
	if !result.analysisReady() {
		return fmt.Errorf("codegen: component-call preprocessing for %s failed without a diagnostic", dir)
	}
	return nil
}

// skeletonParseError wraps a go/parser failure on an assembled .x.go skeleton as
// a positioned diagnostic error. gsx treats user Go as an opaque blob, so Go
// that is invalid only in context — e.g. an `import` after a declaration, which
// gsx copies through verbatim — surfaces here, at the skeleton parse. The
// skeleton carries //line directives, so scanner.Error positions are already
// resolved back to the .gsx origin; converting them to sourceDiagnosticsError
// routes the failure through the normal diagnostic path (framed overlay, --json)
// instead of escaping as a bare, frame-less hard error. A non-ErrorList failure
// (should not occur for a parse error, but be safe) is returned unchanged so a
// genuine operational fault still fails loudly rather than masquerading as a
// user diagnostic.
func skeletonParseError(err error) error {
	var list scanner.ErrorList
	if !errors.As(err, &list) {
		return err
	}
	diags := make([]diag.Diagnostic, 0, len(list))
	for _, e := range list {
		diags = append(diags, diag.Diagnostic{
			Start:    e.Pos,
			End:      e.Pos,
			Severity: diag.Error,
			Code:     "parse-error",
			Message:  e.Msg,
			Source:   "parser",
		})
	}
	return sourceDiagnosticsError{diags: diags}
}

func (mi *moduleImporter) Import(path string) (*types.Package, error) {
	if dir, ok := mi.m.sourcePackageDir(path); ok {
		if mi.seen[dir] {
			// cycle guard: return cached package if ready, else signal cycle error.
			mi.m.mu.Lock()
			pkg := mi.m.pkgTypes[dir]
			mi.m.mu.Unlock()
			if pkg != nil {
				return pkg, nil
			}
			err := fmt.Errorf("import cycle through %s", dir)
			mi.cycleErr = err
			return nil, err
		}
		pkg, err := mi.m.typesPackageWith(dir, mi)
		if err != nil && mi.sourceErr == nil {
			if _, ok := diagnosticsFromSourceError(err); ok {
				mi.sourceErr = err
			}
		}
		return pkg, err
	}
	return mi.m.importWithBundleProjectBoundary(path, mi.external)
}

// reverseClosure returns the reverse-reflexive-transitive closure of seeds over
// both independent analysis graphs. Walking both edge sets at every visited
// node is required for alternating paths (target edge followed by shipping
// edge); a union of two separately computed closures would miss those paths.
// Assumes m.mu.
func (m *Module) reverseClosure(seeds []string) map[string]bool {
	out := map[string]bool{}
	stack := append([]string(nil), seeds...)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if out[d] {
			continue
		}
		out[d] = true
		for importer := range m.importedBy[d] {
			if !out[importer] {
				stack = append(stack, importer)
			}
		}
		for importer := range m.targetImportedBy[d] {
			if !out[importer] {
				stack = append(stack, importer)
			}
		}
		for importer := range m.sourceDeclImportedBy[d] {
			if !out[importer] {
				stack = append(stack, importer)
			}
		}
	}
	return out
}

type invalidationScope struct {
	dirs  map[string]bool
	whole bool
}

func (scope invalidationScope) sorted() []string {
	if len(scope.dirs) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(scope.dirs))
	for dir := range scope.dirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// affectedLocked is the one authoritative transition/invalidation primitive.
// Ordinary seeds use the reverse closure across every analysis graph. If that
// closure reaches configured source, its declarations can change lowering in
// every GSX package, including a cold or in-flight package with no retained
// result yet, so the scope expands to the complete authoritative GSX inventory
// and every currently retained local graph/cache directory. Assumes m.mu.
func (m *Module) affectedLocked(seeds []string) invalidationScope {
	cleanSeeds := make([]string, 0, len(seeds))
	seenSeeds := make(map[string]bool, len(seeds))
	for _, dir := range seeds {
		dir = filepath.Clean(dir)
		if !seenSeeds[dir] {
			seenSeeds[dir] = true
			cleanSeeds = append(cleanSeeds, dir)
		}
	}
	scope := invalidationScope{dirs: m.reverseClosure(cleanSeeds)}
	for dir := range scope.dirs {
		if m.rendererDirs[dir] || m.configuredSourceDirs[dir] {
			scope.whole = true
			break
		}
	}
	if !scope.whole {
		return scope
	}
	for dir := range m.sourceGsxDirs {
		scope.dirs[dir] = true
	}
	for path, fact := range m.sourceInventoryFacts {
		if fact.Present() {
			scope.dirs[filepath.Dir(path)] = true
		}
	}
	for path := range m.overrides {
		if strings.HasSuffix(path, ".gsx") {
			scope.dirs[filepath.Dir(path)] = true
		}
	}
	for dir := range m.pkgTypes {
		scope.dirs[dir] = true
	}
	for dir := range m.targetDeclTypes {
		scope.dirs[dir] = true
	}
	for dir := range m.configuredDeclTypes {
		scope.dirs[dir] = true
	}
	for dir := range m.pkgResults {
		scope.dirs[dir] = true
	}
	addForwardGraphDirs(scope.dirs, m.imports)
	addForwardGraphDirs(scope.dirs, m.targetImports)
	addForwardGraphDirs(scope.dirs, m.sourceDeclImports)
	addReverseGraphDirs(scope.dirs, m.importedBy)
	addReverseGraphDirs(scope.dirs, m.targetImportedBy)
	addReverseGraphDirs(scope.dirs, m.sourceDeclImportedBy)
	return scope
}

func addForwardGraphDirs(dirs map[string]bool, graph map[string][]string) {
	for dir, dependencies := range graph {
		dirs[dir] = true
		for _, dependency := range dependencies {
			dirs[dependency] = true
		}
	}
}

func addReverseGraphDirs(dirs map[string]bool, graph map[string]map[string]bool) {
	for dir, importers := range graph {
		dirs[dir] = true
		for importer := range importers {
			dirs[importer] = true
		}
	}
}

// invalidateConfiguredSourceStateLocked drops every cache whose classification
// may depend on a local configured filter, alias, renderer, or class merger.
// Their declarations and completed tables are rebuilt lazily from current
// authoritative source; all retained package analyses follow because any of
// these signatures can alter emitted lowering. The external importer stays
// warm. Assumes m.mu.
func (m *Module) invalidateConfiguredSourceStateLocked() {
	m.funcTbl, m.funcTblErr, m.funcTblDone = funcTables{}, nil, false
	m.rendererPkgs, m.rendererLocal = nil, nil
	m.rendererPkgsErr, m.rendererPkgsDone = nil, false
	m.rendererTbl, m.rendererTblErr, m.rendererTblDone = nil, nil, false
	m.dirFuncTbls = map[string]funcTables{}
	m.classMergersErr, m.classMergersDone = nil, false
	m.pkgTypes = map[string]*types.Package{}
	m.targetDeclTypes = map[string]*types.Package{}
	m.configuredDeclTypes = map[string]*types.Package{}
	m.pkgResults = map[string]*PackageResult{}
}

// invalidateLocked drops the reverse-closure of ordinary dirs from pkgTypes and
// pkgResults. A configured module-local renderer seed instead clears the
// module-wide renderer-dependent state. Assumes m.mu.
func (m *Module) invalidateLocked(dirs []string) []string {
	scope := m.affectedLocked(dirs)
	if scope.whole {
		m.invalidateConfiguredSourceStateLocked()
		return scope.sorted()
	}
	for d := range scope.dirs {
		delete(m.pkgTypes, d)
		delete(m.targetDeclTypes, d)
		delete(m.configuredDeclTypes, d)
		delete(m.pkgResults, d)
	}
	return scope.sorted()
}

// Invalidate drops the reverse-reflexive-transitive closure of dirs (the dirs
// plus every module-local package that transitively imports them) from pkgTypes
// and pkgResults, so each is re-type-checked from current retained source on the
// next use. Graph edges are retained (refreshed on re-analysis). Everything
// outside the closure stays warm, except that a configured module-local renderer
// seed invalidates every retained package classification while preserving the
// external importer/filter state. This supersedes the coarse whole-cache reset.
//
// Threading: Invalidate takes m.mu but is NOT serialized by analysisMu, so callers
// must not invoke it concurrently with an in-flight Package/Generate on the same
// Module (the recursive importer reads pkgTypes under analysisMu without m.mu). The
// LSP never calls it; the normal incremental path is SetOverride → applyDirty.
func (m *Module) Invalidate(dirs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateLocked(dirs)
}

// Dependents returns the GSX-owned projection of the reverse-reflexive-
// transitive closure of dir over the import graph. Internal invalidation keeps
// every Go-only intermediary in the graph, but watch must regenerate only
// authoritative GSX source dirs. The seed is always retained so a changed or
// newly created GSX dir is safe before the next inventory reload. Before a cold
// inventory exists (Bundle and graph-only tests), the complete closure is
// returned because there is no authoritative source classification to apply.
//
// Threading: like Invalidate, Dependents takes m.mu but is NOT serialized by analysisMu,
// so callers must not invoke it concurrently with an in-flight Package/Generate on the
// same Module (the recursive importer reads the graph under analysisMu without m.mu). The
// watch loop is single-goroutine, so this holds.
func (m *Module) Dependents(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	cl := m.reverseClosure([]string{dir})
	out := make([]string, 0, len(cl))
	for d := range cl {
		if m.sourceInventoryReady && d != dir && !m.sourceGsxDirs[d] {
			continue
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// applyDirty consumes the pending-dirty set (populated by SetOverride): it drops
// the ordinary reverse-closure, or all renderer-dependent analysis state when a
// seed is a configured module-local renderer dir, then clears the set. Called at
// the start of each Package/Generate run (under analysisMu).
func (m *Module) applyDirty() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dirty) == 0 {
		return
	}
	seeds := make([]string, 0, len(m.dirty))
	for d := range m.dirty {
		seeds = append(seeds, d)
	}
	m.invalidateLocked(seeds)
	m.dirty = map[string]bool{}
}

// cachedDirs returns the sorted set of dirs currently in pkgTypes (test hook).
func (m *Module) cachedDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pkgTypes))
	for d := range m.pkgTypes {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// cachedResultDirs returns the sorted set of dirs with a cached PackageResult (test hook).
func (m *Module) cachedResultDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pkgResults))
	for d := range m.pkgResults {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// recordImports updates the project-internal shipping import graph for dir,
// REPLACING dir's previous forward edges. Every authoritative module-local
// package becomes an edge, including Go-only intermediaries; external and
// standard-library imports are ignored.
// Replacement keeps the graph precise across import add/remove: because the
// edited package always re-analyzes in the same turn, its outgoing edges are
// refreshed before any later edit could consult them.
//
// paths must include every import that participates in dir's type-check — both
// the .gsx-hoisted import specs and imports from retained compiled Go syntax.
// Resolving the whole local path preserves alternating GSX -> Go-only -> GSX
// invalidation instead of allowing a bridge to sever the reverse closure.
func (m *Module) recordImports(dir string, paths []string) {
	deps := map[string]bool{}
	for _, p := range paths {
		if dd, ok := m.sourcePackageDir(p); ok {
			deps[dd] = true
		}
	}
	m.mu.Lock()
	inventoryReady := m.sourceInventoryReady
	gsxConsumer := m.sourceGsxDirs[filepath.Clean(dir)]
	m.mu.Unlock()
	if !inventoryReady || gsxConsumer {
		// Configured functions are implicit code-generation dependencies only
		// for GSX consumers. Go-only packages participate in the shipping graph,
		// but their type-check does not consult filters, aliases, renderers, or a
		// class merger; adding those edges would invent dependencies absent from
		// both their authored source and their compilation.
		filterPaths := m.opts.FilterPkgs
		if options, ok := m.dirOptionsFor(dir); ok && options.FilterPkgs != nil {
			filterPaths = options.FilterPkgs
		}
		configuredPaths := append([]string(nil), filterPaths...)
		for _, alias := range m.opts.Aliases {
			configuredPaths = append(configuredPaths, alias.PkgPath)
		}
		for _, renderer := range finalRendererAliases(m.opts.Renderers) {
			configuredPaths = append(configuredPaths, renderer.PkgPath)
		}
		if merger := m.classMergerFor(dir); merger != nil {
			configuredPaths = append(configuredPaths, merger.PkgPath)
		}
		for _, path := range configuredPaths {
			if dd, ok := m.sourcePackageDir(path); ok && dd != dir {
				deps[dd] = true
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.imports[dir] {
		if set := m.importedBy[old]; set != nil {
			delete(set, dir)
		}
	}
	newDeps := make([]string, 0, len(deps))
	for dd := range deps {
		newDeps = append(newDeps, dd)
		if m.importedBy[dd] == nil {
			m.importedBy[dd] = map[string]bool{}
		}
		m.importedBy[dd][dir] = true
	}
	sort.Strings(newDeps)
	m.imports[dir] = newDeps
}

// recordTargetImports replaces one package's edges in the exact-target
// declaration graph. It is deliberately separate from recordImports: shipping
// and target analysis publish at different successful boundaries, so either
// phase replacing a shared edge set could discard dependencies owned by the
// other phase.
func (m *Module) recordTargetImports(dir string, paths []string) {
	deps := map[string]bool{}
	for _, path := range paths {
		if depDir, ok := m.sourcePackageDir(path); ok {
			deps[depDir] = true
		}
	}
	newDeps := make([]string, 0, len(deps))
	for depDir := range deps {
		newDeps = append(newDeps, depDir)
	}
	sort.Strings(newDeps)

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.targetImports[dir] {
		if importers := m.targetImportedBy[old]; importers != nil {
			delete(importers, dir)
		}
	}
	for _, depDir := range newDeps {
		if m.targetImportedBy[depDir] == nil {
			m.targetImportedBy[depDir] = map[string]bool{}
		}
		m.targetImportedBy[depDir][dir] = true
	}
	m.targetImports[dir] = newDeps
}

// recordSourceDeclImports replaces one package's edges in the configured
// declaration-source graph. This graph is independent of exact-target edges:
// both phases may resolve the same directory at different times, and neither
// is allowed to erase invalidation provenance owned by the other.
func (m *Module) recordSourceDeclImports(dir string, paths []string) {
	deps := map[string]bool{}
	for _, path := range paths {
		if depDir, ok := m.sourcePackageDir(path); ok {
			deps[depDir] = true
		}
	}
	newDeps := make([]string, 0, len(deps))
	for depDir := range deps {
		newDeps = append(newDeps, depDir)
	}
	sort.Strings(newDeps)

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.sourceDeclImports[dir] {
		if importers := m.sourceDeclImportedBy[old]; importers != nil {
			delete(importers, dir)
		}
	}
	for _, depDir := range newDeps {
		if m.sourceDeclImportedBy[depDir] == nil {
			m.sourceDeclImportedBy[depDir] = map[string]bool{}
		}
		m.sourceDeclImportedBy[depDir][dir] = true
	}
	m.sourceDeclImports[dir] = newDeps
}

// resolveImportPackageName returns the declared package name from the same
// semantic universe used by type checking. Normal modules resolve local names
// from the authoritative source inventory and external names from retained
// *types.Package values. Bundle mode has no cold source inventory, so after its
// exact importer it may use the deliberately bounded GSX-source fallback below.
func (m *Module) resolveImportPackageName(path string) (string, bool) {
	if name, ok := m.loadedPackageName(path); ok {
		return name, true
	}
	external, err := m.externalImporter()
	if err == nil {
		if pkg, importErr := external.Import(path); importErr == nil && pkg != nil && pkg.Name() != "" {
			return pkg.Name(), true
		}
	}
	if m.opts.Bundle == nil {
		return "", false
	}
	depDir, ok := m.sourcePackageDir(path)
	if !ok {
		return "", false
	}
	return m.bundleSourcePackageName(depDir)
}

// bundleSourcePackageName is Bundle mode's bounded replacement for the normal
// Go-command source inventory. It examines only GSX source in one directory
// already accepted by sourcePackageDir, honors overrides (and SourceOnly), and
// rejects inconsistent clauses instead of choosing an arbitrary first file.
func (m *Module) bundleSourcePackageName(dir string) (string, bool) {
	paths := map[string]bool{}
	if !m.opts.SourceOnly {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
		for _, path := range matches {
			paths[path] = true
		}
	}
	m.mu.Lock()
	for path := range m.overrides {
		if filepath.Dir(path) == dir && strings.HasSuffix(path, ".gsx") {
			paths[path] = true
		}
	}
	m.mu.Unlock()
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	name := ""
	fset := token.NewFileSet()
	for _, path := range ordered {
		source, found := m.source(path)
		if !found {
			continue
		}
		file, err := goparser.ParseFile(fset, path, source, goparser.PackageClauseOnly)
		if err != nil || file.Name == nil {
			return "", false
		}
		if name != "" && file.Name.Name != name {
			return "", false
		}
		name = file.Name.Name
	}
	return name, name != ""
}

// importSpecPosition identifies one user import spec within one .gsx file.
type importSpecPosition struct {
	line int
	path string
}

func fileImportSpecs(f *gsxast.File, fset *token.FileSet) []importSpec {
	var specs []importSpec
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		imps, _, _, err := splitChunk(gc.Src)
		if err != nil {
			continue
		}
		if fset != nil && gc.Pos().IsValid() {
			if tf := fset.File(gc.Pos()); tf != nil {
				base := fset.Position(gc.Pos()).Offset
				for i := range imps {
					imps[i].pos = tf.Pos(base + imps[i].srcOff)
				}
			}
		}
		specs = append(specs, imps...)
	}
	return specs
}

// importSpecsByQualifier resolves each ordinary import spec to the exact name
// it binds in this file. It uses the retained Go/source package universe and
// never parses or preprocesses the imported GSX package merely to classify a
// reference in the importing file.
func (m *Module) importSpecsByQualifier(specs []importSpec) map[string]importSpec {
	out := make(map[string]importSpec)
	for _, spec := range specs {
		if spec.name == "." || spec.name == "_" {
			continue
		}
		alias := spec.name
		if alias == "" {
			var ok bool
			alias, ok = m.resolveImportPackageName(spec.path)
			if !ok {
				continue
			}
		}
		out[alias] = spec
	}
	return out
}

// importGraphSnapshot returns deep copies of the forward and reverse import
// graphs for tests. Reverse edges are flattened (dep -> sorted importer dirs).
func (m *Module) importGraphSnapshot() (fwd map[string][]string, rev map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fwd = map[string][]string{}
	for k, v := range m.imports {
		fwd[k] = append([]string(nil), v...)
	}
	rev = map[string][]string{}
	for dep, set := range m.importedBy {
		for imp := range set {
			rev[dep] = append(rev[dep], imp)
		}
		sort.Strings(rev[dep])
	}
	return fwd, rev
}

// isGsxPackage reports whether dir contains at least one .gsx file (disk or
// override).
func (m *Module) isGsxPackage(dir string) bool {
	m.mu.Lock()
	ready := m.sourceInventoryReady
	if ready {
		for path := range m.sourceInventoryFacts {
			if filepath.Dir(path) == dir {
				m.mu.Unlock()
				return true
			}
		}
		m.mu.Unlock()
		return false
	}
	for path := range m.overrides {
		if filepath.Dir(path) == dir && strings.HasSuffix(path, ".gsx") {
			m.mu.Unlock()
			return true
		}
	}
	m.mu.Unlock()
	if m.opts.SourceOnly {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	return len(matches) > 0
}

// typesPackage type-checks dir's skeletons (building a fresh importer rooted at
// dir) and returns/caches the *types.Package. Entry point for external callers.
func (m *Module) typesPackage(dir string) (*types.Package, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	return m.typesPackageWith(dir, newModuleImporter(m, ext))
}

// typesPackageWith does the work, threading the recursive importer through one
// shipping source graph. GSX directories use analyze's shipping skeletons;
// Go-only directories use their retained compiled syntax. Both are cached in
// pkgTypes for package identity, cycle handling, and warm invalidation.
func (m *Module) typesPackageWith(dir string, mi *moduleImporter) (*types.Package, error) {
	dir = filepath.Clean(dir)
	m.mu.Lock()
	if p, ok := m.pkgTypes[dir]; ok {
		m.mu.Unlock()
		return p, nil
	}
	ready := m.sourceInventoryReady
	_, sourceFound := m.sourcePackages[dir]
	gsxSource := m.sourceGsxDirs[dir]
	m.mu.Unlock()
	if ready {
		if !sourceFound {
			return nil, fmt.Errorf("codegen: shipping source inventory has no package for %s", dir)
		}
		if !gsxSource {
			return m.shippingGoPackageWith(dir, mi)
		}
	}
	a, err := m.analyze(dir, mi)
	if err != nil {
		return nil, err
	}
	return a.pkg, nil
}

// shippingGoPackageWith reconstructs one authoritative Go-only package inside
// the shipping declaration universe. The retained ASTs are the Go command's
// active CompiledGoFiles selection from the Module's frozen environment; no
// generated output, disk reparse, or export-data package participates here.
func (m *Module) shippingGoPackageWith(dir string, mi *moduleImporter) (*types.Package, error) {
	mi.seen[dir] = true
	defer delete(mi.seen, dir)
	sourcePackage, found, ready := m.targetSourcePackage(dir)
	if !ready || !found {
		return nil, fmt.Errorf("codegen: shipping source inventory has no Go-only package for %s", dir)
	}
	files, importPaths, err := m.parseTargetCompanionGoFiles(dir, nil)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("codegen: shipping Go-only package %s has no retained compiled source", dir)
	}
	if err := m.rejectExternalBackedgeImports(files); err != nil {
		return nil, err
	}
	typeEnvironment, err := m.typeCheckEnvironmentForDir(dir)
	if err != nil {
		return nil, err
	}

	// Publish the complete syntactic path before recursive checking. This keeps
	// invalidation correct even when an imported package currently has an error;
	// semantic package publication remains gated on the successful check below.
	m.recordImports(dir, importPaths)
	var typeErrs []types.Error
	config := types.Config{
		Importer:  mi,
		Sizes:     typeEnvironment.sizes,
		GoVersion: typeEnvironment.goVersion,
		Error: func(err error) {
			if typeErr, ok := err.(types.Error); ok {
				typeErrs = append(typeErrs, typeErr)
			}
		},
	}
	pkg := types.NewPackage(sourcePackage.pkgPath, sourcePackage.name)
	checker := types.NewChecker(&config, m.fset, pkg, nil)
	_ = checker.Files(files)
	if mi.sourceErr != nil {
		return nil, mi.sourceErr
	}
	if mi.cycleErr != nil {
		return nil, mi.cycleErr
	}
	if err := typeErrorsAsSourceError(typeErrs); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = pkg
	m.mu.Unlock()
	return pkg, nil
}

// analyzed is the full retained result of analyzing one gsx package: the parsed
// gsx files, the type-checked skeleton, harvested type/expr maps, and the
// component cross-index inputs. typesPackage consumes only a.pkg; Module.Package
// (retained analysis) and Module.Generate (codegen) consume the rest.
type analyzed struct {
	pkgName    string
	gsxFiles   map[string]*gsxast.File        // gsx path -> parsed file
	gsxFset    *token.FileSet                 // gsx positions
	skelFset   *token.FileSet                 // skeleton positions (same fset as gsxFset for Module)
	goFiles    []*goast.File                  // parsed skeletons + shared helper
	compsByXGo map[string][]*gsxast.Component // skeleton abs path -> components
	table      funcTables                     // filters + renderers this dir's pipes/render boundaries consult (see filters.go's funcTables)
	merger     *ClassMergerRef                // the class merger for this dir (Options.ClassMerger, or its PerDir override)
	classifier *attrclass.Classifier          // the attrclass.Classifier for this dir (Options.Classifier, or its PerDir override)
	// callSites is nil when preprocessing failed or a later validation step
	// removed any file before skeleton construction. Target discovery therefore
	// consumes either the complete package registry or no registry, never a
	// partially active set with missing skeleton markers.
	callSites         *callSiteRegistry
	targetFacts       map[callSiteID]componentTargetFact
	targetExprFacts   map[gsxast.Node]expressionFact
	targetPackage     *types.Package
	positionalPlan    componentPositionalPackagePlan
	targetErrs        []types.Error     // target-phase-fatal type errors retained privately until exact call planning becomes authoritative
	targetDiagnostics []diag.Diagnostic // target-phase-fatal source diagnostics retained on the same private boundary
	resolved          map[gsxast.Node]types.Type
	exprMap           map[gsxast.Node]goast.Expr
	ctrlMap           map[gsxast.Node]ctrlRef            // control-flow node -> skeleton clause pos + containing node
	sigTypes          map[*gsxast.Component][]SigTypeRef // component -> parameter type spans (go-to-def on a param type)
	pkg               *types.Package
	info              *types.Info
	compByKey         map[string][]*gsxast.Component // componentKey -> component(s); >1 = build-tag variants (for Name + NamePos)
	objKey            map[types.Object]string        // component func object -> componentKey
	componentPlan     componentTargetPlan            // finalized declarations used by every retained LSP index
	bag               *diag.Bag                      // diagnostics from parse + script resolution; used by Generate
	importSpecs       []importSpec                   // hoisted .gsx import specs (for unused-import detection)
	typeErrs          []types.Error                  // raw type errors from checkSkeletonPackage
	unusedImports     map[string][]UnusedImport      // .gsx abs path -> unused imports (Package's LSP surface; see unusedFromSkeletons)
	missingImports    map[string][]MissingImport     // .gsx abs path -> undefined qualifiers (Package's LSP surface; see missingFromSkeletons)
	sourceIndex       *sourceintel.Index             // immutable authored semantic facts harvested from the full skeleton check

}

// unusedImportForSpecs reports whether e is an unused-import error for one of
// a caller-selected set of import specs. go/types spells the error in TWO forms
// ($GOROOT/src/go/types/resolver.go, errorUnusedPkg):
//
//	"path" imported and not used            (plain, dot, or path-named alias)
//	"path" imported as alias and not used   (renamed import)
//
// and both must match — a renamed import (`import comp "…"`) whose only use
// was the failed tag emits the second form. Match is exact, not heuristic:
// the error's //line-adjusted position must land on a sunk spec's own .gsx
// LINE and the quoted path must be that spec's path, so a same-path sibling
// spec on another line keeps its own error.
func unusedImportForSpecs(e types.Error, specs map[string]map[importSpecPosition]bool) (file string, key importSpecPosition, ok bool) {
	if !isUnusedImportMsg(e.Msg) {
		return "", importSpecPosition{}, false
	}
	pos := e.Fset.Position(e.Pos)
	set := specs[pos.Filename]
	if set == nil {
		return "", importSpecPosition{}, false
	}
	// Extract the quoted path from the message (same parse pickImportByPath
	// uses); it is the first quoted string in both message forms.
	i := strings.IndexByte(e.Msg, '"')
	if i < 0 {
		return "", importSpecPosition{}, false
	}
	j := strings.IndexByte(e.Msg[i+1:], '"')
	if j < 0 {
		return "", importSpecPosition{}, false
	}
	key = importSpecPosition{line: pos.Line, path: e.Msg[i+1 : i+1+j]}
	if !set[key] {
		return "", importSpecPosition{}, false
	}
	return pos.Filename, key, true
}

// isUnusedImportMsg matches BOTH go/types unused-import message forms — see
// unusedImportForSpecs's doc.
func isUnusedImportMsg(msg string) bool {
	if strings.Contains(msg, "imported and not used") {
		return true
	}
	return strings.Contains(msg, "imported as ") && strings.Contains(msg, " and not used")
}

type typeErrorCorrespondenceKey struct {
	filename     string
	line, column int
	soft         bool
}

func correspondenceKeyForTypeError(typeErr types.Error) (typeErrorCorrespondenceKey, bool) {
	if typeErr.Fset == nil || !typeErr.Pos.IsValid() {
		return typeErrorCorrespondenceKey{}, false
	}
	position := typeErr.Fset.Position(typeErr.Pos)
	if position.Filename == "" || position.Line <= 0 || position.Column <= 0 {
		return typeErrorCorrespondenceKey{}, false
	}
	return typeErrorCorrespondenceKey{
		filename: position.Filename,
		line:     position.Line,
		column:   position.Column,
		soft:     typeErr.Soft,
	}, true
}

// unmatchedTargetTypeErrors performs one-to-one phase correspondence without
// interpreting checker messages. A reportable full-skeleton error owns one
// target-phase error at the same authored source point and softness class;
// every unpaired target error remains independently fatal and visible.
func unmatchedTargetTypeErrors(targetErrs, fullErrs []types.Error) []types.Error {
	fullByKey := make(map[typeErrorCorrespondenceKey]int, len(fullErrs))
	for _, fullErr := range fullErrs {
		if key, ok := correspondenceKeyForTypeError(fullErr); ok {
			fullByKey[key]++
		}
	}
	unmatched := make([]types.Error, 0, len(targetErrs))
	for _, targetErr := range targetErrs {
		key, ok := correspondenceKeyForTypeError(targetErr)
		if ok && fullByKey[key] > 0 {
			fullByKey[key]--
			continue
		}
		unmatched = append(unmatched, targetErr)
	}
	return unmatched
}

// sortedGsxFilePaths returns gsxFiles' keys in sorted order, so callers that
// derive order-significant output (goFiles' file-processing order, and any
// diagnostics emitted while walking files) from a range over gsxFiles get a
// deterministic result instead of Go's randomized map iteration order. See
// analyze's goFiles-building loop for why order matters: go/types.Checker
// processes files in slice order, and which file it visits first decides
// which of two same-position diagnostics (e.g. a package-scope redeclaration
// across build-tag variants) is the "redeclared" one vs. the "other
// declaration" one.
func sortedGsxFilePaths(gsxFiles map[string]*gsxast.File) []string {
	paths := make([]string, 0, len(gsxFiles))
	for path := range gsxFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// analyze performs the shared parse -> skeleton -> type-check pipeline for one
// gsx package dir, threading the recursive importer mi, and returns the rich
// analyzed result. It preserves Task 4's cycle behaviour: a cycle detected
// during type-check is propagated (without caching) via mi.cycleErr.
func (m *Module) analyze(dir string, mi *moduleImporter) (*analyzed, error) {
	mi.seen[dir] = true
	defer delete(mi.seen, dir)

	// Use the Module-wide shared fset for parse + skeleton so //line directives
	// from buildSkeleton reference valid positions, AND so this package's objects
	// share one FileSet with sibling packages and external deps (see Module's
	// "FileSet" note). For Module the gsx fset and skeleton fset are the same:
	// skeleton idents resolve back to .gsx via the //line directives the parser
	// honoured.
	fset := m.fset
	// bag is created here (using the shared fset for position resolution) so that
	// script-resolution diagnostics recorded below share the same fset as the
	// parsed .gsx files. Generate returns a.bag.Sorted() so errors surface.
	bag := diag.NewBag(fset)
	parsed, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	gsxFiles, pkgName := parsed.files, parsed.name
	companionFiles, goImportPaths, err := m.parseTargetCompanionGoFiles(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	// Materialize embedded markup, classify the now-complete JavaScript tree,
	// and assign stable candidate IDs before any skeleton/probe/emit walk.
	// Component identity remains unstamped until exact target discovery below.
	declNames := packageDeclNamesFromFiles(companionFiles, gsxFiles)
	preprocessed, err := parsed.preprocessComponentCallSites(declNames, fset, m.classifierFor(dir), bag)
	if err != nil {
		return nil, err
	}
	callSites := preprocessed.registry
	// Reserved-prefix validation is lexical and deliberately survives invalid Go
	// expression structure. Run it after preprocessing has annotated excluded
	// GoBlocks, but before a not-ready package is discarded, so an independent
	// later `_gsx` violation is not hidden by an earlier reconstruction error.
	reservedFiles := make(map[string]bool)
	for _, path := range sortedGsxFilePaths(gsxFiles) {
		f := gsxFiles[path]
		if rds := checkReservedDecls(f); len(rds) > 0 {
			reservedFiles[path] = true
			for _, rd := range rds {
				bag.Errorf(rd.pos, rd.pos+token.Pos(len(rd.name)), "", "identifier %q uses the reserved _gsx prefix (reserved for generated code)", rd.name)
			}
		}
	}
	if !preprocessed.analysisReady() {
		gsxFiles = nil // package-level skip: Generate's loop emits nothing
		callSites = nil
	}
	if err := m.validateBundleProjectImports(gsxFiles, fset); err != nil {
		return nil, err
	}
	if len(reservedFiles) != 0 {
		for path := range reservedFiles {
			delete(gsxFiles, path)
		}
		callSites = nil
	}
	pkgPath := dir
	if path, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir); ok {
		pkgPath = path
	}
	targetImporter := newComponentTargetImporter(m, mi.external)
	// The root package is a transient preflight/discovery package, not a target
	// declaration cache entry. Mark it active across both phases so an imported
	// dependency cannot recursively construct a second root universe.
	targetImporter.loading[dir] = true
	defer delete(targetImporter.loading, dir)
	typeEnvironment, err := m.typeCheckEnvironmentForDir(dir)
	if err != nil {
		return nil, err
	}
	componentPlan, err := m.finalizedComponentTargetPlan(dir, pkgPath, pkgName, gsxFiles, parsed.sources, fset, bag, targetImporter, typeEnvironment)
	if err != nil {
		return nil, err
	}
	if componentPlan.invalidMembership {
		gsxFiles = nil
		callSites = nil
	}
	// Per-dir: an imported sibling package resolves its OWN filter table here,
	// because analyze is the recursion point for the import graph.
	table, err := m.filterTableFor(dir, true)
	if err != nil {
		return nil, err
	}
	var targetFacts map[callSiteID]componentTargetFact
	var targetExprFacts map[gsxast.Node]expressionFact
	var targetPackage *types.Package
	var targetRuntime runtimeContract
	var targetPlanningReady bool
	var positionalPlan componentPositionalPackagePlan
	var targetErrs []types.Error
	var targetDiagnostics []diag.Diagnostic
	if callSites != nil && (callSites.hasCandidates() || len(componentPlan.families) != 0) {
		targetBag := diag.NewBag(fset)
		targetResult, unrelatedTargetErrs, targetErr := discoverComponentTargets(
			m,
			dir, pkgPath,
			pkgName, gsxFiles, componentPlan, callSites, table,
			fset, targetBag, targetImporter, typeEnvironment,
		)
		if targetErr != nil {
			return nil, targetErr
		}
		if len(targetResult.diagnostics) != 0 {
			targetErrs = append(targetErrs, unrelatedTargetErrs...)
			targetDiagnostics = append(targetDiagnostics, targetResult.diagnostics...)
			for _, diagnostic := range targetResult.diagnostics {
				bag.Add(diagnostic)
			}
			// Target or runtime incompleteness rejects the package before full
			// skeleton construction. Partial semantic facts never escape.
			gsxFiles = map[string]*gsxast.File{}
			callSites = nil
		} else {
			// Errors outside exact target-marker spans remain fatal, but the full
			// skeleton is their authoritative diagnostic owner. Continuing preserves
			// missing-import/source-error publication without letting positional
			// planning consume incomplete operand facts.
			targetErrs = append(targetErrs, unrelatedTargetErrs...)
			targetFacts = targetResult.facts
			targetExprFacts = targetResult.expressionFacts
			targetPackage = targetResult.pkg
			targetRuntime, err = runtimeContractFromAnalysisPackage(targetPackage)
			if err != nil {
				return nil, err
			}
			targetPlanningReady = len(unrelatedTargetErrs) == 0
			if err := callSites.finalizeComponentIdentity(targetFacts, targetRuntime, fset, bag); err != nil {
				return nil, err
			}
		}
	} else if callSites != nil {
		if err := callSites.finalizeComponentIdentity(nil, runtimeContract{}, fset, bag); err != nil {
			return nil, err
		}
	}
	// Mutual wrapper cycles consume only final semantic stamps.
	reportWrapperCycles(gsxFiles, bag)
	var goFiles []*goast.File
	var mappedFiles []sourceintel.MappedFile
	compsByXGo := map[string][]*gsxast.Component{}
	// gwMarkupsByXGo holds, per skeleton file, the GoWithElements-embedded
	// values' markup lists in source order (buildSkeleton's gwMarkups). Each
	// is probed inline via an `_gsxelem(N)`-marked IIFE in the skeleton;
	// harvestEmbeddedElements uses this slice to resolve those probes back
	// onto the markup's nodes. Kept SEPARATE from compsByXGo (these are not
	// components) so LSP/SigTypeRef consumers of compsByXGo never see them.
	gwMarkupsByXGo := map[string][][]gsxast.Markup{}
	ctrlOffByXGo := map[string]map[gsxast.Node]int{}
	// targetImports are import specs referenced by the exact component-target
	// syntax but intentionally absent from the operand skeleton. They remain
	// named in emitted Go; this set only removes the operand skeleton's false
	// unused-import error after the target pass has owned that reference.
	targetImports := map[string]map[importSpecPosition]bool{}
	var allImportSpecs []importSpec
	// skelByGsx captures exactly what unused_imports_syntactic.go's
	// buildPackageSkeletons builds from a SEPARATE, importer-free parse: this
	// loop already has each file's skeleton AST and hoisted import specs.
	// Reusing it after type-checking (unusedFromSkeletons, below)
	// means Package()'s unused-import detection costs no extra parse, no
	// extra lock, and no re-entry into applyDirty.
	skelByGsx := map[string]fileSkeleton{}
	skelErr := false
	// Iterate gsxFiles in deterministic (sorted-path) order, not map order:
	// this loop appends to goFiles, and goFiles' order is the order
	// checkSkeletonPackage's go/types.Checker processes files in. When two
	// build-tag variant files declare the same package-scope name (e.g. a raw-Go
	// helper redeclared per-variant), go/types blames the SECOND file it sees
	// with "redeclared in this block" and the FIRST with "other declaration of"
	// — both at the same //line-mapped position. Random map order would flip
	// which file is "first" from run to run, flipping which message attaches to
	// that shared position and making diagnostics.golden flaky (see the
	// buildtags/helper_variant corpus case).
	for _, path := range sortedGsxFilePaths(gsxFiles) {
		f := gsxFiles[path]
		// Reserved `_gsx` identifiers are rejected BEFORE the file's skeleton is
		// built, not after it is type-checked. A name that happens to collide with
		// an alias the generator emits today (`var _gsxrt = 1`) would ALSO draw
		// `_gsxrt already declared through import of package gsx` from the skeleton
		// type-check, at the very same position — two diagnostics for one mistake,
		// the useless one first. Skipping the file here (the same per-file skip an
		// attrError takes below) means its skeleton never reaches go/types, so the
		// gsx diagnostic is the only one reported; it is also the only one reported
		// for a name like `_gsxfoo`, which collides with nothing the generator
		// emits today and would otherwise pass silently.
		//
		// The skip is per-file, same as the attrError skip below: a SIBLING file
		// that uses a component declared in this one now draws a spurious
		// `undefined: Comp` on top of the real diagnostic, because this file's
		// component never reaches compsByXGo. That cascade is new for this pass
		// (nothing skipped a file before it ran ahead of buildSkeleton). It is
		// accepted for the same reason the attrError one is: the correctly
		// positioned diagnostic on the actual mistake is worth an extra,
		// obviously-downstream error on a file that did nothing wrong.
		// Body-scope reservation (ctx/children/attrs): a POSITIONED, worded
		// diagnostic upgrading the raw Go collision error. Unlike checkReservedDecls
		// above (and the attrError skip below), this does NOT delete the file: a
		// reserved body binding does not double-report at a single position the way a
		// `_gsx` alias collision does, and the rest of the file's real diagnostics
		// should still surface. The diagnostic is error-severity, so generate still
		// fails (exit 1); the emit/build path is never reached for the offending
		// package (an error diagnostic gates buildability), so no crash and no
		// suppression of sibling diagnostics — the least-suppressive wiring that
		// still fails the build.
		for _, decl := range f.Decls {
			comp, ok := decl.(*gsxast.Component)
			if !ok {
				continue
			}
			for _, rb := range checkReservedBodyBindings(comp) {
				bag.Errorf(rb.pos, rb.pos+token.Pos(len(rb.name)), "reserved-identifier",
					"identifier %q is reserved (%s) — rename it", rb.name, reservedBodyMeaning(rb.name))
			}
		}
		build, berr := buildMappedSkeleton(f, table, fset, bag, &componentPlan, skeletonFull, path, parsed.sources[path])
		if berr != nil {
			// buildSkeleton error handling: a positioned attrError becomes a
			// diagnostic and skips this file; any other error is also recorded as a
			// positionless diagnostic (stripping "codegen: " prefix) and skips the
			// whole package. Neither case is a hard infrastructure error — return
			// nil, err is reserved for fs I/O failures, filter-load failures, etc.
			if ae, ok := errors.AsType[*attrError](berr); ok {
				bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
				delete(gsxFiles, path)
				callSites = nil
				continue
			}
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: strings.TrimPrefix(berr.Error(), "codegen: "), Source: "codegen"})
			skelErr = true
			break
		}
		skel := build.source
		comps := build.components
		imps := build.imports
		ctrlOff := build.ctrlStarts
		gwMarkups := build.markupGroups
		allImportSpecs = append(allImportSpecs, imps...)
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		absXpath := filepath.Join(dir, base+".x.go")
		gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
		if perr != nil {
			return nil, skeletonParseError(perr)
		}
		goFiles = append(goFiles, gf)
		mappedFiles = append(mappedFiles, sourceintel.MappedFile{
			AST:       gf,
			TokenFile: fset.File(gf.Pos()),
			SourceMap: build.sourceMap,
			SourceVersion: sourceintel.SourceVersion{
				Size:   len(parsed.sources[path]),
				SHA256: build.sourceHash,
			},
		})
		compsByXGo[absXpath] = comps
		gwMarkupsByXGo[absXpath] = gwMarkups
		ctrlOffByXGo[absXpath] = ctrlOff
		targetQualifiers := componentTargetQualifiers(callSites, targetFacts, path)
		if len(targetQualifiers) != 0 {
			byQualifier := m.importSpecsByQualifier(imps)
			set := make(map[importSpecPosition]bool)
			for qualifier := range targetQualifiers {
				if spec, ok := byQualifier[qualifier]; ok && spec.pos.IsValid() {
					set[importSpecPosition{line: fset.Position(spec.pos).Line, path: spec.path}] = true
				}
			}
			if len(set) != 0 {
				targetImports[path] = set
			}
		}
		skelByGsx[path] = fileSkeleton{
			skel: gf, imps: imps, targetQualifiers: targetQualifiers,
		}
	}
	if skelErr {
		gsxFiles = map[string]*gsxast.File{} // package-level skip: Generate's loop emits nothing
		callSites = nil
	}
	// Shared _gsxuse/_gsxcompsig helpers, added to every package's overlay.
	//
	// _gsxunwrap's trailing parameter is `...any` (not `...error`): it must NOT
	// reject a non-(T, error) tuple at the unwrap site, because doing so emits a
	// go/types message mentioning _gsxunwrap that stripGsxunwrap cannot clean
	// (`… in argument to _gsxunwrap: string does not implement error`). Non-(T,
	// error) child-prop tuples are instead rejected with a clean gsx diagnostic in
	// genChildComponent (see emit.go). `...any` keeps the field-type compat check
	// intact (it still checks the unwrapped first value T against the field) while
	// swallowing any extra results, so no internal helper name leaks.
	//
	// _gsxuseq quietly harvests child-prop and element-spread types. Errors inside
	// it are suppressed because each expression also has a native typed probe that
	// reports the error once.
	//
	// _gsxusen is the QUIET, ALIGNMENT-NEUTRAL keep-alive: like _gsxuseq its
	// errors are suppressed (harvestProbeSpans matches it too), but unlike
	// _gsxuse/_gsxuseq it is NOT counted by harvest's k-ordering (harvestBody
	// only advances on _gsxuse/_gsxuseq). It carries a composed attrs expression
	// purely to keep its identifiers and filter imports live and type-checked —
	// the bag has no single interp node to harvest onto, and counting it would
	// shift every later interp's harvested type by one slot.
	//
	// _gsxstr is the whole-literal-pipe seed-probe's per-hole placeholder
	// conversion (see analyze.go's embeddedProbeSeed): it always returns
	// `string`, mirroring how EVERY successful branch of the real emit-time
	// holeStringExpr (string(x), strconv.Format*, (x).String()) ALSO always
	// yields a `string`-typed expression — so a seed built with _gsxstr
	// type-checks to the exact same static type as codegen's precisely-typed
	// seed, without needing each hole's real type known yet (impossible at
	// skeleton-build time). Its trailing `...any` tolerates a bare (no-pipe)
	// hole expression that itself returns a (T, error) tuple, exactly like
	// _gsxunwrap's shape.
	helperXgoPath := filepath.Join(dir, "_gsxshared.x.go")
	helper, _ := goparser.ParseFile(fset, helperXgoPath, analysisPreludeSource(pkgName), goparser.SkipObjectResolution)
	goFiles = append(goFiles, helper)

	// Include the exact companion Go syntax retained from the Module's one cold
	// packages.Load. That inventory is the Go command's authoritative active
	// CompiledGoFiles set for the Module's frozen build environment, including
	// custom GOFLAGS tags and cgo-transformed files. A second build.ImportDir or
	// disk parse would select a different universe and would also discard cgo's
	// generated syntax.
	goFiles = append(goFiles, companionFiles...)
	if err := m.rejectExternalBackedgeImports(goFiles); err != nil {
		return nil, err
	}

	// Use the module-qualified import path (not the absolute filesystem dir) as
	// the package path so that type names in diagnostic messages match the batch
	// path's behavior — packages.Load assigns proper import paths, e.g.
	// "corpustest/cases/pkg.Widget", while types.NewPackage(absDir, ...) would
	// produce the raw filesystem path. normalizeDiagPaths would then strip only
	// the temp-dir prefix, leaving "cases/pkg.Widget" instead of "corpustest/cases/pkg.Widget".
	pkg, info, typeErrs := checkSkeletonPackage(pkgPath, pkgName, goFiles, fset, mi, typeEnvironment)
	if mi.sourceErr != nil {
		return nil, mi.sourceErr
	}
	if len(targetImports) != 0 {
		kept := typeErrs[:0]
		for _, e := range typeErrs {
			if _, _, ok := unusedImportForSpecs(e, targetImports); ok {
				continue
			}
			kept = append(kept, e)
		}
		typeErrs = kept
	}
	// Tolerate cross-file build-tag variant redeclarations of raw Go decls: a
	// same-name const/var/type/func across ≥2 files whose //go:build constraints
	// are provably disjoint and whose signatures match is a build variant that
	// go build — not gsx — resolves (icon.gsx `//go:build !never` vs
	// icon_never.gsx `//go:build never`). Non-disjoint tags, a signature
	// mismatch, or a within-file duplicate keep the error. Applied to both the
	// skeleton errors here and the target-plan errors below, since both phases
	// type-check the same colliding files.
	variantConstraints := buildConstraintByFile(gsxFiles)
	variantSites := collectVariantDeclSites(goFiles, fset)
	typeErrs = suppressCrossFileVariantRedeclarations(typeErrs, variantSites, variantConstraints)
	// Collect the skeleton byte spans of every _gsxuseq(...) child-prop or
	// element-spread harvest probe. Each expression is also checked in a native
	// typed context (the props literal or gsx.Attrs assignment), so suppressing
	// errors inside _gsxuseq avoids duplicate diagnostics. Positions are raw
	// token.Pos in the shared fset, directly comparable to a types.Error's Pos.
	//
	// Computed ONCE per *goast.File here (spansByFile), keyed by pointer: every
	// .gsx-derived skeleton in goFiles is the SAME *goast.File instance stored in
	// skelByGsx[path].skel (see the file loop above), so missingFromSkeletons
	// (add_imports.go) looks its file up in this same map instead of re-walking
	// it — the diagnostic the user sees anchors at the props-literal copy, so
	// MissingImport.Pos must match it too, without a second linear pass over
	// every skeleton.
	spansByFile := make(map[*goast.File][]posSpan, len(goFiles))
	var quietSpans []posSpan
	var reportableFullTypeErrs []types.Error
	for _, gf := range goFiles {
		spans := harvestProbeSpans(gf)
		spansByFile[gf] = spans
		quietSpans = append(quietSpans, spans...)
	}
	for _, e := range typeErrs {
		suppressed := false
		for _, s := range quietSpans {
			if s.start <= e.Pos && e.Pos < s.end {
				suppressed = true
				break
			}
		}
		if suppressed {
			continue // redundant quiet harvest-probe error; the native operand context reports it
		}
		p := e.Fset.Position(e.Pos) // e.Fset is the shared fset; //line maps skeleton → .gsx
		if strings.HasSuffix(p.Filename, ".x.go") {
			continue // synthetic skeleton position: no //line directive, so no valid .gsx location to report
		}
		msg := stripGsxunwrap(e.Msg)
		// Exact positional planning owns component inference diagnostics. Every
		// retained skeleton error is therefore a native Go error and passes through
		// verbatim after internal unwrap names are removed.
		bag.Add(diag.Diagnostic{Start: p, End: p, Severity: diag.Error, Message: msg, Source: "types"})
		reportableFullTypeErrs = append(reportableFullTypeErrs, e)
	}
	for _, targetErr := range unmatchedTargetTypeErrors(suppressCrossFileVariantRedeclarations(targetErrs, variantSites, variantConstraints), reportableFullTypeErrs) {
		bag.Add(componentTargetTypeDiagnostic(targetErr))
	}
	if mi.cycleErr != nil {
		// A cycle was detected during this package's type-check; propagate
		// the error without caching so the caller receives it.
		return nil, mi.cycleErr
	}
	sourceIndex := sourceintel.BuildIndex(info, mappedFiles)
	mappedFiles = nil
	if callSites != nil && targetPlanningReady {
		var planningDiagnostics []diag.Diagnostic
		positionalPlan, planningDiagnostics = planComponentPositionalCalls(componentPositionalPlanningInput{
			callSites:       callSites,
			targets:         targetFacts,
			expressionFacts: targetExprFacts,
			runtime:         targetRuntime,
			analysisPackage: targetPackage,
			fset:            fset,
		})
		for _, diagnostic := range planningDiagnostics {
			bag.Add(diagnostic)
		}
	}

	// Cache the type-checked package so every entry point — Package, Generate, and
	// the recursive importer — sees the same freshly-checked result. This eliminates
	// the latent stale-cache bug where Package(A)/Generate(A) called analyze(A)
	// directly without writing pkgTypes[A], so a later importer of A would hit a
	// stale (or absent) entry written by an earlier typesPackageWith call.
	//
	// Lock discipline: release m.mu BEFORE calling m.recordImports, which acquires
	// m.mu internally.
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = pkg
	m.mu.Unlock()

	// Record the project-internal import graph for this package. Only successful
	// analyses reach this point, keeping the graph consistent with type-checked
	// packages. cycleErr paths are excluded (they are not cached in pkgTypes either).
	// Both the .gsx-hoisted imports and the hand-written .go imports are recorded so
	// every sibling gsx package that participates in this package's type-check gets a
	// reverse edge (else editing it would not invalidate this importer).
	importPaths := make([]string, 0, len(allImportSpecs)+len(goImportPaths))
	for _, s := range allImportSpecs {
		importPaths = append(importPaths, s.path)
	}
	importPaths = append(importPaths, goImportPaths...)
	m.recordImports(dir, importPaths)

	// Harvest once into resolved + exprMap (both consumed downstream: resolved by
	// Generate, exprMap surfaced by Package). Build the component cross-index
	// inputs (compByKey / objKey).
	resolved := map[gsxast.Node]types.Type{}
	exprMap := map[gsxast.Node]goast.Expr{}
	compByKey := map[string][]*gsxast.Component{} // logical component key -> component(s); >1 = build-tag variants
	objKey := map[types.Object]string{}           // every public/private skeleton component object -> logical component key
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		comps, ok := compsByXGo[fname]
		if !ok {
			continue
		}
		harvest(gf, comps, info, resolved, exprMap, &componentPlan, nil)
		// Second pass for this file's GoWithElements-embedded values (see
		// buildSkeleton's gwMarkups doc): resolve each inline `_gsxelem(N)`-
		// marked IIFE's probe calls back onto the embedded value's own markup
		// list, using the same ordered probe stream as ordinary component bodies.
		harvestEmbeddedElements(gf, gwMarkupsByXGo[fname], info, resolved, exprMap, nil)
		declLogicalKeys := map[string]string{}
		for _, c := range comps {
			logicalKey := componentPlan.logicalKey(c)
			compByKey[logicalKey] = append(compByKey[logicalKey], c)
			if emission, ok := componentPlan.emission(c); ok {
				if emission.public {
					declLogicalKeys[componentKey(c)] = logicalKey
				}
				if emission.splitBody && emission.bodyName != "" {
					declLogicalKeys[componentKeyWithName(c, emission.bodyName)] = logicalKey
				}
			}
		}
		for _, decl := range gf.Decls {
			fd, ok := decl.(*goast.FuncDecl)
			if !ok {
				continue
			}
			logicalKey, ok := declLogicalKeys[funcDeclKey(fd)]
			if !ok {
				continue
			}
			if obj := info.Defs[fd.Name]; obj != nil {
				objKey[obj] = logicalKey
			}
		}
	}

	// Build CtrlMap: skeleton clause position + containing node per control-flow node.
	ctrlMap := map[gsxast.Node]ctrlRef{}
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		co, ok := ctrlOffByXGo[fname]
		if !ok {
			continue
		}
		clauseText := make(map[gsxast.Node]string, len(co))
		for n := range co {
			clauseText[n] = ctrlClauseText(n)
		}
		sub := buildCtrlMap(gf, fset, co, clauseText)
		maps.Copy(ctrlMap, sub)
	}

	// Build SigTypes: per component, the byte span of each navigable signature
	// region in the .gsx — parameter types, type-parameter names/constraints,
	// and a method receiver type — paired with its type-checked skeleton
	// expression, so the LSP can resolve go-to-def / hover on identifiers there.
	sigTypes := map[*gsxast.Component][]SigTypeRef{}
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		comps, ok := compsByXGo[fname]
		if !ok {
			continue
		}
		for _, c := range comps {
			if refs := buildSigTypeRefs(gf, c, &componentPlan); refs != nil {
				sigTypes[c] = refs
			}
		}
	}

	// Unused imports for the LSP surface (Package's PackageResult.UnusedImports),
	// computed from the skeletons this loop already built and the package
	// already type-checked above — no extra parse, no lock, no packages.Load.
	// See unusedFromSkeletons' doc and the design doc
	// (docs/superpowers/specs/2026-07-09-lsp-unused-imports-design.md).
	unusedImports := unusedFromSkeletons(skelByGsx, pkg)

	// Missing imports for the LSP surface (Package's PackageResult.MissingImports),
	// computed from the same skeletons and the same type-checked info — no extra
	// parse, no lock, no packages.Load. Filters out each file's harvest-probe
	// copy of a child-prop expression using spansByFile, the same per-file spans
	// the type-error loop's quietSpans, above, is built from. See
	// missingFromSkeletons' doc.
	missingImports := missingFromSkeletons(skelByGsx, fset, info, spansByFile)

	return &analyzed{
		pkgName:           pkgName,
		gsxFiles:          gsxFiles,
		gsxFset:           fset,
		skelFset:          fset,
		goFiles:           goFiles,
		compsByXGo:        compsByXGo,
		table:             table,
		merger:            m.classMergerFor(dir),
		classifier:        m.classifierFor(dir),
		callSites:         callSites,
		targetFacts:       targetFacts,
		targetExprFacts:   targetExprFacts,
		targetPackage:     targetPackage,
		positionalPlan:    positionalPlan,
		targetErrs:        targetErrs,
		targetDiagnostics: targetDiagnostics,
		resolved:          resolved,
		exprMap:           exprMap,
		ctrlMap:           ctrlMap,
		sigTypes:          sigTypes,
		pkg:               pkg,
		info:              info,
		compByKey:         compByKey,
		objKey:            objKey,
		componentPlan:     componentPlan,
		bag:               bag,
		importSpecs:       allImportSpecs,
		typeErrs:          typeErrs,
		unusedImports:     unusedImports,
		missingImports:    missingImports,
		sourceIndex:       sourceIndex,
	}, nil
}

// posSpan is a byte-range [start, end) of token.Pos values in a shared
// token.FileSet — comparable directly against another token.Pos from the same
// fset without going through Position()/PositionFor(), since a raw token.Pos
// is just an offset into the fset regardless of any //line directive.
type posSpan struct{ start, end token.Pos }

// harvestProbeSpans returns the skeleton byte spans of f's _gsxuseq(...)
// operand/spread harvest probes and _gsxusen(...) composed-bag keep-alives.
// Each probed expression is also checked in its native typed context, so the
// probe copy is redundant: type errors inside it are suppressed and the user
// diagnostic anchors at the native occurrence. Anything that must agree with that
// diagnostic — MissingImport.Pos, notably (missingFromSkeletons, in
// add_imports.go) — must skip these spans too, or it will point at the
// _gsxuseq copy instead of wherever the diagnostic actually landed.
//
// One AST walk per file; callers that need every file's spans (the type-error
// loop) accumulate this per goFiles entry rather than re-walking.
func harvestProbeSpans(f *goast.File) []posSpan {
	var spans []posSpan
	goast.Inspect(f, func(n goast.Node) bool {
		call, ok := n.(*goast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*goast.Ident); ok && (id.Name == "_gsxuseq" || id.Name == "_gsxusen") {
			spans = append(spans, posSpan{call.Pos(), call.End()})
		}
		return true
	})
	return spans
}

// parsePackageWithFset parses every .gsx in dir into the provided fset and
// returns the private package owner for the one preprocessing transition. The
// shared FileSet remains required for valid skeleton //line directives.
func (m *Module) parsePackageWithFset(dir string, fset *token.FileSet) (*parsedGSXPackage, error) {
	paths := map[string]struct{}{}
	m.mu.Lock()
	inventoryReady := m.sourceInventoryReady
	if inventoryReady {
		for path := range m.sourceInventoryFacts {
			if filepath.Dir(path) == dir {
				paths[path] = struct{}{}
			}
		}
	} else {
		for path := range m.overrides {
			if filepath.Dir(path) == dir && strings.HasSuffix(path, ".gsx") {
				paths[path] = struct{}{}
			}
		}
	}
	m.mu.Unlock()
	if !inventoryReady && !m.opts.SourceOnly {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
		for _, p := range matches {
			paths[p] = struct{}{}
		}
	}
	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Strings(orderedPaths)
	files := map[string]*gsxast.File{}
	sources := map[string][]byte{}
	pkgName := ""
	pkgPath := ""
	classifier := m.classifierFor(dir)
	for _, p := range orderedPaths {
		src, ok := m.source(p)
		if !ok {
			continue
		}
		f, perrs := gsxparser.ParseFileWithClassifier(fset, p, src, 0, classifier)
		if len(perrs) > 0 {
			diags := make([]diag.Diagnostic, 0, len(perrs))
			for _, perr := range perrs {
				pos := fset.Position(perr.Pos)
				end := pos
				if perr.End.IsValid() {
					end = fset.Position(perr.End)
				}
				diags = append(diags, diag.Diagnostic{
					Start:    pos,
					End:      end,
					Severity: diag.Error,
					Code:     "parse-error",
					Message:  perr.Msg,
					Source:   "parser",
				})
			}
			return nil, sourceDiagnosticsError{diags: diags}
		}
		wsnorm.Normalize(f)
		if pkgName != "" && f.Package != pkgName {
			return nil, fmt.Errorf(
				"codegen: GSX package %s contains different package clauses: %s declares %q; %s declares %q",
				dir, pkgPath, pkgName, p, f.Package,
			)
		}
		files[p] = f
		sources[p] = append([]byte(nil), src...)
		if pkgName == "" {
			pkgName = f.Package
			pkgPath = p
		}
	}
	return newParsedGSXPackageWithSources(pkgName, files, sources), nil
}

// stripGsxunwrap removes all occurrences of _gsxunwrap(...) in s, replacing each
// with its argument. This ensures type error messages from the skeleton
// type-checker do not expose the internal _gsxunwrap helper name to users.
// Nested parentheses inside the argument are handled via bracket counting.
func stripGsxunwrap(s string) string {
	const prefix = "_gsxunwrap("
	if !strings.Contains(s, prefix) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		depth := 1
		j := i + len(prefix)
		for j < len(s) && depth > 0 {
			switch s[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			j++
		}
		// s[i+len(prefix) : j-1] is the content inside _gsxunwrap(...)
		b.WriteString(s[i+len(prefix) : j-1])
		s = s[j:]
	}
	return b.String()
}
