package gen

import (
	"path/filepath"
	"strings"

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
	Diags   []diag.Diagnostic
	OK      bool
	Err     error
}

// watchSession holds the live state for a running generate-on-change session:
// the warm module-wide resolver and the original configuration.
type watchSession struct {
	cfg      watchConfig
	root     string
	resolver *CachedResolver
}

// newWatchSession runs the initial cold generate (full cross-package
// correctness, creates every .x.go), then builds the warm resolver over the
// now-complete module. Returns the session plus per-dir startup results so the
// caller can emit them.
func newWatchSession(cfg watchConfig) (*watchSession, []cycleResult, error) {
	dirs, err := discoverDirs(cfg.paths)
	if err != nil {
		return nil, nil, err
	}
	root, _, err := moduleRoot(dirs[0])
	if err != nil {
		return nil, nil, err
	}
	// Cold generate: writes all .x.go files to disk.
	res, gerr := generateCached(cfg.paths, cfg.filterPkgs, cfg.aliases, cfg.cls, cfg.predLabel, cfg.fm, true, cfg.cssMin, cfg.jsMin)
	startup := []cycleResult{{
		Dir:     root,
		Written: res.Written,
		Diags:   res.Diags,
		OK:      gerr == nil,
		Err:     opErr(res, gerr),
	}}

	s := &watchSession{cfg: cfg, root: root}
	// Build the warm resolver over the whole module (needs .x.go on disk).
	if err := s.rebuild(); err != nil {
		return nil, startup, err
	}
	return s, startup, nil
}

// rebuild (re)constructs the module-wide CachedResolver. Call after a dep-
// surface change (new import, new .x.go added, etc.).
func (s *watchSession) rebuild() error {
	r, err := newModuleResolver(s.root, s.cfg.filterPkgs, s.cfg.aliases)
	if err != nil {
		return err
	}
	s.resolver = r
	return nil
}

// regen warm-regenerates one dir using the cached resolver. On a
// cached-importer miss (a newly added import the resolver has never loaded),
// it rebuilds once and retries.
func (s *watchSession) regen(dir string) cycleResult {
	res, err := s.resolver.Generate(dir, nil)
	if err != nil && isCachedImporterMiss(err, res) {
		if rebuildErr := s.rebuild(); rebuildErr == nil {
			res, err = s.resolver.Generate(dir, nil)
		}
	}
	written := writeFiles(dir, res.Files)
	return cycleResult{
		Dir:     dir,
		Written: written,
		Diags:   res.Diags,
		OK:      err == nil && !anyErrorDiag(res.Diags),
		Err:     opErr(res, err),
	}
}

// writeFiles persists a resolver Result's Files (keyed by absolute .x.go
// paths) to dir via hash-gated restore, returning the paths actually written.
func writeFiles(dir string, files map[string][]byte) []string {
	po := pkgOutput{}
	for absXGo, b := range files {
		po[filepath.Base(absXGo)] = b
	}
	written, _ := restore(dir, po)
	return written
}

// opErr extracts a genuine operational error from a Result/err pair.
// Error-severity diagnostics are reported via Diags, not here.
func opErr(res Result, err error) error {
	if err != nil && !anyErrorDiag(res.Diags) {
		return err
	}
	return nil
}

// isCachedImporterMiss reports whether err (or a diagnostic message) indicates
// a cached-importer miss — a newly added import the resolver never loaded.
func isCachedImporterMiss(err error, res Result) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "cached importer") {
		return true
	}
	for _, d := range res.Diags {
		if strings.Contains(d.Message, "cached importer") {
			return true
		}
	}
	return false
}

// newModuleResolver builds a warm CachedResolver whose importer covers the whole
// module: all in-module packages (so cross-package gsx component refs resolve)
// plus their transitive dependencies. filterPkgs/aliases thread the user's
// pipeline filters, exactly as the cold path does. The one-time packages.Load
// happens here; resolver.Generate calls afterwards run fully in-process.
func newModuleResolver(moduleDir string, filterPkgs []string, aliases []codegen.FilterAlias) (*CachedResolver, error) {
	// "./..." expands to every package in the module; packages.Load (NeedDeps)
	// pulls their transitive deps into the importer map. This is what lets a
	// later resolver.Generate of one package see sibling packages' types.
	allow := []string{"./..."}
	inner, err := codegen.NewCachedResolver(moduleDir, append([]string{codegen.StdImportPath}, filterPkgs...), aliases, allow)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{inner: inner}, nil
}
