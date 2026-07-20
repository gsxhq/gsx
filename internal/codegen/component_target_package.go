package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type componentTargetPackageResult struct {
	pkg             *types.Package
	info            *types.Info
	files           []*goast.File
	facts           map[callSiteID]componentTargetFact
	expressionFacts map[gsxast.Node]expressionFact
	diagnostics     []diag.Diagnostic
}

type componentTargetExpressionHarvest struct {
	parsed          *goast.File
	source          *gsxast.File
	embeddedMarkups [][]gsxast.Markup
}

func pairedTargetOutputs(gsxFiles map[string]*gsxast.File) map[string]bool {
	paired := make(map[string]bool, len(gsxFiles))
	for path := range gsxFiles {
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		paired[filepath.Join(filepath.Dir(path), base+".x.go")] = true
	}
	return paired
}

// parseTargetCompanionGoFiles loads the active hand-written Go surface while
// excluding exactly the generated output paired with each authoritative GSX
// file. Other .x.go files remain ordinary source; broad extension filtering
// would hide legitimate hand-written or orphan declarations.
func (m *Module) parseTargetCompanionGoFiles(dir string, gsxFiles map[string]*gsxast.File) ([]*goast.File, []string, error) {
	if m.opts.SourceOnly {
		return nil, nil, nil
	}
	paired := pairedTargetOutputs(gsxFiles)
	packageInfo, found, ready := m.targetSourcePackage(dir)
	if !ready && m.opts.Bundle == nil {
		// Normal mode has one authoritative Go-command source inventory. Ensure it
		// exists before answering instead of falling back to a build-oblivious disk
		// scan when a semantic caller reaches companion facts before another cold
		// load happened to run.
		if _, err := m.externalImporter(); err != nil {
			return nil, nil, err
		}
		packageInfo, found, ready = m.targetSourcePackage(dir)
	}
	if !ready {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Bundle callers may supply the entire package as in-memory GSX into a
			// virtual directory. A directory that does not exist cannot contain a
			// handwritten Go companion; the parsed override set is therefore the
			// complete source surface for this package.
			if os.IsNotExist(err) && len(gsxFiles) != 0 {
				return nil, nil, nil
			}
			return nil, nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(filepath.Clean(dir), entry.Name())
			if !paired[path] {
				return nil, nil, fmt.Errorf("codegen: exact component target discovery requires the normal module resolver to select active companion Go files in %s", dir)
			}
		}
		return nil, nil, nil
	}
	if !found {
		return nil, nil, nil
	}
	if len(packageInfo.metadataErrors) != 0 {
		messages := make([]string, 0, len(packageInfo.metadataErrors))
		for _, loadErr := range packageInfo.metadataErrors {
			messages = append(messages, loadErr.Error())
		}
		return nil, nil, fmt.Errorf("codegen: cannot determine active companion Go files in %s: %s", dir, strings.Join(messages, "; "))
	}
	if len(packageInfo.invariantErrors) != 0 {
		return nil, nil, fmt.Errorf("codegen: incomplete active companion syntax inventory in %s: %s", dir, strings.Join(packageInfo.invariantErrors, "; "))
	}
	var files []*goast.File
	var imports []string
	for _, path := range packageInfo.compiledGoFiles {
		if paired[path] {
			continue
		}
		file := packageInfo.syntaxByFile[path]
		if file == nil {
			return nil, nil, fmt.Errorf("codegen: active companion syntax missing for %s", path)
		}
		files = append(files, file)
		for _, spec := range file.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil {
				imports = append(imports, path)
			}
		}
	}
	return files, imports, nil
}

func discoverComponentTargets(
	module *Module,
	dir, pkgPath, pkgName string,
	gsxFiles map[string]*gsxast.File,
	gsxSources map[string][]byte,
	plan componentTargetPlan,
	callSites *callSiteRegistry,
	table funcTables,
	fset *token.FileSet,
	bag *diag.Bag,
	importer types.Importer,
	typeEnvironment typeCheckEnvironment,
) (componentTargetPackageResult, []types.Error, error) {
	if bag.HasErrors() {
		return componentTargetPackageResult{diagnostics: bag.Sorted()}, nil, nil
	}
	markers, err := newComponentTargetMarkerRegistry(callSites)
	if err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	paths := make([]string, 0, len(gsxFiles))
	for path := range gsxFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var goFiles []*goast.File
	var importPaths []string
	var expressionHarvests []componentTargetExpressionHarvest
	for _, path := range paths {
		markerStart := len(markers.ordered)
		skeleton, err := buildComponentTargetSkeleton(
			gsxFiles[path], table, fset, bag, markers, plan, skeletonTargetDiscovery,
		)
		if err != nil {
			return componentTargetPackageResult{}, nil, err
		}
		for _, spec := range skeleton.imports {
			importPaths = append(importPaths, spec.path)
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		skeletonPath := filepath.Join(dir, base+".target.x.go")
		parsed, err := goparser.ParseFile(fset, skeletonPath, skeleton.source, goparser.SkipObjectResolution)
		if err != nil {
			return componentTargetPackageResult{}, nil, skeletonParseError(err)
		}
		if err := bindComponentTargetMarkers(parsed, markerStart, fset, markers); err != nil {
			return componentTargetPackageResult{}, nil, err
		}
		goFiles = append(goFiles, parsed)
		expressionHarvests = append(expressionHarvests, componentTargetExpressionHarvest{
			parsed:          parsed,
			source:          gsxFiles[path],
			embeddedMarkups: skeleton.embeddedMarkups,
		})
	}
	if bag.HasErrors() {
		return componentTargetPackageResult{diagnostics: bag.Sorted()}, nil, nil
	}
	if err := markers.validateComplete(); err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	preludePath := filepath.Join(dir, "_gsxtarget_shared.x.go")
	prelude, err := goparser.ParseFile(fset, preludePath, analysisPreludeSource(pkgName), goparser.SkipObjectResolution)
	if err != nil {
		return componentTargetPackageResult{}, nil, skeletonParseError(err)
	}
	goFiles = append(goFiles, prelude)
	companions, companionImports, err := module.parseTargetCompanionGoFiles(dir, gsxFiles)
	if err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	goFiles = append(goFiles, companions...)
	importPaths = append(importPaths, companionImports...)
	if err := module.rejectExternalBackedgeImports(goFiles); err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	// Publish the complete direct path surface before recursive type-checking.
	// These path-only edges are invalidation provenance, not semantic package
	// facts: they must survive an imported declaration failure while packages,
	// target facts, and type objects remain unpublished.
	module.recordTargetImports(dir, importPaths)

	pkg, info, typeErrs := checkComponentTargetPackage(pkgPath, pkgName, goFiles, fset, importer, componentTargetCheckConfig{typeEnvironment: typeEnvironment})
	facts, unrelated, err := harvestComponentTargetFacts(goFiles, fset, info, typeErrs, markers)
	if err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	rootProvenance, err := componentTargetDeclarationProvenances(gsxFiles, gsxSources, fset, plan)
	if err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	for site, fact := range facts {
		identity := fact.origin
		if identity == nil {
			identity = fact.object
		}
		key := componentCallTargetKey(identity)
		switch {
		case identity == nil || identity.Pkg() == nil || key == "":
		case identity.Pkg().Path() == pkgPath:
			fact.declaration = cloneComponentTargetDeclarationProvenance(rootProvenance[key])
		default:
			fact.declaration = module.componentTargetDeclarationProvenance(identity.Pkg().Path(), key)
		}
		facts[site] = fact
	}
	expressionFacts := make(map[gsxast.Node]expressionFact)
	for _, harvest := range expressionHarvests {
		maps.Copy(expressionFacts, harvestComponentTargetExpressionFacts(
			harvest.parsed, harvest.source, pkg, info, fset,
			harvest.embeddedMarkups, plan, callSites,
		))
	}
	return componentTargetPackageResult{
		pkg:             pkg,
		info:            info,
		files:           goFiles,
		facts:           facts,
		expressionFacts: expressionFacts,
		diagnostics:     bag.Sorted(),
	}, unrelated, nil
}

func (m *Module) componentTargetDeclarationProvenance(packagePath, key string) componentTargetDeclarationProvenance {
	return cloneComponentTargetDeclarationProvenance(m.componentTargetPackageProvenance(packagePath)[key])
}

func (m *Module) componentTargetPackageProvenance(packagePath string) map[string]componentTargetDeclarationProvenance {
	dir, ok := m.sourcePackageDir(packagePath)
	if !ok {
		return nil
	}
	m.mu.Lock()
	cached := m.targetDeclProvenance[dir]
	provenance := make(map[string]componentTargetDeclarationProvenance, len(cached))
	for key, declaration := range cached {
		provenance[key] = cloneComponentTargetDeclarationProvenance(declaration)
	}
	m.mu.Unlock()
	return provenance
}

// harvestComponentTargetExpressionFacts resolves authored component-call
// operands from the exact discovery skeleton that was type-checked to resolve
// their targets. The skeleton's probe registry and embedded-markup index are
// part of that artifact: rebuilding either after checking could associate a
// source node with a different expression AST.
func harvestComponentTargetExpressionFacts(
	parsed *goast.File,
	source *gsxast.File,
	pkg *types.Package,
	info *types.Info,
	fset *token.FileSet,
	embeddedMarkups [][]gsxast.Markup,
	plan componentTargetPlan,
	candidates *callSiteRegistry,
) map[gsxast.Node]expressionFact {
	resolved := make(map[gsxast.Node]types.Type)
	expressions := make(map[gsxast.Node]goast.Expr)
	var components []*gsxast.Component
	for _, declaration := range source.Decls {
		if component, ok := declaration.(*gsxast.Component); ok {
			components = append(components, component)
		}
	}
	harvest(parsed, components, info, resolved, expressions, &plan, candidates)
	harvestEmbeddedElements(parsed, embeddedMarkups, info, resolved, expressions, candidates)

	facts := make(map[gsxast.Node]expressionFact, len(expressions))
	for node, expr := range expressions {
		// The variadic _gsxuse/_gsxuseq probe gives the package checker enough
		// context to validate every shape, including tuples, but assigning an
		// untyped constant to its `any` element type defaults that constant. Recheck
		// the SAME AST node at the SAME lexical position without an assignment/call
		// context. CheckExpr neither reconstructs nor reparses source, and therefore
		// recovers the authored TypeAndValue while resolving locals against the
		// already-checked package's scope tree.
		exactInfo := &types.Info{Types: make(map[goast.Expr]types.TypeAndValue)}
		if err := types.CheckExpr(fset, pkg, expr.Pos(), expr, exactInfo); err != nil {
			// The package check already owns expression diagnostics. An invalid
			// expression has no authoritative standalone operand fact to publish.
			continue
		}
		tv, ok := exactInfo.Types[expr]
		if !ok {
			continue
		}
		fact := expressionFact{
			tv:                  tv,
			isNil:               tv.IsNil(),
			hasOrderedOperation: expressionHasOrderedOperation(expr),
		}
		fact.tuple, _ = tv.Type.(*types.Tuple)
		facts[node] = fact
	}
	return facts
}
