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
	ec         interpEmitCtx // element/fragment emission; zero → elements rejected
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
func lowerGoParts(hoistBuf *bytes.Buffer, parts []ast.GoPart, lc lowerCtx) (string, bool) {
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
			if !emitGoExprEmbeddedInterp(hoistBuf, &eb, p, lc.resolved, lc.table, lc.imports, lc.rt, lc.interpTemp, lc.bag, lc.hasCtx, lc.noErrChannel, lc.errReturn) {
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
