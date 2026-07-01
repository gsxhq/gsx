// parser/boundary.go
package parser

import (
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

// goExprEnd returns the index of the `}` that matches the `{` at src[open],
// scanning Go tokens from `open` so that (a) braces inside strings, runes, and
// comments do not count and (b) any markup prose BEFORE `open` is never
// tokenized. ok is false if no matching brace is found.
func goExprEnd(src string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(src); {
		if end, ok := skipGSXEmbeddedLiteral(src, i); ok {
			i = end
			continue
		}
		if end, ok := skipQuotedOrComment(src, i); ok {
			i = end
			continue
		}
		switch src[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
			if depth == 0 && src[i] == '}' {
				return i, true
			}
		}
		i++
	}
	return 0, false
}

func composedDelims(src string) (commas, colons []int) {
	depth := 0
	for i := 0; i < len(src); {
		if end, ok := skipGSXEmbeddedLiteral(src, i); ok {
			i = end
			continue
		}
		if end, ok := skipQuotedOrComment(src, i); ok {
			i = end
			continue
		}
		switch src[i] {
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
		case ',':
			if depth == 0 {
				commas = append(commas, i)
			}
		case ':':
			if depth == 0 {
				colons = append(colons, i)
			}
		}
		i++
	}
	return commas, colons
}

func skipGSXEmbeddedLiteral(src string, i int) (int, bool) {
	switch {
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], "js`"):
		return embeddedLiteralEnd(src, i+len("js`"))
	case hasIdentBoundary(src, i) && strings.HasPrefix(src[i:], "css`"):
		return embeddedLiteralEnd(src, i+len("css`"))
	default:
		return 0, false
	}
}

func hasIdentBoundary(src string, i int) bool {
	return i == 0 || !isIdentByte(src[i-1])
}

func embeddedLiteralEnd(src string, i int) (int, bool) {
	for i < len(src) {
		if src[i] == '`' && !backtickEscapedIn(src, i) {
			return i + 1, true
		}
		i++
	}
	return len(src), true
}

func backtickEscapedIn(src string, backtick int) bool {
	n := 0
	for i := backtick - 1; i >= 0 && src[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func skipQuotedOrComment(src string, i int) (int, bool) {
	switch src[i] {
	case '"', '\'':
		quote := src[i]
		i++
		for i < len(src) {
			if src[i] == '\\' {
				i += 2
				continue
			}
			if src[i] == quote {
				return i + 1, true
			}
			i++
		}
		return len(src), true
	case '`':
		i++
		for i < len(src) && src[i] != '`' {
			i++
		}
		if i < len(src) {
			return i + 1, true
		}
		return len(src), true
	case '/':
		if i+1 >= len(src) {
			return 0, false
		}
		switch src[i+1] {
		case '/':
			i += 2
			for i < len(src) && src[i] != '\n' {
				i++
			}
			return i, true
		case '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < len(src) {
				return i + 2, true
			}
			return len(src), true
		}
	}
	return 0, false
}

// scanToBlockBrace finds the byte offset of the '{' that opens a control-flow
// body. `from` points just after the leading `keyword` ("if"/"for"/"switch").
// It enumerates each '{' at paren/bracket depth 0 and returns the first one for
// which `keyword <header> {}` parses as a valid Go statement — delegating
// composite-literal disambiguation to go/parser, so bare composite literals in a
// `for … range` clause are handled correctly. ok is false if none parse.
func scanToBlockBrace(src string, from int, keyword string) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
		case token.LBRACE:
			if depth == 0 {
				b := from + fset.Position(pos).Offset
				if blockHeaderParses(keyword + " " + src[from:b]) {
					return b, true
				}
				depth++ // composite-literal brace; descend into it
			} else {
				depth++
			}
		case token.RBRACE:
			depth--
		}
	}
}

// blockHeaderParses reports whether `header {}` is a valid Go control-flow
// statement (header includes the leading keyword). Used to locate the body brace
// of a gsx control-flow construct with full Go fidelity.
func blockHeaderParses(header string) bool {
	_, err := goparser.ParseFile(token.NewFileSet(), "", "package p\nfunc _(){\n"+header+"{}\n}", 0)
	return err == nil
}

// scanToCaseColon returns the index of the ':' that ends a switch case list,
// scanning Go tokens from `from` (the start of the case list) and returning the
// first ':' at paren/bracket/brace depth 0. Scanning starts at `from` — not
// offset 0 — so markup prose before the case list (e.g. an earlier case body
// with an apostrophe) is never tokenized and cannot desync the scanner. ok is
// false if no such ':' is found.
func scanToCaseColon(src string, from int) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COLON:
			if depth == 0 {
				return from + fset.Position(pos).Offset, true
			}
		}
	}
}

// valueSwitchArmEnd returns the byte offset of the next top-level case,
// default, or switch-closing brace after a value-switch arm.
// Delimiters nested inside Go composite literals, function literals, or other
// bracketed expressions are ignored.
func valueSwitchArmEnd(src string, from int) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := from + fset.Position(pos).Offset
		if depth == 0 {
			switch tok {
			case token.CASE, token.DEFAULT, token.RBRACE:
				return off, true
			}
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		}
	}
}

// parenEnd returns the index of the `)` matching the `(` at src[open], scanning
// Go tokens from `open` so prose before `open` is never tokenized.
func parenEnd(src string, open int) (int, bool) {
	sub := src[open:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RPAREN {
				return open + fset.Position(pos).Offset, true
			}
		}
	}
}
