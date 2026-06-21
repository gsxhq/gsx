// Package wsnorm implements the gsx JSX-style whitespace normalization pass.
//
// Normalize is a pure, in-place AST transform: within each non-preserved children
// list it collapses cosmetic indentation and inter-content whitespace exactly as
// React/Babel handle JSXText, while leaving Go fragments, static attributes,
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
func Normalize(f *ast.File) {
	if f == nil {
		return
	}
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			c.Body = normalizeMarkup(c.Body, false)
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

func isPreserveTag(tag string) bool {
	return preserveTags[strings.ToLower(tag)]
}

// normalizeMarkup normalizes a children list in place and returns the resulting
// (possibly shorter) slice. When preserve is true, Text nodes are kept verbatim
// but the structure is still walked so that nested attributes/slots are reached.
func normalizeMarkup(nodes []ast.Markup, preserve bool) []ast.Markup {
	out := nodes[:0]
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			if preserve {
				out = append(out, v)
				continue
			}
			norm, keep := normalizeText(v.Value)
			if !keep {
				// Drop the emptied text node entirely.
				continue
			}
			v.Value = norm
			// Exact text spans are not load-bearing for codegen/fmt; we keep the
			// node's original Pos/End rather than re-deriving a span for the
			// rewritten value. (Documented choice: re-span to the original.)
			ast.SetSpan(v, v.Pos(), v.End())
			out = append(out, v)
		case *ast.Element:
			childPreserve := preserve || isPreserveTag(v.Tag)
			v.Children = normalizeMarkup(v.Children, childPreserve)
			// Attribute markup slots are reached regardless of element preserve:
			// a slot is a fresh expression-boundary context (see normalizeAttrs).
			normalizeAttrs(v.Attrs)
			out = append(out, v)
		case *ast.Fragment:
			v.Children = normalizeMarkup(v.Children, preserve)
			out = append(out, v)
		case *ast.IfMarkup:
			v.Then = normalizeMarkup(v.Then, preserve)
			v.Else = normalizeMarkup(v.Else, preserve)
			out = append(out, v)
		case *ast.ForMarkup:
			v.Body = normalizeMarkup(v.Body, preserve)
			out = append(out, v)
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				v.Cases[i].Body = normalizeMarkup(v.Cases[i].Body, preserve)
			}
			out = append(out, v)
		default:
			// *Interp, *GoBlock, *Doctype, *HTMLComment and any other leaf:
			// untouched.
			out = append(out, n)
		}
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
		}
	}
}

// normalizeText applies the per-Text JSX whitespace rule to a single Text value.
//
// Whitespace = ASCII space, tab, '\n', '\r' (per HTML/JSX; Unicode spaces are
// intentionally not treated as collapsible whitespace).
//
//   - If v is ALL whitespace: drop it (keep=false) when it contains a newline
//     (\n or \r) — cosmetic indentation; otherwise return a single inline space
//     " " (keep=true).
//   - Else: split into lead (leading whitespace run), core (the middle) and trail
//     (trailing whitespace run); collapse every internal whitespace run in core to
//     a single space. The result is core, prefixed with a space iff lead is
//     non-empty AND contains no newline, and suffixed with a space iff trail is
//     non-empty AND contains no newline.
func normalizeText(v string) (out string, keep bool) {
	if v == "" {
		// The parser never emits empty Text; treat empty as no-newline all-ws.
		return " ", true
	}
	if isAllWhitespace(v) {
		if containsNewline(v) {
			return "", false
		}
		return " ", true
	}

	leadEnd := 0
	for leadEnd < len(v) && isWS(v[leadEnd]) {
		leadEnd++
	}
	trailStart := len(v)
	for trailStart > leadEnd && isWS(v[trailStart-1]) {
		trailStart--
	}
	lead := v[:leadEnd]
	core := v[leadEnd:trailStart]
	trail := v[trailStart:]

	var b strings.Builder
	if lead != "" && !containsNewline(lead) {
		b.WriteByte(' ')
	}
	collapseInto(&b, core)
	if trail != "" && !containsNewline(trail) {
		b.WriteByte(' ')
	}
	return b.String(), true
}

// collapseInto writes core to b, collapsing every internal whitespace run to a
// single space. core has no leading or trailing whitespace.
func collapseInto(b *strings.Builder, core string) {
	inWS := false
	for i := 0; i < len(core); i++ {
		c := core[i]
		if isWS(c) {
			inWS = true
			continue
		}
		if inWS {
			b.WriteByte(' ')
			inWS = false
		}
		b.WriteByte(c)
	}
}

func isWS(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isAllWhitespace(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isWS(s[i]) {
			return false
		}
	}
	return true
}

func containsNewline(s string) bool {
	return strings.IndexByte(s, '\n') >= 0 || strings.IndexByte(s, '\r') >= 0
}
