// Package cssfmt re-indents the CSS inside <style> bodies during gsx fmt. It is
// a conservative token-pass: it normalizes leading indentation to tabs by brace
// depth and changes nothing else — no reflow, no invented/stripped blank lines,
// no intra-line spacing changes. Strings and /* */ comments are opaque. It is
// the built-in rawfmt.Formatter for <style>; rawfmt owns the outer (tag-depth)
// indent.
package cssfmt

import (
	"strings"

	"github.com/gsxhq/gsx/internal/reindent"
)

// TokenSignature returns a whitespace-, comment-, and optional-semicolon-
// agnostic signature of src: the significant tokens (tWS and tComment dropped)
// joined by "\x1f". A ';' immediately before a '}' or end of input is dropped —
// it is insignificant in CSS, and the formatter normalizes its presence (it
// always emits a trailing ';' on the last declaration in a block). So a minified
// body and its formatted form share a signature iff they are the same CSS up to
// whitespace and that optional terminator. On a tokenizer error (unterminated
// string/comment) it returns the raw source prefixed with "\x00err\x00" so
// malformed input still compares equal to itself (the printer leaves it
// verbatim).
func TokenSignature(src []byte) string {
	toks, err := tokenize(src)
	if err != nil {
		return "\x00err\x00" + string(src)
	}
	// Significant tokens only.
	var sig []token
	for _, t := range toks {
		if t.kind == tWS || t.kind == tComment {
			continue
		}
		sig = append(sig, t)
	}
	// Drop a ';' that is immediately followed by '}' or is the final token.
	var out []string
	for i, t := range sig {
		if t.kind == tSemi {
			if i == len(sig)-1 || sig[i+1].kind == tRBrace {
				continue
			}
		}
		out = append(out, t.text)
	}
	return strings.Join(out, "\x1f")
}

// Format re-indents a self-contained CSS source string. width is accepted for
// interface symmetry with the rawfmt.Formatter wiring but is unused (a
// re-indenter does not wrap on width). Returns an error only on a tokenizer
// error (unterminated string/comment) so the caller falls back to verbatim.
func Format(src []byte, width, tabWidth int) ([]byte, error) {
	out, ok := reindent.Reindent(src, cssAdapter{}, tabWidth)
	if !ok {
		return nil, errUnterminated
	}
	return []byte(out), nil
}

// FormatLines is Format returning the re-indented LOGICAL lines. A multi-line
// token (template literal, block comment) stays within ONE line, so the caller
// can place each logical line at its depth without re-indenting the token's
// interior. ok=false on a lex error → caller renders verbatim.
func FormatLines(src []byte, width, tabWidth int) ([]string, bool) {
	return reindent.ReindentLines(src, cssAdapter{}, tabWidth)
}

var errUnterminated = stringError("cssfmt: unterminated string or comment")

type stringError string

func (e stringError) Error() string { return string(e) }

// cssAdapter maps the CSS tokenizer's tokens onto reindent.Class.
type cssAdapter struct{}

func (cssAdapter) Tokenize(src []byte) ([]reindent.Token, bool) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, false
	}
	var out []reindent.Token
	for _, t := range toks {
		switch t.kind {
		case tWS:
			out = append(out, splitWS(t.text)...)
		case tComment:
			// /* */ may span lines; its interior re-bases with the code (comment
			// whitespace is insignificant, unlike a string's).
			out = append(out, reindent.SplitComment(t.text)...)
		case tString:
			// CSS strings are single-line and opaque (verbatim).
			out = append(out, reindent.Token{Class: reindent.Opaque, Text: t.text})
		case tLBrace:
			out = append(out, reindent.Token{Class: reindent.Open, Text: t.text})
		case tRBrace:
			out = append(out, reindent.Token{Class: reindent.Close, Text: t.text})
		// Only braces `{}` drive indentation — NOT parens. CSS parens only appear
		// single-line (`@media (...)`, `calc(...)`, `url(...)`), so counting them
		// would risk over-indenting and never helps. Mirrors the JS brace-only rule.
		default:
			out = append(out, reindent.Token{Class: reindent.Other, Text: t.text})
		}
	}
	return out, true
}

// splitWS turns a CSS whitespace run (which may contain newlines) into Newline
// tokens (one per line break, preserving blank lines) and Space tokens for the
// rest. \r\n counts as one line break; a lone \r also counts as one (never
// dropped). All line breaks are normalized to '\n'.
func splitWS(text string) []reindent.Token {
	var out []reindent.Token
	var sp strings.Builder
	flush := func() {
		if sp.Len() > 0 {
			out = append(out, reindent.Token{Class: reindent.Space, Text: sp.String()})
			sp.Reset()
		}
	}
	for i := 0; i < len(text); {
		switch c := text[i]; c {
		case '\n':
			flush()
			out = append(out, reindent.Token{Class: reindent.Newline, Text: "\n"})
			i++
		case '\r':
			flush()
			out = append(out, reindent.Token{Class: reindent.Newline, Text: "\n"})
			i++
			if i < len(text) && text[i] == '\n' {
				i++ // CRLF = one line break
			}
		default:
			sp.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}
