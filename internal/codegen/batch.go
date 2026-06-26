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

// compOwner identifies the package directory and componentKey that own a
// component func object, for routing cross-package references (see the
// cross-package find-references design).
type compOwner struct{ dir, key string }

// analyzedPkg retains one analyzed package's fileset and use info for the
// post-loop cross-package reference pass.
type analyzedPkg struct {
	dir  string
	fset *token.FileSet
	info *types.Info
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

	// CtrlMap maps each control-flow node (ForMarkup/IfMarkup/GoBlock) to its
	// skeleton clause position and smallest containing skeleton go/ast node.
	// Used by the LSP to bridge a cursor in a for/if/goblock clause to the
	// skeleton for go-to-definition on loop variables and condition identifiers.
	CtrlMap map[gsxast.Node]ctrlRef

	// UnusedImports lists, per .gsx file path, the imports the file declares but
	// does not use — safe to drop on format. Empty unless the package's ONLY type
	// errors are unused-import errors (else removal is unsafe).
	UnusedImports map[string][]UnusedImport

	// Types is the analyzed package's go/types.Package, retained for the LSP
	// (e.g. hover's qualifier). nil when the package failed before type-checking.
	Types *types.Package
}

// UnusedImport is one import a .gsx file declares but never references, as
// determined by the type-checker. Name is "" for a default import.
type UnusedImport struct {
	Name string
	Path string
}

// GeneratePackagesWithFilters generates .x.go for every .gsx across the given
// package dirs, which MUST all live in the same Go module rooted at moduleDir.
// It resolves types for ALL packages with a SINGLE go/packages load (loading
// ONLY the given dirs as explicit patterns, not the whole module), and loads the
// filter table once using filterPkgs (empty ⇒ built-in std filter). A per-package
// error (parse or type-resolution) is recorded in that dir's PackageResult.Err
// without failing the others. The returned map is keyed by the normalized
// absolute dir.
func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, aliases []FilterAlias, cls *attrclass.Classifier, fm FieldMatcher, cssMin, jsMin func(string) (string, error), srcOverride map[string][]byte) (map[string]*PackageResult, error) {
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
	table, err := loadFilterTableMulti(moduleDir, filterPkgs, aliases)
	if err != nil {
		return nil, fmt.Errorf("codegen: load filter table: %w", err)
	}

	// Step 4: build ONE combined overlay across all included dirs.
	overlay := map[string][]byte{}
	// skelCompsByPath maps absXpath → slice of *gsxast.Component (from buildSkeleton).
	skelCompsByPath := map[string][]*gsxast.Component{}
	// ctrlOffByXGo maps absXpath → ctrlOff (control-flow clause byte-offsets in the
	// skeleton). Collected here per-file for Task 3 (LSP go-to-def on loop vars etc.).
	ctrlOffByXGo := map[string]map[gsxast.Node]int{}
	importsByDir := map[string][]importSpec{} // dir → hoisted import specs across its files

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
			skel, comps, imps, ctrlOff, err := buildSkeleton(file, table, pf, np, byo, fm, fset)
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
			ctrlOffByXGo[absXpath] = ctrlOff
			importsByDir[dir] = append(importsByDir[dir], imps...)
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
		overlay[sharedPath] = []byte("package " + pkgName + "\n\nfunc _gsxuse(...any) {}\nfunc _gsxcompsig(any) {}\n")
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
	compObjOwner := map[types.Object]compOwner{}
	var analyzed []analyzedPkg
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
		res.Types = pkg.Types
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

		// Build CtrlMap: skeleton clause position + containing node per control-flow node.
		ctrlMap := map[gsxast.Node]ctrlRef{}
		for _, f := range pkg.Syntax {
			fname := pkg.Fset.Position(f.Pos()).Filename
			co, ok := ctrlOffByXGo[fname]
			if !ok {
				continue
			}
			clauseText := make(map[gsxast.Node]string, len(co))
			for n := range co {
				clauseText[n] = ctrlClauseText(n)
			}
			sub := buildCtrlMap(f, pkg.Fset, co, clauseText)
			for k, v := range sub {
				ctrlMap[k] = v
			}
		}
		res.CtrlMap = ctrlMap

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
		// Accumulate this package's component objects and use info for the
		// cross-package reference pass (design §3). Done before the type-error
		// `continue`s below so every indexed package participates.
		for key, obj := range compObjByKey {
			compObjOwner[obj] = compOwner{dir: pkgDir, key: key}
		}
		analyzed = append(analyzed, analyzedPkg{dir: pkgDir, fset: pkg.Fset, info: pkg.TypesInfo})

		// Build the single-package cross/nav indexes (shared with Module.Package).
		res.CrossIndex, res.NavIndex = buildCrossNav(compByKey, objKey, fset, pkg.Fset, pkg.TypesInfo, pkg.Types)
		res.UnusedImports = detectUnusedImports(pkg, importsByDir[pkgDir], fset)

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

	// Cross-package reference pass: route a use of an imported component into
	// the DECLARING component's CrossRef. In-package refs were already added by
	// Case 1 above (owner.dir == ap.dir, skipped here). For a single-dir batch
	// compObjOwner holds one dir, so nothing is appended. See design §3.
	for _, ap := range analyzed {
		for id, obj := range ap.info.Uses {
			owner, ok := compObjOwner[obj]
			if !ok || owner.dir == ap.dir {
				continue
			}
			p := ap.fset.Position(id.Pos())
			if strings.HasSuffix(p.Filename, ".x.go") {
				continue // synthetic skeleton position, no //line
			}
			res := result[owner.dir]
			if res == nil || res.CrossIndex == nil {
				continue
			}
			cr := res.CrossIndex[owner.key]
			cr.Refs = append(cr.Refs, p)
			res.CrossIndex[owner.key] = cr
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

// detectUnusedImports correlates the package's type errors with the .gsx
// positions of its hoisted imports. It returns a non-nil map only when EVERY
// type error is a genuine "imported and not used" error landing on a hoisted
// import line. If any error is not of that exact class (e.g. "could not import"
// for an unresolvable package, or any semantic error on a non-import line), the
// analysis is unreliable and nil is returned — never remove under uncertainty.
// Returns nil when there are no type errors (nothing unused).
func detectUnusedImports(pkg *packages.Package, imports []importSpec, gsxFset *token.FileSet) map[string][]UnusedImport {
	if len(pkg.TypeErrors) == 0 || len(imports) == 0 {
		return nil
	}
	type posKey struct {
		file string
		line int
	}
	byPos := map[posKey][]importSpec{}
	for _, imp := range imports {
		if !imp.pos.IsValid() {
			continue // unresolved position: cannot correlate safely
		}
		p := gsxFset.Position(imp.pos)
		k := posKey{p.Filename, p.Line}
		byPos[k] = append(byPos[k], imp)
	}
	out := map[string][]UnusedImport{}
	for _, e := range pkg.TypeErrors {
		ep := e.Fset.Position(e.Pos)
		specs, ok := byPos[posKey{ep.Filename, ep.Line}]
		if !ok || !strings.Contains(e.Msg, "imported and not used") {
			return nil // not a clean unused-import error → analysis unreliable, remove nothing
		}
		spec := specs[0]
		if len(specs) > 1 {
			spec = pickImportByPath(specs, e.Msg)
		}
		out[ep.Filename] = append(out[ep.Filename], UnusedImport{Name: spec.name, Path: spec.path})
	}
	return out
}

// pickImportByPath disambiguates several imports sharing one .gsx line using the
// path go/types names in the error (`"<path>" imported ...`). Falls back to the
// first spec if the path is not found.
func pickImportByPath(specs []importSpec, msg string) importSpec {
	if i := strings.IndexByte(msg, '"'); i >= 0 {
		if j := strings.IndexByte(msg[i+1:], '"'); j >= 0 {
			path := msg[i+1 : i+1+j]
			for _, s := range specs {
				if s.path == path {
					return s
				}
			}
		}
	}
	return specs[0]
}

// GeneratePackages is GeneratePackagesWithFilters with the built-in std filter
// package (kept for the test corpus and any std-only caller).
func GeneratePackages(moduleDir string, dirs []string) (map[string]*PackageResult, error) {
	return GeneratePackagesWithFilters(moduleDir, dirs, nil, nil, nil, nil, nil, nil, nil)
}

// detectUnusedImportsFromErrs is the Module path equivalent of detectUnusedImports:
// it correlates raw go/types type errors (from checkSkeletonPackage) with the
// hoisted .gsx import specs to identify unused imports, using the same conservative
// logic as the batch path (returns nil if any error is not a clean unused-import).
func detectUnusedImportsFromErrs(typeErrs []types.Error, imports []importSpec, gsxFset *token.FileSet) map[string][]UnusedImport {
	if len(typeErrs) == 0 || len(imports) == 0 {
		return nil
	}
	type posKey struct {
		file string
		line int
	}
	byPos := map[posKey][]importSpec{}
	for _, imp := range imports {
		if !imp.pos.IsValid() {
			continue // unresolved position: cannot correlate safely
		}
		p := gsxFset.Position(imp.pos)
		k := posKey{p.Filename, p.Line}
		byPos[k] = append(byPos[k], imp)
	}
	out := map[string][]UnusedImport{}
	for _, e := range typeErrs {
		ep := e.Fset.Position(e.Pos)
		specs, ok := byPos[posKey{ep.Filename, ep.Line}]
		if !ok || !strings.Contains(e.Msg, "imported and not used") {
			return nil // not a clean unused-import error → analysis unreliable, remove nothing
		}
		spec := specs[0]
		if len(specs) > 1 {
			spec = pickImportByPath(specs, e.Msg)
		}
		out[ep.Filename] = append(out[ep.Filename], UnusedImport{Name: spec.name, Path: spec.path})
	}
	return out
}
