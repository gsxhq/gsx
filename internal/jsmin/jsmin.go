// Package jsmin is gsx's codegen-time safe JS minifier: a tdewolff-lexer-driven
// pass over the static JS of <script> blocks. It strips comments and indentation
// and collapses intra-line whitespace, but KEEPS every newline so automatic
// semicolon insertion is never altered — it performs no value rewrites and never
// fuses tokens. String/template/regex literals and /*! … */ bang comments are
// emitted verbatim.
package jsmin

import (
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// minifyJS applies the safe-minification set to a complete JS string. It is
// driven by tdewolff's JS lexer (correct strings/templates/regex/ASI), so the
// lexer handles regex-vs-division disambiguation via regexPosition tracking.
func minifyJS(s string) string {
	if s == "" {
		return ""
	}
	l := js.NewLexer(parse.NewInputString(s))
	var out strings.Builder
	out.Grow(len(s))
	pendingSpace := false // collapsed intra-line whitespace, not yet emitted
	pendingNL := 0        // count of pending ASI-significant newlines to emit

	// prevTT tracks the last significant (non-whitespace, non-comment, non-newline)
	// token type for regex-position detection. Initialise to ErrorToken which acts
	// as "start of input" — regexPosition returns true for it.
	prevTT := js.ErrorToken

	flush := func() {
		// Newlines win over space; all are dropped at the very start (out empty).
		if pendingNL > 0 {
			if out.Len() > 0 {
				for range pendingNL {
					out.WriteByte('\n')
				}
			}
		} else if pendingSpace {
			if out.Len() > 0 {
				out.WriteByte(' ')
			}
		}
		pendingSpace, pendingNL = false, 0
	}

	for {
		tt, data := l.Next()
		if tt == js.ErrorToken {
			break // EOF (or lex error — emit what we have)
		}
		switch tt {
		case js.WhitespaceToken:
			pendingSpace = true
		case js.LineTerminatorToken:
			// Collapse consecutive blank lines: cap at 1.
			if pendingNL == 0 {
				pendingNL = 1
			}
		case js.CommentToken:
			// Both /* … */ block comments and // line comments produce CommentToken.
			// (Block comments that span lines produce CommentLineTerminatorToken.)
			if strings.HasPrefix(string(data), "/*!") {
				flush()
				out.Write(data) // bang comment: keep verbatim
				prevTT = tt
			} else if strings.HasPrefix(string(data), "//") {
				// Single-line comment: the comment occupied one line. Its trailing \n
				// will arrive as the next LineTerminatorToken. To ensure that line is
				// preserved as a blank line (ASI + blank line) when there is already a
				// pending newline, increment rather than cap.
				// e.g. `a()\n// note\nb()` → a() [NL from \n] [NL from comment line] b()
				pendingNL++
			} else if bytesContainNewline(data) {
				// This path is unreachable (block comments with newlines become
				// CommentLineTerminatorToken), but guard anyway.
				if pendingNL == 0 {
					pendingNL = 1
				}
			} else {
				pendingSpace = true // a removed inline block comment still separates tokens
			}
		case js.CommentLineTerminatorToken:
			// /* … */ block comment containing at least one newline. Strip it but
			// count as one newline (the block comment's line break).
			if pendingNL == 0 {
				pendingNL = 1
			}
		case js.DivToken, js.DivEqToken:
			// The lexer returns DivToken when it cannot determine from internal state
			// alone whether `/` is division or the start of a regex. We use prevTT to
			// decide: if the previous significant token puts us in expression-start
			// position, call l.RegExp() to re-lex the full regex literal verbatim.
			if tt == js.DivToken && regexPosition(prevTT) {
				rtt, rdata := l.RegExp()
				if rtt == js.RegExpToken {
					flush()
					out.Write(rdata)
					prevTT = js.RegExpToken
					continue
				}
				// l.RegExp() failed unexpectedly — fall through to emit the DivToken
				// as a punctuator (conservative; the stream may be broken).
			}
			flush()
			out.Write(data)
			prevTT = tt
		default:
			// Identifier / keyword / punctuator / number / string / template /
			// regex — all emitted verbatim with their interior intact.
			flush()
			out.Write(data)
			prevTT = tt
		}
	}
	return out.String()
}

// regexPosition returns true when a DivToken following the given previous
// significant token should be re-lexed as a regex literal rather than division.
//
// The rule: `/` opens a regex when it follows a token that cannot end an
// expression (assignment operators, binary operators, open brackets, statement
// keywords, unary keywords, comma/semicolon/colon, or start-of-input).
// It is division when it follows a token that can end an expression
// (identifier, number, close-bracket/paren, `++`/`--`).
func regexPosition(prev js.TokenType) bool {
	switch {
	case prev == js.ErrorToken: // start of input
		return true
	case js.IsIdentifier(prev): // plain identifier → division (a / b)
		return false
	case js.IsNumeric(prev): // number → division (1 / 2)
		return false
	}
	switch prev {
	// Tokens that end an expression → division.
	case js.CloseParenToken,   // (x) / y
		js.CloseBracketToken,  // a[i] / y
		js.IncrToken,          // x++ / y  (postfix)
		js.DecrToken,          // x-- / y  (postfix)
		js.RegExpToken,        // /re/ / y (rare but valid)
		js.StringToken,        // "s" / y
		js.TemplateToken,      // `t` / y
		js.TemplateEndToken,   // `…${x}` / y
		js.TrueToken,          // true / y
		js.FalseToken,         // false / y
		js.NullToken,          // null / y
		js.ThisToken:          // this / y
		return false

	// Reserved words that return a value → starts a new expr? No: these end expressions.
	// (super, new handled below; they START expressions)

	// Everything else is expression-start position → regex.
	default:
		return true
	}
}

func bytesContainNewline(b []byte) bool {
	for _, c := range b {
		if c == '\n' || c == '\r' {
			return true
		}
	}
	return false
}
