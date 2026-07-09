package codegen

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
)

// DirOptions overrides Module-level options for a single package dir. The zero
// value means "inherit from Options".
//
// FilterPkgs, when non-nil, replaces Options.FilterPkgs for this dir's filter
// table. It must name only packages the Module already loaded — i.e. packages
// reachable from Options.FilterPkgs, Options.LoadPkgs, or the module's own
// "./..." — because the table is harvested from the loaded types with NO
// packages.Load. Naming an unloaded package is a hard error, never an empty
// table: a silently-empty table would make a "this filter must be rejected"
// test pass for the wrong reason.
type DirOptions struct {
	FilterPkgs  []string        // nil = inherit Options.FilterPkgs
	ClassMerger *ClassMergerRef // nil = inherit Options.ClassMerger
}

// Options configures a Module. ModuleRoot is the absolute module root (dir
// containing go.mod); ModulePath is its declared module path (from go.mod).
type Options struct {
	ModuleRoot string
	ModulePath string
	// FilterPkgs is the module-wide filter set: it is both loaded into the
	// external importer AND harvested into the default filter table.
	FilterPkgs []string
	// LoadPkgs names extra packages to load into the external importer WITHOUT
	// giving them filter semantics. It is the union half of the union/per-dir
	// split: a caller that needs several dirs to see different filter tables
	// lists every filter package here (one load) and narrows each dir's table
	// via PerDir. A superset here is inert — it only makes more packages
	// importable — whereas a superset in a dir's table silently widens the
	// filter whitelist.
	LoadPkgs []string
	// PerDir maps a package dir to its option overrides. Keys are matched
	// against the dir strings passed to Generate/Package (cleaned), and are
	// also consulted for dirs reached transitively through imports, so an
	// imported sibling package resolves its own filter table. Unsupported in
	// Bundle mode (the Bundle carries exactly one prebuilt table).
	PerDir       map[string]DirOptions
	Aliases      []FilterAlias
	FieldMatcher FieldMatcher
	Classifier   *attrclass.Classifier
	CSSMin       func(string) (string, error) // custom static-CSS minifier (nil = built-in when CSSMinify)
	JSMin        func(string) (string, error) // custom static-JS minifier (nil = built-in when JSMinify)
	CSSMinify    bool                         // minify static <style> CSS
	JSMinify     bool                         // minify static <script> JS
	// Bundle, when non-nil, supplies the external importer and filter table
	// directly (a prebuilt Bundle) so the Module type-checks skeletons
	// with NO packages.Load / `go list` — the mode a WASM build uses. The Module
	// then operates override-only (callers SetOverride all source). Bundle mode is
	// GENERATION-ONLY: the bundle's *types.Package values live in a foreign
	// FileSet, so imported-object positions do not resolve against m.fset; use
	// Generate, not Package, in this mode.
	Bundle *Bundle
	// ClassMerger, when non-nil, names an exported package-level func of type
	// func([]string) string that codegen emits in place of gsx.DefaultClassMerge.
	// Codegen imports the package under the reserved alias _gsxcm and emits
	// _gsxcm.<FuncName> at every class merge site.
	ClassMerger *ClassMergerRef
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
// unambiguously against the single fset, exactly like the Module's own
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
	opts              Options
	overrides         map[string][]byte          // abs .gsx path -> in-memory source
	ext               types.Importer             // lazily built external importer (stdlib + third-party)
	extPkgs           map[string]*types.Package  // the types behind ext, kept for subprocess-free filter-table harvests
	extLoads          int                        // count of external packages.Load calls (observability; test hook)
	filterTbl         filterTable                // lazily built filter table (see cachedFilterTable)
	filterTblErr      error                      // error from the filter-table load (cached alongside filterTbl)
	filterTblDone     bool                       // true once the filter table has been loaded (success or error)
	filterLoads       int                        // count of filter-table loads performed (observability; test hook)
	dirFilterTbls     map[string]filterTable     // per-dir filter-table memo, keyed by canonical FilterPkgs key
	perDirMergersErr  error                      // cached result of validatePerDirMergers
	perDirMergersDone bool                       // true once the PerDir mergers have been validated
	fset              *token.FileSet             // module-wide shared FileSet (see "FileSet" / "Growth" notes above)
	pkgTypes          map[string]*types.Package  // abs dir -> checked *types.Package cache
	pkgResults        map[string]*PackageResult  // abs dir -> cached full analysis result (Package path only)
	depFacts          map[string]*depPropFacts   // abs dep dir -> cached imported prop facts (see importedPropFacts)
	imports           map[string][]string        // dir -> its project-gsx dependency dirs (forward edges)
	importedBy        map[string]map[string]bool // dep dir -> set of importer dirs (reverse edges)
	dirty             map[string]bool            // dirs with a pending content change (consumed by applyDirty)
	fsetBaseline      int                        // m.fset.Base() captured after the last packages.Load (growth measured since here)
	fsetRebuildBytes  int                        // rebuild fset when fset.Base()-fsetBaseline exceeds this; 0 disables
	rebuildCount      int                        // count of fset rebuilds performed (observability; exposed via rebuilds())
	mu                sync.Mutex                 // guards overrides, ext, pkgTypes, pkgResults, depFacts, imports, importedBy, dirty
	analysisMu        sync.Mutex                 // serializes Package/Generate/typesPackage (see concurrency contract)
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
	// A Bundle carries exactly one prebuilt importer and one prebuilt filter
	// table, so per-dir narrowing has nothing to narrow. Silently ignoring PerDir
	// here would hand a dir the Bundle's whole table — the union leak this design
	// exists to prevent — so reject the combination outright.
	if opts.Bundle != nil && (len(opts.PerDir) > 0 || len(opts.LoadPkgs) > 0) {
		return nil, fmt.Errorf("codegen: Options.Bundle is incompatible with PerDir/LoadPkgs (a Bundle carries one prebuilt filter table)")
	}
	return &Module{
		opts:             opts,
		overrides:        map[string][]byte{},
		fset:             token.NewFileSet(),
		dirFilterTbls:    map[string]filterTable{},
		pkgResults:       map[string]*PackageResult{},
		depFacts:         map[string]*depPropFacts{},
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
	if m.opts.Bundle != nil {
		// Bundle mode: the importer is prebuilt; no packages.Load. Returned
		// directly (not cached into m.ext) so rebuildFset's reset is harmless.
		return m.opts.Bundle.importer(), nil
	}
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
	loadPaths = append(loadPaths, m.opts.LoadPkgs...)
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
	m.extPkgs = mp
	m.extLoads++
	m.fsetBaseline = m.fset.Base()
	m.mu.Unlock()
	return m.ext, nil
}

// externalLoads returns the number of external packages.Load calls performed
// (test hook). Together with filterTableLoads it guards the warm-regen perf
// invariant: a warm regeneration must trigger ZERO go-list reloads.
func (m *Module) externalLoads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.extLoads
}

// cachedFilterTable memoizes the filter table for the Module's lifetime. The
// table is the harvest of a packages.Load over the filter packages — an external
// go-list + type-check that costs ~150ms — and it depends ONLY on inputs that are
// immutable for a Module: opts.ModuleRoot, opts.FilterPkgs, opts.Aliases. So it is
// loaded once and reused across every analyze() call, instead of reloading on each
// warm regen (the pre-cache behaviour, which made every --watch cycle pay the full
// packages.Load and turned ~10ms warm regens into ~150ms ones).
//
// Lifetime/invalidation: cleared by rebuildFset (alongside ext), and a filter
// package is Go source — any .go/go.mod change drives the watch loop through
// reopen(), which builds fresh Modules, so a filter-package edit is naturally
// picked up. Called only from analyze, which runs under analysisMu; the m.mu
// double-check mirrors externalImporter.
func (m *Module) cachedFilterTable() (filterTable, error) {
	if m.opts.Bundle != nil {
		return m.opts.Bundle.filters(), nil
	}
	m.mu.Lock()
	if m.filterTblDone {
		defer m.mu.Unlock()
		return m.filterTbl, m.filterTblErr
	}
	m.mu.Unlock()
	tbl, err := loadFilterTableMulti(m.opts.ModuleRoot, dedupFilterPkgs(m.opts.FilterPkgs), m.opts.Aliases)
	m.mu.Lock()
	m.filterTbl, m.filterTblErr, m.filterTblDone = tbl, err, true
	m.filterLoads++
	m.mu.Unlock()
	return tbl, err
}

// filterTableLoads returns the number of filter-table loads performed (test hook).
func (m *Module) filterTableLoads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filterLoads
}

// validatePerDirMergers type-checks every PerDir class merger against the
// external importer's already-loaded types. It is a no-op when no dir overrides
// the merger, so the common path pays nothing.
//
// Called from Generate and Package, not just GenerateDirs: an unvalidated bad
// merger emits `_gsxcm.<Missing>` into the .x.go and exits 0, so the failure
// lands on `go build` far from its cause. The module-level Options.ClassMerger
// is validated by GenerateDirs (and by gen's watch session at startup); this is
// the per-dir equivalent, memoized so repeated Generate calls pay once.
func (m *Module) validatePerDirMergers() error {
	m.mu.Lock()
	if m.perDirMergersDone {
		defer m.mu.Unlock()
		return m.perDirMergersErr
	}
	m.mu.Unlock()

	var refs []*ClassMergerRef
	for _, d := range m.opts.PerDir {
		if d.ClassMerger != nil {
			refs = append(refs, d.ClassMerger)
		}
	}
	if len(refs) == 0 {
		m.mu.Lock()
		m.perDirMergersDone = true
		m.mu.Unlock()
		return nil
	}
	if _, err := m.externalImporter(); err != nil {
		return err // not memoized: a load failure is worth retrying
	}
	m.mu.Lock()
	extPkgs := m.extPkgs
	m.mu.Unlock()

	var err error
	for _, ref := range refs {
		if err = validateClassMergerFromTypes(extPkgs, ref); err != nil {
			break
		}
	}
	m.mu.Lock()
	m.perDirMergersErr, m.perDirMergersDone = err, true
	m.mu.Unlock()
	return err
}

// dirOptionsFor returns the PerDir entry for dir, if any.
func (m *Module) dirOptionsFor(dir string) (DirOptions, bool) {
	if len(m.opts.PerDir) == 0 {
		return DirOptions{}, false
	}
	d, ok := m.opts.PerDir[filepath.Clean(dir)]
	return d, ok
}

// classMergerFor returns the class merger that applies to dir.
func (m *Module) classMergerFor(dir string) *ClassMergerRef {
	if d, ok := m.dirOptionsFor(dir); ok && d.ClassMerger != nil {
		return d.ClassMerger
	}
	return m.opts.ClassMerger
}

// filterTableFor returns the filter table that applies to dir.
//
// Without a PerDir override this is cachedFilterTable — one packages.Load per
// Module, shared by every dir. With one, the table is harvested from the types
// the external importer already loaded, so N dirs with N different filter sets
// cost ONE load between them rather than N. That is the whole point of the
// union/per-dir split: Options.LoadPkgs is loaded once, DirOptions.FilterPkgs
// only narrows what that dir may see.
//
// A dir naming a filter package the importer never loaded is an error. It must
// never degrade to an empty table — a corpus case that asserts "this filter is
// rejected because its package is not whitelisted" would then pass while
// testing nothing.
func (m *Module) filterTableFor(dir string) (filterTable, error) {
	if m.opts.Bundle != nil {
		return m.opts.Bundle.filters(), nil
	}
	d, ok := m.dirOptionsFor(dir)
	if !ok || d.FilterPkgs == nil {
		return m.cachedFilterTable()
	}
	pkgs := dedupFilterPkgs(d.FilterPkgs)
	key := strings.Join(pkgs, "\x00")

	m.mu.Lock()
	if tbl, hit := m.dirFilterTbls[key]; hit {
		m.mu.Unlock()
		return tbl, nil
	}
	m.mu.Unlock()

	// The importer's types are the harvest source, so it must be loaded first.
	// Generate/Package already did this; the call is a cache hit there.
	if _, err := m.externalImporter(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	extPkgs := m.extPkgs
	m.mu.Unlock()

	tbl, err := loadFilterTableFromTypes(extPkgs, pkgs, m.opts.Aliases)
	if err != nil {
		return nil, fmt.Errorf("codegen: filter table for %s: %w", dir, err)
	}
	m.mu.Lock()
	m.dirFilterTbls[key] = tbl
	m.mu.Unlock()
	return tbl, nil
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
	m.extPkgs = nil
	m.filterTbl, m.filterTblErr, m.filterTblDone = filterTable{}, nil, false
	m.dirFilterTbls = map[string]filterTable{}
	m.perDirMergersErr, m.perDirMergersDone = nil, false
	m.pkgTypes = map[string]*types.Package{}
	m.pkgResults = map[string]*PackageResult{}
	m.depFacts = map[string]*depPropFacts{}
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
// without codegen (Files stays empty; Generate fills it). It populates the
// FileSets, *types.Info,
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
	if err := m.validatePerDirMergers(); err != nil {
		return nil, err
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	a, err := m.analyze(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
	if err != nil {
		if diags, ok := diagnosticsFromParseError(err); ok {
			return &PackageResult{Files: map[string][]byte{}, Diags: diags}, nil
		}
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
		SigTypes: a.sigTypes,
	}
	// Run emit for side-effect diagnostics only (unknown filter, attr-error, etc.).
	// Gated on len(a.typeErrs)==0, exactly like Generate: running generateFile on a
	// type-error package adds spurious secondary diagnostics (e.g. "could not resolve
	// type of interpolation") because resolved lacks entries for identifiers the
	// type-checker flagged. The type-error diagnostics themselves are already in the
	// bag (added in analyze). We discard the generated bytes; only bag side-effects matter.
	//
	// Safe despite emit's in-place AST mutation: analyze re-parses a fresh gsx AST
	// on every call, so there is no previously-mutated tree that could be corrupted
	// by a concurrent or repeated generateFile pass on the same nodes.
	if len(a.typeErrs) == 0 && len(a.signatureConflicts) == 0 {
		for path, f := range a.gsxFiles {
			ff := a.factsByFile[path]
			generateFile(f, a.pkg, a.resolved, a.table, ff.propFields, ff.nodeProps, ff.attrsProps, ff.byo,
				a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, a.bag, nil, nil, true, true, a.merger, a.sunkImports[path])
		}
	}
	res.Diags = a.bag.Sorted()
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFset, a.skelFset, a.info, a.pkg)
	res.UnusedImports = detectUnusedImports(a.typeErrs, a.importSpecs, a.gsxFset)
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
// Type-error semantics: a package that fails to type-check emits NOTHING (the
// emit loop below is gated on len(a.typeErrs)==0), and the type-error
// diagnostics collected by checkSkeletonPackage are surfaced via the returned
// slice (analyze adds them to the bag). The golden corpus test drives this path
// directly, so type-error corpus cases are validated byte-for-byte.
func (m *Module) Generate(dir string) (map[string][]byte, []diag.Diagnostic, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	if err := m.validatePerDirMergers(); err != nil {
		return nil, nil, err
	}
	ext, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	a, err := m.analyze(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
	if err != nil {
		if diags, ok := diagnosticsFromParseError(err); ok {
			return map[string][]byte{}, diags, nil
		}
		return nil, nil, err
	}
	// Use the bag created in analyze (shares fset, carries script-resolution diags).
	bag := a.bag
	out := map[string][]byte{}
	// When a package has type errors, skip generateFile entirely — only the
	// type-error diagnostics are surfaced. Running generateFile on a type-error
	// package emits spurious secondary diagnostics (e.g. "could not resolve type of
	// interpolation") because resolved lacks entries for identifiers the type-checker
	// flagged as undefined.
	if len(a.typeErrs) == 0 && len(a.signatureConflicts) == 0 {
		for path, f := range a.gsxFiles {
			ff := a.factsByFile[path]
			gen, ok := generateFile(f, a.pkg, a.resolved, a.table, ff.propFields, ff.nodeProps, ff.attrsProps, ff.byo,
				a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, bag, m.opts.CSSMin, m.opts.JSMin, m.opts.CSSMinify, m.opts.JSMinify, a.merger, a.sunkImports[path])
			if !ok {
				continue
			}
			out[path] = gen
		}
	}
	return out, bag.Sorted(), nil
}
