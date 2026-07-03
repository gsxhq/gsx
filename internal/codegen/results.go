package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
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

// PackageResult is the per-package outcome of code generation.
type PackageResult struct {
	Files map[string][]byte // .gsx path -> generated .x.go source
	Diags []diag.Diagnostic // all diagnostics collected for this package

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

	// CtrlMap maps each control-flow node (ForMarkup/IfMarkup/GoBlock, and each
	// value-form if condition's *ValueIf) to its
	// skeleton clause position and smallest containing skeleton go/ast node.
	// Used by the LSP to bridge a cursor in a for/if/goblock clause to the
	// skeleton for go-to-definition on loop variables and condition identifiers.
	CtrlMap map[gsxast.Node]ctrlRef

	// SigTypes maps each component to the navigable spans in its signature —
	// parameter types (e.g. `store.Comment` in `component C(c []store.Comment)`),
	// type-parameter names and constraints, and a method receiver type — so the
	// LSP can answer go-to-definition / hover on the identifiers inside them.
	SigTypes map[*gsxast.Component][]SigTypeRef

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

// detectUnusedImports correlates raw go/types type errors (from
// checkSkeletonPackage) with the hoisted .gsx import specs to identify unused
// imports, using the conservative logic: returns nil if any error is not a clean
// unused-import error, or when there are no type errors (nothing unused). Never
// removes imports under uncertainty.
func detectUnusedImports(typeErrs []types.Error, imports []importSpec, gsxFset *token.FileSet) map[string][]UnusedImport {
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
