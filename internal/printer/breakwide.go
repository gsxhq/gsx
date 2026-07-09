package printer

import (
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"sort"
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
		if compositeLitFullyBroken(fset, lit) {
			return true
		}
		target = lit
		return false
	})
	if target == nil {
		return src, false
	}

	// Insert a newline before every element that still shares its line with
	// whatever precedes it: the `{` for element 0, the previous element for
	// everyone else. An element that already starts its own line (this literal
	// may be PARTIALLY broken -- some elements one-per-line, others not) is left
	// alone, or a blank line would appear before it. gofmt supplies the
	// indentation, the alignment, and (with blockFormBraces) the closing brace.
	// A comma already separates interior elements, and `{` precedes the first,
	// so no comma is inserted here and automatic semicolon insertion cannot fire.
	var offsets []int
	firstPos := fset.Position(target.Elts[0].Pos())
	if firstPos.Line == fset.Position(target.Lbrace).Line {
		offsets = append(offsets, firstPos.Offset)
	}
	prevLine := firstPos.Line
	for _, elt := range target.Elts[1:] {
		pos := fset.Position(elt.Pos())
		if pos.Line == prevLine {
			offsets = append(offsets, pos.Offset)
		}
		prevLine = pos.Line
	}
	if len(offsets) == 0 {
		// No element actually shares a line with its predecessor: nothing to
		// insert. Report no progress rather than claim a no-op round succeeded --
		// the caller's loop terminates on changed=false, and a false "changed"
		// here would spin forever reprocessing the identical source.
		return src, false
	}
	// Apply right to left (largest offset first). Every offset was measured
	// against the pre-mutation src; inserting a "\n" at one offset shifts every
	// BYTE AFTER it, never before. Descending order guarantees each remaining
	// offset in the list still points at the same source position when its turn
	// comes, exactly as the single-offset special case did before this loop was
	// generalized.
	sort.Sort(sort.Reverse(sort.IntSlice(offsets)))
	out := src
	for _, off := range offsets {
		// Replace the run of spaces/tabs before the element with a newline.
		start := off
		for start > 0 && (out[start-1] == ' ' || out[start-1] == '\t') {
			start--
		}
		out = out[:start] + "\n" + out[off:]
	}
	return blockFormBraces(out), true
}

// compositeLitFullyBroken reports whether lit's elements are already one per
// line: the first element sits off the `{` line, and each later element sits
// on a later line than the one before it.
//
// An earlier version of this check compared only the FIRST and LAST element's
// lines, on the theory that "last on a later line than first" meant "already
// broken". It doesn't: a literal the author broke PARTIALLY -- elements 0 and
// 1 still sharing the `{` line, element 2 alone on the next -- also satisfies
// "last > first", so that check mistook it for fully broken and skipped it
// forever, leaving its over-long line unfixed.
func compositeLitFullyBroken(fset *gotoken.FileSet, lit *goast.CompositeLit) bool {
	prevLine := fset.Position(lit.Elts[0].Pos()).Line
	if prevLine <= fset.Position(lit.Lbrace).Line {
		return false
	}
	for _, elt := range lit.Elts[1:] {
		line := fset.Position(elt.Pos()).Line
		if line <= prevLine {
			return false
		}
		prevLine = line
	}
	return true
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
