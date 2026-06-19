// parser/boundary.go
package parser

import (
	"go/scanner"
	"go/token"
)

// goExprEnd returns the index of the `}` that matches the `{` at src[open],
// scanning Go tokens from `open` so that (a) braces inside strings, runes, and
// comments do not count and (b) any markup prose BEFORE `open` is never
// tokenized. ok is false if no matching brace is found.
func goExprEnd(src string, open int) (int, bool) {
	sub := src[open:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	// ScanComments so comment text (which may contain braces) is consumed as a unit.
	s.Init(file, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LBRACE, token.LPAREN, token.LBRACK:
			depth++
		case token.RBRACE, token.RPAREN, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RBRACE {
				return open + fset.Position(pos).Offset, true
			}
		}
	}
}

// scanToBlockBrace returns the index of the '{' that opens a control-flow body,
// scanning Go tokens from offset `from` and returning the first '{' found at
// paren/bracket/brace depth 0. Composite-literal braces inside parens (Go
// requires parens for composite literals in control-flow headers) are skipped.
// ok is false if no such '{' is found.
func scanToBlockBrace(src string, from int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < from {
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
		case token.LBRACE:
			if depth == 0 {
				return off, true
			}
			depth++
		case token.RBRACE:
			depth--
		}
	}
}

// scanToCaseColon returns the index of the ':' that ends a switch case list,
// scanning Go tokens from offset `from` and returning the first ':' at
// paren/bracket/brace depth 0. ok is false if no such ':' is found.
func scanToCaseColon(src string, from int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < from {
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COLON:
			if depth == 0 {
				return off, true
			}
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
