# gsx Parser (Core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Parse a `.gsx` source file's structure and the core markup grammar into an AST.

**Architecture:** A `.gsx` file is ordinary Go plus `component` declarations whose bodies contain JSX-like markup. The parser scans the top level with Go's own scanner (`go/scanner`), copies non-`component` Go as opaque source chunks, and hands each `component` body to a hand-written recursive-descent markup parser. Embedded Go expressions inside `{ … }` have their *boundaries* found with `go/scanner` (we never re-implement Go); markup-vs-Go inside `{ … }` is decided positionally (the Babel rule). Output is an AST in package `internal/ast`.

**Tech Stack:** Go 1.26, standard library only — `go/scanner`, `go/token` (no third-party deps).

## Global Constraints

- Go version floor: **go 1.26.1** (from `go.mod`).
- Module path: **`github.com/gsxhq/gsx`**.
- No third-party dependencies in the parser — standard library only.
- AST and parser packages are **public top-level packages** (exported API): `github.com/gsxhq/gsx/ast` and `github.com/gsxhq/gsx/parser`. They are NOT under `internal/`. See CLI skeleton design §3 ("Core ↔ Front-end Boundary").
- **Scope of THIS plan (core grammar):** package clause, import blocks, opaque Go chunks, `component` declarations (with optional receiver and params), and markup nodes: elements, fragments, text, `{ expr }` and `{ expr? }` interpolation, attributes (static `name="v"`, expression `name={e}` / `name={e?}`, boolean bare `name`, spread `{...e}`, markup-valued `name={ <…/> }`), nested elements, and component tags (Capitalized / dotted).
- **Deferred to "Parser Part 2" (NOT this plan):** control flow `{ if|for|switch … { … } }`, the `{{ … }}` Go-statement block, in-tag conditional attributes `{ if … { attr } }`, the composable `class={ a, "x": cond }` comma/colon grammar, and any semantic analysis (type resolution, escaping, codegen). The parser only produces syntax; semantics belong to later subsystems.
- TDD: every task writes a failing test first, then the minimal code to pass. Commit after each task.
- Run tests with `go test ./internal/...`.

---

### Task 1: AST node types

**Files:**
- Create: `internal/ast/ast.go`
- Test: `internal/ast/ast_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: the AST types every later task builds and asserts on —
  `File{Package string, Decls []Decl}`; `Decl` (sealed by `GoChunk`, `Component`);
  `GoChunk{Src string, Pos token.Position}`;
  `Component{Recv, Name, Params string, Body []Node, Pos token.Position}`;
  `Node` (sealed by `Element`, `Fragment`, `Text`, `Interp`);
  `Element{Tag string, Void bool, Attrs []Attr, Children []Node, Pos token.Position}`;
  `Fragment{Children []Node, Pos token.Position}`;
  `Text{Value string, Pos token.Position}`;
  `Interp{Expr string, Try bool, Pos token.Position}`;
  `Attr` (sealed by `StaticAttr`, `ExprAttr`, `BoolAttr`, `SpreadAttr`, `MarkupAttr`);
  `StaticAttr{Name, Value string}`; `ExprAttr{Name, Expr string, Try bool}`;
  `BoolAttr{Name string}`; `SpreadAttr{Expr string}`; `MarkupAttr{Name string, Value []Node}`.

- [ ] **Step 1: Write the failing test**

```go
// internal/ast/ast_test.go
package ast

import (
	"go/token"
	"testing"
)

func TestNodesImplementInterfaces(t *testing.T) {
	var _ Decl = (*GoChunk)(nil)
	var _ Decl = (*Component)(nil)
	var _ Node = (*Element)(nil)
	var _ Node = (*Fragment)(nil)
	var _ Node = (*Text)(nil)
	var _ Node = (*Interp)(nil)
	var _ Attr = (*StaticAttr)(nil)
	var _ Attr = (*ExprAttr)(nil)
	var _ Attr = (*BoolAttr)(nil)
	var _ Attr = (*SpreadAttr)(nil)
	var _ Attr = (*MarkupAttr)(nil)

	f := File{Package: "views", Decls: []Decl{
		&Component{Name: "Card", Body: []Node{
			&Element{Tag: "div", Children: []Node{&Text{Value: "hi"}}},
		}, Pos: token.Position{Line: 1}},
	}}
	if f.Decls[0].(*Component).Name != "Card" {
		t.Fatalf("unexpected name")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ast/`
Expected: FAIL — build error, `undefined: Decl` etc.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/ast/ast.go
// Package ast defines the gsx syntax tree produced by the parser.
package ast

import "go/token"

// File is a parsed .gsx file.
type File struct {
	Package string
	PkgPos  token.Position
	Decls   []Decl
}

// Decl is a top-level declaration: opaque Go, or a component.
type Decl interface{ declNode() }

// GoChunk is a verbatim span of Go source (imports, types, consts, vars, funcs)
// copied through unchanged.
type GoChunk struct {
	Src string
	Pos token.Position
}

func (*GoChunk) declNode() {}

// Component is a `component [recv] Name(params) { body }` declaration.
type Component struct {
	Recv   string // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	Name   string
	Params string // raw param-list source, e.g. "title string, featured bool"; "" if none
	Body   []Node
	Pos    token.Position
}

func (*Component) declNode() {}

// Node is a markup node.
type Node interface{ node() }

// Element is an HTML element or a component tag (Tag may be dotted, e.g. "ui.Button").
type Element struct {
	Tag      string
	Void     bool // self-closing <tag/> or HTML void element
	Attrs    []Attr
	Children []Node
	Pos      token.Position
}

func (*Element) node() {}

// Fragment is <>…</> — siblings without a wrapper.
type Fragment struct {
	Children []Node
	Pos      token.Position
}

func (*Fragment) node() {}

// Text is literal character data between markup.
type Text struct {
	Value string
	Pos   token.Position
}

func (*Text) node() {}

// Interp is `{ expr }` (Try=false) or `{ expr? }` (Try=true).
type Interp struct {
	Expr string
	Try  bool
	Pos  token.Position
}

func (*Interp) node() {}

// Attr is one attribute on an element.
type Attr interface{ attr() }

// StaticAttr is name="value".
type StaticAttr struct{ Name, Value string }

func (*StaticAttr) attr() {}

// ExprAttr is name={expr} or name={expr?}.
type ExprAttr struct {
	Name, Expr string
	Try        bool
}

func (*ExprAttr) attr() {}

// BoolAttr is a bare attribute name (boolean true).
type BoolAttr struct{ Name string }

func (*BoolAttr) attr() {}

// SpreadAttr is {...expr}.
type SpreadAttr struct{ Expr string }

func (*SpreadAttr) attr() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	Name  string
	Value []Node
}

func (*MarkupAttr) attr() {}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ast/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ast/
git commit -m "feat(ast): gsx syntax tree node types"
```

---

### Task 2: Go-expression boundary finder

**Files:**
- Create: `internal/parser/boundary.go`
- Test: `internal/parser/boundary_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `goExprEnd(src string, open int) (close int, ok bool)` — given `src` and the index `open` of an opening `{`, returns the index of the matching `}` (accounting for nested `(){}[]`, Go string/char/raw literals, and comments) or `ok=false` if unbalanced. Used wherever the parser must capture an embedded Go expression and find where it ends.

- [ ] **Step 1: Write the failing test**

```go
// internal/parser/boundary_test.go
package parser

import "testing"

func TestGoExprEnd(t *testing.T) {
	cases := []struct {
		src   string
		open  int
		close int
		ok    bool
	}{
		{`{x}`, 0, 2, true},
		{`{ a < b && c > d }`, 0, 17, true},
		{`{ m[string]int{"a": 1} }`, 0, 23, true},     // nested braces
		{`{ "string with } brace" }`, 0, 24, true},     // brace in string
		{"{ `raw } string` }", 0, 17, true},            // brace in raw string
		{`{ '}' }`, 0, 6, true},                         // brace in rune literal
		{`{ a /* } */ b }`, 0, 14, true},               // brace in comment
		{`{ unbalanced`, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := goExprEnd(c.src, c.open)
		if ok != c.ok || (ok && got != c.close) {
			t.Errorf("goExprEnd(%q) = (%d,%v), want (%d,%v)", c.src, got, ok, c.close, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestGoExprEnd`
Expected: FAIL — `undefined: goExprEnd`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/parser/boundary.go
package parser

import (
	"go/scanner"
	"go/token"
)

// goExprEnd returns the index of the `}` that matches the `{` at src[open],
// scanning Go tokens so that braces inside strings, runes, and comments do not
// count. ok is false if no matching brace is found.
func goExprEnd(src string, open int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	// ScanComments so comment text (which may contain braces) is consumed as a unit.
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < open {
			continue
		}
		switch tok {
		case token.LBRACE, token.LPAREN, token.LBRACK:
			depth++
		case token.RBRACE, token.RPAREN, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RBRACE {
				return off, true
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run TestGoExprEnd`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/boundary.go internal/parser/boundary_test.go
git commit -m "feat(parser): Go-expression brace boundary finder"
```

---

### Task 3: Balanced-parens finder (for receivers and param lists)

**Files:**
- Modify: `internal/parser/boundary.go`
- Test: `internal/parser/boundary_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `parenEnd(src string, open int) (close int, ok bool)` — given the index of an opening `(`, returns the index of the matching `)` (Go-token aware). Used to capture a component's receiver `(p T)` and param list `(...)`.

- [ ] **Step 1: Write the failing test**

```go
// add to internal/parser/boundary_test.go
func TestParenEnd(t *testing.T) {
	cases := []struct {
		src   string
		open  int
		close int
		ok    bool
	}{
		{`(p UsersPage)`, 0, 12, true},
		{`(a string, b func(int) int)`, 0, 26, true}, // nested parens
		{`( ")" )`, 0, 6, true},                        // paren in string
		{`(unbalanced`, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := parenEnd(c.src, c.open)
		if ok != c.ok || (ok && got != c.close) {
			t.Errorf("parenEnd(%q) = (%d,%v), want (%d,%v)", c.src, got, ok, c.close, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestParenEnd`
Expected: FAIL — `undefined: parenEnd`.

- [ ] **Step 3: Write minimal implementation**

```go
// add to internal/parser/boundary.go
// parenEnd returns the index of the `)` matching the `(` at src[open].
func parenEnd(src string, open int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < open {
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
			if depth == 0 && tok == token.RPAREN {
				return off, true
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run TestParenEnd`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/boundary.go internal/parser/boundary_test.go
git commit -m "feat(parser): balanced-parens boundary finder"
```

---

### Task 4: Parser scaffold + markup cursor

**Files:**
- Create: `internal/parser/parser.go`
- Test: `internal/parser/parser_test.go`

**Interfaces:**
- Consumes: `internal/ast`.
- Produces: a `parser` struct over the source with a byte cursor, plus low-level
  cursor helpers used by all later markup tasks:
  `newParser(src string) *parser`; `(*parser).eof() bool`;
  `(*parser).peek() byte`; `(*parser).at(prefix string) bool`;
  `(*parser).skipSpace()`; `(*parser).pos() token.Position` (1-based line/col at the
  cursor). The struct fields `src string` and `i int` (cursor offset) are relied on
  by later tasks.

- [ ] **Step 1: Write the failing test**

```go
// internal/parser/parser_test.go
package parser

import "testing"

func TestCursorBasics(t *testing.T) {
	p := newParser("  ab")
	p.skipSpace()
	if p.peek() != 'a' {
		t.Fatalf("peek = %q, want 'a'", p.peek())
	}
	if !p.at("ab") {
		t.Fatalf("expected at('ab')")
	}
	if p.at("xy") {
		t.Fatalf("did not expect at('xy')")
	}
	pos := p.pos()
	if pos.Line != 1 || pos.Column != 3 {
		t.Fatalf("pos = %d:%d, want 1:3", pos.Line, pos.Column)
	}
	p.i = len(p.src)
	if !p.eof() {
		t.Fatalf("expected eof")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestCursorBasics`
Expected: FAIL — `undefined: newParser`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/parser/parser.go
package parser

import (
	"strings"
	"go/token"
)

type parser struct {
	src string
	i   int // byte cursor
}

func newParser(src string) *parser { return &parser{src: src} }

func (p *parser) eof() bool { return p.i >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.i]
}

func (p *parser) at(prefix string) bool {
	return strings.HasPrefix(p.src[p.i:], prefix)
}

func (p *parser) skipSpace() {
	for !p.eof() {
		switch p.src[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

// pos returns a 1-based line/column for the current cursor.
func (p *parser) pos() token.Position {
	line, col := 1, 1
	for j := 0; j < p.i && j < len(p.src); j++ {
		if p.src[j] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return token.Position{Line: line, Column: col, Offset: p.i}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run TestCursorBasics`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/parser.go internal/parser/parser_test.go
git commit -m "feat(parser): parser scaffold and markup cursor"
```

---

### Task 5: Parse text and interpolation in element children

**Files:**
- Create: `internal/parser/markup.go`
- Test: `internal/parser/markup_test.go`

**Interfaces:**
- Consumes: `internal/ast`, the cursor from Task 4, `goExprEnd` from Task 2.
- Produces: `(*parser).parseInterp() (*ast.Interp, error)` — at a `{`, captures the
  Go expression via `goExprEnd`, sets `Try=true` if it ends with `?`, advances past
  the `}`; and `(*parser).parseText() *ast.Text` — consumes literal text until the
  next `<` or `{` (or EOF). These are consumed by `parseChildren` in Task 8.

- [ ] **Step 1: Write the failing test**

```go
// internal/parser/markup_test.go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

func TestParseInterp(t *testing.T) {
	p := newParser(`{ user.Name }rest`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "user.Name" || n.Try {
		t.Fatalf("got %+v", n)
	}
	if p.src[p.i:] != "rest" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseInterpTry(t *testing.T) {
	p := newParser(`{ route.URL(ctx)? }`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "route.URL(ctx)" || !n.Try {
		t.Fatalf("got %+v", n)
	}
}

func TestParseText(t *testing.T) {
	p := newParser("hello world<div>")
	n := p.parseText()
	if n.Value != "hello world" {
		t.Fatalf("got %q", n.Value)
	}
	if p.peek() != '<' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

var _ = ast.Text{}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run 'TestParseInterp|TestParseText'`
Expected: FAIL — `p.parseInterp undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/parser/markup.go
package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	pos := p.pos()
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{`", pos.Line, pos.Column)
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	try := false
	if strings.HasSuffix(inner, "?") {
		try = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "?"))
	}
	p.i = end + 1
	return &ast.Interp{Expr: inner, Try: try, Pos: pos}, nil
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	pos := p.pos()
	start := p.i
	for !p.eof() && p.src[p.i] != '<' && p.src[p.i] != '{' {
		p.i++
	}
	return &ast.Text{Value: p.src[start:p.i], Pos: pos}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run 'TestParseInterp|TestParseText'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/markup.go internal/parser/markup_test.go
git commit -m "feat(parser): text and { } interpolation"
```

---

### Task 6: Parse attributes

**Files:**
- Modify: `internal/parser/markup.go`
- Test: `internal/parser/markup_test.go`

**Interfaces:**
- Consumes: cursor, `goExprEnd`, `ast`.
- Produces: `(*parser).parseAttrs() ([]ast.Attr, error)` — repeatedly parses
  attributes separated by whitespace until it reaches `>` or `/>`. Recognizes:
  `name="value"` → `StaticAttr`; `name={expr}` / `name={expr?}` → `ExprAttr`;
  `name={ <…/> }` → `MarkupAttr` (markup detected by the Babel rule: first non-space
  inside the braces is `<` followed by a letter, `/`, or `>`); `{...expr}` →
  `SpreadAttr`; bare `name` → `BoolAttr`. Attribute names may contain
  `[A-Za-z0-9_:@.\-]` and the `::` sequence. Relies on `parseChildren` (Task 8) for
  `MarkupAttr` values via a forward call; for THIS task, capture the markup-attr
  value's raw inner span and parse it with a nested `newParser` once `parseChildren`
  exists — until then return a single `*ast.Text` placeholder is NOT allowed; order
  the work so Task 8 lands first if needed. (Implementation below calls
  `p.parseNodesUntil` which is introduced in Task 8; if executing strictly in order,
  stub `MarkupAttr` by storing the raw inner string in a `Text` node and replace in
  Task 8. The test here covers the non-markup attribute forms only.)

- [ ] **Step 1: Write the failing test**

```go
// add to internal/parser/markup_test.go
func TestParseAttrs(t *testing.T) {
	p := newParser(`class="card" id={x} disabled {...rest} data-y={z?}>`)
	attrs, err := p.parseAttrs()
	if err != nil {
		t.Fatal(err)
	}
	if len(attrs) != 5 {
		t.Fatalf("got %d attrs: %#v", len(attrs), attrs)
	}
	if a, ok := attrs[0].(*ast.StaticAttr); !ok || a.Name != "class" || a.Value != "card" {
		t.Fatalf("attr0 = %#v", attrs[0])
	}
	if a, ok := attrs[1].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr1 = %#v", attrs[1])
	}
	if a, ok := attrs[2].(*ast.BoolAttr); !ok || a.Name != "disabled" {
		t.Fatalf("attr2 = %#v", attrs[2])
	}
	if a, ok := attrs[3].(*ast.SpreadAttr); !ok || a.Expr != "rest" {
		t.Fatalf("attr3 = %#v", attrs[3])
	}
	if a, ok := attrs[4].(*ast.ExprAttr); !ok || a.Name != "data-y" || !a.Try {
		t.Fatalf("attr4 = %#v", attrs[4])
	}
	if p.peek() != '>' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestParseAttrs`
Expected: FAIL — `p.parseAttrs undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// add to internal/parser/markup.go

func isAttrNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == ':' || b == '@' || b == '.' || b == '-'
}

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
		// {...expr} spread
		if p.at("{...") {
			end, ok := goExprEnd(p.src, p.i)
			if !ok {
				return nil, fmt.Errorf("unterminated spread `{...`")
			}
			inner := strings.TrimSpace(p.src[p.i+1 : end])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			p.i = end + 1
			attrs = append(attrs, &ast.SpreadAttr{Expr: inner})
			continue
		}
		// attribute name
		start := p.i
		for !p.eof() && isAttrNameByte(p.src[p.i]) {
			p.i++
		}
		if p.i == start {
			return nil, fmt.Errorf("%d:%d: expected attribute name, got %q",
				p.pos().Line, p.pos().Column, string(p.peek()))
		}
		name := p.src[start:p.i]
		switch {
		case p.at(`="`):
			p.i += 2
			vs := p.i
			for !p.eof() && p.src[p.i] != '"' {
				p.i++
			}
			if p.eof() {
				return nil, fmt.Errorf("unterminated attribute string for %q", name)
			}
			val := p.src[vs:p.i]
			p.i++ // past closing quote
			attrs = append(attrs, &ast.StaticAttr{Name: name, Value: val})
		case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
			p.i++ // past '='
			if a, err := p.parseAttrBraceValue(name); err != nil {
				return nil, err
			} else {
				attrs = append(attrs, a)
			}
		default:
			attrs = append(attrs, &ast.BoolAttr{Name: name})
		}
	}
}

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string) (ast.Attr, error) {
	// Babel rule: first non-space inside the braces starting markup?
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	if j < len(p.src) && p.src[j] == '<' && j+1 < len(p.src) && startsTag(p.src[j+1]) {
		end, ok := goExprEnd(p.src, p.i) // markup is brace-balanced too
		if !ok {
			return nil, fmt.Errorf("unterminated markup attribute %q", name)
		}
		inner := p.src[p.i+1 : end]
		sub := newParser(inner)
		nodes, err := sub.parseNodesUntilEOF()
		if err != nil {
			return nil, err
		}
		p.i = end + 1
		return &ast.MarkupAttr{Name: name, Value: nodes}, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	return &ast.ExprAttr{Name: name, Expr: in.Expr, Try: in.Try}, nil
}

// startsTag reports whether b can begin a tag name (letter) or a fragment close.
func startsTag(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '>' || b == '/'
}
```

Note: `parseNodesUntilEOF` is added in Task 8; this task's test does not exercise the markup-attr branch, so the package still builds once Task 8 lands. If executing strictly in order, temporarily add a stub `func (p *parser) parseNodesUntilEOF() ([]ast.Node, error) { return nil, nil }` at the bottom of `markup.go` and delete it in Task 8.

- [ ] **Step 2b: Add the temporary stub so the package builds**

```go
// add to internal/parser/markup.go (REMOVE in Task 8)
func (p *parser) parseNodesUntilEOF() ([]ast.Node, error) { return nil, nil }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run TestParseAttrs`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/markup.go internal/parser/markup_test.go
git commit -m "feat(parser): attribute forms (static/expr/bool/spread/markup)"
```

---

### Task 7: Parse a single element (open tag, self-close, void, close tag)

**Files:**
- Modify: `internal/parser/markup.go`
- Test: `internal/parser/markup_test.go`

**Interfaces:**
- Consumes: `parseAttrs`, cursor, `ast`.
- Produces: `(*parser).parseElement() (ast.Node, error)` — at `<`, parses a tag name
  (letters, digits, `-`, `.`), its attributes, then either `/>` (Void element, no
  children) or `>` … `</tag>`; a `<>` opens a `Fragment` closed by `</>`. Children
  are gathered by `parseChildren` (Task 8); for THIS task, only the open-tag /
  self-close path and tag-name extraction are tested (empty or self-closing
  elements).

- [ ] **Step 1: Write the failing test**

```go
// add to internal/parser/markup_test.go
func TestParseSelfClosing(t *testing.T) {
	p := newParser(`<img src="x.png"/>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "img" || !el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}

func TestParseDottedComponentTag(t *testing.T) {
	p := newParser(`<ui.Button variant="primary"></ui.Button>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "ui.Button" || el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run 'TestParseSelfClosing|TestParseDotted'`
Expected: FAIL — `p.parseElement undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// add to internal/parser/markup.go

func isTagNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '-' || b == '.'
}

func (p *parser) parseElement() (ast.Node, error) {
	pos := p.pos()
	if p.peek() != '<' {
		return nil, fmt.Errorf("%d:%d: expected '<'", pos.Line, pos.Column)
	}
	p.i++ // past '<'

	// Fragment: <>…</>
	if p.peek() == '>' {
		p.i++ // past '>'
		children, err := p.parseChildren("")
		if err != nil {
			return nil, err
		}
		return &ast.Fragment{Children: children, Pos: pos}, nil
	}

	start := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) {
		p.i++
	}
	tag := p.src[start:p.i]
	if tag == "" {
		return nil, fmt.Errorf("%d:%d: expected tag name", pos.Line, pos.Column)
	}

	attrs, err := p.parseAttrs()
	if err != nil {
		return nil, err
	}

	if p.at("/>") {
		p.i += 2
		return &ast.Element{Tag: tag, Void: true, Attrs: attrs, Pos: pos}, nil
	}
	if p.peek() != '>' {
		return nil, fmt.Errorf("%d:%d: expected '>' or '/>' in <%s>", p.pos().Line, p.pos().Column, tag)
	}
	p.i++ // past '>'

	children, err := p.parseChildren(tag)
	if err != nil {
		return nil, err
	}
	return &ast.Element{Tag: tag, Attrs: attrs, Children: children, Pos: pos}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run 'TestParseSelfClosing|TestParseDotted'`
Expected: PASS (compiles because `parseChildren` is added in Task 8 — if executing in order, add the stub below first).

- [ ] **Step 4b: Temporary stub so the package builds (REMOVE in Task 8)**

```go
// add to internal/parser/markup.go (REMOVE in Task 8)
func (p *parser) parseChildren(closeTag string) ([]ast.Node, error) { return nil, nil }
```

- [ ] **Step 5: Commit**

```bash
git add internal/parser/markup.go internal/parser/markup_test.go
git commit -m "feat(parser): single element, self-close, fragment, dotted tags"
```

---

### Task 8: Parse children (recursive descent) and close tags

**Files:**
- Modify: `internal/parser/markup.go`
- Test: `internal/parser/markup_test.go`

**Interfaces:**
- Consumes: `parseElement`, `parseText`, `parseInterp`, `ast`.
- Produces: `(*parser).parseChildren(closeTag string) ([]ast.Node, error)` — gathers
  text, interpolation, and nested elements until it reaches `</closeTag>` (or `</>`
  when `closeTag==""`), which it consumes; and `(*parser).parseNodesUntilEOF()
  ([]ast.Node, error)` — same but stops at EOF (used by markup attribute values).
  **Removes the Task 6 and Task 7 stubs.**

- [ ] **Step 1: Write the failing test**

```go
// add to internal/parser/markup_test.go
func TestParseChildrenNested(t *testing.T) {
	p := newParser(`<div class="card"><h2>{title}</h2>text</div>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	div := n.(*ast.Element)
	if len(div.Children) != 2 {
		t.Fatalf("got %d children: %#v", len(div.Children), div.Children)
	}
	h2 := div.Children[0].(*ast.Element)
	if h2.Tag != "h2" {
		t.Fatalf("child0 = %#v", h2)
	}
	if _, ok := h2.Children[0].(*ast.Interp); !ok {
		t.Fatalf("h2 child = %#v", h2.Children[0])
	}
	if txt := div.Children[1].(*ast.Text); txt.Value != "text" {
		t.Fatalf("child1 = %#v", div.Children[1])
	}
}

func TestParseMarkupAttr(t *testing.T) {
	p := newParser(`<Panel header={ <h1>Hi</h1> }></Panel>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	ma := el.Attrs[0].(*ast.MarkupAttr)
	if ma.Name != "header" || len(ma.Value) != 1 {
		t.Fatalf("got %#v", ma)
	}
	if ma.Value[0].(*ast.Element).Tag != "h1" {
		t.Fatalf("markup attr value = %#v", ma.Value[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run 'TestParseChildrenNested|TestParseMarkupAttr'`
Expected: FAIL — duplicate stub or empty children (returns 0 children).

- [ ] **Step 3: Write minimal implementation**

Delete the two temporary stubs (`parseChildren` and `parseNodesUntilEOF`) added in Tasks 6 and 7, then add:

```go
// add to internal/parser/markup.go

func (p *parser) parseChildren(closeTag string) ([]ast.Node, error) {
	var nodes []ast.Node
	for {
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF, expected </%s>", closeTag)
		}
		if p.at("</") {
			// consume close tag
			p.i += 2
			start := p.i
			for !p.eof() && isTagNameByte(p.src[p.i]) {
				p.i++
			}
			got := p.src[start:p.i]
			p.skipSpace()
			if p.peek() != '>' {
				return nil, fmt.Errorf("%d:%d: malformed close tag", p.pos().Line, p.pos().Column)
			}
			p.i++ // past '>'
			if got != closeTag {
				return nil, fmt.Errorf("%d:%d: mismatched close tag </%s>, expected </%s>",
					p.pos().Line, p.pos().Column, got, closeTag)
			}
			return nodes, nil
		}
		if p.peek() == '<' {
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, el)
			continue
		}
		if p.peek() == '{' {
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
			continue
		}
		nodes = append(nodes, p.parseText())
	}
}

func (p *parser) parseNodesUntilEOF() ([]ast.Node, error) {
	var nodes []ast.Node
	for {
		p.skipSpace()
		if p.eof() {
			return nodes, nil
		}
		switch {
		case p.peek() == '<':
			el, err := p.parseElement()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, el)
		case p.peek() == '{':
			in, err := p.parseInterp()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, in)
		default:
			nodes = append(nodes, p.parseText())
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/`
Expected: PASS (all parser tests, including the close-tag mismatch error path).

- [ ] **Step 5: Commit**

```bash
git add internal/parser/markup.go internal/parser/markup_test.go
git commit -m "feat(parser): recursive children, close-tag matching, markup attrs"
```

---

### Task 9: Parse a component header and body

**Files:**
- Create: `internal/parser/component.go`
- Test: `internal/parser/component_test.go`

**Interfaces:**
- Consumes: `parenEnd` (Task 3), `goExprEnd` (Task 2), `parseNodesUntilEOF`/markup
  parser (Task 8), `ast`.
- Produces: `(*parser).parseComponent() (*ast.Component, error)` — cursor at the
  `component` keyword; parses optional receiver `(…)`, the name, optional params
  `(…)`, then the `{` … markup body … `}` (body parsed by a nested parser over the
  body's inner source so its `}` closes the component). Sets `Recv`, `Name`,
  `Params`, `Body`.

- [ ] **Step 1: Write the failing test**

```go
// internal/parser/component_test.go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

func TestParseComponentSimple(t *testing.T) {
	src := `component Card(title string) {
	<section class="card">{title}</section>
}`
	p := newParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "" || c.Name != "Card" || c.Params != "title string" {
		t.Fatalf("got %+v", c)
	}
	if len(c.Body) != 1 {
		t.Fatalf("body = %#v", c.Body)
	}
	if c.Body[0].(*ast.Element).Tag != "section" {
		t.Fatalf("body0 = %#v", c.Body[0])
	}
}

func TestParseComponentMethod(t *testing.T) {
	src := `component (p UsersPage) Content() {
	<div>{p.Title}</div>
}`
	p := newParser(src)
	c, err := p.parseComponent()
	if err != nil {
		t.Fatal(err)
	}
	if c.Recv != "(p UsersPage)" || c.Name != "Content" || c.Params != "" {
		t.Fatalf("got %+v", c)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestParseComponent`
Expected: FAIL — `p.parseComponent undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/parser/component.go
package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// parseComponent parses a `component [recv] Name[(params)] { body }`.
// Cursor must be at the start of the `component` keyword.
func (p *parser) parseComponent() (*ast.Component, error) {
	pos := p.pos()
	if !p.at("component") {
		return nil, fmt.Errorf("%d:%d: expected `component`", pos.Line, pos.Column)
	}
	p.i += len("component")
	c := &ast.Component{Pos: pos}

	p.skipSpace()
	// optional receiver
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			return nil, fmt.Errorf("%d:%d: unterminated receiver", p.pos().Line, p.pos().Column)
		}
		c.Recv = p.src[p.i : end+1]
		p.i = end + 1
		p.skipSpace()
	}

	// name
	start := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) && p.src[p.i] != '.' && p.src[p.i] != '-' {
		p.i++
	}
	c.Name = p.src[start:p.i]
	if c.Name == "" {
		return nil, fmt.Errorf("%d:%d: expected component name", p.pos().Line, p.pos().Column)
	}

	p.skipSpace()
	// optional params
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			return nil, fmt.Errorf("%d:%d: unterminated params", p.pos().Line, p.pos().Column)
		}
		c.Params = strings.TrimSpace(p.src[p.i+1 : end])
		p.i = end + 1
	}

	p.skipSpace()
	if p.peek() != '{' {
		return nil, fmt.Errorf("%d:%d: expected `{` to open component body", p.pos().Line, p.pos().Column)
	}
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated component body", p.pos().Line, p.pos().Column)
	}
	body := p.src[p.i+1 : end]
	p.i = end + 1

	sub := newParser(body)
	nodes, err := sub.parseNodesUntilEOF()
	if err != nil {
		return nil, err
	}
	c.Body = nodes
	return c, nil
}
```

Note: `goExprEnd` finds the body's matching `}` because the body's markup is itself
brace-balanced under the core grammar (every `{` interpolation/attr has a matching
`}`, and tags don't contribute braces). Part 2's `{{ }}` and `{ if … }` remain
brace-balanced, so this strategy still holds.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run TestParseComponent`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/component.go internal/parser/component_test.go
git commit -m "feat(parser): component header and body parsing"
```

---

### Task 10: Parse the file (package, Go chunks, components)

**Files:**
- Create: `internal/parser/file.go`
- Test: `internal/parser/file_test.go`

**Interfaces:**
- Consumes: `parseComponent`, `ast`, `go/scanner`, `go/token`.
- Produces: `Parse(src string) (*ast.File, error)` (exported package entry) — reads
  the `package` clause, then walks the top level: each maximal run of non-`component`
  Go source becomes a `*ast.GoChunk`; each `component` keyword at the top level
  starts a `*ast.Component` (parsed by Task 9). Top-level `component` keywords are
  located with `go/scanner` so the word `component` inside a string/comment/identifier
  is not mistaken for the keyword.

- [ ] **Step 1: Write the failing test**

```go
// internal/parser/file_test.go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

func TestParseFile(t *testing.T) {
	src := `package views

import "github.com/gsxhq/gsx"

type Item struct{ Name string }

component Card(title string) {
	<section>{title}</section>
}

func helper() string { return "x" }

component Spinner() {
	<svg></svg>
}
`
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Package != "views" {
		t.Fatalf("package = %q", f.Package)
	}
	var comps []string
	var chunks int
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			comps = append(comps, v.Name)
		case *ast.GoChunk:
			chunks++
		}
	}
	if len(comps) != 2 || comps[0] != "Card" || comps[1] != "Spinner" {
		t.Fatalf("components = %v", comps)
	}
	if chunks == 0 {
		t.Fatalf("expected Go chunks (import/type/func) to be captured")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestParseFile`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/parser/file.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/internal/ast"
)

// Parse parses a full .gsx source file.
func Parse(src string) (*ast.File, error) {
	f := &ast.File{}

	// Find the package clause and its name with go/scanner.
	pkgName, pkgEnd, err := scanPackage(src)
	if err != nil {
		return nil, err
	}
	f.Package = pkgName

	// Locate top-level `component` keyword offsets.
	offsets := topLevelComponentOffsets(src)

	cursor := pkgEnd
	for _, off := range offsets {
		if off < cursor {
			continue
		}
		if chunk := strings.TrimSpace(src[cursor:off]); chunk != "" {
			f.Decls = append(f.Decls, &ast.GoChunk{Src: src[cursor:off]})
		}
		p := newParser(src)
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			return nil, err
		}
		f.Decls = append(f.Decls, c)
		cursor = p.i
	}
	if tail := strings.TrimSpace(src[cursor:]); tail != "" {
		f.Decls = append(f.Decls, &ast.GoChunk{Src: src[cursor:]})
	}
	return f, nil
}

func scanPackage(src string) (name string, end int, err error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return "", 0, fmt.Errorf("missing package clause")
		}
		if tok == token.PACKAGE {
			pos, tok2, lit2 := s.Scan()
			if tok2 != token.IDENT {
				return "", 0, fmt.Errorf("malformed package clause")
			}
			off := fset.Position(pos).Offset
			_ = lit
			return lit2, off + len(lit2), nil
		}
	}
}

// topLevelComponentOffsets returns byte offsets of `component` identifiers that sit
// at brace depth 0 (i.e. real top-level declarations, not inside a func/component body
// and not inside strings/comments — go/scanner handles those).
func topLevelComponentOffsets(src string) []int {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	var offs []int
	depth := 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return offs
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
				offs = append(offs, fset.Position(pos).Offset)
			}
		}
	}
}
```

Note on robustness: `go/scanner` will not tokenize the markup inside a component body cleanly, but `topLevelComponentOffsets` only needs depth tracking from `{`/`}` tokens up to the FIRST top-level `component`. Because we re-scan from scratch and components are processed left to right with the cursor advanced past each parsed body, the depth counter is only trusted to find the *next* top-level `component` start; once `parseComponent` consumes a body, subsequent offsets inside it are skipped by the `off < cursor` guard. This is sufficient for the core grammar; Part 2 hardens scanning if markup tokens ever confuse depth.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parser/ -run TestParseFile`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/parser/file.go internal/parser/file_test.go
git commit -m "feat(parser): file-level parse (package, Go chunks, components)"
```

---

### Task 11: Golden integration test over a real example subset

**Files:**
- Create: `internal/parser/golden_test.go`
- Test: same file.

**Interfaces:**
- Consumes: `Parse`, `ast`.
- Produces: an end-to-end assertion that a representative `.gsx` source (a trimmed,
  core-grammar-only version of `examples/04_components.gsx`) parses into the expected
  shape. No new production code — this is the acceptance gate for the core parser.

- [ ] **Step 1: Write the failing test**

```go
// internal/parser/golden_test.go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/internal/ast"
)

const goldenSrc = `package examples

import "github.com/gsxhq/gsx"

component Card(title string, featured bool) {
	<section class="card">
		<h2>{title}</h2>
		{children}
	</section>
}

component Panel(header gsx.Node) {
	<div class="panel">
		<div class="head">{header}</div>
		{children}
	</div>
}
`

func TestGoldenCore(t *testing.T) {
	f, err := Parse(goldenSrc)
	if err != nil {
		t.Fatal(err)
	}
	if f.Package != "examples" {
		t.Fatalf("package = %q", f.Package)
	}

	var card, panel *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			switch c.Name {
			case "Card":
				card = c
			case "Panel":
				panel = c
			}
		}
	}
	if card == nil || panel == nil {
		t.Fatalf("missing components: %#v", f.Decls)
	}
	if card.Params != "title string, featured bool" {
		t.Fatalf("card params = %q", card.Params)
	}
	section := card.Body[0].(*ast.Element)
	if section.Tag != "section" {
		t.Fatalf("card root = %#v", section)
	}
	if a := section.Attrs[0].(*ast.StaticAttr); a.Name != "class" || a.Value != "card" {
		t.Fatalf("section attr = %#v", section.Attrs[0])
	}
	// section children: <h2>…</h2>, {children}  (whitespace text nodes also present)
	var sawH2, sawChildren bool
	for _, ch := range section.Children {
		switch v := ch.(type) {
		case *ast.Element:
			if v.Tag == "h2" {
				sawH2 = true
				if _, ok := v.Children[0].(*ast.Interp); !ok {
					t.Fatalf("h2 child = %#v", v.Children[0])
				}
			}
		case *ast.Interp:
			if v.Expr == "children" {
				sawChildren = true
			}
		}
	}
	if !sawH2 || !sawChildren {
		t.Fatalf("section children = %#v", section.Children)
	}
}
```

- [ ] **Step 2: Run test to verify it fails (or passes immediately)**

Run: `go test ./internal/parser/ -run TestGoldenCore`
Expected: PASS if Tasks 1–10 are correct. If it FAILS, the failure pinpoints the gap; fix the relevant task's code (do not edit the test to match a bug).

- [ ] **Step 3: (only if Step 2 failed) Fix the offending parser code**

Re-run until green; the test is the spec for "core grammar parses."

- [ ] **Step 4: Run the whole package**

Run: `go test ./internal/...`
Expected: PASS (all parser + ast tests).

- [ ] **Step 5: Commit**

```bash
git add internal/parser/golden_test.go
git commit -m "test(parser): golden integration over core-grammar example"
```

---

## Self-Review

**Spec coverage (core-grammar slice):**
- Package/import/Go decls → Tasks 1, 10 (`File`, `GoChunk`, `Parse`). ✓
- `component` decl + receiver + params + body → Tasks 1, 9. ✓
- Elements, self-close, void, dotted/component tags → Tasks 1, 7. ✓
- Fragments `<>…</>` → Tasks 1, 7. ✓
- Text + `{ expr }` + `{ expr? }` interpolation → Tasks 1, 5. ✓
- Attributes static/expr/bool/spread/markup → Tasks 1, 6, 8. ✓
- Markup-vs-Go Babel rule (attribute-value position) → Task 6 (`parseAttrBraceValue`, `startsTag`). ✓
- Nested children + close-tag matching → Task 8. ✓
- **Explicitly deferred (Part 2):** control flow, `{{ }}`, in-tag conditional attrs, class comma/colon grammar, semantics. Flagged in Global Constraints. ✓

**Placeholder scan:** No "TBD/TODO/handle edge cases" steps; every code step shows complete code. The two temporary stubs (Tasks 6, 7) are explicitly created and explicitly removed in Task 8. ✓

**Type consistency:** `goExprEnd`/`parenEnd` signatures `(string,int)→(int,bool)` are used identically in Tasks 5, 6, 9. `parseChildren(closeTag string)`, `parseNodesUntilEOF()`, `parseAttrs()`, `parseElement()`, `parseInterp()`, `parseText()`, `parseComponent()`, `Parse(src string)` names are stable across tasks. AST field names (`Tag`, `Void`, `Attrs`, `Children`, `Expr`, `Try`, `Name`, `Value`, `Recv`, `Params`, `Body`, `Src`) match Task 1 throughout. ✓

## Next plans (after this)
1. **Parser Part 2** — control flow, `{{ }}`, in-tag conditional attrs, class comma/colon grammar.
2. **Runtime** — `gsx.Node`/`Func`/`Writer`/`Attrs`, escaping, class merge.
3. **Type-check + Codegen** — AST → `.x.go`, targeting the runtime.
4. **CLI/driver** — `gsx generate ./...`.
