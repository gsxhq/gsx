package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	gsxparser "github.com/gsxhq/gsx/parser"
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

// moduleImporter resolves a project gsx package from the warm graph (skeletons)
// and everything else from external. seen breaks the (DAG-guaranteed-acyclic)
// recursion defensively.
type moduleImporter struct {
	m        *Module
	external types.Importer
	seen     map[string]bool
}

func (mi *moduleImporter) Import(path string) (*types.Package, error) {
	if dir, ok := dirForImportPath(mi.m.opts.ModuleRoot, mi.m.opts.ModulePath, path); ok {
		if mi.m.isGsxPackage(dir) {
			if mi.seen[dir] {
				// cycle guard (shouldn't happen — Go forbids import cycles)
				if p, ok := mi.m.pkgTypes[dir]; ok {
					return p, nil
				}
			}
			return mi.m.typesPackageWith(dir, mi)
		}
	}
	return mi.external.Import(path)
}

// isGsxPackage reports whether dir contains at least one .gsx file (disk or
// override).
func (m *Module) isGsxPackage(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if len(matches) > 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for p := range m.overrides {
		if filepath.Dir(p) == dir && strings.HasSuffix(p, ".gsx") {
			return true
		}
	}
	return false
}

// typesPackage type-checks dir's skeletons (building a fresh importer rooted at
// dir) and returns/caches the *types.Package. Entry point for external callers.
func (m *Module) typesPackage(dir string) (*types.Package, error) {
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	return m.typesPackageWith(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
}

// typesPackageWith does the work, threading the recursive importer.
func (m *Module) typesPackageWith(dir string, mi *moduleImporter) (*types.Package, error) {
	m.mu.Lock()
	if p, ok := m.pkgTypes[dir]; ok {
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()
	mi.seen[dir] = true

	// Use a single fset shared across parse + skeleton so //line directives from
	// buildSkeleton reference valid positions from the same FileSet.
	fset := token.NewFileSet()
	gsxFiles, pkgName, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	table, err := loadFilterTableMulti(m.opts.ModuleRoot, m.opts.FilterPkgs, m.opts.Aliases)
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	var goFiles []*goast.File
	for path, f := range gsxFiles {
		skel, _, _, berr := buildSkeleton(f, table, propFields, nodeProps, byo, m.opts.FieldMatcher, fset)
		if berr != nil {
			return nil, berr
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		gf, perr := goparser.ParseFile(fset, filepath.Join(dir, base+".x.go"), skel, goparser.SkipObjectResolution)
		if perr != nil {
			return nil, perr
		}
		goFiles = append(goFiles, gf)
	}
	// Shared _gsxuse/_gsxcompsig helpers, mirroring the batch overlay.
	helper, _ := goparser.ParseFile(fset, filepath.Join(dir, "_gsxshared.x.go"),
		"package "+pkgName+"\n\nfunc _gsxuse(...any) {}\nfunc _gsxcompsig(any) {}\n", goparser.SkipObjectResolution)
	goFiles = append(goFiles, helper)

	pkg, _, _ := checkSkeletonPackage(dir, pkgName, goFiles, fset, mi)
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = pkg
	m.mu.Unlock()
	return pkg, nil
}

// parsePackage parses every .gsx in dir (override-aware) and returns the parsed
// files + package name using a fresh fset.
func (m *Module) parsePackage(dir string) (map[string]*gsxast.File, string, error) {
	return m.parsePackageWithFset(dir, token.NewFileSet())
}

// parsePackageWithFset parses every .gsx in dir into the provided fset so the
// caller can share it with buildSkeleton (required for valid //line directives).
func (m *Module) parsePackageWithFset(dir string, fset *token.FileSet) (map[string]*gsxast.File, string, error) {
	paths := map[string]struct{}{}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	for _, p := range matches {
		paths[p] = struct{}{}
	}
	m.mu.Lock()
	for p := range m.overrides {
		if filepath.Dir(p) == dir && strings.HasSuffix(p, ".gsx") {
			paths[p] = struct{}{}
		}
	}
	m.mu.Unlock()
	files := map[string]*gsxast.File{}
	pkgName := ""
	for p := range paths {
		src, ok := m.source(p)
		if !ok {
			continue
		}
		f, perrs := gsxparser.ParseFileWithClassifier(fset, p, src, 0, m.opts.Classifier)
		if len(perrs) > 0 {
			return nil, "", fmt.Errorf("parse error in %s: %s", p, perrs[0].Msg)
		}
		wsnorm.Normalize(f)
		files[p] = f
		pkgName = f.Package
	}
	return files, pkgName, nil
}
