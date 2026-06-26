package codegen

import (
	"go/token"
	"go/types"
	"os"
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
// Cache invalidation: pkgTypes and ext are NOT invalidated on SetOverride —
// imported sibling packages stay cached after an edit. Invalidate is Phase 2.
//
// FileSet: the Module uses ONE *token.FileSet (m.fset) for its whole lifetime,
// covering BOTH the external packages.Load AND every project analyze() call. So
// every type-object position — package A, sibling B, external dep — resolves
// unambiguously against the single fset, exactly like the batch path's single
// packages.Load fset. This is what makes cross-package go-to-def (the expression
// path) resolve a sibling's obj.Pos() to the sibling's source rather than a
// random spot in the importing package.
//
// Growth (Phase 1, accepted): because the fset is Module-lifetime, re-analyzing a
// project package each edit (ResetPackageCache clears pkgTypes → re-parse into the
// same fset) accumulates fset entries. Bounding this (rebuild the Module /
// incremental re-analysis) is a Phase-2 concern. Do NOT rebuild the fset per edit:
// that would orphan the warm ext importer's positions.
type Module struct {
	opts       Options
	overrides  map[string][]byte         // abs .gsx path -> in-memory source
	ext        types.Importer            // lazily built external importer (stdlib + third-party)
	fset       *token.FileSet            // module-wide shared FileSet (see "FileSet" / "Growth" notes above)
	pkgTypes   map[string]*types.Package // abs dir -> checked *types.Package cache
	mu         sync.Mutex                // guards overrides, ext, pkgTypes
	analysisMu sync.Mutex                // serializes Package/Generate/typesPackage (see concurrency contract)
}

// Open constructs a Module. It does not load anything yet; analysis is lazy.
func Open(opts Options) (*Module, error) {
	cls := opts.Classifier
	if cls == nil {
		cls = attrclass.Builtin()
		opts.Classifier = cls
	}
	return &Module{opts: opts, overrides: map[string][]byte{}, fset: token.NewFileSet()}, nil
}

// SetOverride records in-memory source for a .gsx path (an unsaved editor buffer
// or playground source), shadowing disk content. Note: it does NOT invalidate
// the pkgTypes cache or the external importer (ext) — callers that need a
// fresh analysis after an edit must call Invalidate (Phase 2).
func (m *Module) SetOverride(absPath string, src []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides[absPath] = src
}

// source returns the bytes for absPath: override first, else disk.
func (m *Module) source(absPath string) ([]byte, bool) {
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
	m.mu.Unlock()
	return m.ext, nil
}

// Package returns the full retained analysis for a single gsx package dir,
// equivalent to the go-list batch path's per-package result but without codegen
// (Files stays empty; Generate fills it). It populates the FileSets, *types.Info,
// *types.Package, ExprMap, GSXFiles, and the cross/nav indexes used by the LSP.
func (m *Module) Package(dir string) (*PackageResult, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
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
			a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, a.bag, nil, nil)
	}
	res.Diags = a.bag.Sorted()
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFset, a.skelFset, a.info, a.pkg)
	res.UnusedImports = detectUnusedImportsFromErrs(a.typeErrs, a.importSpecs, a.gsxFset)
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
			a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, bag, nil, nil)
		if !ok {
			continue
		}
		out[path] = gen
	}
	return out, bag.Sorted(), nil
}
