package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	"go/build"
	goparser "go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
	"github.com/gsxhq/gsx/internal/wsnorm"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// importPathForDir maps an absolute package dir under moduleRoot to its Go
// import path. ok is false when dir is not within moduleRoot.
func importPathForDir(moduleRoot, modulePath, dir string) (string, bool) {
	rel, err := filepath.Rel(moduleRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return modulePath, true
	}
	return modulePath + "/" + filepath.ToSlash(rel), true
}

// dirForImportPath is the inverse of importPathForDir. ok is false when
// importPath is not under modulePath (e.g. stdlib or third-party).
func dirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return moduleRoot, true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(importPath, prefix)
	return filepath.Join(moduleRoot, filepath.FromSlash(rel)), true
}

// checkSkeletonPackage type-checks already-parsed package files against imp and
// returns the resulting *types.Package + *types.Info. Type errors are collected
// (not fatal): go/types fills Info best-effort even when some files don't check,
// matching the existing CachedResolver.check behaviour.
func checkSkeletonPackage(dir, pkgName string, files []*goast.File, fset *token.FileSet, imp types.Importer) (*types.Package, *types.Info, []types.Error) {
	info := &types.Info{
		Types: map[goast.Expr]types.TypeAndValue{},
		Defs:  map[*goast.Ident]types.Object{},
		Uses:  map[*goast.Ident]types.Object{},
	}
	var errs []types.Error
	conf := types.Config{
		Importer: imp,
		Error: func(e error) {
			if te, ok := e.(types.Error); ok {
				errs = append(errs, te)
			}
		},
	}
	pkg := types.NewPackage(dir, pkgName)
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files)
	return pkg, info, errs
}

// moduleImporter resolves a project gsx package from the warm graph (skeletons)
// and everything else from external. seen breaks recursion on import cycles;
// cycleErr records the first cycle detected so typesPackageWith can propagate it.
//
// Transitive .x.go boundary (Phase 0, known gap): Import routes only direct
// project gsx packages through the skeleton graph. A Go-only package in the
// project that transitively imports a sibling gsx package is routed to external,
// which loaded it from disk .x.go via packages.Load("./..."). A gsx package
// that imports such a Go-only intermediary therefore transitively resolves those
// sibling gsx symbols from disk .x.go, not from skeletons. This narrow
// (gsx → Go-only → gsx) path is unexercised by the corpus; closing it is
// deferred to Phase 1/2.
type moduleImporter struct {
	m        *Module
	external types.Importer
	seen     map[string]bool
	cycleErr error
}

type parseDiagnosticsError struct {
	diags []diag.Diagnostic
}

func (e parseDiagnosticsError) Error() string {
	if len(e.diags) == 0 {
		return "parse error"
	}
	d := e.diags[0]
	if d.Start.IsValid() {
		return fmt.Sprintf("parse error in %s:%d:%d: %s", d.Start.Filename, d.Start.Line, d.Start.Column, d.Message)
	}
	return "parse error: " + d.Message
}

func diagnosticsFromParseError(err error) ([]diag.Diagnostic, bool) {
	var perr parseDiagnosticsError
	if !errors.As(err, &perr) {
		return nil, false
	}
	return append([]diag.Diagnostic(nil), perr.diags...), true
}

func (mi *moduleImporter) Import(path string) (*types.Package, error) {
	if dir, ok := dirForImportPath(mi.m.opts.ModuleRoot, mi.m.opts.ModulePath, path); ok {
		if mi.m.isGsxPackage(dir) {
			if mi.seen[dir] {
				// cycle guard: return cached package if ready, else signal cycle error.
				if p, ok := mi.m.pkgTypes[dir]; ok {
					return p, nil
				}
				err := fmt.Errorf("import cycle through %s", dir)
				mi.cycleErr = err
				return nil, err
			}
			return mi.m.typesPackageWith(dir, mi)
		}
	}
	return mi.external.Import(path)
}

// reverseClosure returns the reverse-reflexive-transitive closure of seeds over
// importedBy: each seed plus every dir that transitively imports it. Assumes m.mu.
func (m *Module) reverseClosure(seeds []string) map[string]bool {
	out := map[string]bool{}
	stack := append([]string(nil), seeds...)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if out[d] {
			continue
		}
		out[d] = true
		for importer := range m.importedBy[d] {
			if !out[importer] {
				stack = append(stack, importer)
			}
		}
	}
	return out
}

// invalidateLocked drops the reverse-closure of dirs from pkgTypes and pkgResults. Assumes m.mu.
func (m *Module) invalidateLocked(dirs []string) {
	for d := range m.reverseClosure(dirs) {
		delete(m.pkgTypes, d)
		delete(m.pkgResults, d)
		delete(m.depFacts, d)
	}
}

// Invalidate drops the reverse-reflexive-transitive closure of dirs (the dirs
// plus every project gsx package that transitively imports them) from pkgTypes
// and pkgResults, so each is re-type-checked from current skeletons on next use. Graph edges are
// retained (refreshed on re-analyze). Everything outside the closure stays warm.
// This supersedes the coarse whole-cache reset.
//
// Threading: Invalidate takes m.mu but is NOT serialized by analysisMu, so callers
// must not invoke it concurrently with an in-flight Package/Generate on the same
// Module (the recursive importer reads pkgTypes under analysisMu without m.mu). The
// LSP never calls it; the normal incremental path is SetOverride → applyDirty.
func (m *Module) Invalidate(dirs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateLocked(dirs)
}

// Dependents returns the reverse-reflexive-transitive closure of dir over the import
// graph: dir plus every project gsx package that transitively imports it. Watch uses it
// to regenerate every package affected by a change to dir. Returns just dir when nothing
// imports it (or dir is unknown to the graph).
//
// Threading: like Invalidate, Dependents takes m.mu but is NOT serialized by analysisMu,
// so callers must not invoke it concurrently with an in-flight Package/Generate on the
// same Module (the recursive importer reads the graph under analysisMu without m.mu). The
// watch loop is single-goroutine, so this holds.
func (m *Module) Dependents(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cl := m.reverseClosure([]string{dir})
	out := make([]string, 0, len(cl))
	for d := range cl {
		out = append(out, d)
	}
	return out
}

// applyDirty consumes the pending-dirty set (populated by SetOverride): it drops
// the reverse-closure of the dirty dirs from pkgTypes + pkgResults and clears the set. Called
// at the start of each Package/Generate run (under analysisMu).
func (m *Module) applyDirty() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dirty) == 0 {
		return
	}
	seeds := make([]string, 0, len(m.dirty))
	for d := range m.dirty {
		seeds = append(seeds, d)
	}
	m.invalidateLocked(seeds)
	m.dirty = map[string]bool{}
}

// cachedDirs returns the sorted set of dirs currently in pkgTypes (test hook).
func (m *Module) cachedDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pkgTypes))
	for d := range m.pkgTypes {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// cachedResultDirs returns the sorted set of dirs with a cached PackageResult (test hook).
func (m *Module) cachedResultDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pkgResults))
	for d := range m.pkgResults {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// recordImports updates the project-internal import graph for dir, REPLACING
// dir's previous forward edges. Only project gsx packages (the things that can
// live in pkgTypes) become edges; external/stdlib/Go-only imports are ignored.
// Replacement keeps the graph precise across import add/remove: because the
// edited package always re-analyzes in the same turn, its outgoing edges are
// refreshed before any later edit could consult them.
//
// paths must include every import that participates in dir's type-check — both
// the .gsx-hoisted import specs AND the imports of the package's hand-written .go
// files. A gsx package that imports a sibling gsx package SOLELY through a
// hand-written .go (e.g. a model.go) is still type-checked against that sibling's
// skeleton, so its reverse edge must be recorded or editing the sibling would not
// invalidate it.
//
// Resolves dep dirs (isGsxPackage locks m.mu) BEFORE taking m.mu, then mutates
// the graph under the lock.
func (m *Module) recordImports(dir string, paths []string) {
	deps := map[string]bool{}
	for _, p := range paths {
		if dd, ok := dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, p); ok && m.isGsxPackage(dd) {
			deps[dd] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.imports[dir] {
		if set := m.importedBy[old]; set != nil {
			delete(set, dir)
		}
	}
	newDeps := make([]string, 0, len(deps))
	for dd := range deps {
		newDeps = append(newDeps, dd)
		if m.importedBy[dd] == nil {
			m.importedBy[dd] = map[string]bool{}
		}
		m.importedBy[dd][dir] = true
	}
	m.imports[dir] = newDeps
}

// depPropFacts is the cached per-dep-dir prop-fact bundle consumed by the
// per-file qualified merge (fileScopedFacts): everything call-site attr
// splitting needs to treat an imported component like a same-package one.
// Derived syntactically by componentPropFieldsFor (plus its BYO external
// load), so it holds no fset positions and survives fset rebuilds — it is
// still reset there for uniformity. Invalidation: invalidateLocked deletes
// the entry whenever the dep dir is in the dirty closure.
type depPropFacts struct {
	pkgName     string
	propFields  map[string]map[string]bool
	nodeProps   map[string]map[string]bool
	attrsProps  map[string]map[string]bool
	genericSigs map[string]*genericSig
	byo         *byoData
}

// importedPropFacts returns dir's prop facts, deriving and caching them on
// first use. Callers run under analysisMu (analyze), so derivation is
// single-flight; m.mu guards the map for Invalidate callers.
func (m *Module) importedPropFacts(depDir string) (*depPropFacts, error) {
	m.mu.Lock()
	if f, ok := m.depFacts[depDir]; ok {
		m.mu.Unlock()
		return f, nil
	}
	m.mu.Unlock()
	files, pkgName, err := m.parsePackageWithFset(depDir, m.fset)
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(depDir, files)
	if err != nil {
		return nil, err
	}
	sigs := genericSigsFor(files, byo)
	// Prefer REAL package names over the requalification engine's
	// last-path-segment heuristic (lookupDepImportPath) for each generic
	// component's declaring-file imports: an unaliased import's spec.name
	// is blank at parse time, so lookupDepImportPath would otherwise have to
	// GUESS the dep-of-a-dep's declared package name from its import path's
	// last segment — wrong whenever the two differ. Fill it in here, when
	// resolvable, from an authoritative source instead (see
	// resolveImportPackageName): this is strictly additive (only blank
	// names are touched) and never invalidates the heuristic's own
	// fail-safe behavior for paths that remain unresolved.
	for _, sig := range sigs {
		for i := range sig.imports {
			if sig.imports[i].name != "" {
				continue
			}
			if name, ok := m.resolveImportPackageName(depDir, sig.imports[i].path); ok {
				sig.imports[i].name = name
			}
		}
	}
	f := &depPropFacts{pkgName: pkgName, propFields: propFields, nodeProps: nodeProps, attrsProps: attrsProps, genericSigs: sigs, byo: byo}
	m.mu.Lock()
	if m.depFacts == nil {
		m.depFacts = map[string]*depPropFacts{}
	}
	m.depFacts[depDir] = f
	m.mu.Unlock()
	return f, nil
}

// resolveImportPackageName returns the REAL declared package name for path,
// preferring authoritative sources over infer.go's last-path-segment
// heuristic: a project-internal path resolves via its own directory's
// package clause (quickPackageName — exact, not a guess); an external path
// is resolved best-effort via go/build, which reliably handles GOROOT/
// stdlib paths but does not understand go.mod module-cache resolution for
// third-party paths — a failure there is expected and simply leaves the
// import spec's name blank, falling back to the (still safe, fails-closed-
// on-ambiguity) heuristic already documented on lookupDepImportPath. srcDir
// anchors go/build's relative resolution.
func (m *Module) resolveImportPackageName(srcDir, path string) (string, bool) {
	if depDir, ok := dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, path); ok {
		return quickPackageName(depDir)
	}
	pkg, err := build.Import(path, srcDir, build.IgnoreVendor)
	if err != nil || pkg.Name == "" {
		return "", false
	}
	return pkg.Name, true
}

// quickPackageName reads the package name declared in dir by parsing ONLY
// the package clause (parser.PackageClauseOnly) of its first Go or gsx
// source file. This is cheap and safe even for a .gsx file: gsx source
// always opens with a plain `package NAME` line using Go's own package-
// clause grammar, and PackageClauseOnly stops scanning immediately after
// that clause, so it never has to tokenize gsx-only syntax (component/JSX)
// appearing later in the file.
func quickPackageName(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".gsx") {
			continue
		}
		f, err := goparser.ParseFile(fset, filepath.Join(dir, name), nil, goparser.PackageClauseOnly)
		if err != nil || f.Name == nil {
			continue
		}
		return f.Name.Name, true
	}
	return "", false
}

// fileFacts is the per-.gsx-file view of prop facts: the package's own facts
// plus, for each gsx package imported BY THIS FILE, the dep's facts qualified
// under the file's alias. Go import aliases are file-scoped, so these views
// must be too — a package-wide alias merge collides when two files bind the
// same alias to different packages.
type fileFacts struct {
	propFields  map[string]map[string]bool
	nodeProps   map[string]map[string]bool
	attrsProps  map[string]map[string]bool
	genericSigs map[string]*genericSig
	byo         *byoData

	// depAliasSpecs maps each gsx-dep import's EFFECTIVE alias in this file
	// (the explicit spec name, or the dep's real declared package name for a
	// plain import — exactly the alias fileScopedFacts qualifies the dep's
	// facts under, i.e. the qualifier a dotted tag spells) to its import
	// SPEC (path + resolved .gsx position). analyze uses it to resolve a
	// failed generic tag's alias (inferRegistry.failedAliases) to the exact
	// import spec whose skeleton "imported and not used" error must be
	// treated as spurious — keyed by the spec's position, not path alone, so
	// a second spec of the SAME path in the file (e.g. an unused dot import
	// alongside the sunk named alias) keeps its own, REAL unused error. See
	// the sunk-import filtering in analyze.
	depAliasSpecs map[string]importSpec
}

// sunkImportKey identifies one user import spec within one .gsx file by its
// source LINE plus import path — the granularity at which a skeleton
// unused-import error can be correlated back to the spec that produced it
// (each spec gets its own //line-mapped skeleton import line; the same
// file+line correlation detectUnusedImports relies on). Both the type-error
// filter (sunkUnusedImportError) and the emitted file's blank-import rewrite
// (generateFile) key on this, so only the EXACT sunk spec is filtered and
// rewritten — never a same-path sibling spec.
type sunkImportKey struct {
	line int
	path string
}

// fileImportSpecs extracts f's hoisted import specs with resolved .gsx
// positions (mirroring buildSkeleton's spec-position block: gc.Src starts
// exactly at gc.Pos(), so chunk offset + intra-chunk offset is the absolute
// .gsx offset). Chunk parse errors are skipped here — buildSkeleton surfaces
// them with a clean diagnostic.
func fileImportSpecs(f *gsxast.File, fset *token.FileSet) []importSpec {
	var specs []importSpec
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		imps, _, _, err := splitChunk(gc.Src)
		if err != nil {
			continue
		}
		if fset != nil && gc.Pos().IsValid() {
			if tf := fset.File(gc.Pos()); tf != nil {
				base := fset.Position(gc.Pos()).Offset
				for i := range imps {
					imps[i].pos = tf.Pos(base + imps[i].srcOff)
				}
			}
		}
		specs = append(specs, imps...)
	}
	return specs
}

// fileScopedFacts builds the per-file fact view for one parsed .gsx file.
// base facts are shared (not copied) when the file imports no gsx packages;
// otherwise shallow clones are extended with alias-qualified dep entries.
// A dep whose facts cannot be derived (parse/analysis error) is skipped with
// a positioned Warning: its components fall back to the assume-prop regime
// (declared == nil) instead of silently flip-flopping between regimes.
func (m *Module) fileScopedFacts(dir string, f *gsxast.File, propFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, bag *diag.Bag, fset *token.FileSet) *fileFacts {
	out := &fileFacts{propFields: propFields, nodeProps: nodeProps, attrsProps: attrsProps, genericSigs: nil, byo: byo}
	cloned := false
	seen := map[string]bool{} // alias+"\x00"+depDir: dedupe repeated specs
	for _, spec := range fileImportSpecs(f, fset) {
		if spec.name == "." || spec.name == "_" {
			continue // dot/blank imports carry no qualified tags
		}
		depDir, ok := dirForImportPath(m.opts.ModuleRoot, m.opts.ModulePath, spec.path)
		if !ok || depDir == dir || !m.isGsxPackage(depDir) {
			continue
		}
		facts, err := m.importedPropFacts(depDir)
		if err != nil {
			pos := fset.Position(spec.pos)
			bag.Add(diag.Diagnostic{
				Start: pos, End: pos, Severity: diag.Warning, Code: "imported-props-unavailable", Source: "codegen",
				Message: fmt.Sprintf("cannot analyze imported gsx package %q: %v; its component props are not discovered (identifier attrs on its components are treated as prop fields)", spec.path, err),
			})
			continue
		}
		alias := spec.name
		if alias == "" {
			alias = facts.pkgName
		}
		key := alias + "\x00" + depDir
		if seen[key] {
			continue
		}
		seen[key] = true
		if !cloned {
			out.propFields = maps.Clone(propFields)
			out.nodeProps = maps.Clone(nodeProps)
			out.attrsProps = maps.Clone(attrsProps)
			out.genericSigs = map[string]*genericSig{}
			out.depAliasSpecs = map[string]importSpec{}
			out.byo = byo.cloneForFile()
			cloned = true
		}
		out.depAliasSpecs[alias] = spec
		for name, fields := range facts.propFields {
			out.propFields[alias+"."+name] = fields
		}
		for name, fields := range facts.nodeProps {
			out.nodeProps[alias+"."+name] = fields
		}
		for name, fields := range facts.attrsProps {
			out.attrsProps[alias+"."+name] = fields
		}
		for name, sig := range facts.genericSigs {
			out.genericSigs[alias+"."+name] = sig
		}
		out.byo.mergeQualified(alias, facts.byo)
	}
	return out
}

// importGraphSnapshot returns deep copies of the forward and reverse import
// graphs for tests. Reverse edges are flattened (dep -> sorted importer dirs).
func (m *Module) importGraphSnapshot() (fwd map[string][]string, rev map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fwd = map[string][]string{}
	for k, v := range m.imports {
		fwd[k] = append([]string(nil), v...)
	}
	rev = map[string][]string{}
	for dep, set := range m.importedBy {
		for imp := range set {
			rev[dep] = append(rev[dep], imp)
		}
		sort.Strings(rev[dep])
	}
	return fwd, rev
}

// isGsxPackage reports whether dir contains at least one .gsx file (disk or
// override).
func (m *Module) isGsxPackage(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if len(matches) > 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for p := range m.overrides {
		if filepath.Dir(p) == dir && strings.HasSuffix(p, ".gsx") {
			return true
		}
	}
	return false
}

// typesPackage type-checks dir's skeletons (building a fresh importer rooted at
// dir) and returns/caches the *types.Package. Entry point for external callers.
func (m *Module) typesPackage(dir string) (*types.Package, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	return m.typesPackageWith(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
}

// typesPackageWith does the work, threading the recursive importer. It calls
// analyze and returns only the *types.Package (the importer path needs nothing
// else), caching the result for the cycle guard + repeat lookups.
func (m *Module) typesPackageWith(dir string, mi *moduleImporter) (*types.Package, error) {
	m.mu.Lock()
	if p, ok := m.pkgTypes[dir]; ok {
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()
	a, err := m.analyze(dir, mi)
	if err != nil {
		return nil, err
	}
	return a.pkg, nil
}

// analyzed is the full retained result of analyzing one gsx package: the parsed
// gsx files, the type-checked skeleton, harvested type/expr maps, and the
// component cross-index inputs. typesPackage consumes only a.pkg; Module.Package
// (retained analysis) and Module.Generate (codegen) consume the rest.
type analyzed struct {
	pkgName            string
	gsxFiles           map[string]*gsxast.File        // gsx path -> parsed file
	gsxFset            *token.FileSet                 // gsx positions
	skelFset           *token.FileSet                 // skeleton positions (same fset as gsxFset for Module)
	goFiles            []*goast.File                  // parsed skeletons + shared helper
	compsByXGo         map[string][]*gsxast.Component // skeleton abs path -> components
	table              filterTable
	propFields         map[string]map[string]bool
	nodeProps          map[string]map[string]bool
	attrsProps         map[string]map[string]bool
	byo                *byoData
	factsByFile        map[string]*fileFacts // per-file fact views; propFields/nodeProps/attrsProps/byo keep the package-local base facts
	resolved           map[gsxast.Node]types.Type
	exprMap            map[gsxast.Node]goast.Expr
	ctrlMap            map[gsxast.Node]ctrlRef            // control-flow node -> skeleton clause pos + containing node
	sigTypes           map[*gsxast.Component][]SigTypeRef // component -> parameter type spans (go-to-def on a param type)
	pkg                *types.Package
	info               *types.Info
	compByKey          map[string]*gsxast.Component // componentKey -> component (for Name + NamePos)
	objKey             map[types.Object]string      // component func object -> componentKey
	bag                *diag.Bag                    // diagnostics from parse + script resolution; used by Generate
	importSpecs        []importSpec                 // hoisted .gsx import specs (for unused-import detection)
	typeErrs           []types.Error                // raw type errors from checkSkeletonPackage
	signatureConflicts []signatureConflict          // same-name different-signature component collisions (block emission)

	// sunkImports maps a .gsx file path to the import SPECS (line+path keys)
	// the type-checker PROVED were used only by a requalification-failed
	// generic tag (the skeleton sink dropped that only reference, and the
	// resulting spurious "imported [as x] and not used" error was filtered —
	// see the confirmedSunk block in analyze). generateFile rewrites each
	// such spec to a blank `_` import so the emitted .x.go compiles (the
	// emitted sink drops the reference too). An import with ANY other use
	// never lands here — its named import must stay — and a same-path
	// SIBLING spec (different line) is never touched.
	sunkImports map[string]map[sunkImportKey]bool
}

// sunkUnusedImportError reports whether e is a spurious unused-import
// skeleton error caused by a requalification-failed generic tag's sink
// dropping the import's only skeleton reference — see the filtering site in
// analyze for the full story. go/types spells the error in TWO forms
// ($GOROOT/src/go/types/resolver.go, errorUnusedPkg):
//
//	"path" imported and not used            (plain, dot, or path-named alias)
//	"path" imported as alias and not used   (renamed import)
//
// and both must match — a renamed import (`import comp "…"`) whose only use
// was the failed tag emits the second form. Match is exact, not heuristic:
// the error's //line-adjusted position must land on a sunk spec's own .gsx
// LINE and the quoted path must be that spec's path (sunkImportKey), so a
// same-path sibling spec on another line — e.g. a genuinely unused dot
// import next to the sunk alias — keeps its own, REAL error. On a match it
// returns the .gsx file and key, so the caller can record the pair as
// CONFIRMED unused (driving the emitted file's blank-import rewrite).
func sunkUnusedImportError(e types.Error, sunkImports map[string]map[sunkImportKey]bool) (file string, key sunkImportKey, ok bool) {
	if !isUnusedImportMsg(e.Msg) {
		return "", sunkImportKey{}, false
	}
	pos := e.Fset.Position(e.Pos)
	set := sunkImports[pos.Filename]
	if set == nil {
		return "", sunkImportKey{}, false
	}
	// Extract the quoted path from the message (same parse pickImportByPath
	// uses); it is the first quoted string in both message forms.
	i := strings.IndexByte(e.Msg, '"')
	if i < 0 {
		return "", sunkImportKey{}, false
	}
	j := strings.IndexByte(e.Msg[i+1:], '"')
	if j < 0 {
		return "", sunkImportKey{}, false
	}
	key = sunkImportKey{line: pos.Line, path: e.Msg[i+1 : i+1+j]}
	if !set[key] {
		return "", sunkImportKey{}, false
	}
	return pos.Filename, key, true
}

// isUnusedImportMsg matches BOTH go/types unused-import message forms — see
// sunkUnusedImportError's doc.
func isUnusedImportMsg(msg string) bool {
	if strings.Contains(msg, "imported and not used") {
		return true
	}
	return strings.Contains(msg, "imported as ") && strings.Contains(msg, " and not used")
}

// analyze performs the shared parse -> skeleton -> type-check pipeline for one
// gsx package dir, threading the recursive importer mi, and returns the rich
// analyzed result. It preserves Task 4's cycle behaviour: a cycle detected
// during type-check is propagated (without caching) via mi.cycleErr.
func (m *Module) analyze(dir string, mi *moduleImporter) (*analyzed, error) {
	mi.seen[dir] = true

	// Use the Module-wide shared fset for parse + skeleton so //line directives
	// from buildSkeleton reference valid positions, AND so this package's objects
	// share one FileSet with sibling packages and external deps (see Module's
	// "FileSet" note). For Module the gsx fset and skeleton fset are the same:
	// skeleton idents resolve back to .gsx via the //line directives the parser
	// honoured.
	fset := m.fset
	// bag is created here (using the shared fset for position resolution) so that
	// script-resolution diagnostics recorded below share the same fset as the
	// parsed .gsx files. Generate returns a.bag.Sorted() so errors surface.
	bag := diag.NewBag(fset)
	gsxFiles, pkgName, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	// Classify <script> @{ } JS contexts (after wsnorm.Normalize).
	// If ANY file fails resolution, skip the ENTIRE package (no generated output),
	// matching Module's package-level-skip semantics:
	//   hasErr=true; break  →  if hasErr { continue }  (no .x.go for any file).
	// Diagnostics are recorded in bag and surfaced by Generate via bag.Sorted().
	scriptErr := false
	for _, f := range gsxFiles {
		if !jsx.ResolveScripts(f, bag) {
			scriptErr = true
		}
	}
	if scriptErr {
		gsxFiles = nil // package-level skip: Generate's loop emits nothing
	}
	table, err := m.cachedFilterTable()
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	// genericSigs is the PACKAGE-WIDE props-type-name -> *genericSig map (see
	// analyze.go's genericSig/buildSkeleton doc): built once here from every
	// .gsx file's components (not just one file's), so a tag in page.gsx can
	// infer against a generic component declared in a sibling box.gsx of the
	// same package. The SAME map is passed to every file's buildSkeleton call
	// below.
	genericSigs := genericSigsFor(gsxFiles, byo)
	var goFiles []*goast.File
	compsByXGo := map[string][]*gsxast.Component{}
	factsByXGo := map[string]*fileFacts{}
	ctrlOffByXGo := map[string]map[gsxast.Node]int{}
	// inferByXGo carries each file's inference-probe registry (built fresh by
	// buildSkeleton alongside its skeleton source) so harvest can resolve a
	// probe call's instantiated type back onto the *gsxast.Element it was
	// emitted for — see infer.go's inferRegistry doc.
	inferByXGo := map[string]*inferRegistry{}
	// sunkImports maps a .gsx file path to the CANDIDATE import specs
	// (line+path keys) whose only skeleton reference may have been dropped
	// by a requalification-failed generic tag's sink — see the failedAliases
	// doc on inferRegistry and the population site in the buildSkeleton loop
	// below. Confirmation (the type-checker actually reporting the spec
	// unused) happens in the confirmedSunk block after the type check.
	sunkImports := map[string]map[sunkImportKey]bool{}
	var allImportSpecs []importSpec
	factsByFile := map[string]*fileFacts{}
	skelErr := false
	// inferNames is the ONE package-wide inference-probe-helper name
	// allocator shared by every file's buildSkeleton call below (see
	// inferNameAllocator's doc) — without it, two sibling files that each
	// caller-side-infer against a shared component would independently mint
	// the literal helper name "_gsxinfer1", and since every sibling
	// skeleton is type-checked together as one package, that collides:
	// `_gsxinfer1 redeclared in this block`, failing the whole package.
	inferNames := newInferNameAllocator()
	for path, f := range gsxFiles {
		ff := m.fileScopedFacts(dir, f, propFields, nodeProps, attrsProps, byo, bag, fset)
		factsByFile[path] = ff
		skel, comps, imps, ctrlOff, infReg, berr := buildSkeleton(f, table, ff.propFields, ff.nodeProps, ff.attrsProps, genericSigs, ff.genericSigs, ff.byo, m.opts.FieldMatcher, fset, bag, inferNames)
		if berr != nil {
			// buildSkeleton error handling: a positioned attrError becomes a
			// diagnostic and skips this file; any other error is also recorded as a
			// positionless diagnostic (stripping "codegen: " prefix) and skips the
			// whole package. Neither case is a hard infrastructure error — return
			// nil, err is reserved for fs I/O failures, filter-load failures, etc.
			if ae, ok := errors.AsType[*attrError](berr); ok {
				bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
				delete(gsxFiles, path)
				continue
			}
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: strings.TrimPrefix(berr.Error(), "codegen: "), Source: "codegen"})
			skelErr = true
			break
		}
		allImportSpecs = append(allImportSpecs, imps...)
		// A requalification-failed generic tag's SKELETON sink drops the tag's
		// reference to its dep package (see analyze.go's emitProbes fail-safe
		// branch), so when that tag was the file's ONLY use of the import, the
		// skeleton type-check raises a SPURIOUS `"path" imported and not used`
		// hard error at the user's import line — the import IS used in the
		// .gsx source. Resolve each failed alias to its import path now
		// (fileFacts.depAliasSpecs) so the type-error loop below can filter
		// exactly those errors, and Generate can rewrite the emitted import to
		// a blank `_` import (the emitted file drops the reference too).
		if len(infReg.failedAliases) > 0 && ff.depAliasSpecs != nil {
			set := map[sunkImportKey]bool{}
			for alias := range infReg.failedAliases {
				if spec, ok := ff.depAliasSpecs[alias]; ok && spec.pos.IsValid() {
					set[sunkImportKey{line: fset.Position(spec.pos).Line, path: spec.path}] = true
				}
			}
			if len(set) > 0 {
				sunkImports[path] = set
			}
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		absXpath := filepath.Join(dir, base+".x.go")
		gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
		if perr != nil {
			return nil, perr
		}
		goFiles = append(goFiles, gf)
		compsByXGo[absXpath] = comps
		factsByXGo[absXpath] = ff
		ctrlOffByXGo[absXpath] = ctrlOff
		inferByXGo[absXpath] = infReg
	}
	if skelErr {
		gsxFiles = map[string]*gsxast.File{} // package-level skip: Generate's loop emits nothing
	}
	// Shared _gsxuse/_gsxcompsig helpers, added to every package's overlay.
	//
	// _gsxunwrap's trailing parameter is `...any` (not `...error`): it must NOT
	// reject a non-(T, error) tuple at the unwrap site, because doing so emits a
	// go/types message mentioning _gsxunwrap that stripGsxunwrap cannot clean
	// (`… in argument to _gsxunwrap: string does not implement error`). Non-(T,
	// error) child-prop tuples are instead rejected with a clean gsx diagnostic in
	// genChildComponent (see emit.go). `...any` keeps the field-type compat check
	// intact (it still checks the unwrapped first value T against the field) while
	// swallowing any extra results, so no internal helper name leaks.
	//
	// _gsxuseq quietly harvests child-prop and element-spread types. Errors inside
	// it are suppressed because each expression also has a native typed probe that
	// reports the error once.
	//
	// _gsxstr is the whole-literal-pipe seed-probe's per-hole placeholder
	// conversion (see analyze.go's embeddedProbeSeed): it always returns
	// `string`, mirroring how EVERY successful branch of the real emit-time
	// holeStringExpr (string(x), strconv.Format*, (x).String()) ALSO always
	// yields a `string`-typed expression — so a seed built with _gsxstr
	// type-checks to the exact same static type as codegen's precisely-typed
	// seed, without needing each hole's real type known yet (impossible at
	// skeleton-build time). Its trailing `...any` tolerates a bare (no-pipe)
	// hole expression that itself returns a (T, error) tuple, exactly like
	// _gsxunwrap's shape.
	helperXgoPath := filepath.Join(dir, "_gsxshared.x.go")
	helper, _ := goparser.ParseFile(fset, helperXgoPath,
		"package "+pkgName+"\n\nfunc _gsxuse(...any) {}\nfunc _gsxuseq(...any) {}\nfunc _gsxcompsig(any) {}\nfunc _gsxunwrap[T any](v T, _ ...any) T { return v }\nfunc _gsxstr(any, ...any) string { return \"\" }\n", goparser.SkipObjectResolution)
	goFiles = append(goFiles, helper)

	// Include the package's hand-written .go files (model.go, helper.go, etc.)
	// so that companion types and functions are visible during skeleton
	// type-checking alongside the synthetic .x.go overlays.
	//
	// Use build.ImportDir (build-constraint- and test-file-aware) instead of a
	// raw glob so that *_test.go and build-excluded files are correctly omitted —
	// matching the behaviour of resolver.go.
	// On error (e.g. no buildable Go in the dir yet) we simply add nothing.
	//
	// Excluded from the result: live-skeleton overlay paths (compsByXGo keys —
	// the in-memory skeletons already cover them) and the synthetic helper shim
	// (helperXgoPath). Hand-written .x.go files (e.g. gsxshared.x.go) and
	// orphaned .x.go files (from a deleted .gsx) are intentionally included —
	// they are visible to the type-checker as on-disk .go files.
	// goImportPaths collects the imports of the hand-written .go files so the import
	// graph (recordImports) also tracks sibling gsx packages reached only through a
	// companion .go (e.g. a model.go), not just through .gsx-hoisted imports.
	var goImportPaths []string
	if bp, berr := build.ImportDir(dir, 0); berr == nil {
		for _, name := range bp.GoFiles {
			absPath := filepath.Join(dir, name)
			if compsByXGo[absPath] != nil || absPath == helperXgoPath {
				continue // already represented as a synthetic overlay
			}
			src, readErr := os.ReadFile(absPath)
			if readErr != nil {
				continue // file disappeared; not fatal
			}
			realGF, parseErr := goparser.ParseFile(fset, absPath, src, goparser.SkipObjectResolution)
			if parseErr != nil {
				continue // parse error surfaced by type-checker via Error func
			}
			goFiles = append(goFiles, realGF)
			for _, imp := range realGF.Imports {
				if p, uerr := strconv.Unquote(imp.Path.Value); uerr == nil {
					goImportPaths = append(goImportPaths, p)
				}
			}
		}
	}

	// Use the module-qualified import path (not the absolute filesystem dir) as
	// the package path so that type names in diagnostic messages match the batch
	// path's behavior — packages.Load assigns proper import paths, e.g.
	// "corpustest/cases/pkg.Widget", while types.NewPackage(absDir, ...) would
	// produce the raw filesystem path. normalizeDiagPaths would then strip only
	// the temp-dir prefix, leaving "cases/pkg.Widget" instead of "corpustest/cases/pkg.Widget".
	pkgPath := dir
	if ip, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir); ok {
		pkgPath = ip
	}
	pkg, info, typeErrs := checkSkeletonPackage(pkgPath, pkgName, goFiles, fset, mi)
	// Filter SPURIOUS unused-import errors caused by a requalification-failed
	// generic tag's skeleton sink (see the sunkImports doc above): the import
	// IS used in the .gsx source — the failed tag is its use — but the sink
	// dropped its only skeleton reference, so go/types reports
	// `"path" imported and not used` at the user's import line. Left in
	// typeErrs, that single spurious error would (a) hard-fail generation for
	// the WHOLE package (the len(typeErrs)==0 gate below and in module.go)
	// and (b) bury the actionable inference-unavailable warning. Filtering is
	// exact, not heuristic: only errors whose //line-adjusted position lands
	// in a file with a sunk set AND whose quoted path is in that set are
	// dropped; a genuinely unused import of the same path in ANOTHER file
	// keeps its error (its position names that other file).
	//
	// confirmedSunk narrows sunkImports to the (file, spec) pairs the
	// type-checker actually PROVED unused — i.e. exactly the errors filtered
	// here. Only those specs are rewritten to blank `_` imports in the
	// emitted file (analyzed.sunkImports → generateFile): a dep referenced by
	// ANOTHER tag or expression in the same file never produces the unused
	// error, so its named import must stay (blanking it would break those
	// other references).
	var confirmedSunk map[string]map[sunkImportKey]bool
	if len(sunkImports) > 0 {
		kept := typeErrs[:0]
		for _, e := range typeErrs {
			if file, key, ok := sunkUnusedImportError(e, sunkImports); ok {
				if confirmedSunk == nil {
					confirmedSunk = map[string]map[sunkImportKey]bool{}
				}
				if confirmedSunk[file] == nil {
					confirmedSunk[file] = map[sunkImportKey]bool{}
				}
				confirmedSunk[file][key] = true
				continue
			}
			kept = append(kept, e)
		}
		typeErrs = kept
	}
	// Tolerate cross-file duplicate top-level decls (same-name build-tag
	// variants — components or helpers). gsx does not parse build tags; go
	// build filters by tag and is the arbiter of a real same-config duplicate.
	// Runs before the diagnostics loop below so a tolerated redeclaration never
	// becomes a bag diagnostic AND never lands in the stored a.typeErrs (which
	// gates emission). Same-file redeclarations are untouched.
	typeErrs = suppressCrossFileRedeclarations(typeErrs)
	// Collect the skeleton byte spans of every _gsxuseq(...) child-prop or
	// element-spread harvest probe. Each expression is also checked in a native
	// typed context (the props literal or gsx.Attrs assignment), so suppressing
	// errors inside _gsxuseq avoids duplicate diagnostics. Positions are raw
	// token.Pos in the shared fset, directly comparable to a types.Error's Pos.
	var quietSpans []struct{ start, end token.Pos }
	for _, gf := range goFiles {
		goast.Inspect(gf, func(n goast.Node) bool {
			call, ok := n.(*goast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*goast.Ident); ok && id.Name == "_gsxuseq" {
				quietSpans = append(quietSpans, struct{ start, end token.Pos }{call.Pos(), call.End()})
			}
			return true
		})
	}
	for _, e := range typeErrs {
		suppressed := false
		for _, s := range quietSpans {
			if s.start <= e.Pos && e.Pos < s.end {
				suppressed = true
				break
			}
		}
		if suppressed {
			continue // redundant child-prop harvest-probe error; the props literal reports it
		}
		p := e.Fset.Position(e.Pos) // e.Fset is the shared fset; //line maps skeleton → .gsx
		if strings.HasSuffix(p.Filename, ".x.go") {
			continue // synthetic skeleton position: no //line directive, so no valid .gsx location to report
		}
		msg := stripGsxunwrap(e.Msg)
		// Rewrite an inference-probe error ONLY when its RAW (un-//line-
		// adjusted) skeleton position lands inside one of THIS file's own
		// recorded inference-probe spans (see infer.go's inferRegistry doc and
		// inferSite.span) — never by guessing from the tag's overall .gsx
		// line/column range (the finding-6/7 hijack: a user's own failing
		// generic call whose ADJUSTED position merely falls somewhere inside an
		// unrelated component tag's body used to get blamed on that tag). A
		// registry miss leaves msg untouched, so every other type error
		// (including a user's own "cannot infer" from their own generic call)
		// passes through verbatim. See rewriteProbeDiag for the per-shape
		// rewrite rules.
		if site, rawOffset, ok := probeSiteForError(inferByXGo, fset, e.Pos); ok {
			msg = rewriteProbeDiag(msg, site, rawOffset)
		}
		bag.Add(diag.Diagnostic{Start: p, End: p, Severity: diag.Error, Message: msg, Source: "types"})
	}
	if mi.cycleErr != nil {
		// A cycle was detected during this package's type-check; propagate
		// the error without caching so the caller receives it.
		return nil, mi.cycleErr
	}

	// Cache the type-checked package so every entry point — Package, Generate, and
	// the recursive importer — sees the same freshly-checked result. This eliminates
	// the latent stale-cache bug where Package(A)/Generate(A) called analyze(A)
	// directly without writing pkgTypes[A], so a later importer of A would hit a
	// stale (or absent) entry written by an earlier typesPackageWith call.
	//
	// Lock discipline: release m.mu BEFORE calling m.recordImports, which acquires
	// m.mu internally.
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = pkg
	m.mu.Unlock()

	// Record the project-internal import graph for this package. Only successful
	// analyses reach this point, keeping the graph consistent with type-checked
	// packages. cycleErr paths are excluded (they are not cached in pkgTypes either).
	// Both the .gsx-hoisted imports and the hand-written .go imports are recorded so
	// every sibling gsx package that participates in this package's type-check gets a
	// reverse edge (else editing it would not invalidate this importer).
	importPaths := make([]string, 0, len(allImportSpecs)+len(goImportPaths))
	for _, s := range allImportSpecs {
		importPaths = append(importPaths, s.path)
	}
	importPaths = append(importPaths, goImportPaths...)
	m.recordImports(dir, importPaths)

	// Harvest once into resolved + exprMap (both consumed downstream: resolved by
	// Generate, exprMap surfaced by Package). Build the component cross-index
	// inputs (compByKey / objKey).
	resolved := map[gsxast.Node]types.Type{}
	exprMap := map[gsxast.Node]goast.Expr{}
	compByKey := map[string]*gsxast.Component{} // componentKey -> component
	compObjByKey := map[string]types.Object{}   // componentKey -> component func object
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		comps, ok := compsByXGo[fname]
		if !ok {
			continue
		}
		harvest(gf, comps, info, resolved, exprMap, inferByXGo[fname])
		// Mark every element whose cross-package generic probe was skipped
		// due to a requalification failure (Task 4 — see
		// inferRegistry.failed's doc) with the types.Invalid sentinel: no
		// probe was ever emitted for it, so harvest never visits it, but
		// emit time (genChildComponent) must still know to skip it rather
		// than generate an uninstantiated (invalid) call. Safe as an
		// out-of-band signal here specifically because generateFile only
		// ever runs when len(a.typeErrs)==0 (see module.go's Generate/
		// Package doc), so a LEGITIMATE harvested type can never equal this
		// sentinel — that only occurs when go/types itself hit a type
		// error, which would already have aborted generation for the whole
		// package.
		if fr := inferByXGo[fname]; fr != nil {
			for _, el := range fr.failed {
				resolved[el] = types.Typ[types.Invalid]
			}
		}
		for _, c := range comps {
			compByKey[componentKey(c)] = c
		}
		for _, decl := range gf.Decls {
			fd, ok := decl.(*goast.FuncDecl)
			if !ok {
				continue
			}
			if _, ok := compByKey[funcDeclKey(fd)]; !ok {
				continue
			}
			if obj := info.Defs[fd.Name]; obj != nil {
				compObjByKey[funcDeclKey(fd)] = obj
			}
		}
	}
	objKey := map[types.Object]string{} // reverse: object -> componentKey
	for key, obj := range compObjByKey {
		objKey[obj] = key
	}

	// Build CtrlMap: skeleton clause position + containing node per control-flow node.
	ctrlMap := map[gsxast.Node]ctrlRef{}
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		co, ok := ctrlOffByXGo[fname]
		if !ok {
			continue
		}
		clauseText := make(map[gsxast.Node]string, len(co))
		for n := range co {
			clauseText[n] = ctrlClauseText(n)
		}
		sub := buildCtrlMap(gf, fset, co, clauseText)
		maps.Copy(ctrlMap, sub)
	}

	// Build SigTypes: per component, the byte span of each navigable signature
	// region in the .gsx — parameter types, type-parameter names/constraints,
	// and a method receiver type — paired with its type-checked skeleton
	// expression, so the LSP can resolve go-to-def / hover on identifiers there.
	sigTypes := map[*gsxast.Component][]SigTypeRef{}
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		comps, ok := compsByXGo[fname]
		if !ok {
			continue
		}
		for _, c := range comps {
			if refs := buildSigTypeRefs(gf, c, byo); refs != nil {
				sigTypes[c] = refs
			}
		}
	}

	// A same-name component declared with DIFFERENT signatures across files is a
	// genuine ambiguity (its cross-file redeclaration was suppressed above, so
	// nothing else will flag it). Report a clean duplicate-component error at
	// each site and record the conflict so emission is blocked (module.go gates
	// on len(signatureConflicts)==0 as well as len(typeErrs)==0).
	sigConflicts := detectSignatureConflicts(gsxFiles)
	for _, sc := range sigConflicts {
		var files []string
		for _, cc := range sc.comps {
			files = append(files, filepath.Base(cc.path))
		}
		for _, cc := range sc.comps {
			bag.Errorf(cc.comp.NamePos, cc.comp.NamePos+token.Pos(len(cc.comp.Name)), "duplicate-component",
				"component %s is declared with different signatures across build-tagged files (%s); build-tag variants must share the same signature — rename the variants or align their parameters",
				cc.comp.Name, strings.Join(files, ", "))
		}
	}

	return &analyzed{
		pkgName:            pkgName,
		gsxFiles:           gsxFiles,
		gsxFset:            fset,
		skelFset:           fset,
		goFiles:            goFiles,
		compsByXGo:         compsByXGo,
		table:              table,
		propFields:         propFields,
		nodeProps:          nodeProps,
		attrsProps:         attrsProps,
		byo:                byo,
		factsByFile:        factsByFile,
		resolved:           resolved,
		exprMap:            exprMap,
		ctrlMap:            ctrlMap,
		sigTypes:           sigTypes,
		pkg:                pkg,
		info:               info,
		compByKey:          compByKey,
		objKey:             objKey,
		bag:                bag,
		importSpecs:        allImportSpecs,
		typeErrs:           typeErrs,
		sunkImports:        confirmedSunk,
		signatureConflicts: sigConflicts,
	}, nil
}

// probeSiteForError resolves a type-checker error's position to the
// inference-probe site it landed inside, if any — the ONLY signal the
// probe diagnostic rewrite (rewriteProbeDiag, called from analyze's
// type-error loop) trusts. pos is a types.Error's Pos in the shared fset;
// the lookup uses the UNADJUSTED position (fset.PositionFor(pos, false),
// ignoring //line directives) to recover the raw skeleton filename + byte
// offset a probe's span was recorded in — see infer.go's inferRegistry doc
// (POSITION MAPPING) and TestInferProbeRawSpanRecovery for why the adjusted
// position (which names the .gsx, not the skeleton) cannot be compared
// against a span directly. inferByXGo is keyed by the skeleton's own
// absolute .x.go path, exactly the raw position's Filename. The raw offset
// is also returned so rewriteProbeDiag can resolve WHICH probe argument an
// argument-positioned error names (inferSite.argAt).
func probeSiteForError(inferByXGo map[string]*inferRegistry, fset *token.FileSet, pos token.Pos) (*inferSite, int, bool) {
	raw := fset.PositionFor(pos, false)
	reg := inferByXGo[raw.Filename]
	if reg == nil {
		return nil, 0, false
	}
	site, ok := reg.siteAt(raw.Offset)
	return site, raw.Offset, ok
}

// rewriteProbeDiag rewrites a type-checker error message whose position
// landed inside a recorded inference-probe span (probeSiteForError) into a
// user-facing diagnostic that names the TAG and never an internal probe
// helper. rawOffset is the error's raw skeleton offset (for argAt). The
// shapes, each pinned by a test in infer_test.go:
//
//  1. plain inference failure — "in call to _gsxinferN, cannot infer T
//     (declared at ...)" (or the _gsxcompsig prefix): the declared-at
//     substance is an internal skeleton artifact, so the message becomes the
//     tag-naming instantiate hint with the component's REAL arity.
//  2. inference failure WITH substance — "in call to _gsxinferN, mismatched
//     types untyped int and untyped string (cannot infer T)": the substance
//     names the user's own conflicting values, so it is preserved: tag +
//     substance + hint.
//  3. constraint violation — "T (type time.Duration) does not satisfy ...":
//     inference SUCCEEDED and the winning type argument violates the
//     component's constraint; instantiating explicitly would fail the same
//     way, so NO instantiate hint — tag + substance only.
//  4. uninstantiated composite-literal probe — "cannot use generic type
//     XxxProps[T any] without instantiation" (the default-branch probe for a
//     generic tag supplying NO declared prop: there is nothing to infer
//     FROM, so the instantiate advice is exactly right). Gated on the
//     message naming THIS site's props type (its BASE identifier — see
//     below), so a user's own without-instantiation error (e.g. passing
//     their own generic func value as a prop, positioned inside the probe
//     span) passes through verbatim.
//  5. argument-positioned leak — "cannot use 2 (...) as string value in
//     argument to _gsxinfer1" (wrong type on a NON-generic prop of a generic
//     tag; go/types positions it AT the offending argument): the probe
//     reference is scrubbed and replaced with the prop's declared name +
//     the tag, resolved via argAt from the error's raw offset.
//  6. any OTHER message still naming a probe helper: the helper name is
//     replaced with the tag reference — never invent structure for an
//     unknown shape, but never leak the internal name either.
//
// Anything else (e.g. an "undefined: x" inside a supplied prop expression,
// which go/types also positions inside the probe span) passes through
// untouched.
func rewriteProbeDiag(msg string, site *inferSite, rawOffset int) string {
	tag := site.el.Tag
	hint := fmt.Sprintf("please instantiate with <%s[%s] ...>", tag, arityTypeHint(site.arity))
	// A supplied attr EXPRESSION is inlined into the probe call's arguments,
	// so a user's own failing generic call there (`value={First(nil)}`)
	// positions its error INSIDE the probe span too — the span match alone
	// cannot distinguish the probe's own failure from the user's nested one.
	// Two extra signals do (both verified live on go 1.26.1):
	//   - the probe's OWN cannot-infer messages always carry the
	//     "in call to _gsxinferN, "/"in call to _gsxcompsig, " prefix
	//     (prefixed below); a user's nested call carries ITS OWN name there
	//     instead ("in call to First, cannot infer T ..."), so an
	//     un-prefixed cannot-infer at a probe span is the user's and passes
	//     through untouched;
	//   - the probe's OWN constraint violations position at the CALLEE,
	//     while a user's nested one positions inside an ARGUMENT expression
	//     (argAt hit) — so an argument-positioned does-not-satisfy passes
	//     through untouched.
	prefixed := probeNameLeakRE.MatchString(msg)
	stripped := probeNameLeakRE.ReplaceAllString(msg, "")
	// The without-instantiation arm matches the props type's BASE identifier:
	// for an ALIAS-imported component the recorded propsType is the caller's
	// spelling ("comp.HolderProps") while go/types prints the dep package's
	// own NAME ("components.HolderProps") — the base identifier after the
	// last dot is the invariant part of both spellings.
	basePropsIdent := site.propsType
	if i := strings.LastIndexByte(basePropsIdent, '.'); i >= 0 {
		basePropsIdent = basePropsIdent[i+1:]
	}
	switch {
	case prefixed && strings.HasPrefix(stripped, "cannot infer"):
		return fmt.Sprintf("type inference failed for <%s>; %s", tag, hint)
	case prefixed && strings.Contains(stripped, "cannot infer"):
		return fmt.Sprintf("type inference failed for <%s>: %s; %s", tag, stripped, hint)
	case strings.Contains(stripped, "does not satisfy"):
		if _, isArg := site.argAt(rawOffset); isArg {
			return msg // argument-positioned: the user's own nested constraint violation
		}
		return fmt.Sprintf("type inference for <%s> failed: %s", tag, stripped)
	case strings.Contains(stripped, "without instantiation") && strings.Contains(stripped, basePropsIdent):
		return fmt.Sprintf("type inference failed for <%s>; %s", tag, hint)
	case probeArgLeakRE.MatchString(stripped):
		substance := probeArgLeakRE.ReplaceAllString(stripped, "")
		if prop, ok := site.argAt(rawOffset); ok {
			return fmt.Sprintf("%s for prop %q of <%s>", substance, prop, tag)
		}
		return fmt.Sprintf("%s for <%s>", substance, tag)
	case probeNameRefRE.MatchString(stripped):
		return probeNameRefRE.ReplaceAllString(stripped, "<"+tag+">")
	default:
		return msg
	}
}

// arityTypeHint renders the `[type, type]` explicit-type-arg hint matching a
// generic component's real arity (inferSite.arity, sourced from
// genericSig.arity — module_importer.go's dep facts for an imported
// component, parseTypeParamNames for a same-package one; both funnel through
// the SAME genericSig.arity field, see genericSigsFor). n <= 0 (should not
// happen for a recorded probe site, but fails safe) renders a single
// placeholder rather than an empty bracket.
func arityTypeHint(n int) string {
	if n <= 0 {
		n = 1
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "type"
	}
	return strings.Join(parts, ", ")
}

// probeNameLeakRE matches the "in call to _gsxinferN, " / "in call to
// _gsxcompsig, " prefix go/types can attach to an error raised while
// checking one of infer.go's synthesized probe calls (e.g. "in call to
// _gsxinfer1, cannot infer T ..." or a constraint violation "in call to
// _gsxinfer2, U (type time.Duration) does not satisfy ..."). Stripped ONLY
// from a message whose position already matched a registered probe span
// (probeSiteForError) — this is never applied to an arbitrary user error.
var probeNameLeakRE = regexp.MustCompile(`^in call to (_gsxinfer[0-9]+|_gsxcompsig), `)

// probeArgLeakRE matches the " in argument to _gsxinferN" clause go/types
// appends to an assignability error at a probe-call argument ("cannot use 2
// (untyped int constant) as string value in argument to _gsxinfer1" — the
// wrong-type-on-a-NON-generic-prop shape). rewriteProbeDiag scrubs it and
// names the prop + tag instead — see shape 5 in its doc.
var probeArgLeakRE = regexp.MustCompile(` in argument to (_gsxinfer[0-9]+|_gsxcompsig)`)

// probeNameRefRE matches ANY remaining reference to a synthesized probe
// helper's name in an error message — rewriteProbeDiag's last-resort scrub
// (shape 6): an unknown message shape at a probe span never leaks the
// internal name, each reference is replaced with the tag.
var probeNameRefRE = regexp.MustCompile(`_gsxinfer[0-9]+|_gsxcompsig`)

// parsePackageWithFset parses every .gsx in dir into the provided fset so the
// caller can share it with buildSkeleton (required for valid //line directives).
func (m *Module) parsePackageWithFset(dir string, fset *token.FileSet) (map[string]*gsxast.File, string, error) {
	paths := map[string]struct{}{}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	for _, p := range matches {
		paths[p] = struct{}{}
	}
	m.mu.Lock()
	for p := range m.overrides {
		if filepath.Dir(p) == dir && strings.HasSuffix(p, ".gsx") {
			paths[p] = struct{}{}
		}
	}
	m.mu.Unlock()
	files := map[string]*gsxast.File{}
	pkgName := ""
	for p := range paths {
		src, ok := m.source(p)
		if !ok {
			continue
		}
		f, perrs := gsxparser.ParseFileWithClassifier(fset, p, src, 0, m.opts.Classifier)
		if len(perrs) > 0 {
			diags := make([]diag.Diagnostic, 0, len(perrs))
			for _, perr := range perrs {
				pos := fset.Position(perr.Pos)
				end := pos
				if perr.End.IsValid() {
					end = fset.Position(perr.End)
				}
				diags = append(diags, diag.Diagnostic{
					Start:    pos,
					End:      end,
					Severity: diag.Error,
					Code:     "parse-error",
					Message:  perr.Msg,
					Source:   "parser",
				})
			}
			return nil, "", parseDiagnosticsError{diags: diags}
		}
		wsnorm.Normalize(f)
		files[p] = f
		pkgName = f.Package
	}
	return files, pkgName, nil
}

// stripGsxunwrap removes all occurrences of _gsxunwrap(...) in s, replacing each
// with its argument. This ensures type error messages from the skeleton
// type-checker do not expose the internal _gsxunwrap helper name to users.
// Nested parentheses inside the argument are handled via bracket counting.
func stripGsxunwrap(s string) string {
	const prefix = "_gsxunwrap("
	if !strings.Contains(s, prefix) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		depth := 1
		j := i + len(prefix)
		for j < len(s) && depth > 0 {
			switch s[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			j++
		}
		// s[i+len(prefix) : j-1] is the content inside _gsxunwrap(...)
		b.WriteString(s[i+len(prefix) : j-1])
		s = s[j:]
	}
	return b.String()
}
