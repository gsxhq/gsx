package lsp

import (
	"go/ast"
	"go/token"
)

// innermostIdent returns the innermost *ast.Ident in expr's subtree whose
// [Pos, End) contains pos, or nil if pos falls on no identifier (e.g. on a '.'
// or operator).
func innermostIdent(expr ast.Expr, pos token.Pos) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(expr, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if pos < n.Pos() || pos >= n.End() {
			return false // pos not in this node; prune
		}
		if id, ok := n.(*ast.Ident); ok {
			found = id
		}
		return true // descend: a child ident (e.g. selector Sel) may be tighter
	})
	return found
}
