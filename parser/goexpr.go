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
// It does not parse the element — it only locates where one starts and skips
// past its textual span so the interior isn't re-scanned as Go tokens (which
// would desync on markup like `class="c"` or nested tags). The real element
// parse is Task 3's job.
func scanGoElementMarks(src string) []goElemMark {
	var marks []goElemMark
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	expectOperand := true
	skipUntil := 0 // tokens starting before this offset are inside an already-marked element span
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		off := fset.Position(pos).Offset
		if off < skipUntil {
			continue
		}
		if expectOperand && tok == token.LSS && byteBeginsTag(src, off+1) {
			marks = append(marks, goElemMark{Off: off})
			skipUntil = elementSpanEnd(src, off)
			// After an element, we're back at a position expecting an
			// operator (the element itself was the operand).
			expectOperand = false
			continue
		}
		expectOperand = tokenExpectsOperandAfter(tok)
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
		return true
	}
}

// elementSpanEnd returns the offset just past the element beginning at the
// '<' at src[off] — i.e. just past its matching `/>` or `</tag>`. It tracks
// nesting depth for same-shaped open/close tags and skips over quoted
// attribute values and brace-balanced interpolations so a stray '<', '>', or
// '{' inside them can't desync the walk.
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
		if end, ok := skipQuotedOrComment(src, i); ok {
			i = end
			continue
		}
		switch src[i] {
		case '{':
			end, ok := goDepth1End(src, i+1)
			if !ok {
				return len(src)
			}
			i = end + 1
		case '<':
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
		default:
			i++
		}
	}
	return len(src)
}

// scanTagClose finds the offset of the '>' that closes the tag opening (or
// closing) at src[i] (the '<'), skipping quoted attribute values and
// brace-balanced interpolations so a '>' inside them isn't mistaken for the
// tag's close.
func scanTagClose(src string, i int) (int, bool) {
	j := i + 1
	for j < len(src) {
		if end, ok := skipQuotedOrComment(src, j); ok {
			j = end
			continue
		}
		switch src[j] {
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
