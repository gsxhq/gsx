package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"slices"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// Hoisting an error-carrying hole moves its evaluation to a statement before
// the one consuming the field. That is sound only when the literal holding
// the hole is evaluated unconditionally within the field's Go: never on the
// right of && / || (Go may skip that side) and never inside a func literal
// (which runs later, or not at all). fieldShape finds those literals by
// parsing the field with every nested construct masked by a Go operand.

// Remedies for an error-carrying hole in a conditionally evaluated literal.
const (
	logicalRHSErrRemedy = "a literal on the right of && or || may be skipped, so its error cannot be returned before the expression — compute the value first"
	funcLitErrRemedy    = "a literal inside a func literal is evaluated only when the func runs, so its error cannot be returned before the expression — compute the value first"
)

// fieldSyntax is the Go grammar position a field's text occupies.
type fieldSyntax uint8

const (
	syntaxExpr         fieldSyntax = iota // an expression
	syntaxIfHeader                        // [init;] cond of an if
	syntaxSwitchHeader                    // [init;] tag of a switch
	syntaxCaseList                        // expression list of a case
	syntaxForClause                       // a for clause
)

// fieldSyntaxOf returns the grammar position of the Go-expression field owned
// by owner (lowerCtx.owner).
func fieldSyntaxOf(owner ast.Node) fieldSyntax {
	switch owner.(type) {
	case *ast.IfMarkup, *ast.CondAttr, *ast.ValueIf:
		return syntaxIfHeader
	case *ast.SwitchMarkup, *ast.SwitchAttr, *ast.ValueSwitch:
		return syntaxSwitchHeader
	case *ast.CaseClause, *ast.AttrCaseClause, *ast.ValueSwitchCase:
		return syntaxCaseList
	case *ast.ForMarkup:
		return syntaxForClause
	}
	return syntaxExpr
}

// fieldShape is the parse of one split field.
type fieldShape struct {
	// conditional maps each nested literal evaluated conditionally to the
	// goexpr-literal-error remedy its error-carrying holes are rejected with.
	conditional map[ast.GoPart]string
	// evals lists, in source order, every operand of the field whose
	// evaluation Go orders left to right — see evalNode — outside func
	// literals. Hoisting a construct's statements pins the ones before it
	// (fieldPins).
	evals []evalNode
	// root is the masked parse: the expression, or the if/switch statement
	// of a header. constructs maps each construct's masked operand node to
	// its part, and constructDepth gives that node's number of ancestors
	// within root (harvestEvalFacts aligns root with the skeleton through
	// them).
	root           goast.Node
	constructs     map[goast.Node]ast.GoPart
	constructDepth map[ast.GoPart]int
	// initEnd and bodyStart delimit an if/switch header's init statement,
	// [0, initEnd), from its condition or tag, [bodyStart, len): offsets in
	// the field's masked text (see maskedField.split). initEnd is -1 when the
	// header has no init statement.
	initEnd, bodyStart int
	masked             maskedField
}

// evalKind classifies an evalNode.
type evalKind uint8

const (
	evalCall    evalKind = iota // a call expression; a conversion parses as one too (evalFact tells them apart)
	evalRecv                    // a receive operation, <-x
	evalLogical                 // a && / || operation
	evalLiteral                 // a nested prefixed literal with a @{ } hole
)

// evalNode is one operand of a field that Go evaluates in lexical left-to-
// right order relative to the others: a function or method call, a receive,
// a logical operation (evaluated whole, with its short-circuit), or a nested
// literal whose holes make calls. start and end are its source span in the
// .gsx file, the key of its evalFact. node is its masked parse node.
type evalNode struct {
	start, end token.Pos
	kind       evalKind
	node       goast.Node
}

// maskedField is a field's parts with every nested construct replaced by a
// Go operand: `""` for a literal, `nil` for an element or fragment.
type maskedField struct {
	text   string
	starts []int // offset of parts[i] in text
	parts  []ast.GoPart
}

func maskParts(parts []ast.GoPart) maskedField {
	var b strings.Builder
	m := maskedField{starts: make([]int, len(parts)), parts: parts}
	for i, part := range parts {
		m.starts[i] = b.Len()
		switch p := part.(type) {
		case ast.GoText:
			b.WriteString(p.Src)
		case *ast.EmbeddedInterp:
			b.WriteString(`""`)
		default:
			b.WriteString("nil")
		}
	}
	m.text = b.String()
	return m
}

// split returns the parts covering masked offsets [from, to), slicing GoText
// runs at the boundaries. Boundaries never fall inside a construct's operand:
// they are Go syntax boundaries of the surrounding text.
func (m maskedField) split(from, to int) []ast.GoPart {
	var out []ast.GoPart
	for i, part := range m.parts {
		start := m.starts[i]
		end := len(m.text)
		if i+1 < len(m.parts) {
			end = m.starts[i+1]
		}
		lo, hi := max(start, from), min(end, to)
		if lo >= hi {
			continue
		}
		if t, ok := part.(ast.GoText); ok {
			piece := ast.GoText{Src: t.Src[lo-start : hi-start]}
			if t.Pos().IsValid() {
				ast.SetSpan(&piece, t.Pos()+token.Pos(lo-start), t.Pos()+token.Pos(hi-start))
			}
			out = append(out, piece)
			continue
		}
		out = append(out, part)
	}
	return out
}

// sourcePos returns the .gsx position of masked offset off, a Go syntax
// boundary of the field: inside a GoText run it maps byte for byte; at a
// construct it is the construct's start, or its end when atEnd. It returns
// NoPos when the part covering off has no position.
func (m maskedField) sourcePos(off int, atEnd bool) token.Pos {
	for i, part := range m.parts {
		start := m.starts[i]
		end := len(m.text)
		if i+1 < len(m.parts) {
			end = m.starts[i+1]
		}
		if off < start || off > end || (atEnd && off == start && start != end) || (!atEnd && off == end && start != end) {
			continue
		}
		if !part.Pos().IsValid() {
			return token.NoPos
		}
		if _, ok := part.(ast.GoText); ok {
			return part.Pos() + token.Pos(off-start)
		}
		if atEnd {
			return part.End()
		}
		return part.Pos()
	}
	return token.NoPos
}

// analyzeField parses parts (the split overlay of a field in grammar position
// syntax) with their constructs masked and returns its shape.
func analyzeField(parts []ast.GoPart, syntax fieldSyntax) (fieldShape, error) {
	m := maskParts(parts)
	shape := fieldShape{initEnd: -1, masked: m}
	var prefix, suffix string
	switch syntax {
	case syntaxIfHeader:
		prefix, suffix = "if ", " {}"
	case syntaxSwitchHeader:
		prefix, suffix = "switch ", " {}"
	case syntaxCaseList:
		prefix, suffix = "switch {\ncase ", ":\n}"
	case syntaxForClause:
		prefix, suffix = "for ", " {}"
	}
	fset := token.NewFileSet()
	var root goast.Node
	base := 0
	if syntax == syntaxExpr {
		expr, err := goparser.ParseExprFrom(fset, "", m.text, 0)
		if err != nil {
			return shape, err
		}
		root = expr
		base = fset.File(expr.Pos()).Base()
	} else {
		const head = "package p\nfunc _() {\n"
		file, err := goparser.ParseFile(fset, "", head+prefix+m.text+suffix+"\n}\n", 0)
		if err != nil {
			return shape, err
		}
		body := file.Decls[0].(*goast.FuncDecl).Body
		if len(body.List) != 1 {
			return shape, fmt.Errorf("header parses as %d statements", len(body.List))
		}
		root = body.List[0]
		base = fset.File(file.Pos()).Base() + len(head) + len(prefix)
		var init, rest goast.Node
		switch s := root.(type) {
		case *goast.IfStmt:
			if s.Init != nil {
				init, rest = s.Init, s.Cond
			}
		case *goast.SwitchStmt:
			if s.Init != nil {
				init, rest = s.Init, s.Tag // Tag is nil in a tagless switch
			}
		case *goast.TypeSwitchStmt:
			if s.Init != nil {
				init, rest = s.Init, s.Assign
			}
		}
		if init != nil {
			shape.initEnd = int(init.End()) - base
			// A tagless switch with an init statement (`switch x := f(); {`)
			// has an empty tag: everything after the init is separator.
			shape.bodyStart = len(m.text)
			if rest != nil {
				shape.bodyStart = int(rest.Pos()) - base
			}
		}
	}

	shape.root = root
	constructAt := map[int]ast.GoPart{}
	for i, part := range parts {
		if _, ok := part.(ast.GoText); !ok {
			constructAt[m.starts[i]] = part
		}
	}
	shape.constructs = map[goast.Node]ast.GoPart{}
	shape.constructDepth = map[ast.GoPart]int{}
	var stack []goast.Node
	funcLits := 0 // func literals on stack: operands inside one are not evaluated with the field
	addEval := func(n goast.Node, kind evalKind) {
		start := m.sourcePos(int(n.Pos())-base, false)
		end := m.sourcePos(int(n.End())-base, true)
		if start.IsValid() && end.IsValid() {
			shape.evals = append(shape.evals, evalNode{start: start, end: end, kind: kind, node: n})
		}
	}
	goast.Inspect(root, func(n goast.Node) bool {
		if n == nil {
			if _, ok := stack[len(stack)-1].(*goast.FuncLit); ok {
				funcLits--
			}
			stack = stack[:len(stack)-1]
			return true
		}
		var construct ast.GoPart
		switch n := n.(type) {
		case *goast.BasicLit:
			construct = constructAt[int(n.Pos())-base]
		case *goast.Ident:
			construct = constructAt[int(n.Pos())-base]
		}
		if construct != nil {
			shape.constructs[n] = construct
			shape.constructDepth[construct] = len(stack)
		}
		inFuncLit := funcLits > 0
		switch x := n.(type) {
		case *goast.BasicLit:
			if lit, ok := construct.(*ast.EmbeddedInterp); ok {
				if remedy := conditionalRemedy(stack, n); remedy != "" {
					if shape.conditional == nil {
						shape.conditional = map[ast.GoPart]string{}
					}
					shape.conditional[lit] = remedy
				}
				if !inFuncLit && literalHasHole(lit) {
					addEval(n, evalLiteral)
				}
			}
		case *goast.CallExpr:
			if !inFuncLit {
				addEval(n, evalCall)
			}
		case *goast.UnaryExpr:
			if !inFuncLit && x.Op == token.ARROW {
				addEval(n, evalRecv)
			}
		case *goast.BinaryExpr:
			if !inFuncLit && (x.Op == token.LAND || x.Op == token.LOR) {
				addEval(n, evalLogical)
			}
		}
		if _, ok := n.(*goast.FuncLit); ok {
			funcLits++
		}
		stack = append(stack, n)
		return true
	})
	return shape, nil
}

// literalHasHole reports whether a nested literal has a @{ } hole, the only
// part of it that evaluates anything.
func literalHasHole(lit *ast.EmbeddedInterp) bool {
	return slices.ContainsFunc(lit.Segments, func(seg ast.Markup) bool { _, ok := seg.(*ast.Interp); return ok })
}

// conditionalRemedy reports whether node n, reached through ancestors stack,
// is evaluated conditionally, as the remedy for rejecting its error holes.
func conditionalRemedy(stack []goast.Node, n goast.Node) string {
	rhs := false
	for i, anc := range stack {
		child := n
		if i+1 < len(stack) {
			child = stack[i+1]
		}
		switch a := anc.(type) {
		case *goast.FuncLit:
			return funcLitErrRemedy
		case *goast.BinaryExpr:
			if (a.Op == token.LAND || a.Op == token.LOR) && child == a.Y {
				rhs = true
			}
		}
	}
	if rhs {
		return logicalRHSErrRemedy
	}
	return ""
}
