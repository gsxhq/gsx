// parser/boundary.go
package parser

import (
	"go/scanner"
	"go/token"
)

// goExprEnd returns the index of the `}` that matches the `{` at src[open],
// scanning Go tokens so that braces inside strings, runes, and comments do not
// count. ok is false if no matching brace is found.
func goExprEnd(src string, open int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	// ScanComments so comment text (which may contain braces) is consumed as a unit.
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < open {
			continue
		}
		switch tok {
		case token.LBRACE, token.LPAREN, token.LBRACK:
			depth++
		case token.RBRACE, token.RPAREN, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RBRACE {
				return off, true
			}
		}
	}
}

// parenEnd returns the index of the `)` matching the `(` at src[open].
func parenEnd(src string, open int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < open {
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RPAREN {
				return off, true
			}
		}
	}
}
