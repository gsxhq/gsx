# gsx `|>` Pipeline Parser (Part A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Parse the `|>` pipeline operator inside interpolation and attribute-expression values into the public AST, with full test coverage. No codegen (blocked on stages that don't exist yet).

**Architecture:** Extend the existing markup parser. `parseInterp` (the single choke point for `{ expr }` and `name={ expr }`) splits its inner Go string on top-level `|>` and records a seed expression plus an ordered list of filter stages on the existing `ast.Interp` / `ast.ExprAttr` nodes. Splitting reuses `go/scanner` (as `parser/boundary.go` already does) — `|>` is detected as an `OR` token immediately followed by a `GTR` token at bracket depth 0. No Go-expression parser is added; seed and stage arguments stay opaque strings.

**Tech Stack:** Go 1.26, `go/scanner`, `go/token`. Tests: standard `go test`, the `internal/corpus` txtar golden harness (`-update` regenerates goldens).

## Global Constraints

- Module `github.com/gsxhq/gsx`; Go `1.26.1`.
- Unexported by default; only `ast.PipeStage` and the new `Stages` fields are exported (they are part of the public AST API).
- **Backward compatibility is mandatory:** an interpolation with no `|>` must produce exactly today's `Interp`/`ExprAttr` (same `Expr`, same `Try`, `Stages == nil`). All existing tests must stay green.
- Semantics recorded by the parser (lowering is a later, blocked phase): `a |> R` ≡ `R(a)`; a stage is `name` (bare → `R(a)`) or `name(args)` (parameterized partial application → `R(args)(a)`); `HasArgs` distinguishes `f` from `f()`; per-stage and seed `?` is the try-marker.
- Error messages use the existing `"%d:%d: message"` line:col form, anchored at the interpolation's start position.

---

### Task 1: AST fields + printer for pipeline stages

**Files:**
- Modify: `ast/ast.go` (add `PipeStage`; add `Stages` to `Interp` ~line 150 and `ExprAttr` ~line 167)
- Modify: `ast/print.go` (`*Interp` case ~line 79; `*ExprAttr` case ~line 80)
- Test: `ast/print_test.go`

**Interfaces:**
- Produces:
  ```go
  // PipeStage is one `|> name` / `|> name(args)` filter in a pipeline.
  type PipeStage struct {
      Name    string // filter name, possibly dotted: "upper", "truncate", "strings.ToUpper"
      Args    string // raw arg source inside parens; "" if none or empty parens
      HasArgs bool   // true if `(...)` was present (distinguishes `f` from `f()`)
      Try     bool   // trailing `?` on this stage
  }
  // Interp.Stages / ExprAttr.Stages: nil for a plain expression; when non-empty,
  // Expr is the seed and Stages are applied left-to-right.
  ```

- [ ] **Step 1: Write the failing test** — append to `ast/print_test.go`:

```go
func TestFprintInterpWithPipeStages(t *testing.T) {
	n := &ast.Interp{
		Expr: "name",
		Stages: []ast.PipeStage{
			{Name: "upper"},
			{Name: "truncate", Args: "20", HasArgs: true},
		},
	}
	var b strings.Builder
	if err := ast.Fprint(&b, n); err != nil {
		t.Fatal(err)
	}
	want := `Interp expr="name" try=false
  PipeStage name=upper args="" hasArgs=false try=false
  PipeStage name=truncate args="20" hasArgs=true try=false
`
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
```
(Ensure `strings` is imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ast -run TestFprintInterpWithPipeStages`
Expected: FAIL — `unknown field 'Stages'` (compile error) or output mismatch.

- [ ] **Step 3: Add the AST types.** In `ast/ast.go`, add the `Stages []PipeStage` field to `Interp` and to `ExprAttr`, and add the `PipeStage` type just below `Interp`:

```go
// Interp is `{ expr }` (Try=false) or `{ expr? }` (Try=true). When Stages is
// non-empty, Expr is the pipeline seed and Stages are applied left-to-right.
type Interp struct {
	span
	Expr   string
	Try    bool
	Stages []PipeStage
}

func (*Interp) markupNode() {}

// PipeStage is one `|> name` / `|> name(args)` filter in a pipeline (a plain
// value, not a Node).
type PipeStage struct {
	Name    string
	Args    string
	HasArgs bool
	Try     bool
}
```
And in `ExprAttr`:
```go
type ExprAttr struct {
	span
	Name, Expr string
	Try        bool
	Stages     []PipeStage
}
```

- [ ] **Step 4: Render stages in the printer.** In `ast/print.go`, extend the `*Interp` and `*ExprAttr` cases to print stages after the node line (mirroring the `*ClassAttr`/`ClassPart` convention):

```go
case *Interp:
	if _, err := fmt.Fprintf(w, "%sInterp expr=%q try=%v\n", indent, n.Expr, n.Try); err != nil {
		return err
	}
	if err := fprintStages(w, indent, n.Stages); err != nil {
		return err
	}
```
and for `*ExprAttr`:
```go
case *ExprAttr:
	if _, err := fmt.Fprintf(w, "%sExprAttr name=%s expr=%q try=%v\n", indent, n.Name, n.Expr, n.Try); err != nil {
		return err
	}
	if err := fprintStages(w, indent, n.Stages); err != nil {
		return err
	}
```
Add the helper near the bottom of `print.go` (before the final `}` of the file):
```go
func fprintStages(w io.Writer, indent string, stages []PipeStage) error {
	for _, st := range stages {
		if _, err := fmt.Fprintf(w, "%s  PipeStage name=%s args=%q hasArgs=%v try=%v\n",
			indent, st.Name, st.Args, st.HasArgs, st.Try); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ast`
Expected: PASS (all ast tests).

- [ ] **Step 6: Commit**

```bash
git add ast/ast.go ast/print.go ast/print_test.go
git commit -m "feat(ast): pipeline stages on Interp/ExprAttr + printer"
```

---

### Task 2: `splitPipe` — depth-0 `|>` splitter

**Files:**
- Create: `parser/pipe.go`
- Test: `parser/pipe_test.go`

**Interfaces:**
- Consumes: `go/scanner`, `go/token`.
- Produces: `func splitPipe(src string) []string` — segments split on top-level `|>`; returns `[]string{src}` when there is no top-level `|>`.

- [ ] **Step 1: Write the failing test** — create `parser/pipe_test.go`:

```go
package parser

import (
	"reflect"
	"testing"
)

func TestSplitPipe(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"name", []string{"name"}},
		{"name |> upper", []string{"name ", " upper"}},
		{"a |> b |> c", []string{"a ", " b ", " c"}},
		{"x |> truncate(20)", []string{"x ", " truncate(20)"}},
		{`join(a |> b)`, []string{`join(a |> b)`}},   // |> inside parens: depth 1, not split
		{`"a |> b"`, []string{`"a |> b"`}},            // |> inside string literal
		{"flagsA | flagsB", []string{"flagsA | flagsB"}}, // bitwise OR (no `>`): not a pipe
		{"a || b", []string{"a || b"}},                // logical OR: not a pipe
		{"a | > b", []string{"a | > b"}},              // `| >` with gap: not a pipe
	}
	for _, c := range cases {
		got := splitPipe(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPipe(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser -run TestSplitPipe`
Expected: FAIL — `undefined: splitPipe`.

- [ ] **Step 3: Implement `splitPipe`** — create `parser/pipe.go`:

```go
// parser/pipe.go
package parser

import (
	"go/scanner"
	"go/token"
)

// splitPipe splits src on top-level `|>` pipeline operators — those at bracket
// depth 0, outside strings, runes, and comments. Segments are returned in order
// with surrounding whitespace preserved (the caller trims). With no top-level
// `|>`, it returns a single segment equal to src. `|>` is an `OR` token (`|`)
// immediately followed by a `GTR` token (`>`) with no gap; `||`, `|=`, and `| >`
// never match.
func splitPipe(src string) []string {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	var splits []int // byte offset of each `|` that begins a top-level `|>`
	depth := 0
	prevTok := token.ILLEGAL
	prevOff := -1
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.GTR:
			if depth == 0 && prevTok == token.OR && off == prevOff+1 {
				splits = append(splits, prevOff)
			}
		}
		prevTok = tok
		prevOff = off
	}
	if len(splits) == 0 {
		return []string{src}
	}
	segs := make([]string, 0, len(splits)+1)
	start := 0
	for _, sp := range splits {
		segs = append(segs, src[start:sp])
		start = sp + 2 // skip "|>"
	}
	return append(segs, src[start:])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./parser -run TestSplitPipe`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add parser/pipe.go parser/pipe_test.go
git commit -m "feat(parser): splitPipe — depth-0 |> splitter"
```

---

### Task 3: stage & pipeline parsing (`parsePipeStage`, `parsePipe`)

**Files:**
- Modify: `parser/pipe.go`
- Test: `parser/pipe_test.go`

**Interfaces:**
- Consumes: `splitPipe` (Task 2); `parenEnd` (`parser/boundary.go`); `isIdentByte` (`parser/markup.go`); `ast.PipeStage` (Task 1).
- Produces:
  - `func parsePipeStage(seg string) (ast.PipeStage, error)`
  - `func parsePipe(inner string) (seed string, seedTry bool, stages []ast.PipeStage, err error)`

- [ ] **Step 1: Write the failing tests** — append to `parser/pipe_test.go`:

```go
func TestParsePipeStage(t *testing.T) {
	ok := []struct {
		in   string
		want ast.PipeStage
	}{
		{"upper", ast.PipeStage{Name: "upper"}},
		{" trim ", ast.PipeStage{Name: "trim"}},
		{"truncate(20)", ast.PipeStage{Name: "truncate", Args: "20", HasArgs: true}},
		{"f()", ast.PipeStage{Name: "f", Args: "", HasArgs: true}},
		{"strings.ToUpper", ast.PipeStage{Name: "strings.ToUpper"}},
		{"validate()?", ast.PipeStage{Name: "validate", HasArgs: true, Try: true}},
		{"join(\", \")", ast.PipeStage{Name: "join", Args: "\", \"", HasArgs: true}},
	}
	for _, c := range ok {
		got, err := parsePipeStage(c.in)
		if err != nil {
			t.Errorf("parsePipeStage(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePipeStage(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
	bad := []string{"", "  ", "?", "123", "a b", "f(", ".x", "f(x)y"}
	for _, in := range bad {
		if _, err := parsePipeStage(in); err == nil {
			t.Errorf("parsePipeStage(%q): expected error, got nil", in)
		}
	}
}

func TestParsePipe(t *testing.T) {
	seed, try, stages, err := parsePipe("name? |> upper |> truncate(20)")
	if err != nil {
		t.Fatal(err)
	}
	if seed != "name" || !try {
		t.Fatalf("seed=%q try=%v, want \"name\" true", seed, try)
	}
	want := []ast.PipeStage{{Name: "upper"}, {Name: "truncate", Args: "20", HasArgs: true}}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages=%#v, want %#v", stages, want)
	}

	// No pipe → seed only, nil stages (backward-compat shape).
	seed, try, stages, err = parsePipe("greeting(name)?")
	if err != nil || seed != "greeting(name)" || !try || stages != nil {
		t.Fatalf("plain: seed=%q try=%v stages=%#v err=%v", seed, try, stages, err)
	}
}
```
Add `"github.com/gsxhq/gsx/ast"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser -run 'TestParsePipe'`
Expected: FAIL — `undefined: parsePipeStage` / `parsePipe`.

- [ ] **Step 3: Implement parsing** — append to `parser/pipe.go` (add `fmt`, `strings`, and `github.com/gsxhq/gsx/ast` to its imports):

```go
// isStageName reports whether s is a (optionally dotted) Go identifier, e.g.
// "upper" or "strings.ToUpper".
func isStageName(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			b := part[i]
			if i == 0 && b >= '0' && b <= '9' {
				return false
			}
			if !isIdentByte(b) {
				return false
			}
		}
	}
	return true
}

// parsePipeStage parses one filter segment: `name`, `name(args)`, or either with
// a trailing `?`.
func parsePipeStage(seg string) (ast.PipeStage, error) {
	s := strings.TrimSpace(seg)
	try := false
	if strings.HasSuffix(s, "?") {
		try = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "?"))
	}
	if s == "" {
		return ast.PipeStage{}, fmt.Errorf("empty pipeline stage")
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		name := strings.TrimSpace(s[:i])
		end, ok := parenEnd(s, i)
		if !ok {
			return ast.PipeStage{}, fmt.Errorf("unterminated `(` in pipeline stage %q", seg)
		}
		if strings.TrimSpace(s[end+1:]) != "" {
			return ast.PipeStage{}, fmt.Errorf("trailing text after `)` in pipeline stage %q", seg)
		}
		if !isStageName(name) {
			return ast.PipeStage{}, fmt.Errorf("invalid pipeline filter name %q", name)
		}
		return ast.PipeStage{Name: name, Args: strings.TrimSpace(s[i+1 : end]), HasArgs: true, Try: try}, nil
	}
	if !isStageName(s) {
		return ast.PipeStage{}, fmt.Errorf("invalid pipeline filter name %q", s)
	}
	return ast.PipeStage{Name: s, Try: try}, nil
}

// parsePipe splits inner into a seed expression and its filter stages. With no
// top-level `|>`, stages is nil and the result matches the pre-pipeline shape
// (seed = the expression, seedTry = its trailing `?`).
func parsePipe(inner string) (seed string, seedTry bool, stages []ast.PipeStage, err error) {
	segs := splitPipe(inner)
	seed = strings.TrimSpace(segs[0])
	if strings.HasSuffix(seed, "?") {
		seedTry = true
		seed = strings.TrimSpace(strings.TrimSuffix(seed, "?"))
	}
	for _, seg := range segs[1:] {
		st, e := parsePipeStage(seg)
		if e != nil {
			return "", false, nil, e
		}
		stages = append(stages, st)
	}
	return seed, seedTry, stages, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./parser -run 'TestParsePipe'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add parser/pipe.go parser/pipe_test.go
git commit -m "feat(parser): parse pipeline stages (name, args, try)"
```

---

### Task 4: wire pipelines into `parseInterp` and `ExprAttr`

**Files:**
- Modify: `parser/markup.go` (`parseInterp` ~lines 13-30; `ExprAttr` construction ~line 528)
- Test: `parser/markup_test.go`

**Interfaces:**
- Consumes: `parsePipe` (Task 3); `ast.Interp.Stages`, `ast.ExprAttr.Stages` (Task 1).
- Produces: `*ast.Interp` / `*ast.ExprAttr` carrying `Stages` when the value contains `|>`.

- [ ] **Step 1: Write the failing tests** — append to `parser/markup_test.go` (match the file's existing test style; use `parser.ParseFile`):

```go
func TestParseInterpPipeline(t *testing.T) {
	src := "package p\ncomponent C() { <p>{ name |> upper |> truncate(20) }</p> }\n"
	f, err := parser.ParseFile(token.NewFileSet(), "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var interp *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if i, ok := n.(*ast.Interp); ok {
			interp = i
		}
		return true
	})
	if interp == nil {
		t.Fatal("no Interp found")
	}
	if interp.Expr != "name" {
		t.Errorf("seed = %q, want \"name\"", interp.Expr)
	}
	want := []ast.PipeStage{{Name: "upper"}, {Name: "truncate", Args: "20", HasArgs: true}}
	if !reflect.DeepEqual(interp.Stages, want) {
		t.Errorf("stages = %#v, want %#v", interp.Stages, want)
	}
}

func TestParseAttrPipeline(t *testing.T) {
	src := "package p\ncomponent C() { <a href={ u |> absolute }>x</a> }\n"
	f, err := parser.ParseFile(token.NewFileSet(), "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ea *ast.ExprAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if a, ok := n.(*ast.ExprAttr); ok {
			ea = a
		}
		return true
	})
	if ea == nil || ea.Expr != "u" || len(ea.Stages) != 1 || ea.Stages[0].Name != "absolute" {
		t.Fatalf("ExprAttr pipeline not parsed: %#v", ea)
	}
}
```
Ensure the test file imports `reflect`, `go/token`, `github.com/gsxhq/gsx/ast`, `github.com/gsxhq/gsx/parser` (check existing imports first; add only what's missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser -run 'Pipeline'`
Expected: FAIL — `interp.Stages` is nil (seed currently parsed as the whole expr `name |> upper |> truncate(20)`).

- [ ] **Step 3: Rewrite `parseInterp` to split the pipeline.** Replace the body of `parseInterp` in `parser/markup.go` so it calls `parsePipe` instead of stripping only a trailing `?`:

```go
func (p *parser) parseInterp() (*ast.Interp, error) {
	start := p.i
	startPos := p.posAt(start)
	resolvedPos := p.file.Position(startPos)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, fmt.Errorf("%d:%d: unterminated `{`", resolvedPos.Line, resolvedPos.Column)
	}
	inner := strings.TrimSpace(p.src[p.i+1 : end])
	seed, seedTry, stages, perr := parsePipe(inner)
	if perr != nil {
		return nil, fmt.Errorf("%d:%d: %v", resolvedPos.Line, resolvedPos.Column, perr)
	}
	p.i = end + 1
	n := &ast.Interp{Expr: seed, Try: seedTry, Stages: stages}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}
```

- [ ] **Step 4: Propagate stages to `ExprAttr`.** At the `ExprAttr` construction (~line 528), add the `Stages` field:

```go
ea := &ast.ExprAttr{Name: name, Expr: in.Expr, Try: in.Try, Stages: in.Stages}
```

- [ ] **Step 5: Run the new tests and the full parser/ast suite**

Run: `go test ./parser ./ast ./internal/...`
Expected: PASS — new pipeline tests pass AND all pre-existing tests stay green (backward compatibility: no-`|>` inputs are unchanged).

- [ ] **Step 6: Commit**

```bash
git add parser/markup.go parser/markup_test.go
git commit -m "feat(parser): parse |> pipelines in interpolation and attributes"
```

---

### Task 5: golden integration cases (txtar)

**Files:**
- Create: `internal/corpus/testdata/pipeline/15_pipeline.txtar`
- Create: `internal/corpus/testdata/pipeline/16_pipeline_attr_try.txtar`
- Create: `internal/corpus/testdata/pipeline/e05_empty_pipe_stage.txtar`

**Interfaces:**
- Consumes: the corpus harness (`internal/corpus/corpus_test.go`), which globs `testdata/pipeline/*.txtar`, parses `input.gsx`, and compares `ast.golden` / `diagnostics.golden`. `-update` regenerates goldens.

- [ ] **Step 1: Create the success cases with hand-written goldens.**

`internal/corpus/testdata/pipeline/15_pipeline.txtar`:
```
gsx pipeline: interpolation with bare + parameterized filters.
-- input.gsx --
package examples

component C(name string) { <p>{ name |> upper |> truncate(20) }</p> }
-- diagnostics.golden --
-- ast.golden --
File package=examples
  Component name=C recv="" params="name string"
    Element tag=p void=false
      Interp expr="name" try=false
        PipeStage name=upper args="" hasArgs=false try=false
        PipeStage name=truncate args="20" hasArgs=true try=false
```

`internal/corpus/testdata/pipeline/16_pipeline_attr_try.txtar`:
```
gsx pipeline: attribute value with a failable filter stage.
-- input.gsx --
package examples

component C(u string) { <a href={ u |> absolute()? }>x</a> }
-- diagnostics.golden --
-- ast.golden --
File package=examples
  Component name=C recv="" params="u string"
    Element tag=a void=false
      ExprAttr name=href expr="u" try=false
        PipeStage name=absolute args="" hasArgs=true try=true
      Text value="x"
```

- [ ] **Step 2: Create the error case.**

`internal/corpus/testdata/pipeline/e05_empty_pipe_stage.txtar`:
```
gsx pipeline: an empty stage (trailing `|>` with no filter) is an error.
-- input.gsx --
package examples

component C(name string) { <p>{ name |> }</p> }
-- diagnostics.golden --
3:30: empty pipeline stage
-- ast.golden --
```
(If the harness records parse errors differently from the `diagnostics.golden` section — confirm by reading `corpus_test.go` — adjust this case to match how `e01`–`e04` express their expected error. The exact column may differ; see Step 3.)

- [ ] **Step 3: Run the corpus tests; reconcile goldens.**

Run: `go test ./internal/corpus`
Expected: PASS. If the success goldens mismatch only in whitespace/format, regenerate them and diff to confirm the structure is what you intended:

```bash
go test ./internal/corpus -update
git diff internal/corpus/testdata/pipeline/15_pipeline.txtar
```
For the error case, if the harness's error text/column differs from the placeholder above, copy the actual `3:NN: empty pipeline stage` line the parser produces into `diagnostics.golden` (the message text is fixed by Task 3; only the column may shift).

- [ ] **Step 4: Run the whole suite**

Run: `go test ./...`
Expected: PASS (all packages).

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/testdata/pipeline/15_pipeline.txtar \
        internal/corpus/testdata/pipeline/16_pipeline_attr_try.txtar \
        internal/corpus/testdata/pipeline/e05_empty_pipe_stage.txtar
git commit -m "test(corpus): golden cases for |> pipelines"
```

---

## Self-Review

**Spec coverage (Part A only — B/C/D blocked):**
- `|>` operator parsing, `a |> R` recorded as seed + stages → Tasks 2-4. ✓
- Bare vs parameterized (`HasArgs`) → Task 3. ✓
- `|>` not `|` (depth-0 `OR`+`GTR` adjacency; `|`/`||`/`| >` excluded) → Task 2 tests. ✓
- Per-stage and seed `?` → Tasks 3-4. ✓
- Works in interpolation AND attribute values → Task 4 (both choke points). ✓
- Top-level only / parens are opaque Go → `splitPipe` depth tracking (Task 2 `join(a |> b)` case). ✓
- Public AST carries it → Task 1 (`PipeStage`, `Stages` exported). ✓
- Backward compatibility → Task 4 Step 5 runs the full pre-existing suite. ✓
- NOT in scope (blocked): codegen lowering, resolver, `std`, gen lib — correctly absent.

**Placeholder scan:** The only soft spot is the error case's exact column/format (Task 5 Steps 2-3), which depends on the harness's error convention; the steps tell the implementer to read `corpus_test.go` and the existing `e0x` cases and match them. Message text is fixed in code (Task 3).

**Type consistency:** `PipeStage{Name, Args, HasArgs, Try}` is identical across Tasks 1, 3, 4, 5. `parsePipe` signature `(seed string, seedTry bool, stages []ast.PipeStage, err error)` is consumed exactly in Task 4 Step 3. `splitPipe(string) []string` consumed in Task 3. Printer field order matches the golden lines in Tasks 1 and 5.
