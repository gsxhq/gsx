# gsx Parser — Brace-Matching Soundness Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the parser's brace/paren matching sound over markup so that ordinary prose (apostrophes, contractions) and valid Go composite literals in control-flow headers parse correctly, by ensuring `go/scanner` only ever runs over pure-Go regions.

**Architecture:** Split brace handling by what lies between the delimiters. (A) Pure-Go delimiters keep `go/scanner` but scan from the opening delimiter, so preceding prose is never tokenized. (B) Markup-bounded regions (component body, markup attribute, and file-level component discovery) stop using `go/scanner` entirely — they parse markup recursively until the top-level `}`, or scan only the pure-Go gaps between components. (C) The control-flow body brace is found with full Go fidelity by test-parsing candidate braces with `go/parser`.

**Tech Stack:** Go 1.26.1, standard library only. Packages: `github.com/gsxhq/gsx/parser`, `github.com/gsxhq/gsx/ast`. `go/scanner`, `go/token`, and (new) `go/parser` for boundary finding.

## Global Constraints

- **Standard library only** — no third-party dependencies.
- **The invariant being restored:** `go/scanner` (and `go/parser`) must only ever tokenize **pure-Go** regions, never markup prose. Every change in this plan exists to make that true.
- **Fail-fast parser** — stops at first error, returns `(nil, err)`; error strings are `line:col: message` using rune columns from `p.file.Position(pos)`.
- **Public API unchanged** — AST node shapes, `Inspect`, `Fprint`, `ParseFile` signature, and the go/ast parity all stay as-is. This is an internal-correctness fix.
- **Behavior preservation** — all existing tests (`go test ./...`) stay green, including the Part 2 pipeline goldens and `examples_coverage.golden`. Whitespace handling is unchanged (component bodies and control bodies already skip inter-node whitespace).
- **gofmt + go vet clean**; prefer `gopls check -severity=hint` to catch newly-unused functions.

### Reference: confirmed bugs (must be fixed)

- **C1 (Critical):** an apostrophe (or lone `'`) in markup prose opens a Go rune literal in the scanner that swallows to end-of-line, eating a later same-line brace. Repros: `<p>Today's items: {n}</p>` → `unterminated \`{\``; `component C() { { if c { <p>it's here</p> } } }` → `unterminated component body`.
- **I2 (Important):** `scanToBlockBrace` takes a bare composite literal's `{` as the body brace. Repro: `{ for _, v := range []int{1,2} { … } }` → `expected \`}\` to close \`{ for … }\``.
- **B3 (part of C1):** `topLevelComponentOffsets` scans the whole file, so a same-line apostrophe+brace in one component body desyncs brace depth and can drop a later component.

---

## File Structure

- `parser/boundary.go` — **modify**: `goExprEnd`/`parenEnd` scan from `open`; rewrite `scanToBlockBrace` to test-parse candidates with `go/parser` (add `blockHeaderParses`). Add `import "go/parser"`.
- `parser/markup.go` — **modify**: add `parseMarkupUntilClose(what string)`; make `parseControlBody` delegate to it; switch the markup-attribute branch of `parseAttrBraceValue` to in-place parse; pass the keyword to `scanToBlockBrace` from `parseIfTail`/`parseForMarkup`/`parseSwitchMarkup`.
- `parser/attrs.go` — **modify**: `parseCondAttrTail` passes the keyword to `scanToBlockBrace`.
- `parser/component.go` — **modify**: parse the component body in place via `parseMarkupUntilClose` (drop `goExprEnd` + `newSub`).
- `parser/file.go` — **modify**: replace `topLevelComponentOffsets` (whole-file pre-scan) with `nextTopLevelComponent` (incremental, pure-Go gaps only) and an interleaved walk.
- `parser/parser.go` — **modify (cleanup)**: remove `newSub` if it becomes unused after B1/B2.
- Tests: `parser/boundary_test.go`, `parser/markup_test.go`, `parser/component_test.go`, `parser/file_test.go`, and a new `parser/soundness_test.go` for the consolidated C1/I2/B3 repros.

---

### Task 1 (A): `goExprEnd` and `parenEnd` scan from the opening delimiter

**Files:**
- Modify: `parser/boundary.go`
- Test: `parser/boundary_test.go`

**Interfaces:**
- Produces: `goExprEnd(src string, open int) (int, bool)` and `parenEnd(src string, open int) (int, bool)` — same signatures and return semantics (index of the matching `}`/`)`), but no longer desync on prose **before** `open`.

- [ ] **Step 1: Write the failing test**

Add to `parser/boundary_test.go`:

```go
func TestGoExprEndIgnoresPrecedingProse(t *testing.T) {
	// An apostrophe BEFORE the brace must not desync the scanner: goExprEnd is
	// asked to match the brace at `open`, and only the region from `open` on
	// (pure Go) should be tokenized.
	src := `Today's items: {n}`
	open := strings.IndexByte(src, '{')
	end, ok := goExprEnd(src, open)
	if !ok {
		t.Fatalf("goExprEnd returned ok=false; want match at the closing brace")
	}
	if src[end] != '}' || end != len(src)-1 {
		t.Fatalf("end=%d (src[end]=%q), want %d (the final '}')", end, string(src[end]), len(src)-1)
	}
}

func TestParenEndIgnoresPrecedingProse(t *testing.T) {
	src := `it's (a, b)`
	open := strings.IndexByte(src, '(')
	end, ok := parenEnd(src, open)
	if !ok || src[end] != ')' || end != len(src)-1 {
		t.Fatalf("parenEnd end=%d ok=%v, want final ')'", end, ok)
	}
}
```

Ensure `parser/boundary_test.go` imports `"strings"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run 'TestGoExprEndIgnoresPrecedingProse|TestParenEndIgnoresPrecedingProse' -v`
Expected: FAIL — `ok=false` (the apostrophe before `{`/`(` desyncs the current offset-0 scan).

- [ ] **Step 3: Rewrite `goExprEnd` to scan from `open`**

In `parser/boundary.go`, replace `goExprEnd` with:

```go
// goExprEnd returns the index of the `}` that matches the `{` at src[open],
// scanning Go tokens from `open` so that (a) braces inside strings, runes, and
// comments do not count and (b) any markup prose BEFORE `open` is never
// tokenized. ok is false if no matching brace is found.
func goExprEnd(src string, open int) (int, bool) {
	sub := src[open:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	// ScanComments so comment text (which may contain braces) is consumed as a unit.
	s.Init(file, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LBRACE, token.LPAREN, token.LBRACK:
			depth++
		case token.RBRACE, token.RPAREN, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RBRACE {
				return open + fset.Position(pos).Offset, true
			}
		}
	}
}
```

- [ ] **Step 4: Rewrite `parenEnd` to scan from `open`**

In `parser/boundary.go`, replace `parenEnd` with:

```go
// parenEnd returns the index of the `)` matching the `(` at src[open], scanning
// Go tokens from `open` so prose before `open` is never tokenized.
func parenEnd(src string, open int) (int, bool) {
	sub := src[open:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RPAREN {
				return open + fset.Position(pos).Offset, true
			}
		}
	}
}
```

- [ ] **Step 5: Run to verify pass + no regressions**

Run: `go test ./parser/ ./ast/ ./internal/corpus/`
Expected: PASS. (The two new tests pass; all existing parser/corpus tests — which exercise `goExprEnd`/`parenEnd` via interpolation, spread, class, receiver/params — stay green because the result is identical for synced input.)

- [ ] **Step 6: Commit**

```bash
git add parser/boundary.go parser/boundary_test.go
git commit -m "fix(parser): goExprEnd/parenEnd scan from open delimiter (no preceding-prose desync)"
```

---

### Task 2 (B1): Parse component bodies in place; add `parseMarkupUntilClose`

**Files:**
- Modify: `parser/markup.go`
- Modify: `parser/component.go`
- Test: `parser/markup_test.go`, `parser/component_test.go`

**Interfaces:**
- Consumes: `parseElement`, `parseBraceNode`, `parseTextCtx` (existing); `p.skipSpace`, `p.eof`, `p.peek`, `p.pos`, `p.file`.
- Produces: `func (p *parser) parseMarkupUntilClose(what string) ([]ast.Markup, error)` — parses markup until and consuming the matching top-level `}`; `what` names the construct in the unterminated-EOF error. `parseControlBody` delegates to it. `parseComponent` uses it for the body in place (no `goExprEnd`, no `newSub`).

- [ ] **Step 1: Write the failing test**

Add to `parser/component_test.go`:

```go
func TestComponentBodyWithApostrophe(t *testing.T) {
	// C1: apostrophe in body markup on the same line as a later brace must parse.
	src := "package p\ncomponent C(n int) {\n\t<p>Today's items: {n}</p>\n}"
	file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	comp := file.Decls[0].(*ast.Component)
	p := comp.Body[0].(*ast.Element)
	if p.Tag != "p" {
		t.Fatalf("body[0] = %#v, want <p>", comp.Body[0])
	}
	// <p> children: Text "Today's items: " then Interp{n}
	var sawApostropheText, sawInterp bool
	for _, c := range p.Children {
		if txt, ok := c.(*ast.Text); ok && strings.Contains(txt.Value, "Today's") {
			sawApostropheText = true
		}
		if in, ok := c.(*ast.Interp); ok && in.Expr == "n" {
			sawInterp = true
		}
	}
	if !sawApostropheText || !sawInterp {
		t.Fatalf("children = %#v (apostropheText=%v interp=%v)", p.Children, sawApostropheText, sawInterp)
	}
}

func TestComponentBodyControlFlowWithApostrophe(t *testing.T) {
	// C1: apostrophe inside a control-flow body inside a component body.
	src := "package p\ncomponent C(c bool) {\n\t{ if c { <p>it's here</p> } }\n}"
	if _, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0); err != nil {
		t.Fatalf("parse error: %v", err)
	}
}

func TestComponentBodyUnterminated(t *testing.T) {
	// Negative: a body missing its closing brace fails cleanly (no panic/hang).
	src := "package p\ncomponent C() {\n\t<p>hi</p>"
	_, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err == nil {
		t.Fatal("expected unterminated-body error, got nil")
	}
	if !strings.Contains(err.Error(), "component body") {
		t.Fatalf("error = %v, want mention of `component body`", err)
	}
}
```

Ensure `parser/component_test.go` imports `"go/token"`, `"strings"`, and `"github.com/gsxhq/gsx/ast"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run 'TestComponentBodyWithApostrophe|TestComponentBodyControlFlowWithApostrophe|TestComponentBodyUnterminated' -v`
Expected: FAIL — the first two with `unterminated \`{\`` / `unterminated component body` (current `goExprEnd`-over-markup desync); the third may pass or fail with a differently-worded error.

- [ ] **Step 3: Add `parseMarkupUntilClose` and delegate `parseControlBody`**

First read the current `parseControlBody` in `parser/markup.go` to confirm its exact loop and error text. Then add `parseMarkupUntilClose` and replace `parseControlBody`'s body with a delegation:

```go
// parseMarkupUntilClose parses a markup sequence terminated by the matching
// top-level '}', which it consumes. `what` names the enclosing construct for the
// unterminated-EOF error (e.g. "control-flow body", "component body"). Inter-node
// whitespace is skipped; text within nodes is preserved. The terminating '}' is
// the first top-level '}'; a '}' inside a nested element's text or a `{…}`
// construct is consumed by those sub-parsers, not seen here.
func (p *parser) parseMarkupUntilClose(what string) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		p.skipSpace()
		if p.eof() {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated %s, expected `}`", cp.Line, cp.Column, what)
		}
		switch {
		case p.peek() == '}':
			p.i++ // consume the closing brace
			return nodes, nil
		case p.peek() == '<':
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, el)
		case p.peek() == '{':
			node, skipped, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			if !skipped {
				nodes = append(nodes, node)
			}
		default:
			nodes = append(nodes, p.parseTextCtx(true))
		}
	}
}

// parseControlBody parses a control-flow body: markup until the matching '}'.
func (p *parser) parseControlBody() ([]ast.Markup, error) {
	return p.parseMarkupUntilClose("control-flow body")
}
```

If the previous `parseControlBody` used a different EOF message or omitted the `p.skipSpace()` at loop top, match the prior observable behavior: the prior control-flow tests must stay green. (The prior implementation skips inter-node whitespace and terminates on top-level `}`, which `parseMarkupUntilClose` reproduces.)

- [ ] **Step 4: Parse the component body in place**

In `parser/component.go`, replace the body-bounding block (from the `p.peek() != '{'` check through the `c.Body = nodes` assignment) with:

```go
	p.skipSpace()
	if p.peek() != '{' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `{` to open component body", cp.Line, cp.Column)
	}
	p.i++ // past body '{'
	nodes, err := p.parseMarkupUntilClose("component body")
	if err != nil {
		return nil, err
	}
	c.Body = nodes
	ast.SetSpan(c, startPos, p.posAt(p.i))
	return c, nil
```

Remove the now-unused `goExprEnd` call, `bodyStart`/`body`/`subBase` locals, the `newSub` call, and `parseNodesUntilEOF` invocation from `parseComponent`. (Do not delete `newSub` itself yet — Task 3 removes it if it becomes unused.)

- [ ] **Step 5: Run to verify pass + no regressions**

Run: `go test ./parser/ ./ast/ ./internal/corpus/`
Expected: PASS — the three new component tests pass; all existing parser and corpus tests (which parse component bodies) stay green.

- [ ] **Step 6: Commit**

```bash
git add parser/markup.go parser/component.go parser/component_test.go
git commit -m "fix(parser): parse component bodies in place via parseMarkupUntilClose (B1)"
```

---

### Task 3 (B2): Parse markup attributes in place; drop `newSub` if unused

**Files:**
- Modify: `parser/markup.go`
- Modify: `parser/parser.go` (cleanup)
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `parseMarkupUntilClose` (Task 2).
- Produces: `parseAttrBraceValue` markup branch parses in place until `}` (no `goExprEnd`/`newSub`).

- [ ] **Step 1: Write the failing test**

Add to `parser/markup_test.go`:

```go
func TestMarkupAttrWithApostrophe(t *testing.T) {
	// C1: apostrophe inside a markup-attribute value must parse.
	p := testParser(`<Panel header={ <h1>Today's news</h1> }></Panel>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := n.(*ast.Element)
	ma, ok := el.Attrs[0].(*ast.MarkupAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.MarkupAttr", el.Attrs[0])
	}
	h1 := ma.Value[0].(*ast.Element)
	if h1.Tag != "h1" {
		t.Fatalf("markup attr value = %#v", ma.Value)
	}
	var txt *ast.Text
	for _, c := range h1.Children {
		if t2, ok := c.(*ast.Text); ok {
			txt = t2
		}
	}
	if txt == nil || !strings.Contains(txt.Value, "Today's") {
		t.Fatalf("h1 children = %#v, want text containing apostrophe", h1.Children)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run TestMarkupAttrWithApostrophe -v`
Expected: FAIL — `unterminated markup attribute` (the current `goExprEnd`-over-markup desync on `Today's`).

- [ ] **Step 3: Parse the markup-attribute value in place**

In `parser/markup.go`, in `parseAttrBraceValue`, replace the markup branch (the `if j < len(p.src) && p.src[j] == '<' && …` block that calls `goExprEnd` + `newSub` + `parseNodesUntilEOF`) with:

```go
	if j < len(p.src) && p.src[j] == '<' && j+1 < len(p.src) && startsTag(p.src[j+1]) {
		p.i++ // past '{'
		nodes, err := p.parseMarkupUntilClose("markup attribute")
		if err != nil {
			return nil, err
		}
		ma := &ast.MarkupAttr{Name: name, Value: nodes}
		ast.SetSpan(ma, attrStartPos, p.posAt(p.i))
		return ma, nil
	}
```

- [ ] **Step 4: Remove `newSub` if now unused**

Check whether `newSub` (and `parseNodesUntilEOF`, if it too is now unused) still have callers:

Run: `gopls check -severity=hint parser/parser.go parser/markup.go parser/file.go`
And: `grep -rn 'newSub\|parseNodesUntilEOF' parser/`

If `newSub` has no remaining callers, delete its definition from `parser/parser.go`. If `parseNodesUntilEOF` has no remaining callers, delete it from `parser/markup.go`. If either is still referenced (e.g. by a test), leave it and note that in the commit message.

- [ ] **Step 5: Run to verify pass + no regressions**

Run: `go test ./parser/ ./ast/ ./internal/corpus/` and `go vet ./parser/` and `gofmt -l parser/`
Expected: PASS, vet clean, no formatting diffs.

- [ ] **Step 6: Commit**

```bash
git add parser/markup.go parser/parser.go parser/markup_test.go
git commit -m "fix(parser): parse markup-attribute values in place; drop unused newSub (B2)"
```

---

### Task 4 (B3): Interleaved top-level component discovery in `file.go`

**Files:**
- Modify: `parser/file.go`
- Test: `parser/file_test.go`

**Interfaces:**
- Consumes: `parseComponent` (now consumes the body in place, Task 2); `scanPackage` (existing).
- Produces: `func nextTopLevelComponent(src string, from int) (int, bool)` — offset of the next depth-0 `component` keyword at or after `from`, scanning Go tokens over the pure-Go gap only. `ParseFile`'s main loop becomes interleaved. `topLevelComponentOffsets` is removed.

- [ ] **Step 1: Write the failing test**

Add to `parser/file_test.go`:

```go
func TestMultiComponentWithApostrophe(t *testing.T) {
	// B3: an apostrophe (same line as a brace) in the FIRST component's body must
	// not cause the SECOND component to be dropped/misparsed.
	src := "package p\n" +
		"component A() {\n\t<p>Jack's profile</p>\n}\n" +
		"component B() {\n\t<span>ok</span>\n}\n"
	file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var names []string
	for _, d := range file.Decls {
		if c, ok := d.(*ast.Component); ok {
			names = append(names, c.Name)
		}
	}
	if len(names) != 2 || names[0] != "A" || names[1] != "B" {
		t.Fatalf("component names = %v, want [A B]", names)
	}
}

func TestGoDeclsBetweenComponents(t *testing.T) {
	// Interleaved Go funcs/types between components still split correctly.
	src := "package p\n" +
		"type T struct{ X int }\n" +
		"component A() {\n\t<a/>\n}\n" +
		"func helper() string { return \"x\" }\n" +
		"component B() {\n\t<b/>\n}\n"
	file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	// Expect: GoChunk(type T), Component A, GoChunk(func helper), Component B
	var kinds []string
	for _, d := range file.Decls {
		switch d.(type) {
		case *ast.GoChunk:
			kinds = append(kinds, "go")
		case *ast.Component:
			kinds = append(kinds, "comp")
		}
	}
	want := []string{"go", "comp", "go", "comp"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("decl kinds = %v, want %v", kinds, want)
	}
}
```

Ensure `parser/file_test.go` imports `"reflect"`, `"go/token"`, and `"github.com/gsxhq/gsx/ast"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run 'TestMultiComponentWithApostrophe|TestGoDeclsBetweenComponents' -v`
Expected: `TestMultiComponentWithApostrophe` FAILS (the whole-file scan in `topLevelComponentOffsets` desyncs on `Jack's` and miscounts, dropping/merging B). `TestGoDeclsBetweenComponents` may already pass.

- [ ] **Step 3: Add `nextTopLevelComponent` and remove `topLevelComponentOffsets`**

In `parser/file.go`, delete `topLevelComponentOffsets` and add:

```go
// nextTopLevelComponent returns the byte offset of the next `component`
// identifier at brace depth 0 at or after `from`, scanning Go tokens over
// src[from:]. The region [from, returned offset) is a pure-Go gap: component
// bodies (which contain markup) begin after the `component` keyword and are
// consumed by parseComponent, never by this scan. found is false if there is no
// further top-level component.
func nextTopLevelComponent(src string, from int) (int, bool) {
	sub := src[from:]
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(localFile, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LBRACE:
			depth++
		case token.RBRACE:
			if depth > 0 {
				depth--
			}
		case token.IDENT:
			if depth == 0 && lit == "component" {
				return from + localFset.Position(pos).Offset, true
			}
		}
	}
}
```

- [ ] **Step 4: Make `ParseFile`'s main loop interleaved**

In `parser/file.go`, replace the `offsets := topLevelComponentOffsets(srcBytes)` line and the `for _, off := range offsets { … }` loop with:

```go
	cursor := pkgEnd
	p := newParser(file, srcStr)
	for {
		off, found := nextTopLevelComponent(srcStr, cursor)
		if !found {
			break
		}
		if chunk := strings.TrimSpace(srcStr[cursor:off]); chunk != "" {
			gc := &ast.GoChunk{Src: srcStr[cursor:off]}
			ast.SetSpan(gc, file.Pos(cursor), file.Pos(off))
			f.Decls = append(f.Decls, gc)
		}
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			return nil, err
		}
		f.Decls = append(f.Decls, c)
		cursor = p.i
	}
```

(Leave the existing trailing-`GoChunk` block after the loop unchanged; the `f := &ast.File{…}` construction and `ast.SetSpan(f, …)` above are also unchanged.)

- [ ] **Step 5: Run to verify pass + no regressions**

Run: `go test ./parser/ ./ast/ ./internal/corpus/`
Expected: PASS — both new file tests pass; all existing tests (including the examples corpus, which has multi-decl files) stay green.

- [ ] **Step 6: Commit**

```bash
git add parser/file.go parser/file_test.go
git commit -m "fix(parser): interleaved top-level component discovery (no whole-file markup scan) (B3)"
```

---

### Task 5 (C): Full-fidelity control-flow body brace via `go/parser` (fixes I2)

**Files:**
- Modify: `parser/boundary.go`
- Modify: `parser/markup.go` (`parseIfTail`, `parseForMarkup`, `parseSwitchMarkup`)
- Modify: `parser/attrs.go` (`parseCondAttrTail`)
- Test: `parser/markup_test.go`

**Interfaces:**
- Produces: `scanToBlockBrace(src string, from int, keyword string) (int, bool)` — `from` points just after the keyword; `keyword` is `"if"`/`"for"`/`"switch"`. Returns the offset of the body-opening `{`, validated by test-parsing `keyword + " " + src[from:b] + "{}"`. New helper `blockHeaderParses(header string) bool`.
- Consumes: `go/parser` (new import in `boundary.go`).

- [ ] **Step 1: Write the failing test**

Add to `parser/markup_test.go`:

```go
func TestParseForRangeSliceLiteral(t *testing.T) {
	// I2: ranging over a bare composite literal — the literal's '{' must NOT be
	// taken as the body brace.
	p := testParser(`{ for _, v := range []int{1, 2} { <a>{v}</a> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	n, ok := node.(*ast.ForMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.ForMarkup", node)
	}
	if n.Clause != "_, v := range []int{1, 2}" {
		t.Fatalf("Clause = %q", n.Clause)
	}
	if n.Body[0].(*ast.Element).Tag != "a" {
		t.Fatalf("body = %#v", n.Body)
	}
}

func TestParseForRangeMapLiteral(t *testing.T) {
	p := testParser(`{ for k := range map[string]int{"a": 1} { <i>{k}</i> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	n := node.(*ast.ForMarkup)
	if n.Clause != `k := range map[string]int{"a": 1}` {
		t.Fatalf("Clause = %q", n.Clause)
	}
}

func TestParseIfParenComposite(t *testing.T) {
	// Paren-wrapped composite in an if condition still resolves to the body brace.
	p := testParser(`{ if (struct{ Ok bool }{Ok: true}).Ok { <y/> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	n := node.(*ast.IfMarkup)
	if n.Then[0].(*ast.Element).Tag != "y" {
		t.Fatalf("then = %#v", n.Then)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run 'TestParseForRangeSliceLiteral|TestParseForRangeMapLiteral' -v`
Expected: FAIL — `expected \`}\` to close \`{ for … }\`` (the composite literal's `{` is mistaken for the body brace).

- [ ] **Step 3: Rewrite `scanToBlockBrace` with `go/parser` validation**

In `parser/boundary.go`, add `"go/parser"` to the import block, then replace `scanToBlockBrace` with:

```go
// scanToBlockBrace finds the byte offset of the '{' that opens a control-flow
// body. `from` points just after the leading `keyword` ("if"/"for"/"switch").
// It enumerates each '{' at paren/bracket depth 0 and returns the first one for
// which `keyword <header> {}` parses as a valid Go statement — delegating
// composite-literal disambiguation to go/parser, so bare composite literals in a
// `for … range` clause are handled correctly. ok is false if none parse.
func scanToBlockBrace(src string, from int, keyword string) (int, bool) {
	sub := src[from:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
		case token.LBRACE:
			if depth == 0 {
				b := from + fset.Position(pos).Offset
				if blockHeaderParses(keyword + " " + src[from:b]) {
					return b, true
				}
				depth++ // composite-literal brace; descend into it
			} else {
				depth++
			}
		case token.RBRACE:
			depth--
		}
	}
}

// blockHeaderParses reports whether `header {}` is a valid Go control-flow
// statement (header includes the leading keyword). Used to locate the body brace
// of a gsx control-flow construct with full Go fidelity.
func blockHeaderParses(header string) bool {
	_, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc _(){\n"+header+"{}\n}", 0)
	return err == nil
}
```

- [ ] **Step 4: Pass the keyword from each caller**

In `parser/markup.go`:
- In `parseIfTail`, change the `scanToBlockBrace(p.src, p.i)` call to `scanToBlockBrace(p.src, p.i, "if")`.
- In `parseForMarkup`, change `scanToBlockBrace(p.src, p.i)` to `scanToBlockBrace(p.src, p.i, "for")`.
- In `parseSwitchMarkup`, change `scanToBlockBrace(p.src, p.i)` to `scanToBlockBrace(p.src, p.i, "switch")`.

In `parser/attrs.go`:
- In `parseCondAttrTail`, change `scanToBlockBrace(p.src, p.i)` to `scanToBlockBrace(p.src, p.i, "if")`.

(In every caller `p.i` already points just after the keyword at the call site — confirm by reading each; the `from` contract is "just after the keyword".)

- [ ] **Step 5: Run to verify pass + no regressions**

Run: `go test ./parser/ ./ast/ ./internal/corpus/`
Expected: PASS — the three new control-flow tests pass; all existing control-flow tests (plain `if`/`for`/`switch`, tagless switch, type switch, C-style for, `s[1:2]` case) stay green.

- [ ] **Step 6: Commit**

```bash
git add parser/boundary.go parser/markup.go parser/attrs.go parser/markup_test.go
git commit -m "fix(parser): full-fidelity control-flow body brace via go/parser test-parse (I2)"
```

---

### Task 6: Consolidated soundness regression suite, corpus + fuzz

**Files:**
- Create: `parser/soundness_test.go`
- Modify: `internal/corpus/testdata/examples_coverage.golden` (regenerate; expect unchanged or improved)
- Create: `parser/testdata/fuzz/FuzzParseFile/seed_apostrophe`

**Interfaces:**
- Consumes: `ParseFile`, `FuzzParseFile`, the examples-coverage tracker.

- [ ] **Step 1: Write the consolidated soundness test**

Create `parser/soundness_test.go`:

```go
package parser

import (
	"go/token"
	"testing"
)

// TestSoundnessNoDesync feeds inputs that previously desynced go/scanner over
// markup prose (C1) or mis-located the control-flow body brace (I2). Each must
// parse without error.
func TestSoundnessNoDesync(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"interp after apostrophe", "package p\ncomponent C(n int) { <p>Today's items: {n}</p> }"},
		{"goblock after apostrophe", "package p\ncomponent C(name string) { <p>it's {{ x := f(name) }}{x}</p> }"},
		{"class after apostrophe", "package p\ncomponent C(a string) { <p>don't <b class={a}>x</b></p> }"},
		{"if after apostrophe", "package p\ncomponent C(c bool) { <p>you're {n} <span>{ if c { <a/> } }</span></p> }"},
		{"spread after apostrophe", "package p\ncomponent C() { <p>Jack's <input {...attrs}/></p> }"},
		{"apostrophe in control body", "package p\ncomponent C(c bool) { { if c { <p>it's here</p> } } }"},
		{"apostrophe in nested element", "package p\ncomponent C() { <ul><li>can't</li><li>won't</li></ul> }"},
		{"multi-component apostrophe", "package p\ncomponent A() { <p>Jack's</p> }\ncomponent B() { <span>ok</span> }"},
		{"for range slice literal", "package p\ncomponent C() { <ul>{ for _, v := range []int{1,2} { <li>{v}</li> } }</ul> }"},
		{"for range map literal", "package p\ncomponent C() { { for k := range map[string]int{\"a\":1} { <i>{k}</i> } } }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseFile(token.NewFileSet(), "t.gsx", tc.src, 0); err != nil {
				t.Fatalf("parse error: %v", err)
			}
		})
	}
}

// TestSoundnessCleanErrors confirms genuinely malformed inputs still fail fast
// with a position, no panic, no hang.
func TestSoundnessCleanErrors(t *testing.T) {
	bad := []string{
		"package p\ncomponent C() { <p>hi</p>",                 // unterminated body
		"package p\ncomponent C(n int) { {n }",                 // unterminated interp/body
		"package p\ncomponent C() { <input {...attrs }",        // unterminated tag/body
	}
	for _, src := range bad {
		if _, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0); err == nil {
			t.Fatalf("expected error for %q, got nil", src)
		}
	}
}
```

- [ ] **Step 2: Run the soundness suite**

Run: `go test ./parser/ -run 'TestSoundness' -v`
Expected: PASS (all sub-cases parse / fail cleanly).

- [ ] **Step 3: Add an apostrophe fuzz seed**

Read `parser/testdata/fuzz/FuzzParseFile/seed_control_flow` to copy the exact `go test fuzz v1` format, then create `parser/testdata/fuzz/FuzzParseFile/seed_apostrophe` mirroring it, with the seed string value:

```
package p
component A(n int) { <p>Today's items: {n} — don't {{ x := f() }}<b class={x}>{x}</b></p> }
component B() { <ul>{ for _, v := range []int{1,2} { <li>it's {v}</li> } }</ul> }
```

- [ ] **Step 4: Regenerate the examples-coverage tracker and run everything**

Run: `go test ./internal/corpus/ -update`
Then: `go test ./...`
Expected: ALL PASS. Read `internal/corpus/testdata/examples_coverage.golden`: the 8 previously-`ok` examples stay `ok` (none should regress); 01/02/06/10 remain the DOCTYPE/raw-text gaps. If any previously-`ok` example regressed to an error, STOP and report it — that is a regression in this fix, not an accepted change.

- [ ] **Step 5: Short fuzz to confirm no new crashers**

Run: `go test ./parser/ -run xxx -fuzz FuzzParseFile -fuzztime 30s`
Expected: no crashers. Then stop the fuzz; `go test ./parser/` (seed corpus) passes.

- [ ] **Step 6: Commit**

```bash
git add parser/soundness_test.go parser/testdata/fuzz internal/corpus/testdata/examples_coverage.golden
git commit -m "test(parser): consolidated brace-soundness regression suite + fuzz seed"
```

---

## Self-Review

**1. Spec coverage** (against `2026-06-19-gsx-parser-brace-soundness.md`):
- A (goExprEnd/parenEnd scan from open) → Task 1. ✓
- B1 (component body in place) + the `parseMarkupUntilClose` helper → Task 2. ✓
- B2 (markup attribute in place) + `newSub` removal → Task 3. ✓
- B3 (file.go interleaved discovery, remove `topLevelComponentOffsets`) → Task 4. ✓
- C (scanToBlockBrace full fidelity via go/parser) → Task 5. ✓
- Test obligations (C1 variants, I2 variants, B3 multi-component, negative/clean-error, fuzz, coverage no-regression) → Tasks 1–6, consolidated in Task 6. ✓
- `scanToCaseColon` unchanged (already correct) → not touched. ✓

**2. Placeholder scan:** No TBD/TODO/"handle edge cases". Task 3's `newSub` removal is conditional-on-evidence (`grep`/`gopls`), which is a concrete decision procedure, not a placeholder. Task 6's coverage regen is a deterministic `-update` + an explicit no-regression check.

**3. Type/contract consistency:**
- `parseMarkupUntilClose(what string) ([]ast.Markup, error)` defined in Task 2, reused by `parseControlBody` (Task 2), component body (Task 2), and markup attribute (Task 3) — identical signature throughout. ✓
- `scanToBlockBrace(src string, from int, keyword string)` — the new 3-arg form is defined in Task 5 and all four callers (`parseIfTail`, `parseForMarkup`, `parseSwitchMarkup`, `parseCondAttrTail`) are updated in the same task, so no caller is left on the old 2-arg form. ✓
- `goExprEnd`/`parenEnd` keep their existing 2-arg signatures (Task 1) — callers untouched. ✓
- `nextTopLevelComponent(src string, from int) (int, bool)` replaces `topLevelComponentOffsets`; the only caller is `ParseFile`, updated in the same task (Task 4). ✓

---

## Execution Notes for the Controller

- Tasks are sequential: 1 → 2 → 3 → 4 → 5 → 6. Task 2 introduces `parseMarkupUntilClose` (used by Tasks 2 and 3). Task 4 depends on Task 2 (component body consumed in place so `cursor = p.i` is correct). Task 5 is independent of B but is sequenced last among fixes so the I2 tests run against the fully-sound parser.
- After each task, the FULL existing suite (`go test ./parser/ ./ast/ ./internal/corpus/`) must stay green — this fix is behavior-preserving except for the previously-failing inputs.
- Model guidance: Task 1 (mechanical, two near-identical functions) and Task 6 (test + regen) → cheap model. Tasks 2, 3, 4, 5 carry recursive-descent / file-structure judgment → standard model. Final whole-branch review → most capable model.
- This branch (`fix/parser-brace-soundness`) lands on `main` after review. No rebase needed (Part 2 already merged).
