package sourceview

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
)

// goFileHeader returns the bytes of src from offset 0 through the end of its
// last import declaration, or through the package clause when it has none.
// That prefix holds everything go list reads from a Go file (build
// constraints, package clause, imports, cgo preamble), so a body-only edit
// leaves it byte-identical. It is a raw prefix, not a reconstruction, so every
// leading comment is included. If the header does not parse, the whole of src
// is returned: any doubt is a miss.
func goFileHeader(src []byte) []byte {
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return src
	}
	end := file.Name.End()
	for _, decl := range file.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT && gd.End() > end {
			end = gd.End()
		}
	}
	// Positions are offset by the file base; a fresh FileSet starts at 1.
	off := int(end) - 1
	if off < 0 || off > len(src) {
		return src
	}
	return src[:off]
}

// goEmbedLines returns every line of src whose trimmed text starts with
// "//go:embed", in order. //go:embed lines live below the imports, so they are
// hashed separately from the header. Lines are sub-slices of src.
func goEmbedLines(src []byte) [][]byte {
	var lines [][]byte
	for line := range bytes.SplitSeq(src, []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("//go:embed")) {
			lines = append(lines, line)
		}
	}
	return lines
}
