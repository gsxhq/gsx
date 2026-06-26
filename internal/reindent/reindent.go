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
	toks, ok := a.Tokenize(src)
	if !ok {
		return "", false
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

	var b strings.Builder
	depth := 0
	for li, line := range lines {
		if li > 0 {
			b.WriteByte('\n')
		}
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
			continue // blank line: newline only, no indent
		}
		indent := depth
		if content[0].Class == Close && indent > 0 {
			indent--
		}
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
	}
	return b.String(), true
}
