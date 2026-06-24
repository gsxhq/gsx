package parser

import (
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	start := p.i
	startPos := p.posAt(start)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, p.errorf(startPos, "unterminated `{`")
	}
	rawInner := p.src[p.i+1 : end]
	lead := len(rawInner) - len(strings.TrimLeft(rawInner, " \t\r\n"))
	exprPos := p.posAt(p.i + 1 + lead)
	inner := strings.TrimSpace(rawInner)
	seed, seedTry, stages, perr := parsePipe(inner)
	if perr != nil {
		return nil, p.errorf(startPos, "%v", perr)
	}
	p.i = end + 1
	n := &ast.Interp{Expr: seed, Try: seedTry, Stages: stages, ExprPos: exprPos}
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
		return false, p.errorf(startPos, "unterminated block comment")
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
	outerEnd, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, p.errorf(startPos, "unterminated `{{`")
	}
	innerEnd, ok := goExprEnd(p.src, p.i+1)
	if !ok || innerEnd >= outerEnd {
		return nil, p.errorf(startPos, "malformed `{{ }}` block")
	}
	if strings.TrimSpace(p.src[innerEnd+1:outerEnd]) != "" {
		return nil, p.errorf(startPos, "malformed `{{ }}` block")
	}
	code := strings.TrimSpace(p.src[p.i+2 : innerEnd])
	p.i = outerEnd + 1
	n := &ast.GoBlock{Code: code}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// isIdentByte reports whether b can be part of a Go identifier.
func isIdentByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_'
}

// atWord reports whether the source at the cursor is exactly the word w,
// not followed by an identifier character (so `else` matches but `elsewhere`
// does not).
func (p *parser) atWord(w string) bool {
	if !p.at(w) {
		return false
	}
	next := p.i + len(w)
	return next >= len(p.src) || !isIdentByte(p.src[next])
}

// braceKeyword returns the leading control-flow keyword ("if", "for", "switch")
// inside the `{ … }` at the cursor (which must be at '{'), or "" if the first
// token is not one of those keywords. It does not move the cursor.
func (p *parser) braceKeyword() string {
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	start := j
	for j < len(p.src) && p.src[j] >= 'a' && p.src[j] <= 'z' {
		j++
	}
	kw := p.src[start:j]
	switch kw {
	case "if", "for", "switch":
		if j < len(p.src) && isIdentByte(p.src[j]) {
			return ""
		}
		return kw
	}
	return ""
}

// parseMarkupUntilClose parses a markup sequence terminated by the matching
// top-level '}', which it consumes. `what` names the enclosing construct for the
// unterminated-EOF error (e.g. "control-flow body", "component body"). Inter-node
// whitespace is skipped; text within nodes is preserved. The terminating '}' is
// the first top-level '}'; a '}' inside a nested element's text or a `{…}`
// construct is consumed by those sub-parsers, not seen here.
func (p *parser) parseMarkupUntilClose(what string) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated %s, expected `}`", what)
		}
		switch {
		case p.peek() == '}':
			p.i++ // consume the closing brace
			return nodes, nil
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
			if !skipped {
				nodes = append(nodes, node)
			}
		default:
			nodes = append(nodes, p.parseTextCtx(true))
		}
	}
}

// parseControlBody parses a control-flow body: markup until the matching '}'.
// The cursor must be just past the opening '{'.
func (p *parser) parseControlBody() ([]ast.Markup, error) {
	return p.parseMarkupUntilClose("control-flow body")
}

// parseForMarkup parses `{ for Clause { Body } }`. Cursor at '{'; the caller has
// verified the leading keyword is "for".
func (p *parser) parseForMarkup() (ast.Markup, error) {
	startPos := p.posAt(p.i)
	p.i++ // past '{'
	p.skipSpace()
	p.i += len("for")
	clauseStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i, "for")
	if !ok {
		return nil, p.errorf(p.posAt(p.i), "expected `{` after `for` clause")
	}
	clause := strings.TrimSpace(p.src[clauseStart:braceOff])
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseControlBody()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		return nil, p.errorf(p.pos(), "expected `}` to close `{ for … }`")
	}
	p.i++ // past outer '}'
	n := &ast.ForMarkup{Clause: clause, Body: body}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseIfMarkup parses `{ if … { … } [else …] }`. Cursor at '{'; the caller has
// verified the leading keyword is "if".
func (p *parser) parseIfMarkup() (ast.Markup, error) {
	startPos := p.posAt(p.i)
	p.i++ // past outer '{'
	p.skipSpace()
	n, err := p.parseIfTail()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		return nil, p.errorf(p.pos(), "expected `}` to close `{ if … }`")
	}
	p.i++ // past outer '}'
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseIfTail parses `if Cond { Then } [else if … | else { Else }]`, with the
// cursor at the `if` keyword. It is recursive: an `else if` builds a nested
// IfMarkup in Else.
func (p *parser) parseIfTail() (*ast.IfMarkup, error) {
	kwPos := p.posAt(p.i)
	p.i += 2 // past 'if'
	condStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i, "if")
	if !ok {
		return nil, p.errorf(p.posAt(p.i), "expected `{` after `if` condition")
	}
	cond := strings.TrimSpace(p.src[condStart:braceOff])
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseControlBody()
	if err != nil {
		return nil, err
	}
	n := &ast.IfMarkup{Cond: cond, Then: body}
	p.skipSpace()
	if p.atWord("else") {
		p.i += len("else")
		p.skipSpace()
		switch {
		case p.peek() == '{':
			p.i++ // past '{'
			elseBody, err := p.parseControlBody()
			if err != nil {
				return nil, err
			}
			n.Else = elseBody
		case p.atWord("if"):
			elseIf, err := p.parseIfTail()
			if err != nil {
				return nil, err
			}
			n.Else = []ast.Markup{elseIf}
		default:
			return nil, p.errorf(p.pos(), "expected `{` or `if` after `else`")
		}
	}
	ast.SetSpan(n, kwPos, p.posAt(p.i))
	return n, nil
}

// parseSwitchMarkup parses `{ switch [Tag] { case … default … } }`. Cursor at
// '{'; the caller has verified the leading keyword is "switch".
func (p *parser) parseSwitchMarkup() (ast.Markup, error) {
	startPos := p.posAt(p.i)
	p.i++ // past outer '{'
	p.skipSpace()
	p.i += len("switch")
	tagStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i, "switch")
	if !ok {
		return nil, p.errorf(p.posAt(p.i), "expected `{` after `switch`")
	}
	tag := strings.TrimSpace(p.src[tagStart:braceOff])
	p.i = braceOff + 1 // past switch-body '{'

	var cases []*ast.CaseClause
	for {
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated `switch`, expected `}`")
		}
		if p.peek() == '}' {
			p.i++ // past switch-body '}'
			break
		}
		cc, err := p.parseCaseClause()
		if err != nil {
			return nil, err
		}
		cases = append(cases, cc)
	}

	p.skipSpace()
	if p.peek() != '}' {
		return nil, p.errorf(p.pos(), "expected `}` to close `{ switch … }`")
	}
	p.i++ // past outer '}'
	n := &ast.SwitchMarkup{Tag: tag, Cases: cases}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseCaseClause parses one `case List:` or `default:` arm with its markup
// body. Cursor at the `case` or `default` keyword.
func (p *parser) parseCaseClause() (*ast.CaseClause, error) {
	startPos := p.posAt(p.i)
	cc := &ast.CaseClause{}
	switch {
	case p.atWord("case"):
		p.i += len("case")
		listStart := p.i
		colonOff, ok := scanToCaseColon(p.src, p.i)
		if !ok {
			return nil, p.errorf(p.posAt(p.i), "expected `:` in `case`")
		}
		cc.List = strings.TrimSpace(p.src[listStart:colonOff])
		p.i = colonOff + 1 // past ':'
	case p.atWord("default"):
		p.i += len("default")
		p.skipSpace()
		if p.peek() != ':' {
			return nil, p.errorf(p.pos(), "expected `:` after `default`")
		}
		cc.Default = true
		p.i++ // past ':'
	default:
		return nil, p.errorf(p.pos(), "expected `case` or `default` in `switch`")
	}
	body, err := p.parseCaseBody()
	if err != nil {
		return nil, err
	}
	cc.Body = body
	ast.SetSpan(cc, startPos, p.posAt(p.i))
	return cc, nil
}

// parseCaseBody parses the markup body of a case arm. It does NOT consume the
// terminator: it stops (without advancing) at the next `case`/`default` keyword
// or at the switch body's closing `}`.
func (p *parser) parseCaseBody() ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated `case` body")
		}
		if p.peek() == '}' || p.atWord("case") || p.atWord("default") {
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
			if !skipped {
				nodes = append(nodes, node)
			}
		default:
			nodes = append(nodes, p.parseTextCtx(true))
		}
	}
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
	switch p.braceKeyword() {
	case "if":
		n, err := p.parseIfMarkup()
		return n, false, err
	case "for":
		n, err := p.parseForMarkup()
		return n, false, err
	case "switch":
		n, err := p.parseSwitchMarkup()
		return n, false, err
	}
	in, err := p.parseInterp()
	return in, false, err
}

func (p *parser) parseAttrs() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unexpected EOF in attributes")
		}
		if p.peek() == '>' || p.at("/>") {
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

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	// Babel rule: first non-space inside the braces starting markup?
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	if j < len(p.src) && p.src[j] == '<' && j+1 < len(p.src) && startsTag(p.src[j+1]) {
		p.i++ // past '{'
		nodes, err := p.parseMarkupUntilClose("markup attribute")
		if err != nil {
			return nil, err
		}
		ma := &ast.MarkupAttr{Name: name, Value: nodes}
		ast.SetSpan(ma, attrStartPos, p.posAt(p.i))
		return ma, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	ea := &ast.ExprAttr{Name: name, Expr: in.Expr, Try: in.Try, Stages: in.Stages}
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
	if p.peek() != '<' {
		return nil, p.errorf(startPos, "expected '<'")
	}
	p.i++ // past '<'

	// `<!…`: DOCTYPE or HTML comment (both preserved verbatim).
	if p.peek() == '!' {
		return p.parseBang(start, startPos)
	}

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
		return nil, p.errorf(startPos, "expected tag name")
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
		return nil, p.errorf(p.pos(), "expected '>' or '/>' in <%s>", tag)
	}
	p.i++ // past '>'

	// Raw-text elements (<script>, <style>): content is verbatim until the
	// matching case-insensitive close tag. No markup/interpolation inside.
	if isRawTextTag(tag) {
		children, err := p.parseRawTextBody(tag, startPos)
		if err != nil {
			return nil, err
		}
		el := &ast.Element{Tag: tag, Attrs: attrs, Children: children}
		ast.SetSpan(el, startPos, p.posAt(p.i))
		return el, nil
	}

	children, err := p.parseChildren(tag)
	if err != nil {
		return nil, err
	}
	el := &ast.Element{Tag: tag, Attrs: attrs, Children: children}
	ast.SetSpan(el, startPos, p.posAt(p.i))
	return el, nil
}

// isRawTextTag reports whether tag is an HTML raw-text element (case-insensitive
// "script" or "style"), whose body is consumed verbatim.
func isRawTextTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "script", "style":
		return true
	}
	return false
}

// parseBang parses a `<!…` construct after the leading `<!` `!` byte: either an
// HTML comment `<!-- … -->` or a `<!DOCTYPE …>` declaration. The cursor is at the
// '!'. start is the byte offset of the opening '<'; startPos describes it.
func (p *parser) parseBang(start int, startPos token.Pos) (ast.Markup, error) {
	if p.at("!--") {
		p.i += len("!--") // past '!--'
		bodyStart := p.i
		for !p.eof() {
			if p.at("-->") {
				text := p.src[bodyStart:p.i]
				p.i += len("-->")
				n := &ast.HTMLComment{Text: text}
				ast.SetSpan(n, startPos, p.posAt(p.i))
				return n, nil
			}
			p.i++
		}
		return nil, p.errorf(startPos, "unterminated `<!--` comment")
	}
	// DOCTYPE (case-insensitive); cursor at '!'.
	if len(p.src)-p.i >= len("!doctype") &&
		strings.EqualFold(p.src[p.i+1:p.i+1+len("doctype")], "doctype") {
		for !p.eof() {
			if p.peek() == '>' {
				p.i++ // past '>'
				n := &ast.Doctype{Text: p.src[start:p.i]}
				ast.SetSpan(n, startPos, p.posAt(p.i))
				return n, nil
			}
			p.i++
		}
		return nil, p.errorf(startPos, "unterminated `<!DOCTYPE`")
	}
	return nil, p.errorf(startPos, "expected `<!--` or `<!DOCTYPE` after `<!`")
}

// parseRawTextBody consumes a raw-text element body until the matching
// case-insensitive `</tag>` close tag, which it consumes. For <style> and
// <script> the body is split into Text and @{ … } Interp children; for every
// other raw-text tag the body is a single verbatim Text. openPos describes the
// open tag, used for the unterminated error.
func (p *parser) parseRawTextBody(tag string, openPos token.Pos) ([]ast.Markup, error) {
	interpolate := strings.EqualFold(tag, "style") || strings.EqualFold(tag, "script")
	closeLower := "</" + strings.ToLower(tag)
	var nodes []ast.Markup
	segStart := p.i
	segStartPos := p.posAt(p.i)
	flush := func(end int) {
		if end > segStart {
			txt := &ast.Text{Value: p.src[segStart:end]}
			ast.SetSpan(txt, segStartPos, p.posAt(end))
			nodes = append(nodes, txt)
		}
	}
	for !p.eof() {
		// Close tag?
		if p.peek() == '<' && p.i+1 < len(p.src) && p.src[p.i+1] == '/' &&
			p.i+len(closeLower) <= len(p.src) &&
			strings.EqualFold(p.src[p.i:p.i+len(closeLower)], closeLower) {
			after := p.i + len(closeLower)
			if after >= len(p.src) || !isTagNameByte(p.src[after]) {
				flush(p.i)
				p.i += len(closeLower)
				p.skipSpace()
				if p.peek() != '>' {
					return nil, p.errorf(p.pos(), "malformed close tag </%s>", tag)
				}
				p.i++ // past '>'
				return nodes, nil
			}
		}
		// Interpolation? (trigger is exactly `@{`.)
		if interpolate && p.peek() == '@' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			flush(p.i)
			p.i++ // past '@'; cursor now at '{' for parseInterp
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			segStart = p.i
			segStartPos = p.posAt(p.i)
			continue
		}
		p.i++
	}
	return nil, p.errorf(openPos, "unterminated raw-text element <%s>", tag)
}

func (p *parser) parseChildren(closeTag string) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		if p.eof() {
			return nil, p.errorf(token.NoPos, "unexpected EOF, expected </%s>", closeTag)
		}
		if p.at("</") {
			mmTokPos := p.pos()
			// consume close tag
			p.i += 2
			start := p.i
			for !p.eof() && isTagNameByte(p.src[p.i]) {
				p.i++
			}
			got := p.src[start:p.i]
			p.skipSpace()
			if p.peek() != '>' {
				return nil, p.errorf(p.pos(), "malformed close tag")
			}
			p.i++ // past '>'
			if got != closeTag {
				return nil, p.errorf(mmTokPos, "mismatched close tag </%s>, expected </%s>", got, closeTag)
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
