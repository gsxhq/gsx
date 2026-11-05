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
	if f == nil {
		return
	}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			v.Body = normalizeMarkup(v.Body, false)
		case *ast.GoWithElements:
			NormalizeGoParts(v.Parts)
		}
	}
}

// NormalizeGoParts normalizes, in place, each element or fragment literal in a
// Go expression split into parts: a top-level GoWithElements region, or a
// codegen split of a Go-expression field nested anywhere in markup. Either way
// the Go expression is a fresh (preserve=false) context, so a literal in a
// <pre> body's interpolation collapses like one in top-level Go. Text and
// prefixed-literal parts are untouched.
func NormalizeGoParts(parts []ast.GoPart) {
	for _, part := range parts {
		switch p := part.(type) {
		case *ast.Element:
			// Mirrors normalizeMarkup's own *ast.Element case.
			p.Children = normalizeMarkup(p.Children, IsPreserveTag(p.Tag))
			normalizeAttrs(p.Attrs)
		case *ast.Fragment:
			p.Children = normalizeMarkup(p.Children, false)
		}
	}
}

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

// normalizeMarkup normalizes a children list in place and returns the resulting
// (possibly shorter) slice. When preserve is true, Text nodes are kept verbatim
// but the structure is still walked so that nested attributes/slots are reached.
func normalizeMarkup(nodes []ast.Markup, preserve bool) []ast.Markup {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			childPreserve := preserve || IsPreserveTag(v.Tag)
			v.Children = normalizeMarkup(v.Children, childPreserve)
			// Attribute markup slots are reached regardless of element preserve:
			// a slot is a fresh expression-boundary context (see normalizeAttrs).
			normalizeAttrs(v.Attrs)
		case *ast.Fragment:
			v.Children = normalizeMarkup(v.Children, preserve)
		case *ast.MarkerRegion:
			// Like Fragment: no wrapper tag, so no isPreserveTag override — a
			// region's children normalize in the ambient preserve context. (Marker
			// is void — no Children — so it needs no case here; it already falls
			// through to default untouched, which is correct.)
			v.Children = normalizeMarkup(v.Children, preserve)
		case *ast.IfMarkup:
			v.Then = normalizeMarkup(v.Then, preserve)
			v.Else = normalizeMarkup(v.Else, preserve)
		case *ast.ForMarkup:
			v.Body = normalizeMarkup(v.Body, preserve)
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				v.Cases[i].Body = normalizeMarkup(v.Cases[i].Body, preserve)
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
		normalizeTextRegion(nodes[start:end], sourceSibling(nodes, start-1), sourceSibling(nodes, end))
		start = end
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

// normalizeAttrs walks an attribute list to reach markup slots, normalizing each
// MarkupAttr.Value as a FRESH children-list context (preserve=false: an element's
// preserve does not leak across the `name={ … }` expression boundary into the
// slot's markup). CondAttr branches are recursed to reach nested MarkupAttrs.
// Other attribute kinds carry no markup and are skipped.
func normalizeAttrs(attrs []ast.Attr) {
	for _, a := range attrs {
		switch v := a.(type) {
		case *ast.MarkupAttr:
			v.Value = normalizeMarkup(v.Value, false)
		case *ast.CondAttr:
			normalizeAttrs(v.Then)
			normalizeAttrs(v.Else)
		case *ast.SwitchAttr:
			for _, cc := range v.Cases {
				normalizeAttrs(cc.Body)
			}
		}
	}
}

// normalizeTextRegion scans Text separated only by source-transparent nodes as
// one text stream. A whitespace run belongs to its first contributing Text;
// no substantive bytes move across comments or Go statements.
func normalizeTextRegion(nodes []ast.Markup, before, after bool) {
	var pending *ast.Text
	newline, content := false, false
	for _, node := range nodes {
		text, ok := node.(*ast.Text)
		if !ok {
			continue
		}
		raw := text.Value
		var out strings.Builder
		for i := 0; i < len(raw); {
			if isWS(raw[i]) {
				if pending == nil {
					pending = text
				}
				newline = newline || raw[i] == '\n' || raw[i] == '\r'
				i++
				continue
			}
			if pending != nil {
				if !newline || content || before {
					if pending == text {
						out.WriteByte(' ')
					} else {
						pending.Value += " "
					}
				}
				pending, newline = nil, false
			}
			start := i
			for i < len(raw) && !isWS(raw[i]) {
				i++
			}
			out.WriteString(raw[start:i])
			content = true
		}
		text.Value = out.String()
	}
	if pending != nil && (!newline || content && after) {
		pending.Value += " "
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
