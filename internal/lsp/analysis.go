package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// Package is the retained, read-only result of analyzing one .gsx package: the
// diagnostics plus everything the read-intelligence features need. The two
// FileSets are distinct — GSXFset resolves gsx node positions; Fset resolves
// skeleton/object positions (honoring //line).
type Package struct {
	Diags   []diag.Diagnostic
	GSXFset *token.FileSet
	Fset    *token.FileSet
	Info    *types.Info
	ExprMap map[gsxast.Node]ast.Expr // gsx Interp/ExprAttr → skeleton go/ast expr
	Files   map[string]*gsxast.File  // .gsx path → parsed gsx AST
}
