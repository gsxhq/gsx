package gen

import (
	"path/filepath"
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
	Diags   []diag.Diagnostic
	OK      bool
	Err     error
	DurMs   int64
}

func (r cycleResult) durationMs() int64 { return r.DurMs }

// watchSession holds the live state for a running generate-on-change session.
// Discovery may span several independent modules, so the session keeps one warm
// resolver per module root: each dir's regenerate must run against the resolver
// anchored at ITS module, or cross-package refs in a sibling module read as "not
// loaded". root is the primary module root, used only for the startup banner.
type watchSession struct {
	cfg       watchConfig
	root      string                     // primary module root (startup banner only)
	roots     []string                   // every module root the session spans
	resolvers map[string]*CachedResolver // module root -> warm resolver
}

// newWatchSession runs the initial cold generate (full cross-package
// correctness, creates every .x.go), then builds a warm resolver for every
// module spanned. Returns the session plus startup results so the caller can
// emit them.
func newWatchSession(cfg watchConfig) (*watchSession, []cycleResult, error) {
	dirs, err := discoverDirs(cfg.paths)
	if err != nil {
		return nil, nil, err
	}
	groups, _ := groupByModule(dirs)
	if len(groups) == 0 {
		// No enclosing module for any discovered dir — surface the lookup error.
		_, _, mErr := moduleRoot(dirs[0])
		return nil, nil, mErr
	}
	// Cold generate: writes all .x.go files to disk across every module.
	res, gerr := generateCached(cfg.paths, cfg.filterPkgs, cfg.aliases, cfg.cls, cfg.fm, true, cfg.cssMin, cfg.jsMin, cfg.cssMinify, cfg.jsMinify)
	// generateCached folds error-severity diagnostics into its returned error, so
	// gerr==nil already implies !anyErrorDiag(res.Diags). The two-term form is
	// kept symmetric with the warm path (regen) for clarity and robustness.
	startup := []cycleResult{{
		Dir:     groups[0].root,
		Written: res.Written,
		Diags:   res.Diags,
		OK:      gerr == nil && !anyErrorDiag(res.Diags),
		Err:     opErr(res, gerr),
	}}

	s := &watchSession{cfg: cfg, root: groups[0].root, resolvers: map[string]*CachedResolver{}}
	for _, g := range groups {
		s.roots = append(s.roots, g.root)
	}
	// Build the warm resolver for every module (needs .x.go on disk).
	if err := s.rebuild(); err != nil {
		return nil, startup, err
	}
	return s, startup, nil
}

// rebuild (re)constructs the warm CachedResolver for every module the session
// spans. Call after a dep-surface change (new import, new .x.go added, etc.).
func (s *watchSession) rebuild() error {
	resolvers := make(map[string]*CachedResolver, len(s.roots))
	for _, root := range s.roots {
		r, err := newModuleResolver(root, s.cfg.filterPkgs, s.cfg.aliases)
		if err != nil {
			return err
		}
		resolvers[root] = r
	}
	s.resolvers = resolvers
	return nil
}

// regen warm-regenerates one dir using its module's cached resolver. On a
// cached-importer miss (a newly added import the resolver has never loaded), it
// rebuilds once and retries. A dir in a module the session has not seen yet
// (e.g. a freshly created sub-module) is registered and its resolver built.
func (s *watchSession) regen(dir string) cycleResult {
	start := time.Now()
	root, _, rerr := moduleRoot(dir)
	if rerr != nil {
		return cycleResult{Dir: dir, Err: rerr, DurMs: time.Since(start).Milliseconds()}
	}
	if _, ok := s.resolvers[root]; !ok {
		s.roots = append(s.roots, root)
		if rebuildErr := s.rebuild(); rebuildErr != nil {
			return cycleResult{Dir: dir, Err: rebuildErr, DurMs: time.Since(start).Milliseconds()}
		}
	}
	res, err := s.resolvers[root].Generate(dir, nil)
	if err != nil && isCachedImporterMiss(err, res) {
		if rebuildErr := s.rebuild(); rebuildErr == nil {
			res, err = s.resolvers[root].Generate(dir, nil)
		}
	}
	written, werr := writeFiles(dir, res.Files)
	var finalErr error
	switch {
	case err != nil && !anyErrorDiag(res.Diags):
		finalErr = err
	case werr != nil:
		finalErr = werr
	}
	return cycleResult{
		Dir:     dir,
		Written: written,
		Diags:   res.Diags,
		OK:      err == nil && !anyErrorDiag(res.Diags) && werr == nil,
		Err:     finalErr,
		DurMs:   time.Since(start).Milliseconds(),
	}
}

// writeFiles persists a resolver Result's Files (keyed by absolute .x.go
// paths) to dir via hash-gated restore, returning the paths actually written
// and any I/O error (e.g. disk full, permission denied).
func writeFiles(dir string, files map[string][]byte) ([]string, error) {
	po := pkgOutput{}
	for absXGo, b := range files {
		po[filepath.Base(absXGo)] = b
	}
	written, _, err := restore(dir, po)
	return written, err
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
