// parser/pipe.go
package parser

import (
	"go/scanner"
	"go/token"
)

// splitPipe splits src on top-level `|>` pipeline operators — those at bracket
// depth 0, outside strings, runes, and comments. Segments are returned in order
// with surrounding whitespace preserved (the caller trims). With no top-level
// `|>`, it returns a single segment equal to src. `|>` is an `OR` token (`|`)
// immediately followed by a `GTR` token (`>`) with no gap; `||`, `|=`, and `| >`
// never match.
func splitPipe(src string) []string {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	var splits []int // byte offset of each `|` that begins a top-level `|>`
	depth := 0
	prevTok := token.ILLEGAL
	prevOff := -1
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.GTR:
			if depth == 0 && prevTok == token.OR && off == prevOff+1 {
				splits = append(splits, prevOff)
			}
		}
		prevTok = tok
		prevOff = off
	}
	if len(splits) == 0 {
		return []string{src}
	}
	segs := make([]string, 0, len(splits)+1)
	start := 0
	for _, sp := range splits {
		segs = append(segs, src[start:sp])
		start = sp + 2 // skip "|>"
	}
	return append(segs, src[start:])
}
