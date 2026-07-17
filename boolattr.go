package gsx

import "strings"

// Toggle forces boolean-attribute (presence) semantics on any attribute name,
// bypassing IsBooleanAttr: Toggle(true) writes a bare ` name`, Toggle(false)
// writes nothing. It exists for names the HTML spec cannot know — web components,
// Datastar directives — where a plain bool would otherwise stringify to
// "true"/"false".
//
// It is a value, not syntax, so the same expression works on an element, as a
// component prop, and in a hand-written bag: gsx.Toggle(b) travels to the leaf
// where the presence decision is actually made.
type Toggle bool

// IsBooleanAttr reports whether name is an HTML boolean (presence-only)
// attribute: one where presence alone means true and the value is ignored, so
// only ABSENCE can express false. Matching is ASCII-case-insensitive (HTML
// attribute names fold).
//
// This is the single source of truth for boolean-attribute classification, used
// by both the runtime (Spread, on a bag's bool value) and codegen (a static
// name={ boolExpr }, at generate time). A bool value on such a name renders as a
// bare attribute or is omitted; a bool value on any other name stringifies to
// "true"/"false", which is what enumerated attributes (aria-*, contenteditable,
// data-*) require. The list is consulted only for bool-typed values, so a string
// value like required="foo" is never affected — gsx does not police HTML.
func IsBooleanAttr(name string) bool {
	return booleanAttrs[strings.ToLower(name)]
}

// booleanAttrs is the effective presence-only set: the WHATWG-derived core plus
// the curated extras, keyed lowercase.
//
// MEMBERSHIP RULE — is there a string that means false? No → only absence can
// express it → it belongs here. The WHATWG "Value: Boolean attribute" column is
// a PROXY for this and is wrong in both directions (see presenceOnlyExtras and
// the guard test), so the rule, not the column, decides.
//
// Derived from the WHATWG HTML index of attributes (Value == "Boolean
// attribute"), 2026-07 snapshot, cross-checked against Vue's isBooleanAttr
// (@vue/shared) and React's HTML attribute table. Obsolete-table entries
// (nowrap, compact, declare, scoped, seamless) are deliberately excluded.
var booleanAttrs = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range booleanAttrCore {
		m[n] = true
	}
	for _, n := range presenceOnlyExtras {
		m[n] = true
	}
	return m
}()

// booleanAttrCore is the mechanically-derivable part: every current-index row
// typed "Boolean attribute". Regenerate wholesale from the index; it carries no
// judgement.
var booleanAttrCore = []string{
	"allowfullscreen",
	"async",
	"autofocus",
	"autoplay",
	"checked",
	"controls",
	"default",
	"defer",
	"disabled",
	"formnovalidate",
	"inert",
	"ismap",
	"itemscope",
	"loop",
	"multiple",
	"muted",
	"nomodule",
	"novalidate",
	"open",
	"playsinline",
	"readonly",
	"required",
	"reversed",
	"selected",
}

// presenceOnlyExtras are attributes the index does NOT type "Boolean attribute"
// but for which no string means false — so a bool value must toggle, not
// stringify. Hand-curated; each entry has a reason and MUST survive a
// regeneration of booleanAttrCore.
//
//   - hidden:   the index types it *enumerated* (until-found / hidden / ""), but
//     its INVALID VALUE DEFAULT is the Hidden state — so hidden="false" HIDES the
//     element. A bool value must toggle. (A string value like "until-found" is
//     untouched, since the list is consulted only for bools.)
//   - download: the index types it *Text* (a filename), but download="true"
//     would be a file literally named "true". A bool value must toggle.
var presenceOnlyExtras = []string{
	"hidden",
	"download",
}
