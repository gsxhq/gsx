package codegen

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// Nested gsx literals (f`/js`/css`) and element literals inside a Go
// expression are lowered only where materializeEmbeddedMarkup splits the
// expression: body interpolations and {{ }} blocks. Every other Go-expression
// position carries its text verbatim into the skeleton, where go/parser would
// reject it with an unpositioned "missing ','" cascade. The gates below report
// one positioned nested-literal diagnostic per such construct instead.

// gateNestedLiteralAttrs checks every Go-expression position in an element's
// attributes. It reports whether all of them are free of nested constructs.
func gateNestedLiteralAttrs(attrs []gsxast.Attr, bag *diag.Bag) bool {
	ok := true
	var anchor gsxast.Node
	check := func(src string, pos token.Pos, where string) {
		if !gateNestedLiteral(src, pos, anchor, where, false, bag) {
			ok = false
		}
	}
	for _, a := range attrs {
		anchor = a
		switch t := a.(type) {
		case *gsxast.ExprAttr:
			// A braced literal that is the whole value (`title={f`…`}`,
			// `h={js`…`}`) has its own lowering.
			if !gateNestedLiteral(t.Expr, t.ExprPos, t, "an attribute value", true, bag) {
				ok = false
			}
		case *gsxast.SpreadAttr:
			check(t.Expr, t.ExprPos, "a spread")
		case *gsxast.ComposedAttr:
			for i := range t.Parts {
				part := &t.Parts[i]
				where := "a " + t.Name + " part"
				check(part.Expr, part.ExprPos, where)
				check(part.Cond, part.CondPos, "a "+t.Name+" condition")
				if part.CF != nil {
					gateValueCF(part.CF, where, check)
				}
			}
		case *gsxast.CondAttr:
			check(t.Cond, t.CondPos, "a conditional-attribute condition")
			thenOK, elseOK := gateNestedLiteralAttrs(t.Then, bag), gateNestedLiteralAttrs(t.Else, bag)
			ok = ok && thenOK && elseOK
		case *gsxast.SwitchAttr:
			check(t.Tag, t.TagPos, "a switch header")
			for _, cc := range t.Cases {
				check(cc.List, cc.ListPos, "a case list")
				if !gateNestedLiteralAttrs(cc.Body, bag) {
					ok = false
				}
			}
		case *gsxast.OrderedAttrsAttr:
			for i := range t.Pairs {
				// A pair carries no value offset: NoPos anchors the report at
				// the pair itself.
				anchor = &t.Pairs[i]
				check(t.Pairs[i].Value, token.NoPos, "an attrs literal value")
			}
		}
	}
	return ok
}

func gateValueCF(cf *gsxast.ValueCF, where string, check func(string, token.Pos, string)) {
	if cf.If != nil {
		for vi := cf.If; vi != nil; vi = vi.ElseIf {
			check(vi.Cond, vi.CondPos, "an if condition")
		}
	} else if cf.Switch != nil {
		check(cf.Switch.Tag, cf.Switch.TagPos, "a switch header")
		for _, c := range cf.Switch.Cases {
			check(c.List, c.ListPos, "a case list")
		}
	}
	for _, arm := range valueFormArms(cf) {
		if arm.Segments == nil {
			check(arm.Expr, arm.ExprPos, where)
		}
	}
}

// gateNestedLiteralHeaders checks the Go header of a control-flow node.
func gateNestedLiteralHeaders(node gsxast.Markup, bag *diag.Bag) bool {
	switch n := node.(type) {
	case *gsxast.IfMarkup:
		return gateNestedLiteral(n.Cond, n.CondPos, n, "an if header", false, bag)
	case *gsxast.ForMarkup:
		return gateNestedLiteral(n.Clause, n.ClausePos, n, "a for clause", false, bag)
	case *gsxast.SwitchMarkup:
		ok := gateNestedLiteral(n.Tag, n.TagPos, n, "a switch header", false, bag)
		for _, cc := range n.Cases {
			if !gateNestedLiteral(cc.List, cc.ListPos, cc, "a case list", false, bag) {
				ok = false
			}
		}
		return ok
	}
	return true
}

// gateNestedLiteral reports each gsx construct nested in the Go expression src
// (whose first byte is at pos) and returns whether there were none. When pos
// is NoPos — src is not a verbatim source slice — reports anchor at the owning
// node. When allowWhole is set, a single literal spanning all of src is not
// nested.
func gateNestedLiteral(src string, pos token.Pos, anchor gsxast.Node, where string, allowWhole bool, bag *diag.Bag) bool {
	if src == "" {
		return true
	}
	cs := gsxparser.EmbeddedConstructs(src)
	if len(cs) == 0 {
		return true
	}
	if allowWhole && len(cs) == 1 && !cs[0].IsElement && cs[0].Off == 0 && cs[0].End == len(strings.TrimRight(src, " \t\r\n")) {
		return true
	}
	for _, c := range cs {
		start, end := anchor.Pos(), anchor.End()
		if pos.IsValid() {
			start, end = pos+token.Pos(c.Off), pos+token.Pos(c.End)
		}
		if c.IsElement {
			bag.Errorf(start, end, "nested-literal",
				"an element literal inside a Go expression is not supported in %s yet; build it in a Go function and call that", where)
			continue
		}
		bag.Errorf(start, end, "nested-literal",
			"%s literal inside a Go expression is not supported in %s yet; assign it to a variable in a {{ }} block first", literalArticle(src[c.Off:]), where)
	}
	return false
}

// literalArticle names the literal starting s with its article: "an f", "a
// js", "a css".
func literalArticle(s string) string {
	for _, p := range [...]string{"js", "css"} {
		if strings.HasPrefix(s, p) {
			return "a " + p
		}
	}
	return "an f"
}
