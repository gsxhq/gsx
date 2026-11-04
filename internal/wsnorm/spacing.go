package wsnorm

import (
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gsxhq/gsx/ast"
)

// inlineTextElements are the HTML elements whose content flows inside the
// surrounding line of text: the WHATWG "text-level semantics" elements (§4.5)
// plus the edit elements ins and del (§4.7). Excluded from §4.5 are br and wbr,
// which exist to break the line, and rt/rp, which only annotate inside a ruby.
// Embedded and interactive phrasing content (img, svg, input, button, select,
// label, …) is deliberately absent: those render as boxes whose distance from
// the text is set by layout (margin, gap), not by a space character.
var inlineTextElements = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true,
	"cite": true, "code": true, "data": true, "del": true, "dfn": true,
	"em": true, "i": true, "ins": true, "kbd": true, "mark": true,
	"q": true, "ruby": true, "s": true, "samp": true, "small": true,
	"span": true, "strong": true, "sub": true, "sup": true, "time": true,
	"u": true, "var": true,
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

// Edge is what a child renders at one edge: its kind and, for content whose
// text is known statically, the character at that edge (0 otherwise).
// Optional marks a control-flow block that may render nothing at all (an if
// branch, a loop with no iterations, a switch with no matching case), so the
// next sibling may face the boundary instead.
type Edge struct {
	Kind     EdgeKind
	R        rune
	Optional bool
}

// Spacing decides whether a line break that normalization drops beside a
// Text loses a space the reader expects: the boundary separates text from
// inline content. The child-element-spacing lint reports such breaks; the
// formatter never writes one. Both use this one rule, so formatted output
// never raises the lint.
//
// Interp classifies an interpolation that is not a plain string literal. It
// is nil where types are unknown (the formatter): every such interpolation
// is then taken to be inline content, which over-approximates the lint.
type Spacing struct {
	Interp func(n *ast.Interp) Edge
}

// DroppedBreakLoses reports whether a dropped line break beside the Text
// nodes[i] — after it when dir is 1, before it when dir is -1 — separates it
// from inline content, returning the index of the sibling that supplies that
// content. Siblings that render nothing (comments, Go blocks) are looked
// through. A text edge that itself renders a space (&nbsp;), and a boundary
// where typography never puts a space (see NoSpaceBoundary), lose nothing.
func (s Spacing) DroppedBreakLoses(nodes []ast.Markup, i, dir int) (int, bool) {
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
	left, right := own.R, f.R
	if dir < 0 {
		left, right = f.R, own.R
	}
	if NoSpaceBoundary(left, right) {
		return -1, false
	}
	return j, true
}

// NoSpaceBoundary reports whether left followed by right is a boundary where
// running text never takes a space: after an opening bracket or initial quote
// (Unicode Ps, Pi), or before a closing bracket, final quote or terminal
// punctuation (Unicode Pe, Pf, Terminal_Punctuation: . , ; : ! ?). There
// the dropped line break is what the author meant (`</a>` then `.` on the
// next line). An unknown character (0) never exempts.
func NoSpaceBoundary(left, right rune) bool {
	if left != 0 && unicode.In(left, unicode.Ps, unicode.Pi) {
		return true
	}
	return right != 0 && unicode.In(right, unicode.Pe, unicode.Pf, unicode.Terminal_Punctuation)
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
// content when either is, since that rendering loses the space. Contents
// with different edge characters leave the character unknown. Optional is
// left for the caller to set.
func combineEdges(a, b Edge) Edge {
	switch {
	case a.Kind == EdgeContent && b.Kind == EdgeContent:
		if a.R != b.R {
			return Edge{Kind: EdgeContent}
		}
		return Edge{Kind: EdgeContent, R: a.R}
	case a.Kind == EdgeContent:
		return Edge{Kind: EdgeContent, R: a.R}
	case b.Kind == EdgeContent:
		return Edge{Kind: EdgeContent, R: b.R}
	case a.Kind == EdgeOpaque || b.Kind == EdgeOpaque:
		return Edge{Kind: EdgeOpaque}
	case a.Kind == EdgeSpace || b.Kind == EdgeSpace:
		return Edge{Kind: EdgeSpace}
	}
	return Edge{}
}

// textEdge classifies normalized static text. Static text renders verbatim,
// so its character references are decoded to find the edge character.
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
	return Edge{Kind: EdgeContent, R: r}
}
