package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// positionOf resolves a type-object position to a token.Position using the
// appropriate FileSet. It tries pkg.Fset first (covers skeleton/same-package
// objects), and when that returns an empty Filename (position belongs to an
// external importer's fset), falls back to pkg.ExtFset. This handles the warm-
// Module path's split-fset layout transparently; in the batch path ExtFset is
// nil and only Fset is used.
func positionOf(pkg *Package, pos token.Pos) token.Position {
	if pkg.Fset != nil {
		if p := pkg.Fset.Position(pos); p.Filename != "" {
			return p
		}
	}
	if pkg.ExtFset != nil {
		return pkg.ExtFset.Position(pos)
	}
	return token.Position{}
}

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

// Package is the retained, read-only result of analyzing one .gsx package: the
// diagnostics plus everything the read-intelligence features need. The two
// FileSets are distinct — GSXFset resolves gsx node positions; Fset resolves
// skeleton/object positions (honoring //line).
type Package struct {
	Diags      []diag.Diagnostic
	GSXFset    *token.FileSet
	Fset       *token.FileSet
	// ExtFset covers positions of external (imported) package objects when the
	// warm-Module analysis path is used; nil in the batch path (where Fset covers
	// all packages). Use positionOf(pkg, pos) instead of pkg.Fset.Position(pos)
	// when the object may come from an imported package.
	ExtFset    *token.FileSet
	Info       *types.Info
	Types      *types.Package
	ExprMap    map[gsxast.Node]ast.Expr // gsx Interp/ExprAttr → skeleton go/ast expr
	Files      map[string]*gsxast.File  // .gsx path → parsed gsx AST
	CrossIndex map[string]CrossRef
	NavIndex   []NavRef // navigable Go references → .gsx targets (func, props-struct, field)

	// UnusedImports lists, per .gsx file path, imports that file declares but does
	// not use — what formatting may safely drop. Empty when analysis is unreliable.
	UnusedImports map[string][]gsxfmt.ImportRef
}
