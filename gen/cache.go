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
)

func generateCached(paths, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, useCache bool, cssMin, jsMin, jsonMin func(string) (string, error), cssMinify, jsMinify, verbatimTags bool, classMerger *codegen.ClassMergerRef) (Result, error) {
	result, _, err := generateCachedWithReport(paths, filterPkgs, aliases, renderers, cls, useCache, cssMin, jsMin, jsonMin, cssMinify, jsMinify, verbatimTags, classMerger)
	return result, err
}

func generateCachedWithReport(paths, filterPkgs []string, aliases []codegen.FilterAlias, renderers []codegen.RendererAlias, cls *attrclass.Classifier, useCache bool, cssMin, jsMin, jsonMin func(string) (string, error), cssMinify, jsMinify, verbatimTags bool, classMerger *codegen.ClassMergerRef) (Result, cacheReport, error) {
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
	config := moduleGenerateConfig{
		filterPkgs:   filterPkgs,
		aliases:      aliases,
		renderers:    renderers,
		classifier:   cls,
		useCache:     useCache,
		cssMin:       cssMin,
		jsMin:        jsMin,
		jsonMin:      jsonMin,
		cssMinify:    cssMinify,
		jsMinify:     jsMinify,
		verbatimTags: verbatimTags,
		classMerger:  classMerger,
	}
	for _, g := range groups {
		report.Modules = append(report.Modules, generateModule(g, config, &res))
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

// generateModule generates one module group through explicit prepare,
// classification, and commit phases. It returns its report even on early error
// paths; final cross-module result aggregation remains the caller's job.
func generateModule(g moduleGroup, config moduleGenerateConfig, out *Result) (moduleReport moduleCacheReport) {
	var result Result
	defer func() {
		out.Written = append(out.Written, result.Written...)
		out.Errs = append(out.Errs, result.Errs...)
		out.Diags = append(out.Diags, result.Diags...)
		out.UpToDate += result.UpToDate
		out.Removed = append(out.Removed, result.Removed...)
	}()

	// A stale owned output can poison semantic loading, so orphan removal remains
	// before preparation and either generation path.
	for _, dir := range g.dirs {
		removed, err := removeOrphanXgo(dir)
		if err != nil {
			result.Errs = append(result.Errs, err)
			continue
		}
		result.Removed = append(result.Removed, removed...)
	}

	var err error
	var prep cachePreparation
	prep, moduleReport, err = prepareCache(g, config)
	if err != nil {
		result.Errs = append(result.Errs, err)
		return moduleReport
	}
	classifyStart := time.Now()
	classification := classifyCache(prep)
	moduleReport.Durations.Classify = time.Since(classifyStart)
	moduleReport.recordClassification(classification)
	commitCache(prep, classification, &moduleReport, &result)
	// NOTE: no config-manifest write here. The resolved-config projection is
	// served on demand by `gsx info --json` (live re-resolve); nothing reads a
	// persisted manifest. Writing it on every generate forced a redundant
	// packages.Load (ResolveFilters type-checks the filter packages) per module —
	// the dominant cost of a fully-cached generate — for output no one consumed.
	return moduleReport
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
