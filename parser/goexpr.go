// parser/goexpr.go
package parser

import (
	"go/scanner"
	"go/token"
)

// goElemMark is the byte offset (relative to the scanned span) of a '<' that
// begins a gsx element at a Go operand-start position.
type goElemMark struct{ Off int }

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
