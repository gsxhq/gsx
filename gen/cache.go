package gen

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
)

func generateCached(paths, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, fm codegen.FieldMatcher, useCache bool, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool) (Result, error) {
	var res Result
	dirs, err := discoverDirs(paths)
	if err != nil {
		return res, err
	}
	if len(dirs) == 0 {
		return res, nil
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
		generateModule(g, filterPkgs, aliases, cls, fm, useCache, cssMin, jsMin, cssMinify, jsMinify, &res)
	}

	sort.Strings(res.Written)
	if len(res.Errs) > 0 {
		return res, errors.Join(res.Errs...)
	}
	// Return a non-nil error when there are error-severity diagnostics, so that
	// callers using the Go API (Generate, generate) can still detect failure
	// without inspecting res.Diags directly.
	if anyErrorDiag(res.Diags) {
		return res, errors.New("codegen: diagnostics reported")
	}
	return res, nil
}

// generateModule generates every .gsx package dir in one module group g, anchored
// at g.root, appending its outcome (written paths, diagnostics, operational
// errors) to res. It mirrors the single-module pipeline: cache HIT restore +
// MISS regenerate when the incremental cache is usable, else one batched
// generate. Final result aggregation (sort, error join) is the caller's job, so
// this only appends to res.
func generateModule(g moduleGroup, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, fm codegen.FieldMatcher, useCache bool, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool, out *Result) {
	root, modPath, dirs := g.root, g.modPath, g.dirs

	// Work against a LOCAL result so the per-module manifest guard can ask "was
	// THIS module clean?" without seeing sibling modules' errors. Merge into the
	// shared out on every exit path (including the early operational-error returns).
	var res Result
	defer func() {
		out.Written = append(out.Written, res.Written...)
		out.Errs = append(out.Errs, res.Errs...)
		out.Diags = append(out.Diags, res.Diags...)
		out.UpToDate += res.UpToDate
	}()

	cdir, enabled := cacheDir()
	if !useCache {
		enabled = false
	}
	// modPath must be non-empty for the cache key to correctly classify
	// in-module deps. An empty modPath (malformed/missing module line in
	// go.mod) silently breaks dep invalidation, so treat it as disabled.
	if modPath == "" {
		enabled = false
	}

	clsFingerprint := cls.Fingerprint()

	genOpts := codegen.Options{
		ModulePath:   modPath,
		FilterPkgs:   filterPkgs,
		Aliases:      aliases,
		Classifier:   cls,
		FieldMatcher: fm,
		CSSMin:       cssMin,
		JSMin:        jsMin,
		CSSMinify:    cssMinify,
		JSMinify:     jsMinify,
	}

	// No cache: one batched generate (Tier 0 path).
	if !enabled {
		writeAll(dirs, mustGen(root, dirs, genOpts, &res), &res)
		return
	}

	graph, gerr := loadGraph(root)
	bctx := buildContext(root)
	codegenID := codegenIdentity()
	goModH := fileHashOrEmpty(filepath.Join(root, "go.mod"))
	goSumH := fileHashOrEmpty(filepath.Join(root, "go.sum"))

	keys := map[string]string{} // dir -> key (only when computable)
	var miss []string
	for _, dir := range dirs {
		if gerr != nil {
			miss = append(miss, dir) // graph failed → regenerate everything (safe)
			continue
		}
		k, err := computeKey(dir, graph, modPath, goModH, goSumH, bctx, codegenID, filterPkgs, aliases, clsFingerprint, fm != nil, cssMinify, jsMinify)
		if err != nil {
			miss = append(miss, dir) // uncertain → MISS
			continue
		}
		keys[dir] = k
		if _, ok := storeGet(cdir, k); ok {
			continue // HIT
		}
		miss = append(miss, dir)
	}

	// RESTORE phase: write every HIT's cached output to disk (hash-gated), BEFORE generating.
	for _, dir := range dirs {
		k, ok := keys[dir]
		if !ok {
			continue
		}
		if slices.Contains(miss, dir) {
			continue
		}
		out, ok := storeGet(cdir, k)
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

	// GENERATE phase: only the miss set, in ONE load.
	if len(miss) > 0 {
		genOut, err := codegen.GenerateDirs(root, miss, genOpts, nil)
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
			if anyErrorDiag(dr.Diags) {
				// Error-severity diagnostics: diagnostics already in res.Diags above;
				// skip writing partial/invalid output to disk.
				continue
			}
			po := toPkgOutput(dr.Files)
			written, upToDate, werr := restore(dir, po) // write generated bytes (hash-gated)
			if werr != nil {
				res.Errs = append(res.Errs, werr) // genuine I/O error
				continue
			}
			res.Written = append(res.Written, written...)
			res.UpToDate += upToDate
			if k, ok := keys[dir]; ok {
				_ = storePut(cdir, k, po) // best-effort cache write
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
// match (hash-gated). Returns the paths it actually wrote and the count of
// outputs that were already current (byte-identical, so skipped).
func restore(dir string, out pkgOutput) (written []string, upToDate int, err error) {
	for rel, data := range out {
		target := filepath.Join(dir, rel)
		if existing, rerr := os.ReadFile(target); rerr == nil && bytes.Equal(existing, data) {
			upToDate++ // already current — no write
			continue
		}
		if werr := os.WriteFile(target, data, 0o644); werr != nil {
			return written, upToDate, fmt.Errorf("%s: %w", target, werr)
		}
		written = append(written, target)
	}
	return written, upToDate, nil
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
		if anyErrorDiag(dr.Diags) {
			// Error-severity diagnostics: skip writing partial/invalid output.
			continue
		}
		written, upToDate, werr := restore(dir, toPkgOutput(dr.Files))
		if werr != nil {
			res.Errs = append(res.Errs, werr) // genuine I/O error
			continue
		}
		res.Written = append(res.Written, written...)
		res.UpToDate += upToDate
	}
}
