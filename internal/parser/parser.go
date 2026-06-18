package parser

import (
	"go/token"
	"strings"
)

type parser struct {
	src string
	i   int // byte cursor
}

func newParser(src string) *parser { return &parser{src: src} }

func (p *parser) eof() bool { return p.i >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.i]
}

func (p *parser) at(prefix string) bool {
	return strings.HasPrefix(p.src[p.i:], prefix)
}

func (p *parser) skipSpace() {
	for !p.eof() {
		switch p.src[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

// pos returns a 1-based line/column for the current cursor.
func (p *parser) pos() token.Position {
	line, col := 1, 1
	for j := 0; j < p.i && j < len(p.src); j++ {
		if p.src[j] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return token.Position{Line: line, Column: col, Offset: p.i}
}
