package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type componentTargetPackageResult struct {
	pkg         *types.Package
	info        *types.Info
	files       []*goast.File
	facts       map[callSiteID]componentTargetFact
	imports     []string
	diagnostics []diag.Diagnostic
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
	paired := pairedTargetOutputs(gsxFiles)
	packageInfo, found, ready := m.targetSourcePackage(dir)
	if !ready {
		entries, err := os.ReadDir(dir)
		if err != nil {
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
	plan componentTargetPlan,
	callSites *callSiteRegistry,
	table funcTables,
	factsByFile map[string]*fileFacts,
	fieldMatcher FieldMatcher,
	fset *token.FileSet,
	bag *diag.Bag,
	importer types.Importer,
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
	for _, path := range paths {
		markerStart := len(markers.ordered)
		fileFacts := factsByFile[path]
		if fileFacts == nil {
			return componentTargetPackageResult{}, nil, &targetPackageInvariantError{message: "missing file-scoped facts for " + path}
		}
		skeleton, err := buildComponentTargetSkeleton(
			gsxFiles[path], table,
			fileFacts.propFields, fileFacts.nodeProps, fileFacts.attrsProps,
			fileFacts.byo, fieldMatcher, fset, bag, markers, plan, skeletonTargetDiscovery,
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

	pkg, info, typeErrs := checkComponentTargetPackage(pkgPath, pkgName, goFiles, fset, importer, componentTargetCheckConfig{})
	validateComponentVariantSignatures(goFiles, info, plan, bag)
	facts, unrelated, err := harvestComponentTargetFacts(goFiles, fset, info, typeErrs, markers)
	if err != nil {
		return componentTargetPackageResult{}, nil, err
	}
	return componentTargetPackageResult{pkg: pkg, info: info, files: goFiles, facts: facts, imports: importPaths, diagnostics: bag.Sorted()}, unrelated, nil
}

type targetPackageInvariantError struct {
	message string
}

func (e *targetPackageInvariantError) Error() string {
	return "codegen: target discovery: " + e.message
}
