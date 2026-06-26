// internal/cssfmt/token.go
package cssfmt

import (
	"fmt"
	"strings"
)

type tokKind int

const (
	tWS tokKind = iota // whitespace run (may contain newlines)
	tComment           // /* ... */
	tString            // "..." or '...'
	tWord              // ident/number/dimension/hash/at-keyword/sentinel: any run of "word" bytes
	tLBrace            // {
	tRBrace            // }
	tLParen            // (
	tRParen            // )
	tColon             // :
	tSemi              // ;
	tComma             // ,
	tDelim             // any other single byte (> + ~ * . # = etc.)
)

type token struct {
	kind tokKind
	text string
}

// isWordByte reports whether b continues an unquoted "word" (identifier,
// number, dimension, hash, at-keyword, !important, sentinel). It deliberately
// includes everything that is not whitespace, a string/comment opener, or one
// of the structural punctuation bytes handled separately — so values like
// "12px", "#fff", "@media", "!important", "translateX" stay single tokens.
func isWordByte(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f', '"', '\'', '{', '}', '(', ')', ':', ';', ',', '.':
		return false
	}
	return true
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f':
		return true
	}
	return false
}

// tokenize splits CSS source into a flat token stream. It is total except for
// unterminated strings and comments, which return an error (Task 4's parser
// turns that into a verbatim fallback).
func tokenize(src []byte) ([]token, error) {
	s := string(src)
	var toks []token
	i := 0
	for i < len(s) {
		b := s[i]
		switch {
		case isSpaceByte(b):
			j := i + 1
			for j < len(s) && isSpaceByte(s[j]) {
				j++
			}
			toks = append(toks, token{tWS, s[i:j]})
			i = j
		case b == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated comment")
			}
			j := i + 2 + end + 2
			toks = append(toks, token{tComment, s[i:j]})
			i = j
		case b == '"' || b == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					j += 2
					continue
				}
				if s[j] == b {
					j++
					break
				}
				if s[j] == '\n' {
					return nil, fmt.Errorf("unterminated string")
				}
				j++
			}
			if j > len(s) || (j <= len(s) && s[j-1] != b) {
				return nil, fmt.Errorf("unterminated string")
			}
			toks = append(toks, token{tString, s[i:j]})
			i = j
		case b == '{':
			toks = append(toks, token{tLBrace, "{"})
			i++
		case b == '}':
			toks = append(toks, token{tRBrace, "}"})
			i++
		case b == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case b == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case b == ':':
			toks = append(toks, token{tColon, ":"})
			i++
		case b == ';':
			toks = append(toks, token{tSemi, ";"})
			i++
		case b == ',':
			toks = append(toks, token{tComma, ","})
			i++
		case isWordByte(b):
			j := i + 1
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			toks = append(toks, token{tWord, s[i:j]})
			i = j
		default:
			toks = append(toks, token{tDelim, s[i : i+1]})
			i++
		}
	}
	return toks, nil
}
