package parser

import (
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// splitComposed splits the inner source of a `class={ … }` / `style={ … }`
// value into contributions. src is the text between the outer `{` and `}`;
// base is the absolute byte offset of src[0] within p.src (used so value-form
// nodes get real positions). Contributions are separated by commas at
// bracket/brace/paren depth 0; within a contribution a depth-0 ':' separates an
// `expr : cond` conditional from its condition. A trailing comma yields no empty
// part.
func (p *parser) splitComposed(name, src string, base int) ([]ast.ClassPart, error) {
	commas, colons := composedDelims(src)

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

		// Detect a leading control-flow keyword (if/switch) and parse the value-form
		// instead of the normal expr:cond pattern.
		segSrc := src[segStart:segEnd]
		trimOff := len(segSrc) - len(strings.TrimLeft(segSrc, " \t\r\n"))
		if kw := leadingKeyword(segSrc); kw == "if" || kw == "switch" {
			// A depth-0 colon in a value-form segment is a disallowed guard.
			for _, c := range colons {
				if c > segStart && c < segEnd {
					return nil, p.errorf(p.posAt(base+c), "a value-form %s in class/style takes no `: cond` guard", kw)
				}
			}
			// Compute the absolute offset of the keyword's first byte (skip any
			// leading whitespace so the parsers' at+len("kw") arithmetic is correct).
			cf, err := p.parseValueCF(base+segStart+trimOff, kw)
			if err != nil {
				return nil, err
			}
			// Guard: assert nothing meaningful follows the value-form within this
			// segment. p.offsetOf converts the token.Pos back to a byte offset in
			// p.src; subtract base to get the offset within src.
			endOff := p.offsetOf(cf.End()) - base
			if rest := strings.TrimSpace(src[endOff:segEnd]); rest != "" {
				return nil, p.errorf(cf.End(), "unexpected %q after value-form %s in class/style; pipe stages on a value-form result are not supported", rest, kw)
			}
			parts = append(parts, ast.ClassPart{CF: cf})
			ast.SetSpan(&parts[len(parts)-1], p.posAt(base+segStart), p.posAt(base+segEnd))
			continue
		}

		colon := -1
		for _, c := range colons {
			if c > segStart && c < segEnd {
				colon = c
				break
			}
		}
		if strings.HasPrefix(segSrc[trimOff:], "css`") {
			if name != "style" {
				return nil, p.errorf(p.posAt(base+segStart+trimOff), "css literal parts are only valid in style={...}")
			}
			literalOff := base + segStart + trimOff
			old := p.i
			p.i = literalOff
			lang, segments, err := p.parseEmbeddedAttrLiteral()
			literalEnd := p.i
			p.i = old
			if err != nil {
				return nil, err
			}
			if lang != ast.EmbeddedCSS {
				return nil, p.errorf(p.posAt(literalOff), "expected css literal in style={...}")
			}
			partEnd := segEnd
			var condSrc string
			condPos := token.NoPos
			if colon >= 0 {
				partEnd = colon
				condSrc = strings.TrimSpace(src[colon+1 : segEnd])
				condOff := base + colon + 1 + leadingSpaceLen(src[colon+1:segEnd])
				if err := validateGoExpr(condSrc); err != nil {
					return nil, p.errorf(p.posAt(condOff), "invalid %s condition %q: %v", name, condSrc, err)
				}
				condPos = p.posAt(condOff)
			}
			if rest := strings.TrimSpace(src[literalEnd-base : partEnd]); rest != "" {
				return nil, p.errorf(p.posAt(literalEnd), "unexpected %q after css literal in style={...}", rest)
			}
			parts = append(parts, ast.ClassPart{CSSSegments: segments, Cond: condSrc, CondPos: condPos})
			ast.SetSpan(&parts[len(parts)-1], p.posAt(base+segStart), p.posAt(base+segEnd))
			continue
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
		exprPos := base + segStart + leadingSpaceLen(src[segStart:segEnd])
		seed, stages, perr := parsePipe(exprSrc, p.posAt(exprPos))
		if perr != nil {
			return nil, p.pipeErrorf(p.posAt(exprPos), perr)
		}
		if err := validateGoExpr(seed); err != nil {
			return nil, p.errorf(p.posAt(exprPos), "invalid %s expression %q: %v", name, seed, err)
		}
		condPos := token.NoPos
		if condSrc != "" {
			condOff := base + colon + 1 + leadingSpaceLen(src[colon+1:segEnd])
			if err := validateGoExpr(condSrc); err != nil {
				return nil, p.errorf(p.posAt(condOff), "invalid %s condition %q: %v", name, condSrc, err)
			}
			condPos = p.posAt(condOff)
		}
		parts = append(parts, ast.ClassPart{Expr: seed, ExprPos: p.posAt(exprPos), Cond: condSrc, CondPos: condPos, Stages: stages})
		ast.SetSpan(&parts[len(parts)-1], p.posAt(base+segStart), p.posAt(base+segEnd))
	}
	return parts, nil
}

func leadingSpaceLen(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t\r\n"))
}

func validateGoExpr(expr string) error {
	_, err := goparser.ParseExpr(expr)
	return err
}

// leadingKeyword returns "if" or "switch" if seg's first token is that keyword
// (followed by a non-identifier byte), else "".
func leadingKeyword(seg string) string {
	s := strings.TrimLeft(seg, " \t\r\n")
	for _, kw := range [...]string{"if", "switch"} {
		if strings.HasPrefix(s, kw) && (len(s) == len(kw) || !isIdentByte(s[len(kw)])) {
			return kw
		}
	}
	return ""
}

// parseComposedAttr parses a `class={ … }` / `style={ … }` composable
// contribution list. Cursor must be at the '{' of the value.
func (p *parser) parseComposedAttr(name string, startPos token.Pos) (ast.Attr, error) {
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, p.errorf(p.pos(), "unterminated `{` in %s value", name)
	}
	parts, err := p.splitComposed(name, p.src[p.i+1:end], p.i+1)
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
	coreOff := p.i + 1 + leadingSpaceLen(p.src[p.i+1:end])
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
	// ExprPos maps the seed back to its source bytes for LSP cursor bridging.
	// It survives a bare pipeline (the seed is a prefix of the source text) but
	// not the paren-unwrap below, which rewrites the seed away from the source.
	exprPos := p.posAt(coreOff)
	if len(stages) == 0 {
		if unwrapped, ok := balancedParenUnwrap(core); ok {
			if s2, st2, err := parsePipe(unwrapped, token.NoPos); err == nil && len(st2) > 0 {
				seed, stages = s2, st2
				exprPos = token.NoPos
			}
		}
	}
	p.i = end + 1
	sa := &ast.SpreadAttr{Expr: seed, ExprPos: exprPos, Stages: stages}
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

	// Dispatch on the value start. Each downstream parser assumes the cursor is
	// positioned exactly at its literal opener (`js`/`css`, `"`, or `{`).
	switch {
	case p.at("js`") || p.at("css`") || p.at("`"):
		return p.parseEmbeddedAttrValue(name, attrStartPos)
	case !p.eof() && p.src[p.i] == '"':
		quotePos := p.posAt(p.i)
		p.i++ // past opening '"'
		vs := p.i
		for !p.eof() && p.src[p.i] != '"' {
			p.i++
		}
		if p.eof() {
			return nil, p.errorfRange(quotePos, p.pos(), "unterminated attribute string for %q", name)
		}
		val := p.src[vs:p.i]
		p.i++ // past closing quote
		sa := &ast.StaticAttr{Name: name, Value: val}
		ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
		return sa, nil
	case !p.eof() && p.src[p.i] == '{':
		if strings.HasPrefix(p.src[p.i+1:], "js`") ||
			strings.HasPrefix(p.src[p.i+1:], "css`") ||
			strings.HasPrefix(p.src[p.i+1:], "`") {
			return p.parseBracedEmbeddedAttrValue(name, attrStartPos)
		}
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

// parseBracedEmbeddedAttrValue parses `name={`…`}` / `name={`…` |> f}` — a
// lone backtick literal (optionally `js`/`css` tagged), optionally followed by
// a whole-literal `|>` pipeline, as the entire braced value. Cursor must be at
// the '{' of the value (the caller has already checked the byte right after it
// starts a `js`/`css`/bare backtick literal).
//
// Like tryParseBodyEmbeddedInterp, this function only commits to EmbeddedAttr
// once the whole shape matches cleanly. On any other outcome — the literal
// fails to close (e.g. a Go raw string ending in `\`, which gsx's
// backtick-escape convention misreads as an escape), trailing content that is
// neither `}` nor a valid `|>` pipeline, or a malformed pipe-stage region — it
// rewinds to the saved start and falls back to the ordinary braced-value parse
// (parseAttrBraceValue), so the value is read as an ordinary Go expression
// instead. A genuinely malformed lone literal then surfaces as a Go-expression
// parse error rather than an embedded-literal one; that's an acceptable trade
// for not having to distinguish "meant to be embedded" from "meant to be Go".
func (p *parser) parseBracedEmbeddedAttrValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	start := p.i // at '{'
	// errMark snapshots p.errs so an abandoned trial can be fully undone:
	// p.errorf (and p.pipeErrorf) record a diagnostic into p.errs as a side
	// effect regardless of whether the caller propagates the returned error,
	// so falling back to parseAttrBraceValue must also truncate p.errs back to
	// this mark or the abandoned trial's diagnostic would still surface from
	// ParseFile.
	errMark := len(p.errs)
	fallback := func() (ast.Attr, error) {
		p.i = start
		p.errs = p.errs[:errMark]
		// Mirror parseSingleAttr's class/style dispatch: a non-literal
		// class/style value must remain a composed ClassAttr so the
		// fallthrough/forwarding merge machinery recognizes it, not a plain
		// ExprAttr (which would silently drop the component's own contribution
		// when a caller forwards class/style via an attrs bag).
		if name == "class" || name == "style" {
			return p.parseComposedAttr(name, attrStartPos)
		}
		return p.parseAttrBraceValue(name, attrStartPos)
	}
	p.i++ // past '{'
	// parseEmbeddedAttrLiteral consumes the literal INCLUDING any gsx
	// backslash-backtick escapes and leaves the cursor right after the closing
	// backtick. Only the region AFTER the literal (pipe stages, or nothing but
	// `}`) is bounded by a Go-aware scan below (goStagesEnd) — that region
	// can't contain a gsx backtick escape, so a Go-aware scan is safe there
	// even though it is not safe over the literal itself (see goStagesEnd
	// doc).
	lang, segments, err := p.parseEmbeddedAttrLiteral()
	if err != nil {
		return fallback()
	}
	p.skipSpace()
	afterLiteral := p.i
	if !p.eof() && p.src[p.i] == '}' {
		p.i++ // past '}'
		ea := &ast.EmbeddedAttr{Name: name, Lang: lang, Segments: segments}
		ast.SetSpan(ea, attrStartPos, p.posAt(p.i))
		return ea, nil
	}
	// Optional whole-literal `|> f` pipeline: name={`…` |> f}.
	if p.at("|>") {
		if end, ok := goStagesEnd(p.src, afterLiteral); ok {
			slice := p.src[afterLiteral:end]
			stages, perr := parseTrailingStages(slice, p.posAt(afterLiteral))
			if perr != nil {
				return fallback()
			}
			ea := &ast.EmbeddedAttr{Name: name, Lang: lang, Segments: segments, Stages: stages}
			p.i = end + 1 // past '}'
			ast.SetSpan(ea, attrStartPos, p.posAt(p.i))
			return ea, nil
		}
	}
	// Not `}` and not a `|>` pipeline (e.g. `{`a` + x}`, or an unterminated
	// `|>` tail) — the backtick was only part of a larger Go expression.
	return fallback()
}

func (p *parser) parseEmbeddedAttrValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
	lang, segments, err := p.parseEmbeddedAttrLiteral()
	if err != nil {
		return nil, err
	}
	ea := &ast.EmbeddedAttr{Name: name, Lang: lang, Segments: segments}
	ast.SetSpan(ea, attrStartPos, p.posAt(p.i))
	return ea, nil
}

func (p *parser) parseEmbeddedAttrLiteral() (ast.EmbeddedLang, []ast.Markup, error) {
	var lang ast.EmbeddedLang
	literalStart := p.i
	var opener int
	switch {
	case p.at("js`"):
		lang = ast.EmbeddedJS
		p.i += len("js`")
		opener = literalStart + len("js")
	case p.at("css`"):
		lang = ast.EmbeddedCSS
		p.i += len("css`")
		opener = literalStart + len("css")
	case p.at("`"):
		lang = ast.EmbeddedText
		p.i += len("`")
		opener = literalStart // the backtick itself
	default:
		return 0, nil, p.errorf(p.pos(), "expected embedded attribute literal")
	}
	segments, err := p.parseEmbeddedSegments(lang, opener)
	if err != nil {
		return 0, nil, err
	}
	return lang, segments, nil
}

func (p *parser) parseEmbeddedSegments(lang ast.EmbeddedLang, opener int) ([]ast.Markup, error) {
	var segments []ast.Markup
	segStart := p.i
	segStartPos := p.posAt(segStart)
	flush := func(end int) {
		if end > segStart {
			txt := &ast.Text{Value: unescapeEmbedded(p.src[segStart:end])}
			ast.SetSpan(txt, segStartPos, p.posAt(end))
			segments = append(segments, txt)
		}
	}
	for !p.eof() {
		if p.src[p.i] == '`' {
			if p.embeddedBacktickEscaped(p.i) {
				p.i++
				continue
			}
			flush(p.i)
			p.i++ // past closing backtick
			return segments, nil
		}
		if p.src[p.i] == '@' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			if p.embeddedAtBraceEscaped(p.i) {
				p.i++ // consume '@'; '{' handled next iteration as literal
				continue
			}
			flush(p.i)
			p.i++ // past '@'; cursor now at '{' for parseInterp
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			segments = append(segments, in)
			segStart = p.i
			segStartPos = p.posAt(p.i)
			continue
		}
		p.i++
	}
	switch lang {
	case ast.EmbeddedJS:
		return nil, p.errorf(p.posAt(opener), "unterminated js attribute literal")
	case ast.EmbeddedCSS:
		return nil, p.errorf(p.posAt(opener), "unterminated css attribute literal")
	default:
		return nil, p.errorf(p.posAt(opener), "unterminated embedded attribute literal")
	}
}

func (p *parser) embeddedBacktickEscaped(backtick int) bool {
	n := 0
	for i := backtick - 1; i >= 0 && p.src[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// embeddedAtBraceEscaped reports whether the '@' at p.src[at] (immediately
// followed by '{') is preceded by an odd number of backslashes, meaning the
// hole opener is escaped (`\@{`) and should be treated as literal text.
func (p *parser) embeddedAtBraceEscaped(at int) bool {
	n := 0
	for i := at - 1; i >= 0 && p.src[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// unescapeEmbedded collapses the two backslash escapes recognized inside
// embedded literals (text/js/css): a backslash-escaped backtick becomes a
// literal backtick, and `\@{` becomes a literal `@{`.
func unescapeEmbedded(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			// \`  -> `
			if s[i+1] == '`' {
				b.WriteByte('`')
				i++
				continue
			}
			// \@{ -> @{
			if s[i+1] == '@' && i+2 < len(s) && s[i+2] == '{' {
				b.WriteString("@{")
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseAttrsUntilBrace parses an attribute list terminated by '}' (the body of a
// conditional attribute). It consumes the closing '}'.
func (p *parser) parseAttrsUntilBrace() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		wsStart := p.i
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unexpected EOF in `{ if … }` attribute body")
		}
		if p.peek() == '}' {
			p.i++ // consume '}'
			return attrs, nil
		}
		if c, ok, err := p.parseTagComment(); err != nil {
			return nil, err
		} else if ok {
			c.Trailing = len(attrs) > 0 && !strings.ContainsRune(p.src[wsStart:p.i], '\n')
			attrs = append(attrs, c)
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
	condPos := p.posAt(condStart + leadingSpaceLen(p.src[condStart:braceOff]))
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseAttrsUntilBrace()
	if err != nil {
		return nil, err
	}
	n := &ast.CondAttr{Cond: cond, CondPos: condPos, Then: body}
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
// within the original source (used to set the span on each OrderedPair).
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

		// Compute the value span: start = first non-space byte after the colon,
		// end = last non-space byte of the value (trimmed trailing whitespace).
		valueStart := colon + 1
		for valueStart < segEnd && (src[valueStart] == ' ' || src[valueStart] == '\t') {
			valueStart++
		}
		valueEnd := segEnd
		for valueEnd > valueStart && (src[valueEnd-1] == ' ' || src[valueEnd-1] == '\t') {
			valueEnd--
		}

		var pr ast.OrderedPair
		pr.Key = key
		pr.Value = rawValue
		ast.SetSpan(&pr, p.posAt(base+valueStart), p.posAt(base+valueEnd))
		pairs = append(pairs, pr)
	}
	return pairs, nil
}
