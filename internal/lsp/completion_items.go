package lsp

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Sort tiers for completion items (lower sorts first). sortText is built as
// fmt.Sprintf("%02d%s", tier, label) so items merge into one client-side sort
// order within a list.
//
// tierLocal and tierContext are both 5 by design: a Go-expression context
// (locals/params) and a gsx-native context (filters/components/attrs) never
// populate the same completion list, since classifyCompletionContext picks
// exactly one completionContextKind per cursor position. The shared value
// cannot collide.
const (
	tierLocal     = 5  // locals, params
	tierMember    = 10 // +embedding depth (10..29 clamped)
	tierPackage   = 30 // package-scope decls
	tierImported  = 40 // imported package names / their members
	tierUniverse  = 50
	tierKeyword   = 60
	tierContext   = 5  // context-native items: filters, components, attrs
	tierSecondary = 20 // e.g. HTML tags merged under a capitalized prefix
)

// completionTokenSpan scans identifier bytes backward from off in text and
// returns the [start, off) byte span of the token being completed. Identifier
// bytes: letters, digits, '_', and — when allowDash is set (attr/tag
// contexts) — '-'. A dot is never part of the token: member completion
// replaces only the selector after the last '.', never the receiver
// expression before it.
func completionTokenSpan(text string, off int, allowDash bool) (start, end int) {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	start = off
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || (allowDash && r == '-') {
			start -= size
			continue
		}
		break
	}
	return start, off
}

// startsWithUpper reports whether s's first rune is an uppercase letter. Used
// by ctxTag completion to classify the typed tag token (component names are
// capitalized by convention); the result does not affect ranking until Task
// 15 merges HTML tag names into the same completion list.
func startsWithUpper(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// newCompletionItem builds a CompletionItem whose TextEdit replaces [start,end)
// in text with newText, expressed in ORIGINAL buffer coordinates via
// rangeForSpan and the negotiated encoding enc. sortText is
// fmt.Sprintf("%02d%s", tier, label) so every tier merges into one
// client-side sort order. FilterText is set to newText only when it differs
// from label (e.g. an attribute insert of `name=""`), so the client keeps
// matching against what the user actually typed.
func newCompletionItem(text string, start, end int, enc encoding, label, newText string, kind, tier int, detail string, doc *MarkupContent) CompletionItem {
	item := CompletionItem{
		Label:         label,
		Kind:          kind,
		Detail:        detail,
		Documentation: doc,
		SortText:      fmt.Sprintf("%02d%s", tier, label),
		TextEdit: &TextEdit{
			Range:   rangeForSpan(text, start, end, enc),
			NewText: newText,
		},
	}
	if newText != label {
		item.FilterText = newText
	}
	return item
}
