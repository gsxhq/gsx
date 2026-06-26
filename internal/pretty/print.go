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
		case kindConcat, kindFill:
			for i := len(c.doc.parts) - 1; i >= 0; i-- {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[i]})
			}
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
