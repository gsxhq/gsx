# Ordered Attributes (`{{ }}`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `gsx.OrderedAttrs` runtime type and a `name={{ "k": v, … }}` attribute-value literal so a caller can pass an order-preserving attribute bag to a component prop that spreads it (e.g. Datastar `data-*` directives).

**Architecture:** Four layers, each independently testable. (1) Runtime: an ordered slice type + an order-preserving `SpreadOrdered` writer method that reuses `Spread`'s exact escaping. (2) Codegen dispatch: track which props are typed `gsx.OrderedAttrs` and emit `SpreadOrdered` instead of the sorted `Spread` when spreading one. (3) Parser: recognise `name={{ … }}` and build a new AST node. (4) Codegen lowering: emit the literal as a `gsx.OrderedAttrs{…}` composite literal at the component call site. Layer (2) is provable end-to-end with a hand-written Go literal (no sugar), so the sugar in (3)+(4) lands on a verified runtime+dispatch base.

**Tech Stack:** Go 1.26.1, `go/scanner` + `go/token` (parser), `go/types` (codegen resolution), the txtar corpus harness (`internal/corpus`).

## Global Constraints

- **Runtime root is standard-library only.** `gsx.OrderedAttrs`, `Attr`, and `SpreadOrdered` use only stdlib. Tooling (`parser`, `internal/codegen`) may use `golang.org/x/tools`.
- **Security escaping is a faithful `html/template` port, never an approximation.** `SpreadOrdered` reuses `validAttrName` + `AttrValue` unchanged; only the key sort is removed vs `Spread`.
- **No "simple heuristics" in core logic** — real implementations only.
- **Every syntax/codegen change ships a corpus case** pinning `input.gsx` + (where relevant) `generated.x.go.golden` + `render.golden`. Regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify WITHOUT `-update`. A forgotten `coverage.golden` bump fails the suite.
- **Don't hand-edit `.x.go` or golden files** — change the source and regenerate.
- Run `make check` (inner loop) before declaring a task done; `make ci` before merge.
- Pin Go to `GO_VERSION` (1.26.1) — a different minor reintroduces gofmt drift.
- Commit frequently, one logical change per commit.

## File Structure

- **Create** `orderedattrs.go` (root pkg) — `Attr`, `OrderedAttrs`, `Writer.SpreadOrdered`. (Keeps the new type out of the already-busy `attrs.go`; same package.)
- **Create** `orderedattrs_test.go` (root pkg) — unit tests for `SpreadOrdered`.
- **Modify** `ast/ast.go` — add `OrderedPair` + `OrderedAttrsAttr` node.
- **Modify** `parser/attrs.go` — detect `={{`, add `parseOrderedAttrsLiteral`.
- **Modify** `parser/markup.go:190`-area dispatch only if needed (detection lives in `parseSingleAttr`, `attrs.go:156`).
- **Modify** `internal/codegen/analyze.go` — track `orderedProps` (params/fields typed `gsx.OrderedAttrs`), mirroring `nodeProps`.
- **Modify** `internal/codegen/emit.go` — (a) spread dispatch to `SpreadOrdered`; (b) lower `OrderedAttrsAttr` to a `gsx.OrderedAttrs{…}` field assignment in `genChildComponent`.
- **Create** corpus cases under `internal/corpus/testdata/cases/orderedattrs/`.
- **Modify** `docs/guide/` (attributes/config page) + `docs/ROADMAP.md`.

---

### Task 1: Runtime type + `SpreadOrdered`

**Files:**
- Create: `orderedattrs.go`
- Test: `orderedattrs_test.go`

**Interfaces:**
- Consumes: existing `Writer` helpers in `attrs.go`/`writer.go` — `gw.err`, `gw.writeStr(string)`, `gw.AttrValue(string)`, `gw.BoolAttr(name string, on bool)`, and package funcs `validAttrName(string) bool`, `toStr(any) string`.
- Produces: `type Attr struct { Key string; Value any }`, `type OrderedAttrs []Attr`, `func (gw *Writer) SpreadOrdered(ctx context.Context, a OrderedAttrs)`.

- [ ] **Step 1: Write the failing test**

In `orderedattrs_test.go`:

```go
package gsx

import (
	"bytes"
	"context"
	"testing"
)

func renderSpreadOrdered(a OrderedAttrs) string {
	var b bytes.Buffer
	gw := W(&b)
	gw.SpreadOrdered(context.Background(), a)
	if err := gw.Err(); err != nil {
		panic(err)
	}
	return b.String()
}

func TestSpreadOrderedPreservesOrderNoSort(t *testing.T) {
	// Keys deliberately NOT alphabetical: a sorted bag would reorder them.
	got := renderSpreadOrdered(OrderedAttrs{
		{Key: "data-signals", Value: "{count:0}"},
		{Key: "data-text", Value: "$count"},
		{Key: "data-a", Value: "z"},
	})
	want := ` data-signals="{count:0}" data-text="$count" data-a="z"`
	if got != want {
		t.Fatalf("order not preserved\n got: %q\nwant: %q", got, want)
	}
}

func TestSpreadOrderedBoolAndEscapingAndUnsafe(t *testing.T) {
	got := renderSpreadOrdered(OrderedAttrs{
		{Key: "data-show", Value: true},   // bare when true
		{Key: "data-hide", Value: false},  // omitted when false
		{Key: "title", Value: `a"b`},      // attribute-escaped
		{Key: "bad name", Value: "x"},     // unsafe name -> dropped
	})
	want := ` data-show title="a&#34;b"`
	if got != want {
		t.Fatalf("bool/escape/unsafe wrong\n got: %q\nwant: %q", got, want)
	}
}

func TestSpreadOrderedEmptyNoop(t *testing.T) {
	if got := renderSpreadOrdered(OrderedAttrs{}); got != "" {
		t.Fatalf("empty bag should write nothing, got %q", got)
	}
	if got := renderSpreadOrdered(nil); got != "" {
		t.Fatalf("nil bag should write nothing, got %q", got)
	}
}

func TestSpreadOrderedDuplicateKeysTolerated(t *testing.T) {
	got := renderSpreadOrdered(OrderedAttrs{
		{Key: "data-x", Value: "1"},
		{Key: "data-x", Value: "2"},
	})
	want := ` data-x="1" data-x="2"` // emitted in order; browser applies first-wins
	if got != want {
		t.Fatalf("dup keys\n got: %q\nwant: %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestSpreadOrdered -v`
Expected: FAIL — `OrderedAttrs`/`SpreadOrdered` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `orderedattrs.go`:

```go
package gsx

import "context"

// Attr is one ordered attribute pair. Value is rendered like an Attrs value: a
// bool toggles a bare boolean attribute; anything else is stringified (toStr)
// and attribute-escaped.
type Attr struct {
	Key   string
	Value any
}

// OrderedAttrs is an insertion-ordered, duplicate-tolerant attribute bag. Unlike
// Attrs (a map that Spread renders in sorted key order), OrderedAttrs renders in
// slice order — for callers who must control attribute order (e.g. Datastar
// data-* directives). Construct it directly or via the gsx `{{ "k": v }}` literal.
type OrderedAttrs []Attr

// SpreadOrdered writes the pairs of a in slice order. It mirrors Spread's
// per-attribute behavior exactly — the same validAttrName gate (a structurally
// unsafe name is dropped, never emitted), the same bool handling (a true bool is
// a bare attribute, false is omitted), and the same AttrValue escaping — and
// differs ONLY in that it does not sort. An empty/nil bag writes nothing.
func (gw *Writer) SpreadOrdered(ctx context.Context, a OrderedAttrs) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	for _, kv := range a {
		if !validAttrName(kv.Key) {
			continue // unsafe/invalid attribute name — drop it
		}
		if b, ok := kv.Value.(bool); ok {
			gw.BoolAttr(kv.Key, b)
			continue
		}
		gw.writeStr(" ")
		gw.writeStr(kv.Key)
		gw.writeStr(`="`)
		gw.AttrValue(toStr(kv.Value))
		gw.writeStr(`"`)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestSpreadOrdered -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add orderedattrs.go orderedattrs_test.go
git commit -m "feat(runtime): gsx.OrderedAttrs + Writer.SpreadOrdered (order-preserving spread)"
```

---

### Task 2: Codegen — track `orderedProps` and dispatch `SpreadOrdered`

This proves the runtime+codegen path end-to-end using a **hand-written** `gsx.OrderedAttrs{…}` value in the existing `={expr}` form — no new syntax yet. A component declares a `gsx.OrderedAttrs` param, spreads it with `{ p... }`, and a caller passes a literal; the spread must emit `SpreadOrdered` (no sort) so order survives.

**Files:**
- Modify: `internal/codegen/analyze.go` (`componentPropFieldsFor` loop near `:84`; `isGsxNodeType` near `:244`)
- Modify: `internal/codegen/emit.go` (spread emission at `:604` and `:1299`; thread the new set through the same call chain as `nodeProps`)
- Test: `internal/corpus/testdata/cases/orderedattrs/spread_preserves_order.txtar`

**Interfaces:**
- Consumes: Task 1's `gsx.OrderedAttrs` + `SpreadOrdered`.
- Produces: an `orderedProps map[string]map[string]bool` (propsType → fieldName → true) recording params/fields whose declared type is exactly `gsx.OrderedAttrs`, mirroring `nodeProps`; and spread emission that, when the spread subject resolves to such a field, emits `_gsxgw.SpreadOrdered(ctx, <expr>)`.

- [ ] **Step 1: Write the failing corpus case**

Create `internal/corpus/testdata/cases/orderedattrs/spread_preserves_order.txtar`:

```
# Spreading a gsx.OrderedAttrs param emits SpreadOrdered (slice order, NO sort).
# Hand-written literal in the existing ={expr} form — no {{ }} sugar yet.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Box(bag gsx.OrderedAttrs) {
	<div { bag... }>x</div>
}

component Page() {
	<Box bag={gsx.OrderedAttrs{{Key: "data-signals", Value: "{c:0}"}, {Key: "data-text", Value: "$c"}, {Key: "data-a", Value: "z"}}}/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
```

(Leave `generated.x.go.golden` absent for now; the render golden is the behavioral pin. `-update` fills `render.golden`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/corpus -run TestCorpus -update` then `go test ./internal/corpus -run TestCorpus`
Expected (before code change): the `<div { bag... }>` spread emits `Spread(ctx, bag)`, which does not compile (`bag` is `OrderedAttrs`, `Spread` wants `Attrs`) OR compiles against `Spread` and re-sorts to `data-a data-signals data-text`. Either way the case fails to render `data-signals data-text data-a` in order. Confirm the failure (compile error or wrong order) before proceeding.

- [ ] **Step 3: Track `orderedProps` in analyze**

In `internal/codegen/analyze.go`, add a helper mirroring `isGsxNodeType`:

```go
// isOrderedAttrsType reports whether a param's declared type string is exactly
// the gsx OrderedAttrs type (qualified or, in-package, bare).
func isOrderedAttrsType(typ string) bool {
	t := strings.TrimSpace(typ)
	return t == "gsx.OrderedAttrs" || t == "_gsxrt.OrderedAttrs" || t == "OrderedAttrs"
}
```

In `componentPropFieldsFor` (the loop that builds `propFields`/`nodeProps`, around `:84` where `isGsxNodeType(p.typ)` is checked), populate a parallel `orderedProps` map keyed identically (propsType → fieldName → true) when `isOrderedAttrsType(p.typ)`. Return it alongside `nodeProps`. (Match the exact qualifier forms the codebase uses for the runtime import alias — grep `_gsxrt` in emit.go to confirm; adjust `isOrderedAttrsType` to whatever alias the generated import uses.)

- [ ] **Step 4: Dispatch `SpreadOrdered` in emit**

Thread `orderedProps` through the same call chain that carries `nodeProps` into the element/spread emitters. At each manual-spread emission site (`emit.go:604`, `:1299`), when the spread subject (the trimmed `SpreadAttr.Expr`, or a resolved selector) names a field present in `orderedProps` for the enclosing component's props type, emit:

```go
fmt.Fprintf(b, "\t\t_gsxgw.SpreadOrdered(ctx, %s)\n", spreadExpr)
```

otherwise keep the existing `_gsxgw.Spread(ctx, %s)`. The root auto-spread of the implicit `Attrs` bag (`emit.go:556`) is untouched — it is always the map.

- [ ] **Step 5: Regenerate and verify order**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then inspect the case's `render.golden` — it MUST be:

```
<div data-signals="{c:0}" data-text="$c" data-a="z">x</div>
```

Run WITHOUT update: `go test ./internal/corpus -run TestCorpus`
Expected: PASS. If `render.golden` shows alphabetical (`data-a data-signals data-text`), dispatch did not take effect — fix before committing.

- [ ] **Step 6: Pin the generated output**

Add an empty `-- generated.x.go.golden --` section to the case, rerun `-update`, and confirm the generated body contains `_gsxgw.SpreadOrdered(ctx, bag)` (not `Spread`). Verify without `-update`.

- [ ] **Step 7: `make check` + commit**

Run: `make check`
Expected: all green.

```bash
git add internal/codegen/analyze.go internal/codegen/emit.go internal/corpus/testdata/cases/orderedattrs/spread_preserves_order.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): dispatch SpreadOrdered for gsx.OrderedAttrs props"
```

---

### Task 3: Parser — `name={{ "k": v, … }}` → `OrderedAttrsAttr`

**Files:**
- Modify: `ast/ast.go` (add nodes after `ClassAttr`, near `:366`)
- Modify: `parser/attrs.go` (`parseSingleAttr` `=`-then-`{` branch, `:190`; add `parseOrderedAttrsLiteral`)
- Test: `parser/orderedattrs_test.go` (new) + `internal/corpus/testdata/cases/orderedattrs/parse_literal.txtar` (ast.golden) + error cases

**Interfaces:**
- Consumes: `goExprEnd(src string, open int) (int, bool)` (`parser/boundary.go:14`); the `go/scanner` depth-split pattern from `splitComposed` (`parser/attrs.go`).
- Produces: `ast.OrderedPair{ Key string; Value string; ValuePos token.Pos }`, `ast.OrderedAttrsAttr{ Name string; Pairs []OrderedPair }` (embeds `span`, implements `attrNode()`). Parser builds this node for `name={{ … }}`.

- [ ] **Step 1: Add AST nodes (no test yet — exercised via parser test)**

In `ast/ast.go` after `ClassAttr`:

```go
// OrderedPair is one "key": value pair of an OrderedAttrsAttr. Key is the
// unquoted attribute name (string-literal key, already unquoted). Value is the
// raw Go expression source; ValuePos is the offset of its first char.
type OrderedPair struct {
	Key      string
	Value    string
	ValuePos token.Pos
}

// OrderedAttrsAttr is name={{ "k1": v1, "k2": v2 }} — an ordered attribute bag
// literal in attribute-value position. It lowers to a gsx.OrderedAttrs{…}
// composite literal bound to the matched prop field. Distinct from a body GoBlock
// ({{ stmt }}); the two never share a parse position.
type OrderedAttrsAttr struct {
	span
	Name  string
	Pairs []OrderedPair
}

func (*OrderedAttrsAttr) attrNode() {}
```

- [ ] **Step 2: Write the failing parser test**

In `parser/orderedattrs_test.go`:

```go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func parseOneAttrElement(t *testing.T, src string) *ast.OrderedAttrsAttr {
	t.Helper()
	f, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	// Walk to the single OrderedAttrsAttr in the file.
	var found *ast.OrderedAttrsAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if oa, ok := n.(*ast.OrderedAttrsAttr); ok {
			found = oa
		}
		return true
	})
	if found == nil {
		t.Fatal("no OrderedAttrsAttr parsed")
	}
	return found
}

func TestParseOrderedAttrsLiteral(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<Card container-attrs={{ \"data-signals\": sig, \"hx-on:click\": h, \"data-show\": true }}/>\n}\n"
	oa := parseOneAttrElement(t, src)
	if oa.Name != "container-attrs" {
		t.Fatalf("name = %q", oa.Name)
	}
	want := []struct{ k, v string }{
		{"data-signals", "sig"},
		{"hx-on:click", "h"},
		{"data-show", "true"},
	}
	if len(oa.Pairs) != len(want) {
		t.Fatalf("got %d pairs, want %d: %+v", len(oa.Pairs), len(want), oa.Pairs)
	}
	for i, w := range want {
		if oa.Pairs[i].Key != w.k || oa.Pairs[i].Value != w.v {
			t.Errorf("pair %d = %q:%q, want %q:%q", i, oa.Pairs[i].Key, oa.Pairs[i].Value, w.k, w.v)
		}
	}
}

func TestParseOrderedAttrsNestedBracesInValue(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<Card x={{ \"data-m\": map[string]int{\"a\": 1} }}/>\n}\n"
	oa := parseOneAttrElement(t, src)
	if len(oa.Pairs) != 1 || oa.Pairs[0].Value != `map[string]int{"a": 1}` {
		t.Fatalf("nested-brace value mis-parsed: %+v", oa.Pairs)
	}
}

func TestParseOrderedAttrsErrors(t *testing.T) {
	cases := map[string]string{
		"bare key":          "package p\ncomponent C() {\n\t<Card x={{ \"data-x\" }}/>\n}\n",
		"unquoted key":      "package p\ncomponent C() {\n\t<Card x={{ data-x: 1 }}/>\n}\n",
		"standalone spread": "package p\ncomponent C() {\n\t<div {{ \"data-x\": 1 }}>y</div>\n}\n",
	}
	for name, src := range cases {
		if _, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0); err == nil {
			t.Errorf("%s: expected a parse error, got none", name)
		}
	}
}
```

(Verified: `ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error)` at `parser/file.go:25`; `ast.Inspect(node, func(Node) bool)` at `ast/ast.go:389`; `ast.SetSpan` at `ast/ast.go:32`. The richer `[]Error` variant is `ParseFileWithClassifier` if you need per-error positions.)

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./parser -run TestParseOrderedAttrs -v`
Expected: FAIL — literal not recognised (no `OrderedAttrsAttr` produced) / errors not raised.

- [ ] **Step 4: Implement detection + parser**

In `parser/attrs.go`, in `parseSingleAttr`'s branch (currently):

```go
case p.peek() == '=' && p.i+1 < len(p.src) && p.src[p.i+1] == '{':
	p.i++ // past '='
	if name == "class" || name == "style" {
		return p.parseComposedAttr(name, attrStartPos)
	}
	return p.parseAttrBraceValue(name, attrStartPos)
```

insert the `{{` check right after `p.i++` (cursor now at the first `{`):

```go
	p.i++ // past '='
	if p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
		return p.parseOrderedAttrsLiteral(name, attrStartPos)
	}
	if name == "class" || name == "style" {
		...
```

Add `parseOrderedAttrsLiteral`. Cursor is at the first `{` of `{{`. Use `goExprEnd` to find the matching final `}`; the inner literal text is `src[firstBrace+2 : end-1]`. Split it with the `splitComposed` scanner pattern (top-level commas → segments; top-level colon → key/value). Require the key segment to be a single string literal (use `strconv.Unquote`); the value is the trimmed remainder (must be non-empty → else "bare key" error). On a standalone `{{` (the `parseSingleAttr` `p.peek()=='{'` branch at `:157`, NOT after `name=`), add an explicit rejection with a pointed message ("`{{ }}` is only valid as an attribute value `name={{ … }}`, not a standalone spread").

```go
func (p *parser) parseOrderedAttrsLiteral(name string, startPos token.Pos) (ast.Attr, error) {
	open := p.i // at first '{' of '{{'
	end, ok := goExprEnd(p.src, open)
	if !ok {
		return nil, p.errorf(p.posAt(open), "unterminated `{{` in %s value", name)
	}
	inner := p.src[open+2 : end-1] // text between '{{' and '}}'
	pairs, err := p.splitOrderedPairs(inner, open+2)
	if err != nil {
		return nil, err
	}
	p.i = end + 1
	n := &ast.OrderedAttrsAttr{Name: name, Pairs: pairs}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}
```

`splitOrderedPairs` mirrors `splitComposed`: scan `inner` with `go/scanner`, record depth-0 `COMMA` and `COLON` offsets, segment on commas, and for each non-empty segment take the first depth-0 colon as the key/value boundary. Unquote the key (error on non-string-literal key); error if no colon or empty value. Compute `ValuePos` as `base + valueStartOffset`.

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./parser -run TestParseOrderedAttrs -v`
Expected: PASS (all sub-tests, including the three error cases and the nested-brace value).

- [ ] **Step 6: Parser corpus case (ast snapshot)**

Create `internal/corpus/testdata/cases/orderedattrs/parse_literal.txtar` with an `input.gsx` using `name={{ … }}` and an `-- ast.golden --` section (a parser-layer snapshot — no codegen). Regenerate with `-update`, verify without.

- [ ] **Step 7: `make check` + commit**

```bash
git add ast/ast.go parser/attrs.go parser/orderedattrs_test.go internal/corpus/testdata/cases/orderedattrs/parse_literal.txtar internal/corpus/testdata/coverage.golden
git commit -m "feat(parser): ordered-attrs literal name={{ \"k\": v }} -> OrderedAttrsAttr"
```

---

### Task 4: Codegen — lower `OrderedAttrsAttr` to a `gsx.OrderedAttrs{…}` literal

Connects the sugar (Task 3) to the dispatch (Task 2): a `{{ … }}` value at a component call site becomes a `gsx.OrderedAttrs{…}` composite literal bound to the matched prop field.

**Files:**
- Modify: `internal/codegen/emit.go` (`genChildComponent`, `:1923`; the attr-binding loop using `matchField`, `:2158`+)
- Test: `internal/corpus/testdata/cases/orderedattrs/value_literal.txtar` (generated + render), plus context cases below

**Interfaces:**
- Consumes: `ast.OrderedAttrsAttr` (Task 3); `gsx.OrderedAttrs` + dispatch (Tasks 1–2); `matchField` (`fieldmatch.go`).
- Produces: in the child-component prop-binding path, an `*ast.OrderedAttrsAttr` whose name matches a field lowers to `FieldName: gsx.OrderedAttrs{{Key: "…", Value: <expr>}, …}`.

- [ ] **Step 1: Write the failing corpus case**

Create `internal/corpus/testdata/cases/orderedattrs/value_literal.txtar`:

```
# {{ }} value literal binds to a gsx.OrderedAttrs prop, spread in source order.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Card(containerAttrs gsx.OrderedAttrs) {
	<div class="container" { containerAttrs... }>{children}</div>
}

component Page() {
	<Card container-attrs={{ "data-signals": "{open:false}", "data-text": "$open", "data-a": "z" }}>hi</Card>
}
-- invoke --
Page()
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/corpus -run TestCorpus -update` then without `-update`.
Expected: FAIL — `genChildComponent` does not yet handle `*ast.OrderedAttrsAttr` (the attr is ignored / errors, no `ContainerAttrs:` assignment emitted).

- [ ] **Step 3: Implement lowering**

In `genChildComponent`'s attr loop (where `matchField(declared, t.Name, fm)` decides prop vs fallthrough, `emit.go:2158`+), add a case for `*ast.OrderedAttrsAttr`:
- Resolve the field via `matchField(declared, oa.Name, fm)` (e.g. `container-attrs` → `ContainerAttrs`). If it matches no field, emit a diagnostic: an ordered literal must bind to a declared `gsx.OrderedAttrs` field (it cannot fall through into the map `Attrs` bag).
- Emit the field assignment by building the composite literal:

```go
// for *ast.OrderedAttrsAttr oa bound to field fn:
var sb strings.Builder
fmt.Fprintf(&sb, "%s: %s.OrderedAttrs{", fn, rtAlias) // rtAlias = the runtime import alias used elsewhere in emit
for _, pr := range oa.Pairs {
	fmt.Fprintf(&sb, "{Key: %q, Value: %s}, ", pr.Key, pr.Value)
}
sb.WriteString("}")
// append sb.String() to the props-literal field list, same place ExprAttr fields go
```

Use the same runtime package alias the surrounding code uses for `gsx.Attrs` — `emit.go:2092` names it (the `<rtPkg>.Attrs` construction); reuse that `rtPkg` variable to build `<rtPkg>.OrderedAttrs{…}`. Ensure the runtime import is added to `imports` (same mechanism as other `gsx.` references).

- [ ] **Step 4: Regenerate + verify**

Run: `go test ./internal/corpus -run TestCorpus -update`
Inspect `value_literal.txtar`:
- `generated.x.go.golden` contains `ContainerAttrs: gsx.OrderedAttrs{{Key: "data-signals", Value: "{open:false}"}, {Key: "data-text", Value: "$open"}, {Key: "data-a", Value: "z"}}` and the component spread emits `SpreadOrdered`.
- `render.golden` is:
```
<div class="container" data-signals="{open:false}" data-text="$open" data-a="z">hi</div>
```
Run without `-update`: PASS.

- [ ] **Step 5: Add context + edge corpus cases**

Add cases under `internal/corpus/testdata/cases/orderedattrs/`:
- `bool_pair.txtar` — `{{ "data-show": true, "data-hide": false }}` → `data-show` present, `data-hide` absent.
- `escaping.txtar` — a value needing attribute escaping + an unsafe key dropped.
- `dup_keys.txtar` — duplicate key renders twice in order.
- `datastar_example.txtar` — the motivating ordered `data-*` directive set.
- `bind_no_field_error.txtar` — `{{ }}` bound to a name with no matching `gsx.OrderedAttrs` field → diagnostic (pin `diagnostics.golden`).
Regenerate; verify without `-update`.

- [ ] **Step 6: `make check` + commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/orderedattrs/ internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): lower {{ }} ordered-attrs literal to gsx.OrderedAttrs"
```

---

### Task 5: Docs + ROADMAP

**Files:**
- Modify: `docs/guide/` (the attributes/spread page — find it with `grep -rl "Attrs" docs/guide`)
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Document the feature**

In the attributes guide, add an "Ordered attributes" section covering: the `gsx.OrderedAttrs` type; the `name={{ "k": v }}` literal (quoted keys — explain why; bool form `"k": true`; values are plain Go expressions, `|>` pipelines not supported inside the literal); that it renders in source order vs the sorted `gsx.Attrs` map; the no-class/style-merge limitation; and the Datastar motivation. Contrast with `gsx.Attrs`.

- [ ] **Step 2: Update ROADMAP**

Add a line under the appropriate section noting ordered attributes (`{{ }}` / `gsx.OrderedAttrs`) shipped, dated 2026-06-29.

- [ ] **Step 3: Commit**

```bash
git add docs/guide docs/ROADMAP.md
git commit -m "docs: ordered attributes ({{ }} / gsx.OrderedAttrs)"
```

---

### Task 6: `make ci` + independent adversarial review

- [ ] **Step 1: Full CI**

Run: `make ci`
Expected: all green (build/vet/test both modules, examples drift, gofmt + gsx fmt). Fix any drift; regenerate goldens if needed.

- [ ] **Step 2: Independent adversarial review**

Per project convention, before merging dispatch one independent adversarial reviewer that builds throwaway probe programs (not just reads the diff). Probe ideas: a Datastar-style directive set whose required order is non-alphabetical (prove order survives); an unsafe key in a `{{ }}` literal (prove it's dropped, no tag breakout); a `}` inside a value string/composite literal (prove `}}` scanning isn't fooled); a `{{ }}` bound to a `gsx.Attrs` (map) prop and to no field (prove diagnostics); confirm an ordinary `{ attrs... }` map spread still sorts (no regression). Address findings before merge.

- [ ] **Step 3: Finish the branch**

Use superpowers:finishing-a-development-branch to choose merge/PR. The corpus commit (`6d50a02`) and this feature ride together on `worktree-ordered-attrs`.

---

## Self-Review

**Spec coverage:**
- Runtime `gsx.OrderedAttrs` + `SpreadOrdered` → Task 1. ✓
- `{{ }}` literal syntax (quoted keys, bool form, plain-expr values) → Task 3. ✓
- Type-directed `SpreadOrdered` dispatch → Task 2. ✓
- Value lowering to `gsx.OrderedAttrs{…}` → Task 4. ✓
- No class/style merge → inherent (Task 1 emits verbatim; Task 4 binds to a field, no merge path); documented Task 5. ✓
- No standalone spread / GoBlock coexistence → Task 3 rejection + error test. ✓
- Security (reuse `validAttrName`/`AttrValue`) → Task 1 test + Task 6 probe. ✓
- Duplicate-key tolerance, empty bag → Task 1 tests. ✓
- Cache key: no new knob → nothing to add (noted in spec); Task 6 `make ci` guards incremental output. ✓
- Docs + ROADMAP → Task 5. ✓
- Corpus per context (value/spread/bool/escaping/dup/error/datastar/parse) → Tasks 2–4. ✓

**Placeholder scan:** Codegen Tasks 2 & 4 intentionally defer exact threading/alias names to the implementer, but pin the observable contract precisely (the generated golden text + render golden) and name the exact functions/lines to modify and the pattern to mirror (`nodeProps`/`isGsxNodeType`). This is the gsx-canonical golden-driven workflow, not a placeholder. Verify alias/threading against the real code while implementing.

**Type consistency:** `Attr{Key, Value}`, `OrderedAttrs []Attr`, `SpreadOrdered(ctx, OrderedAttrs)`, `OrderedPair{Key, Value, ValuePos}`, `OrderedAttrsAttr{Name, Pairs}`, `orderedProps`/`isOrderedAttrsType` — names used consistently across tasks.
