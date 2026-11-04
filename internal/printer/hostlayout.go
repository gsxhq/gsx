package printer

import (
	"go/token"
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
	c.field(pos, c.p.goFieldDoc(src, form, plain, pos))
}
func (c *hostCursor) stages(stages []ast.PipeStage) {
	for _, st := range stages {
		c.token("|>")
		c.field(st.NamePos, pretty.Text(st.Name))
		if st.HasArgs {
			c.token("(")
			c.field(st.ArgsPos, c.p.goFieldDoc(st.Args, argsField, fmtArgs, st.ArgsPos))
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
