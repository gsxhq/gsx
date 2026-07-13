// parser/goexpr.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
)

// goElemMark is the byte offset (relative to the scanned span) of a '<' that
// begins a gsx element at a Go operand-start position.
type goElemMark struct{ Off int }

// goExprScan is the result of scanning one interpolation body (`{ … }`) with
// scanGoExpr: a single unified pass that reports everything the legacy byte
// scanners (goDepth1End, splitPipe, composedDelims) and scanGoElementMarks
// compute across four separate walks. All offsets are absolute within the
// scanned src.
type goExprScan struct {
	Close     int          // offset of the depth-0 '}' closing the expr; -1 if none
	Pipes     []int        // offsets of the '|' that begins each top-level '|>' operator
	Commas    []int        // offsets of top-level ',' (ordered/composed attrs)
	Colons    []int        // offsets of top-level ':' (ordered/composed attrs)
	Marks     []goElemMark // operand-position tag/fragment starts
	Backticks [][2]int     // [start,end) spans of backtick literals (bare / js` / css`)
}

// scanRegionObserver, when non-nil, is invoked by goDepth1End with each
// (src, from) region it is asked to delimit. It is a test-only choke-point
// observer used by the corpus differential harness (goexpr_scan_test.go) to
// capture the exact interpolation regions the parser scans, so scanGoExpr can be
// proved byte-identical to the legacy scanners on all of them. It is nil in
// production — a passive hook, never a reroute (that is Task 2).
//
// Lifecycle: this is a PERMANENT regression guard, not a Task-2-and-delete
// scaffold. Once Task 2 reroutes goDepth1End's/composedDelims's callers onto
// scanGoExpr directly, the legacy scanners stop being called in production —
// but they should stay in the tree (dead in prod, alive in tests) precisely
// so this differential keeps running: it is the only thing that continuously
// re-proves scanGoExpr's single unified pass still agrees with the four
// independent byte/token walks it replaced, across the whole corpus, on
// every future syntax change.
var scanRegionObserver func(src string, from int)

// composedRegionObserver, when non-nil, is invoked by composedDelims with the
// inner class/style source it splits. Test-only, like scanRegionObserver: it
// lets the corpus differential compare scanGoExpr's Commas/Colons against
// composedDelims on exactly the (class/style) regions composedDelims actually
// serves in production — where a depth-0 ',' / ':' is usually a real
// ordered-attr delimiter, but can also be the ':' of a Go `:=` (a value-form
// if/switch's `;`-separated init — see TestScanGoExprValueFormInitDivergence
// in goexpr_scan_test.go for the one construct where composedDelims and
// scanGoExpr intentionally diverge on this). Nil in production. Same
// permanent-regression-guard lifecycle as scanRegionObserver above.
var composedRegionObserver func(src string)

// scanGoExpr scans a Go interpolation body with go/scanner, starting at byte
// offset `from` and treating that position as already one level deep — exactly
// as goDepth1End(src, from) does, i.e. as if the opening `{` immediately before
// `from` had been consumed. It reports, in one pass, the closing brace, the
// top-level pipe/comma/colon delimiters, the operand-position element marks,
// and the backtick-literal spans.
//
// It unifies, and is intended to replace (Task 2), these legacy byte/token
// walks: goDepth1End (Close), splitPipe (Pipes), composedDelims (Commas/Colons),
// and scanGoElementMarks (Marks) — while adding gsx-escape-aware backtick spans
// (Backticks) that none of them delimit correctly.
//
// The design mirrors scanGoElementMarks's resume-past-span loop: go/scanner is a
// streaming Go lexer, and neither an element's prose body nor a gsx backtick
// literal is Go — a lone apostrophe/URL in tag text, or a gsx-escaped backtick
// (`js`a\`b“ — go/scanner ends the raw string at the escaped backtick), would
// desync a continuous scan. So on each element (elementSpanEnd) or backtick
// literal (embeddedLiteralEnd) we record the span, then RE-INIT go/scanner just
// past it. A span skipped this way is opaque: its interior contributes nothing
// to depth or to any delimiter.
//
// Depth is tracked from the paren/bracket/brace token stream, starting at 1;
// Close is the RBRACE that returns it to 0. Top-level delimiters are those at
// depth 1 (the expression's own level) — the same set composedDelims/splitPipe
// record at their depth 0, since they scan the brace-stripped inner while this
// scans from inside the brace.
func scanGoExpr(src string, from int) goExprScan {
	res := goExprScan{Close: -1}
	buf := []byte(src)
	base := from // absolute offset where the current Go-token segment begins
	depth := 1
	expectOperand := true
	// prevTok/prevOff track the immediately-preceding token so a '|>' can be
	// recognized as an OR at p followed by a GTR at p+1 (no gap), exactly as
	// splitPipe does. Reset across a span resume so nothing spuriously pairs
	// with a token on the far side.
	prevTok := token.ILLEGAL
	prevOff := -1

	for base < len(buf) {
		fset := token.NewFileSet()
		file := fset.AddFile("", fset.Base(), len(buf)-base)
		var s scanner.Scanner
		s.Init(file, buf[base:], nil, scanner.ScanComments)

		advanced := false
		for {
			pos, tok, _ := s.Scan()
			if tok == token.EOF {
				break
			}
			off := base + fset.Position(pos).Offset

			// Backtick literal. go/scanner reports it as a STRING beginning with
			// '`'. A PREFIXED gsx literal (f`/js`/css`) uses
			// gsx's escape-aware end (embeddedLiteralEnd, which honours the `\``
			// escape) that differs from go/scanner's raw-string end, so we take
			// over its span and resume past it. A BARE backtick is a plain Go raw
			// string — go/scanner already tokenized it correctly (Go raw strings
			// have no escapes and cannot contain a backtick), so we leave it alone
			// and let it flow through as an ordinary STRING operand. This is why
			// gsx never reinterprets a bare Go raw string: interpolation is opt-in
			// behind a prefix.
			if tok == token.STRING && off < len(src) && src[off] == '`' {
				if p := langPrefixStart(src, off); p >= 0 {
					end, _ := embeddedLiteralEnd(src, off+1, '`')
					res.Backticks = append(res.Backticks, [2]int{p, end})
					base = end
					expectOperand = false // a literal is a completed operand
					prevTok = token.ILLEGAL
					prevOff = -1
					advanced = true
					break
				}
				// Bare backtick: fall through to the operand-tracking switch
				// below, treating go/scanner's STRING as a completed Go operand.
			}

			// A `"`-delimited gsx literal (f"/js"/css") — the escape-hatch for
			// content containing a backtick. go/scanner reports it as an
			// interpreted STRING, but its tokenization CANNOT be trusted: gsx's
			// `\@{` is an invalid Go escape, and a `"` inside a hole would
			// prematurely end go/scanner's string. So, exactly as for the
			// prefixed backtick form, we take over the span with gsx's
			// escape-aware `"`-end (embeddedLiteralEnd with delim '"') and resume
			// past it. A BARE `"…"` (langPrefixStart < 0) is a plain Go string —
			// interpolation is opt-in behind the prefix — so it flows through as
			// an ordinary operand.
			if tok == token.STRING && off < len(src) && src[off] == '"' {
				if p := langPrefixStart(src, off); p >= 0 {
					end, _ := embeddedLiteralEnd(src, off+1, '"')
					res.Backticks = append(res.Backticks, [2]int{p, end})
					base = end
					expectOperand = false
					prevTok = token.ILLEGAL
					prevOff = -1
					advanced = true
					break
				}
			}

			// Operand-position element literal: record the mark and resume past
			// its textual span (see scanGoElementMarks).
			if expectOperand && tok == token.LSS && byteBeginsTag(src, off+1) {
				res.Marks = append(res.Marks, goElemMark{Off: off})
				base = elementSpanEnd(src, off)
				expectOperand = false // the element was the operand
				prevTok = token.ILLEGAL
				prevOff = -1
				advanced = true
				break
			}

			switch tok {
			case token.LPAREN, token.LBRACK, token.LBRACE:
				depth++
			case token.RPAREN, token.RBRACK:
				depth--
			case token.RBRACE:
				depth--
				if depth == 0 {
					res.Close = off
					return res
				}
			case token.COMMA:
				if depth == 1 {
					res.Commas = append(res.Commas, off)
				}
			case token.COLON:
				if depth == 1 {
					res.Colons = append(res.Colons, off)
				}
			case token.GTR:
				if depth == 1 && prevTok == token.OR && off == prevOff+1 {
					res.Pipes = append(res.Pipes, prevOff)
				}
			}
			expectOperand = tokenExpectsOperandAfter(tok)
			prevTok = tok
			prevOff = off
		}
		if !advanced {
			break
		}
	}
	return res
}

// langPrefixStart reports the start offset of an `f`/`js`/`css` prefix that
// immediately precedes the literal delimiter at src[delim] (a backtick or a
// `"`), applying the same ident-boundary rule as skipGSXEmbeddedLiteral (so
// `xjs`…“ is a bare literal, not a js one). It inspects only the bytes BEFORE
// src[delim], so it is delimiter-agnostic and serves both the backtick forms
// (f`/js`/css`) and the `"`-delimited escape-hatch forms (f"/js"/css").
// Returns -1 when there is no such prefix — i.e. a bare delimiter whose span
// starts at the delimiter itself (a plain Go raw/interpreted string —
// interpolation is opt-in behind an f/js/css prefix).
func langPrefixStart(src string, delim int) int {
	if delim >= 2 && src[delim-2:delim] == "js" && hasIdentBoundary(src, delim-2) {
		return delim - 2
	}
	if delim >= 3 && src[delim-3:delim] == "css" && hasIdentBoundary(src, delim-3) {
		return delim - 3
	}
	if delim >= 1 && src[delim-1] == 'f' && hasIdentBoundary(src, delim-1) {
		return delim - 1
	}
	return -1
}

// scanGoElementMarks tokenizes src with go/scanner and returns, in order,
// every '<' that begins a gsx element at an operand-start position — i.e. a
// position where Go grammar expects a value (an operand), not an operator.
// This is what makes `<div/>` distinguishable from `a < b`, `a << b`,
// `x <- ch`, and comparison chains: those all tokenize to something other
// than a bare token.LSS at an operand position (LEQ/SHL/ARROW are distinct
// tokens, and `<` right after an operand like an IDENT or `)` is infix).
//
// It does not parse the element — it only locates where one starts, then
// skips past its textual span and RESUMES Go tokenization on the far side.
// The resume (rather than a continuous scan with offset-filtering) is
// essential: go/scanner is a streaming Go lexer, and an element's prose body
// is not Go — a lone apostrophe (`it's`) or an unquoted URL (`http://x`) in
// text would be lexed as an unterminated rune literal / line comment and run
// the scanner to EOF, swallowing every later element. So after each element
// we re-init the scanner past the element span, where real Go resumes.
// The real element parse is Task 3's job.
func scanGoElementMarks(src string) []goElemMark {
	var marks []goElemMark
	buf := []byte(src)
	base := 0 // absolute offset where the current Go-token segment begins
	expectOperand := true
	for base < len(buf) {
		fset := token.NewFileSet()
		file := fset.AddFile("", fset.Base(), len(buf)-base)
		var s scanner.Scanner
		s.Init(file, buf[base:], nil, scanner.ScanComments)

		advanced := false
		for {
			pos, tok, _ := s.Scan()
			if tok == token.EOF {
				break
			}
			off := base + fset.Position(pos).Offset
			if expectOperand && tok == token.LSS && byteBeginsTag(src, off+1) {
				marks = append(marks, goElemMark{Off: off})
				// Skip past the element's textual span and resume Go
				// tokenization there. After an element we're at a position
				// expecting an operator (the element was the operand).
				base = elementSpanEnd(src, off)
				expectOperand = false
				advanced = true
				break
			}
			expectOperand = tokenExpectsOperandAfter(tok)
		}
		if !advanced {
			break
		}
	}
	return marks
}

// goSplitItem is one operand-position construct a Go-region split path must
// carve out: either a gsx element/fragment mark (IsLiteral false, Off is the
// '<') or a prefixed backtick literal span (IsLiteral true, Off is the
// f`/js`/css` prefix start). It is the merged, source-ordered stream that
// scanGoParts reports, so a split path can interleave parsed elements and
// *EmbeddedInterp literals exactly as they appear. The literal's end is
// recovered by the split path itself (the sub-parser's cursor), so it is not
// carried here.
type goSplitItem struct {
	Off       int
	IsLiteral bool
}

// scanGoParts tokenizes src with go/scanner and returns, in source order, every
// operand-position gsx element mark AND every prefixed backtick literal span
// (f`/js`/css`). It is scanGoElementMarks generalized to also report the
// literal spans scanGoExpr's Backticks records — the SAME two constructs
// (element marks, gsx-escape-aware backtick spans) with the SAME resume-past-
// span discipline, but over a full Go region (no depth/delimiter/close-brace
// tracking, which region-level splits don't need). A BARE backtick (a plain Go
// raw string) is NOT a literal — interpolation is opt-in behind the prefix, so
// langPrefixStart returns -1 and it flows through as an ordinary operand.
func scanGoParts(src string) []goSplitItem {
	var items []goSplitItem
	buf := []byte(src)
	base := 0
	expectOperand := true
	for base < len(buf) {
		fset := token.NewFileSet()
		file := fset.AddFile("", fset.Base(), len(buf)-base)
		var s scanner.Scanner
		s.Init(file, buf[base:], nil, scanner.ScanComments)

		advanced := false
		for {
			pos, tok, _ := s.Scan()
			if tok == token.EOF {
				break
			}
			off := base + fset.Position(pos).Offset

			// Prefixed backtick literal at a Go-expression value position. All three
			// prefixes split here (langPrefixStart already validates the prefix set):
			// f`…` lowers to a plain Go string concat (embeddedValueExpr), while
			// js`…`/css`…` lower to gsx.RawJS/gsx.RawCSS wrapping the same concat with
			// per-hole contextual escaping (see docs/superpowers/specs on
			// expression-valued js/css literals). A bare backtick (langPrefixStart
			// < 0) is a plain Go raw string and flows through untouched.
			if tok == token.STRING && off < len(src) && src[off] == '`' {
				if p := langPrefixStart(src, off); p >= 0 {
					end, _ := embeddedLiteralEnd(src, off+1, '`')
					items = append(items, goSplitItem{Off: p, IsLiteral: true})
					base = end
					expectOperand = false
					advanced = true
					break
				}
			}

			// The `"`-delimited counterpart (f"…"/js"…"/css"…") at a Go-expression
			// value position — the escape-hatch for content containing a backtick.
			// Same split as the backtick form above: all three prefixes split; f"
			// lowers to a Go string, js"/css" to gsx.RawJS/gsx.RawCSS. A bare "…" is
			// a plain Go string.
			if tok == token.STRING && off < len(src) && src[off] == '"' {
				if p := langPrefixStart(src, off); p >= 0 {
					end, _ := embeddedLiteralEnd(src, off+1, '"')
					items = append(items, goSplitItem{Off: p, IsLiteral: true})
					base = end
					expectOperand = false
					advanced = true
					break
				}
			}

			// Operand-position element literal.
			if expectOperand && tok == token.LSS && byteBeginsTag(src, off+1) {
				items = append(items, goSplitItem{Off: off})
				base = elementSpanEnd(src, off)
				expectOperand = false
				advanced = true
				break
			}

			expectOperand = tokenExpectsOperandAfter(tok)
		}
		if !advanced {
			break
		}
	}
	return items
}

// byteBeginsTag reports whether the byte at i can start a tag name, a
// fragment (`<>`), or a close (`</...`) — i.e. startsTag's letter/'>'/'/'
// classes. It excludes '-' defensively (a channel receive), though go/scanner
// already tokenizes `<-` as a single ARROW token distinct from LSS, so LSS
// never has '-' as its immediately-following byte; the exclusion documents
// that invariant rather than papering over a gap.
func byteBeginsTag(src string, i int) bool {
	if i >= len(src) {
		return false
	}
	c := src[i]
	if c == '-' {
		return false
	}
	return startsTag(c)
}

// tokenExpectsOperandAfter reports whether, after consuming tok (Go's
// scanner already having classified it), the parser sits at an operand-start
// position (prefix context) rather than an operator/infix position.
//
// Values and closing delimiters put us in infix position (operator
// expected): identifiers, literals, and `)`/`]`/`}` all denote a completed
// expression, so a following `<` is a comparison, not an element.
//
// Everything else — operators, opening delimiters, `,`/`;`/`:`, assignment,
// and most keywords (if/for/switch/return/go/defer/...) — puts us in prefix
// position (operand expected), so a following `<tag` is an element start.
func tokenExpectsOperandAfter(tok token.Token) bool {
	switch tok {
	case token.IDENT, token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING,
		token.RPAREN, token.RBRACK, token.RBRACE:
		return false
	default:
		// The `break`/`continue`/`fallthrough` keywords also land here and
		// report "operand expected", which is imprecise — nothing may follow
		// them. It's harmless: no supported context places an element after
		// those keywords, so the misclassification is never observable.
		return true
	}
}

// elementSpanEnd returns the offset just past the element beginning at the
// '<' at src[off] — i.e. just past its matching `/>` or `</tag>`. It tracks
// nesting depth for same-shaped open/close tags so a stray '<' or '{' inside
// the span can't desync the walk.
//
// The walk alternates between two contexts, which have different lexical
// rules:
//
//   - Inside a tag (between '<' and its closing '>'), a `>` may appear inside
//     a quoted attribute value (`title="a > b"`) or a `{ }` interpolation, so
//     those must be skipped when looking for the tag's close — this is
//     scanTagClose's job.
//   - In text content between tags, `"` `'` “ ` “ `/` are ordinary prose
//     bytes, NOT string/rune/comment delimiters. Only `<` (a nested tag) and
//     `{` (a Go interpolation) are structural. Running a Go-lexical scanner
//     over prose here is wrong: a lone apostrophe (`it's`) or an unquoted URL
//     (`http://x`) would be misread as an unterminated rune/comment and run
//     the skip to end-of-string. This mirrors boundary.go's invariant that
//     markup prose is never tokenized as Go.
//
// It is intentionally not a full element parser: it doesn't validate tag
// name matching between an open and its close, or reject malformed markup.
// It only needs to skip past the element's textual span so the token scan
// above doesn't re-examine (and misinterpret) its interior; Task 3 does the
// real, name-matched parse.
func elementSpanEnd(src string, off int) int {
	i := off
	depth := 0
	for i < len(src) {
		// Invariant: src[i] == '<', the start of a tag (open, close, or
		// self-close). Guaranteed on entry and re-established by the
		// text-content advance at the bottom of the loop.
		closing := i+1 < len(src) && src[i+1] == '/'
		end, ok := scanTagClose(src, i)
		if !ok {
			return len(src)
		}
		selfClosing := !closing && src[end-1] == '/'
		i = end + 1
		switch {
		case closing:
			depth--
			if depth <= 0 {
				return i
			}
		case selfClosing:
			if depth == 0 {
				// self-closing outer element (e.g. <div/> with no
				// children) — the element ends here.
				return i
			}
			// nested self-closing child: depth unchanged
		default:
			depth++
		}
		// Advance through text content to the next tag. Only '<' and '{' are
		// structural here; every other byte (including quotes, backticks, and
		// slashes) is literal prose.
		for i < len(src) && src[i] != '<' {
			if src[i] == '{' {
				braceEnd, ok := goDepth1End(src, i+1)
				if !ok {
					return len(src)
				}
				i = braceEnd + 1
				continue
			}
			i++
		}
	}
	return len(src)
}

// scanTagClose finds the offset of the '>' that closes the tag opening (or
// closing) at src[i] (the '<'), skipping quoted attribute values and
// brace-balanced interpolations so a '>' inside either isn't mistaken for the
// tag's close. Quote handling here is HTML attribute semantics — scan to the
// matching quote byte, no backslash escapes or comment/raw-string rules — not
// Go-lexical, since attribute values are not Go strings.
func scanTagClose(src string, i int) (int, bool) {
	j := i + 1
	for j < len(src) {
		switch src[j] {
		case '"', '\'':
			j = skipAttrQuoted(src, j)
		case '{':
			end, ok := goDepth1End(src, j+1)
			if !ok {
				return 0, false
			}
			j = end + 1
		case '>':
			return j, true
		default:
			j++
		}
	}
	return 0, false
}

// skipAttrQuoted returns the offset just past the quoted attribute value that
// opens at src[i] (a '"' or '\”), i.e. one past its matching close quote.
// HTML attribute values don't process backslash escapes, so it simply scans
// to the next occurrence of the opening quote byte.
func skipAttrQuoted(src string, i int) int {
	quote := src[i]
	for j := i + 1; j < len(src); j++ {
		if src[j] == quote {
			return j + 1
		}
	}
	return len(src)
}

// splitGoElements scans src — a verbatim run of Go source that would
// otherwise become a plain *ast.GoChunk, seated at absolute file position
// base — for gsx elements at Go operand-start positions (scanGoElementMarks,
// the Task 1 detector). When none are found, it returns the unchanged
// *ast.GoChunk: the common case, so every existing GoChunk consumer sees no
// churn. When one or more marks are found, each is handed to the real
// element parser (parseElement) seated at its absolute offset, and the
// result is a *ast.GoWithElements whose Parts interleave GoText runs
// (verbatim Go source, possibly empty — e.g. two elements back to back) with
// the parsed *ast.Element nodes, in source order. Concatenating GoText.Src
// and each element's own source span reproduces src exactly, in every case
// including the error paths below.
func (p *parser) splitGoElements(src string, base token.Pos) []ast.Decl {
	items := scanGoParts(src)
	if len(items) == 0 {
		gc := &ast.GoChunk{Src: src}
		ast.SetSpan(gc, base, base+token.Pos(len(src)))
		return []ast.Decl{gc}
	}

	// Peel a leading run of `import` declarations into their own GoChunk. An
	// embedded element makes this whole region an *ast.GoWithElements, whose
	// GoText parts are spliced verbatim AFTER the skeleton's (analyze.go) and
	// output's (emit.go) synthesized declarations — so a user `import` left
	// inside the region lands after a declaration ("imports must appear before
	// other declarations"). A plain GoChunk, by contrast, has its imports hoisted
	// into the file's import block by both sides (via splitChunk); splitting the
	// leading imports off routes them through that already-correct path. A stray
	// `import` that FOLLOWS a non-import declaration inside the region is not
	// peeled (leadingImportEnd stops at the first non-import token) and remains
	// correctly reported as invalid Go.
	if impEnd := leadingImportEnd(src); impEnd > 0 {
		gc := &ast.GoChunk{Src: src[:impEnd]}
		ast.SetSpan(gc, base, base+token.Pos(impEnd))
		rest := p.splitGoElements(src[impEnd:], base+token.Pos(impEnd))
		return append([]ast.Decl{gc}, rest...)
	}

	// subBase is the absolute byte offset (within p.file) of src[0] — what a
	// sub-parser's `base` field needs so its pos()/posAt() (file.Pos(base+i))
	// resolve to the right byte in the shared file, exactly as if the whole
	// file were being parsed at this offset.
	subBase := p.file.Offset(base)

	var parts []ast.GoPart
	cursor := 0 // offset within src of the next unconsumed byte

	finish := func() []ast.Decl {
		parts = append(parts, goTextPart(src, cursor, len(src), base))
		we := &ast.GoWithElements{Parts: parts}
		ast.SetSpan(we, base, base+token.Pos(len(src)))
		return []ast.Decl{we}
	}

	for _, m := range items {
		if m.Off < cursor {
			// The span-skip's estimate of a previous element's end
			// (elementSpanEnd, used by scanGoParts to resume
			// tokenizing) disagreed with parseElement's real, name-matched
			// end for that element, and this mark falls inside text already
			// consumed. Drop it rather than slice with from > to.
			continue
		}
		parts = append(parts, goTextPart(src, cursor, m.Off, base))

		sub := &parser{file: p.file, src: src, base: subBase, classifier: p.classifier}
		if m.IsLiteral {
			// A prefixed backtick literal (f`/js`/css`) in Go-expression position
			// → an *ast.EmbeddedInterp value (emit lowers it to a Go string).
			lit, err := sub.parseEmbeddedInterpPart(m.Off)
			p.errs = append(p.errs, sub.errs...)
			if err != nil {
				// Fold the rest back in as verbatim text (the literal turned out
				// not to close cleanly as an embedded literal); the error is
				// recorded in p.errs.
				cursor = m.Off
				return finish()
			}
			parts = append(parts, lit)
			cursor = sub.i
			continue
		}
		sub.i = m.Off
		markup, err := sub.parseElement()
		p.errs = append(p.errs, sub.errs...)
		if err != nil {
			// Forward progress: fold the rest of src back in as verbatim
			// text and stop; the error is already recorded in p.errs.
			cursor = m.Off
			return finish()
		}
		switch node := markup.(type) {
		case *ast.Element:
			parts = append(parts, node)
		case *ast.Fragment:
			// A <>…</> fragment is a first-class Go-expression value: a
			// gsx.Node holding its children with no wrapper element. Admitted
			// as a GoPart alongside *Element; lowered by emit.go to an inline
			// gsx.Func closure over the children (empty <></> renders nothing).
			parts = append(parts, node)
		default:
			// Any other markup (none reach here today — byteBeginsTag's
			// remaining candidates are never flagged as marks) is not a
			// supported Go-expression value. Preserve its bytes as verbatim
			// GoText so the round-trip invariant holds.
			p.errorf(base+token.Pos(m.Off), "gsx: %s is not supported as a Go expression value here", markupKind(markup))
			parts = append(parts, goTextPart(src, m.Off, sub.i, base))
			cursor = sub.i
			continue
		}
		cursor = sub.i
	}
	return finish()
}

// SplitGoExprElements splits src — a Go-expression fragment seated at absolute
// file position base within fset — into interleaved GoText and *Element/*Fragment
// parts at its operand-position <tag>/<> marks, using the SHARED fset so every
// parsed node carries a real source position (its interps then probe and //line
// against the right .gsx offsets). It returns nil when src holds no element mark
// (the caller's fast path — an ordinary Go expression). Unlike the file-level
// splitGoElements it performs NO import hoisting (an expression fragment cannot
// contain an import declaration) and returns bare []ast.GoPart rather than a
// *GoWithElements decl. Concatenating each part's source reproduces src exactly.
//
// This is codegen's shared split for embedded tags inside a `{ }` interpolation
// (and other Go-expression positions): the analysis pass stores the result on
// ast.Interp.Embedded and the emit pass reads the SAME nodes, so resolved types
// key on one set of pointers. A parse error is returned in the second result;
// on error the consumed prefix plus the remaining source (as verbatim GoText) is
// still returned so callers can fall back to emitting src unchanged.
func SplitGoExprElements(fset *token.FileSet, src string, base token.Pos, cls *attrclass.Classifier) ([]ast.GoPart, []Error) {
	items := scanGoParts(src)
	if len(items) == 0 {
		return nil, nil
	}
	if cls == nil {
		cls = attrclass.Builtin()
	}
	file := fset.File(base)
	if file == nil {
		return nil, []Error{{Pos: base, Msg: "gsx: no file for embedded-element base position"}}
	}
	// subBase is the absolute byte offset of src[0] within file — what each
	// sub-parser's base needs so posAt (file.Pos(base+i)) resolves to the right
	// byte in the shared file (mirrors splitGoElements's subBase).
	subBase := file.Offset(base)

	var parts []ast.GoPart
	var errs []Error
	cursor := 0
	tail := func() {
		parts = append(parts, goTextPart(src, cursor, len(src), base))
	}
	for _, m := range items {
		if m.Off < cursor {
			// The span-skip estimate disagreed with the real parse; this mark
			// falls inside already-consumed text. Drop it (mirrors splitGoElements).
			continue
		}
		parts = append(parts, goTextPart(src, cursor, m.Off, base))

		sub := &parser{file: file, src: src, base: subBase, classifier: cls}
		if m.IsLiteral {
			// A prefixed backtick literal (f`/js`/css`) at operand position →
			// an *ast.EmbeddedInterp value (emit lowers it to a Go string).
			lit, err := sub.parseEmbeddedInterpPart(m.Off)
			errs = append(errs, sub.errs...)
			if err != nil {
				cursor = m.Off
				tail()
				return parts, errs
			}
			parts = append(parts, lit)
			cursor = sub.i
			continue
		}
		sub.i = m.Off
		markup, err := sub.parseElement()
		errs = append(errs, sub.errs...)
		if err != nil {
			// Forward progress: fold the rest of src back in as verbatim text.
			cursor = m.Off
			tail()
			return parts, errs
		}
		switch node := markup.(type) {
		case *ast.Element:
			parts = append(parts, node)
		case *ast.Fragment:
			parts = append(parts, node)
		default:
			// Not a supported Go-expression value (unreachable today: only a
			// fragment can reach here). Preserve its bytes as verbatim GoText.
			errs = append(errs, Error{
				Pos: base + token.Pos(m.Off),
				Msg: fmt.Sprintf("gsx: %s is not supported as a Go expression value here", markupKind(markup)),
			})
			parts = append(parts, goTextPart(src, m.Off, sub.i, base))
			cursor = sub.i
			continue
		}
		cursor = sub.i
	}
	tail()
	return parts, errs
}

// leadingImportEnd returns the byte offset in src at which a leading run of
// top-level `import` declarations (plus the whitespace trailing them) ends, or 0
// if src does not begin with one. The remainder — everything from the returned
// offset on — is what becomes the *ast.GoWithElements.
//
// The offset sits just before the first non-whitespace byte after the last
// import (its `func`/`var`/`type`/`const` keyword, an operand like `<`, or a doc
// comment). Trailing whitespace is absorbed into the import chunk so it carries
// the blank-line padding between the imports and the next declaration — the
// printer derives inter-declaration spacing from a chunk's trailing newlines
// (see printer.endsWithBlankLine). Stopping BEFORE a comment keeps a doc comment
// attached to the declaration it documents.
func leadingImportEnd(src string) int {
	end := lastLeadingImportEnd(src)
	if end == 0 {
		return 0
	}
	for end < len(src) {
		switch src[end] {
		case ' ', '\t', '\r', '\n':
			end++
		default:
			return end
		}
	}
	return end
}

// lastLeadingImportEnd tokenizes src with go/scanner and returns the byte offset
// immediately after the last of a leading run of IMPORT declarations — single
// (`import "x"`, `import n "x"`, `import . "x"`) or grouped (`import ( … )`) —
// or 0 if src does not begin with one. It stops at the first token that is not
// part of such a declaration.
//
// A nil scanner error handler is deliberate: the region past the imports may hold
// element prose that is not valid Go, but the walk never reaches it — it returns
// at the first non-import token, which always precedes any embedded element.
func lastLeadingImportEnd(src string) int {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, 0) // mode 0: skip comments

	end := 0
	for {
		_, tok, _ := s.Scan()
		if tok == token.SEMICOLON {
			continue // auto-inserted semicolons separate consecutive imports
		}
		if tok != token.IMPORT {
			return end
		}
		// Consume one import declaration; advance end to just past it.
		pos, t, lit := s.Scan()
		if t == token.LPAREN {
			depth := 1
			for depth > 0 {
				p, tt, _ := s.Scan()
				switch tt {
				case token.EOF:
					return end // malformed group; keep what we have
				case token.LPAREN:
					depth++
				case token.RPAREN:
					depth--
					if depth == 0 {
						end = fset.Position(p).Offset + 1 // past ')'
					}
				}
			}
			continue
		}
		// Single spec: optional alias (IDENT / '.' / '_') then the STRING path.
		for t != token.STRING {
			if t == token.EOF || t == token.SEMICOLON || t == token.IMPORT {
				return end // malformed single import; keep what we have
			}
			pos, t, lit = s.Scan()
		}
		end = fset.Position(pos).Offset + len(lit)
	}
}

// goTextPart builds a GoText part covering src[from:to], positioned at base
// (the absolute file position of src[0]).
func goTextPart(src string, from, to int, base token.Pos) ast.GoPart {
	gt := ast.GoText{Src: src[from:to]}
	ast.SetSpan(&gt, base+token.Pos(from), base+token.Pos(to))
	return gt
}

// markupKind names a parsed ast.Markup for the "unsupported here" error
// message when parseElement returns something other than *ast.Element for a
// detected mark (currently only a fragment can reach this, since
// byteBeginsTag's other candidate bytes — '!' for doctype/comment — are
// never flagged as marks by scanGoElementMarks).
func markupKind(m ast.Markup) string {
	switch m.(type) {
	case *ast.Fragment:
		return "a fragment (<>...</>) literal"
	default:
		return fmt.Sprintf("a %T", m)
	}
}
