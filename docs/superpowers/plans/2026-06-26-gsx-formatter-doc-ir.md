# gsx Formatter: Width-Aware Layout on a Doc IR — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the gsx printer's purely-structural, render-direct layout with a width-aware pretty-printer built on a reusable, language-agnostic document IR, fixing the over-collapsing bug and adding attribute-list wrapping + multi-line attribute values.

**Architecture:** A new `internal/pretty` package implements a Wadler/Prettier-style document model (`Text`, `Concat`, `Indent`, `Group`, `Line`/`SoftLine`/`HardLine`, `Fill`, `IfBreak`, `BreakParent`) plus a `Print(doc, width)` engine using the canonical `fits`/best two-function algorithm with forced-break propagation. The gsx printer (`internal/printer`) is rebuilt to *build Docs* instead of writing strings; relative indentation via `Indent` removes all manual depth threading. `printWidth` is discovered from `gsx.toml` in `gen` and threaded down as a plain `int`.

**Tech Stack:** Go (stdlib only: `strings`, `unicode/utf8`, `go/format`, `go/parser`, `go/token`, `go/ast`), `github.com/BurntSushi/toml` (already a dep), the existing `github.com/gsxhq/gsx/ast` package.

## Global Constraints

- Module path: `github.com/gsxhq/gsx`. New package import path: `github.com/gsxhq/gsx/internal/pretty`.
- `internal/pretty` depends ONLY on the Go standard library — no `ast`, `gen`, or gsx-specific imports (it is the shared foundation for the in-progress JS/CSS formatters).
- `internal/printer` and `internal/gsxfmt` MUST NOT import `gen` (avoid an import cycle). Width arrives as a plain `int` parameter.
- Two standing contracts must hold for every formatter change, enforced by `internal/printer/corpus_test.go`:
  - **Render-faithfulness:** `normalizedAST(src) == normalizedAST(format(src))`.
  - **Idempotence:** `format(format(src)) == format(src)`.
- Print width default is **80**. Tab indentation is emitted as literal `\t` and measured at **4 columns** for fit checks. Column/length measurement uses `utf8.RuneCountInString` (gsx text contains multibyte glyphs like `·`).
- Layout discipline for gsx children/control-flow lists: **all-or-nothing per list** (`Group` + `SoftLine`), never greedy `Fill`.
- Unexported by default (lowercase) unless serialization or cross-package use requires export.
- Commit after every green step. Commit messages end with:
  `Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk`
- Run tests from the worktree root: `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/fmt-doc-ir`.

## File Structure

| File | Responsibility | Tasks |
| --- | --- | --- |
| `internal/pretty/doc.go` | Doc value type, kinds, constructors, forced-break propagation | 1, 2 |
| `internal/pretty/print.go` | `Print` engine + `fits` lookahead | 1, 2 |
| `internal/pretty/doc_test.go` | IR behavioral unit tests | 1, 2 |
| `internal/printer/segment.go` | `segmentChildren`: whitespace-safe segmentation + breakable/edge-guard | 3 |
| `internal/printer/segment_test.go` | segmentation unit tests | 3 |
| `internal/printer/printer.go` | Doc-building printer (rebuilt) | 4, 5, 6 |
| `internal/printer/printer_test.go` | exact-output unit tests (some updated) | 4, 5, 6 |
| `internal/printer/corpus_test.go` | property tests (unchanged); new fixtures added under corpus | 4, 5, 6, 8 |
| `internal/gsxfmt/gsxfmt.go` | add `width int` param, pass to `Fprint` | 4 |
| `gen/fmt.go` | CLI `runFmt`: resolve width per dir, pass down; `Format` wrapper | 4, 7 |
| `gen/configfile.go` | `printWidth` TOML key | 7 |
| `gen/options.go` (or wherever `config` struct lives) | `printWidth` field + default + merge | 7 |
| `gen/lsp.go` | `lspAnalyzer.PrintWidth(dir) int` | 7 |
| `internal/lsp/format.go` | call `analyzer.PrintWidth`, pass to formatter | 7 |
| `internal/lsp/server.go` | add `PrintWidth(dir string) int` to `Analyzer` interface | 7 |

---

## Task 1: Doc IR core + width-aware engine

**Files:**
- Create: `internal/pretty/doc.go`
- Create: `internal/pretty/print.go`
- Test: `internal/pretty/doc_test.go`

**Interfaces:**
- Produces:
  - `type Doc struct{ ... }` (opaque; fields unexported).
  - `func Text(s string) Doc`
  - `func Concat(ds ...Doc) Doc`
  - `func Indent(d Doc) Doc`
  - `func Group(d Doc) Doc`
  - `var Line Doc` (flat → `" "`), `var SoftLine Doc` (flat → `""`), `var HardLine Doc` (always breaks; forces enclosing groups), `var BreakParent Doc`
  - `func Print(d Doc, width int) string` (width ≤ 0 ⇒ 80)

- [ ] **Step 1: Write the failing test**

Create `internal/pretty/doc_test.go`:

```go
package pretty

import "testing"

func TestTextConcat(t *testing.T) {
	got := Print(Concat(Text("a"), Text("b"), Text("c")), 80)
	if got != "abc" {
		t.Fatalf("got %q want %q", got, "abc")
	}
}

func TestGroupFitsStaysFlat(t *testing.T) {
	// "[a, b]" is 6 cols, fits in 80 → flat (Line renders as space).
	d := Group(Concat(Text("["), Text("a,"), Line, Text("b"), Text("]")))
	got := Print(d, 80)
	want := "[a, b]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGroupOverflowBreaks(t *testing.T) {
	// Width 4 forces the group to break: each Line becomes newline+indent.
	d := Group(Concat(Text("["), Indent(Concat(SoftLine, Text("aaa,"), Line, Text("bbb"))), SoftLine, Text("]")))
	got := Print(d, 4)
	want := "[\n\taaa,\n\tbbb\n]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNestedGroupInnerStaysFlat(t *testing.T) {
	// Outer breaks (width 10), inner "(x, y)" still fits on its own line → flat.
	inner := Group(Concat(Text("("), Text("x,"), Line, Text("y"), Text(")")))
	d := Group(Concat(Text("f"), Indent(Concat(SoftLine, inner)), SoftLine))
	got := Print(d, 10)
	want := "f\n\t(x, y)\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHardLineForcesEnclosingGroupBreak(t *testing.T) {
	// The group fits in 80 cols, but a HardLine inside forces it to break.
	d := Group(Concat(Text("a"), HardLine, Text("b")))
	got := Print(d, 80)
	want := "a\nb"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTrailingContentCountedInFit(t *testing.T) {
	// Group content "x" fits in 3, but trailing "</p>" pushes the line over →
	// group breaks. Verifies fits() looks past the group into rest commands.
	group := Group(Concat(Text("<p>"), Indent(Concat(SoftLine, Text("x"))), SoftLine, Text("</p>")))
	got := Print(group, 5)
	want := "<p>\n\tx\n</p>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMultibyteWidth(t *testing.T) {
	// "·····" is 5 runes (10 bytes). At width 6 it fits flat; the assertion is
	// just that it renders verbatim (measurement uses rune count, not bytes).
	got := Print(Group(Text("·····")), 6)
	if got != "·····" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pretty/`
Expected: FAIL — `package pretty` / `Print` undefined (no source files yet).

- [ ] **Step 3: Write `internal/pretty/doc.go`**

```go
// Package pretty is a language-agnostic Wadler/Prettier-style pretty-printing
// document model. Build a Doc with the constructors (Text, Concat, Group, …)
// and render it to a width-bounded string with Print. It has no dependency
// beyond the Go standard library so it can be shared across formatters (gsx
// markup today; JS and CSS bodies later).
package pretty

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
		for _, p := range d.parts {
			if containsForcedBreak(p) {
				return true
			}
		}
		return false
	case kindIfBreak:
		return containsForcedBreak(d.parts[0]) || containsForcedBreak(d.parts[1])
	default:
		return false
	}
}
```

- [ ] **Step 4: Write `internal/pretty/print.go`**

```go
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/pretty/`
Expected: PASS (all Task 1 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/pretty/doc.go internal/pretty/print.go internal/pretty/doc_test.go
git commit -m "feat(pretty): Wadler/Prettier-style Doc IR + width-aware engine

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 2: Fill, IfBreak primitives

**Files:**
- Modify: `internal/pretty/doc.go` (add `Fill`, `IfBreak` constructors)
- Modify: `internal/pretty/print.go` (handle `kindFill` greedy layout)
- Test: `internal/pretty/doc_test.go`

**Interfaces:**
- Consumes: everything from Task 1.
- Produces:
  - `func Fill(ds ...Doc) Doc` — alternating [content, separator, content, …]; greedy wrap.
  - `func IfBreak(broken, flat Doc) Doc`.

- [ ] **Step 1: Write the failing test**

Append to `internal/pretty/doc_test.go`:

```go
func TestIfBreakFlatAndBroken(t *testing.T) {
	// Flat: trailing comma suppressed; Broken: trailing comma added.
	mk := func() Doc {
		return Group(Concat(
			Text("["),
			Indent(Concat(SoftLine, Text("a"), Text(","), Line, Text("b"), IfBreak(Text(","), Text("")))),
			SoftLine, Text("]"),
		))
	}
	if got := Print(mk(), 80); got != "[a, b]" {
		t.Fatalf("flat: got %q want %q", got, "[a, b]")
	}
	if got := Print(mk(), 4); got != "[\n\ta,\n\tb,\n]" {
		t.Fatalf("broken: got %q want %q", got, "[\n\ta,\n\tb,\n]")
	}
}

func TestFillGreedyWrap(t *testing.T) {
	// Words separated by Line; width 5 packs greedily: "aa bb" fits (5), next
	// "cc" would overflow → break before it.
	d := Fill(Text("aa"), Line, Text("bb"), Line, Text("cc"))
	got := Print(d, 5)
	want := "aa bb\ncc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pretty/ -run 'TestIfBreak|TestFill'`
Expected: FAIL — `Fill` / `IfBreak` undefined.

- [ ] **Step 3: Add constructors in `doc.go`**

Add after the `BreakParent` declaration:

```go
// Fill is a greedy per-element layout over an alternating list
// [content, separator, content, separator, …, content]: it keeps content on
// the current line until the next content would not fit, breaking the
// separator before it. Provided for the JS/CSS formatters; the gsx markup
// printer uses Group/SoftLine (all-or-nothing) instead.
func Fill(ds ...Doc) Doc { return Doc{kind: kindFill, parts: ds} }

// IfBreak renders broken when the enclosing Group breaks, else flat.
func IfBreak(broken, flat Doc) Doc { return Doc{kind: kindIfBreak, parts: []Doc{broken, flat}} }
```

- [ ] **Step 4: Replace the `kindFill` handling in `print.go`**

In `Print`, the `kindConcat, kindFill` case currently treats Fill like Concat. Split Fill into its own case implementing the standard algorithm. Change:

```go
		case kindConcat, kindFill:
			for i := len(c.doc.parts) - 1; i >= 0; i-- {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[i]})
			}
```

to:

```go
		case kindConcat:
			for i := len(c.doc.parts) - 1; i >= 0; i-- {
				stack = append(stack, cmd{c.indent, c.mode, c.doc.parts[i]})
			}
		case kindFill:
			stack = fillStep(stack, c, width-pos)
```

Then add `fillStep` to `print.go`:

```go
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
```

Note: `fits` already handles `kindFill` in its switch (treated like Concat for measurement), so no change there.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/pretty/`
Expected: PASS (all Task 1 + Task 2 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/pretty/doc.go internal/pretty/print.go internal/pretty/doc_test.go
git commit -m "feat(pretty): add Fill (greedy wrap) and IfBreak primitives

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 3: Whitespace-safe children segmentation

This is the faithfulness core: deciding WHERE a children list may break without altering the normalized AST. It is a pure function over `[]ast.Markup`, testable in isolation.

**Files:**
- Create: `internal/printer/segment.go`
- Test: `internal/printer/segment_test.go`

**Background (significant spaces):** After `wsnorm`, a significant space lives only inside a `*ast.Text` value as a leading or trailing ASCII space. A break inserted at a boundary is faithful iff no significant space sits on either side of that boundary (a newline run there is dropped by `wsnorm` on re-parse). Block edges add whitespace right after the opener and before the closer, so a leading space on the first child or a trailing space on the last child makes block layout unsafe.

**Interfaces:**
- Consumes: `github.com/gsxhq/gsx/ast` (`ast.Markup`, `ast.Text`).
- Produces:
  - `type segment struct { nodes []ast.Markup }` — a maximal glued run.
  - `func segmentChildren(nodes []ast.Markup) (segs []segment, breakable bool)` — `breakable` is true iff the list has ≥1 safe interior boundary AND passes the edge guard. When `breakable` is false the whole list is one segment laid out inline.

- [ ] **Step 1: Write the failing test**

Create `internal/printer/segment_test.go`:

```go
package printer

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func txt(s string) *ast.Text   { return &ast.Text{Value: s} }
func interp() *ast.Interp      { return &ast.Interp{Expr: "x"} }
func elem(tag string) *ast.Element { return &ast.Element{Tag: tag} }

func segWords(segs []segment) []int {
	out := make([]int, len(segs))
	for i, s := range segs {
		out[i] = len(s.nodes)
	}
	return out
}

func TestSegmentSafeBoundaryBreakable(t *testing.T) {
	// [Text("by "), Interp, IfMarkup] — "by " glues to Interp; Interp|IfMarkup
	// is a safe boundary → two segments, breakable.
	nodes := []ast.Markup{txt("by "), interp(), &ast.IfMarkup{Cond: "c"}}
	segs, breakable := segmentChildren(nodes)
	if !breakable {
		t.Fatal("want breakable")
	}
	if got := segWords(segs); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("segments = %v, want [2 1]", got)
	}
}

func TestSegmentAllGluedNotBreakable(t *testing.T) {
	// [Text("a "), <b>, Text(" b")] — both boundaries glued → one segment, inline.
	nodes := []ast.Markup{txt("a "), elem("b"), txt(" b")}
	segs, breakable := segmentChildren(nodes)
	if breakable {
		t.Fatal("want not breakable (all glued)")
	}
	if got := segWords(segs); len(got) != 1 || got[0] != 3 {
		t.Fatalf("segments = %v, want [3]", got)
	}
}

func TestSegmentTwoBlocksBreakable(t *testing.T) {
	// [<p>, <p>] — no text, safe boundary between → two segments, breakable.
	nodes := []ast.Markup{elem("p"), elem("p")}
	segs, breakable := segmentChildren(nodes)
	if !breakable || len(segs) != 2 {
		t.Fatalf("want breakable 2 segments, got breakable=%v segs=%v", breakable, segWords(segs))
	}
}

func TestSegmentLeadingSpaceEdgeGuardForcesInline(t *testing.T) {
	// First child has a significant leading space → block opener would absorb it.
	nodes := []ast.Markup{txt(" x"), elem("p")}
	_, breakable := segmentChildren(nodes)
	if breakable {
		t.Fatal("leading significant space must force inline")
	}
}

func TestSegmentTrailingSpaceEdgeGuardForcesInline(t *testing.T) {
	// Last child has a significant trailing space → block closer would absorb it.
	nodes := []ast.Markup{elem("p"), txt("x ")}
	_, breakable := segmentChildren(nodes)
	if breakable {
		t.Fatal("trailing significant space must force inline")
	}
}

func TestSegmentInterpOnlyNotBreakable(t *testing.T) {
	// A single Interp — one segment, nothing to break.
	_, breakable := segmentChildren([]ast.Markup{interp()})
	if breakable {
		t.Fatal("single child cannot be breakable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer/ -run TestSegment`
Expected: FAIL — `segment` / `segmentChildren` undefined.

- [ ] **Step 3: Write `internal/printer/segment.go`**

```go
package printer

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// segment is a maximal run of adjacent children that must lay out on one line
// because a significant (literal) space glues them together.
type segment struct {
	nodes []ast.Markup
}

// segmentChildren splits a whitespace-normalized children list into segments at
// whitespace-safe boundaries and reports whether the list may lay out as a
// broken block.
//
// A boundary between nodes[i] and nodes[i+1] is GLUED (no break) iff a
// significant space sits on it: nodes[i] is a *ast.Text whose value ends in
// ' ', or nodes[i+1] is a *ast.Text whose value starts with ' '. Otherwise the
// boundary is SAFE and starts a new segment.
//
// breakable is true iff there is at least one safe boundary (more than one
// segment) AND the edge guard passes: the first node must not begin with a
// significant space and the last node must not end with one (a block opener /
// closer would otherwise absorb that space and change the normalized AST).
func segmentChildren(nodes []ast.Markup) (segs []segment, breakable bool) {
	if len(nodes) == 0 {
		return nil, false
	}
	cur := segment{nodes: []ast.Markup{nodes[0]}}
	for i := 1; i < len(nodes); i++ {
		if glued(nodes[i-1], nodes[i]) {
			cur.nodes = append(cur.nodes, nodes[i])
			continue
		}
		segs = append(segs, cur)
		cur = segment{nodes: []ast.Markup{nodes[i]}}
	}
	segs = append(segs, cur)

	if len(segs) < 2 {
		return segs, false
	}
	if leadsWithSpace(nodes[0]) || trailsWithSpace(nodes[len(nodes)-1]) {
		return segs, false
	}
	return segs, true
}

// glued reports whether a significant space binds left and right.
func glued(left, right ast.Markup) bool {
	return trailsWithSpace(left) || leadsWithSpace(right)
}

func leadsWithSpace(n ast.Markup) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.HasPrefix(t.Value, " ")
}

func trailsWithSpace(n ast.Markup) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.HasSuffix(t.Value, " ")
}
```

> If `go build ./internal/printer/` reports that `ast.Interp`, `ast.Element`, or `ast.IfMarkup` field names in the test differ from the real `ast` package, open `ast/` and adjust the test constructors (`interp`, `elem`) to the actual field names. The production code in `segment.go` only depends on `*ast.Text` and `.Value`, which are stable.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/printer/ -run TestSegment`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/printer/segment.go internal/printer/segment_test.go
git commit -m "feat(printer): whitespace-safe children segmentation

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 4: Rebuild the printer on the Doc IR (children + control-flow; attributes still inline)

This is the core change. The printer's methods are rewritten to BUILD `pretty.Doc` values instead of writing strings, and `Fprint` renders with `pretty.Print`. Relative `Indent` replaces all manual `depth int` threading. Children/control-flow get the new width-aware, segment-based layout (fixing the reported bug). Attributes are rendered exactly as today, but returned as a single `pretty.Text` (real attribute wrapping is Task 5). The Go-fragment helpers (`fmtExpr`, `fmtStmts`, … lines 526–868) are UNCHANGED in this task.

**Files:**
- Modify: `internal/printer/printer.go` (rewrite lines 1–524; keep the Go-fragment helpers below unchanged)
- Modify: `internal/gsxfmt/gsxfmt.go` (add `width int` param)
- Modify: `gen/fmt.go` (callers pass `80` for now; Task 7 makes it config-driven)
- Modify: `internal/lsp/format.go` (caller passes `80` for now)
- Test: `internal/printer/printer_test.go` (update exact-output expectations that now collapse/reflow)
- Test: `internal/printer/corpus_test.go` corpus dir (add the reported-bug fixture)

**Interfaces:**
- Consumes: `internal/pretty` (Task 1–2), `segmentChildren`/`segment` (Task 3).
- Produces:
  - `func Fprint(w io.Writer, f *ast.File, width int) error`
  - `gsxfmt.Format(name string, src []byte, width int) ([]byte, error)`
  - `gsxfmt.FormatRemovingImports(name string, src []byte, unused []ImportRef, width int) ([]byte, error)`

- [ ] **Step 1: Write the failing test (the reported bug + collapse-when-fits)**

Add to `internal/printer/printer_test.go`. (`normPrint`/helper conventions already exist in this file; mirror them. The shared print helper must now pass a width — use 80.)

First, find the existing print helper. The current tests call `Fprint(&buf, file)` (2-arg). Update that helper call site to pass a width; e.g. if there is a local `func mustPrint(t, src) string`, change its `Fprint` call to `Fprint(&buf, f, 80)`. Then add:

```go
func TestBlockBreaksMixedTextControlFlow(t *testing.T) {
	// The reported bug: a long <p> with text + interp + an if must break at the
	// safe boundary (Interp|IfMarkup), keeping "· <a>…</a>" glued by its space.
	src := `package p
component C() {
	<p class="text-sm text-slate-500">
		by {props.Author.Username}
		{ if props.Category.Slug != "" {
			· <a class="hover:underline" href={ categoryPage{} |> url("slug", props.Category.Slug) }>{props.Category.Name}</a>
		} }
	</p>
}`
	want := `package p

component C() {
	<p class="text-sm text-slate-500">
		by {props.Author.Username}
		{ if props.Category.Slug != "" {
			· <a class="hover:underline" href={ categoryPage{} |> url("slug", props.Category.Slug) }>{props.Category.Name}</a>
		} }
	</p>
}
`
	assertFormat(t, src, want) // assertFormat = parse → wsnorm → Fprint(…,80); see existing helpers
}

func TestShortBlockCollapsesToOneLine(t *testing.T) {
	// "true Prettier": a short block structure that fits 80 cols lays out flat.
	src := `package p
component C() {
	<div>
		<p>plain</p>
	</div>
}`
	want := `package p

component C() {
	<div><p>plain</p></div>
}
`
	assertFormat(t, src, want)
}
```

If the file has no `assertFormat` helper with this exact shape, add one near the top of `printer_test.go`:

```go
func assertFormat(t *testing.T, src, want string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "c.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := Fprint(&b, f, 80); err != nil {
		t.Fatalf("print: %v", err)
	}
	if got := b.String(); got != want {
		t.Fatalf("format mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
```

(Imports needed by the helper: `bytes`, `go/token`, `github.com/gsxhq/gsx/parser`, `github.com/gsxhq/gsx/internal/wsnorm`. Add any missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer/ -run 'TestBlockBreaksMixed|TestShortBlockCollapses'`
Expected: FAIL — `Fprint` arity (now 3-arg) and/or layout mismatch.

- [ ] **Step 3: Rewrite `internal/printer/printer.go` (lines 1–524)**

Replace everything from the top of the file DOWN TO (but NOT including) the comment block `// ---- Go-fragment formatting helpers ---` (currently line 526) with the following. Keep the Go-fragment helpers (line 526 to end) exactly as-is.

```go
// Package printer renders a (normalized) gsx AST back to canonical gsx source.
//
// Fprint assumes the AST has already been whitespace-normalized (via
// internal/wsnorm); gsx fmt does that first. The printer builds a width-aware
// pretty.Doc describing the layout, then renders it: cosmetic newlines and tab
// indentation are added only at whitespace-safe boundaries (which wsnorm drops
// on a re-parse), so the output is render-faithful and idempotent.
//
// It depends on github.com/gsxhq/gsx/ast, internal/pretty, plus go/format,
// go/parser, go/token and the standard library.
package printer

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"io"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
)

// Fprint writes the canonical gsx rendering of f to w, wrapping lists that
// exceed width columns. width <= 0 uses pretty's default (80).
func Fprint(w io.Writer, f *ast.File, width int) error {
	var p printer
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width))
	return err
}

// printer accumulates the first I/O-independent error encountered while
// building the document.
type printer struct {
	err error
}

func (p *printer) fail(format string, args ...any) pretty.Doc {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
	return pretty.Text("")
}

// file emits `package P` then each declaration separated by one blank line.
func (p *printer) file(f *ast.File) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("package "), pretty.Text(f.Package), pretty.HardLine}
	for _, d := range f.Decls {
		parts = append(parts, pretty.HardLine, p.decl(d))
	}
	parts = append(parts, pretty.HardLine)
	return pretty.Concat(parts...)
}

func (p *printer) decl(d ast.Decl) pretty.Doc {
	switch v := d.(type) {
	case *ast.GoChunk:
		return pretty.Concat(multiline(fmtGoChunk(v.Src)), pretty.HardLine)
	case *ast.Component:
		return p.component(v)
	default:
		return p.fail("printer: unknown decl type %T", d)
	}
}

// component emits `component [recv ]Name(params) {` + body + `}`. The body
// always breaks after `{` (like a Go func body): a block body puts each segment
// on its own line; an inline body sits on one indented line; the closing `}`
// sits on its own line.
func (p *printer) component(c *ast.Component) pretty.Doc {
	head := []pretty.Doc{pretty.Text("component ")}
	if c.Recv != "" {
		head = append(head, pretty.Text(fmtRecv(c.Recv)), pretty.Text(" "))
	}
	head = append(head,
		pretty.Text(c.Name), pretty.Text("("), pretty.Text(fmtParams(c.Params)), pretty.Text(") {"))

	body := pretty.Text("")
	if len(c.Body) > 0 {
		inner, _ := p.childrenInner(c.Body)
		body = pretty.Concat(pretty.Indent(pretty.Concat(pretty.HardLine, inner)))
	}
	return pretty.Concat(pretty.Concat(head...), body, pretty.HardLine, pretty.Text("}"), pretty.HardLine)
}

// childrenInner builds the inline content of a children list (the segments,
// joined by SoftLine at safe boundaries when breakable) and reports whether the
// list is breakable. For preserved subtrees use childrenPreserve instead.
func (p *printer) childrenInner(nodes []ast.Markup) (doc pretty.Doc, breakable bool) {
	segs, breakable := segmentChildren(nodes)
	parts := make([]pretty.Doc, 0, len(segs)*2)
	for i, s := range segs {
		if i > 0 {
			parts = append(parts, pretty.SoftLine)
		}
		parts = append(parts, p.segment(s))
	}
	return pretty.Concat(parts...), breakable
}

// segment renders one glued run on a single (flat) line.
func (p *printer) segment(s segment) pretty.Doc {
	parts := make([]pretty.Doc, 0, len(s.nodes))
	for _, n := range s.nodes {
		parts = append(parts, p.markup(n))
	}
	return pretty.Concat(parts...)
}

// element renders <tag attrs>children</tag>.
func (p *printer) element(e *ast.Element) pretty.Doc {
	open := []pretty.Doc{pretty.Text("<"), pretty.Text(e.Tag)}
	for _, a := range e.Attrs {
		open = append(open, pretty.Text(" "), pretty.Text(attrInline(a)))
	}
	openTag := pretty.Concat(open...)

	if e.Void && len(e.Children) == 0 {
		return pretty.Concat(openTag, pretty.Text("/>"))
	}
	close := pretty.Concat(pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))

	if strings.EqualFold(e.Tag, "style") || strings.EqualFold(e.Tag, "script") {
		return pretty.Concat(openTag, pretty.Text(">"), p.rawHoleChildren(e.Children), close)
	}
	if isPreserveTag(e.Tag) {
		return pretty.Concat(openTag, pretty.Text(">"), p.childrenPreserve(e.Children), close)
	}

	inner, breakable := p.childrenInner(e.Children)
	if !breakable {
		return pretty.Concat(openTag, pretty.Text(">"), inner, close)
	}
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.SoftLine, inner)), pretty.SoftLine)
	return pretty.Group(pretty.Concat(openTag, pretty.Text(">"), body, close))
}

// childrenPreserve emits pre/textarea bodies verbatim (no added indentation).
func (p *printer) childrenPreserve(nodes []ast.Markup) pretty.Doc {
	parts := make([]pretty.Doc, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, p.markup(n))
	}
	return pretty.Concat(parts...)
}

// markup dispatches one node to its Doc builder.
func (p *printer) markup(n ast.Markup) pretty.Doc {
	switch v := n.(type) {
	case *ast.Element:
		return p.element(v)
	case *ast.Fragment:
		return p.fragment(v)
	case *ast.IfMarkup:
		return p.ifMarkup(v)
	case *ast.ForMarkup:
		return p.forMarkup(v)
	case *ast.SwitchMarkup:
		return p.switchMarkup(v)
	case *ast.GoBlock:
		return p.goBlock(v)
	case *ast.Doctype:
		return pretty.Text(v.Text)
	case *ast.HTMLComment:
		return pretty.Concat(pretty.Text("<!--"), pretty.Text(v.Text), pretty.Text("-->"))
	case *ast.Text:
		return pretty.Text(v.Value)
	case *ast.Interp:
		return p.interp(v)
	default:
		return p.fail("printer: unknown markup type %T", n)
	}
}

func (p *printer) fragment(f *ast.Fragment) pretty.Doc {
	inner, breakable := p.childrenInner(f.Children)
	if !breakable {
		return pretty.Concat(pretty.Text("<>"), inner, pretty.Text("</>"))
	}
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.SoftLine, inner)), pretty.SoftLine)
	return pretty.Group(pretty.Concat(pretty.Text("<>"), body, pretty.Text("</>")))
}

func (p *printer) interp(i *ast.Interp) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("{ "), pretty.Text(fmtExpr(i.Expr))}
	for _, s := range i.Stages {
		parts = append(parts, pretty.Text(" |> "), pretty.Text(pipeStageStr(s)))
	}
	parts = append(parts, pretty.Text(" }"))
	return pretty.Concat(parts...)
}

func pipeStageStr(s ast.PipeStage) string {
	if s.HasArgs {
		return s.Name + "(" + fmtArgs(s.Args) + ")"
	}
	return s.Name
}

func (p *printer) goBlock(b *ast.GoBlock) pretty.Doc {
	return pretty.Concat(pretty.Text("{{ "), multiline(fmtStmts(b.Code)), pretty.Text(" }}"))
}

// ifMarkup renders `{ if cond { … }[ else …] }` as a group: short → one line,
// long → block body.
func (p *printer) ifMarkup(i *ast.IfMarkup) pretty.Doc {
	return pretty.Group(pretty.Concat(pretty.Text("{ "), p.ifChain(i), pretty.Text(" }")))
}

func (p *printer) ifChain(i *ast.IfMarkup) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("if "), pretty.Text(fmtExpr(i.Cond)), pretty.Text(" {"), p.cfBody(i.Then), pretty.Text("}")}
	if len(i.Else) == 0 {
		return pretty.Concat(parts...)
	}
	if len(i.Else) == 1 {
		if elseIf, ok := i.Else[0].(*ast.IfMarkup); ok {
			parts = append(parts, pretty.Text(" else "), p.ifChain(elseIf))
			return pretty.Concat(parts...)
		}
	}
	parts = append(parts, pretty.Text(" else {"), p.cfBody(i.Else), pretty.Text("}"))
	return pretty.Concat(parts...)
}

func (p *printer) forMarkup(f *ast.ForMarkup) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.Text("{ for "), pretty.Text(fmtClause(f.Clause)), pretty.Text(" {"), p.cfBody(f.Body), pretty.Text("} }")))
}

// cfBody renders a control-flow body between an already-emitted `{` and a
// caller-emitted `}`. Inline → ` content ` (single-space padded, via Line so a
// flat body reads `{ … }` and never `{{`/`}}`). Block → newline-indented.
func (p *printer) cfBody(nodes []ast.Markup) pretty.Doc {
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	inner, breakable := p.childrenInner(nodes)
	if !breakable {
		return pretty.Concat(pretty.Text(" "), inner, pretty.Text(" "))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(pretty.Line, inner)), pretty.Line)
}

// switchMarkup always breaks (cases on their own lines) via HardLine.
func (p *printer) switchMarkup(s *ast.SwitchMarkup) pretty.Doc {
	head := []pretty.Doc{pretty.Text("{ switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), pretty.Text(fmtExpr(s.Tag)))
	}
	head = append(head, pretty.Text(" {"))

	caseParts := make([]pretty.Doc, 0, len(s.Cases))
	for _, c := range s.Cases {
		label := pretty.Text("default:")
		if !c.Default {
			label = pretty.Concat(pretty.Text("case "), pretty.Text(fmtCaseList(c.List)), pretty.Text(":"))
		}
		caseParts = append(caseParts, pretty.HardLine, pretty.Concat(label, p.caseBody(c.Body)))
	}
	return pretty.Concat(
		pretty.Concat(head...),
		pretty.Indent(pretty.Concat(caseParts...)),
		pretty.HardLine, pretty.Text("} }"))
}

// caseBody renders a switch arm. Block → each segment on its own line (one
// deeper than the `case`); inline → follows the colon.
func (p *printer) caseBody(nodes []ast.Markup) pretty.Doc {
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	inner, breakable := p.childrenInner(nodes)
	if !breakable {
		return inner
	}
	return pretty.Indent(pretty.Concat(pretty.HardLine, inner))
}

// multiline turns a possibly multi-line Go fragment into a Doc: lines are
// joined with HardLine so the engine re-indents continuation lines to the
// current level (and any multi-line fragment forces its enclosing group to
// break). A single-line fragment is a plain Text.
func multiline(s string) pretty.Doc {
	if !strings.Contains(s, "\n") {
		return pretty.Text(s)
	}
	lines := strings.Split(s, "\n")
	parts := make([]pretty.Doc, 0, len(lines)*2)
	for i, ln := range lines {
		if i > 0 {
			parts = append(parts, pretty.HardLine)
		}
		parts = append(parts, pretty.Text(ln))
	}
	return pretty.Concat(parts...)
}

// --- attributes (inline for now; real wrapping is a later task) -------------

// attrInline renders an attribute to its single-line gsx text, exactly as the
// pre-IR printer did. (Multi-line attribute layout is added later.)
func attrInline(a ast.Attr) string {
	var b strings.Builder
	writeAttrInline(&b, a)
	return b.String()
}

func writeAttrInline(b *strings.Builder, a ast.Attr) {
	switch v := a.(type) {
	case *ast.StaticAttr:
		b.WriteString(v.Name)
		b.WriteString(`="`)
		b.WriteString(v.Value)
		b.WriteString(`"`)
	case *ast.BoolAttr:
		b.WriteString(v.Name)
	case *ast.ExprAttr:
		b.WriteString(v.Name)
		b.WriteString("={")
		b.WriteString(fmtExpr(v.Expr))
		for _, s := range v.Stages {
			b.WriteString(" |> ")
			b.WriteString(pipeStageStr(s))
		}
		b.WriteString("}")
	case *ast.SpreadAttr:
		b.WriteString("{ ")
		if len(v.Stages) > 0 {
			b.WriteString("(")
			b.WriteString(fmtExpr(v.Expr))
			for _, s := range v.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			b.WriteString(")... }")
		} else {
			b.WriteString(fmtExpr(v.Expr))
			b.WriteString("... }")
		}
	case *ast.ClassAttr:
		b.WriteString(v.Name)
		b.WriteString("={ ")
		for i, part := range v.Parts {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmtExpr(part.Expr))
			for _, s := range part.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			if part.Cond != "" {
				b.WriteString(": ")
				b.WriteString(fmtExpr(part.Cond))
			}
		}
		b.WriteString(" }")
	case *ast.CondAttr:
		b.WriteString("{ ")
		writeCondAttrChain(b, v)
		b.WriteString(" }")
	case *ast.MarkupAttr:
		b.WriteString(v.Name)
		b.WriteString("={ ")
		for _, n := range v.Value {
			b.WriteString(markupInlineString(n))
		}
		b.WriteString(" }")
	case *ast.JSAttr:
		b.WriteString(v.Name)
		b.WriteString(`="`)
		writeRawHoleString(b, v.Segments)
		b.WriteString(`"`)
	}
}

func writeCondAttrChain(b *strings.Builder, c *ast.CondAttr) {
	b.WriteString("if ")
	b.WriteString(fmtExpr(c.Cond))
	b.WriteString(" {")
	writeCondAttrList(b, c.Then)
	b.WriteString("}")
	if len(c.Else) == 0 {
		return
	}
	if len(c.Else) == 1 {
		if elseIf, ok := c.Else[0].(*ast.CondAttr); ok {
			b.WriteString(" else ")
			writeCondAttrChain(b, elseIf)
			return
		}
	}
	b.WriteString(" else {")
	writeCondAttrList(b, c.Else)
	b.WriteString("}")
}

func writeCondAttrList(b *strings.Builder, attrs []ast.Attr) {
	for _, a := range attrs {
		b.WriteString(" ")
		writeAttrInline(b, a)
	}
	if len(attrs) > 0 {
		b.WriteString(" ")
	}
}

// markupInlineString renders a markup node to its flat gsx text (used inside
// attribute slots, which always lay out inline). It reuses the Doc builder and
// prints it flat at a very wide margin so no Line ever breaks.
func markupInlineString(n ast.Markup) string {
	var p printer
	return pretty.Print(p.markup(n), 1<<30)
}

// rawHoleChildren renders <style>/<script> children: Text verbatim, Interp with
// the @{ } delimiter. Pipeline Stages are preserved faithfully.
func (p *printer) rawHoleChildren(nodes []ast.Markup) pretty.Doc {
	var b strings.Builder
	writeRawHoleString(&b, nodes)
	return pretty.Text(b.String())
}

func writeRawHoleString(b *strings.Builder, nodes []ast.Markup) {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			b.WriteString(v.Value)
		case *ast.Interp:
			b.WriteString("@{ ")
			b.WriteString(fmtExpr(v.Expr))
			for _, s := range v.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			b.WriteString(" }")
		default:
			b.WriteString(markupInlineString(n))
		}
	}
}

// isPreserveTag mirrors wsnorm: pre/textarea/script/style keep bodies verbatim.
func isPreserveTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "pre", "textarea", "script", "style":
		return true
	}
	return false
}
```

> The Go-fragment helpers below (`fmtGoChunk`, `fmtExpr`, `fmtArgs`, `fmtStmts`, `fmtParams`, `fmtRecv`, `fmtClause`, …) stay UNCHANGED in this task. Note the imports block above keeps `bytes`, `goast`, `format`, `goparser`, `gotoken` because those helpers use them. If `go vet` flags an unused import after the rewrite, it means a helper moved — re-check, do not blindly delete.

- [ ] **Step 4: Update `internal/gsxfmt/gsxfmt.go` to thread width**

Change both functions to accept `width int` and pass it to `Fprint`:

```go
func Format(name string, src []byte, width int) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := printer.Fprint(&b, f, width); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
```

Apply the same `width int` addition + `printer.Fprint(&b, f, width)` to `FormatRemovingImports`.

- [ ] **Step 5: Update the callers to pass 80 (Task 7 makes them config-driven)**

- `gen/fmt.go:85`: `formatted, err := gsxfmt.FormatRemovingImports(path, orig, unusedByPath[abs], 80)`
- `gen/fmt.go:134` (`formatGsx`): `return gsxfmt.Format(name, src, 80)`
- `internal/lsp/format.go:37`: `formatted, err := gsxfmt.FormatRemovingImports(path, []byte(text), unused, 80)`

- [ ] **Step 6: Run the new tests**

Run: `go test ./internal/printer/ -run 'TestBlockBreaksMixed|TestShortBlockCollapses'`
Expected: PASS. If the middot test fails on indentation, inspect the diff — the most likely cause is a missing `SoftLine` between segments or an `Indent` nesting error in `element`/`cfBody`.

- [ ] **Step 7: Fix and re-baseline the rest of the printer + formatter suites**

Run: `go test ./internal/printer/ ./internal/gsxfmt/ ./internal/wsnorm/ ./gen/ 2>&1 | tail -40`

- The **property** corpus tests (`TestCorpusIdempotence`, `TestCorpusFaithfulness`) MUST stay green. If either fails, the layout is unfaithful or non-idempotent — fix the printer, do NOT weaken the test.
- Several **exact-output** unit tests in `printer_test.go` (e.g. `TestElementBlock`, `TestNestedBlockInline`, `TestIfElseIfElse`, `TestForMarkup`, `TestFragment`) will now produce collapsed-when-fits or width-reflowed output. For EACH failing exact-output test: read the new output from the failure diff, confirm it is faithful (same normalized AST) and reads correctly, then update the test's `want` string to the new canonical output. Do not change inputs.
- Verify idempotence on each updated expectation by eye: the `want` you set, fed back through the formatter, must be unchanged (the property test enforces this across the corpus, but keep the unit expectations canonical).

- [ ] **Step 8: Add the reported-bug fixture to the corpus**

The corpus lives under `internal/corpus/testdata/cases/` (per-context subdirs). Add a fixture exercising the bug so it is covered by the property tests. Create `internal/corpus/testdata/cases/control_flow/mixed_text_if_block.gsx`:

```gsx
package p

component C() {
	<p class="text-sm text-slate-500">
		by {props.Author.Username}
		{ if props.Category.Slug != "" {
			· <a class="hover:underline" href={ categoryPage{} |> url("slug", props.Category.Slug) }>{props.Category.Name}</a>
		} }
	</p>
}
```

> Confirm the corpus loader's directory/extension convention by reading `corpus_test.go` / `internal/corpus` first; match the existing fixture naming. If fixtures are txtar rather than bare `.gsx`, add a txtar case in the same style instead.

Run: `go test ./internal/printer/ -run TestCorpus`
Expected: PASS (idempotent + faithful on the new fixture).

- [ ] **Step 9: Full build + test sweep**

Run: `go build ./... && go test ./... 2>&1 | tail -30`
Expected: build clean; all packages PASS. Fix any caller of the changed signatures the grep in this plan did not anticipate (search: `grep -rn 'gsxfmt.Format\|printer.Fprint' --include='*.go' .`).

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "feat(printer): rebuild on Doc IR; width-aware children/control-flow

Fixes the over-collapsing bug: mixed text+interp+control-flow lists break at
whitespace-safe boundaries (segmentChildren) instead of forcing inline.
Relative pretty.Indent replaces manual depth threading. Fprint/gsxfmt take a
width param (callers pass 80; config wiring follows). Attributes still render
inline. Property corpus (idempotence + faithfulness) stays green.

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 5: Attribute-list wrapping

Make the opening tag a `Group`: flat → `<tag a b c>`; broken → one attr per indented line with `>` on its own line. Force the break when any `*ast.CondAttr` is present, and force the children to break when the opening tag breaks.

**Files:**
- Modify: `internal/printer/printer.go` (`element`; add `attrDoc`)
- Test: `internal/printer/printer_test.go`
- Test: corpus fixture for conditional-attribute wrapping

**Interfaces:**
- Consumes: Task 4 printer; `pretty.BreakParent`.
- Produces: `func (p *printer) attrDoc(a ast.Attr) pretty.Doc` (single-attr Doc; still single-line value — multi-line values are Task 6).

- [ ] **Step 1: Write the failing test**

```go
func TestAttrWrapOnConditionalAttr(t *testing.T) {
	// A CondAttr forces the opening tag to break, one attr per line, > alone;
	// the broken tag forces the (single spread) child onto its own line.
	src := `package p
component C(p Props) {
	<a if p.ID != "" { id={ p.ID } } href={ p.Href } class={ buttonClass(p) } { p.Attributes... }>{ children... }</a>
}`
	want := `package p

component C(p Props) {
	<a
		if p.ID != "" {
			id={ p.ID }
		}
		href={ p.Href }
		class={ buttonClass(p) }
		{ p.Attributes... }
	>
		{ children... }
	</a>
}
`
	assertFormat(t, src, want)
}

func TestAttrStayInlineWhenShort(t *testing.T) {
	src := `package p
component C() {
	<a href="/x" class="b">go</a>
}`
	want := `package p

component C() {
	<a href="/x" class="b">go</a>
}
`
	assertFormat(t, src, want)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer/ -run 'TestAttrWrap|TestAttrStayInline'`
Expected: FAIL — attributes currently render inline only.

- [ ] **Step 3: Implement `attrDoc` and rewrite `element`'s opening tag**

Add `attrDoc` (mirrors `attrInline` but returns a Doc; conditional attributes use HardLine-broken interior just like switch, since templ always expands them):

```go
// attrDoc renders one attribute as a Doc. Value expressions remain single-line
// in this task (multi-line values are added later). A conditional attribute is
// rendered with its `{ if … { … } }` body broken across lines (templ-style),
// emitting a BreakParent so the enclosing opening-tag group breaks.
func (p *printer) attrDoc(a ast.Attr) pretty.Doc {
	if c, ok := a.(*ast.CondAttr); ok {
		return pretty.Concat(pretty.BreakParent, pretty.Text("{ "), p.condAttrChainDoc(c), pretty.Text(" }"))
	}
	return pretty.Text(attrInline(a))
}

func (p *printer) condAttrChainDoc(c *ast.CondAttr) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("if "), pretty.Text(fmtExpr(c.Cond)), pretty.Text(" {"),
		p.condAttrListDoc(c.Then), pretty.Text("}")}
	if len(c.Else) == 0 {
		return pretty.Concat(parts...)
	}
	if len(c.Else) == 1 {
		if elseIf, ok := c.Else[0].(*ast.CondAttr); ok {
			parts = append(parts, pretty.Text(" else "), p.condAttrChainDoc(elseIf))
			return pretty.Concat(parts...)
		}
	}
	parts = append(parts, pretty.Text(" else {"), p.condAttrListDoc(c.Else), pretty.Text("}"))
	return pretty.Concat(parts...)
}

// condAttrListDoc lays a conditional attribute's inner attrs one per line.
func (p *printer) condAttrListDoc(attrs []ast.Attr) pretty.Doc {
	inner := make([]pretty.Doc, 0, len(attrs)*2)
	for _, a := range attrs {
		inner = append(inner, pretty.HardLine, p.attrDoc(a))
	}
	return pretty.Indent(pretty.Concat(inner...))
}
```

Rewrite `element` to make the opening tag a group and couple tag-break to children-break. Replace the `element` function body from Task 4 with:

```go
func (p *printer) element(e *ast.Element) pretty.Doc {
	attrs := make([]pretty.Doc, 0, len(e.Attrs)*2)
	for _, a := range e.Attrs {
		attrs = append(attrs, pretty.Line, p.attrDoc(a))
	}
	// Opening tag group: flat → `<tag a b>`; broken → each attr on its own line
	// with `>` (or `/>`) alone. A forced break inside any attr (CondAttr) breaks
	// the group; otherwise it breaks only on width overflow.
	tagBroken := elementHasCondAttr(e)

	selfClose := e.Void && len(e.Children) == 0
	tail := pretty.Text(">")
	if selfClose {
		tail = pretty.Text("/>")
	}
	var openGroupBody pretty.Doc
	if len(e.Attrs) == 0 {
		openGroupBody = pretty.Concat(pretty.Text("<"), pretty.Text(e.Tag), tail)
	} else {
		openGroupBody = pretty.Concat(
			pretty.Text("<"), pretty.Text(e.Tag),
			pretty.Indent(pretty.Concat(attrs...)),
			pretty.Line, tail)
	}
	openTag := pretty.Group(openGroupBody)

	if selfClose {
		return openTag
	}
	close := pretty.Concat(pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))

	if strings.EqualFold(e.Tag, "style") || strings.EqualFold(e.Tag, "script") {
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
	if isPreserveTag(e.Tag) {
		return pretty.Concat(openTag, p.childrenPreserve(e.Children), close)
	}

	inner, breakable := p.childrenInner(e.Children)
	if len(e.Children) == 0 {
		return pretty.Concat(openTag, close)
	}
	// Couple tag-break to children-break: when the opening tag breaks, children
	// break too (avoids `>{ children… }</div>`). Encode by adding BreakParent to
	// the children group when the tag is forced broken; for width-driven tag
	// breaks the children group still decides independently (its own width).
	childBody := pretty.Concat(pretty.Indent(pretty.Concat(pretty.SoftLine, inner)), pretty.SoftLine)
	if !breakable {
		// A non-breakable children list cannot host added breaks; keep inline.
		if tagBroken {
			// Tag broke but children can't safely break: place children inline
			// directly after `>` on the same (final) tag line.
			return pretty.Concat(openTag, inner, close)
		}
		return pretty.Concat(openTag, inner, close)
	}
	forceChildren := pretty.Text("")
	if tagBroken {
		forceChildren = pretty.BreakParent
	}
	return pretty.Concat(openTag, pretty.Group(pretty.Concat(forceChildren, childBody, close)))
}

// elementHasCondAttr reports whether any attribute is conditional (forces the
// opening tag to wrap, templ-style).
func elementHasCondAttr(e *ast.Element) bool {
	for _, a := range e.Attrs {
		if _, ok := a.(*ast.CondAttr); ok {
			return true
		}
	}
	return false
}
```

> Note: the children group now includes the closing tag so `</a>` lands on its own line when children break (matching the expected output). Re-check `TestBlockBreaksMixedTextControlFlow` and the corpus still pass after this restructuring.

- [ ] **Step 4: Run the new tests + regression**

Run: `go test ./internal/printer/ -run 'TestAttr|TestBlockBreaksMixed|TestShortBlockCollapses|TestCorpus'`
Expected: PASS. Then fix any other exact-output attribute tests (`TestAttrKinds`, `TestCondAttr`, `TestMarkupAttr`, `TestJSAttr`, `TestVoidElement`) whose canonical output changed; update their `want` to the new faithful output.

- [ ] **Step 5: Add a conditional-attr corpus fixture**

Create `internal/corpus/testdata/cases/elements/cond_attr_wrap.gsx` (match the real corpus convention):

```gsx
package p

component Button(p Props) {
	<a
		if p.ID != "" {
			id={ p.ID }
		}
		href={ p.Href }
		class={ buttonClass(p) }
		{ p.Attributes... }
	>
		{ children... }
	</a>
}
```

Run: `go test ./internal/printer/ -run TestCorpus`
Expected: PASS (idempotent + faithful).

- [ ] **Step 6: Full sweep + commit**

```bash
go build ./... && go test ./... 2>&1 | tail -20
git add -A
git commit -m "feat(printer): attribute-list wrapping (CondAttr + width); broken tag breaks children

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 6: Multi-line attribute values + Go-comment fidelity

Allow a long `class={ … }` (or any expr attribute value / class part) to wrap as gofmt'd Go, indented under the attribute, and preserve comments inside the expression. The fix replaces `go/format.Node` (which drops comments attached to the FileSet) with a `format.Source`-then-extract path for expression values.

**Files:**
- Modify: `internal/printer/printer.go` (`fmtExpr` → comment-preserving; `attrDoc` value rendering for `ExprAttr`/`ClassAttr`/`MarkupAttr`)
- Test: `internal/printer/printer_test.go`
- Test: corpus fixture (`TwMerge` with an interior comment)

**Interfaces:**
- Consumes: Task 5 printer.
- Produces:
  - `func fmtExprDoc(src string) pretty.Doc` — a Doc for a Go expression that may be multi-line (HardLine-joined) and preserves comments.
  - Updated `attrDoc` building `name={` value `}` with the value possibly multi-line.

- [ ] **Step 1: Write the failing test**

```go
func TestAttrValueMultilinePreservesComment(t *testing.T) {
	src := `package p
component C(p Props) {
	<p class={ utils.TwMerge(
		// keep this comment
		"text-[0.8rem] font-medium",
		p.Class,
	) }>x</p>
}`
	// The comment must survive, and the long call must stay multi-line.
	got := format80(t, src) // helper: parse→wsnorm→Fprint(…,80)→string
	if !strings.Contains(got, "// keep this comment") {
		t.Fatalf("comment dropped:\n%s", got)
	}
	if !strings.Contains(got, "utils.TwMerge(\n") {
		t.Fatalf("expr not multi-line:\n%s", got)
	}
	// Idempotence: re-formatting is a fixed point.
	if again := format80(t, got); again != got {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", got, again)
	}
}
```

Add helper if absent:

```go
func format80(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "c.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := Fprint(&b, f, 80); err != nil {
		t.Fatalf("print: %v", err)
	}
	return b.String()
}
```

(Needs `strings` import in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/printer/ -run TestAttrValueMultiline`
Expected: FAIL — comment dropped and/or value collapsed to one line.

- [ ] **Step 3: Add a comment-preserving expression formatter**

Add to `printer.go` (near the Go-fragment helpers). It wraps the expression in a full file (so `format.Source` keeps comments via the FileSet), formats, and extracts the RHS of `var _ = <expr>`, de-indenting one level:

```go
// fmtExprPreserving formats a Go expression, PRESERVING comments (unlike
// fmtExpr's format.Node path). It wraps the expression as a package-level
// `var _ = <expr>` and runs format.Source, then extracts the value text. The
// result may be multi-line (gofmt's own wrapping of a long call); continuation
// lines are de-indented by one tab (the var-decl level). On any error it falls
// back to fmtExpr (single line, comment-free) so fmt never fails.
func fmtExprPreserving(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	wrapped := "package p\nvar _ = " + trimmed + "\n"
	out, err := format.Source([]byte(wrapped))
	if err != nil {
		return fmtExpr(src)
	}
	s := string(out)
	const marker = "var _ = "
	i := strings.Index(s, marker)
	if i < 0 {
		return fmtExpr(src)
	}
	body := s[i+len(marker):]
	body = strings.TrimRight(body, "\n")
	// De-indent continuation lines by one tab (gofmt indents the value's
	// wrapped lines to the declaration's body level).
	lines := strings.Split(body, "\n")
	for j := 1; j < len(lines); j++ {
		lines[j] = strings.TrimPrefix(lines[j], "\t")
	}
	return strings.Join(lines, "\n")
}

// fmtExprDoc returns a Doc for a Go expression value, multi-line when gofmt
// wraps it (HardLine-joined; comments preserved).
func fmtExprDoc(src string) pretty.Doc {
	return multiline(fmtExprPreserving(src))
}
```

- [ ] **Step 4: Use `fmtExprDoc` for attribute values**

In `attrDoc` (and its helpers), render `ExprAttr`, `ClassAttr` part expressions, and `MarkupAttr` value-expressions through `fmtExprDoc` so they can be multi-line. Replace the inline-string rendering for these attribute kinds with Doc construction. Add to `attrDoc`:

```go
func (p *printer) attrDoc(a ast.Attr) pretty.Doc {
	switch v := a.(type) {
	case *ast.CondAttr:
		return pretty.Concat(pretty.BreakParent, pretty.Text("{ "), p.condAttrChainDoc(v), pretty.Text(" }"))
	case *ast.ExprAttr:
		val := []pretty.Doc{fmtExprDoc(v.Expr)}
		for _, s := range v.Stages {
			val = append(val, pretty.Text(" |> "), pretty.Text(pipeStageStr(s)))
		}
		return wrapAttrValue(v.Name, pretty.Concat(val...))
	case *ast.ClassAttr:
		parts := make([]pretty.Doc, 0, len(v.Parts)*2)
		for i, part := range v.Parts {
			if i > 0 {
				parts = append(parts, pretty.Text(", "))
			}
			seg := []pretty.Doc{fmtExprDoc(part.Expr)}
			for _, s := range part.Stages {
				seg = append(seg, pretty.Text(" |> "), pretty.Text(pipeStageStr(s)))
			}
			if part.Cond != "" {
				seg = append(seg, pretty.Text(": "), pretty.Text(fmtExpr(part.Cond)))
			}
			parts = append(parts, pretty.Concat(seg...))
		}
		return wrapAttrValue(v.Name, pretty.Concat(parts...))
	default:
		return pretty.Text(attrInline(a))
	}
}

// wrapAttrValue renders `name={ value }` when the value is single-line, or
// `name={` / indented value / `}` when the value is multi-line. The multi-line
// shape is selected structurally: a value containing a forced break makes the
// group break.
func wrapAttrValue(name string, value pretty.Doc) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.Text(name), pretty.Text("={"),
		pretty.Indent(pretty.Concat(pretty.Line, value)),
		pretty.Line, pretty.Text("}")))
}
```

> `wrapAttrValue` produces `name={ value }` when flat (Line → space) and the multi-line form when broken. A value carrying a HardLine (from `multiline`) forces the group to break. Confirm the single-line spacing matches the existing canonical (`class={ X }`): flat renders `name={` + ` ` + value + ` ` + `}` = `name={ X }`. Good.

- [ ] **Step 5: Run new test + regression**

Run: `go test ./internal/printer/ -run 'TestAttrValueMultiline|TestAttr|TestCorpus'`
Expected: PASS. Update any exact-output expectations for `class={…}`/`name={…}` attributes whose canonical spacing is confirmed identical (should be unchanged for single-line values). The faithfulness property test guards correctness.

- [ ] **Step 6: Add the comment-in-value corpus fixture**

Create `internal/corpus/testdata/cases/attributes/class_multiline_comment.gsx`:

```gsx
package p

component C(p Props) {
	<p class={
		utils.TwMerge(
			// keep this comment
			"text-[0.8rem] font-medium",
			p.Class,
		),
	}>x</p>
}
```

Run: `go test ./internal/printer/ -run TestCorpus`
Expected: PASS (faithful + idempotent; comment retained).

- [ ] **Step 7: Full sweep + commit**

```bash
go build ./... && go test ./... 2>&1 | tail -20
git add -A
git commit -m "feat(printer): multi-line attribute values + Go-comment fidelity

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 7: printWidth config — gsx.toml + CLI + LSP

Discover `printWidth` from `gsx.toml` and thread it through the CLI (`runFmt`) and LSP (`Analyzer.PrintWidth`). Default 80.

**Files:**
- Modify: `gen/configfile.go` (`tomlConfig.PrintWidth`)
- Modify: the file defining the internal `config` struct (`gen/options.go` — confirm with `grep -n 'type config struct' gen/*.go`): add `printWidth int`; default + merge.
- Modify: `gen/fmt.go` (`runFmt`: resolve width per file dir; pass down)
- Modify: `gen/lsp.go` (`lspAnalyzer.PrintWidth`)
- Modify: `internal/lsp/server.go` (`Analyzer` interface: add `PrintWidth(dir string) int`)
- Modify: `internal/lsp/format.go` (`handleFormatting`: resolve + pass width)
- Test: `gen/configfile_test.go` (printWidth decoding + default)
- Test: `internal/lsp` fake analyzer (add `PrintWidth`)

**Interfaces:**
- Consumes: Task 4 formatter signatures.
- Produces:
  - `tomlConfig.PrintWidth int` (`toml:"printWidth"`)
  - `config.printWidth int`; `func (c config) effectivePrintWidth() int` (≤0 ⇒ 80)
  - `lsp.Analyzer.PrintWidth(dir string) int`

- [ ] **Step 1: Write the failing config test**

Add to `gen/configfile_test.go`:

```go
func TestConfigPrintWidth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("printWidth = 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.effectivePrintWidth(); got != 100 {
		t.Fatalf("printWidth = %d, want 100", got)
	}
}

func TestConfigPrintWidthDefault(t *testing.T) {
	var c config
	if got := c.effectivePrintWidth(); got != 80 {
		t.Fatalf("default printWidth = %d, want 80", got)
	}
}
```

(Ensure `os`, `path/filepath` are imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestConfigPrintWidth`
Expected: FAIL — `printWidth`/`effectivePrintWidth`/`PrintWidth` unknown.

- [ ] **Step 3: Add the TOML key + config field + default**

In `gen/configfile.go`, add to `tomlConfig`:

```go
	PrintWidth int `toml:"printWidth"`
```

In `loadConfig`, after building `cfg`, copy it through:

```go
	cfg.printWidth = tc.PrintWidth
```

In the `config` struct (its defining file), add:

```go
	printWidth int // gsx.toml printWidth; 0 means "unset" → 80 at use
```

Add the accessor (same file as `config`):

```go
// effectivePrintWidth returns the configured print width, defaulting to 80 when
// unset (zero or negative).
func (c config) effectivePrintWidth() int {
	if c.printWidth <= 0 {
		return 80
	}
	return c.printWidth
}
```

In `mergeConfig` (gen/configfile.go), carry printWidth with opts-wins semantics:

```go
	merged.printWidth = base.printWidth
	if opts.printWidth > 0 {
		merged.printWidth = opts.printWidth
	}
```

- [ ] **Step 4: Run config test**

Run: `go test ./gen/ -run TestConfigPrintWidth`
Expected: PASS.

- [ ] **Step 5: Thread width into the CLI (`runFmt`)**

In `gen/fmt.go`, resolve width per file directory (cache per dir) and pass it to the formatter. Add a small resolver and use it:

```go
// printWidthFor returns the effective gsx.toml printWidth for dir (default 80),
// best-effort: discovery/decoding failures fall back to 80.
func printWidthFor(dir string) int {
	path, ok := discoverConfig(dir)
	if !ok {
		return 80
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return 80
	}
	return cfg.effectivePrintWidth()
}
```

In `runFmt`, replace the `gsxfmt.FormatRemovingImports(path, orig, unusedByPath[abs], 80)` from Task 4 with a per-dir width (cache to avoid re-reading the same gsx.toml):

```go
	widthByDir := map[string]int{}
	// … inside the per-file loop, before formatting:
	dir := filepath.Dir(path)
	width, ok := widthByDir[dir]
	if !ok {
		width = printWidthFor(dir)
		widthByDir[dir] = width
	}
	formatted, err := gsxfmt.FormatRemovingImports(path, orig, unusedByPath[abs], width)
```

Update `formatGsx` to use the CWD's width:

```go
func formatGsx(name string, src []byte) ([]byte, error) {
	return gsxfmt.Format(name, src, printWidthFor("."))
}
```

- [ ] **Step 6: Thread width into the LSP**

In `internal/lsp/server.go`, add to the `Analyzer` interface:

```go
	// PrintWidth returns the gsx.toml print width for the given directory
	// (default 80). Used by textDocument/formatting.
	PrintWidth(dir string) int
```

In `internal/lsp/format.go` `handleFormatting`, resolve the document's directory and pass the width (the handler already computes `path`; derive `dir := filepath.Dir(path)`):

```go
	width := s.analyzer.PrintWidth(filepath.Dir(path))
	formatted, err := gsxfmt.FormatRemovingImports(path, []byte(text), unused, width)
```

(Add `path/filepath` to the imports if not present.)

In `gen/lsp.go`, implement the method on `lspAnalyzer`:

```go
// PrintWidth resolves the effective gsx.toml print width for dir, layering the
// programmatic optCfg over the file config exactly like Analyze. Best-effort:
// returns 80 on any failure.
func (a lspAnalyzer) PrintWidth(dir string) int {
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	return merged.effectivePrintWidth()
}
```

Update any test/fake `Analyzer` implementations in `internal/lsp` to satisfy the new method. Find them:

```bash
grep -rn 'Analyze(' internal/lsp/*_test.go
```

For each fake analyzer type, add:

```go
func (f *fakeAnalyzer) PrintWidth(string) int { return 80 }
```

(Use the actual fake type name reported by the grep.)

- [ ] **Step 7: Build + full test sweep**

Run: `go build ./... && go test ./... 2>&1 | tail -30`
Expected: build clean; all PASS. The LSP formatting tests should still pass (default 80 unchanged behavior).

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(config): printWidth in gsx.toml, threaded through gsx fmt + LSP

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Task 8: Design-system acceptance + final property sweep

Validate against real-world patterns from `ds/` and lock in idempotence/faithfulness across new fixtures. No production code changes expected — this is a coverage + verification task. If a fixture reveals a layout bug, fix the relevant earlier-task code and note it.

**Files:**
- Test: new corpus fixtures under `internal/corpus/testdata/cases/`
- Test: `internal/printer/printer_test.go` (a couple of width-sensitivity assertions)

- [ ] **Step 1: Add width-sensitivity test**

```go
func TestPrintWidthControlsWrap(t *testing.T) {
	src := `package p
component C() {
	<ul><li>one</li><li>two</li><li>three</li></ul>
}`
	// Fits at 80 → one line.
	flat := format80(t, src)
	if !strings.Contains(flat, "<ul><li>one</li><li>two</li><li>three</li></ul>") {
		t.Fatalf("width 80 should stay flat:\n%s", flat)
	}
	// At width 20 → breaks each <li> onto its own line.
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "c.gsx", []byte(src), 0)
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := Fprint(&b, f, 20); err != nil {
		t.Fatal(err)
	}
	narrow := b.String()
	if !strings.Contains(narrow, "<ul>\n\t\t<li>one</li>\n") {
		t.Fatalf("width 20 should break list:\n%s", narrow)
	}
}
```

Run: `go test ./internal/printer/ -run TestPrintWidthControlsWrap`
Expected: PASS (fix layout if not — likely a missing Group around the `<ul>` children, which `element` already provides).

- [ ] **Step 2: Add ds-derived fixtures**

Translate representative `ds/` patterns into gsx fixtures (the formatter's faithfulness/idempotence properties are the assertions). Add under `internal/corpus/testdata/cases/elements/`:

`ds_button.gsx`:

```gsx
package button

component Button(p Props) {
	{ if p.Href != "" && !p.Disabled {
		<a
			if p.ID != "" {
				id={ p.ID }
			}
			href={ p.Href }
			class={ buttonClass(p) }
			{ p.Attributes... }
		>
			{ children... }
		</a>
	} else {
		<button
			if p.ID != "" {
				id={ p.ID }
			}
			class={ buttonClass(p) }
			disabled?={ p.Disabled }
			{ p.Attributes... }
		>
			{ children... }
		</button>
	} }
}
```

`ds_form_message.gsx`:

```gsx
package form

component Message(p MessageProps) {
	<p
		if p.ID != "" {
			id={ p.ID }
		}
		class={
			utils.TwMerge(
				"text-[0.8rem] font-medium",
				messageVariantClass(p.Variant),
				p.Class,
			),
		}
		{ p.Attributes... }
	>
		{ children... }
	</p>
}
```

> Adjust to the exact gsx surface syntax the parser accepts (e.g. `disabled?={ … }` boolean-expr attr, spread `{ p.Attributes... }`). If the parser rejects a construct, simplify the fixture to a parseable equivalent that still exercises the same layout (conditional attrs + multi-line class value + spread).

Run: `go test ./internal/printer/ -run TestCorpus`
Expected: PASS (idempotent + faithful on all fixtures).

- [ ] **Step 3: Manual acceptance on a real `ds/` component (optional but recommended)**

Build the CLI and format a copied-in component to eyeball readability:

```bash
go build -o /tmp/gsx ./cmd/gsx 2>/dev/null || go build -o /tmp/gsx .
# Translate one real component to .gsx by hand into /tmp/acc.gsx, then:
/tmp/gsx fmt /tmp/acc.gsx && /tmp/gsx fmt /tmp/acc.gsx | diff - <(/tmp/gsx fmt /tmp/acc.gsx) && echo "idempotent"
```

Confirm: long components stay expanded and readable; short wrappers collapse; the reported `<p>` middot case reads as in the spec.

- [ ] **Step 4: Final full sweep + commit**

```bash
go build ./... && go test ./... 2>&1 | tail -30
git add -A
git commit -m "test(printer): ds-derived acceptance fixtures + width-sensitivity

Claude-Session: https://claude.ai/code/session_01GFCX3bXsnaSw2HKGSBEhQk"
```

---

## Self-Review

**Spec coverage:**
- Reusable Doc IR (`internal/pretty`) with all primitives → Tasks 1–2. ✓
- Width-aware children/control-flow, all-or-nothing, safe-boundary breaks, edge guard → Tasks 3–4. ✓
- `if`/`for` groupable, `switch` hard-broken, multi-line `{{ }}` forces break → Task 4 (`ifMarkup`/`forMarkup`/`switchMarkup`/`goBlock`+`multiline`). ✓
- Attribute-list wrapping, CondAttr forces break, broken tag ⇒ broken children → Task 5. ✓
- Multi-line attribute values + Go-comment fidelity → Task 6. ✓
- `printWidth` in gsx.toml, threaded (CLI + LSP), default 80, no import cycle → Task 7. ✓
- Faithfulness + idempotence preserved (property corpus) → enforced every task; new fixtures Tasks 4–6, 8. ✓
- Preserve tags / raw holes unchanged → Task 4 (`childrenPreserve`, `rawHoleChildren`). ✓
- Tab width 4, rune-based measurement → Task 1 (`tabWidth`, `utf8.RuneCountInString`). ✓

**Notable behavior consequence (call out at handoff):** "true Prettier" width-aware collapse means a *short* block structure that fits 80 cols renders on one line (`<div><p>plain</p></div>`). This was the chosen option; existing exact-output unit tests that asserted expanded short layouts are updated in Task 4 Step 7.

**Placeholder scan:** No TBD/TODO; every code step shows complete code. Fixtures flagged with "match the corpus convention" point the implementer to read `internal/corpus`/`corpus_test.go` first — that is a deliberate verification step, not a placeholder.

**Type consistency:** `Fprint(w, f, width)`, `gsxfmt.Format(name, src, width)`, `FormatRemovingImports(name, src, unused, width)`, `pretty.Print(doc, width)`, `segmentChildren([]ast.Markup) ([]segment, bool)`, `effectivePrintWidth()`, `Analyzer.PrintWidth(dir) int` are used consistently across tasks. `attrInline`/`writeAttrInline` (Task 4) coexist with `attrDoc` (Tasks 5–6); `attrDoc`'s `default` branch falls back to `attrInline` for kinds not given a Doc form.
