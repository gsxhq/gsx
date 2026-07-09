package printer

import (
	goast "go/ast"
	goparser "go/parser"
	goscanner "go/scanner"
	gotoken "go/token"
	"sort"
)

// blockFormBraces returns src with a line break inserted before the closing
// brace of every composite literal whose opening brace is followed by a line
// break — so a literal the author chose to write in block form closes in block
// form too.
//
// gofmt does not do this. go/printer's exprList (go/printer/nodes.go) makes
// three independent decisions from the ORIGINAL source positions:
//
//   - all entries print on one line iff the `{` line, the first element's line,
//     and the last element's end line are the same (nodes.go:145);
//   - line breaks BETWEEN elements are copied from the source, never invented;
//   - the `}` gets its own line, and a terminating comma, iff the source already
//     put it on a line after the last element (nodes.go:294).
//
// So gofmt honours a break after `{` but never propagates it to the matching
// `}`, and `{\n\ta: 1}` stays welded shut. This pass supplies only the missing
// third break; it never touches the second, so fields the author packed onto one
// line stay packed. gofmt then owns everything downstream — the terminating
// comma, the indentation, the key alignment.
//
// The result is a gofmt FIXED POINT: once `}` sits on its own line, rule three
// above reproduces it. So this pass adds a rule on top of gofmt without ever
// fighting it, and running it on its own output is a no-op.
//
// The inserted text is ",\n", not "\n", unless a terminating comma is already
// present. A bare newline before `}` would let Go's automatic semicolon
// insertion put a `;` after the last element, and the literal would stop parsing.
//
// src must be a complete Go file. On any parse error it is returned unchanged:
// this is a layout nicety, never a reason for gsx fmt to fail.
func blockFormBraces(src string) string {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "", src, goparser.SkipObjectResolution)
	if err != nil {
		return src
	}

	type insertion struct {
		offset int
		text   string
	}
	var inserts []insertion

	goast.Inspect(file, func(n goast.Node) bool {
		lit, ok := n.(*goast.CompositeLit)
		if !ok || lit.Incomplete || len(lit.Elts) == 0 {
			return true
		}
		lbrace := fset.Position(lit.Lbrace)
		first := fset.Position(lit.Elts[0].Pos())
		if lbrace.Line == first.Line {
			return true // author kept `{` and the first element together
		}
		lastEnd := fset.Position(lit.Elts[len(lit.Elts)-1].End())
		rbrace := fset.Position(lit.Rbrace)
		if rbrace.Line > lastEnd.Line {
			return true // `}` already opens its own line
		}
		text := ",\n"
		if hasCommaToken(src[lastEnd.Offset:rbrace.Offset]) {
			text = "\n"
		}
		inserts = append(inserts, insertion{offset: rbrace.Offset, text: text})
		return true
	})
	if len(inserts) == 0 {
		return src
	}

	// Apply right to left so each insertion's offset stays valid: an edit only
	// shifts text after it, and a nested literal's `}` always precedes its
	// parent's.
	sort.Slice(inserts, func(i, j int) bool { return inserts[i].offset > inserts[j].offset })
	out := src
	for _, in := range inserts {
		out = out[:in.offset] + in.text + out[in.offset:]
	}
	return out
}

// hasCommaToken reports whether src contains a comma as a real Go token — not
// one inside a string literal, rune literal, or comment. The span between a
// composite literal's last element and its closing brace is short, but it can
// hold a /*…*/ comment, and a comma inside one is not a terminating comma.
func hasCommaToken(src string) bool {
	fset := gotoken.NewFileSet()
	f := fset.AddFile("", fset.Base(), len(src))
	var sc goscanner.Scanner
	sc.Init(f, []byte(src), func(gotoken.Position, string) {}, goscanner.ScanComments)
	for {
		_, tok, _ := sc.Scan()
		switch tok {
		case gotoken.EOF:
			return false
		case gotoken.COMMA:
			return true
		}
	}
}
