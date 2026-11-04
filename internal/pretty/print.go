package pretty

import (
	"strings"
	"unicode/utf8"
)

// DefaultPrintWidth is the target line width when nothing is configured. It is
// the ONLY default: the sites that used to spell 80 in-line (this package's own
// fallback, gen's config fallback, and effectivePrintWidth) all read it, so they
// cannot drift apart.
//
// 120 rather than 80 because gsx markup nests, and every level of nesting spends
// part of the budget on indentation before one character of content is printed.
// At 80 an element six levels deep has almost nothing left and gets broken one
// attribute per line for a reason no reader would recognize.
const DefaultPrintWidth = 120

// DefaultTabWidth is the column width of one tab when nothing is configured.
// Indentation is emitted as tabs; this is how many columns each one occupies
// when deciding whether a group fits. It applies both to the printer's own
// indent levels and to literal tabs inside a Text doc — the same physical tab
// in the same output file, which must be measured the same way.
const DefaultTabWidth = 2

type mode uint8

const (
	modeFlat mode = iota
	modeBreak
)

type cmd struct {
	indent int
	mode   mode
	doc    Doc
}

// Print renders d at the given right margin (columns). width <= 0 uses
// DefaultPrintWidth. Indentation is emitted as tabs; each tab counts as
// tabWidth columns when measuring fit.
func Print(d Doc, width, tabWidth int) string {
	if width <= 0 {
		width = DefaultPrintWidth
	}
	if tabWidth <= 0 {
		tabWidth = DefaultTabWidth
	}
	var b strings.Builder
	pos := 0
	stack := []cmd{{indent: 0, mode: modeBreak, doc: d}}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch c.doc.kind {
		case kindText:
			b.WriteString(c.doc.text)
			pos = advance(pos, c.doc.text, tabWidth)
		case kindConcat:
			for i := len(c.doc.parts) - 1; i >= 0; i-- {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[i]})
			}
		case kindFill:
			stack = fillStep(stack, c, width-pos, tabWidth)
		case kindFillSep:
			m := modeBreak
			if fits(width-pos, cmd{c.indent, modeFlat, Concat(c.doc.parts...)}, nil, tabWidth, groupsAsPrinted) {
				m = modeFlat
			}
			stack = append(stack, cmd{c.indent, m, c.doc.parts[0]})
		case kindIndent:
			stack = append(stack, cmd{c.indent + 1, c.mode, c.doc.parts[0]})
		case kindLine:
			if c.doc.hard || c.mode == modeBreak {
				b.WriteByte('\n')
				for i := 0; i < c.indent; i++ {
					b.WriteByte('\t')
				}
				pos = c.indent * tabWidth
			} else {
				b.WriteString(c.doc.text)
				pos += utf8.RuneCountInString(c.doc.text)
			}
		case kindGroup:
			child := c.doc.parts[0]
			if c.doc.forced {
				stack = append(stack, cmd{c.indent, modeBreak, child})
			} else if fits(width-pos, cmd{c.indent, modeFlat, child}, stack, tabWidth, groupsInherit) {
				stack = append(stack, cmd{c.indent, modeFlat, child})
			} else {
				stack = append(stack, cmd{c.indent, modeBreak, child})
			}
		case kindIfBreak:
			if c.mode == modeBreak {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[0]})
			} else {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[1]})
			}
		case kindBreakParent:
			// no output; its effect is via the forced flag on enclosing groups.
		}
	}
	return b.String()
}

// fillStep implements one step of the greedy Fill layout, pushing onto stack
// (LIFO) the commands to process next. parts alternate content/separator.
//
// The separator after a content is decided once that content has printed (see
// kindFillSep), at the column where its last line really ends: it stays flat
// when the next content's first line fits there. Content is measured as Print
// lays it out (groupsAsPrinted), so a group that breaks counts up to its first
// break, the same as the forced group it re-parses into.
func fillStep(stack []cmd, c cmd, remaining int, tabWidth int) []cmd {
	parts := c.doc.parts
	if len(parts) == 0 {
		return stack
	}
	m := modeBreak
	if fits(remaining, cmd{c.indent, modeFlat, parts[0]}, nil, tabWidth, groupsAsPrinted) {
		m = modeFlat
	}
	if len(parts) > 1 {
		var next Doc
		if len(parts) > 2 {
			next = parts[2]
			stack = append(stack, cmd{c.indent, c.mode, Doc{kind: kindFill, parts: parts[2:]}})
		}
		stack = append(stack, cmd{c.indent, c.mode, Doc{kind: kindFillSep, parts: []Doc{parts[1], next}}})
	}
	return append(stack, cmd{c.indent, m, parts[0]})
}

// groupPolicy is how fits measures a non-forced Group. A forced group is
// always measured broken, up to its first break.
type groupPolicy uint8

const (
	// groupsInherit measures the group in its parent's mode: the flat
	// question Print's kindGroup case asks of a group's own child.
	groupsInherit groupPolicy = iota
	// groupsAsPrinted makes the decision Print will make. Print decides every
	// non-forced group on its own, whatever mode its parent was pushed in, so
	// a group that does not fit flat prints broken and occupies the line only
	// up to its first break, measured like the forced group it re-parses into.
	groupsAsPrinted
)

// advance returns the column after writing s starting at column pos. A literal
// tab counts as tabWidth columns, matching how the printer's own indent levels
// are measured — otherwise the same tab is one column here and tabWidth there.
func advance(pos int, s string, tabWidth int) int {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
		pos = 0
	}
	return pos + utf8.RuneCountInString(s) + (tabWidth-1)*strings.Count(s, "\t")
}

// fits reports whether next (a group's child, in flat mode) followed by the
// remaining commands rest (in their own modes) fits within remaining columns on
// the current line. It stops — returning true — at the first break it would
// emit (a hard line, or a line in break mode), so trailing same-line content
// after the group is correctly counted. rest is a LIFO stack (last = next).
// groups says how a non-forced Group is measured.
func fits(remaining int, next cmd, rest []cmd, tabWidth int, groups groupPolicy) bool {
	if remaining < 0 {
		return false
	}
	local := []cmd{next}
	restIdx := len(rest)
	for {
		if len(local) == 0 {
			if restIdx == 0 {
				return true
			}
			restIdx--
			local = append(local, rest[restIdx])
			continue
		}
		c := local[len(local)-1]
		local = local[:len(local)-1]
		switch c.doc.kind {
		case kindText:
			remaining -= utf8.RuneCountInString(c.doc.text) + (tabWidth-1)*strings.Count(c.doc.text, "\t")
			if remaining < 0 {
				return false
			}
		case kindConcat, kindFill:
			for i := len(c.doc.parts) - 1; i >= 0; i-- {
				local = append(local, cmd{c.indent, c.mode, c.doc.parts[i]})
			}
		case kindIndent:
			local = append(local, cmd{c.indent + 1, c.mode, c.doc.parts[0]})
		case kindGroup:
			gm := c.mode
			switch {
			case c.doc.forced:
				gm = modeBreak
			case groups == groupsAsPrinted:
				// The decision Print's kindGroup case makes, against what
				// follows the group here.
				after := append(rest[:restIdx:restIdx], local...)
				if fits(remaining, cmd{c.indent, modeFlat, c.doc.parts[0]}, after, tabWidth, groupsInherit) {
					gm = modeFlat
				} else {
					gm = modeBreak
				}
			}
			local = append(local, cmd{c.indent, gm, c.doc.parts[0]})
		case kindLine:
			if c.doc.hard || c.mode == modeBreak {
				return true
			}
			remaining -= utf8.RuneCountInString(c.doc.text) + (tabWidth-1)*strings.Count(c.doc.text, "\t")
			if remaining < 0 {
				return false
			}
		case kindIfBreak:
			if c.mode == modeBreak {
				local = append(local, cmd{c.indent, c.mode, c.doc.parts[0]})
			} else {
				local = append(local, cmd{c.indent, c.mode, c.doc.parts[1]})
			}
		case kindFillSep:
			// Decided only once the content before it has printed, so
			// nothing before it can count on it staying flat.
			return true
		case kindBreakParent:
			// ignored in fits; propagation handled by the forced flag.
		}
	}
}
