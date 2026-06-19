// parser/boundary.go
package parser

import (
	goparser "go/parser"
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
