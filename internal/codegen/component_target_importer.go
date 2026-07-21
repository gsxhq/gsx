package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

// componentTargetImporter resolves module-local GSX packages from current
// exact-signature declaration skeletons and delegates every other import to the
// already-warm external importer. loading is a per-analysis recursion stack;
// completed packages live only in Module.targetDeclTypes.
type componentTargetImporter struct {
	module    *Module
	external  types.Importer
	loading   map[string]bool
	sourceErr error
}

func newComponentTargetImporter(module *Module, external types.Importer) *componentTargetImporter {
	return &componentTargetImporter{
		module:   module,
		external: external,
		loading:  map[string]bool{},
	}
}

func (importer *componentTargetImporter) Import(path string) (*types.Package, error) {
	if importer == nil || importer.module == nil {
		return nil, fmt.Errorf("codegen: nil exact component target importer")
	}
	if dir, ok := importer.module.sourcePackageDir(path); ok {
		pkg, err := importer.module.targetDeclarationPackage(dir, importer)
		if err != nil && importer.sourceErr == nil {
			if _, ok := diagnosticsFromSourceError(err); ok {
				importer.sourceErr = err
			}
		}
		return pkg, err
	}
	if importer.external == nil {
		return nil, fmt.Errorf("codegen: exact component target importer has no external importer for %q", path)
	}
	return importer.module.importWithBundleProjectBoundary(path, importer.external)
}

func typeErrorsAsSourceError(typeErrs []types.Error) error {
	if len(typeErrs) == 0 {
		return nil
	}
	diagnostics := make([]diag.Diagnostic, 0, len(typeErrs))
	for _, typeErr := range typeErrs {
		position := typeErr.Fset.Position(typeErr.Pos)
		diagnostics = append(diagnostics, diag.Diagnostic{
			Start:    position,
			End:      position,
			Severity: diag.Error,
			Message:  typeErr.Msg,
			Source:   "types",
		})
	}
	return sourceDiagnosticsError{diags: diagnostics}
}

func (m *Module) targetDeclarationPackage(dir string, importer *componentTargetImporter) (*types.Package, error) {
	m.mu.Lock()
	if pkg := m.targetDeclTypes[dir]; pkg != nil {
		m.mu.Unlock()
		return pkg, nil
	}
	m.mu.Unlock()

	if importer.loading[dir] {
		return nil, fmt.Errorf("import cycle through %s", dir)
	}
	importer.loading[dir] = true
	defer delete(importer.loading, dir)

	parsed, err := m.parsePackageWithFset(dir, m.fset)
	if err != nil {
		return nil, err
	}
	gsxFiles, pkgName := parsed.files, parsed.name
	sourcePackage, sourceFound, inventoryReady := m.targetSourcePackage(dir)
	if pkgName == "" && sourceFound {
		pkgName = sourcePackage.name
	}
	if len(gsxFiles) == 0 && (!inventoryReady || !sourceFound) {
		return nil, fmt.Errorf("codegen: exact component target package %s has neither GSX source nor retained compiled Go source", dir)
	}
	if pkgName == "" {
		return nil, fmt.Errorf("codegen: exact component target package %s has no package name", dir)
	}
	companions, companionImports, err := m.parseTargetCompanionGoFiles(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	bag := diag.NewBag(m.fset)
	var preprocessed callSitePreprocessResult
	if len(gsxFiles) != 0 {
		preprocessed, err = parsed.preprocessComponentCallSites(packageDeclNamesFromFiles(companions, gsxFiles), m.fset, m.classifierFor(dir), bag)
		if err != nil {
			return nil, err
		}
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	if err := m.validateBundleProjectImports(gsxFiles, m.fset); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(gsxFiles))
	for path := range gsxFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	pkgPath := dir
	if sourceFound && sourcePackage.pkgPath != "" {
		pkgPath = sourcePackage.pkgPath
	} else if path, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir); ok {
		pkgPath = path
	}
	typeEnvironment, err := m.typeCheckEnvironmentForDir(dir)
	if err != nil {
		return nil, err
	}
	plan, err := m.finalizedComponentTargetPlan(dir, pkgPath, pkgName, gsxFiles, parsed.sources, m.fset, bag, importer, typeEnvironment)
	if err != nil {
		return nil, err
	}
	if len(gsxFiles) != 0 {
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	var files []*goast.File
	var importPaths []string
	for _, path := range paths {
		skeleton, err := buildComponentTargetSkeleton(
			gsxFiles[path], funcTables{}, m.fset, bag, nil, plan, skeletonTargetDeclarations,
		)
		if err != nil {
			return nil, err
		}
		skeletonPath := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".gsx")+".target.x.go")
		file, err := goparser.ParseFile(m.fset, skeletonPath, skeleton.source, goparser.SkipObjectResolution)
		if err != nil {
			return nil, skeletonParseError(err)
		}
		files = append(files, file)
		for _, spec := range skeleton.imports {
			importPaths = append(importPaths, spec.path)
		}
	}
	if len(gsxFiles) != 0 {
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	files = append(files, companions...)
	importPaths = append(importPaths, companionImports...)
	if len(files) == 0 {
		return nil, fmt.Errorf("codegen: exact component target package %s has no source files", dir)
	}
	if err := m.rejectExternalBackedgeImports(files); err != nil {
		return nil, err
	}
	// The direct syntactic/path surface is complete now. Replace its path-only
	// invalidation edges before any recursive type-check can fail; semantic cache
	// publication remains gated on the successful checker result below.
	m.recordTargetImports(dir, importPaths)

	pkg, _, typeErrs := checkComponentTargetPackage(pkgPath, pkgName, files, m.fset, importer, componentTargetCheckConfig{
		ignoreFuncBodies:         true,
		disableUnusedImportCheck: true,
		typeEnvironment:          typeEnvironment,
	})
	if importer.sourceErr != nil {
		return nil, importer.sourceErr
	}
	if len(gsxFiles) != 0 {
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	if err := typeErrorsAsSourceError(typeErrs); err != nil {
		return nil, err
	}
	provenance, err := componentTargetDeclarationProvenances(gsxFiles, parsed.sources, m.fset, plan)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.targetDeclTypes == nil {
		m.targetDeclTypes = map[string]*types.Package{}
	}
	if m.targetDeclProvenance == nil {
		m.targetDeclProvenance = componentTargetProvenanceCache{}
	}
	m.targetDeclTypes[dir] = pkg
	m.targetDeclProvenance[dir] = provenance
	m.mu.Unlock()
	return pkg, nil
}
