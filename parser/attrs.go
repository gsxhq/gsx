package parser

import (
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
)

// splitComposed splits the inner source of a `class={ … }` / `style={ … }`
// value into contributions. Contributions are separated by commas at
// bracket/brace/paren depth 0; within a contribution a depth-0 ':' separates an
// `expr : cond` conditional from its condition. A trailing comma yields no empty
// part.
func splitComposed(src string) ([]ast.ClassPart, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	var commas, colons []int
	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		off := fset.Position(pos).Offset
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COMMA:
			if depth == 0 {
				commas = append(commas, off)
			}
		case token.COLON:
			if depth == 0 {
				colons = append(colons, off)
			}
		}
	}

	// Segment boundaries: [-1] + commas + [len]. Each segment is (start, end).
	bounds := make([]int, 0, len(commas)+2)
	bounds = append(bounds, -1)
	bounds = append(bounds, commas...)
	bounds = append(bounds, len(src))

	var parts []ast.ClassPart
	for k := 0; k+1 < len(bounds); k++ {
		segStart := bounds[k] + 1
		segEnd := bounds[k+1]
		if strings.TrimSpace(src[segStart:segEnd]) == "" {
			continue // empty segment (e.g. trailing comma)
		}
		colon := -1
		for _, c := range colons {
			if c > segStart && c < segEnd {
				colon = c
				break
			}
		}
		// The expr segment (before any `: cond` guard) may carry a `|>` pipeline.
		// The guard Cond is a plain boolean expression and is NEVER piped.
		var exprSrc, condSrc string
		if colon >= 0 {
			exprSrc = strings.TrimSpace(src[segStart:colon])
			condSrc = strings.TrimSpace(src[colon+1 : segEnd])
		} else {
			exprSrc = strings.TrimSpace(src[segStart:segEnd])
		}
		seed, stages, perr := parsePipe(exprSrc)
		if perr != nil {
			return nil, perr
		}
		parts = append(parts, ast.ClassPart{Expr: seed, Cond: condSrc, Stages: stages})
	}
	return parts, nil
}

// parseComposedAttr parses a `class={ … }` / `style={ … }` composable
// contribution list. Cursor must be at the '{' of the value.
func (p *parser) parseComposedAttr(name string, startPos token.Pos) (ast.Attr, error) {
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, p.errorf(p.pos(), "unterminated `{` in %s value", name)
	}
	parts, err := splitComposed(p.src[p.i+1 : end])
	if err != nil {
		return nil, err
	}
	p.i = end + 1
	n := &ast.ClassAttr{Name: name, Parts: parts}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseSpreadAttr parses `{ expr... }` at the cursor (which must be at '{').
// The trailing `...` is the Go-convention spread (matching templ `{ p.Attrs... }`).
// In attribute position a `{ }` without trailing `...` is an error.
func (p *parser) parseSpreadAttr() (ast.Attr, error) {
	attrStartPos := p.posAt(p.i)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, p.errorf(p.pos(), "unterminated `{` in attributes")
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	if !strings.HasSuffix(inner, "...") {
		// Detect the old leading-dots form and emit a helpful hint.
		if strings.HasPrefix(inner, "...") {
			expr := strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			return nil, p.errorf(attrStartPos, "expected `...` trailing spread inside `{ }` attribute; did you mean `{ %s... }`?", expr)
		}
		return nil, p.errorf(attrStartPos, "expected `...` trailing spread inside `{ }` attribute")
	}
	expr := strings.TrimSpace(strings.TrimSuffix(inner, "..."))
	// The spread/splat subject may carry a `|>` pipeline (`{ seed |> f... }`).
	seed, stages, perr := parsePipe(expr)
	if perr != nil {
		return nil, perr
	}
	p.i = end + 1
	sa := &ast.SpreadAttr{Expr: seed, Stages: stages}
	ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
	return sa, nil
}

// parseSingleAttr parses exactly one attribute at the cursor: a conditional
// `{ if … }`, a spread `{ expr... }`, or a name-based attribute
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
		return nil, p.errorf(p.pos(), "expected attribute name, got %q", string(p.peek()))
	}
	name := p.src[attrStart:p.i]
	switch {
	case p.at(`="`):
		p.i += 2
		if p.classifier.Context(name) == attrclass.CtxJS {
			return p.parseJSAttrValue(name, attrStartPos)
		}
		vs := p.i
		for !p.eof() && p.src[p.i] != '"' {
			p.i++
		}
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated attribute string for %q", name)
		}
		val := p.src[vs:p.i]
		p.i++ // past closing quote
		sa := &ast.StaticAttr{Name: name, Value: val}
		ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
		return sa, nil
	case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
		p.i++ // past '='
		if name == "class" || name == "style" {
			return p.parseComposedAttr(name, attrStartPos)
		}
		return p.parseAttrBraceValue(name, attrStartPos)
	default:
		ba := &ast.BoolAttr{Name: name}
		ast.SetSpan(ba, attrStartPos, p.posAt(p.i))
		return ba, nil
	}
}

// parseJSAttrValue parses a JS-context attribute's double-quoted value, splitting
// @{ } holes into Text + Interp segments like a <script> body, bounded by the
// closing '"'. The cursor must be just past the opening `="`. If the value has at
// least one hole it returns a *ast.JSAttr; if it is hole-free it returns a
// *ast.StaticAttr with the raw value (no behavior change). parseInterp does
// Go-aware brace-balancing, so a '"' inside a hole (e.g. @{ "v" }) is consumed by
// the hole and does not prematurely terminate the value.
func (p *parser) parseJSAttrValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	valStart := p.i
	var segments []ast.Markup
	segStart := p.i
	segStartPos := p.posAt(p.i)
	hasInterp := false
	flush := func(end int) {
		if end > segStart {
			txt := &ast.Text{Value: p.src[segStart:end]}
			ast.SetSpan(txt, segStartPos, p.posAt(end))
			segments = append(segments, txt)
		}
	}
	for !p.eof() {
		// Closing quote terminates the value.
		if p.src[p.i] == '"' {
			flush(p.i)
			closeOff := p.i
			p.i++ // past closing quote
			if !hasInterp {
				// Hole-free JS attribute: keep StaticAttr (no behavior change).
				sa := &ast.StaticAttr{Name: name, Value: p.src[valStart:closeOff]}
				ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
				return sa, nil
			}
			ja := &ast.JSAttr{Name: name, Segments: segments}
			ast.SetSpan(ja, attrStartPos, p.posAt(p.i))
			return ja, nil
		}
		// Interpolation hole? (trigger is exactly `@{`.)
		if p.src[p.i] == '@' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			flush(p.i)
			p.i++ // past '@'; cursor now at '{' for parseInterp
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			segments = append(segments, in)
			hasInterp = true
			segStart = p.i
			segStartPos = p.posAt(p.i)
			continue
		}
		p.i++
	}
	return nil, p.errorf(p.pos(), "unterminated attribute string for %q", name)
}

// parseAttrsUntilBrace parses an attribute list terminated by '}' (the body of a
// conditional attribute). It consumes the closing '}'.
func (p *parser) parseAttrsUntilBrace() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unexpected EOF in `{ if … }` attribute body")
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
		return nil, p.errorf(p.pos(), "expected `}` to close `{ if … }` attribute")
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
	braceOff, ok := scanToBlockBrace(p.src, p.i, "if")
	if !ok {
		return nil, p.errorf(p.posAt(p.i), "expected `{` after `if` condition")
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
			return nil, p.errorf(p.pos(), "expected `{` or `if` after `else`")
		}
	}
	ast.SetSpan(n, kwPos, p.posAt(p.i))
	return n, nil
}
