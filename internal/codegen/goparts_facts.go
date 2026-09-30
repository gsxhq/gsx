package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strconv"

	gsxast "github.com/gsxhq/gsx/ast"
)

// evalKey identifies an evalNode by its .gsx source span.
type evalKey struct{ start, end token.Pos }

// evalFact is what go/types knows about one call or logical operation of a
// field holding a nested literal — what fieldPins needs to pin it without
// changing its meaning.
type evalFact struct {
	// conversion: the call converts to a type; it calls nothing.
	conversion bool
	// constant: the value is a compile-time constant; nothing is evaluated,
	// and a temp would give an untyped constant its default type.
	constant bool
	// untypedBool: a logical operation over untyped booleans (comparisons,
	// untyped boolean constants), whose value is itself untyped.
	untypedBool bool
	// typ is the type go/types recorded: after an untyped value's implicit
	// conversion, its final type.
	typ types.Type
}

// harvestEvalFacts records the evalFact of every call and logical operation
// in the fields of gsxFile that hold a nested literal with a hole — the only
// fields where a hole can hoist. skel is gsxFile's type-checked skeleton,
// markups its probe markup groups (the `_gsxelem(N)` index).
//
// A field's skeleton probe is its parts verbatim with each construct replaced
// by its tagged probe IIFE, so the skeleton subtree mirrors the field's
// masked parse node for node (fieldShape.root). The subtree is found by
// climbing from one literal's IIFE as many levels as the literal's masked
// operand sits below the masked root, and the two are aligned in preorder; a
// field whose trees disagree records nothing (a pin that needs it then fails
// loudly in fieldPins.fact).
func harvestEvalFacts(gsxFile *gsxast.File, skel *goast.File, markups [][]gsxast.Markup, info *types.Info, out map[evalKey]evalFact) {
	type field struct {
		parts  []gsxast.GoPart
		syntax fieldSyntax
		lit    *gsxast.EmbeddedInterp
	}
	var fields []field
	consider := func(owner gsxast.Node, parts []gsxast.GoPart) {
		syntax := fieldSyntaxOf(owner)
		if syntax == syntaxCaseList || syntax == syntaxForClause {
			return
		}
		for _, part := range parts {
			if lit, ok := part.(*gsxast.EmbeddedInterp); ok && literalHasHole(lit) {
				fields = append(fields, field{parts: parts, syntax: syntax, lit: lit})
				return
			}
		}
	}
	gsxast.InspectEmbedded(gsxFile, func(n gsxast.Node) bool {
		gsxast.GoFields(n, func(f gsxast.GoField) {
			if *f.Embedded != nil {
				consider(f.Owner, *f.Embedded)
			}
		})
		if interp, ok := n.(*gsxast.Interp); ok && interp.Embedded != nil {
			consider(interp, interp.Embedded)
		}
		return true
	})
	if len(fields) == 0 {
		return
	}

	// The probe IIFE of the literal whose segments are markups[N] is tagged
	// `_gsxelem(N)`; record each IIFE's path from the file root.
	groupIndex := make(map[*gsxast.Markup]int, len(markups))
	for i, group := range markups {
		if len(group) > 0 {
			groupIndex[&group[0]] = i
		}
	}
	ancestors := map[int][]goast.Node{}
	var stack []goast.Node
	goast.Inspect(skel, func(n goast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if idx, ok := probeIIFEIndex(n); ok {
			ancestors[idx] = append(append([]goast.Node(nil), stack...), n)
		}
		stack = append(stack, n)
		return true
	})

	for _, fd := range fields {
		idx, ok := groupIndex[&fd.lit.Segments[0]]
		if !ok {
			continue
		}
		anc, ok := ancestors[idx]
		if !ok {
			continue
		}
		shape, err := analyzeField(fd.parts, fd.syntax)
		if err != nil {
			continue
		}
		depth, ok := shape.constructDepth[fd.lit]
		if !ok || depth >= len(anc) {
			continue
		}
		pairs, ok := alignProbe(shape, anc[len(anc)-1-depth])
		if !ok {
			continue
		}
		for _, e := range shape.evals {
			if e.kind != evalCall && e.kind != evalLogical {
				continue
			}
			expr, ok := pairs[e.node].(goast.Expr)
			if !ok {
				continue
			}
			tv := info.Types[expr]
			fact := evalFact{constant: tv.Value != nil, typ: tv.Type}
			switch x := expr.(type) {
			case *goast.CallExpr:
				fact.conversion = info.Types[x.Fun].IsType()
			case *goast.BinaryExpr:
				fact.untypedBool = untypedBoolOperation(x, info)
			}
			out[evalKey{e.start, e.end}] = fact
		}
	}
}

// probeIIFEIndex reports whether n is a construct's probe IIFE
// (`func() T { _gsxelem(N); … }()`) and returns N.
func probeIIFEIndex(n goast.Node) (int, bool) {
	call, ok := n.(*goast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return 0, false
	}
	fl, ok := call.Fun.(*goast.FuncLit)
	if !ok || !isEmbeddedElemProbeFuncLit(fl) {
		return 0, false
	}
	marker := fl.Body.List[0].(*goast.ExprStmt).X.(*goast.CallExpr)
	lit, ok := marker.Args[0].(*goast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	idx, err := strconv.Atoi(lit.Value)
	return idx, err == nil
}

// alignProbe pairs each node of shape's masked parse with the node of the
// skeleton subtree skelRoot at the same preorder position, a construct's
// masked operand with its probe IIFE. For a header only the init statement
// and the condition, tag or type-switch guard are aligned (the skeleton's
// statement bodies are its own). It fails when the trees disagree.
func alignProbe(shape fieldShape, skelRoot goast.Node) (map[goast.Node]goast.Node, bool) {
	maskedRoots, ok := headerParts(shape.root)
	if !ok {
		return nil, false
	}
	skelRoots, ok := headerParts(skelRoot)
	if !ok || len(maskedRoots) != len(skelRoots) {
		return nil, false
	}
	var masked, skeleton []goast.Node
	for i := range maskedRoots {
		if (maskedRoots[i] == nil) != (skelRoots[i] == nil) {
			return nil, false
		}
		if maskedRoots[i] == nil {
			continue
		}
		goast.Inspect(maskedRoots[i], func(n goast.Node) bool {
			if n != nil {
				masked = append(masked, n)
			}
			return true
		})
		goast.Inspect(skelRoots[i], func(n goast.Node) bool {
			if n == nil {
				return true
			}
			skeleton = append(skeleton, n)
			_, iife := probeIIFEIndex(n)
			return !iife
		})
	}
	if len(masked) != len(skeleton) {
		return nil, false
	}
	pairs := make(map[goast.Node]goast.Node, len(masked))
	for i, m := range masked {
		s := skeleton[i]
		if _, construct := shape.constructs[m]; construct {
			if _, iife := probeIIFEIndex(s); !iife {
				return nil, false
			}
		} else if reflect.TypeOf(m) != reflect.TypeOf(s) {
			return nil, false
		}
		pairs[m] = s
	}
	return pairs, true
}

// headerParts returns the subtrees of root a field's text covers: root itself
// for an expression, the init statement and condition/tag/guard for an
// if/switch header.
func headerParts(root goast.Node) ([]goast.Node, bool) {
	var init goast.Stmt
	var rest goast.Node
	switch s := root.(type) {
	case *goast.IfStmt:
		init = s.Init
		if s.Cond != nil {
			rest = s.Cond
		}
	case *goast.SwitchStmt:
		init = s.Init
		if s.Tag != nil {
			rest = s.Tag
		}
	case *goast.TypeSwitchStmt:
		init = s.Init
		if s.Assign != nil {
			rest = s.Assign
		}
	case goast.Expr:
		return []goast.Node{s}, true
	default:
		return nil, false
	}
	if init == nil {
		return []goast.Node{nil, rest}, true
	}
	return []goast.Node{init, rest}, true
}

// untypedBoolOperation reports whether the logical operation x combines
// untyped booleans only — comparisons and untyped boolean constants, through
// parentheses and `!` — so that its value is untyped too.
func untypedBoolOperation(x goast.Expr, info *types.Info) bool {
	switch x := x.(type) {
	case *goast.ParenExpr:
		return untypedBoolOperation(x.X, info)
	case *goast.UnaryExpr:
		return x.Op == token.NOT && untypedBoolOperation(x.X, info)
	case *goast.BinaryExpr:
		switch x.Op {
		case token.LAND, token.LOR:
			return untypedBoolOperation(x.X, info) && untypedBoolOperation(x.Y, info)
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return true
		}
	case *goast.Ident:
		return untypedConst(info.Uses[x])
	case *goast.SelectorExpr:
		return untypedConst(info.Uses[x.Sel])
	}
	return false
}

func untypedConst(obj types.Object) bool {
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	b, ok := c.Type().(*types.Basic)
	return ok && b.Info()&types.IsUntyped != 0
}
