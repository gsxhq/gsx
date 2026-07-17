package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/types"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

// sourceDeclResolver reconstructs declaration-only shipping packages from the
// authoritative local source inventory. It is shared by every configured
// function provider (filters, aliases, renderers, and class mergers), so none
// of those consumers can observe module-local export data from the cold load.
// Declaration mode intentionally omits component bodies, breaking the otherwise
// circular dependency in which resolving a GSX filter package would itself need
// the completed filter table.
type sourceDeclResolver struct {
	m         *Module
	external  types.Importer
	pkgs      map[string]*types.Package
	loading   map[string]bool
	sourceErr error
}

func newSourceDeclResolver(m *Module, external types.Importer) *sourceDeclResolver {
	return &sourceDeclResolver{
		m:        m,
		external: external,
		pkgs:     map[string]*types.Package{},
		loading:  map[string]bool{},
	}
}

func newConfiguredSourceDeclResolver(m *Module, external types.Importer) *sourceDeclResolver {
	m.mu.Lock()
	if m.configuredDeclTypes == nil {
		m.configuredDeclTypes = map[string]*types.Package{}
	}
	packages := m.configuredDeclTypes
	m.mu.Unlock()
	return &sourceDeclResolver{
		m:        m,
		external: external,
		pkgs:     packages,
		loading:  map[string]bool{},
	}
}

func (r *sourceDeclResolver) packageForDir(dir string) (*types.Package, error) {
	if pkg, ok := r.pkgs[dir]; ok {
		return pkg, nil
	}
	if r.loading[dir] {
		return nil, fmt.Errorf("import cycle through %s", dir)
	}
	r.loading[dir] = true
	defer delete(r.loading, dir)

	fset := r.m.fset
	parsed, err := r.m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	gsxFiles, pkgName := parsed.files, parsed.name
	sourcePackage, sourceFound, inventoryReady := r.m.targetSourcePackage(dir)
	if pkgName == "" && sourceFound {
		pkgName = sourcePackage.name
	}
	if len(gsxFiles) == 0 && (!inventoryReady || !sourceFound) {
		return nil, fmt.Errorf("renderer declaration package %s has neither GSX source nor retained compiled Go source", dir)
	}
	if pkgName == "" {
		return nil, fmt.Errorf("renderer declaration package %s has no package name", dir)
	}
	companions, companionImports, err := r.m.parseTargetCompanionGoFiles(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	bag := diag.NewBag(fset)
	var preprocessed callSitePreprocessResult
	if len(gsxFiles) != 0 {
		declNames := packageDeclNamesFromFiles(companions, gsxFiles)
		preprocessed, err = parsed.preprocessComponentCallSites(declNames, fset, r.m.classifierFor(dir), bag)
		if err != nil {
			return nil, err
		}
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	if err := r.m.validateBundleProjectImports(gsxFiles, fset); err != nil {
		return nil, err
	}
	pkgPath := ""
	if sourceFound {
		pkgPath = sourcePackage.pkgPath
	}
	if pkgPath == "" {
		var ok bool
		pkgPath, ok = importPathForDir(r.m.opts.ModuleRoot, r.m.opts.ModulePath, dir)
		if !ok {
			return nil, fmt.Errorf("renderer declaration package %s is outside module root %s", dir, r.m.opts.ModuleRoot)
		}
	}
	typeEnvironment, err := r.m.typeCheckEnvironmentForDir(dir)
	if err != nil {
		return nil, err
	}
	componentPlan, err := r.m.finalizedComponentTargetPlan(dir, pkgPath, pkgName, gsxFiles, parsed.sources, fset, bag, r, typeEnvironment)
	if err != nil {
		return nil, err
	}
	if len(gsxFiles) != 0 {
		if err := componentPreprocessFailure(dir, preprocessed, bag); err != nil {
			return nil, err
		}
	}
	// Declaration skeletons stub component and embedded-markup bodies. They do
	// not consult configured filters or renderers, which is what makes this
	// resolver a non-circular source of those functions' own signatures.
	table := funcTables{}
	goFiles := make([]*goast.File, 0, len(gsxFiles))
	var importPaths []string
	for path, file := range gsxFiles {
		skeleton, _, imports, _, _, buildErr := buildSkeleton(
			file,
			table,
			fset,
			bag,
			&componentPlan,
			skeletonDeclarations,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		for _, spec := range imports {
			importPaths = append(importPaths, spec.path)
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		xgoPath := filepath.Join(dir, base+".x.go")
		file, parseErr := goparser.ParseFile(fset, xgoPath, skeleton, goparser.SkipObjectResolution)
		if parseErr != nil {
			return nil, skeletonParseError(parseErr)
		}
		goFiles = append(goFiles, file)
	}

	goFiles = append(goFiles, companions...)
	importPaths = append(importPaths, companionImports...)
	if err := r.m.rejectExternalBackedgeImports(goFiles); err != nil {
		return nil, err
	}
	// Publish the renderer root's exact-source edges before recursive checking.
	// Go-only intermediaries remain internal graph nodes so a leaf edit reaches
	// the configured renderer root and invalidates the module-wide renderer table.
	r.m.recordSourceDeclImports(dir, importPaths)
	var typeErrs []error
	conf := types.Config{
		Importer:  r,
		Sizes:     typeEnvironment.sizes,
		GoVersion: typeEnvironment.goVersion,
		Error: func(err error) {
			if typeErr, ok := err.(types.Error); ok && isUnusedImportMsg(typeErr.Msg) {
				return
			}
			typeErrs = append(typeErrs, err)
		},
	}
	pkg := types.NewPackage(pkgPath, pkgName)
	checker := types.NewChecker(&conf, fset, pkg, nil)
	_ = checker.Files(goFiles)
	if r.sourceErr != nil {
		return nil, r.sourceErr
	}
	if err := errors.Join(typeErrs...); err != nil {
		return nil, err
	}
	r.pkgs[dir] = pkg
	return pkg, nil
}

func (r *sourceDeclResolver) Import(path string) (*types.Package, error) {
	if dir, ok := r.m.sourcePackageDir(path); ok {
		// One renderer declaration universe owns every authoritative local
		// package: GSX dirs contribute shipping/renderer skeletons, while Go-only
		// dirs contribute their retained compiled syntax. Recursing through r
		// preserves package identity and prevents a bridge from observing exact
		// verbatim declarations while a direct dependency observes Props ABI.
		pkg, err := r.packageForDir(dir)
		if err != nil && r.sourceErr == nil {
			if _, ok := diagnosticsFromSourceError(err); ok {
				r.sourceErr = err
			}
		}
		return pkg, err
	}
	return r.m.importWithBundleProjectBoundary(path, r.external)
}
