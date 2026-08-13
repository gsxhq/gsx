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
	// reads — see mainModuleBackedge.
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

// mainModuleBackedge reports whether this world carries code that belongs to
// the module being served. Nothing composes a main-module package into a world
// — sharedWorldComposition drops them and worldGaps never adds one — so any
// that appears got there as a DEPENDENCY of an external package: the one-way
// boundary externalBackedgePackages rejects on the full load, and the shape a
// configured back-edging filter package takes (gsxui-style `merge.Merge`
// called from an out-of-module filter). One ownership test decides it, with no
// exemption to reason about, because there is no legitimate reason for a world
// to hold main-module code at all.
//
// An empty modulePath means the caller has no main module to speak of: nothing
// is local to it, projectSourcePackages retains nothing, and the full load's
// own externalBackedgePackages finds no boundary either — so there is no
// boundary here to enforce.
func (w *sharedWorld) mainModuleBackedge(modulePath string) bool {
	if modulePath == "" {
		return false
	}
	for _, owner := range w.moduleOf {
		if owner == modulePath {
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

// The three verdicts a Module can reach about the shared world: served by it
// (fast), refused before any load because one world cannot serve the module's
// per-dir config variance (ineligible), or returned to the full load after the
// loads because the world came back without types the project needs (fellBack).
// Together with sharedWorldBackedge they account for every externalImporter
// call, which is what makes "did this project ride the world?" answerable from
// outside — the question the gsxui A/B had to add temporary instrumentation to
// ask. See SharedWorldFastPaths, SharedWorldIneligibleModules,
// SharedWorldCoverageFallbacks.
var sharedWorldIneligible, sharedWorldFellBack, sharedWorldFast atomic.Int64

// SharedWorldFastPaths returns the process-wide count of externalImporter
// resolutions served by the shared world: the project half plus the world's
// synthetic entries, with no full-mode per-Module load.
func SharedWorldFastPaths() int64 { return sharedWorldFast.Load() }

// SharedWorldIneligibleModules returns the process-wide count of Modules whose
// configuration cannot be composed into one world (per-dir class mergers or
// per-dir non-std filter packages). They take the single full-mode load
// directly, without paying for a world first.
func SharedWorldIneligibleModules() int64 { return sharedWorldIneligible.Load() }

// SharedWorldCoverageFallbacks returns the process-wide count of Modules
// returned to the full load because the world did not carry types the project
// half needs. coveringWorld composes the module's external references, so this
// counts only packages that came back from the world load without types — a
// broken or unresolvable dependency — not ordinary out-of-config imports.
func SharedWorldCoverageFallbacks() int64 { return sharedWorldFellBack.Load() }

// sharedWorldBackedge counts Modules returned to the full load because the
// composed world's closure re-entered their main module (see
// sharedWorld.mainModuleBackedge). It is the visible record the design asks
// for: a back-edging configuration must never be served silently, because the
// full load is the path that turns it into the hard configuration error.
// Exported as SharedWorldBackedgeFallbacks — every consumer, in-package or
// not, reads it through that accessor (see
// TestConfiguredExternalBackedgeIsHardConfigurationError and
// TestSharedWorldExternalConfigBackedgeFallsBack).
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
// actually issued a packages.Load for a shared external world's closure — a
// cold miss (a new key: a new configuration, or a changed set of external
// packages the project references) or a stale-freshness reload after a
// module-owned file the world stamped changed on disk. Only EXTERNAL code is
// ever stamped: main-module code does not enter a world, so no edit inside the
// project — including an edit to a class merger the project owns — can move
// this counter. It does not count a Module's ordinary project-half reload
// (see ProjectLoadCalls), which fires on every authored .go edit.
//
// Tests use the distinction to pin the freshness design's claim: a .go edit
// anywhere in the project must leave this counter alone — see
// gen/watch_sharedworld_test.go (including the merger-edit pin) and
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
// returned to the full per-Module load because a world's closure re-entered
// their main module (see sharedWorld.mainModuleBackedge). Mirrors
// ProjectLoadCalls and SharedWorldLoads: a back-edging configuration must never
// be served silently, because the full load is the path that turns it into the
// hard configuration error. Consuming tests, all in internal/codegen:
// TestConfiguredExternalBackedgeIsHardConfigurationError
// (external_backedge_test.go) and
// TestSharedWorldExternalConfigBackedgeFallsBack
// (sharedworld_configured_test.go).
func SharedWorldBackedgeFallbacks() int64 { return sharedWorldBackedge.Load() }

// sharedWorlds holds two kinds of entry, with two different lifetimes.
//
// CONFIG-TIER entries are unbounded and never evicted, as they always were:
// their key is one per distinct (config paths, origin, env, toolchain)
// closure, which a CLI or test process holds one or two of and a long-lived
// LSP one per open project. They are the stable part — a project's
// configuration changes when gsx.toml does.
//
// EXTENSION-TIER entries are keyed by the set of external packages the project
// REFERENCES, which moves whenever an import is added or removed. Left
// unbounded, a long-lived LSP session would accumulate one full types graph and
// FileSet per import-set the developer ever passed through — exactly the
// retention the #134–#138 LSP memory work exists to avoid. So each (module
// root, config-tier key) owns at most ONE extension entry: minting a new one
// drops the previous one for that slot. Reverting an import change reloads
// rather than hitting, which is the right trade — a bounded cache that
// occasionally reloads beats an unbounded one that never forgets.
//
// Duplicate concurrent cold loads for one key are possible and accepted (last
// write wins; both worlds are correct) — a singleflight would add coordination
// for a cold-start-only cost.
var (
	sharedWorldMu sync.Mutex
	sharedWorlds  = map[string]*sharedWorld{}
	// extensionSlots maps "module root + config-tier key" to the extension key
	// currently occupying that slot, so minting the next one can drop it.
	extensionSlots = map[string]string{}
)

// sharedWorldCacheSize reports how many world entries the process holds. Tests
// use deltas across it to pin the extension-tier bound (see
// TestSharedWorldKeyFollowsImportsNotEdits); nothing in production reads it.
func sharedWorldCacheSize() int {
	sharedWorldMu.Lock()
	defer sharedWorldMu.Unlock()
	return len(sharedWorlds)
}

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
// std resolve through go.mod, which sharedWorldOrigin already keys). What is
// not is a package resolved RELATIVE to this root: a nested module under the
// main module's import prefix, named as a load root and therefore composed
// into the extension tier. Nothing else in the key distinguishes two roots that
// declare the same module path and hold different code at that path — two
// checkouts of one project, e.g. worktrees, open in one LSP. Without this, the
// second root would be served the first root's working-copy types and would
// even pass sharedWorld.fresh, because the stamps belong to the other directory
// and are genuinely unchanged. It is the same aliasing sharedWorldOrigin exists
// to prevent, one level down.
//
// This is belt-and-braces, and largely subsumed by sharedWorldOrigin: a nested
// module reached through a filesystem `replace` is already differentiated by
// the ABSOLUTE target path the origin records, and an empty module path is not
// reachable from any production caller (Open's callers read it from go.mod).
// Main-module config packages, the case this originally existed for, no longer
// compose into any world at all (see sharedWorldComposition). It is kept
// because the cost is one prefix scan of a short path list and the failure it
// prevents — one root served another root's types, with freshness agreeing — is
// silent and severe. The test errs toward binding: over-binding costs sharing,
// under-binding would alias.
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
// A config package that lives in the MAIN module (gsxui's merge/) is left
// OUT. Its types never came from the world in the first place: module-local
// paths resolve through configuredSourcePackages' source resolver, and
// externalImporter drops every local path from the published importer, so the
// world's copy was only ever a load-set member. What it did do was stamp the
// merger's files into every world tier, so editing the class merger — a
// routine edit — invalidated all of them and cost two world rebuilds
// (measured: +16% on gsxui's merger cycle). The merger's own external
// dependencies are not lost: the project half references them, so the
// extension tier composes them like any other gap. Main-module code therefore
// never enters a shared world, which is also what lets mainModuleBackedge be
// one flat ownership test.
//
// ok is false when the module's config cannot be composed into ONE world: a
// PerDir entry carrying its own class merger or a non-std filter package wants
// a world that differs by directory, which this phase does not support — that
// module keeps the original single full-mode load. A PerDir entry that only
// repeats the std filter (the "inherit" shape) is not variance and does not
// disqualify. ok is false too when the exclusion above empties the set, which
// happens only when the module being built IS the gsx runtime: there is no
// external world left to share, and the full load is the honest answer.
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
		// The prefix test errs toward exclusion: a nested module sharing the
		// main module's import prefix is dropped here too, and the extension
		// tier picks it up from the project half's references, where its
		// non-main-module identity is a loaded fact rather than a guess.
		if p == "" || p == m.opts.ModulePath || (m.opts.ModulePath != "" && strings.HasPrefix(p, m.opts.ModulePath+"/")) {
			continue
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil, false
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
// is detected on real Imports. That check is what keeps a composed world as
// back-edge-free as the fixed {runtime, std} closure was by construction.
//
// Main-module code never enters a world, config packages included (see
// sharedWorldComposition): its types come from the retained project source, and
// its dirs stay retained source packages because the project half loads
// "./...", which covers every main-module dir whether or not the configuration
// names it.
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

	// The project half runs FIRST because coveringWorld cannot compose the
	// extension tier without it: the references it discovers ARE the tier's
	// path set. That is the whole reason for the order — it does not make the
	// fallbacks below cheaper. They still discard this load and re-load
	// everything (three or four packages.Load calls where the pre-shared-world
	// code paid one), which is acceptable only because the shapes that reach
	// them are rare or already fatal; the shape that used to reach them on
	// every gsxui cycle is now composed instead of refused.
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

	world, err := m.coveringWorld(cfg, configPaths, pkgs, mainModule)
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
	if world.mainModuleBackedge(m.opts.ModulePath) {
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
		for key, imported := range p.Imports {
			importPath := resolvedImportPath(key, imported)
			if pseudoImportPath(importPath) {
				continue
			}
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
func (m *Module) coveringWorld(cfg *packages.Config, configPaths []string, pkgs []*packages.Package, mainModule map[string]bool) (*sharedWorld, error) {
	configKey := m.sharedWorldKeyFor(cfg, configPaths)
	world, err := loadSharedWorld(configKey, "", cfg, configPaths)
	if err != nil {
		return nil, err
	}
	gaps := worldGaps(pkgs, mainModule, world)
	if len(gaps) == 0 {
		return world, nil
	}
	worldPaths := append(append(make([]string, 0, len(configPaths)+len(gaps)), configPaths...), gaps...)
	sort.Strings(worldPaths)
	// The slot this extension occupies: one per (module root, config tier), so
	// the next import-set change replaces it instead of adding to the cache.
	slot := filepath.Clean(m.opts.ModuleRoot) + "\x00" + configKey
	return loadSharedWorld(m.sharedWorldKeyFor(cfg, worldPaths), slot, cfg, worldPaths)
}

// pseudoImportPath reports paths that name no loadable package, so no world can
// ever carry them and neither the gap set nor the coverage check may treat them
// as a package: "C" is cgo's pseudo-import, which go list reports among a cgo
// package's imports (sourceview already keeps it out of the manifest's load
// roots for the same reason). Asking for it as a gap would put an unloadable
// root in the world's path set; treating it as uncovered would send every
// project with a cgo package down the full load forever.
func pseudoImportPath(path string) bool { return path == "" || path == "C" }

// resolvedImportPath returns the package path an import RESOLVES to. The
// Imports map is keyed by the import string as written, which is not the
// imported package's path whenever the toolchain redirects it: the stdlib's own
// vendored dependencies are written "golang.org/x/net/http/httpproxy" and
// resolve to "vendor/golang.org/x/net/http/httpproxy", the form every types map
// — including sharedWorld.types — is keyed by. Comparing the written string
// against the world made a project whose load roots include net/http (any
// `.gsx` importing it) unservable forever, on a path that has nothing to do
// with the world's composition. The stub packages a reduced load produces
// carry the resolved path, so this needs no NeedDeps.
func resolvedImportPath(key string, imported *packages.Package) string {
	if imported != nil && imported.PkgPath != "" {
		return imported.PkgPath
	}
	return key
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
		if pseudoImportPath(path) || mainModule[path] || world.types[path] != nil {
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
		for key, imported := range p.Imports {
			need(resolvedImportPath(key, imported))
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

// sharedWorldKeyFor derives the process-wide cache key for one composed path
// set: origin, root binding, toolchain. Both tiers of coveringWorld go through
// it, so a tier-two world is keyed by exactly the same rules as a tier-one
// world — only their lifetimes differ (see sharedWorlds).
func (m *Module) sharedWorldKeyFor(cfg *packages.Config, paths []string) string {
	toolchain := ""
	if m.goContext != nil && m.goContext.goLauncher != nil {
		toolchain = m.goContext.goLauncher.CompilerIdentity()
	}
	origin := sharedWorldOrigin(m.opts.ModuleRoot, cfg.Env, paths)
	if sharedWorldRootBound(paths, m.opts.ModulePath) {
		origin += "\x00root\x00" + filepath.Clean(m.opts.ModuleRoot)
	}
	return sharedWorldKey(paths, cfg.Env, toolchain, origin)
}

// loadSharedWorld returns the cached closure for key, loading it if absent or
// stale. Freshness stamps cover module-owned files only; stdlib is covered by
// the toolchain identity in the key.
// slot, when non-empty, names the single extension-tier seat this key occupies:
// publishing into it drops whatever key held it before (see sharedWorlds).
func loadSharedWorld(key, slot string, cfg *packages.Config, loadPaths []string) (*sharedWorld, error) {
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
	if slot != "" {
		if previous, held := extensionSlots[slot]; held && previous != key {
			delete(sharedWorlds, previous)
		}
		extensionSlots[slot] = key
	}
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
