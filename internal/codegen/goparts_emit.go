package codegen

import (
	"bytes"
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
// channel: its error-carrying holes are rejected instead of hoisted.
func lowerGoParts(hoistBuf *bytes.Buffer, parts []ast.GoPart, lc lowerCtx) (string, bool) {
	var conditional map[ast.GoPart]string
	if lc.noErrChannel == "" && hasEmbeddedLiteral(parts) {
		shape, ok := lc.analyze(parts)
		if !ok {
			return "", false
		}
		conditional = shape.conditional
	}
	return lowerShapedGoParts(hoistBuf, parts, lc, conditional)
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

// lowerShapedGoParts is lowerGoParts with the conditional-literal remedies of
// the enclosing field already computed.
func lowerShapedGoParts(hoistBuf *bytes.Buffer, parts []ast.GoPart, lc lowerCtx, conditional map[ast.GoPart]string) (string, bool) {
	var eb bytes.Buffer
	for _, part := range parts {
		switch p := part.(type) {
		case ast.GoText:
			eb.WriteString(p.Src)
		case *ast.Element:
			if !lc.elements {
				lc.rejectElement()
				return "", false
			}
			ec := lc.ec
			if !emitElementValue(&eb, p, ec.currentPkg, lc.resolved, lc.table, lc.imports, lc.rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, lc.interpTemp, lc.fset, ec.cls, lc.bag, ec.mergeExpr, ec.enclosingAttrsBound, ec.positionalPlan) {
				return "", false
			}
		case *ast.Fragment:
			if !lc.elements {
				lc.rejectElement()
				return "", false
			}
			ec := lc.ec
			if !emitFragmentValue(&eb, p, ec.currentPkg, lc.resolved, lc.table, lc.imports, lc.rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, lc.interpTemp, lc.fset, ec.cls, lc.bag, ec.mergeExpr, ec.enclosingAttrsBound, ec.positionalPlan) {
				return "", false
			}
		case *ast.EmbeddedInterp:
			if len(p.Stages) > 0 {
				lc.bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
				return "", false
			}
			noErrChannel := lc.noErrChannel
			if remedy, ok := conditional[p]; ok {
				noErrChannel = remedy
			}
			if !emitGoExprEmbeddedInterp(hoistBuf, &eb, p, lc.resolved, lc.table, lc.imports, lc.rt, lc.interpTemp, lc.bag, lc.hasCtx, noErrChannel, lc.errReturn) {
				return "", false
			}
		default:
			lc.bag.Errorf(lc.owner.Pos(), lc.owner.End(), "unsupported-node", "unsupported embedded interpolation part %T", part)
			return "", false
		}
	}
	return eb.String(), true
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
		expr, ok := lowerShapedGoParts(b, embedded, lc, shape.conditional)
		return strings.TrimSpace(expr), false, ok
	}
	m := shape.masked
	var initHoists, condHoists bytes.Buffer
	init, ok := lowerShapedGoParts(&initHoists, m.split(0, shape.initEnd), lc, shape.conditional)
	if !ok {
		return "", false, false
	}
	// Between the init statement and the condition there is only `;` and
	// layout: no construct, so nothing is written to initHoists.
	sep, ok := lowerShapedGoParts(&initHoists, m.split(shape.initEnd, shape.bodyStart), lc, nil)
	if !ok {
		return "", false, false
	}
	cond, ok := lowerShapedGoParts(&condHoists, m.split(shape.bodyStart, len(m.text)), lc, shape.conditional)
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
