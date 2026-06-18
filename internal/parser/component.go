package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// parseComponent parses a `component [recv] Name[(params)] { body }`.
// Cursor must be at the start of the `component` keyword.
func (p *parser) parseComponent() (*ast.Component, error) {
	pos := p.pos()
	if !p.at("component") {
		return nil, fmt.Errorf("%d:%d: expected `component`", pos.Line, pos.Column)
	}
	p.i += len("component")
	c := &ast.Component{Pos: pos}

	p.skipSpace()
	// optional receiver
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			return nil, fmt.Errorf("%d:%d: unterminated receiver", p.pos().Line, p.pos().Column)
		}
		c.Recv = p.src[p.i : end+1]
		p.i = end + 1
		p.skipSpace()
	}

	// name
	start := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) && p.src[p.i] != '.' && p.src[p.i] != '-' {
		p.i++
	}
	c.Name = p.src[start:p.i]
	if c.Name == "" {
		return nil, fmt.Errorf("%d:%d: expected component name", p.pos().Line, p.pos().Column)
	}

	p.skipSpace()
	// optional params
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			return nil, fmt.Errorf("%d:%d: unterminated params", p.pos().Line, p.pos().Column)
		}
		c.Params = strings.TrimSpace(p.src[p.i+1 : end])
		p.i = end + 1
	}

	p.skipSpace()
	if p.peek() != '{' {
		return nil, fmt.Errorf("%d:%d: expected `{` to open component body", p.pos().Line, p.pos().Column)
	}
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated component body", p.pos().Line, p.pos().Column)
	}
	body := p.src[p.i+1 : end]
	p.i = end + 1

	sub := newParser(body)
	nodes, err := sub.parseNodesUntilEOF()
	if err != nil {
		return nil, err
	}
	c.Body = nodes
	return c, nil
}
