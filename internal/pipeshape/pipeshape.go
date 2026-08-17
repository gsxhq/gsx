// Package pipeshape walks the lowered shape of a gsx pipeline expression
// (`{ x |> f(args…) }`). The lowering nests each stage's filter call through
// its subject: `Func([ctx,] subject, args…)`, with the subject itself the
// previous stage's call (or, at the bottom, the seed expression) — Walk peels
// that chain outside-in and Stages reads the per-node pipe-stage metadata that
// tells a caller how many layers to peel.
package pipeshape

import (
	"go/ast"

	gsxast "github.com/gsxhq/gsx/ast"
)

// CtxIdent is the reserved ambient render-context identifier the codegen lowering
// injects as the first argument of a ctx-taking filter. It MUST match codegen's
// pipeCtxIdent ("ctx"). The value is stable; a real ctx-injected end-to-end guard
// lands with the filter-resolution wiring (std has no ctx filter today).
const CtxIdent = "ctx"

// Walk peels the N seed-first filter layers of a lowered pipeline expression.
// The lowering shape is `Func([ctx,] subject, args…)` nested via the subject, so
// for stage i it returns the filter's Sel ident and its user stage args, and at
// the bottom the (unwrapped) seed expression. ok=false on any unexpected shape.
func Walk(skel ast.Expr, n int) (selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
	if n <= 0 {
		return nil, nil, nil, false
	}
	selSel = make([]*ast.Ident, n)
	selArgs = make([][]ast.Expr, n)
	cur := skel
	for i := n - 1; i >= 0; i-- {
		call, isCall := cur.(*ast.CallExpr)
		if !isCall {
			return nil, nil, nil, false
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || len(call.Args) == 0 {
			return nil, nil, nil, false
		}
		selSel[i] = sel.Sel
		subjIdx := 0
		if id, isID := call.Args[0].(*ast.Ident); isID && id.Name == CtxIdent {
			subjIdx = 1 // ctx injected at args[0]
		}
		if subjIdx >= len(call.Args) {
			return nil, nil, nil, false
		}
		selArgs[i] = call.Args[subjIdx+1:]
		cur = call.Args[subjIdx]
	}
	return selSel, selArgs, unwrapParens(cur), true
}

func unwrapParens(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// Stages returns the gsx pipe stages carried by node, or nil when node is not a
// pipe-stage-bearing kind.
func Stages(node gsxast.Node) []gsxast.PipeStage {
	switch e := node.(type) {
	case *gsxast.Interp:
		return e.Stages
	case *gsxast.ExprAttr:
		return e.Stages
	case *gsxast.SpreadAttr:
		return e.Stages
	case *gsxast.ComposedPart:
		return e.Stages
	case *gsxast.ValueArm:
		return e.Stages
	}
	return nil
}
