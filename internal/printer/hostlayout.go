package printer

import (
	"go/token"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
)

// hostCursor reads only grammar punctuation and trivia from source geometry.
// Semantic fields and child nodes always render from the current AST.
type hostCursor struct {
	p        *printer
	pos, end token.Pos
	docs     []pretty.Doc
}

func (p *printer) host(n ast.Node) *hostCursor { return &hostCursor{p: p, pos: n.Pos(), end: n.End()} }
func (c *hostCursor) remaining() string {
	s, ok := c.p.source.Slice(c.pos, c.end)
	if !ok {
		c.p.fail("printer: missing GSX host source layout")
		return ""
	}
	return s
}
func (c *hostCursor) gap() {
	s := c.remaining()
	gap := s[:len(s)-len(strings.TrimLeft(s, " \t\r\n\f"))]
	c.docs = append(c.docs, pretty.Text(gap))
	c.pos += token.Pos(len(gap))
}
func (c *hostCursor) token(text string) {
	c.gap()
	if !strings.HasPrefix(c.remaining(), text) {
		c.p.fail("printer: incompatible GSX host source layout at %d, expected %q", c.pos, text)
		return
	}
	c.docs = append(c.docs, pretty.Text(text))
	c.pos += token.Pos(len(text))
}
func (c *hostCursor) child(n ast.Node, doc pretty.Doc) {
	gap, ok := c.p.source.Slice(c.pos, n.Pos())
	if !ok || strings.Trim(gap, " \t\r\n\f") != "" {
		c.p.fail("printer: incompatible GSX child source layout")
		return
	}
	c.docs = append(c.docs, pretty.Text(gap), doc)
	c.pos = n.End()
}

func (c *hostCursor) field(pos token.Pos, doc pretty.Doc) {
	gap, valid := c.p.source.Slice(c.pos, pos)
	end, ok := c.p.source.FieldEnd(pos)
	if !valid || strings.Trim(gap, " \t\r\n\f") != "" || !ok {
		c.p.fail("printer: missing GSX field source origin at %d", pos)
		return
	}
	c.docs = append(c.docs, pretty.Text(gap), doc)
	c.pos = end
}

func (c *hostCursor) goField(src string, pos token.Pos, form goFieldForm, plain func(string) string) {
	if c.p.anchoredGoField(src) {
		c.field(pos, c.p.goFieldDoc(src, form, plain, pos))
	} else {
		c.field(pos, pretty.Text(src))
	}
}
func (c *hostCursor) stages(stages []ast.PipeStage) {
	for _, st := range stages {
		c.token("|>")
		c.field(st.NamePos, pretty.Text(st.Name))
		if st.HasArgs {
			c.token("(")
			c.field(st.ArgsPos, pretty.Text(st.Args))
			c.token(")")
		}
	}
}
func (c *hostCursor) done() pretty.Doc {
	c.gap()
	if c.pos != c.end {
		c.p.fail("printer: unconsumed GSX host source layout at %d", c.pos)
	}
	return pretty.Concat(c.docs...)
}
func (p *printer) anchoredGoField(src string) bool {
	if src == "" || !mayHoldGoValue(src) {
		return false
	}
	if value, ok := p.anchoredFields[src]; ok {
		return value
	}
	if p.anchoredFields == nil {
		p.anchoredFields = make(map[string]bool)
	}
	parts, ok := splitGoParts(src)
	value := false
	if ok {
		for _, part := range parts {
			if p.anchoredGoMarkup(part) {
				value = true
			}
		}
	}
	p.anchoredFields[src] = value
	return value
}
func (p *printer) sourceInterp(i *ast.Interp) pretty.Doc {
	c := p.host(i)
	c.token("{")
	c.goField(i.Expr, i.ExprPos, exprField, fmtExprPreserving)
	c.stages(i.Stages)
	c.token("}")
	return c.done()
}
func (p *printer) sourceAttr(a ast.Attr) (pretty.Doc, bool) {
	c := p.host(a)
	switch v := a.(type) {
	case *ast.CommentAttr:
		return pretty.Text(v.Layout.Leading + v.Text + v.Layout.Trailing), true
	case *ast.BoolAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
	case *ast.StaticAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
		c.token("=")
		c.token("\"")
		c.field(c.pos, pretty.Text(v.Raw))
		c.token("\"")
	case *ast.EmbeddedAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
		c.token("=")
		if v.Braced {
			c.token("{")
		}
		p.sourceEmbedded(c, v.Lang, v.Segments, embeddedDelim(v.DoubleQuoted))
		c.stages(v.Stages)
		if v.Braced {
			c.token("}")
		}

	case *ast.ExprAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
		c.token("=")
		c.token("{")
		c.goField(v.Expr, v.ExprPos, exprField, fmtExprPreserving)
		c.stages(v.Stages)
		c.token("}")
	case *ast.OrderedAttrsAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
		c.token("=")
		c.token("{{")
		for i := range v.Pairs {
			if i > 0 {
				c.token(",")
			}
			pair := &v.Pairs[i]
			c.field(pair.KeyPos, pretty.Text(p.sourceKey(pair)))
			c.token(":")
			c.goField(pair.Value, pair.ValuePos, exprField, fmtExprPreserving)
		}
		if strings.HasPrefix(strings.TrimLeft(c.remaining(), " \t\r\n\f"), ",") {
			c.token(",")
		}
		c.token("}}")
	case *ast.ComposedAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
		c.token("=")
		c.token("{")
		for i := range v.Parts {
			if i > 0 {
				c.token(",")
			}
			part := &v.Parts[i]
			c.child(part, p.sourceComposedPart(part))
		}
		if strings.HasPrefix(strings.TrimLeft(c.remaining(), " \t\r\n\f"), ",") {
			c.token(",")
		}
		c.token("}")
	case *ast.CondAttr:
		p.sourceCondAttr(c, v, true)
	case *ast.SwitchAttr:
		c.token("{")
		c.token("switch")
		if v.Tag != "" {
			c.goField(v.Tag, v.TagPos, switchField, fmtExpr)
		}
		c.token("{")
		for _, cc := range v.Cases {
			if cc.Default {
				c.token("default")
			} else {
				c.token("case")
				c.goField(cc.List, cc.ListPos, caseField, fmtCaseList)
			}
			c.token(":")
			for _, a := range cc.Body {
				c.child(a, p.sourceAttrChild(a))
			}
		}
		c.token("}")
		c.token("}")
	case *ast.MarkupAttr:
		c.field(v.Pos(), pretty.Text(v.Name))
		c.token("=")
		c.token("{")
		p.sourceMarkupBody(c, v.Value, v.Layout)
		c.token("}")
	case *ast.SpreadAttr:
		c.token("{")
		paren := strings.HasPrefix(strings.TrimLeft(c.remaining(), " \t\r\n\f"), "(") && len(v.Stages) > 0
		if paren {
			c.token("(")
		}
		c.goField(v.Expr, v.ExprPos, exprField, fmtExprPreserving)
		c.stages(v.Stages)
		if paren {
			c.token(")")
		}
		c.token("...")
		c.token("}")
	default:
		return pretty.Doc{}, false
	}
	return c.done(), true
}
func (p *printer) sourceCondAttr(c *hostCursor, v *ast.CondAttr, outer bool) {
	if outer {
		c.token("{")
	}
	c.token("if")
	c.goField(v.Cond, v.CondPos, ifField, fmtExpr)
	c.token("{")
	for _, a := range v.Then {
		c.child(a, p.sourceAttrChild(a))
	}
	c.token("}")
	if len(v.Else) > 0 {
		c.token("else")
		if len(v.Else) == 1 {
			if nested, ok := v.Else[0].(*ast.CondAttr); ok {
				p.sourceCondAttr(c, nested, false)
				if outer {
					c.token("}")
				}
				return
			}
		}
		c.token("{")
		for _, a := range v.Else {
			c.child(a, p.sourceAttrChild(a))
		}
		c.token("}")
	}
	if outer {
		c.token("}")
	}
}
func (p *printer) sourceComposedPart(v *ast.ComposedPart) pretty.Doc {
	c := p.host(v)
	if v.CF != nil {
		p.sourceValueCF(c, v.CF)
	} else if v.LiteralSegments != nil {
		c.field(v.LiteralPos, p.sourceLiteral(v.LiteralPos, v.LiteralEnd, v.LiteralLang, v.LiteralSegments, embeddedDelim(v.LiteralDoubleQuoted)))
	} else {
		c.goField(v.Expr, v.ExprPos, exprField, fmtExprPreserving)
		c.stages(v.Stages)
	}
	if v.Cond != "" {
		c.token(":")
		c.goField(v.Cond, v.CondPos, exprField, fmtExpr)
	}
	return c.done()
}
func (p *printer) sourceValueCF(c *hostCursor, v *ast.ValueCF) {
	if v.If != nil {
		p.sourceValueIf(c, v.If)
		return
	}
	s := v.Switch
	c.token("switch")
	if s.Tag != "" {
		c.goField(s.Tag, s.TagPos, switchField, fmtExpr)
	}
	c.token("{")
	for _, cc := range s.Cases {
		if cc.Default {
			c.token("default")
		} else {
			c.token("case")
			c.goField(cc.List, cc.ListPos, caseField, fmtCaseList)
		}
		c.token(":")
		c.child(cc.Value, p.sourceValueArm(cc.Value))
	}
	c.token("}")
}
func (p *printer) sourceValueIf(c *hostCursor, v *ast.ValueIf) {
	c.token("if")
	c.goField(v.Cond, v.CondPos, ifField, fmtExpr)
	c.child(v.Then, p.sourceValueArm(v.Then))
	if v.ElseIf != nil {
		c.token("else")
		p.sourceValueIf(c, v.ElseIf)
	} else if v.Else != nil {
		c.token("else")
		c.child(v.Else, p.sourceValueArm(v.Else))
	}
}
func (p *printer) sourceValueArm(v *ast.ValueArm) pretty.Doc {
	c := p.host(v)
	if v.SourceBraced {
		c.token("{")
	}
	if v.Segments != nil {
		c.field(v.LiteralPos, p.sourceLiteral(v.LiteralPos, v.LiteralEnd, v.Lang, v.Segments, embeddedDelim(v.DoubleQuoted)))
	} else {
		c.goField(v.Expr, v.ExprPos, exprField, fmtExprPreserving)
		c.stages(v.Stages)
	}
	if v.SourceBraced {
		c.token("}")
	}
	return c.done()
}

func (p *printer) sourceMarkupBody(c *hostCursor, nodes []ast.Markup, layout ast.BodyLayout) {
	c.docs = append(c.docs, pretty.Text(layout.Leading), pretty.Indent(p.children(nodes)), pretty.Text(layout.Trailing))
	if len(nodes) > 0 {
		c.pos = nodes[len(nodes)-1].End() + token.Pos(len(layout.Trailing))
	} else {
		c.pos += token.Pos(len(layout.Leading) + len(layout.Trailing))
	}
}
func (p *printer) sourceIf(i *ast.IfMarkup) pretty.Doc {
	c := p.host(i)
	c.token("{")
	p.sourceIfChain(c, i)
	c.token("}")
	return c.done()
}
func (p *printer) sourceIfChain(c *hostCursor, i *ast.IfMarkup) {
	c.token("if")
	if strings.ContainsAny(i.ThenLayout.Leading, "\r\n") && !p.anchoredGoField(i.Cond) {
		c.field(i.CondPos, p.goFieldDoc(i.Cond, ifField, fmtExpr, i.CondPos))
	} else {
		c.goField(i.Cond, i.CondPos, ifField, fmtExpr)
	}
	c.token("{")
	p.sourceMarkupBody(c, i.Then, i.ThenLayout)
	c.token("}")
	if len(i.Else) > 0 {
		c.token("else")
		if len(i.Else) == 1 {
			if nested, ok := i.Else[0].(*ast.IfMarkup); ok {
				p.sourceIfChain(c, nested)
				return
			}
		}
		c.token("{")
		p.sourceMarkupBody(c, i.Else, i.ElseLayout)
		c.token("}")
	}
}
func (p *printer) sourceFor(f *ast.ForMarkup) pretty.Doc {
	c := p.host(f)
	c.token("{")
	c.token("for")
	if strings.ContainsAny(f.Layout.Leading, "\r\n") && !p.anchoredGoField(f.Clause) {
		c.field(f.ClausePos, p.goFieldDoc(f.Clause, forField, fmtClause, f.ClausePos))
	} else {
		c.goField(f.Clause, f.ClausePos, forField, fmtClause)
	}
	c.token("{")
	p.sourceMarkupBody(c, f.Body, f.Layout)
	c.token("}")
	c.token("}")
	return c.done()
}
func (p *printer) sourceSwitch(s *ast.SwitchMarkup) pretty.Doc {
	c := p.host(s)
	c.token("{")
	c.token("switch")
	if s.Tag != "" {
		boundary := s.End()
		if len(s.Cases) > 0 {
			boundary = s.Cases[0].Pos()
		}
		c.goFieldBefore(s.Tag, s.TagPos, switchField, fmtExpr, boundary)
	}
	c.token("{")
	for _, cc := range s.Cases {
		if cc.Default {
			c.token("default")
		} else {
			c.token("case")
			if strings.ContainsAny(cc.Layout.Leading, "\r\n") && !p.anchoredGoField(cc.List) {
				c.field(cc.ListPos, p.goFieldDoc(cc.List, caseField, fmtCaseList, cc.ListPos))
			} else {
				c.goField(cc.List, cc.ListPos, caseField, fmtCaseList)
			}
		}
		c.token(":")
		p.sourceMarkupBody(c, cc.Body, cc.Layout)
	}
	c.token("}")
	c.token("}")
	return c.done()
}

func (p *printer) sourceGoBlock(b *ast.GoBlock) pretty.Doc {
	c := p.host(b)
	c.token("{{")
	text, values := p.goBlockCode(b.Code, b.CodePos)
	c.field(b.CodePos, p.goTextDoc(text, values, false))
	c.token("}}")
	return c.done()
}

func (p *printer) sourceComponent(v *ast.Component) pretty.Doc {
	c := p.host(v)
	c.token("component")
	if v.Recv != "" {
		c.field(v.RecvPos, pretty.Text(v.Recv))
	}
	c.field(v.NamePos, pretty.Text(v.Name))
	if v.TypeParams != "" {
		c.token("[")
		c.field(v.TypeParamsPos, pretty.Text(v.TypeParams))
		c.token("]")
	}
	if v.ParamsPos.IsValid() {
		c.token("(")
		if v.Params != "" {
			c.field(v.ParamsPos, pretty.Text(v.Params))
		}
		c.token(")")
	}
	c.token("{")
	p.sourceMarkupBody(c, v.Body, v.Layout)
	c.token("}")
	return pretty.Concat(c.done(), pretty.HardLine)
}

func (p *printer) sourceAttrChild(a ast.Attr) pretty.Doc {
	if doc, ok := p.sourceAttr(a); ok {
		return doc
	}
	return p.attrDoc(a)
}
func (p *printer) sourceLiteral(start, end token.Pos, lang ast.EmbeddedLang, nodes []ast.Markup, delim byte) pretty.Doc {
	c := &hostCursor{p: p, pos: start, end: end}
	p.sourceEmbedded(c, lang, nodes, delim)
	return c.done()
}
func (p *printer) sourceEmbedded(c *hostCursor, lang ast.EmbeddedLang, nodes []ast.Markup, delim byte) {
	c.token(embeddedLangName(lang) + string(delim))
	for _, n := range nodes {
		switch n := n.(type) {
		case *ast.Text:
			var b strings.Builder
			writeEmbeddedLiteralText(&b, n.Value, delim)
			c.child(n, pretty.Text(b.String()))
		case *ast.Interp:
			c.token("@")
			inner := p.host(n)
			inner.token("{")
			inner.goField(n.Expr, n.ExprPos, exprField, fmtExprPreserving)
			inner.stages(n.Stages)
			inner.token("}")
			c.child(n, inner.done())
		}
	}
	c.token(string(delim))
}

func nodeStages(n ast.Node) []ast.PipeStage {
	switch v := n.(type) {
	case *ast.Interp:
		return v.Stages
	case *ast.ExprAttr:
		return v.Stages
	case *ast.SpreadAttr:
		return v.Stages
	case *ast.ComposedPart:
		return v.Stages
	case *ast.ValueArm:
		return v.Stages
	case *ast.EmbeddedAttr:
		return v.Stages
	case *ast.EmbeddedInterp:
		return v.Stages
	}
	return nil
}
func (p *printer) sourceEmbeddedInterp(v *ast.EmbeddedInterp) pretty.Doc {
	c := p.host(v)
	c.token("{")
	p.sourceEmbedded(c, v.Lang, v.Segments, embeddedDelim(v.DoubleQuoted))
	c.stages(v.Stages)
	c.token("}")
	return c.done()
}

// sourceKey preserves a Go string token's authored spelling only while it
// denotes the current semantic key; edited keys use their current value.
func (p *printer) sourceKey(pair *ast.OrderedPair) string {
	if end, ok := p.source.FieldEnd(pair.KeyPos); ok {
		if raw, ok := p.source.Slice(pair.KeyPos, end); ok {
			if value, err := strconv.Unquote(raw); err == nil && value == pair.Key {
				return raw
			}
		}
	}
	return strconv.Quote(pair.Key)
}

// goFieldBefore may format a header whose following syntax boundary starts on
// another physical line. Only the parser-established trailing trivia is read.
func (c *hostCursor) goFieldBefore(src string, pos token.Pos, form goFieldForm, plain func(string) string, boundary token.Pos) {
	end, ok := c.p.source.FieldEnd(pos)
	gap, valid := c.p.source.Slice(end, boundary)
	trailing := gap[len(strings.TrimRight(gap, " \t\r\n\f")):]
	if ok && valid && strings.ContainsAny(trailing, "\r\n") && !c.p.anchoredGoField(src) {
		c.field(pos, c.p.goFieldDoc(src, form, plain, pos))
	} else {
		c.goField(src, pos, form, plain)
	}
}
