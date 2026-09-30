package codegen

import (
	"bytes"
	"fmt"
	"go/types"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// fieldPins assembles one field's lowered parts in source order and keeps
// Go's evaluation order when a nested literal hoists statements ahead of the
// statement consuming the field.
//
// The Go spec evaluates the calls, method calls, receives and logical
// operations of an expression in lexical left-to-right order. A hoisted hole
// (`v, err := f(); if err != nil { … }`) runs before the whole expression, so
// before its hoist every such operand lexically before the literal is pinned
// to a `_gsxvN` temp, in source order, and the temp replaces it in the
// expression — the field-level form of literalConcat's rule inside one
// literal. Only the outermost operands are pinned (a call nested in a pinned
// call is evaluated as part of it). Conversions and constants evaluate
// nothing and stay in place; operands inside a func literal are not evaluated
// there and stay too. Without a hoist the parts are concatenated unchanged.
type fieldPins struct {
	lc       lowerCtx
	hoistBuf *bytes.Buffer
	evals    []evalNode
	pieces   []loweredPiece
	pinned   []pinnedSpan // disjoint, in source order
}

// loweredPiece is one part's lowered text and source span. A verbatim piece
// (a GoText run) maps byte for byte onto its span.
type loweredPiece struct {
	start, end int
	text       string
	verbatim   bool
}

// pinnedSpan is an operand's source span replaced by ref, its temp.
type pinnedSpan struct {
	start, end int
	ref        string
}

func (f *fieldPins) add(part ast.GoPart, text string, verbatim bool) {
	f.pieces = append(f.pieces, loweredPiece{start: int(part.Pos()), end: int(part.End()), text: text, verbatim: verbatim})
}

// before pins every operand evaluated before lit, which is about to hoist.
func (f *fieldPins) before(lit ast.GoPart) bool {
	if len(f.pieces) == 0 {
		return true
	}
	lo, hi := f.pieces[0].start, int(lit.Pos())
	if lo <= 0 || hi <= 0 {
		f.lc.bag.Errorf(lit.Pos(), lit.End(), "unsupported-node", "codegen: internal: a nested literal's field has no source positions to order its operands by")
		return false
	}
	var span, inert []evalNode
	for _, e := range f.evals {
		if int(e.start) < lo || int(e.end) > hi {
			continue
		}
		span = append(span, e)
		if e.kind == evalCall || e.kind == evalLogical {
			fact, ok := f.fact(e, lit)
			if !ok {
				return false
			}
			if fact.constant {
				// Nothing inside a constant is evaluated (unsafe.Sizeof's
				// operand, len of an array): it and its operands stay put.
				inert = append(inert, e)
			}
		}
	}
	var cands []evalNode
	for _, e := range span {
		if enclosed(inert, e, true) || f.covered(e) {
			continue
		}
		eff, ok := f.effective(e, span, inert, lit)
		if !ok {
			return false
		}
		if eff {
			cands = append(cands, e)
		}
	}
	for _, c := range cands {
		if enclosed(cands, c, false) {
			continue
		}
		if !f.pin(c, lit) {
			return false
		}
	}
	return true
}

// enclosed reports whether one of evals encloses e (or is e, when self).
func enclosed(evals []evalNode, e evalNode, self bool) bool {
	for _, d := range evals {
		if (self || d != e) && d.start <= e.start && e.end <= d.end {
			return true
		}
	}
	return false
}

// covered reports whether e is (inside) an operand already pinned.
func (f *fieldPins) covered(e evalNode) bool {
	for _, p := range f.pinned {
		if p.start <= int(e.start) && int(e.end) <= p.end {
			return true
		}
	}
	return false
}

// fact returns e's type facts, harvested from the type-checked skeleton.
func (f *fieldPins) fact(e evalNode, lit ast.GoPart) (evalFact, bool) {
	fact, ok := f.lc.table.evalFacts[evalKey{e.start, e.end}]
	if !ok {
		f.lc.bag.Errorf(lit.Pos(), lit.End(), "unsupported-node", "codegen: internal: no type facts for the operand at %s, which must run before this literal's hoisted hole", f.lc.fset.Position(e.start))
		return fact, false
	}
	return fact, true
}

// effective reports whether pinning e keeps an evaluation in order: a call
// that is not a conversion, a receive, a literal with a hole, or a logical
// operation enclosing one of those. span is the operands before the hoisting
// literal; e is neither constant nor inside a constant (inert).
func (f *fieldPins) effective(e evalNode, span, inert []evalNode, lit ast.GoPart) (bool, bool) {
	switch e.kind {
	case evalRecv, evalLiteral:
		return true, true
	case evalCall:
		fact, ok := f.fact(e, lit)
		return ok && !fact.conversion, ok
	}
	for _, inner := range span {
		if inner.kind == evalLogical || inner.start < e.start || inner.end > e.end || enclosed(inert, inner, true) {
			continue
		}
		eff, ok := f.effective(inner, span, inert, lit)
		if !ok || eff {
			return eff, ok
		}
	}
	return false, true
}

// pin writes `_gsxvN := <e>` to the hoist buffer and substitutes the temp
// for e.
func (f *fieldPins) pin(e evalNode, lit ast.GoPart) bool {
	var fact evalFact
	if e.kind == evalCall || e.kind == evalLogical {
		var ok bool
		if fact, ok = f.fact(e, lit); !ok {
			return false
		}
	}
	if _, tuple := fact.typ.(*types.Tuple); tuple {
		// Go admits a multi-value call only as the sole argument of another
		// call, which then encloses it: never an outermost operand.
		f.lc.bag.Errorf(lit.Pos(), lit.End(), "unsupported-node", "codegen: internal: the multi-value operand at %s cannot be pinned before this literal's hoisted hole", f.lc.fset.Position(e.start))
		return false
	}
	name := fmt.Sprintf("_gsxv%d", *f.lc.interpTemp)
	*f.lc.interpTemp++
	fmt.Fprintf(f.hoistBuf, "\t\t%s := %s\n", name, f.render(int(e.start), int(e.end)))
	ref := name
	if e.kind == evalLogical {
		if fact.untypedBool && !isBoolType(fact.typ) {
			// The operation's value is an untyped boolean the context
			// converts to another boolean type; the temp is a typed bool. A
			// comparison yields an untyped boolean again.
			ref = "(" + name + " == true)"
		}
	}
	kept := f.pinned[:0]
	for _, p := range f.pinned {
		if p.start < int(e.start) || p.end > int(e.end) {
			kept = append(kept, p)
		}
	}
	f.pinned = kept
	at := len(f.pinned)
	for i, p := range f.pinned {
		if p.start > int(e.start) {
			at = i
			break
		}
	}
	f.pinned = append(f.pinned[:at], append([]pinnedSpan{{start: int(e.start), end: int(e.end), ref: ref}}, f.pinned[at:]...)...)
	return true
}

// isBoolType reports whether t is the predeclared bool (or untyped bool).
func isBoolType(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && (b.Kind() == types.Bool || b.Kind() == types.UntypedBool)
}

// render returns the lowered text of source span [start, end), pinned
// operands replaced by their temps.
func (f *fieldPins) render(start, end int) string {
	var sb strings.Builder
	cur := start
	for _, p := range f.pinned {
		if p.start < start || p.end > end {
			continue
		}
		f.raw(&sb, cur, p.start)
		sb.WriteString(p.ref)
		cur = p.end
	}
	f.raw(&sb, cur, end)
	return sb.String()
}

// raw writes the lowered text of source span [start, end), which starts and
// ends at Go syntax boundaries, never inside a construct.
func (f *fieldPins) raw(sb *strings.Builder, start, end int) {
	for _, p := range f.pieces {
		if p.verbatim {
			lo, hi := max(start, p.start), min(end, p.end)
			if lo < hi {
				sb.WriteString(p.text[lo-p.start : hi-p.start])
			}
			continue
		}
		if start <= p.start && p.end <= end {
			sb.WriteString(p.text)
		}
	}
}

// String returns the field's expression.
func (f *fieldPins) String() string {
	if len(f.pinned) == 0 {
		var sb strings.Builder
		for _, p := range f.pieces {
			sb.WriteString(p.text)
		}
		return sb.String()
	}
	return f.render(f.pieces[0].start, f.pieces[len(f.pieces)-1].end)
}
