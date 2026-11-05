// Package wsnorm implements the gsx normalized whitespace pass.
//
// Normalize is a pure, in-place AST transform: within each non-preserved children
// list it discards layout-only indentation and preserves prose word boundaries,
// while leaving Go fragments, static attributes,
// Doctype/HTMLComment, and the whitespace-significant elements (pre, textarea,
// script, style) byte-for-byte unchanged.
//
// The pass is idempotent by construction: normalized text re-normalizes to itself.
//
// Normalize and Lint share one traversal (walker) and one region scanner
// (scanRegion), so a lint can never disagree with what normalization renders.
//
// It depends only on github.com/gsxhq/gsx/ast and the standard library.
package wsnorm

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// Normalize rewrites whitespace in every children list of every component in f,
// in place. It does not affect Go fragments, static attribute values, raw-text
// script/style bodies (kept verbatim), or pre/textarea subtrees.
//
// Elements embedded directly in Go-expression position (ast.GoWithElements,
// e.g. `var help = <a href={u}>{ label }</a>`) are reached too: each is a
// markup subtree exactly like a component's body, just rooted at a
// GoWithElements Part instead of a Component.Body, so it needs the same
// whitespace normalization. Skipping it would leave an embedded element's Text
// children un-normalized, which surfaces as a real fmt bug: a re-printed,
// width-broken element's freshly-introduced cosmetic newlines never collapse
// back out on the next parse/print pass, so fmt fails its idempotence
// contract (each pass would introduce more literal whitespace than the last).
func Normalize(f *ast.File) {
	normalizer.file(f)
}

// NormalizeGoParts normalizes, in place, each element or fragment literal in a
// Go expression split into parts: a top-level GoWithElements region, or a
// codegen split of a Go-expression field nested anywhere in markup. Either way
// the Go expression is a fresh (preserve=false) context, so a literal in a
// <pre> body's interpolation collapses like one in top-level Go. Text and
// prefixed-literal parts are untouched.
func NormalizeGoParts(parts []ast.GoPart) {
	normalizer.goParts(parts)
}

var normalizer = walker{region: normalizeRegion, prune: true}

// preserveTags is the set of lowercased tag names whose subtrees keep whitespace
// verbatim. pre/textarea are whitespace-significant in HTML; script/style bodies
// are raw JS/CSS that must never be collapsed.
var preserveTags = map[string]bool{
	"pre":      true,
	"textarea": true,
	"script":   true,
	"style":    true,
}

// IsPreserveTag reports whether tag (any case) roots a whitespace-significant
// subtree (pre, textarea) or a raw-text body (script, style). The parser uses
// the same predicate to suppress bare `//` content-comment recognition.
func IsPreserveTag(tag string) bool {
	return preserveTags[strings.ToLower(tag)]
}

// walker visits every children list whitespace normalization processes, with
// the pre/textarea/script/style preserve rule, and calls region for each text
// region of a non-preserved list: a maximal run nodes[start:end] of Text and
// source-transparent nodes. When prune is set, Text emptied by region is
// removed from the list.
type walker struct {
	region func(nodes []ast.Markup, start, end int)
	prune  bool
}

func (w walker) file(f *ast.File) {
	if f == nil {
		return
	}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			v.Body = w.markup(v.Body, false)
		case *ast.GoWithElements:
			w.goParts(v.Parts)
		}
	}
}

func (w walker) goParts(parts []ast.GoPart) {
	for _, part := range parts {
		switch p := part.(type) {
		case *ast.Element:
			// Mirrors markup's own *ast.Element case.
			p.Children = w.markup(p.Children, IsPreserveTag(p.Tag))
			w.attrs(p.Attrs)
		case *ast.Fragment:
			p.Children = w.markup(p.Children, false)
		}
	}
}

// markup processes a children list and returns the resulting (possibly
// shorter, when pruning) slice. When preserve is true, Text nodes are kept
// verbatim but the structure is still walked so that nested attributes/slots
// are reached.
func (w walker) markup(nodes []ast.Markup, preserve bool) []ast.Markup {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			v.Children = w.markup(v.Children, preserve || IsPreserveTag(v.Tag))
			// Attribute markup slots are reached regardless of element preserve:
			// a slot is a fresh expression-boundary context (see attrs).
			w.attrs(v.Attrs)
		case *ast.Fragment:
			v.Children = w.markup(v.Children, preserve)
		case *ast.MarkerRegion:
			// Like Fragment: no wrapper tag, so no IsPreserveTag override — a
			// region's children normalize in the ambient preserve context. (Marker
			// is void — no Children — so it needs no case here.)
			v.Children = w.markup(v.Children, preserve)
		case *ast.IfMarkup:
			v.Then = w.markup(v.Then, preserve)
			v.Else = w.markup(v.Else, preserve)
		case *ast.ForMarkup:
			v.Body = w.markup(v.Body, preserve)
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				v.Cases[i].Body = w.markup(v.Cases[i].Body, preserve)
			}
		default:
			// *Interp, *GoBlock, *Doctype, *HTMLComment and any other leaf:
			// untouched.
		}
	}
	if preserve {
		return nodes
	}
	for start := 0; start < len(nodes); {
		if !isTextRegionNode(nodes[start]) {
			start++
			continue
		}
		end := start + 1
		for end < len(nodes) && isTextRegionNode(nodes[end]) {
			end++
		}
		w.region(nodes, start, end)
		start = end
	}
	if !w.prune {
		return nodes
	}
	out := nodes[:0]
	for _, node := range nodes {
		if text, ok := node.(*ast.Text); ok && text.Value == "" {
			continue
		}
		out = append(out, node)
	}
	return out
}

// attrs walks an attribute list to reach markup slots, processing each
// MarkupAttr.Value as a FRESH children-list context (preserve=false: an element's
// preserve does not leak across the `name={ … }` expression boundary into the
// slot's markup). CondAttr branches are recursed to reach nested MarkupAttrs.
// Other attribute kinds carry no markup and are skipped.
func (w walker) attrs(attrs []ast.Attr) {
	for _, a := range attrs {
		switch v := a.(type) {
		case *ast.MarkupAttr:
			v.Value = w.markup(v.Value, false)
		case *ast.CondAttr:
			w.attrs(v.Then)
			w.attrs(v.Else)
		case *ast.SwitchAttr:
			for _, cc := range v.Cases {
				w.attrs(cc.Body)
			}
		}
	}
}

// Run is one ASCII whitespace run of a text region, as scanRegion reports it.
// A run may span several Text nodes separated by source-transparent nodes.
type Run struct {
	// Parts are the run's byte ranges, in source order, each inside one Text
	// value. The scanner reuses the slice between runs; copy it to retain it.
	Parts []RunPart
	// Newline reports a CR or LF anywhere in the run.
	Newline bool
	// Renders reports that normalization emits one space for the run (placed
	// in Parts[0].Text).
	Renders bool
	// Prev and Next are the region words adjacent to the run, or "" when the
	// run faces the region edge instead.
	Prev, Next string
	// PrevNode and NextNode are the source siblings the run faces at a region
	// edge (Prev or Next is ""), or nil at a children-list edge.
	PrevNode, NextNode ast.Markup
	// PrevIndex and NextIndex are their indexes in the children list.
	PrevIndex, NextIndex int
}

// RunPart is the byte range [Start, End) of one Text value inside a Run.
type RunPart struct {
	Text       *ast.Text
	Start, End int
}

// scanRegion is the whitespace rule. It scans nodes[start:end], Text separated
// only by source-transparent nodes, as one text stream, calling word for each
// non-whitespace chunk and run for each whitespace run, in source order. A run
// renders one space unless it contains a newline and does not sit between
// content: region words or a source sibling on each side.
func scanRegion(nodes []ast.Markup, start, end int, word func(*ast.Text, string), run func(*Run)) {
	var prevNode, nextNode ast.Markup
	if sourceSibling(nodes, start-1) {
		prevNode = nodes[start-1]
	}
	if sourceSibling(nodes, end) {
		nextNode = nodes[end]
	}
	r := Run{PrevIndex: start - 1, NextIndex: end}
	inRun, content, prev := false, false, ""
	for _, node := range nodes[start:end] {
		text, ok := node.(*ast.Text)
		if !ok {
			continue
		}
		raw := text.Value
		for i := 0; i < len(raw); {
			j := i
			if isWS(raw[i]) {
				for j < len(raw) && isWS(raw[j]) {
					r.Newline = r.Newline || raw[j] == '\n' || raw[j] == '\r'
					j++
				}
				if !inRun {
					inRun = true
				}
				r.Parts = append(r.Parts, RunPart{Text: text, Start: i, End: j})
				i = j
				continue
			}
			for j < len(raw) && !isWS(raw[j]) {
				j++
			}
			chunk := raw[i:j]
			if inRun {
				r.Renders = !r.Newline || content || prevNode != nil
				r.Prev, r.Next, r.PrevNode, r.NextNode = prev, chunk, nil, nil
				if !content {
					r.PrevNode = prevNode
				}
				run(&r)
				inRun, r.Newline, r.Parts = false, false, r.Parts[:0]
			}
			word(text, chunk)
			content, prev = true, chunk
			i = j
		}
	}
	if inRun {
		r.Renders = !r.Newline || content && nextNode != nil
		r.Prev, r.Next, r.PrevNode, r.NextNode = prev, "", nil, nextNode
		if !content {
			r.PrevNode = prevNode
		}
		run(&r)
	}
}

// normalizeRegion rewrites each Text of nodes[start:end] to its normalized
// value: words verbatim, and one space for each rendering run, owned by the
// run's first Text. No substantive bytes move across comments or Go statements.
func normalizeRegion(nodes []ast.Markup, start, end int) {
	type textOut struct {
		text *ast.Text
		b    []byte
	}
	var outs []textOut
	at := func(t *ast.Text) int {
		for i := range outs {
			if outs[i].text == t {
				return i
			}
		}
		outs = append(outs, textOut{text: t})
		return len(outs) - 1
	}
	scanRegion(nodes, start, end,
		func(t *ast.Text, chunk string) {
			i := at(t)
			outs[i].b = append(outs[i].b, chunk...)
		},
		func(r *Run) {
			if r.Renders {
				i := at(r.Parts[0].Text)
				outs[i].b = append(outs[i].b, ' ')
			}
		})
	for _, node := range nodes[start:end] {
		if t, ok := node.(*ast.Text); ok {
			t.Value = ""
		}
	}
	for _, o := range outs {
		o.text.Value = string(o.b)
	}
}

func isTextRegionNode(node ast.Markup) bool {
	_, text := node.(*ast.Text)
	return text || IsSourceTransparent(node)
}

func sourceSibling(nodes []ast.Markup, i int) bool {
	if i < 0 || i >= len(nodes) {
		return false
	}
	comment, ok := nodes[i].(*ast.Comment)
	return !ok || !comment.Bare
}

func isWS(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
