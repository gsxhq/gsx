package codegen

import (
	"bytes"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
)

// Options configures a Module. ModuleRoot is the absolute module root (dir
// containing go.mod); ModulePath is its declared module path (from go.mod).
type Options struct {
	ModuleRoot   string
	ModulePath   string
	FilterPkgs   []string
	Aliases      []FilterAlias
	FieldMatcher FieldMatcher
	Classifier   *attrclass.Classifier
}

// Module is a warm, in-process analysis graph for one module root. It is the
// single analysis core consumed by generate, watch, the LSP, fmt, and the
// playground.
//
// Concurrency contract (Phase 1): analysisMu serializes the three top-level
// analysis entry points — Package, Generate, and typesPackage — so that only
// one analysis runs on a given Module at a time. mu guards the overrides, ext,
// and pkgTypes map fields and is acquired independently of analysisMu (it is
// also acquired inside externalImporter and typesPackageWith, which are called
// from within a held analysisMu). The internal recursive path
// (typesPackageWith → analyze → moduleImporter.Import → typesPackageWith) does
// NOT acquire analysisMu — those functions run within a held analysisMu and
// re-acquiring would deadlock. True fine-grained concurrent analysis (multiple
// roots in parallel or partial invalidation) is deferred to Phase 2.
//
// Cache invalidation: SetOverride compares incoming bytes against the current
// source (override-or-disk) and marks filepath.Dir(absPath) dirty when the
// content actually changed. Package and Generate call applyDirty at the start of
// each run: it drops the reverse-reflexive-transitive closure of dirty dirs from
// pkgTypes (the changed dir plus every project gsx package that transitively
// imports it), then clears dirty. This means only the affected subgraph is
// re-type-checked; unchanged packages and the warm ext importer stay cached.
// Invalidate is the public entry point for callers that need to drop a dir without
// calling Package/Generate.
//
// Known gap (no didChangeWatchedFiles hook yet): invalidation is driven by
// SetOverride content diffs, so a disk-only change to a package that has no open
// override (e.g. an external tool rewriting a .gsx file) is not auto-detected — a
// warm pkgTypes entry would stay stale until that file gets an override or a caller
// invokes Invalidate. A future file-watch hook closes this.
//
// FileSet: the Module uses ONE *token.FileSet (m.fset) for its whole lifetime,
// covering BOTH the external packages.Load AND every project analyze() call. So
// every type-object position — package A, sibling B, external dep — resolves
// unambiguously against the single fset, exactly like the batch path's single
// packages.Load fset. This is what makes cross-package go-to-def (the expression
// path) resolve a sibling's obj.Pos() to the sibling's source rather than a
// random spot in the importing package.
//
// Growth bounding: because the fset is Module-lifetime, re-analyzing a project
// package each edit (applyDirty clears pkgTypes → re-parse into the same fset)
// accumulates fset entries (token.FileSet is append-only). maybeRebuildFset (called
// at the start of Package/Generate) bounds this: when project re-parse growth
// (fset.Base() - fsetBaseline) exceeds fsetRebuildBytes, rebuildFset replaces the
// fset AND drops ext+pkgTypes+pkgResults TOGETHER, so nothing live holds positions
// into the discarded fset. The import graph, dirty set, and overrides survive
// (path/content-based). Do NOT rebuild the fset per edit, and never reset the fset
// while keeping ext, pkgTypes, or pkgResults: that would orphan their positions.
type Module struct {
	opts             Options
	overrides        map[string][]byte          // abs .gsx path -> in-memory source
	ext              types.Importer             // lazily built external importer (stdlib + third-party)
	fset             *token.FileSet             // module-wide shared FileSet (see "FileSet" / "Growth" notes above)
	pkgTypes         map[string]*types.Package  // abs dir -> checked *types.Package cache
	pkgResults       map[string]*PackageResult  // abs dir -> cached full analysis result (Package path only)
	imports          map[string][]string        // dir -> its project-gsx dependency dirs (forward edges)
	importedBy       map[string]map[string]bool // dep dir -> set of importer dirs (reverse edges)
	dirty            map[string]bool            // dirs with a pending content change (consumed by applyDirty)
	fsetBaseline     int                        // m.fset.Base() captured after the last packages.Load (growth measured since here)
	fsetRebuildBytes int                        // rebuild fset when fset.Base()-fsetBaseline exceeds this; 0 disables
	rebuildCount     int                        // count of fset rebuilds performed (observability; exposed via rebuilds())
	mu               sync.Mutex                 // guards overrides, ext, pkgTypes, pkgResults, imports, importedBy, dirty
	analysisMu       sync.Mutex                 // serializes Package/Generate/typesPackage (see concurrency contract)
}

// defaultFsetRebuildBytes bounds the module-lifetime FileSet's project re-parse
// growth: when fset.Base() climbs this many bytes past the post-load baseline, the
// Module rebuilds fset+ext+pkgTypes+pkgResults. 256 MiB is generous enough that a rebuild is
// rare (tens of full re-analyses of a large package) yet caps leaked token.File
// memory. Internal perf knob (not gsx.toml / computeKey); overridable via
// GSX_FSET_REBUILD_BYTES (0 disables; like GSXCACHE).
const defaultFsetRebuildBytes = 256 << 20

// fsetRebuildBytesFromEnv returns the GSX_FSET_REBUILD_BYTES override if set to a
// valid non-negative integer (0 disables rebuilding), else defaultFsetRebuildBytes.
func fsetRebuildBytesFromEnv() int {
	if v, ok := os.LookupEnv("GSX_FSET_REBUILD_BYTES"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultFsetRebuildBytes
}

// Open constructs a Module. It does not load anything yet; analysis is lazy.
func Open(opts Options) (*Module, error) {
	cls := opts.Classifier
	if cls == nil {
		cls = attrclass.Builtin()
		opts.Classifier = cls
	}
	return &Module{
		opts:             opts,
		overrides:        map[string][]byte{},
		fset:             token.NewFileSet(),
		pkgResults:       map[string]*PackageResult{},
		imports:          map[string][]string{},
		importedBy:       map[string]map[string]bool{},
		dirty:            map[string]bool{},
		fsetRebuildBytes: fsetRebuildBytesFromEnv(),
	}, nil
}

// SetOverride records in-memory source for a .gsx path (an unsaved editor buffer
// or playground source), shadowing disk content. It marks filepath.Dir(absPath)
// dirty when src differs from the current source (override-or-disk); identical
// bytes mark nothing dirty. Invalidation is applied lazily by applyDirty at the
// next Package/Generate call.
func (m *Module) SetOverride(absPath string, src []byte) {
	base, haveBase := m.currentSource(absPath)
	// A real change: if a base exists, it must differ from src. If no base exists
	// (file not on disk, no prior override), only non-empty src counts as new content.
	changed := haveBase && !bytes.Equal(base, src) || !haveBase && len(src) > 0
	m.mu.Lock()
	if changed {
		if m.dirty == nil {
			m.dirty = map[string]bool{}
		}
		m.dirty[filepath.Dir(absPath)] = true
	}
	m.overrides[absPath] = src
	m.mu.Unlock()
}

// currentSource returns the bytes currently backing absPath (override if
// present, else disk) and whether any source was found. Used by SetOverride to
// detect a real content change. It takes m.mu only briefly to read the override
// map and reads disk outside the lock.
func (m *Module) currentSource(absPath string) ([]byte, bool) {
	m.mu.Lock()
	ov, ok := m.overrides[absPath]
	m.mu.Unlock()
	if ok {
		return ov, true
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, false
	}
	return b, true
}

// source returns the bytes for absPath: override first, else disk.
func (m *Module) source(absPath string) ([]byte, bool) {
	return m.currentSource(absPath)
}

// dirtyDirs returns the sorted pending-dirty dirs (test hook; does not clear).
func (m *Module) dirtyDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.dirty))
	for d := range m.dirty {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// externalImporter lazily loads non-project dependency types once (stdlib,
// third-party, .go-only packages) and caches them. Project gsx packages never
// reach it (moduleImporter routes those to typesPackage).
//
// Transitive .x.go boundary (Phase 0, known gap): the load includes "./..." with
// NeedDeps, so a non-gsx project package that itself imports a gsx package will
// carry that gsx package's on-disk .x.go types. A focused gsx package that
// imports such a Go-only intermediary therefore transitively resolves sibling gsx
// symbols from disk .x.go rather than from skeletons. This narrow
// (gsx → Go-only → gsx) case is unexercised by the corpus and has no consumer
// yet; closing it (making all gsx-reachable types come from skeletons) is
// deferred to Phase 1/2.
func (m *Module) externalImporter() (types.Importer, error) {
	m.mu.Lock()
	if m.ext != nil {
		defer m.mu.Unlock()
		return m.ext, nil
	}
	m.mu.Unlock()
	// Use the Module-wide shared FileSet for packages.Load so that every imported
	// dependency's type-object positions live in the SAME fset as the project
	// packages analyze() type-checks. One fset for the whole Module means an
	// object from any package — project A, sibling B, or external dep — resolves
	// unambiguously via m.fset.Position(obj.Pos()).
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Fset: m.fset,
		Dir:  m.opts.ModuleRoot,
	}
	// Always load the gsx runtime ("github.com/gsxhq/gsx") so that skeleton
	// type-checking can resolve gsx.Node / gsx.Attrs / etc. The skeleton file
	// every buildSkeleton emits always begins with
	//   import _gsxrt "github.com/gsxhq/gsx"
	// so the importer must carry that package. This mirrors newCachedResolver
	// (resolver.go) which lists "github.com/gsxhq/gsx" first for the same reason.
	loadPaths := append([]string{"github.com/gsxhq/gsx", stdImportPath}, m.opts.FilterPkgs...)
	loadPaths = append(loadPaths, "./...")
	pkgs, err := packages.Load(cfg, loadPaths...)
	if err != nil {
		return nil, err
	}
	mp := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			mp[p.PkgPath] = p.Types
		}
	})
	m.mu.Lock()
	m.ext = mapImporter(mp)
	m.fsetBaseline = m.fset.Base()
	m.mu.Unlock()
	return m.ext, nil
}

// maybeRebuildFset rebuilds the FileSet (and ext/pkgTypes/pkgResults) when project re-parse
// growth since the last load exceeds fsetRebuildBytes. A zero threshold disables it.
// Called at the start of Package/Generate (under analysisMu), before applyDirty.
func (m *Module) maybeRebuildFset() {
	m.mu.Lock()
	over := m.fsetRebuildBytes > 0 && m.fset.Base()-m.fsetBaseline > m.fsetRebuildBytes
	m.mu.Unlock()
	if over {
		m.rebuildFset()
	}
}

// rebuildFset discards the grown FileSet and the caches that hold positions into it
// — ext, pkgTypes, and pkgResults — together, so nothing live references the old fset (no orphaned
// positions). The next externalImporter reloads ext into the fresh fset and recaptures
// fsetBaseline; analyze re-parses into it. The import graph, dirty set, and overrides
// survive (path/content-based), so reverse-dependency invalidation keeps working.
// Assumes analysisMu held by the caller (Package/Generate); takes m.mu for the writes.
func (m *Module) rebuildFset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fset = token.NewFileSet()
	m.ext = nil
	m.pkgTypes = map[string]*types.Package{}
	m.pkgResults = map[string]*PackageResult{}
	m.fsetBaseline = 0
	m.rebuildCount++
}

// rebuilds returns the number of fset rebuilds performed (test hook).
func (m *Module) rebuilds() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rebuildCount
}

// Package returns the full retained analysis for a single gsx package dir,
// equivalent to the go-list batch path's per-package result but without codegen
// (Files stays empty; Generate fills it). It populates the FileSets, *types.Info,
// *types.Package, ExprMap, GSXFiles, and the cross/nav indexes used by the LSP.
func (m *Module) Package(dir string) (*PackageResult, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	m.mu.Lock()
	cached := m.pkgResults[dir]
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	a, err := m.analyze(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
	if err != nil {
		return nil, err
	}
	res := &PackageResult{
		Files:    map[string][]byte{},
		GSXFset:  a.gsxFset,
		Fset:     a.skelFset,
		Info:     a.info,
		Types:    a.pkg,
		GSXFiles: a.gsxFiles,
		ExprMap:  a.exprMap,
		CtrlMap:  a.ctrlMap,
	}
	// Run emit for side-effect diagnostics only (unknown filter, attr-error, etc.
	// that are added during generateFile). We discard the generated bytes; only the
	// bag side-effects matter for LSP diagnostics. This mirrors Generate's emit
	// loop but retains the bag's script/parse diagnostics from analyze.
	//
	// Safe despite emit's in-place AST mutation: analyze re-parses a fresh gsx AST
	// on every call, so there is no previously-mutated tree that could be corrupted
	// by a concurrent or repeated generateFile pass on the same nodes.
	for _, f := range a.gsxFiles {
		generateFile(f, a.resolved, a.table, a.propFields, a.nodeProps, a.byo,
			a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, a.bag, nil, nil, true, true)
	}
	res.Diags = a.bag.Sorted()
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFset, a.skelFset, a.info, a.pkg)
	res.UnusedImports = detectUnusedImportsFromErrs(a.typeErrs, a.importSpecs, a.gsxFset)
	m.mu.Lock()
	m.pkgResults[dir] = res
	m.mu.Unlock()
	return res, nil
}

// Generate runs analysis on dir and emits a .x.go for every .gsx file in the
// package. It returns the generated bytes keyed by the gsx file's absolute path,
// any diagnostics (including script-resolution errors from analyze), and a hard
// error only when analysis itself fails (parse error, load error, etc.).
// Emit errors (per-component) are soft: they surface as diagnostics in the
// returned slice and the file is omitted from out.
//
// Phase-0 equivalence note: byte-for-byte equivalence with the go-list batch
// path (GeneratePackagesWithFilters) is established only for single-package,
// non-type-error corpus cases (the corpus gate skips type-error cases). On a
// package that fails to type-check the batch path emits nothing (it deletes the
// dir from its work-set on TypeErrors); Generate emits best-effort output.
// Additionally, type-error diagnostics collected by checkSkeletonPackage are
// currently discarded (analyze ignores the []types.Error return), so Generate's
// returned diagnostics omit type errors that the batch path would surface.
// Proper type-error emission semantics and surfacing type-error diagnostics via
// the returned slice are deferred to Phase 1.
func (m *Module) Generate(dir string) (map[string][]byte, []diag.Diagnostic, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	ext, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	a, err := m.analyze(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
	if err != nil {
		return nil, nil, err
	}
	// Use the bag created in analyze (shares fset, carries script-resolution diags).
	bag := a.bag
	out := map[string][]byte{}
	for path, f := range a.gsxFiles {
		gen, ok := generateFile(f, a.resolved, a.table, a.propFields, a.nodeProps, a.byo,
			a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, bag, nil, nil, true, true)
		if !ok {
			continue
		}
		out[path] = gen
	}
	return out, bag.Sorted(), nil
}
