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
		ModuleRoot:  root,
		ModulePath:  modPath,
		FilterPkgs:  s.cfg.filterPkgs,
		Aliases:     s.cfg.aliases,
		Renderers:   s.cfg.renderers,
		Classifier:  s.cfg.cls,
		CSSMin:      s.cfg.cssMin,
		JSMin:       s.cfg.jsMin,
		JSONMin:     s.cfg.jsonMin,
		CSSMinify:   s.cfg.cssMinify,
		JSMinify:    s.cfg.jsMinify,
		ClassMerger: s.cfg.classMerger,
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

	// Validate the class merger once at session startup so a bad-signature merger
	// surfaces a clear error instead of silently emitting uncompilable .x.go
	// files on every regen cycle. The LSP and fmt call codegen.Open directly and
	// must not pay a packages.Load per call, so this validation lives here and
	// NOT in codegen.Open or codegen.GenerateDirs (the latter already validates).
	if cfg.classMerger != nil {
		if err := codegen.ValidateClassMerger(targets.moduleRoots[0], cfg.classMerger); err != nil {
			return nil, err
		}
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
	for _, dir := range dirs {
		startup = append(startup, s.regenDir(dir))
	}
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
	for _, dir := range dirs {
		results = append(results, s.regenDir(dir))
	}
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

// regenDir warm-regenerates one package dir using its module's warm Module. It
// calls Module.Generate, maps the gsx-path-keyed output to .x.go files, and
// writes them via the hash-gated writeFiles helper.
func (s *watchSession) regenDir(dir string) cycleResult {
	start := time.Now()
	m, err := s.moduleForDir(dir)
	if err != nil {
		return cycleResult{Dir: dir, Err: err, DurMs: time.Since(start).Milliseconds()}
	}
	// Refresh authoritative saved disk facts for dir before warm invalidation,
	// then evict dir's stale cached analysis. The warm Module reads source from a
	// frozen cold-load manifest, not live disk, so without this a .gsx edit is
	// invisible to Generate. RefreshDiskSources is the disk counterpart to
	// SetOverride (it re-enumerates dir's package/import/membership facts and
	// updates the saved layer beneath any override); Invalidate then drops dir's
	// reverse closure so it re-type-checks from the refreshed source. This is the
	// same refresh-before-invalidation sequence regenPending applies to the
	// changed dir, and it restores the "regenDir regenerates dir from current
	// disk" contract every direct caller (initial generate, reopen, warm
	// dependents) relies on.
	if err := m.RefreshDiskSources(dir); err != nil {
		return cycleResult{Dir: dir, Err: err, DurMs: time.Since(start).Milliseconds()}
	}
	m.Invalidate(dir)
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
	for dir := range pending {
		m, err := s.moduleForDir(dir)
		if err != nil {
			results = append(results, cycleResult{Dir: dir, Err: err})
			continue
		}
		// Saved GSX creates, writes, renames, and removals all refresh the exact
		// directory source view before ordinary graph invalidation. The Module
		// decides from parsed package/import facts whether this stays warm or
		// requires an atomic source-inventory reload; watch does not guess from
		// fsnotify operation kinds.
		if err := m.RefreshDiskSources(dir); err != nil {
			return results, fmt.Errorf("refresh saved GSX sources in %s: %w", dir, err)
		}
		m.Invalidate(dir)
		empty := onlyGeneratedRemains(dir)
		for _, dep := range m.Dependents(dir) {
			if empty && dep == dir {
				continue
			}
			affected[dep] = true
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
			if len(removed) > 0 {
				results = append(results, cycleResult{Dir: dir, Removed: removed, OK: true})
			}
			continue
		}
	}
	for dir := range affected {
		results = append(results, s.regenDir(dir))
	}
	return results, nil
}
