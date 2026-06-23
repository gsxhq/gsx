// Package codegen lowers a parsed gsx AST to Go source (.x.go) targeting the
// gsx runtime.
//
// SPIKE SCOPE (deliberately growing — see docs): components with inline params,
// pass-through Go (GoChunks: types/helpers), static markup, and interpolation
// whose expression type is resolved by go/types in the component's scope. Used
// params are bound to same-named locals so interpolation expressions emit
// VERBATIM (e.g. {user.Name} -> gw.Text(user.Name) after `user := p.User`).
// Still unsupported (clear errors): attributes, control flow, methods, `?`,
// child components, and any render type beyond string/int.
package codegen

import (
	"fmt"
	"go/token"
	"os"
	"path/filepath"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
	"github.com/gsxhq/gsx/internal/wsnorm"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// GeneratePackage generates a .x.go for every .gsx file in dir using only the
// gsx built-in filter package (std). It is a thin wrapper over
// GeneratePackageWithFilters preserved for callers (and the test harness) that
// do not configure custom filter packages.
func GeneratePackage(dir string) (map[string][]byte, error) {
	return GeneratePackageWithFilters(dir, []string{stdImportPath}, nil, nil, nil)
}

// GeneratePackageWithFilters generates a .x.go for every .gsx file in dir,
// resolving interpolation types with go/types over the WHOLE package — the
// package's hand-written .go files plus synthesized skeletons of the gsx
// components, injected via go/packages Overlay. This resolves cross-file type
// references and cross-component calls. dir must be inside a Go module. The
// result maps each .gsx path to its generated source.
//
// filterPkgs is the ORDERED list of filter package import paths whose exported
// funcs are harvested (by contract) into the filter table, with LAST-WINS
// precedence: a later package shadows an earlier same-named filter. Each filter
// is qualified at lowering time by its owning package's reserved import alias
// (std → _gsxstd, every other package → _gsxf<i>). An empty filterPkgs defaults
// to just the std package; duplicate paths are removed preserving first-seen
// order (last-wins still applies to NAME collisions across distinct packages).
func GeneratePackageWithFilters(dir string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error)) (map[string][]byte, error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	filterPkgs = dedupFilterPkgs(filterPkgs)
	matches, err := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	bag := diag.NewBag(fset)
	files := map[string]*gsxast.File{}
	for _, m := range matches {
		src, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		f, err := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		// Apply the JSX whitespace model before type resolution + emit, so cosmetic
		// indentation is not rendered (the parser stays lossless; wsnorm is the one
		// shared pass — mirror this in batch.go's GeneratePackages).
		wsnorm.Normalize(f)
		// Classify each <script> @{ } hole's JS context (and un-split comment holes
		// to literal text) BEFORE type resolution + emit. Fails closed on an
		// unsafe/ambiguous position, surfacing as this file's codegen diagnostic.
		if !jsx.ResolveScripts(f, bag) {
			if diags := bag.Sorted(); len(diags) > 0 {
				return nil, fmt.Errorf("%s: %s", m, diags[0].Message)
			}
			return nil, fmt.Errorf("%s: jsx: unclassifiable error", m)
		}
		files[m] = f
	}

	// Derive the call-site prop-field map from the parsed ASTs (same-package),
	// BEFORE type resolution. The SAME map drives BOTH the probe split
	// (resolveTypesPkg → buildSkeleton → emitProbes) and the emit split
	// (generateFile → genChildComponent → childPropsLiteral), so emit ≡ probe is
	// guaranteed with no second type-check.
	propFields, err := componentPropFieldsFor(files)
	if err != nil {
		return nil, err
	}

	resolved, table, err := resolveTypesPkgWithFilters(dir, files, propFields, filterPkgs, fset)
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}
	for path, file := range files {
		gen, genOK := generateFile(file, resolved, table, propFields, fset, cls, bag, cssMin, jsMin)
		if !genOK {
			// Collect diagnostics from bag into an error for the legacy single-package API.
			if diags := bag.Sorted(); len(diags) > 0 {
				return nil, fmt.Errorf("%s: %s", path, diags[len(diags)-1].Message)
			}
			return nil, fmt.Errorf("%s: codegen: unknown error", path)
		}
		out[path] = gen
	}
	return out, nil
}

// dedupFilterPkgs returns filterPkgs with duplicate import paths removed,
// preserving first-seen order. An empty (or nil) list defaults to just the gsx
// built-in std filter package, so callers always get std available.
func dedupFilterPkgs(filterPkgs []string) []string {
	if len(filterPkgs) == 0 {
		return []string{stdImportPath}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(filterPkgs))
	for _, p := range filterPkgs {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
