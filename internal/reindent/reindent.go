// Package reindent is the language-agnostic core of gsx's embedded-language
// formatters. Given a flat stream of classified tokens from a per-language
// Adapter, it RE-BASES a block of embedded code: it strips the block's own
// common leading indentation so the caller can re-place it under the tag or
// attribute, while PRESERVING the author's relative indentation exactly.
//
// It deliberately does NOT compute indentation from language structure (braces,
// case labels, expression continuations). A structural re-indenter would have to
// model a large, open-ended slice of the embedded grammar (switch/case, ASI-based
// statement continuation, every bracket/operator nuance) and would mis-indent
// some construct it didn't anticipate. Re-basing keeps the author's own
// indentation — which for hand-formatted code is already correct — and cannot
// introduce an indentation error for any construct, known or unknown. It never
// adds or removes a line break or blank line, never reflows, never alters
// intra-line spacing. Strings, templates, regex, and comments are Opaque —
// emitted verbatim, their internal newlines treated as content, not structure.
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

// Reindent re-bases src using a. Returns (formatted, true), or ("", false) on an
// adapter failure. The block's common leading indentation is removed; the
// author's relative indentation is preserved.
func Reindent(src []byte, a Adapter, tabWidth int) (string, bool) {
	lines, ok := ReindentLines(src, a, tabWidth)
	if !ok {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// ReindentLines is Reindent returning the re-based LOGICAL lines instead of a
// joined string. An Opaque token's internal newlines stay WITHIN a line (they are
// content, not line boundaries), so a returned element may itself contain '\n'.
// A blank logical line is an empty string. Reindent(src, a) equals
// strings.Join(ReindentLines(src, a), "\n").
//
// Algorithm (per logical line, split on Newline tokens):
//   - Separate the line's leading whitespace from its content; trim trailing
//     whitespace. A line with no content is blank ("").
//   - Compute the block's base = the longest common leading-whitespace prefix of
//     the non-blank logical lines, EXCLUDING the first logical line. The first
//     line is excluded because in an inline attribute value (name=js"{ … }") its
//     `{` is attached to the delimiter with no leading whitespace and is not the
//     block's base — counting it would force base="" and leave the body's own
//     source indentation baked in (the caller would then double-indent it).
//   - Emit each line with the base prefix removed and the rest of its leading
//     whitespace kept verbatim (its relative indentation).
//
// The split into logical lines also lets the outer Doc layer (rawfmt) place each
// line at the target depth WITHOUT re-indenting the interior of a multi-line
// Opaque token (a template literal or block comment).
func ReindentLines(src []byte, a Adapter, tabWidth int) ([]string, bool) {
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

	// Separate leading whitespace from content for each logical line.
	type parsed struct{ leading, content string }
	ps := make([]parsed, len(lines))
	for i, line := range lines {
		start, end := 0, len(line)
		for start < end && line[start].Class == Space {
			start++
		}
		for end > start && line[end-1].Class == Space {
			end--
		}
		var lead strings.Builder
		for _, t := range line[:start] {
			lead.WriteString(t.Text)
		}
		var content strings.Builder
		for _, t := range line[start:end] {
			content.WriteString(t.Text)
		}
		ps[i] = parsed{lead.String(), content.String()}
	}

	// base = common leading-whitespace prefix over the non-blank lines, EXCLUDING
	// the first non-blank line. The first line of an inline embedded value is
	// attached to the delimiter at column 0 (`name=js"{ … }"`, `name=js"(x) => {`),
	// so its indentation is an artifact, not the block's base — counting it would
	// force base="" and leave the body's own source indentation baked in (the
	// caller then double-indents it). The body dedents by its real base (e.g. the
	// closing `}` line), and the first line is re-attached by the caller. If
	// excluding leaves nothing (a single-line body), fall back to the first line's
	// own leading so it still dedents to zero.
	//
	// The cost: a bare multi-line continuation whose FIRST line is at the base
	// (e.g. an event handler `x = a \n || b` with no other statement) loses its
	// hanging indent. That is rare — embedded bodies are objects, functions, or
	// multi-statement blocks whose base level recurs on a later line.
	// Exclude the first content line from the base ONLY for an inline body (its
	// first logical line carries content, e.g. `{` attached to the delimiter at
	// column 0 — an artifact, not the block's base). A block body opens on its own
	// line (first logical line empty), so its first content line is a real base
	// line and MUST be counted — otherwise a 2-line body like `x = a \n || b`
	// treats the indented continuation as the base and strips its hanging indent.
	inline := len(ps) > 0 && ps[0].content != ""
	base := ""
	seen := false
	firstLeading, haveFirst := "", false
	for _, p := range ps {
		if p.content == "" {
			continue
		}
		if inline && !haveFirst {
			firstLeading, haveFirst = p.leading, true
			continue
		}
		if !seen {
			base, seen = p.leading, true
			continue
		}
		base = commonPrefix(base, p.leading)
	}
	if !seen {
		base = firstLeading
	}

	// Brace depth per logical line (only `{ }` count — see the adapter's classify;
	// parens/brackets are intentionally excluded so `foo(x, () => {` indents its
	// body one level, not two). A line's own indent is the depth at its start,
	// dedented by one if the line opens with `}` (it closes the enclosing block).
	depths := make([]int, len(lines))
	depth := 0
	for i, line := range lines {
		lineDepth := depth
		if startsWithClose(line) {
			lineDepth--
		}
		if lineDepth < 0 {
			lineDepth = 0
		}
		depths[i] = lineDepth
		for _, t := range line {
			switch t.Class {
			case Open:
				depth++
			case Close:
				depth--
			}
		}
	}

	// Impose brace depth per LEVEL while preserving the author's relative structure
	// WITHIN a level. For each brace-depth level, the common leading prefix of its
	// (base-stripped) lines is the level's own baseline; a line's own extra beyond
	// that baseline is what the author added on top of the brace structure — a
	// `case:` body, a `||` continuation, a method chain. The final indent is the
	// brace depth (as tabs) PLUS that extra. So flush and 2-space bodies both
	// collapse to the brace depth (their extra is empty), a nested object goes one
	// level deeper per brace, an indented continuation keeps its hanging indent,
	// and a `case:` body stays one level under its label.
	levelBase := map[int]string{}
	levelSeen := map[int]bool{}
	for i, p := range ps {
		if p.content == "" {
			continue
		}
		rel := strings.TrimPrefix(p.leading, base)
		d := depths[i]
		if !levelSeen[d] {
			levelBase[d], levelSeen[d] = rel, true
			continue
		}
		levelBase[d] = commonPrefix(levelBase[d], rel)
	}

	out := make([]string, len(ps))
	for i, p := range ps {
		if p.content == "" {
			out[i] = ""
			continue
		}
		rel := strings.TrimPrefix(p.leading, base)
		extra := strings.TrimPrefix(rel, levelBase[depths[i]])
		out[i] = strings.Repeat("\t", depths[i]) + extra + p.content
	}
	return out, true
}

// startsWithClose reports whether a logical line's first non-space token is a
// closing brace, so its own indentation dedents one level.
func startsWithClose(line []Token) bool {
	for _, t := range line {
		if t.Class == Space {
			continue
		}
		return t.Class == Close
	}
	return false
}

// SplitComment tokenizes a (possibly multi-line) comment so its interior lines
// RE-BASE with the surrounding code. A block comment's whitespace is
// insignificant, so — unlike a string / template / regex literal, which stays a
// single verbatim Opaque token — its continuation lines should align to the
// re-based code: the first line is one Opaque token; each subsequent line becomes
// Newline + Space(leading) + Opaque(rest), so its leading indentation is
// dedented and re-based like any logical line while its content stays verbatim.
// A single-line comment returns one Opaque token unchanged.
func SplitComment(text string) []Token {
	if !strings.Contains(text, "\n") {
		return []Token{{Class: Opaque, Text: text}}
	}
	var toks []Token
	for i, ln := range strings.Split(text, "\n") {
		if i == 0 {
			toks = append(toks, Token{Class: Opaque, Text: ln})
			continue
		}
		toks = append(toks, Token{Class: Newline, Text: "\n"})
		j := 0
		for j < len(ln) && (ln[j] == ' ' || ln[j] == '\t') {
			j++
		}
		if j > 0 {
			toks = append(toks, Token{Class: Space, Text: ln[:j]})
		}
		toks = append(toks, Token{Class: Opaque, Text: ln[j:]})
	}
	return toks
}

// commonPrefix returns the longest common byte prefix of a and b.
func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
