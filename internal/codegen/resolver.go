package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
	"github.com/gsxhq/gsx/internal/wsnorm"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// StdImportPath is the gsx built-in filter package. Re-exported from the
// internal stdImportPath constant so the public gen package (and external
// callers such as gsxplayground) can reference it without coupling to the
// internal filters.go symbol.
const StdImportPath = stdImportPath

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

// CachedResolver uses a prebuilt importer (no subprocess per render).
// Its dependencies are loaded once via packages.Load against moduleDir; each
// per-file check runs entirely in-process via go/types.
//
// Use NewCachedResolver to construct a CachedResolver. The zero value is not
// valid.
type CachedResolver struct {
	imp   types.Importer
	table filterTable
}

// filters returns the prebuilt filterTable so callers can skip a second
// loadFilterTableMulti when using this resolver.
func (c *CachedResolver) filters() filterTable { return c.table }

// check implements typeResolver. It parses the overlay files, derives the real
// package name from the parsed AST, and runs go/types in-process. Type errors
// are collected via conf.Error and returned as a *cachedTypeErrors so that
// GeneratePackagesWithResolver can surface them as positioned diagnostics —
// matching the behavior of the default packages.Load path (pkg.TypeErrors).
func (c *CachedResolver) check(dir string, overlay map[string][]byte, fset *token.FileSet) ([]*goast.File, *types.Info, error) {
	var files []*goast.File
	for path, src := range overlay {
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, f)
	}

	// Also include the package's hand-written .go files (NOT the generated
	// .x.go, which the overlay skeletons replace) so a symbol a .gsx references
	// that is defined in a sibling .go file resolves — matching the cold
	// packages.Load path. go/build is build-constraint- and test-file-aware
	// (GoFiles excludes *_test.go and build-excluded files). If the dir has no
	// buildable Go package (e.g. only .gsx, no .x.go yet), ImportDir errors and
	// we simply add nothing.
	if bp, berr := build.ImportDir(dir, 0); berr == nil {
		for _, name := range bp.GoFiles {
			if strings.HasSuffix(name, ".x.go") {
				continue // generated; the overlay provides the fresh skeleton
			}
			f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil, nil, perr
			}
			files = append(files, f)
		}
	}

	// Derive the real package name from the parsed AST rather than hardcoding
	// "views". The skeleton files all declare the same package; any file works.
	pkgName := ""
	for _, f := range files {
		if f.Name != nil && f.Name.Name != "" {
			pkgName = f.Name.Name
			break
		}
	}
	if pkgName == "" {
		pkgName = "views" // safe fallback; buildSkeleton always sets package
	}

	info := &types.Info{Types: map[goast.Expr]types.TypeAndValue{}}
	var typeErrs []types.Error
	conf := types.Config{
		Importer: c.imp,
		Error: func(e error) {
			// types.Config.Error is always called with a types.Error value.
			// Use a direct type assertion; non-types.Error values are silently
			// dropped (impossible in practice, but defensive).
			if te, ok := e.(types.Error); ok {
				typeErrs = append(typeErrs, te)
			}
		},
	}
	pkg := types.NewPackage(dir, pkgName)
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files) // type errors are captured above via conf.Error
	if len(typeErrs) > 0 {
		return files, info, &cachedTypeErrors{fset: fset, errs: typeErrs}
	}
	return files, info, nil
}

// cachedTypeErrors carries type errors produced by CachedResolver.check. Each
// error has a Fset and Pos so GeneratePackagesWithResolver can resolve positions
// back to the .gsx source via //line directives in the skeleton.
type cachedTypeErrors struct {
	fset *token.FileSet
	errs []types.Error
}

func (e *cachedTypeErrors) Error() string {
	if len(e.errs) == 0 {
		return "no type errors"
	}
	return e.errs[0].Msg
}

// newCachedResolver loads filterPkgs (plus "github.com/gsxhq/gsx" and
// allowImports) once from moduleDir and returns a resolver whose check method
// runs without any subprocess. The returned resolver's filters() method exposes
// the prebuilt filterTable so callers can skip a second loadFilterTableMulti.
func newCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*CachedResolver, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  moduleDir,
	}
	loadPaths := []string{"github.com/gsxhq/gsx"}
	loadPaths = append(loadPaths, filterPkgs...)
	for _, a := range aliases {
		loadPaths = append(loadPaths, a.PkgPath)
	}
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
	table, err := loadFilterTableMulti(moduleDir, filterPkgs, aliases)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{imp: mapImporter(m), table: table}, nil
}

// NewCachedResolver is the public constructor for CachedResolver. It loads
// filterPkgs (plus "github.com/gsxhq/gsx" and allowImports) once from
// moduleDir and returns a resolver ready for in-process generation with no
// per-render subprocess.
func NewCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*CachedResolver, error) {
	return newCachedResolver(moduleDir, filterPkgs, aliases, allowImports)
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

// GeneratePackagesWithResolver generates .x.go for every .gsx across the given
// package dirs using the provided CachedResolver (no per-render go list /
// subprocess). It threads the resolver's prebuilt importer and filter table
// through each dir's type-check step instead of launching a subprocess.
//
// Type errors from the cached checker are surfaced as positioned diagnostics
// in each PackageResult.Diags. Positions resolve to the .gsx source via the
// //line directives embedded in each skeleton by buildSkeleton/emitSkeletonLine.
//
// srcOverride maps absolute .gsx paths to their in-memory source; nil means
// read from disk. The returned map is keyed by normalized absolute dir.
func GeneratePackagesWithResolver(moduleDir string, dirs []string, resolver *CachedResolver, cls *attrclass.Classifier, srcOverride map[string][]byte) (map[string]*PackageResult, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	filterPkgs := []string{stdImportPath}

	result := make(map[string]*PackageResult, len(dirs))
	absDirs := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			result[dir] = &PackageResult{Err: fmt.Errorf("codegen: abs(%s): %w", dir, err)}
			continue
		}
		result[abs] = &PackageResult{Files: map[string][]byte{}}
		absDirs = append(absDirs, abs)
	}

	errDiagReported := errors.New("codegen: diagnostics reported")

	for _, dir := range absDirs {
		res := result[dir]
		fset := token.NewFileSet()
		bag := diag.NewBag(fset)

		// Step 1: parse .gsx files.
		// Collect the set of .gsx paths to process: disk files (from glob) plus
		// any srcOverride entries under this dir that do not exist on disk. This
		// supports fully in-memory packages (playground: srcOverride provides all
		// source; no .gsx files need to exist on disk).
		gsxPaths := map[string]struct{}{}
		if matches, err2 := filepath.Glob(filepath.Join(dir, "*.gsx")); err2 == nil {
			for _, m := range matches {
				gsxPaths[m] = struct{}{}
			}
		}
		// Also include any srcOverride key that lives directly under dir.
		for k := range srcOverride {
			if !strings.HasSuffix(k, ".gsx") {
				continue
			}
			if filepath.Dir(k) == dir {
				gsxPaths[k] = struct{}{}
			}
		}

		files := make(map[string]*gsxast.File, len(gsxPaths))
		hasErr := false
		for m := range gsxPaths {
			var src []byte
			if ov, ok := srcOverride[m]; ok {
				src = ov
			} else {
				b, err := os.ReadFile(m)
				if err != nil {
					bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "parser"})
					hasErr = true
					break
				}
				src = b
			}
			f, perrs := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
			for _, e := range perrs {
				bag.Report(e.Pos, e.Pos, diag.Error, "syntax", "parser", "%s", e.Msg)
			}
			if len(perrs) > 0 {
				hasErr = true
				continue
			}
			wsnorm.Normalize(f)
			if !jsx.ResolveScripts(f, bag) {
				hasErr = true
				break
			}
			files[m] = f
		}
		if hasErr {
			res.Diags = bag.Sorted()
			if bag.HasErrors() {
				res.Err = errDiagReported
			}
			continue
		}
		if len(files) == 0 {
			res.Diags = bag.Sorted()
			continue
		}

		// Step 2: derive propFields and nodeProps.
		propFields, nodeProps, byo, err := componentPropFieldsFor(dir, files)
		if err != nil {
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			res.Diags = bag.Sorted()
			res.Err = errDiagReported
			continue
		}

		// Step 3: resolve types using the cached resolver (no subprocess).
		// resolveTypesPkgWithFilters detects *CachedResolver and uses its prebuilt
		// filter table and importer instead of calling packages.Load.
		resolved, table, resolveErr := resolveTypesPkgWithFilters(dir, files, propFields, nodeProps, byo, nil, filterPkgs, nil, fset, resolver)
		if resolveErr != nil {
			// Check whether this is a cachedTypeErrors (positioned type errors)
			// vs a genuine infrastructure error.
			var cte *cachedTypeErrors
			if errors.As(resolveErr, &cte) {
				// Surface each type error as a positioned diagnostic. The skeleton
				// has //line directives so fset.Position(e.Pos) maps back to the
				// .gsx source file:line:col — matching the batch path's behavior.
				for _, e := range cte.errs {
					p := cte.fset.Position(e.Pos)
					bag.Add(diag.Diagnostic{
						Start:    p,
						End:      p,
						Severity: diag.Error,
						Message:  e.Msg,
						Source:   "types",
					})
				}
				res.Diags = bag.Sorted()
				res.Err = errDiagReported
				continue
			}
			// Genuine infrastructure error.
			bag.Add(diag.Diagnostic{
				Severity: diag.Error,
				Message:  strings.TrimPrefix(resolveErr.Error(), "codegen: "),
				Source:   "codegen",
			})
			res.Diags = bag.Sorted()
			res.Err = errDiagReported
			continue
		}

		// Step 4: generate each file.
		for path, file := range files {
			gen, genOK := generateFile(file, resolved, table, propFields, nodeProps, byo, fset, cls, nil, bag, nil, nil, true, true)
			if !genOK {
				_ = path
				continue
			}
			res.Files[path] = gen
		}

		res.Diags = bag.Sorted()
		if bag.HasErrors() {
			res.Err = errDiagReported
		}
	}

	return result, nil
}
