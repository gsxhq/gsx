package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseSpreadAttr parses `{ ...expr }` at the cursor (which must be at '{'),
// tolerant of whitespace after '{' and around '...'. In attribute position a
// non-spread, non-conditional `{ }` is an error.
func (p *parser) parseSpreadAttr() (ast.Attr, error) {
	attrStartPos := p.posAt(p.i)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("unterminated `{` in attributes")
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	if !strings.HasPrefix(inner, "...") {
		cp := p.file.Position(attrStartPos)
		return nil, fmt.Errorf("%d:%d: expected `...` spread inside `{ }` attribute", cp.Line, cp.Column)
	}
	expr := strings.TrimSpace(strings.TrimPrefix(inner, "..."))
	p.i = end + 1
	sa := &ast.SpreadAttr{Expr: expr}
	ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
	return sa, nil
}

// parseSingleAttr parses exactly one attribute at the cursor: a conditional
// `{ if … }`, a spread `{ ...expr }`, or a name-based attribute
// (static / expr / markup / bool). The cursor must be at the attribute start
// (not whitespace, not a comment, not a terminator).
func (p *parser) parseSingleAttr() (ast.Attr, error) {
	if p.peek() == '{' {
		if p.braceKeyword() == "if" {
			return p.parseCondAttr()
		}
		return p.parseSpreadAttr()
	}
	attrStart := p.i
	attrStartPos := p.posAt(attrStart)
	for !p.eof() && isAttrNameByte(p.src[p.i]) {
		p.i++
	}
	if p.i == attrStart {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected attribute name, got %q", cp.Line, cp.Column, string(p.peek()))
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
		sa := &ast.StaticAttr{Name: name, Value: val}
		ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
		return sa, nil
	case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
		p.i++ // past '='
		return p.parseAttrBraceValue(name, attrStartPos)
	default:
		ba := &ast.BoolAttr{Name: name}
		ast.SetSpan(ba, attrStartPos, p.posAt(p.i))
		return ba, nil
	}
}

// parseAttrsUntilBrace parses an attribute list terminated by '}' (the body of a
// conditional attribute). It consumes the closing '}'.
func (p *parser) parseAttrsUntilBrace() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		p.skipSpace()
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF in `{ if … }` attribute body")
		}
		if p.peek() == '}' {
			p.i++ // consume '}'
			return attrs, nil
		}
		if sk, err := p.skipTagComment(); err != nil {
			return nil, err
		} else if sk {
			continue
		}
		a, err := p.parseSingleAttr()
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, a)
	}
}

// parseCondAttr parses `{ if Cond { Then } [else …] }` in attribute position.
// Cursor at '{'; the caller has verified the leading keyword is "if".
func (p *parser) parseCondAttr() (ast.Attr, error) {
	startPos := p.posAt(p.i)
	p.i++ // past outer '{'
	p.skipSpace()
	n, err := p.parseCondAttrTail()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `}` to close `{ if … }` attribute", cp.Line, cp.Column)
	}
	p.i++ // past outer '}'
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseCondAttrTail parses `if Cond { Then } [else if … | else { Else }]` with
// the cursor at the `if` keyword. An `else if` builds a nested CondAttr in Else.
func (p *parser) parseCondAttrTail() (*ast.CondAttr, error) {
	kwPos := p.posAt(p.i)
	p.i += 2 // past 'if'
	condStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i)
	if !ok {
		cp := p.file.Position(p.posAt(p.i))
		return nil, fmt.Errorf("%d:%d: expected `{` after `if` condition", cp.Line, cp.Column)
	}
	cond := strings.TrimSpace(p.src[condStart:braceOff])
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseAttrsUntilBrace()
	if err != nil {
		return nil, err
	}
	n := &ast.CondAttr{Cond: cond, Then: body}
	p.skipSpace()
	if p.atWord("else") {
		p.i += len("else")
		p.skipSpace()
		switch {
		case p.peek() == '{':
			p.i++ // past '{'
			elseBody, err := p.parseAttrsUntilBrace()
			if err != nil {
				return nil, err
			}
			n.Else = elseBody
		case p.atWord("if"):
			elseIf, err := p.parseCondAttrTail()
			if err != nil {
				return nil, err
			}
			n.Else = []ast.Attr{elseIf}
		default:
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected `{` or `if` after `else`", cp.Line, cp.Column)
		}
	}
	ast.SetSpan(n, kwPos, p.posAt(p.i))
	return n, nil
}
