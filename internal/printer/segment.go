package printer

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// segment is a maximal run of adjacent children that must lay out on one line
// because a significant (literal) space glues them together.
type segment struct {
	nodes []ast.Markup
}

// segmentChildren splits a whitespace-normalized children list into segments at
// whitespace-safe boundaries and reports whether the list may lay out as a
// broken block.
//
// A boundary between nodes[i] and nodes[i+1] is GLUED (no break) iff a
// significant space sits on it: nodes[i] is a *ast.Text whose value ends in
// ' ', or nodes[i+1] is a *ast.Text whose value starts with ' '. Otherwise the
// boundary is SAFE and starts a new segment.
//
// breakable is true iff there is at least one safe boundary (more than one
// segment) AND the edge guard passes: the first node must not begin with a
// significant space and the last node must not end with one (a block opener /
// closer would otherwise absorb that space and change the normalized AST).
func segmentChildren(nodes []ast.Markup) (segs []segment, breakable bool) {
	if len(nodes) == 0 {
		return nil, false
	}
	cur := segment{nodes: []ast.Markup{nodes[0]}}
	for i := 1; i < len(nodes); i++ {
		if glued(nodes[i-1], nodes[i]) {
			cur.nodes = append(cur.nodes, nodes[i])
			continue
		}
		segs = append(segs, cur)
		cur = segment{nodes: []ast.Markup{nodes[i]}}
	}
	segs = append(segs, cur)

	if len(segs) < 2 {
		return segs, false
	}
	if leadsWithSpace(nodes[0]) || trailsWithSpace(nodes[len(nodes)-1]) {
		return segs, false
	}
	return segs, true
}

// glued reports whether a significant space binds left and right.
func glued(left, right ast.Markup) bool {
	return trailsWithSpace(left) || leadsWithSpace(right)
}

func leadsWithSpace(n ast.Markup) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.HasPrefix(t.Value, " ")
}

func trailsWithSpace(n ast.Markup) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.HasSuffix(t.Value, " ")
}
