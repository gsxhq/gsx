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
	module   *Module
	external types.Importer
	loading  map[string]bool
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
	if dir, ok := importer.module.exactTargetPackageDir(path); ok {
		return importer.module.targetDeclarationPackage(dir, importer)
	}
	if importer.external == nil {
		return nil, fmt.Errorf("codegen: exact component target importer has no external importer for %q", path)
	}
	return importer.external.Import(path)
}

func targetTypeErrorsAsSourceError(typeErrs []types.Error) error {
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
	bag := diag.NewBag(m.fset)
	var preprocessed callSitePreprocessResult
	if len(gsxFiles) != 0 {
		preprocessed, err = parsed.preprocessComponentCallSites(packageDeclNames(dir, gsxFiles), m.fset, m.classifierFor(dir), bag)
		if err != nil {
			return nil, err
		}
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}

	paths := make([]string, 0, len(gsxFiles))
	for path := range gsxFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	plan := newComponentTargetPlan(gsxFiles, parsed.sources, bag)
	if len(gsxFiles) != 0 {
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	var files []*goast.File
	var importPaths []string
	for _, path := range paths {
		skeleton, err := buildComponentTargetSkeleton(
			gsxFiles[path], funcTables{}, nil, nil, nil, nil,
			m.opts.FieldMatcher, m.fset, bag, nil, plan, skeletonTargetDeclarations,
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
	companions, companionImports, err := m.parseTargetCompanionGoFiles(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	files = append(files, companions...)
	importPaths = append(importPaths, companionImports...)
	if len(files) == 0 {
		return nil, fmt.Errorf("codegen: exact component target package %s has no source files", dir)
	}

	pkgPath := dir
	if sourceFound && sourcePackage.pkgPath != "" {
		pkgPath = sourcePackage.pkgPath
	} else if path, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir); ok {
		pkgPath = path
	}
	pkg, info, typeErrs := checkComponentTargetPackage(pkgPath, pkgName, files, m.fset, importer, componentTargetCheckConfig{
		ignoreFuncBodies:         true,
		disableUnusedImportCheck: true,
	})
	validateComponentVariantSignatures(files, info, plan, bag)
	if len(gsxFiles) != 0 {
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	if err := targetTypeErrorsAsSourceError(typeErrs); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.targetDeclTypes == nil {
		m.targetDeclTypes = map[string]*types.Package{}
	}
	m.targetDeclTypes[dir] = pkg
	m.mu.Unlock()
	m.recordTargetImports(dir, importPaths)
	return pkg, nil
}
