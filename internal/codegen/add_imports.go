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
// analyze deliberately emits a SECOND copy of a component child-prop
// expression, under its own //line stamp, as an inference-harvest probe (see
// infer.go's inferRegistry doc) — so one source-level qualifier can appear
// TWICE in the skeleton: once at its real site (the props literal / gsx.Attrs
// assignment), once inside a _gsxuseq(...) probe call. The type-error loop
// (module_importer.go) suppresses type errors landing inside a probe span
// because "the props literal reports it" — so the ONE diagnostic the user
// actually sees anchors at the props-literal copy, never the probe copy.
// MissingImport.Pos is documented to mirror that diagnostic, so this function
// must skip the same probe spans, computed by the same harvestProbeSpans
// helper the type-error loop's quietSpans is built from
// (module_importer.go). This is NOT the same thing as probeSiteForError:
// that reports membership in an inferRegistry TYPE-INFERENCE span, which for
// a GENERIC tag covers the props-literal occurrence (it is the one
// participating in inference) — the occurrence we must KEEP — so filtering
// on it used to drop the wrong copy for generics while accidentally working
// for plain tags (where it never matched anything, and the (Name, Symbol)
// dedupe below did all the work by keeping whichever copy ast.Inspect visited
// first).
//
// A second layer of dedupe collapses by (Name, Symbol) per file: two
// GENUINE uses of the same qualifier+symbol (e.g. fmt.Sprint on two
// different lines, neither inside a probe) still collapse to one entry,
// because the caller (organizeImports / the quickfix) adds one import either
// way — see add_imports_test.go's TestMissingImportsRepeatedGenuineUsesCollapse.
// With the probe copies now filtered out up front, the surviving Pos for a
// deduped (Name, Symbol) is always the FIRST non-probe occurrence in
// ast.Inspect order — for the child-prop case, that is the props-literal
// occurrence, matching the diagnostic.
//
// Pure: walks ASTs analyze already parsed. No IO, no lock, no packages.Load, no
// importer call. spansByFile is computed once per file by analyze (shared with
// the type-error loop's quietSpans) and passed in, so this function itself does
// no AST walk of its own beyond the SelectorExpr inspection below; per ident,
// inHarvestProbe scans that file's span slice, so the cost is O(idents × spans)
// — spans are few (one per child-prop/spread probe), so this stays cheap. Safe
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
				return true // _gsxuseq harvest-probe copy of a child-prop expr; the props-literal occurrence is reported instead
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
	importerPath, _ := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir)
	graph := m.depGraphPackages()

	var cands []string
	seen := map[string]bool{}
	for path, pkg := range graph {
		if pkg.Name() == name && !seen[path] && stdpath.InternalVisible(path, importerPath) {
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

	// Ambiguous: keep only candidates that export the symbol.
	var exact []string
	for _, path := range cands {
		if m.packageExports(graph, path, symbol) {
			exact = append(exact, path)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return cands // nothing eliminated it; let the caller offer them all
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

// packageExports reports whether path's package declares an exported `symbol`. A
// package already in the graph answers from its scope for free; otherwise its
// export data is read (and cached) via the gc importer. A load failure reports
// false: better to offer one fewer candidate than to add a wrong import.
func (m *Module) packageExports(graph map[string]*types.Package, path, symbol string) bool {
	if pkg, ok := graph[path]; ok {
		return pkg.Scope().Lookup(symbol) != nil
	}
	pkg, err := m.exportDataImporter().Import(path)
	if err != nil || !pkg.Complete() {
		return false
	}
	return pkg.Scope().Lookup(symbol) != nil
}

// exportDataImporter lazily builds and caches a gc export-data importer, used
// only by ResolveImportCandidates (a user-triggered code-action path, never
// Package()'s hot path) to answer "does this stdlib table candidate export
// symbol X" without a fresh packages.Load. Guarded by m.mu, NOT analysisMu:
// Package() holds analysisMu for the duration of an analysis, and sync.Mutex is
// not reentrant, so taking it here would self-deadlock any caller that (however
// indirectly) reached this from within a held analysisMu. ResolveImportCandidates
// never runs on that path, so m.mu is the correct, narrower lock.
func (m *Module) exportDataImporter() types.Importer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gcImporter == nil {
		m.gcImporter = importer.ForCompiler(m.fset, "gc", nil)
	}
	return m.gcImporter
}
