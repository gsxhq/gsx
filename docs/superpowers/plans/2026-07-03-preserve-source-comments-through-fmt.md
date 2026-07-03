# Preserve source comments through `gsx fmt` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retain gsx source comments (tag-interior `//` `/* */`, and braced `{/* */}` `{// }` in both attribute and content position) through the formatter instead of dropping them at parse time.

**Architecture:** Two new AST nodes — `CommentAttr` (attribute position, always printed bare) and `Comment` (content position, printed braced). The parser emits them instead of skipping; codegen/LSP treat them as no-ops (never rendered); the printer re-emits them with own-line/trailing placement fidelity. Bare `//` in text content stays literal text (unchanged).

**Tech Stack:** Go (stdlib-only runtime; parser hand-written recursive descent; printer uses an internal `pretty.Doc` model). Sibling grammars: tree-sitter (JS), TextMate (JSON), CodeMirror/Shiki.

## Global Constraints

- Runtime/root package stays **standard-library only**; parser/printer/gen may use `golang.org/x/tools`.
- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (1.26.1) — a different minor re-introduces gofmt drift.
- Every syntax/codegen change ships a **txtar corpus case per context**; regenerate goldens with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- Don't hand-edit `.x.go` or golden files — regenerate.
- Before merging: `make check` (inner loop) then `make ci` (authoritative).
- Work directly on `main` in each repo (user directive; small change).
- Run `make lint` after any syntax change.

**Invariant (must never break):** bare `//` and `/* */` in **text/child content** are literal text and render verbatim. `TestContentIsLiteral` (`parser/markup_test.go:336`) must stay green.

---

### Task 1: `CommentAttr` — attribute-position comments retained

**Files:**
- Modify: `ast/ast.go` (add `CommentAttr` after the other `Attr` types, ~line 498)
- Modify: `parser/markup.go` (replace `skipTagComment` usage in `parseAttrs:494`; add `parseTagComment`)
- Modify: `parser/attrs.go` (`parseAttrsUntilBrace:451`)
- Modify: codegen attr walker + LSP attr walker (no-op cases — find via step 3)
- Test: `parser/markup_test.go` (flip `TestTagTrailingLineComment`, `TestTagOwnLineComment`, `TestTagBlockComment`; add braced-in-attr test)

**Interfaces:**
- Produces: `type CommentAttr struct { span; Text string; Block bool; Trailing bool }` implementing `ast.Attr`. `Text` = inner text, delimiters and any wrapping `{ }` stripped and space-trimmed. `Block` = true for `/* */`, false for `//`. `Trailing` = true when the comment sat on the same source line as the previous attribute.
- Produces: `func (p *parser) parseTagComment() (*ast.CommentAttr, bool, error)` — at cursor, recognizes bare `//`/`/*` or a comment-only `{ … }`; returns `(node, true, nil)` on a comment, `(nil, false, nil)` otherwise, `(nil, false, err)` on unterminated `/*`. Does NOT set `Trailing` (caller sets it).

- [ ] **Step 1: Write the failing tests** — flip the three drop-asserting tests and add a braced-in-attr test in `parser/markup_test.go`.

```go
func TestTagTrailingLineComment(t *testing.T) {
	// `<div id={x} // trailing\n class="y">` → id, CommentAttr(trailing), class
	p := testParser("<div id={x} // trailing\n class=\"y\"></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 3 {
		t.Fatalf("got %d attrs, want 3: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id}", el.Attrs[0])
	}
	c, ok := el.Attrs[1].(*ast.CommentAttr)
	if !ok || c.Block || c.Text != "trailing" || !c.Trailing {
		t.Fatalf("attr1 = %#v, want CommentAttr{//, trailing, trailing=true}", el.Attrs[1])
	}
	if a, ok := el.Attrs[2].(*ast.StaticAttr); !ok || a.Name != "class" {
		t.Fatalf("attr2 = %#v, want StaticAttr{class}", el.Attrs[2])
	}
}

func TestTagOwnLineComment(t *testing.T) {
	p := testParser("<div\n // own line\n id={x}></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %#v", len(el.Attrs), el.Attrs)
	}
	c, ok := el.Attrs[0].(*ast.CommentAttr)
	if !ok || c.Block || c.Text != "own line" || c.Trailing {
		t.Fatalf("attr0 = %#v, want CommentAttr{//, own line, trailing=false}", el.Attrs[0])
	}
	if a, ok := el.Attrs[1].(*ast.ExprAttr); !ok || a.Name != "id" {
		t.Fatalf("attr1 = %#v, want ExprAttr{id}", el.Attrs[1])
	}
}

func TestTagBlockComment(t *testing.T) {
	p := testParser("<div /* note */ id={x}></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %#v", len(el.Attrs), el.Attrs)
	}
	c, ok := el.Attrs[0].(*ast.CommentAttr)
	if !ok || !c.Block || c.Text != "note" {
		t.Fatalf("attr0 = %#v, want CommentAttr{/* */, note}", el.Attrs[0])
	}
}

func TestTagBracedCommentInAttrs(t *testing.T) {
	// Braced comment-only {…} is legal in attr position; parses to CommentAttr.
	p := testParser("<div {/* braced */} id={x} {// line\n} title=\"t\"></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 4 {
		t.Fatalf("got %d attrs, want 4: %#v", len(el.Attrs), el.Attrs)
	}
	if c, ok := el.Attrs[0].(*ast.CommentAttr); !ok || !c.Block || c.Text != "braced" {
		t.Fatalf("attr0 = %#v, want CommentAttr{/* */, braced}", el.Attrs[0])
	}
	if c, ok := el.Attrs[2].(*ast.CommentAttr); !ok || c.Block || c.Text != "line" {
		t.Fatalf("attr2 = %#v, want CommentAttr{//, line}", el.Attrs[2])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./parser -run 'TestTag' -v`
Expected: FAIL — `CommentAttr` undefined / attr counts wrong.

- [ ] **Step 3: Add the `CommentAttr` AST node** in `ast/ast.go` after `OrderedAttrsAttr`:

```go
// CommentAttr is a source-only comment in an element's attribute list: bare
// `// text` / `/* text */`, or a braced comment-only `{/* */}` / `{// }`. It is
// never rendered (codegen ignores it); the formatter preserves it. Braced forms
// canonicalize to bare on output, so no "braced" flag is retained.
type CommentAttr struct {
	span
	Text     string // inner text, delimiters and wrapping braces stripped, trimmed
	Block    bool   // true = /* */, false = //
	Trailing bool   // true = same source line as the previous attribute
}

func (*CommentAttr) attrNode() {}
```

- [ ] **Step 4: Add `parseTagComment`** in `parser/markup.go` (below `skipTagComment`). It reuses `commentOnly` + `goExprEnd` for the braced case:

```go
// parseTagComment recognizes a tag-interior comment at the cursor: bare `//` /
// `/* */`, or a comment-only `{ … }`. Returns (node, true, nil) if consumed.
// Trailing is left false; the caller sets it from the preceding whitespace.
func (p *parser) parseTagComment() (*ast.CommentAttr, bool, error) {
	start := p.i
	if p.at("/*") {
		p.i += 2
		for !p.eof() {
			if p.at("*/") {
				text := strings.TrimSpace(p.src[start+2 : p.i])
				p.i += 2
				n := &ast.CommentAttr{Text: text, Block: true}
				ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
				return n, true, nil
			}
			p.i++
		}
		return nil, false, p.errorf(p.posAt(start), "unterminated block comment")
	}
	if p.at("//") {
		p.i += 2
		for !p.eof() && p.src[p.i] != '\n' {
			p.i++
		}
		text := strings.TrimSpace(p.src[start+2 : p.i])
		n := &ast.CommentAttr{Text: text, Block: false}
		ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
		return n, true, nil
	}
	if p.peek() == '{' {
		end, ok := goExprEnd(p.src, p.i)
		if !ok {
			return nil, false, nil
		}
		inner := p.src[p.i+1 : end]
		if !commentOnly(inner) {
			return nil, false, nil
		}
		trimmed := strings.TrimSpace(inner)
		block := strings.HasPrefix(trimmed, "/*")
		var text string
		if block {
			text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "/*"), "*/"))
		} else {
			text = strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
		}
		p.i = end + 1
		n := &ast.CommentAttr{Text: text, Block: block}
		ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
		return n, true, nil
	}
	return nil, false, nil
}
```

- [ ] **Step 5: Wire both attr loops** to call `parseTagComment` and set `Trailing`. In `parser/markup.go` `parseAttrs` (replace the `skipTagComment` block at :494):

```go
func (p *parser) parseAttrs() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		wsStart := p.i
		p.skipSpace()
		if p.eof() {
			return nil, p.errorf(p.pos(), "unexpected EOF in attributes")
		}
		if p.peek() == '>' || p.at("/>") {
			return attrs, nil
		}
		if c, ok, err := p.parseTagComment(); err != nil {
			return nil, err
		} else if ok {
			c.Trailing = len(attrs) > 0 && !strings.ContainsRune(p.src[wsStart:p.i], '\n')
			attrs = append(attrs, c)
			continue
		}
		a, err := p.parseSingleAttr()
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, a)
	}
}
```

Apply the identical shape to `parser/attrs.go` `parseAttrsUntilBrace` (:440) — same `wsStart`/`parseTagComment`/`Trailing` block, keeping its `}` terminator check.

- [ ] **Step 6: Add codegen + LSP no-op cases.** Find the attr type switches:

Run: `grep -rn "ast.OrderedAttrsAttr" gen/ lsp/ internal/ | grep -v _test`
For each exhaustive `switch a := attr.(type)` over `ast.Attr` in codegen and LSP, add `case *ast.CommentAttr:` that does nothing (codegen: emit nothing; LSP: `continue`/skip). If a switch has a `default` that panics on unknown attrs, the explicit no-op case is required.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./parser -run 'TestTag|TestContentIsLiteral' -v`
Expected: PASS (including the unchanged `TestContentIsLiteral`).

- [ ] **Step 8: Build everything green**

Run: `go build ./... && go test ./parser ./gen ./lsp 2>&1 | tail -20`
Expected: no build errors; existing suites pass (codegen ignores the new node).

- [ ] **Step 9: Commit**

```bash
git add ast/ast.go parser/markup.go parser/attrs.go parser/markup_test.go gen lsp
git commit -m "feat(parser): retain attribute-position comments as CommentAttr

Bare // /* */ and braced {/* */} {// } in an element's attribute list (and
{ if COND { attrs } } blocks) now parse to ast.CommentAttr instead of being
dropped. Codegen/LSP treat it as a no-op; nothing renders. Sets Trailing from
the preceding whitespace. Flips the three drop-asserting parser tests.

Claude-Session: https://claude.ai/code/session_015tYp5dghxdoVJzYqVF3kDz"
```

---

### Task 2: `Comment` — content-position comments retained

**Files:**
- Modify: `ast/ast.go` (add `Comment` after `HTMLComment`, ~line 209)
- Modify: `parser/markup.go` (`parseBraceNode:459`, `skipBracedComment`)
- Modify: codegen markup walker + LSP markup walker (no-op cases)
- Test: `parser/markup_test.go` (flip `TestBracedContentComment`, `TestBracedLineComment`)

**Interfaces:**
- Produces: `type Comment struct { span; Text string; Block bool }` implementing `ast.Markup`. Braced content comment `{/* */}` / `{// }`; `Text` = inner comment text trimmed; `Block` distinguishes `/* */` from `//`.

- [ ] **Step 1: Write the failing tests** — flip the two braced content tests to assert a `Comment` node survives (and the sibling `Text` remains):

```go
func TestBracedContentComment(t *testing.T) {
	src := `<div>{/* a content comment with <tags> and a } brace */}keep</div>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 2 {
		t.Fatalf("got %d children, want 2: %#v", len(el.Children), el.Children)
	}
	c, ok := el.Children[0].(*ast.Comment)
	if !ok || !c.Block {
		t.Fatalf("child0 = %#v, want *ast.Comment block", el.Children[0])
	}
	if txt, ok := el.Children[1].(*ast.Text); !ok || txt.Value != "keep" {
		t.Fatalf("child1 = %#v, want Text{keep}", el.Children[1])
	}
}

func TestBracedLineComment(t *testing.T) {
	src := "<div>{// just a line comment\n}x</div>"
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 2 {
		t.Fatalf("got %d children, want 2: %#v", len(el.Children), el.Children)
	}
	c, ok := el.Children[0].(*ast.Comment)
	if !ok || c.Block || c.Text != "just a line comment" {
		t.Fatalf("child0 = %#v, want *ast.Comment line 'just a line comment'", el.Children[0])
	}
	if txt, ok := el.Children[1].(*ast.Text); !ok || txt.Value != "x" {
		t.Fatalf("child1 = %#v, want Text{x}", el.Children[1])
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./parser -run 'TestBraced' -v`
Expected: FAIL — `ast.Comment` undefined.

- [ ] **Step 3: Add the `Comment` AST node** in `ast/ast.go` after `HTMLComment`:

```go
// Comment is a source-only content comment: `{/* text */}` or `{// text }`
// between child nodes. Unlike HTMLComment it is NOT rendered — the formatter
// preserves it, codegen drops it. (Bare // in text content is literal Text.)
type Comment struct {
	span
	Text  string
	Block bool
}

func (*Comment) markupNode() {}
```

- [ ] **Step 4: Emit the node from `parseBraceNode`.** Replace `skipBracedComment` with a variant that builds the node. In `parser/markup.go`, change `parseBraceNode` (:464):

```go
	if c, ok, err := p.parseBracedComment(); err != nil {
		return nil, false, err
	} else if ok {
		return c, false, nil
	}
```

and add `parseBracedComment` (adapting `skipBracedComment`):

```go
// parseBracedComment builds a *ast.Comment when the `{…}` at the cursor is
// comment-only, advancing past `}`. Returns (nil, false, nil) otherwise.
func (p *parser) parseBracedComment() (*ast.Comment, bool, error) {
	if p.peek() != '{' {
		return nil, false, nil
	}
	start := p.i
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, false, nil
	}
	inner := p.src[p.i+1 : end]
	if !commentOnly(inner) {
		return nil, false, nil
	}
	trimmed := strings.TrimSpace(inner)
	block := strings.HasPrefix(trimmed, "/*")
	var text string
	if block {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "/*"), "*/"))
	} else {
		text = strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
	}
	p.i = end + 1
	n := &ast.Comment{Text: text, Block: block}
	ast.SetSpan(n, p.posAt(start), p.posAt(p.i))
	return n, true, nil
}
```

Delete the now-unused `skipBracedComment` (confirm no other caller: `grep -rn skipBracedComment parser/`).

- [ ] **Step 5: Add codegen + LSP no-op cases** for `*ast.Comment` in the markup type switches (same discovery: `grep -rn "ast.HTMLComment" gen/ lsp/ internal/ | grep -v _test`). Codegen emits nothing; LSP skips.

- [ ] **Step 6: Run tests to verify pass**

Run: `go test ./parser -run 'TestBraced|TestContentIsLiteral' -v`
Expected: PASS.

- [ ] **Step 7: Build + suites green**

Run: `go build ./... && go test ./parser ./gen ./lsp 2>&1 | tail -20`
Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add ast/ast.go parser/markup.go parser/markup_test.go gen lsp
git commit -m "feat(parser): retain content-position comments as ast.Comment

{/* */} and {// } between child nodes parse to ast.Comment instead of being
dropped. Not rendered (codegen no-op); the formatter will preserve them.

Claude-Session: https://claude.ai/code/session_015tYp5dghxdoVJzYqVF3kDz"
```

---

### Task 3: Printer — re-emit comments faithfully

**Files:**
- Modify: `internal/printer/printer.go` (`element:177-181`, `attrDoc:262`, `markup:412`, `writeAttrInline`, cond-attr chain docs `condAttrChainDoc`/`writeCondAttrChain`)
- Test: `internal/printer/printer_test.go`

**Interfaces:**
- Consumes: `ast.CommentAttr{Text, Block, Trailing}`, `ast.Comment{Text, Block}`.

- [ ] **Step 1: Write failing printer tests** in `internal/printer/printer_test.go`. Use the existing test helper (find how other tests format — likely `mustFormat(t, src)` or `Fprint`; mirror the nearest existing printer test). Cases:

```go
func TestFmtAttrOwnLineComment(t *testing.T) {
	src := "package v\ncomponent C() {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t// hello\n\t\tid={name}\n\t/>\n}\n"
	got := mustFormat(t, src)
	want := "package v\n\ncomponent C() {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t// hello\n\t\tid={name}\n\t/>\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFmtAttrTrailingComment(t *testing.T) {
	// trailing // stays on the attr's line
	src := "package v\ncomponent C() {\n\t<input id={name} // note\n\t\tvalue=\"x\" />\n}\n"
	got := mustFormat(t, src)
	if !strings.Contains(got, "id={ name } // note") {
		t.Fatalf("trailing comment not glued to attr:\n%s", got)
	}
}

func TestFmtAttrBlockCommentInline(t *testing.T) {
	src := "package v\ncomponent C() {\n\t<div /* note */ id={x}></div>\n}\n"
	got := mustFormat(t, src)
	if !strings.Contains(got, "<div /* note */ id={ x }>") {
		t.Fatalf("block comment did not stay inline:\n%s", got)
	}
}

func TestFmtAttrBracedCommentCanonicalizesToBare(t *testing.T) {
	src := "package v\ncomponent C() {\n\t<div {/* note */} id={x}></div>\n}\n"
	got := mustFormat(t, src)
	if !strings.Contains(got, "/* note */") || strings.Contains(got, "{/* note */}") {
		t.Fatalf("braced attr comment not canonicalized to bare:\n%s", got)
	}
}

func TestFmtContentComment(t *testing.T) {
	src := "package v\ncomponent C() {\n\t<p>{/* hidden */}Visible</p>\n}\n"
	got := mustFormat(t, src)
	if !strings.Contains(got, "{/* hidden */}") {
		t.Fatalf("content comment dropped:\n%s", got)
	}
}

func TestFmtCommentsIdempotent(t *testing.T) {
	src := "package v\ncomponent C() {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t// a\n\t\tid={name} // b\n\t\t/* c */\n\t\tvalue=\"x\"\n\t/>\n\t<p>{/* d */}Hi</p>\n}\n"
	once := mustFormat(t, src)
	twice := mustFormat(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/printer -run 'TestFmt.*Comment' -v`
Expected: FAIL (comments dropped / unknown node panic).

- [ ] **Step 3: Handle `CommentAttr` in the element attr loop.** In `internal/printer/printer.go` `element` (:178-181), replace the loop:

```go
	attrs := make([]pretty.Doc, 0, len(e.Attrs)*2)
	for _, a := range e.Attrs {
		if c, ok := a.(*ast.CommentAttr); ok {
			sep := pretty.Line
			if c.Trailing {
				sep = pretty.Text(" ")
			}
			attrs = append(attrs, sep, p.attrDoc(a))
			if !c.Block {
				attrs = append(attrs, pretty.BreakParent) // // must not be flat
			}
			continue
		}
		attrs = append(attrs, pretty.Line, p.attrDoc(a))
	}
```

- [ ] **Step 4: Render the `CommentAttr` text.** Add to `writeAttrInline` (so `attrInline`/`attrDoc` default path emits it):

```go
	case *ast.CommentAttr:
		if v.Block {
			b.WriteString("/* ")
			b.WriteString(v.Text)
			b.WriteString(" */")
		} else {
			b.WriteString("// ")
			b.WriteString(v.Text)
		}
```

- [ ] **Step 5: Handle `Comment` in the markup switch.** In `markup` (:428, next to `HTMLComment`):

```go
	case *ast.Comment:
		if v.Block {
			return pretty.Concat(pretty.Text("{/* "), pretty.Text(v.Text), pretty.Text(" */}"))
		}
		return pretty.Concat(pretty.Text("{// "), pretty.Text(v.Text), pretty.Text(" }"))
```

Note: the `{// text }` line form is legal on one line here because the printer controls layout and there is no trailing source to be commented out; if a later idempotency failure shows the `}` being swallowed, switch the line form to `{/* text */}` canonicalization instead (decide during step 7 by observing the test).

- [ ] **Step 6: Handle comments inside `{ if COND { attrs } }` bodies.** Inspect `condAttrChainDoc` and `writeCondAttrChain`; a `CondAttr`'s `Then`/`Else` are `[]ast.Attr` that may now contain `*ast.CommentAttr`. Apply the same own-line/trailing/BreakParent logic used in step 3 where those bodies are laid out, and the same `writeAttrInline` case covers the inline string path.

- [ ] **Step 7: Run tests to verify pass**

Run: `go test ./internal/printer -run 'TestFmt.*Comment' -v`
Expected: PASS, including idempotency. If `TestFmtContentComment` line-form fails, apply the step-5 note.

- [ ] **Step 8: Full printer suite + gofmt/gsx fmt self-check**

Run: `go test ./internal/printer && gofmt -l internal/printer/`
Expected: pass; no gofmt drift.

- [ ] **Step 9: Commit**

```bash
git add internal/printer
git commit -m "feat(printer): preserve source comments through gsx fmt

CommentAttr prints bare (// text, /* text */) with own-line vs trailing
placement fidelity; // forces the tag group to break, /* */ may stay inline.
Braced attr comments canonicalize to bare. ast.Comment prints {/* */} / {// }
in content position. Idempotent.

Claude-Session: https://claude.ai/code/session_015tYp5dghxdoVJzYqVF3kDz"
```

---

### Task 4: Corpus cases — pin generate/render unaffected (per-context)

**Files:**
- Create: `internal/corpus/testdata/cases/comments/attr_comment.txtar`
- Create: `internal/corpus/testdata/cases/comments/cond_attr_comment.txtar`
- Create: `internal/corpus/testdata/cases/comments/content_comment.txtar`

- [ ] **Step 1: Inspect an existing case** for the exact txtar section layout (`input.gsx`, `invoke`, `generated.x.go.golden`, `render.golden`, `coverage.golden` manifest):

Run: `ls internal/corpus/testdata/cases/elements/ && sed -n '1,40p' internal/corpus/testdata/cases/elements/html_comment.txtar`

- [ ] **Step 2: Write the three input `.gsx` sections** (goldens filled by `-update`). `attr_comment.txtar` input:

```gsx
package views

component C(name string) {
	<input
		type="checkbox"
		// own-line note
		id={name} // trailing note
		/* block note */
		value="x"
	/>
}
```

`cond_attr_comment.txtar` input:

```gsx
package views

component C(on bool) {
	<input
		{ if on {
			// enabled path
			checked
		} }
	/>
}
```

`content_comment.txtar` input:

```gsx
package views

component C() {
	<p>{/* hidden */}Visible{// also hidden
	}</p>
}
```

Each also needs an `-- invoke --` line (e.g. `C(CProps{Name: "n"})`, `C(CProps{On: true})`, `C(CProps{})`) matching the corpus harness — mirror the format seen in step 1.

- [ ] **Step 3: Generate goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Expected: writes `generated.x.go.golden` + `render.golden` for the three cases and updates `coverage.golden`.

- [ ] **Step 4: Verify the render goldens contain NO comment text** (comments must not render). Inspect the three `render.golden` blocks:

Run: `grep -l "note\|hidden" internal/corpus/testdata/cases/comments/*.txtar`
Expected: matches only in the `input.gsx` sections, never in `render.golden` sections (open each to confirm the rendered HTML omits comment text).

- [ ] **Step 5: Verify without `-update`**

Run: `go test ./internal/corpus -run TestCorpus`
Expected: PASS (goldens stable).

- [ ] **Step 6: Commit**

```bash
git add internal/corpus/testdata/cases/comments internal/corpus/testdata/cases/coverage.golden
git commit -m "test(corpus): comments in attr/cond-attr/content position (never render)

Claude-Session: https://claude.ai/code/session_015tYp5dghxdoVJzYqVF3kDz"
```

---

### Task 5: Docs — rewrite comments guide + runnable examples

**Files:**
- Modify: `docs/guide/syntax/comments.md`
- Create: `examples/216-tag-comments.txtar`, `examples/217-content-comments-preserved.txtar` (routed to the comments page)
- Regenerate: `docs/guide/syntax/_generated/comments/*.md` via `make examples`

- [ ] **Step 1: Add routed examples.** Mirror `examples/215-html-comments.txtar`. `examples/216-tag-comments.txtar`:

```
-- doc --
name: Attribute comments
summary: // and /* */ (and braced {/* */}) inside a tag are source-only and survive gsx fmt.
category: Elements
page: comments
pageOrder: 20
-- input.gsx --
package views

component Toggle(name string) {
	<input
		type="checkbox"
		// source-only note, never rendered
		id={name}
	/>
}
-- invoke --
Toggle(ToggleProps{Name: "agree"})
-- render.golden --
<input type="checkbox" id="agree"/>
```

`examples/217-content-comments-preserved.txtar`:

```
-- doc --
name: Content comments
summary: {/* … */} content comments are dropped from output but preserved by gsx fmt.
category: Elements
page: comments
pageOrder: 30
-- input.gsx --
package views

component Note() {
	<p>{/* hidden note */}Visible text</p>
}
-- invoke --
Note(NoteProps{})
-- render.golden --
<p>Visible text</p>
```

- [ ] **Step 2: Regenerate**

Run: `make examples`
Expected: creates `docs/guide/syntax/_generated/comments/020-*.md` and `030-*.md`.

- [ ] **Step 3: Rewrite `docs/guide/syntax/comments.md`** — fix the wrong "can never appear inside element markup at all" sentence, add the position table (v-pre wrapped where `{{ }}` appears), document tag-interior comments + that all source comments survive `fmt`, state the text-content-`//`-is-literal rule, and `@include` the two new generated partials.

Position table to embed (wrap the whole prose section that contains `{{`/`}}` in `::: v-pre`):

```markdown
| Position | `//` `/* */` bare | `{// }` `{/* */}` braced | `<!-- -->` |
|---|---|---|---|
| Inside a tag `<… >` | comment (survives `fmt`) | comment (survives `fmt`, printed bare) | n/a |
| Text / child content | literal text (renders) | content comment (survives `fmt`) | renders |
```

- [ ] **Step 4: Verify examples drift + build**

Run: `make check`
Expected: pass (examples-drift check green — goldens match).

- [ ] **Step 5: Commit**

```bash
git add docs/guide/syntax/comments.md docs/guide/syntax/_generated/comments examples/216-tag-comments.txtar examples/217-content-comments-preserved.txtar docs/guide/examples.md docs/examples.json playground/server/examples.json
git commit -m "docs: document source comments and that gsx fmt preserves them

Claude-Session: https://claude.ai/code/session_015tYp5dghxdoVJzYqVF3kDz"
```

- [ ] **Step 6: Mirror to gsxhq.github.io** — copy the updated `comments.md` and generated partials into `../gsxhq.github.io/guide/syntax/`, per the two-repo docs flow. Commit there on `main`.

---

### Task 6: tree-sitter-gsx — highlight source comments

**Files (repo `../tree-sitter-gsx`):**
- Modify: `grammar.js`
- Modify: `queries/highlights.scm`
- Modify: `test/corpus/*` (grammar tests)
- Sync: `test/examples/`

- [ ] **Step 1: Inspect current comment handling**

Run: `grep -n "comment" ../tree-sitter-gsx/grammar.js ../tree-sitter-gsx/queries/highlights.scm`
Determine whether tag-interior `//` `/* */` and braced `{/* */}` are recognized today.

- [ ] **Step 2: Add a `comment` rule** for tag-interior `//` and `/* */` in the attribute-list context (and confirm braced comments fall under the existing Go `{ }` injection or add a rule). Follow the grammar's existing token style.

- [ ] **Step 3: Map to `@comment`** in `queries/highlights.scm`.

- [ ] **Step 4: Add a grammar test** in `test/corpus/` with an element carrying `//`, `/* */`, and `{/* */}` attribute comments and the expected parse tree.

- [ ] **Step 5: Verify**

Run: `cd ../tree-sitter-gsx && npx tree-sitter generate && npx tree-sitter test`
Expected: PASS. Also parse a sample: `npx tree-sitter parse -q test/examples/<a file with comments>`.

- [ ] **Step 6: Commit on `main`** in `../tree-sitter-gsx`.

---

### Task 7: vscode-gsx — TextMate comment scopes

**Files (repo `../vscode-gsx`):**
- Modify: the TextMate grammar JSON (find via `grep -rln tmLanguage ../vscode-gsx`)
- Modify: grammar test fixtures

- [ ] **Step 1: Inspect** the grammar's attribute-context patterns and existing comment scopes (`grep -n comment ../vscode-gsx/syntaxes/*.json`).

- [ ] **Step 2: Add patterns** matching tag-interior `//` (`comment.line.double-slash`) and `/* */` (`comment.block`) inside the element/attribute context, plus braced `{/* */}` / `{// }`.

- [ ] **Step 3: Add/extend a grammar test** (the repo's scope-assertion fixture) covering the three forms.

- [ ] **Step 4: Verify** with the repo's grammar test command (`npm test` or the documented scope test).

- [ ] **Step 5: Commit on `main`.** Version bump / release only if the repo's tag-gated release is desired (per `vscode-gsx-published` memory: bump `package.json` → push `vX.Y.Z`). Do NOT release without confirmation.

---

### Task 8: Docs highlight + playground CodeMirror

**Files (repo `../gsxhq.github.io`, local dir `website`):**
- Modify: VitePress/Shiki gsx highlight grammar
- Modify: playground CodeMirror gsx mode/stream parser

- [ ] **Step 1: Locate** the highlight definitions:

Run: `grep -rln "comment\|gsx" ../gsxhq.github.io/.vitepress ../gsxhq.github.io/playground 2>/dev/null | head`

- [ ] **Step 2: Shiki/VitePress** — if a TextMate grammar is embedded for gsx, apply the Task 7 patterns; if it reuses tree-sitter-gsx, ensure it's synced.

- [ ] **Step 3: Playground CodeMirror** — extend the gsx stream/lezer parser so tag-interior `//` `/* */` and braced `{/* */}` tokenize as `comment`.

- [ ] **Step 4: Verify locally** — build the docs / run the playground dev server and eyeball a `.gsx` snippet with all comment forms highlighting as comments (heed the `gsx-docs-local-verify-gotchas` memory: rebuild `gsx.wasm` + cache-bust; `--link` symlink 404s).

- [ ] **Step 5: Commit on `main`** in `../gsxhq.github.io`.

---

## Final verification

- [ ] `make ci` in `gsx` is green (build/vet/test both modules, examples drift, gofmt + gsx fmt).
- [ ] `make lint` green.
- [ ] Round-trip check: write the user's original `<input>` example to a scratch `.gsx`, run `go run ./cmd/gsx fmt`, confirm the `//` comments survive.
- [ ] Siblings build/test green on `main`.

## Self-review notes

- Spec coverage: AST (T1,T2) · parser both positions + braced-in-attr (T1,T2) · printer own-line/trailing/block-inline/braced-canonicalize/content/cond-body (T3) · codegen+LSP no-op (T1,T2) · corpus per-context (T4) · docs+examples+mirror (T5) · siblings tree-sitter/vscode/docs+playground (T6,T7,T8) · invariant `TestContentIsLiteral` guarded (T1). All spec sections mapped.
- Type consistency: `CommentAttr{Text,Block,Trailing}` and `Comment{Text,Block}` used identically across T1–T4; `parseTagComment`/`parseBracedComment` signatures fixed in T1/T2 interfaces.
- Open runtime decision flagged inline: content line-form `{// }` single-line vs canonicalize (T3 step 5 note) — resolved by observing the idempotency test.
