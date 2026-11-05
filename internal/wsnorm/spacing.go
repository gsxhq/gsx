package wsnorm

import "github.com/gsxhq/gsx/ast"

// TextBoundarySibling reports the source sibling facing a Text node. A text
// boundary stays bonded during formatting: an added newline can introduce a
// separator or move the first whitespace across a transparent comment.
func TextBoundarySibling(nodes []ast.Markup, i, dir int) (int, bool) {
	if _, ok := nodes[i].(*ast.Text); !ok {
		return -1, false
	}
	for j := i + dir; j >= 0 && j < len(nodes); j += dir {
		if IsSourceTransparent(nodes[j]) {
			continue
		}
		if comment, ok := nodes[j].(*ast.Comment); ok && comment.Bare {
			return -1, false
		}
		return j, true
	}
	return -1, false
}

// IsSourceTransparent reports markup that does not divide a source text run.
// Bare line comments are excluded: they must own a line and cannot touch text.
func IsSourceTransparent(node ast.Markup) bool {
	switch v := node.(type) {
	case *ast.Comment:
		return !v.Bare
	case *ast.HTMLComment, *ast.GoBlock:
		return true
	}
	return false
}
