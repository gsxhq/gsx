package gen

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceview"
)

func generateCached(paths, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, useCache bool, cssMin, jsMin, jsonMin func(string) (string, error), cssMinify, jsMinify bool, classMerger *codegen.ClassMergerRef) (Result, error) {
	result, _, err := generateCachedWithReport(paths, filterPkgs, aliases, renderers, cls, useCache, cssMin, jsMin, jsonMin, cssMinify, jsMinify, classMerger)
	return result, err
}

func generateCachedWithReport(paths, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, useCache bool, cssMin, jsMin, jsonMin func(string) (string, error), cssMinify, jsMinify bool, classMerger *codegen.ClassMergerRef) (Result, cacheReport, error) {
	var res Result
	var report cacheReport
	dirs, err := discoverDirs(paths)
	if err != nil {
		return res, report, err
	}

	// Walk-level orphan sweep: a directory whose only .gsx was just deleted
	// drops out of `dirs` entirely (discoverDirs only returns dirs that still
	// directly contain a .gsx), so the per-dir sweep inside
	// generateModule/writeDirOutcome below — which only runs for dirs in `dirs`
	// — never fires for it. This must run even when `dirs` ends up empty (e.g.
	// the requested path's ONLY .gsx was just deleted). See gen/orphan.go.
	if removed, swErr := sweepOrphanDirs(paths, dirs); swErr != nil {
		res.Errs = append(res.Errs, swErr)
	} else {
		res.Removed = removed
	}

	if len(dirs) == 0 {
		if len(res.Errs) > 0 {
			return res, report, errors.Join(res.Errs...)
		}
		return res, report, nil
	}

	// Discovery walks DOWN across go.mod boundaries, so the dir set may span
	// several independent modules (e.g. an examples/ tree of sub-modules). Each
	// module must be loaded against its OWN root: go/packages, cache keys, and the
	// manifest are all module-anchored, and a dir loaded against a foreign module
	// fails to type-check. Group by module and generate each group separately.
	groups, noModule := groupByModule(dirs)
	for _, d := range noModule {
		res.Errs = append(res.Errs, fmt.Errorf("gen: no go.mod found above %s", d))
	}
	for _, g := range groups {
		generateModule(g, filterPkgs, aliases, renderers, cls, useCache, cssMin, jsMin, jsonMin, cssMinify, jsMinify, classMerger, &res, &report)
	}

	sort.Strings(res.Written)
	sort.Strings(res.Removed)
	if len(res.Errs) > 0 {
		return res, report, errors.Join(res.Errs...)
	}
	// Return a non-nil error when there are error-severity diagnostics, so that
	// callers using the Go API (Generate, generate) can still detect failure
	// without inspecting res.Diags directly.
	if anyErrorDiag(res.Diags) {
		return res, report, errors.New("codegen: diagnostics reported")
	}
	return res, report, nil
}

// generateModule generates every .gsx package dir in one module group g, anchored
// at g.root, appending its outcome (written paths, diagnostics, operational
// errors) to res. It mirrors the single-module pipeline: cache HIT restore +
// MISS regenerate when the incremental cache is usable, else one batched
// generate. Final result aggregation (sort, error join) is the caller's job, so
// this only appends to res.
func generateModule(g moduleGroup, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, useCache bool, cssMin, jsMin, jsonMin func(string) (string, error), cssMinify, jsMinify bool, classMerger *codegen.ClassMergerRef, out *Result, report *cacheReport) {
	root, modPath, dirs := g.root, g.modPath, g.dirs

	// Work against a LOCAL result so the per-module manifest guard can ask "was
	// THIS module clean?" without seeing sibling modules' errors. Merge into the
	// shared out on every exit path (including the early operational-error returns).
	var res Result
	moduleReport := moduleCacheReport{Root: root}
	defer func() {
		out.Written = append(out.Written, res.Written...)
		out.Errs = append(out.Errs, res.Errs...)
		out.Diags = append(out.Diags, res.Diags...)
		out.UpToDate += res.UpToDate
		out.Removed = append(out.Removed, res.Removed...)
		report.Modules = append(report.Modules, moduleReport)
	}()

	// Dir-scoped orphan sweep: runs for every dir in this module BEFORE any
	// generation happens below, not after. codegen.GenerateDirs (and Tier 0's
	// mustGen) type-check each dir's real on-disk files, and a gsx-owned orphan
	// .x.go left over from a just-deleted .gsx is NOT covered by the skeleton
	// overlay (which only replaces a .x.go that still has a matching .gsx) — it
	// would be read as ordinary Go source, and if it happens to be a stale
	// poison file its own undefined-identifier tripwire would fail the CURRENT
	// generate for the whole dir. That is the exact sticky-poison trap this
	// feature exists to close, so the sweep must complete before either
	// generation path (contrast with regenDir in watchsession.go, which sweeps
	// immediately before its own Module.Generate call for the same reason).
	for _, dir := range dirs {
		removed, rerr := removeOrphanXgo(dir)
		if rerr != nil {
			res.Errs = append(res.Errs, rerr)
			continue
		}
		res.Removed = append(res.Removed, removed...)
	}

	prepareStart := time.Now()
	cdir, enabled := cacheDir()
	if !useCache {
		enabled = false
		moduleReport.BypassReason = cacheReasonDisabledByOption
		moduleReport.BypassDetail = "cache disabled by option"
	}
	// modPath must be non-empty for the cache key to correctly classify
	// in-module deps. An empty modPath (malformed/missing module line in
	// go.mod) silently breaks dep invalidation, so treat it as disabled.
	if modPath == "" {
		enabled = false
		moduleReport.BypassReason = cacheReasonMissingModulePath
		moduleReport.BypassDetail = "module path is empty"
	}
	if !enabled && moduleReport.BypassReason == "" {
		moduleReport.BypassReason = cacheReasonDisabledByOption
		moduleReport.BypassDetail = "cache unavailable"
	}
	moduleReport.Enabled = enabled

	clsFingerprint := cls.Fingerprint()
	goContext := codegen.CaptureGoCommandContext(root)
	sourceManifest, manifestErr := sourceview.Build(sourceview.BuildOptions{ModuleRoot: root, ModulePath: modPath})
	if manifestErr != nil {
		res.Errs = append(res.Errs, fmt.Errorf("gen: build source manifest: %w", manifestErr))
		return
	}

	genOpts := codegen.Options{
		ModulePath:       modPath,
		GoCommandContext: goContext,
		SourceManifest:   sourceManifest,
		FilterPkgs:       filterPkgs,
		Aliases:          aliases,
		Renderers:        renderers,
		Classifier:       cls,
		CSSMin:           cssMin,
		JSMin:            jsMin,
		JSONMin:          jsonMin,
		CSSMinify:        cssMinify,
		JSMinify:         jsMinify,
		ClassMerger:      classMerger,
	}
	moduleReport.Durations.Prepare = time.Since(prepareStart)

	// No cache: one batched generate (Tier 0 path).
	if !enabled {
		for _, dir := range dirs {
			moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionUncacheable, Reason: moduleReport.BypassReason, Detail: moduleReport.BypassDetail})
		}
		generateStart := time.Now()
		moduleReport.SemanticGeneration = true
		writeAll(dirs, mustGen(root, dirs, genOpts, &res), &res)
		moduleReport.Durations.Generate = time.Since(generateStart)
		return
	}

	classifyStart := time.Now()
	bctx, contextErr := goContext.CacheFingerprint()
	if contextErr != nil {
		// Only a valid context whose source universe is deliberately outside the
		// persistent key can fall back to uncached generation. Capture, command,
		// decoding, and live-provenance failures are unsafe to continue: the
		// context may have changed and Module does not own enough state to reopen it.
		if errors.Is(contextErr, codegen.ErrUncacheableGoContext) {
			for _, dir := range dirs {
				moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionUncacheable, Reason: cacheReasonGoContextUncacheable, Detail: contextErr.Error()})
			}
			moduleReport.Durations.Classify = time.Since(classifyStart)
			// CacheFingerprint returns uncacheable classifications without its
			// normal final validation. Establish the uncached semantic commit
			// boundary here before Module consumes that context.
			if err := goContext.ValidateCurrent(); err != nil {
				res.Errs = append(res.Errs, fmt.Errorf("gen: validate uncacheable Go command context before generation: %w", err))
				return
			}
			generateStart := time.Now()
			moduleReport.SemanticGeneration = true
			writeAll(dirs, mustGen(root, dirs, genOpts, &res), &res)
			moduleReport.Durations.Generate = time.Since(generateStart)
			return
		}
		res.Errs = append(res.Errs, fmt.Errorf("gen: fingerprint Go command context: %w", contextErr))
		return
	}
	graphRoots := []string{"github.com/gsxhq/gsx"}
	graphRoots = append(graphRoots, configuredPackagePaths(filterPkgs, aliases, renderers, classMerger)...)
	graph, graphErr := loadGraphWithContext(goContext, sourceManifest, dedupSorted(graphRoots))
	var projection *sourceview.CacheProjection
	var projectionErr error
	if graphErr == nil {
		projection, projectionErr = sourceview.NewCacheProjection(sourceManifest, graph)
	}
	codegenID := codegenIdentity()
	keyConfig := cacheKeyConfig{
		buildContext:          bctx,
		codegenIdentity:       codegenID,
		additionalSourceRoots: []string{"github.com/gsxhq/gsx"},
		filterPackages:        filterPkgs,
		aliases:               aliases,
		renderers:             renderers,
		classifierFingerprint: clsFingerprint,
		cssMinify:             cssMinify,
		jsMinify:              jsMinify,
		classMerger:           classMerger,
	}

	keys := map[string]string{} // dir -> key (only when computable)
	hits := map[string]pkgOutput{}
	var miss []string
	for _, dir := range dirs {
		if graphErr != nil {
			moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionUncacheable, Reason: cacheReasonGraphQueryFailed, Detail: graphErr.Error()})
			miss = append(miss, dir) // graph failed → regenerate everything (safe)
			continue
		}
		if projectionErr != nil {
			moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionUncacheable, Reason: cacheReasonProjectionFailed, Detail: projectionErr.Error()})
			miss = append(miss, dir) // graph failed → regenerate everything (safe)
			continue
		}
		k, err := computeKey(dir, projection, keyConfig)
		if err != nil {
			moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionUncacheable, Reason: cacheReasonKeyFailed, Detail: err.Error()})
			miss = append(miss, dir) // uncertain → MISS
			continue
		}
		keys[dir] = k
		if cached, ok := storeGet(cdir, k); ok {
			hits[dir] = cached
			moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionHit, Reason: cacheReasonEntryHit})
			continue // HIT
		}
		moduleReport.Dirs = append(moduleReport.Dirs, cacheDirReport{Dir: dir, Decision: cacheDecisionMiss, Reason: cacheReasonEntryMissing})
		miss = append(miss, dir)
	}
	moduleReport.Durations.Classify = time.Since(classifyStart)

	// CacheFingerprint was computed before graph loading and key classification.
	// Revalidate at the semantic commit boundary immediately before any cached
	// bytes are consumed or any MISS is generated and stored. A changed
	// launcher, compiler, or vendor-selection state must fail closed without
	// publishing output under a stale provenance key.
	if err := goContext.ValidateCurrent(); err != nil {
		res.Errs = append(res.Errs, fmt.Errorf("gen: validate Go command context before cache commit: %w", err))
		return
	}

	// RESTORE phase: write every HIT's cached output to disk (hash-gated), BEFORE generating.
	restoreStart := time.Now()
	for _, dir := range dirs {
		out, ok := hits[dir]
		if !ok {
			continue
		}
		written, upToDate, werr := restore(dir, out)
		if werr != nil {
			res.Errs = append(res.Errs, werr)
			return
		}
		res.Written = append(res.Written, written...)
		res.UpToDate += upToDate
	}
	moduleReport.Durations.Restore = time.Since(restoreStart)

	// GENERATE phase: only the miss set, in ONE load.
	if len(miss) > 0 {
		generateStart := time.Now()
		moduleReport.SemanticGeneration = true
		genOut, err := codegen.GenerateDirs(root, miss, genOpts, nil)
		moduleReport.Durations.Generate = time.Since(generateStart)
		if err != nil {
			res.Errs = append(res.Errs, err)
			return
		}
		for _, dir := range miss {
			dr, ok := genOut[dir]
			if !ok {
				continue
			}
			// Collect structured diagnostics regardless of error state.
			res.Diags = append(res.Diags, dr.Diags...)
			po := writeDirOutcome(dir, dr, &res)
			if po == nil {
				continue // failed dir (poisoned) or I/O error — never cached
			}
			if k, ok := keys[dir]; ok {
				if err := storePut(cdir, k, po); err != nil {
					moduleReport.StoreFailures = append(moduleReport.StoreFailures, cacheDirReport{Dir: dir, Reason: cacheReasonStoreWriteFailed, Detail: err.Error()})
				}
			}
		}
	}
	// NOTE: no config-manifest write here. The resolved-config projection is
	// served on demand by `gsx info --json` (live re-resolve); nothing reads a
	// persisted manifest. Writing it on every generate forced a redundant
	// packages.Load (ResolveFilters type-checks the filter packages) per module —
	// the dominant cost of a fully-cached generate — for output no one consumed.
}

// anyErrorDiag reports whether any diagnostic has Error severity.
func anyErrorDiag(diags []diag.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
}

// restore writes a package's output to disk, skipping files whose bytes already
// match (hash-gated). Writes are temp+rename in the target dir: a poison file
// that lands truncated is a *parse* error that would confuse the LSP and
// skeleton scanner, so partial writes must be impossible.
// Returns the paths it actually wrote and the count of outputs that were
// already current (byte-identical, so skipped).
func restore(dir string, out pkgOutput) (written []string, upToDate int, err error) {
	for rel, data := range out {
		target := filepath.Join(dir, rel)
		if existing, rerr := os.ReadFile(target); rerr == nil && bytes.Equal(existing, data) {
			upToDate++ // already current — no write
			continue
		}
		if werr := writeFileAtomic(target, data); werr != nil {
			return written, upToDate, fmt.Errorf("%s: %w", target, werr)
		}
		written = append(written, target)
	}
	return written, upToDate, nil
}

// writeFileAtomic writes data to target via a same-dir temp file + rename, so
// readers never observe a partial file. Mode 0644 to match os.WriteFile use.
func writeFileAtomic(target string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".gsx-w-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// toPkgOutput converts codegen's gsxPath->bytes (absolute .gsx paths) into the
// store's relative-path (<base>.x.go) -> bytes form.
func toPkgOutput(files map[string][]byte) pkgOutput {
	po := pkgOutput{}
	for gsxPath, src := range files {
		base := filepath.Base(gsxPath)
		base = base[:len(base)-len(".gsx")]
		po[base+".x.go"] = src
	}
	return po
}

// mustGen / writeAll: the no-cache fallback (Tier 0 path) reused by generateCached.
func mustGen(root string, dirs []string, opts codegen.Options, res *Result) map[string]codegen.DirResult {
	out, err := codegen.GenerateDirs(root, dirs, opts, nil)
	if err != nil {
		res.Errs = append(res.Errs, err)
		return nil
	}
	return out
}

// writeAll appends one generate's outcome (diagnostics, operational errors,
// written paths) to res. It is append-only: final aggregation (sort, error
// join) is the caller's job, since res may span several modules.
func writeAll(dirs []string, out map[string]codegen.DirResult, res *Result) {
	if out == nil {
		return
	}
	for _, dir := range dirs {
		dr, ok := out[dir]
		if !ok {
			continue
		}
		// Collect structured diagnostics regardless of error state.
		res.Diags = append(res.Diags, dr.Diags...)
		writeDirOutcome(dir, dr, res)
	}
}

// writeDirOutcome routes one dir's generate outcome to disk: fresh output on
// success, poison files for the blamed .gsx on error-severity diagnostics.
// Poison also flows through the hash-gated restore, so repeated failures are
// write-free and the next success overwrites by ordinary byte inequality.
// Returns the pkgOutput that was written (for the success-path cache put),
// or nil when the dir failed.
//
// The dir-scoped orphan sweep for dir already ran in generateModule BEFORE
// generation (it must precede the type-check that reads dir's real on-disk
// files — see the comment there); this function does not repeat it.
func writeDirOutcome(dir string, dr codegen.DirResult, res *Result) pkgOutput {
	if anyErrorDiag(dr.Diags) {
		po, perr := poisonPkgOutput(dir, dr.Diags)
		if perr != nil {
			res.Errs = append(res.Errs, perr)
			return nil
		}
		written, upToDate, werr := restore(dir, po)
		if werr != nil {
			res.Errs = append(res.Errs, werr)
			return nil
		}
		res.Written = append(res.Written, written...)
		res.UpToDate += upToDate
		return nil
	}
	po := toPkgOutput(dr.Files)
	written, upToDate, werr := restore(dir, po)
	if werr != nil {
		res.Errs = append(res.Errs, werr) // genuine I/O error
		return nil
	}
	res.Written = append(res.Written, written...)
	res.UpToDate += upToDate
	return po
}
