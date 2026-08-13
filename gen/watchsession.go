package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

// cycleResult is the outcome of one generate cycle (cold startup or warm regen).
// Dir is the package directory regenerated. OK is true when the generate
// produced no error-severity diagnostics and no operational error. Diags holds
// all structured diagnostics; Err holds genuine operational failures (not
// codegen/type errors — those live in Diags).
type cycleResult struct {
	Dir     string
	Written []string
	// Removed holds the gsx-owned orphan .x.go paths deleted in this dir this
	// cycle (see gen/orphan.go). Populated both by regenDir's dir-scoped sweep
	// and by regenPending's onlyGeneratedRemains branch (the dir has no .gsx
	// left at all, so there is nothing to regenerate — only to sweep).
	Removed []string
	Diags   []diag.Diagnostic
	OK      bool
	Err     error
	DurMs   int64
	// Reload is the human-readable reason (codegen.RefreshVerdict.Describe())
	// this cycle required a full in-place world reload, e.g. "changed Go
	// source dep/dep.go". Empty when the cycle stayed warm — never a rendered
	// empty reason (RefreshVerdict.Describe() can itself return "" for a
	// pending-but-unattributed reload; consumers must treat that as "no
	// note", same as this field's own zero value). Reload is module-wide, not
	// per-dir: regenDirs stamps only its own first generated result per
	// Module per regenDirs call (reusing its chargedRefresh convention).
	// regenPending's orphan-only branch stamps its own lone result ONLY when
	// dir has no dependent at all (a true go-only leaf, e.g. an unreferenced
	// cmd/ package) — that dir never reaches regenDirs (affected stays empty
	// for it), so its own branch is the sole place its cause can surface; a
	// dir WITH a dependent leaves its own result unstamped (or unproduced,
	// when nothing was removed) since the dependent's own regenDirs call
	// independently recomputes and stamps the SAME module-wide verdict — see
	// its call site. These are still independent branches, not a single
	// per-cycle gate, so one regenPending cycle CAN produce more than one
	// Reload-carrying result for the same Module (e.g. two unrelated
	// dependent-less leaves changing in one batch) — every consumer folds a
	// batch down to one note via "first non-empty wins" (aggregateEvent and
	// firstReload for the dev panel, emitter.cycleBatch for the watch CLI's
	// console and NDJSON), not by relying on cycleResult itself being
	// pre-deduped. Results are produced in sorted dir order so "first" is the
	// same note on every run.
	Reload string
}

func (r cycleResult) durationMs() int64 { return r.DurMs }

// watchSession holds the live state for a running generate-on-change session.
// Discovery may span several independent modules, so the session keeps one warm
// Module per module root: each dir's regeneration must run against the Module
// anchored at ITS module, or cross-package refs in a sibling module read as "not
// loaded". root is the primary module root, used only for the startup banner.
type watchSession struct {
	cfg            watchConfig
	root           string                     // primary module root (startup banner only)
	roots          []string                   // every module root the session spans
	requestedRoots []string                   // exact user-selected trees; never excluded as descendants
	watchRoots     []string                   // requested trees plus their owning module trees
	modules        map[string]*codegen.Module // module root -> warm Module
}

// openModule constructs a fresh *codegen.Module for the given module root,
// threading all watchConfig options (filters, aliases, classifier, minifiers)
// into codegen.Open. It does not perform any analysis; analysis is
// lazy and triggered by the first Generate call.
func (s *watchSession) openModule(root string) (*codegen.Module, error) {
	_, modPath, err := moduleRoot(root)
	if err != nil {
		return nil, err
	}
	return codegen.Open(codegen.Options{
		ModuleRoot:   root,
		ModulePath:   modPath,
		FilterPkgs:   s.cfg.filterPkgs,
		Aliases:      s.cfg.aliases,
		Renderers:    s.cfg.renderers,
		Classifier:   s.cfg.cls,
		CSSMin:       s.cfg.cssMin,
		JSMin:        s.cfg.jsMin,
		JSONMin:      s.cfg.jsonMin,
		CSSMinify:    s.cfg.cssMinify,
		JSMinify:     s.cfg.jsMinify,
		VerbatimTags: s.cfg.verbatimTags,
		ClassMerger:  s.cfg.classMerger,
	})
}

// moduleForDir returns the warm Module for dir's enclosing module root. If the
// root has not been seen before (e.g. a newly created sub-module), it opens a
// new Module, stores it, and registers the root.
func (s *watchSession) moduleForDir(dir string) (*codegen.Module, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	if s.modules[root] == nil {
		m, err := s.openModule(root)
		if err != nil {
			return nil, err
		}
		s.modules[root] = m
		s.roots = append(s.roots, root)
		sort.Strings(s.roots)
	}
	return s.modules[root], nil
}

// goEditNeedsReopen reports whether an authored .go change in dir must escalate
// to a full session reopen instead of the in-place, closure-scoped goDirty
// regeneration.
//
// The in-place path refreshes exactly ONE Module: the one moduleForDir picks,
// rooted at dir's NEAREST go.mod. That is the whole story while dir belongs to
// the session module whose tree contains it. It is not when dir belongs to a
// module NESTED inside another session module — the layout a go.mod
// `replace example.com/x => ./x` produces. There the refresh lands on the
// nested Module, which nothing in the session generates against, while the
// consuming outer module structurally cannot observe the change: sourceview's
// walk stops at every nested go.mod, and RefreshDiskSourcesAndInvalidate
// rejects a dir the outer root does not own. Its dependents would keep
// generating against pre-edit types — silently wrong output, not a build
// error (a string→int signature keeps `Text(string(...))` compiling). Reopen
// instead: it rebuilds every session module from current disk, which is what
// the classification did for every .go edit before the goDirty split.
//
// Escalation is deliberately keyed on containment, not on parsing replace
// directives: an in-tree nested module is precisely the shape a directory
// replace produces, and a nested module the outer one does NOT consume costs
// only the reopen it always used to pay. Sibling module roots never escalate —
// an edit in module B's dir is served correctly by B's own Module, and no
// other session module contains it.
//
// A nested go.mod is not the only ownership boundary. sourceview's oracle also
// rejects any path with a `vendor` segment, so a vendored .go write — one
// `go mod vendor` produces hundreds — is attributed by moduleRoot to the
// enclosing module that provably cannot refresh it. Routed in place, the
// refresh returns an error, which RETAINS the dirty transaction and makes
// every later fire replay and fail it: the session wedges until restart.
// Consulting the same oracle the refresh will consult is the fix; a vendored
// write is dependency-surface movement anyway, so the reopen is both correct
// and what the pre-goDirty classification did. Discovery skips vendor
// (gen/gen.go's skipDirs), so the reopen never visits it.
//
// Cost: one moduleRoot plus one ownership probe per authored .go change.
// classifyDirtyFile skips the call entirely once the cycle is already
// escalated, so a bulk vendor rewrite pays it once, not once per file.
func (s *watchSession) goEditNeedsReopen(dir string) bool {
	if s == nil {
		return false
	}
	owner, _, err := moduleRoot(dir)
	if err != nil {
		// dir's module boundary is unreadable — a removed tree, an unparsable
		// go.mod, a file outside every module. Fail closed: reopen re-resolves
		// the whole session from disk, which is exactly the recovery an
		// unreadable boundary needs, and it cannot wedge the way an in-place
		// refresh of an unroutable dir does.
		return true
	}
	owner = filepath.Clean(owner)
	// The oracle RefreshDiskSourcesAndInvalidate itself applies. A probe error
	// leaves ownership indeterminate, which is the same fail-closed case.
	if owned, ownErr := sourceview.OwnsDir(owner, dir); ownErr != nil || !owned {
		return true
	}
	if len(s.roots) < 2 {
		// One module in the session: nothing else can contain dir.
		return false
	}
	for _, root := range s.roots {
		if filepath.Clean(root) != owner && pathWithinTree(root, dir) {
			return true
		}
	}
	return false
}

// prepareWatchSession resolves the structural observation roots and opens one
// lazy Module per module root. It deliberately does not discover GSX files or
// generate: callers must arm fsnotify first, then call initialGenerate. Keeping
// those phases separate closes the startup edit gap.
func prepareWatchSession(cfg watchConfig) (*watchSession, error) {
	targets, err := resolveWatchTargets(cfg.paths)
	if err != nil {
		return nil, err
	}
	if len(targets.moduleRoots) == 0 {
		return nil, fmt.Errorf("watch: no module roots resolved from %v", cfg.paths)
	}

	s := &watchSession{
		cfg:            cfg,
		root:           targets.moduleRoots[0],
		roots:          append([]string(nil), targets.moduleRoots...),
		requestedRoots: append([]string(nil), targets.requestedRoots...),
		watchRoots:     append([]string(nil), targets.watchRoots...),
		modules:        map[string]*codegen.Module{},
	}
	for _, root := range s.roots {
		m, err := s.openModule(root)
		if err != nil {
			return nil, err
		}
		s.modules[root] = m
	}

	// Validate the class merger once at session startup so a bad-signature merger
	// surfaces a clear error instead of silently emitting uncompilable .x.go
	// files on every regen cycle. This runs against s.root's ALREADY-OPEN,
	// fully-configured Module (codegen.Module.ValidateConfiguredMergers) rather
	// than a throwaway probe built from just the ClassMergerRef: a probe scoped
	// to the merger alone composes a NARROWER shared-world path set than this
	// Module's own FilterPkgs/Aliases/Renderers, which used to mint a second,
	// differently-keyed world at every configured-module session startup instead
	// of sharing the one Generate is about to load anyway (see
	// TestWatchSession_ConfiguredModuleWorldBudget). The LSP and fmt call
	// codegen.Open directly and must not pay a packages.Load per call, so this
	// validation lives here and NOT in codegen.Open or codegen.GenerateDirs (the
	// latter already validates too, memoized, as a defense-in-depth backstop).
	if cfg.classMerger != nil {
		if err := s.modules[s.root].ValidateConfiguredMergers(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// initialGenerate snapshots the authored package membership after observation
// is armed, then generates every discovered package and sweeps cold-start
// orphans. The first Generate on each lazy Module publishes its immutable source
// manifest and populates the import graph used by warm reverse invalidation.
func (s *watchSession) initialGenerate() ([]cycleResult, error) {
	dirs, err := discoverDirs(s.cfg.paths)
	if err != nil {
		return nil, err
	}
	_, noModule := groupByModule(dirs)
	if len(noModule) > 0 {
		// A requested tree may contain both valid modules and a stray GSX package
		// outside every module. Do not silently omit the stray package.
		_, _, moduleErr := moduleRoot(noModule[0])
		return nil, moduleErr
	}

	// Walk-level orphan sweep: mirrors generateCached's sweepOrphanDirs call in
	// gen/cache.go. discoverDirs only returns dirs that still directly contain
	// a .gsx, so a directory whose sole .gsx was deleted before `gsx dev` ever
	// started drops out of `dirs` entirely and the per-dir sweep inside
	// regenDir below never fires for it — its stale gsx-owned .x.go would
	// otherwise survive indefinitely. Must run before the per-dir regen loop,
	// same ordering as the batch path.
	startup := sweepOrphanStartup(s.cfg.paths, dirs)
	startup = append(startup, s.regenDirs(dirs)...)
	return startup, nil
}

// reopen (re)opens a fresh Module for every module root the session spans, then
// re-runs regenDir for every discovered dir so the import graphs are fully
// repopulated. Call after a dep-surface change (new import, .go edit, go.mod
// change, etc.). This replaces the old rebuild() method.
//
// Returns the per-dir cycle results (mirroring initialGenerate's startup slice)
// so the caller can emit them. Returns (nil, err) on operational failure.
func (s *watchSession) reopen() ([]cycleResult, error) {
	targets, err := resolveWatchTargets(s.cfg.paths)
	if err != nil {
		return nil, err
	}
	modules := make(map[string]*codegen.Module, len(targets.moduleRoots))
	for _, root := range targets.moduleRoots {
		m, err := s.openModule(root)
		if err != nil {
			return nil, err
		}
		modules[root] = m
	}
	// Publish the replacement module set only after every root opened
	// successfully, so a transient failure cannot leave a half-reopened session.
	s.modules = modules
	s.roots = append(s.roots[:0], targets.moduleRoots...)
	s.watchRoots = append(s.watchRoots[:0], targets.watchRoots...)
	s.root = targets.moduleRoots[0]
	// Repopulate the graph for every dir by regenerating.
	dirs, err := discoverDirs(s.cfg.paths)
	if err != nil {
		return nil, err
	}
	// Walk-level orphan sweep — see the matching call in initialGenerate for
	// why this can't be left to the per-dir sweep inside regenDir alone.
	results := sweepOrphanStartup(s.cfg.paths, dirs)
	results = append(results, s.regenDirs(dirs)...)
	return results, nil
}

type watchTargets struct {
	requestedRoots []string
	watchRoots     []string
	moduleRoots    []string
}

// resolveWatchTargets separates structural observation from GSX package
// discovery. The requested paths determine which trees the user intends to
// watch, while their owning module roots ensure changes to go.mod and authored
// Go dependencies elsewhere in the module are also observed. Nested modules
// are registered up front even when they do not contain a .gsx file yet.
func resolveWatchTargets(paths []string) (watchTargets, error) {
	requested, err := requestedWatchRoots(paths)
	if err != nil {
		return watchTargets{}, err
	}

	moduleSet := map[string]bool{}
	scanSet := map[string]bool{}
	for _, root := range requested {
		scanSet[root] = true
		if ownerRoot, _, rootErr := moduleRoot(root); rootErr == nil {
			moduleSet[ownerRoot] = true
			scanSet[ownerRoot] = true
		}
	}

	// Scan both explicitly requested trees and enclosing module trees. The
	// latter matters when the user asks to watch one package: sibling authored
	// Go packages and nested modules are still part of its build graph.
	scanRoots := sortedSet(scanSet)
	for _, root := range compactModuleScanRoots(scanRoots) {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != root && moduleScanExcluded(path) {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() != "go.mod" {
				return nil
			}
			owner, _, ownerErr := moduleRoot(filepath.Dir(path))
			if ownerErr != nil {
				return ownerErr
			}
			moduleSet[owner] = true
			return nil
		})
		if err != nil {
			return watchTargets{}, err
		}
	}
	if len(moduleSet) == 0 {
		// No enclosing or nested module exists. Reuse moduleRoot's established
		// diagnostic rather than inventing a second no-module error surface.
		_, _, err := moduleRoot(requested[0])
		return watchTargets{}, err
	}

	moduleRoots := sortedSet(moduleSet)
	watchSet := map[string]bool{}
	for _, root := range requested {
		watchSet[root] = true
	}
	for _, root := range moduleRoots {
		watchSet[root] = true
	}
	return watchTargets{
		requestedRoots: requested,
		watchRoots:     compactRoots(sortedSet(watchSet)),
		moduleRoots:    moduleRoots,
	}, nil
}

func requestedWatchRoots(paths []string) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	roots := map[string]bool{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			path = filepath.Dir(path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		roots[filepath.Clean(abs)] = true
	}
	return sortedSet(roots), nil
}

func sortedSet(set map[string]bool) []string {
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

// compactRoots removes a root already covered by an ancestor. Inputs and
// outputs are deterministic; an explicitly requested junk-named root remains
// valid because exclusion applies only while descending below each root.
func compactRoots(roots []string) []string {
	return compactRootsBy(roots, excludedDir)
}

func compactModuleScanRoots(roots []string) []string {
	return compactRootsBy(roots, moduleScanExcluded)
}

func moduleScanExcluded(path string) bool {
	return excludedDir(path) || shouldSkipDir(filepath.Base(path))
}

func compactRootsBy(roots []string, excluded func(string) bool) []string {
	sort.Slice(roots, func(i, j int) bool {
		depthI := strings.Count(filepath.Clean(roots[i]), string(filepath.Separator))
		depthJ := strings.Count(filepath.Clean(roots[j]), string(filepath.Separator))
		if depthI != depthJ {
			return depthI < depthJ
		}
		return roots[i] < roots[j]
	})
	var compact []string
	for _, root := range roots {
		covered := false
		for _, parent := range compact {
			if treeRootCovers(parent, root, excluded) {
				covered = true
				break
			}
		}
		if !covered {
			compact = append(compact, root)
		}
	}
	sort.Strings(compact)
	return compact
}

// treeRootCovers reports whether walking parent with the supplied exclusion
// rules will actually reach child. A lexical ancestor does not cover an
// explicitly requested root below a skipped subtree.
func treeRootCovers(parent, child string, excluded func(string) bool) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if rel == "." {
		return true
	}
	current := parent
	for elem := range strings.SplitSeq(rel, string(filepath.Separator)) {
		current = filepath.Join(current, elem)
		if excluded(current) {
			return false
		}
	}
	return true
}

// sweepOrphanStartup runs the walk-level orphan sweep (sweepOrphanDirs) over
// paths, treating kept as the currently discovered dirs (dirs that still
// directly contain a .gsx — never swept). It's the watch-session cold-start
// counterpart to generateCached's call in gen/cache.go, and exists because
// initialGenerate/reopen only regenerate discovered dirs: a directory whose
// only .gsx is already gone drops out of discovery and would never be visited
// by regenDir's dir-scoped sweep.
//
// Removed paths are converted into cycleResults grouped by their real parent
// directory — never a fabricated Dir — mirroring regenPending's
// onlyGeneratedRemains branch (gen/watch.go) so callers (dev's overlay/log
// output, the watch emitter) see and report the removal exactly like any
// other warm-loop orphan sweep. A sweep I/O error (e.g. a permission-denied
// os.Remove) is folded into one Err-only cycleResult rather than treated as
// fatal to session startup — the same non-fatal handling generateCached gives
// the identical error.
func sweepOrphanStartup(paths, kept []string) []cycleResult {
	removed, err := sweepOrphanDirs(paths, kept)
	byDir := map[string][]string{}
	for _, p := range removed {
		d := filepath.Dir(p)
		byDir[d] = append(byDir[d], p)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	results := make([]cycleResult, 0, len(dirs)+1)
	for _, d := range dirs {
		results = append(results, cycleResult{Dir: d, Removed: byDir[d], OK: true})
	}
	if err != nil {
		results = append(results, cycleResult{Err: err})
	}
	return results
}

// regenDirs warm-regenerates the given package dirs from current disk,
// returning one cycleResult per dir in input order.
//
// Saved disk facts are refreshed — and stale cached analysis evicted — in ONE
// atomic RefreshDiskSourcesAndInvalidate batch per owning module before any
// generation. The refresh is the disk counterpart to SetOverride: it
// re-enumerates the dirs' package/import/membership facts beneath any override
// layer, and the eviction drops their reverse closures so generation
// re-type-checks from the refreshed source. The warm Module otherwise reads a
// frozen cold-load manifest, so without this a .gsx edit is invisible to
// Generate. Batching keeps the "regenerate from current disk" contract while
// paying the module-wide refresh bookkeeping once per cycle instead of once
// per directory — the per-dir form made cold startup and dep-surface reopen
// O(dirs × module files): ~26s on a 78-dir/1169-file module whose batch
// generate takes ~5s. TestWatchSession_ColdStartParseWorkIsLinear pins this.
//
// A disk write that lands after the batch refresh but before a later dir's
// Generate is not lost: observation is armed before every caller of this
// helper snapshots membership, so the write's fsnotify event re-dirties the
// dir and the next cycle regenerates it — the same guarantee the per-dir
// refresh relied on for writes landing after ITS refresh.
func (s *watchSession) regenDirs(dirs []string) []cycleResult {
	results := make([]cycleResult, len(dirs))
	starts := make([]time.Time, len(dirs))
	modules := make([]*codegen.Module, len(dirs))
	batchDirs := map[*codegen.Module][]string{}
	var batchOrder []*codegen.Module
	for i, dir := range dirs {
		starts[i] = time.Now()
		m, err := s.moduleForDir(dir)
		if err != nil {
			results[i] = cycleResult{Dir: dir, Err: err, DurMs: time.Since(starts[i]).Milliseconds()}
			continue
		}
		modules[i] = m
		if _, seen := batchDirs[m]; !seen {
			batchOrder = append(batchOrder, m)
		}
		batchDirs[m] = append(batchDirs[m], dir)
	}
	refreshMs := make(map[*codegen.Module]int64, len(batchOrder))
	// moduleReload holds each module's world-reload verdict description for
	// this cycle ("" when no reload is pending). The batch call's own verdict
	// wins outright; a fallback per-dir retry keeps the first non-empty
	// Describe() it sees, since a Module-wide pending reload can be
	// attributed by an earlier dir's own scoped diff and only echoed via
	// persisted attribution by a later dir's call (see refreshVerdictLocked's
	// three-way fallback in RefreshDiskSourcesAndInvalidate's doc).
	moduleReload := make(map[*codegen.Module]string, len(batchOrder))
	dirErr := map[string]error{}
	dirErrMs := map[string]int64{}
	for _, m := range batchOrder {
		t := time.Now()
		_, verdict, err := m.RefreshDiskSourcesAndInvalidate(batchDirs[m]...)
		refreshMs[m] = time.Since(t).Milliseconds()
		if err == nil {
			moduleReload[m] = verdict.Describe()
			continue
		}
		// The batch refresh is all-or-nothing (atomic, no partial state), so
		// one broken dir — an unreadable directory, a paired .x.go path
		// occupied by a non-regular file — would otherwise block every
		// healthy sibling in the module for the cycle. Re-scope per dir: the
		// failure isolates to exactly the culprit dirs, and healthy dirs
		// refresh and regenerate, restoring the per-dir form's partial-failure
		// behavior (a module-wide failure such as a broken cold Build simply
		// fails every per-dir attempt too, matching the old cold path).
		refreshMs[m] = 0 // per-dir retries carry their own timing below
		for _, dir := range batchDirs[m] {
			dt := time.Now()
			_, dirVerdict, derr := m.RefreshDiskSourcesAndInvalidate(dir)
			if derr != nil {
				dirErr[dir] = derr
			} else if moduleReload[m] == "" {
				moduleReload[m] = dirVerdict.Describe()
			}
			dirErrMs[dir] = time.Since(dt).Milliseconds()
		}
	}
	chargedRefresh := make(map[*codegen.Module]bool, len(batchOrder))
	for i, dir := range dirs {
		m := modules[i]
		if m == nil {
			continue // moduleForDir already failed; results[i] is set
		}
		if err := dirErr[dir]; err != nil {
			results[i] = cycleResult{Dir: dir, Err: err, DurMs: dirErrMs[dir]}
			continue
		}
		results[i] = s.generateDir(m, dir)
		// Fold this dir's share of refresh time into its result so aggregate
		// durations (aggregateEvent sums per-dir DurMs) cover the whole
		// cycle's work: the batch refresh is charged to the module's first
		// generated dir, a fallback per-dir refresh to its own dir.
		results[i].DurMs += dirErrMs[dir]
		if !chargedRefresh[m] {
			chargedRefresh[m] = true
			results[i].DurMs += refreshMs[m]
			// Same "first result of the cycle" convention as the refresh-time
			// charge above: the reload note is module-wide, not per-dir, so it
			// lands on exactly one result per module per cycle.
			results[i].Reload = moduleReload[m]
		}
	}
	return results
}

// regenDir warm-regenerates one package dir from current disk; it is the
// single-dir form of regenDirs and shares its refresh/generate contract.
func (s *watchSession) regenDir(dir string) cycleResult {
	return s.regenDirs([]string{dir})[0]
}

// generateDir runs the post-refresh half of one dir's regeneration: orphan
// sweep, Module.Generate, poison-on-error, and the hash-gated write. Callers
// must have refreshed and invalidated dir's saved disk facts first (see
// regenDirs).
func (s *watchSession) generateDir(m *codegen.Module, dir string) cycleResult {
	start := time.Now()
	// Dir-scoped orphan sweep: a .gsx sibling deletion in dir is independent
	// of what this cycle regenerates for the .gsx files still present, so it
	// runs unconditionally (mirrors writeDirOutcome in gen/cache.go).
	removed, remErr := removeOrphanXgo(dir)
	out, diags, gerr := m.Generate(dir)
	files := make(map[string][]byte, len(out))
	for gsxPath, b := range out {
		files[strings.TrimSuffix(gsxPath, ".gsx")+".x.go"] = b
	}
	// Error diagnostics: the module skipped emitting this package, so `out` is
	// empty for the blamed files. Write poison instead of leaving stale .x.go —
	// same invariant as the batch path (see gen/poison.go). A poisoning failure
	// (e.g. os.ReadDir erroring) must not be silently dropped: it's surfaced via
	// Err below so stale .x.go left in place is at least visible.
	var poisonErr error
	if anyErrorDiag(diags) {
		if po, perr := poisonPkgOutput(dir, diags); perr == nil {
			for rel, b := range po {
				files[filepath.Join(dir, rel)] = b
			}
		} else {
			poisonErr = perr
		}
	}
	written, werr := writeFiles(dir, files)
	var finalErr error
	switch {
	case gerr != nil && !anyErrorDiag(diags):
		finalErr = gerr
	case poisonErr != nil:
		finalErr = poisonErr
	case remErr != nil:
		finalErr = remErr
	case werr != nil:
		finalErr = werr
	}
	return cycleResult{
		Dir:     dir,
		Written: written,
		Removed: removed,
		Diags:   diags,
		OK:      gerr == nil && !anyErrorDiag(diags) && werr == nil && remErr == nil,
		Err:     finalErr,
		DurMs:   time.Since(start).Milliseconds(),
	}
}

// writeFiles persists generated bytes (keyed by absolute .x.go paths) to dir
// via hash-gated restore, returning the paths actually written and any I/O error
// (e.g. disk full, permission denied).
func writeFiles(dir string, files map[string][]byte) ([]string, error) {
	po := pkgOutput{}
	for absXGo, b := range files {
		po[filepath.Base(absXGo)] = b
	}
	written, _, err := restore(dir, po)
	return written, err
}

// regenPending runs one regeneration pass over the dirty set. When depDirty is
// set, it reopens every module (full) and returns all per-dir results. Otherwise
// it invalidates each pending dir (skipping dirs with no remaining .gsx),
// computes the affected reverse-closure from each Module's import graph, and
// regenerates those dirs. On a reopen error it returns (nil, err); the caller
// must preserve pending+depDirty and retry on the next fire.
func (s *watchSession) regenPending(pending map[string]bool, depDirty bool) ([]cycleResult, error) {
	if depDirty {
		return s.reopen()
	}
	affected := map[string]bool{}
	var results []cycleResult
	// Sorted, like affectedDirs below: the orphan-only branch can stamp a
	// reload note on more than one result, and every consumer folds a batch
	// with "first non-empty wins" — so which cause a developer is shown must
	// not depend on map iteration order.
	for _, dir := range sortedSet(pending) {
		m, err := s.moduleForDir(dir)
		if err != nil {
			results = append(results, cycleResult{Dir: dir, Err: err})
			continue
		}
		// Saved GSX creates, writes, renames, and removals all refresh the exact
		// directory source view before ordinary graph invalidation. The Module
		// decides from parsed package/import facts whether this stays warm or
		// requires an atomic source-inventory reload; watch does not guess from
		// fsnotify operation kinds. Refresh and invalidate run as the one atomic
		// RefreshDiskSourcesAndInvalidate call (not separate RefreshDiskSources +
		// Invalidate calls) per RefreshDiskSources' own doc: a caller that also
		// needs invalidation must use the combined form so no analysis can
		// observe refreshed saved bytes through stale retained facts; it also
		// surfaces the refresh's RefreshVerdict for the orphan-only branch below,
		// which produces a cycleResult of its own and would otherwise carry no
		// reload attribution (a non-empty dir's own reload note is captured by
		// the later regenDirs(affectedDirs) call instead).
		_, verdict, err := m.RefreshDiskSourcesAndInvalidate(dir)
		if err != nil {
			return results, fmt.Errorf("refresh saved GSX sources in %s: %w", dir, err)
		}
		empty := onlyGeneratedRemains(dir)
		hasDependent := false
		for _, dep := range m.Dependents(dir) {
			if empty && dep == dir {
				continue
			}
			affected[dep] = true
			hasDependent = true
		}
		if empty {
			// Nothing left to regenerate in dir, but a .gsx that used to live
			// here may have just been deleted, leaving its .x.go orphaned. This
			// is the walk-level sweep's job for the watch loop: unlike the batch
			// path (sweepOrphanDirs walks the tree), fsnotify already told us
			// exactly which dir changed, so a plain dir-scoped sweep suffices.
			removed, rerr := removeOrphanXgo(dir)
			if rerr != nil {
				results = append(results, cycleResult{Dir: dir, Err: rerr})
				continue
			}
			// A dir can reach here with nothing removed at all: a .gsx-free
			// package (e.g. a cmd/ leaf nothing imports) whose .go file just
			// changed is "empty" (onlyGeneratedRemains doesn't require a
			// leftover .x.go). When it ALSO has no dependents (hasDependent is
			// false), nothing else in this cycle will ever carry its reload
			// cause forward — affected stays empty for it, so it never reaches
			// regenDirs. Stamp this dir's own lone result in exactly that case.
			// When a dependent DOES exist, leave a nothing-removed result
			// unproduced here: the dependent's own regenDirs call independently
			// recomputes (and stamps) the SAME module-wide verdict, and adding
			// a second, phantom "regenerated nothing" result for dir here would
			// both duplicate the note and violate "one cycleResult per dir that
			// actually did something" (see TestWatchSession_GoEditRegeneratesOnlyDependents,
			// which asserts dep contributes no cycleResult when page, its
			// dependent, carries the note instead).
			reload := verdict.Describe()
			if len(removed) > 0 || (reload != "" && !hasDependent) {
				results = append(results, cycleResult{Dir: dir, Removed: removed, OK: true, Reload: reload})
			}
			continue
		}
	}
	// The affected reverse closure can be large (a shared component dirties
	// every dependent); regenerate it as one batch so the refresh bookkeeping
	// is paid per cycle, not per dir. Sorted for deterministic emit order.
	affectedDirs := make([]string, 0, len(affected))
	for dir := range affected {
		affectedDirs = append(affectedDirs, dir)
	}
	sort.Strings(affectedDirs)
	results = append(results, s.regenDirs(affectedDirs)...)
	return results, nil
}
