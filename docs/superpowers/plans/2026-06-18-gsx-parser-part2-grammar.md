# gsx Parser — Part 2 Grammar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the gsx parser from core markup to the full "Part 2" body/attribute grammar — `{{ }}` Go-statement blocks, `{ if/for/switch }` control flow, in-tag `{ if … }` conditional attributes, and composable `class`/`style` `{ … }` contribution lists — producing typed AST nodes consistent with the existing public go/ast-style API.

**Architecture:** Hand-written recursive descent, continuing the existing parser. The `{`-leading dispatch in child contexts is centralized in one helper (`parseBraceNode`) so component bodies, element children, and control-flow bodies share it. Go-bearing regions (`{{ }}` content, control-flow headers, case lists, class contributions) are located with `go/scanner` brace/bracket-depth counting — never by scanning markup prose. Markup bodies are parsed character-by-character by gsx, terminating control-flow bodies on a depth-0 `}`. Each new construct gets a dedicated typed AST node mirroring `go/ast`'s separate-node style (`IfMarkup`, `ForMarkup`, `SwitchMarkup`/`CaseClause`, `GoBlock`, `CondAttr`, `ClassAttr`).

**Tech Stack:** Go 1.26.1, standard library only. Packages: `github.com/gsxhq/gsx/ast`, `github.com/gsxhq/gsx/parser`, `github.com/gsxhq/gsx/internal/corpus`. `go/scanner` + `go/token` for Go-boundary finding.

## Global Constraints

- **Standard library only** — no third-party dependencies, ever.
- **Module path** `github.com/gsxhq/gsx`; source extension `.gsx`; runtime symbols `gsx.*`.
- **Public API parity with go/ast** — every AST node embeds the unexported `span` (positions via `Pos()`/`End()` only); the parser records positions through `ast.SetSpan`. New exported nodes must be registered in `SetSpan`, `Inspect`, and `Fprint`.
- **Parser is fail-fast** — it stops at the first error and returns `(nil, err)`. Error strings are `line:col: message` using rune columns from `p.file.Position(pos)`.
- **Unexported by default** — new helper functions/types that need no serialization start lowercase.
- **The parser captures Go as opaque text** — conditions, clauses, switch tags, case lists, `{{ }}` bodies, and class contributions are stored as trimmed source strings; type resolution is a later (codegen) concern. The parser does not type-check them.
- **gofmt + go vet clean**; every task ends green via `go test ./...`.

### Disambiguation rule (the three brace forms, by leading token)

Inside a markup/child context a `{` is resolved in this order:

1. `{{` → **GoBlock** (`{{ stmt }}`).
2. `{/* … */}` or `{// … \n}` (comment-only inner) → skipped (already implemented).
3. `{ if … }` / `{ for … }` / `{ switch … }` (leading Go keyword) → **control flow**.
4. otherwise → `{ expr }` / `{ expr? }` **interpolation** (already implemented).

In an **attribute** context a `{` is resolved as:

1. `{ if … }` (leading `if`) → **CondAttr** (in-tag conditional attribute).
2. `{ ...expr }` → **SpreadAttr** (already implemented).
3. otherwise → error "expected `...` spread inside `{ }` attribute" (already implemented).

For `class={ … }` / `style={ … }` specifically, the brace value is a **composable contribution list** (ClassAttr), not a single expression.

### Known limitations (out of scope for Part 2, document, do not implement)

- **Markup inside `{{ }}`** (e.g. `{{ h := <h1>Hi</h1> }}`): the parser captures the raw Go text; `go/scanner` brace-matching works as long as the embedded markup contains no apostrophes/backticks that desync the scanner. No example uses this; leave as a captured string.
- **Bare multi-word text at control-body / case-body top level**: control-flow and case bodies are markup; literal text is character data terminated by `<`, `{`, or `}`. The `case`/`default`/`}` terminators are recognized only at node boundaries (after the previous node, following whitespace). To emit literal text that contains `}` or the word `case`, wrap it in an element or interpolation. All Part 2 examples wrap case bodies in elements.
- **DOCTYPE `<!DOCTYPE>`, HTML comments `<!-- -->`, raw-text `<script>`/`<style>`** remain separate core gaps, not Part 2.

---

## File Structure

- `ast/ast.go` — **modify**: add `GoBlock`, `IfMarkup`, `ForMarkup`, `SwitchMarkup`, `CaseClause`, `CondAttr`, `ClassAttr` node types and the `ClassPart` value struct; register all Node-implementing types in `SetSpan` and `Inspect`.
- `ast/print.go` — **modify**: add `Fprint` cases for the new nodes.
- `ast/ast_test.go`, `ast/print_test.go` — **modify**: tests for `Inspect`/`SetSpan`/`Fprint` of the new nodes.
- `parser/boundary.go` — **modify**: add `scanToBlockBrace` and `scanToCaseColon` depth-aware scanners.
- `parser/markup.go` — **modify**: add `braceKeyword`, `atWord`, `isIdentByte`, `parseTextCtx`, `parseGoBlock`, `parseBraceNode`, control-flow parsers (`parseIfMarkup`/`parseIfTail`, `parseForMarkup`, `parseSwitchMarkup`/`parseCaseClause`/`parseCaseBody`, `parseControlBody`); rewire `parseChildren`/`parseNodesUntilEOF` to use `parseBraceNode`.
- `parser/attrs.go` — **create**: move/extract attribute parsing here as it grows — `parseSingleAttr`, `parseSpreadAttr`, `parseAttrsUntilBrace`, `parseCondAttr`/`parseCondAttrTail`, `parseComposedAttr`, `splitComposed`. (Keep `parseAttrs` and `parseAttrBraceValue` in `markup.go` or move them too — see Task 6.)
- `parser/markup_test.go` — **modify**: unit tests for every new parse path.
- `internal/corpus/testdata/pipeline/*.txtar` — **create**: whole-pipeline golden cases for the new constructs.
- `internal/corpus/testdata/examples_coverage.golden` — **regenerate** via `-update`.
- `examples/12_children_attrs.gsx` — **modify**: convert the remaining content-position `//` comment (line ~54) to `{/* … */}`.
- `parser/testdata/fuzz/FuzzParseFile/` — **create**: seeds for the new constructs.

---

### Task 1: AST node types for Part 2

**Files:**
- Modify: `ast/ast.go`
- Modify: `ast/print.go`
- Test: `ast/ast_test.go`, `ast/print_test.go`

**Interfaces:**
- Consumes: existing `span`, `Node`, `Markup`, `Attr` interfaces and the `SetSpan`/`Inspect`/`Fprint` functions.
- Produces (used by all later tasks):
  - `ast.GoBlock{ Code string }` — implements `Markup`.
  - `ast.IfMarkup{ Cond string; Then []Markup; Else []Markup }` — implements `Markup`. An `else if` is represented as `Else = []Markup{<*IfMarkup>}` (go/ast style); a plain `else` puts the else-body markup in `Else`; no else → `Else == nil`.
  - `ast.ForMarkup{ Clause string; Body []Markup }` — implements `Markup`.
  - `ast.SwitchMarkup{ Tag string; Cases []*CaseClause }` — implements `Markup`. `Tag == ""` for a tagless switch.
  - `ast.CaseClause{ List string; Default bool; Body []Markup }` — implements `Node` (embeds `span`) but is neither `Markup` nor `Attr`.
  - `ast.CondAttr{ Cond string; Then []Attr; Else []Attr }` — implements `Attr`. `else if` → `Else = []Attr{<*CondAttr>}`.
  - `ast.ClassPart{ Expr, Cond string }` — plain value struct (NOT a Node). `Cond == ""` means unconditional.
  - `ast.ClassAttr{ Name string; Parts []ClassPart }` — implements `Attr`. `Name` is `"class"` or `"style"`.

- [ ] **Step 1: Write the failing tests**

Add to `ast/ast_test.go`:

```go
func TestPart2NodesImplementInterfaces(t *testing.T) {
	var _ Markup = (*GoBlock)(nil)
	var _ Markup = (*IfMarkup)(nil)
	var _ Markup = (*ForMarkup)(nil)
	var _ Markup = (*SwitchMarkup)(nil)
	var _ Attr = (*CondAttr)(nil)
	var _ Attr = (*ClassAttr)(nil)
	var _ Node = (*CaseClause)(nil)
}

func TestSetSpanPart2(t *testing.T) {
	nodes := []Node{
		&GoBlock{}, &IfMarkup{}, &ForMarkup{}, &SwitchMarkup{}, &CaseClause{}, &CondAttr{}, &ClassAttr{},
	}
	for _, n := range nodes {
		SetSpan(n, token.Pos(10), token.Pos(20))
		if n.Pos() != token.Pos(10) || n.End() != token.Pos(20) {
			t.Fatalf("%T: SetSpan not applied: pos=%d end=%d", n, n.Pos(), n.End())
		}
	}
}

func TestInspectPart2(t *testing.T) {
	// if (then: Text) else (Interp); for (Text); switch (case: Element); cond attr; class attr
	tree := &Component{Body: []Markup{
		&IfMarkup{Cond: "x", Then: []Markup{&Text{Value: "t"}}, Else: []Markup{&Interp{Expr: "y"}}},
		&ForMarkup{Clause: "i := range xs", Body: []Markup{&Text{Value: "b"}}},
		&SwitchMarkup{Tag: "k", Cases: []*CaseClause{{List: `"a"`, Body: []Markup{&Element{Tag: "span"}}}}},
		&GoBlock{Code: "z := 1"},
	}}
	var kinds []string
	Inspect(tree, func(n Node) bool {
		if n != nil {
			kinds = append(kinds, fmt.Sprintf("%T", n))
		}
		return true
	})
	// Must visit the IfMarkup, its Then Text, its Else Interp, ForMarkup+Text,
	// SwitchMarkup+CaseClause+Element, GoBlock.
	want := []string{
		"*ast.Component",
		"*ast.IfMarkup", "*ast.Text", "*ast.Interp",
		"*ast.ForMarkup", "*ast.Text",
		"*ast.SwitchMarkup", "*ast.CaseClause", "*ast.Element",
		"*ast.GoBlock",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("Inspect order:\n got %v\nwant %v", kinds, want)
	}
}
```

Ensure `ast/ast_test.go` imports `"fmt"`, `"reflect"`, `"go/token"`, `"testing"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./ast/ -run TestPart2NodesImplementInterfaces -v`
Expected: FAIL — `undefined: GoBlock` (etc.).

- [ ] **Step 3: Add the node types**

In `ast/ast.go`, after the existing `MarkupAttr` definition, add:

```go
// GoBlock is `{{ stmt }}` — a Go-statement escape hatch in a component body.
// Code is the trimmed Go source between the `{{` and `}}` delimiters.
type GoBlock struct {
	span
	Code string
}

func (*GoBlock) markupNode() {}

// IfMarkup is `{ if Cond { Then } [else if … | else { Else }] }`.
// An `else if` is stored as Else = []Markup{<*IfMarkup>} (go/ast style); a plain
// `else` puts its body in Else; no else clause leaves Else nil.
type IfMarkup struct {
	span
	Cond string
	Then []Markup
	Else []Markup
}

func (*IfMarkup) markupNode() {}

// ForMarkup is `{ for Clause { Body } }`. Clause is the raw Go for/range clause.
type ForMarkup struct {
	span
	Clause string
	Body   []Markup
}

func (*ForMarkup) markupNode() {}

// SwitchMarkup is `{ switch Tag { Cases } }`. Tag is "" for a tagless switch.
type SwitchMarkup struct {
	span
	Tag   string
	Cases []*CaseClause
}

func (*SwitchMarkup) markupNode() {}

// CaseClause is one `case List:` or `default:` arm of a SwitchMarkup. It is a
// Node (for Inspect and positions) but is neither Markup nor Attr. List is the
// raw Go case expression(s); Default is true for the `default:` arm (List == "").
type CaseClause struct {
	span
	List    string
	Default bool
	Body    []Markup
}

// CondAttr is an in-tag `{ if Cond { Then } [else …] }` conditional attribute.
// Then and Else are attribute lists; an `else if` is Else = []Attr{<*CondAttr>}.
type CondAttr struct {
	span
	Cond string
	Then []Attr
	Else []Attr
}

func (*CondAttr) attrNode() {}

// ClassPart is one contribution in a composable class/style list: an
// unconditional Expr, or Expr emitted when Cond is true. Cond == "" → always.
// It is a plain value, not a Node.
type ClassPart struct {
	Expr string
	Cond string
}

// ClassAttr is `class={ … }` / `style={ … }` — a composable contribution list.
// Name is "class" or "style".
type ClassAttr struct {
	span
	Name  string
	Parts []ClassPart
}

func (*ClassAttr) attrNode() {}
```

- [ ] **Step 4: Register in SetSpan**

In `ast/ast.go`, extend the `switch v := n.(type)` in `SetSpan` with:

```go
	case *GoBlock:
		v.span = s
	case *IfMarkup:
		v.span = s
	case *ForMarkup:
		v.span = s
	case *SwitchMarkup:
		v.span = s
	case *CaseClause:
		v.span = s
	case *CondAttr:
		v.span = s
	case *ClassAttr:
		v.span = s
```

- [ ] **Step 5: Register in Inspect**

In `ast/ast.go`, extend the `switch n := node.(type)` in `Inspect` (before the trailing comment) with:

```go
	case *IfMarkup:
		for _, m := range n.Then {
			Inspect(m, f)
		}
		for _, m := range n.Else {
			Inspect(m, f)
		}
	case *ForMarkup:
		for _, m := range n.Body {
			Inspect(m, f)
		}
	case *SwitchMarkup:
		for _, c := range n.Cases {
			Inspect(c, f)
		}
	case *CaseClause:
		for _, m := range n.Body {
			Inspect(m, f)
		}
	case *CondAttr:
		for _, a := range n.Then {
			Inspect(a, f)
		}
		for _, a := range n.Else {
			Inspect(a, f)
		}
		// GoBlock, ClassAttr: leaves (ClassParts are not Nodes)
```

Update the leaf-list comment in the `Inspect` doc to include the new leaves.

- [ ] **Step 6: Run AST tests**

Run: `go test ./ast/ -run 'TestPart2NodesImplementInterfaces|TestSetSpanPart2|TestInspectPart2' -v`
Expected: PASS.

- [ ] **Step 7: Write the failing Fprint test**

Add to `ast/print_test.go`:

```go
func TestFprintPart2(t *testing.T) {
	tree := &Component{Name: "X", Body: []Markup{
		&GoBlock{Code: "n := 1"},
		&IfMarkup{Cond: "a > 0",
			Then: []Markup{&Text{Value: "yes"}},
			Else: []Markup{&Interp{Expr: "fallback"}}},
		&ForMarkup{Clause: "_, r := range rows", Body: []Markup{&Element{Tag: "li"}}},
		&SwitchMarkup{Tag: "k", Cases: []*CaseClause{
			{List: `"warn"`, Body: []Markup{&Element{Tag: "span"}}},
			{Default: true, Body: []Markup{&Text{Value: "info"}}},
		}},
		&Element{Tag: "div", Attrs: []Attr{
			&CondAttr{Cond: `id != ""`, Then: []Attr{&BoolAttr{Name: "hidden"}}},
			&ClassAttr{Name: "class", Parts: []ClassPart{
				{Expr: `"btn"`},
				{Expr: `"on"`, Cond: "active"},
			}},
		}},
	}}
	var b strings.Builder
	if err := Fprint(&b, tree); err != nil {
		t.Fatal(err)
	}
	want := `Component name=X recv="" params=""
  GoBlock code="n := 1"
  IfMarkup cond="a > 0"
    then:
      Text value="yes"
    else:
      Interp expr="fallback" try=false
  ForMarkup clause="_, r := range rows"
    Element tag=li void=false
  SwitchMarkup tag="k"
    CaseClause list="\"warn\"" default=false
      Element tag=span void=false
    CaseClause list="" default=true
      Text value="info"
  Element tag=div void=false
    CondAttr cond="id != \"\""
      then:
        BoolAttr name=hidden
    ClassAttr name=class
      ClassPart expr="\"btn\"" cond=""
      ClassPart expr="\"on\"" cond="active"
`
	if b.String() != want {
		t.Fatalf("Fprint mismatch:\n--- got ---\n%s\n--- want ---\n%s", b.String(), want)
	}
}
```

- [ ] **Step 8: Run to verify failure**

Run: `go test ./ast/ -run TestFprintPart2 -v`
Expected: FAIL — falls through to `<unknown node *ast.GoBlock>`.

- [ ] **Step 9: Add Fprint cases**

In `ast/print.go`, add these cases to the `switch n := node.(type)` (before `default:`). Follow the existing `if _, err := fmt.Fprintf(...); err != nil { return err }` style for every write:

```go
	case *GoBlock:
		if _, err := fmt.Fprintf(w, "%sGoBlock code=%q\n", indent, n.Code); err != nil {
			return err
		}
	case *IfMarkup:
		if _, err := fmt.Fprintf(w, "%sIfMarkup cond=%q\n", indent, n.Cond); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s  then:\n", indent); err != nil {
			return err
		}
		for _, c := range n.Then {
			if err := fprintNode(w, c, depth+2); err != nil {
				return err
			}
		}
		if n.Else != nil {
			if _, err := fmt.Fprintf(w, "%s  else:\n", indent); err != nil {
				return err
			}
			for _, c := range n.Else {
				if err := fprintNode(w, c, depth+2); err != nil {
					return err
				}
			}
		}
	case *ForMarkup:
		if _, err := fmt.Fprintf(w, "%sForMarkup clause=%q\n", indent, n.Clause); err != nil {
			return err
		}
		for _, c := range n.Body {
			if err := fprintNode(w, c, depth+1); err != nil {
				return err
			}
		}
	case *SwitchMarkup:
		if _, err := fmt.Fprintf(w, "%sSwitchMarkup tag=%q\n", indent, n.Tag); err != nil {
			return err
		}
		for _, cc := range n.Cases {
			if err := fprintNode(w, cc, depth+1); err != nil {
				return err
			}
		}
	case *CaseClause:
		if _, err := fmt.Fprintf(w, "%sCaseClause list=%q default=%v\n", indent, n.List, n.Default); err != nil {
			return err
		}
		for _, c := range n.Body {
			if err := fprintNode(w, c, depth+1); err != nil {
				return err
			}
		}
	case *CondAttr:
		if _, err := fmt.Fprintf(w, "%sCondAttr cond=%q\n", indent, n.Cond); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s  then:\n", indent); err != nil {
			return err
		}
		for _, a := range n.Then {
			if err := fprintNode(w, a, depth+2); err != nil {
				return err
			}
		}
		if n.Else != nil {
			if _, err := fmt.Fprintf(w, "%s  else:\n", indent); err != nil {
				return err
			}
			for _, a := range n.Else {
				if err := fprintNode(w, a, depth+2); err != nil {
					return err
				}
			}
		}
	case *ClassAttr:
		if _, err := fmt.Fprintf(w, "%sClassAttr name=%s\n", indent, n.Name); err != nil {
			return err
		}
		for _, part := range n.Parts {
			if _, err := fmt.Fprintf(w, "%s  ClassPart expr=%q cond=%q\n", indent, part.Expr, part.Cond); err != nil {
				return err
			}
		}
```

- [ ] **Step 10: Run AST package tests**

Run: `go test ./ast/ -v`
Expected: PASS (all existing + new).

- [ ] **Step 11: Commit**

```bash
git add ast/ast.go ast/print.go ast/ast_test.go ast/print_test.go
git commit -m "feat(ast): Part 2 node types (GoBlock, control flow, CondAttr, ClassAttr)"
```

---

### Task 2: `{{ }}` Go-statement blocks + centralized brace dispatch

**Files:**
- Modify: `parser/markup.go`
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.GoBlock` (Task 1); existing `goExprEnd`, `parseInterp`, `skipBracedComment`, `parseElement`, `parseText`, cursor helpers (`p.i`, `p.peek()`, `p.at`, `p.eof()`, `p.posAt`, `p.pos`, `p.file`, `p.src`).
- Produces:
  - `func (p *parser) parseGoBlock() (*ast.GoBlock, error)` — cursor at `{{`.
  - `func (p *parser) parseBraceNode() (node ast.Markup, skipped bool, err error)` — cursor at `{`; dispatches GoBlock / comment (skipped) / interpolation. (Control-flow cases are added in Tasks 3–5.)
  - `func (p *parser) parseTextCtx(inBlock bool) *ast.Text` — text run; when `inBlock`, also stops at `}`. `parseText` becomes `parseTextCtx(false)`.

- [ ] **Step 1: Write the failing tests**

Add to `parser/markup_test.go`:

```go
func TestParseGoBlock(t *testing.T) {
	// {{ … }} at child level becomes a GoBlock with trimmed Code; trailing text remains.
	p := testParser("{{ x := f() }}rest<")
	node, skipped, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	if skipped {
		t.Fatal("GoBlock should not be skipped")
	}
	gb, ok := node.(*ast.GoBlock)
	if !ok {
		t.Fatalf("got %T, want *ast.GoBlock", node)
	}
	if gb.Code != "x := f()" {
		t.Fatalf("Code = %q, want %q", gb.Code, "x := f()")
	}
	if p.src[p.i:] != "rest<" {
		t.Fatalf("cursor at %q, want %q", p.src[p.i:], "rest<")
	}
}

func TestParseGoBlockNestedBraces(t *testing.T) {
	// Inner Go braces (composite literal, if-block) must not end the {{ }} early.
	p := testParser("{{ if err != nil { return err }; m := map[string]int{} }}")
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	gb := node.(*ast.GoBlock)
	want := "if err != nil { return err }; m := map[string]int{}"
	if gb.Code != want {
		t.Fatalf("Code = %q, want %q", gb.Code, want)
	}
}

func TestGoBlockInComponentBody(t *testing.T) {
	// End-to-end: a {{ }} between markup siblings in a component body.
	src := `package p
component C() {
	<div>
		{{ initials := f(name) }}
		<span>{initials}</span>
	</div>
}`
	file := parseStringT(t, src)
	comp := file.Decls[0].(*ast.Component)
	div := comp.Body[0].(*ast.Element)
	var sawGoBlock bool
	for _, c := range div.Children {
		if gb, ok := c.(*ast.GoBlock); ok {
			sawGoBlock = true
			if gb.Code != "initials := f(name)" {
				t.Fatalf("Code = %q", gb.Code)
			}
		}
	}
	if !sawGoBlock {
		t.Fatal("no GoBlock found in component body children")
	}
}
```

If `parseStringT` does not yet exist as a helper, add it near the top of `parser/markup_test.go`:

```go
// parseStringT parses a full .gsx source string and fails the test on error.
func parseStringT(t *testing.T, src string) *ast.File {
	t.Helper()
	file, err := ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return file
}
```

Ensure the test file imports `"go/token"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run 'TestParseGoBlock|TestGoBlockInComponentBody' -v`
Expected: FAIL — `p.parseBraceNode undefined`.

- [ ] **Step 3: Add `parseTextCtx` and rewire `parseText`**

In `parser/markup.go`, replace the existing `parseText` with:

```go
// parseTextCtx consumes literal text up to the next '<' or '{' (or EOF). When
// inBlock is true (inside a control-flow body) it also stops at '}', which
// terminates the enclosing block.
func (p *parser) parseTextCtx(inBlock bool) *ast.Text {
	start := p.i
	startPos := p.posAt(start)
	for !p.eof() {
		b := p.src[p.i]
		if b == '<' || b == '{' || (inBlock && b == '}') {
			break
		}
		p.i++
	}
	n := &ast.Text{Value: p.src[start:p.i]}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n
}

// parseText consumes literal text up to the next '<' or '{' (or EOF).
func (p *parser) parseText() *ast.Text {
	return p.parseTextCtx(false)
}
```

- [ ] **Step 4: Add `parseGoBlock`**

In `parser/markup.go`, add:

```go
// parseGoBlock parses `{{ stmt }}`. Cursor must be at the first '{' of `{{`.
// It captures the Go statement source between the doubled braces. Nested Go
// braces are handled by go/scanner brace-matching.
func (p *parser) parseGoBlock() (*ast.GoBlock, error) {
	startPos := p.posAt(p.i)
	cp := p.file.Position(startPos)
	outerEnd, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{{`", cp.Line, cp.Column)
	}
	innerEnd, ok := goExprEnd(p.src, p.i+1)
	if !ok || innerEnd >= outerEnd {
		return nil, fmt.Errorf("%d:%d: malformed `{{ }}` block", cp.Line, cp.Column)
	}
	if strings.TrimSpace(p.src[innerEnd+1:outerEnd]) != "" {
		return nil, fmt.Errorf("%d:%d: malformed `{{ }}` block", cp.Line, cp.Column)
	}
	code := strings.TrimSpace(p.src[p.i+2 : innerEnd])
	p.i = outerEnd + 1
	n := &ast.GoBlock{Code: code}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}
```

- [ ] **Step 5: Add `parseBraceNode` and rewire child loops**

In `parser/markup.go`, add:

```go
// parseBraceNode dispatches a `{`-leading construct in a child/markup context.
// Cursor must be at '{'. It returns (node, false, nil) for a GoBlock, control
// flow, or interpolation; (nil, true, nil) when a comment-only `{ }` was
// skipped; or (nil, false, err) on error. Control-flow cases are wired in
// Tasks 3–5.
func (p *parser) parseBraceNode() (ast.Markup, bool, error) {
	if p.at("{{") {
		gb, err := p.parseGoBlock()
		return gb, false, err
	}
	if sk, err := p.skipBracedComment(); err != nil {
		return nil, false, err
	} else if sk {
		return nil, true, nil
	}
	in, err := p.parseInterp()
	return in, false, err
}
```

Then in `parseChildren`, replace the `if p.peek() == '{' { … }` block with:

```go
		if p.peek() == '{' {
			node, skipped, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			if skipped {
				continue
			}
			nodes = append(nodes, node)
			continue
		}
```

And in `parseNodesUntilEOF`, replace the `case p.peek() == '{':` block with:

```go
		case p.peek() == '{':
			node, skipped, err := p.parseBraceNode()
			if err != nil {
				return nil, err
			}
			if skipped {
				continue
			}
			nodes = append(nodes, node)
```

- [ ] **Step 6: Run to verify pass**

Run: `go test ./parser/ -run 'TestParseGoBlock|TestGoBlockInComponentBody|TestParse' -v`
Expected: PASS (new GoBlock tests + all existing markup tests still green — the rewire is behavior-preserving for comments/interpolation).

- [ ] **Step 7: Run the full parser + ast packages**

Run: `go test ./parser/ ./ast/`
Expected: ok.

- [ ] **Step 8: Commit**

```bash
git add parser/markup.go parser/markup_test.go
git commit -m "feat(parser): {{ }} GoBlock + centralized brace dispatch (parseBraceNode)"
```

---

### Task 3: `{ if … }` control flow

**Files:**
- Modify: `parser/boundary.go`
- Modify: `parser/markup.go`
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.IfMarkup`, `ast.GoBlock` (Task 1); `parseBraceNode`, `parseTextCtx`, `parseElement` (Task 2); `goExprEnd`.
- Produces:
  - `func scanToBlockBrace(src string, from int) (int, bool)` — index of the `{` opening a control-flow body, found at paren/bracket depth 0 starting at `from`.
  - `func isIdentByte(b byte) bool`, `func (p *parser) atWord(w string) bool`, `func (p *parser) braceKeyword() string`.
  - `func (p *parser) parseControlBody() ([]ast.Markup, error)` — cursor just past a body `{`; parses markup until and consuming the matching `}`.
  - `func (p *parser) parseIfMarkup() (ast.Markup, error)` and `func (p *parser) parseIfTail() (*ast.IfMarkup, error)`.
  - `parseBraceNode` now dispatches leading `if` to `parseIfMarkup`.

- [ ] **Step 1: Write the failing tests**

Add to `parser/markup_test.go`:

```go
func TestParseIfSimple(t *testing.T) {
	p := testParser(`{ if ok { <b>yes</b> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*ast.IfMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.IfMarkup", node)
	}
	if n.Cond != "ok" {
		t.Fatalf("Cond = %q", n.Cond)
	}
	if len(n.Then) != 1 || n.Then[0].(*ast.Element).Tag != "b" {
		t.Fatalf("Then = %#v", n.Then)
	}
	if n.Else != nil {
		t.Fatalf("Else should be nil, got %#v", n.Else)
	}
}

func TestParseIfElse(t *testing.T) {
	p := testParser(`{ if a { <b>1</b> } else { <i>2</i> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.IfMarkup)
	if len(n.Else) != 1 || n.Else[0].(*ast.Element).Tag != "i" {
		t.Fatalf("Else = %#v", n.Else)
	}
}

func TestParseIfElseIfChain(t *testing.T) {
	p := testParser(`{ if a { <x/> } else if b { <y/> } else { <z/> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.IfMarkup)
	if n.Cond != "a" {
		t.Fatalf("Cond = %q", n.Cond)
	}
	if len(n.Else) != 1 {
		t.Fatalf("expected else-if chain, Else = %#v", n.Else)
	}
	elseIf, ok := n.Else[0].(*ast.IfMarkup)
	if !ok {
		t.Fatalf("Else[0] = %T, want *ast.IfMarkup", n.Else[0])
	}
	if elseIf.Cond != "b" {
		t.Fatalf("else-if Cond = %q", elseIf.Cond)
	}
	if len(elseIf.Else) != 1 || elseIf.Else[0].(*ast.Element).Tag != "z" {
		t.Fatalf("final else = %#v", elseIf.Else)
	}
}

func TestParseIfWithInterpAndText(t *testing.T) {
	p := testParser(`{ if it.Active { <strong>{it.Name}</strong> } else { {it.Name} } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.IfMarkup)
	strong := n.Then[0].(*ast.Element)
	if strong.Children[0].(*ast.Interp).Expr != "it.Name" {
		t.Fatalf("then interp = %#v", strong.Children[0])
	}
	// else body has whitespace text + interp; find the interp
	var elseInterp *ast.Interp
	for _, m := range n.Else {
		if in, ok := m.(*ast.Interp); ok {
			elseInterp = in
		}
	}
	if elseInterp == nil || elseInterp.Expr != "it.Name" {
		t.Fatalf("else interp = %#v", n.Else)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run TestParseIf -v`
Expected: FAIL — `{ if … }` currently parses as an `*ast.Interp`, so the `.(*ast.IfMarkup)` assertion panics/fails.

- [ ] **Step 3: Add `scanToBlockBrace`**

In `parser/boundary.go`, add:

```go
// scanToBlockBrace returns the index of the '{' that opens a control-flow body,
// scanning Go tokens from offset `from` and returning the first '{' found at
// paren/bracket/brace depth 0. Composite-literal braces inside parens (Go
// requires parens for composite literals in control-flow headers) are skipped.
// ok is false if no such '{' is found.
func scanToBlockBrace(src string, from int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < from {
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
		case token.LBRACE:
			if depth == 0 {
				return off, true
			}
			depth++
		case token.RBRACE:
			depth--
		}
	}
}
```

- [ ] **Step 4: Add the small lexical helpers**

In `parser/markup.go`, add:

```go
// isIdentByte reports whether b can be part of a Go identifier.
func isIdentByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_'
}

// atWord reports whether the source at the cursor is exactly the word w,
// not followed by an identifier character (so `else` matches but `elsewhere`
// does not).
func (p *parser) atWord(w string) bool {
	if !p.at(w) {
		return false
	}
	next := p.i + len(w)
	return next >= len(p.src) || !isIdentByte(p.src[next])
}

// braceKeyword returns the leading control-flow keyword ("if", "for", "switch")
// inside the `{ … }` at the cursor (which must be at '{'), or "" if the first
// token is not one of those keywords. It does not move the cursor.
func (p *parser) braceKeyword() string {
	j := p.i + 1
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\n' || p.src[j] == '\r') {
		j++
	}
	start := j
	for j < len(p.src) && p.src[j] >= 'a' && p.src[j] <= 'z' {
		j++
	}
	kw := p.src[start:j]
	switch kw {
	case "if", "for", "switch":
		if j < len(p.src) && isIdentByte(p.src[j]) {
			return ""
		}
		return kw
	}
	return ""
}
```

- [ ] **Step 5: Add `parseControlBody`**

In `parser/markup.go`, add:

```go
// parseControlBody parses a markup sequence forming a control-flow body. The
// cursor must be just past the opening '{'. It parses children until the
// matching '}' at this level, consumes that '}', and returns the children.
func (p *parser) parseControlBody() ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		if p.eof() {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated control-flow body, expected `}`", cp.Line, cp.Column)
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
```

- [ ] **Step 6: Add `parseIfMarkup` / `parseIfTail`**

In `parser/markup.go`, add:

```go
// parseIfMarkup parses `{ if … { … } [else …] }`. Cursor at '{'; the caller has
// verified the leading keyword is "if".
func (p *parser) parseIfMarkup() (ast.Markup, error) {
	startPos := p.posAt(p.i)
	p.i++ // past outer '{'
	p.skipSpace()
	n, err := p.parseIfTail()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `}` to close `{ if … }`", cp.Line, cp.Column)
	}
	p.i++ // past outer '}'
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseIfTail parses `if Cond { Then } [else if … | else { Else }]`, with the
// cursor at the `if` keyword. It is recursive: an `else if` builds a nested
// IfMarkup in Else.
func (p *parser) parseIfTail() (*ast.IfMarkup, error) {
	kwPos := p.posAt(p.i)
	p.i += 2 // past 'if'
	condStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i)
	if !ok {
		cp := p.file.Position(p.posAt(p.i))
		return nil, fmt.Errorf("%d:%d: expected `{` after `if` condition", cp.Line, cp.Column)
	}
	cond := strings.TrimSpace(p.src[condStart:braceOff])
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseControlBody()
	if err != nil {
		return nil, err
	}
	n := &ast.IfMarkup{Cond: cond, Then: body}
	p.skipSpace()
	if p.atWord("else") {
		p.i += len("else")
		p.skipSpace()
		switch {
		case p.peek() == '{':
			p.i++ // past '{'
			elseBody, err := p.parseControlBody()
			if err != nil {
				return nil, err
			}
			n.Else = elseBody
		case p.atWord("if"):
			elseIf, err := p.parseIfTail()
			if err != nil {
				return nil, err
			}
			n.Else = []ast.Markup{elseIf}
		default:
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected `{` or `if` after `else`", cp.Line, cp.Column)
		}
	}
	ast.SetSpan(n, kwPos, p.posAt(p.i))
	return n, nil
}
```

- [ ] **Step 7: Dispatch `if` in `parseBraceNode`**

In `parser/markup.go`, in `parseBraceNode`, after the `skipBracedComment` block and before the final `parseInterp`, add:

```go
	switch p.braceKeyword() {
	case "if":
		n, err := p.parseIfMarkup()
		return n, false, err
	}
```

- [ ] **Step 8: Run to verify pass**

Run: `go test ./parser/ -run TestParseIf -v`
Expected: PASS.

- [ ] **Step 9: Run the full parser package**

Run: `go test ./parser/ ./ast/`
Expected: ok.

- [ ] **Step 10: Commit**

```bash
git add parser/boundary.go parser/markup.go parser/markup_test.go
git commit -m "feat(parser): { if / else if / else } control flow"
```

---

### Task 4: `{ for … }` control flow

**Files:**
- Modify: `parser/markup.go`
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.ForMarkup` (Task 1); `scanToBlockBrace`, `parseControlBody`, `braceKeyword`, `parseBraceNode` (Tasks 2–3).
- Produces: `func (p *parser) parseForMarkup() (ast.Markup, error)`; `parseBraceNode` dispatches leading `for`.

- [ ] **Step 1: Write the failing tests**

Add to `parser/markup_test.go`:

```go
func TestParseForRange(t *testing.T) {
	p := testParser(`{ for i, it := range items { <li>{it.Name}</li> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*ast.ForMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.ForMarkup", node)
	}
	if n.Clause != "i, it := range items" {
		t.Fatalf("Clause = %q", n.Clause)
	}
	li := n.Body[0].(*ast.Element)
	if li.Tag != "li" {
		t.Fatalf("body = %#v", n.Body)
	}
}

func TestParseForCStyle(t *testing.T) {
	p := testParser(`{ for i := 0; i < n; i++ { <x/> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.ForMarkup)
	if n.Clause != "i := 0; i < n; i++" {
		t.Fatalf("Clause = %q", n.Clause)
	}
}

func TestParseForWithGoBlockInside(t *testing.T) {
	p := testParser(`{ for i := range xs { {{ v := g(i) }}<a>{v}</a> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.ForMarkup)
	var sawGoBlock bool
	for _, m := range n.Body {
		if _, ok := m.(*ast.GoBlock); ok {
			sawGoBlock = true
		}
	}
	if !sawGoBlock {
		t.Fatalf("expected a GoBlock inside the for body, got %#v", n.Body)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run TestParseFor -v`
Expected: FAIL — `{ for … }` still parses as `*ast.Interp`.

- [ ] **Step 3: Add `parseForMarkup`**

In `parser/markup.go`, add:

```go
// parseForMarkup parses `{ for Clause { Body } }`. Cursor at '{'; the caller has
// verified the leading keyword is "for".
func (p *parser) parseForMarkup() (ast.Markup, error) {
	startPos := p.posAt(p.i)
	p.i++ // past '{'
	p.skipSpace()
	p.i += len("for")
	clauseStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i)
	if !ok {
		cp := p.file.Position(p.posAt(p.i))
		return nil, fmt.Errorf("%d:%d: expected `{` after `for` clause", cp.Line, cp.Column)
	}
	clause := strings.TrimSpace(p.src[clauseStart:braceOff])
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseControlBody()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `}` to close `{ for … }`", cp.Line, cp.Column)
	}
	p.i++ // past outer '}'
	n := &ast.ForMarkup{Clause: clause, Body: body}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}
```

- [ ] **Step 4: Dispatch `for` in `parseBraceNode`**

In `parser/markup.go`, extend the `switch p.braceKeyword()` added in Task 3:

```go
	case "for":
		n, err := p.parseForMarkup()
		return n, false, err
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./parser/ -run TestParseFor -v`
Expected: PASS.

- [ ] **Step 6: Run the full parser package**

Run: `go test ./parser/ ./ast/`
Expected: ok.

- [ ] **Step 7: Commit**

```bash
git add parser/markup.go parser/markup_test.go
git commit -m "feat(parser): { for … } control flow"
```

---

### Task 5: `{ switch … }` control flow

**Files:**
- Modify: `parser/boundary.go`
- Modify: `parser/markup.go`
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.SwitchMarkup`, `ast.CaseClause` (Task 1); `scanToBlockBrace`, `atWord`, `braceKeyword`, `parseElement`, `parseBraceNode`, `parseTextCtx` (Tasks 2–3).
- Produces:
  - `func scanToCaseColon(src string, from int) (int, bool)` — index of the `:` ending a case list, at depth 0.
  - `func (p *parser) parseSwitchMarkup() (ast.Markup, error)`, `func (p *parser) parseCaseClause() (*ast.CaseClause, error)`, `func (p *parser) parseCaseBody() ([]ast.Markup, error)`.
  - `parseBraceNode` dispatches leading `switch`.

- [ ] **Step 1: Write the failing tests**

Add to `parser/markup_test.go`:

```go
func TestParseSwitch(t *testing.T) {
	src := `{ switch kind {
		case "warning":
			<span>warn</span>
		case "error":
			<span>err</span>
		default:
			<span>info</span>
		} }`
	p := testParser(src)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*ast.SwitchMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.SwitchMarkup", node)
	}
	if n.Tag != "kind" {
		t.Fatalf("Tag = %q", n.Tag)
	}
	if len(n.Cases) != 3 {
		t.Fatalf("got %d cases, want 3: %#v", len(n.Cases), n.Cases)
	}
	if n.Cases[0].List != `"warning"` || n.Cases[0].Default {
		t.Fatalf("case0 = %#v", n.Cases[0])
	}
	if !n.Cases[2].Default || n.Cases[2].List != "" {
		t.Fatalf("case2 (default) = %#v", n.Cases[2])
	}
	if n.Cases[1].Body[0].(*ast.Element).Tag != "span" {
		t.Fatalf("case1 body = %#v", n.Cases[1].Body)
	}
}

func TestParseSwitchTagless(t *testing.T) {
	src := `{ switch {
		case x > 0:
			<a/>
		} }`
	p := testParser(src)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.SwitchMarkup)
	if n.Tag != "" {
		t.Fatalf("Tag = %q, want empty", n.Tag)
	}
	if n.Cases[0].List != "x > 0" {
		t.Fatalf("case list = %q", n.Cases[0].List)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run TestParseSwitch -v`
Expected: FAIL — `{ switch … }` still parses as `*ast.Interp`.

- [ ] **Step 3: Add `scanToCaseColon`**

In `parser/boundary.go`, add:

```go
// scanToCaseColon returns the index of the ':' that ends a switch case list,
// scanning Go tokens from offset `from` and returning the first ':' at
// paren/bracket/brace depth 0. ok is false if no such ':' is found.
func scanToCaseColon(src string, from int) (int, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		off := fset.Position(pos).Offset
		if off < from {
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COLON:
			if depth == 0 {
				return off, true
			}
		}
	}
}
```

- [ ] **Step 4: Add `parseSwitchMarkup`, `parseCaseClause`, `parseCaseBody`**

In `parser/markup.go`, add:

```go
// parseSwitchMarkup parses `{ switch [Tag] { case … default … } }`. Cursor at
// '{'; the caller has verified the leading keyword is "switch".
func (p *parser) parseSwitchMarkup() (ast.Markup, error) {
	startPos := p.posAt(p.i)
	p.i++ // past outer '{'
	p.skipSpace()
	p.i += len("switch")
	tagStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i)
	if !ok {
		cp := p.file.Position(p.posAt(p.i))
		return nil, fmt.Errorf("%d:%d: expected `{` after `switch`", cp.Line, cp.Column)
	}
	tag := strings.TrimSpace(p.src[tagStart:braceOff])
	p.i = braceOff + 1 // past switch-body '{'

	var cases []*ast.CaseClause
	for {
		p.skipSpace()
		if p.eof() {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated `switch`, expected `}`", cp.Line, cp.Column)
		}
		if p.peek() == '}' {
			p.i++ // past switch-body '}'
			break
		}
		cc, err := p.parseCaseClause()
		if err != nil {
			return nil, err
		}
		cases = append(cases, cc)
	}

	p.skipSpace()
	if p.peek() != '}' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `}` to close `{ switch … }`", cp.Line, cp.Column)
	}
	p.i++ // past outer '}'
	n := &ast.SwitchMarkup{Tag: tag, Cases: cases}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseCaseClause parses one `case List:` or `default:` arm with its markup
// body. Cursor at the `case` or `default` keyword.
func (p *parser) parseCaseClause() (*ast.CaseClause, error) {
	startPos := p.posAt(p.i)
	cc := &ast.CaseClause{}
	switch {
	case p.atWord("case"):
		p.i += len("case")
		listStart := p.i
		colonOff, ok := scanToCaseColon(p.src, p.i)
		if !ok {
			cp := p.file.Position(p.posAt(p.i))
			return nil, fmt.Errorf("%d:%d: expected `:` in `case`", cp.Line, cp.Column)
		}
		cc.List = strings.TrimSpace(p.src[listStart:colonOff])
		p.i = colonOff + 1 // past ':'
	case p.atWord("default"):
		p.i += len("default")
		p.skipSpace()
		if p.peek() != ':' {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected `:` after `default`", cp.Line, cp.Column)
		}
		cc.Default = true
		p.i++ // past ':'
	default:
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `case` or `default` in `switch`", cp.Line, cp.Column)
	}
	body, err := p.parseCaseBody()
	if err != nil {
		return nil, err
	}
	cc.Body = body
	ast.SetSpan(cc, startPos, p.posAt(p.i))
	return cc, nil
}

// parseCaseBody parses the markup body of a case arm. It does NOT consume the
// terminator: it stops (without advancing) at the next `case`/`default` keyword
// or at the switch body's closing `}`.
func (p *parser) parseCaseBody() ([]ast.Markup, error) {
	var nodes []ast.Markup
	for {
		p.skipSpace()
		if p.eof() {
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: unterminated `case` body", cp.Line, cp.Column)
		}
		if p.peek() == '}' || p.atWord("case") || p.atWord("default") {
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
```

- [ ] **Step 5: Dispatch `switch` in `parseBraceNode`**

In `parser/markup.go`, extend the `switch p.braceKeyword()`:

```go
	case "switch":
		n, err := p.parseSwitchMarkup()
		return n, false, err
```

- [ ] **Step 6: Run to verify pass**

Run: `go test ./parser/ -run TestParseSwitch -v`
Expected: PASS.

- [ ] **Step 7: Run the full parser package + the examples coverage check**

Run: `go test ./parser/ ./ast/ ./internal/corpus/`
Expected: `./parser/` and `./ast/` ok. `./internal/corpus/` may now FAIL `TestExamplesCoverage` because examples 03/07/08 (control flow) now parse into structured nodes — this is expected and regenerated in Task 8. If it fails ONLY on the coverage golden diff, that is acceptable at this point; if it fails with a parser error or panic, fix the parser before continuing.

- [ ] **Step 8: Commit**

```bash
git add parser/boundary.go parser/markup.go parser/markup_test.go
git commit -m "feat(parser): { switch / case / default } control flow"
```

---

### Task 6: In-tag conditional attributes `{ if … }`

**Files:**
- Create: `parser/attrs.go`
- Modify: `parser/markup.go` (extract attribute parsing)
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.CondAttr`, `ast.SpreadAttr`, `ast.StaticAttr`, `ast.ExprAttr`, `ast.BoolAttr`, `ast.MarkupAttr` (Task 1); `scanToBlockBrace`, `atWord`, `braceKeyword`, `skipTagComment`, `goExprEnd` (existing/Tasks 2–3); `parseAttrBraceValue` (existing).
- Produces:
  - `func (p *parser) parseSingleAttr() (ast.Attr, error)` — parses exactly one attribute at the cursor (spread, conditional, or name-based static/expr/bool/markup).
  - `func (p *parser) parseSpreadAttr() (ast.Attr, error)` — the existing `{...expr}` logic, extracted.
  - `func (p *parser) parseAttrsUntilBrace() ([]ast.Attr, error)` — attribute list terminated by `}`.
  - `func (p *parser) parseCondAttr() (ast.Attr, error)`, `func (p *parser) parseCondAttrTail() (*ast.CondAttr, error)`.
  - `parseAttrs` reworked to delegate to `parseSingleAttr`.

This task refactors the existing `parseAttrs` so that the per-attribute logic is shared between the tag-attribute loop (terminated by `>`/`/>`) and the conditional-attribute body loop (terminated by `}`). The refactor must keep `TestParseAttrs`, `TestParseSelfClosing`, `TestParseMarkupAttr`, `TestParseSpreadWhitespace`, and the tag-comment tests green.

- [ ] **Step 1: Write the failing tests**

Add to `parser/markup_test.go`:

```go
func TestParseCondAttr(t *testing.T) {
	// One-off conditional attribute on a tag.
	p := testParser(`<input { if id != "" { id={id} } }/>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := node.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	ca, ok := el.Attrs[0].(*ast.CondAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.CondAttr", el.Attrs[0])
	}
	if ca.Cond != `id != ""` {
		t.Fatalf("Cond = %q", ca.Cond)
	}
	if len(ca.Then) != 1 {
		t.Fatalf("Then = %#v", ca.Then)
	}
	ea, ok := ca.Then[0].(*ast.ExprAttr)
	if !ok || ea.Name != "id" || ea.Expr != "id" {
		t.Fatalf("Then[0] = %#v", ca.Then[0])
	}
}

func TestParseCondAttrBool(t *testing.T) {
	p := testParser(`<input { if required { required } }/>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	ca := node.(*ast.Element).Attrs[0].(*ast.CondAttr)
	if _, ok := ca.Then[0].(*ast.BoolAttr); !ok {
		t.Fatalf("Then[0] = %T, want *ast.BoolAttr", ca.Then[0])
	}
}

func TestParseCondAttrWithOtherAttrs(t *testing.T) {
	// Conditional attr composes with normal attrs and a spread on one element.
	p := testParser(`<button type="button" { if on { disabled } } {...rest}>x</button>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := node.(*ast.Element)
	if len(el.Attrs) != 3 {
		t.Fatalf("got %d attrs, want 3: %#v", len(el.Attrs), el.Attrs)
	}
	if _, ok := el.Attrs[0].(*ast.StaticAttr); !ok {
		t.Fatalf("attr0 = %T", el.Attrs[0])
	}
	if _, ok := el.Attrs[1].(*ast.CondAttr); !ok {
		t.Fatalf("attr1 = %T", el.Attrs[1])
	}
	if _, ok := el.Attrs[2].(*ast.SpreadAttr); !ok {
		t.Fatalf("attr2 = %T", el.Attrs[2])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run TestParseCondAttr -v`
Expected: FAIL — `{ if … }` in attribute position currently errors with "expected `...` spread inside `{ }` attribute".

- [ ] **Step 3: Create `parser/attrs.go` and extract the per-attribute logic**

Create `parser/attrs.go`:

```go
package parser

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// parseSpreadAttr parses `{ ...expr }` at the cursor (which must be at '{'),
// tolerant of whitespace after '{' and around '...'. In attribute position a
// non-spread, non-conditional `{ }` is an error.
func (p *parser) parseSpreadAttr() (ast.Attr, error) {
	attrStartPos := p.posAt(p.i)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("unterminated `{` in attributes")
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	if !strings.HasPrefix(inner, "...") {
		cp := p.file.Position(attrStartPos)
		return nil, fmt.Errorf("%d:%d: expected `...` spread inside `{ }` attribute", cp.Line, cp.Column)
	}
	expr := strings.TrimSpace(strings.TrimPrefix(inner, "..."))
	p.i = end + 1
	sa := &ast.SpreadAttr{Expr: expr}
	ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
	return sa, nil
}

// parseSingleAttr parses exactly one attribute at the cursor: a conditional
// `{ if … }`, a spread `{ ...expr }`, or a name-based attribute
// (static / expr / markup / bool). The cursor must be at the attribute start
// (not whitespace, not a comment, not a terminator).
func (p *parser) parseSingleAttr() (ast.Attr, error) {
	if p.peek() == '{' {
		if p.braceKeyword() == "if" {
			return p.parseCondAttr()
		}
		return p.parseSpreadAttr()
	}
	attrStart := p.i
	attrStartPos := p.posAt(attrStart)
	for !p.eof() && isAttrNameByte(p.src[p.i]) {
		p.i++
	}
	if p.i == attrStart {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected attribute name, got %q", cp.Line, cp.Column, string(p.peek()))
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
		sa := &ast.StaticAttr{Name: name, Value: val}
		ast.SetSpan(sa, attrStartPos, p.posAt(p.i))
		return sa, nil
	case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
		p.i++ // past '='
		return p.parseAttrBraceValue(name, attrStartPos)
	default:
		ba := &ast.BoolAttr{Name: name}
		ast.SetSpan(ba, attrStartPos, p.posAt(p.i))
		return ba, nil
	}
}

// parseAttrsUntilBrace parses an attribute list terminated by '}' (the body of a
// conditional attribute). It consumes the closing '}'.
func (p *parser) parseAttrsUntilBrace() ([]ast.Attr, error) {
	var attrs []ast.Attr
	for {
		p.skipSpace()
		if p.eof() {
			return nil, fmt.Errorf("unexpected EOF in `{ if … }` attribute body")
		}
		if p.peek() == '}' {
			p.i++ // consume '}'
			return attrs, nil
		}
		if sk, err := p.skipTagComment(); err != nil {
			return nil, err
		} else if sk {
			continue
		}
		a, err := p.parseSingleAttr()
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, a)
	}
}

// parseCondAttr parses `{ if Cond { Then } [else …] }` in attribute position.
// Cursor at '{'; the caller has verified the leading keyword is "if".
func (p *parser) parseCondAttr() (ast.Attr, error) {
	startPos := p.posAt(p.i)
	p.i++ // past outer '{'
	p.skipSpace()
	n, err := p.parseCondAttrTail()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek() != '}' {
		cp := p.file.Position(p.pos())
		return nil, fmt.Errorf("%d:%d: expected `}` to close `{ if … }` attribute", cp.Line, cp.Column)
	}
	p.i++ // past outer '}'
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}

// parseCondAttrTail parses `if Cond { Then } [else if … | else { Else }]` with
// the cursor at the `if` keyword. An `else if` builds a nested CondAttr in Else.
func (p *parser) parseCondAttrTail() (*ast.CondAttr, error) {
	kwPos := p.posAt(p.i)
	p.i += 2 // past 'if'
	condStart := p.i
	braceOff, ok := scanToBlockBrace(p.src, p.i)
	if !ok {
		cp := p.file.Position(p.posAt(p.i))
		return nil, fmt.Errorf("%d:%d: expected `{` after `if` condition", cp.Line, cp.Column)
	}
	cond := strings.TrimSpace(p.src[condStart:braceOff])
	p.i = braceOff + 1 // past body '{'
	body, err := p.parseAttrsUntilBrace()
	if err != nil {
		return nil, err
	}
	n := &ast.CondAttr{Cond: cond, Then: body}
	p.skipSpace()
	if p.atWord("else") {
		p.i += len("else")
		p.skipSpace()
		switch {
		case p.peek() == '{':
			p.i++ // past '{'
			elseBody, err := p.parseAttrsUntilBrace()
			if err != nil {
				return nil, err
			}
			n.Else = elseBody
		case p.atWord("if"):
			elseIf, err := p.parseCondAttrTail()
			if err != nil {
				return nil, err
			}
			n.Else = []ast.Attr{elseIf}
		default:
			cp := p.file.Position(p.pos())
			return nil, fmt.Errorf("%d:%d: expected `{` or `if` after `else`", cp.Line, cp.Column)
		}
	}
	ast.SetSpan(n, kwPos, p.posAt(p.i))
	return n, nil
}
```

- [ ] **Step 4: Rework `parseAttrs` in `markup.go` to delegate**

In `parser/markup.go`, replace the entire body of `parseAttrs` with:

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
		if sk, err := p.skipTagComment(); err != nil {
			return nil, err
		} else if sk {
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

(The spread and name-based logic now lives in `parseSingleAttr`/`parseSpreadAttr` in `attrs.go`. Delete the inlined spread/name code from the old `parseAttrs`.)

- [ ] **Step 5: Run to verify pass + no regressions**

Run: `go test ./parser/ -run 'TestParseCondAttr|TestParseAttrs|TestParseSelfClosing|TestParseMarkupAttr|TestParseSpreadWhitespace|TestTag' -v`
Expected: PASS (new conditional-attr tests + all pre-existing attribute/tag-comment tests).

- [ ] **Step 6: Run the full parser package**

Run: `go test ./parser/ ./ast/`
Expected: ok.

- [ ] **Step 7: Commit**

```bash
git add parser/attrs.go parser/markup.go parser/markup_test.go
git commit -m "feat(parser): in-tag { if … } conditional attributes; extract attrs.go"
```

---

### Task 7: Composable `class` / `style` contribution lists

**Files:**
- Modify: `parser/attrs.go`
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.ClassAttr`, `ast.ClassPart` (Task 1); `goExprEnd`; `parseSingleAttr`, `parseAttrBraceValue` (Task 6/existing).
- Produces:
  - `func (p *parser) parseComposedAttr(name string, startPos token.Pos) (ast.Attr, error)` — `class={ … }` / `style={ … }`.
  - `func splitComposed(src string) ([]ast.ClassPart, error)` — split the brace inner into parts by depth-0 commas and `expr:cond` colons.
  - `parseSingleAttr` routes `class`/`style` brace values to `parseComposedAttr`.

- [ ] **Step 1: Write the failing tests**

Add to `parser/markup_test.go`:

```go
func TestParseComposedClass(t *testing.T) {
	src := `<a class={
		"group flex gap-x-3",
		variantClass(v),
		"bg-active": isActive,
		"text-muted": !isActive,
		class,
	}></a>`
	p := testParser(src)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := node.(*ast.Element)
	ca, ok := el.Attrs[0].(*ast.ClassAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.ClassAttr", el.Attrs[0])
	}
	if ca.Name != "class" {
		t.Fatalf("Name = %q", ca.Name)
	}
	want := []ast.ClassPart{
		{Expr: `"group flex gap-x-3"`},
		{Expr: `variantClass(v)`},
		{Expr: `"bg-active"`, Cond: "isActive"},
		{Expr: `"text-muted"`, Cond: "!isActive"},
		{Expr: `class`},
	}
	if !reflect.DeepEqual(ca.Parts, want) {
		t.Fatalf("Parts:\n got %#v\nwant %#v", ca.Parts, want)
	}
}

func TestParseComposedStyleSingle(t *testing.T) {
	// style={ … } with one part; no trailing comma.
	p := testParser(`<div style={ "color: red" }></div>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	ca := node.(*ast.Element).Attrs[0].(*ast.ClassAttr)
	if ca.Name != "style" || len(ca.Parts) != 1 || ca.Parts[0].Expr != `"color: red"` {
		t.Fatalf("got %#v", ca.Parts)
	}
}

func TestComposedColonInsideBracketsIsOneExpr(t *testing.T) {
	// A ':' inside a Go index/slice expr must NOT split expr:cond.
	p := testParser(`<a class={ m[k], s[1:2] }></a>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	ca := node.(*ast.Element).Attrs[0].(*ast.ClassAttr)
	want := []ast.ClassPart{{Expr: "m[k]"}, {Expr: "s[1:2]"}}
	if !reflect.DeepEqual(ca.Parts, want) {
		t.Fatalf("Parts = %#v, want %#v", ca.Parts, want)
	}
}

func TestNonClassBraceStaysExprAttr(t *testing.T) {
	// A non-class/style attribute with a brace value is still an ExprAttr.
	p := testParser(`<input value={x}/>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := node.(*ast.Element).Attrs[0].(*ast.ExprAttr); !ok {
		t.Fatalf("attr0 = %T, want *ast.ExprAttr", node.(*ast.Element).Attrs[0])
	}
}
```

Ensure `parser/markup_test.go` imports `"reflect"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser/ -run 'TestParseComposed|TestComposedColon' -v`
Expected: FAIL — `class={ … }` currently becomes an `*ast.ExprAttr` (or errors on the multi-line comma list), not a `*ast.ClassAttr`.

- [ ] **Step 3: Add `splitComposed`**

In `parser/attrs.go`, add (and add `"go/scanner"`, `"go/token"` to the imports):

```go
// splitComposed splits the inner source of a `class={ … }` / `style={ … }`
// value into contributions. Contributions are separated by commas at
// bracket/brace/paren depth 0; within a contribution a depth-0 ':' separates an
// `expr : cond` conditional from its condition. A trailing comma yields no empty
// part.
func splitComposed(src string) ([]ast.ClassPart, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	var commas, colons []int
	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		off := fset.Position(pos).Offset
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COMMA:
			if depth == 0 {
				commas = append(commas, off)
			}
		case token.COLON:
			if depth == 0 {
				colons = append(colons, off)
			}
		}
	}

	// Segment boundaries: [-1] + commas + [len]. Each segment is (start, end).
	bounds := make([]int, 0, len(commas)+2)
	bounds = append(bounds, -1)
	bounds = append(bounds, commas...)
	bounds = append(bounds, len(src))

	var parts []ast.ClassPart
	for k := 0; k+1 < len(bounds); k++ {
		segStart := bounds[k] + 1
		segEnd := bounds[k+1]
		if strings.TrimSpace(src[segStart:segEnd]) == "" {
			continue // empty segment (e.g. trailing comma)
		}
		colon := -1
		for _, c := range colons {
			if c > segStart && c < segEnd {
				colon = c
				break
			}
		}
		if colon >= 0 {
			parts = append(parts, ast.ClassPart{
				Expr: strings.TrimSpace(src[segStart:colon]),
				Cond: strings.TrimSpace(src[colon+1 : segEnd]),
			})
		} else {
			parts = append(parts, ast.ClassPart{Expr: strings.TrimSpace(src[segStart:segEnd])})
		}
	}
	return parts, nil
}
```

- [ ] **Step 4: Add `parseComposedAttr`**

In `parser/attrs.go`, add (and add `"go/token"` import — already added in Step 3):

```go
// parseComposedAttr parses a `class={ … }` / `style={ … }` composable
// contribution list. Cursor must be at the '{' of the value.
func (p *parser) parseComposedAttr(name string, startPos token.Pos) (ast.Attr, error) {
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("unterminated `{` in %s value", name)
	}
	parts, err := splitComposed(p.src[p.i+1 : end])
	if err != nil {
		return nil, err
	}
	p.i = end + 1
	n := &ast.ClassAttr{Name: name, Parts: parts}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}
```

- [ ] **Step 5: Route `class`/`style` in `parseSingleAttr`**

In `parser/attrs.go`, in `parseSingleAttr`, change the brace-value case:

```go
	case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
		p.i++ // past '='
		if name == "class" || name == "style" {
			return p.parseComposedAttr(name, attrStartPos)
		}
		return p.parseAttrBraceValue(name, attrStartPos)
```

- [ ] **Step 6: Run to verify pass**

Run: `go test ./parser/ -run 'TestParseComposed|TestComposedColon|TestNonClassBrace' -v`
Expected: PASS.

- [ ] **Step 7: Run the full parser + ast packages**

Run: `go test ./parser/ ./ast/`
Expected: ok.

- [ ] **Step 8: Commit**

```bash
git add parser/attrs.go parser/markup_test.go
git commit -m "feat(parser): composable class/style { … } contribution lists"
```

---

### Task 8: Corpus integration, example cleanup, fuzz seeds

**Files:**
- Modify: `examples/12_children_attrs.gsx`
- Create: `internal/corpus/testdata/pipeline/09_goblock.txtar`
- Create: `internal/corpus/testdata/pipeline/10_if.txtar`
- Create: `internal/corpus/testdata/pipeline/11_for.txtar`
- Create: `internal/corpus/testdata/pipeline/12_switch.txtar`
- Create: `internal/corpus/testdata/pipeline/13_cond_attr.txtar`
- Create: `internal/corpus/testdata/pipeline/14_composable_class.txtar`
- Modify: `internal/corpus/testdata/examples_coverage.golden` (regenerate)
- Create: `parser/testdata/fuzz/FuzzParseFile/seed_control_flow`, `parser/testdata/fuzz/FuzzParseFile/seed_goblock`

**Interfaces:**
- Consumes: the `TestPipeline` harness in `internal/corpus/corpus_test.go` (parses `input.gsx`, compares `ast.golden` via `ast.Fprint` and `diagnostics.golden`); the `TestExamplesCoverage` golden tracker; `FuzzParseFile` (no-panic + span invariant).

- [ ] **Step 1: Fix the remaining content-position comment in example 12**

In `examples/12_children_attrs.gsx`, the `LoginForm` component (around line 52–57) has a content-position `//` comment which is literal text per the comment rule and breaks the parse. Replace:

```gsx
component LoginForm() {
	<form>
		// label -> prop; name/required/hx-get are placed on the inner <input> via {...attrs}
		<Field label="Email" name="email" required hx-get="/check-email"/>
	</form>
}
```

with:

```gsx
component LoginForm() {
	<form>
		{/* label -> prop; name/required/hx-get are placed on the inner <input> via {...attrs} */}
		<Field label="Email" name="email" required hx-get="/check-email"/>
	</form>
}
```

- [ ] **Step 2: Investigate example 04's gap**

Run: `go test ./internal/corpus/ -run TestExamplesCoverage -update` then read the regenerated `internal/corpus/testdata/examples_coverage.golden`. For `04_components.gsx`, if it still reports an error, open the file at the reported `line:col`. If the offending construct is a content-position `//`/`/* */` comment (literal text per the comment rule), convert it to a braced `{/* … */}` comment exactly as in Step 1. If it is a DOCTYPE / raw-text / `<!-- -->` construct (the separate core gaps listed in Global Constraints), leave it — it is out of scope for Part 2 and remains a recorded coverage entry. Do not invent parser behavior to make it pass.

- [ ] **Step 3: Create the GoBlock pipeline case**

Create `internal/corpus/testdata/pipeline/09_goblock.txtar`:

```
gsx pipeline: {{ }} Go-statement block between markup siblings.

-- input.gsx --
package examples

component Chip(name string) {
	<div>
		{{ initials := f(name) }}
		<span>{initials}</span>
	</div>
}

-- ast.golden --
File package=examples
  Component name=Chip recv="" params="name string"
    Element tag=div void=false
      Text value="\n\t\t"
      GoBlock code="initials := f(name)"
      Text value="\n\t\t"
      Element tag=span void=false
        Interp expr="initials" try=false
      Text value="\n\t"

-- diagnostics.golden --
```

NOTE: the exact `Text value=…` whitespace nodes must match what the parser emits. After creating each `.txtar` in this task, run the pipeline test with `-update` (Step 8) to fill in the real `ast.golden`, then read the diff and confirm the structure (node kinds and key fields) matches the intent above. The whitespace Text nodes are expected; do not hand-tune them — trust the `-update` output once the node structure is verified correct.

- [ ] **Step 4: Create the `if` pipeline case**

Create `internal/corpus/testdata/pipeline/10_if.txtar`:

```
gsx pipeline: { if / else if / else } control flow.

-- input.gsx --
package examples

component Status(ok bool, warn bool) {
	<header>
		{ if ok {
			<h2>OK</h2>
		} else if warn {
			<h2>Warn</h2>
		} else {
			<h2>Fail</h2>
		} }
	</header>
}

-- diagnostics.golden --
```

(Leave `ast.golden` out of the archive initially; `-update` will add it. Verify the dump shows `IfMarkup` → `then:`/`else:` with a nested `IfMarkup` for the `else if`.)

- [ ] **Step 5: Create the `for` and `switch` pipeline cases**

Create `internal/corpus/testdata/pipeline/11_for.txtar`:

```
gsx pipeline: { for … } control flow with interpolation.

-- input.gsx --
package examples

component List(rows []string) {
	<ul>
		{ for _, r := range rows {
			<li>{r}</li>
		} }
	</ul>
}

-- diagnostics.golden --
```

Create `internal/corpus/testdata/pipeline/12_switch.txtar`:

```
gsx pipeline: { switch … } with case and default arms.

-- input.gsx --
package examples

component Badge(kind string) {
	<span>
		{ switch kind {
		case "warn":
			<b>warn</b>
		default:
			<b>info</b>
		} }
	</span>
}

-- diagnostics.golden --
```

- [ ] **Step 6: Create the conditional-attr and composable-class pipeline cases**

Create `internal/corpus/testdata/pipeline/13_cond_attr.txtar`:

```
gsx pipeline: in-tag { if … } conditional attribute composed with a spread.

-- input.gsx --
package examples

component Button(id string) {
	<button { if id != "" { id={id} } } {...attrs}>x</button>
}

-- diagnostics.golden --
```

Create `internal/corpus/testdata/pipeline/14_composable_class.txtar`:

```
gsx pipeline: composable class list with conditional contributions.

-- input.gsx --
package examples

component Link(active bool, class string) {
	<a class={
		"flex gap-2",
		"bg-active": active,
		class,
	}>link</a>
}

-- diagnostics.golden --
```

- [ ] **Step 7: Add fuzz seeds**

Create `parser/testdata/fuzz/FuzzParseFile/seed_control_flow` with the exact corpus-file format used by the existing seeds (match `seed_valid`'s `go test fuzz v1` header format — read `parser/testdata/fuzz/FuzzParseFile/seed_valid` first and mirror it). The seed string value:

```
package p
component C(xs []int, k string) {
	<ul>{ for _, x := range xs { <li>{ if x > 0 { <b>{x}</b> } else { zero } }</li> } }</ul>
	<span>{ switch k { case "a": <i>a</i> default: <i>z</i> } }</span>
}
```

Create `parser/testdata/fuzz/FuzzParseFile/seed_goblock` similarly, with value:

```
package p
component C(name string) {
	<div>{{ n := f(name) }}<button { if n != "" { disabled } } class={ "x", "y": n != "" }>{n}</button></div>
}
```

- [ ] **Step 8: Regenerate goldens and verify**

Run: `go test ./internal/corpus/ -update`
Then run: `go test ./...`
Expected: all PASS. Read each new `pipeline/*.txtar`'s now-populated `ast.golden` and confirm the node structure matches the construct (GoBlock, IfMarkup with nested else-if, ForMarkup, SwitchMarkup+CaseClause, CondAttr, ClassAttr with the right Parts). Read the regenerated `examples_coverage.golden` and confirm examples 03/05/07/08/09/11/12 now report `ok` (05/09/11 via conditional attrs + composable class; 03/07/08 already parsed but now produce structured control-flow nodes; 12 via the comment fix). 01/02/06/10 (and possibly 04) remain non-`ok` — those are the separate DOCTYPE / raw-text / HTML-comment core gaps, not Part 2.

- [ ] **Step 9: Run fuzz briefly to confirm no panics on the new constructs**

Run: `go test ./parser/ -run xxx -fuzz FuzzParseFile -fuzztime 20s`
Expected: no new crashers; `go test ./parser/` (the seed corpus) passes. (Use `-run xxx` to skip unit tests during the fuzz run.)

- [ ] **Step 10: Commit**

```bash
git add examples/12_children_attrs.gsx internal/corpus/testdata parser/testdata/fuzz
git commit -m "test(corpus): Part 2 pipeline cases, coverage regen, fuzz seeds; fix example 12 comment"
```

---

## Self-Review

**1. Spec coverage** (against `2026-06-18-gsx-templating-design.md` §4–5 and the Part 2 roadmap):
- `{{ stmt }}` escape hatch → Task 2 (GoBlock). ✓
- `{ if … } / { else if … } / { else … }` → Task 3. ✓
- `{ for … }` (range, C-style) → Task 4. ✓
- `{ switch … }` (case, default, tagless) → Task 5. ✓
- In-tag conditional attributes `{ if cond { attr } }` → Task 6. ✓
- Composable `class`/`style` comma + `"x": cond` colon grammar, depth-0 split, trailing comma, static `class="…"` still works (untouched StaticAttr path) → Task 7. ✓
- Disambiguation by leading token (`{{` vs `{ if/for/switch` vs `{ expr }`) → Task 2 + Tasks 3–5 (braceKeyword). ✓
- `?` try-marker → already implemented (Interp/ExprAttr `Try`); unchanged. Control-flow/GoBlock bodies that contain `{ expr? }` interpolations still capture `Try` via `parseInterp`. ✓
- AST/`Inspect`/`Fprint`/`SetSpan` parity for all new nodes → Task 1. ✓
- Out of scope, explicitly recorded: DOCTYPE, `<!-- -->`, raw-text `<script>`/`<style>`, markup-inside-`{{ }}`. ✓

**2. Placeholder scan:** No "TBD"/"handle edge cases"/"similar to Task N". Task 8's `ast.golden` bodies are intentionally generated via `-update` (whitespace Text nodes can't be reliably hand-authored), with an explicit structural verification step — this is a deterministic regenerate-then-verify, not a placeholder.

**3. Type consistency:**
- `parseBraceNode` returns `(ast.Markup, bool, error)` — used identically in `parseChildren`, `parseNodesUntilEOF` (Task 2), `parseControlBody` (Task 3), `parseCaseBody` (Task 5). ✓
- `scanToBlockBrace(src string, from int) (int, bool)` — used by `parseIfTail`, `parseForMarkup`, `parseSwitchMarkup`, `parseCondAttrTail`. ✓
- `parseSingleAttr` / `parseSpreadAttr` / `parseAttrsUntilBrace` signatures consistent between Tasks 6 and 7. ✓
- `parseComposedAttr(name string, startPos token.Pos)` mirrors the existing `parseAttrBraceValue(name string, attrStartPos token.Pos)` signature. ✓
- `ast.ClassPart` is a value struct (not a Node); `Inspect`/`SetSpan` never reference it; `Fprint` prints it inline. ✓
- Else-representation convention (`Else = []Markup{<*IfMarkup>}` / `[]Attr{<*CondAttr>}`) consistent between AST doc (Task 1), `parseIfTail` (Task 3), `parseCondAttrTail` (Task 6), and the `Fprint`/`Inspect` tests. ✓

---

## Execution Notes for the Controller

- Tasks are sequential: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8. Task 1 is foundational; Tasks 3–5 each extend the single `switch p.braceKeyword()` in `parseBraceNode`; Task 7 extends `parseSingleAttr` from Task 6.
- After Task 5, `internal/corpus`'s `TestExamplesCoverage` golden will be stale (control-flow examples now produce structured nodes). That is expected; it is regenerated in Task 8. Do not regenerate it earlier — the diff at Task 8 is the visible proof of the grammar landing.
- Model guidance: Tasks 1, 4, 8 are mechanical (cheap model). Tasks 2, 3, 5, 6, 7 carry recursive-descent judgment (standard model). The final whole-branch review uses the most capable model.
