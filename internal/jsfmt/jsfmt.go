// Package jsfmt re-indents the JavaScript inside executable <script> bodies
// during gsx fmt. It is a conservative token-pass driven by tdewolff's JS LEXER
// (no parser): it normalizes leading indentation to tabs by brace depth and
// changes nothing else — it KEEPS every newline, so automatic semicolon
// insertion is never altered (identical safety to internal/jsmin). Strings,
// template literals, regex, and comments are opaque. It is the built-in
// rawfmt.Formatter for <script>; rawfmt owns the outer (tag-depth) indent.
package jsfmt

import (
	"io"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"

	"github.com/gsxhq/gsx/internal/reindent"
)

// Format re-indents a self-contained JS source string. width is accepted for
// interface symmetry with the rawfmt.Formatter wiring but is unused. Returns an
// error only on a lexer error so the caller falls back to verbatim.
func Format(src []byte, width int) ([]byte, error) {
	out, ok := reindent.Reindent(src, jsAdapter{})
	if !ok {
		return nil, stringError("jsfmt: lex error")
	}
	return []byte(out), nil
}

// FormatLines is Format returning the re-indented LOGICAL lines. A multi-line
// token (template literal, block comment) stays within ONE line, so the caller
// can place each logical line at its depth without re-indenting the token's
// interior. ok=false on a lex error → caller renders verbatim.
func FormatLines(src []byte, width int) ([]string, bool) {
	return reindent.ReindentLines(src, jsAdapter{})
}

type stringError string

func (e stringError) Error() string { return string(e) }

// TokenSignature returns a whitespace- and comment-agnostic signature of src:
// the significant tokens joined by "\x1f". Two JS strings with the same
// significant tokens (e.g. messy vs re-indented) share a signature. On a lex
// error it returns the raw source prefixed with "\x00err\x00" so malformed JS
// (left verbatim by the printer) compares equal to itself.
func TokenSignature(src []byte) string {
	toks, ok := lexClassified(src)
	if !ok {
		return "\x00err\x00" + string(src)
	}
	var sig []string
	for _, t := range toks {
		switch t.Class {
		case reindent.Space, reindent.Newline, reindent.Opaque:
			// Opaque covers comments (insignificant) AND string/template/regex
			// literals (significant) — but literals are emitted verbatim by the
			// re-indenter, so they never change across formatting; to keep the
			// signature discriminating we still include literals, EXCLUDING
			// comments. Distinguish below.
			if t.Class == reindent.Opaque && !isComment(t.Text) {
				sig = append(sig, t.Text)
			}
		default:
			sig = append(sig, t.Text)
		}
	}
	return strings.Join(sig, "\x1f")
}

func isComment(s string) bool {
	return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*")
}

// jsAdapter drives tdewolff's JS lexer and classifies tokens for reindent.
type jsAdapter struct{}

func (jsAdapter) Tokenize(src []byte) ([]reindent.Token, bool) { return lexClassified(src) }

func lexClassified(src []byte) ([]reindent.Token, bool) {
	l := js.NewLexer(parse.NewInputString(string(src)))
	var toks []reindent.Token
	prevTT := js.ErrorToken
	for {
		tt, data := l.Next()
		if tt == js.ErrorToken {
			if err := l.Err(); err != nil && err != io.EOF {
				return nil, false // real lex error
			}
			break // clean EOF
		}
		switch tt {
		case js.WhitespaceToken:
			toks = append(toks, reindent.Token{Class: reindent.Space, Text: string(data)})
		case js.LineTerminatorToken:
			// Emit one Newline per line break so blank lines are preserved AND no
			// ASI-significant break is ever dropped. tdewolff returns \n, \r, \r\n,
			// U+2028, and U+2029 as LineTerminatorToken (possibly in runs); count line
			// breaks treating \r\n as one. All are normalized to '\n' (CRLF/exotic
			// terminators -> LF).
			n := countLineBreaks(data)
			if n == 0 {
				n = 1 // defensive: a terminator token always represents >=1 break
			}
			for i := 0; i < n; i++ {
				toks = append(toks, reindent.Token{Class: reindent.Newline, Text: "\n"})
			}
		case js.CommentToken, js.CommentLineTerminatorToken:
			// // and /* */ (the latter may span lines) — opaque, verbatim.
			toks = append(toks, reindent.Token{Class: reindent.Opaque, Text: string(data)})
		case js.DivToken, js.DivEqToken:
			// The lexer returns DivToken when it cannot determine from internal state
			// alone whether `/` is division or the start of a regex. We use prevTT to
			// decide: if the previous significant token puts us in expression-start
			// position, call l.RegExp() to re-lex the full regex literal verbatim.
			if tt == js.DivToken && regexPosition(prevTT) {
				rtt, rdata := l.RegExp()
				if rtt == js.RegExpToken {
					toks = append(toks, reindent.Token{Class: reindent.Opaque, Text: string(rdata)})
					prevTT = js.RegExpToken
					continue
				}
			}
			toks = append(toks, reindent.Token{Class: reindent.Other, Text: string(data)})
			prevTT = tt
		default:
			toks = append(toks, classify(tt, data))
			prevTT = tt
		}
	}
	return toks, true
}

func classify(tt js.TokenType, data []byte) reindent.Token {
	switch tt {
	case js.StringToken, js.TemplateToken, js.TemplateStartToken,
		js.TemplateMiddleToken, js.TemplateEndToken, js.RegExpToken:
		return reindent.Token{Class: reindent.Opaque, Text: string(data)}
	case js.OpenBraceToken:
		return reindent.Token{Class: reindent.Open, Text: string(data)}
	case js.CloseBraceToken:
		return reindent.Token{Class: reindent.Close, Text: string(data)}
	// Only braces `{}` drive indentation — NOT parens/brackets. A line like
	// `foo('x', (e) => {` has an unclosed `(` AND an opening `{`; counting both
	// would indent the body two levels (the bug). Real-world JS indents block
	// scope only, so brace-only reproduces hand/prettier-formatted code (the
	// callback pattern `call(args, () => {…})` is ubiquitous in Alpine/htmx).
	// Bare multi-line paren/bracket continuations stay flat — acceptable and
	// vanishingly rare in practice (0 occurrences across the sampled real code).
	default:
		return reindent.Token{Class: reindent.Other, Text: string(data)}
	}
}

// countLineBreaks counts JS line terminators in data, treating \r\n as a single
// break. Recognizes \n, \r, U+2028 (LINE SEPARATOR), and U+2029 (PARAGRAPH SEPARATOR).
func countLineBreaks(data []byte) int {
	s := string(data)
	n := 0
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\r':
			n++
			i += sz
			if i < len(s) && s[i] == '\n' {
				i++ // CRLF counts once
			}
		case '\n', ' ', ' ':
			n++
			i += sz
		default:
			i += sz
		}
	}
	return n
}

// regexPosition reports whether a DivToken following prev should be re-lexed as
// a regex literal rather than division. Copied from internal/jsmin (same
// tdewolff-lexer disambiguation) — kept local rather than shared, mirroring how
// jsmin and jsx each keep their own copy.
func regexPosition(prev js.TokenType) bool {
	switch {
	case prev == js.ErrorToken: // start of input
		return true
	case js.IsIdentifier(prev): // a / b
		return false
	case js.IsNumeric(prev): // 1 / 2
		return false
	}
	switch prev {
	case js.CloseParenToken, js.CloseBracketToken, js.CloseBraceToken,
		js.IncrToken, js.DecrToken, js.RegExpToken, js.StringToken,
		js.TemplateToken, js.TemplateEndToken, js.TrueToken, js.FalseToken,
		js.NullToken, js.ThisToken, js.SuperToken:
		return false
	default:
		return true
	}
}
