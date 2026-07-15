package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/gsxhq/gsx/internal/diag"
	"golang.org/x/tools/go/packages"
)

// skeletonUsedNames returns the set of identifiers used as the qualifier X in
// any selector expression X.Sel within f. An imported package name can only be
// referenced this way (or via a dot/blank import, handled separately), so this
// set is exactly "which import local-names are referenced" for a valid Go file.
func skeletonUsedNames(f *goast.File) map[string]bool {
	used := map[string]bool{}
	goast.Inspect(f, func(n goast.Node) bool {
		if sel, ok := n.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	return used
}

// importBaseName is the last path segment — the CONVENTIONAL default local name,
// which for some packages (e.g. gopkg.in/yaml.v3 → "yaml") is NOT the real
// package name. It is used only as a fast "definitely used" check; a base that
// is not referenced makes the import a removal CANDIDATE whose real name must be
// resolved before removal.
func importBaseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// classifyUnusedImports splits a file's hoisted import specs into definitely-unused
// imports and removal candidates, given the set of referenced qualifier names.
//
//   - `_` / `.` imports are never removed (always "used").
//   - An import whose only skeleton reference was dropped by a requalification-
//     failed generic tag (sunk) is never removed — it IS used in the .gsx source.
//   - An aliased import's explicit name is authoritative: unused iff its alias is
//     not referenced.
//   - A default import is kept when its path base is referenced; otherwise it is a
//     CANDIDATE (its real package name may differ from the base and still be used).
func classifyUnusedImports(used map[string]bool, imps []importSpec, sunk map[sunkImportKey]bool, gsxFset *token.FileSet) (unused []UnusedImport, candidates []importSpec) {
	for _, imp := range imps {
		if imp.name == "_" || imp.name == "." {
			continue
		}
		if sunk != nil && imp.pos.IsValid() {
			k := sunkImportKey{line: gsxFset.Position(imp.pos).Line, path: imp.path}
			if sunk[k] {
				continue
			}
		}
		if imp.name != "" {
			if !used[imp.name] {
				unused = append(unused, UnusedImport{Name: imp.name, Path: imp.path})
			}
			continue
		}
		if used[importBaseName(imp.path)] {
			continue
		}
		candidates = append(candidates, imp)
	}
	return unused, candidates
}

// fileSkeleton is one .gsx file's lowered skeleton AST plus the import
// metadata buildPackageSkeletons harvests alongside it: the file's hoisted
// import specs (imps) and the set of specs sunk by a requalification-failed
// generic tag (sunk) — see analyze's sunkImports doc for why a sunk import is
// never a removal candidate even when the skeleton drops its only reference.
type fileSkeleton struct {
	skel *goast.File
	imps []importSpec
	sunk map[sunkImportKey]bool
}

// packageSkeletons is the per-package result of buildPackageSkeletons: every
// buildable .gsx file's skeleton, keyed by its absolute .gsx path, plus the
// FileSet those skeletons (and the .gsx positions in their import specs)
// resolve against.
type packageSkeletons struct {
	gsxFset *token.FileSet
	byGsx   map[string]fileSkeleton // .gsx abs path -> skeleton + import specs + sunk set
	// goParseDiags holds the Go parse errors the skeletons produced, positioned
	// back at their .gsx origin by the skeletons' //line directives. gsx copies
	// user Go through as an opaque blob, so Go that is invalid only in context (an
	// `import` after a declaration) is detectable nowhere earlier than here.
	goParseDiags []diag.Diagnostic
}

// buildPackageSkeletons lowers every .gsx file in dir to its skeleton AST WITHOUT
// type-checking (no importer, no dependency resolution) and returns, per file,
// the parsed skeleton, its hoisted import specs, and its sunk-import set. It
// mirrors analyze's per-file loop (module_importer.go:769-819) using the same
// buildSkeleton lowering, but keeps only what unused-import detection needs. A
// file whose preprocessing or skeleton build fails is omitted, so the caller
// keeps all of that file's imports. Unaffected siblings remain independently
// classifiable. Package-wide syntax or JavaScript preprocessing failure omits
// the whole package because no complete registry exists.
func (m *Module) buildPackageSkeletons(dir string) (*packageSkeletons, error) {
	m.analysisMu.Lock()
	defer m.analysisMu.Unlock()
	m.maybeRebuildFset()
	m.applyDirty()
	fset := m.fset
	bag := diag.NewBag(fset)
	parsed, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	gsxFiles := parsed.files
	// This importer-free lane owns a separate parse, so it must route that AST
	// through the same materialize/JS-resolution/stamp/ID preprocessor as
	// retained analysis. This lane discards the registry after preprocessing,
	// but still requires the complete syntax and JavaScript gates before it may
	// derive body facts from this independently parsed AST.
	declNames := packageDeclNames(dir, gsxFiles)
	preprocessed, err := parsed.preprocessComponentCallSites(declNames, fset, m.classifierFor(dir), bag)
	if err != nil {
		return nil, err
	}
	if !preprocessed.analysisReady() {
		out := &packageSkeletons{gsxFset: fset, byGsx: map[string]fileSkeleton{}}
		if !preprocessed.syntaxOK {
			out.goParseDiags = bag.Sorted()
		}
		return out, nil
	}
	componentPlan := newComponentTargetPlan(gsxFiles, parsed.sources, bag)
	// Some diagnosed regions, notably direct element literals inside {{ }}, are
	// deliberately preserved in the registry while the rest of the package can
	// still be inspected. Their skeletons omit the whole invalid region, so using
	// those skeletons for import removal could delete an import whose only authored
	// reference is inside the preserved region. Block exactly the files carrying
	// preprocessing errors; an unpositioned error blocks the whole package because
	// it cannot be attributed safely. A positioned filename outside this parse's
	// exact file set is equally unattributable and therefore also blocks the
	// package rather than silently failing to protect any file.
	blockedFiles := make(map[string]bool)
	knownFiles := make(map[string]bool, len(gsxFiles))
	for path := range gsxFiles {
		knownFiles[filepath.Clean(path)] = true
	}
	blockPackage := false
	for _, d := range bag.Sorted() {
		if d.Severity != diag.Error {
			continue
		}
		path := filepath.Clean(d.Start.Filename)
		if d.Start.Filename == "" || !knownFiles[path] {
			blockPackage = true
			continue
		}
		blockedFiles[path] = true
	}
	table, err := m.filterTableFor(dir, false)
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	genericSigs := genericSigsFor(gsxFiles, byo)
	inferNames := newInferNameAllocator()
	out := &packageSkeletons{gsxFset: fset, byGsx: map[string]fileSkeleton{}}
	for path, f := range gsxFiles {
		if blockPackage || blockedFiles[filepath.Clean(path)] {
			continue
		}
		ff := m.fileScopedFacts(dir, f, propFields, nodeProps, attrsProps, byo, bag, fset)
		if ff.failed {
			// The dependency contract is intentionally unavailable. Building with
			// whatever sibling facts happened to succeed would create a partial
			// skeleton and could turn a dropped qualified component reference into
			// a false unused-import removal for this file.
			continue
		}
		skel, _, imps, _, infReg, _, berr := buildSkeleton(f, table, ff.propFields, ff.nodeProps, ff.attrsProps,
			genericSigs, ff.genericSigs, ff.byo, m.opts.FieldMatcher, fset, bag, inferNames, &componentPlan, skeletonFull)
		if berr != nil {
			continue // unbuildable → keep all imports (no entry)
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		absXpath := filepath.Join(dir, base+".x.go")
		gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
		if perr != nil {
			// The user's Go does not parse. Keep every import (no entry for this
			// file), but hand the positioned diagnostics up: this is the only place
			// the fmt path can see them, and `gsx fmt` must not silently succeed on
			// Go it could not format. A non-ErrorList fault yields no diagnostics
			// and is dropped, exactly as before.
			if ds, ok := diagnosticsFromSourceError(skeletonParseError(perr)); ok {
				out.goParseDiags = append(out.goParseDiags, ds...)
			}
			continue
		}
		sunk := map[sunkImportKey]bool{}
		if len(infReg.failedAliases) > 0 && ff.depAliasSpecs != nil {
			for alias := range infReg.failedAliases {
				if spec, ok := ff.depAliasSpecs[alias]; ok && spec.pos.IsValid() {
					sunk[sunkImportKey{line: fset.Position(spec.pos).Line, path: spec.path}] = true
				}
			}
		}
		out.byGsx[path] = fileSkeleton{skel: gf, imps: imps, sunk: sunk}
	}
	return out, nil
}

// resolvePackageNamesCalls counts invocations of resolvePackageNames — the
// only place in the unused-import classifier that shells out to `go list`
// (packages.Load). Test-only instrumentation (see
// TestPackageUnusedImportsDoesNotCallGoList) proving deterministically, not by
// timing, that the LSP's Package() path never hits it: candidate names are
// resolved from the already-type-checked *types.Package instead (see
// importNamesFromTypes). The CLI path (Module.UnusedImports) has no type
// information and keeps calling this.
var resolvePackageNamesCalls atomic.Int64

// resolvePackageNames returns the real package name for each import path, via a
// NeedName-only load (no type-checking, no dependency resolution). Unresolvable
// paths are simply absent from the result, so the caller keeps those imports.
func (m *Module) resolvePackageNames(paths []string) map[string]string {
	resolvePackageNamesCalls.Add(1)
	out := map[string]string{}
	if len(paths) == 0 {
		return out
	}
	cfg := &packages.Config{Mode: packages.NeedName, Dir: m.opts.ModuleRoot}
	pkgs, err := packages.Load(cfg, paths...)
	if err != nil {
		return out
	}
	for _, p := range pkgs {
		if p.PkgPath != "" && p.Name != "" {
			out[p.PkgPath] = p.Name
		}
	}
	return out
}

// importNamesFromTypes returns pkg's direct-import path -> declared package
// name, straight from the already-type-checked *types.Package — no
// packages.Load, no subprocess. types.Package.Imports() lists every directly
// imported package INCLUDING unused ones (verified: an unused "math/rand/v2"
// still appears, with name "rand"), which is exactly the candidate-resolution
// signal unusedFromSkeletons needs. A nil pkg (type-check failed before
// producing one) yields an empty map, so every candidate is conservatively
// kept by the caller — mirroring resolvePackageNames' own "absent path ⇒ keep"
// contract.
//
// Completeness gates every entry (imp.Complete()). Normal-mode cold inventory
// makes every authored GSX import a load root, so valid imports ordinarily have
// authoritative complete packages even when no generated output reaches them.
// An importer failure or another bounded importer can still leave go/types an
// incomplete placeholder named after the path's last segment ("v2" for
// "math/rand/v2"). Trusting that guessed name would make
// classifyUnusedImports' candidate check compare the file's used-name set
// against the fabricated name instead of the real one, so a live reference
// (e.g. `rand.IntN(3)`) is invisible and the import is reported unused — the
// LSP then deletes a working import out from under the user. Skipping
// incomplete imports here means the caller (unusedImportsCore's
// `!ok → continue`) conservatively KEEPS every such import — the same
// "absent path ⇒ keep" contract resolvePackageNames already provides for the
// CLI path. See docs/superpowers/specs/2026-07-09-lsp-unused-imports-design.md.
func importNamesFromTypes(pkg *types.Package) map[string]string {
	out := map[string]string{}
	if pkg == nil {
		return out
	}
	for _, imp := range pkg.Imports() {
		if !imp.Complete() {
			// Fabricated path-base placeholder — not a fact, do not trust it.
			continue
		}
		out[imp.Path()] = imp.Name()
	}
	return out
}

// unusedImportsCore is the ONE classifier body shared by both unused-import
// surfaces (Module.UnusedImports for the CLI, unusedFromSkeletons for the
// LSP's Package()): per file, skeletonUsedNames -> classifyUnusedImports finds
// definitely-unused imports and removal CANDIDATES (default imports whose path
// base isn't referenced), then resolveNames is called exactly once with every
// candidate path across the whole package, and each candidate's real name is
// checked against the file's used set before it is reported unused. An
// unresolvable candidate path (resolveNames' result has no entry) is
// conservatively kept, never removed.
func unusedImportsCore(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, resolveNames func(paths []string) map[string]string) map[string][]UnusedImport {
	out := map[string][]UnusedImport{}
	usedByFile := map[string]map[string]bool{}
	type pending struct {
		gsxPath string
		imp     importSpec
	}
	var candidates []pending
	candPaths := map[string]bool{}
	for gsxPath, fs := range byGsx {
		used := skeletonUsedNames(fs.skel)
		usedByFile[gsxPath] = used
		unused, cands := classifyUnusedImports(used, fs.imps, fs.sunk, gsxFset)
		if len(unused) > 0 {
			out[gsxPath] = unused
		}
		for _, c := range cands {
			candidates = append(candidates, pending{gsxPath, c})
			candPaths[c.path] = true
		}
	}
	if len(candPaths) == 0 {
		return out
	}
	paths := make([]string, 0, len(candPaths))
	for p := range candPaths {
		paths = append(paths, p)
	}
	names := resolveNames(paths)
	for _, p := range candidates {
		realName, ok := names[p.imp.path]
		if !ok {
			continue // unresolvable → conservative keep
		}
		if !usedByFile[p.gsxPath][realName] {
			out[p.gsxPath] = append(out[p.gsxPath], UnusedImport{Name: p.imp.name, Path: p.imp.path})
		}
	}
	return out
}

// unusedFromSkeletons is the LSP-facing entry point for unused-import
// detection: given the per-file skeletons analyze already built and the
// package it already type-checked, it classifies unused imports via
// unusedImportsCore, resolving candidate names from pkg (importNamesFromTypes)
// instead of a fresh packages.Load — no extra parse, no lock, no subprocess.
// See Module.Package's use of a.unusedImports and the design doc
// (docs/superpowers/specs/2026-07-09-lsp-unused-imports-design.md).
func unusedFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, pkg *types.Package) map[string][]UnusedImport {
	names := importNamesFromTypes(pkg)
	return unusedImportsCore(byGsx, gsxFset, func([]string) map[string]string { return names })
}

// UnusedImports returns, per .gsx file (abs path) in dir, the imports the file
// declares but never references — determined syntactically from the skeleton,
// with NO type-checking and NO dependency resolution. Default imports whose path
// base is not referenced have their real package name resolved via a single
// cheap NeedName load before removal, so a package whose name differs from its
// path base (e.g. gopkg.in/yaml.v3 → "yaml") is handled correctly.
//
// It also returns the Go parse diagnostics the skeletons produced, already
// positioned at their .gsx origin. They are diagnostics, not an error: a file
// whose Go does not parse simply keeps all its imports, and its siblings are
// still analyzed. The returned error stays reserved for faults that make the
// whole package unanalyzable (a gsx parse failure, an unloadable module), which
// callers must not present to the user as a Go diagnostic.
func (m *Module) UnusedImports(dir string) (map[string][]UnusedImport, []diag.Diagnostic, error) {
	ps, err := m.buildPackageSkeletons(dir)
	if err != nil {
		return nil, nil, err
	}
	out := unusedImportsCore(ps.byGsx, ps.gsxFset, m.resolvePackageNames)
	return out, ps.goParseDiags, nil
}
