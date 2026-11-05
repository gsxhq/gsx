package wsnorm

import (
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gsxhq/gsx/ast"
)

// inlineTextElements are HTML text-level elements and the phrasing elements
// whose ordinary child text participates in surrounding prose. Transparent
// ins/del/map expose their child edges. Void/break elements, raw or inert
// content, embedded media, fallback-only content and value controls are not
// prose inferred from their source children. An empty eligible element is
// still opaque. This classification does not inspect CSS or component output.
var inlineTextElements = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true,
	"cite": true, "code": true, "data": true, "del": true, "dfn": true,
	"em": true, "i": true, "ins": true, "kbd": true, "mark": true,
	"q": true, "ruby": true, "s": true, "samp": true, "small": true,
	"span": true, "strong": true, "sub": true, "sup": true, "time": true,
	"u": true, "var": true,
	"button": true, "label": true, "output": true, "map": true,
}

// EdgeKind classifies what a child renders at one of its edges.
type EdgeKind uint8

const (
	// EdgeNone: nothing renders there (a comment, a Go block, an empty body).
	EdgeNone EdgeKind = iota
	// EdgeSpace: a whitespace character that renders.
	EdgeSpace
	// EdgeContent: inline text or text-level content.
	EdgeContent
	// EdgeOpaque: block-level, component, empty-element or otherwise unknown
	// output.
	EdgeOpaque
)

// Edge classifies what a child renders at one edge. Optional marks a
// control-flow block that may render nothing, exposing the next sibling.
type Edge struct {
	Kind     EdgeKind
	Optional bool
}

// Spacing decides whether a substantive Text boundary needs a prose separator.
// Normalization and the formatter share this structural rule. It never inspects
// runtime values or styles, and never inserts separators between sibling tags
// without an authored substantive Text boundary.
//
// Interp classifies an interpolation that is not a plain string literal. It
// is nil where types are unknown (the formatter): every such interpolation
// is then taken to be potential prose content, consistently during generation
// and formatting.
type Spacing struct {
	Interp func(n *ast.Interp) Edge
}

// NeedsProseSpace reports an unspaced substantive Text boundary that needs a
// prose separator when authored whitespace is normalized there. It returns the
// sibling supplying the prose edge, looking through nonrendering nodes and
// accounting for every feasible branch. Already-spaced boundaries
// need no inferred separator. The formatter also uses this predicate to avoid
// introducing a separator at a joined source boundary.
func (s Spacing) NeedsProseSpace(nodes []ast.Markup, i, dir int) (int, bool) {
	t, ok := nodes[i].(*ast.Text)
	if !ok || strings.TrimSpace(t.Value) == "" {
		return -1, false
	}
	own := textEdge(t.Value, dir < 0)
	if own.Kind == EdgeSpace {
		return -1, false
	}
	j, f := s.scan(nodes, i+dir, dir)
	if f.Kind != EdgeContent {
		return -1, false
	}
	return j, true
}

// LineBreakPreservesSpace reports whether replacing an authored inline ASCII
// space with a newline preserves normalized output. An existing rendered gap
// satisfies newline spacing, so an extra explicit space beside it cannot wrap.
func LineBreakPreservesSpace(left, right string) bool {
	return textEdge(left, false).Kind != EdgeSpace && textEdge(right, true).Kind != EdgeSpace
}

// scan computes the edge that nodes[from], nodes[from+dir], … present at the
// boundary they face: the first child that renders something decides it,
// except that a child which may render nothing (Edge.Optional) is combined
// with whatever follows it. It returns the index of the child that supplied
// inline content (-1 when none did).
func (s Spacing) scan(nodes []ast.Markup, from, dir int) (int, Edge) {
	acc, at := Edge{}, -1
	for j := from; j >= 0 && j < len(nodes); j += dir {
		f := s.edge(nodes[j], dir > 0)
		if f.Kind == EdgeNone {
			continue
		}
		if f.Kind == EdgeContent && at < 0 {
			at = j
		}
		acc = combineEdges(acc, f)
		if !f.Optional {
			return at, acc
		}
	}
	acc.Optional = true
	return at, acc
}

// edge classifies what n renders at its leading (leading=true) or trailing
// edge.
func (s Spacing) edge(n ast.Markup, leading bool) Edge {
	switch v := n.(type) {
	case *ast.Text:
		return textEdge(v.Value, leading)
	case *ast.Element:
		// A text-level element is part of the line of text, so its edge is
		// that of its own content. One with no content at that edge (an
		// empty <span> used as a rule or an icon) is a styled box: opaque.
		if v.IsComponent || !inlineTextElements[v.Tag] {
			return Edge{Kind: EdgeOpaque}
		}
		if f := s.listEdge(v.Children, leading); f.Kind != EdgeNone {
			f.Optional = false
			return f
		}
		return Edge{Kind: EdgeOpaque}
	case *ast.Interp:
		if len(v.Stages) == 0 && v.Embedded == nil {
			if lit, err := strconv.Unquote(strings.TrimSpace(v.Expr)); err == nil {
				return valueEdge(lit, leading)
			}
		}
		if s.Interp == nil {
			return Edge{Kind: EdgeContent}
		}
		return s.Interp(v)
	case *ast.EmbeddedInterp:
		return Edge{Kind: EdgeContent}
	case *ast.Comment:
		// A bare `//` comment beside text is already the
		// bare-comment-touches-text error; it never separates text from text.
		if v.Bare {
			return Edge{Kind: EdgeOpaque}
		}
		return Edge{}
	case *ast.GoBlock, *ast.HTMLComment:
		return Edge{}
	case *ast.Fragment:
		return s.listEdge(v.Children, leading)
	case *ast.MarkerRegion:
		return s.listEdge(v.Children, leading)
	case *ast.IfMarkup:
		return branches(s.listEdge(v.Then, leading), s.listEdge(v.Else, leading))
	case *ast.ForMarkup:
		f := s.listEdge(v.Body, leading)
		f.Optional = true
		return f
	case *ast.SwitchMarkup:
		f, hasDefault := Edge{}, false
		for i, cc := range v.Cases {
			hasDefault = hasDefault || cc.Default
			if i == 0 {
				f = s.listEdge(cc.Body, leading)
				continue
			}
			f = branches(f, s.listEdge(cc.Body, leading))
		}
		f.Optional = f.Optional || !hasDefault
		return f
	}
	return Edge{Kind: EdgeOpaque}
}

// listEdge is the edge of a children list at its start (leading) or end.
func (s Spacing) listEdge(nodes []ast.Markup, leading bool) Edge {
	from, dir := 0, 1
	if !leading {
		from, dir = len(nodes)-1, -1
	}
	_, f := s.scan(nodes, from, dir)
	if f.Kind == EdgeNone {
		f.Optional = true
	}
	return f
}

// branches merges the edges of two alternative bodies; the result may render
// nothing when either body may.
func branches(a, b Edge) Edge {
	f := combineEdges(a, b)
	f.Optional = a.Optional || b.Optional || a.Kind == EdgeNone || b.Kind == EdgeNone
	return f
}

// combineEdges merges two edges that can each face the boundary (alternative
// branches, or an optional block and what follows it): the result is inline
// content when either is, since that rendering needs the source separator.
// Optional is left for the caller to set.
func combineEdges(a, b Edge) Edge {
	switch {
	case a.Kind == EdgeContent || b.Kind == EdgeContent:
		return Edge{Kind: EdgeContent}
	case a.Kind == EdgeOpaque || b.Kind == EdgeOpaque:
		return Edge{Kind: EdgeOpaque}
	case a.Kind == EdgeSpace || b.Kind == EdgeSpace:
		return Edge{Kind: EdgeSpace}
	}
	return Edge{}
}

// textEdge classifies normalized static text. Character references are decoded
// to distinguish content from an existing space at the edge.
func textEdge(s string, leading bool) Edge {
	return valueEdge(html.UnescapeString(s), leading)
}

// valueEdge classifies a rendered string by its first or last character.
func valueEdge(s string, leading bool) Edge {
	if s == "" {
		return Edge{}
	}
	var r rune
	if leading {
		r, _ = utf8.DecodeRuneInString(s)
	} else {
		r, _ = utf8.DecodeLastRuneInString(s)
	}
	// Any Unicode White_Space renders a gap, including a non-breaking space
	// written as &nbsp; that normalization (ASCII-only) keeps.
	if unicode.IsSpace(r) {
		return Edge{Kind: EdgeSpace}
	}
	return Edge{Kind: EdgeContent}
}
