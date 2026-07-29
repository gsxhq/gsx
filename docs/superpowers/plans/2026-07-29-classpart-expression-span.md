# ClassPart Expression-Only Source Span Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose an exact expression-only endpoint on `ast.ClassPart` so source rewriters can replace the pipeline seed without deleting stages or a `: cond` guard.

**Architecture:** Preserve `ClassPart.Pos()`/`End()` as the complete contribution span. Add `(*ClassPart).ExprEnd() token.Pos`, derived from the parser's existing byte-faithful `ExprPos` plus `Expr` invariant, and pin both the standalone API contract and parsed-source behavior.

**Tech Stack:** Go 1.26.1, `go/token`, the existing `ast` and `parser` test packages, `gopls`, and repository Make targets.

## Global Constraints

- Keep the root runtime dependency-free; this AST-only change adds no dependency.
- `Pos()` and `End()` continue to bound the complete class/style contribution.
- `ExprEnd()` returns `token.NoPos` for a nil receiver or invalid `ExprPos`.
- For a valid expression span, `ExprEnd()` is exactly `ExprPos + token.Pos(len(Expr))`; positions and string lengths are byte-based.
- CSS-literal and value-form parts continue to have no expression span.
- Do not add `IsPlain`, parser state, stored end-position fields, compatibility shims, or sibling syntax/editor changes.
- Do not modify generated `.x.go` files or corpus goldens; syntax, formatting, code generation, and rendering are unchanged.
- Follow test-driven development: tests must fail because `ExprEnd` is absent before implementation.
- After implementation review, an independent adversarial reviewer must run a throwaway parser/rewrite probe that replaces only a guarded and pipelined seed and confirms the suffix remains byte-for-byte intact.

---

## File Structure

- `ast/ast.go` — owns the public `ClassPart` contract and the new `ExprEnd` method.
- `ast/ast_test.go` — pins nil, invalid-position, ASCII, and UTF-8 endpoint behavior independently of parsing.
- `parser/navpos_test.go` — pins the parser's byte-faithful expression span across every relevant `ClassPart` form and distinguishes it from the whole-node span.

No new production or test file is needed; these responsibilities already belong to the three files above.

### Task 1: Add and verify the expression-only span API

**Files:**
- Modify: `ast/ast.go:615-642`
- Modify: `ast/ast_test.go`
- Modify: `parser/navpos_test.go`

**Interfaces:**
- Consumes: `ClassPart.Expr string`, `ClassPart.ExprPos token.Pos`, `ClassPart.Pos() token.Pos`, `ClassPart.End() token.Pos`, `token.Pos.IsValid() bool`.
- Produces: `func (p *ClassPart) ExprEnd() token.Pos`.

- [ ] **Step 1: Add the failing standalone AST contract test**

Append this test to `ast/ast_test.go`:

```go
func TestClassPartExprEnd(t *testing.T) {
	var nilPart *ClassPart
	if got := nilPart.ExprEnd(); got != token.NoPos {
		t.Fatalf("nil ExprEnd() = %d, want NoPos", got)
	}

	tests := []struct {
		name string
		part *ClassPart
		want token.Pos
	}{
		{
			name: "invalid position",
			part: &ClassPart{Expr: "button.Role()"},
			want: token.NoPos,
		},
		{
			name: "ASCII",
			part: &ClassPart{Expr: "button.Role()", ExprPos: token.Pos(11)},
			want: token.Pos(11 + len("button.Role()")),
		},
		{
			name: "UTF-8 uses byte length",
			part: &ClassPart{Expr: `"é"`, ExprPos: token.Pos(23)},
			want: token.Pos(23 + len(`"é"`)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.part.ExprEnd(); got != tt.want {
				t.Fatalf("ExprEnd() = %d, want %d", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Add the failing parsed-source span matrix**

Append this test to `parser/navpos_test.go`:

```go
func TestClassPartExpressionSpan(t *testing.T) {
	src := []byte(`package p

component C(cond bool) {
	<div
		class={
			"plain",
			button.Role() : cond,
			makeClass(
				"é",
			) |> upper,
			other() |> upper : cond,
		}
		style={
			css` + "`" + `color: red` + "`" + `,
			if cond { "display:block" } else { "display:none" },
		}
	></div>
}
`)
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "span.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		part, ok := n.(*ast.ClassPart)
		if !ok {
			return true
		}
		if part.CSSSegments != nil {
			seen["css"] = true
			if got := part.ExprEnd(); got != token.NoPos {
				t.Errorf("CSS ExprEnd() = %d, want NoPos", got)
			}
			return true
		}
		if part.CF != nil {
			seen["value-form"] = true
			if got := part.ExprEnd(); got != token.NoPos {
				t.Errorf("value-form ExprEnd() = %d, want NoPos", got)
			}
			return true
		}

		start := fset.Position(part.ExprPos).Offset
		end := fset.Position(part.ExprEnd()).Offset
		if got := string(src[start:end]); got != part.Expr {
			t.Errorf("expression span = %q, want %q", got, part.Expr)
		}

		switch {
		case part.Cond != "" && len(part.Stages) > 0:
			seen["guarded-pipeline"] = true
		case part.Cond != "":
			seen["guarded"] = true
		case len(part.Stages) > 0:
			seen["pipeline"] = true
		default:
			seen["plain"] = true
		}
		if (part.Cond != "" || len(part.Stages) > 0) && part.End() <= part.ExprEnd() {
			t.Errorf("whole-node End() = %d, want beyond ExprEnd() = %d for Expr %q", part.End(), part.ExprEnd(), part.Expr)
		}
		if strings.Contains(part.Expr, "\n") {
			seen["multiline"] = true
		}
		if strings.Contains(part.Expr, "é") {
			seen["utf8"] = true
		}
		return true
	})

	for _, kind := range []string{
		"plain",
		"guarded",
		"pipeline",
		"guarded-pipeline",
		"multiline",
		"utf8",
		"css",
		"value-form",
	} {
		if !seen[kind] {
			t.Errorf("did not inspect a %s ClassPart", kind)
		}
	}
}
```

Add `"strings"` to `parser/navpos_test.go`'s import block:

```go
import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)
```

- [ ] **Step 3: Run both focused tests and confirm the intended failure**

Run:

```bash
go test ./ast ./parser -run 'TestClassPartExprEnd|TestClassPartExpressionSpan' -count=1
```

Expected: compilation fails because `(*ClassPart).ExprEnd` is undefined. Any parser failure or unrelated compile failure must be investigated before implementation.

- [ ] **Step 4: Document the two span contracts and implement `ExprEnd`**

Replace the opening `ClassPart` documentation and its `ExprPos` field comment in `ast/ast.go`, then add the method immediately after the struct:

```go
// ClassPart is one complete contribution in a composable class/style list. Its
// Pos and End span covers the contribution between comma delimiters, including
// surrounding trivia and any pipeline stages or `: cond` guard.
//
// A part is an unconditional Expr, an Expr emitted when Cond is true, an
// explicit CSS literal inside style={...}, or a value-form if/switch.
// Cond == "" means always. When Stages is non-empty, Expr is the pipeline seed
// and Stages are applied left-to-right (`seed |> s0 |> s1 ...`), mirroring
// Interp.Stages; the guard Cond is never piped.
//
// ClassPart is a Node so it can be keyed in the resolved map for renderer
// application and (T, error) auto-unwrap on plain parts, conditional or not
// (#88). When CSSSegments != nil, Expr/Cond/Stages/CF are unused. When CF !=
// nil, Expr/Cond/Stages and CSSSegments are unused.
type ClassPart struct {
	span
	Expr string
	// ExprPos is the first byte of Expr, the pipeline seed. ExprPos through
	// ExprEnd() is the exact expression-only source span. It is NoPos for
	// CSS-literal and value-form parts, which have no Expr.
	ExprPos token.Pos
	Cond    string
	// CondPos is the position of the first char of the `: cond` guard text in
	// source (NoPos when Cond == "").
	CondPos     token.Pos
	Stages      []PipeStage
	CSSSegments []Markup
	// CSSDoubleQuoted records the delimiter of a composed CSS literal part
	// (style={ css`…` } vs style={ css"…" }). See EmbeddedAttr.DoubleQuoted.
	CSSDoubleQuoted bool
	CF              *ValueCF
}

// ExprEnd returns the position immediately after Expr. It returns NoPos when
// the part is nil or has no source expression position.
func (p *ClassPart) ExprEnd() token.Pos {
	if p == nil || !p.ExprPos.IsValid() {
		return token.NoPos
	}
	return p.ExprPos + token.Pos(len(p.Expr))
}
```

- [ ] **Step 5: Format the changed Go files**

Run:

```bash
gofmt -w ast/ast.go ast/ast_test.go parser/navpos_test.go
```

Expected: the files are formatted with no other source changes.

- [ ] **Step 6: Run the focused tests and confirm they pass**

Run:

```bash
go test ./ast ./parser -run 'TestClassPartExprEnd|TestClassPartExpressionSpan' -count=1
```

Expected: both packages report `ok`.

- [ ] **Step 7: Run static checks on every changed Go file**

Run:

```bash
gopls check -severity=hint ast/ast.go ast/ast_test.go parser/navpos_test.go
```

Expected: no diagnostics.

- [ ] **Step 8: Run the repository verification gates**

Run, in order:

```bash
make check
make lint
make ci
```

Expected: every target exits successfully. `make ci` is the authoritative uncached gate.

- [ ] **Step 9: Commit the implementation**

Review `git diff` to confirm only the three planned Go files changed, then run:

```bash
git add ast/ast.go ast/ast_test.go parser/navpos_test.go
git commit -m "feat(ast): expose ClassPart expression end"
```

Expected: one implementation commit containing the API, documentation, and tests.

## Review acceptance

The task reviewer must verify the implementation against the design before code-quality review. The final independent adversarial reviewer must parse a guarded, pipelined part in a throwaway program, replace only `[ExprPos, ExprEnd())`, and demonstrate that the bytes containing both `|> stage` and `: cond` are unchanged. Any reviewer finding is fixed in the implementation commit's branch and all focused and repository gates are rerun before completion.
