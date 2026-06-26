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
// breakable is true iff the edge guard passes: the first node must not begin
// with a significant space and the last node must not end with one (a block
// opener / closer would otherwise absorb that space and change the normalized
// AST). A single edge-safe segment is breakable too — a lone block-level child
// must be free to take its own line so the document hierarchy stays visible;
// the layout engine still keeps it inline when the whole element fits the width.
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

// blockLevel reports whether a node is a block-level construct: one whose
// presence in a children list makes the list lay out as a broken block so the
// document hierarchy is visible. Everything that is not bare Text/Interp counts
// (every element — gsx treats all tags as block-level — plus fragments and
// control flow). Text and Interp are inline.
func blockLevel(n ast.Markup) bool {
	switch n.(type) {
	case *ast.Element, *ast.Fragment, *ast.IfMarkup, *ast.ForMarkup,
		*ast.SwitchMarkup, *ast.GoBlock, *ast.Doctype, *ast.HTMLComment:
		return true
	default:
		return false
	}
}

// hasBlockChild reports whether nodes contains at least one block-level child,
// i.e. whether the list should break structurally (regardless of width).
func hasBlockChild(nodes []ast.Markup) bool {
	for _, n := range nodes {
		if blockLevel(n) {
			return true
		}
	}
	return false
}
