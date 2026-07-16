package codegen

//go:generate go run ./mkstdlibindex

import (
	goast "go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"slices"
	"sync/atomic"

	"github.com/gsxhq/gsx/internal/codegen/stdpath"
)

// missingFromSkeletons finds, per .gsx file, every qualifier used in a selector
// expression that go/types could not resolve to anything.
//
// The test is exact, not a heuristic: go/types records an object in info.Uses
// for an imported package (a *types.PkgName), a local variable, or any other
// resolved reference. A selector root is always a reference occurrence (never
// a declaration site, so info.Defs is not relevant here), so an identifier at
// the root of a selector with no entry in info.Uses resolved to nothing — it
// is an undefined qualifier, which is what a missing import looks like. The
// alternative, scraping "undefined: fmt" out of type-error message text, is a
// heuristic and is not used.
//
// Positions are reported in the .gsx source: the skeleton carries //line
// directives, so gsxFset maps a skeleton position back to its .gsx origin, the
// same way diagnostics already do.
//
// analyze deliberately emits a SECOND copy of some component operand
// expressions, under their own //line stamps, as quiet type-harvest probes. A
// source qualifier can therefore appear once in its native validation context
// and once inside _gsxuseq(...). The type-error loop suppresses errors in the
// quiet copy, so MissingImport.Pos must skip the same harvestProbeSpans and
// retain the native occurrence.
//
// A second layer of dedupe collapses by (Name, Symbol) per file: two
// GENUINE uses of the same qualifier+symbol (e.g. fmt.Sprint on two
// different lines, neither inside a probe) still collapse to one entry,
// because the caller (organizeImports / the quickfix) adds one import either
// way — see add_imports_test.go's TestMissingImportsRepeatedGenuineUsesCollapse.
// With the probe copies now filtered out up front, the surviving Pos for a
// deduped (Name, Symbol) is always the FIRST non-probe occurrence in
// ast.Inspect order, matching the diagnostic.
//
// Pure: walks ASTs analyze already parsed. No IO, no lock, no packages.Load, no
// importer call. spansByFile is computed once per file by analyze (shared with
// the type-error loop's quietSpans) and passed in, so this function itself does
// no AST walk of its own beyond the SelectorExpr inspection below; per ident,
// inHarvestProbe scans that file's span slice, so the cost is O(idents × spans)
// — spans are few (one per operand/spread probe), so this stays cheap. Safe
// on the Package() hot path.
func missingFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, info *types.Info, spansByFile map[*goast.File][]posSpan) map[string][]MissingImport {
	if info == nil {
		return nil
	}
	out := map[string][]MissingImport{}
	for gsxPath, fs := range byGsx {
		probeSpans := spansByFile[fs.skel]
		var found []MissingImport
		seen := map[string]bool{} // name+symbol: one report per distinct qualifier+symbol per file
		goast.Inspect(fs.skel, func(n goast.Node) bool {
			se, ok := n.(*goast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := se.X.(*goast.Ident)
			if !ok {
				return true
			}
			if _, used := info.Uses[id]; used {
				return true // an imported package, a local, a field...
			}
			// A selector root (se.X) is always a reference occurrence, never a
			// declaration site, so only info.Uses (checked above) can resolve it.
			if inHarvestProbe(probeSpans, id.Pos()) {
				return true // _gsxuseq harvest-probe copy; the native operand occurrence is reported instead
			}
			key := id.Name + "." + se.Sel.Name
			if seen[key] {
				return true
			}
			seen[key] = true
			found = append(found, MissingImport{Name: id.Name, Symbol: se.Sel.Name, Pos: gsxFset.Position(id.Pos())})
			return true
		})
		if len(found) > 0 {
			out[gsxPath] = found
		}
	}
	return out
}

// inHarvestProbe reports whether pos falls inside one of spans (each a
// harvestProbeSpans result for the same file/fset — see that function's doc).
func inHarvestProbe(spans []posSpan, pos token.Pos) bool {
	for _, s := range spans {
		if s.start <= pos && pos < s.end {
			return true
		}
	}
	return false
}

// resolveImportCandidatesCalls counts ResolveImportCandidates invocations. Import
// resolution may read package export data, which must never happen on the
// Package() hot path (the LSP calls Package per debounced analysis). Test-only
// instrumentation: TestPackageDoesNotResolveImports asserts this counter does not
// move across Package(), and DOES move for a direct resolve — so the zero
// assertion cannot be vacuous.
var resolveImportCandidatesCalls atomic.Int64

// ResolveImportCandidates maps an undefined qualifier to the import path(s) that
// could supply it, most-likely-first is NOT implied — the caller decides what to
// do with 0, 1, or many.
//
// dir is the absolute directory of the .gsx package doing the asking (the LSP's
// planned Analyzer.ResolveImport(dir, name, symbol) surface lines up with this).
// It is resolved to that package's own import path via importPathForDir, then
// used to apply Go's internal-visibility rule (stdpath.InternalVisible) to every
// candidate from both sources below: a path with an "internal" component is
// offered only when dir's package is in the tree rooted at that component's
// parent. This is what lets a project's own myapp/internal/db be offered to
// myapp or myapp/views, while encoding/json/internal never is (no importer
// outside GOROOT is ever under "encoding/json"). If dir cannot be resolved to an
// import path (e.g. outside the module), importerPath is "", which
// InternalVisible treats like any other path not under the required prefix —
// conservative, not a special case.
//
// Two sources, both lookups, never a filesystem scan:
//
//   - the module's dependency graph, which analyze already type-checked, giving
//     each package's REAL declared name and a populated scope; and
//   - a baked stdlib name -> path table, for std packages the module does not
//     already reach.
//
// When more than one candidate survives, keep only those that actually export
// `symbol` — this is what collapses `rand` to math/rand/v2 for rand.IntN. A
// candidate already in the graph is checked for free via its scope; one known
// only from the table needs its export data, which the go/importer caches
// (~30-50ms cold, ~25us warm). If NO candidate exports the symbol (a typo, or an
// unloadable package), all candidates are kept: the caller then offers one
// quickfix each rather than guessing.
//
// This is why it must never run on the Package() hot path. It is called only from
// user-triggered code-action handlers.
//
// An unknown name returns nil. goimports would scan the module cache here — a
// measured 1.4s per unresolved identifier, which is the normal mid-typing state.
// We do not.
func (m *Module) ResolveImportCandidates(dir, name, symbol string) []string {
	resolveImportCandidatesCalls.Add(1)
	if name == "" {
		return nil
	}
	// Candidate names, package identities, scopes, and the shared FileSet must
	// all come from one analysis generation. This is a user-triggered slow path,
	// so serialize the complete operation with Package/Generate instead of
	// capturing a split snapshot and trying to reconcile it after the fact.
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()

	importerPath, _ := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir)
	graph := m.depGraphPackages()
	names := m.importCandidatePackageNames(graph)

	var cands []string
	seen := map[string]bool{}
	for path, packageName := range names {
		if packageName == name && !seen[path] && stdpath.InternalVisible(path, importerPath) {
			seen[path] = true
			cands = append(cands, path)
		}
	}
	for _, path := range stdlibIndex[name] {
		if !seen[path] && stdpath.InternalVisible(path, importerPath) {
			seen[path] = true
			cands = append(cands, path)
		}
	}
	slices.Sort(cands)
	if len(cands) <= 1 {
		return cands
	}

	// Ambiguous: keep only candidates that export the symbol. Local scopes are
	// rebuilt on demand through one declaration resolver within the same
	// serialized snapshot used for enumeration above.
	external, externalErr := m.externalImporter()
	var resolver *sourceDeclResolver
	if externalErr == nil {
		resolver = newSourceDeclResolver(m, external)
	}
	var exact []string
	for _, path := range cands {
		if m.packageExports(graph, resolver, path, symbol) {
			exact = append(exact, path)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return cands // nothing eliminated it; let the caller offer them all
}

// importCandidatePackageNames combines the safe external type graph with the
// authoritative main-module identities partitioned out of it. External
// dependencies that re-enter the main module are absent: they are unsupported
// imports, not code-action candidates.
func (m *Module) importCandidatePackageNames(graph map[string]*types.Package) map[string]string {
	names := make(map[string]string, len(graph))
	for path, pkg := range graph {
		names[path] = pkg.Name()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, source := range m.sourcePackages {
		if source.pkgPath != "" && source.name != "" {
			names[source.pkgPath] = source.name
		}
	}
	return names
}

// depGraphPackages returns path -> *types.Package for every COMPLETE package
// the module's external importer loaded, keyed by import path (see
// completeDepGraphPackages, which does the actual Complete() gating and is
// exercised directly by TestDepGraphPackagesSkipsIncomplete since a real
// mapImporter here always comes from a live packages.Load).
//
// packages.Load's NeedDeps hands back the FULL transitive closure, not just
// the packages a caller explicitly requested, so this graph always contains
// std-internal packages incidentally reached through something else in the
// graph (e.g. the gsx runtime imports "encoding/json", which imports
// "encoding/json/internal": that package is in every Module's dep graph, and
// is named "internal", making it a false candidate for an undefined `internal`
// qualifier). It also contains a legitimate same-tree project-local `internal`
// package the same way. Neither is filtered HERE: this function has no
// per-call knowledge of which .gsx package is asking, so it cannot apply Go's
// "importer must be rooted at the parent of internal" rule. Its caller,
// ResolveImportCandidates, has that context (the dir argument) and applies
// stdpath.InternalVisible to every candidate from this graph (and from
// stdlibIndex) before returning.
func (m *Module) depGraphPackages() map[string]*types.Package {
	ext, err := m.externalImporter()
	if err != nil {
		return map[string]*types.Package{}
	}
	if m.opts.Bundle == nil {
		m.mu.Lock()
		packages := make(mapImporter, len(m.extPkgs))
		for path, pkg := range m.extPkgs {
			packages[path] = pkg
		}
		m.mu.Unlock()
		return completeDepGraphPackages(packages)
	}
	mi, ok := ext.(mapImporter)
	if !ok {
		// Defensive, not a documented bundle-mode gap: every current Bundle
		// constructor (bundle.go, resolver.go) sets Bundle.imp to a mapImporter,
		// so this assertion succeeds in bundle mode too and the graph IS
		// enumerable there. A future importer.Importer that is not a mapImporter
		// would land here and yield an empty (not panicking) graph instead.
		return map[string]*types.Package{}
	}
	return completeDepGraphPackages(mi)
}

// completeDepGraphPackages filters mi down to path -> *types.Package for every
// COMPLETE package. go/types fabricates an incomplete placeholder named after
// the import path's last segment for any path its importer never loaded
// ("math/rand/v2" -> name "v2"). That name is a guess, and trusting it once
// made the LSP delete a used import (PR #64). Never read Name() off an
// incomplete package — skip it instead.
func completeDepGraphPackages(mi mapImporter) map[string]*types.Package {
	out := map[string]*types.Package{}
	for path, pkg := range mi {
		if pkg == nil || !pkg.Complete() {
			continue
		}
		out[path] = pkg
	}
	return out
}

// packageExports reports whether path's package declares an exported `symbol`.
// A safe external package already in graph answers from its scope for free;
// main-module source is rebuilt through resolver; remaining stdlib-table
// candidates use cached gc export data. A load failure reports false: better to
// offer one fewer candidate than add a wrong import.
func (m *Module) packageExports(graph map[string]*types.Package, resolver *sourceDeclResolver, path, symbol string) bool {
	if pkg, ok := graph[path]; ok {
		return pkg.Scope().Lookup(symbol) != nil
	}
	if resolver != nil {
		m.mu.Lock()
		_, local := m.sourcePackageDirs[path]
		m.mu.Unlock()
		if local {
			pkg, err := resolver.Import(path)
			return err == nil && pkg != nil && pkg.Scope().Lookup(symbol) != nil
		}
	}
	pkg, err := m.importExportData(path)
	if err != nil || !pkg.Complete() {
		return false
	}
	return pkg.Scope().Lookup(symbol) != nil
}

// exportDataImporter lazily builds and caches a gc export-data importer, used
// only by ResolveImportCandidates (a user-triggered code-action path, never
// Package()'s hot path) to answer "does this stdlib table candidate export
// symbol X" without a fresh packages.Load. ResolveImportCandidates holds
// analysisMu for the complete enumeration-and-filter operation; m.mu only
// protects publication of the cached field.
func (m *Module) exportDataImporter() types.Importer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gcImporter == nil {
		m.gcImporter = importer.ForCompiler(m.fset, "gc", nil)
	}
	return m.gcImporter
}

// importExportData calls Import on the cached gc export-data importer. Its sole
// caller is ResolveImportCandidates, whose analysisMu critical section
// serializes the gc importer's mutable cache together with the authoritative
// package snapshot.
func (m *Module) importExportData(path string) (*types.Package, error) {
	imp := m.exportDataImporter()
	return imp.Import(path)
}
