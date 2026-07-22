package lsp

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
)

// expectedTypeAt derives the type the Go cursor is expected to produce, for
// ranking (never filtering) candidates — a candidate whose type matches sorts
// ahead of the rest WITHIN its locality tier. It implements only the
// cheaply-sound cases and fails silent (returns nil, no boost) otherwise.
//
// DERIVATION SUBSET (v1):
//   - Inner call argument `{ f(▮) }` / `title={ f(▮) }`: the innermost enclosing
//     CallExpr WITHIN the bridged hole expression drives the expected type — the
//     callee's parameter at the cursor's argument index (variadic tail handled).
//     This is derivable from the bridged skel alone (no enclosing-statement
//     context needed). Selector-receiver positions are naturally excluded: a
//     cursor on `X` in `X.m()` sits before the call's `(`, so no call matches.
//   - Component attr value hole `title={ ▮ }`: the bound parameter's type, read
//     from the ephemeral ComponentCalls fact keyed by the ExprAttr under the
//     cursor.
//
// The inner-call case is tried FIRST so that `title={ f(▮) }` ranks on f's
// parameter (the immediate position) rather than title's type.
//
// DELIBERATELY SKIPPED (need enclosing-statement / AST-path context the bridge
// does not retain, or are too broad to rank on):
//   - Cross-statement positions: assignment RHS (LHS type), `return` result type,
//     binary-operand type, top-level call argument where the hole IS the argument
//     rather than containing the call. The bridge hands us only the hole's own
//     skeleton expr, not its enclosing skeleton statement, so these are
//     unreachable without retaining whole skeleton files.
//   - Interp render position `{ ▮ }` top-level: expected = "renderable", too broad
//     to rank on — skipped (spec 1c).
func expectedTypeAt(eph *Package, cc completionContext, skel ast.Expr, skelPos token.Pos, exprStartOff int) types.Type {
	if eph == nil || eph.Info == nil {
		return nil
	}
	// Inner call argument, most specific — try first.
	if t := innerCallArgExpectedType(eph.Info, skel, skelPos); t != nil {
		return t
	}
	// Component attr value hole: the bound parameter's declared type.
	if _, ok := cc.node.(*gsxast.ExprAttr); ok {
		if t := componentAttrExpectedType(eph, exprStartOff); t != nil {
			return t
		}
	}
	return nil
}

// componentAttrExpectedType returns the declared type of the component parameter
// bound to the ExprAttr whose value expression starts at byte offset
// exprStartOff, or nil when no planned component call binds such an attribute.
// The ComponentCalls facts are keyed by authored *gsxast.Element and their
// Params by the exact authored *gsxast.ExprAttr, so matching the attribute's
// value-expression start offset (stable across the classifier's and the
// ephemeral analysis's independent parses of identical bytes) pins the binding.
func componentAttrExpectedType(eph *Package, exprStartOff int) types.Type {
	if eph == nil || eph.GSXFset == nil || eph.ComponentCalls == nil {
		return nil
	}
	for _, fact := range eph.ComponentCalls {
		for attr, param := range fact.Params {
			ea, ok := attr.(*gsxast.ExprAttr)
			if !ok || !ea.ExprPos.IsValid() || param.Var == nil {
				continue
			}
			if eph.GSXFset.Position(ea.ExprPos).Offset == exprStartOff {
				return param.Var.Type()
			}
		}
	}
	return nil
}

// innerCallArgExpectedType finds the innermost *ast.CallExpr in root whose
// argument region ( `(` .. `)` ] contains pos, and returns the callee
// parameter type at the cursor's argument index. Returns nil when the cursor is
// not inside a call's argument list, the callee has no resolvable signature (a
// type conversion `T(x)`, an unresolved name), or the argument index has no
// parameter. root is the bridged hole expression and pos the cursor's skeleton
// position; both live in the ephemeral Info's universe, so the resolved
// parameter type is directly comparable to candidate types.
func innerCallArgExpectedType(info *types.Info, root ast.Expr, pos token.Pos) types.Type {
	if info == nil || root == nil || !pos.IsValid() {
		return nil
	}
	var best *ast.CallExpr
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// The cursor must sit within the argument region: strictly after the
		// opening paren and at or before the closing paren (a completion cursor
		// sits after the last typed byte). A cursor on the callee/receiver is
		// before Lparen and so never matches — selector-receiver irrelevance.
		if pos <= call.Lparen || pos > call.Rparen {
			return true
		}
		// Innermost wins: prefer the call with the tightest paren span.
		if best == nil || (call.Lparen >= best.Lparen && call.Rparen <= best.Rparen) {
			best = call
		}
		return true
	})
	if best == nil {
		return nil
	}
	tv, ok := info.Types[best.Fun]
	if !ok || tv.Type == nil {
		return nil
	}
	sig, ok := tv.Type.Underlying().(*types.Signature)
	if !ok {
		return nil
	}
	return paramTypeAt(sig, callArgIndexAt(best, pos))
}

// callArgIndexAt returns the positional argument index the cursor at pos falls
// in: the index of the first argument whose End is at or after pos, or len(Args)
// when pos is past every argument (the next, not-yet-typed positional slot).
func callArgIndexAt(call *ast.CallExpr, pos token.Pos) int {
	for i, arg := range call.Args {
		if pos <= arg.End() {
			return i
		}
	}
	return len(call.Args)
}

// paramTypeAt returns the type of the idx-th positional parameter of sig,
// resolving the variadic tail: an index at or past the final variadic parameter
// yields that parameter's element type (each variadic argument has the element
// type, not the slice type). Returns nil for a non-variadic overflow (more
// arguments than parameters) or an empty signature.
func paramTypeAt(sig *types.Signature, idx int) types.Type {
	params := sig.Params()
	n := params.Len()
	if n == 0 || idx < 0 {
		return nil
	}
	if idx < n-1 {
		return params.At(idx).Type()
	}
	last := params.At(n - 1)
	if sig.Variadic() {
		if s, ok := last.Type().(*types.Slice); ok {
			return s.Elem()
		}
	}
	if idx == n-1 {
		return last.Type()
	}
	return nil
}

// typeMatches reports whether a candidate of type candType satisfies the
// expected type at the cursor: directly assignable, or — for a function
// candidate — its single result is assignable (calling it satisfies the
// position). Both types must come from the same go/types universe (the ephemeral
// Info) for AssignableTo to be sound; the callers guarantee this.
func typeMatches(candType, expected types.Type) bool {
	if candType == nil || expected == nil {
		return false
	}
	if types.AssignableTo(candType, expected) {
		return true
	}
	if sig, ok := candType.Underlying().(*types.Signature); ok {
		if res := sig.Results(); res.Len() == 1 && types.AssignableTo(res.At(0).Type(), expected) {
			return true
		}
	}
	return false
}

// rankedSortText builds a completion item's SortText. With no expected type in
// play (expected == nil) it is byte-identical to the historical
// fmt.Sprintf("%02d%s", tier, label) — the no-regression contract for every
// context that derives no expected type. With an expected type it inserts a
// single match digit AFTER the tier digits and BEFORE the label: '0' when the
// candidate's type matches, '1' otherwise. This keeps locality tiers dominant
// (a matched package-scope item never outranks an unmatched local) while
// refining the order WITHIN a tier so type-matching candidates lead their ties.
func rankedSortText(tier int, label string, expected, candType types.Type) string {
	if expected == nil {
		return fmt.Sprintf("%02d%s", tier, label)
	}
	bit := byte('1')
	if typeMatches(candType, expected) {
		bit = '0'
	}
	return fmt.Sprintf("%02d%c%s", tier, bit, label)
}
