package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	"go/build"
	goparser "go/parser"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

type rendererDeclResolver struct {
	m        *Module
	external types.Importer
	pkgs     map[string]*types.Package
	loading  map[string]bool
}

func newRendererDeclResolver(m *Module, external types.Importer) *rendererDeclResolver {
	return &rendererDeclResolver{
		m:        m,
		external: external,
		pkgs:     map[string]*types.Package{},
		loading:  map[string]bool{},
	}
}

func (r *rendererDeclResolver) packageForDir(dir string) (*types.Package, error) {
	if pkg, ok := r.pkgs[dir]; ok {
		return pkg, nil
	}
	if r.loading[dir] {
		return nil, fmt.Errorf("import cycle through %s", dir)
	}
	r.loading[dir] = true
	defer delete(r.loading, dir)

	fset := r.m.fset
	gsxFiles, pkgName, err := r.m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	table, err := r.tablesForDir(dir)
	if err != nil {
		return nil, err
	}
	genericSigs := genericSigsFor(gsxFiles, byo)
	declNames := packageDeclNames(dir, gsxFiles)
	bag := diag.NewBag(fset)
	inferNames := newInferNameAllocator()
	goFiles := make([]*goast.File, 0, len(gsxFiles))
	skeletonPaths := make(map[string]bool, len(gsxFiles))
	for path, file := range gsxFiles {
		skeleton, _, _, _, _, _, buildErr := buildSkeleton(
			file,
			table,
			propFields,
			nodeProps,
			attrsProps,
			genericSigs,
			nil,
			byo,
			r.m.opts.FieldMatcher,
			fset,
			r.m.opts.Classifier,
			bag,
			inferNames,
			declNames,
			skeletonDeclarations,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		xgoPath := filepath.Join(dir, base+".x.go")
		file, parseErr := goparser.ParseFile(fset, xgoPath, skeleton, goparser.SkipObjectResolution)
		if parseErr != nil {
			return nil, skeletonParseError(parseErr)
		}
		goFiles = append(goFiles, file)
		skeletonPaths[xgoPath] = true
	}

	bp, buildErr := build.ImportDir(dir, 0)
	if buildErr != nil {
		var noGoErr *build.NoGoError
		if !errors.As(buildErr, &noGoErr) {
			return nil, buildErr
		}
	} else {
		for _, name := range bp.GoFiles {
			path := filepath.Join(dir, name)
			if skeletonPaths[path] {
				continue
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, fmt.Errorf("read active Go companion %s: %w", path, readErr)
			}
			file, parseErr := goparser.ParseFile(fset, path, src, goparser.SkipObjectResolution)
			if parseErr != nil {
				return nil, fmt.Errorf("parse active Go companion %s: %w", path, parseErr)
			}
			goFiles = append(goFiles, file)
		}
	}

	pkgPath, ok := importPathForDir(r.m.opts.ModuleRoot, r.m.opts.ModulePath, dir)
	if !ok {
		return nil, fmt.Errorf("renderer declaration package %s is outside module root %s", dir, r.m.opts.ModuleRoot)
	}
	var typeErrs []error
	conf := types.Config{
		Importer:         r,
		IgnoreFuncBodies: true,
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
	if err := errors.Join(typeErrs...); err != nil {
		return nil, err
	}
	r.pkgs[dir] = pkg
	return pkg, nil
}

func (r *rendererDeclResolver) tablesForDir(dir string) (funcTables, error) {
	if r.m.opts.Bundle != nil {
		table := r.m.opts.Bundle.tables()
		table.renderers = nil
		return table, nil
	}
	pkgs := r.m.opts.FilterPkgs
	if opts, ok := r.m.dirOptionsFor(dir); ok && opts.FilterPkgs != nil {
		pkgs = opts.FilterPkgs
	}
	filters, err := r.m.filterTableFromExt(dedupFilterPkgs(pkgs))
	if err != nil {
		return funcTables{}, err
	}
	return funcTables{filters: filters}, nil
}

func (r *rendererDeclResolver) Import(path string) (*types.Package, error) {
	if dir, ok := dirForImportPath(r.m.opts.ModuleRoot, r.m.opts.ModulePath, path); ok && r.m.isGsxPackage(dir) {
		return r.packageForDir(dir)
	}
	return r.external.Import(path)
}
