package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	pos := p.pos()
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{`", pos.Line, pos.Column)
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	try := false
	if strings.HasSuffix(inner, "?") {
		try = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "?"))
	}
	p.i = end + 1
	return &ast.Interp{Expr: inner, Try: try, Pos: pos}, nil
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	pos := p.pos()
	start := p.i
	for !p.eof() && p.src[p.i] != '<' && p.src[p.i] != '{' {
		p.i++
	}
	return &ast.Text{Value: p.src[start:p.i], Pos: pos}
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
			end, ok := goExprEnd(p.src, p.i)
			if !ok {
				return nil, fmt.Errorf("unterminated spread `{...`")
			}
			inner := strings.TrimSpace(p.src[p.i+1 : end])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			p.i = end + 1
			attrs = append(attrs, &ast.SpreadAttr{Expr: inner})
			continue
		}
		// attribute name
		start := p.i
		for !p.eof() && isAttrNameByte(p.src[p.i]) {
			p.i++
		}
		if p.i == start {
			return nil, fmt.Errorf("%d:%d: expected attribute name, got %q",
				p.pos().Line, p.pos().Column, string(p.peek()))
		}
		name := p.src[start:p.i]
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
			attrs = append(attrs, &ast.StaticAttr{Name: name, Value: val})
		case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
			p.i++ // past '='
			if a, err := p.parseAttrBraceValue(name); err != nil {
				return nil, err
			} else {
				attrs = append(attrs, a)
			}
		default:
			attrs = append(attrs, &ast.BoolAttr{Name: name})
		}
	}
}

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string) (ast.Attr, error) {
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
		inner := p.src[p.i+1 : end]
		sub := newParser(inner)
		nodes, err := sub.parseNodesUntilEOF()
		if err != nil {
			return nil, err
		}
		p.i = end + 1
		return &ast.MarkupAttr{Name: name, Value: nodes}, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	return &ast.ExprAttr{Name: name, Expr: in.Expr, Try: in.Try}, nil
}

// startsTag reports whether b can begin a tag name (letter) or a fragment close.
func startsTag(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '>' || b == '/'
}

// parseNodesUntilEOF is added in Task 8; temporary stub for now.
func (p *parser) parseNodesUntilEOF() ([]ast.Node, error) { return nil, nil }
