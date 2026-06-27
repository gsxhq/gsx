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
// Module per module root: each dir's regeneration must run against the Module
// anchored at ITS module, or cross-package refs in a sibling module read as "not
// loaded". root is the primary module root, used only for the startup banner.
type watchSession struct {
	cfg     watchConfig
	root    string                     // primary module root (startup banner only)
	roots   []string                   // every module root the session spans
	modules map[string]*codegen.Module // module root -> warm Module
}

// openModule constructs a fresh *codegen.Module for the given module root,
// threading all watchConfig options (filters, aliases, classifier, field matcher,
// minifiers) into codegen.Open. It does not perform any analysis; analysis is
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
		FieldMatcher: s.cfg.fm,
		Classifier:   s.cfg.cls,
		CSSMin:       s.cfg.cssMin,
		JSMin:        s.cfg.jsMin,
		CSSMinify:    s.cfg.cssMinify,
		JSMinify:     s.cfg.jsMinify,
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
	}
	return s.modules[root], nil
}

// newWatchSession opens a warm Module for every discovered module root, then
// runs an initial regenDir for every discovered dir. The per-dir generate writes
// all .x.go files AND fully populates each Module's import graph (needed for
// Task 4's reverse-closure). Returns the session plus per-dir startup results so
// the caller can emit them.
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

	s := &watchSession{cfg: cfg, root: groups[0].root, modules: map[string]*codegen.Module{}}
	for _, g := range groups {
		s.roots = append(s.roots, g.root)
		m, err := s.openModule(g.root)
		if err != nil {
			return nil, nil, err
		}
		s.modules[g.root] = m
	}

	var startup []cycleResult
	for _, dir := range dirs {
		startup = append(startup, s.regenDir(dir))
	}
	return s, startup, nil
}

// reopen (re)opens a fresh Module for every module root the session spans, then
// re-runs regenDir for every discovered dir so the import graphs are fully
// repopulated. Call after a dep-surface change (new import, .go edit, go.mod
// change, etc.). This replaces the old rebuild() method.
func (s *watchSession) reopen() error {
	for _, root := range s.roots {
		m, err := s.openModule(root)
		if err != nil {
			return err
		}
		s.modules[root] = m
	}
	// Repopulate the graph for every dir by regenerating.
	dirs, err := discoverDirs(s.cfg.paths)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		s.regenDir(dir)
	}
	return nil
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
	out, diags, gerr := m.Generate(dir)
	files := make(map[string][]byte, len(out))
	for gsxPath, b := range out {
		files[strings.TrimSuffix(gsxPath, ".gsx")+".x.go"] = b
	}
	written, werr := writeFiles(dir, files)
	var finalErr error
	switch {
	case gerr != nil && !anyErrorDiag(diags):
		finalErr = gerr
	case werr != nil:
		finalErr = werr
	}
	return cycleResult{
		Dir:     dir,
		Written: written,
		Diags:   diags,
		OK:      gerr == nil && !anyErrorDiag(diags) && werr == nil,
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
