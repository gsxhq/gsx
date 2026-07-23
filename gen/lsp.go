package gen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/lsp"
	"github.com/gsxhq/gsx/internal/sourceintel"
	"github.com/gsxhq/gsx/internal/sourceview"
)

// lspAnalyzer is the concrete code analysis behind the language server: it
// resolves the module root for a directory, retrieves (or lazily creates) a warm
// per-root *codegen.Module, receives serialized buffer-lifetime transitions,
// and returns the retained Package — the diagnostics plus read-only type
// info (Fset, TypesInfo, expr-map, gsx AST) the read-intelligence features need.
// The Module self-invalidates: Package drops the changed package and its
// reverse-dependency closure from the type cache, keeping the rest warm. It never
// writes .x.go to disk.
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
	mu            sync.Mutex
	byRoot        map[string]*codegen.Module
	modulePaths   map[string]string // module root -> module directive bound into byRoot[root]
	configIDs     map[string]string // module root -> exact semantic config bound into byRoot[root]
	overrideRoots map[string]moduleOverride
}

// moduleOverride records the exact Module instance holding one buffer's
// authority. Root alone is insufficient: changing a go.mod module directive
// replaces the warm Module at the same filesystem root, while buffers opened
// under the previous identity still need an exact clear-and-replay transfer.
type moduleOverride struct {
	root       string
	module     *codegen.Module
	source     []byte
	configPath string
}

// newLSPAnalyzer constructs an lspAnalyzer with an empty warm-module cache.
func newLSPAnalyzer(cfg config, warnw io.Writer) lspAnalyzer {
	return lspAnalyzer{
		optCfg: cfg,
		warnw:  warnw,
		mods: &moduleSet{
			byRoot:        map[string]*codegen.Module{},
			modulePaths:   map[string]string{},
			configIDs:     map[string]string{},
			overrideRoots: map[string]moduleOverride{},
		},
		ec: newEditorConfigResolver(),
	}
}

func (mods *moduleSet) setOverride(root string, module *codegen.Module, path string, source []byte, configPath string) ([]string, error) {
	mods.mu.Lock()
	defer mods.mu.Unlock()
	path = filepath.Clean(path)
	var affected []string
	var clearErr error
	if previous, ok := mods.overrideRoots[path]; ok && previous.module != module {
		delete(mods.overrideRoots, path)
		if previous.module != nil {
			// A root move clears the prior override; its affected closure must
			// still reach the caller for eviction alongside the new scope.
			cleared, err := previous.module.ClearOverride(path)
			affected = append(affected, cleared...)
			clearErr = err
		}
	}
	affected = append(affected, module.SetOverride(path, source)...)
	if configPath != "" {
		configPath = filepath.Clean(configPath)
	}
	mods.overrideRoots[path] = moduleOverride{root: root, module: module, source: bytes.Clone(source), configPath: configPath}
	return sortedUniqueDirs(affected), clearErr
}

func (mods *moduleSet) snapshot() (map[string]moduleOverride, map[string]*codegen.Module) {
	mods.mu.Lock()
	defer mods.mu.Unlock()
	overrides := make(map[string]moduleOverride, len(mods.overrideRoots))
	for path, owner := range mods.overrideRoots {
		owner.source = bytes.Clone(owner.source)
		overrides[path] = owner
	}
	modules := make(map[string]*codegen.Module, len(mods.byRoot))
	maps.Copy(modules, mods.byRoot)
	return overrides, modules
}

func (mods *moduleSet) evictRoots(roots map[string]bool) {
	mods.mu.Lock()
	defer mods.mu.Unlock()
	for root := range roots {
		delete(mods.byRoot, root)
		delete(mods.modulePaths, root)
		delete(mods.configIDs, root)
	}
}

func (mods *moduleSet) clearOverride(path string) ([]string, error) {
	owner, ok := mods.detachOverride(path)
	if !ok || owner.module == nil {
		return nil, nil
	}
	return owner.module.ClearOverride(filepath.Clean(path))
}

// detachOverride removes path's owner record before any fallible root discovery
// or module construction begins. The returned Module still owns the buffer
// bytes until the caller either proves this is a same-root update or clears it;
// analysis transitions are serialized, so this preserves exact same-byte
// SetOverride semantics without permitting a failed transition to retain stale
// authority after it returns.
func (mods *moduleSet) detachOverride(path string) (moduleOverride, bool) {
	mods.mu.Lock()
	defer mods.mu.Unlock()
	path = filepath.Clean(path)
	owner, ok := mods.overrideRoots[path]
	if !ok {
		return moduleOverride{}, false
	}
	delete(mods.overrideRoots, path)
	return owner, true
}

func sortedUniqueDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	sort.Strings(dirs)
	out := dirs[:1]
	for _, dir := range dirs[1:] {
		if dir != out[len(out)-1] {
			out = append(out, dir)
		}
	}
	return out
}

// module returns the warm *codegen.Module for root (lazy-initialised). merged is
// the resolved config for the directory being analysed. The returned Module is
// shared across calls and self-invalidates: SetOverride records content diffs as
// dirty dirs, and Package (called from Analyze) applies the reverse-reflexive-
// transitive closure via applyDirty so importers of changed packages are
// automatically re-type-checked. No manual cache management is required.
func (a lspAnalyzer) module(root, modPath string, merged config) (*codegen.Module, []string, error) {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	configID := lspSemanticConfigIdentity(merged)
	if m, ok := a.mods.byRoot[root]; ok && a.mods.modulePaths[root] == modPath && a.mods.configIDs[root] == configID {
		return m, nil, nil
	}
	m, err := codegen.Open(codegen.Options{
		ModuleRoot:  root,
		ModulePath:  modPath,
		FilterPkgs:  merged.filterPkgs,
		Aliases:     merged.aliases,
		Renderers:   merged.renderers,
		Classifier:  merged.classifier(),
		ClassMerger: merged.classMerger,
		URLPresets:  merged.urlPresets,
	})
	if err != nil {
		return nil, nil, err
	}

	// A changed module directive creates a distinct Module identity at the same
	// root. Transfer every still-open buffer from its exact previous Module so
	// the new analysis universe neither loses unsaved bytes nor leaves stale
	// authority behind. Paths are sorted to make joined errors deterministic.
	var paths []string
	for path, owner := range a.mods.overrideRoots {
		if owner.root == root && owner.module != m {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var affected []string
	var transferErr error
	for _, path := range paths {
		owner := a.mods.overrideRoots[path]
		affected = append(affected, filepath.Dir(path))
		if owner.module != nil {
			cleared, clearErr := owner.module.ClearOverride(path)
			affected = append(affected, cleared...)
			transferErr = errors.Join(transferErr, clearErr)
		}
		affected = append(affected, m.SetOverride(path, owner.source)...)
		owner.module = m
		a.mods.overrideRoots[path] = owner
	}
	a.mods.byRoot[root] = m
	a.mods.modulePaths[root] = modPath
	a.mods.configIDs[root] = configID
	return m, sortedUniqueDirs(affected), transferErr
}

// lspSemanticConfigIdentity is the complete code-analysis configuration bound
// into one warm Module. Formatter/minifier/dev settings are deliberately absent:
// they do not participate in Module.Package semantics. Renderer registrations
// are reduced to their effective last-wins table before hashing.
func lspSemanticConfigIdentity(cfg config) string {
	finalRenderers := map[string]codegen.RendererAlias{}
	for _, renderer := range cfg.renderers {
		finalRenderers[renderer.TypeKey] = renderer
	}
	rendererKeys := make([]string, 0, len(finalRenderers))
	for key := range finalRenderers {
		rendererKeys = append(rendererKeys, key)
	}
	sort.Strings(rendererKeys)
	renderers := make([]codegen.RendererAlias, 0, len(rendererKeys))
	for _, key := range rendererKeys {
		renderers = append(renderers, finalRenderers[key])
	}
	classMerger := ""
	if cfg.classMerger != nil {
		classMerger = cfg.classMerger.PkgPath + "." + cfg.classMerger.FuncName
	}
	payload := struct {
		FilterPackages []string
		Aliases        []codegen.FilterAlias
		Renderers      []codegen.RendererAlias
		Classifier     string
		ClassMerger    string
	}{
		FilterPackages: cfg.filterPkgs,
		Aliases:        cfg.aliases,
		Renderers:      renderers,
		Classifier:     cfg.classifier().Fingerprint(),
		ClassMerger:    classMerger,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("encode LSP semantic config identity: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// adaptPackageResult converts a *codegen.PackageResult (the Module path's output)
// into the *lsp.Package the server's read-intelligence features consume.
// Every field mapping is preserved: Diags, GSXFset, Fset, Info, SourceIndex,
// Types, ExprMap, GSXFiles→Files, CrossIndex/NavIndex/CtrlMap/SigTypes
// conversions, UnusedImports conversion.
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
	calls := make(map[*gsxast.Element]lsp.ComponentCallFact, len(pr.ComponentCalls))
	for element, call := range pr.ComponentCalls {
		params := make(map[gsxast.Attr]lsp.ComponentParamFact, len(call.Params))
		for attr, param := range call.Params {
			params[attr] = lsp.ComponentParamFact{
				Var:     param.Var,
				Origin:  param.Origin,
				Name:    param.Name,
				Ordinal: param.Ordinal,
				Role:    lsp.ComponentParamRole(param.Role),
			}
		}
		paramDecls := make(map[int][]sourceintel.VersionedSpan, len(call.ParamDecls))
		for ordinal, declarations := range call.ParamDecls {
			paramDecls[ordinal] = append([]sourceintel.VersionedSpan(nil), declarations...)
		}
		calls[element] = lsp.ComponentCallFact{
			Target:             call.Target,
			TargetOrigin:       call.TargetOrigin,
			TargetPackage:      call.TargetPackage,
			TargetKey:          call.TargetKey,
			Signature:          call.Signature,
			Params:             params,
			TargetDecls:        append([]sourceintel.VersionedSpan(nil), call.TargetDecls...),
			ParamDecls:         paramDecls,
			TargetPresentation: call.TargetPresentation,
		}
	}
	componentDecls := make(map[lsp.ComponentDeclKey][]sourceintel.VersionedSpan, len(pr.ComponentDecls))
	for key, declarations := range pr.ComponentDecls {
		componentDecls[lsp.ComponentDeclKey{PackagePath: key.PackagePath, ComponentKey: key.ComponentKey}] = append([]sourceintel.VersionedSpan(nil), declarations...)
	}
	filters := make([]lsp.FilterCandidate, len(pr.Filters))
	for i, fc := range pr.Filters {
		// fc.Pos is already a resolved token.Position (see codegen's
		// filterEntry.pos) — a straight copy becomes the exact same
		// {file,line} shape completionItem/resolve already serves for every
		// other lazy-doc candidate (T9/T10).
		filters[i] = lsp.FilterCandidate{Name: fc.Name, Pkg: fc.Pkg, Func: fc.Func, WantsCtx: fc.WantsCtx, Pos: fc.Pos}
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
		ComponentCalls: calls,
		ComponentDecls: componentDecls,
		SourceIndex:    pr.SourceIndex,
		CtrlMap:        ctrl,
		SigTypes:       sig,
		UnusedImports:  unused,
		MissingImports: missing,
		Filters:        filters,
		URLPresets:     pr.URLPresets,
	}
}

func (a lspAnalyzer) SetOverride(path string, source []byte) ([]string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	absPath = filepath.Clean(absPath)
	// A buffer path can move to a different module when a nearer go.mod appears,
	// disappears, or becomes invalid. Detach its owner record before resolving or
	// opening the next owner: either operation may fail. The old Module retains
	// the bytes only long enough to distinguish an exact same-root update; every
	// failure clears it before returning.
	previous, hadPrevious := a.mods.detachOverride(absPath)
	clearPrevious := func() ([]string, error) {
		if !hadPrevious || previous.module == nil {
			return nil, nil
		}
		return previous.module.ClearOverride(absPath)
	}
	dir := filepath.Dir(absPath)
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		oldAffected, clearErr := clearPrevious()
		return oldAffected, errors.Join(clearErr, err)
	}
	configPath, _ := discoverConfig(dir)
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, moduleAffected, moduleErr := a.module(root, modPath, merged)
	if m == nil {
		oldAffected, clearErr := clearPrevious()
		return sortedUniqueDirs(append(oldAffected, moduleAffected...)), errors.Join(clearErr, moduleErr)
	}
	if hadPrevious && previous.root == root && previous.module == m {
		newAffected, setErr := a.mods.setOverride(root, m, absPath, source, configPath)
		return sortedUniqueDirs(append(moduleAffected, newAffected...)), errors.Join(moduleErr, setErr)
	}
	oldAffected, clearErr := clearPrevious()
	newAffected, setErr := a.mods.setOverride(root, m, absPath, source, configPath)
	affected := append(moduleAffected, oldAffected...)
	affected = append(affected, newAffected...)
	if hadPrevious && previous.module != m {
		affected = append(affected, filepath.Dir(absPath))
	}
	return sortedUniqueDirs(affected), errors.Join(moduleErr, clearErr, setErr)
}

func (a lspAnalyzer) ClearOverride(path string) ([]string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return a.mods.clearOverride(absPath)
}

// RefreshDisk applies one saved-filesystem notification batch to the retained
// analyzer universe. Source refresh and reverse-closure eviction are atomic per
// Module. Configuration and Go-universe changes replace only Modules proven to
// be governed by the changed file, replaying their open buffers through the
// ordinary ownership transition.
func (a lspAnalyzer) RefreshDisk(paths []string) ([]string, error) {
	var gsxPaths, configPaths, goModPaths, goWorkPaths []string
	for _, path := range paths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		absPath = filepath.Clean(absPath)
		switch {
		case strings.HasSuffix(absPath, ".gsx"):
			gsxPaths = append(gsxPaths, absPath)
		case filepath.Base(absPath) == configFileName:
			configPaths = append(configPaths, absPath)
		case filepath.Base(absPath) == "go.mod":
			goModPaths = append(goModPaths, absPath)
		case filepath.Base(absPath) == "go.work":
			goWorkPaths = append(goWorkPaths, canonicalWatchedPath(absPath))
		}
	}
	gsxPaths = sortedUniqueDirs(gsxPaths)
	configPaths = sortedUniqueDirs(configPaths)
	goModPaths = sortedUniqueDirs(goModPaths)
	goWorkPaths = sortedUniqueDirs(goWorkPaths)

	overrides, modules := a.mods.snapshot()
	replay := map[string]moduleOverride{}
	evictRoots := map[string]bool{}
	for _, goModPath := range goModPaths {
		root := filepath.Dir(goModPath)
		if modules[root] != nil {
			evictRoots[root] = true
		}
	}
	for path, owner := range overrides {
		dir := filepath.Dir(path)
		newConfigPath, _ := discoverConfig(dir)
		if owner.configPath != "" && slices.Contains(configPaths, owner.configPath) ||
			newConfigPath != "" && slices.Contains(configPaths, filepath.Clean(newConfigPath)) {
			replay[path] = owner
		}
		newRoot, _, rootErr := moduleRoot(dir)
		for _, goModPath := range goModPaths {
			changedRoot := filepath.Dir(goModPath)
			if owner.root == changedRoot || rootErr == nil && newRoot == changedRoot ||
				rootErr != nil && pathWithinTree(changedRoot, dir) {
				replay[path] = owner
				evictRoots[owner.root] = true
				if rootErr == nil {
					evictRoots[newRoot] = true
				}
			}
		}
	}

	for root, module := range modules {
		if len(goWorkPaths) == 0 {
			break
		}
		oldWorkspace, err := module.GoWorkFile()
		if err != nil {
			return nil, fmt.Errorf("resolve retained workspace for %s: %w", root, err)
		}
		newWorkspace, err := codegen.ResolveGoWorkFile(root)
		if err != nil {
			return nil, fmt.Errorf("resolve refreshed workspace for %s: %w", root, err)
		}
		if oldWorkspace != "" {
			oldWorkspace = canonicalWatchedPath(oldWorkspace)
		}
		if newWorkspace != "" {
			newWorkspace = canonicalWatchedPath(newWorkspace)
		}
		if !slices.Contains(goWorkPaths, oldWorkspace) && !slices.Contains(goWorkPaths, newWorkspace) {
			continue
		}
		evictRoots[root] = true
		for path, owner := range overrides {
			if owner.root == root {
				replay[path] = owner
			}
		}
	}

	if len(evictRoots) != 0 {
		a.mods.evictRoots(evictRoots)
	}
	var affected []string
	var transitionErr error
	replayPaths := make([]string, 0, len(replay))
	for path := range replay {
		replayPaths = append(replayPaths, path)
	}
	sort.Strings(replayPaths)
	for _, path := range replayPaths {
		owner := replay[path]
		changed, err := a.SetOverride(path, owner.source)
		affected = append(affected, changed...)
		if err != nil {
			affected = append(affected, filepath.Dir(path))
			transitionErr = errors.Join(transitionErr, fmt.Errorf("rebind %s: %w", path, err))
		}
	}
	if transitionErr != nil {
		return sortedUniqueDirs(affected), transitionErr
	}

	_, modules = a.mods.snapshot()
	dirsByModule := map[*codegen.Module][]string{}
	for _, path := range gsxPaths {
		dir := filepath.Dir(path)
		root, _, err := moduleRoot(dir)
		if err != nil {
			return sortedUniqueDirs(affected), err
		}
		if module := modules[root]; module != nil {
			dirsByModule[module] = append(dirsByModule[module], dir)
		}
	}
	for module, dirs := range dirsByModule {
		changed, err := module.RefreshDiskSourcesAndInvalidate(sortedUniqueDirs(dirs)...)
		affected = append(affected, changed...)
		if err != nil {
			transitionErr = errors.Join(transitionErr, err)
		}
	}
	return sortedUniqueDirs(affected), transitionErr
}

// canonicalWatchedPath gives client paths and Go-command paths one identity.
// On macOS those commonly differ by /var versus /private/var. For delete events
// the leaf no longer exists, so canonicalize its existing parent and reattach
// the basename.
func canonicalWatchedPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	if resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(resolvedParent, filepath.Base(path))
	}
	return path
}

func (a lspAnalyzer) Analyze(dir string, _ map[string][]byte) (*lsp.Package, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
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

// AnalyzeEphemeral runs a one-shot, cursor-local analysis of content against the
// warm Module for dir without mutating override lifetime or the persistent
// per-dir cache — see codegen.Module.AnalyzeEphemeral for the exact contract
// (path must be for an open/override-backed buffer; repairs are cursor-local).
// It mirrors Analyze's module-resolution shape (resolve root -> warm Module ->
// analyze -> adapt) so it shares the same warm type-cache Analyze uses.
func (a lspAnalyzer) AnalyzeEphemeral(dir, path string, content []byte) (*lsp.Package, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	pr, err := m.AnalyzeEphemeral(abs, filepath.Clean(absPath), content)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return &lsp.Package{}, nil
	}
	return adaptPackageResult(pr), nil
}

// AnalyzeEphemeralNonBlocking is the non-blocking variant of AnalyzeEphemeral
// (see codegen.Module.TryAnalyzeEphemeral). It returns acquired=false without
// analyzing when the warm Module's analysis lock is already held by an
// in-flight background Package/Generate — the LSP dispatch loop uses it so a
// completion or nav request never stalls behind that background work. On
// acquired=true the (*lsp.Package, error) pair is identical to
// AnalyzeEphemeral's; on acquired=false the package is nil and the handler
// serves a retained-snapshot fallback (or replies empty/null) instead. A
// module-resolution error (root/config/warm-Module setup) is returned with
// acquired=false since no analysis was attempted.
func (a lspAnalyzer) AnalyzeEphemeralNonBlocking(dir, path string, content []byte) (*lsp.Package, bool, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, false, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, false, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	pr, acquired, err := m.TryAnalyzeEphemeral(abs, filepath.Clean(absPath), content)
	if !acquired {
		return nil, false, err
	}
	if err != nil {
		return nil, true, err
	}
	if pr == nil {
		return &lsp.Package{}, true, nil
	}
	return adaptPackageResult(pr), true, nil
}

// AnalyzeModule analyzes every gsx package in the module containing dir and
// returns a flat cross-reference list. It reuses the warm per-root Module
// (same instance Analyze uses), so the warm type-cache is shared across
// per-dir Package calls. Cross-package CrossRef routing — a ref in pkg A to
// a component declared in pkg B routing into B's CrossRef — is performed by
// an explicit second pass over all packages' type-info, mirroring the batch
// path's compObjOwner pass. Matching is by import-path string rather than
// types.Object pointer equality, so it is stable across concurrent or
// differently-ordered type-checker runs. Serialized SetOverride/ClearOverride
// transitions update the warm Module before this read-only analysis begins.
func (a lspAnalyzer) AnalyzeModule(dir string, _ map[string][]byte) ([]lsp.CrossRef, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
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

	// Phase 4a: route authored markup calls by their exact codegen identity.
	// Same-package calls were already added to CrossIndex by Package; only
	// cross-package calls need ownership routing here.
	for _, e := range entries {
		if e.pr.Types == nil {
			continue
		}
		myPath := e.pr.Types.Path()
		for element, call := range e.pr.ComponentCalls {
			if element == nil || call.TargetPackage == "" || call.TargetPackage == myPath || call.TargetKey == "" || !element.TagPos.IsValid() {
				continue
			}
			declDir, ok := importPathToDir[call.TargetPackage]
			if !ok {
				continue
			}
			owner := ownerKey{declDir, call.TargetKey}
			cr, exists := cross[owner]
			if !exists {
				continue
			}
			cr.Refs = append(cr.Refs, e.pr.GSXFset.Position(element.TagPos))
			cross[owner] = cr
		}
	}

	// Phase 4b: route real Go references. Markup references are deliberately
	// excluded here; they are owned exclusively by ComponentCalls above.
	authoredGSX := make(map[string]bool)
	for _, entry := range entries {
		for path := range entry.pr.GSXFiles {
			authoredGSX[filepath.Clean(path)] = true
		}
	}
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
			pairedGSX, pairedCandidate := sourceview.PairedGSXPath(p.Filename)
			if !strings.HasSuffix(p.Filename, ".go") || pairedCandidate && authoredGSX[pairedGSX] {
				continue
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

// AnalyzeModuleParams returns complete GSX-authored parameter families for
// semantic rename. Unlike references and workspace symbols, rename cannot use
// partial module results: an un-analyzable GSX package could contain an omitted
// invocation, which would make the resulting WorkspaceEdit silently partial.
//
// Codegen publishes declarations, semantic body uses, and exact planner-bound
// invocation attrs independently per package. This method performs the sole
// module-wide join by the stable semantic key (target package path, component
// key, ordinal). A ref
// without a GSX declaration family is intentionally ignored: it belongs to a
// plain-Go callable or an invalid/non-equivalent GSX family and is not a safe
// rename target.
func (a lspAnalyzer) AnalyzeModuleParams(dir string, _ map[string][]byte) ([]lsp.ComponentParamRenameFact, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}

	results := make([]*codegen.PackageResult, 0, len(dirs))
	for _, packageDir := range dirs {
		result, err := m.Package(packageDir)
		if err != nil {
			return nil, fmt.Errorf("analyze component parameter package %s: %w", packageDir, err)
		}
		if result == nil {
			continue
		}
		for _, diagnostic := range result.Diags {
			if diagnostic.Severity == diag.Error {
				return nil, fmt.Errorf("component parameter rename unavailable while %s has analysis errors", packageDir)
			}
		}
		results = append(results, result)
	}

	byKey := make(map[lsp.ComponentParamKey]*lsp.ComponentParamRenameFact)
	for _, result := range results {
		for _, declaration := range result.ComponentParamDecls {
			key := lsp.ComponentParamKey{
				PackagePath:  declaration.PackagePath,
				ComponentKey: declaration.ComponentKey,
				Ordinal:      declaration.Ordinal,
			}
			if key.PackagePath == "" || key.ComponentKey == "" || key.Ordinal < 0 ||
				declaration.Name == "" || declaration.Origin == nil || len(declaration.Decls) == 0 {
				return nil, errors.New("codegen published an incomplete component parameter family")
			}
			if _, exists := byKey[key]; exists {
				return nil, fmt.Errorf("codegen published duplicate component parameter family %+v", key)
			}
			byKey[key] = &lsp.ComponentParamRenameFact{
				Key:          key,
				Name:         declaration.Name,
				Role:         lsp.ComponentParamRole(declaration.Role),
				Origin:       declaration.Origin.Origin(),
				Decls:        append([]token.Position(nil), declaration.Decls...),
				BlockedNames: append([]string(nil), declaration.BlockedNames...),
			}
		}
	}

	for _, result := range results {
		for _, reference := range result.ComponentParamRefs {
			key := lsp.ComponentParamKey{
				PackagePath:  reference.PackagePath,
				ComponentKey: reference.ComponentKey,
				Ordinal:      reference.Ordinal,
			}
			family := byKey[key]
			if family == nil {
				continue
			}
			if reference.Origin == nil || reference.Name != family.Name || lsp.ComponentParamRole(reference.Role) != family.Role || !reference.Ref.IsValid() {
				return nil, fmt.Errorf("codegen published a component parameter ref inconsistent with family %+v", key)
			}
			family.Refs = append(family.Refs, reference.Ref)
			family.BlockedNames = append(family.BlockedNames, reference.BlockedNames...)
		}
	}

	facts := make([]lsp.ComponentParamRenameFact, 0, len(byKey))
	for _, family := range byKey {
		sortTokenPositions(family.Decls)
		sortTokenPositions(family.Refs)
		sort.Strings(family.BlockedNames)
		family.BlockedNames = slices.Compact(family.BlockedNames)
		facts = append(facts, *family)
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Key.PackagePath != facts[j].Key.PackagePath {
			return facts[i].Key.PackagePath < facts[j].Key.PackagePath
		}
		if facts[i].Key.ComponentKey != facts[j].Key.ComponentKey {
			return facts[i].Key.ComponentKey < facts[j].Key.ComponentKey
		}
		return facts[i].Key.Ordinal < facts[j].Key.Ordinal
	})
	return facts, nil
}

func sortTokenPositions(positions []token.Position) {
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Filename != positions[j].Filename {
			return positions[i].Filename < positions[j].Filename
		}
		return positions[i].Offset < positions[j].Offset
	})
}

// ModuleSymbols returns every symbol declared in every .gsx package in the
// module containing dir, for workspace/symbol. It reuses the warm per-root
// Module (same instance Analyze/AnalyzeModule use) and calls lsp.FileSymbols on
// each package's parsed files. Un-analyzable dirs are skipped (partial results
// tolerated). Serialized buffer transitions already updated the warm Module.
func (a lspAnalyzer) ModuleSymbols(dir string, override map[string][]byte) ([]lsp.Symbol, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	dirs = moduleSymbolDirs(root, dirs, override)
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}
	var syms []lsp.Symbol
	for _, d := range dirs {
		pr, err := m.Package(d)
		if err != nil || pr == nil {
			continue
		}
		for path, file := range pr.GSXFiles {
			source, ok := override[path]
			if !ok {
				source, err = os.ReadFile(path)
				if err != nil {
					continue
				}
			}
			syms = append(syms, lsp.FileSymbols(path, source, file, pr.GSXFset, pr.SourceIndex)...)
		}
	}
	return syms, nil
}

// moduleSymbolDirs augments disk discovery with directories owned by exact
// authored GSX overrides. Workspace partitioning normally supplies only this
// module's open buffers; the lexical Rel check is a defensive ownership
// boundary for direct Analyzer callers. It neither walks new roots nor infers
// ownership from basename or string prefix.
func moduleSymbolDirs(root string, discovered []string, override map[string][]byte) []string {
	root = filepath.Clean(root)
	dirs := append([]string(nil), discovered...)
	for path := range override {
		path = filepath.Clean(path)
		if _, authored := sourceview.PairedGeneratedOutputPath(path); !authored {
			continue
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		dirs = append(dirs, filepath.Dir(path))
	}
	return sortedUniqueDirs(dirs)
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
// failure falls through to built-ins (pretty.DefaultPrintWidth,
// pretty.DefaultTabWidth), never fails.
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
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil
	}
	return m.ResolveImportCandidates(dir, name, symbol)
}

// ExportedSymbols returns the exported top-level symbols of the package at
// importPath, for auto-import completion of an unimported qualifier. Best-effort
// like ResolveImport: a module that cannot be opened yields nothing.
func (a lspAnalyzer) ExportedSymbols(dir, importPath string) []lsp.ImportSymbol {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil
	}
	syms := m.PackageExportedSymbols(importPath)
	if len(syms) == 0 {
		return nil
	}
	out := make([]lsp.ImportSymbol, len(syms))
	for i, sym := range syms {
		out[i] = lsp.ImportSymbol{Name: sym.Name, Kind: importSymbolKind(sym.Kind), Detail: sym.Detail}
	}
	return out
}

// ImportablePackages returns every package dir could import (name + path), for
// auto-import package-name completion. Best-effort like ExportedSymbols.
func (a lspAnalyzer) ImportablePackages(dir string) []lsp.ImportablePackage {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, _, err := a.module(root, modPath, merged)
	if err != nil {
		return nil
	}
	pkgs := m.ImportablePackageNames(dir)
	if len(pkgs) == 0 {
		return nil
	}
	out := make([]lsp.ImportablePackage, len(pkgs))
	for i, pkg := range pkgs {
		out[i] = lsp.ImportablePackage{Name: pkg.Name, Path: pkg.Path}
	}
	return out
}

// importSymbolKind adapts codegen's coarse SymbolKind to the LSP's mirror enum.
// The two enums are parallel by design (each package owns its own so neither
// depends on the other); this explicit switch is the single translation seam.
func importSymbolKind(k codegen.SymbolKind) lsp.SymbolKind {
	switch k {
	case codegen.SymbolFunc:
		return lsp.SymbolFunc
	case codegen.SymbolVar:
		return lsp.SymbolVar
	case codegen.SymbolConst:
		return lsp.SymbolConst
	case codegen.SymbolTypeStruct:
		return lsp.SymbolTypeStruct
	case codegen.SymbolTypeInterface:
		return lsp.SymbolTypeInterface
	default:
		return lsp.SymbolTypeOther
	}
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
