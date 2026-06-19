package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseComponent parses a `component [recv] Name[(params)] { body }`.
// Cursor must be at the start of the `component` keyword.
func (p *parser) parseComponent() (*ast.Component, error) {
	start := p.i
	startPos := p.posAt(start)
	curPos := p.file.Position(startPos)
	if !p.at("component") {
		return nil, fmt.Errorf("%d:%d: expected `component`", curPos.Line, curPos.Column)
	}
	p.i += len("component")
	c := &ast.Component{}

	p.skipSpace()
	// optional receiver
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated receiver", cp.Line, cp.Column)
		}
		c.Recv = p.src[p.i : end+1]
		p.i = end + 1
		p.skipSpace()
	}

	// name
	nameStart := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) && p.src[p.i] != '.' && p.src[p.i] != '-' {
		p.i++
	}
	c.Name = p.src[nameStart:p.i]
	if c.Name == "" {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected component name", cp.Line, cp.Column)
	}

	p.skipSpace()
	// optional params
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated params", cp.Line, cp.Column)
		}
		c.Params = strings.TrimSpace(p.src[p.i+1 : end])
		p.i = end + 1
	}

	p.skipSpace()
	if p.peek() != '{' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `{` to open component body", cp.Line, cp.Column)
	}
	p.i++ // past body '{'
	nodes, err := p.parseMarkupUntilClose("component body")
	if err != nil {
		return nil, err
	}
	c.Body = nodes
	ast.SetSpan(c, startPos, p.posAt(p.i))
	return c, nil
}
