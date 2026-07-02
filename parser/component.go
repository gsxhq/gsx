package parser

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseComponent parses a `component [recv] Name[(params)] { body }`.
// Cursor must be at the start of the `component` keyword.
func (p *parser) parseComponent() (*ast.Component, error) {
	start := p.i
	startPos := p.posAt(start)
	if !p.at("component") {
		return nil, p.errorf(startPos, "expected `component`")
	}
	p.i += len("component")
	c := &ast.Component{}

	p.skipSpace()
	// optional receiver
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			return nil, p.errorf(p.pos(), "unterminated receiver")
		}
		c.RecvPos = p.posAt(p.i)
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
	c.NamePos = p.posAt(nameStart)
	if c.Name == "" {
		return nil, p.errorf(p.pos(), "expected component name")
	}

	p.skipSpace()
	// optional type params
	if p.peek() == '[' {
		end, ok := bracketEnd(p.src, p.i)
		if !ok {
			return nil, p.errorf(p.pos(), "unterminated type params")
		}
		raw := p.src[p.i+1 : end]
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		c.TypeParamsPos = p.posAt(p.i + 1 + lead)
		c.TypeParams = strings.TrimSpace(raw)
		p.i = end + 1
		p.skipSpace()
	}

	// optional params
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			return nil, p.errorf(p.pos(), "unterminated params")
		}
		raw := p.src[p.i+1 : end]
		// ParamsPos: first non-whitespace byte after `(` — the start of the param list.
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		c.ParamsPos = p.posAt(p.i + 1 + lead)
		c.Params = strings.TrimSpace(raw)
		p.i = end + 1
	}

	p.skipSpace()
	if p.peek() != '{' {
		return nil, p.errorf(p.pos(), "expected `{` to open component body")
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
