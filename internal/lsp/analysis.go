package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// CrossRef is one component's cross-boundary entry (see the .go->.gsx design):
// its name, its .gsx declaration, and every reference, as resolved positions.
type CrossRef struct {
	Name string
	Decl token.Position
	Refs []token.Position
}

// NavRef is one navigable Go reference (in a .go file) and the .gsx position it
// targets. From is the reference site; Name is the identifier text (used for
// cursor-span checking); To is the .gsx declaration target.
type NavRef struct {
	From token.Position
	Name string
	To   token.Position
}

// CtrlRef is the LSP mirror of codegen.ctrlRef: a control-flow clause's
// skeleton position and smallest containing skeleton node, used for
// go-to-definition on loop variables and condition identifiers.
type CtrlRef struct {
	ClauseStart token.Pos
	Node        ast.Node // skeleton node scoping innermostIdent
}

// SigTypeRef is the LSP mirror of codegen.SigTypeRef: one navigable signature
// span's position (GSXPos) and byte length (Len) in the .gsx — a parameter
// type, a type-parameter name or constraint, or a method receiver type —
// paired with the type-checked skeleton expression (SkelTyp) whose bytes are
// identical to that source span. The LSP bridges a cursor into SkelTyp by
// relative offset and resolves the identifier via go/types.
type SigTypeRef struct {
	GSXPos  token.Pos
	Len     int
	SkelTyp ast.Expr
}

// Package is the retained, read-only result of analyzing one .gsx package: the
// diagnostics plus everything the read-intelligence features need. GSXFset
// resolves gsx node positions; Fset resolves skeleton/object positions
// (honoring //line). Under the Module path both may point to the same
// *token.FileSet (the module-wide shared fset); callers must not assume they
// are distinct objects.
type Package struct {
	Diags      []diag.Diagnostic
	GSXFset    *token.FileSet
	Fset       *token.FileSet
	Info       *types.Info
	Types      *types.Package
	ExprMap    map[gsxast.Node]ast.Expr // gsx Interp/ExprAttr → skeleton go/ast expr
	Files      map[string]*gsxast.File  // .gsx path → parsed gsx AST
	CrossIndex map[string]CrossRef
	NavIndex   []NavRef // navigable Go references → .gsx targets (func, props-struct, field)

	// CtrlMap maps each control-flow node (ForMarkup/IfMarkup/GoBlock) to its
	// skeleton clause position and smallest containing skeleton node. Used by the
	// LSP for go-to-definition on loop variables and condition identifiers.
	CtrlMap map[gsxast.Node]CtrlRef

	// SigTypes maps each component to the type spans in its parameter list, so a
	// cursor on an identifier inside a parameter TYPE (e.g. `store.Comment` in
	// `component C(c []store.Comment)`) can be bridged into the type-checked
	// skeleton and resolved for go-to-definition / hover.
	SigTypes map[*gsxast.Component][]SigTypeRef

	// UnusedImports lists, per .gsx file path, imports that file declares but does
	// not use — what formatting may safely drop. Empty when analysis is unreliable.
	UnusedImports map[string][]gsxfmt.ImportRef
}
