package codegen

import (
	"crypto/sha256"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// The shared external world holds the dependency closure that every Module in a
// process resolves identically — the gsx runtime, gsx/std, and their transitive
// stdlib. Loading it costs ~230ms and yields 85 packages / 667 files / ~205k
// lines; before this it was re-derived once per Module (803 times across the
// test suite) and, because maybeRebuildFset fires on sourceInventoryDirty,
// once per edit in `gsx dev` and the LSP.
//
// The world owns its OWN FileSet, which is never rebuilt: external dependencies
// do not change while it stays fresh, so there is nothing to reclaim. That
// FileSet reserves a Pos range far above anything a per-Module FileSet reaches
// (sharedWorldBase), which makes the two ranges disjoint. A Pos therefore
// identifies its owner numerically — see Module.position — and asking the wrong
// FileSet yields an INVALID position rather than a plausible wrong one, so a
// mis-routed lookup fails loudly in tests instead of corrupting go-to-definition.
const sharedWorldBase = 1 << 40

// fileStamp is a file's identity for freshness checking: the same
// size+modtime pair the launcher digest cache uses, for the same reason (a
// rewrite always moves at least one of them).
type fileStamp struct {
	path  string
	size  int64
	mtime int64
}

type sharedWorld struct {
	fset  *token.FileSet
	types map[string]*types.Package
	// errs carries each closure package's load/type errors. The synthetic
	// entries hand them to externalImporter's harvest so a BROKEN runtime (the
	// exact thing `gsx dev` edits) fails as loudly on the fast path as on the
	// full load — dropping them made a runtime type error surface as silently
	// partial types downstream.
	errs map[string][]packages.Error
	// moduleOf maps each closure package to the module that owns it (absent for
	// the stdlib, which belongs to no module). It is what the back-edge guard
	// reads: a closure package owned by the LOADING module's own module path,
	// other than the composed config packages themselves, means the world
	// dragged main-module code in — see mainModuleBackedge.
	//
	// The owning module PATH is recorded rather than packages.Module.Main
	// because Main is relative to the root the world was built from, and one
	// world can serve several roots (the key is the path set + origin, not the
	// root). The module path is the same fact projectSourcePackages tests
	// against, so the two agree on what "main module" means for a given Module.
	moduleOf map[string]string
	// stamps covers modification of a file that was loaded; dirStamps covers
	// files being ADDED to or REMOVED from a loaded package, which no per-file
	// check can see (a directory's mtime moves when its entries change).
	// Together they catch the three ways the runtime can change under us.
	stamps    []fileStamp
	dirStamps []fileStamp
}

// mainModuleBackedge reports whether the closure holds a package owned by
// modulePath other than the composed config packages themselves — a package the
// world had no business loading.
//
// An empty modulePath means the caller has no main module to speak of: nothing
// is local to it, projectSourcePackages retains nothing, and the full load's
// own externalBackedgePackages finds no boundary either — so there is no
// boundary here to enforce.
func (w *sharedWorld) mainModuleBackedge(modulePath string, composed map[string]bool) bool {
	if modulePath == "" {
		return false
	}
	for pkgPath, owner := range w.moduleOf {
		if owner == modulePath && !composed[pkgPath] {
			return true
		}
	}
	return false
}

// fresh reports whether the sources the world was built from are unchanged.
// Stdlib files are excluded by the caller (the toolchain identity already covers
// them), so this stats only the gsx module's own files — the ones `gsx dev`
// edits mid-session.
func (w *sharedWorld) fresh() bool {
	for _, s := range w.stamps {
		info, err := os.Stat(s.path)
		if err != nil || info.Size() != s.size || info.ModTime().UnixNano() != s.mtime {
			return false
		}
	}
	for _, s := range w.dirStamps {
		info, err := os.Stat(s.path)
		if err != nil || info.ModTime().UnixNano() != s.mtime {
			return false
		}
	}
	return true
}

var sharedWorldIneligible, sharedWorldFellBack, sharedWorldFast atomic.Int64

// sharedWorldBackedge counts Modules returned to the full load because the
// composed world's closure re-entered their main module (see
// sharedWorld.mainModuleBackedge). It is the visible record the design asks
// for: a back-edging configuration must never be served silently, because the
// full load is the path that turns it into the hard configuration error.
var sharedWorldBackedge atomic.Int64

// projectLoads counts every packages.Load call issued by this process from
// anywhere in internal/codegen. It is incremented in exactly one place —
// loadPackages, the package's sole caller of packages.Load — so a new load
// site cannot forget it (see loadpackages.go and
// TestProjectLoadsHasOneLoadSite). One counter across every call site because
// the invariant it pins is process-wide: "how many go-list loads did this
// process issue," not which call site issued them.
// Tests use it to pin the go-list call budget of warm edit cycles — a `.go`
// edit must trigger a small, dir-count-independent number of loads, not one
// per directory. See TestWatchSession_EditLoadBudget.
var projectLoads atomic.Uint64

// ProjectLoadCalls returns the process-wide count of packages.Load
// invocations issued by internal/codegen.
func ProjectLoadCalls() uint64 { return projectLoads.Load() }

// sharedWorlds is deliberately unbounded and never evicted: keys are one per
// distinct (origin, env, toolchain) closure, which a CLI or test process holds
// one or two of, and a long-lived LSP holds one per open project. Duplicate
// concurrent cold loads for one key are possible and accepted (last write
// wins; both worlds are correct) — a singleflight would add coordination for a
// cold-start-only cost. Revisit only with evidence of real accumulation.
var (
	sharedWorldMu sync.Mutex
	sharedWorlds  = map[string]*sharedWorld{}
)

// sharedWorldOrigin describes WHERE this module root resolves the gsx runtime
// from. Without it two modules that replace the runtime to different directories
// would share one cache entry, and the second would be served the first's types
// — the entry would even pass sharedWorld.fresh, because the stamps it checks
// belong to the other directory and are genuinely unchanged.
//
// Returning moduleRoot is the safe fallback: it makes the key unique, which
// costs sharing but can never alias two different runtimes.
func sharedWorldOrigin(moduleRoot string, buildEnv []string) string {
	if work := environmentValue(buildEnv, "GOWORK"); work != "" && work != "off" {
		// A workspace can redirect module resolution in ways go.mod does not
		// record. Do not share across it.
		return moduleRoot
	}
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return moduleRoot
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return moduleRoot
	}
	relevant := func(path string) bool {
		return path == gsxRuntimeImportPath || strings.HasPrefix(path, gsxRuntimeImportPath+"/")
	}
	var b strings.Builder
	for _, r := range f.Require {
		if r.Mod.Path != "" && relevant(r.Mod.Path) {
			fmt.Fprintf(&b, "require\x00%s\x00%s\n", r.Mod.Path, r.Mod.Version)
		}
	}
	for _, r := range f.Replace {
		if r.Old.Path == "" || !relevant(r.Old.Path) {
			continue
		}
		target := r.New.Path
		if r.New.Version == "" && !filepath.IsAbs(target) {
			// A filesystem replace is relative to the module root.
			target = filepath.Clean(filepath.Join(moduleRoot, target))
		}
		fmt.Fprintf(&b, "replace\x00%s\x00%s\x00%s\n", r.Old.Path, target, r.New.Version)
	}
	return b.String()
}

// sharedWorldRootBound reports whether the composed path set names a package
// that could belong to THIS module root, in which case the world it builds must
// not be shared with any other root.
//
// The fixed base pair is module-independent by construction (the runtime and
// std resolve through go.mod, which sharedWorldOrigin already keys). A config
// package, however, may live in the main module itself (gsxui's merge/), and
// nothing else in the key distinguishes two roots that declare the same module
// path and the same config — two checkouts of one project, e.g. worktrees, open
// in one LSP. Without this, the second root would be served the first root's
// working-copy types and would even pass sharedWorld.fresh, because the stamps
// belong to the other directory and are genuinely unchanged. It is the same
// aliasing sharedWorldOrigin exists to prevent, one level down.
//
// The test is by import-path prefix, which is sound in the SAFE direction: a
// main-module package's path always starts with the module path, so no
// main-module config package escapes, and a nested module that shares the
// prefix merely loses sharing. An empty module path forfeits the test
// altogether, so every non-base config path binds to the root.
func sharedWorldRootBound(paths []string, modulePath string) bool {
	for _, p := range paths {
		if p == gsxRuntimeImportPath || p == stdImportPath {
			continue
		}
		if modulePath == "" || p == modulePath || strings.HasPrefix(p, modulePath+"/") {
			return true
		}
	}
	return false
}

// sharedWorldKey identifies a closure by everything that can change its types:
// the resolved load paths, where those modules resolve from, the frozen build
// environment, and the toolchain identity. Source edits are caught separately by
// sharedWorld.fresh.
func sharedWorldKey(loadPaths, buildEnv []string, toolchain, origin string) string {
	h := sha256.New()
	fmt.Fprintf(h, "origin\x00%s\x00", origin)
	write := func(tag string, vals []string) {
		sorted := append([]string(nil), vals...)
		sort.Strings(sorted)
		fmt.Fprintf(h, "%s\x00%d\x00", tag, len(sorted))
		for _, v := range sorted {
			fmt.Fprintf(h, "%s\x00", v)
		}
	}
	write("paths", loadPaths)
	write("env", buildEnv)
	fmt.Fprintf(h, "toolchain\x00%s\x00", toolchain)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// sharedWorldComposition returns the load-path set for this Module's shared
// world: the fixed gsx-runtime closure PLUS the module's resolved config
// packages — the same packages the full-mode loadPaths derivation in
// externalImporter puts through packages.Load (module.go:704-727), read via
// the same already-resolved accessors (ClassMergerRef.PkgPath,
// FilterAlias.PkgPath, RendererAlias.PkgPath via finalRendererAliases) rather
// than re-parsing "pkg.Func" strings. Config packages are harvested from the
// LOADED types, so composing them into the world (instead of excluding them)
// is what lets a configured module take the fast path at all: a project half
// loaded without NeedTypes could never serve them.
//
// ok is false only when the module's config cannot be composed into ONE
// world: a PerDir entry carrying its own class merger or a non-std filter
// package wants a world that differs by directory, which this phase does not
// support — that module keeps the original single full-mode load. A PerDir
// entry that only repeats the std filter (the "inherit" shape) is not
// variance and does not disqualify.
func (m *Module) sharedWorldComposition() (paths []string, ok bool) {
	o := m.opts
	for _, d := range o.PerDir {
		if d.ClassMerger != nil {
			return nil, false
		}
		for _, f := range d.FilterPkgs {
			if f != stdImportPath {
				return nil, false
			}
		}
	}

	set := map[string]bool{gsxRuntimeImportPath: true, stdImportPath: true}
	for _, f := range o.FilterPkgs {
		set[f] = true
	}
	for _, p := range o.LoadPkgs {
		set[p] = true
	}
	for _, a := range o.Aliases {
		set[a.PkgPath] = true
	}
	for _, r := range finalRendererAliases(o.Renderers) {
		set[r.PkgPath] = true
	}
	if o.ClassMerger != nil {
		set[o.ClassMerger.PkgPath] = true
	}

	paths = make([]string, 0, len(set))
	for p := range set {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, true
}

// projectLoadMode drops NeedTypes/NeedDeps: the caller discards every
// module-local *types.Package the load produces (semantic importers re-check
// local directories from retained source), so type-checking the dependency
// closure for the project's sake is work that is thrown away. What remains —
// names, files, module identity, imports — is what projectSourcePackages and
// back-edge detection actually read.
const projectLoadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedSyntax | packages.NeedTypesSizes | packages.NeedModule | packages.NeedImports

// loadExternalGraph resolves the dependency graph for externalImporter. On the
// eligible path it serves the gsx closure from the process-wide shared world and
// loads only the project's own packages, then hands back the project packages
// plus one synthetic entry per shared package so the caller's allTypes/errs
// harvest is unchanged. Synthetic entries carry no Imports and no Module, so
// back-edge detection (externalBackedgePackages, called by the caller on this
// function's return value) sees none and projectSourcePackages skips them.
//
// Dropping Imports from the synthetic entries is only safe because a world that
// re-enters the main module never reaches them: mainModuleBackedge below
// rejects it wholesale and the Module takes the full load, where the boundary
// is detected on real Imports. That check is what makes the composed world
// (which may name config packages, including main-module ones) as
// back-edge-free as the fixed {runtime, std} closure was by construction.
//
// A main-module CONFIG package in the world is not a back-edge — it is the
// hard case the design names. Its types still come from the retained project
// source (configuredSourcePackages routes module-local paths to the source
// resolver, and the caller drops every local path from the published importer),
// so the world's copy is a load-set member, not a second universe. Its dir stays
// a retained source package because the project half still loads "./...", which
// covers every main-module dir whether or not it is a config package.
func (m *Module) loadExternalGraph(cfg *packages.Config, loadPaths []string) ([]*packages.Package, error) {
	sharedPaths, ok := m.sharedWorldComposition()
	if !ok {
		sharedWorldIneligible.Add(1)
		return loadPackages(cfg, loadPaths...)
	}
	shared := make(map[string]bool, len(sharedPaths))
	for _, p := range sharedPaths {
		shared[p] = true
	}
	var projectPaths []string
	for _, p := range loadPaths {
		if !shared[p] {
			projectPaths = append(projectPaths, p)
		}
	}

	toolchain := ""
	if m.goContext != nil && m.goContext.goLauncher != nil {
		toolchain = m.goContext.goLauncher.CompilerIdentity()
	}
	origin := sharedWorldOrigin(m.opts.ModuleRoot, cfg.Env)
	if sharedWorldRootBound(sharedPaths, m.opts.ModulePath) {
		origin += "\x00root\x00" + filepath.Clean(m.opts.ModuleRoot)
	}
	key := sharedWorldKey(sharedPaths, cfg.Env, toolchain, origin)
	world, err := loadSharedWorld(key, cfg, sharedPaths)
	if err != nil {
		return nil, err
	}

	// The world may only carry code this Module treats as EXTERNAL. A config
	// package that imports back into the main module drags main-module packages
	// into the closure, which is the shape externalBackedgePackages rejects on
	// the full load — and the synthetic entries below drop the Imports that
	// would reveal it, so nothing downstream could see it here. Serving such a
	// Module would silently skip the hard configuration error for an external
	// config package, and would publish a second copy of main-module packages
	// that every other phase rebuilds from source. Returning it to the full load
	// restores both behaviors exactly, because the full load is where they live.
	//
	// The verdict is a pure function of the world's contents and this Module's
	// identity, so it is stable for as long as the entry is cached — "permanent
	// for that key", as the design requires — while staying correct for a world
	// shared by roots whose main modules differ.
	if world.mainModuleBackedge(m.opts.ModulePath, shared) {
		sharedWorldBackedge.Add(1)
		return loadPackages(cfg, loadPaths...)
	}

	projectCfg := *cfg
	projectCfg.Mode = projectLoadMode
	pkgs, err := loadPackages(&projectCfg, projectPaths...)
	if err != nil {
		return nil, err
	}

	// The shared world carries the gsx closure and nothing else, but a project
	// package may import something outside it — another module in the workspace,
	// a third-party dependency, a nested module. Those types came from the full
	// load's NeedDeps, which the project half does not request, so serving this
	// Module from the shared world would silently lose them.
	//
	// Checking each project package's IMMEDIATE imports is sufficient: the world
	// is a closure, so anything it contains has its own dependencies inside it.
	// If any import is unaccounted for, fall back to the original single load —
	// correctness first, and the fallback costs only the modules that need it.
	// mainModule records packages that belong to THIS module. Presence in the
	// project load is not enough — a stdlib or nested-module root loaded in
	// reduced mode is present but carries no types. Module.Main is the same test
	// projectSourcePackages uses, and it needs no import-path prefix guessing
	// (a nested module can share the main module's prefix).
	mainModule := map[string]bool{}
	for _, p := range pkgs {
		if p == nil || p.PkgPath == "" {
			continue
		}
		if p.Module != nil && p.Module.Main && p.Module.Path == m.opts.ModulePath {
			mainModule[p.PkgPath] = true
		}
	}
	// Every extra load path (a manifest LoadRoot) must have resolved as a package
	// of THIS module. One that did not is outside the module — a nested module,
	// which can share the main module's import prefix, so no prefix test would be
	// sound — and its types would have come from the full load's NeedDeps.
	// A LoadRoot already resident in the closure IS served (via the synthetic
	// entries below), so it must not force a fallback: .gsx files importing
	// stdlib packages (strings, fmt, time) are the common case, and treating
	// them as unservable silently returned most real projects to the full
	// per-Module load.
	for _, p := range projectPaths {
		if p == "./..." || mainModule[p] || world.types[p] != nil {
			continue
		}
		sharedWorldFellBack.Add(1)
		return loadPackages(cfg, loadPaths...)
	}
	for _, p := range pkgs {
		if p == nil {
			continue
		}
		for importPath := range p.Imports {
			if world.types[importPath] == nil && !mainModule[importPath] {
				sharedWorldFellBack.Add(1)
				return loadPackages(cfg, loadPaths...)
			}
		}
	}

	sharedWorldFast.Add(1)
	m.mu.Lock()
	m.sharedFset = world.fset
	m.mu.Unlock()
	for path, t := range world.types {
		pkgs = append(pkgs, &packages.Package{PkgPath: path, Types: t, Errors: world.errs[path]})
	}
	return pkgs, nil
}

// loadSharedWorld returns the cached closure for key, loading it if absent or
// stale. Freshness stamps cover module-owned files only; stdlib is covered by
// the toolchain identity in the key.
func loadSharedWorld(key string, cfg *packages.Config, loadPaths []string) (*sharedWorld, error) {
	sharedWorldMu.Lock()
	cached, ok := sharedWorlds[key]
	sharedWorldMu.Unlock()
	if ok && cached.fresh() {
		return cached, nil
	}

	fset := token.NewFileSet()
	// Reserve the high Pos range BEFORE anything is added, so every position in
	// this world sorts above every per-Module position.
	fset.AddFile("gsx:shared-world-base", sharedWorldBase, 0)

	loadCfg := *cfg
	loadCfg.Fset = fset
	pkgs, err := loadPackages(&loadCfg, loadPaths...)
	externalClosureLoads.Add(1)
	if err != nil {
		return nil, err
	}

	world := &sharedWorld{
		fset:     fset,
		types:    map[string]*types.Package{},
		errs:     map[string][]packages.Error{},
		moduleOf: map[string]string{},
	}
	seen := map[string]bool{}
	seenDir := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			world.types[p.PkgPath] = p.Types
		}
		if len(p.Errors) > 0 {
			world.errs[p.PkgPath] = p.Errors
		}
		if p.Module != nil && p.Module.Path != "" {
			world.moduleOf[p.PkgPath] = p.Module.Path
		}
		// Only module-owned files are stamped (p.Module == nil means stdlib,
		// which the toolchain identity in the key already covers). The previous
		// GOROOT-prefix exclusion keyed off an env var that is unset in normal
		// environments, so every stdlib file was being stat'd per freshness
		// check for nothing.
		if p.Module == nil {
			return
		}
		for _, f := range p.CompiledGoFiles {
			if seen[f] {
				continue
			}
			seen[f] = true
			info, statErr := os.Stat(f)
			if statErr != nil {
				continue
			}
			world.stamps = append(world.stamps, fileStamp{path: f, size: info.Size(), mtime: info.ModTime().UnixNano()})
			dir := filepath.Dir(f)
			if !seenDir[dir] {
				seenDir[dir] = true
				if dirInfo, dirErr := os.Stat(dir); dirErr == nil {
					world.dirStamps = append(world.dirStamps, fileStamp{path: dir, mtime: dirInfo.ModTime().UnixNano()})
				}
			}
		}
	})

	sharedWorldMu.Lock()
	sharedWorlds[key] = world
	sharedWorldMu.Unlock()
	return world, nil
}

// positionResolver builds a resolver over an IMMUTABLE pair of FileSets. The
// two reserve disjoint Pos ranges (see sharedWorldBase), so the owner is
// decided numerically — no package lookup, and no way to silently read one
// FileSet's Pos as the other's.
//
// The resolver must always be a closure over fsets captured at one instant,
// never a live read of Module fields: a retained resolver (the LSP keeps
// PackageResult snapshots across analyses) racing rebuildFset would otherwise
// read a REPLACED fset and return a plausible-looking position in the wrong
// file — the adversarial review demonstrated exactly that, in both Pos ranges.
func positionResolver(skel, shared *token.FileSet) func(token.Pos) token.Position {
	return func(pos token.Pos) token.Position {
		if pos >= token.Pos(sharedWorldBase) {
			if shared != nil {
				return shared.Position(pos)
			}
			return token.Position{}
		}
		if skel != nil {
			return skel.Position(pos)
		}
		return token.Position{}
	}
}

// positionResolverPhysical is positionResolver without //line adjustment —
// PositionFor(pos, false) — for consumers that need the physical file (the
// LSP's generated-output fallback).
func positionResolverPhysical(skel, shared *token.FileSet) func(token.Pos) token.Position {
	return func(pos token.Pos) token.Position {
		if pos >= token.Pos(sharedWorldBase) {
			if shared != nil {
				return shared.PositionFor(pos, false)
			}
			return token.Position{}
		}
		if skel != nil {
			return skel.PositionFor(pos, false)
		}
		return token.Position{}
	}
}

// snapshotSharedFset reads the shared world FileSet under m.mu for capture
// into a result snapshot.
func (m *Module) snapshotSharedFset() *token.FileSet {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sharedFset
}

// positionFor is the during-analysis resolver (filter harvest and friends run
// under analysisMu, where m.fset is stable). Both fields are read under m.mu;
// snapshots published to callers that outlive the analysis must use
// positionResolver instead.
func (m *Module) positionFor(pos token.Pos) token.Position {
	m.mu.Lock()
	skel, shared := m.fset, m.sharedFset
	m.mu.Unlock()
	return positionResolver(skel, shared)(pos)
}
