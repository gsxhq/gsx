package lsp

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx"
	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/htmldata"
)

// htmlTagItems enumerates every HTML element from the vendored dataset as a tag
// candidate. kind = ciKindProperty; doc = the tag's markdown documentation
// (MDN-linked). Tier: tierContext normally, tierSecondary when the typed prefix
// is capitalized — a capitalized prefix means the user is almost certainly
// reaching for a component (PascalCase convention), so HTML tags sort BELOW the
// component candidates (which stay tierContext) rather than interleaving with
// them. The client still filters both lists against what is typed; the tier only
// decides ordering when both match.
//
// This list is codegen-fact-free: it is a static dataset lookup, so it is
// offered even when package analysis is a shell (see tagCompletion).
func htmlTagItems(prefixCapitalized bool, text string, start, end int, enc encoding) []CompletionItem {
	tier := tierContext
	if prefixCapitalized {
		tier = tierSecondary
	}
	items := make([]CompletionItem, 0, len(htmldata.Tags))
	for _, tag := range htmldata.Tags {
		items = append(items, newCompletionItem(text, start, end, enc, tag.Name, tag.Name, ciKindProperty, tier, "", markdownDoc(tag.Doc)))
	}
	return items
}

// htmlAttrItems enumerates attribute-name candidates for an HTML element: the
// element's tag-specific attributes (from the vendored per-tag table) plus the
// global attributes, plus — when htmxEnabled — the htmx hx-* attributes. Every
// attribute already present on el is excluded by lowercase name (HTML attribute
// names fold), so a second `class` is never offered — EXCEPT the one whose name
// exact-matches the typed token [start,end): the cursor is mid-typing that very
// attribute (`<div class§`), not authoring a duplicate, so it stays offered.
// This mirrors componentAttrItems' cursor-on-bound-attr carve-out.
//
// Boolean attributes — those the dataset marks presence-only (.Boolean()) OR
// that gsx.IsBooleanAttr classifies as boolean — insert the bare name (`hidden`):
// their presence alone is the value. Every other attribute inserts `name=""`
// with FilterText = name, so the client keeps matching against the name the user
// typed while the edit drops the cursor inside the quotes' worth of text.
//
// el is the CLASSIFICATION-parse element (repairAtCursor's own parse of the
// cursor buffer). Unlike the component path this needs no ephemeral-analysis
// bridge: presence exclusion reads only el.Attrs' names, which are a pure parse
// fact of the same bytes — no codegen semantics required — so this path works
// even when analysis is a shell.
//
// tagName == "" (no dataset tag matches) yields tag-specific attributes as an
// empty contribution, so the candidate set collapses to GlobalAttributes (plus
// hx-* when htmxEnabled) — the "globals only" shape componentAttrItems reuses
// for a component's forwarded-attrs catch-all, where no concrete receiving
// element is known. tier is threaded into every item's SortText so callers can
// rank this list against sibling candidates: tierContext for the direct HTML
// path, tierSecondary for the forwarded-globals path so real component params
// sort first.
func htmlAttrItems(el *gsxast.Element, tagName string, htmxEnabled bool, tier int, text string, start, end int, enc encoding) []CompletionItem {
	// typed is the attribute-name token the cursor sits on. When a present
	// attribute's lowercase name exact-matches it, that attribute must stay
	// offered rather than be excluded as "already present" — the cursor is
	// mid-typing that very attribute (e.g. `<div class§`), not authoring a
	// duplicate. This mirrors componentAttrItems' cursor-on-bound-attr
	// carve-out (attrNameSpanContains in completion_gsx.go) but stays
	// fset-free: [start,end) into text is already the right coordinate space
	// here, so no ephemeral-analysis bridge is needed.
	typed := strings.ToLower(text[start:end])

	present := map[string]bool{}
	if el != nil {
		for _, a := range el.Attrs {
			if name, ok := attrName(a); ok {
				lower := strings.ToLower(name)
				if lower == typed {
					continue // cursor is on this very attribute's own token; keep it offered
				}
				present[lower] = true
			}
		}
	}

	var candidates []htmldata.Attribute
	candidates = append(candidates, tagAttributes(tagName)...)
	candidates = append(candidates, htmldata.GlobalAttributes...)
	if htmxEnabled {
		candidates = append(candidates, htmldata.HTMXAttributes...)
	}

	seen := map[string]bool{}
	items := make([]CompletionItem, 0, len(candidates))
	for _, attr := range candidates {
		lower := strings.ToLower(attr.Name)
		if present[lower] || seen[lower] {
			continue
		}
		seen[lower] = true
		if attr.Boolean() || gsx.IsBooleanAttr(attr.Name) {
			// Presence-only: insert the bare name.
			items = append(items, htmlAttrItem(text, start, end, enc, attr.Name, attr.Name, ciKindField, tier, markdownDoc(attr.Doc)))
			continue
		}
		// Value attribute: insert `name=""`, but keep FilterText = name so the
		// client matches against the typed name, not the `=""` suffix.
		items = append(items, htmlAttrItem(text, start, end, enc, attr.Name, attr.Name+`=""`, ciKindField, tier, markdownDoc(attr.Doc)))
	}
	return items
}

// htmlValueItems enumerates the allowed values for el's attribute from the
// dataset's value sets. It looks up the attribute's ValueSet on the tag-specific
// attribute first, then falls back to the same-named global attribute (many
// enumerated attributes — dir, contenteditable — are global). A freeform
// attribute (empty ValueSet, or a ValueSet with no members) yields nothing.
// kind = ciKindEnumMember, tier = tierContext.
func htmlValueItems(tagName, attrName, text string, start, end int, enc encoding) []CompletionItem {
	set := valueSetFor(tagName, attrName)
	values, ok := htmldata.ValueSets[set]
	if set == "" || !ok || len(values) == 0 {
		return nil
	}
	items := make([]CompletionItem, 0, len(values))
	for _, v := range values {
		items = append(items, newCompletionItem(text, start, end, enc, v.Name, v.Name, ciKindEnumMember, tierContext, "", markdownDoc(v.Doc)))
	}
	return items
}

// tagAttributes returns the per-tag attribute list for tagName, or nil when the
// tag is not in the dataset (a component tag, a custom element, or a
// mid-edit-broken name). Global attributes are added by the caller.
func tagAttributes(tagName string) []htmldata.Attribute {
	for _, tag := range htmldata.Tags {
		if tag.Name == tagName {
			return tag.Attrs
		}
	}
	return nil
}

// valueSetFor resolves the ValueSet key for (tagName, attrName): the tag-specific
// attribute's ValueSet if the tag declares that attribute, else the same-named
// global attribute's ValueSet. Returns "" when neither carries an enumerated set.
func valueSetFor(tagName, attrName string) string {
	lower := strings.ToLower(attrName)
	for _, tag := range htmldata.Tags {
		if tag.Name != tagName {
			continue
		}
		for _, a := range tag.Attrs {
			if strings.ToLower(a.Name) == lower && a.ValueSet != "" {
				return a.ValueSet
			}
		}
	}
	for _, a := range htmldata.GlobalAttributes {
		if strings.ToLower(a.Name) == lower {
			return a.ValueSet
		}
	}
	return ""
}

// htmlAttrItem builds an attribute-name completion item. It differs from
// newCompletionItem in one way that matters: for a value attribute newText is
// `name=""` but FilterText must stay the bare name so the client matches the
// user's typed prefix against the name, not the `=""` insertion. newText == the
// name for a boolean attribute, in which case FilterText is left unset (the
// client falls back to the label).
func htmlAttrItem(text string, start, end int, enc encoding, name, newText string, kind int, tier int, doc *MarkupContent) CompletionItem {
	item := CompletionItem{
		Label:         name,
		Kind:          kind,
		Documentation: doc,
		SortText:      fmt.Sprintf("%02d%s", tier, name),
		TextEdit: &TextEdit{
			Range:   rangeForSpan(text, start, end, enc),
			NewText: newText,
		},
	}
	if newText != name {
		item.FilterText = name
	}
	return item
}

// markdownDoc wraps dataset documentation (already markdown, with MDN/htmx
// reference links) as LSP MarkupContent. Empty doc yields nil so the item
// carries no Documentation field at all.
func markdownDoc(s string) *MarkupContent {
	if s == "" {
		return nil
	}
	return &MarkupContent{Kind: "markdown", Value: s}
}
