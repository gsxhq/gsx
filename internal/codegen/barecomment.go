package codegen

import (
	"go/token"

	gsxast "github.com/gsxhq/gsx/ast"
)

// bareCommentViolation is a bare `//` content comment whose immediate sibling
// is text. Detected post-wsnorm: a LEGAL bare comment's whitespace neighbors
// (always newline-bearing, by the line-start rule) have been dropped by
// normalization, so any surviving adjacent *ast.Text is real content touching
// the comment across only whitespace — exactly the spec's "touches text".
type bareCommentViolation struct {
	pos, end token.Pos
}

// checkBareComments walks every child-content list in f and returns the bare
// comments that touch text. Braced comments (Bare=false) are exempt — they
// have always been legal against text. Preserve subtrees need no special
// case: the parser never produces Bare comments inside them.
func checkBareComments(f *gsxast.File) []bareCommentViolation {
	var out []bareCommentViolation
	check := func(nodes []gsxast.Markup) {
		for i, n := range nodes {
			c, ok := n.(*gsxast.Comment)
			if !ok || !c.Bare {
				continue
			}
			touches := false
			if i > 0 {
				_, touches = nodes[i-1].(*gsxast.Text)
			}
			if !touches && i+1 < len(nodes) {
				_, touches = nodes[i+1].(*gsxast.Text)
			}
			if touches {
				out = append(out, bareCommentViolation{pos: c.Pos(), end: c.End()})
			}
		}
	}
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		switch v := n.(type) {
		case *gsxast.Component:
			check(v.Body)
		case *gsxast.Element:
			check(v.Children)
		case *gsxast.Fragment:
			check(v.Children)
		case *gsxast.MarkerRegion:
			check(v.Children)
		case *gsxast.MarkupAttr:
			check(v.Value)
		case *gsxast.IfMarkup:
			check(v.Then)
			check(v.Else)
		case *gsxast.ForMarkup:
			check(v.Body)
		case *gsxast.CaseClause:
			check(v.Body)
		}
		return true
	})
	return out
}
