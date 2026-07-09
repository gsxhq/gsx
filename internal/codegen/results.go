package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// CrossRef is one component's cross-boundary entry: its name, its .gsx
// declaration, and every reference (resolved positions — .go call sites stay
// .go; .gsx <Card/> tags map to .gsx via the skeleton's child-tag //line).
// Name's length bounds the cursor-on-reference span check in the LSP.
//
// Decls holds every build-tag variant's declaration position (sorted by
// filename then offset); Decl is kept equal to Decls[0] as the primary
// position for callers that only care about "the" declaration.
type CrossRef struct {
	Name  string
	Decl  token.Position
	Decls []token.Position
	Refs  []token.Position
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
