package lsp

import (
	"go/ast"
	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
)

// inspectWithEmbedded walks node exactly like gsxast.Inspect but ALSO descends
// into every *Interp's Embedded parts — the interleaved
// GoText/*Element/*Fragment/*EmbeddedInterp split codegen's analysis pass seats
// on an interpolation whose Go expression carries an operand-position <tag>/<>
// or prefixed-backtick (f`/js`/css`) literal (e.g. `{ wrap(<Badge/>) }`,
// `{ emphasize(f`hi @{name}`) }`). gsxast.Inspect treats *Interp as a leaf and
// never walks Embedded — correct for the parser/printer/fmt, which re-parse and
// never populate it — but the LSP navigates a codegen-analyzed AST where those
// embedded elements, their child interps/props, and each backtick literal's
// @{ } holes are real nodes with resolved types. Each embedded part is handed to
// gsxast.Inspect, so an embedded element's children, a fragment's children, and
// an EmbeddedInterp's segments recurse normally: nav reaches them as the SAME
// node types (and thus the SAME resolution) a body child would have. Nested
// embedding (an embedded interp that itself carries Embedded) is handled because
// the shared visit closure re-descends on every *Interp it meets.
func inspectWithEmbedded(node gsxast.Node, f func(gsxast.Node) bool) {
	var visit func(gsxast.Node) bool
	visit = func(n gsxast.Node) bool {
		if !f(n) {
			return false
		}
		if interp, ok := n.(*gsxast.Interp); ok {
			for _, part := range interp.Embedded {
				gsxast.Inspect(part, visit)
			}
		}
		return true
	}
	gsxast.Inspect(node, visit)
}

// innermostIdent returns the innermost *ast.Ident in n's subtree whose
// [Pos, End) contains pos, or nil if pos falls on no identifier (e.g. on a '.'
// or operator). Accepts any ast.Node (including ast.Expr, ast.Stmt, etc.).
func innermostIdent(n ast.Node, pos token.Pos) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(n, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		if pos < node.Pos() || pos >= node.End() {
			return false // pos not in this node; prune
		}
		if id, ok := node.(*ast.Ident); ok {
			found = id
		}
		return true // descend: a child ident (e.g. selector Sel) may be tighter
	})
	return found
}
