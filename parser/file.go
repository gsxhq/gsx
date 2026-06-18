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
	// Register line offsets so that file.Position can resolve line/column correctly.
	// go/scanner does this automatically when scanning; our markup parser does not,
	// so we register all newlines here before any parsing begins.
	for i, b := range srcBytes {
		if b == '\n' {
			file.AddLine(i + 1)
		}
	}
	srcStr := string(srcBytes)

	pkgName, pkgKwPos, pkgEnd, err := scanPackage(file, srcBytes)
	if err != nil {
		return nil, err
	}

	offsets := topLevelComponentOffsets(srcBytes)

	f := &ast.File{
		Package: pkgName,
	}
	ast.SetSpan(f, pkgKwPos, file.Pos(len(srcBytes)))

	cursor := pkgEnd
	p := newParser(file, srcStr)
	for _, off := range offsets {
		if off < cursor {
			continue
		}
		if chunk := strings.TrimSpace(srcStr[cursor:off]); chunk != "" {
			chunkStart := file.Pos(cursor)
			chunkEnd := file.Pos(off)
			gc := &ast.GoChunk{Src: srcStr[cursor:off]}
			ast.SetSpan(gc, chunkStart, chunkEnd)
			f.Decls = append(f.Decls, gc)
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
		gc := &ast.GoChunk{Src: srcStr[cursor:]}
		ast.SetSpan(gc, chunkStart, chunkEnd)
		f.Decls = append(f.Decls, gc)
	}
	return f, nil
}

// scanPackage finds the package clause. Returns the package name, position of the
// `package` keyword token (as token.Pos in the given file), and byte offset after
// the package name (used to advance the cursor past the package clause).
func scanPackage(file *token.File, src []byte) (name string, kwPos token.Pos, end int, err error) {
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(src))
	var s scanner.Scanner
	s.Init(localFile, src, nil, 0)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return "", token.NoPos, 0, fmt.Errorf("missing package clause")
		}
		if tok == token.PACKAGE {
			kwOff := localFset.Position(pos).Offset
			mappedKwPos := file.Pos(kwOff)
			_ = lit
			namePos, tok2, lit2 := s.Scan()
			if tok2 != token.IDENT {
				return "", token.NoPos, 0, fmt.Errorf("malformed package clause")
			}
			nameOff := localFset.Position(namePos).Offset
			return lit2, mappedKwPos, nameOff + len(lit2), nil
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
