package printer

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
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

// glued reports whether a significant space (or the {" "} idiom's left bond)
// binds left and right onto one line.
func glued(left, right ast.Markup) bool {
	return trailsWithSpace(left) || leadsWithSpace(right) || isSpacingInterp(right)
}

// isSpacingInterp reports whether n is the {" "} spacing idiom: an
// interpolation of a single Go string literal whose value is only ASCII
// spaces. Such an interp is layout glue — it bonds to its left neighbor and
// offers a break after — because its rendered space, unlike a literal space
// in Text, survives any adjacent line break (wsnorm cannot collapse it).
func isSpacingInterp(n ast.Markup) bool {
	i, ok := n.(*ast.Interp)
	if !ok || len(i.Stages) > 0 {
		return false
	}
	s := strings.TrimSpace(i.Expr)
	if len(s) < 2 || (s[0] != '"' && s[0] != '`') {
		return false
	}
	v, err := strconv.Unquote(s)
	if err != nil || v == "" {
		return false
	}
	return strings.Trim(v, " ") == ""
}

// leadingBreak reports whether n — a non-Text markup leaf — carries a
// LeadingBreak fact: the source placed a line break in the whitespace
// immediately before it in its children list (see
// ast.Element/Interp/EmbeddedInterp.LeadingBreak). Any other Markup kind
// (Fragment, IfMarkup, …) is always block-level and never reaches fillParts'
// leaf-joint check, so it has no fact to report.
func leadingBreak(n ast.Markup) bool {
	switch v := n.(type) {
	case *ast.Element:
		return v.LeadingBreak
	case *ast.Interp:
		return v.LeadingBreak
	case *ast.EmbeddedInterp:
		return v.LeadingBreak
	default:
		return false
	}
}

func leadsWithSpace(n ast.Markup) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.HasPrefix(t.Value, " ")
}

func trailsWithSpace(n ast.Markup) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.HasSuffix(t.Value, " ")
}

// blockLevel reports whether n is block-level: a construct whose presence
// makes the children list lay out as a broken block so the document
// hierarchy stays visible. Inline atoms and interps are NOT block-level; a
// non-atom element (wrong tag, author multiline, forced break inside) is.
func (p *printer) blockLevel(n ast.Markup) bool {
	if e, ok := n.(*ast.Element); ok {
		_, atom := p.atomDoc(e)
		return !atom
	}
	switch n.(type) {
	case *ast.Fragment, *ast.IfMarkup, *ast.ForMarkup, *ast.SwitchMarkup,
		*ast.GoBlock, *ast.Doctype, *ast.HTMLComment:
		return true
	default:
		return false
	}
}

// hasBlockChild reports whether nodes contains at least one block-level child,
// i.e. whether the list should break structurally (regardless of width).
func (p *printer) hasBlockChild(nodes []ast.Markup) bool {
	return slices.ContainsFunc(nodes, p.blockLevel)
}

// fillParts flattens a children run into pretty.Fill parts — alternating
// cluster docs and separators. Joints between leaves follow wsnorm physics
// (see the design spec's table):
//
//   - bond (no separator; leaves concat into one cluster): a significant
//     space touching a non-word leaf — breaking it would drop the space at
//     render — and the left side of a {" "} spacing interp;
//   - word gap (pretty.Line, flat " "): space between two words of one Text
//     node — a break there re-normalizes to the same single space;
//   - safe gap (pretty.SoftLine, flat ""): direct adjacency and the right
//     side of a spacing interp — a break there drops nothing.
//
// The flat rendering is byte-identical to the normalized source, so layout
// can never change the rendered HTML.
func (p *printer) fillParts(nodes []ast.Markup) []pretty.Doc {
	var parts []pretty.Doc
	var cur []pretty.Doc
	gap := func(sep pretty.Doc) {
		parts = append(parts, pretty.Concat(cur...), sep)
		cur = nil
	}
	for i, n := range nodes {
		if t, ok := n.(*ast.Text); ok {
			v := t.Value
			// Word gaps exist ONLY at ASCII spaces — wsnorm's whitespace
			// model (see wsnorm.go's normalizeText doc). Unicode spaces
			// (NBSP, U+3000, …) are content and must stay inside their
			// word: post-normalization, interior ASCII whitespace runs in
			// core are exactly one ' ', so splitting the trimmed core on
			// ' ' is exact (strings.Fields would also split on Unicode
			// whitespace, dropping those runes at render time).
			core := strings.Trim(v, " ")
			var words []string
			if core != "" {
				words = strings.Split(core, " ")
			}
			switch {
			// i==0 never lands here: segmentChildren's edge guard bars a
			// leading-space Text from starting a segment, and a leading-space
			// Text always glues leftward — so this branch only fires mid-segment.
			case strings.HasPrefix(v, " "):
				cur = append(cur, pretty.Text(" ")) // bond to the previous leaf
			case i > 0 && len(words) > 0 && p.caseBodyBondWord(words[0]):
				// The next node's first word is a bare "case"/"default" inside
				// a case body: bond instead of a safe gap. Not calling gap
				// here changes nothing rendered (SoftLine's flat form is
				// already "" — direct adjacency), it only removes the break
				// candidate, so the fill can never place that word at the
				// start of a line (2026-07-25 finding: the parser would then
				// read it as a real arm label the author never wrote).
			case i > 0:
				gap(pretty.SoftLine) // direct adjacency: safe gap
			}
			for j, w := range words {
				if j > 0 {
					if p.caseBodyBondWord(w) {
						cur = append(cur, pretty.Text(" ")) // bond: never breakable
					} else {
						gap(pretty.Line) // word gap
					}
				}
				cur = append(cur, pretty.Text(w))
			}
			if strings.HasSuffix(v, " ") && v != " " {
				cur = append(cur, pretty.Text(" ")) // bond to the next leaf
			}
			continue
		}
		switch {
		case i == 0:
		case trailsWithSpace(nodes[i-1]) || isSpacingInterp(n):
			// bonded: the previous Text's trailing space is already in cur,
			// or the {" "} idiom glues to its left neighbor. Never affected by
			// LeadingBreak — a significant space adjacent to a tag never
			// coexists with a surviving newline (wsnorm drops newline-bearing
			// edge runs), so this joint has no fact to honor.
		case leadingBreak(n):
			// safe gap where the source placed a line break immediately before
			// n: preserve it as a hard break instead of a reflowable one. This
			// also forces the enclosing group open via containsForcedBreak.
			gap(pretty.HardLine)
		default:
			gap(pretty.SoftLine)
		}
		cur = append(cur, p.inlineLeaf(n))
	}
	return append(parts, pretty.Concat(cur...))
}

// inlineLeaf renders one non-Text leaf: atoms flat, everything else through
// the normal markup path (a block element glued into a text segment keeps
// today's group-based rendering).
func (p *printer) inlineLeaf(n ast.Markup) pretty.Doc {
	if e, ok := n.(*ast.Element); ok {
		if doc, ok := p.atomDoc(e); ok {
			return doc
		}
	}
	return p.markup(n)
}
