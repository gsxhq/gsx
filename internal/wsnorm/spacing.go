package wsnorm

import "github.com/gsxhq/gsx/ast"

// TextBoundarySibling reports the source sibling facing a Text node. A text
// boundary stays bonded during formatting: an added newline can introduce a
// separator or move the first whitespace across a transparent comment. A
// transparent prefix/tail remains bonded even without another source sibling.
func TextBoundarySibling(nodes []ast.Markup, i, dir int) (int, bool) {
	if _, ok := nodes[i].(*ast.Text); !ok {
		return -1, false
	}
	last := -1
	for j := i + dir; j >= 0 && j < len(nodes); j += dir {
		if IsSourceTransparent(nodes[j]) {
			last = j
			continue
		}
		if comment, ok := nodes[j].(*ast.Comment); ok && comment.Bare {
			return last, last >= 0
		}
		return j, true
	}
	return last, last >= 0
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
