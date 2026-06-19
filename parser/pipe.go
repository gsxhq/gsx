// parser/pipe.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
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

// isStageName reports whether s is a (optionally dotted) Go identifier, e.g.
// "upper" or "strings.ToUpper".
func isStageName(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			b := part[i]
			if i == 0 && b >= '0' && b <= '9' {
				return false
			}
			if !isIdentByte(b) {
				return false
			}
		}
	}
	return true
}

// parsePipeStage parses one filter segment: `name`, `name(args)`, or either with
// a trailing `?`.
func parsePipeStage(seg string) (ast.PipeStage, error) {
	s := strings.TrimSpace(seg)
	try := false
	if strings.HasSuffix(s, "?") {
		try = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "?"))
	}
	if s == "" {
		return ast.PipeStage{}, fmt.Errorf("empty pipeline stage")
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		name := strings.TrimSpace(s[:i])
		end, ok := parenEnd(s, i)
		if !ok {
			return ast.PipeStage{}, fmt.Errorf("unterminated `(` in pipeline stage %q", seg)
		}
		if strings.TrimSpace(s[end+1:]) != "" {
			return ast.PipeStage{}, fmt.Errorf("trailing text after `)` in pipeline stage %q", seg)
		}
		if !isStageName(name) {
			return ast.PipeStage{}, fmt.Errorf("invalid pipeline filter name %q", name)
		}
		return ast.PipeStage{Name: name, Args: strings.TrimSpace(s[i+1 : end]), HasArgs: true, Try: try}, nil
	}
	if !isStageName(s) {
		return ast.PipeStage{}, fmt.Errorf("invalid pipeline filter name %q", s)
	}
	return ast.PipeStage{Name: s, Try: try}, nil
}

// parsePipe splits inner into a seed expression and its filter stages. With no
// top-level `|>`, stages is nil and the result matches the pre-pipeline shape
// (seed = the expression, seedTry = its trailing `?`).
func parsePipe(inner string) (seed string, seedTry bool, stages []ast.PipeStage, err error) {
	segs := splitPipe(inner)
	seed = strings.TrimSpace(segs[0])
	if strings.HasSuffix(seed, "?") {
		seedTry = true
		seed = strings.TrimSpace(strings.TrimSuffix(seed, "?"))
	}
	for _, seg := range segs[1:] {
		st, e := parsePipeStage(seg)
		if e != nil {
			return "", false, nil, e
		}
		stages = append(stages, st)
	}
	return seed, seedTry, stages, nil
}
