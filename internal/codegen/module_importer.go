package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
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
// and everything else from external. seen breaks recursion on import cycles;
// cycleErr records the first cycle detected so typesPackageWith can propagate it.
type moduleImporter struct {
	m        *Module
	external types.Importer
	seen     map[string]bool
	cycleErr error
}

func (mi *moduleImporter) Import(path string) (*types.Package, error) {
	if dir, ok := dirForImportPath(mi.m.opts.ModuleRoot, mi.m.opts.ModulePath, path); ok {
		if mi.m.isGsxPackage(dir) {
			if mi.seen[dir] {
				// cycle guard: return cached package if ready, else signal cycle error.
				if p, ok := mi.m.pkgTypes[dir]; ok {
					return p, nil
				}
				err := fmt.Errorf("import cycle through %s", dir)
				mi.cycleErr = err
				return nil, err
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

// typesPackageWith does the work, threading the recursive importer. It calls
// analyze and returns only the *types.Package (the importer path needs nothing
// else), caching the result for the cycle guard + repeat lookups.
func (m *Module) typesPackageWith(dir string, mi *moduleImporter) (*types.Package, error) {
	m.mu.Lock()
	if p, ok := m.pkgTypes[dir]; ok {
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()
	a, err := m.analyze(dir, mi)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = a.pkg
	m.mu.Unlock()
	return a.pkg, nil
}

// analyzed is the full retained result of analyzing one gsx package: the parsed
// gsx files, the type-checked skeleton, harvested type/expr maps, and the
// component cross-index inputs. typesPackage consumes only a.pkg; Module.Package
// (retained analysis) and Module.Generate (codegen) consume the rest.
type analyzed struct {
	pkgName    string
	gsxFiles   map[string]*gsxast.File        // gsx path -> parsed file
	gsxFset    *token.FileSet                 // gsx positions
	skelFset   *token.FileSet                 // skeleton positions (same fset as gsxFset for Module)
	goFiles    []*goast.File                  // parsed skeletons + shared helper
	compsByXGo map[string][]*gsxast.Component // skeleton abs path -> components
	table      filterTable
	propFields map[string]map[string]bool
	nodeProps  map[string]map[string]bool
	byo        *byoData
	resolved   map[gsxast.Node]types.Type
	exprMap    map[gsxast.Node]goast.Expr
	pkg        *types.Package
	info       *types.Info
	compByKey  map[string]*gsxast.Component // componentKey -> component (for Name + NamePos)
	objKey     map[types.Object]string      // component func object -> componentKey
	bag        *diag.Bag                    // diagnostics from parse + script resolution; used by Generate
}

// analyze performs the shared parse -> skeleton -> type-check pipeline for one
// gsx package dir, threading the recursive importer mi, and returns the rich
// analyzed result. It preserves Task 4's cycle behaviour: a cycle detected
// during type-check is propagated (without caching) via mi.cycleErr.
func (m *Module) analyze(dir string, mi *moduleImporter) (*analyzed, error) {
	mi.seen[dir] = true

	// Use a single fset shared across parse + skeleton so //line directives from
	// buildSkeleton reference valid positions from the same FileSet. For Module
	// the gsx fset and skeleton fset are the same: skeleton idents resolve back to
	// .gsx via the //line directives the parser honoured.
	fset := token.NewFileSet()
	// bag is created here (using the shared fset for position resolution) so that
	// script-resolution diagnostics recorded below share the same fset as the
	// parsed .gsx files. Generate returns a.bag.Sorted() so errors surface.
	bag := diag.NewBag(fset)
	gsxFiles, pkgName, err := m.parsePackageWithFset(dir, fset)
	if err != nil {
		return nil, err
	}
	// Classify <script> @{ } JS contexts (mirrors batch.go after wsnorm.Normalize).
	// If ANY file fails resolution, skip the ENTIRE package (no generated output),
	// matching batch's package-level-skip semantics:
	//   hasErr=true; break  →  if hasErr { continue }  (no .x.go for any file).
	// Diagnostics are recorded in bag and surfaced by Generate via bag.Sorted().
	scriptErr := false
	for _, f := range gsxFiles {
		if !jsx.ResolveScripts(f, bag) {
			scriptErr = true
		}
	}
	if scriptErr {
		gsxFiles = nil // package-level skip: Generate's loop emits nothing
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
	compsByXGo := map[string][]*gsxast.Component{}
	for path, f := range gsxFiles {
		skel, comps, _, berr := buildSkeleton(f, table, propFields, nodeProps, byo, m.opts.FieldMatcher, fset)
		if berr != nil {
			return nil, berr
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		absXpath := filepath.Join(dir, base+".x.go")
		gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
		if perr != nil {
			return nil, perr
		}
		goFiles = append(goFiles, gf)
		compsByXGo[absXpath] = comps
	}
	// Shared _gsxuse/_gsxcompsig helpers, mirroring the batch overlay.
	helperXgoPath := filepath.Join(dir, "_gsxshared.x.go")
	helper, _ := goparser.ParseFile(fset, helperXgoPath,
		"package "+pkgName+"\n\nfunc _gsxuse(...any) {}\nfunc _gsxcompsig(any) {}\n", goparser.SkipObjectResolution)
	goFiles = append(goFiles, helper)

	// Include the package's hand-written .go files (model.go, helper.go, etc.)
	// so that companion types and functions are visible during skeleton
	// type-checking — mirroring how GeneratePackagesWithFilters's packages.Load
	// overlay sees them on disk alongside the synthetic .x.go overlays.
	// Excluded: each .x.go whose path matches a skeleton already in goFiles
	// (compsByXGo), the synthetic helper above (helperXgoPath), and _test files.
	realGoFiles, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, realPath := range realGoFiles {
		if compsByXGo[realPath] != nil || realPath == helperXgoPath {
			continue // already represented as a synthetic overlay
		}
		src, readErr := os.ReadFile(realPath)
		if readErr != nil {
			continue // file disappeared; not fatal
		}
		realGF, parseErr := goparser.ParseFile(fset, realPath, src, goparser.SkipObjectResolution)
		if parseErr != nil {
			continue // parse error surfaced by type-checker via Error func
		}
		goFiles = append(goFiles, realGF)
	}

	pkg, info, _ := checkSkeletonPackage(dir, pkgName, goFiles, fset, mi)
	if mi.cycleErr != nil {
		// A cycle was detected during this package's type-check; propagate
		// the error without caching so the caller receives it.
		return nil, mi.cycleErr
	}

	// Harvest once into resolved + exprMap (both consumed downstream: resolved by
	// Generate, exprMap surfaced by Package). Build the component cross-index
	// inputs (compByKey / objKey), mirroring the batch path.
	resolved := map[gsxast.Node]types.Type{}
	exprMap := map[gsxast.Node]goast.Expr{}
	compByKey := map[string]*gsxast.Component{} // componentKey -> component
	compObjByKey := map[string]types.Object{}   // componentKey -> component func object
	for _, gf := range goFiles {
		fname := fset.Position(gf.Pos()).Filename
		comps, ok := compsByXGo[fname]
		if !ok {
			continue
		}
		harvest(gf, comps, info, resolved, exprMap)
		for _, c := range comps {
			compByKey[componentKey(c)] = c
		}
		for _, decl := range gf.Decls {
			fd, ok := decl.(*goast.FuncDecl)
			if !ok {
				continue
			}
			if _, ok := compByKey[funcDeclKey(fd)]; !ok {
				continue
			}
			if obj := info.Defs[fd.Name]; obj != nil {
				compObjByKey[funcDeclKey(fd)] = obj
			}
		}
	}
	objKey := map[types.Object]string{} // reverse: object -> componentKey
	for key, obj := range compObjByKey {
		objKey[obj] = key
	}

	return &analyzed{
		pkgName:    pkgName,
		gsxFiles:   gsxFiles,
		gsxFset:    fset,
		skelFset:   fset,
		goFiles:    goFiles,
		compsByXGo: compsByXGo,
		table:      table,
		propFields: propFields,
		nodeProps:  nodeProps,
		byo:        byo,
		resolved:   resolved,
		exprMap:    exprMap,
		pkg:        pkg,
		info:       info,
		compByKey:  compByKey,
		objKey:     objKey,
		bag:        bag,
	}, nil
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
