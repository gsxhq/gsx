// parser/pipe.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// balancedParenUnwrap removes one outer parenthesis layer from s when s is a
// single fully-parenthesized expression — i.e. s is `(` + inner + `)` and the
// `)` matching the opening `(` is the final token. Returns (inner, true) in that
// case, else (s, false). Token-aware (via go/scanner) so parens inside string,
// rune, or comment tokens are not miscounted. Used by parseSpreadAttr to accept
// the canonical parenthesized piped-spread form `{ (seed |> f)... }`.
func balancedParenUnwrap(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' {
		return s, false
	}
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(s))
	var sc scanner.Scanner
	sc.Init(file, []byte(s), nil, scanner.ScanComments)
	depth := 0
	for {
		pos, tok, _ := sc.Scan()
		if tok == token.EOF {
			return s, false
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
			if depth == 0 {
				// This token closes the opening paren at offset 0. s is fully
				// parenthesized only if it is the final token (last byte).
				if file.Offset(pos) == len(s)-1 {
					return strings.TrimSpace(s[1 : len(s)-1]), true
				}
				return s, false
			}
		}
	}
}

// splitPipe splits src on top-level `|>` pipeline operators — those at bracket
// depth 0, outside strings, runes, and comments. Segments are returned in order
// with surrounding whitespace preserved (the caller trims). With no top-level
// `|>`, it returns a single segment equal to src. `|>` is an `OR` token (`|`)
// immediately followed by a `GTR` token (`>`) with no gap; `||`, `|=`, and `| >`
// never match.
func splitPipe(src string) []string {
	// Fast path: no `|>` substring anywhere → no pipeline. Avoids a scanner pass
	// on the common plain-interpolation case. Safe because a real `|>` operator
	// necessarily contains the substring `|>`; this only skips when it is absent.
	if !strings.Contains(src, "|>") {
		return []string{src}
	}
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
	for part := range strings.SplitSeq(s, ".") {
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

// errTryMarker is returned for any trailing `?` (on the seed or a stage). gsx has
// no try-marker: a `(T, error)` value is auto-unwrapped at codegen (the error
// propagates out of the enclosing Render). Go has no `?` operator, so a trailing
// `?` is unambiguously the removed marker — reject it with a migration hint rather
// than letting `expr?` poison type resolution for the whole file.
var errTryMarker = fmt.Errorf("the `?` try-marker is not supported; gsx auto-unwraps (T, error) values — remove the `?` (handle the error explicitly with `{ if v, err := f(); err != nil { … } }`)")

// parsePipeStage parses one filter segment: `name` or `name(args)`. segBase is
// the source position of seg[0], so NamePos/ArgsPos resolve to real source.
func parsePipeStage(seg string, segBase token.Pos) (ast.PipeStage, error) {
	leadWS := len(seg) - len(strings.TrimLeft(seg, " \t\r\n"))
	namePos := segBase + token.Pos(leadWS) // first non-ws char = the name's first char
	s := strings.TrimSpace(seg)
	if strings.HasSuffix(s, "?") {
		return ast.PipeStage{}, errTryMarker
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
		rawArgs := s[i+1 : end]
		argsLead := len(rawArgs) - len(strings.TrimLeft(rawArgs, " \t\r\n"))
		// s[k] is at namePos+k; args' first char is s[i+1+argsLead].
		argsPos := namePos + token.Pos(i+1+argsLead)
		return ast.PipeStage{Name: name, Args: strings.TrimSpace(rawArgs), HasArgs: true, NamePos: namePos, ArgsPos: argsPos}, nil
	}
	if !isStageName(s) {
		return ast.PipeStage{}, fmt.Errorf("invalid pipeline filter name %q", s)
	}
	return ast.PipeStage{Name: s, NamePos: namePos}, nil
}

// parsePipe splits inner into a seed expression and its filter stages. base is
// the source position of inner[0]; stage positions are derived from it.
// With no top-level `|>`, stages is nil and seed is the whole expression. A
// trailing `?` on the seed (the removed try-marker) is rejected.
func parsePipe(inner string, base token.Pos) (seed string, stages []ast.PipeStage, err error) {
	segs := splitPipe(inner)
	seed = strings.TrimSpace(segs[0])
	if strings.HasSuffix(seed, "?") {
		return "", nil, errTryMarker
	}
	segOff := len(segs[0]) + 2 // segs[1] starts after segs[0] + "|>"
	for _, seg := range segs[1:] {
		st, e := parsePipeStage(seg, base+token.Pos(segOff))
		if e != nil {
			return "", nil, e
		}
		stages = append(stages, st)
		segOff += len(seg) + 2
	}
	return seed, stages, nil
}
