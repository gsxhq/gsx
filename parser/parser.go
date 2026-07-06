package parser

import (
	"errors"
	"fmt"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// Error is a positioned parser diagnostic. Pos resolves to file:line:col via the
// FileSet the parser was created with. Exported so codegen can convert it to a
// diag.Diagnostic.
type Error struct {
	Pos token.Pos
	End token.Pos
	Msg string
}

type parser struct {
	file       *token.File
	src        string
	base       int // absolute byte offset of src[0] in file
	i          int // byte cursor within src
	classifier *attrclass.Classifier
	errs       []Error
}

// errorf records a positioned error and returns an error whose Error() returns
// the message text. Callers only check err != nil; the positioned errors are
// read from p.errs by the caller.
func (p *parser) errorf(pos token.Pos, format string, args ...any) error {
	return p.errorfRange(pos, token.NoPos, format, args...)
}

func (p *parser) errorfRange(pos, end token.Pos, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	p.errs = append(p.errs, Error{Pos: pos, End: end, Msg: msg})
	return fmt.Errorf("%s", msg)
}

func (p *parser) pipeErrorf(fallback token.Pos, err error) error {
	var pe pipeError
	if errors.As(err, &pe) && pe.pos.IsValid() {
		return p.errorfRange(pe.pos, pe.end, "%s", pe.msg)
	}
	return p.errorf(fallback, "%v", err)
}

// newParser creates a parser for src at absolute offset 0 in file.
func newParser(file *token.File, src string) *parser {
	return &parser{file: file, src: src, base: 0, classifier: attrclass.Builtin()}
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

// newlineFollows reports whether the whitespace run in src starting at off
// contains a line break before the next non-whitespace byte — i.e. the author
// placed the following content on its own line. The formatter uses this to keep
// a body block-formatted when the source did (see ast.*Multiline fields).
func newlineFollows(src string, off int) bool {
	for off < len(src) {
		switch src[off] {
		case '\n', '\r':
			return true
		case ' ', '\t':
			off++
		default:
			return false
		}
	}
	return false
}

// pos returns the token.Pos of the current cursor position.
func (p *parser) pos() token.Pos {
	return p.file.Pos(p.base + p.i)
}

// posAt returns the token.Pos for a specific byte offset within p.src.
func (p *parser) posAt(off int) token.Pos {
	return p.file.Pos(p.base + off)
}
