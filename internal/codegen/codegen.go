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
	gsxparser "github.com/gsxhq/gsx/parser"
)

// GeneratePackage generates a .x.go for every .gsx file in dir, resolving
// interpolation types with go/types over the WHOLE package — the package's
// hand-written .go files plus synthesized skeletons of the gsx components,
// injected via go/packages Overlay. This resolves cross-file type references
// and cross-component calls. dir must be inside a Go module. The result maps
// each .gsx path to its generated source.
func GeneratePackage(dir string) (map[string][]byte, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	files := map[string]*gsxast.File{}
	for _, m := range matches {
		src, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		f, err := gsxparser.ParseFile(fset, m, src, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
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

	resolved, table, err := resolveTypesPkg(dir, files, propFields)
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}
	for path, file := range files {
		gen, err := generateFile(file, resolved, table, propFields, fset)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[path] = gen
	}
	return out, nil
}
