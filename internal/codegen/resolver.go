package codegen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// typeResolver turns a skeleton overlay (path -> Go source) into the per-file
// type info harvest consumes. The default uses packages.Load (go list); the
// cached impl uses a prebuilt importer + go/types (no subprocess).
type typeResolver interface {
	check(dir string, overlay map[string][]byte, fset *token.FileSet) (files []*goast.File, info *types.Info, err error)
}

// packagesLoadResolver is the default (unchanged) behavior.
type packagesLoadResolver struct{}

func (packagesLoadResolver) check(dir string, overlay map[string][]byte, fset *token.FileSet) ([]*goast.File, *types.Info, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: dir, Overlay: overlay, Fset: fset,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("codegen: load package: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("codegen: no package found in %s", dir)
	}
	if len(pkgs[0].Errors) > 0 {
		return nil, nil, fmt.Errorf("codegen: type resolution failed: %s", pkgs[0].Errors[0])
	}
	return pkgs[0].Syntax, pkgs[0].TypesInfo, nil
}

// cachedResolver uses a prebuilt importer (no subprocess per render).
// Its dependencies are loaded once via packages.Load against moduleDir; each
// per-file check runs entirely in-process via go/types.
type cachedResolver struct {
	imp   types.Importer
	table filterTable
}

// newCachedResolver loads filterPkgs (plus "github.com/gsxhq/gsx" and
// allowImports) once from moduleDir and returns a resolver whose check method
// runs without any subprocess. The returned resolver's filters() method exposes
// the prebuilt filterTable so callers can skip a second loadFilterTableMulti.
func newCachedResolver(moduleDir string, filterPkgs []string, allowImports []string) (*cachedResolver, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  moduleDir,
	}
	loadPaths := []string{"github.com/gsxhq/gsx"}
	loadPaths = append(loadPaths, filterPkgs...)
	loadPaths = append(loadPaths, allowImports...)
	pkgs, err := packages.Load(cfg, loadPaths...)
	if err != nil {
		return nil, err
	}
	m := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			m[p.PkgPath] = p.Types
		}
	})
	table, err := loadFilterTableMulti(moduleDir, filterPkgs)
	if err != nil {
		return nil, err
	}
	return &cachedResolver{imp: mapImporter(m), table: table}, nil
}

// filters returns the prebuilt filterTable so callers can skip a second
// loadFilterTableMulti when using this resolver.
func (c *cachedResolver) filters() filterTable { return c.table }

func (c *cachedResolver) check(dir string, overlay map[string][]byte, fset *token.FileSet) ([]*goast.File, *types.Info, error) {
	var files []*goast.File
	for path, src := range overlay {
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, f)
	}
	info := &types.Info{Types: map[goast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: c.imp, Error: func(error) {}} // collect, never fatal
	pkg := types.NewPackage("views", "views")
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files) // type errors surface as a later diagnostic, not here
	return files, info, nil
}

// mapImporter implements types.Importer using a prebuilt map of package paths
// to *types.Package values.
type mapImporter map[string]*types.Package

func (m mapImporter) Import(path string) (*types.Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("cached importer: %q not loaded", path)
}
