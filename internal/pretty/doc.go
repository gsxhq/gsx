// Package pretty is a language-agnostic Wadler/Prettier-style pretty-printing
// document model. Build a Doc with the constructors (Text, Concat, Group, …)
// and render it to a width-bounded string with Print. It has no dependency
// beyond the Go standard library so it can be shared across formatters (gsx
// markup today; JS and CSS bodies later).
package pretty

import "slices"

// kind tags the Doc variant.
type kind uint8

const (
	kindText kind = iota
	kindConcat
	kindIndent
	kindLine        // a soft/space/hard break candidate (see flat, hard)
	kindGroup       // flat if it fits, else broken
	kindFill        // greedy per-element wrap (Task 2)
	kindIfBreak     // parts[0] when broken, parts[1] when flat (Task 2)
	kindBreakParent // forces the nearest enclosing Group to break
)

// Doc is an opaque pretty-printing document. The zero Doc is an empty Text.
// All compound variants store their children in parts; single-child variants
// (Indent, Group) use parts[0]; IfBreak uses parts[0]=broken, parts[1]=flat.
type Doc struct {
	kind   kind
	text   string // kindText content; kindLine flat representation
	hard   bool   // kindLine: a hard break (always newline; forces parents)
	forced bool   // kindGroup: precomputed "must break" (contains a forced break)
	parts  []Doc
}

// Text is a literal fragment. For verbatim multi-line content (e.g. preserved
// <pre> bodies) the string MAY contain newlines; the engine writes it as-is and
// resets the column to after the last newline. Normal markup Text never embeds
// a newline (cosmetic breaks are modeled with Line/HardLine).
func Text(s string) Doc { return Doc{kind: kindText, text: s} }

// Concat renders ds in order with no separator.
func Concat(ds ...Doc) Doc { return Doc{kind: kindConcat, parts: ds} }

// Indent renders d with the break-indent increased by one tab level.
func Indent(d Doc) Doc { return Doc{kind: kindIndent, parts: []Doc{d}} }

// Group renders d flat if it fits the remaining width on the current line,
// else broken. A group containing any hard break (HardLine/BreakParent, at any
// depth, including inside nested groups) is forced to break.
func Group(d Doc) Doc { return Doc{kind: kindGroup, parts: []Doc{d}, forced: containsForcedBreak(d)} }

// Line is a break candidate that renders as a single space when flat.
var Line = Doc{kind: kindLine, text: " "}

// SoftLine is a break candidate that renders as nothing when flat.
var SoftLine = Doc{kind: kindLine, text: ""}

// HardLine always renders as a newline + indent and forces every enclosing
// Group to break.
var HardLine = Doc{kind: kindLine, text: "", hard: true}

// BreakParent forces the nearest enclosing Group to break. It emits nothing.
var BreakParent = Doc{kind: kindBreakParent}

// Fill is a greedy per-element layout over an alternating list
// [content, separator, content, separator, …, content]: it keeps content on
// the current line until the next content would not fit, breaking the
// separator before it. Provided for the JS/CSS formatters; the gsx markup
// printer uses Group/SoftLine (all-or-nothing) instead.
func Fill(ds ...Doc) Doc { return Doc{kind: kindFill, parts: ds} }

// IfBreak renders broken when the enclosing Group breaks, else flat.
func IfBreak(broken, flat Doc) Doc { return Doc{kind: kindIfBreak, parts: []Doc{broken, flat}} }

// containsForcedBreak reports whether d carries a forced break that must
// propagate to an enclosing group. A nested Group already has its forced flag
// computed (Docs are built inside-out), so a forced inner group propagates.
func containsForcedBreak(d Doc) bool {
	switch d.kind {
	case kindLine:
		return d.hard
	case kindBreakParent:
		return true
	case kindGroup:
		return d.forced
	case kindIndent:
		return containsForcedBreak(d.parts[0])
	case kindConcat, kindFill:
		return slices.ContainsFunc(d.parts, containsForcedBreak)
	case kindIfBreak:
		// Only the broken branch (parts[0]) participates in the break decision:
		// when the enclosing group is flat it renders parts[1], so a forced
		// break in parts[1] must not propagate and force the group to break.
		return containsForcedBreak(d.parts[0])
	default:
		return false
	}
}
