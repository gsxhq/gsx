package parser

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	start := p.i
	startPos := p.posAt(start)
	resolvedPos := p.file.Position(startPos)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{`", resolvedPos.Line, resolvedPos.Column)
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	try := false
	if strings.HasSuffix(inner, "?") {
		try = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "?"))
	}
	p.i = end + 1
	return &ast.Interp{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Expr: inner, Try: try}, nil
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	for !p.eof() && p.src[p.i] != '<' && p.src[p.i] != '{' {
		p.i++
	}
	return &ast.Text{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Value: p.src[start:p.i]}
}

func isAttrNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == ':' || b == '@' || b == '.' || b == '-'
}

func (p *parser) parseAttrs() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		p.skipSpace()
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF in attributes")
		}
		if p.peek() == '>' || p.at("/>") {
			return attrs, nil
		}
		// {...expr} spread
		if p.at("{...") {
			attrStart := p.i
			attrStartPos := p.posAt(attrStart)
			end, ok := goExprEnd(p.src, p.i)
			if !ok {
				return nil, fmt.Errorf("unterminated spread `{...`")
			}
			inner := strings.TrimSpace(p.src[p.i+1 : end])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			p.i = end + 1
			attrs = append(attrs, &ast.SpreadAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Expr: inner})
			continue
		}
		// attribute name
		attrStart := p.i
		attrStartPos := p.posAt(attrStart)
		for !p.eof() && isAttrNameByte(p.src[p.i]) {
			p.i++
		}
		if p.i == attrStart {
			curPos := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected attribute name, got %q",
				curPos.Line, curPos.Column, string(p.peek()))
		}
		name := p.src[attrStart:p.i]
		switch {
		case p.at(`="`):
			p.i += 2
			vs := p.i
			for !p.eof() && p.src[p.i] != '"' {
				p.i++
			}
			if p.eof() {
				return nil, fmt.Errorf("unterminated attribute string for %q", name)
			}
			val := p.src[vs:p.i]
			p.i++ // past closing quote
			attrs = append(attrs, &ast.StaticAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name, Value: val})
		case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
			p.i++ // past '='
			if a, err := p.parseAttrBraceValue(name, attrStartPos); err != nil {
				return nil, err
			} else {
				attrs = append(attrs, a)
			}
		default:
			attrs = append(attrs, &ast.BoolAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name})
		}
	}
}

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	// Babel rule: first non-space inside the braces starting markup?
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	if j < len(p.src) && p.src[j] == '<' && j+1 < len(p.src) && startsTag(p.src[j+1]) {
		end, ok := goExprEnd(p.src, p.i) // markup is brace-balanced too
		if !ok {
			return nil, fmt.Errorf("unterminated markup attribute %q", name)
		}
		innerStart := p.i + 1
		inner := p.src[innerStart:end]
		subBase := p.base + innerStart
		sub := newSub(p.file, inner, subBase)
		nodes, err := sub.parseNodesUntilEOF()
		if err != nil {
			return nil, err
		}
		p.i = end + 1
		return &ast.MarkupAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name, Value: nodes}, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	return &ast.ExprAttr{Span: ast.Span{Start: attrStartPos, Finish: in.Span.Finish}, Name: name, Expr: in.Expr, Try: in.Try}, nil
}

// startsTag reports whether b can begin a tag name (letter) or a fragment close.
func startsTag(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '>' || b == '/'
}

func isTagNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '-' || b == '.'
}

func (p *parser) parseElement() (ast.Markup, error) {
	start := p.i
	startPos := p.posAt(start)
	resolvedPos := p.file.Position(startPos)
	if p.peek() != '<' {
		return nil, fmt.Errorf("%d:%d: expected '<'", resolvedPos.Line, resolvedPos.Column)
	}
	p.i++ // past '<'

	// Fragment: <>…</>
	if p.peek() == '>' {
		p.i++ // past '>'
		children, err := p.parseChildren("")
		if err != nil {
			return nil, err
		}
		return &ast.Fragment{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Children: children}, nil
	}

	tagStart := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) {
		p.i++
	}
	tag := p.src[tagStart:p.i]
	if tag == "" {
		return nil, fmt.Errorf("%d:%d: expected tag name", resolvedPos.Line, resolvedPos.Column)
	}

	attrs, err := p.parseAttrs()
	if err != nil {
		return nil, err
	}

	if p.at("/>") {
		p.i += 2
		return &ast.Element{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Tag: tag, Void: true, Attrs: attrs}, nil
	}
	if p.peek() != '>' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected '>' or '/>' in <%s>", cp.Line, cp.Column, tag)
	}
	p.i++ // past '>'

	children, err := p.parseChildren(tag)
	if err != nil {
		return nil, err
	}
	return &ast.Element{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Tag: tag, Attrs: attrs, Children: children}, nil
}

func (p *parser) parseChildren(closeTag string) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF, expected </%s>", closeTag)
		}
		if p.at("</") {
			mmPos := p.file.Position(p.pos())
			// consume close tag
			p.i += 2
			start := p.i
			for !p.eof() && isTagNameByte(p.src[p.i]) {
				p.i++
			}
			got := p.src[start:p.i]
			p.skipSpace()
			if p.peek() != '>' {
				cp := p.file.Position(p.pos())
				return nil, fmt.Errorf("%d:%d: malformed close tag", cp.Line, cp.Column)
			}
			p.i++ // past '>'
			if got != closeTag {
				return nil, fmt.Errorf("%d:%d: mismatched close tag </%s>, expected </%s>",
					mmPos.Line, mmPos.Column, got, closeTag)
			}
			return nodes, nil
		}
		if p.peek() == '<' {
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, el)
			continue
		}
		if p.peek() == '{' {
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			continue
		}
		nodes = append(nodes, p.parseText())
	}
}

func (p *parser) parseNodesUntilEOF() ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		p.skipSpace()
		if p.eof() {
			return nodes, nil
		}
		switch {
		case p.peek() == '<':
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, el)
		case p.peek() == '{':
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
		default:
			nodes = append(nodes, p.parseText())
		}
	}
}
