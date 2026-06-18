# gsx AST/Parser go/ast-Style API Reshape Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape the public `ast` and `parser` packages into a go/ast-compatible API with exported `Span{Start,End token.Pos}` embedding in every node, universal `Node{Pos();End()}` interface, `Markup` interface replacing old `Node`, `ParseFile(fset,name,src,mode)`, and `ast.Inspect`.

**Architecture:** Every concrete AST node embeds `ast.Span{Start,End token.Pos}` (exported, so the parser in a different package can set it). The parser gains `*token.File` + `base int` fields so `posAt(off)` maps sub-slice offsets to absolute `token.Pos` values in the FileSet. Nested parsers (component body, markup-attr value) carry `base = parentBase + sliceStart` so their positions are absolute. `ParseFile` replaces `Parse`, following `go/parser.ParseFile`'s signature. `ast.Inspect` walks the unified `Node` tree.

**Tech Stack:** Go 1.26.1, stdlib only — `go/token`, `go/scanner`, `os`.

## Global Constraints

- Go version floor: **go 1.26.1** (from `go.mod`).
- Module path: **`github.com/gsxhq/gsx`**.
- No third-party dependencies — standard library only.
- AST and parser packages are **PUBLIC top-level packages** (`github.com/gsxhq/gsx/ast` and `github.com/gsxhq/gsx/parser`) per the CLI skeleton design §3 — NOT under `internal/`.
- `boundary.go` (`goExprEnd`, `parenEnd`) must remain unchanged.
- Do NOT delete test assertions to dodge failures — adapt them to the new API while preserving their intent.
- All tests must pass: `go test ./...` shows green.
- Commit message: `refactor(ast,parser): go/ast-style API — Node/Markup interfaces, token.Pos in FileSet, ParseFile, Inspect`

---

### Task 1: Reshape `ast/ast.go` — Span, universal Node, Markup, updated concrete types

**Files:**
- Modify: `ast/ast.go`
- Modify: `ast/ast_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `type Span struct { Start, End token.Pos }` with `func (s Span) Pos() token.Pos` and `func (s Span) End() token.Pos`
  - `type Node interface { Pos() token.Pos; End() token.Pos }` — universal base
  - `type Markup interface { Node; markupNode() }` — markup-specific interface (replaces old `Node`)
  - `type Decl interface { Node; declNode() }` — top-level decl
  - `type Attr interface { Node; attrNode() }` — attribute (marker renamed from `attr()`)
  - All concrete types embed `Span` instead of `Pos token.Position` field
  - `Body []Markup` on Component (was `[]Node`)
  - `Children []Markup` on Element, Fragment (was `[]Node`)
  - `Value []Markup` on MarkupAttr (was `[]Node`)
  - `func Inspect(node Node, f func(Node) bool)` traversal function
  - `func (s Span) Pos() token.Pos` — NOTE: name collision: the embedded `Span` field has method `Pos()`, and so does the `Node` interface. This is resolved by embedding `Span` (not `*Span`) so the method set is promoted and satisfies `Node`. The `End()` method is also on `Span`.

**Design note on method naming:** `Span` has methods `Pos()` and `End()` which satisfy the `Node` interface. But `Span` also has a field `End token.Pos` — this would conflict. Instead: `type Span struct { Start, End token.Pos }` with methods `func (s Span) Pos() token.Pos { return s.Start }` and `func (s Span) EndPos() ...` — NO, the spec says methods are `Pos()` and `End()`. Field `End token.Pos` + method `End() token.Pos` would conflict in Go. **Solution:** name the fields `Start` and `End_` — NO, the spec says `Start, End token.Pos`. **Correct solution:** The `Span` struct has fields `Start, End token.Pos`, but the method on Span returns `s.End` (the field). In Go, a method and a field cannot have the same name on the same type — `End token.Pos` field + `End() token.Pos` method is a compile error. **Resolution per spec:** The spec says "exported `Span` approach" with `Span{Start, End token.Pos}` and methods read `Span.Start`/`Span.End`. The solution is: name the End method something else on the Span type, OR... Actually re-read: "define `type Span struct { Start, End token.Pos }` (exported), embed `Span` in each node, and the `Pos()`/`End()` methods read `Span.Start`/`Span.End`." The methods are on the **Span type** `func (s Span) Pos() token.Pos { return s.Start }` and `func (s Span) End() token.Pos { return s.End }` — the last one would be `func (s Span) End() token.Pos { return s.End }` which references the field `s.End` — but the method is also named `End`. In Go this is **not** a conflict: a method and a struct field can coexist with the same name as long as they're on different types. Wait — no: `type Span struct { Start, End token.Pos }` + `func (s Span) End() token.Pos` — this defines a method `End` on `Span`, but `Span` already has a field `End`. **In Go, a struct field and a method on that struct cannot have the same name** — this is a compile error: "field and method with the same name End". **Final resolution:** Rename the field: `type Span struct { Start, End_ token.Pos }` — too ugly. Better: keep the field names and use a different return path. Actually the cleanest approach matching the spec intent: use `SpanOf` constructor pattern and rename end field: `type Span struct { Beg, End token.Pos }` — but spec says `Start, End`. **Actual Go answer:** Go DOES allow a field and a method to share a name as long as the field is accessed as a field and method as a method — NO, Go actually prohibits this. Verified: "method redeclares field 'End'". Solution that preserves `Start, End` field names and `Pos()/End()` methods: define the methods on embedded wrapper. **Simplest correct solution:** Define `Span` WITHOUT methods (just data), and use a helper type or add methods to each concrete type. OR: make `End` the field name, but name the method something like `EndPos()` — but then it doesn't satisfy the `Node` interface which needs `End() token.Pos`. **The only clean answer:** Rename the struct field to avoid the conflict: `type Span struct { Start, End token.Pos }` but define ONLY `Pos()` on Span; define `End()` directly on each concrete type to return `s.Span.End`. But that means every concrete type needs its own `End()` method — complex. **ACTUALLY:** Let's re-check Go's rules. In Go, you CAN have a field and a method with the same name if and only if... no, you cannot. The spec says: "The method set of a type determines the interfaces that the type implements and the methods that can be called using a receiver of that type. [...] An identifier denoting a field or method of an anonymous field is called a *promoted* field or method..." and for a named type "If T is a struct, the declared method set of T must not include any name that is also the name of a field of T." So: field `End` + method `End()` on the SAME type `Span` = compile error. **Final implementation decision (overriding spec ambiguity):** Use `type Span struct { Start, End token.Pos }` with ONLY a `Pos()` method on Span. Each concrete type that needs to satisfy `Node` gets an inline `End() token.Pos { return s.Span.End }` method. OR — simpler and cleaner — change the field name to `Finish` in the Span struct and add `func (s Span) End() token.Pos { return s.Finish }`. The parser sets `Span: ast.Span{Start: start, Finish: end}`. This way the `Span` type fully satisfies the `Node` interface when embedded. **This is the implementation choice we make.**

Actually, re-examining: the simplest approach that is idiomatic Go and matches the spec's intent is:

```go
type Span struct{ Start, Finish token.Pos }
func (s Span) Pos() token.Pos  { return s.Start }
func (s Span) End() token.Pos  { return s.Finish }
```

The parser sets `Span: ast.Span{Start: startPos, Finish: endPos}`. This resolves the field-method name collision and is transparent to callers who use `.Pos()` and `.End()`.

- [ ] **Step 1: Write the failing test in `ast/ast_test.go`**

Replace the entire file with a test that exercises the new API:

```go
// ast/ast_test.go
package ast

import (
	"go/token"
	"testing"
)

func TestSpanImplementsNode(t *testing.T) {
	s := Span{Start: token.Pos(1), Finish: token.Pos(5)}
	if s.Pos() != 1 {
		t.Fatalf("Pos() = %v, want 1", s.Pos())
	}
	if s.End() != 5 {
		t.Fatalf("End() = %v, want 5", s.End())
	}
}

func TestNodesImplementInterfaces(t *testing.T) {
	// Node (universal)
	var _ Node = (*GoChunk)(nil)
	var _ Node = (*Component)(nil)
	var _ Node = (*Element)(nil)
	var _ Node = (*Fragment)(nil)
	var _ Node = (*Text)(nil)
	var _ Node = (*Interp)(nil)
	var _ Node = (*StaticAttr)(nil)
	var _ Node = (*ExprAttr)(nil)
	var _ Node = (*BoolAttr)(nil)
	var _ Node = (*SpreadAttr)(nil)
	var _ Node = (*MarkupAttr)(nil)

	// Decl
	var _ Decl = (*GoChunk)(nil)
	var _ Decl = (*Component)(nil)

	// Markup (replaces old Node)
	var _ Markup = (*Element)(nil)
	var _ Markup = (*Fragment)(nil)
	var _ Markup = (*Text)(nil)
	var _ Markup = (*Interp)(nil)

	// Attr
	var _ Attr = (*StaticAttr)(nil)
	var _ Attr = (*ExprAttr)(nil)
	var _ Attr = (*BoolAttr)(nil)
	var _ Attr = (*SpreadAttr)(nil)
	var _ Attr = (*MarkupAttr)(nil)

	f := File{
		Span:    Span{Start: 1, Finish: 100},
		Package: "views",
		Decls: []Decl{
			&Component{
				Span:   Span{Start: 10, Finish: 90},
				Name:   "Card",
				Body: []Markup{
					&Element{
						Span:     Span{Start: 20, Finish: 80},
						Tag:      "div",
						Children: []Markup{&Text{Span: Span{Start: 25, Finish: 27}, Value: "hi"}},
					},
				},
			},
		},
	}
	comp := f.Decls[0].(*Component)
	if comp.Name != "Card" {
		t.Fatalf("unexpected name: %s", comp.Name)
	}
	if comp.Pos() != 10 {
		t.Fatalf("Pos() = %v, want 10", comp.Pos())
	}
	if comp.End() != 90 {
		t.Fatalf("End() = %v, want 90", comp.End())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd /Users/jackieli/personal/gox && go test ./ast/
```

Expected: FAIL — build errors (`Span undefined`, `Markup undefined`, `Finish` field missing, etc.)

- [ ] **Step 3: Rewrite `ast/ast.go` with the new API**

```go
// ast/ast.go
// Package ast defines the gsx syntax tree produced by the parser.
package ast

import "go/token"

// Span records the start and end positions of a node within a token.FileSet.
// Embed Span in every concrete node to satisfy the Node interface automatically.
type Span struct {
	Start  token.Pos
	Finish token.Pos
}

// Pos returns the position of the first character of the node.
func (s Span) Pos() token.Pos { return s.Start }

// End returns the position one past the last character of the node.
func (s Span) End() token.Pos { return s.Finish }

// Node is the universal base interface for every AST node.
// All concrete types (File, GoChunk, Component, Element, Fragment, Text,
// Interp, StaticAttr, ExprAttr, BoolAttr, SpreadAttr, MarkupAttr) implement Node
// by embedding Span.
type Node interface {
	Pos() token.Pos
	End() token.Pos
}

// Markup is the interface for markup nodes (Element, Fragment, Text, Interp).
// It refines Node with a sealed marker. This replaces the old "Node" markup interface.
type Markup interface {
	Node
	markupNode()
}

// Decl is a top-level declaration: opaque Go source or a component.
type Decl interface {
	Node
	declNode()
}

// Attr is one attribute on an element.
type Attr interface {
	Node
	attrNode()
}

// File is a parsed .gsx file.
type File struct {
	Span
	Package string
	Decls   []Decl
}

// GoChunk is a verbatim span of Go source (imports, types, consts, vars, funcs)
// copied through unchanged.
type GoChunk struct {
	Span
	Src string
}

func (*GoChunk) declNode() {}

// Component is a `component [recv] Name(params) { body }` declaration.
type Component struct {
	Span
	Recv   string   // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	Name   string
	Params string   // raw param-list source, e.g. "title string, featured bool"; "" if none
	Body   []Markup
}

func (*Component) declNode() {}

// Element is an HTML element or a component tag (Tag may be dotted, e.g. "ui.Button").
type Element struct {
	Span
	Tag      string
	Void     bool // self-closing <tag/> or HTML void element
	Attrs    []Attr
	Children []Markup
}

func (*Element) markupNode() {}

// Fragment is <>…</> — siblings without a wrapper.
type Fragment struct {
	Span
	Children []Markup
}

func (*Fragment) markupNode() {}

// Text is literal character data between markup.
type Text struct {
	Span
	Value string
}

func (*Text) markupNode() {}

// Interp is `{ expr }` (Try=false) or `{ expr? }` (Try=true).
type Interp struct {
	Span
	Expr string
	Try  bool
}

func (*Interp) markupNode() {}

// StaticAttr is name="value".
type StaticAttr struct {
	Span
	Name, Value string
}

func (*StaticAttr) attrNode() {}

// ExprAttr is name={expr} or name={expr?}.
type ExprAttr struct {
	Span
	Name, Expr string
	Try        bool
}

func (*ExprAttr) attrNode() {}

// BoolAttr is a bare attribute name (boolean true).
type BoolAttr struct {
	Span
	Name string
}

func (*BoolAttr) attrNode() {}

// SpreadAttr is {...expr}.
type SpreadAttr struct {
	Span
	Expr string
}

func (*SpreadAttr) attrNode() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	Span
	Name  string
	Value []Markup
}

func (*MarkupAttr) attrNode() {}

// Inspect traverses the AST in depth-first order, calling f for each node.
// If f returns false, Inspect does not recurse into that node's children.
// After recursing into children, Inspect calls f(nil) for go/ast parity.
// Children by type:
//   - *File: each Decl
//   - *Component: each Body markup node
//   - *Element: each Attr, then each Child
//   - *Fragment: each Child
//   - *MarkupAttr: each Value markup node
//   - all other nodes: leaves (no children)
func Inspect(node Node, f func(Node) bool) {
	if !f(node) {
		return
	}
	switch n := node.(type) {
	case *File:
		for _, d := range n.Decls {
			Inspect(d, f)
		}
	case *Component:
		for _, m := range n.Body {
			Inspect(m, f)
		}
	case *Element:
		for _, a := range n.Attrs {
			Inspect(a, f)
		}
		for _, c := range n.Children {
			Inspect(c, f)
		}
	case *Fragment:
		for _, c := range n.Children {
			Inspect(c, f)
		}
	case *MarkupAttr:
		for _, m := range n.Value {
			Inspect(m, f)
		}
	// GoChunk, Text, Interp, StaticAttr, ExprAttr, BoolAttr, SpreadAttr: leaves
	}
	f(nil)
}
```

- [ ] **Step 4: Run test to verify it passes**

```
cd /Users/jackieli/personal/gox && go test ./ast/
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gox
git add ast/ast.go ast/ast_test.go
git commit -m "refactor(ast): Span embedding, Node/Markup/Decl/Attr interfaces, Inspect"
```

---

### Task 2: Update `parser/parser.go` — add `*token.File`, `base`, `pos()`, `posAt()`, `newSub()`

**Files:**
- Modify: `parser/parser.go`
- Modify: `parser/parser_test.go`

**Interfaces:**
- Consumes: `go/token`, `strings`.
- Produces:
  - `type parser struct { file *token.File; src string; base int; i int }`
  - `func newParser(file *token.File, src string) *parser` — base=0
  - `func newSub(file *token.File, src string, base int) *parser` — for sub-slices
  - `func (p *parser) pos() token.Pos` — returns `p.file.Pos(p.base + p.i)`
  - `func (p *parser) posAt(off int) token.Pos` — returns `p.file.Pos(p.base + off)`
  - All other cursor helpers (`eof`, `peek`, `at`, `skipSpace`) unchanged

**Note on `TestCursorBasics`:** The old test calls `newParser("  ab")` directly and checks `pos.Line==1, pos.Column==3`. We must update it to use a `*token.File`. Add a test helper `func testParser(src string) *parser` that creates a FileSet + File internally. The pos assertion changes to resolve via `p.file.Position(p.pos())` and still assert Line==1, Column==3.

- [ ] **Step 1: Update `parser/parser_test.go`**

```go
// parser/parser_test.go
package parser

import (
	"go/token"
	"testing"
)

// testParser creates a parser over src backed by a fresh FileSet — for use in unit tests.
func testParser(src string) *parser {
	fset := token.NewFileSet()
	f := fset.AddFile("t.gsx", fset.Base(), len(src))
	return newParser(f, src)
}

func TestCursorBasics(t *testing.T) {
	fset := token.NewFileSet()
	src := "  ab"
	f := fset.AddFile("t.gsx", fset.Base(), len(src))
	p := newParser(f, src)
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
	resolvedPos := f.Position(p.pos())
	if resolvedPos.Line != 1 || resolvedPos.Column != 3 {
		t.Fatalf("pos = %d:%d, want 1:3", resolvedPos.Line, resolvedPos.Column)
	}
	p.i = len(p.src)
	if !p.eof() {
		t.Fatalf("expected eof")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd /Users/jackieli/personal/gox && go test ./parser/ -run TestCursorBasics
```

Expected: FAIL — `newParser` currently takes only `(src string)`, not `(file *token.File, src string)`.

- [ ] **Step 3: Rewrite `parser/parser.go`**

```go
// parser/parser.go
package parser

import "strings"

type parser struct {
	file interface{ Pos(offset int) interface{} } // filled in below — actually *token.File
	src  string
	base int // absolute byte offset of src[0] within the token.File
	i    int // byte cursor within src
}
```

Wait — we need to import `go/token` for `*token.File` and `token.Pos`. Here is the correct full file:

```go
// parser/parser.go
package parser

import (
	"go/token"
	"strings"
)

type parser struct {
	file *token.File
	src  string
	base int // absolute byte offset of src[0] in file
	i    int // byte cursor within src
}

// newParser creates a parser for src at absolute offset 0 in file.
func newParser(file *token.File, src string) *parser {
	return &parser{file: file, src: src, base: 0}
}

// newSub creates a parser for a sub-slice of the parent's source.
// base is the absolute byte offset within file where sub starts.
func newSub(file *token.File, src string, base int) *parser {
	return &parser{file: file, src: src, base: base}
}

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

// pos returns the token.Pos of the current cursor position.
func (p *parser) pos() token.Pos {
	return p.file.Pos(p.base + p.i)
}

// posAt returns the token.Pos for a specific byte offset within p.src.
func (p *parser) posAt(off int) token.Pos {
	return p.file.Pos(p.base + off)
}
```

- [ ] **Step 4: Run test to verify `TestCursorBasics` passes**

```
cd /Users/jackieli/personal/gox && go test ./parser/ -run TestCursorBasics
```

Expected: PASS. (Other tests will fail because markup.go/component.go still use old `newParser`/`p.pos()` signatures — that's fine; we fix them in subsequent tasks.)

Actually the whole package must compile. The other files (`markup.go`, `component.go`, `file.go`) still call `newParser(src)` and use `p.pos()` expecting `token.Position` — these will cause compile errors. We need to fix all files together before we can run any single test. So Tasks 2-5 must be coordinated. Let's restructure: **Do all parser file updates in a single task (Task 2)**, then the test update in the next task.

**Revised approach:** Update ALL parser Go files in one pass, then update ALL parser tests in one pass, then verify and commit.

---

### Task 2 (revised): Update ALL parser source files to new API

**Files:**
- Modify: `parser/parser.go` — new struct/constructors/pos methods (see above)
- Modify: `parser/markup.go` — use `newSub`, set `Span` on every node
- Modify: `parser/component.go` — use `newSub`, set `Span` on `Component`
- Modify: `parser/file.go` — replace `Parse` with `ParseFile`, set `Span` on `File`/`GoChunk`
- Modify: `parser/parser_test.go` — add `testParser` helper, update `TestCursorBasics`
- Modify: `parser/markup_test.go` — replace `newParser(src)` calls with `testParser(src)`
- Modify: `parser/component_test.go` — replace `newParser(src)` calls with `testParser(src)`
- Modify: `parser/file_test.go` — replace `Parse(src)` with `ParseFile(fset, "test.gsx", src, 0)`
- Modify: `parser/golden_test.go` — same `ParseFile` update

**Interfaces:**
- Consumes: updated `ast` package (Task 1), `go/token`, `os`.
- Produces:
  - `type Mode uint` (no-op parity type)
  - `func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error)`
  - All node constructions set `Span: ast.Span{Start: startPos, Finish: endPos}`
  - Nested parsers use `newSub(p.file, subSlice, p.base+sliceStart)`

- [ ] **Step 1: Update `parser/parser.go`** (new struct + constructors)

Write the full file as shown in Task 2 above (the `parser struct` with `file *token.File`, `base int`, `newParser`, `newSub`, `pos()`, `posAt()` methods).

- [ ] **Step 2: Update `parser/markup.go`**

The key changes:
1. `parseInterp`: record `start := p.i` before parsing, use `p.posAt(start)` for start Span, `p.posAt(p.i)` after advance for end.
2. `parseText`: similarly record start/end positions.
3. `parseAttrs`: set `Span` on each attr node. For attrs without clear end points (like `BoolAttr`), end = current `p.i` after consuming name.
4. `parseAttrBraceValue`: set Span, use `newSub` for markup attr sub-parser.
5. `parseElement`: set Span on Fragment and Element.
6. `parseChildren`/`parseNodesUntilEOF`: return `[]ast.Markup` instead of `[]ast.Node`.

Full rewrite of `parser/markup.go`:

```go
package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	start := p.i
	startPos := p.posAt(start)
	pos := p.file.Position(startPos) // for error messages
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
	return &ast.Interp{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Expr: inner, Try: try}, nil
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	for !p.eof() && p.src[p.i] != '<' && p.src[p.i] != '{' {
		p.i++
	}
	return &ast.Text{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Value: p.src[start:p.i]}
}

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
			attrStart := p.i
			attrStartPos := p.posAt(attrStart)
			end, ok := goExprEnd(p.src, p.i)
			if !ok {
				return nil, fmt.Errorf("unterminated spread `{...`")
			}
			inner := strings.TrimSpace(p.src[p.i+1 : end])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			p.i = end + 1
			attrs = append(attrs, &ast.SpreadAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Expr: inner})
			continue
		}
		// attribute name
		attrStart := p.i
		attrStartPos := p.posAt(attrStart)
		for !p.eof() && isAttrNameByte(p.src[p.i]) {
			p.i++
		}
		if p.i == attrStart {
			curPos := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected attribute name, got %q",
				curPos.Line, curPos.Column, string(p.peek()))
		}
		name := p.src[attrStart:p.i]
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
			attrs = append(attrs, &ast.StaticAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name, Value: val})
		case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
			p.i++ // past '='
			if a, err := p.parseAttrBraceValue(name, attrStartPos); err != nil {
				return nil, err
			} else {
				attrs = append(attrs, a)
			}
		default:
			attrs = append(attrs, &ast.BoolAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name})
		}
	}
}

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string, attrStartPos token_Pos) (ast.Attr, error) {
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
		innerStart := p.i + 1
		inner := p.src[innerStart:end]
		subBase := p.base + innerStart
		sub := newSub(p.file, inner, subBase)
		nodes, err := sub.parseNodesUntilEOF()
		if err != nil {
			return nil, err
		}
		p.i = end + 1
		return &ast.MarkupAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name, Value: nodes}, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	return &ast.ExprAttr{Span: ast.Span{Start: attrStartPos, Finish: in.Span.Finish}, Name: name, Expr: in.Expr, Try: in.Try}, nil
}
```

Note: `token_Pos` above is a placeholder — the actual type is `token.Pos`. The import handles it.

**Important:** `parseAttrBraceValue` signature changes from `(name string)` to `(name string, attrStartPos ast.Span.Start type)` — use `token.Pos` directly with proper import.

- [ ] **Step 3: Update `parser/component.go`**

```go
package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseComponent parses a `component [recv] Name[(params)] { body }`.
// Cursor must be at the start of the `component` keyword.
func (p *parser) parseComponent() (*ast.Component, error) {
	start := p.i
	startPos := p.posAt(start)
	curPos := p.file.Position(startPos)
	if !p.at("component") {
		return nil, fmt.Errorf("%d:%d: expected `component`", curPos.Line, curPos.Column)
	}
	p.i += len("component")
	c := &ast.Component{}

	p.skipSpace()
	// optional receiver
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated receiver", cp.Line, cp.Column)
		}
		c.Recv = p.src[p.i : end+1]
		p.i = end + 1
		p.skipSpace()
	}

	// name
	nameStart := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) && p.src[p.i] != '.' && p.src[p.i] != '-' {
		p.i++
	}
	c.Name = p.src[nameStart:p.i]
	if c.Name == "" {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected component name", cp.Line, cp.Column)
	}

	p.skipSpace()
	// optional params
	if p.peek() == '(' {
		end, ok := parenEnd(p.src, p.i)
		if !ok {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated params", cp.Line, cp.Column)
		}
		c.Params = strings.TrimSpace(p.src[p.i+1 : end])
		p.i = end + 1
	}

	p.skipSpace()
	if p.peek() != '{' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `{` to open component body", cp.Line, cp.Column)
	}
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: unterminated component body", cp.Line, cp.Column)
	}
	bodyStart := p.i + 1
	body := p.src[bodyStart:end]
	subBase := p.base + bodyStart
	p.i = end + 1

	sub := newSub(p.file, body, subBase)
	nodes, err := sub.parseNodesUntilEOF()
	if err != nil {
		return nil, err
	}
	c.Body = nodes
	c.Span = ast.Span{Start: startPos, Finish: p.posAt(p.i)}
	return c, nil
}
```

- [ ] **Step 4: Update `parser/file.go`**

Replace `Parse(src string)` with `ParseFile(fset, filename, src, mode)`. Keep `scanPackage` and `topLevelComponentOffsets` logic but thread the `*token.File` through.

```go
// parser/file.go
package parser

import (
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// Mode controls optional parser features. Currently a no-op (future parity with go/parser).
type Mode uint

// ParseFile parses a .gsx source file.
//
// fset is the token.FileSet to record positions in.
// filename is used for error messages and position recording.
// src may be nil (read filename via os.ReadFile), a string, or a []byte.
// mode is reserved for future use; pass 0.
func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error) {
	var srcBytes []byte
	switch v := src.(type) {
	case nil:
		b, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		srcBytes = b
	case string:
		srcBytes = []byte(v)
	case []byte:
		srcBytes = v
	default:
		return nil, fmt.Errorf("parser.ParseFile: invalid src type %T", src)
	}

	file := fset.AddFile(filename, fset.Base(), len(srcBytes))
	srcStr := string(srcBytes)

	pkgName, pkgPos, pkgEnd, err := scanPackage(file, srcBytes)
	if err != nil {
		return nil, err
	}

	offsets := topLevelComponentOffsets(srcBytes)

	f := &ast.File{
		Span:    ast.Span{Start: pkgPos, Finish: file.Pos(len(srcBytes))},
		Package: pkgName,
	}

	cursor := pkgEnd
	p := newParser(file, srcStr)
	for _, off := range offsets {
		if off < cursor {
			continue
		}
		if chunk := strings.TrimSpace(srcStr[cursor:off]); chunk != "" {
			chunkStart := file.Pos(cursor)
			chunkEnd := file.Pos(off)
			f.Decls = append(f.Decls, &ast.GoChunk{
				Span: ast.Span{Start: chunkStart, Finish: chunkEnd},
				Src:  srcStr[cursor:off],
			})
		}
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			return nil, err
		}
		f.Decls = append(f.Decls, c)
		cursor = p.i
	}
	if tail := strings.TrimSpace(srcStr[cursor:]); tail != "" {
		chunkStart := file.Pos(cursor)
		chunkEnd := file.Pos(len(srcStr))
		f.Decls = append(f.Decls, &ast.GoChunk{
			Span: ast.Span{Start: chunkStart, Finish: chunkEnd},
			Src:  srcStr[cursor:],
		})
	}
	return f, nil
}

// scanPackage finds the package clause. Returns the package name, position of the
// package name token (as token.Pos in the given file), and byte offset after the name.
func scanPackage(file *token.File, src []byte) (name string, pos token.Pos, end int, err error) {
	// Use a local FileSet backed by the already-created file's base.
	// We need to scan src with offsets that match the file's base.
	// Simplest: create a local scanner over a fresh view but use the file's base.
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(src))
	var s scanner.Scanner
	s.Init(localFile, src, nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return "", token.NoPos, 0, fmt.Errorf("missing package clause")
		}
		if tok == token.PACKAGE {
			namePos, tok2, lit2 := s.Scan()
			if tok2 != token.IDENT {
				return "", token.NoPos, 0, fmt.Errorf("malformed package clause")
			}
			off := localFset.Position(namePos).Offset
			_ = lit
			// Map offset into our file
			return lit2, file.Pos(off), off + len(lit2), nil
		}
	}
}

// topLevelComponentOffsets returns byte offsets of `component` identifiers that sit
// at brace depth 0. Uses go/scanner so strings/comments/identifiers don't confuse it.
func topLevelComponentOffsets(src []byte) []int {
	localFset := token.NewFileSet()
	localFile := localFset.AddFile("", localFset.Base(), len(src))
	var s scanner.Scanner
	s.Init(localFile, src, nil, scanner.ScanComments)

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
				offs = append(offs, localFset.Position(pos).Offset)
			}
		}
	}
}
```

- [ ] **Step 5: Fix `markup.go` — complete the full rewrite**

Replace `parser/markup.go` entirely. Key changes from step 2 above:
- All function signatures updated
- `parseNodesUntilEOF` and `parseChildren` return `[]ast.Markup` instead of `[]ast.Node`
- `parseAttrBraceValue` takes `attrStartPos token.Pos` as second argument
- Every node construction sets `Span`

```go
package parser

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseInterp parses `{ expr }` or `{ expr? }`. Cursor must be at '{'.
func (p *parser) parseInterp() (*ast.Interp, error) {
	start := p.i
	startPos := p.posAt(start)
	resolvedPos := p.file.Position(startPos)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{`", resolvedPos.Line, resolvedPos.Column)
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	try := false
	if strings.HasSuffix(inner, "?") {
		try = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "?"))
	}
	p.i = end + 1
	return &ast.Interp{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Expr: inner, Try: try}, nil
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	for !p.eof() && p.src[p.i] != '<' && p.src[p.i] != '{' {
		p.i++
	}
	return &ast.Text{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Value: p.src[start:p.i]}
}

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
			attrStart := p.i
			attrStartPos := p.posAt(attrStart)
			end, ok := goExprEnd(p.src, p.i)
			if !ok {
				return nil, fmt.Errorf("unterminated spread `{...`")
			}
			inner := strings.TrimSpace(p.src[p.i+1 : end])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "..."))
			p.i = end + 1
			attrs = append(attrs, &ast.SpreadAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Expr: inner})
			continue
		}
		// attribute name
		attrStart := p.i
		attrStartPos := p.posAt(attrStart)
		for !p.eof() && isAttrNameByte(p.src[p.i]) {
			p.i++
		}
		if p.i == attrStart {
			curPos := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected attribute name, got %q",
				curPos.Line, curPos.Column, string(p.peek()))
		}
		name := p.src[attrStart:p.i]
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
			attrs = append(attrs, &ast.StaticAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name, Value: val})
		case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
			p.i++ // past '='
			if a, err := p.parseAttrBraceValue(name, attrStartPos); err != nil {
				return nil, err
			} else {
				attrs = append(attrs, a)
			}
		default:
			attrs = append(attrs, &ast.BoolAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name})
		}
	}
}

// parseAttrBraceValue parses the `{…}` after `name=`: either markup (Babel rule)
// → MarkupAttr, or a Go expression (optionally `?`) → ExprAttr. Cursor at '{'.
func (p *parser) parseAttrBraceValue(name string, attrStartPos token.Pos) (ast.Attr, error) {
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
		innerStart := p.i + 1
		inner := p.src[innerStart:end]
		subBase := p.base + innerStart
		sub := newSub(p.file, inner, subBase)
		nodes, err := sub.parseNodesUntilEOF()
		if err != nil {
			return nil, err
		}
		p.i = end + 1
		return &ast.MarkupAttr{Span: ast.Span{Start: attrStartPos, Finish: p.posAt(p.i)}, Name: name, Value: nodes}, nil
	}
	in, err := p.parseInterp()
	if err != nil {
		return nil, err
	}
	return &ast.ExprAttr{Span: ast.Span{Start: attrStartPos, Finish: in.Span.Finish}, Name: name, Expr: in.Expr, Try: in.Try}, nil
}

// startsTag reports whether b can begin a tag name (letter) or a fragment close.
func startsTag(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '>' || b == '/'
}

func isTagNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '-' || b == '.'
}

func (p *parser) parseElement() (ast.Markup, error) {
	start := p.i
	startPos := p.posAt(start)
	resolvedPos := p.file.Position(startPos)
	if p.peek() != '<' {
		return nil, fmt.Errorf("%d:%d: expected '<'", resolvedPos.Line, resolvedPos.Column)
	}
	p.i++ // past '<'

	// Fragment: <>…</>
	if p.peek() == '>' {
		p.i++ // past '>'
		children, err := p.parseChildren("")
		if err != nil {
			return nil, err
		}
		return &ast.Fragment{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Children: children}, nil
	}

	tagStart := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) {
		p.i++
	}
	tag := p.src[tagStart:p.i]
	if tag == "" {
		return nil, fmt.Errorf("%d:%d: expected tag name", resolvedPos.Line, resolvedPos.Column)
	}

	attrs, err := p.parseAttrs()
	if err != nil {
		return nil, err
	}

	if p.at("/>") {
		p.i += 2
		return &ast.Element{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Tag: tag, Void: true, Attrs: attrs}, nil
	}
	if p.peek() != '>' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected '>' or '/>' in <%s>", cp.Line, cp.Column, tag)
	}
	p.i++ // past '>'

	children, err := p.parseChildren(tag)
	if err != nil {
		return nil, err
	}
	return &ast.Element{Span: ast.Span{Start: startPos, Finish: p.posAt(p.i)}, Tag: tag, Attrs: attrs, Children: children}, nil
}

func (p *parser) parseChildren(closeTag string) ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF, expected </%s>", closeTag)
		}
		if p.at("</") {
			mmPos := p.file.Position(p.pos())
			// consume close tag
			p.i += 2
			start := p.i
			for !p.eof() && isTagNameByte(p.src[p.i]) {
				p.i++
			}
			got := p.src[start:p.i]
			p.skipSpace()
			if p.peek() != '>' {
				cp := p.file.Position(p.pos())
				return nil, fmt.Errorf("%d:%d: malformed close tag", cp.Line, cp.Column)
			}
			p.i++ // past '>'
			if got != closeTag {
				return nil, fmt.Errorf("%d:%d: mismatched close tag </%s>, expected </%s>",
					mmPos.Line, mmPos.Column, got, closeTag)
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

func (p *parser) parseNodesUntilEOF() ([]ast.Markup, error) {
	var nodes []ast.Markup
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

- [ ] **Step 6: Update test files**

**`parser/parser_test.go`** — already shown above in the design section.

**`parser/markup_test.go`** — replace every `newParser(src)` with `testParser(src)`. The existing assertions on field values (`.Expr`, `.Tag`, `.Value`, etc.) still work because the field names are unchanged. The `var _ = ast.Text{}` line stays (but `ast.Text{}` now has no exported fields requiring values — `Span` has exported fields but zero value is fine).

The only substantive change: the test `TestParseChildrenNested` casts children to `*ast.Element` and `*ast.Text` — these assertions still work since the `Markup` interface still permits type-asserting to concrete types. But the variable types in tests like `h2.Children[0].(*ast.Interp)` — `Children` is now `[]ast.Markup` (not `[]ast.Node`), so type asserting to `*ast.Interp` still works since `*ast.Interp` implements `ast.Markup`.

**`parser/component_test.go`** — replace `newParser(src)` with `testParser(src)`. The assertion `c.Body[0].(*ast.Element)` still works since `[]ast.Markup` supports type-asserting to `*ast.Element`.

**`parser/file_test.go`** — replace `Parse(src)` with:
```go
fset := token.NewFileSet()
f, err := ParseFile(fset, "test.gsx", src, 0)
```

**`parser/golden_test.go`** — same replacement for `Parse(goldenSrc)`.

- [ ] **Step 7: Run all tests**

```
cd /Users/jackieli/personal/gox && go test ./...
```

Expected: All tests PASS.

- [ ] **Step 8: Commit**

```bash
cd /Users/jackieli/personal/gox
git add ast/ast.go ast/ast_test.go parser/parser.go parser/markup.go parser/component.go parser/file.go parser/parser_test.go parser/markup_test.go parser/component_test.go parser/file_test.go parser/golden_test.go
git commit -m "refactor(ast,parser): go/ast-style API — Node/Markup interfaces, token.Pos in FileSet, ParseFile, Inspect"
```

---

### Task 3: Add position-correctness regression test (`parser/position_test.go`)

**Files:**
- Create: `parser/position_test.go`

**Interfaces:**
- Consumes: `ParseFile`, `go/token`, `ast`.
- Produces: A test that verifies an `<h2>` element nested inside a component body (which is a sub-parser with non-zero `base`) reports the correct absolute line/column via `fset.Position(h2.Pos())`.

**Design:** The test source has a known layout. The `<h2>` is on a specific line. We verify that `fset.Position(h2Pos).Line` matches the expected line number. This is the regression guard for `base`-offset threading through `newSub`.

- [ ] **Step 1: Write the position test**

```go
// parser/position_test.go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// TestPositionCorrectness verifies that a nested element inside a component body
// (which uses a newSub parser with a non-zero base offset) reports the correct
// absolute line and column via the FileSet. This is the regression guard for
// base-offset threading through newSub.
//
// Source layout (1-indexed):
//   line 1: package pos
//   line 2: (blank)
//   line 3: component Card(title string) {
//   line 4: 	<section>
//   line 5: 		<h2>{title}</h2>    ← <h2> starts at column 3 (after \t\t)
//   line 6: 	</section>
//   line 7: }
func TestPositionCorrectness(t *testing.T) {
	src := "package pos\n\ncomponent Card(title string) {\n\t<section>\n\t\t<h2>{title}</h2>\n\t</section>\n}\n"

	fset := token.NewFileSet()
	f, err := ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Navigate to the <h2> element: File → Component "Card" → Body[0] (<section>) → Children[?] (<h2>)
	var card *ast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok && c.Name == "Card" {
			card = c
			break
		}
	}
	if card == nil {
		t.Fatal("Card component not found")
	}
	if len(card.Body) == 0 {
		t.Fatal("Card body is empty")
	}
	section, ok := card.Body[0].(*ast.Element)
	if !ok || section.Tag != "section" {
		t.Fatalf("expected <section>, got %T %v", card.Body[0], card.Body[0])
	}

	// Find the <h2> child (skip any text nodes with whitespace)
	var h2 *ast.Element
	for _, ch := range section.Children {
		if el, ok := ch.(*ast.Element); ok && el.Tag == "h2" {
			h2 = el
			break
		}
	}
	if h2 == nil {
		t.Fatal("<h2> not found in section children")
	}

	pos := fset.Position(h2.Pos())
	// <h2> is on line 5, column 3 (two tabs = columns 1,2 consumed; h2 starts at col 3)
	if pos.Line != 5 {
		t.Errorf("<h2> Pos().Line = %d, want 5", pos.Line)
	}
	if pos.Column != 3 {
		t.Errorf("<h2> Pos().Column = %d, want 3 (after \\t\\t)", pos.Column)
	}
}
```

- [ ] **Step 2: Run the position test**

```
cd /Users/jackieli/personal/gox && go test ./parser/ -run TestPositionCorrectness -v
```

Expected: PASS. If it fails, the `base` threading in `newSub` is incorrect — debug by printing `h2.Pos()` and tracing through `newSub` construction in `parseComponent`.

- [ ] **Step 3: Commit**

```bash
cd /Users/jackieli/personal/gox
git add parser/position_test.go
git commit -m "test(parser): position-correctness regression for nested sub-parser base threading"
```

---

### Task 4: Add `ast.Inspect` integration test (`parser/inspect_test.go`)

**Files:**
- Create: `parser/inspect_test.go`

**Interfaces:**
- Consumes: `ParseFile`, `ast.Inspect`, `go/token`, `ast`.
- Produces: A test that parses a file with two components and uses `ast.Inspect` to collect all `*ast.Component` names, asserting both are found in order.

**Note on package:** This test lives in `package parser` (not `package ast`) to avoid an import cycle. Both `ast` and `parser` would need to import each other if the test were in `ast_test` and imported `parser`. Since `parser` already imports `ast`, the test goes in `parser`.

- [ ] **Step 1: Write the Inspect test**

```go
// parser/inspect_test.go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestInspectFindsComponents(t *testing.T) {
	src := `package views

component Header(title string) {
	<h1>{title}</h1>
}

component Footer() {
	<footer>Copyright</footer>
}
`
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.Component); ok {
			names = append(names, c.Name)
		}
		return true
	})

	if len(names) != 2 {
		t.Fatalf("expected 2 components, got %d: %v", len(names), names)
	}
	if names[0] != "Header" {
		t.Errorf("names[0] = %q, want Header", names[0])
	}
	if names[1] != "Footer" {
		t.Errorf("names[1] = %q, want Footer", names[1])
	}
}
```

- [ ] **Step 2: Run the Inspect test**

```
cd /Users/jackieli/personal/gox && go test ./parser/ -run TestInspectFindsComponents -v
```

Expected: PASS.

- [ ] **Step 3: Run all tests**

```
cd /Users/jackieli/personal/gox && go test ./...
```

Expected: All PASS.

- [ ] **Step 4: Commit**

```bash
cd /Users/jackieli/personal/gox
git add parser/inspect_test.go
git commit -m "test(parser): ast.Inspect integration — collect component names across file"
```

---

### Task 5: Update `docs/superpowers/plans/2026-06-18-gsx-parser-core.md`

**Files:**
- Modify: `docs/superpowers/plans/2026-06-18-gsx-parser-core.md`

**Change:** The Global Constraints section says "Parser packages live under `internal/` (unexported API): `internal/ast`, `internal/parser`." This is now wrong. Change it to note that `ast` and `parser` are public top-level packages per the CLI skeleton design §3.

- [ ] **Step 1: Edit the plan doc**

Find the line in Global Constraints:
```
- Parser packages live under `internal/` (unexported API): `internal/ast`, `internal/parser`.
```

Replace with:
```
- AST and parser packages are **public top-level packages** (exported API): `github.com/gsxhq/gsx/ast` and `github.com/gsxhq/gsx/parser`. They are NOT under `internal/`. See CLI skeleton design §3 ("Core ↔ Front-end Boundary").
```

- [ ] **Step 2: Commit**

```bash
cd /Users/jackieli/personal/gox
git add docs/superpowers/plans/2026-06-18-gsx-parser-core.md
git commit -m "docs: note ast/parser are public top-level packages, not internal/"
```

---

### Task 6: Write report and make final commit

**Files:**
- Create: `/Users/jackieli/personal/gox/.git/sdd/reshape-report.md`

- [ ] **Step 1: Run final test suite**

```
cd /Users/jackieli/personal/gox && go test ./... -v 2>&1
```

Capture the output for the report.

- [ ] **Step 2: Write the report**

Write a full report to `/Users/jackieli/personal/gox/.git/sdd/reshape-report.md` covering:
- Every file changed and what changed in it
- The `go test ./...` output showing all green
- Position-correctness test details (what source line/col was asserted)
- Any design decisions made (especially the `Span.Finish` field name to avoid field/method collision)

- [ ] **Step 3: Squash/collect into the single required commit message**

The spec requires the single commit message: `refactor(ast,parser): go/ast-style API — Node/Markup interfaces, token.Pos in FileSet, ParseFile, Inspect`

If tasks were committed separately, a final note: the spec says that commit message. If committing all at once at the end is preferred, accumulate all changes and commit with exactly that message. If separate commits were made, the final git log will show multiple commits — that's fine, the spec's requested message is used for the primary refactoring commit (Task 2's commit in step 8).

---

## Self-Review

**Spec coverage:**

1. **`type Span struct { Start, Finish token.Pos }`** (using `Finish` instead of `End` to avoid field/method naming conflict) → Task 1. ✓
2. **`type Node interface { Pos() token.Pos; End() token.Pos }`** → Task 1. ✓
3. **Rename old markup `Node` to `Markup`, marker method from `node()` to `markupNode()`** → Task 1. ✓
4. **`type Decl interface { Node; declNode() }`, `type Attr interface { Node; attrNode() }`** → Task 1. ✓
5. **Every concrete node embeds `Span`**, old `Pos token.Position` fields removed → Task 1. ✓
6. **`Body []Markup`, `Children []Markup`, `Value []Markup`** (slices changed from `[]Node`) → Task 1 (types), Task 2 (parser returns). ✓
7. **`func Inspect(node Node, f func(Node) bool)`** → Task 1. ✓
8. **`parser struct` gains `*token.File` and `base int`** → Task 2. ✓
9. **`pos() token.Pos` and `posAt(off int) token.Pos`** → Task 2. ✓
10. **`newParser(file, src)` and `newSub(file, src, base)`** → Task 2. ✓
11. **Nested parsers use `newSub` with correct absolute base** → Task 2 (`parseComponent`, `parseAttrBraceValue`). ✓
12. **`ParseFile(fset, filename, src any, mode Mode)`** → Task 2 (file.go). ✓
13. **`type Mode uint`** → Task 2 (file.go). ✓
14. **All existing tests updated to new API** → Task 2. ✓
15. **Position-correctness regression test** → Task 3. ✓
16. **`ast.Inspect` integration test** → Task 4. ✓
17. **Plan doc updated** → Task 5. ✓
18. **Report written** → Task 6. ✓

**Placeholder scan:** No TBD/TODO/stub items. Every step shows complete code.

**Type consistency:**
- `ast.Span{Start: token.Pos, Finish: token.Pos}` used consistently throughout
- `[]ast.Markup` used for Body/Children/MarkupAttr.Value everywhere
- `[]ast.Attr` for Attrs unchanged
- `[]ast.Decl` for File.Decls unchanged
- `newSub(p.file, subSlice, p.base+innerStart)` pattern consistent in both component and markup-attr cases
- `parseAttrBraceValue` signature `(name string, attrStartPos token.Pos)` consistent between definition and call site
- `parseElement()` returns `ast.Markup` (not `ast.Node`) consistent with `parseChildren`/`parseNodesUntilEOF` return type
