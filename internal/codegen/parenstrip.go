package codegen

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/goexprshape"
)

// goWithElementsParenShapes classifies each *ast.Element/*ast.Fragment part of
// v by its position in the surrounding Go expression (see goexprshape.Classify)
// — an assignment RHS, return operand, or keyed composite-literal field value
// vs. a call argument or bare composite-literal element. The result has one
// entry per v.Parts index; GoText and *ast.EmbeddedInterp positions are always
// goexprshape.Plain (EmbeddedInterp lowers to a string value, never a closure,
// so it has no matching decorative-paren convention to strip).
//
// Reuses the same substituted-source technique internal/printer's
// fmtGoExprParts uses to feed go/format, but with a single-rune placeholder per
// hole (codegen doesn't need width-matched placeholders — that's solely for
// gofmt's own column alignment, which codegen never performs).
func goWithElementsParenShapes(v *ast.GoWithElements) []goexprshape.Result {
	result := make([]goexprshape.Result, len(v.Parts))
	holeCount := 0
	var text strings.Builder
	for _, part := range v.Parts {
		if gt, ok := part.(ast.GoText); ok {
			text.WriteString(gt.Src)
			continue
		}
		holeCount++
	}
	if holeCount == 0 {
		return result
	}
	hole, ok := parenShapeHoleRune(text.String())
	if !ok {
		return result
	}

	const wrapper = "package _gsxshape\n"
	var src strings.Builder
	indices := make([]int, 0, holeCount) // v.Parts index for each hole, in order
	shapeHoles := make([]goexprshape.Hole, 0, holeCount)
	for i, part := range v.Parts {
		if gt, ok := part.(ast.GoText); ok {
			src.WriteString(gt.Src)
			continue
		}
		indices = append(indices, i)
		start := len(wrapper) + src.Len()
		shapeHoles = append(shapeHoles, goexprshape.Hole{Start: start, End: start + len(hole)})
		src.WriteString(hole)
	}
	shapes := goexprshape.Classify(wrapper+src.String(), shapeHoles)
	for j, i := range indices {
		result[i] = shapes[j]
	}
	return result
}

// parenShapeHoleRuneCandidates are Unicode modifier letters, which Go accepts
// as identifier letters; vanishingly unlikely to occur in real source.
var parenShapeHoleRuneCandidates = []string{"ᴳ", "ᴴ", "ᴵ", "ᴶ"}

// parenShapeHoleRune picks a placeholder rune absent from text, so the
// substituted source's holes can only match where they were placed.
func parenShapeHoleRune(text string) (string, bool) {
	for _, r := range parenShapeHoleRuneCandidates {
		if !strings.Contains(text, r) {
			return r, true
		}
	}
	return "", false
}

// parenWrappable reports whether part is an *ast.Element or *ast.Fragment
// classified goexprshape.ParenWrap AND actually sitting inside a real paren at
// its shapes index — i.e. gsx fmt (or a human) decorated it with a paren that
// must be stripped here. Shape alone is not enough: e.g. a `var (…)` group's
// own closing paren can immediately follow an UNWRAPPED, ParenWrap-eligible
// value with no relation to it at all — only Result.Wrapped confirms there is
// actually something to strip.
func parenWrappable(part ast.GoPart, shapes []goexprshape.Result, i int) bool {
	if i >= len(shapes) {
		return false
	}
	switch part.(type) {
	case *ast.Element, *ast.Fragment:
		return shapes[i].Shape == goexprshape.ParenWrap && shapes[i].Wrapped
	default:
		return false
	}
}
