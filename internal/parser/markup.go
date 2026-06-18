package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	pos := p.pos()
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{`", pos.Line, pos.Column)
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	try := false
	if strings.HasSuffix(inner, "?") {
		try = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "?"))
	}
	p.i = end + 1
	return &ast.Interp{Expr: inner, Try: try, Pos: pos}, nil
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	pos := p.pos()
	start := p.i
	for !p.eof() && p.src[p.i] != '<' && p.src[p.i] != '{' {
		p.i++
	}
	return &ast.Text{Value: p.src[start:p.i], Pos: pos}
}
