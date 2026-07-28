package gsx

import "strings"

// Toggle forces boolean-attribute (presence) semantics on any attribute name,
// bypassing BoolRendersBare: Toggle(true) writes a bare ` name`, Toggle(false)
// writes nothing. Its remaining use is the names where the platform DOES define
// a value vocabulary but the author wants presence anyway — a plain bool on a
// name the platform never defined already toggles on its own.
//
// It is a value, not syntax, so the same expression works on an element, as a
// component prop, and in a hand-written bag: gsx.Toggle(b) travels to the leaf
// where the presence decision is actually made.
//
// gsx also uses Toggle internally when a syntactically bare attribute must
// travel through an Attrs bag, carrying its authored presence to the leaf
// independent of what the name tables say.
type Toggle bool

// BoolRendersBare reports whether a bool value on this attribute name renders
// as presence — bare when true, omitted when false — rather than as the string
// "true"/"false". It is the single source of truth for that decision, consulted
// by codegen for a static name={boolExpr} and by the runtime at the bag leaf.
//
// ONE RULE: a bool renders as presence UNLESS HTML's own value vocabulary for
// that name is the literal strings "true" and "false" — the aria-* states plus
// draggable, spellcheck, contenteditable and writingsuggestions. On those the
// string IS the state and absence means something else entirely: a screen
// reader announces aria-expanded="false" as collapsed and says nothing at all
// when the attribute is absent, and contenteditable/spellcheck inherit, so only
// the literal "false" opts a subtree out.
//
// Everywhere else — data-*, custom-element attributes, x-*, hx-*, an author's
// own name — a bool can only mean presence, and presence is what CSS reads:
// [data-open] and Tailwind's data-open: variant match data-open="false" too, so
// the stringified form would fire styles while the thing is closed.
//
// Consulted only for bool-typed values, so a string value is never diverted
// here: data-x="false" stays data-x="false", and a JS-expression attribute
// written the normal way (x-show=js`open`) is untouched. To get the string form
// from a Go bool, say so — data-x={strconv.FormatBool(b)}. To force presence on
// a name that takes a value, use Toggle.
func BoolRendersBare(name string) bool {
	lower := strings.ToLower(name)
	return !trueFalseAttr(lower) && !trueFalseExtras[lower]
}

// trueFalseExtras are enumerated true/false attributes the generated
// trueFalseAttr table misses because the vendored dataset records no value
// vocabulary for them. Each entry states why HTML's "false" is load-bearing;
// each MUST survive a dataset refresh.
//
//   - contenteditable: enumerated true/false/plaintext-only, and it INHERITS —
//     only contenteditable="false" opts a subtree out of an editable ancestor.
//   - writingsuggestions: enumerated true/false, added to HTML after this
//     dataset snapshot, and it inherits the same way.
var trueFalseExtras = map[string]bool{
	"contenteditable":    true,
	"writingsuggestions": true,
}

// IsBooleanAttr reports whether name is an HTML boolean (presence-only)
// attribute: one where presence alone means true and the value is ignored, so
// only ABSENCE can express false. Matching is ASCII-case-insensitive (HTML
// attribute names fold).
//
// This is the single source of truth for boolean-attribute classification, and
// it answers ONLY the HTML question — LSP completion uses it to insert a bare
// name. It is deliberately not part of the render decision: BoolRendersBare
// renders these bare because nothing gives them a true/false vocabulary, not
// because they are on this list.
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
// but for which no string means false. Hand-curated; each entry has a reason and
// MUST survive a regeneration of booleanAttrCore. They matter for the HTML
// classification IsBooleanAttr exposes (LSP completion offers them bare), not
// for rendering — BoolRendersBare reaches the same answer without a list.
//
//   - hidden:   the index types it *enumerated* (until-found / hidden / ""), but
//     its INVALID VALUE DEFAULT is the Hidden state — so hidden="false" HIDES the
//     element. Only absence expresses false.
//   - download: the index types it *Text* (a filename), but download="true"
//     would be a file literally named "true".
var presenceOnlyExtras = []string{
	"hidden",
	"download",
}
