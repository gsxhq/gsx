package codegen

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// bareCommentViolation is a bare content comment adjacent to substantive text.
// Whitespace-only neighbouring text remains output but does not invalidate it.
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
				text, ok := nodes[i-1].(*gsxast.Text)
				touches = ok && strings.Trim(text.Value, " \t\n\r\f") != ""
			}
			if !touches && i+1 < len(nodes) {
				text, ok := nodes[i+1].(*gsxast.Text)
				touches = ok && strings.Trim(text.Value, " \t\n\r\f") != ""
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
