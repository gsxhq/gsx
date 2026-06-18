# GSX Markup Comments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add comment support to the GSX parser — tag-interior `//`/`/* */` are stripped, element content is literal, and `{/* … */}` braced comment-only blocks in content position are also stripped.

**Architecture:** Two new parser methods (`skipTagComment`, `skipBracedComment`) + one pure helper (`commentOnly`) wire into existing `parseAttrs`, `parseChildren`, and `parseNodesUntilEOF`. No new AST nodes — comments produce nothing. The `commentOnly` helper reuses `go/scanner` exactly as `boundary.go` does.

**Tech Stack:** Go 1.26.1, stdlib only (`go/scanner`, `go/token`, `fmt`). Module: `github.com/gsxhq/gsx`.

## Global Constraints

- stdlib only — no third-party dependencies
- Go module: `github.com/gsxhq/gsx`, go 1.26.1
- All new names unexported (lowercase) unless serialization is needed
- `go test ./...` must pass GREEN after every task
- Do NOT modify `parseText` — element content is always literal
- Key files to modify: `parser/markup.go`, `parser/markup_test.go`, `examples/12_children_attrs.gsx`, `docs/superpowers/specs/2026-06-18-gsx-templating-design.md`, `internal/corpus/testdata/examples_coverage.golden`
- Report written to `.git/sdd/markup-comments-report.md`

---

### Task 1: Tag-interior comments — `skipTagComment` + `parseAttrs` wiring

**Files:**
- Modify: `/Users/jackieli/personal/gox/parser/markup.go`
- Test: `/Users/jackieli/personal/gox/parser/markup_test.go`

**Interfaces:**
- Produces: `func (p *parser) skipTagComment() (skipped bool, err error)` — called inside the `parseAttrs` loop right after `p.skipSpace()` and after the `>`/`/>` return check.

- [ ] **Step 1: Write failing tests**

Add to `/Users/jackieli/personal/gox/parser/markup_test.go` (before `var _ = ast.Text{}`):

```go
func TestTagTrailingLineComment(t *testing.T) {
	// `<div id={x} // trailing\n class="y">` → div with exactly two attrs (ExprAttr id, StaticAttr class)
	p := testParser("<div id={x} // trailing\n class=\"y\">rest")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
	if a, ok := el.Attrs[1].(*ast.StaticAttr); !ok || a.Name != "class" || a.Value != "y" {
		t.Fatalf("attr1 = %#v, want StaticAttr{class, y}", el.Attrs[1])
	}
}

func TestTagOwnLineComment(t *testing.T) {
	// `<div\n // own line\n id={x}>` → div with one attr id
	p := testParser("<div\n // own line\n id={x}>rest")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
}

func TestTagBlockComment(t *testing.T) {
	// `<div /* note */ id={x}>` → div with one attr id
	p := testParser("<div /* note */ id={x}>rest")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
}

func TestUnterminatedTagBlockComment(t *testing.T) {
	// `<div /* oops>` → parseElement returns an error mentioning "unterminated block comment"
	p := testParser("<div /* oops>")
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for unterminated block comment, got nil")
	}
	if !strings.Contains(err.Error(), "unterminated block comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
cd /Users/jackieli/personal/gox && go test ./parser/ -run "TestTagTrailingLineComment|TestTagOwnLineComment|TestTagBlockComment|TestUnterminatedTagBlockComment" -v 2>&1 | head -40
```

Expected: FAIL — `parseAttrs` does not strip comments yet.

- [ ] **Step 3: Add `skipTagComment` and wire it into `parseAttrs`**

In `/Users/jackieli/personal/gox/parser/markup.go`:

First, add `"fmt"` is already imported. Verify imports are fine as-is.

Add `skipTagComment` after the `isAttrNameByte` function (around line 47):

```go
// skipTagComment skips one // or /* */ comment in tag-interior position.
// Returns (true, nil) if a comment was consumed, (false, nil) if not at a comment,
// or (false, error) for an unterminated block comment.
func (p *parser) skipTagComment() (bool, error) {
	if p.at("/*") {
		start := p.i
		p.i += 2 // past '/*'
		for !p.eof() {
			if p.at("*/") {
				p.i += 2 // past '*/'
				return true, nil
			}
			p.i++
		}
		// unterminated
		startPos := p.posAt(start)
		resolvedPos := p.file.Position(startPos)
		return false, fmt.Errorf("%d:%d: unterminated block comment", resolvedPos.Line, resolvedPos.Column)
	}
	if p.at("//") {
		p.i += 2 // past '//'
		for !p.eof() && p.src[p.i] != '\n' {
			p.i++
		}
		// leave '\n' in place so skipSpace() sees it
		return true, nil
	}
	return false, nil
}
```

Then, in `parseAttrs`, after `p.skipSpace()` and after the `>`/`/>` return check, insert the comment-skip call. The modified `parseAttrs` loop should look like:

```go
func (p *parser) parseAttrs() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		p.skipSpace()
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF in attributes")
		}
		if p.peek() == '>' || p.at("/>") {
			return attrs, nil
		}
		// skip tag-interior // or /* */ comments
		if sk, err := p.skipTagComment(); err != nil {
			return nil, err
		} else if sk {
			continue
		}
		// {...expr} spread — tolerant of whitespace after `{` and around `...`
		// ... (rest unchanged)
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd /Users/jackieli/personal/gox && go test ./parser/ -run "TestTagTrailingLineComment|TestTagOwnLineComment|TestTagBlockComment|TestUnterminatedTagBlockComment" -v
```

Expected: all PASS

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/jackieli/personal/gox && go test ./...
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/jackieli/personal/gox && git add parser/markup.go parser/markup_test.go && git commit -m "$(cat <<'EOF'
feat(parser): tag-interior // and /* */ comments stripped in parseAttrs

Add skipTagComment() parser method and wire it into the parseAttrs loop
right after skipSpace() and the >/"/> return check.  Block comments with
no terminating */ produce a positioned error.  Tests: trailing line
comment, own-line comment, block comment, unterminated block comment.
EOF
)"
```

---

### Task 2: Content-position braced comments — `commentOnly` + `skipBracedComment` + child loop wiring

**Files:**
- Modify: `/Users/jackieli/personal/gox/parser/markup.go`
- Test: `/Users/jackieli/personal/gox/parser/markup_test.go`

**Interfaces:**
- Produces: `func commentOnly(src string) bool` — uses `go/scanner` with `ScanComments` mode to scan `src` and return true iff there are no tokens other than `token.COMMENT` and `token.SEMICOLON` before `token.EOF`. (SEMICOLON can appear from Go scanner's auto-insert rules in edge cases; it is harmless to accept.)
- Produces: `func (p *parser) skipBracedComment() (skipped bool, err error)` — if at `{` and the content is comment-only, advance past the closing `}` and return (true, nil); otherwise return (false, nil) without moving the cursor.

**Consumes:**
- `goExprEnd(src, open)` from `parser/boundary.go`
- `commentOnly(src)` defined in this task

- [ ] **Step 1: Write failing tests**

Add to `/Users/jackieli/personal/gox/parser/markup_test.go`:

```go
func TestContentIsLiteral(t *testing.T) {
	// CRITICAL: text between > and < or { is verbatim; // and /* */ are NOT stripped.
	src := `<a>http://example.com // and /* this */ stay literal</a>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	txt, ok := el.Children[0].(*ast.Text)
	if !ok {
		t.Fatalf("child is %T, want *ast.Text", el.Children[0])
	}
	want := "http://example.com // and /* this */ stay literal"
	if txt.Value != want {
		t.Fatalf("text value = %q, want %q", txt.Value, want)
	}
}

func TestBracedContentComment(t *testing.T) {
	// {/* comment with <tags> and a } brace */} is skipped; "keep" remains as Text.
	// goExprEnd handles the } inside the comment (scanner-aware).
	src := `<div>{/* a content comment with <tags> and a } brace */}keep</div>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	txt, ok := el.Children[0].(*ast.Text)
	if !ok {
		t.Fatalf("child is %T, want *ast.Text", el.Children[0])
	}
	if txt.Value != "keep" {
		t.Fatalf("text value = %q, want %q", txt.Value, "keep")
	}
}

func TestBracedLineComment(t *testing.T) {
	// {// line comment\n} is skipped; "x" remains as Text.
	src := "<div>{// just a line comment\n}x</div>"
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	txt, ok := el.Children[0].(*ast.Text)
	if !ok {
		t.Fatalf("child is %T, want *ast.Text", el.Children[0])
	}
	if txt.Value != "x" {
		t.Fatalf("text value = %q, want %q", txt.Value, "x")
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
cd /Users/jackieli/personal/gox && go test ./parser/ -run "TestContentIsLiteral|TestBracedContentComment|TestBracedLineComment" -v 2>&1 | head -40
```

Expected: `TestContentIsLiteral` PASS (parseText is unchanged), `TestBracedContentComment` and `TestBracedLineComment` FAIL (braced comments not yet stripped).

- [ ] **Step 3: Add `commentOnly` helper and `skipBracedComment` method**

`commentOnly` needs `go/scanner` and `go/token` — both are already imported in `boundary.go`. In `markup.go`, `go/token` is imported but `go/scanner` is not yet. We need to add it.

At the top of `markup.go`, update the import block to include `"go/scanner"`:

```go
import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)
```

Add `commentOnly` as a package-level function (after `skipTagComment`):

```go
// commentOnly reports whether src contains only Go comments (no real expression tokens).
// A {/* … */} or {// … \n} whose body passes this check can be silently dropped.
func commentOnly(src string) bool {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)
	for {
		_, tok, _ := s.Scan()
		switch tok {
		case token.EOF:
			return true
		case token.COMMENT, token.SEMICOLON:
			// allowed — comments and auto-inserted semicolons are fine
		default:
			return false
		}
	}
}
```

Add `skipBracedComment` method (after `commentOnly`):

```go
// skipBracedComment checks whether the `{…}` at the current cursor is comment-only.
// If so, it advances past the closing `}` and returns (true, nil).
// Otherwise it returns (false, nil) without moving the cursor.
// Unterminated `{` is not an error here — parseInterp handles that.
func (p *parser) skipBracedComment() (bool, error) {
	if p.peek() != '{' {
		return false, nil
	}
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return false, nil
	}
	inner := p.src[p.i+1 : end]
	if !commentOnly(inner) {
		return false, nil
	}
	p.i = end + 1
	return true, nil
}
```

- [ ] **Step 4: Wire `skipBracedComment` into `parseChildren`**

In `parseChildren`, replace the `if p.peek() == '{'` block:

Old code (around line 259-265):
```go
		if p.peek() == '{' {
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			continue
		}
```

New code:
```go
		if p.peek() == '{' {
			if sk, err := p.skipBracedComment(); err != nil {
				return nil, err
			} else if sk {
				continue
			}
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			continue
		}
```

- [ ] **Step 5: Wire `skipBracedComment` into `parseNodesUntilEOF`**

In `parseNodesUntilEOF`, replace the `case p.peek() == '{'` case:

Old code (around line 285-290):
```go
		case p.peek() == '{':
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
```

New code:
```go
		case p.peek() == '{':
			if sk, err := p.skipBracedComment(); err != nil {
				return nil, err
			} else if sk {
				continue
			}
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
```

- [ ] **Step 6: Run tests — expect PASS**

```bash
cd /Users/jackieli/personal/gox && go test ./parser/ -run "TestContentIsLiteral|TestBracedContentComment|TestBracedLineComment" -v
```

Expected: all PASS

- [ ] **Step 7: Run full test suite**

```bash
cd /Users/jackieli/personal/gox && go test ./...
```

Expected: all PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/jackieli/personal/gox && git add parser/markup.go parser/markup_test.go && git commit -m "$(cat <<'EOF'
feat(parser): braced content comments {/* */} and {// \n} stripped in child loops

Add commentOnly() helper (go/scanner-based) and skipBracedComment() method.
Wire both into parseChildren and parseNodesUntilEOF — if peek=='{'
and the brace body is comment-only, skip it; otherwise fall through to
parseInterp.  Element text stays completely untouched (parseText unchanged).
Tests: content is literal (URLs/comments), braced block comment, braced
line comment.
EOF
)"
```

---

### Task 3: Fix example 12 — convert content-position comment to braced form

**Files:**
- Modify: `/Users/jackieli/personal/gox/examples/12_children_attrs.gsx`

**Context:** Line 35 of `12_children_attrs.gsx` currently has:
```
		// class -> merges to "btn btn-primary w-full"; data-test/hx-post/@click -> <button>
```
This is between `<div>` and `<Button>` — element content position. Under the new rule this is literal text, and its content (`<button>`) would break parsing. Convert it to a braced comment.

- [ ] **Step 1: Edit line 35 in `12_children_attrs.gsx`**

Replace the bare line comment with a braced comment. The component `Toolbar()` block (lines 33-40) currently reads:

```gsx
component Toolbar() {
	<div>
		// class -> merges to "btn btn-primary w-full"; data-test/hx-post/@click -> <button>
		<Button variant="primary" class="w-full" data-test="save" hx-post="/save" @click="go()">
			Save
		</Button>
	</div>
}
```

Change it to:

```gsx
component Toolbar() {
	<div>
		{/* class -> merges to "btn btn-primary w-full"; data-test/hx-post/@click -> <button> */}
		<Button variant="primary" class="w-full" data-test="save" hx-post="/save" @click="go()">
			Save
		</Button>
	</div>
}
```

- [ ] **Step 2: Run full test suite**

```bash
cd /Users/jackieli/personal/gox && go test ./...
```

Expected: all PASS (the `examples_coverage.golden` may now differ — that's OK, we'll update it in Task 5).

If `TestExamplesCoverage` fails with a golden mismatch, that's expected and will be fixed in Task 5.

- [ ] **Step 3: Commit**

```bash
cd /Users/jackieli/personal/gox && git add examples/12_children_attrs.gsx && git commit -m "$(cat <<'EOF'
fix(example): 12 content comment -> braced {/* */}

The bare // comment between <div> and <Button> was in element content
position (literal text under the new rule).  Its <button> text broke
parsing.  Convert to {/* … */} so it is stripped as a content-position
comment.
EOF
)"
```

---

### Task 4: Update spec and regenerate examples_coverage.golden

**Files:**
- Modify: `/Users/jackieli/personal/gox/docs/superpowers/specs/2026-06-18-gsx-templating-design.md`
- Modify: `/Users/jackieli/personal/gox/internal/corpus/testdata/examples_coverage.golden`

- [ ] **Step 1: Update the spec §5 Comments bullet**

In `docs/superpowers/specs/2026-06-18-gsx-templating-design.md`, find the Comments bullet in §5:

```markdown
- **Comments:** HTML comments `<!-- … -->` pass through.
```

Replace it with:

```markdown
- **Comments:** Three forms, by position:
  - **Tag-interior** (inside `<tag … >`/`/>`): `// … ` (line, to end-of-line) and `/* … */` (block) are stripped — no AST node, no output.  An unterminated `/* … ` is a parse error.
  - **Element content** (between `>` and `</` or `{`): always **literal text**.  `//`, `/* */`, and URLs render verbatim — nothing is stripped.
  - **Content-position comment** (in child position): wrap in braces → `{/* … */}` or `{// …\n}`.  A `{ … }` whose body is *comment-only* (no real Go tokens) is stripped — produces no node.  A `{ … }` with any real token is a normal interpolation.
  - `<!-- … -->` HTML comments pass through to the output (later: parsed, not yet emitted).
```

- [ ] **Step 2: Regenerate `examples_coverage.golden`**

```bash
cd /Users/jackieli/personal/gox && go test ./internal/corpus/ -run TestExamplesCoverage -update
```

Expected: updates `internal/corpus/testdata/examples_coverage.golden` — `12_children_attrs.gsx` should flip from the old diagnostic to `ok` (or a different diagnostic if there are other issues).

- [ ] **Step 3: Verify the full test suite is GREEN**

```bash
cd /Users/jackieli/personal/gox && go test ./...
```

Expected: all PASS

- [ ] **Step 4: Commit**

```bash
cd /Users/jackieli/personal/gox && git add docs/superpowers/specs/2026-06-18-gsx-templating-design.md internal/corpus/testdata/examples_coverage.golden && git commit -m "$(cat <<'EOF'
docs+test: comment rule in spec; regen examples coverage

Extend §5 Comments bullet to document the three-position comment rule:
tag-interior //, /* */, element content is literal, content-position
{/* */}.  Regenerate examples_coverage.golden to reflect example 12
now parsing cleanly.
EOF
)"
```

---

### Task 5: Write the implementation report

**Files:**
- Create: `/Users/jackieli/personal/gox/.git/sdd/markup-comments-report.md`

- [ ] **Step 1: Check current golden file content**

```bash
cat /Users/jackieli/personal/gox/internal/corpus/testdata/examples_coverage.golden
```

Record the before-and-after diff for the report.

- [ ] **Step 2: Run tests and capture output**

```bash
cd /Users/jackieli/personal/gox && go test ./... -v 2>&1 | tail -30
```

- [ ] **Step 3: Write the report**

Create `/Users/jackieli/personal/gox/.git/sdd/markup-comments-report.md`:

```markdown
# Markup Comments Implementation Report

## Rule as Implemented

Three comment forms, by position:

1. **Tag-interior** (`parseAttrs`): `//` (to `\n` or EOF) and `/* */` (to matching `*/`) are stripped by `skipTagComment()`.  Called right after `skipSpace()` and after the `>`/`/>` return guard.  Unterminated `/*` returns a positioned error: `LINE:COL: unterminated block comment`.
2. **Element content** (`parseText`): unchanged — text between `>` and `</`/`{` is verbatim.  `//`, `/* */`, and URLs all render as-is.
3. **Content-position braced comment** (`parseChildren`, `parseNodesUntilEOF`): a `{ … }` whose interior contains only Go comments (detected by `commentOnly()` using `go/scanner` with `ScanComments`) is stripped by `skipBracedComment()`.  `goExprEnd()` correctly handles `}` inside comments, so `{/* … } … */}` terminates at the real closing brace.

## Tests Added

| Test | Input | Expected |
|------|-------|----------|
| `TestTagTrailingLineComment` | `<div id={x} // trailing\n class="y">` | 2 attrs: ExprAttr{id}, StaticAttr{class} |
| `TestTagOwnLineComment` | `<div\n // own line\n id={x}>` | 1 attr: ExprAttr{id} |
| `TestTagBlockComment` | `<div /* note */ id={x}>` | 1 attr: ExprAttr{id} |
| `TestContentIsLiteral` | `<a>http://example.com // and /* this */ stay literal</a>` | 1 Text child with full literal string |
| `TestBracedContentComment` | `<div>{/* comment with <tags> and a } brace */}keep</div>` | 1 Text child: "keep" |
| `TestBracedLineComment` | `<div>{// just a line comment\n}x</div>` | 1 Text child: "x" |
| `TestUnterminatedTagBlockComment` | `<div /* oops>` | error containing "unterminated block comment" |

## examples_coverage.golden Changes

`12_children_attrs.gsx` flipped from:
```
12_children_attrs.gsx: 39:2: mismatched close tag </div>, expected </button>
```
to:
```
12_children_attrs.gsx: ok
```
(or updated diagnostic if other parse errors remain in the file)

## go test output

[captured from: `go test ./... -v`]

All packages PASS.
```

- [ ] **Step 4: No commit needed for report** (`.git/sdd/` is not tracked)

---

## Self-Review Against Spec

**Spec coverage check:**

| Spec requirement | Task |
|-----------------|------|
| A. `skipTagComment()` helper, `/**/` and `//` in tag interior | Task 1 |
| A. Error for unterminated `/* */` with position | Task 1 |
| A. Wire after `p.skipSpace()` and after `>`/`/>` check, before spread | Task 1 |
| B. `commentOnly(src)` using `go/scanner` | Task 2 |
| B. `skipBracedComment()` — checks `commentOnly`, advances only on match | Task 2 |
| B. Wire into `parseChildren` and `parseNodesUntilEOF` | Task 2 |
| C. Fix example 12 — convert bare `//` comment to `{/* */}` | Task 3 |
| D. 7 tests in `markup_test.go`, each using `testParser` | Tasks 1+2 |
| E. Update spec §5 Comments bullet | Task 4 |
| F. Regenerate `examples_coverage.golden` | Task 4 |
| G. Write report to `.git/sdd/markup-comments-report.md` | Task 5 |
| H. Three commits with specified messages (approximately) | Tasks 1+2, 3, 4 |

**Placeholder scan:** None found.

**Type consistency:** `skipTagComment` returns `(bool, error)` and is used as `if sk, err := p.skipTagComment(); err != nil { return nil, err } else if sk { continue }` in Task 1. `skipBracedComment` same signature used the same way in Task 2. `commentOnly` returns `bool` used in `skipBracedComment`. All consistent.

**Note on `parseAttrs` return type:** `parseAttrs` returns `([]ast.Attr, error)` and the error propagation `return nil, err` is consistent with existing code.

**Note on `parseNodesUntilEOF` switch statement:** The `case p.peek() == '{':` is inside a `switch` (not `if/else`). The `continue` in the wired code will continue the outer `for` loop, which is correct — `switch` in Go does not fall through by default, and `continue` in a `case` continues the enclosing `for`.
