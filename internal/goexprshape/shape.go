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
	sanitized, offsets := collapseHoleWhitespace(src, holes)
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

// isSpace reports whether b is Go source whitespace (the same set go/scanner
// treats as insignificant between tokens).
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// collapseHoleWhitespace returns a copy of src where, for every hole whose
// immediately-preceding non-whitespace byte is an opening bracket, the
// whitespace before it is collapsed to a single space (if it contains a
// newline) — and likewise, for every hole whose immediately-following
// non-whitespace byte is a closing bracket, the whitespace after it. Returns
// each hole's new Start offset in the returned string, in the same order as
// holes. Processes holes right-to-left (by Start, descending) so editing one
// hole's neighborhood never disturbs an as-yet-unprocessed (necessarily
// earlier) hole's byte offsets.
func collapseHoleWhitespace(src string, holes []Hole) (string, []int) {
	order := make([]int, len(holes))
	for i := range order {
		order[i] = i
	}
	// Simple insertion sort by Start descending — holes is always small (one
	// entry per gsx value in a single GoWithElements decl).
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && holes[order[j-1]].Start < holes[order[j]].Start; j-- {
			order[j-1], order[j] = order[j], order[j-1]
		}
	}
	offsets := make([]int, len(holes))
	s := src
	for _, i := range order {
		h := holes[i]
		before := h.Start
		for before > 0 && isSpace(s[before-1]) {
			before--
		}
		after := h.End
		for after < len(s) && isSpace(s[after]) {
			after++
		}
		beforeWS, afterWS := s[before:h.Start], s[h.End:after]
		collapseBefore := before > 0 && isOpenBracket(s[before-1]) && containsNewline(beforeWS)
		collapseAfter := after < len(s) && isCloseBracket(s[after]) && containsNewline(afterWS)
		if !collapseBefore && !collapseAfter {
			offsets[i] = h.Start
			continue
		}
		newBefore, newAfter := beforeWS, afterWS
		if collapseBefore {
			newBefore = " "
		}
		if collapseAfter {
			newAfter = " "
		}
		s = s[:before] + newBefore + s[h.Start:h.End] + newAfter + s[after:]
		offsets[i] = before + len(newBefore)
	}
	return s, offsets
}

func isOpenBracket(b byte) bool  { return b == '(' || b == '[' || b == '{' }
func isCloseBracket(b byte) bool { return b == ')' || b == ']' || b == '}' }

func containsNewline(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
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
