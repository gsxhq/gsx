// Package reindent is the language-agnostic core of gsx's embedded-language
// formatters. Given a flat stream of classified tokens from a per-language
// Adapter, it re-emits each logical line at its brace-nesting depth using tabs,
// preserving the author's line structure exactly: it never adds or removes a
// line break or a blank line, never reflows, and never alters intra-line
// spacing. Strings, templates, regex, and comments are Opaque — emitted
// verbatim, their internal newlines treated as content, not structure.
package reindent

import "strings"

// Class is how the core treats one token for indentation purposes.
type Class uint8

const (
	Other   Class = iota // ordinary token: emit verbatim, no structural effect
	Open                 // increases block depth for following lines (e.g. "{" "(" "[")
	Close                // decreases block depth; a line STARTING with it dedents
	Newline              // a real line break OUTSIDE any literal/comment
	Space                // inter-token / leading whitespace (NOT inside a literal)
	Opaque               // string/template/regex/comment: emit verbatim, may span lines
)

// Token is one classified lexical token. Text is the exact source bytes.
type Token struct {
	Class Class
	Text  string
}

// Adapter turns language source into the classified token stream. It returns
// ok=false on a lex/tokenize error; Reindent then reports failure and the caller
// renders verbatim.
type Adapter interface {
	Tokenize(src []byte) (toks []Token, ok bool)
}

// Reindent re-indents src using a. Returns (formatted, true), or ("", false) on
// an adapter failure. One tab is emitted per nesting level.
//
// Algorithm (per logical line, split on Newline tokens):
//   - indent = depth, minus one if the line's first significant token is a Close
//     (so a closer dedents to its opener's level); clamped at >= 0.
//   - emit indent tabs, then the line's content with leading and trailing Space
//     tokens dropped and everything else verbatim.
//   - depth += (Open count) - (Close count) on the line, clamped at >= 0.
//
// Blank lines (no content) emit just the newline — no tabs, no trailing space.
func Reindent(src []byte, a Adapter) (string, bool) {
	lines, ok := ReindentLines(src, a)
	if !ok {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// ReindentLines is Reindent returning the re-indented LOGICAL lines instead of a
// joined string. An Opaque token's internal newlines stay WITHIN a line (they are
// content, not line boundaries), so a returned element may itself contain '\n'.
// A blank logical line is an empty string. Reindent(src, a) equals
// strings.Join(ReindentLines(src, a), "\n").
//
// This split lets the outer Doc-building layer (rawfmt) place each logical line
// at its depth WITHOUT re-indenting the interior physical lines of a multi-line
// Opaque token (a template literal or block comment), which would corrupt the
// token's value and break idempotence.
func ReindentLines(src []byte, a Adapter) ([]string, bool) {
	toks, ok := a.Tokenize(src)
	if !ok {
		return nil, false
	}

	// Split into logical lines on Newline tokens. Opaque tokens keep their
	// internal newlines (they are content, not line boundaries).
	var lines [][]Token
	var cur []Token
	for _, t := range toks {
		if t.Class == Newline {
			lines = append(lines, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	lines = append(lines, cur)

	out := make([]string, 0, len(lines))
	depth := 0
	for _, line := range lines {
		// Trim leading and trailing Space tokens.
		start, end := 0, len(line)
		for start < end && line[start].Class == Space {
			start++
		}
		for end > start && line[end-1].Class == Space {
			end--
		}
		content := line[start:end]
		if len(content) == 0 {
			out = append(out, "") // blank line: no indent
			continue
		}
		indent := depth
		if content[0].Class == Close && indent > 0 {
			indent--
		}
		var b strings.Builder
		for i := 0; i < indent; i++ {
			b.WriteByte('\t')
		}
		opens, closes := 0, 0
		for _, t := range content {
			b.WriteString(t.Text)
			switch t.Class {
			case Open:
				opens++
			case Close:
				closes++
			}
		}
		depth += opens - closes
		if depth < 0 {
			depth = 0
		}
		out = append(out, b.String())
	}
	return out, true
}
