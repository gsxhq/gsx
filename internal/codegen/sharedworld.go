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
// — sharedWorldComposition drops them — so any
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
var sharedWorldIneligible, sharedWorldFellBack, sharedWorldFast, sharedWorldPreload atomic.Int64

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
// half references: a Go file importing a package outside the configured
// closure, or a dependency that came back from the world load without types.
// It is the post-load half of the eligibility rule — the half that costs three
// loads to reach, which is why the verdict is remembered per Module (see
// SharedWorldPreloadFallbacks).
func SharedWorldCoverageFallbacks() int64 { return sharedWorldFellBack.Load() }

// SharedWorldPreloadFallbacks returns the process-wide count of Modules that
// took the single full-mode load without touching a world at all: a manifest
// LoadRoot outside the configured closure (decided before any load), or a
// Module already known unservable from an earlier analysis. This is the
// cost-free refusal — exactly the one load the pre-shared-world code paid.
func SharedWorldPreloadFallbacks() int64 { return sharedWorldPreload.Load() }

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

// sharedWorlds is unbounded and never evicted: keys are one per distinct
// (config paths, origin, env, toolchain) closure, which a CLI or test process
// holds one or two of and a long-lived LSP one per open project. That is a
// bounded set because it follows CONFIGURATION, which changes when gsx.toml
// does — unlike the project's import set, which moves whenever a developer
// types an import. (An earlier revision keyed a second tier on that import set
// and needed an eviction policy to stay bounded; the policy could delete
// another root's config world, which is one of the reasons that tier was
// descoped. See the design's "Extension tier, descoped".)
//
// Only HEALTHY worlds are published here — see loadSharedWorld. Duplicate
// concurrent cold loads for one key are possible and accepted (last write wins;
// both worlds are correct).
var (
	sharedWorldMu sync.Mutex
	sharedWorlds  = map[string]*sharedWorld{}
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
// packages from. Without it two roots that resolve the same import path to
// different code would share one cache entry, and the second would be served
// the first's types — the entry would even pass sharedWorld.fresh, because the
// stamps it checks belong to the other directory and are genuinely unchanged.
//
// It hashes EVERY resolution directive of go.mod plus the whole go.sum, not
// the lines that mention a composed package. Filtering by relevance was wrong
// in a way only a probe found: a version bump or replace swap of a DEPENDENCY
// of a composed package changes what that package's types are built from while
// touching no line the filter admitted, so the cached world stayed keyed,
// stayed fresh (the old target's files are unchanged), and served types the
// compiler no longer agreed with — including silently different emitted bytes
// when the change altered a filter's arity or a renderer's type. Hashing the
// resolution wholesale is right by construction rather than by enumeration:
// any go.mod edit that can move any dependency re-keys every world for this
// root.
//
// The one directive deliberately EXCLUDED is `module` — the main module's own
// path. A world holds no main-module code (see sharedWorldComposition), so the
// name this root gives itself cannot change what is in one; including it would
// give every project its own private world and give up the cross-root sharing
// that is the whole point (two roots that resolve the gsx runtime identically
// must share its closure — TestExternalClosureLoadsOncePerProcess).
// Filesystem replace targets are resolved to absolute paths, because two roots
// can spell the same relative target and mean different directories.
//
// Two situations forfeit content hashing and bind the key to the module root
// instead — losing cross-root sharing, never correctness:
//
//   - a Go workspace (GOWORK), which redirects resolution in ways go.mod does
//     not record;
//   - vendoring, where packages resolve out of vendor/ and go.mod does not
//     constrain their contents at all. Two worktrees of one vendored project
//     have byte-identical go.mod files and may hold different vendored code —
//     probed on this branch, and it emitted the wrong .x.go bytes. PR #178's
//     commit message claimed this guard; it was never in the tree.
func sharedWorldOrigin(moduleRoot string, buildEnv []string, vendored bool) string {
	if work := environmentValue(buildEnv, "GOWORK"); work != "" && work != "off" {
		return moduleRoot
	}
	if vendored {
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
	var b strings.Builder
	if f.Go != nil {
		// The main module's language version participates in module-graph
		// pruning, so it can change the build list.
		fmt.Fprintf(&b, "go\x00%s\n", f.Go.Version)
	}
	if f.Toolchain != nil {
		fmt.Fprintf(&b, "toolchain\x00%s\n", f.Toolchain.Name)
	}
	for _, r := range f.Require {
		if r.Mod.Path != "" {
			fmt.Fprintf(&b, "require\x00%s\x00%s\n", r.Mod.Path, r.Mod.Version)
		}
	}
	for _, r := range f.Replace {
		if r.Old.Path == "" {
			continue
		}
		target := r.New.Path
		if r.New.Version == "" && !filepath.IsAbs(target) {
			// A filesystem replace is relative to the module root: the same
			// line in two roots names two directories.
			target = filepath.Clean(filepath.Join(moduleRoot, target))
		}
		fmt.Fprintf(&b, "replace\x00%s\x00%s\x00%s\x00%s\n", r.Old.Path, r.Old.Version, target, r.New.Version)
	}
	for _, e := range f.Exclude {
		fmt.Fprintf(&b, "exclude\x00%s\x00%s\n", e.Mod.Path, e.Mod.Version)
	}
	// go.sum pins the CONTENT of every versioned module in the graph, so a
	// re-resolution that keeps the version strings but changes the bytes (a
	// republished tag, a different GOPROXY) still re-keys.
	if sum, sumErr := os.ReadFile(filepath.Join(moduleRoot, "go.sum")); sumErr == nil {
		b.WriteString("\x00go.sum\x00")
		b.Write(sum)
	}
	return b.String()
}

// moduleIsVendored reports whether this module resolves packages from vendor/.
// The frozen GoCommandContext already computed it for the cache fingerprint;
// without one (syntax-only Opens, tests), the modules.txt marker cmd/go itself
// keys vendor mode on is read directly.
func (m *Module) moduleIsVendored() bool {
	if m.goContext != nil {
		return m.goContext.vendorDir
	}
	_, err := os.Stat(filepath.Join(m.opts.ModuleRoot, "vendor", "modules.txt"))
	return err == nil
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
// eligible path it serves the gsx closure plus the module's out-of-module config
// packages from the process-wide shared world and loads only the project's own
// packages, then hands back the project packages plus one synthetic entry per
// shared package so the caller's allTypes/errs harvest is unchanged. Synthetic
// entries carry no Imports and no Module, so back-edge detection
// (externalBackedgePackages, called by the caller on this function's return
// value) sees none and projectSourcePackages skips them.
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
//
// The world serves what the CONFIGURATION names and nothing else. A module that
// needs types from anywhere else — a `.gsx` importing an out-of-module package,
// a Go file importing a third-party dependency the config closure does not
// reach — is not servable, and says so as early as it can: the load-path scan
// below runs before any load at all, so such a module pays exactly the one
// full-mode load it paid before this phase existed. (An earlier revision
// composed those references into a second world tier. It worked and it was
// fast, but its identity model could not capture what determined its contents —
// see the design's "Extension tier, descoped" — so it is gone and this rule
// covers the same ground by refusing instead of composing.)
func (m *Module) loadExternalGraph(cfg *packages.Config, loadPaths []string) ([]*packages.Package, error) {
	// A Module that has already been found unservable stays unservable: the
	// verdict below costs two loads to reach, and a dev loop re-runs this on
	// every `.go` edit. Remembering it is what keeps the ineligible cost at the
	// pre-phase one load per cycle rather than three. It can only be
	// conservative — a module that becomes servable again keeps taking the full
	// load until it is reopened, which go.mod changes and config changes do
	// anyway.
	if m.worldUnservable() {
		sharedWorldPreload.Add(1)
		return loadPackages(cfg, loadPaths...)
	}
	configPaths, ok := m.sharedWorldComposition()
	if !ok {
		sharedWorldIneligible.Add(1)
		return loadPackages(cfg, loadPaths...)
	}
	shared := make(map[string]bool, len(configPaths))
	for _, p := range configPaths {
		shared[p] = true
	}

	// Pre-load eligibility. Every load path is either the module's own "./...",
	// a package of this module (covered by that pattern), or a composed config
	// package the world carries. Anything else is a manifest LoadRoot pointing
	// outside the configuration — the types for it would have come from the full
	// load's NeedDeps, and no world this Module composes has them. Deciding it
	// here, before the project half and the world load, is what makes the
	// refusal cost exactly one load.
	for _, p := range loadPaths {
		if p == "./..." || shared[p] || m.ownsImportPath(p) || maybeStandardImportPath(p) {
			continue
		}
		sharedWorldPreload.Add(1)
		m.markWorldUnservable()
		return loadPackages(cfg, loadPaths...)
	}

	var projectPaths []string
	for _, p := range loadPaths {
		if !shared[p] {
			projectPaths = append(projectPaths, p)
		}
	}
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

	world, err := loadSharedWorld(m.sharedWorldKeyFor(cfg, configPaths), cfg, configPaths)
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
	if world.mainModuleBackedge(m.opts.ModulePath) {
		sharedWorldBackedge.Add(1)
		m.markWorldUnservable()
		m.clearSharedFset()
		return loadPackages(cfg, loadPaths...)
	}

	// Coverage. The project half is loaded WITHOUT types, so every package it
	// references from outside this module must be in the world. The load-path
	// scan above cannot see these: they are imports of the module's own Go and
	// .gsx files, discovered only by loading them. Reaching this costs three
	// loads, which is why the verdict is remembered — the SECOND cycle of such a
	// module is back to one.
	for _, p := range projectPaths {
		if p == "./..." || mainModule[p] || world.types[p] != nil {
			continue
		}
		sharedWorldFellBack.Add(1)
		m.markWorldUnservable()
		m.clearSharedFset()
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
				m.markWorldUnservable()
				m.clearSharedFset()
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

// maybeStandardImportPath applies cmd/go's own test for a standard-library
// import path — no dot in the first path element — to decide whether a load
// root MIGHT already be inside the world. It has to be a "might": the world
// carries the stdlib packages the gsx runtime's closure reaches, which is most
// of the common ones but not all of them (net/rpc is not in it), and no
// pre-load test can know which. So a stdlib-shaped load root is admitted here
// and validated after the world loads, where `world.types[p] != nil` answers it
// exactly. Getting this wrong in the other direction is what would hurt: a
// `.gsx` importing "strings" is the common case, and refusing it up front would
// take most real projects off the fast path — the #178 review's lesson.
//
// A module whose path has no dot in its first element ("foo/bar", from
// `go mod init foo`) is admitted too, and likewise resolved by the post-load
// check. Both errors land on the safe side: a slower verdict, never a wrong one.
func maybeStandardImportPath(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// ownsImportPath reports whether path names a package of this module. The
// prefix test errs toward "mine", which is safe here: a nested module sharing
// the prefix is then loaded into the project half instead of being refused
// up front, and the coverage check below catches it with real module identity.
func (m *Module) ownsImportPath(path string) bool {
	if m.opts.ModulePath == "" {
		return false
	}
	return path == m.opts.ModulePath || strings.HasPrefix(path, m.opts.ModulePath+"/")
}

// worldUnservable/markWorldUnservable carry the sticky verdict described in
// loadExternalGraph. Guarded by m.mu like every other cross-analysis field.
func (m *Module) worldUnservable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sharedWorldUnservable
}

func (m *Module) markWorldUnservable() {
	m.mu.Lock()
	m.sharedWorldUnservable = true
	m.mu.Unlock()
}

// clearSharedFset drops a shared-world FileSet the Module may be holding from
// an earlier analysis. Every fallback path calls it: the full load puts every
// position back in m.fset, so a retained m.sharedFset would leave
// positionResolver routing high Pos values at a FileSet this analysis no longer
// uses — positions from a world that no longer serves this Module.
func (m *Module) clearSharedFset() {
	m.mu.Lock()
	m.sharedFset = nil
	m.mu.Unlock()
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

// sharedWorldKeyFor derives the process-wide cache key for one composed path
// set: origin, root binding, toolchain.
func (m *Module) sharedWorldKeyFor(cfg *packages.Config, paths []string) string {
	toolchain := ""
	if m.goContext != nil && m.goContext.goLauncher != nil {
		toolchain = m.goContext.goLauncher.CompilerIdentity()
	}
	origin := sharedWorldOrigin(m.opts.ModuleRoot, cfg.Env, m.moduleIsVendored())
	if sharedWorldRootBound(paths, m.opts.ModulePath) {
		origin += "\x00root\x00" + filepath.Clean(m.opts.ModuleRoot)
	}
	return sharedWorldKey(paths, cfg.Env, toolchain, origin)
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
	}
	seen := map[string]bool{}
	seenDir := map[string]bool{}
	healthy := true
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			world.types[p.PkgPath] = p.Types
		}
		if len(p.Errors) > 0 {
			world.errs[p.PkgPath] = p.Errors
			healthy = false
		}
		// A module-owned package with no compiled files is broken in the way
		// that leaves nothing to stamp: build constraints excluded everything,
		// the directory is empty, the package does not exist yet. go/packages
		// still materializes a non-nil EMPTY types.Package for it, so every
		// downstream check passes and the world looks servable.
		if p.Module != nil && len(p.CompiledGoFiles) == 0 {
			healthy = false
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

	// An unhealthy world is SERVED — its errors must surface loudly, exactly as
	// #178 established for a broken runtime — but it is not published. Caching it
	// is what turns a transient authoring state into a permanent one: a package
	// that is broken in a fileless way contributes no stamps, so fresh() can
	// never fail for it, and no key component moves when the developer fixes the
	// file, so the process serves the stale failure until it restarts. The
	// adversarial review wedged both a config package and a dependency this way.
	// Not caching costs a reload per cycle while broken — precisely the
	// pre-shared-world cost, for precisely the broken shape — and the next cycle
	// after the fix is healthy and cached.
	if !healthy {
		return world, nil
	}
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
