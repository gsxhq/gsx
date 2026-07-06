# Element Literals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow `<tag>…</tag>` element syntax in Go expression position inside `.gsx` files, evaluating to a `gsx.Node` (a baked *Element*), so markup can be an ordinary Go value without a throwaway `component` declaration.

**Architecture:** gsx's parser captures top-level Go regions as opaque `ast.GoChunk{Src string}` and every `{ }` expression as a raw string — there is no Go-expression tree and no notion of "operand-start position." We add a scanner (`parser/goexpr.go`) that walks a Go source span with `go/scanner`, tracks prefix/infix (operand vs operator) position, and at operand positions where a `<` begins a tag (letter/`>`/`/`, but not `<-`) hands off to the existing re-entrant `parseElement`. The result is a new AST node holding an interleaved sequence of Go text and `ast.Element`s. Codegen lowers each embedded element to a `gsx.Func(...)` node value spliced into the surrounding Go; the `analyze.go` skeleton lowers it to a typed `gsx.Node` expression so `go/types` still validates the whole region.

**Tech Stack:** Go, `go/scanner` + `go/token` (stdlib, already used in `parser/boundary.go`/`parser/pipe.go`), gsx's hand-written recursive-descent parser, the txtar corpus harness.

## Global Constraints

- Runtime (root `gsx` package) stays **standard-library only**; parser/codegen may use `golang.org/x/tools`. (CLAUDE.md)
- **No "simple heuristics" in core logic** — the operand-position detector must be a real prefix/infix tracker over Go tokens, not a byte-pattern guess. (CLAUDE.md)
- Every syntax/codegen change **ships txtar corpus cases** pinning `input.gsx` + `generated.x.go.golden` + `render.golden`, **one case per context** the syntax is valid in. Regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`. (CLAUDE.md)
- Generated `.x.go` and golden files are **never hand-edited** — change source and regenerate. (CLAUDE.md)
- Before merging: `make ci` (uncached, mirrors GitHub CI). Inner loop: `make check`. Pin Go to `GO_VERSION` (currently 1.26.1). (CLAUDE.md)
- Syntax changes also update siblings: `../tree-sitter-gsx`, `../vscode-gsx`, `../gsxhq.github.io` (CodeMirror + VitePress). (CLAUDE.md) — folded into the final task.
- gsx invariant: **generated code always compiles for valid input**; the `analyze.go` skeleton pre-checks types. Element-in-Go-expression must preserve both.

**Element vs Component (design invariant):** a `<tag>` expression is an *Element* — a baked `gsx.Node`. Render-site attribute injection does **not** apply (that's the deferred `gsx.Component` feature). See `docs/superpowers/specs/2026-07-06-element-literals-design.md`.

---

## Task 1: Operand-position element detector (the spike)

Retires the load-bearing risk: can we reliably tell `<tag>` from `a < b` / `<-ch` / `a << b` inside opaque Go text? Pure function + exhaustive table test. **If a disambiguation case here proves intractable, stop and revisit the approach before continuing.**

**Files:**
- Create: `parser/goexpr.go`
- Test: `parser/goexpr_test.go`

**Interfaces:**
- Produces:
  - `type goElemMark struct { Off int }` — byte offset (relative to the scanned span) of a `<` that begins an element at an operand position.
  - `func scanGoElementMarks(src string) []goElemMark` — tokenizes `src` with `go/scanner`, returns every operand-position element start, in order. Does **not** parse the element (Task 3 does the handoff); this isolates the risky disambiguation for testing.

- [ ] **Step 1: Write the failing test**

Table-driven; each case is Go source and the offsets where an element legitimately starts (empty = none).

```go
package parser

import "testing"

func TestScanGoElementMarks(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []int // byte offsets of element-starting '<'
	}{
		// --- elements at operand positions ---
		{"assign", `x = <div/>`, []int{4}},
		{"define", `x := <Foo/>`, []int{5}},
		{"return", `return <div/>`, []int{7}},
		{"call arg", `f(<Foo/>)`, []int{2}},
		{"second call arg", `f(a, <Foo/>)`, []int{5}},
		{"slice elem", `[]T{<a/>, <b/>}`, []int{4, 9}},
		{"composite value", `M{K: <Foo/>}`, []int{5}},
		{"paren", `(<div/>)`, []int{1}},
		{"unary not", `!<Foo/>`, []int{1}}, // nonsensical but position-correct
		{"binary rhs", `x && <Foo/>`, []int{5}},

		// --- NOT elements: '<' in operator position is less-than ---
		{"less than", `a < b`, nil},
		{"less no space", `a<b`, nil},
		{"lte", `a <= b`, nil},
		{"shift", `a << b`, nil},
		{"cmp chain", `a < b && c > d`, nil},
		{"index cmp", `arr[i] < n`, nil},
		{"call result cmp", `f(x) < g(y)`, nil},

		// --- NOT elements: channel ops ---
		{"chan recv", `x := <-ch`, nil},
		{"chan send", `ch <- x`, nil},
		{"recv in call", `f(<-ch)`, nil},

		// --- Go generics use [] not <> : no ambiguity ---
		{"generic call", `Map[int, string](m)`, nil},
		{"generic decl frag", `[]Pair[K, V]{}`, nil},

		// --- element with nested Go / attrs / children ---
		{"attrs+interp", `x = <a href={u} class="c">{ label }</a>`, []int{4}},
		{"nested tag not counted twice", `x = <div><span/></div>`, []int{4}}, // outer only; inner is inside the element span
		{"lt after element", `<Foo/> < 3`, []int{0}}, // element first, then a real '<'
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			marks := scanGoElementMarks(c.src)
			got := make([]int, len(marks))
			for i, m := range marks {
				got[i] = m.Off
			}
			if !equalInts(got, c.want) {
				t.Fatalf("scanGoElementMarks(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./parser -run TestScanGoElementMarks`
Expected: FAIL — `scanGoElementMarks` undefined.

- [ ] **Step 3: Implement the detector**

Prefix/infix tracker over `go/scanner`. `expectOperand` starts true and is set by each token's class. At an operand position, a `token.LSS` whose following raw byte begins a tag (`startsTag`, and not `-`) is an element start; skip past the element's textual span by counting to its matching close so we don't re-scan its internals. For the span-skip, reuse the existing element boundary logic minimally (a light `<...>` depth walk is enough for the mark scan; Task 3 does the real parse).

```go
package parser

import (
	"go/scanner"
	"go/token"
)

type goElemMark struct{ Off int }

// scanGoElementMarks returns the byte offsets of every '<' that begins a gsx
// element at a Go operand-start position within src.
func scanGoElementMarks(src string) []goElemMark {
	var marks []goElemMark
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	expectOperand := true
	skipUntil := -1 // byte offset; tokens before this are inside an element span
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		off := fset.Position(pos).Offset
		if off < skipUntil {
			continue
		}
		if expectOperand && tok == token.LSS && byteBeginsTag(src, off+1) {
			marks = append(marks, goElemMark{Off: off})
			skipUntil = elementSpanEnd(src, off) // byte just past </tag> or />
			// after an element we are in operator position
			expectOperand = false
			continue
		}
		expectOperand = tokenExpectsOperandAfter(tok, lit)
	}
	return marks
}

// byteBeginsTag reports whether the byte at i can start a tag name / fragment /
// close — i.e. a letter, '>' or '/', but NOT '-' (which would make '<-' a
// channel receive) and NOT '=' / '<' (LEQ / SHL, handled as distinct tokens).
func byteBeginsTag(src string, i int) bool {
	if i >= len(src) {
		return false
	}
	c := src[i]
	if c == '-' {
		return false
	}
	return startsTag(c) // existing classifier in parser/markup.go: letter, '>', '/'
}

// tokenExpectsOperandAfter reports whether, after consuming tok, the parser is
// at an operand-start position (prefix) rather than expecting an operator (infix).
func tokenExpectsOperandAfter(tok token.Token, lit string) bool {
	switch tok {
	// operands and closers -> now expect an operator
	case token.IDENT, token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING,
		token.RPAREN, token.RBRACK, token.RBRACE:
		return false
	// keywords that are values/close-ish
	case token.RETURN, token.GO, token.DEFER, token.RANGE, token.CASE:
		return true
	}
	// keyword idents that are operands: true/false/nil/iota arrive as IDENT (handled above).
	// break/continue/fallthrough have no operand; treat as operand-expecting is harmless
	// (no '<' follows them meaningfully). Everything else (operators, '(', '[', '{',
	// ',', ';', ':', '=', ':=', binary/unary ops, keywords like if/for/switch/return)
	// -> expect an operand.
	return true
}
```

`elementSpanEnd(src, off)` — a minimal `<...>` walker that returns the byte just past the element (matching `</tag>` or `/>`), skipping quoted attr values and nested `{ }`. Provide it here or reuse Task 3's parse. For the mark scan a shallow tag-depth counter suffices:

```go
// elementSpanEnd returns the offset just past the element beginning at '<' (off).
// It tracks tag depth (<tag ...> / </tag> / <tag.../>), skipping attr strings and
// { } interpolations so their bytes don't affect the count.
func elementSpanEnd(src string, off int) int {
	i := off
	depth := 0
	for i < len(src) {
		switch src[i] {
		case '"', '\'', '`':
			i = skipGoStringLike(src, i) // reuse parser/boundary.go helper
			continue
		case '{':
			i = matchBrace(src, i) // reuse goExprEnd-style balance
			continue
		case '<':
			if i+1 < len(src) && src[i+1] == '/' {
				depth--
				i = indexByteFrom(src, '>', i) + 1
				if depth <= 0 {
					return i
				}
				continue
			}
			// opening or self-closing
			end := indexByteFrom(src, '>', i)
			if end > 0 && src[end-1] == '/' {
				// self-closing; depth unchanged
				if depth == 0 {
					return end + 1
				}
			} else {
				depth++
			}
			i = end + 1
			continue
		default:
			i++
		}
	}
	return len(src)
}
```

(Helpers `skipGoStringLike`, `matchBrace`, `indexByteFrom` are thin wrappers over existing `parser/boundary.go` scanners — implement as needed; keep them here.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./parser -run TestScanGoElementMarks -v`
Expected: PASS, all cases. If `cmp chain` / `chan recv` / `generic call` fail, fix `tokenExpectsOperandAfter` classification — those are the disambiguation core.

- [ ] **Step 5: Commit**

```bash
git add parser/goexpr.go parser/goexpr_test.go
git commit -m "feat(parser): operand-position element detector for tags in Go expressions"
```

---

## Task 2: AST node for Go-with-embedded-elements

**Files:**
- Modify: `ast/ast.go` (add node near `GoChunk` ~`:135` and `Interp` ~`:236`)
- Test: `ast/ast_test.go` (or `parser/goexpr_test.go` if ast has no test file — check first)

**Interfaces:**
- Produces:
  - `type GoText struct { Src string }` — a raw Go text run (may be empty).
  - `type GoWithElements struct { Parts []GoPart }` where `type GoPart interface{}` is satisfied by `GoText` and `*Element`. Used both as a top-level file item (replacing a `GoChunk` that contains elements) and as an interpolation expression body.
  - Add a `Pos` field mirroring neighboring nodes (`token.Pos`) so `//line` directives and diagnostics stay accurate.

- [ ] **Step 1: Write the failing test** — construct a `GoWithElements` and assert its parts round-trip.

```go
func TestGoWithElementsShape(t *testing.T) {
	n := ast.GoWithElements{Parts: []ast.GoPart{
		ast.GoText{Src: "x = "},
		&ast.Element{Tag: "div", Void: true},
	}}
	if len(n.Parts) != 2 {
		t.Fatalf("want 2 parts, got %d", len(n.Parts))
	}
	if _, ok := n.Parts[0].(ast.GoText); !ok {
		t.Fatalf("part 0 not GoText")
	}
	if _, ok := n.Parts[1].(*ast.Element); !ok {
		t.Fatalf("part 1 not *Element")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./ast` — FAIL, undefined types.
- [ ] **Step 3: Implement** — add the types to `ast/ast.go`; keep `GoChunk` unchanged for the (common) no-element case.
- [ ] **Step 4: Run to verify it passes** — `go test ./ast` — PASS.
- [ ] **Step 5: Commit**

```bash
git add ast/ast.go ast/ast_test.go
git commit -m "feat(ast): GoWithElements node holding Go text interleaved with elements"
```

---

## Task 3: Parse top-level GoChunks into elements

Wire the detector + `parseElement` handoff so a top-level `var`/`func`/`return`/call-arg region that contains element(s) becomes a `GoWithElements`.

**Files:**
- Modify: `parser/file.go` (`GoChunk` capture at `:98-102` and `:116-122`)
- Modify: `parser/goexpr.go` (add `splitGoElements`)
- Test: `parser/goexpr_test.go`

**Interfaces:**
- Consumes: `scanGoElementMarks` (Task 1), `parseElement` (`parser/markup.go:688`), `GoWithElements`/`GoText` (Task 2).
- Produces: `func (p *parser) splitGoElements(src string, base token.Pos) ast.GoPart` — returns a `GoText{src}` when no marks, else builds a `GoWithElements` by slicing text runs around each mark and invoking `parseElement` (seated at the mark's byte offset on a sub-parser over `src`) for each element.

- [ ] **Step 1: Write the failing test**

```go
func TestSplitGoElements(t *testing.T) {
	src := `var help = <a href="/help">?</a>`
	p := newTestParser(src) // helper: seat a parser over src; see parser/*_test.go
	part := p.splitGoElements(src, token.Pos(1))
	we, ok := part.(ast.GoWithElements)
	if !ok {
		t.Fatalf("want GoWithElements, got %T", part)
	}
	// expect: ["var help = ", <a>, ""]
	if len(we.Parts) != 3 {
		t.Fatalf("parts=%d want 3", len(we.Parts))
	}
	if we.Parts[0].(ast.GoText).Src != "var help = " {
		t.Fatalf("lead text=%q", we.Parts[0].(ast.GoText).Src)
	}
	el := we.Parts[1].(*ast.Element)
	if el.Tag != "a" {
		t.Fatalf("tag=%q", el.Tag)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./parser -run TestSplitGoElements` — FAIL.
- [ ] **Step 3: Implement `splitGoElements`** and call it from `parser/file.go` where GoChunks are emitted: replace `ast.GoChunk{Src: s}` with `p.splitGoElements(s, pos)` (which returns a plain `GoText`/`GoChunk`-equivalent when there are no elements, preserving the fast path). Keep `GoChunk` for the no-element result to minimize downstream churn, OR always return `GoText` and update the file-item switch — pick the smaller diff after reading `parser/file.go`.
- [ ] **Step 4: Run to verify it passes** — `go test ./parser -run TestSplitGoElements -v` — PASS. Then `go test ./parser` — no regressions.
- [ ] **Step 5: Commit**

```bash
git add parser/goexpr.go parser/file.go parser/goexpr_test.go
git commit -m "feat(parser): extract elements from top-level Go regions into GoWithElements"
```

---

## Task 4: Codegen — lower embedded elements to node values

Emit a `GoWithElements` as its Go text with each `*Element` replaced by the element's lowered `gsx.Node` expression, so the whole region compiles.

**Files:**
- Modify: `internal/codegen/emit.go` (top-level item emission; reuse element lowering used by `genInterp`/child rendering ~`:1530`)
- Test: `internal/codegen/*_test.go` (unit) + corpus in Task 6

**Interfaces:**
- Consumes: `GoWithElements` (Task 2), existing element emission (the code that turns an `ast.Element` into a `gsx.Func(func(ctx,w)...)` value — locate the shared helper used for element children).
- Produces: emission such that `var help = <a …>?</a>` becomes `var help = gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { … })` (a `gsx.Node`), with `//line` directives preserved.

- [ ] **Step 1: Write the failing test** — a unit test that runs codegen over a minimal file with `var x = <div/>` and asserts the output contains `gsx.Func(` in the var initializer position and no literal `<div/>`.
- [ ] **Step 2: Run to verify it fails.**
- [ ] **Step 3: Implement** — in the top-level emitter, when an item is `GoWithElements`, write each `GoText.Src` verbatim and, for each `*Element`, call the existing element-to-`gsx.Func` lowering (the same path child elements use), splicing its expression inline. Ensure `io`/`context`/`gsx` imports are present (they already are for component files; confirm the import-injection covers element-only files).
- [ ] **Step 4: Run to verify it passes.**
- [ ] **Step 5: Commit**

```bash
git commit -am "feat(codegen): emit embedded elements as gsx.Node values in Go regions"
```

---

## Task 5: Type-check skeleton for embedded elements

Keep gsx's "generated code always compiles / errors reported pre-build" invariant: teach `analyze.go` to lower an embedded element to a typed `gsx.Node` expression in the skeleton so `go/types` validates the whole region (including interpolations inside the element).

**Files:**
- Modify: `internal/codegen/analyze.go` (`probeExpr` ~`:1618`, `emitProbes` ~`:919`)
- Test: `internal/codegen/analyze_test.go`

**Interfaces:**
- Consumes: `GoWithElements`; existing element-probe machinery (elements inside component bodies are already type-checked — reuse that lowering to a typed expression).
- Produces: skeleton text where each embedded `<tag>` is replaced by an expression of type `gsx.Node` (e.g. the component call `Foo(FooProps{…})` for a component tag, or a synthesized `_gsxrt.Node`-typed placeholder whose interpolations are still emitted as `_gsxuse(expr)` lines for checking).

- [ ] **Step 1: Write a failing test** — a source with a type error *inside* an embedded element interpolation (`var x = <a href={notAString}/>` where `notAString` is an `int`) must produce a diagnostic, not silently pass.
- [ ] **Step 2: Run to verify it fails** (no diagnostic yet).
- [ ] **Step 3: Implement** the skeleton lowering for `GoWithElements`.
- [ ] **Step 4: Run to verify it passes** (diagnostic present; valid input still clean).
- [ ] **Step 5: Commit**

```bash
git commit -am "feat(codegen): type-check embedded elements via the analyze skeleton"
```

---

## Task 6: Corpus cases per context + render verification

**Files:**
- Create: `internal/corpus/testdata/cases/element-literals/var.txtar`
- Create: `internal/corpus/testdata/cases/element-literals/return.txtar`
- Create: `internal/corpus/testdata/cases/element-literals/call-arg.txtar`
- Create: `internal/corpus/testdata/cases/element-literals/struct-field.txtar`
- Create: `internal/corpus/testdata/cases/element-literals/component-tag.txtar` (`<Foo x={e}/>` in expr position)
- Modify: `internal/corpus/testdata/coverage.golden` (regenerated)

- [ ] **Step 1: Write `input.gsx` for each case** — a small `.gsx` exercising the context, e.g. `var.txtar`:

```
-- input.gsx --
package demo

var help = <a href="/help" class="text-blue-600">?</a>

component Uses() {
	<div>{ help }</div>
}
```

- [ ] **Step 2: Generate goldens** — `go test ./internal/corpus -run TestCorpus -update`
- [ ] **Step 3: Verify without update** — `go test ./internal/corpus -run TestCorpus` — PASS.
- [ ] **Step 4: Eyeball each `generated.x.go.golden`** — confirm `gsx.Func(` in the value position and correct `render.golden` HTML.
- [ ] **Step 5: Commit**

```bash
git add internal/corpus/testdata/cases/element-literals internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): element literals in var/return/call-arg/struct-field/component-tag"
```

---

## Task 7: Docs, ROADMAP, and sibling syntax

**Files:**
- Modify: `docs/guide/syntax.md` (or the generated syntax docs area) — element-literals section (wrap literal `{{ }}` in `::: v-pre`)
- Modify: `docs/ROADMAP.md` — mark element literals shipped
- Modify: `../tree-sitter-gsx` grammar, `../vscode-gsx`, `../gsxhq.github.io` CodeMirror + VitePress — recognize `<tag>` in expression position

- [ ] **Step 1:** Write the guide section with a runnable example (nav icon, `RenderComponent(<Foo/>)`).
- [ ] **Step 2:** Update ROADMAP.
- [ ] **Step 3:** Update the three sibling repos' syntax highlighting; note tests/build for each.
- [ ] **Step 4:** Run `make lint`.
- [ ] **Step 5: Commit** (gsx repo) and commit siblings separately.

```bash
git add docs/ && git commit -m "docs: element literals guide + ROADMAP"
```

---

## Final verification

- [ ] `make ci` (uncached) passes.
- [ ] Dogfood: in one-learning, convert one nav-item icon to `<HomeIcon class="w-5 h-5"/>` and one `RenderComponent` call to inline `<Foo/>`; `gsx generate` + build clean.

## Self-review notes (for the planner, not a task)

- **Spec coverage:** semantics (Task 3/4), Element-not-Component baked behavior (design invariant, corpus in Task 6), no type-structure change (nothing touches `gsx.Component`/`FooProps`), parser-ambiguity crux (Task 1). Deferred component-values intentionally out of scope.
- **Risk gate:** Task 1 is a hard gate. If `tokenExpectsOperandAfter` can't cleanly separate the disambiguation table (esp. channel ops and comparison chains), pause and reconsider before Tasks 3–5.
- **Open detail to resolve during Task 4/5:** exact reuse point of the existing element→`gsx.Func` lowering — confirm the child-element emitter is callable outside a component body (imports, `ctx`/writer capture). If it assumes a component scope, extract a shared helper first (fold into Task 4).
