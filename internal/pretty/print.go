package pretty

import (
	"strings"
	"unicode/utf8"
)

const (
	defaultWidth = 80
	tabWidth     = 4
)

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

// Print renders d at the given right margin (columns). width <= 0 uses 80.
// Indentation is emitted as tabs; each tab counts as tabWidth columns when
// measuring fit.
func Print(d Doc, width int) string {
	if width <= 0 {
		width = defaultWidth
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
			pos = advance(pos, c.doc.text)
		case kindConcat:
			for i := len(c.doc.parts) - 1; i >= 0; i-- {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[i]})
			}
		case kindFill:
			stack = fillStep(stack, c, width-pos)
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
			} else if fits(width-pos, cmd{c.indent, modeFlat, child}, stack) {
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
func fillStep(stack []cmd, c cmd, remaining int) []cmd {
	parts := c.doc.parts
	if len(parts) == 0 {
		return stack
	}
	content := cmd{c.indent, modeFlat, parts[0]}
	contentFits := fits(remaining, content, nil)
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
	pair := cmd{c.indent, modeFlat, Concat(parts[0], sep, parts[2])}
	pairFits := fits(remaining, pair, nil)
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

// advance returns the new column after writing s, accounting for embedded
// newlines in verbatim (preserved) Text.
func advance(pos int, s string) int {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return utf8.RuneCountInString(s[i+1:])
	}
	return pos + utf8.RuneCountInString(s)
}

// fits reports whether next (a group's child, in flat mode) followed by the
// remaining commands rest (in their own modes) fits within remaining columns on
// the current line. It stops — returning true — at the first break it would
// emit (a hard line, or a line in break mode), so trailing same-line content
// after the group is correctly counted. rest is a LIFO stack (last = next).
func fits(remaining int, next cmd, rest []cmd) bool {
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
			remaining -= utf8.RuneCountInString(c.doc.text)
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
			if c.doc.forced {
				gm = modeBreak
			}
			local = append(local, cmd{c.indent, gm, c.doc.parts[0]})
		case kindLine:
			if c.doc.hard || c.mode == modeBreak {
				return true
			}
			remaining -= utf8.RuneCountInString(c.doc.text)
			if remaining < 0 {
				return false
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
