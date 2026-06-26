package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
)

// importPathForDir maps an absolute package dir under moduleRoot to its Go
// import path. ok is false when dir is not within moduleRoot.
func importPathForDir(moduleRoot, modulePath, dir string) (string, bool) {
	rel, err := filepath.Rel(moduleRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return modulePath, true
	}
	return modulePath + "/" + filepath.ToSlash(rel), true
}

// dirForImportPath is the inverse of importPathForDir. ok is false when
// importPath is not under modulePath (e.g. stdlib or third-party).
func dirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return moduleRoot, true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(importPath, prefix)
	return filepath.Join(moduleRoot, filepath.FromSlash(rel)), true
}

// checkSkeletonPackage type-checks already-parsed package files against imp and
// returns the resulting *types.Package + *types.Info. Type errors are collected
// (not fatal): go/types fills Info best-effort even when some files don't check,
// matching the existing CachedResolver.check behaviour.
func checkSkeletonPackage(dir, pkgName string, files []*goast.File, fset *token.FileSet, imp types.Importer) (*types.Package, *types.Info, []types.Error) {
	info := &types.Info{
		Types: map[goast.Expr]types.TypeAndValue{},
		Defs:  map[*goast.Ident]types.Object{},
		Uses:  map[*goast.Ident]types.Object{},
	}
	var errs []types.Error
	conf := types.Config{
		Importer: imp,
		Error: func(e error) {
			if te, ok := e.(types.Error); ok {
				errs = append(errs, te)
			}
		},
	}
	pkg := types.NewPackage(dir, pkgName)
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files)
	return pkg, info, errs
}
