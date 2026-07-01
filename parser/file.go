// parser/file.go
package parser

import (
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
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
	f, errs := ParseFileWithClassifier(fset, filename, src, mode, attrclass.Builtin())
	if len(errs) > 0 {
		e := errs[0]
		pos := fset.Position(e.Pos)
		if pos.IsValid() {
			return f, fmt.Errorf("%d:%d: %s", pos.Line, pos.Column, e.Msg)
		}
		return f, errors.New(e.Msg)
	}
	return f, nil
}

// ParseFileWithClassifier parses a .gsx source file. cls is retained for callers
// that share parser/codegen setup; the parser currently does not consult it.
// A nil cls means built-ins only.
func ParseFileWithClassifier(fset *token.FileSet, filename string, src any, mode Mode, cls *attrclass.Classifier) (*ast.File, []Error) {
	if cls == nil {
		cls = attrclass.Builtin()
	}

	var srcBytes []byte
	switch v := src.(type) {
	case nil:
		b, err := os.ReadFile(filename)
		if err != nil {
			return nil, []Error{{Pos: token.NoPos, Msg: err.Error()}}
		}
		srcBytes = b
	case string:
		srcBytes = []byte(v)
	case []byte:
		srcBytes = v
	default:
		return nil, []Error{{Pos: token.NoPos, Msg: fmt.Sprintf("parser.ParseFile: invalid src type %T", src)}}
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
		return nil, []Error{{Pos: pkgKwPos, Msg: err.Error()}}
	}

	f := &ast.File{
		Package: pkgName,
	}
	// Capture the doc-comment block preceding `package` (everything before the
	// keyword, sans surrounding whitespace) so formatting can preserve it.
	if kwOff := file.Offset(pkgKwPos); kwOff > 0 {
		if doc := strings.TrimSpace(srcStr[:kwOff]); doc != "" {
			f.Doc = doc
		}
	}
	ast.SetSpan(f, pkgKwPos, file.Pos(len(srcBytes)))

	cursor := pkgEnd
	p := newParser(file, srcStr)
	p.classifier = cls
	for {
		off, found := nextTopLevelComponent(srcStr, cursor)
		if !found {
			break
		}
		if chunk := strings.TrimSpace(srcStr[cursor:off]); chunk != "" {
			gc := &ast.GoChunk{Src: srcStr[cursor:off]}
			ast.SetSpan(gc, file.Pos(cursor), file.Pos(off))
			f.Decls = append(f.Decls, gc)
		}
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			// Error already recorded in p.errs. Resync strictly past this component's
			// `component` keyword so the next scan can't re-match it (forward progress),
			// skip the broken component, and continue.
			resyncFrom := max(off+len("component"), p.i)
			cursor = resyncFrom
			continue
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
	return f, p.errs
}

// scanPackage finds the package clause. Returns the package name, position of the
// `package` keyword token (as token.Pos in the given file), and byte offset after
// the package name (used to advance the cursor past the package clause).
// On error, pkgKwPos may be token.NoPos (if the package keyword was never found).
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
			nameOff := localFset.Position(namePos).Offset
			if tok2 != token.IDENT || localFset.Position(namePos).Line != localFset.Position(pos).Line {
				return "", mappedKwPos, 0, fmt.Errorf("malformed package clause")
			}
			return lit2, mappedKwPos, nameOff + len(lit2), nil
		}
	}
}

// nextTopLevelComponent returns the byte offset of the next `component`
// identifier at brace depth 0 at or after `from`, scanning Go tokens over
// src[from:]. The region [from, returned offset) is a pure-Go gap: component
// bodies (which contain markup) begin after the `component` keyword and are
// consumed by parseComponent, never by this scan. found is false if there is no
// further top-level component.
func nextTopLevelComponent(src string, from int) (int, bool) {
	sub := src[from:]
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(localFile, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return 0, false
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
				return from + localFset.Position(pos).Offset, true
			}
		}
	}
}
