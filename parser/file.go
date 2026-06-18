// parser/file.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// Mode controls optional parser features. Currently a no-op (future parity with go/parser).
type Mode uint

// ParseFile parses a .gsx source file.
//
// fset is the token.FileSet to record positions in.
// filename is used for error messages and position recording.
// src may be nil (read filename via os.ReadFile), a string, or a []byte.
// mode is reserved for future use; pass 0.
func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error) {
	var srcBytes []byte
	switch v := src.(type) {
	case nil:
		b, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		srcBytes = b
	case string:
		srcBytes = []byte(v)
	case []byte:
		srcBytes = v
	default:
		return nil, fmt.Errorf("parser.ParseFile: invalid src type %T", src)
	}

	file := fset.AddFile(filename, fset.Base(), len(srcBytes))
	srcStr := string(srcBytes)

	pkgName, pkgPos, pkgEnd, err := scanPackage(file, srcBytes)
	if err != nil {
		return nil, err
	}

	offsets := topLevelComponentOffsets(srcBytes)

	f := &ast.File{
		Span:    ast.Span{Start: pkgPos, Finish: file.Pos(len(srcBytes))},
		Package: pkgName,
	}

	cursor := pkgEnd
	p := newParser(file, srcStr)
	for _, off := range offsets {
		if off < cursor {
			continue
		}
		if chunk := strings.TrimSpace(srcStr[cursor:off]); chunk != "" {
			chunkStart := file.Pos(cursor)
			chunkEnd := file.Pos(off)
			f.Decls = append(f.Decls, &ast.GoChunk{
				Span: ast.Span{Start: chunkStart, Finish: chunkEnd},
				Src:  srcStr[cursor:off],
			})
		}
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			return nil, err
		}
		f.Decls = append(f.Decls, c)
		cursor = p.i
	}
	if tail := strings.TrimSpace(srcStr[cursor:]); tail != "" {
		chunkStart := file.Pos(cursor)
		chunkEnd := file.Pos(len(srcStr))
		f.Decls = append(f.Decls, &ast.GoChunk{
			Span: ast.Span{Start: chunkStart, Finish: chunkEnd},
			Src:  srcStr[cursor:],
		})
	}
	return f, nil
}

// scanPackage finds the package clause. Returns the package name, position of the
// package name token (as token.Pos in the given file), and byte offset after the name.
func scanPackage(file *token.File, src []byte) (name string, pos token.Pos, end int, err error) {
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(src))
	var s scanner.Scanner
	s.Init(localFile, src, nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return "", token.NoPos, 0, fmt.Errorf("missing package clause")
		}
		if tok == token.PACKAGE {
			namePos, tok2, lit2 := s.Scan()
			if tok2 != token.IDENT {
				return "", token.NoPos, 0, fmt.Errorf("malformed package clause")
			}
			off := localFset.Position(namePos).Offset
			_ = lit
			// Map offset into our file
			return lit2, file.Pos(off), off + len(lit2), nil
		}
	}
}

// topLevelComponentOffsets returns byte offsets of `component` identifiers that sit
// at brace depth 0. Uses go/scanner so strings/comments/identifiers don't confuse it.
func topLevelComponentOffsets(src []byte) []int {
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(src))
	var s scanner.Scanner
	s.Init(localFile, src, nil, scanner.ScanComments)

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
				offs = append(offs, localFset.Position(pos).Offset)
			}
		}
	}
}
