# Processing Instructions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `<?marker name=…>` and `<?start name=…> … <?end>` processing-instruction markup to gsx, so pages can emit the placeholders used by declarative partial updates.

**Architecture:** Two new AST markup nodes (`ast.Marker`, `ast.MarkerRegion`) parsed by a `parsePI` branch in `parseElement`, symmetric to the existing `<!` → `parseBang`. The `name` value reuses gsx's existing attribute-value grammar and `*StaticAttr`/`*ExprAttr` nodes, so holes, pipelines, and LSP navigation come free. A new PI-name sink rejects `>` and `"` — the two bytes that cannot be escaped inside processing-instruction data.

**Tech Stack:** Go 1.26.1, standard library only in the runtime (root `gsx` package). Tooling (`internal/…`) may use `golang.org/x/tools`.

## Global Constraints

- **Design source of truth:** `docs/superpowers/specs/2026-07-24-processing-instructions-design.md`. Read it before Task 1.
- **Fixed vocabulary:** only `marker`, `start`, `end` are valid PI targets. Any other target is an error. General `<?target data?>` is explicitly out of scope.
- **Terminator:** gsx source uses `>`. `?>` is a diagnostic pointing at `>`.
- **`name` is required** on `marker` and `start`; `<?end>` takes no attributes.
- **Strict escaping, never silent:** static `name` containing `>` or `"` is a compile-time diagnostic; a dynamic one is a render error. Never strip.
- **Runtime is stdlib-only** — nothing added to the root package may import outside the standard library.
- **Do not hand-edit `.x.go` or golden files.** Regenerate: `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- **Work in this worktree** (`.claude/worktrees/processing-instructions`, branch `worktree-processing-instructions`). Never `cd` to the main checkout.
- **Before finishing:** `make check` must pass.

---

### Task 1: AST nodes

**Files:**
- Modify: `ast/ast.go` (add nodes next to `Doctype`/`HTMLComment` ~line 260-280; add `SetSpan` cases ~line 54; add `Inspect` cases)
- Test: `ast/ast_test.go` (create if absent)

**Interfaces:**
- Consumes: nothing.
- Produces: `ast.Marker{Name Attr}` and `ast.MarkerRegion{Name Attr, Children []Markup, ChildrenMultiline bool}`, both implementing `markupNode()`. Every later task uses these.

- [ ] **Step 1: Write the failing test**

In `ast/ast_test.go`:

```go
func TestMarkerNodesAreMarkup(t *testing.T) {
	var _ Markup = (*Marker)(nil)
	var _ Markup = (*MarkerRegion)(nil)
}

func TestInspectWalksMarkerRegion(t *testing.T) {
	region := &MarkerRegion{
		Name:     &StaticAttr{Name: "name", Value: "feed"},
		Children: []Markup{&Text{Value: "hi"}},
	}
	var seen []string
	Inspect(region, func(n Node) bool {
		switch n.(type) {
		case *MarkerRegion:
			seen = append(seen, "region")
		case *StaticAttr:
			seen = append(seen, "name")
		case *Text:
			seen = append(seen, "text")
		}
		return true
	})
	want := []string{"region", "name", "text"}
	if !slices.Equal(seen, want) {
		t.Fatalf("Inspect visited %v, want %v", seen, want)
	}
}
```

Import `slices` and `testing`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ast -run 'TestMarkerNodesAreMarkup|TestInspectWalksMarkerRegion'`
Expected: FAIL — build error, `undefined: Marker`.

- [ ] **Step 3: Add the nodes**

In `ast/ast.go`, immediately after the `HTMLComment` block:

```go
// Marker is a `<?marker name=…>` processing instruction: a void placeholder that
// declarative partial updates patch by name. Name is a *StaticAttr or *ExprAttr
// whose Name is "name" — reusing the attribute-value grammar means holes,
// pipelines, and editor navigation work with no extra machinery.
type Marker struct {
	span
	Name Attr
}

func (*Marker) markupNode() {}

// MarkerRegion is a `<?start name=…> … <?end>` processing-instruction pair. Its
// children are the temporary content shown until a patch replaces the region.
// The closing `<?end>` is consumed by the parser and is not a node.
type MarkerRegion struct {
	span
	Name              Attr
	Children          []Markup
	ChildrenMultiline bool
}

func (*MarkerRegion) markupNode() {}
```

- [ ] **Step 4: Wire SetSpan and Inspect**

In the `SetSpan` type switch (~line 54, beside `case *Doctype:`), add:

```go
	case *Marker:
		v.span = s
	case *MarkerRegion:
		v.span = s
```

In `Inspect`, add cases that walk the name attribute and children (follow the shape used by `*Element`):

```go
	case *Marker:
		if n.Name != nil {
			Inspect(n.Name, f)
		}
	case *MarkerRegion:
		if n.Name != nil {
			Inspect(n.Name, f)
		}
		for _, c := range n.Children {
			Inspect(c, f)
		}
```

Place them so they match the existing switch's ordering conventions; read the surrounding cases first.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./ast/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add ast/
git commit -m "ast: Marker and MarkerRegion processing-instruction nodes"
```

---

### Task 2: Parser terminator generalization (pure refactor)

Two parser helpers hardcode their terminator. PIs need different ones, so generalize first with **no behavior change** — this task must not alter a single golden.

**Files:**
- Modify: `parser/markup.go` (`parseAttrs` ~line 701; `parseChildren` ~line 952)
- Test: existing suites are the test — this is a refactor.

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func (p *parser) parseAttrsUntil(stop func() bool) (attrs []ast.Attr, multiline bool, err error)` — `parseAttrs()` becomes a wrapper passing the `>` / `/>` stop.
  - `type childTerm struct { tag string; piEnd bool }` and `func (p *parser) parseChildrenTerm(term childTerm) ([]ast.Markup, token.Pos, error)` — `parseChildren(closeTag string)` becomes a wrapper passing `childTerm{tag: closeTag}`.

- [ ] **Step 1: Capture the baseline**

Run: `go test ./parser/... ./internal/corpus -run 'TestParse|TestCorpus'`
Expected: PASS. Record that it passed — this task's success criterion is that the same command still passes unchanged.

- [ ] **Step 2: Generalize parseAttrs**

Rename the existing `parseAttrs` body to `parseAttrsUntil(stop func() bool)`, replacing its terminator check

```go
		if p.peek() == '>' || p.at("/>") {
			return attrs, sawNewline && len(attrs) > 0, nil
		}
```

with

```go
		if stop() {
			return attrs, sawNewline && len(attrs) > 0, nil
		}
```

Then add the wrapper, preserving the original doc comment on it:

```go
func (p *parser) parseAttrs() (attrs []ast.Attr, multiline bool, err error) {
	return p.parseAttrsUntil(func() bool { return p.peek() == '>' || p.at("/>") })
}
```

- [ ] **Step 3: Generalize parseChildren**

Add above `parseChildren`:

```go
// childTerm describes how a child list ends: a `</tag>` close tag (tag is "" for
// a fragment's `</>`), or a `<?end>` processing instruction closing a
// MarkerRegion. Exactly one form applies per list.
type childTerm struct {
	tag   string
	piEnd bool
}
```

Rename the existing `parseChildren` body to `parseChildrenTerm(term childTerm)`, replacing every use of `closeTag` with `term.tag`, and add the wrapper:

```go
func (p *parser) parseChildren(closeTag string) ([]ast.Markup, token.Pos, error) {
	return p.parseChildrenTerm(childTerm{tag: closeTag})
}
```

Leave the `<?end>` handling for Task 4 — this step only moves code.

- [ ] **Step 4: Verify nothing changed**

Run: `go test ./parser/... ./internal/corpus -run 'TestParse|TestCorpus'`
Expected: PASS, identical to Step 1. If any golden differs, the refactor changed behavior — revert and redo.

- [ ] **Step 5: Commit**

```bash
git add parser/markup.go
git commit -m "parser: generalize attr and child terminators (no behavior change)"
```

---

### Task 3: Parse `<?marker name=…>`

**Files:**
- Modify: `parser/markup.go` (`parseElement` ~line 766; add `parsePI` next to `parseBang`)
- Modify: `parser/identifier.go` (`startsTagAt` ~line 97)
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.Marker` (Task 1); `parseAttrsUntil` (Task 2).
- Produces: `func (p *parser) parsePI(start int, startPos token.Pos) (ast.Markup, error)` — cursor at `?`, `start` is the offset of `<`. Task 4 extends it with the `start`/`end` targets.

- [ ] **Step 1: Write the failing tests**

In `parser/markup_test.go`:

```go
func TestParseMarkerStatic(t *testing.T) {
	p := testParser(`<?marker name="results">`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	m, ok := n.(*ast.Marker)
	if !ok {
		t.Fatalf("got %T, want *ast.Marker", n)
	}
	a, ok := m.Name.(*ast.StaticAttr)
	if !ok || a.Name != "name" || a.Value != "results" {
		t.Fatalf("name = %#v", m.Name)
	}
}

func TestParseMarkerExpr(t *testing.T) {
	p := testParser(`<?marker name={item.ID}>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	m := n.(*ast.Marker)
	a, ok := m.Name.(*ast.ExprAttr)
	if !ok || a.Expr != "item.ID" {
		t.Fatalf("name = %#v", m.Name)
	}
}

func TestParseMarkerErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`<?nope name="x">`, "unknown processing-instruction target"},
		{`<?marker>`, "requires a `name`"},
		{`<?marker name="x"?>`, "use `>`"},
		{`<?marker name="x" id="y">`, "only a `name`"},
		{`<?end>`, "without a matching"},
	} {
		p := testParser(tc.src)
		_, err := p.parseElement()
		if err == nil {
			t.Fatalf("%s: want error", tc.src)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want containing %q", tc.src, err, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./parser -run TestParseMarker`
Expected: FAIL — `<?marker …>` currently errors with "expected tag name".

- [ ] **Step 3: Add the `?` branch and parsePI**

In `parseElement`, directly after the `<!` branch:

```go
	// `<?…`: a processing instruction (fixed marker/start/end vocabulary).
	if p.peek() == '?' {
		return p.parsePI(start, startPos)
	}
```

Add near `parseBang`:

```go
// Processing-instruction targets gsx accepts. The HTML tokenizer allows any
// target matching [A-Za-z_][A-Za-z0-9_-]* (whatwg/html#12118), but only these
// three carry defined semantics (declarative partial updates), so gsx rejects
// everything else rather than emit markup whose meaning it cannot describe.
const (
	piMarker = "marker"
	piStart  = "start"
	piEnd    = "end"
)

// parsePI parses a processing instruction. The cursor is at the '?' following
// '<'; start is the byte offset of that '<' and startPos describes it. `<?end>`
// is only meaningful as a MarkerRegion terminator, so it is an error here —
// parseChildrenTerm consumes the legitimate ones.
func (p *parser) parsePI(start int, startPos token.Pos) (ast.Markup, error) {
	p.i++ // past '?'
	targetStart := p.i
	p.i = scanTagName(p.src, p.i)
	target := p.src[targetStart:p.i]
	switch target {
	case piMarker:
		name, err := p.parsePIName(target, startPos)
		if err != nil {
			return nil, err
		}
		m := &ast.Marker{Name: name}
		ast.SetSpan(m, startPos, p.posAt(p.i))
		return m, nil
	case piEnd:
		return nil, p.errorf(startPos, "`<?end>` without a matching `<?start`")
	}
	return nil, p.errorf(startPos, "unknown processing-instruction target %q, expected `marker`, `start`, or `end`", target)
}

// parsePIName parses the required `name=…` of a marker/start PI and consumes the
// closing '>'. It returns the name attribute, which is a *ast.StaticAttr or
// *ast.ExprAttr.
func (p *parser) parsePIName(target string, startPos token.Pos) (ast.Attr, error) {
	attrs, _, err := p.parseAttrsUntil(func() bool { return p.peek() == '>' || p.at("?>") })
	if err != nil {
		return nil, err
	}
	if p.at("?>") {
		return nil, p.errorf(p.pos(), "`?>` does not close a gsx processing instruction; use `>`")
	}
	if len(attrs) == 0 {
		return nil, p.errorf(startPos, "`<?%s` requires a `name` attribute", target)
	}
	if len(attrs) > 1 {
		return nil, p.errorf(startPos, "`<?%s` takes only a `name` attribute", target)
	}
	name := attrs[0]
	switch a := name.(type) {
	case *ast.StaticAttr:
		if a.Name != "name" {
			return nil, p.errorf(a.Pos(), "`<?%s` requires a `name` attribute, got %q", target, a.Name)
		}
	case *ast.ExprAttr:
		if a.Name != "name" {
			return nil, p.errorf(a.Pos(), "`<?%s` requires a `name` attribute, got %q", target, a.Name)
		}
	default:
		return nil, p.errorf(name.Pos(), "`<?%s` requires a `name=\"…\"` or `name={…}` attribute", target)
	}
	p.i++ // past '>'
	return name, nil
}
```

- [ ] **Step 4: Let `<?` start markup in Go-expression and markup-attribute positions**

In `parser/identifier.go`, `startsTagAt`:

```go
	if src[at] == '>' || src[at] == '/' || src[at] == '?' {
		return true
	}
```

This is what lets `<?marker …>` be recognized as an element literal (`parser/goexpr.go:539`) and inside a markup attribute (`parser/markup.go:738`). `<?` is not valid Go, so no Go expression is misread.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./parser -run TestParseMarker && go test ./parser/...`
Expected: PASS, and no regression in the rest of the parser suite.

- [ ] **Step 6: Commit**

```bash
git add parser/
git commit -m "parser: parse <?marker name=…> processing instructions"
```

---

### Task 4: Parse `<?start name=…> … <?end>`

**Files:**
- Modify: `parser/markup.go` (`parsePI`; `parseChildrenTerm`)
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `ast.MarkerRegion` (Task 1); `parseChildrenTerm`/`childTerm` (Task 2); `parsePI`/`parsePIName` (Task 3).
- Produces: nothing new — extends `parsePI`.

- [ ] **Step 1: Write the failing tests**

```go
func TestParseMarkerRegion(t *testing.T) {
	p := testParser(`<?start name="feed"><p>loading</p><?end>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := n.(*ast.MarkerRegion)
	if !ok {
		t.Fatalf("got %T, want *ast.MarkerRegion", n)
	}
	if a := r.Name.(*ast.StaticAttr); a.Value != "feed" {
		t.Fatalf("name = %q", a.Value)
	}
	if len(r.Children) != 1 {
		t.Fatalf("children = %#v", r.Children)
	}
	if el, ok := r.Children[0].(*ast.Element); !ok || el.Tag != "p" {
		t.Fatalf("child = %#v", r.Children[0])
	}
}

func TestParseMarkerRegionNested(t *testing.T) {
	p := testParser(`<?start name="a"><?start name="b"><?end><?marker name="c"><?end>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	r := n.(*ast.MarkerRegion)
	if len(r.Children) != 2 {
		t.Fatalf("children = %#v", r.Children)
	}
	if _, ok := r.Children[0].(*ast.MarkerRegion); !ok {
		t.Fatalf("child0 = %T", r.Children[0])
	}
	if _, ok := r.Children[1].(*ast.Marker); !ok {
		t.Fatalf("child1 = %T", r.Children[1])
	}
}

func TestParseMarkerRegionUnterminated(t *testing.T) {
	p := testParser(`<?start name="feed"><p>x</p>`)
	if _, err := p.parseElement(); err == nil || !strings.Contains(err.Error(), "<?end>") {
		t.Fatalf("err = %v, want one naming <?end>", err)
	}
}

func TestParseEndPIRejectsAttrs(t *testing.T) {
	p := testParser(`<?start name="feed"><?end name="feed">`)
	if _, err := p.parseElement(); err == nil || !strings.Contains(err.Error(), "takes no attributes") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./parser -run TestParseMarkerRegion`
Expected: FAIL — `unknown processing-instruction target "start"`.

- [ ] **Step 3: Handle the `start` target**

In `parsePI`'s switch, add before `case piEnd:`:

```go
	case piStart:
		name, err := p.parsePIName(target, startPos)
		if err != nil {
			return nil, err
		}
		childrenMultiline := newlineFollows(p.src, p.i)
		children, _, err := p.parseChildrenTerm(childTerm{piEnd: true})
		if err != nil {
			return nil, err
		}
		r := &ast.MarkerRegion{Name: name, Children: children, ChildrenMultiline: childrenMultiline}
		ast.SetSpan(r, startPos, p.posAt(p.i))
		return r, nil
```

- [ ] **Step 4: Terminate child lists on `<?end>`**

In `parseChildrenTerm`, before the `p.at("</")` branch:

```go
		if term.piEnd && p.atPITarget(piEnd) {
			endPos := p.pos()
			p.i += len("<?") + len(piEnd)
			attrs, _, err := p.parseAttrsUntil(func() bool { return p.peek() == '>' || p.at("?>") })
			if err != nil {
				return nil, token.NoPos, err
			}
			if p.at("?>") {
				return nil, token.NoPos, p.errorf(p.pos(), "`?>` does not close a gsx processing instruction; use `>`")
			}
			if len(attrs) > 0 {
				return nil, token.NoPos, p.errorf(endPos, "`<?end>` takes no attributes")
			}
			p.i++ // past '>'
			return nodes, token.NoPos, nil
		}
```

Add the helper beside `parsePI`:

```go
// atPITarget reports whether the cursor is at `<?` followed by exactly target
// (so `<?end>` matches but `<?ending>` does not).
func (p *parser) atPITarget(target string) bool {
	if !p.at("<?") {
		return false
	}
	end := scanTagName(p.src, p.i+len("<?"))
	return p.src[p.i+len("<?"):end] == target
}
```

Fix the EOF message so it names the right terminator:

```go
		if p.eof() {
			if term.piEnd {
				return nil, token.NoPos, p.errorf(token.NoPos, "unexpected EOF, expected <?end>")
			}
			return nil, token.NoPos, p.errorf(token.NoPos, "unexpected EOF, expected </%s>", term.tag)
		}
```

Nesting works without extra code: an inner `<?start` is parsed by `parseElement` → `parsePI`, which consumes its own `<?end>`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./parser/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add parser/
git commit -m "parser: parse <?start …> … <?end> processing-instruction regions"
```

---

### Task 5: Runtime PI-name sink

**Files:**
- Modify: `escape.go` (add `piNameInvalid`; `strings` already imported)
- Modify: `writer.go` (add `PIName`; `fmt` already imported)
- Test: `escape_test.go` or `writer_test.go` (root package — follow whichever exists)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func piNameInvalid(s string) bool` (unexported)
  - `func (gw *Writer) PIName(s string)` — writes `s`, or sets the render error if `s` cannot be represented. Task 6's codegen emits calls to it.

- [ ] **Step 1: Write the failing test**

```go
func TestWriterPINameRejectsUnrepresentable(t *testing.T) {
	for _, s := range []string{`a>b`, `a"b`, `x?>y`} {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.PIName(s)
		if gw.Err() == nil {
			t.Fatalf("PIName(%q): want error, got none (wrote %q)", s, buf.String())
		}
	}
}

func TestWriterPINameAcceptsSafe(t *testing.T) {
	for _, s := range []string{`results`, `row-1`, `a?b`, `a<b`, `a'b`, `héllo`} {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.PIName(s)
		if gw.Err() != nil {
			t.Fatalf("PIName(%q): unexpected error %v", s, gw.Err())
		}
		if buf.String() != s {
			t.Fatalf("PIName(%q) wrote %q", s, buf.String())
		}
	}
}
```

`x?>y` is included to prove the `>` rule already covers the `?>` terminator.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestWriterPIName`
Expected: FAIL — `gw.PIName undefined`.

- [ ] **Step 3: Add the filter**

In `escape.go`:

```go
// piNameInvalid reports whether s cannot be emitted inside a processing
// instruction's name="…" value.
//
// Processing-instruction data is opaque: the HTML tokenizer performs no
// character-reference decoding inside it (whatwg/html#12118), so neither byte
// below has an escaped form. They can only be rejected.
//
//   - '>' ends the processing instruction, so a value containing one breaks out
//     of the PI and the remainder is parsed as live HTML.
//   - '"' ends the name="…" quoting that declarative partial updates reads, so a
//     value containing one can forge further pseudo-attributes.
//
// '?>' needs no separate rule: rejecting '>' already excludes it.
func piNameInvalid(s string) bool { return strings.ContainsAny(s, ">\"") }
```

- [ ] **Step 4: Add the Writer method**

In `writer.go`, beside the other sinks:

```go
// PIName writes s as a processing instruction's name="…" value. Unlike every
// other sink there is no escaping to fall back on, so an unrepresentable value
// is a render error rather than a silently altered name — a stripped name would
// mistarget the update with no signal.
func (gw *Writer) PIName(s string) {
	if gw.err != nil {
		return
	}
	if piNameInvalid(s) {
		gw.err = fmt.Errorf("gsx: processing-instruction name %q contains '>' or '\"', which cannot be escaped in processing-instruction data", s)
		return
	}
	_, gw.err = io.WriteString(gw.w, s)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test . -run TestWriterPIName && go test .`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add escape.go writer.go *_test.go
git commit -m "gsx: PI-name sink rejecting unescapable > and \""
```

---

### Task 6: Codegen

**Files:**
- Modify: `internal/codegen/emit.go` (`genNode` ~line 1890, beside `*ast.Doctype`)
- Create: `internal/corpus/testdata/cases/pi/marker_static.txtar`
- Create: `internal/corpus/testdata/cases/pi/marker_expr.txtar`

**Interfaces:**
- Consumes: `ast.Marker`/`ast.MarkerRegion` (Task 1); `Writer.PIName` (Task 5).
- Produces: rendered PI output; Task 8 extends the corpus matrix.

- [ ] **Step 1: Write the failing corpus cases**

`internal/corpus/testdata/cases/pi/marker_static.txtar`:

```
# A static <?marker name="…"> emits verbatim: the whole PI is one constant run,
# so codegen folds it into a single _gsxgw.S call with no runtime filtering.
-- input.gsx --
package views

component Page() {
	<div><?marker name="results"></div>
}
-- invoke --
Page()
-- render.golden --
-- generated.x.go.golden --
-- diagnostics.golden --
```

`internal/corpus/testdata/cases/pi/marker_expr.txtar`:

```
# A <?marker name={expr}> routes the value through the PI-name sink, which
# rejects '>' and '"' at render time (they cannot be escaped in PI data).
-- input.gsx --
package views

component Page(id string) {
	<?marker name={id}>
}
-- invoke --
Page("row-1")
-- render.golden --
-- generated.x.go.golden --
-- diagnostics.golden --
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/corpus -run 'TestCorpus/pi'`
Expected: FAIL — genNode has no case for the node, so generation errors or emits nothing.

- [ ] **Step 3: Add the genNode cases**

In `genNode`, beside `case *ast.Doctype:`:

```go
	case *ast.Marker:
		if !genPIOpen(b, "marker", t.Name, table, imports, rt, interpTemp, bag, resolved) {
			return false
		}
	case *ast.MarkerRegion:
		if !genPIOpen(b, "start", t.Name, table, imports, rt, interpTemp, bag, resolved) {
			return false
		}
		for _, c := range t.Children {
			if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
				return false
			}
		}
		emitS(b, "<?end>")
```

Add the helper near the other `emit*` helpers:

```go
// genPIOpen emits `<?target name="…">`. A static name is validated here and
// folded into the surrounding constant run; a dynamic one goes through the
// runtime PI-name sink, which errors on a value it cannot represent. Escaping is
// chosen by the node, never by attrclass name classification.
func genPIOpen(b *bytes.Buffer, target string, name ast.Attr, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) bool {
	switch a := name.(type) {
	case *ast.StaticAttr:
		if strings.ContainsAny(a.Value, ">\"") {
			bag.Errorf(a.Pos(), a.End(), "invalid-pi-name",
				"processing-instruction name %q contains '>' or '\"', which cannot be escaped in processing-instruction data", a.Value)
			return false
		}
		emitS(b, "<?"+target+` name="`+a.Value+`">`)
		return true
	case *ast.ExprAttr:
		emitS(b, "<?"+target+` name="`)
		// Emit the value expression through the PIName sink. Follow the existing
		// ExprAttr value path (pipeline stages, (T, error) unwrapping) used by
		// emitAttrValue for URL sinks — reuse it rather than re-deriving it.
		if !emitPIName(b, a, table, imports, rt, interpTemp, bag, resolved) {
			return false
		}
		emitS(b, `">`)
		return true
	}
	bag.Errorf(name.Pos(), name.End(), "invalid-pi-name", "processing-instruction name must be a string literal or {expr}")
	return false
}
```

Implement `emitPIName` by following how an existing dynamic attribute value is emitted for a URL sink in this file (search for `gw.URL(` / `urlWriterMethod`) so pipeline stages and `(T, error)` unwrapping behave identically; the only difference is the emitted method name `PIName`.

- [ ] **Step 4: Generate and inspect the goldens**

Run: `go test ./internal/corpus -run 'TestCorpus/pi' -update`

Then read both `.txtar` files and confirm by eye:
- `marker_static` renders `<div><?marker name="results"></div>` and its generated code contains one `_gsxgw.S("<div><?marker name=\"results\"></div>")`-style constant run.
- `marker_expr` renders `<?marker name="row-1">` and its generated code calls `_gsxgw.PIName(id)` between two `S` calls.

If either is wrong, fix the implementation — **never hand-edit a golden**.

- [ ] **Step 5: Verify without -update**

Run: `go test ./internal/corpus -run 'TestCorpus/pi'`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/
git commit -m "codegen: emit <?marker> and <?start>…<?end>"
```

---

### Task 7: Formatter

**Files:**
- Modify: `internal/printer/printer.go` (~line 718, beside `case *ast.Doctype:`)
- Modify: `internal/printer/segment.go` (`blockLevel` ~line 74)
- Create: `internal/gsxfmt/testdata/cases/pi_marker.txtar`
- Create: `internal/gsxfmt/testdata/cases/pi_region.txtar`

**Interfaces:**
- Consumes: `ast.Marker`/`ast.MarkerRegion` (Task 1).
- Produces: stable formatting; no API.

- [ ] **Step 1: Write the failing fmt cases**

Read an existing case in `internal/gsxfmt/testdata/cases/` first to copy the exact section layout. Then create `pi_marker.txtar` with an `input.gsx` whose PIs are badly indented, and `pi_region.txtar` covering a region with children and a nested region. Leave `fmt.golden` empty for now.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus`
Expected: FAIL — the printer has no case, so output is wrong or panics.

- [ ] **Step 3: Add printer cases**

In `printer.go`, beside `case *ast.Doctype:`:

```go
	case *ast.Marker:
		return pretty.Concat(pretty.Text("<?marker "), p.attr(v.Name), pretty.Text(">"))
	case *ast.MarkerRegion:
		return p.markerRegion(v)
```

Implement `markerRegion` by following how `p.element` lays out an open tag, indented children, and a close — the close is the literal `<?end>`. Reuse the existing children-layout helper rather than writing a new one, and honor `ChildrenMultiline` the same way `Element`/`Fragment` do.

Use the printer's existing attribute-printing entry point for `v.Name` (find what `p.element` calls for a single attribute) instead of the `p.attr` placeholder above if the real name differs.

- [ ] **Step 4: Mark both node types block-level**

In `segment.go`:

```go
	case *ast.Element, *ast.Fragment, *ast.IfMarkup, *ast.ForMarkup,
		*ast.SwitchMarkup, *ast.GoBlock, *ast.Doctype, *ast.HTMLComment,
		*ast.Marker, *ast.MarkerRegion:
		return true
```

- [ ] **Step 5: Generate and verify goldens**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -update`, read the resulting `fmt.golden` sections and confirm the layout is what you'd hand-write, then run without `-update`.
Expected: PASS

- [ ] **Step 6: Check formatter idempotence and the whole suite**

Run: `go test ./internal/gsxfmt/... ./internal/printer/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/printer/ internal/gsxfmt/
git commit -m "fmt: lay out processing instructions"
```

---

### Task 8: Corpus matrix and diagnostics

CLAUDE.md requires a corpus case per context in which new syntax is valid. Tasks 6 and 7 covered the two simplest; this task completes the matrix.

**Files:**
- Create, under `internal/corpus/testdata/cases/pi/`:
  - `marker_pipeline.txtar` — `<?marker name={id |> upper}>` (use a filter the corpus already registers; check `internal/corpus/testdata/cases/pipelines/` for one)
  - `region_children.txtar` — region with element + text + interpolation children
  - `region_nested.txtar` — region containing a region and a marker
  - `pi_in_control_flow.txtar` — markers inside `{ if … }` and `{ for … }`
  - `pi_element_literal.txtar` — `x := <?marker name="a">` in Go-expression position, rendered via a component
  - `e_unknown_target.txtar`, `e_missing_name.txtar`, `e_end_attrs.txtar`, `e_unterminated_region.txtar`, `e_stray_end.txtar`, `e_question_terminator.txtar`, `e_static_name_unsafe.txtar` — error cases with only `diagnostics.golden` (no `invoke`), following `internal/corpus/testdata/cases/parser/e03_bad_attr_name.txtar`
- Modify: `internal/corpus/testdata/coverage.golden` (regenerated, never hand-edited)

**Interfaces:**
- Consumes: everything from Tasks 3–7.
- Produces: the pinned behavior contract.

- [ ] **Step 1: Write the cases**

Each begins with a `#` comment stating what it pins and why — read two or three neighbors first to match the house voice. Renderable cases need `input.gsx`, `invoke`, and empty golden sections; error cases need `input.gsx` and `diagnostics.golden` only.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/corpus -run 'TestCorpus/pi'`
Expected: FAIL — goldens are empty.

- [ ] **Step 3: Generate goldens**

Run: `go test ./internal/corpus -run 'TestCorpus/pi' -update`

- [ ] **Step 4: Read every generated golden**

For each case, confirm the render and diagnostics are genuinely what the feature should do. An `-update` run makes tests pass by definition — the review is the actual test. Pay attention to:
- `e_static_name_unsafe` must produce a **positioned** diagnostic, not an unpositioned one.
- `pi_element_literal` must actually render the PI, not silently drop it.

- [ ] **Step 5: Verify clean, then run the full corpus**

Run: `go test ./internal/corpus -run TestCorpus`
Expected: PASS, including the regenerated `coverage.golden` manifest.

- [ ] **Step 6: Commit**

```bash
git add internal/corpus/testdata/
git commit -m "corpus: processing-instruction matrix and diagnostics"
```

---

### Task 9: Docs and sibling grammars

**Files:**
- Create: `docs/guide/syntax/processing-instructions.md`
- Modify: `docs/guide/syntax.md` (add to the syntax index)
- Modify: `../gsxhq.github.io/.vitepress/config.mts` (sidebar entry)
- Modify: `../tree-sitter-gsx/grammar.js` (+ regenerate)
- Modify: `../vscode-gsx/syntaxes/gsx.tmLanguage.json`
- Modify: `../gsxhq.github.io` CodeMirror syntax (find the mode file)

**Interfaces:**
- Consumes: the shipped syntax.
- Produces: documentation and editor support.

- [ ] **Step 1: Write the guide page**

Keep it concise — behavior plainly, rationale in the spec. Cover: the two forms; that `name` takes a literal or `{expr}`; the fixed `marker`/`start`/`end` vocabulary and why; that `>` and `"` in a name are rejected (compile-time for literals, render error for expressions); and a short example pairing a marker with `<template for="…">`.

Note: `docs/guide/**` is the canonical source — the website syncs it. Any literal `{{ }}` in prose must be wrapped in a `::: v-pre` block or the VitePress build fails.

- [ ] **Step 2: Add the sidebar entry**

In `../gsxhq.github.io/.vitepress/config.mts`, add the page beside the other syntax entries.

- [ ] **Step 3: Verify the docs build**

Run, from `../gsxhq.github.io`: `node scripts/sync-docs.mjs && npm run build`
Expected: build succeeds with no dead links.

- [ ] **Step 4: Update the three grammars**

Add both PI forms to the tree-sitter grammar (regenerate per that repo's README), the VS Code TextMate grammar, and the site's CodeMirror mode. Verify highlighting on a sample file for each.

- [ ] **Step 5: Commit (separate commit per repo)**

```bash
git add docs/
git commit -m "docs: processing-instruction syntax"
```

Commit the sibling repos in their own working copies; they are separate repositories and need their own PRs.

---

### Task 10: Final gate

- [ ] **Step 1: Run the authoritative gate**

Run: `make ci`
Expected: PASS. Do not interpret a non-zero exit as anything but failure, and never chain a merge on a swallowed exit code.

- [ ] **Step 2: Run the linter**

Run: `make lint`
Expected: PASS

- [ ] **Step 3: Request review**

Use the `superpowers:requesting-code-review` skill, then an independent adversarial reviewer that builds throwaway probe programs — in particular one that renders a `<?marker name={x}>` with `x` containing `>` and confirms the render errors rather than emitting broken markup.

## Self-Review Notes

Spec coverage check — every spec section maps to a task: syntax → 3, 4; AST → 1; parser → 2, 3, 4; escaping → 5, 6; codegen → 6; formatter → 7; diagnostics → 3, 4, 6, 8; testing → 6, 7, 8; siblings → 9. The `?>` diagnostic decided during spec self-review is covered in Tasks 3 and 8 (`e_question_terminator`).

Deliberately deferred, per the spec: general `<?target data?>`, pseudo-attributes beyond `name`, and any `<template for>` sugar.
