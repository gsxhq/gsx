package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
)

// ctxIdent is the reserved ambient render-context identifier the codegen lowering
// injects as the first argument of a ctx-taking filter. It MUST match codegen's
// pipeCtxIdent ("ctx"); the ctx-injected pipeline e2e test guards this.
const ctxIdent = "ctx"

// walkPipe peels the N seed-first filter layers of a lowered pipeline expression.
// The lowering shape is `Func([ctx,] subject, args…)` nested via the subject, so
// for stage i it returns the filter's Sel ident and its user stage args, and at
// the bottom the (unwrapped) seed expression. ok=false on any unexpected shape.
func walkPipe(skel ast.Expr, n int) (selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
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
		if id, isID := call.Args[0].(*ast.Ident); isID && id.Name == ctxIdent {
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

func pipeStages(node gsxast.Node) []gsxast.PipeStage {
	switch e := node.(type) {
	case *gsxast.Interp:
		return e.Stages
	case *gsxast.ExprAttr:
		return e.Stages
	}
	return nil
}

func useObj(pkg *Package, id *ast.Ident) types.Object {
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	return obj
}

func identInArgs(args []ast.Expr, pos token.Pos) *ast.Ident {
	for _, a := range args {
		if a.Pos() <= pos && pos < a.End() {
			return innermostIdent(a, pos)
		}
	}
	return nil
}

// pipedTarget resolves the go/types object under the cursor inside a piped node,
// plus the .gsx byte span of the hovered region (for a Range). ok=false (→ null)
// when the cursor is on no resolvable region or the lowered shape is unexpected.
// It never panics — every assertion is guarded.
func pipedTarget(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (types.Object, [2]int, bool) {
	stages := pipeStages(node)
	skel := pkg.ExprMap[node]
	if skel == nil || len(stages) == 0 || pkg.Info == nil || pkg.GSXFset == nil {
		return nil, [2]int{}, false
	}
	selSel, selArgs, seedExpr, ok := walkPipe(skel, len(stages))
	if !ok {
		return nil, [2]int{}, false
	}

	// seed region: [seedStart, seedStart+len(seedText)); byte-identical to seedExpr.
	seedStart := pkg.GSXFset.Position(exprPos).Offset
	seedText := exprText(node)
	if seedExpr != nil && off >= seedStart && off < seedStart+len(seedText) {
		if id := innermostIdent(seedExpr, seedExpr.Pos()+token.Pos(off-seedStart)); id != nil {
			if obj := useObj(pkg, id); obj != nil {
				start := seedStart + int(id.Pos()-seedExpr.Pos())
				return obj, [2]int{start, start + len(id.Name)}, true
			}
		}
		return nil, [2]int{}, false
	}

	for i, st := range stages {
		// filter name region.
		if st.NamePos.IsValid() {
			nameStart := pkg.GSXFset.Position(st.NamePos).Offset
			if off >= nameStart && off < nameStart+len(st.Name) {
				if selSel[i] != nil {
					if obj := useObj(pkg, selSel[i]); obj != nil {
						return obj, [2]int{nameStart, nameStart + len(st.Name)}, true
					}
				}
				return nil, [2]int{}, false
			}
		}
		// filter args region: byte-identical to the skeleton args.
		if st.HasArgs && st.ArgsPos.IsValid() && len(selArgs[i]) > 0 {
			argsStart := pkg.GSXFset.Position(st.ArgsPos).Offset
			if off >= argsStart && off < argsStart+len(st.Args) {
				base := selArgs[i][0].Pos()
				if id := identInArgs(selArgs[i], base+token.Pos(off-argsStart)); id != nil {
					if obj := useObj(pkg, id); obj != nil {
						start := argsStart + int(id.Pos()-base)
						return obj, [2]int{start, start + len(id.Name)}, true
					}
				}
				return nil, [2]int{}, false
			}
		}
	}
	return nil, [2]int{}, false
}
