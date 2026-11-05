package wsnorm

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// NeedsSourceSpace reports the sibling facing an unspaced Text edge. Braced comments
// and Go statements are transparent to source text; every other markup node
// supplies a boundary regardless of what it eventually renders. An existing
// normalized source space suffices, including across transparent nodes.
func NeedsSourceSpace(nodes []ast.Markup, i, dir int) (int, bool) {
	t, ok := nodes[i].(*ast.Text)
	if !ok || isAllWhitespace(t.Value) || sourceSpace(t.Value, dir < 0) {
		return -1, false
	}
	for j := i + dir; j >= 0 && j < len(nodes); j += dir {
		switch v := nodes[j].(type) {
		case *ast.Comment:
			// A bare line comment must own its source line; touching text is
			// rejected separately. It cannot participate in a text fill run.
			if v.Bare {
				return -1, false
			}
			continue
		case *ast.HTMLComment, *ast.GoBlock:
			continue
		case *ast.Text:
			if sourceSpace(v.Value, dir > 0) {
				return -1, false
			}
		}
		return j, true
	}
	return -1, false
}

func sourceSpace(text string, leading bool) bool {
	if leading {
		return strings.HasPrefix(text, " ")
	}
	return strings.HasSuffix(text, " ")
}
