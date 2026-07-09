package printer

import (
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"strings"
	"unicode/utf8"
)

// breakWideLiterals returns src with the fields of over-long composite literals
// broken one per line, until no line exceeds width (or no further progress is
// possible).
//
// gofmt never breaks a long line: go/printer copies the breaks between a
// literal's elements from the source and invents none (go/printer/nodes.go,
// exprList). So an over-long `{a: 1, b: 2, …}` stays over-long. prettier, faced
// with the same object literal, breaks its properties — and that, not wrapping
// the element that happens to sit inside it, is the remedy the line needs.
//
// Each round gofmt's the source, finds the first over-long line, and breaks the
// OUTERMOST composite literal on it. A nested literal is only reached on a later
// round, and only if its own line is still over budget after the outer break —
// which converges on the fewest breaks that bring every line under the limit.
//
// Termination is on NO PROGRESS, never on a round count: a single field wider
// than the budget cannot be fixed by breaking, and must not loop forever.
//
// The output is a gofmt FIXED POINT. gofmt preserves the breaks this pass adds,
// so re-running the pass on its own output is a no-op, and gsx fmt extends gofmt
// without ever fighting it. See TestBreakWideLiteralsOutputIsGofmtFixedPoint.
//
// src must be a complete Go file. On any parse or format error it is returned
// unchanged: this is a layout nicety, never a reason for gsx fmt to fail.
func breakWideLiterals(src string, width, tabWidth int) string {
	for {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			return src
		}
		next, changed := breakFirstWideLiteral(string(formatted), width, tabWidth)
		if !changed {
			return string(formatted)
		}
		src = next
	}
}

// breakFirstWideLiteral finds the first line of src exceeding width and breaks
// the outermost composite literal that starts on it, returning changed=false
// when there is no such line or no literal to break (no progress).
func breakFirstWideLiteral(src string, width, tabWidth int) (string, bool) {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "", src, goparser.SkipObjectResolution)
	if err != nil {
		return src, false
	}
	badLine := firstWideLine(src, width, tabWidth)
	if badLine == 0 {
		return src, false
	}

	// The outermost literal whose opening brace is on badLine and whose fields
	// are not already broken. goast.Inspect visits parents before children, so
	// the first match is the outermost.
	var target *goast.CompositeLit
	goast.Inspect(file, func(n goast.Node) bool {
		if target != nil {
			return false
		}
		lit, ok := n.(*goast.CompositeLit)
		// An outer `[]T{{…}}` has exactly ONE element (the inner literal), and
		// breaking it is precisely the outermost-first case. Only an empty
		// literal has nothing to break.
		if !ok || lit.Incomplete || len(lit.Elts) == 0 {
			return true
		}
		if fset.Position(lit.Lbrace).Line != badLine {
			return true
		}
		// Already one-per-line? Then breaking it again is not progress.
		first := fset.Position(lit.Elts[0].Pos()).Line
		last := fset.Position(lit.Elts[len(lit.Elts)-1].Pos()).Line
		if last > first {
			return true
		}
		target = lit
		return false
	})
	if target == nil {
		return src, false
	}

	// Insert a newline before every element after the first. gofmt supplies the
	// indentation, the alignment, and (with blockFormBraces) the closing brace.
	// A comma already separates the elements, so no comma is inserted here and
	// automatic semicolon insertion cannot fire.
	offsets := make([]int, 0, len(target.Elts)-1)
	for _, elt := range target.Elts[1:] {
		offsets = append(offsets, fset.Position(elt.Pos()).Offset)
	}
	out := src
	for i := len(offsets) - 1; i >= 0; i-- {
		off := offsets[i]
		// Replace the run of spaces before the element with a newline.
		start := off
		for start > 0 && (out[start-1] == ' ' || out[start-1] == '\t') {
			start--
		}
		out = out[:start] + "\n" + out[off:]
	}
	// The first element must also start its own line, and the brace must close on
	// one. blockFormBraces does the latter; do the former here.
	firstOff := fset.Position(target.Elts[0].Pos()).Offset
	start := firstOff
	for start > 0 && (out[start-1] == ' ' || out[start-1] == '\t') {
		start--
	}
	if start > 0 && out[start-1] == '{' {
		out = out[:start] + "\n" + out[firstOff:]
	}
	return blockFormBraces(out), true
}

// firstWideLine returns the 1-based number of the first line of src wider than
// width, or 0. A tab counts as tabWidth columns, matching internal/pretty.
func firstWideLine(src string, width, tabWidth int) int {
	for i, line := range strings.Split(src, "\n") {
		cols := utf8.RuneCountInString(line) + (tabWidth-1)*strings.Count(line, "\t")
		if cols > width {
			return i + 1
		}
	}
	return 0
}
