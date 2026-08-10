package parser

import (
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
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
	seed, stages, perr := parsePipe(inner, exprPos)
	if perr != nil {
		return nil, p.pipeErrorf(startPos, perr)
	}
	// W1: catch a `|>` directly after a value-position literal NESTED inside
	// seed (e.g. `wrap(f`hi` |> upper)`) here, at parse time, rather than
	// leaving it to analyze.go's deferred SplitGoExprElements split. seed's
	// OWN top-level `|>` chain was already peeled into stages by parsePipe
	// above, so this only ever fires on a literal-pipe buried inside a larger
	// Go expression — see reportWholeLiteralPipes's doc for why this can't
	// just wait for that later split: by the time it runs, the malformed
	// `|>` text has already been folded into the analyzed AST, and reporting
	// it there competes with (and today loses to) the skeleton's own Go
	// parse of the same broken text, which fires first and aborts with a
	// generic, unpositioned-feeling error instead of this one.
	// containsEmbeddedLiteralPrefix (not a bare delimiter check) gates the
	// scan: parseInterp is the parse hot path, and an ordinary Go string in
	// the seed must not pay for a go/scanner tokenization.
	if containsEmbeddedLiteralPrefix(seed) {
		reportWholeLiteralPipes(p, scanGoParts(seed), exprPos)
	}
	p.i = end + 1
	n := &ast.Interp{Expr: seed, Stages: stages, ExprPos: exprPos}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseTextCtx consumes literal text up to the next '<' or '{' (or EOF). When
// inBlock is true (inside a control-flow body) it also stops at '}', which
// terminates the enclosing block. It also stops at a line-start `//` content
// comment, leaving it for the caller to parse as a separate Comment node.
func (p *parser) parseTextCtx(inBlock bool) *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	for !p.eof() {
		b := p.src[p.i]
		if b == '<' || b == '{' || (inBlock && b == '}') {
			break
		}
		if b == '/' && p.atBareContentComment() {
			break
		}
		p.i++
	}
	n := &ast.Text{Value: p.src[start:p.i]}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n
}

// atBareContentComment reports whether the cursor sits at a line-start `//` in
// child content: the next two bytes are slashes and every byte between the
// previous newline (or file start) and the cursor is space, tab, or CR.
// Suppressed inside pre/textarea subtrees, whose text is verbatim.
func (p *parser) atBareContentComment() bool { return p.atBareContentCommentAt(p.i) }

// atBareContentCommentAt is atBareContentComment at an arbitrary offset, so a
// text scanner can test a position it has not moved the cursor to yet.
func (p *parser) atBareContentCommentAt(off int) bool {
	if p.preserveDepth > 0 || !strings.HasPrefix(p.src[off:], "//") {
		return false
	}
	for j := off - 1; j >= 0; j-- {
		switch p.src[j] {
		case ' ', '\t', '\r':
		case '\n':
			return true
		default:
			return false
		}
	}
	return true // file start counts as line start
}

// parseBareComment consumes a bare `//` line comment to end of line (the '\n'
// is left for the following text node / skipSpace). Cursor must satisfy
// atBareContentComment.
func (p *parser) parseBareComment() *ast.Comment {
	start := p.i
	p.i += 2 // past '//'
	for !p.eof() && p.src[p.i] != '\n' {
		p.i++
	}
	n := &ast.Comment{Text: strings.TrimSpace(p.src[start+2 : p.i]), Bare: true}
	ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
	return n
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	return p.parseTextCtx(false)
}

// parseTagComment recognizes a tag-interior comment at the cursor: bare `//` or
// `/* */`, or a comment-only `{ … }` group. Returns (nodes, true, nil) when one
// is consumed — a braced group yields one CommentAttr per interior comment, so
// the slice is non-empty whenever ok is true — (nil, false, nil) when the
// cursor is not at a comment, or (nil, false, err) for an unterminated block
// comment. Trailing is left false on the first node; the caller sets it from
// the preceding whitespace. Later nodes' Trailing comes from the group's own
// interior layout (same source line as the part before = trailing).
func (p *parser) parseTagComment() ([]*ast.CommentAttr, bool, error) {
	start := p.i
	if p.at("/*") {
		p.i += 2 // past '/*'
		for !p.eof() {
			if p.at("*/") {
				text := strings.TrimSpace(p.src[start+2 : p.i])
				p.i += 2 // past '*/'
				n := &ast.CommentAttr{Text: text, Block: true}
				ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
				return []*ast.CommentAttr{n}, true, nil
			}
			p.i++
		}
		return nil, false, p.errorf(p.posAt(start), "unterminated block comment")
	}
	if p.at("//") {
		p.i += 2 // past '//'
		for !p.eof() && p.src[p.i] != '\n' {
			p.i++
		}
		text := strings.TrimSpace(p.src[start+2 : p.i])
		// leave '\n' in place so skipSpace() sees it
		n := &ast.CommentAttr{Text: text, Block: false}
		ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
		return []*ast.CommentAttr{n}, true, nil
	}
	if p.peek() == '{' {
		end, ok := goExprEnd(p.src, p.i)
		if !ok {
			return nil, false, nil
		}
		parts, ok := commentParts(p.src[p.i+1 : end])
		if !ok {
			return nil, false, nil
		}
		innerBase := p.i + 1
		groupStart, groupEnd := p.posAt(start), p.posAt(end+1)
		p.i = end + 1 // past '}'
		if len(parts) == 0 {
			// Empty group ({} / { } / { ; }): a single empty block comment —
			// /**/ is the one bare spelling with nothing in it.
			n := &ast.CommentAttr{Text: "", Block: true}
			ast.SetSpan(n, groupStart, groupEnd)
			return []*ast.CommentAttr{n}, true, nil
		}
		nodes := make([]*ast.CommentAttr, len(parts))
		for i, part := range parts {
			n := &ast.CommentAttr{
				Text:     part.text,
				Block:    part.block,
				Trailing: i > 0 && part.line == parts[i-1].line,
			}
			// The braces belong to the group, not to any one comment: the
			// first span starts at `{` and the last ends past `}`, so the
			// nodes together still cover the group's full source extent.
			s, e := p.posAt(innerBase+part.off), p.posAt(innerBase+part.end)
			if i == 0 {
				s = groupStart
			}
			if i == len(parts)-1 {
				e = groupEnd
			}
			ast.SetSpan(n, s, e)
			nodes[i] = n
		}
		return nodes, true, nil
	}
	return nil, false, nil
}

// commentPart is one comment inside a comment-only `{ … }` group, located
// relative to the group's interior.
type commentPart struct {
	text  string
	block bool // true = /* */, false = //
	off   int  // byte offset of the comment token within the interior
	end   int  // byte offset just past the comment token
	line  int  // 1-based line within the interior
}

// commentParts scans the interior of a braced group and returns its comments
// in source order. ok=false when the interior holds any real Go token — then
// the brace is not a comment group but an expression (or an error) for other
// parse paths. Semicolons, explicit or auto-inserted, are lexical noise (as
// they always were here) and contribute no part; a group with no parts at all
// ({}, { }, { ; }) is still a comment group, just an empty one.
func commentParts(src string) (parts []commentPart, ok bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)
	for {
		pos, tok, lit := s.Scan()
		switch tok {
		case token.EOF:
			return parts, true
		case token.COMMENT:
			off := file.Offset(pos)
			part := commentPart{
				block: strings.HasPrefix(lit, "/*"),
				off:   off,
				line:  file.Line(pos),
			}
			// The token's end is measured in the SOURCE, not via len(lit):
			// go/scanner strips '\r' from comment literals, so a CRLF-bearing
			// comment's lit under-counts the source extent by one byte per CR.
			if part.block {
				part.text = strings.TrimSpace(lit[2 : len(lit)-2])
				part.end = off + strings.Index(src[off:], "*/") + len("*/")
			} else {
				part.text = strings.TrimSpace(lit[2:])
				part.end = off + len(src[off:])
				if nl := strings.IndexByte(src[off:], '\n'); nl >= 0 {
					part.end = off + nl
				}
				for part.end > off && src[part.end-1] == '\r' {
					part.end--
				}
			}
			parts = append(parts, part)
		case token.SEMICOLON:
			// allowed — explicit or auto-inserted semicolons are fine
		default:
			return nil, false
		}
	}
}

// parseBracedComment builds *ast.Comment nodes when the `{…}` at the current
// cursor is comment-only, advancing past the closing `}` — one node per
// interior comment (the slice is non-empty whenever ok is true; an empty group
// is a single empty block comment, canonical output `{}`). Otherwise it
// returns (nil, false, nil) without moving the cursor. Unterminated `{` is not
// an error here — parseInterp handles that.
func (p *parser) parseBracedComment() ([]*ast.Comment, bool, error) {
	if p.peek() != '{' {
		return nil, false, nil
	}
	start := p.i
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, false, nil
	}
	parts, ok := commentParts(p.src[p.i+1 : end])
	if !ok {
		return nil, false, nil
	}
	innerBase := p.i + 1
	groupStart, groupEnd := p.posAt(start), p.posAt(end+1)
	p.i = end + 1
	if len(parts) == 0 {
		n := &ast.Comment{Text: "", Block: true}
		ast.SetSpan(n, groupStart, groupEnd)
		return []*ast.Comment{n}, true, nil
	}
	nodes := make([]*ast.Comment, len(parts))
	for i, part := range parts {
		n := &ast.Comment{Text: part.text, Block: part.block}
		// Brace ownership as in parseTagComment: first span opens at `{`,
		// last closes past `}`.
		s, e := p.posAt(innerBase+part.off), p.posAt(innerBase+part.end)
		if i == 0 {
			s = groupStart
		}
		if i == len(parts)-1 {
			e = groupEnd
		}
		ast.SetSpan(n, s, e)
		nodes[i] = n
	}
	return nodes, true, nil
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
	rawCode := p.src[p.i+2 : innerEnd]
	lead := len(rawCode) - len(strings.TrimLeft(rawCode, " \t\r\n"))
	codePos := p.posAt(p.i + 2 + lead)
	code := strings.TrimSpace(rawCode)
	// W1: same parse-time pre-check as parseInterp — a GoBlock has no
	// top-level pipe grammar of its own (code is plain Go statement text, so
	// there is no parsePipe stage-stripping step to exclude first), so this
	// fires on ANY literal directly followed by `|>` in code, nested or not.
	// Same containsEmbeddedLiteralPrefix gate as parseInterp: an ordinary Go
	// string in the block must not pay for a go/scanner tokenization.
	if containsEmbeddedLiteralPrefix(code) {
		reportWholeLiteralPipes(p, scanGoParts(code), codePos)
	}
	p.i = outerEnd + 1
	n := &ast.GoBlock{Code: code, CodePos: codePos}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// atWord reports whether the source at the cursor is exactly the word w,
// not followed by a Go identifier rune (so `else` matches but `elsewhere` and
// `elseπ` do not).
func (p *parser) atWord(w string) bool {
	return atWordAt(p.src, p.i, w)
}

// atWordAt is atWord's free-function counterpart for scanning at an arbitrary
// offset rather than the parser's own cursor (used by the case-body label
// lookahead in caseBodyLabelStart, which probes ahead of p.i).
func atWordAt(src string, i int, w string) bool {
	if !strings.HasPrefix(src[i:], w) {
		return false
	}
	return !goIdentifierContinueAt(src, i+len(w))
}

// braceKeyword returns the leading control-flow keyword ("if", "for", "switch")
// inside the `{ … }` at the cursor (which must be at '{'), or "" if the first
// token is not one of those keywords. It does not move the cursor.
func (p *parser) braceKeyword() string {
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	for _, kw := range [...]string{"if", "for", "switch"} {
		if strings.HasPrefix(p.src[j:], kw) && !goIdentifierContinueAt(p.src, j+len(kw)) {
			return kw
		}
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
	return p.parseMarkupUntilCloseWS(what, false)
}

// parseMarkupUntilCloseWS is parseMarkupUntilClose with control over inter-node
// whitespace. When preserveWS is false (markup-attribute slots) leading
// whitespace before each node is skipped, as before. When true (control-flow
// bodies) whitespace falls into parseTextCtx and becomes a text node for wsnorm;
// parseControlBody then trims the brace-interior edges via trimBodyEdges.
func (p *parser) parseMarkupUntilCloseWS(what string, preserveWS bool) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		if !preserveWS {
			p.skipSpace()
		}
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated %s, expected `}`", what)
		}
		switch {
		case p.peek() == '}':
			p.i++ // consume the closing brace
			return nodes, nil
		case p.peek() == '<':
			leadingBreak := newlineBefore(p.src, p.i)
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			stampLeadingBreak(el, leadingBreak)
			nodes = append(nodes, el)
		case p.peek() == '{':
			leadingBreak := newlineBefore(p.src, p.i)
			bnodes, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			stampLeadingBreak(bnodes[0], leadingBreak)
			nodes = append(nodes, bnodes...)
		case p.atBareContentComment():
			nodes = append(nodes, p.parseBareComment())
		default:
			nodes = append(nodes, p.parseTextCtx(true))
		}
	}
}

// parseControlBody parses a control-flow body: markup until the matching '}'.
// The cursor must be just past the opening '{'. Interior whitespace is preserved
// for wsnorm; whitespace immediately inside the braces is trimmed.
func (p *parser) parseControlBody() ([]ast.Markup, error) {
	nodes, err := p.parseMarkupUntilCloseWS("control-flow body", true)
	if err != nil {
		return nil, err
	}
	return trimBodyEdges(nodes), nil
}

// trimBodyEdges strips whitespace immediately inside the control-flow body
// braces: the leading whitespace of the first node and the trailing whitespace
// of the last node, when those nodes are Text. This mirrors how gsx trims the
// interior of `{ expr }` and `{{ code }}`. Interior whitespace between nodes is
// left for wsnorm's JSX rule. An emptied edge Text node is dropped.
func trimBodyEdges(nodes []ast.Markup) []ast.Markup {
	if len(nodes) > 0 {
		if t, ok := nodes[0].(*ast.Text); ok {
			t.Value = strings.TrimLeft(t.Value, " \t\r\n")
			if t.Value == "" {
				nodes = nodes[1:]
			}
		}
	}
	if len(nodes) > 0 {
		if t, ok := nodes[len(nodes)-1].(*ast.Text); ok {
			t.Value = strings.TrimRight(t.Value, " \t\r\n")
			if t.Value == "" {
				nodes = nodes[:len(nodes)-1]
			}
		}
	}
	// A body that trims down to nothing must be represented identically to a
	// literally-empty `{}` body (nil), not a non-nil empty slice left over from
	// reslicing — otherwise the two are only render-equivalent, not
	// structurally equal, and AST-faithfulness comparisons (e.g. fmt idempotence)
	// see a spurious diff.
	if len(nodes) == 0 {
		return nil
	}
	return nodes
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
	rawClause := p.src[clauseStart:braceOff]
	lead := len(rawClause) - len(strings.TrimLeft(rawClause, " \t\r\n"))
	clausePos := p.posAt(clauseStart + lead)
	clause := strings.TrimSpace(rawClause)
	p.i = braceOff + 1 // past body '{'
	bodyMultiline := newlineFollows(p.src, p.i)
	body, err := p.parseControlBody()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		return nil, p.errorf(p.pos(), "expected `}` to close `{ for … }`")
	}
	p.i++ // past outer '}'
	n := &ast.ForMarkup{Clause: clause, ClausePos: clausePos, Body: body, BodyMultiline: bodyMultiline}
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
	rawCond := p.src[condStart:braceOff]
	lead := len(rawCond) - len(strings.TrimLeft(rawCond, " \t\r\n"))
	condPos := p.posAt(condStart + lead)
	cond := strings.TrimSpace(rawCond)
	p.i = braceOff + 1 // past body '{'
	thenMultiline := newlineFollows(p.src, p.i)
	body, err := p.parseControlBody()
	if err != nil {
		return nil, err
	}
	n := &ast.IfMarkup{Cond: cond, CondPos: condPos, Then: body, ThenMultiline: thenMultiline}
	p.skipSpace()
	if p.atWord("else") {
		p.i += len("else")
		p.skipSpace()
		switch {
		case p.peek() == '{':
			p.i++ // past '{'
			n.ElseMultiline = newlineFollows(p.src, p.i)
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
	tagPos := token.NoPos
	if tag != "" {
		tagPos = p.posAt(tagStart + leadingSpaceLen(p.src[tagStart:braceOff]))
	}
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
	n := &ast.SwitchMarkup{Tag: tag, TagPos: tagPos, Cases: cases}
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
		if cc.List != "" {
			cc.ListPos = p.posAt(listStart + leadingSpaceLen(p.src[listStart:colonOff]))
		}
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
	// BodyMultiline records that the source placed a line break immediately
	// after the colon, mirroring how IfMarkup/ForMarkup record it after their
	// body's opening `{` (newlineFollows).
	cc.BodyMultiline = newlineFollows(p.src, p.i)
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
		// Look past whitespace to detect a terminator without destroying interior
		// whitespace: if the next token is a terminator, the skipped whitespace was
		// the case body's trailing edge (trimmed by trimBodyEdges); otherwise
		// restore so the whitespace becomes part of the following text node.
		save := p.i
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unterminated `case` body")
		}
		if p.peek() == '}' || p.atWord("case") || p.atWord("default") {
			return trimBodyEdges(nodes), nil
		}
		p.i = save
		switch {
		case p.peek() == '<':
			leadingBreak := newlineBefore(p.src, p.i)
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			stampLeadingBreak(el, leadingBreak)
			nodes = append(nodes, el)
		case p.peek() == '{':
			leadingBreak := newlineBefore(p.src, p.i)
			bnodes, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			stampLeadingBreak(bnodes[0], leadingBreak)
			nodes = append(nodes, bnodes...)
		case p.atBareContentComment():
			nodes = append(nodes, p.parseBareComment())
		default:
			nodes = append(nodes, p.parseCaseBodyText())
		}
	}
}

// parseCaseBodyText consumes literal text within a `case`/`default` arm body.
// It is a case-body-specific variant of parseTextCtx(true): besides the same
// '<'/'{'/'}' terminators, it also stops at a line-start valid arm label (see
// caseBodyTextEnd) — the fix for the defect where a text-ending arm swallowed
// the next case/default label. The check is kept out of parseTextCtx itself
// (the common text scanner run over every text node in the file, including
// element children and if/for bodies, which have no case/default terminators
// of their own) so that hot path pays nothing for it.
func (p *parser) parseCaseBodyText() *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	p.i = p.caseBodyTextEnd(start)
	n := &ast.Text{Value: p.src[start:p.i]}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n
}

// caseBodyTextEnd returns the byte offset in src where a case-body text run
// starting at `start` must end: '<', '{', '}' (mirroring parseTextCtx(true)),
// or the start of a trailing-whitespace run that leads to a line-start valid
// arm label. In the label case the stop point is BEFORE that whitespace, not
// at the label itself: parseCaseBody's top-of-loop skipSpace+terminator check
// then consumes it exactly as it already does at a node boundary, so the
// label is recognized identically whether it follows text or an element.
func (p *parser) caseBodyTextEnd(start int) int {
	src := p.src
	i := start
	for i < len(src) {
		switch src[i] {
		case '<', '{', '}':
			return i
		case '/':
			// A line-start `//` content comment ends the run exactly as it does
			// in parseTextCtx: the run stops AT the slashes (their leading
			// whitespace stays in this text node) so parseCaseBody's next
			// iteration dispatches it as a Comment. Without this, an arm's text
			// swallowed the comment — the same defect class as the arm label.
			if p.atBareContentCommentAt(i) {
				return i
			}
		case ' ', '\t', '\r', '\n':
			if i == start || !isCaseBodySpace(src[i-1]) {
				if caseBodyLabelAfterWS(src, i) {
					return i
				}
			}
		}
		i++
	}
	return i
}

func isCaseBodySpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n':
		return true
	}
	return false
}

// caseBodyLabelAfterWS reports whether the whitespace run in src starting at
// off (src[off] is itself whitespace) crosses at least one line break and,
// once fully skipped, reaches a valid case/default arm label — i.e. the label
// begins its own physical line (the design's line-start rule). A whitespace
// run that never crosses a newline is same-line spacing and never a label
// boundary, regardless of what follows it.
func caseBodyLabelAfterWS(src string, off int) bool {
	sawNewline := false
	j := off
	for j < len(src) {
		switch src[j] {
		case '\n', '\r':
			sawNewline = true
			j++
			continue
		case ' ', '\t':
			j++
			continue
		}
		break
	}
	return sawNewline && j < len(src) && caseBodyLabelStart(src, j)
}

// caseBodyLabelStart reports whether src at offset i begins a valid switch
// arm label per parseCaseClause's own grammar: `default`, optionally followed
// by whitespace, then `:`; or `case` followed by a list terminated by a `:`
// found via scanToCaseColonBounded — the same string/rune-aware colon scan
// parseCaseClause uses (scanToCaseColon), but bounded to the keyword's own
// physical line since this call is SPECULATIVE (see scanToCaseColonBounded):
// unlike parseCaseClause's committed scan, this one runs for every "case"-
// leading word candidate in ordinary case-body prose, so it must not pay for
// scanning to EOF. `case "a:b":` is still recognized correctly since the
// colon is on the same line. It does not check line position; callers
// (caseBodyLabelAfterWS) gate that separately.
func caseBodyLabelStart(src string, i int) bool {
	if atWordAt(src, i, "default") {
		j := i + len("default")
		for j < len(src) && isCaseBodySpace(src[j]) {
			j++
		}
		return j < len(src) && src[j] == ':'
	}
	if atWordAt(src, i, "case") {
		_, ok := scanToCaseColonBounded(src, i+len("case"))
		return ok
	}
	return false
}

// parseBraceNode dispatches a `{`-leading construct in a child/markup context.
// Cursor must be at '{'. It returns (nodes, nil) — a single node for a
// GoBlock, control flow, or interpolation; one node per interior comment for
// a comment-only `{ … }` group — or (nil, err) on error. The slice is never
// empty on success.
func (p *parser) parseBraceNode() ([]ast.Markup, error) {
	if p.at("{{") {
		gb, err := p.parseGoBlock()
		if err != nil {
			return nil, err
		}
		return []ast.Markup{gb}, nil
	}
	if cs, ok, err := p.parseBracedComment(); err != nil {
		return nil, err
	} else if ok {
		nodes := make([]ast.Markup, len(cs))
		for i, c := range cs {
			nodes[i] = c
		}
		return nodes, nil
	}
	switch p.braceKeyword() {
	case "if":
		n, err := p.parseIfMarkup()
		if err != nil {
			return nil, err
		}
		return []ast.Markup{n}, nil
	case "for":
		n, err := p.parseForMarkup()
		if err != nil {
			return nil, err
		}
		return []ast.Markup{n}, nil
	case "switch":
		n, err := p.parseSwitchMarkup()
		if err != nil {
			return nil, err
		}
		return []ast.Markup{n}, nil
	}
	if in, ok, err := p.tryParseBodyEmbeddedInterp(); err != nil {
		return nil, err
	} else if ok {
		return []ast.Markup{in}, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	return []ast.Markup{in}, nil
}

// tryParseBodyEmbeddedInterp recognizes a lone f`…` literal — optionally
// followed by a whole-literal `|>` pipeline — as the *entire* value of a body
// `{ }`: {f`…@{expr}…`} or {f`…` |> f}. Cursor must be at the opening `{`. A
// bare (unprefixed) backtick is a plain Go raw string, not an interpolating
// literal, so it is left to parseInterp.
//
// It returns (nil, false, nil) with the cursor (and any diagnostics recorded
// by an abandoned trial) rewound to its entry state whenever the `{ }`
// doesn't turn out to be this shape — no leading backtick, a `js`/`css`
// literal, trailing content after the literal that isn't a `|>` pipeline
// (e.g. `{ `a` + b }`), or ANY parse failure along the way (the literal itself
// fails to close, e.g. a Go raw string ending in `\` that gsx's
// backtick-escape convention misreads as an escape; or the trailing pipe-stage
// region is malformed). This function only ever *commits* to EmbeddedInterp
// once it has cleanly matched the whole shape; on any other outcome, including
// an error, it defers to the caller's parseInterp so the content is read as an
// ordinary Go expression. That does mean a lone literal that really is
// malformed (e.g. `{`oops`, truly unterminated) surfaces as a Go-expression
// parse error instead of an embedded-literal one — an acceptable trade for not
// having to distinguish "meant to be embedded" from "meant to be Go".
func (p *parser) tryParseBodyEmbeddedInterp() (*ast.EmbeddedInterp, bool, error) {
	start := p.i // at '{'
	startPos := p.posAt(start)
	// errMark snapshots p.errs so a failed trial can be fully undone: p.errorf
	// (and p.pipeErrorf) record a diagnostic into p.errs as a side effect
	// regardless of whether the caller propagates the returned error, so
	// merely discarding the error value here is not enough — rewind must also
	// truncate p.errs back to this mark or the abandoned trial's diagnostic
	// would still surface from ParseFile.
	errMark := len(p.errs)
	rewind := func() {
		p.i = start
		p.errs = p.errs[:errMark]
	}
	p.i++ // past '{'
	p.skipSpace()
	if !p.at("f`") && !p.at(`f"`) {
		// Only an f`…` / f"…" literal interpolates in body position. A bare
		// backtick or bare `"` is a plain Go string (interpolation is opt-in
		// behind the f prefix); js`/css` embedded literals aren't valid in body
		// position; and anything else isn't a lone literal at all. Rewind and let
		// parseInterp scan (and, where relevant, error on) the whole `{ }` as an
		// ordinary Go expression.
		rewind()
		return nil, false, nil
	}
	// parseEmbeddedAttrLiteral consumes the literal INCLUDING any gsx
	// backslash-backtick escapes and leaves the cursor right after the closing
	// backtick. Only the
	// region AFTER the literal (pipe stages, or nothing but `}`) is bounded by
	// a Go-aware scan below — that region can't contain a gsx backtick escape,
	// so goStagesEnd is safe there even though goExprEnd is not safe over the
	// literal itself.
	lang, dquoted, segs, err := p.parseEmbeddedAttrLiteral()
	if err != nil {
		// The literal didn't close cleanly (e.g. a Go raw string ending in `\`
		// that the gsx backtick-escape convention swallows as an escape). This
		// isn't a lone embedded literal after all — rewind and let parseInterp
		// read the `{ }` as an ordinary Go expression.
		rewind()
		return nil, false, nil
	}
	if lang != ast.EmbeddedText {
		rewind()
		return nil, false, nil
	}
	p.skipSpace()
	afterLiteral := p.i
	if !p.eof() && p.src[p.i] == '}' {
		p.i++ // past '}'
		node := &ast.EmbeddedInterp{Lang: ast.EmbeddedText, Segments: segs, DoubleQuoted: dquoted}
		ast.SetSpan(node, startPos, p.posAt(p.i))
		return node, true, nil
	}
	if !p.at("|>") {
		// Anything else (e.g. `+ b`) means the backtick was only part of a
		// larger Go expression, so this isn't a lone literal.
		rewind()
		return nil, false, nil
	}
	end, ok := goStagesEnd(p.src, afterLiteral)
	if !ok {
		// The `|>` tail never closes — not a valid embedded-literal pipeline.
		// Rewind rather than error; the Go-expression fallback will surface its
		// own (differently worded) parse error if the source really is broken.
		rewind()
		return nil, false, nil
	}
	slice := p.src[afterLiteral:end]
	stages, perr := parseTrailingStages(slice, p.posAt(afterLiteral))
	if perr != nil {
		rewind()
		return nil, false, nil
	}
	p.i = end + 1 // past '}'
	node := &ast.EmbeddedInterp{Lang: ast.EmbeddedText, Segments: segs, Stages: stages, DoubleQuoted: dquoted}
	ast.SetSpan(node, startPos, p.posAt(p.i))
	return node, true, nil
}

// parseAttrsUntil consumes an element's attribute list up to (but not past) the
// point where stop reports true. multiline reports whether the author placed a
// line break inside the opening tag's inter-token whitespace — between the tag
// name and an attribute, between two attributes, or before the closing
// delimiter — so the formatter can preserve that vertical layout. A newline
// inside an attribute's value is consumed by parseSingleAttr, never by these
// whitespace skips, so it does not set the flag; and with zero attributes there
// is no list to break, so the flag stays false even for `<div\n>`.
func (p *parser) parseAttrsUntil(stop func() bool) (attrs []ast.Attr, multiline bool, err error) {
	sawNewline := false
	for {
		wsStart := p.i
		p.skipSpace()
		if strings.ContainsAny(p.src[wsStart:p.i], "\n\r") {
			sawNewline = true
		}
		if p.eof() {
			return nil, false, p.errorf(p.pos(), "unexpected EOF in attributes")
		}
		if stop() {
			return attrs, sawNewline && len(attrs) > 0, nil
		}
		if c, ok, cerr := p.parseTagComment(); cerr != nil {
			return nil, false, cerr
		} else if ok {
			c[0].Trailing = len(attrs) > 0 && !strings.ContainsRune(p.src[wsStart:p.i], '\n')
			for _, n := range c {
				attrs = append(attrs, n)
			}
			continue
		}
		a, aerr := p.parseSingleAttr()
		if aerr != nil {
			return nil, false, aerr
		}
		attrs = append(attrs, a)
	}
}

// parseAttrs consumes an element's attribute list up to (but not past) the
// closing `>` or `/>`.
func (p *parser) parseAttrs() (attrs []ast.Attr, multiline bool, err error) {
	return p.parseAttrsUntil(func() bool { return p.peek() == '>' || p.at("/>") })
}

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	// Babel rule: first non-space inside the braces starting markup?
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	if j < len(p.src) && p.src[j] == '<' && startsTagAt(p.src, j+1) {
		p.i++ // past '{'
		// A `name={ … }` markup slot is a fresh non-preserve context: an
		// enclosing pre/textarea's verbatim treatment does not leak across the
		// expression boundary into the slot's own markup. This mirrors
		// wsnorm's normalizeAttrs, which normalizes every MarkupAttr.Value
		// with preserve=false regardless of the element's own preserve state
		// (internal/wsnorm/wsnorm.go) — the parser and wsnorm must agree on
		// where "verbatim" ends or a `//` in a slot renders as neither a
		// recognized comment nor a diagnosed error.
		savedPreserveDepth := p.preserveDepth
		p.preserveDepth = 0
		nodes, err := p.parseMarkupUntilClose("markup attribute")
		p.preserveDepth = savedPreserveDepth
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
	ea := &ast.ExprAttr{Name: name, Expr: in.Expr, ExprPos: in.ExprPos, Stages: in.Stages}
	ast.SetSpan(ea, attrStartPos, in.End())
	return ea, nil
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

	// `<?…`: a processing instruction (fixed marker/start/end vocabulary).
	if p.peek() == '?' {
		return p.parsePI(startPos)
	}

	// Fragment: <>…</>
	if p.peek() == '>' {
		p.i++ // past '>'
		childrenMultiline := newlineFollows(p.src, p.i)
		children, _, err := p.parseChildren("")
		if err != nil {
			return nil, err
		}
		fr := &ast.Fragment{Children: children, ChildrenMultiline: childrenMultiline}
		ast.SetSpan(fr, startPos, p.posAt(p.i))
		return fr, nil
	}

	tagStart := p.i
	p.i = scanTagName(p.src, p.i)
	tag := p.src[tagStart:p.i]
	if tag == "" {
		return nil, p.errorf(startPos, "expected tag name")
	}
	tagPos := p.posAt(tagStart)
	var typeArgs string
	var typeArgsOpenPos token.Pos
	var typeArgsPos token.Pos
	var typeArgsClosePos token.Pos
	if p.peek() == '[' {
		typeArgsOpenPos = p.pos()
		end, ok := bracketEnd(p.src, p.i)
		if !ok {
			return nil, p.errorf(p.pos(), "unterminated type args")
		}
		typeArgsClosePos = p.posAt(end)
		raw := p.src[p.i+1 : end]
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		typeArgsPos = p.posAt(p.i + 1 + lead)
		typeArgs = strings.TrimSpace(raw)
		if typeArgs == "" {
			return nil, p.errorf(p.pos(), "empty type argument list in <%s[]>", tag)
		}
		p.i = end + 1
	}

	attrs, attrsMultiline, err := p.parseAttrs()
	if err != nil {
		return nil, err
	}

	if p.at("/>") {
		p.i += 2
		el := &ast.Element{Tag: tag, TagPos: tagPos, TypeArgs: typeArgs, TypeArgsOpenPos: typeArgsOpenPos, TypeArgsPos: typeArgsPos, TypeArgsClosePos: typeArgsClosePos, Void: true, Attrs: attrs, AttrsMultiline: attrsMultiline}
		ast.SetSpan(el, startPos, p.posAt(p.i))
		return el, nil
	}
	if p.peek() != '>' {
		return nil, p.errorf(p.pos(), "expected '>' or '/>' in <%s>", tag)
	}
	p.i++ // past '>'
	childrenMultiline := newlineFollows(p.src, p.i)

	// Raw-text elements (<script>, <style>): content is verbatim until the
	// matching case-insensitive close tag. No markup/interpolation inside.
	if isRawTextTag(tag) {
		children, err := p.parseRawTextBody(tag, startPos)
		if err != nil {
			return nil, err
		}
		el := &ast.Element{Tag: tag, TagPos: tagPos, TypeArgs: typeArgs, TypeArgsOpenPos: typeArgsOpenPos, TypeArgsPos: typeArgsPos, TypeArgsClosePos: typeArgsClosePos, Attrs: attrs, Children: children, AttrsMultiline: attrsMultiline}
		ast.SetSpan(el, startPos, p.posAt(p.i))
		return el, nil
	}

	if wsnorm.IsPreserveTag(tag) {
		p.preserveDepth++
	}
	children, closeNamePos, err := p.parseChildren(tag)
	if wsnorm.IsPreserveTag(tag) {
		p.preserveDepth--
	}
	if err != nil {
		return nil, err
	}
	el := &ast.Element{Tag: tag, TagPos: tagPos, TypeArgs: typeArgs, TypeArgsOpenPos: typeArgsOpenPos, TypeArgsPos: typeArgsPos, TypeArgsClosePos: typeArgsClosePos, Attrs: attrs, Children: children, CloseNamePos: closeNamePos, ChildrenMultiline: childrenMultiline, AttrsMultiline: attrsMultiline}
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

// Processing-instruction targets gsx accepts. The HTML tokenizer allows any
// target matching [A-Za-z_][A-Za-z0-9_-]* (whatwg/html#12118), but only these
// three carry defined semantics (declarative partial updates), so gsx rejects
// everything else rather than emit markup whose meaning it cannot describe.
const (
	piMarker = "marker"
	piStart  = "start"
	piEnd    = "end"
)

// parsePI parses a processing instruction. The cursor is at the '?' following
// '<'; startPos describes the opening '<'. `<?end>` is only meaningful as a
// MarkerRegion terminator, so it is an error here — parseChildrenTerm consumes
// the legitimate ones.
func (p *parser) parsePI(startPos token.Pos) (ast.Markup, error) {
	p.i++ // past '?'
	targetStart := p.i
	p.i = scanTagName(p.src, p.i)
	target := p.src[targetStart:p.i]
	switch target {
	case piMarker:
		name, err := p.parsePIName(target, startPos)
		if err != nil {
			return nil, err
		}
		m := &ast.Marker{Name: name}
		ast.SetSpan(m, startPos, p.posAt(p.i))
		return m, nil
	case piStart:
		name, err := p.parsePIName(target, startPos)
		if err != nil {
			return nil, err
		}
		childrenMultiline := newlineFollows(p.src, p.i)
		children, _, err := p.parseChildrenTerm(childTerm{piEnd: true})
		if err != nil {
			return nil, err
		}
		r := &ast.MarkerRegion{Name: name, Children: children, ChildrenMultiline: childrenMultiline}
		ast.SetSpan(r, startPos, p.posAt(p.i))
		return r, nil
	case piEnd:
		return nil, p.errorf(startPos, "`<?end>` without a matching `<?start`")
	}
	return nil, p.errorf(startPos, "unknown processing-instruction target %q, expected `marker`, `start`, or `end`", target)
}

// atPITarget reports whether the cursor is at `<?` followed by exactly target
// (so `<?end>` matches but `<?ending>` does not).
func (p *parser) atPITarget(target string) bool {
	if !p.at("<?") {
		return false
	}
	end := scanTagName(p.src, p.i+len("<?"))
	return p.src[p.i+len("<?"):end] == target
}

// parsePIName parses the required `name=…` of a marker/start PI and consumes the
// closing '>'. It returns the name attribute, which is a *ast.StaticAttr or
// *ast.ExprAttr.
func (p *parser) parsePIName(target string, startPos token.Pos) (ast.Attr, error) {
	attrs, _, err := p.parseAttrsUntil(func() bool { return p.peek() == '>' || p.at("?>") })
	if err != nil {
		return nil, err
	}
	if p.at("?>") {
		return nil, p.errorf(p.pos(), "`?>` does not close a gsx processing instruction; use `>`")
	}
	if len(attrs) == 0 {
		return nil, p.errorf(startPos, "`<?%s` requires a `name` attribute", target)
	}
	if len(attrs) > 1 {
		return nil, p.errorf(startPos, "`<?%s` takes only a `name` attribute", target)
	}
	name := attrs[0]
	switch a := name.(type) {
	case *ast.StaticAttr:
		if a.Name != "name" {
			return nil, p.errorf(a.Pos(), "`<?%s` requires a `name` attribute, got %q", target, a.Name)
		}
	case *ast.ExprAttr:
		if a.Name != "name" {
			return nil, p.errorf(a.Pos(), "`<?%s` requires a `name` attribute, got %q", target, a.Name)
		}
	default:
		return nil, p.errorf(name.Pos(), "`<?%s` requires a `name=\"…\"` or `name={…}` attribute", target)
	}
	p.i++ // past '>'
	return name, nil
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
			if !tagNameRuneAt(p.src, after) {
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

// stampLeadingBreak sets n's LeadingBreak field to lb when n is one of the node
// kinds that carry the fact (*ast.Element, *ast.Interp, *ast.EmbeddedInterp —
// the formatter's inline-fill leaves); a no-op for any other Markup (e.g. the
// always-block-level *ast.Fragment/*ast.IfMarkup/*ast.ForMarkup/*ast.SwitchMarkup
// and the *ast.Marker/*ast.MarkerRegion processing instructions, which never sit
// at a fill's safe-gap joint).
func stampLeadingBreak(n ast.Markup, lb bool) {
	switch v := n.(type) {
	case *ast.Element:
		v.LeadingBreak = lb
	case *ast.Interp:
		v.LeadingBreak = lb
	case *ast.EmbeddedInterp:
		v.LeadingBreak = lb
	}
}

// childTerm describes how a child list ends: a `</tag>` close tag (tag is "" for
// a fragment's `</>`), or a `<?end>` processing instruction closing a
// MarkerRegion. Exactly one form applies per list.
type childTerm struct {
	tag   string
	piEnd bool
}

// parseChildrenTerm parses markup children up to the matching terminator
// described by term (which it consumes). It returns the children, the position
// of the first char of the name in the closing tag (token.NoPos when term.tag
// is empty — a `</>` fragment — or on error), and any error.
func (p *parser) parseChildrenTerm(term childTerm) ([]ast.Markup, token.Pos, error) {
	var nodes []ast.Markup
	for {
		if p.eof() {
			if term.piEnd {
				return nil, token.NoPos, p.errorf(token.NoPos, "unexpected EOF, expected <?end>")
			}
			return nil, token.NoPos, p.errorf(token.NoPos, "unexpected EOF, expected </%s>", term.tag)
		}
		if term.piEnd && p.atPITarget(piEnd) {
			endPos := p.pos()
			p.i += len("<?") + len(piEnd)
			attrs, _, err := p.parseAttrsUntil(func() bool { return p.peek() == '>' || p.at("?>") })
			if err != nil {
				return nil, token.NoPos, err
			}
			if p.at("?>") {
				return nil, token.NoPos, p.errorf(p.pos(), "`?>` does not close a gsx processing instruction; use `>`")
			}
			if len(attrs) > 0 {
				return nil, token.NoPos, p.errorf(endPos, "`<?end>` takes no attributes")
			}
			p.i++ // past '>'
			return nodes, token.NoPos, nil
		}
		if p.at("</") {
			mmTokPos := p.pos()
			// consume close tag
			p.i += 2
			start := p.i
			p.i = scanTagName(p.src, p.i)
			got := p.src[start:p.i]
			closeNamePos := p.posAt(start)
			p.skipSpace()
			if p.peek() != '>' {
				return nil, token.NoPos, p.errorf(p.pos(), "malformed close tag")
			}
			p.i++ // past '>'
			if term.piEnd {
				// A region is terminated ONLY by `<?end>`. term.tag is "" for a
				// region, so the `got != term.tag` comparison below would let a
				// fragment close `</>` silently end the region (and would then
				// mis-name `</>` as the expected terminator for any other close
				// tag). Reject every `</…>` here instead, naming `<?end>`.
				return nil, token.NoPos, p.errorf(mmTokPos, "mismatched close tag </%s>, expected <?end>", got)
			}
			if got != term.tag {
				return nil, token.NoPos, p.errorf(mmTokPos, "mismatched close tag </%s>, expected </%s>", got, term.tag)
			}
			if term.tag == "" {
				closeNamePos = token.NoPos // `</>` fragment — no name
			}
			return nodes, closeNamePos, nil
		}
		if p.peek() == '<' {
			leadingBreak := newlineBefore(p.src, p.i)
			el, err := p.parseElement()
			if err != nil {
				return nil, token.NoPos, err
			}
			stampLeadingBreak(el, leadingBreak)
			nodes = append(nodes, el)
			continue
		}
		if p.peek() == '{' {
			leadingBreak := newlineBefore(p.src, p.i)
			bnodes, err := p.parseBraceNode()
			if err != nil {
				return nil, token.NoPos, err
			}
			stampLeadingBreak(bnodes[0], leadingBreak)
			nodes = append(nodes, bnodes...)
			continue
		}
		if p.atBareContentComment() {
			nodes = append(nodes, p.parseBareComment())
			continue
		}
		nodes = append(nodes, p.parseText())
	}
}

// parseChildren parses markup children up to the matching `</closeTag>` (which
// it consumes). It returns the children, the position of the first char of the
// name in the closing tag (token.NoPos when closeTag is empty — a `</>`
// fragment — or on error), and any error.
func (p *parser) parseChildren(closeTag string) ([]ast.Markup, token.Pos, error) {
	return p.parseChildrenTerm(childTerm{tag: closeTag})
}
