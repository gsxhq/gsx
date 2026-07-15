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
// Like Invalidate, callers must not invoke this concurrently with an in-flight
// Package or Generate on the same Module.
func (m *Module) RefreshDiskSources(dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	if m.opts.Bundle != nil || m.opts.SourceOnly {
		return fmt.Errorf("codegen: RefreshDiskSources requires a normal source-backed Module")
	}
	root := filepath.Clean(m.opts.ModuleRoot)
	dirSet := map[string]bool{}
	for _, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("codegen: resolve disk-source refresh directory %s: %w", dir, err)
		}
		abs = filepath.Clean(abs)
		if !pathWithin(root, abs) {
			return fmt.Errorf("codegen: disk-source refresh directory %s is outside module root %s", abs, root)
		}
		owned, err := moduleOwnsPath(root, filepath.Join(abs, ".gsx-refresh-ownership"))
		if err != nil {
			return fmt.Errorf("codegen: inspect disk-source refresh ownership for %s: %w", abs, err)
		}
		if !owned {
			return fmt.Errorf("codegen: disk-source refresh directory %s is not owned by module root %s", abs, root)
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
			return fmt.Errorf("codegen: refresh saved source manifest: %w", err)
		}
		if len(savedFiles) != 0 {
			base, err = base.WithFileSnapshots(savedFiles)
			if err != nil {
				return fmt.Errorf("codegen: apply captured saved source before refresh: %w", err)
			}
		}
		orderedDirs := make([]string, 0, len(dirSet))
		for dir := range dirSet {
			orderedDirs = append(orderedDirs, dir)
		}
		sort.Strings(orderedDirs)
		refreshed, err := base.RefreshDirs(orderedDirs)
		if err != nil {
			return fmt.Errorf("codegen: refresh saved source manifest: %w", err)
		}
		effective, err := refreshed.WithOverrides(allOverrides)
		if err != nil {
			return fmt.Errorf("codegen: apply overrides to refreshed source manifest: %w", err)
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
		m.mu.Unlock()
		if viewErr != nil {
			return fmt.Errorf("codegen: refreshed saved source view: %w", viewErr)
		}
		return nil
	}
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
