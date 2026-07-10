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
// Each round gofmt's the source, then breaks EVERY outermost composite literal
// that is over budget. A nested literal is only reached on a later round, and
// only if its own line is still over budget after the outer break — which
// converges on the fewest breaks that bring every line under the limit.
//
// Breaking every eligible literal per round (not one) is what keeps the pass
// linear. gofmt is re-run once per ROUND, and rounds are bounded by nesting
// DEPTH, not by the literal count: N over-budget sibling literals all break in
// a single round, so a literal-heavy file costs a handful of gofmt passes, not
// one per literal. Breaking a single literal per round made this pass quadratic
// (N gofmt passes over the whole region for N literals).
//
// Termination is on NO PROGRESS, never on a round count: a single field wider
// than the budget cannot be fixed by breaking, and must not loop forever.
//
// The output is a gofmt FIXED POINT. gofmt preserves the breaks this pass adds,
// so re-running the pass on its own output is a no-op, and gsx fmt extends gofmt
// without ever fighting it. See TestBreakWideLiteralsOutputIsGofmtFixedPoint.
//
// forceMarker names a placeholder substring that stands for a gsx value whose
// real rendered width is unknowable (a multi-line element reaches gofmt as a
// single-rune placeholder — see fmtGoExprParts). A line holding it is treated as
// over budget regardless of its measured width: the value forces a break and can
// never be a one-liner, so the literal around it must break its fields exactly as
// a genuinely over-long one would. forceMarker == "" disables this (the pure-Go
// path, fmtGoChunk, has no holes and passes "").
//
// src must be a complete Go file. On any parse or format error it is returned
// unchanged: this is a layout nicety, never a reason for gsx fmt to fail.
func breakWideLiterals(src string, width, tabWidth int, forceMarker string) string {
	for {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			return src
		}
		next, changed := breakWideLiteralsOnce(string(formatted), width, tabWidth, forceMarker)
		if !changed {
			return string(formatted)
		}
		src = next
	}
}

// breakWideLiteralsOnce breaks, in a single AST walk, every outermost composite
// literal that is over budget and not already fully broken, returning
// changed=false when there is no such literal (no progress). One call breaks all
// eligible SIBLINGS at once; nesting is descended one level per call by the
// caller's loop, so N over-budget sibling literals cost one round, not N.
func breakWideLiteralsOnce(src string, width, tabWidth int, forceMarker string) (string, bool) {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "", src, goparser.SkipObjectResolution)
	if err != nil {
		return src, false
	}
	lines := strings.Split(src, "\n")

	// lineOverBudget reports whether source line ln (1-based) is over budget:
	// measured wider than width, or -- when forceMarker is set -- holding the
	// marker. The marker stands for a multi-line gsx value whose real width is
	// unknowable and which can never be a one-liner (it forces a break), so a
	// literal still holding it must break its fields regardless of the line's
	// measured width.
	lineOverBudget := func(ln int) bool {
		if ln < 1 || ln > len(lines) {
			return false
		}
		line := lines[ln-1]
		cols := utf8.RuneCountInString(line) + (tabWidth-1)*strings.Count(line, "\t")
		if cols > width {
			return true
		}
		return forceMarker != "" && strings.Contains(line, forceMarker)
	}

	// litOverBudget reports whether ANY line lit spans -- from its opening brace
	// through its last element's end -- is over budget. Keying on the brace line
	// alone (an earlier version's mistake) skipped a literal the author partially
	// broke with the `{` on its own line but two fields packed onto one over-long
	// field line: that line then stayed over budget forever. Spanning every line
	// still converges, because the "already fully broken" guard (below) skips a
	// literal whose fields are one-per-line -- so an over-budget line that no
	// remaining break can shorten (a fully broken literal, or a single field
	// wider than the budget) never re-triggers a break.
	litOverBudget := func(lit *goast.CompositeLit) bool {
		start := fset.Position(lit.Lbrace).Line
		end := fset.Position(lit.Elts[len(lit.Elts)-1].End()).Line
		for ln := start; ln <= end; ln++ {
			if lineOverBudget(ln) {
				return true
			}
		}
		return false
	}

	// Collect every outermost over-budget literal whose fields are not already
	// broken. goast.Inspect visits parents before children; when a literal is
	// accepted we return false to SKIP its descendants, because breaking the
	// parent's fields may bring the children back under budget -- descending in
	// the same round is exactly what would over-break. A skipped literal (under
	// budget, or already fully broken) returns true so a child that still needs
	// breaking is reached: an outer `[]T{{…}}` already broken one-per-line is
	// itself fully broken, and its inner literal is the next round's target.
	var targets []*goast.CompositeLit
	goast.Inspect(file, func(n goast.Node) bool {
		lit, ok := n.(*goast.CompositeLit)
		if !ok || lit.Incomplete || len(lit.Elts) == 0 {
			return true
		}
		if !litOverBudget(lit) {
			return true
		}
		// Already one-per-line? Then breaking it again is not progress; descend
		// to reach a nested literal that may still need breaking.
		if compositeLitFullyBroken(fset, lit) {
			return true
		}
		targets = append(targets, lit)
		return false
	})

	// Collect the insertion offset before every element that still shares its
	// line with whatever precedes it: the `{` for element 0, the previous element
	// for everyone else. An element that already starts its own line (a literal
	// may be PARTIALLY broken -- some elements one-per-line, others not) is left
	// alone, or a blank line would appear before it. gofmt supplies the
	// indentation, the alignment, and (with blockFormBraces) the closing brace.
	// A comma already separates interior elements, and `{` precedes the first,
	// so no comma is inserted here and automatic semicolon insertion cannot fire.
	//
	// Offsets are gathered across ALL targets. Targets are never nested in one
	// another (an accepted literal's descendants are skipped), so their offsets
	// are disjoint positions in the single pre-mutation src.
	var offsets []int
	for _, target := range targets {
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
	// comes. This is the same argument blockFormBraces relies on.
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
