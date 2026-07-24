# fmt Inline Atoms + Word-Gap Fill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The gsx formatter stops exploding inline elements (`<code>`, `<a>`, …), wraps long prose greedily at render-free word gaps, and treats `{" "}` as spacing glue — while staying provably render-preserving and idempotent.

**Architecture:** Spec: `docs/superpowers/specs/2026-07-24-fmt-inline-atoms-word-gap-fill-design.md` (read it first). Children lists flatten to leaves (words / atoms / interps) joined by bonds (render-lossy to break — never broken), word gaps (flat `" "`), and safe gaps (flat `""`); breakable joints become separators in a `pretty.Fill`, which already implements greedy packing (`internal/pretty/print.go` `fillStep`). Inline atoms render via a group-free flat doc so width can never break them open. Block lists keep one-segment-per-line structure; only segment interiors gain fill.

**Tech Stack:** Go 1.26.1 (pin per `GO_VERSION` in ci.yml), stdlib only in these packages. Work in a **git worktree** branch `fmt-inline-fill` off `main` (superpowers:using-git-worktrees).

## Global Constraints

- Render faithfulness: every layout change must render byte-identical HTML; the `internal/printer` corpus property tests and `checkFormat`'s idempotence check are the gate.
- No hand-editing goldens; regen with `-update`, then verify WITHOUT `-update` (`go test ./internal/gsxfmt -run TestFmtCorpus -update`).
- Semantic corpus (`internal/corpus`) goldens must NOT change (no codegen change) — if they do, stop and investigate.
- No "simple heuristics": the inline set is prettier's list (spec pins it); joints are derived from wsnorm physics, not guessed.
- Final gate before merge: `make ci` (uncached), plus `make lint`. Inner loop: `make check`.
- Commit after every green task; commit messages end with the Claude-Session trailer used on this branch's earlier commits.

---

### Task 1: `pretty.HasForcedBreak`

**Files:**
- Modify: `internal/pretty/doc.go` (after `containsForcedBreak`, ~line 78)
- Test: `internal/pretty/doc_test.go`

**Interfaces:**
- Produces: `func HasForcedBreak(d Doc) bool` — true iff d contains a HardLine or BreakParent at any depth. Task 3's atom check consumes it.

- [ ] **Step 1: Write the failing test** (append to `doc_test.go`):

```go
func TestHasForcedBreak(t *testing.T) {
	cases := []struct {
		name string
		doc  Doc
		want bool
	}{
		{"text", Text("x"), false},
		{"softline", Concat(Text("a"), SoftLine, Text("b")), false},
		{"line", Concat(Text("a"), Line, Text("b")), false},
		{"hardline", Concat(Text("a"), HardLine), true},
		{"breakparent", Concat(Text("a"), BreakParent), true},
		{"nested group", Group(Concat(Text("a"), Group(Concat(HardLine)))), true},
		{"fill with hard", Fill(Text("a"), SoftLine, Concat(HardLine)), true},
	}
	for _, c := range cases {
		if got := HasForcedBreak(c.doc); got != c.want {
			t.Errorf("%s: HasForcedBreak = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/pretty -run TestHasForcedBreak`
Expected: FAIL — `undefined: HasForcedBreak`

- [ ] **Step 3: Implement** (in `doc.go`, next to `containsForcedBreak`):

```go
// HasForcedBreak reports whether d contains a hard break (HardLine or
// BreakParent) at any depth — i.e. whether d can never render on one line.
func HasForcedBreak(d Doc) bool { return containsForcedBreak(d) }
```

Check `containsForcedBreak` recurses into ALL part-bearing kinds (including
kindFill); if kindFill is missing from its switch, add it — the test's last
case pins this.

- [ ] **Step 4: Run to verify it passes**: `go test ./internal/pretty` → ok

- [ ] **Step 5: Commit**: `git add internal/pretty && git commit -m "feat(pretty): export HasForcedBreak"`

---

### Task 2: Spacing-interp detection + glue

**Files:**
- Modify: `internal/printer/segment.go` (`glued`, new `isSpacingInterp`)
- Test: `internal/printer/segment_test.go`

**Interfaces:**
- Produces: `func isSpacingInterp(n ast.Markup) bool` — true iff n is `*ast.Interp` with no pipe stages whose Expr is a single Go string literal (interpreted or raw) whose value is 1+ ASCII spaces and nothing else. Tasks 4/5 consume it.
- Changes: `glued(left, right)` also returns true when `isSpacingInterp(right)` — `see{" "}` bonds left, so segmentChildren keeps them in one segment.

- [ ] **Step 1: Write the failing test** (append to `segment_test.go`; `ast.Interp` is constructible directly — Expr holds the literal source including quotes):

```go
func TestIsSpacingInterp(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{`" "`, true},
		{`"  "`, true},
		{"` `", true},
		{`""`, false},        // empty renders nothing, not a space
		{`" x "`, false},     // not only spaces
		{`"\t"`, false},      // tab is not the idiom
		{`name`, false},      // not a literal
		{`" " + " "`, false}, // not a single literal
	}
	for _, c := range cases {
		n := &ast.Interp{Expr: c.expr}
		if got := isSpacingInterp(n); got != c.want {
			t.Errorf("isSpacingInterp({%s}) = %v, want %v", c.expr, got, c.want)
		}
	}
	if isSpacingInterp(&ast.Interp{Expr: `" "`, Stages: []ast.PipeStage{{Name: "f"}}}) {
		t.Error("interp with pipe stages must not be spacing")
	}
	if isSpacingInterp(&ast.Text{Value: " "}) {
		t.Error("Text is not a spacing interp")
	}
}

func TestGluedSpacingInterp(t *testing.T) {
	see := &ast.Text{Value: "see"}
	sp := &ast.Interp{Expr: `" "`}
	if !glued(see, sp) {
		t.Error("spacing interp must glue to its left neighbor")
	}
	if glued(sp, see) {
		t.Error("spacing interp must not glue rightward without a space")
	}
}
```

- [ ] **Step 2: Run to verify it fails**: `go test ./internal/printer -run 'TestIsSpacingInterp|TestGluedSpacingInterp'` → FAIL `undefined: isSpacingInterp`

- [ ] **Step 3: Implement** in `segment.go`:

```go
// isSpacingInterp reports whether n is the {" "} spacing idiom: an
// interpolation of a single Go string literal whose value is only ASCII
// spaces. Such an interp is layout glue — it bonds to its left neighbor and
// offers a break after — because its rendered space, unlike a literal space
// in Text, survives any adjacent line break (wsnorm cannot collapse it).
func isSpacingInterp(n ast.Markup) bool {
	i, ok := n.(*ast.Interp)
	if !ok || len(i.Stages) > 0 {
		return false
	}
	s := strings.TrimSpace(i.Expr)
	if len(s) < 2 || (s[0] != '"' && s[0] != '`') {
		return false
	}
	v, err := strconv.Unquote(s)
	if err != nil || v == "" {
		return false
	}
	return strings.Trim(v, " ") == ""
}
```

(`strconv.Unquote` rejects `" " + " "` — the trailing garbage errors — and
handles both quote forms.) Change `glued`:

```go
// glued reports whether a significant space (or the {" "} idiom's left bond)
// binds left and right onto one line.
func glued(left, right ast.Markup) bool {
	return trailsWithSpace(left) || leadsWithSpace(right) || isSpacingInterp(right)
}
```

Add `"strconv"` to imports.

- [ ] **Step 4: Run**: `go test ./internal/printer` — the two new tests pass; if any existing test breaks, it can only be from the `glued` change: inspect, and update the expectation ONLY if the new layout is render-identical (it is, by the idiom's definition — the interp's space is immune to newline collapse).

- [ ] **Step 5: Commit**: `git add internal/printer && git commit -m "feat(fmt): {\" \"} spacing interps glue to their left neighbor"`

---

### Task 3: Inline tag set + atom docs

**Files:**
- Create: `internal/printer/inline.go`
- Test: `internal/printer/inline_test.go`

**Interfaces:**
- Consumes: `pretty.HasForcedBreak` (Task 1), existing `p.attrDoc`, `p.interp`, `p.embeddedInterp`, `p.fail`.
- Produces: `func (p *printer) atomDoc(e *ast.Element) (pretty.Doc, bool)` — flat one-line doc for an inline atom, or ok=false when e is not an atom. Tasks 4/5 consume it. Also `func isInlineTag(tag string) bool`.

- [ ] **Step 1: Write the failing test** (`inline_test.go`). Parse real sources — the harness mirrors `fmtSource` in `printer_test.go`:

```go
package printer

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// firstElement parses src (a component body's markup wrapped for parsing),
// normalizes, and returns the first element of the first component's body.
func firstElement(t *testing.T, src string) *ast.Element {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", []byte("package p\n\ncomponent T() {\n"+src+"\n}\n"), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			for _, m := range c.Body {
				if e, ok := m.(*ast.Element); ok {
					return e
				}
			}
		}
	}
	t.Fatal("no element found")
	return nil
}

func TestAtomDoc(t *testing.T) {
	p := &printer{width: 80, tabWidth: 2}
	atoms := []struct {
		src  string
		flat string
	}{
		{`<code>.gsx</code>`, `<code>.gsx</code>`},
		{`<code class="x">y</code>`, `<code class="x">y</code>`},
		{`<a href="/d"><code>x</code></a>`, `<a href="/d"><code>x</code></a>`},
		{`<br/>`, `<br/>`},
		{`<code>{ v }</code>`, `<code>{ v }</code>`},
	}
	for _, c := range atoms {
		e := firstElement(t, c.src)
		doc, ok := p.atomDoc(e)
		if !ok {
			t.Errorf("%s: expected atom", c.src)
			continue
		}
		if got := pretty.Print(doc, 10, 2); got != c.flat {
			// width 10 on purpose: an atom must render flat even when the
			// width budget is absurdly small.
			t.Errorf("%s: flat = %q, want %q", c.src, got, c.flat)
		}
	}
	notAtoms := []string{
		`<div>x</div>`,                    // not an inline tag
		`<Card>x</Card>`,                  // component
		`<span><div>x</div></span>`,       // block child
		`<code>{ if v { <b/> } }</code>`,  // control-flow child
		"<code>\n\tx\n</code>",            // author ChildrenMultiline wins
		`<textarea>x</textarea>`,          // preserve tag, never inline
		`<script>x</script>`,              // preserve tag, never inline
	}
	for _, src := range notAtoms {
		e := firstElement(t, src)
		if _, ok := p.atomDoc(e); ok {
			t.Errorf("%s: expected NOT atom", src)
		}
	}
}

func TestAtomDocPreserveContext(t *testing.T) {
	p := &printer{width: 80, tabWidth: 2, preserve: true}
	if _, ok := p.atomDoc(firstElement(t, `<code>x</code>`)); ok {
		t.Error("inside a preserve subtree nothing is an atom (text is verbatim)")
	}
}
```

- [ ] **Step 2: Run to verify failure**: `go test ./internal/printer -run TestAtomDoc` → FAIL `undefined: atomDoc`

- [ ] **Step 3: Implement** `inline.go`:

```go
package printer

import (
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
)

// inlineTags is prettier's HTML inline-elements list minus gsx's preserve
// tags (textarea; script/style were never in it). Only these can be atoms in
// text flow. Components (including lowercase tags that codegen later resolves
// to components — the formatter runs without analysis, so IsComponent is
// never stamped here) may be misclassified inline; that is layout-only and
// always render-safe, since an atom's flat rendering is byte-faithful.
var inlineTags = map[string]bool{
	"a": true, "abbr": true, "acronym": true, "b": true, "bdo": true,
	"big": true, "br": true, "button": true, "cite": true, "code": true,
	"dfn": true, "em": true, "font": true, "i": true, "img": true,
	"input": true, "kbd": true, "label": true, "map": true, "object": true,
	"output": true, "q": true, "samp": true, "select": true, "small": true,
	"span": true, "strong": true, "sub": true, "sup": true, "time": true,
	"tt": true, "u": true, "var": true, "video": true, "audio": true,
}

func isInlineTag(tag string) bool { return inlineTags[strings.ToLower(tag)] }

// atomDoc renders e as an inline atom: one flat line, no groups, so width
// pressure can never break it open. ok=false when e is not an atom — wrong
// tag, author multiline layout (which outranks atom status), a non-inline
// child, or any forced break in the assembled doc (line-comment attr,
// CondAttr, multi-line embedded attr value). Inside preserve subtrees text is
// verbatim (may hold significant newlines), so nothing is an atom there.
func (p *printer) atomDoc(e *ast.Element) (pretty.Doc, bool) {
	if p.preserve || !isInlineTag(e.Tag) || e.TypeArgs != "" ||
		e.AttrsMultiline || e.ChildrenMultiline {
		return pretty.Doc{}, false
	}
	parts := []pretty.Doc{pretty.Text("<"), pretty.Text(e.Tag)}
	for _, a := range e.Attrs {
		parts = append(parts, pretty.Text(" "), p.attrDoc(a))
	}
	if e.Void && len(e.Children) == 0 {
		parts = append(parts, pretty.Text("/>"))
	} else {
		parts = append(parts, pretty.Text(">"))
		for _, n := range e.Children {
			switch v := n.(type) {
			case *ast.Text:
				parts = append(parts, pretty.Text(v.Value))
			case *ast.Interp:
				parts = append(parts, p.interp(v))
			case *ast.EmbeddedInterp:
				parts = append(parts, p.embeddedInterp(v))
			case *ast.Element:
				child, ok := p.atomDoc(v)
				if !ok {
					return pretty.Doc{}, false
				}
				parts = append(parts, child)
			default:
				return pretty.Doc{}, false
			}
		}
		parts = append(parts, pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))
	}
	doc := pretty.Concat(parts...)
	if pretty.HasForcedBreak(doc) {
		return pretty.Doc{}, false
	}
	return doc, true
}
```

Note: `pretty.Doc{}` as the zero return requires `Doc` to be constructible;
it is (exported struct, zero value = empty text-less doc, never printed when
ok=false).

- [ ] **Step 4: Run**: `go test ./internal/printer -run TestAtomDoc` → PASS. The
`<textarea>`/`<script>` cases pass because those tags are not in the set;
the ChildrenMultiline case pins that author layout beats atomhood.

- [ ] **Step 5: Commit**: `git add internal/printer && git commit -m "feat(fmt): inline tag set and flat atom docs"`

---

### Task 4: Fill builder (leaves, bonds, gaps)

**Files:**
- Modify: `internal/printer/segment.go` (add `fillParts`, `inlineLeaf`)
- Test: `internal/printer/segment_test.go`

**Interfaces:**
- Consumes: `p.atomDoc` (Task 3), `isSpacingInterp` (Task 2), `trailsWithSpace`.
- Produces: `func (p *printer) fillParts(nodes []ast.Markup) []pretty.Doc` — alternating content/separator list for `pretty.Fill`. Bond = same cluster; word gap = `pretty.Line` separator; safe gap = `pretty.SoftLine` separator. `func (p *printer) inlineLeaf(n ast.Markup) pretty.Doc` — atomDoc for atoms, else `p.markup(n)`. Task 5 consumes both.

- [ ] **Step 1: Write the failing test** (append to `segment_test.go`; reuse `firstElement` from Task 3's test file):

```go
// fillAt prints the element's children as a Fill at the given width.
func fillAt(t *testing.T, src string, width int) string {
	t.Helper()
	p := &printer{width: width, tabWidth: 2}
	e := firstElement(t, src)
	return pretty.Print(pretty.Fill(p.fillParts(e.Children)...), width, 2)
}

func TestFillParts(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		width int
		want  string
	}{
		// Everything fits: flat output is the normalized one-line form.
		{"flat", `<p>alpha beta <code>x</code> gamma</p>`, 80,
			"alpha beta <code>x</code> gamma"},
		// Word gaps break; the tag-adjacent spaces bond beta/code/gamma into
		// one unbreakable cluster.
		{"wrap", `<p>alpha beta <code>x</code> gamma delta epsilon</p>`, 27,
			"alpha\nbeta <code>x</code> gamma\ndelta epsilon"},
		// Direct adjacency is a safe gap: over-narrow width may break after
		// </code> before the bonded-nothing period cluster... but greedy fill
		// keeps 1-char punctuation attached at any sane width.
		{"punct", `<p>uses <code>x</code>, and <code>y</code>.</p>`, 80,
			"uses <code>x</code>, and <code>y</code>."},
		// {" "} bonds left, safe gap right.
		{"spacing", `<p>see{ " " }<a href="/x">docs</a> now</p>`, 12,
			"see{ \" \" }\n<a href=\"/x\">docs</a> now"},
		// An interp bonded by significant spaces joins the cluster.
		{"interp bond", `<p>count: { n } items left today</p>`, 16,
			"count: { n } items\nleft today"},
	}
	for _, c := range cases {
		if got := fillAt(t, c.src, c.width); got != c.want {
			t.Errorf("%s:\n--- got ---\n%s\n--- want ---\n%s", c.name, got, c.want)
		}
	}
}
```

Width arithmetic for the pinned cases (indent 0 in this direct harness):
"wrap" at 27: `alpha` (5) + `" "` + cluster `beta <code>x</code> gamma` (26) =
32 > 27 → break; the cluster (26) fits; + `" "` + `delta` (5) = 32 > 27 →
break; `delta epsilon` (13) fits. "interp bond" at 16: cluster
`count: { n } items` is bond-joined (spaces touch the interp) = 18 > 16 but a
bond NEVER breaks — over-budget stays; then word gap before `left` breaks.
Wait — `{ n } items`: the space between `}` and `items` is the leading space
of the Text node `" items left today"` — a tag-adjacent significant space →
bond. `items`→`left` is a word gap. So the first line is 18 wide: bonds are
allowed to overflow, by design.

- [ ] **Step 2: Run to verify failure**: `go test ./internal/printer -run TestFillParts` → FAIL `undefined: fillParts`

- [ ] **Step 3: Implement** in `segment.go`:

```go
// fillParts flattens a children run into pretty.Fill parts — alternating
// cluster docs and separators. Joints between leaves follow wsnorm physics
// (see the design spec's table):
//
//   - bond (no separator; leaves concat into one cluster): a significant
//     space touching a non-word leaf — breaking it would drop the space at
//     render — and the left side of a {" "} spacing interp;
//   - word gap (pretty.Line, flat " "): space between two words of one Text
//     node — a break there re-normalizes to the same single space;
//   - safe gap (pretty.SoftLine, flat ""): direct adjacency and the right
//     side of a spacing interp — a break there drops nothing.
//
// The flat rendering is byte-identical to the normalized source, so layout
// can never change the rendered HTML.
func (p *printer) fillParts(nodes []ast.Markup) []pretty.Doc {
	var parts []pretty.Doc
	var cur []pretty.Doc
	gap := func(sep pretty.Doc) {
		parts = append(parts, pretty.Concat(cur...), sep)
		cur = nil
	}
	for i, n := range nodes {
		if t, ok := n.(*ast.Text); ok {
			v := t.Value
			switch {
			case strings.HasPrefix(v, " "):
				cur = append(cur, pretty.Text(" ")) // bond to the previous leaf
			case i > 0:
				gap(pretty.SoftLine) // direct adjacency: safe gap
			}
			words := strings.Fields(v)
			for j, w := range words {
				if j > 0 {
					gap(pretty.Line) // word gap
				}
				cur = append(cur, pretty.Text(w))
			}
			if strings.HasSuffix(v, " ") && v != " " {
				cur = append(cur, pretty.Text(" ")) // bond to the next leaf
			}
			continue
		}
		switch {
		case i == 0:
		case trailsWithSpace(nodes[i-1]) || isSpacingInterp(n):
			// bonded: the previous Text's trailing space is already in cur,
			// or the {" "} idiom glues to its left neighbor.
		default:
			gap(pretty.SoftLine)
		}
		cur = append(cur, p.inlineLeaf(n))
	}
	return append(parts, pretty.Concat(cur...))
}

// inlineLeaf renders one non-Text leaf: atoms flat, everything else through
// the normal markup path (a block element glued into a text segment keeps
// today's group-based rendering).
func (p *printer) inlineLeaf(n ast.Markup) pretty.Doc {
	if e, ok := n.(*ast.Element); ok {
		if doc, ok := p.atomDoc(e); ok {
			return doc
		}
	}
	return p.markup(n)
}
```

- [ ] **Step 4: Run**: `go test ./internal/printer -run TestFillParts` → PASS.
If a pinned `want` disagrees with actual greedy output, re-derive the width
arithmetic by hand FIRST (fillStep pairs content+sep+next) — fix the test
only if your arithmetic was wrong, fix `fillParts` if the joint
classification was wrong. Never pin output you can't justify.

- [ ] **Step 5: Commit**: `git add internal/printer && git commit -m "feat(fmt): fill builder over words, atoms, and joints"`

---

### Task 5: Wire fill into children layout

**Files:**
- Modify: `internal/printer/segment.go` (`blockLevel`/`hasBlockChild` become printer methods)
- Modify: `internal/printer/printer.go` (`childrenInner`; `element` force condition ~line 526; `cfBody` ~line 1124; delete `p.segment` ~line 426; grep for other `hasBlockChild(`/`blockLevel(` callers with `gopls references` and update)
- Test: `internal/printer/printer_test.go` (new behavior tests), existing suites

**Interfaces:**
- Consumes: `fillParts`, `inlineLeaf`, `atomDoc`, `segmentChildren`.
- Produces: new `childrenInner` semantics (same signature `(doc pretty.Doc, breakable bool)`); `func (p *printer) hasBlockChild(nodes []ast.Markup) bool`; `func (p *printer) blockLevel(n ast.Markup) bool`. All later tasks are corpus/docs only.

- [ ] **Step 1: Write the failing behavior tests** (append to `printer_test.go`; `checkFormat` prints at width 80 and asserts idempotence):

```go
func TestInlineAtomsStayInline(t *testing.T) {
	// The paragraph that motivated this work: <code> must not explode even
	// though the glued tail overflows; long prose wraps at word gaps.
	checkFormat(t,
		"package p\n\ncomponent C() {\n\t<p>\n\t\tthe CLI vendors real <code>.gsx</code> source into your own module, so what you build against is code you own\n\t</p>\n}\n",
		// Width 80, children indent 2 tabs (4 cols): "the CLI vendors real
		// <code>.gsx</code> source into your own module, so what" measures
		// 4+75=79 ≤ 80; adding " you" overflows → break before "you".
		"package p\n\ncomponent C() {\n\t<p>\n\t\tthe CLI vendors real <code>.gsx</code> source into your own module, so what\n\t\tyou build against is code you own\n\t</p>\n}\n")
}

func TestInlineOnlyOneLinerStays(t *testing.T) {
	// An all-inline children list no longer forces the parent open.
	src := "package p\n\ncomponent C() {\n\t<p>vendors real <code>.gsx</code> source.</p>\n}\n"
	checkFormat(t, src, src)
}

func TestBlockChildStillForces(t *testing.T) {
	checkFormat(t,
		"package p\n\ncomponent C() {\n\t<div><span>a</span> <div>b</div></div>\n}\n",
		"package p\n\ncomponent C() {\n\t<div>\n\t\t<span>a</span> <div>b</div>\n\t</div>\n}\n")
}

func TestSpacingInterpGlue(t *testing.T) {
	checkFormat(t,
		"package p\n\ncomponent C() {\n\t<p>\n\t\tcaller-class-merge work as documented in the guide here (see{ \" \" }\n\t\t<a href=\"/docs/theming\" class=\"underline underline-offset-4\">Theming</a>)\n\t</p>\n}\n",
		"package p\n\ncomponent C() {\n\t<p>\n\t\tcaller-class-merge work as documented in the guide here (see{ \" \" }\n\t\t<a href=\"/docs/theming\" class=\"underline underline-offset-4\">Theming</a>)\n\t</p>\n}\n")
}
```

Derive each `want` by hand from the joint table and width 80 with 2-column
tabs before running; adjust ONLY with written arithmetic (same rule as
Task 4 Step 4).

- [ ] **Step 2: Run to verify failure**: `go test ./internal/printer -run 'TestInline|TestBlockChildStill|TestSpacingInterpGlue'` → FAIL (today: `<code>` explodes, one-liners force open)

- [ ] **Step 3: Implement.** In `segment.go`, replace the free functions:

```go
// blockLevel reports whether n is block-level: a construct whose presence
// makes the children list lay out as a broken block so the document
// hierarchy stays visible. Inline atoms and interps are NOT block-level; a
// non-atom element (wrong tag, author multiline, forced break inside) is.
func (p *printer) blockLevel(n ast.Markup) bool {
	if e, ok := n.(*ast.Element); ok {
		_, atom := p.atomDoc(e)
		return !atom
	}
	switch n.(type) {
	case *ast.Fragment, *ast.IfMarkup, *ast.ForMarkup, *ast.SwitchMarkup,
		*ast.GoBlock, *ast.Doctype, *ast.HTMLComment:
		return true
	default:
		return false
	}
}

// hasBlockChild reports whether nodes contains at least one block-level child.
func (p *printer) hasBlockChild(nodes []ast.Markup) bool {
	return slices.ContainsFunc(nodes, p.blockLevel)
}
```

In `printer.go`, replace `childrenInner` (and delete the now-unused
`p.segment`):

```go
// childrenInner builds the inline content of a children list and reports
// whether the list is breakable. Inline-only lists become one Fill (greedy
// word-gap wrapping; safe gaps between former segments are SoftLine
// separators inside the Fill). Lists with a block child keep the structural
// one-segment-per-line layout — the SoftLines between segments break under
// the caller's forced group — with a Fill inside each segment so prose next
// to a block element still wraps at word gaps. Edge-unsafe lists keep the
// old flat form. For preserved subtrees use childrenPreserve instead.
func (p *printer) childrenInner(nodes []ast.Markup) (doc pretty.Doc, breakable bool) {
	segs, breakable := segmentChildren(nodes)
	if !breakable {
		parts := make([]pretty.Doc, 0, len(segs)*2)
		for i, s := range segs {
			if i > 0 {
				parts = append(parts, pretty.SoftLine)
			}
			for _, n := range s.nodes {
				parts = append(parts, p.markup(n))
			}
		}
		return pretty.Concat(parts...), false
	}
	if !p.hasBlockChild(nodes) {
		return pretty.Fill(p.fillParts(nodes)...), true
	}
	parts := make([]pretty.Doc, 0, len(segs)*2)
	for i, s := range segs {
		if i > 0 {
			parts = append(parts, pretty.SoftLine)
		}
		parts = append(parts, pretty.Fill(p.fillParts(s.nodes)...))
	}
	return pretty.Concat(parts...), true
}
```

Update callers: `element()` force condition becomes
`if p.hasBlockChild(e.Children) || e.ChildrenMultiline`; `cfBody` becomes
`if p.hasBlockChild(nodes) || multiline`; run
`gopls references` on the old free functions (or
`grep -n "hasBlockChild(\|blockLevel(" internal/printer/*.go`) and convert
every remaining call site the same way.

- [ ] **Step 4: Run the new tests**: `go test ./internal/printer -run 'TestInline|TestBlockChildStill|TestSpacingInterpGlue'` → PASS

- [ ] **Step 5: Reconcile the existing printer suites**

Run: `go test ./internal/printer`
Many pinned layouts will shift (inline one-liners stop forcing open;
punctuation orphans rejoin; prose reflows). For EACH failing case: the new
output must differ only in line-break placement at render-free joints —
verify by the joint table, then update the expectation. Any diff that adds
or removes a rendered byte is a bug in Tasks 2-5: stop and fix. The
faithfulness property suites (`corpus_property_test.go`,
`goexpr_gofmt_property_test.go`, fuzz seeds) must pass untouched — they are
the render-identity oracle. Expect `TestFillParts`'s "punct" case to
already pass unchanged.

- [ ] **Step 6: Reconcile the gsxfmt corpus**

```bash
go test ./internal/gsxfmt              # observe failures
go test ./internal/gsxfmt -run TestFmtCorpus -update
go test ./internal/gsxfmt              # verify clean without -update
git diff internal/gsxfmt/testdata      # eyeball EVERY golden diff
```
Same rule: layout-only diffs at render-free joints. Then confirm the
semantic corpus is untouched: `go test ./internal/corpus` must pass with NO
`-update`.

- [ ] **Step 7: Commit**: `git add -A && git commit -m "feat(fmt): fill-based children layout — atoms stay inline, prose wraps at word gaps"`

---

### Task 6: New fmt-corpus cases

**Files:**
- Create: `internal/gsxfmt/testdata/cases/inline-atoms.txtar`
- Create: `internal/gsxfmt/testdata/cases/word-gap-wrap.txtar`
- Create: `internal/gsxfmt/testdata/cases/spacing-interp.txtar`
- Create: `internal/gsxfmt/testdata/cases/inline-mixed-block.txtar`

**Interfaces:** none — pins Task 5's behavior. Follow the existing txtar shape in `internal/gsxfmt/testdata/cases/` (`input.gsx` + `fmt.golden`); copy a neighboring case's file layout exactly.

- [ ] **Step 1: Write the four cases' `input.gsx` sections** covering, across the four files:
  1. atom under width pressure stays inline (the `getting_started` paragraph, verbatim, as `inline-atoms.txtar`);
  2. inline-only one-liner stays one line; atom with author-multiline children falls back to block; `<textarea>`/`<script>` never atoms (add to `inline-atoms.txtar`);
  3. long prose canonical fill, including an author-wrapped paragraph that reflows and a bond-only over-budget line that stays long (`word-gap-wrap.txtar`);
  4. `see{" "}` at line end + `{" "}` between atoms (`spacing-interp.txtar`);
  5. a block `<div>` glued into prose (segment keeps its line, prose fills), and an edge-unsafe list staying flat (`inline-mixed-block.txtar`);
  6. a `pre` subtree containing an inline-tag element — must stay verbatim (add to `inline-mixed-block.txtar`).

- [ ] **Step 2: Generate goldens**: `go test ./internal/gsxfmt -run TestFmtCorpus -update`

- [ ] **Step 3: Review each generated `fmt.golden` against the spec by hand** — this is the step that catches wiring bugs; a golden that surprises you is a finding, not a fact. Then verify: `go test ./internal/gsxfmt` (no `-update`) → ok.

- [ ] **Step 4: Full check**: `make check` → green (both modules, examples drift, gofmt+gsx fmt).
If the repo's own `.gsx` sources or `examples/*.txtar` now have fmt drift
(they will — layouts changed), reformat/regenerate them as directed by the
failing gate's output, eyeball the diffs (layout-only), and include them.

- [ ] **Step 5: Commit**: `git add -A && git commit -m "test(fmt): corpus cases for inline atoms, word-gap fill, spacing interps"`

---

### Task 7: Docs + gate

**Files:**
- Modify: `docs/guide/` formatter page (locate with `grep -rln "gsx fmt" docs/guide/`)
- Modify: `docs/ROADMAP.md` (mark the formatter item)
- Test: `make ci`, `make lint`

**Interfaces:** none.

- [ ] **Step 1: Document the behavior** in the guide's fmt section — concise, behavior-only (per repo feedback: no rationale essays; rationale lives in the spec). Cover, in a few sentences each: inline elements stay in text flow and never break open; long prose wraps between words (render-identical — newlines between words collapse to a space, newlines against a tag would delete the space, so the formatter only ever breaks between words); `{" "}` sticks to the word before it. Wrap any literal `{{ }}` in `::: v-pre` (VitePress).

- [ ] **Step 2: ROADMAP**: update the formatter line to reflect shipped inline-atom + fill layout.

- [ ] **Step 3: Gates**:

```bash
make lint
make ci
```
Expected: both green, uncached. The merge decision chains ONLY on these exit
codes (never `||`-swallowed — see repo memory: a red `make ci` was once
merged through a swallowed exit status).

- [ ] **Step 4: Commit**: `git add -A && git commit -m "docs(fmt): inline flow and word-gap wrapping"`

---

## Final verification (before PR)

- [ ] Independent adversarial review (repo convention): a reviewer that BUILDS probe programs — at minimum: run the new formatter over `../gsxui/site/pages/*.gsx` in a scratch checkout, regenerate, render `/docs/getting-started` via the `pages_test.go` harness, and diff rendered HTML against pre-format rendering (must be byte-identical; the recipe from 2026-07-24's session is in memory `gsx-fmt-inline-atoms-direction`).
- [ ] `go test ./... -count=1` in the root module; `make ci` green.
- [ ] PR body links the spec; PR ends with the session trailer.
