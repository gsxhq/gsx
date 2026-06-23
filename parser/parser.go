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
	Msg string
}

// errParse is the sentinel identity for all parser errors. errorf returns a
// *parseErr that wraps this sentinel so errors.Is(err, errParse) is true, yet
// err.Error() still returns the message text (needed by internal tests that
// call parser methods directly).
var errParse = errors.New("parse error")

// parseErr is a lightweight wrapper returned by errorf. It satisfies
// errors.Is(err, errParse) via its Unwrap method, while still exposing the
// human-readable message through Error().
type parseErr struct{ msg string }

func (e *parseErr) Error() string  { return e.msg }
func (e *parseErr) Unwrap() error  { return errParse }

type parser struct {
	file       *token.File
	src        string
	base       int // absolute byte offset of src[0] in file
	i          int // byte cursor within src
	classifier *attrclass.Classifier
	errs       []Error
}

// errorf records a positioned error and returns a *parseErr whose Error()
// returns the message text; errors.Is(err, errParse) is true via Unwrap.
func (p *parser) errorf(pos token.Pos, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	p.errs = append(p.errs, Error{Pos: pos, Msg: msg})
	return &parseErr{msg: msg}
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

// pos returns the token.Pos of the current cursor position.
func (p *parser) pos() token.Pos {
	return p.file.Pos(p.base + p.i)
}

// posAt returns the token.Pos for a specific byte offset within p.src.
func (p *parser) posAt(off int) token.Pos {
	return p.file.Pos(p.base + off)
}
