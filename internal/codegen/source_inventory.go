package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/types"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/sourceview"
	"golang.org/x/tools/go/packages"
)

// projectSourcePackage is the Go command's active compiled-file selection for
// one retained source package. The main module retains every owned package;
// external packages that lead back into the main module are recorded only as a
// rejected semantic boundary and are never reconstructed in a local universe.
type projectSourcePackage struct {
	pkgPath         string
	name            string
	compiledGoFiles []string
	syntaxByFile    map[string]*goast.File
	metadataErrors  []packages.Error
	invariantErrors []string
	sizes           types.Sizes
	goVersion       string
}

// typeCheckEnvironment is the complete target-dependent input to every
// manual go/types check. It is retained from the same packages.Load that
// selected the package's compiled syntax; allowing either field to fall back
// to go/types defaults would silently type-check a different Go universe.
type typeCheckEnvironment struct {
	sizes     types.Sizes
	goVersion string
}

// packageLanguageVersion converts cmd/go's module metadata to the form
// go/types.Config expects. A module without a go directive has the cmd/go
// specified default language version go1.16; the empty metadata value is not
// an unknown version when module provenance is present.
func packageLanguageVersion(pkg *packages.Package) (string, bool) {
	if pkg == nil || pkg.Module == nil {
		return "", false
	}
	version := pkg.Module.GoVersion
	if version == "" {
		return "go1.16", true
	}
	if !strings.HasPrefix(version, "go") {
		version = "go" + version
	}
	return version, true
}

type gsxSourceInventoryFact = sourceview.FileFact

func inspectGsxSourceInventory(path string, source []byte, present bool) (gsxSourceInventoryFact, []string) {
	fact := sourceview.Inspect(path, source, present)
	return fact, fact.Imports()
}

// RefreshDiskSources refreshes the complete saved .gsx membership and
// package/import facts for dirs. It is the disk counterpart to SetOverride and
// must run before Invalidate in a long-lived normal-mode caller such as watch.
// Every create, write, rename, and remove follows this same exact directory
// scan; callers do not classify events into "body" versus "dependency" edits.
//
// A body-only change preserves the cold importer. Package membership/clause
// changes, or an import addition absent from the published importer, mark the
// source inventory for an atomic FileSet/importer rebuild at the next analysis.
// RefreshDiskSources serializes the refresh itself against analysis. Callers
// that also need invalidation should use RefreshDiskSourcesAndInvalidate so no
// analysis can observe refreshed saved bytes through stale retained facts.
func (m *Module) RefreshDiskSources(dirs ...string) error {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	_, err := m.refreshDiskSources(dirs...)
	return err
}

// RefreshVerdict reports whether a disk refresh left the Module's cold world
// pending an in-place reload, and — when so — a deterministic attribution a
// caller can surface to a developer. It is returned by value from the exact
// critical section that published the refresh (see refreshVerdictLocked):
// reading m.goSourceReload/m.sourceReloadReasons through a later, separate
// m.mu acquisition would risk attributing a concurrent unrelated transition
// (SetOverride/ClearOverride do not serialize against
// RefreshDiskSourcesAndInvalidate) to this call's refresh.
type RefreshVerdict struct {
	WorldReloadPending bool
	Reason             sourceview.ReloadReason // ReloadGoSource for authored Go changes
	Path               string                  // representative file that forced the reload, module-relative; "" when unknown
}

// Describe renders the verdict as a short, human-readable reason suitable for
// console/panel output, e.g. "changed Go source dep/dep.go" or "new import
// outside the loaded world in page/page.gsx". It is "" when no reload is
// pending.
func (v RefreshVerdict) Describe() string {
	if !v.WorldReloadPending {
		return ""
	}
	switch v.Reason {
	case sourceview.ReloadGoSource:
		return "changed Go source " + v.Path
	case sourceview.ReloadImports:
		return "new import outside the loaded world in " + v.Path
	case sourceview.ReloadMembership:
		return "package membership changed in " + v.Path
	case sourceview.ReloadPackage:
		return "package clause changed in " + v.Path
	default:
		return ""
	}
}

// reloadAttribution is the persisted cause of a pending Go-source world
// reload. Unlike m.sourceReloadReasons (a live per-path map that already
// remembers every currently-outstanding .gsx reason), m.goSourceReload is a
// single flag with no memory of which file tripped it — so the call that
// flips it captures the cause here for verdicts returned by later calls
// whose own refresh found nothing new to attribute.
type reloadAttribution struct {
	reason sourceview.ReloadReason
	path   string // module-relative, slash-separated; "" when unknown
}

// RefreshDiskSourcesAndInvalidate atomically refreshes the complete saved-source
// inventory for dirs, computes their exact retained reverse closure, and evicts
// that closure while analysis is excluded. This is the LSP watched-file
// transition: returning affected dirs from the same critical section prevents a
// concurrent Package call from republishing facts from the pre-refresh view.
func (m *Module) RefreshDiskSourcesAndInvalidate(dirs ...string) ([]string, RefreshVerdict, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	verdict, err := m.refreshDiskSources(dirs...)
	if err != nil {
		return nil, RefreshVerdict{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.invalidateLocked(dirs), verdict, nil
}

func (m *Module) refreshDiskSources(dirs ...string) (RefreshVerdict, error) {
	if len(dirs) == 0 {
		return RefreshVerdict{}, nil
	}
	if m.opts.Bundle != nil || m.opts.SourceOnly {
		return RefreshVerdict{}, fmt.Errorf("codegen: RefreshDiskSources requires a normal source-backed Module")
	}
	root := filepath.Clean(m.opts.ModuleRoot)
	dirSet := map[string]bool{}
	for _, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return RefreshVerdict{}, fmt.Errorf("codegen: resolve disk-source refresh directory %s: %w", dir, err)
		}
		abs = filepath.Clean(abs)
		if !pathWithin(root, abs) {
			return RefreshVerdict{}, fmt.Errorf("codegen: disk-source refresh directory %s is outside module root %s", abs, root)
		}
		owned, err := moduleOwnsPath(root, filepath.Join(abs, ".gsx-refresh-ownership"))
		if err != nil {
			return RefreshVerdict{}, fmt.Errorf("codegen: inspect disk-source refresh ownership for %s: %w", abs, err)
		}
		if !owned {
			return RefreshVerdict{}, fmt.Errorf("codegen: disk-source refresh directory %s is not owned by module root %s", abs, root)
		}
		dirSet[abs] = true
	}

	for {
		m.mu.Lock()
		epoch := m.sourceManifestEpoch
		snapshotEpoch := m.sourceSnapshotEpoch
		fset := m.fset
		inventoryReady := m.sourceInventoryReady
		saved := m.savedSourceManifest
		overrides := sourceOverridesForDirs(m.overrides, dirSet)
		allOverrides := cloneSourceOverrides(m.overrides)
		savedFiles := make(map[string]sourceview.FileSnapshot, len(m.savedFileSnapshots))
		for path, snapshot := range m.savedFileSnapshots {
			savedFiles[path] = cloneSourceFileSnapshot(snapshot)
		}
		oldFacts := sourceInventoryFactsForDirs(m.sourceInventoryFacts, dirSet)
		m.mu.Unlock()

		base := saved
		var err error
		if base == nil {
			base, err = sourceview.Build(sourceview.BuildOptions{
				ModuleRoot: m.opts.ModuleRoot,
				ModulePath: m.opts.ModulePath,
				Overrides:  allOverrides,
			})
		}
		if err != nil {
			return RefreshVerdict{}, fmt.Errorf("codegen: refresh saved source manifest: %w", err)
		}
		if len(savedFiles) != 0 {
			base, err = base.WithFileSnapshots(savedFiles)
			if err != nil {
				return RefreshVerdict{}, fmt.Errorf("codegen: apply captured saved source before refresh: %w", err)
			}
		}
		orderedDirs := make([]string, 0, len(dirSet))
		for dir := range dirSet {
			orderedDirs = append(orderedDirs, dir)
		}
		sort.Strings(orderedDirs)
		refreshed, err := base.RefreshDirs(orderedDirs)
		if err != nil {
			return RefreshVerdict{}, fmt.Errorf("codegen: refresh saved source manifest: %w", err)
		}
		effective, err := refreshed.WithOverrides(allOverrides)
		if err != nil {
			return RefreshVerdict{}, fmt.Errorf("codegen: apply overrides to refreshed source manifest: %w", err)
		}
		viewErr := effective.CheckReadable()
		newFacts := sourceInventoryFactsForDirs(effective.Facts(), dirSet)
		manifestChanged := !equalSourceInventoryFacts(oldFacts, newFacts)

		m.mu.Lock()
		if m.sourceManifestEpoch != epoch || m.sourceSnapshotEpoch != snapshotEpoch || m.fset != fset ||
			m.sourceInventoryReady != inventoryReady ||
			m.savedSourceManifest != saved ||
			!equalSourceOverrides(overrides, sourceOverridesForDirs(m.overrides, dirSet)) {
			m.mu.Unlock()
			continue
		}
		if m.sourceInventoryFacts == nil {
			m.sourceInventoryFacts = map[string]gsxSourceInventoryFact{}
		}
		for path := range m.sourceInventoryFacts {
			if dirSet[filepath.Dir(path)] {
				delete(m.sourceInventoryFacts, path)
			}
		}
		maps.Copy(m.sourceInventoryFacts, newFacts)
		m.savedSourceManifest = refreshed
		// Helper-name collision analysis is build-oblivious: inactive files,
		// same-package tests, and orphaned generated files all participate even
		// when their appearance does not change the active packages.Load graph.
		// Publish the exact refreshed effective snapshot independently of the cold
		// sourceManifest so a helper-only disk event can stay warm without leaving
		// direct generation on stale names.
		m.helperGoSourceManifest = effective
		// Disk counterpart of the override rule at module.go:443-445: a changed or
		// added/removed authored .go file invalidates the retained cold world's
		// types, which only an inventory reload can refresh. Paired generated
		// outputs are excluded — the session's own .x.go writes must never reload.
		goChanged, goChangedPath := goSourceChangedInDirs(saved, refreshed, dirSet)
		if goChanged {
			m.sourceManifestEpoch++
			m.goSourceReload = true
			m.sourceInventoryDirty = true
		}
		for path := range m.savedFileSnapshots {
			if dirSet[filepath.Dir(path)] {
				delete(m.savedFileSnapshots, path)
			}
		}
		m.sourceSnapshotEpoch++
		if manifestChanged {
			m.sourceManifestEpoch++
		}
		paths := make(map[string]bool, len(oldFacts)+len(newFacts))
		for path := range oldFacts {
			paths[path] = true
		}
		for path := range newFacts {
			paths[path] = true
		}
		for path := range paths {
			fact, present := newFacts[path]
			m.updateSourceReloadReasonLocked(path, fact, present)
		}
		// Computed here, under the same m.mu section that just published the
		// refresh: a caller-visible read via a later, separate Lock could
		// observe an interleaved SetOverride/ClearOverride's transition
		// instead of (or in addition to) this call's own.
		verdict := m.refreshVerdictLocked(goChanged, goChangedPath)
		m.mu.Unlock()
		if viewErr != nil {
			return RefreshVerdict{}, fmt.Errorf("codegen: refreshed saved source view: %w", viewErr)
		}
		return verdict, nil
	}
}

// refreshVerdictLocked computes the world-reload verdict for a refresh this
// call just published. Must be called with m.mu held, after
// updateSourceReloadReasonLocked has folded in every path this call touched,
// so both goSourceReload and sourceReloadReasons reflect this call's result.
//
// Attribution is deterministic: a Go-source diff found by THIS call always
// wins (freshest and most specific), then the lexicographically-first
// currently-outstanding .gsx reload reason (sourceReloadReasons is a live
// map covering the whole Module, not just this call's dirs, so an earlier
// call's entry for an untouched dir still surfaces here — the row-7
// persistence case). Only when goSourceReload is pending purely from an
// earlier call, with no current .gsx reason to explain it, does this fall
// back to the persisted m.reloadAttribution: goSourceReload itself is a
// single flag with no memory of which file tripped it.
func (m *Module) refreshVerdictLocked(goChanged bool, goChangedPath string) RefreshVerdict {
	verdict := RefreshVerdict{WorldReloadPending: m.goSourceReload || len(m.sourceReloadReasons) != 0}
	if !verdict.WorldReloadPending {
		return verdict
	}
	switch {
	case goChanged:
		verdict.Reason = sourceview.ReloadGoSource
		verdict.Path = reloadDescriptionPath(m.opts.ModuleRoot, goChangedPath)
		m.reloadAttribution = reloadAttribution{reason: verdict.Reason, path: verdict.Path}
	case len(m.sourceReloadReasons) != 0:
		reason, path := firstSourceReloadReason(m.sourceReloadReasons)
		verdict.Reason = reason
		verdict.Path = reloadDescriptionPath(m.opts.ModuleRoot, path)
	default:
		verdict.Reason = m.reloadAttribution.reason
		verdict.Path = m.reloadAttribution.path
	}
	return verdict
}

// firstSourceReloadReason returns the lexicographically-first path in
// reasons and its reason, for deterministic verdict attribution when more
// than one .gsx file is currently outstanding.
func firstSourceReloadReason(reasons map[string]sourceview.ReloadReason) (sourceview.ReloadReason, string) {
	paths := make([]string, 0, len(reasons))
	for path := range reasons {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	path := paths[0]
	return reasons[path], path
}

// reloadDescriptionPath renders an absolute path relative to the module root
// for verdict attribution text, matching the display form used elsewhere
// (e.g. module_importer.go's import-path derivation). Falls back to the raw
// path when it cannot be made relative, which should not happen for a
// module-owned path.
func reloadDescriptionPath(moduleRoot, absPath string) string {
	if absPath == "" {
		return ""
	}
	rel, err := filepath.Rel(moduleRoot, absPath)
	if err != nil {
		return absPath
	}
	return filepath.ToSlash(rel)
}

// goSourceChangedInDirs reports whether any authored .go snapshot in dirs
// differs between two manifests, and when so, the lexicographically-first
// differing absolute path — the verdict's deterministic attribution when a
// refresh touches more than one changed file. nil old means first
// publication: nothing was retained yet, so nothing can be stale.
//
// Per-dir comparison goes through Manifest.HelperGoFilesDiff, the non-copying
// counterpart of HelperGoFiles: it compares retained state and source bytes
// by identity-then-equal instead of cloning every candidate snapshot (map
// entries via HelperGoFiles, then byte slices via FileSnapshot.Source) only
// to discard the copies here.
func goSourceChangedInDirs(old, new *sourceview.Manifest, dirs map[string]bool) (bool, string) {
	if old == nil {
		return false, ""
	}
	paired := func(m *sourceview.Manifest) map[string]bool {
		out := map[string]bool{}
		for _, p := range m.PairedOutputs() {
			if dirs[filepath.Dir(p)] {
				out[p] = true
			}
		}
		return out
	}
	oldPaired, newPaired := paired(old), paired(new)
	var changed []string
	for dir := range dirs {
		changed = append(changed, old.HelperGoFilesDiff(new, dir, oldPaired, newPaired)...)
	}
	if len(changed) == 0 {
		return false, ""
	}
	sort.Strings(changed)
	return true, changed[0]
}

func cloneSourceOverrides(overrides map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(overrides))
	for path, source := range overrides {
		out[path] = bytes.Clone(source)
	}
	return out
}

func sourceOverridesForDirs(overrides map[string][]byte, dirs map[string]bool) map[string][]byte {
	out := map[string][]byte{}
	for path, source := range overrides {
		if dirs[filepath.Dir(path)] && strings.HasSuffix(path, ".gsx") {
			out[path] = append([]byte(nil), source...)
		}
	}
	return out
}

func sourceInventoryFactsForDirs(facts map[string]gsxSourceInventoryFact, dirs map[string]bool) map[string]gsxSourceInventoryFact {
	out := map[string]gsxSourceInventoryFact{}
	for path, fact := range facts {
		if dirs[filepath.Dir(path)] {
			out[path] = fact
		}
	}
	return out
}

func equalSourceOverrides(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for path, source := range left {
		if other, ok := right[path]; !ok || !bytes.Equal(source, other) {
			return false
		}
	}
	return true
}

func equalSourceInventoryFacts(left, right map[string]gsxSourceInventoryFact) bool {
	if len(left) != len(right) {
		return false
	}
	for path, fact := range left {
		if other, ok := right[path]; !ok || other != fact {
			return false
		}
	}
	return true
}

// buildSourceInventorySnapshots derives one effective manifest from the
// explicitly refreshed saved snapshot plus the current override layer. The
// caller's snapshot epoch check rejects either layer changing during a cold
// load.
func (m *Module) buildSourceInventorySnapshots() (*sourceview.Manifest, *sourceview.Manifest, error) {
	m.mu.Lock()
	saved := m.savedSourceManifest
	savedFiles := make(map[string]sourceview.FileSnapshot, len(m.savedFileSnapshots))
	for path, snapshot := range m.savedFileSnapshots {
		savedFiles[path] = cloneSourceFileSnapshot(snapshot)
	}
	overrides := make(map[string][]byte, len(m.overrides))
	for path, source := range m.overrides {
		overrides[path] = bytes.Clone(source)
	}
	m.mu.Unlock()
	if saved == nil {
		var err error
		saved, err = sourceview.Build(sourceview.BuildOptions{
			ModuleRoot: m.opts.ModuleRoot,
			ModulePath: m.opts.ModulePath,
			Overrides:  overrides,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("codegen: build saved source manifest: %w", err)
		}
	}
	if len(savedFiles) != 0 {
		var err error
		saved, err = saved.WithFileSnapshots(savedFiles)
		if err != nil {
			return nil, nil, fmt.Errorf("codegen: apply saved file snapshots: %w", err)
		}
	}
	if len(overrides) == 0 {
		if err := saved.CheckReadable(); err != nil {
			return nil, nil, fmt.Errorf("codegen: saved source view: %w", err)
		}
		return saved, saved, nil
	}
	effective, err := saved.WithOverrides(overrides)
	if err != nil {
		return nil, nil, fmt.Errorf("codegen: apply source overrides: %w", err)
	}
	if err := effective.CheckReadable(); err != nil {
		return nil, nil, fmt.Errorf("codegen: saved source view: %w", err)
	}
	return saved, effective, nil
}

func (m *Module) buildSourceInventoryManifest() (*sourceview.Manifest, error) {
	_, effective, err := m.buildSourceInventorySnapshots()
	return effective, err
}

func moduleOwnsPath(root, path string) (bool, error) { return sourceview.OwnsPath(root, path) }

func pathWithin(root, path string) bool { return sourceview.PathWithin(root, path) }

func projectSourcePackages(loaded []*packages.Package, moduleRoot, physicalRoot, modulePath string, sentinelFiles map[string]bool) map[string]projectSourcePackage {
	byDir := map[string]projectSourcePackage{}
	packages.Visit(loaded, nil, func(pkg *packages.Package) {
		if pkg == nil || pkg.Dir == "" {
			return
		}
		if pkg.Module == nil || !pkg.Module.Main || pkg.Module.Path != modulePath {
			return
		}
		moduleDir, ok := logicalProjectPath(pkg.Module.Dir, moduleRoot, physicalRoot)
		if !ok || moduleDir != filepath.Clean(moduleRoot) {
			return
		}
		dir, ok := logicalProjectPath(pkg.Dir, moduleRoot, physicalRoot)
		if !ok {
			return
		}
		expectedPath, ok := importPathForDir(moduleRoot, modulePath, dir)
		if !ok || expectedPath != pkg.PkgPath {
			return
		}
		byDir[dir] = retainedSourcePackage(pkg, sentinelFiles, moduleRoot, physicalRoot)
	})
	return byDir
}

func retainedSourcePackage(pkg *packages.Package, excludedFiles map[string]bool, moduleRoot, physicalRoot string) projectSourcePackage {
	files := make([]string, 0, len(pkg.CompiledGoFiles))
	for _, path := range pkg.CompiledGoFiles {
		logical, ok := logicalProjectPath(path, moduleRoot, physicalRoot)
		if ok {
			path = logical
		} else {
			path = filepath.Clean(path)
		}
		if !excludedFiles[path] {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	metadataErrors := make([]packages.Error, 0, len(pkg.Errors))
	for _, loadErr := range pkg.Errors {
		if loadErr.Kind != packages.TypeError {
			metadataErrors = append(metadataErrors, loadErr)
		}
	}
	syntaxByFile := make(map[string]*goast.File, len(pkg.Syntax))
	for _, file := range pkg.Syntax {
		if file == nil || pkg.Fset == nil || pkg.Fset.File(file.Pos()) == nil {
			continue
		}
		path := filepath.Clean(pkg.Fset.File(file.Pos()).Name())
		if logical, ok := logicalProjectPath(path, moduleRoot, physicalRoot); ok {
			path = logical
		}
		if !excludedFiles[path] {
			syntaxByFile[path] = file
		}
	}
	var invariantErrors []string
	if len(metadataErrors) == 0 {
		if pkg.TypesSizes == nil {
			invariantErrors = append(invariantErrors, "loaded target type sizes are missing")
		}
		if _, ok := packageLanguageVersion(pkg); !ok {
			invariantErrors = append(invariantErrors, "loaded module language-version provenance is missing")
		}
		for _, path := range files {
			if syntaxByFile[path] == nil {
				invariantErrors = append(invariantErrors, "loaded syntax is missing for "+path)
			}
		}
	}
	goVersion, _ := packageLanguageVersion(pkg)
	return projectSourcePackage{
		pkgPath:         pkg.PkgPath,
		name:            pkg.Name,
		compiledGoFiles: files,
		syntaxByFile:    syntaxByFile,
		metadataErrors:  metadataErrors,
		invariantErrors: invariantErrors,
		sizes:           pkg.TypesSizes,
		goVersion:       goVersion,
	}
}

func logicalProjectPath(path, moduleRoot, physicalRoot string) (string, bool) {
	path = filepath.Clean(path)
	moduleRoot = filepath.Clean(moduleRoot)
	physicalRoot = filepath.Clean(physicalRoot)
	if sourceview.PathWithin(moduleRoot, path) {
		return path, true
	}
	if !sourceview.PathWithin(physicalRoot, path) {
		return "", false
	}
	rel, err := filepath.Rel(physicalRoot, path)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Clean(filepath.Join(moduleRoot, rel)), true
}

// externalBackedgePackages finds non-main packages whose dependency graph
// transitively imports a main-module package. Those paths cross gsx's explicit
// one-way external boundary and are rejected; they are never published through
// the cold importer or reconstructed from source in a phase-local universe.
func externalBackedgePackages(loaded []*packages.Package, localPaths map[string]string) map[string][]string {
	byPath := map[string]*packages.Package{}
	packages.Visit(loaded, nil, func(pkg *packages.Package) {
		if pkg != nil && pkg.PkgPath != "" {
			byPath[pkg.PkgPath] = pkg
		}
	})
	memo := map[string]map[string]bool{}
	visiting := map[string]bool{}
	var localDependencies func(string) map[string]bool
	localDependencies = func(path string) map[string]bool {
		if deps, ok := memo[path]; ok {
			return deps
		}
		if _, local := localPaths[path]; local {
			deps := map[string]bool{path: true}
			memo[path] = deps
			return deps
		}
		if visiting[path] {
			return map[string]bool{}
		}
		visiting[path] = true
		deps := map[string]bool{}
		if pkg := byPath[path]; pkg != nil {
			for importedPath := range pkg.Imports {
				for localPath := range localDependencies(importedPath) {
					deps[localPath] = true
				}
			}
		}
		delete(visiting, path)
		memo[path] = deps
		return deps
	}

	backedges := map[string][]string{}
	for path := range byPath {
		if _, local := localPaths[path]; local {
			continue
		}
		deps := localDependencies(path)
		if len(deps) == 0 {
			continue
		}
		paths := make([]string, 0, len(deps))
		for localPath := range deps {
			paths = append(paths, localPath)
		}
		sort.Strings(paths)
		backedges[path] = paths
	}
	return backedges
}

func (m *Module) typeCheckEnvironmentForDir(dir string) (typeCheckEnvironment, error) {
	if m.opts.Bundle != nil {
		if m.opts.Bundle.sizes == nil {
			return typeCheckEnvironment{}, fmt.Errorf("codegen: Bundle has no target type sizes")
		}
		if m.opts.Bundle.goVersion == "" {
			return typeCheckEnvironment{}, fmt.Errorf("codegen: Bundle has no Go language version")
		}
		return typeCheckEnvironment{sizes: m.opts.Bundle.sizes, goVersion: m.opts.Bundle.goVersion}, nil
	}
	packageInfo, found, ready := m.targetSourcePackage(dir)
	if !ready {
		if _, err := m.externalImporter(); err != nil {
			return typeCheckEnvironment{}, err
		}
		packageInfo, found, ready = m.targetSourcePackage(dir)
	}
	if !ready {
		return typeCheckEnvironment{}, fmt.Errorf("codegen: target source inventory did not become ready for %s", dir)
	}
	if !found {
		return typeCheckEnvironment{}, fmt.Errorf("codegen: target source inventory has no package for %s", dir)
	}
	if packageInfo.sizes == nil {
		return typeCheckEnvironment{}, fmt.Errorf("codegen: target type sizes are missing for %s", dir)
	}
	if packageInfo.goVersion == "" {
		return typeCheckEnvironment{}, fmt.Errorf("codegen: Go language version is missing for %s", dir)
	}
	return typeCheckEnvironment{sizes: packageInfo.sizes, goVersion: packageInfo.goVersion}, nil
}

func (m *Module) targetSourcePackage(dir string) (projectSourcePackage, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pkg, ok := m.sourcePackages[filepath.Clean(dir)]
	return pkg, ok, m.sourceInventoryReady
}

// sourcePackageDir resolves module-local ownership from the authoritative cold
// source index. Bundle mode has no inventory, so it retains the explicitly
// bounded single-GSX-package filesystem path.
func (m *Module) sourcePackageDir(importPath string) (string, bool) {
	m.mu.Lock()
	dir, found := m.sourcePackageDirs[importPath]
	ready := m.sourceInventoryReady
	m.mu.Unlock()
	if ready {
		return dir, found
	}
	dir, found = dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, importPath)
	return dir, found && m.isGsxPackage(dir)
}
