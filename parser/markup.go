package parser

import (
	"fmt"
	"go/scanner"
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
	n := &ast.Interp{Expr: inner, Try: try}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseTextCtx consumes literal text up to the next '<' or '{' (or EOF). When
// inBlock is true (inside a control-flow body) it also stops at '}', which
// terminates the enclosing block.
func (p *parser) parseTextCtx(inBlock bool) *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	for !p.eof() {
		b := p.src[p.i]
		if b == '<' || b == '{' || (inBlock && b == '}') {
			break
		}
		p.i++
	}
	n := &ast.Text{Value: p.src[start:p.i]}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	return p.parseTextCtx(false)
}

func isAttrNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == ':' || b == '@' || b == '.' || b == '-'
}

// skipTagComment skips one // or /* */ comment in tag-interior position.
// Returns (true, nil) if a comment was consumed, (false, nil) if not at a comment,
// or (false, error) for an unterminated block comment.
func (p *parser) skipTagComment() (bool, error) {
	if p.at("/*") {
		start := p.i
		p.i += 2 // past '/*'
		for !p.eof() {
			if p.at("*/") {
				p.i += 2 // past '*/'
				return true, nil
			}
			p.i++
		}
		// unterminated
		startPos := p.posAt(start)
		resolvedPos := p.file.Position(startPos)
		return false, fmt.Errorf("%d:%d: unterminated block comment", resolvedPos.Line, resolvedPos.Column)
	}
	if p.at("//") {
		p.i += 2 // past '//'
		for !p.eof() && p.src[p.i] != '\n' {
			p.i++
		}
		// leave '\n' in place so skipSpace() sees it
		return true, nil
	}
	return false, nil
}

// commentOnly reports whether src contains only Go comments (no real expression tokens).
// A {/* … */} or {// … \n} whose body passes this check can be silently dropped.
func commentOnly(src string) bool {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)
	for {
		_, tok, _ := s.Scan()
		switch tok {
		case token.EOF:
			return true
		case token.COMMENT, token.SEMICOLON:
			// allowed — comments and auto-inserted semicolons are fine
		default:
			return false
		}
	}
}

// skipBracedComment checks whether the `{…}` at the current cursor is comment-only.
// If so, it advances past the closing `}` and returns (true, nil).
// Otherwise it returns (false, nil) without moving the cursor.
// Unterminated `{` is not an error here — parseInterp handles that.
func (p *parser) skipBracedComment() (bool, error) {
	if p.peek() != '{' {
		return false, nil
	}
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return false, nil
	}
	inner := p.src[p.i+1 : end]
	if !commentOnly(inner) {
		return false, nil
	}
	p.i = end + 1
	return true, nil
}

// parseGoBlock parses `{{ stmt }}`. Cursor must be at the first '{' of `{{`.
// It captures the Go statement source between the doubled braces. Nested Go
// braces are handled by go/scanner brace-matching.
func (p *parser) parseGoBlock() (*ast.GoBlock, error) {
	startPos := p.posAt(p.i)
	cp := p.file.Position(startPos)
	outerEnd, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{{`", cp.Line, cp.Column)
	}
	innerEnd, ok := goExprEnd(p.src, p.i+1)
	if !ok || innerEnd >= outerEnd {
		return nil, fmt.Errorf("%d:%d: malformed `{{ }}` block", cp.Line, cp.Column)
	}
	if strings.TrimSpace(p.src[innerEnd+1:outerEnd]) != "" {
		return nil, fmt.Errorf("%d:%d: malformed `{{ }}` block", cp.Line, cp.Column)
	}
	code := strings.TrimSpace(p.src[p.i+2 : innerEnd])
	p.i = outerEnd + 1
	n := &ast.GoBlock{Code: code}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseBraceNode dispatches a `{`-leading construct in a child/markup context.
// Cursor must be at '{'. It returns (node, false, nil) for a GoBlock, control
// flow, or interpolation; (nil, true, nil) when a comment-only `{ }` was
// skipped; or (nil, false, err) on error. Control-flow cases are wired in
// Tasks 3–5.
func (p *parser) parseBraceNode() (ast.Markup, bool, error) {
	if p.at("{{") {
		gb, err := p.parseGoBlock()
		return gb, false, err
	}
	if sk, err := p.skipBracedComment(); err != nil {
		return nil, false, err
	} else if sk {
		return nil, true, nil
	}
	in, err := p.parseInterp()
	return in, false, err
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
		// skip tag-interior // or /* */ comments
		if sk, err := p.skipTagComment(); err != nil {
			return nil, err
		} else if sk {
			continue
		}
		// {...expr} spread — tolerant of whitespace after `{` and around `...`
		// (e.g. `{ ...attrs }`). In attribute position a `{ }` is always a spread.
		if p.peek() == '{' {
			attrStart := p.i
			attrStartPos := p.posAt(attrStart)
			end, ok := goExprEnd(p.src, p.i)
			if !ok {
				return nil, fmt.Errorf("unterminated `{` in attributes")
			}
			inner := strings.TrimSpace(p.src[p.i+1 : end])
			if !strings.HasPrefix(inner, "...") {
				curPos := p.file.Position(attrStartPos)
				return nil, fmt.Errorf("%d:%d: expected `...` spread inside `{ }` attribute",
					curPos.Line, curPos.Column)
			}
			expr := strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			p.i = end + 1
			sa := &ast.SpreadAttr{Expr: expr}
			ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
			attrs = append(attrs, sa)
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
			sa := &ast.StaticAttr{Name: name, Value: val}
			ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
			attrs = append(attrs, sa)
		case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
			p.i++ // past '='
			if a, err := p.parseAttrBraceValue(name, attrStartPos); err != nil {
				return nil, err
			} else {
				attrs = append(attrs, a)
			}
		default:
			ba := &ast.BoolAttr{Name: name}
			ast.SetSpan(ba, attrStartPos, p.posAt(p.i))
			attrs = append(attrs, ba)
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
		ma := &ast.MarkupAttr{Name: name, Value: nodes}
		ast.SetSpan(ma, attrStartPos, p.posAt(p.i))
		return ma, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	ea := &ast.ExprAttr{Name: name, Expr: in.Expr, Try: in.Try}
	ast.SetSpan(ea, attrStartPos, in.End())
	return ea, nil
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
		fr := &ast.Fragment{Children: children}
		ast.SetSpan(fr, startPos, p.posAt(p.i))
		return fr, nil
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
		el := &ast.Element{Tag: tag, Void: true, Attrs: attrs}
		ast.SetSpan(el, startPos, p.posAt(p.i))
		return el, nil
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
	el := &ast.Element{Tag: tag, Attrs: attrs, Children: children}
	ast.SetSpan(el, startPos, p.posAt(p.i))
	return el, nil
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
			node, skipped, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			if skipped {
				continue
			}
			nodes = append(nodes, node)
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
			node, skipped, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			if skipped {
				continue
			}
			nodes = append(nodes, node)
		default:
			nodes = append(nodes, p.parseText())
		}
	}
}
