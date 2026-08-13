package codegen

import (
	"crypto/sha256"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
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
	// imports is the closure's adjacency, kept because the synthetic entries
	// handed to externalImporter carry none: it is the only place the world's
	// shape survives the split load, and the back-edge guard needs REACHABILITY,
	// not just ownership (see mainModuleBackedge).
	imports map[string][]string
	// stamps covers modification of a file that was loaded; dirStamps covers
	// files being ADDED to or REMOVED from a loaded package, which no per-file
	// check can see (a directory's mtime moves when its entries change).
	// Together they catch the three ways the runtime can change under us.
	stamps    []fileStamp
	dirStamps []fileStamp
}

// mainModuleBackedge reports whether this world may serve a Module whose main
// module is modulePath, given the composed config paths. Two shapes disqualify
// it, and both have to be tested:
//
//   - a closure package owned by modulePath that is NOT one of the composed
//     config packages: the world dragged main-module code in beyond the
//     configuration, and every other phase rebuilds that code from source;
//   - an EXTERNAL closure package that transitively reaches one owned by
//     modulePath — the one-way boundary externalBackedgePackages enforces on
//     the full load. Ownership alone cannot see this: when the back-edge target
//     is the composed merger itself (an external filter package calling
//     gsxui-style `merge.Merge`), every main-module package in the closure is a
//     legitimately composed one, and only reachability reveals that an external
//     package depends on it. The full load rejects that configuration outright,
//     so serving it here would emit where the full load emits nothing.
//
// An empty modulePath means the caller has no main module to speak of: nothing
// is local to it, projectSourcePackages retains nothing, and the full load's
// own externalBackedgePackages finds no boundary either — so there is no
// boundary here to enforce.
func (w *sharedWorld) mainModuleBackedge(modulePath string, composed map[string]bool) bool {
	if modulePath == "" {
		return false
	}
	local := map[string]bool{}
	for pkgPath, owner := range w.moduleOf {
		if owner != modulePath {
			continue
		}
		if !composed[pkgPath] {
			return true
		}
		local[pkgPath] = true
	}
	if len(local) == 0 {
		return false
	}
	// Memoized reachability over the closure's adjacency, mirroring
	// externalBackedgePackages: Go forbids import cycles, so the visiting set is
	// belt-and-braces against a malformed graph rather than a real shape.
	reaches := make(map[string]bool, len(w.imports))
	visiting := map[string]bool{}
	var walk func(string) bool
	walk = func(path string) bool {
		if local[path] {
			return true
		}
		if hit, done := reaches[path]; done {
			return hit
		}
		if visiting[path] {
			return false
		}
		visiting[path] = true
		hit := slices.ContainsFunc(w.imports[path], walk)
		delete(visiting, path)
		reaches[path] = hit
		return hit
	}
	for path := range w.imports {
		if !local[path] && walk(path) {
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
// Exported as SharedWorldBackedgeFallbacks — every consumer, in-package or
// not, reads it through that accessor (see
// TestConfiguredExternalBackedgeIsHardConfigurationError,
// TestSharedWorldBackedgeFallsBack,
// TestSharedWorldBackedgeThroughComposedConfigPackageFallsBack).
var sharedWorldBackedge atomic.Int64

// sharedWorldHits counts every loadSharedWorld call that was served from the
// process-wide cache — a key already present whose sharedWorld.fresh() still
// held, so no packages.Load ran. It is the payoff counter for the process
// cache Task 4 exists to prove: a second Module (or watch session) opened
// over the same root and configuration must record a hit here, not a
// SharedWorldLoads increment. See SharedWorldHits and
// gen.TestWatchSession_ConfiguredModuleWorldBudget.
var sharedWorldHits atomic.Int64

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

// SharedWorldLoads returns the process-wide count of times loadSharedWorld
// actually issued a packages.Load for the shared external world's closure —
// a cold miss, or a stale-freshness reload after a module-owned file the
// world stamped (the gsx runtime, or a composed config package such as a
// main-module class merger) changed on disk. It does NOT count a Module's
// ordinary project-half reload (see ProjectLoadCalls), which fires on every
// authored .go edit regardless of whether the edit touched the world.
//
// Tests use the distinction to pin the freshness design's claim: a .go edit
// INSIDE the world's composed closure must move this counter, and a .go edit
// OUTSIDE it (an unrelated project dependency, or a pure .gsx edit) must not
// — see gen/watch_sharedworld_test.go and
// gen.TestWatchSession_ConfiguredModuleWorldBudget.
func SharedWorldLoads() int64 { return externalClosureLoads.Load() }

// SharedWorldHits returns the process-wide count of loadSharedWorld calls
// served from the cache (a fresh entry already keyed for this closure), the
// complement of SharedWorldLoads: every loadSharedWorld call is either a load
// or a hit, never both. It is the process-cache payoff the shared-world
// design exists to prove — see gen.TestWatchSession_ConfiguredModuleWorldBudget,
// which opens a second Module over the same root and configuration and
// asserts this counter moves while SharedWorldLoads does not.
func SharedWorldHits() int64 { return sharedWorldHits.Load() }

// SharedWorldBackedgeFallbacks returns the process-wide count of Modules
// returned to the full per-Module load because the composed world's closure
// re-entered their main module outside the composed config set (see
// sharedWorld.mainModuleBackedge). Mirrors ProjectLoadCalls and
// SharedWorldLoads: a back-edging configuration must never be served
// silently, because the full load is the path that turns it into the hard
// configuration error. Consuming tests, all in internal/codegen:
// TestConfiguredExternalBackedgeIsHardConfigurationError
// (external_backedge_test.go), TestSharedWorldBackedgeFallsBack and
// TestSharedWorldBackedgeThroughComposedConfigPackageFallsBack
// (sharedworld_configured_test.go).
func SharedWorldBackedgeFallbacks() int64 { return sharedWorldBackedge.Load() }

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

// sharedWorldOrigin describes WHERE this module root resolves the world's
// packages from: the gsx runtime, plus the module of every composed config
// package. Without it two modules that resolve the same import path to
// different code would share one cache entry, and the second would be served
// the first's types — the entry would even pass sharedWorld.fresh, because the
// stamps it checks belong to the other directory and are genuinely unchanged.
// Version pins matter as much as replace targets: two projects on structpages
// v1.0.0 and v1.5.0 compose the same path and must not share a world.
//
// Returning moduleRoot is the safe fallback: it makes the key unique, which
// costs sharing but can never alias two different closures.
func sharedWorldOrigin(moduleRoot string, buildEnv, composedPaths []string) string {
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
	// A go.mod line names a MODULE; the composition names PACKAGES. A module is
	// relevant when it owns one of them — the same prefix test sharedWorldRootBound
	// uses, read in the other direction.
	relevant := func(modPath string) bool {
		if modPath == gsxRuntimeImportPath || strings.HasPrefix(modPath, gsxRuntimeImportPath+"/") {
			return true
		}
		for _, p := range composedPaths {
			if p == modPath || strings.HasPrefix(p, modPath+"/") {
				return true
			}
		}
		return false
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
	configPaths, ok := m.sharedWorldComposition()
	if !ok {
		sharedWorldIneligible.Add(1)
		return loadPackages(cfg, loadPaths...)
	}
	shared := make(map[string]bool, len(configPaths))
	for _, p := range configPaths {
		shared[p] = true
	}
	var projectPaths []string
	for _, p := range loadPaths {
		if !shared[p] {
			projectPaths = append(projectPaths, p)
		}
	}

	// The project half runs FIRST. It is the cheap load — no types, no deps —
	// and it is the only thing that can say which external packages this module
	// actually needs, which is what the world has to cover. Deciding that after
	// the world load (the original order) meant a module the world could not
	// serve had already paid for it: three packages.Load calls where the
	// pre-shared-world code paid one, measured as a 12–18% regression on every
	// gsxui dev cycle.
	projectCfg := *cfg
	projectCfg.Mode = projectLoadMode
	pkgs, err := loadPackages(&projectCfg, projectPaths...)
	if err != nil {
		return nil, err
	}

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

	world, worldPaths, err := m.coveringWorld(cfg, configPaths, pkgs, mainModule)
	if err != nil {
		return nil, err
	}
	composed := make(map[string]bool, len(worldPaths))
	for _, p := range worldPaths {
		composed[p] = true
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
	if world.mainModuleBackedge(m.opts.ModulePath, composed) {
		sharedWorldBackedge.Add(1)
		return loadPackages(cfg, loadPaths...)
	}

	// Safety net. coveringWorld composes exactly what the project half
	// references, so these two checks are expected to pass — they fail only
	// when a package the world was ASKED for did not come back with types (a
	// broken or unresolvable dependency), which no composition can fix. The
	// original single load is then the honest answer, and it is the one that
	// reports the underlying error.
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

// coveringWorld returns a world that carries every external package the project
// half references, and the path set it was composed from.
//
// It is two-tier on purpose. Tier one is the CONFIG world — the fixed base pair
// plus this module's config packages — which is identical for every module with
// the same configuration and is therefore the entry a whole process shares. Most
// modules need nothing beyond it: a `.gsx` importing "strings" or "fmt" names a
// package the gsx runtime's own closure already carries, and composing such
// paths as extra roots would mint a distinct world per distinct import set while
// loading byte-identical contents.
//
// Tier two exists for the packages tier one genuinely does not carry —
// gsxui's `.gsx` import of github.com/gsxhq/vite, a third-party dependency
// imported only from the module's Go files, a nested module named as a load
// root. Before this, those modules could not be served at all: the world was
// composed from configuration alone, so the coverage checks rejected them and
// every dev cycle paid the world load, the project-half load AND the full load.
// Composing the gap instead of rejecting it is what puts a real project on the
// fast path — the whole point of the phase.
//
// The extended key is a pure function of (config paths, tier-one contents,
// project references), so it is stable across cycles: editing a `.gsx` body or
// a `.go` body does not move it, and only ADDING or REMOVING an import of a
// package the config world lacks re-keys — rare, and self-healing when it
// happens.
func (m *Module) coveringWorld(cfg *packages.Config, configPaths []string, pkgs []*packages.Package, mainModule map[string]bool) (*sharedWorld, []string, error) {
	world, err := m.sharedWorldFor(cfg, configPaths)
	if err != nil {
		return nil, nil, err
	}
	gaps := worldGaps(pkgs, mainModule, world)
	if len(gaps) == 0 {
		return world, configPaths, nil
	}
	worldPaths := append(append(make([]string, 0, len(configPaths)+len(gaps)), configPaths...), gaps...)
	sort.Strings(worldPaths)
	world, err = m.sharedWorldFor(cfg, worldPaths)
	if err != nil {
		return nil, nil, err
	}
	return world, worldPaths, nil
}

// worldGaps returns the sorted external packages the project half references
// that world does not carry.
//
// A root that is not a main-module package (an out-of-module load root, a
// nested module) contributes ITSELF and not its imports: composing it as a
// world root brings its whole closure. A main-module root contributes its
// immediate non-local imports — the world is a closure, so covering those
// covers everything under them. This mirrors, one step ahead of it, the
// coverage check the caller still runs.
func worldGaps(pkgs []*packages.Package, mainModule map[string]bool, world *sharedWorld) []string {
	gaps := map[string]bool{}
	need := func(path string) {
		// "C" is the cgo pseudo-package: it names no loadable package, and
		// sourceview already keeps it out of the manifest's load roots.
		if path == "" || path == "C" || mainModule[path] || world.types[path] != nil {
			return
		}
		gaps[path] = true
	}
	for _, p := range pkgs {
		if p == nil || p.PkgPath == "" {
			continue
		}
		if !mainModule[p.PkgPath] {
			need(p.PkgPath)
			continue
		}
		for importPath := range p.Imports {
			need(importPath)
		}
	}
	if len(gaps) == 0 {
		return nil
	}
	out := make([]string, 0, len(gaps))
	for path := range gaps {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// sharedWorldFor resolves the process-wide world entry for one composed path
// set: the key derivation (origin, root binding, toolchain) plus the cache
// lookup. Both tiers of coveringWorld go through it, so a tier-two world is
// keyed by exactly the same rules as a tier-one world.
func (m *Module) sharedWorldFor(cfg *packages.Config, paths []string) (*sharedWorld, error) {
	toolchain := ""
	if m.goContext != nil && m.goContext.goLauncher != nil {
		toolchain = m.goContext.goLauncher.CompilerIdentity()
	}
	origin := sharedWorldOrigin(m.opts.ModuleRoot, cfg.Env, paths)
	if sharedWorldRootBound(paths, m.opts.ModulePath) {
		origin += "\x00root\x00" + filepath.Clean(m.opts.ModuleRoot)
	}
	return loadSharedWorld(sharedWorldKey(paths, cfg.Env, toolchain, origin), cfg, paths)
}

// loadSharedWorld returns the cached closure for key, loading it if absent or
// stale. Freshness stamps cover module-owned files only; stdlib is covered by
// the toolchain identity in the key.
func loadSharedWorld(key string, cfg *packages.Config, loadPaths []string) (*sharedWorld, error) {
	sharedWorldMu.Lock()
	cached, ok := sharedWorlds[key]
	sharedWorldMu.Unlock()
	if ok && cached.fresh() {
		sharedWorldHits.Add(1)
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
		imports:  map[string][]string{},
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
		if len(p.Imports) > 0 {
			edges := make([]string, 0, len(p.Imports))
			for importPath := range p.Imports {
				edges = append(edges, importPath)
			}
			world.imports[p.PkgPath] = edges
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
