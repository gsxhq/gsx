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
			stack = fillStep(stack, c, width, pos, tabWidth)
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
// Content is measured as Print will lay it out (see measure), so a group that
// breaks counts only up to its first break, the same as the forced group it
// re-parses into. The separator before a content breaks when the content does
// not fit on the line, and also when it fits only by breaking a group that
// would stay flat at the start of a fresh line (see joins).
func fillStep(stack []cmd, c cmd, width, pos, tabWidth int) []cmd {
	parts := c.doc.parts
	if len(parts) == 0 {
		return stack
	}
	remaining := width - pos
	content := cmd{c.indent, modeFlat, parts[0]}
	contentFits := fits(remaining, content, nil, tabWidth, groupsAsPrinted)
	if len(parts) == 1 {
		m := modeBreak
		if contentFits {
			m = modeFlat
		}
		return append(stack, cmd{c.indent, m, parts[0]})
	}
	sep := parts[1]
	if len(parts) == 2 {
		if contentFits {
			stack = append(stack, cmd{c.indent, modeFlat, sep})
			return append(stack, cmd{c.indent, modeFlat, parts[0]})
		}
		stack = append(stack, cmd{c.indent, modeBreak, sep})
		return append(stack, cmd{c.indent, modeBreak, parts[0]})
	}
	rest := cmd{c.indent, c.mode, Doc{kind: kindFill, parts: parts[2:]}}
	// The separator stays flat when the next content joins the line after this
	// one. When this content breaks, the separator stays flat, as it always has
	// for a forced group: the measure stops at the first break and does not see
	// the last line the next content lands on.
	pairFits := false
	switch left, broke := measure(remaining, cmd{c.indent, modeFlat, Concat(parts[0], sep)}, nil, tabWidth, groupsAsPrinted); {
	case left < 0:
	case broke:
		pairFits = true
	default:
		pairFits = joins(cmd{c.indent, modeFlat, parts[2]}, left, width-c.indent*tabWidth, tabWidth)
	}
	// Push in reverse so content is processed first, then separator, then rest.
	stack = append(stack, rest)
	switch {
	case pairFits:
		stack = append(stack, cmd{c.indent, modeFlat, sep})
		stack = append(stack, cmd{c.indent, modeFlat, parts[0]})
	case contentFits:
		stack = append(stack, cmd{c.indent, modeBreak, sep})
		stack = append(stack, cmd{c.indent, modeFlat, parts[0]})
	default:
		stack = append(stack, cmd{c.indent, modeBreak, sep})
		stack = append(stack, cmd{c.indent, modeBreak, parts[0]})
	}
	return stack
}

// joins reports whether content stays on the current line, with remaining
// columns left, rather than starting a fresh line of fresh columns. It joins
// when it fits as it would print here, unless that takes breaking a group which
// would stay flat on the fresh line: then the break before it buys a flat
// group. A group that breaks either way breaks here, keeping the line; the
// output re-parses with that group forced, which measures the same.
func joins(content cmd, remaining, fresh, tabWidth int) bool {
	if fits(remaining, content, nil, tabWidth, groupsFlat) {
		return true
	}
	return fits(remaining, content, nil, tabWidth, groupsAsPrinted) && !fits(fresh, content, nil, tabWidth, groupsFlat)
}

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
func fits(remaining int, next cmd, rest []cmd, tabWidth int, groups groupPolicy) bool {
	left, _ := measure(remaining, next, rest, tabWidth, groups)
	return left >= 0
}

// groupPolicy is how measure lays out a non-forced Group. A forced group is
// always measured broken, up to its first break.
type groupPolicy uint8

const (
	// groupsInherit measures the group in its parent's mode: the flat
	// question Print's kindGroup case asks of a group's own child.
	groupsInherit groupPolicy = iota
	// groupsFlat measures every non-forced group flat: the content as it
	// prints when no group breaks that is not already broken.
	groupsFlat
	// groupsAsPrinted makes the decision Print will make: Print decides every
	// non-forced group on its own, whatever mode its parent was pushed in, so
	// a group that does not fit flat prints broken and occupies the line only
	// up to its first break — measured as the forced group it re-parses into.
	groupsAsPrinted
)

// measure walks next and then rest the way fits does and returns the columns
// left on the line — negative once it overflows — and whether it stopped at a
// break rather than running out of commands.
func measure(remaining int, next cmd, rest []cmd, tabWidth int, groups groupPolicy) (left int, broke bool) {
	if remaining < 0 {
		return remaining, false
	}
	local := []cmd{next}
	restIdx := len(rest)
	for {
		if len(local) == 0 {
			if restIdx == 0 {
				return remaining, false
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
				return remaining, false
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
			case groups == groupsFlat:
				gm = modeFlat
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
				return remaining, true
			}
			remaining -= utf8.RuneCountInString(c.doc.text) + (tabWidth-1)*strings.Count(c.doc.text, "\t")
			if remaining < 0 {
				return remaining, false
			}
		case kindIfBreak:
			if c.mode == modeBreak {
				local = append(local, cmd{c.indent, c.mode, c.doc.parts[0]})
			} else {
				local = append(local, cmd{c.indent, c.mode, c.doc.parts[1]})
			}
		case kindBreakParent:
			// ignored in fits; propagation handled by the forced flag.
		}
	}
}
