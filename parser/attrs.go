package parser

import (
	"go/scanner"
	"go/token"
	"strconv"
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
		// TODO: thread a base token.Pos into splitComposed so ClassPart stages
		// carry accurate source positions (needed for LSP cursor detection on
		// class/style pipelines). For now, pass NoPos; the LSP interp/exprattr
		// paths (parsePipe in markup.go) are already wired correctly.
		seed, stages, perr := parsePipe(exprSrc, token.NoPos)
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
		if rest, ok := strings.CutPrefix(inner, "..."); ok {
			expr := strings.TrimSpace(rest)
			return nil, p.errorf(attrStartPos, "expected `...` trailing spread inside `{ }` attribute; did you mean `{ %s... }`?", expr)
		}
		return nil, p.errorf(attrStartPos, "expected `...` trailing spread inside `{ }` attribute")
	}
	core := strings.TrimSpace(strings.TrimSuffix(inner, "..."))
	// The spread/splat subject may carry a `|>` pipeline. Its canonical form
	// parenthesizes the pipeline so the trailing `...` reads unambiguously as the
	// spread marker on the whole pipeline: `{ (seed |> f)... }`. parsePipe only
	// splits a top-level `|>`, so a fully-parenthesized pipeline first parses as a
	// stage-less seed; unwrap one outer paren layer in that case so it yields the
	// same seed+stages as the bare `{ seed |> f... }` form (and round-trips with
	// the printer's parenthesized output). A parenthesized NON-pipeline spread
	// keeps its parens.
	// TODO: compute proper base positions for spread pipeline stages (needed for
	// LSP cursor detection on spread pipelines). For now, pass NoPos.
	seed, stages, perr := parsePipe(core, token.NoPos)
	if perr != nil {
		return nil, perr
	}
	if len(stages) == 0 {
		if unwrapped, ok := balancedParenUnwrap(core); ok {
			if s2, st2, err := parsePipe(unwrapped, token.NoPos); err == nil && len(st2) > 0 {
				seed, stages = s2, st2
			}
		}
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
		// A standalone `{{ … }}` is not a valid spread attribute — it is only
		// legal as an attribute value after `name=`. Reject it with a pointed
		// error so users get a clear message rather than a cryptic spread error.
		if p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			return nil, p.errorf(p.posAt(p.i), "`{{ }}` is only valid as an attribute value `name={{ … }}`, not a standalone spread")
		}
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
	nameEnd := p.i

	// Lookahead: skip optional whitespace before '=' WITHOUT committing p.i.
	// This lets us tolerate `name = value`, `name =value`, and `name= value`
	// while still preserving the bool-attr case (`<div foo bar>`) exactly: if
	// no '=' is found across whitespace, p.i stays at nameEnd so the attribute
	// loop's skipSpace() handles the inter-attribute gap.
	j := nameEnd
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\r' || p.src[j] == '\n') {
		j++
	}

	if j >= len(p.src) || p.src[j] != '=' {
		// No '=' found: boolean attribute. Leave p.i at nameEnd.
		ba := &ast.BoolAttr{Name: name}
		ast.SetSpan(ba, attrStartPos, p.posAt(nameEnd))
		return ba, nil
	}

	// Found '='. Advance past it, then skip any post-'=' whitespace.
	p.i = j + 1
	for !p.eof() && (p.src[p.i] == ' ' || p.src[p.i] == '\t' || p.src[p.i] == '\r' || p.src[p.i] == '\n') {
		p.i++
	}

	// Dispatch on the value-start character. Each downstream parser assumes
	// the cursor is positioned exactly at the opening '"' or '{'.
	switch {
	case !p.eof() && p.src[p.i] == '"':
		p.i++ // past opening '"'
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
	case !p.eof() && p.src[p.i] == '{':
		if p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			return p.parseOrderedAttrsLiteral(name, attrStartPos)
		}
		if name == "class" || name == "style" {
			return p.parseComposedAttr(name, attrStartPos)
		}
		return p.parseAttrBraceValue(name, attrStartPos)
	default:
		return nil, p.errorf(p.pos(), "expected attribute value (\"…\" or { … }) after '=' for %q", name)
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

// parseOrderedAttrsLiteral parses `name={{ "k1": v1, "k2": v2 }}` in attribute
// position. The cursor must be at the FIRST `{` of `{{`; attrStartPos is the
// token.Pos of the attribute name start.
func (p *parser) parseOrderedAttrsLiteral(name string, attrStartPos token.Pos) (ast.Attr, error) {
	open := p.i // at first '{' of '{{'
	end, ok := goExprEnd(p.src, open)
	if !ok {
		return nil, p.errorf(p.posAt(open), "unterminated `{{` in %s value", name)
	}
	// inner is the text between '{{' and '}}', i.e. src[open+2 : end-1].
	inner := p.src[open+2 : end-1]
	pairs, err := p.splitOrderedPairs(inner, open+2)
	if err != nil {
		return nil, err
	}
	p.i = end + 1
	n := &ast.OrderedAttrsAttr{Name: name, Pairs: pairs}
	ast.SetSpan(n, attrStartPos, p.posAt(p.i))
	return n, nil
}

// splitOrderedPairs is the ordered-attrs counterpart of splitComposed: it uses
// go/scanner to scan `src` (the text between `{{` and `}}`) at brace/paren/
// bracket depth 0, recording comma and colon offsets, then segments on commas
// and splits each segment at its first depth-0 colon into a quoted-string key
// and a raw-Go value expression. base is the absolute byte offset of src[0]
// within the original source (used to compute ValuePos).
func (p *parser) splitOrderedPairs(src string, base int) ([]ast.OrderedPair, error) {
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

	// Segment boundaries: [-1] + commas + [len(src)].
	bounds := make([]int, 0, len(commas)+2)
	bounds = append(bounds, -1)
	bounds = append(bounds, commas...)
	bounds = append(bounds, len(src))

	var pairs []ast.OrderedPair
	for k := 0; k+1 < len(bounds); k++ {
		segStart := bounds[k] + 1
		segEnd := bounds[k+1]
		isLast := k+2 == len(bounds)
		if strings.TrimSpace(src[segStart:segEnd]) == "" {
			if isLast {
				continue // trailing comma is legal
			}
			// Leading or interior empty segment = stray comma. Position the error
			// at the offending comma (the preceding bounds value).
			commaOff := bounds[k+1]
			return nil, p.errorf(p.posAt(base+commaOff), "ordered-attrs literal has an empty pair (stray comma)")
		}

		// firstNonSpace returns the offset of the first non-whitespace byte in
		// src[start:end], or start if the segment is all whitespace.
		firstNonSpace := func(start, end int) int {
			for i := start; i < end; i++ {
				if src[i] != ' ' && src[i] != '\t' && src[i] != '\r' && src[i] != '\n' {
					return i
				}
			}
			return start
		}

		// Find the first depth-0 colon within this segment.
		colon := -1
		for _, c := range colons {
			if c > segStart && c < segEnd {
				colon = c
				break
			}
		}
		if colon < 0 {
			// No colon found: this is a bare key (e.g. `"data-x"` without a value).
			trimmed := strings.TrimSpace(src[segStart:segEnd])
			keyOff := firstNonSpace(segStart, segEnd)
			return nil, p.errorf(p.posAt(base+keyOff), "ordered-attrs pair %s is missing a %q", trimmed, ": value")
		}

		rawKey := strings.TrimSpace(src[segStart:colon])
		rawValue := strings.TrimSpace(src[colon+1 : segEnd])

		keyOff := firstNonSpace(segStart, colon)
		if rawValue == "" {
			return nil, p.errorf(p.posAt(base+keyOff), "ordered-attrs pair missing value for key %q", rawKey)
		}

		// The key MUST be a Go string literal. Unquote it.
		key, err := strconv.Unquote(rawKey)
		if err != nil {
			return nil, p.errorf(p.posAt(base+keyOff), "ordered-attrs key must be a quoted string literal, got %q", rawKey)
		}

		// ValuePos: offset of the first non-space byte after the colon in src,
		// translated back to absolute source position via base.
		valueStart := colon + 1
		for valueStart < segEnd && (src[valueStart] == ' ' || src[valueStart] == '\t') {
			valueStart++
		}

		pairs = append(pairs, ast.OrderedPair{
			Key:      key,
			Value:    rawValue,
			ValuePos: p.posAt(base + valueStart),
		})
	}
	return pairs, nil
}
