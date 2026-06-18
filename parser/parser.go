package parser

import (
	"go/token"
	"strings"
)

type parser struct {
	file *token.File
	src  string
	base int // absolute byte offset of src[0] in file
	i    int // byte cursor within src
}

// newParser creates a parser for src at absolute offset 0 in file.
func newParser(file *token.File, src string) *parser {
	return &parser{file: file, src: src, base: 0}
}

// newSub creates a parser for a sub-slice of the parent's source.
// base is the absolute byte offset within file where sub starts.
func newSub(file *token.File, src string, base int) *parser {
	return &parser{file: file, src: src, base: base}
}

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

// pos returns the token.Pos of the current cursor position.
func (p *parser) pos() token.Pos {
	return p.file.Pos(p.base + p.i)
}

// posAt returns the token.Pos for a specific byte offset within p.src.
func (p *parser) posAt(off int) token.Pos {
	return p.file.Pos(p.base + off)
}
