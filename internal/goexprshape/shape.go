// Package goexprshape classifies where a gsx value sits within the Go
// expression it was substituted into, at a GoWithElements decl's embedding
// point (e.g. an assignment RHS vs. a call argument). Shared by internal/printer
// (decides whether to visually wrap a value in decorative parens) and
// internal/codegen (decides whether to strip a decorative paren before
// splicing the value's lowered form into the generated .x.go — see each
// package's own doc for why the two decisions are independent).
package goexprshape

import (
	goast "go/ast"
	goparser "go/parser"
	goscanner "go/scanner"
	gotoken "go/token"
	"strings"
)

// Shape classifies a gsx value's position in the surrounding Go expression.
type Shape int

const (
	// Plain covers a call argument, a keyless composite-literal element, an
	// unrecognized position, or a substituted source that failed to parse.
	Plain Shape = iota
	// ParenWrap is an assignment RHS, a return operand, or a keyed
	// composite-literal field's value — a "prefix: value" shape safe to
	// visually wrap in (...) without changing its meaning.
	ParenWrap
)

// Hole is one placeholder's byte range [Start, End) within the src passed to
// Classify — Start..End is the placeholder identifier itself, exclusive of any
// surrounding whitespace.
type Hole struct {
	Start, End int
}

// Result is one hole's classification.
type Result struct {
	// Shape is the position's kind, independent of whether it currently has a
	// decorative paren around it — this answers "would wrapping this value in
	// (...) be safe," not "is it wrapped right now."
	Shape Shape
	// Wrapped reports whether the value, AS GIVEN in src, is actually sitting
	// inside a real *ast.ParenExpr — i.e. whether there is a decorative paren
	// immediately around THIS hole to strip. A GoText run immediately after a
	// ParenWrap-shaped hole is NOT necessarily wrapping it: e.g. a `var (…)`
	// group's own closing paren can immediately follow an unwrapped value with
	// no relation to it at all. Only Wrapped, never Shape alone, licenses
	// stripping — see StripLeadingParen/StripTrailingParen.
	Wrapped bool
}

// Classify reports, for each hole in order, its Result within src — a
// syntactically-complete Go source (the caller's responsibility to complete,
// e.g. with a synthetic package clause) in which each gsx value has been
// replaced by a placeholder identifier occupying its Hole range. A hole whose
// position isn't found among the recognized shapes (including when src fails
// to parse) keeps the zero value (Shape: Plain, Wrapped: false).
//
// Before parsing, the whitespace run immediately touching each hole is
// collapsed to a single space when it sits directly inside an open bracket:
// before the hole, when the nearest non-whitespace byte is "(", "[", or "{";
// after the hole, when it is ")", "]", or "}". src is real, author-written
// GoText concatenated around one-rune placeholders, and on a re-parse of gsx
// fmt's OWN previous output (or any multi-line bare composite-literal element,
// with no decorative paren involved at all) a placeholder can land alone on
// its own line directly inside a bracket — exactly the shape that trips Go's
// automatic semicolon insertion right after the placeholder, breaking the
// parse this function depends on, regardless of whether that bracket turns
// out to be decorative or load-bearing call/list syntax.
//
// This is deliberately narrower than "collapse any newline touching the
// hole": a hole followed by a newline that is NOT bracket-adjacent is a real
// statement separator (e.g. `n := HOLE` immediately followed by a new
// top-level statement on the next line) that Go's own automatic semicolon
// insertion requires to parse at all — collapsing it would break the parse
// this function depends on in the other direction.
func Classify(src string, holes []Hole) []Result {
	results := make([]Result, len(holes))
	sanitized, sanHoles := Sanitize(src, holes)
	offsets := make([]int, len(sanHoles))
	for i, h := range sanHoles {
		offsets[i] = h.Start
	}
	fset := gotoken.NewFileSet()
	f, err := goparser.ParseFile(fset, "", sanitized, 0)
	if err != nil {
		return results
	}
	want := make(map[gotoken.Pos]int, len(offsets))
	for i, off := range offsets {
		want[f.Pos()+gotoken.Pos(off)] = i
	}
	mark := func(e goast.Expr, shape Shape) {
		wrapped := false
		for {
			pe, ok := e.(*goast.ParenExpr)
			if !ok {
				break
			}
			wrapped = true
			e = pe.X
		}
		if i, ok := want[e.Pos()]; ok {
			results[i] = Result{Shape: shape, Wrapped: wrapped}
		}
	}
	goast.Inspect(f, func(n goast.Node) bool {
		switch v := n.(type) {
		case *goast.ValueSpec:
			for _, val := range v.Values {
				mark(val, ParenWrap)
			}
		case *goast.AssignStmt:
			for _, rhs := range v.Rhs {
				mark(rhs, ParenWrap)
			}
		case *goast.ReturnStmt:
			for _, r := range v.Results {
				mark(r, ParenWrap)
			}
		case *goast.CompositeLit:
			for _, elt := range v.Elts {
				if kv, ok := elt.(*goast.KeyValueExpr); ok {
					mark(kv.Value, ParenWrap)
				}
			}
		}
		return true
	})
	return results
}

// Sanitize returns src with every bracket-adjacent newline touching a hole
// collapsed to a single space, plus each hole's range within the returned
// string (same order as holes; the placeholder text itself is never altered,
// only the whitespace around it).
//
// A hole alone on its own line directly inside a bracket — `(\nHOLE\n)`, the
// shape gsx fmt's own decorative-paren output takes — makes Go's automatic
// semicolon insertion place a semicolon after the placeholder identifier, so
// the source no longer parses. Every consumer that hands the substituted
// source to go/parser or go/format must sanitize it first, or that consumer
// silently fails on any file gsx fmt has already formatted once. Classify does
// this internally; internal/printer calls it directly, because go/format needs
// the same treatment for the same reason.
//
// Sanitize is idempotent: its output has no newline left adjacent to a hole's
// brackets, so re-running it is a no-op.
func Sanitize(src string, holes []Hole) (string, []Hole) {
	sanitized, offsets := collapseHoleWhitespace(src, holes)
	out := make([]Hole, len(holes))
	for i, h := range holes {
		out[i] = Hole{Start: offsets[i], End: offsets[i] + (h.End - h.Start)}
	}
	return sanitized, out
}

// isSpace reports whether b is Go source whitespace (the same set go/scanner
// treats as insignificant between tokens).
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// collapseHoleWhitespace returns a copy of src where whitespace runs touching a
// hole are collapsed to a single space, under two separate rules. Returns each
// hole's new Start offset in the returned string, in the same order as holes.
//
// AFTER a hole, whenever the next non-whitespace byte is a REAL (not inside a
// comment) closing bracket. This one is about parsing. Go's automatic semicolon
// insertion fires on the token that ENDS a line, so a placeholder identifier
// ending a line directly before a closing bracket picks up a semicolon and
// `(\nHOLE\n)` becomes `(HOLE;)`, which does not parse.
//
// BEFORE a hole, only when the hole is ALONE INSIDE PARENS — the previous
// non-whitespace byte is a real "(" and the next one is a real ")". This one is
// about layout, and it is deliberately narrow.
//
// A hole is presented to gofmt as a placeholder identifier exactly as wide as
// the value will render flat (see internal/printer's fmtGoExprParts). That is a
// promise that the value occupies one line. `(\n\tHOLE )` breaks the promise:
// the following key:value pair now starts a new SOURCE line, so go/printer emits
// an alignment cell for it (nodes.go:271) even though it joins the line back up
// — and gsx fmt's own decorative-paren output has exactly this shape, so the
// padding reappears and grows on every run. Collapsing restores the inline view.
//
// It must NOT extend to "(", "[" or "{" generally. go/printer reads the line
// break between a composite literal's "{" and its first element to decide
// whether the literal prints on one line (nodes.go:145), and the same break
// between a call's "(" and its first argument for the argument list. Erasing
// those drags the first element up onto the bracket line and silently reflows
// the author's source. A hole sitting alone between "(" and ")" has no list to
// lay out: it is a parenthesized single expression, and gofmt joins it either
// way.
//
// Processes holes in ascending Start order (left to right), tracking a
// cumulative shift so far. This is the opposite of the naive-looking
// right-to-left approach: an edit only ever touches text at or after its own
// hole's position, so an EARLIER (leftward) hole's edit shrinking the string
// shifts every position to its right — including any offset already recorded
// for a LATER (rightward) hole processed first. Left-to-right avoids this
// because each hole's own Start/End is adjusted by the shift accumulated so
// far before it is used, and once a hole's offset is recorded, only edits
// strictly to its right can ever happen afterward, which can't move it.
func collapseHoleWhitespace(src string, holes []Hole) (string, []int) {
	order := make([]int, len(holes))
	for i := range order {
		order[i] = i
	}
	// Simple insertion sort by Start ascending — holes is always small (one
	// entry per gsx value in a single GoWithElements decl).
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && holes[order[j-1]].Start > holes[order[j]].Start; j-- {
			order[j-1], order[j] = order[j], order[j-1]
		}
	}
	comments := commentByteSpans(src)
	offsets := make([]int, len(holes))
	s := src
	shift := 0
	for _, i := range order {
		start, end := holes[i].Start+shift, holes[i].End+shift
		after := end
		for after < len(s) && isSpace(s[after]) {
			after++
		}
		before := start
		for before > 0 && isSpace(s[before-1]) {
			before--
		}
		beforeWS, afterWS := s[before:start], s[end:after]

		closerOK := after < len(s) && !insideAny(comments, after-shift)
		collapseAfter := closerOK && isCloseBracket(s[after]) && containsNewline(afterWS)
		// Alone inside parens: "(" directly before and ")" directly after.
		//
		// The before-collapse this branch enables (see collapseHoleWhitespace's doc
		// for the alignment-drift it fixed, PR #62) is DEAD in production today:
		// internal/printer's breakWideLiterals runs format.Source an extra time over
		// the region, and that pass incidentally normalizes the same drift away.
		// Neutering this branch leaves internal/printer, internal/gsxfmt, and
		// internal/codegen all green; only goexprshape's OWN unit tests fail — so
		// those tests are now this branch's only guard. Do NOT remove it casually:
		// the normalization above is incidental, and if that second format.Source
		// ever goes away this branch becomes load-bearing again with zero
		// integration coverage to catch the regression.
		inParens := before > 0 && s[before-1] == '(' && !insideAny(comments, before-1-shift) &&
			closerOK && s[after] == ')'
		collapseBefore := inParens && containsNewline(beforeWS)

		if !collapseBefore && !collapseAfter {
			offsets[i] = start
			continue
		}
		newBefore, newAfter := beforeWS, afterWS
		if collapseBefore {
			newBefore = " "
		}
		if collapseAfter {
			newAfter = " "
		}
		s = s[:before] + newBefore + s[start:end] + newAfter + s[after:]
		offsets[i] = before + len(newBefore)
		shift += (len(newBefore) + (end - start) + len(newAfter)) - (after - before)
	}
	return s, offsets
}

func isCloseBracket(b byte) bool { return b == ')' || b == ']' || b == '}' }

func containsNewline(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return true
		}
	}
	return false
}

// commentByteSpans returns the [start, end) byte range of every comment in
// src. A bracket-shaped byte inside a comment (e.g. a line comment ending in
// "// (" directly before a hole) is not a real bracket — treating it as one
// would collapse whitespace that isn't actually inside any bracket, which can
// merge two real statements onto the comment's line.
func commentByteSpans(src string) [][2]int {
	fset := gotoken.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var spans [][2]int
	var sc goscanner.Scanner
	sc.Init(file, []byte(src), func(gotoken.Position, string) {}, goscanner.ScanComments)
	base := file.Base()
	for {
		pos, tok, lit := sc.Scan()
		if tok == gotoken.EOF {
			break
		}
		if tok == gotoken.COMMENT {
			start := int(pos) - base
			spans = append(spans, [2]int{start, start + len(lit)})
		}
	}
	return spans
}

func insideAny(spans [][2]int, pos int) bool {
	for _, sp := range spans {
		if pos >= sp[0] && pos < sp[1] {
			return true
		}
	}
	return false
}

// StripTrailingParen drops a decorative "(" — and any whitespace after it —
// from the end of src, when src (after trimming trailing whitespace) ends in
// exactly one "(". Used on the GoText immediately before a ParenWrap-classified
// element/fragment, by both internal/printer (rebuilding fresh output — so an
// already-wrapped value isn't wrapped a second time) and internal/codegen
// (stripping the decorative paren before splicing the value's lowered closure,
// so it can never trip Go's automatic semicolon insertion).
func StripTrailingParen(src string) string {
	trimmed := strings.TrimRight(src, " \t\n\r")
	if strings.HasSuffix(trimmed, "(") {
		return trimmed[:len(trimmed)-1]
	}
	return src
}

// StripLeadingParen drops a decorative ")" — and any whitespace before it —
// from the start of src, when src (after trimming leading whitespace) starts
// with exactly one ")". Used on the GoText immediately after a
// ParenWrap-classified element/fragment; see StripTrailingParen.
func StripLeadingParen(src string) string {
	trimmed := strings.TrimLeft(src, " \t\n\r")
	if strings.HasPrefix(trimmed, ")") {
		return trimmed[1:]
	}
	return src
}
