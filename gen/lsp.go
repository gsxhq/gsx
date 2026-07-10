package gen

import (
	"fmt"
	"go/types"
	"io"
	"path/filepath"
	"strings"
	"sync"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspAnalyzer is the concrete code analysis behind the language server: it
// resolves the module root for a directory, retrieves (or lazily creates) a warm
// per-root *codegen.Module, applies override buffers (which mark changed packages
// dirty), and returns the retained Package — the diagnostics plus read-only type
// info (Fset, TypesInfo, expr-map, gsx AST) the read-intelligence features need.
// The Module self-invalidates: Package drops the changed package and its
// reverse-dependency closure from the type cache, keeping the rest warm. It never
// writes .x.go to disk.
//
// Slice-1 limitation: codegen discovers .gsx files by on-disk glob, so a buffer
// opened in the editor but never saved to disk is not analyzed (its override is
// never consulted). Editing existing .gsx files works; unsaved-new files are a
// slice-2 follow-up.
type lspAnalyzer struct {
	optCfg config                // programmatic opts (empty for the stock binary); layered OVER gsx.toml (opts win on conflict)
	warnw  io.Writer             // best-effort sink for a malformed gsx.toml; nil → discard, never fatal
	mods   *moduleSet            // pointer so the value stored in the Analyzer interface shares state
	ec     *editorConfigResolver // pointer so the value stored in the Analyzer interface shares its .editorconfig cache
}

// moduleSet holds one warm *codegen.Module per module root, reused across Analyze
// calls so the expensive external packages.Load stays warm. Callers may invoke
// Analyze concurrently for different roots; module() serializes access per root.
type moduleSet struct {
	mu     sync.Mutex
	byRoot map[string]*codegen.Module
}

// newLSPAnalyzer constructs an lspAnalyzer with an empty warm-module cache.
func newLSPAnalyzer(cfg config, warnw io.Writer) lspAnalyzer {
	return lspAnalyzer{
		optCfg: cfg,
		warnw:  warnw,
		mods:   &moduleSet{byRoot: map[string]*codegen.Module{}},
		ec:     newEditorConfigResolver(),
	}
}

// module returns the warm *codegen.Module for root (lazy-initialised). merged is
// the resolved config for the directory being analysed. The returned Module is
// shared across calls and self-invalidates: SetOverride records content diffs as
// dirty dirs, and Package (called from Analyze) applies the reverse-reflexive-
// transitive closure via applyDirty so importers of changed packages are
// automatically re-type-checked. No manual cache management is required.
func (a lspAnalyzer) module(root, modPath string, merged config) (*codegen.Module, error) {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	if m, ok := a.mods.byRoot[root]; ok {
		return m, nil
	}
	m, err := codegen.Open(codegen.Options{
		ModuleRoot:   root,
		ModulePath:   modPath,
		FilterPkgs:   merged.filterPkgs,
		Aliases:      merged.aliases,
		FieldMatcher: merged.fieldMatcher,
		Classifier:   merged.classifier(),
	})
	if err != nil {
		return nil, err
	}
	a.mods.byRoot[root] = m
	return m, nil
}

// adaptPackageResult converts a *codegen.PackageResult (the Module path's output)
// into the *lsp.Package the server's read-intelligence features consume.
// Every field mapping is preserved: Diags, GSXFset, Fset, Info, Types, ExprMap,
// GSXFiles→Files, CrossIndex/NavIndex/CtrlMap/SigTypes conversions, UnusedImports conversion.
func adaptPackageResult(pr *codegen.PackageResult) *lsp.Package {
	cross := make(map[string]lsp.CrossRef, len(pr.CrossIndex))
	for k, v := range pr.CrossIndex {
		cross[k] = lsp.CrossRef{Name: v.Name, Decl: v.Decl, Decls: v.Decls, Refs: v.Refs}
	}
	nav := make([]lsp.NavRef, len(pr.NavIndex))
	for i, nr := range pr.NavIndex {
		nav[i] = lsp.NavRef{From: nr.From, Name: nr.Name, To: nr.To}
	}
	unused := make(map[string][]gsxfmt.ImportRef, len(pr.UnusedImports))
	for path, imps := range pr.UnusedImports {
		refs := make([]gsxfmt.ImportRef, len(imps))
		for i, u := range imps {
			refs[i] = gsxfmt.ImportRef{Name: u.Name, Path: u.Path}
		}
		unused[path] = refs
	}
	missing := make(map[string][]lsp.MissingImport, len(pr.MissingImports))
	for path, mis := range pr.MissingImports {
		out := make([]lsp.MissingImport, len(mis))
		for i, mi := range mis {
			out[i] = lsp.MissingImport{Name: mi.Name, Symbol: mi.Symbol, Pos: mi.Pos}
		}
		missing[path] = out
	}
	ctrl := make(map[gsxast.Node]lsp.CtrlRef, len(pr.CtrlMap))
	for k, v := range pr.CtrlMap {
		ctrl[k] = lsp.CtrlRef{ClauseStart: v.ClauseStart, Node: v.Node}
	}
	sig := make(map[*gsxast.Component][]lsp.SigTypeRef, len(pr.SigTypes))
	for c, refs := range pr.SigTypes {
		lr := make([]lsp.SigTypeRef, len(refs))
		for i, r := range refs {
			lr[i] = lsp.SigTypeRef{GSXPos: r.GSXPos, Len: r.Len, SkelTyp: r.SkelTyp}
		}
		sig[c] = lr
	}
	return &lsp.Package{
		Diags:          pr.Diags,
		GSXFset:        pr.GSXFset,
		Fset:           pr.Fset,
		Info:           pr.Info,
		Types:          pr.Types,
		ExprMap:        pr.ExprMap,
		Files:          pr.GSXFiles,
		CrossIndex:     cross,
		NavIndex:       nav,
		CtrlMap:        ctrl,
		SigTypes:       sig,
		UnusedImports:  unused,
		MissingImports: missing,
	}
}

func (a lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}
	for p, src := range override {
		m.SetOverride(p, src)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pr, err := m.Package(abs)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return &lsp.Package{}, nil
	}
	return adaptPackageResult(pr), nil
}

// AnalyzeModule analyzes every gsx package in the module containing dir and
// returns a flat cross-reference list. It reuses the warm per-root Module
// (same instance Analyze uses), so the warm type-cache is shared across
// per-dir Package calls. Cross-package CrossRef routing — a ref in pkg A to
// a component declared in pkg B routing into B's CrossRef — is performed by
// an explicit second pass over all packages' type-info, mirroring the batch
// path's compObjOwner pass. Matching is by import-path string rather than
// types.Object pointer equality, so it is stable across concurrent or
// differently-ordered type-checker runs. override supplies unsaved buffers
// (abs path -> bytes); all overrides are applied before the Package calls so
// find-references sees current editor content.
func (a lspAnalyzer) AnalyzeModule(dir string, override map[string][]byte) ([]lsp.CrossRef, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}
	// Apply all overrides before any Package call so applyDirty sees the full
	// dirty set on the first Package call and subsequent calls run from clean state.
	for p, src := range override {
		m.SetOverride(p, src)
	}

	// Phase 1: analyze every package in the module and collect results.
	type pkgEntry struct {
		dir string
		pr  *codegen.PackageResult
	}
	var entries []pkgEntry
	for _, d := range dirs {
		pr, err := m.Package(d)
		if err != nil {
			continue // skip un-analyzable dirs; match prior batch tolerance (partial results)
		}
		if pr == nil {
			continue
		}
		entries = append(entries, pkgEntry{dir: d, pr: pr})
	}

	// Phase 2: build types-package-path → dir map.
	// The Module's checkSkeletonPackage sets each *types.Package's path to the
	// module-qualified import path (deterministic per dir via importPathForDir), so
	// the same string is set on every *types.Package for a given dir regardless of
	// which type-checker run produced it. Both sides of the Phase-4 match below use
	// that same import-path string, so we match without types.Object pointer
	// equality (which is unstable because Package re-analyzes each dir).
	importPathToDir := map[string]string{}
	for _, e := range entries {
		if e.pr.Types != nil {
			importPathToDir[e.pr.Types.Path()] = e.dir
		}
	}

	// Phase 3: seed the merged cross-ref map from each package's in-package
	// CrossIndex (built by buildCrossNav, which already captures same-package refs).
	// Copy the Refs slice so the cross-package append below does not mutate the
	// cached PackageResult.
	type ownerKey struct{ dir, key string }
	cross := map[ownerKey]lsp.CrossRef{}
	for _, e := range entries {
		for key, v := range e.pr.CrossIndex {
			cross[ownerKey{e.dir, key}] = lsp.CrossRef{
				Name:  v.Name,
				Decl:  v.Decl,
				Decls: v.Decls,
				Refs:  append(v.Refs[:0:0], v.Refs...),
			}
		}
	}

	// Phase 4: cross-package routing pass — mirrors GenerateDirs' compObjOwner loop.
	// For each package's type-info, find *types.Func uses that are declared in
	// OTHER project packages and route those refs into the declaring component's
	// CrossRef. In-package refs are skipped (pkgPath == myPath); external packages
	// (not in importPathToDir) are skipped. Synthetic .x.go positions (no //line
	// mapping back to a real source file) are also skipped, mirroring the batch pass.
	for _, e := range entries {
		if e.pr.Info == nil || e.pr.Types == nil {
			continue
		}
		myPath := e.pr.Types.Path()
		for id, obj := range e.pr.Info.Uses {
			fn, ok := obj.(*types.Func)
			if !ok || fn.Pkg() == nil {
				continue // only component-function refs (plain or method)
			}
			pkgPath := fn.Pkg().Path()
			if pkgPath == myPath {
				continue // in-package ref; already in CrossIndex via buildCrossNav
			}
			declDir, ok := importPathToDir[pkgPath]
			if !ok {
				continue // external or stdlib package — not a project gsx component
			}
			key := crossRefKeyForFunc(fn)
			ok2 := ownerKey{declDir, key}
			if _, exists := cross[ok2]; !exists {
				continue // not a tracked component (e.g. a plain Go func, not a gsx component)
			}
			p := e.pr.Fset.Position(id.Pos())
			if strings.HasSuffix(p.Filename, ".x.go") {
				continue // synthetic skeleton position; no //line directive
			}
			cr := cross[ok2]
			cr.Refs = append(cr.Refs, p)
			cross[ok2] = cr
		}
	}

	// Phase 5: flatten the merged cross-ref map into the return slice.
	var refs []lsp.CrossRef
	for _, cr := range cross {
		refs = append(refs, cr)
	}
	return refs, nil
}

// ModuleSymbols returns every symbol declared in every .gsx package in the
// module containing dir, for workspace/symbol. It reuses the warm per-root
// Module (same instance Analyze/AnalyzeModule use) and calls lsp.FileSymbols on
// each package's parsed files. Un-analyzable dirs are skipped (partial results
// tolerated). override supplies unsaved buffers (abs path -> bytes).
func (a lspAnalyzer) ModuleSymbols(dir string, override map[string][]byte) ([]lsp.Symbol, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}
	for p, src := range override {
		m.SetOverride(p, src)
	}
	var syms []lsp.Symbol
	for _, d := range dirs {
		pr, err := m.Package(d)
		if err != nil || pr == nil {
			continue
		}
		for path, file := range pr.GSXFiles {
			syms = append(syms, lsp.FileSymbols(path, file, pr.GSXFset)...)
		}
	}
	return syms, nil
}

// crossRefKeyForFunc derives the component key for a *types.Func: ".Name" for
// a plain function component and "RecvType.Name" for a method component.
// This mirrors componentKey (analyze.go) applied to the already-typed object.
func crossRefKeyForFunc(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "." + fn.Name()
	}
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	if named, ok := recv.(*types.Named); ok {
		return named.Obj().Name() + "." + fn.Name()
	}
	return "." + fn.Name() // fallback: unnamed receiver
}

// FormatSettings resolves the effective print width and tab width for path,
// applying the SAME precedence gsx fmt does (resolveFormatSettings, gen/fmt.go):
// gsx.toml [formatter] > .editorconfig > built-in. cfg here is the
// programmatic-optCfg-over-file-config merge Analyze already computes, so a
// custom binary's WithXxx opts apply to formatting exactly like they do to
// codegen; es is resolved from path via the resolver's own .editorconfig
// cache. Without this, the LSP's format-on-save could disagree with `gsx fmt`
// on the same file — the exact bug class this project guards against.
//
// path must be ABSOLUTE: dir is derived from it for gsx.toml discovery, and
// the .editorconfig resolution itself requires an absolute path (see
// formatSettingsFor's doc comment for why — the editorconfig library resolves
// a relative path against the process's cwd). Best-effort throughout: any
// failure falls through to built-ins (80, pretty.DefaultTabWidth), never fails.
func (a lspAnalyzer) FormatSettings(path string) gsxfmt.FormatSettings {
	dir := filepath.Dir(path)
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	es := a.ec.settingsFor(path)
	return resolveFormatSettings(merged, es)
}

// ImportsMode resolves the effective gsx.toml [formatter] imports mode for dir,
// layering the programmatic optCfg over the file config exactly like FormatSettings.
// Best-effort: returns the default (goimports) on any failure.
func (a lspAnalyzer) ImportsMode(dir string) gsxfmt.ImportsMode {
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	return merged.effectiveImportsMode()
}

// ResolveImport maps an undefined qualifier to candidate import paths. Best-effort
// like PrintWidth/ImportsMode: a module that cannot be opened yields no candidates
// rather than an error, so a code action degrades to offering nothing.
func (a lspAnalyzer) ResolveImport(dir, name, symbol string) []string {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, err := a.module(root, modPath, merged)
	if err != nil {
		return nil
	}
	return m.ResolveImportCandidates(dir, name, symbol)
}

// resolveConfigBestEffort resolves the LSP's effective config: it discovers a
// gsx.toml from dir (walking up, bounded by .git/module root) and merges it under
// optCfg — exactly as resolveConfig does for generate/info — but for the LSP it
// must NEVER break analysis. A malformed/typo'd gsx.toml is logged to warnw (when
// non-nil) and the optCfg baseline is used; with no gsx.toml, optCfg is returned
// unchanged. It loads no packages (TOML + file walk only), so it is cheap enough
// to run per Analyze, which also picks up gsx.toml edits live.
func resolveConfigBestEffort(dir string, optCfg config, warnw io.Writer) config {
	path, ok := discoverConfig(dir)
	if !ok {
		return optCfg
	}
	fileCfg, err := loadConfig(path)
	if err != nil {
		if warnw != nil {
			fmt.Fprintf(warnw, "gsx: lsp: ignoring %s: %v\n", path, err)
		}
		return optCfg
	}
	return mergeConfig(fileCfg, optCfg)
}

// runLSP runs the gsx language server over stdin/stdout (JSON-RPC), logging
// operational failures to stderr. cfg carries the binary's compiled-in opts
// (empty for the stock binary), layered OVER the project's gsx.toml (opts win) per Analyze.
// It returns a process exit code.
func runLSP(stdin io.Reader, stdout, stderr io.Writer, cfg config, _ []string) int {
	srv := lsp.NewServer(stdin, stdout, newLSPAnalyzer(cfg, stderr))
	if err := srv.Run(); err != nil {
		fmt.Fprintf(stderr, "gsx: lsp: %v\n", err)
		return 1
	}
	return 0
}
