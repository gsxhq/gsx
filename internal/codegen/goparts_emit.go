package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// lowerCtx is what lowering a nested construct needs at one position.
type lowerCtx struct {
	resolved   map[ast.Node]types.Type
	table      funcTables
	imports    map[string]bool
	rt         rtImports
	interpTemp *int
	fset       *token.FileSet
	bag        *diag.Bag
	ec         interpEmitCtx // element/fragment emission; used only when elements
	// elements reports whether ec carries a component emit environment. When
	// false (the zero value) an element/fragment part is rejected with the
	// positioned unsupported-node diagnostic below; ec is then ignored.
	elements bool
	hasCtx   bool
	// noErrChannel is empty where an error-carrying hole can hoist its
	// `if err != nil { <errReturn> }` into a statement before the consuming
	// one. Elsewhere it is the remedy the goexpr-literal-error diagnostic
	// gives for such a hole (goExprLiteralErrorRemedy, caseListErrRemedy,
	// forClauseErrRemedy), and js`/css` holes lower inline with no temps.
	noErrChannel string
	errReturn    string // "return _gsxerr" or "return nil, _gsxerr"
	// owner is the node whose Go-expression field is being lowered; it
	// positions the diagnostics that concern the field as a whole (a rejected
	// element part, an unknown part kind).
	owner ast.Node
}

// lowerGoParts returns parts as one Go expression; hoists go to hoistBuf.
//
// GoText runs are spliced verbatim; each prefixed literal lowers to its Go
// value via emitGoExprEmbeddedInterp (string / RawJS / RawCSS) under the
// position's hasCtx / noErrChannel / errReturn; each element or fragment lowers to
// its gsx.Node closure via emitElementValue / emitFragmentValue when
// lc.elements is set. Any statement a hole needs (tuple unwrap, error return,
// temp) is written to hoistBuf before this returns, so it precedes the
// statement consuming the returned expression. The result is untrimmed.
//
// Where the position hoists, a literal evaluated conditionally within the
// field (fieldShape: right of && / ||, inside a func literal) gets no error
// channel: its error-carrying holes are rejected instead of hoisted, and a
// hoisting literal first pins the operands Go evaluates before it
// (fieldPins).
func lowerGoParts(hoistBuf *bytes.Buffer, parts []ast.GoPart, lc lowerCtx) (string, bool) {
	var shape *fieldShape
	if lc.noErrChannel == "" && hasEmbeddedLiteral(parts) {
		s, ok := lc.analyze(parts)
		if !ok {
			return "", false
		}
		shape = &s
	}
	return lowerShapedGoParts(hoistBuf, parts, lc, shape)
}

// hasEmbeddedLiteral reports whether parts holds a prefixed literal.
func hasEmbeddedLiteral(parts []ast.GoPart) bool {
	for _, part := range parts {
		if _, ok := part.(*ast.EmbeddedInterp); ok {
			return true
		}
	}
	return false
}

// analyze parses parts, the overlay of lc.owner's field, into its shape. A
// parse failure is an internal error: the same text type-checked in the
// skeleton, with each construct as an operand of the same grammar.
func (lc lowerCtx) analyze(parts []ast.GoPart) (fieldShape, bool) {
	shape, err := analyzeField(parts, fieldSyntaxOf(lc.owner))
	if err != nil {
		lc.bag.Errorf(lc.owner.Pos(), lc.owner.End(), "unsupported-node", "codegen: cannot parse the Go around a nested literal: %v", err)
		return shape, false
	}
	return shape, true
}

// lowerShapedGoParts is lowerGoParts with the shape of the enclosing field
// already computed: nil where the position cannot hoist (no literal can then
// write a statement). parts may be a slice of the field (an if/switch header's
// init statement or condition); only that slice's operands are pinned.
func lowerShapedGoParts(hoistBuf *bytes.Buffer, parts []ast.GoPart, lc lowerCtx, shape *fieldShape) (string, bool) {
	var conditional map[ast.GoPart]string
	pins := fieldPins{lc: lc, hoistBuf: hoistBuf}
	if shape != nil {
		conditional = shape.conditional
		pins.evals = shape.evals
	}
	for _, part := range parts {
		switch p := part.(type) {
		case ast.GoText:
			pins.add(p, p.Src, true)
		case *ast.Element:
			if !lc.elements {
				lc.rejectElement()
				return "", false
			}
			ec := lc.ec
			var eb bytes.Buffer
			if !emitElementValue(&eb, p, ec.currentPkg, lc.resolved, lc.table, lc.imports, lc.rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, lc.interpTemp, lc.fset, ec.cls, lc.bag, ec.mergeExpr, ec.enclosingAttrsBound, ec.positionalPlan) {
				return "", false
			}
			pins.add(p, eb.String(), false)
		case *ast.Fragment:
			if !lc.elements {
				lc.rejectElement()
				return "", false
			}
			ec := lc.ec
			var eb bytes.Buffer
			if !emitFragmentValue(&eb, p, ec.currentPkg, lc.resolved, lc.table, lc.imports, lc.rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, lc.interpTemp, lc.fset, ec.cls, lc.bag, ec.mergeExpr, ec.enclosingAttrsBound, ec.positionalPlan) {
				return "", false
			}
			pins.add(p, eb.String(), false)
		case *ast.EmbeddedInterp:
			if len(p.Stages) > 0 {
				lc.bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
				return "", false
			}
			noErrChannel := lc.noErrChannel
			if remedy, ok := conditional[p]; ok {
				noErrChannel = remedy
			}
			var hoists, eb bytes.Buffer
			if !emitGoExprEmbeddedInterp(&hoists, &eb, p, lc.resolved, lc.table, lc.imports, lc.rt, lc.interpTemp, lc.bag, lc.hasCtx, noErrChannel, lc.errReturn) {
				return "", false
			}
			if hoists.Len() > 0 {
				if shape == nil {
					lc.bag.Errorf(p.Pos(), p.End(), "unsupported-node", "codegen: internal: a nested literal hoisted a statement at a position without a field analysis")
					return "", false
				}
				if !pins.before(p) {
					return "", false
				}
				hoistBuf.Write(hoists.Bytes())
			}
			pins.add(p, eb.String(), false)
		default:
			lc.bag.Errorf(lc.owner.Pos(), lc.owner.End(), "unsupported-node", "unsupported embedded interpolation part %T", part)
			return "", false
		}
	}
	return pins.String(), true
}

func (lc lowerCtx) rejectElement() {
	lc.bag.Errorf(lc.owner.Pos(), lc.owner.End(), "unsupported-node", "element literals are not supported inside this interpolation position; bind the element to a variable in a {{ }} block or use a { } child position")
}

// attrLowerCtx is the lowerCtx for the Go-expression fields of an element's or
// component tag's attributes, and of control-flow headers, inside a component
// body: element parts lower against ec, ctx is in scope, and error-carrying
// holes hoist before the attribute's own write (or the header's statement). A site inside an AttrsCond branch thunk overrides
// errReturn with the thunk's "return nil, _gsxerr".
func attrLowerCtx(resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, fset *token.FileSet, bag *diag.Bag, ec interpEmitCtx) lowerCtx {
	return lowerCtx{resolved: resolved, table: table, imports: imports, rt: rt, interpTemp: interpTemp, fset: fset, bag: bag, ec: ec, elements: true, hasCtx: true, errReturn: "return _gsxerr"}
}

// field returns one Go-expression field's value text, trimmed: its split
// overlay lowered through lowerGoParts (hoists go to hoistBuf) when the
// analysis split found a nested construct, else src verbatim. owner positions
// the field-level diagnostics.
func (lc lowerCtx) field(hoistBuf *bytes.Buffer, src string, embedded []ast.GoPart, owner ast.Node) (string, bool) {
	if embedded == nil {
		return strings.TrimSpace(src), true
	}
	lc.owner = owner
	expr, ok := lowerGoParts(hoistBuf, embedded, lc)
	return strings.TrimSpace(expr), ok
}

// header lowers an if condition or a switch tag (with its optional init
// statement) owned by owner; hoists precede the statement. When the header has
// an init statement and anything hoists, the init must run before the
// condition's hoists (which may use its variables): header then opens a block,
// `{\n<init hoists><init>\n<cond hoists>`, and returns block=true; the caller
// emits its if/switch with the returned text and closes the block
// (closeHeaderBlock). The Go spec's implicit block of an if/switch makes this
// equivalent. Without hoists the output is exactly lc.field's.
func (lc lowerCtx) header(b *bytes.Buffer, src string, embedded []ast.GoPart, owner ast.Node) (text string, block, ok bool) {
	var pre bytes.Buffer
	text, block, ok = lc.headerInBlock(&pre, src, embedded, owner)
	if block {
		b.WriteString("{\n")
	}
	b.Write(pre.Bytes())
	return text, block, ok
}

// headerInBlock is header for a caller that already emits the statement as
// the first one of its own fresh block (an else-if written as `else { … }`):
// it writes the init statement and hoists to b without opening a block, and
// reports whether it wrote the init statement there.
func (lc lowerCtx) headerInBlock(b *bytes.Buffer, src string, embedded []ast.GoPart, owner ast.Node) (text string, initHoisted, ok bool) {
	if embedded == nil || lc.noErrChannel != "" {
		text, ok = lc.field(b, src, embedded, owner)
		return text, false, ok
	}
	lc.owner = owner
	shape, ok := lc.analyze(embedded)
	if !ok {
		return "", false, false
	}
	if shape.initEnd < 0 {
		expr, ok := lowerShapedGoParts(b, embedded, lc, &shape)
		return strings.TrimSpace(expr), false, ok
	}
	m := shape.masked
	var initHoists, condHoists bytes.Buffer
	init, ok := lowerShapedGoParts(&initHoists, m.split(0, shape.initEnd), lc, &shape)
	if !ok {
		return "", false, false
	}
	// Between the init statement and the condition there is only `;` and
	// layout: no construct, so nothing is written to initHoists.
	sep, ok := lowerShapedGoParts(&initHoists, m.split(shape.initEnd, shape.bodyStart), lc, &shape)
	if !ok {
		return "", false, false
	}
	cond, ok := lowerShapedGoParts(&condHoists, m.split(shape.bodyStart, len(m.text)), lc, &shape)
	if !ok {
		return "", false, false
	}
	if initHoists.Len() == 0 && condHoists.Len() == 0 {
		return strings.TrimSpace(init + sep + cond), false, true
	}
	b.Write(initHoists.Bytes())
	b.WriteString(strings.TrimSpace(init))
	b.WriteString("\n")
	b.Write(condHoists.Bytes())
	return strings.TrimSpace(cond), true, true
}

// condHeaderHasInit reports whether the if header of t carries an init
// statement, parsing it (its split overlay, constructs masked) as Go. A header
// that does not parse is an internal error — the same text type-checked in the
// skeleton — reported at t (ok=false).
func (lc lowerCtx) condHeaderHasInit(t *ast.CondAttr) (hasInit, ok bool) {
	parts := t.CondEmbedded
	if parts == nil {
		parts = []ast.GoPart{ast.GoText{Src: t.Cond}}
	}
	shape, err := analyzeField(parts, syntaxIfHeader)
	if err != nil {
		lc.bag.Errorf(t.Pos(), t.End(), "unsupported-node", "codegen: cannot parse the conditional attribute header: %v", err)
		return false, false
	}
	return shape.initEnd >= 0, true
}

// switchHeaderBinds reports whether t's switch header declares names its arms
// may read: an init statement, or a type switch's bound variable.
func (lc lowerCtx) switchHeaderBinds(t *ast.SwitchAttr) (binds, ok bool) {
	parts := t.TagEmbedded
	if parts == nil {
		parts = []ast.GoPart{ast.GoText{Src: t.Tag}}
	}
	shape, err := analyzeField(parts, syntaxSwitchHeader)
	if err != nil {
		lc.bag.Errorf(t.Pos(), t.End(), "unsupported-node", "codegen: cannot parse the switch attribute header: %v", err)
		return false, false
	}
	if shape.initEnd >= 0 {
		return true, true
	}
	ts, isType := shape.root.(*goast.TypeSwitchStmt)
	if !isType {
		return false, true
	}
	_, assigns := ts.Assign.(*goast.AssignStmt)
	return assigns, true
}

// attrsCondHeader is a conditional attrs contributor's lowered if header, for
// the (Attrs, error) expression selecting between its branch thunks.
type attrsCondHeader struct {
	rtPkg string
	text  string
	// init is non-nil exactly when the header has an init statement. It then
	// holds the init (preceded by its hoists and followed by the condition's)
	// when anything hoists; otherwise it is empty and text is the whole
	// `init; cond` header.
	init *bytes.Buffer
}

// lowerAttrsCondHeader lowers t's if header. Without an init statement the
// condition lowers in place, its hoists going to b, and expr yields
// AttrsCond(cond, then, else). With one, AttrsCond — an expression — cannot
// hold the statement: the header lowers into the body of the func literal expr
// yields instead (hoists there return through its (Attrs, error) result).
func (lc lowerCtx) lowerAttrsCondHeader(b *bytes.Buffer, t *ast.CondAttr, rtPkg string) (attrsCondHeader, bool) {
	hasInit, ok := lc.condHeaderHasInit(t)
	if !ok {
		return attrsCondHeader{}, false
	}
	h := attrsCondHeader{rtPkg: rtPkg}
	if !hasInit {
		h.text, ok = lc.field(b, t.Cond, t.CondEmbedded, t)
		return h, ok
	}
	h.init = &bytes.Buffer{}
	lc.errReturn = "return nil, _gsxerr"
	h.text, _, ok = lc.headerInBlock(h.init, t.Cond, t.CondEmbedded, t)
	return h, ok
}

// attrsBranchCode is one lowered branch of a conditional attrs contributor.
type attrsBranchCode struct {
	thunk  string // `func() (Attrs, error) { <body>; return <bag>, nil }`
	inline string // the thunk's body statements through its return
}

// expr returns the (Attrs, error) expression evaluating the taken branch; els
// is the zero attrsBranchCode when there is no else branch. Without an init
// statement it is AttrsCond(cond, then, else) over the branch thunks. With
// one it is an immediately invoked func literal running the header as a real
// Go if with each branch's statements inline, so the init runs once, before
// the condition, its variables are in scope in the condition and both
// branches, and only the taken branch is evaluated — AttrsCond's laziness.
func (h attrsCondHeader) expr(then, els attrsBranchCode) string {
	if h.init == nil {
		elsThunk := "nil"
		if els.thunk != "" {
			elsThunk = els.thunk
		}
		return fmt.Sprintf("%s.AttrsCond(%s, %s, %s)", h.rtPkg, h.text, then.thunk, elsThunk)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "func() (%s.Attrs, error) {\n", h.rtPkg)
	b.Write(h.init.Bytes())
	fmt.Fprintf(&b, "if %s {\n%s}", h.text, then.inline)
	if els.thunk == "" {
		b.WriteString("\nreturn nil, nil\n}()")
	} else {
		fmt.Fprintf(&b, " else {\n%s}\n}()", els.inline)
	}
	return b.String()
}
