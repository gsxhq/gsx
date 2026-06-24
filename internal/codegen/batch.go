package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
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

// CrossRef is one component's cross-boundary entry: its name, its .gsx
// declaration, and every reference (resolved positions — .go call sites stay
// .go; .gsx <Card/> tags map to .gsx via the skeleton's child-tag //line).
// Name's length bounds the cursor-on-reference span check in the LSP.
type CrossRef struct {
	Name string
	Decl token.Position
	Refs []token.Position
}

// NavRef is one navigable Go reference (in a .go or skeleton file) and the .gsx
// position it targets. From is the reference site; To is the .gsx declaration.
type NavRef struct {
	From token.Position
	Name string // identifier text, for the cursor-span length check in the LSP
	To   token.Position
}

// PackageResult is the per-package outcome of GeneratePackages.
type PackageResult struct {
	Files map[string][]byte // .gsx path -> generated .x.go source
	Diags []diag.Diagnostic // all diagnostics collected for this package
	Err   error             // transition sentinel: non-nil if any Error-severity diagnostic (until consumers read Diags)

	// Retained analysis for the language server (read-only; nil when the package
	// failed before type-checking). The two FileSets are distinct: GSXFset is the
	// gsx parse fset; Fset is the go/packages skeleton fset.
	GSXFset    *token.FileSet
	Fset       *token.FileSet
	Info       *types.Info
	ExprMap    map[gsxast.Node]goast.Expr
	GSXFiles   map[string]*gsxast.File
	CrossIndex map[string]CrossRef // componentKey → cross-boundary index entry
	NavIndex   []NavRef            // navigable Go references → .gsx targets (func, props-struct, field)
}

// GeneratePackagesWithFilters generates .x.go for every .gsx across the given
// package dirs, which MUST all live in the same Go module rooted at moduleDir.
// It resolves types for ALL packages with a SINGLE go/packages load (loading
// ONLY the given dirs as explicit patterns, not the whole module), and loads the
// filter table once using filterPkgs (empty ⇒ built-in std filter). A per-package
// error (parse or type-resolution) is recorded in that dir's PackageResult.Err
// without failing the others. The returned map is keyed by the normalized
// absolute dir.
func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, cls *attrclass.Classifier, fm FieldMatcher, cssMin, jsMin func(string) (string, error), srcOverride map[string][]byte) (map[string]*PackageResult, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	filterPkgs = dedupFilterPkgs(filterPkgs) // empty → [stdImportPath]

	// Normalize each input dir to an absolute, clean path. If Abs fails for a
	// dir, record the error keyed by the original string and skip it.
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

	// Build a set of included dirs for fast lookup.
	dirSet := make(map[string]bool, len(absDirs))
	for _, dir := range absDirs {
		dirSet[dir] = true
	}

	// Shared fset for ALL parses in this call — positions/harvest rely on it.
	fset := token.NewFileSet()

	// Per-package diagnostic bags — keyed by the same abs dir as result.
	// Each bag holds the shared parse fset so AST token.Pos values resolve
	// correctly. Type errors (from pkg.Fset) are added pre-resolved via Add.
	bags := make(map[string]*diag.Bag, len(absDirs))
	for _, dir := range absDirs {
		bags[dir] = diag.NewBag(fset)
	}

	// Step 1: parse .gsx files per dir. Exclude dirs with parse errors.
	filesByDir := make(map[string]map[string]*gsxast.File, len(absDirs))
	for _, dir := range absDirs {
		bag := bags[dir]
		matches, err := filepath.Glob(filepath.Join(dir, "*.gsx"))
		if err != nil {
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "parser"})
			continue
		}
		files := make(map[string]*gsxast.File, len(matches))
		hasErr := false
		for _, m := range matches {
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
				continue // keep parsing other files to collect all parser errors
			}
			// JSX whitespace pass before resolution + emit (mirror codegen.go).
			wsnorm.Normalize(f)
			// Classify <script> @{ } JS contexts + un-split comment holes before
			// resolution/emit (mirror codegen.go). Fails closed; surfaces as this
			// package's codegen diagnostic.
			if !jsx.ResolveScripts(f, bag) {
				hasErr = true
				break
			}
			files[m] = f
		}
		if hasErr {
			continue
		}
		filesByDir[dir] = files
	}

	// Step 2: derive propFields and nodeProps per dir (MUST stay per-dir; type-name
	// collisions across packages mean we can never merge them). nodeProps records
	// which declared params have type exactly gsx.Node; threaded alongside propFields.
	propFieldsByDir := make(map[string]map[string]map[string]bool, len(absDirs))
	nodePropsByDir := make(map[string]map[string]map[string]bool, len(absDirs))
	byoByDir := make(map[string]*byoData, len(absDirs))
	for _, dir := range absDirs {
		files, ok := filesByDir[dir]
		if !ok {
			continue // already errored
		}
		pf, np, byo, err := componentPropFieldsFor(dir, files)
		if err != nil {
			bags[dir].Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			delete(filesByDir, dir)
			continue
		}
		propFieldsByDir[dir] = pf
		nodePropsByDir[dir] = np
		byoByDir[dir] = byo
	}

	// Step 3: load filter table ONCE from the module root.
	table, err := loadFilterTableMulti(moduleDir, filterPkgs)
	if err != nil {
		return nil, fmt.Errorf("codegen: load filter table: %w", err)
	}

	// Step 4: build ONE combined overlay across all included dirs.
	overlay := map[string][]byte{}
	// skelCompsByPath maps absXpath → slice of *gsxast.Component (from buildSkeleton).
	skelCompsByPath := map[string][]*gsxast.Component{}

	for _, dir := range absDirs {
		files, ok := filesByDir[dir]
		if !ok {
			continue
		}
		pf := propFieldsByDir[dir]

		// Pick package name from any file in this dir.
		pkgName := ""
		for _, f := range files {
			pkgName = f.Package
			break
		}

		np := nodePropsByDir[dir]
		byo := byoByDir[dir]
		skeletonErr := false
		for path, file := range files {
			skel, comps, err := buildSkeleton(file, table, pf, np, byo, fm, fset)
			if err != nil {
				// An attrError carries the offending attr's position and a diagnostic
				// code — emit a positioned diagnostic. Any other error is an infrastructure
				// failure recorded positionlessly (with the "codegen: " prefix stripped).
				var ae *attrError
				if errors.As(err, &ae) {
					bags[dir].Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
				} else {
					bags[dir].Add(diag.Diagnostic{Severity: diag.Error, Message: strings.TrimPrefix(err.Error(), "codegen: "), Source: "codegen"})
				}
				delete(filesByDir, dir)
				skeletonErr = true
				break
			}
			base := strings.TrimSuffix(filepath.Base(path), ".gsx")
			absXpath := filepath.Join(dir, base+".x.go")
			overlay[absXpath] = []byte(skel)
			skelCompsByPath[absXpath] = comps
		}
		if skeletonErr {
			continue
		}

		// Per-dir shared _gsxuse helper — mirror resolveTypesPkg exactly.
		sharedPath, err := freeOverlayPath(dir, "gsxshared", ".x.go", overlay)
		if err != nil {
			bags[dir].Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			delete(filesByDir, dir)
			continue
		}
		overlay[sharedPath] = []byte("package " + pkgName + "\n\nfunc _gsxuse(...any) {}\n")
	}

	// Step 5: ONE packages.Load for the whole module.
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:     moduleDir,
		Overlay: overlay,
	}
	patterns := make([]string, 0, len(absDirs))
	for _, d := range absDirs {
		patterns = append(patterns, d)
	}
	if len(patterns) == 0 {
		return result, nil
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("codegen: load packages: %w", err)
	}

	// Step 6: build one global resolved map. For each loaded pkg, determine its
	// dir from its files, skip deps/stdlib, harvest skeletons.
	resolved := map[gsxast.Node]types.Type{}
	for _, pkg := range pkgs {
		// Determine which input dir this loaded package belongs to by looking at
		// one of its compiled go files. Overlay files live under the dir too.
		pkgDir := ""
		for _, f := range pkg.CompiledGoFiles {
			d := filepath.Dir(f)
			if dirSet[d] {
				pkgDir = d
				break
			}
		}
		if pkgDir == "" {
			// Try GoFiles if CompiledGoFiles was empty / didn't match.
			for _, f := range pkg.GoFiles {
				d := filepath.Dir(f)
				if dirSet[d] {
					pkgDir = d
					break
				}
			}
		}
		if pkgDir == "" {
			continue // stdlib / dependency — not one of ours
		}
		if filesByDir[pkgDir] == nil {
			continue // dir was excluded due to earlier error
		}

		// Retain the analyzed package for the LSP, even if it has type errors
		// (go/types fills TypesInfo best-effort; the skeleton AST is intact).
		res := result[pkgDir]
		res.GSXFset = fset
		res.Fset = pkg.Fset
		res.Info = pkg.TypesInfo
		res.GSXFiles = filesByDir[pkgDir]
		res.ExprMap = map[gsxast.Node]goast.Expr{}
		for _, f := range pkg.Syntax {
			fname := pkg.Fset.Position(f.Pos()).Filename
			comps, ok := skelCompsByPath[fname]
			if !ok {
				continue
			}
			harvest(f, comps, pkg.TypesInfo, resolved, res.ExprMap)
		}

		// Build the slim cross-boundary index: component objects (by componentKey)
		// → their .gsx declaration + every reference, resolved through pkg.Fset
		// (//line maps skeleton refs to .gsx; real .go refs stay .go).
		compByKey := map[string]*gsxast.Component{} // componentKey → component (for Name + NamePos)
		compObjByKey := map[string]types.Object{}   // componentKey → the component's func object
		for _, f := range pkg.Syntax {
			fname := pkg.Fset.Position(f.Pos()).Filename
			comps, ok := skelCompsByPath[fname]
			if !ok {
				continue
			}
			for _, c := range comps {
				compByKey[componentKey(c)] = c
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*goast.FuncDecl)
				if !ok {
					continue
				}
				if _, ok := compByKey[funcDeclKey(fd)]; !ok {
					continue
				}
				if obj := pkg.TypesInfo.Defs[fd.Name]; obj != nil {
					compObjByKey[funcDeclKey(fd)] = obj
				}
			}
		}
		objKey := map[types.Object]string{} // reverse: object → componentKey
		for key, obj := range compObjByKey {
			objKey[obj] = key
		}
		index := map[string]CrossRef{}
		for key, c := range compByKey {
			index[key] = CrossRef{Name: c.Name, Decl: fset.Position(c.NamePos)} // gsx fset → .gsx position
		}

		// Build maps for NavIndex: props-struct objects and field var objects → .gsx targets.
		// structObjToComp maps a props-struct types.Object → the component it belongs to.
		// fieldObjToPos maps a field *types.Var → the .gsx position of the corresponding param.
		structObjToComp := map[types.Object]*gsxast.Component{}
		fieldObjToPos := map[*types.Var]token.Position{}
		for _, c := range compByKey {
			// Derive propsName the same way emitComponentSkeleton does.
			propsName := c.Name + "Props"
			if c.Recv != "" {
				_, _, recvTypeName, rerr := parseRecv(c.Recv)
				if rerr == nil {
					propsName = recvTypeName + c.Name + "Props"
				}
			}
			structObj := pkg.Types.Scope().Lookup(propsName)
			if structObj == nil {
				continue
			}
			structObjToComp[structObj] = c

			// Map each field var → the .gsx position of its corresponding param.
			params, err := parseParams(c.Params)
			if err != nil {
				continue
			}
			st, ok := structObj.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for _, p := range params {
				fname := fieldName(p.name)
				paramPos := fset.Position(c.ParamsPos + token.Pos(p.nameOff))
				for i := 0; i < st.NumFields(); i++ {
					fv := st.Field(i)
					if fv.Name() == fname {
						fieldObjToPos[fv] = paramPos
						break
					}
				}
			}
		}

		var navIndex []NavRef
		for id, obj := range pkg.TypesInfo.Uses {
			p := pkg.Fset.Position(id.Pos())
			if strings.HasSuffix(p.Filename, ".x.go") {
				continue // synthetic skeleton position with no //line — skip
			}
			// Case 1: component func reference → .gsx component decl.
			if key, ok := objKey[obj]; ok {
				c := compByKey[key]
				cr := index[key]
				cr.Refs = append(cr.Refs, p)
				index[key] = cr
				navIndex = append(navIndex, NavRef{
					From: p,
					Name: id.Name,
					To:   fset.Position(c.NamePos),
				})
				continue
			}
			// Case 2: props-struct type reference → start of the .gsx component
			// argument list (the props ARE the params, so CardProps lands on the
			// param list rather than the component name). Components with no params
			// have no ParamsPos; fall back to the component name there.
			if c, ok := structObjToComp[obj]; ok {
				to := c.ParamsPos
				if !to.IsValid() {
					to = c.NamePos
				}
				navIndex = append(navIndex, NavRef{
					From: p,
					Name: id.Name,
					To:   fset.Position(to),
				})
				continue
			}
			// Case 3: props-struct field reference → .gsx param position.
			if fv, ok := obj.(*types.Var); ok {
				if paramPos, ok := fieldObjToPos[fv]; ok {
					navIndex = append(navIndex, NavRef{
						From: p,
						Name: id.Name,
						To:   paramPos,
					})
				}
			}
		}
		res.CrossIndex = index
		res.NavIndex = navIndex

		// Collect ALL type errors into the bag (positioned via pkg.Fset which
		// resolves //line directives back to the .gsx source file).
		if len(pkg.TypeErrors) > 0 {
			pkgBag := bags[pkgDir]
			for _, e := range pkg.TypeErrors {
				p := e.Fset.Position(e.Pos)
				pkgBag.Add(diag.Diagnostic{
					Start:    p,
					End:      p,
					Severity: diag.Error,
					Message:  e.Msg,
					Source:   "types",
				})
			}
			// Also capture any non-type pkg errors (load/list errors) as positionless.
			for _, pe := range pkg.Errors {
				if pe.Kind != packages.TypeError {
					pkgBag.Add(diag.Diagnostic{
						Severity: diag.Error,
						Message:  pe.Msg,
						Source:   "loader",
					})
				}
			}
			delete(filesByDir, pkgDir) // exclude from codegen step
			continue
		}

		// Even if there are no TypeErrors, check for other (load/list) errors.
		if len(pkg.Errors) > 0 {
			pkgBag := bags[pkgDir]
			for _, pe := range pkg.Errors {
				pkgBag.Add(diag.Diagnostic{
					Severity: diag.Error,
					Message:  pe.Msg,
					Source:   "loader",
				})
			}
			delete(filesByDir, pkgDir) // exclude from codegen step
			continue
		}
	}

	// Step 7: generateFile for each included dir.
	for _, dir := range absDirs {
		files, ok := filesByDir[dir]
		if !ok {
			continue // excluded
		}
		pf := propFieldsByDir[dir]
		bag := bags[dir]
		np := nodePropsByDir[dir]
		byo := byoByDir[dir]
		for path, file := range files {
			gen, genOK := generateFile(file, resolved, table, pf, np, byo, fset, cls, fm, bag, cssMin, jsMin)
			if !genOK {
				// Diagnostics already in bag; skip writing this file but continue
				// processing other files in the package so all errors are reported.
				_ = path
				continue
			}
			result[dir].Files[path] = gen
		}
	}

	// Finalize: populate Diags and set transition sentinel Err on each package.
	errDiagReported := errors.New("codegen: diagnostics reported")
	for _, dir := range absDirs {
		bag := bags[dir]
		result[dir].Diags = bag.Sorted()
		if bag.HasErrors() {
			result[dir].Err = errDiagReported
		}
	}

	return result, nil
}

// GeneratePackages is GeneratePackagesWithFilters with the built-in std filter
// package (kept for the test corpus and any std-only caller).
func GeneratePackages(moduleDir string, dirs []string) (map[string]*PackageResult, error) {
	return GeneratePackagesWithFilters(moduleDir, dirs, nil, nil, nil, nil, nil, nil)
}
