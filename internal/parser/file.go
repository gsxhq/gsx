// internal/parser/file.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// Parse parses a full .gsx source file.
func Parse(src string) (*ast.File, error) {
	f := &ast.File{}

	// Find the package clause and its name with go/scanner.
	pkgName, pkgPos, pkgEnd, err := scanPackage(src)
	if err != nil {
		return nil, err
	}
	f.Package = pkgName
	f.PkgPos = pkgPos

	// Locate top-level `component` keyword offsets.
	offsets := topLevelComponentOffsets(src)

	cursor := pkgEnd
	for _, off := range offsets {
		if off < cursor {
			continue
		}
		if chunk := strings.TrimSpace(src[cursor:off]); chunk != "" {
			f.Decls = append(f.Decls, &ast.GoChunk{Src: src[cursor:off]})
		}
		p := newParser(src)
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			return nil, err
		}
		f.Decls = append(f.Decls, c)
		cursor = p.i
	}
	if tail := strings.TrimSpace(src[cursor:]); tail != "" {
		f.Decls = append(f.Decls, &ast.GoChunk{Src: src[cursor:]})
	}
	return f, nil
}

func scanPackage(src string) (name string, pos token.Position, end int, err error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return "", token.Position{}, 0, fmt.Errorf("missing package clause")
		}
		if tok == token.PACKAGE {
			namePos, tok2, lit2 := s.Scan()
			if tok2 != token.IDENT {
				return "", token.Position{}, 0, fmt.Errorf("malformed package clause")
			}
			p := fset.Position(namePos)
			_ = lit
			return lit2, p, p.Offset + len(lit2), nil
		}
	}
}

// topLevelComponentOffsets returns byte offsets of `component` identifiers that sit
// at brace depth 0 (i.e. real top-level declarations, not inside a func/component body
// and not inside strings/comments — go/scanner handles those).
func topLevelComponentOffsets(src string) []int {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	var offs []int
	depth := 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return offs
		}
		switch tok {
		case token.LBRACE:
			depth++
		case token.RBRACE:
			if depth > 0 {
				depth--
			}
		case token.IDENT:
			if depth == 0 && lit == "component" {
				offs = append(offs, fset.Position(pos).Offset)
			}
		}
	}
}
