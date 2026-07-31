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
	// stamps covers modification of a file that was loaded; dirStamps covers
	// files being ADDED to or REMOVED from a loaded package, which no per-file
	// check can see (a directory's mtime moves when its entries change).
	// Together they catch the three ways the runtime can change under us.
	stamps    []fileStamp
	dirStamps []fileStamp
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

// sharedWorldEligible reports whether this Module's external needs are exactly
// the fixed gsx-runtime closure, judged from the resolved load set itself.
//
// Anything else in loadPaths is either user-configured (a filter, renderer,
// alias or class-merger package, which can live inside the main module and so
// import back into it — something the shared closure provably cannot, the gsx
// runtime being standard-library only) or a manifest LoadRoot: a package
// imported ONLY from .gsx files, whose *types* the caller needs. The project
// half is loaded without NeedTypes, so a LoadRoot cannot be served from it.
// Both cases keep the original single full-mode load.
func (m *Module) sharedWorldEligible() bool {
	o := m.opts
	// Filter, renderer, alias and class-merger packages are harvested from the
	// LOADED types, so they cannot be served by a project half loaded without
	// NeedTypes. Their presence disqualifies the fast path outright.
	if len(o.LoadPkgs) > 0 || len(o.Aliases) > 0 || o.ClassMerger != nil || len(finalRendererAliases(o.Renderers)) > 0 {
		return false
	}
	for _, f := range o.FilterPkgs {
		if f != stdImportPath {
			return false
		}
	}
	for _, d := range o.PerDir {
		if d.ClassMerger != nil {
			return false
		}
		for _, f := range d.FilterPkgs {
			if f != stdImportPath {
				return false
			}
		}
	}
	return true
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
// back-edge detection sees none (correct: the shared closure cannot back-edge)
// and projectSourcePackages skips them.
func (m *Module) loadExternalGraph(cfg *packages.Config, loadPaths []string) ([]*packages.Package, error) {
	if !m.sharedWorldEligible() {
		sharedWorldIneligible.Add(1)
		return packages.Load(cfg, loadPaths...)
	}
	sharedPaths := []string{gsxRuntimeImportPath, stdImportPath}
	shared := map[string]bool{gsxRuntimeImportPath: true, stdImportPath: true}
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
	key := sharedWorldKey(sharedPaths, cfg.Env, toolchain, origin)
	world, err := loadSharedWorld(key, cfg, sharedPaths)
	if err != nil {
		return nil, err
	}

	projectCfg := *cfg
	projectCfg.Mode = projectLoadMode
	pkgs, err := packages.Load(&projectCfg, projectPaths...)
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
		return packages.Load(cfg, loadPaths...)
	}
	for _, p := range pkgs {
		if p == nil {
			continue
		}
		for importPath := range p.Imports {
			if world.types[importPath] == nil && !mainModule[importPath] {
				sharedWorldFellBack.Add(1)
				return packages.Load(cfg, loadPaths...)
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
	pkgs, err := packages.Load(&loadCfg, loadPaths...)
	externalClosureLoads.Add(1)
	if err != nil {
		return nil, err
	}

	world := &sharedWorld{fset: fset, types: map[string]*types.Package{}, errs: map[string][]packages.Error{}}
	seen := map[string]bool{}
	seenDir := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			world.types[p.PkgPath] = p.Types
		}
		if len(p.Errors) > 0 {
			world.errs[p.PkgPath] = p.Errors
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
