# Generic Components: Review Fixes + Go Directives + gotip Lane — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the generic-components branch mergeable: fix the 10 verified review findings (type-param values must actually render; no silent diagnostic loss; no whole-run aborts), pass `//go:` directive comments through to generated `.x.go`, and stand up a go1.27-tip test lane so the generic-method path is actually exercised.

**Architecture:** All work happens in the existing worktree `/Users/jackieli/personal/gsxhq/gsx/.worktrees/generic-components` (branch `generic-components`), except Task 13 which edits the sibling repo `~/personal/gsxhq/gsxhq.github.io`. Parser fixes land in `parser/`, classifier + codegen fixes in `internal/codegen/`, one new runtime pair (`TextAny`/`AttrAny`) in the root `gsx` package, corpus cases pin every new behavior.

**Tech Stack:** Go 1.26.1 (pinned), `go/types` + `golang.org/x/exp/typeparams` (new module dep, tooling-only), txtar corpus harness, GitHub Actions.

## Global Constraints

- Go pinned to 1.26.1 (`GO_VERSION` in ci.yml); run all commands with that toolchain.
- Runtime (root `gsx` package) is standard-library only. Task 5 adds root-package code — it may import ONLY stdlib (`strconv`, `fmt`). The new `golang.org/x/exp/typeparams` dep (Task 4) is imported ONLY from `internal/codegen`.
- Never hand-edit `.x.go` or golden files; regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- Every syntax/codegen change ships a corpus case per context (text/attr/child/children).
- No "simple heuristics": term-set classification uses `typeparams.NormalTerms` (real normalization), not string matching.
- Working dir for all commands: the worktree root, unless a task says otherwise.
- End every commit message with:
  `Claude-Session: https://claude.ai/code/session_01R6cMqzYs4Wo28Q68FsQgM5`

**Verification baseline (run before Task 1):** `make check` must pass on the worktree HEAD. If it does not, stop and report.

---

### Task 1: Shared `ast.IsComponentTag` (finding 10)

`parser/markup.go:canHaveTypeArgs` and `internal/codegen/emit.go:isComponentTag` implement the same uppercase-or-dotted rule twice. Hoist one predicate into the `ast` package (both packages already import it).

**Files:**
- Create: `ast/componenttag_test.go`
- Modify: `ast/ast.go` (append), `parser/markup.go:634-639`, `internal/codegen/emit.go:1925-1933`

**Interfaces:**
- Produces: `func IsComponentTag(tag string) bool` in package `ast` — used by Tasks 2-9 test code freely.

- [ ] **Step 1: Write the failing test**

```go
// ast/componenttag_test.go
package ast

import "testing"

func TestIsComponentTag(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{"", false},
		{"div", false},
		{"my-el", false},
		{"Box", true},
		{"ui.Button", true},
		{"p.Row", true},
		{"strings.x", true}, // dotted always wins, mirroring both old impls
	}
	for _, c := range cases {
		if got := IsComponentTag(c.tag); got != c.want {
			t.Errorf("IsComponentTag(%q) = %v, want %v", c.tag, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ast -run TestIsComponentTag -v`
Expected: FAIL — `undefined: IsComponentTag`

- [ ] **Step 3: Implement in ast/ast.go (append at end of file)**

```go
// IsComponentTag reports whether a tag names a component (uppercase first
// letter or dotted, e.g. ui.Button) rather than an HTML element. Single
// source of truth for the parser (type-arg admission) and codegen (call
// lowering) — the two MUST agree or type args get rejected on tags codegen
// lowers as components.
func IsComponentTag(tag string) bool {
	if tag == "" {
		return false
	}
	if strings.Contains(tag, ".") {
		return true
	}
	return tag[0] >= 'A' && tag[0] <= 'Z'
}
```

(`ast/ast.go` may not import `strings` yet — add it.)

- [ ] **Step 4: Delegate both callers**

In `parser/markup.go`, delete the `canHaveTypeArgs` function body logic and delegate:

```go
func canHaveTypeArgs(tag string) bool { return ast.IsComponentTag(tag) }
```

In `internal/codegen/emit.go`, replace the body of `isComponentTag` (keep the name — many call sites):

```go
// isComponentTag delegates to gsxast.IsComponentTag — see that function for the
// rule. Kept as a local alias for the many existing call sites.
func isComponentTag(tag string) bool { return gsxast.IsComponentTag(tag) }
```

(`emit.go` already imports the ast package; check the alias used at the top of the file — it is `"github.com/gsxhq/gsx/ast"`, imported as `ast` or `gsxast` depending on file; match it.)

- [ ] **Step 5: Run tests and commit**

Run: `go test ./ast ./parser ./internal/codegen -count=1`
Expected: PASS

```bash
git add ast/ parser/markup.go internal/codegen/emit.go
git commit -m "refactor: single IsComponentTag predicate in ast package"
```

---

### Task 2: `delimEnd` + type-list stop tokens (findings 4, 9)

`bracketEnd` is a copy of `parenEnd`, and on a missing `]` it scans the whole file and can match a `]` in later markup prose, anchoring errors far from the typo. Fold both into one `delimEnd` and give the bracket variant a stop-token set of tokens that can never occur in a Go type-argument/type-parameter list, so a runaway scan fails fast and the caller's error stays anchored at the `[`.

Stop tokens: `SEMICOLON` (both a real `;` and the scanner's ASI semicolon — Go itself rejects `f[Foo\n]`, so this matches Go), `LSS` (`<` alone is never valid in a type; `<-` in chan types is the distinct `ARROW` token), `GTR` (`>`), `QUO` (`/`), `ILLEGAL`. `parenEnd` keeps `nil` stops — receiver/param scanning behavior is unchanged.

**Files:**
- Modify: `parser/boundary.go:266-318` (replace `parenEnd` + `bracketEnd`)
- Test: `parser/boundary_test.go`, `parser/markup_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func delimEnd(src string, open int, close token.Token, stop map[token.Token]bool) (int, bool)`; `parenEnd`/`bracketEnd` keep their exact signatures `(src string, open int) (int, bool)`.

- [ ] **Step 1: Write the failing tests**

Append to `parser/boundary_test.go`:

```go
func TestBracketEndStopsAtMarkup(t *testing.T) {
	// Missing ']' on the type-arg list; the ']' later in prose must NOT match.
	src := `<Box[int value={7} /> <p>list ] end</p>`
	if _, ok := bracketEnd(src, 4); ok {
		t.Fatal("bracketEnd matched a ']' in unrelated markup prose; want not-found")
	}
}

func TestBracketEndValidLists(t *testing.T) {
	cases := []struct {
		src  string
		open int
		want int // byte offset of the matching ']'
	}{
		{`<Box[int] />`, 4, 8},
		{`<Box[map[string]int] />`, 4, 19},
		{`<Box[chan<- int] />`, 4, 15}, // ARROW must not trip the LSS stop
		{`<Box[T, U] />`, 4, 9},
	}
	for _, c := range cases {
		got, ok := bracketEnd(c.src, c.open)
		if !ok || got != c.want {
			t.Errorf("bracketEnd(%q, %d) = (%d, %v), want (%d, true)", c.src, c.open, got, ok, c.want)
		}
	}
}
```

Append to `parser/markup_test.go`:

```go
func TestUnterminatedTypeArgsAnchoredAtBracket(t *testing.T) {
	src := "package v\n\ncomponent Page() {\n\t<Box[int value={7} />\n\t<p>list ] end</p>\n}\n"
	fset := token.NewFileSet()
	_, errs := ParseFile(fset, "in.gsx", src, 0)
	if len(errs) == 0 {
		t.Fatal("want a parse error")
	}
	pos := fset.Position(errs[0].Pos)
	if pos.Line != 4 {
		t.Fatalf("error anchored at line %d (%s), want line 4 (the broken <Box[ tag)", pos.Line, errs[0].Msg)
	}
	if !strings.Contains(errs[0].Msg, "unterminated type args") {
		t.Fatalf("got error %q, want unterminated type args", errs[0].Msg)
	}
}
```

(Match the existing test-file idioms for constructing/parsing — check neighboring tests in the same files for the exact `ParseFile` signature and error type; adjust the assertion helpers accordingly.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser -run 'TestBracketEnd|TestUnterminatedTypeArgs' -v`
Expected: `TestBracketEndStopsAtMarkup` FAILS (bracketEnd currently finds the prose `]`); `TestUnterminatedTypeArgsAnchoredAtBracket` FAILS (error reported on line 5).

- [ ] **Step 3: Implement delimEnd, replace both functions**

Replace `parenEnd` and `bracketEnd` in `parser/boundary.go` with:

```go
// delimEnd returns the index of the `close` token matching the opener at
// src[open], scanning Go tokens from `open` so prose before `open` is never
// tokenized. `stop` (may be nil) is a set of tokens that terminate the scan as
// not-found: callers use it to bound a scan to a syntactic context where those
// tokens are impossible, so a missing closer fails fast at the opener instead
// of matching an unrelated closer pages later.
func delimEnd(src string, open int, close token.Token, stop map[token.Token]bool) (int, bool) {
	sub := src[open:]
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(sub))
	var s scanner.Scanner
	s.Init(file, []byte(sub), nil, scanner.ScanComments)

	depth := 0
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return 0, false
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
			if depth == 0 && tok == close {
				return open + fset.Position(pos).Offset, true
			}
		default:
			// Stop tokens apply only DIRECTLY inside the scanned list (depth
			// 1): nested brackets/braces may legally contain them (e.g. the
			// `/` in an array-length expression `[8/4]byte`).
			if depth == 1 && stop[tok] {
				return 0, false
			}
		}
	}
}

// typeListStop are tokens that can never occur inside a Go type-argument or
// type-parameter list: `<` alone (chan's `<-` is the distinct ARROW token),
// `>`, `/`, an inserted-or-real semicolon (Go itself rejects a bare newline
// before the `]`), and scanner ILLEGAL. Hitting one means the `[` list is
// unterminated — the caller anchors its error at the opener.
var typeListStop = map[token.Token]bool{
	token.SEMICOLON: true,
	token.LSS:       true,
	token.GTR:       true,
	token.QUO:       true,
	token.ILLEGAL:   true,
}

// parenEnd returns the index of the `)` matching the `(` at src[open].
func parenEnd(src string, open int) (int, bool) {
	return delimEnd(src, open, token.RPAREN, nil)
}

// bracketEnd returns the index of the `]` matching the `[` at src[open],
// bounded by typeListStop (it only ever scans type-arg/type-param lists).
func bracketEnd(src string, open int) (int, bool) {
	return delimEnd(src, open, token.RBRACK, typeListStop)
}
```

- [ ] **Step 4: Run the parser suite**

Run: `go test ./parser -count=1`
Expected: PASS, including the two new tests and all pre-existing bracket/paren tests.

- [ ] **Step 5: Commit**

```bash
git add parser/boundary.go parser/boundary_test.go parser/markup_test.go
git commit -m "fix(parser): bound type-arg bracket scan; dedup parenEnd/bracketEnd into delimEnd"
```

---

### Task 3: Reject empty `[]` type lists (finding 5)

`<Box[]/>` and `component X[](…)` silently parse as "no type args/params" and `gsx fmt` then deletes the brackets. Go rejects `func F[]()`; gsx should reject too, at parse time, anchored at the `[`.

**Files:**
- Modify: `parser/markup.go` (~line 578, after `typeArgs = strings.TrimSpace(raw)`), `parser/component.go` (~line 52, after `c.TypeParams = strings.TrimSpace(raw)`)
- Test: `parser/markup_test.go`, `parser/component_test.go`

- [ ] **Step 1: Write the failing tests**

`parser/markup_test.go`:

```go
func TestEmptyTypeArgsRejected(t *testing.T) {
	src := "package v\n\ncomponent Page() {\n\t<Box[] value={7} />\n}\n"
	fset := token.NewFileSet()
	_, errs := ParseFile(fset, "in.gsx", src, 0)
	if len(errs) == 0 || !strings.Contains(errs[0].Msg, "empty type argument list") {
		t.Fatalf("errs = %v, want empty type argument list", errs)
	}
	if p := fset.Position(errs[0].Pos); p.Line != 4 {
		t.Fatalf("anchored at line %d, want 4", p.Line)
	}
}
```

`parser/component_test.go`:

```go
func TestEmptyTypeParamsRejected(t *testing.T) {
	src := "package v\n\ncomponent Box[](value int) {\n\t<p>x</p>\n}\n"
	fset := token.NewFileSet()
	_, errs := ParseFile(fset, "in.gsx", src, 0)
	if len(errs) == 0 || !strings.Contains(errs[0].Msg, "empty type parameter list") {
		t.Fatalf("errs = %v, want empty type parameter list", errs)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser -run 'TestEmptyType' -v`
Expected: both FAIL (no error produced today).

- [ ] **Step 3: Implement**

`parser/markup.go`, inside the `if p.peek() == '['` block — after `typeArgs = strings.TrimSpace(raw)` and BEFORE `p.i = end + 1`:

```go
		if typeArgs == "" {
			return nil, p.errorf(p.pos(), "empty type argument list in <%s[]>", tag)
		}
```

(`p.i` still sits on the `[` at that point, so `p.pos()` anchors correctly.)

`parser/component.go`, same position in its `'['` block:

```go
		if c.TypeParams == "" {
			return nil, p.errorf(p.pos(), "empty type parameter list")
		}
```

- [ ] **Step 4: Run tests, check the formatter can no longer eat brackets**

Run: `go test ./parser ./internal/printer -count=1`
Expected: PASS (a parse error means `gsx fmt` refuses the file rather than rewriting it — no formatter change needed).

- [ ] **Step 5: Commit**

```bash
git add parser/markup.go parser/component.go parser/markup_test.go parser/component_test.go
git commit -m "fix(parser): reject empty [] type argument/parameter lists"
```

---

### Task 4: Classify type parameters (finding 1, part 1: the classifier)

`classify` (internal/codegen/analyze.go:1452) falls to `catUnsupported` for `*types.TypeParam` because a type param's `Underlying()` is its constraint interface. Extend it with real term-set analysis:

- **Uniform basic category** (all normalized terms classify to the same basic-kind category: string/bytes/int/uint/float/bool) → return that category. The existing emit conversions (`string(v)`, `int64(v)`, …) compile for type params whose whole type set shares the underlying kind, tilde or not.
- **Mixed renderable categories, all terms non-tilde** → new `catAnyMixed`: runtime dispatch (Task 5/6). Non-tilde terms mean the dynamic type is exactly one of the listed types, so a type switch is total.
- **Mixed with any `~` term** → `catUnsupported` (a named concrete type would slip through a runtime type switch — reject statically).
- **Any term unrenderable, or Node/NodeSlice via terms** → `catUnsupported` (a term-only constraint contributes no methods to T's method set, so `v.Render(...)`/`v.String()` would not compile; method-based constraints are already handled by the existing `implementsNode`/`implementsStringer` checks at the top of `classify`, which use `types.NewMethodSet` and therefore see constraint methods).
- Uniform-catStringer-by-terms also → `catAnyMixed`-or-unsupported per the same tilde rule (no static `.String()` path without a constraint method).

Term normalization comes from `golang.org/x/exp/typeparams.NormalTerms` — a real implementation, not a hand-rolled approximation. This is a new module-level dependency imported only from `internal/codegen` (the root runtime package stays stdlib-only; precedent: `golang.org/x/tools` is already a module dep).

**Files:**
- Modify: `internal/codegen/analyze.go` (category enum + `classify` + new `classifyTypeParam`), `go.mod`/`go.sum`
- Test: `internal/codegen/classify_test.go`

**Interfaces:**
- Produces: `catAnyMixed category` (new enum value, appended after `catStringer`); `classify(t types.Type) category` now meaningful for `*types.TypeParam`. Task 6 consumes `catAnyMixed` in both emit switches.

- [ ] **Step 1: Add the dependency**

```bash
go get golang.org/x/exp/typeparams@latest
go mod tidy
```

Expected: `go.mod` gains `golang.org/x/exp/typeparams`. Confirm the root package is untouched: `go list -deps . | grep -c x/exp` must print `0`.

- [ ] **Step 2: Write the failing test**

Append to `internal/codegen/classify_test.go` (reuse the file's `typeCheckFuncs` helper):

```go
// typeParamOf type-checks src and returns the first type parameter of func F.
func typeParamOf(t *testing.T, src string) *types.TypeParam {
	t.Helper()
	scope := typeCheckFuncs(t, src)
	sig := sigOf(t, scope, "F")
	if sig.TypeParams().Len() == 0 {
		t.Fatal("F has no type params")
	}
	return sig.TypeParams().At(0)
}

func TestClassifyTypeParam(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want category
	}{
		{"mixed string|int", `package x
func F[T string | int](v T) {}`, catAnyMixed},
		{"uniform ~string", `package x
func F[T ~string](v T) {}`, catString},
		{"uniform int kinds", `package x
func F[T ~int | ~int64](v T) {}`, catInt},
		{"mixed with tilde", `package x
func F[T ~string | int](v T) {}`, catUnsupported},
		{"unrenderable term", `package x
func F[T string | []int](v T) {}`, catUnsupported},
		{"stringer constraint method", `package x
import "fmt"
func F[T fmt.Stringer](v T) {}`, catStringer},
		{"any", `package x
func F[T any](v T) {}`, catUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(typeParamOf(t, c.src)); got != c.want {
				t.Fatalf("classify = %v, want %v", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/codegen -run TestClassifyTypeParam -v`
Expected: FAIL — `undefined: catAnyMixed`, then (after adding the const alone) wrong categories for the first three cases.

- [ ] **Step 4: Implement**

In `internal/codegen/analyze.go`, extend the enum (append after `catStringer`):

```go
	catStringer
	catAnyMixed // type param whose non-tilde type set mixes renderable basic kinds → runtime dispatch
```

In `classify`, after the `implementsStringer` check and before the `switch u := t.Underlying()` statement, insert:

```go
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		return classifyTypeParam(tp)
	}
```

Add below `classify` (import `"golang.org/x/exp/typeparams"`):

```go
// classifyTypeParam maps a type parameter to a render category from its
// constraint's normalized term set (typeparams.NormalTerms — real
// normalization of unions/embeddings, not an approximation).
//
//   - every term classifies to the SAME basic-kind category → that category:
//     the static conversions the emitter writes (string(v), int64(v), …)
//     compile for the whole type set, tilde or not.
//   - terms mix renderable categories and are ALL non-tilde → catAnyMixed:
//     the dynamic type is exactly one of the listed types, so the runtime
//     type switch (Writer.TextAny/AttrAny) is total.
//   - any ~term in a mixed set → catUnsupported: a named concrete type would
//     fall through the runtime switch, so reject statically.
//   - a term classifying to catNode/catNodeSlice/catStringer contributes no
//     METHOD to T (term ≠ embedded method), so it has no static call path;
//     Stringer terms are still fine in the runtime switch, Node terms are not
//     (rendering needs ctx). Method-BASED constraints (T fmt.Stringer,
//     T gsx.Node) never reach here — classify's method-set checks above run
//     first and see constraint methods via types.NewMethodSet.
func classifyTypeParam(tp *types.TypeParam) category {
	terms, err := typeparams.NormalTerms(tp)
	if err != nil || len(terms) == 0 {
		return catUnsupported // empty/invalid type set, or `any`
	}
	uniform := true
	hasTilde := false
	var first category
	for i, tm := range terms {
		c := classify(tm.Type())
		switch c {
		case catUnsupported, catNode, catNodeSlice:
			return catUnsupported
		}
		if tm.Tilde() {
			hasTilde = true
		}
		if i == 0 {
			first = c
		} else if c != first {
			uniform = false
		}
	}
	if uniform && first != catStringer {
		return first
	}
	if hasTilde {
		return catUnsupported
	}
	return catAnyMixed
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/codegen -run TestClassify -v -count=1`
Expected: PASS. Also run the full package: `go test ./internal/codegen -count=1` — corpus/codegen behavior is unchanged so far because no emit switch handles `catAnyMixed` yet (it hits the same `default:` diagnostics as before).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/codegen/analyze.go internal/codegen/classify_test.go
git commit -m "feat(codegen): classify type parameters via constraint term sets"
```

---

### Task 5: Runtime `TextAny` / `AttrAny` (finding 1, part 2: the runtime)

Mixed non-tilde type sets need runtime dispatch. Add two `Writer` methods to the root package (STDLIB ONLY). Formatting must be byte-identical to the static paths: `strconv.FormatInt(..., 10)`, `strconv.FormatUint(..., 10)`, `strconv.FormatFloat(..., 'g', -1, 64)` (64 even for float32 — that is what the static `FloatInto(float64(v))` path produces), `strconv.FormatBool`, Stringer via `.String()`, string/[]byte verbatim. Cross-reference `gsx.Val` (val.go), which established the named-types-not-matched contract this mirrors.

**Files:**
- Modify: `writer.go`
- Test: `writer_test.go`

**Interfaces:**
- Produces: `func (gw *Writer) TextAny(v any)` (HTML-escaped text write) and `func (gw *Writer) AttrAny(v any)` (attribute-escaped write). Task 6's generated code calls them as `_gsxgw.TextAny(expr)` / `_gsxgw.AttrAny(expr)`.

- [ ] **Step 1: Write the failing tests**

Append to `writer_test.go` (match the file's existing buffer/Writer setup idiom):

```go
func TestTextAny(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"a<b", "a&lt;b"},
		{[]byte("x&y"), "x&amp;y"},
		{7, "7"},
		{int64(-3), "-3"},
		{uint8(200), "200"},
		{1.5, "1.5"},
		{float32(2.5), "2.5"},
		{true, "true"},
		{stubStringer{}, "stub"}, // reuse/declare a Stringer test type in this file
	}
	for _, c := range cases {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.TextAny(c.in)
		if err := gw.Err(); err != nil {
			t.Fatalf("TextAny(%#v): %v", c.in, err)
		}
		if buf.String() != c.want {
			t.Errorf("TextAny(%#v) = %q, want %q", c.in, buf.String(), c.want)
		}
	}
}

func TestTextAnyUnsupportedSetsErr(t *testing.T) {
	type named string // named type: NOT matched, mirroring gsx.Val's contract
	var buf bytes.Buffer
	gw := W(&buf)
	gw.TextAny(named("x"))
	if gw.Err() == nil {
		t.Fatal("want error for named type through TextAny")
	}
}

func TestAttrAnyEscapes(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.AttrAny(`a"b`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "a&#34;b" {
		t.Errorf("AttrAny = %q", got) // adjust expected escape to match AttrValue's actual table
	}
}
```

(If `writer_test.go` lacks a Stringer stub, declare `type stubStringer struct{}` with `func (stubStringer) String() string { return "stub" }`. Verify the exact `AttrValue` escaping of `"` from `escape.go` before pinning the golden — use whatever `AttrValue("a\"b")` actually produces.)

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestTextAny|TestAttrAny' -v`
Expected: FAIL — `gw.TextAny undefined`.

- [ ] **Step 3: Implement in writer.go**

```go
// anyRenderString converts a dynamically-typed renderable value to its text
// form, matching the static per-category emit paths byte-for-byte (FormatInt
// base 10, FormatFloat 'g' -1 64, FormatBool, Stringer.String, string/[]byte
// verbatim). It matches EXACT types only — a named scalar (type Slug string)
// returns ok=false, mirroring gsx.Val's documented contract — because codegen
// only routes here for type parameters whose constraint terms are all
// non-tilde, where the dynamic type is exactly one of the listed types.
func anyRenderString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	case fmt.Stringer:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	case int:
		return strconv.FormatInt(int64(t), 10), true
	case int8:
		return strconv.FormatInt(int64(t), 10), true
	case int16:
		return strconv.FormatInt(int64(t), 10), true
	case int32:
		return strconv.FormatInt(int64(t), 10), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case uint:
		return strconv.FormatUint(uint64(t), 10), true
	case uint8:
		return strconv.FormatUint(uint64(t), 10), true
	case uint16:
		return strconv.FormatUint(uint64(t), 10), true
	case uint32:
		return strconv.FormatUint(uint64(t), 10), true
	case uint64:
		return strconv.FormatUint(t, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(t), 10), true
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 64), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	}
	return "", false
}

// TextAny writes v as escaped text, dispatching on its dynamic type. Codegen
// emits it for interpolations whose type is a type parameter with a MIXED
// non-tilde constraint (e.g. T string | int) — classify proves every term
// renderable at generate time, so the dispatch is total for generated code.
func (gw *Writer) TextAny(v any) {
	s, ok := anyRenderString(v)
	if !ok {
		if gw.err == nil {
			gw.err = fmt.Errorf("gsx: TextAny: unsupported dynamic type %T", v)
		}
		return
	}
	gw.Text(s)
}

// AttrAny is TextAny for attribute-value position (AttrValue escaping).
func (gw *Writer) AttrAny(v any) {
	s, ok := anyRenderString(v)
	if !ok {
		if gw.err == nil {
			gw.err = fmt.Errorf("gsx: AttrAny: unsupported dynamic type %T", v)
		}
		return
	}
	gw.AttrValue(s)
}
```

(Add `"fmt"` and `"strconv"` to writer.go's imports if absent. Check how `gw.err` is guarded elsewhere in writer.go and follow the same pattern.)

- [ ] **Step 4: Run root-package tests**

Run: `go test . -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add writer.go writer_test.go
git commit -m "feat(runtime): Writer.TextAny/AttrAny for mixed-constraint type params"
```

---

### Task 6: Wire `catAnyMixed` into emit + corpus cases per context (findings 1, 6-partial)

**Files:**
- Modify: `internal/codegen/emit.go` — the text-interpolation category switch (the one whose `default:` emits `error[unrenderable]`, ~line 1035-1060) and the attribute-value category switch (the one whose `default:` emits `error[unsupported-attr-type]`, ~line 1900-1920)
- Modify: `internal/corpus/testdata/cases/components/generic_function_component.txtar`
- Create: `internal/corpus/testdata/cases/components/generic_uniform_constraint.txtar`, `internal/corpus/testdata/cases/components/generic_attr_typeparam.txtar`, `internal/corpus/testdata/cases/components/generic_children.txtar`, `internal/corpus/testdata/cases/components/generic_tilde_mixed_diag.txtar`

**Interfaces:**
- Consumes: `catAnyMixed` (Task 4), `_gsxgw.TextAny`/`_gsxgw.AttrAny` (Task 5).

- [ ] **Step 1: Make the headline corpus case actually interpolate**

Edit `generic_function_component.txtar`'s `input.gsx` body:

```
component Box[T string | int](value T) {
	<span>{value}</span>
}
```

(Leave `Page`, `invoke`, and the golden sections in place — they will be regenerated.)

- [ ] **Step 2: Run corpus to verify current failure**

Run: `go test ./internal/corpus -run TestCorpus -count=1 2>&1 | head -30`
Expected: FAIL on `generic_function_component` with `error[unrenderable]: interpolation "value" has type T` — this is the review's headline reproduction.

- [ ] **Step 3: Wire the two emit switches**

Text switch — add before `default:`:

```go
	case catAnyMixed:
		fmt.Fprintf(b, "\t\t_gsxgw.TextAny(%s)\n", expr)
```

Attr switch — add before `default:`:

```go
	case catAnyMixed:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrAny(%s)\n", expr)
```

Then improve both `default:` diagnostics for type params (better DX for the tilde-mixed rejection). In the text switch:

```go
	default:
		if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
			bag.Errorf(n.Pos(), n.End(), "unrenderable",
				"interpolation %q has type parameter %s (constraint %s): only same-kind or all-non-tilde renderable constraints render directly — convert explicitly in the expression", expr, t, tp.Constraint())
			return false
		}
		bag.Errorf(n.Pos(), n.End(), "unrenderable", "interpolation %q has type %s; not a renderable type", expr, t)
		return false
```

Mirror the same shape in the attr switch with code `unsupported-attr-type` and its existing message as the non-type-param fallback.

- [ ] **Step 4: Add the new corpus cases**

`generic_uniform_constraint.txtar` (uniform → static fast path; pins that NO TextAny appears):

```
-- input.gsx --
package views

type Slug string

component Tag[T ~string](value T) {
	<b>{value}</b>
}

component Page() {
	<Tag[Slug] value={Slug("go")} />
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<b>go</b>
-- generated.x.go.golden --
```

`generic_attr_typeparam.txtar` (attr context, mixed set → AttrAny):

```
-- input.gsx --
package views

component Field[T string | int](value T) {
	<input value={value} />
}

component Page() {
	<Field[int] value={7} />
	<Field[string] value={"a<b"} />
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<input value="7"/><input value="a&lt;b"/>
-- generated.x.go.golden --
```

`generic_children.txtar` (children context — generic component receiving children):

```
-- input.gsx --
package views

component Wrap[T string | int](title T) {
	<div>{title}{children}</div>
}

component Page() {
	<Wrap[string] title={"hi"}><em>kid</em></Wrap>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div>hi<em>kid</em></div>
-- generated.x.go.golden --
```

`generic_tilde_mixed_diag.txtar` (tilde-mixed → positioned diagnostic, no output):

```
-- input.gsx --
package views

component Box[T ~string | int](value T) {
	<span>{value}</span>
}

component Page() {
	<Box[string] value={"x"} />
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

(Leave `diagnostics.golden` empty in the initial file; `-update` in Step 5 fills it. After regeneration it MUST contain exactly one `error[unrenderable]` naming the type parameter and its constraint, anchored at the `{value}` interpolation line — if it comes back empty or with a different code, the Task's Step 3 diagnostics wiring is wrong; fix that, don't accept the golden.)

Before regenerating, open one existing diagnostics-bearing case (e.g. under `internal/corpus/testdata/cases/`, grep for a non-empty `diagnostics.golden`) and mirror its exact section layout for error cases (some sections may be omitted when generation fails — copy the convention exactly). Check `render.golden` exactness with the harness's HTML comparison — the exact `<input value="7"/>` spacing/self-closing form must come from `-update`, not be hand-guessed.

- [ ] **Step 5: Regenerate goldens, then verify clean**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
```

Expected: second run PASS. Inspect the regenerated `generic_function_component.txtar` golden: it must contain `_gsxgw.TextAny(_gsxp.Value)` (or the props-bound expression the emitter uses) and `render.golden` must now be `<span>7</span><span>ok</span>`. Inspect `generic_uniform_constraint` golden: static `Text(string(...))`-form call, no `TextAny`. If `-update` also rewrites `coverage.golden`, commit that too (a missed manifest bump fails the suite).

- [ ] **Step 6: Full check + commit**

Run: `go test ./internal/codegen ./internal/corpus . -count=1`
Expected: PASS.

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat(codegen): render type-parameter values (static uniform path + TextAny/AttrAny dispatch)"
```

---

### Task 7: Stop swallowing type-param parse errors (findings 2, 8)

`emitComponentSkeleton` (analyze.go:499) hits a `parseTypeParamNames` error, calls `emitComponentStub`, which RE-parses the same string (analyze.go:2408), swallows the error, and emits a non-generic stub whose prop fields still reference the undeclared `T` → unmappable skeleton type errors → `generateFile` suppressed for the whole package with zero diagnostics.

Fix: parse type params ONCE at the top of `emitComponentSkeleton`, pass the results into `emitComponentStub` (delete its internal re-parse), and on error emit the stub with `params=nil` — exactly the shape the `parseParams`-failure branch already uses, which keeps the skeleton type-checking cleanly so `genComponent`'s existing `invalid-syntax` diagnostic fires. (`genComponent`'s own parse stays — it mirrors the long-standing `parseParams` double-parse pattern; the review's perf point is addressed by removing the guaranteed-to-fail third parse.)

**Files:**
- Modify: `internal/codegen/analyze.go` (`emitComponentSkeleton` ~480-510, `emitComponentStub` ~2407-2415, and every other `emitComponentStub` call site — grep them all)
- Test: create `internal/codegen/generic_typeparam_err_test.go`

**Interfaces:**
- Produces: `emitComponentStub(sb *strings.Builder, c *gsxast.Component, params []param, withRecv bool, typeParamNames []string, typeParamsDecl string)` — Task 8 adds one more knob to this same signature; coordinate if executing out of order.

- [ ] **Step 1: Write the failing test**

```go
// internal/codegen/generic_typeparam_err_test.go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A malformed type-param list must produce a positioned diagnostic and must
// NOT take down generation for healthy siblings in the same package.
func TestBadTypeParamListDiagnosticAndSiblingSurvival(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module badtp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing constraint: go/parser rejects `[T]` in a func type-param list.
	writeFile(t, pkgDir, "bad.gsx", "package views\n\ncomponent Box[T](value T) {\n\t<span>x</span>\n}\n")
	writeFile(t, pkgDir, "good.gsx", "package views\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{pkgDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	dr := out[pkgDir]
	var found bool
	for _, d := range dr.Diags {
		if d.Code == "invalid-syntax" && strings.Contains(d.Message, "type params") {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "bad.gsx") || d.Start.Line != 3 {
				t.Errorf("diagnostic not anchored at bad.gsx:3: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no invalid-syntax diagnostic for the bad type-param list; diags=%+v", dr.Diags)
	}
	var goodGenerated bool
	for path := range dr.Files {
		if strings.HasSuffix(path, "good.gsx") {
			goodGenerated = true
		}
	}
	if !goodGenerated {
		t.Errorf("sibling good.gsx lost its generated output; files=%v", dr.Files)
	}
}
```

(`DirResult{Files map[string][]byte; Diags []diag.Diagnostic}` — Files is keyed by `.gsx` path, `Diagnostic` has `Code`, `Message`, `Start token.Position`; see `generate_dirs.go` and the assertion idiom in `diag_recovery_test.go`. If the fix routes the message differently — e.g. wording other than "type params" — pin whatever `genComponent`'s `invalid-syntax` message actually says, but the code and the bad.gsx anchoring are non-negotiable.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestBadTypeParamList -v`
Expected: FAIL — today: zero diagnostics AND no generated file for either .gsx (the review's live-probe result).

- [ ] **Step 3: Implement**

In `emitComponentSkeleton`, hoist the type-param parse to the TOP (before `parseParams`):

```go
	typeParamNames, tpErr := parseTypeParamNames(c.TypeParams)
	typeParamsDecl := typeParamDecl(c.TypeParams)
	if tpErr != nil {
		typeParamNames, typeParamsDecl = nil, ""
	}
```

Update the two early-error branches to pass them through:

```go
	params, err := parseParams(c.Params)
	if err != nil {
		emitComponentStub(sb, c, nil, true, typeParamNames, typeParamsDecl)
		return errSkipComponent
	}
	if err := checkReservedParams(params); err != nil {
		emitComponentStub(sb, c, params, true, typeParamNames, typeParamsDecl)
		return errSkipComponent
	}
	if tpErr != nil {
		// Unparsable type-param list: emit the stub with NO params (their types
		// may reference the now-undeclared type params — an undefined-T type
		// error here is unmappable and silently kills the whole package's
		// generation). params=nil matches the parseParams-failure shape above;
		// genComponent re-parses at emit time and records the positioned
		// invalid-syntax diagnostic.
		emitComponentStub(sb, c, nil, true, nil, "")
		return errSkipComponent
	}
	typeParamsUse := typeParamUse(typeParamNames)
```

(Delete the old mid-function `parseTypeParamNames` block.) Then in `emitComponentStub`: change the signature to accept `typeParamNames []string, typeParamsDecl string`, delete its internal `parseTypeParamNames`/`typeParamDecl`/`typeParamErr` lines, and use the passed values. Update EVERY call site: `grep -n "emitComponentStub(" internal/codegen/*.go` — including the `withRecv=false` parseRecv-failure branch — passing the hoisted values.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/codegen ./internal/corpus -count=1`
Expected: PASS including the new test and all recovery/diag tests.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/generic_typeparam_err_test.go
git commit -m "fix(codegen): surface bad type-param lists as positioned diagnostics, not silent package loss"
```

---

### Task 8: Toolchain guard for generic METHOD components (finding 3)

On every released toolchain, `component (p Page) Box[T ...]` emits a generic-method skeleton that `go/parser` rejects, and `module_importer.go:449` turns that into a hard whole-run abort. Guard the path: on a toolchain without generic methods, skip the component with a positioned `unsupported-toolchain` diagnostic instead.

**Files:**
- Create: `internal/codegen/toolchain.go`
- Modify: `internal/codegen/analyze.go` (guard in `emitComponentSkeleton`; stub gains an `omitFunc` mode), `internal/codegen/emit.go` (`genComponent` guard), `internal/codegen/generic_method_go127_test.go` (drop its local `supportsGenericMethods`, use the shared one; add the `GSX_REQUIRE_GENERIC_METHODS` gate for Task 12)
- Modify: `docs/guide/syntax.md` (~line 58 table row) and `docs/guide/syntax/composition.md` — state the go1.27+ requirement and the `unsupported-toolchain` error on older toolchains
- Test: create `internal/codegen/generic_method_guard_test.go`

**Interfaces:**
- Consumes: Task 7's `emitComponentStub` signature.
- Produces: `func toolchainHasGenericMethods() bool` (memoized); stub signature gains `omitFunc bool` (final: `emitComponentStub(sb, c, params, withRecv, typeParamNames, typeParamsDecl string, omitFunc bool)` — adjust Task 7's call sites to pass `false`).

- [ ] **Step 1: Write the failing test**

```go
// internal/codegen/generic_method_guard_test.go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On a toolchain WITHOUT generic methods, a generic method component must be
// skipped with a positioned unsupported-toolchain diagnostic — never a hard
// abort — and other packages in the same run must still generate.
func TestGenericMethodUnsupportedToolchain(t *testing.T) {
	if toolchainHasGenericMethods() {
		t.Skip("toolchain parses generic methods; the guard path is inert (covered by TestGenericMethodComponentGo127)")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gm\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	viewsDir := filepath.Join(tmp, "views")
	otherDir := filepath.Join(tmp, "other")
	for _, d := range []string{viewsDir, otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, viewsDir, "views.gsx", "package views\n\ntype Page struct{}\n\ncomponent (p Page) Box[T string | int](value T) {\n\t<span>box</span>\n}\n")
	writeFile(t, otherDir, "other.gsx", "package other\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{viewsDir, otherDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error (whole-run abort — the bug this task fixes): %v", err)
	}
	var found bool
	for _, d := range out[viewsDir].Diags {
		if d.Code == "unsupported-toolchain" {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "views.gsx") || d.Start.Line != 5 {
				t.Errorf("diagnostic not anchored at views.gsx:5: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no unsupported-toolchain diagnostic; diags=%+v", out[viewsDir].Diags)
	}
	if len(out[otherDir].Files) != 1 {
		t.Errorf("sibling package must still generate; got files=%v", out[otherDir].Files)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestGenericMethodUnsupportedToolchain -v`
Expected: FAIL — today `GenerateDirs` returns the raw `method must have no type parameters` parse error and generates nothing.

- [ ] **Step 3: Implement the shared probe**

```go
// internal/codegen/toolchain.go
package codegen

import (
	goparser "go/parser"
	"go/token"
	"sync"
)

// toolchainHasGenericMethods reports whether the ACTIVE toolchain's go/parser
// accepts methods with type parameters (accepted for go1.27; rejected by all
// earlier releases). Probed once per process by parsing a canonical generic
// method — the same parser that will consume our emitted skeletons, so the
// probe can't drift from reality.
var toolchainHasGenericMethods = sync.OnceValue(func() bool {
	const src = "package p\ntype S struct{}\nfunc (S) M[T any](v T) T { return v }\n"
	_, err := goparser.ParseFile(token.NewFileSet(), "generic_method_probe.go", src, 0)
	return err == nil
})
```

In `generic_method_go127_test.go`: delete the local `supportsGenericMethods` and call `toolchainHasGenericMethods()`; also replace the plain skip with the require-gate (used by Task 12's lane):

```go
	if !toolchainHasGenericMethods() {
		if os.Getenv("GSX_REQUIRE_GENERIC_METHODS") == "1" {
			t.Fatal("GSX_REQUIRE_GENERIC_METHODS=1 but the active toolchain does not parse generic methods")
		}
		t.Skip("active Go toolchain does not parse generic methods yet")
	}
```

- [ ] **Step 4: Guard skeleton + emit**

`emitComponentSkeleton` — after Task 7's hoisted type-param parse and the recv-parse section, add:

```go
	if c.Recv != "" && len(typeParamNames) > 0 && !toolchainHasGenericMethods() {
		// A generic METHOD skeleton would fail this toolchain's go/parser and
		// abort the whole run (module_importer hard-errors on skeleton parse
		// failures). Emit the props struct only — no func — and let
		// genComponent record the positioned unsupported-toolchain diagnostic.
		// Sibling call sites of this component get positioned type errors at
		// their probes, which is the standard broken-component experience.
		emitComponentStub(sb, c, params, true, typeParamNames, typeParamsDecl, true /*omitFunc*/)
		return errSkipComponent
	}
```

`emitComponentStub`: add the `omitFunc bool` parameter; when true, emit the props struct exactly as today but skip the entire func/method stub emission (a generic-method stub would itself fail to parse). All existing call sites pass `false`.

`genComponent` (emit.go) — after its `parseTypeParamNames` block:

```go
	if c.Recv != "" && len(typeParamNames) > 0 && !toolchainHasGenericMethods() {
		bag.Errorf(c.Pos(), c.End(), "unsupported-toolchain",
			"generic method components require a Go toolchain with generic methods (go1.27+); active toolchain: %s", runtime.Version())
		return false
	}
```

(import `"runtime"` in emit.go.)

- [ ] **Step 5: Docs**

`docs/guide/syntax.md` — extend the generic-method table row's description: "generic method component; **requires a go1.27+ toolchain — older toolchains report `error[unsupported-toolchain]` for the component and generation continues**". Make the equivalent statement in `docs/guide/syntax/composition.md`'s generic-method section (find the "Meth…" paragraph the review cited).

- [ ] **Step 6: Run tests + commit**

Run: `go test ./internal/codegen -run 'TestGenericMethod' -v -count=1 && go test ./internal/codegen ./internal/corpus -count=1`
Expected: PASS (guard test passes on 1.26.1; go127 test skips).

```bash
git add internal/codegen/toolchain.go internal/codegen/analyze.go internal/codegen/emit.go internal/codegen/generic_method_go127_test.go internal/codegen/generic_method_guard_test.go docs/guide/syntax.md docs/guide/syntax/composition.md
git commit -m "fix(codegen): skip generic method components with a diagnostic on pre-go1.27 toolchains"
```

---

### Task 9: Cross-package generic tag coverage (finding 6, remainder)

`<ui.Button[T]>` is documented, live syntax with ZERO coverage. The corpus harness is single-package (one `input.gsx`), so this lives as a `GenerateDirs` unit test mirroring `writeCrossPkgModule` (batch_crosspkg_test.go) — note that justification in the test comment so the per-context corpus rule's paper trail is clear.

**Files:**
- Create: `internal/codegen/generic_crosspkg_test.go`

- [ ] **Step 1: Write the failing-or-passing test (pins the path either way)**

```go
// internal/codegen/generic_crosspkg_test.go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pins the dotted cross-package generic-tag path <components.Button[int]>:
// typeArgUse appended to a package-qualified callTarget + propsType. The txtar
// corpus is single-package, so this context lives here (per-context coverage
// rule; see CLAUDE.md).
func TestGenericCrossPackageTag(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/xg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "button.gsx", "package components\n\ncomponent Button[T string | int](label T) {\n\t<button>{label}</button>\n}\n")
	writeFile(t, tmp, "post.gsx", "package xg\n\nimport \"example.com/xg/components\"\n\ncomponent Post() {\n\t<components.Button[int] label={7} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	for _, want := range []string{
		"components.Button[int](components.ButtonProps[int]{Label: 7})",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("generated root source missing %q:\n%s", want, root)
		}
	}
}
```

(Adapt the module path in go.mod/imports if `GenerateDirs` requires a full domain-style path — mirror whatever `writeCrossPkgModule` uses, `example.com/x` style, exactly.)

- [ ] **Step 2: Run it**

Run: `go test ./internal/codegen -run TestGenericCrossPackageTag -v`
Two possible outcomes: PASS → the path works and is now pinned; FAIL → the review's "can be broken today" warning was real. If it fails, debug the dotted-tag lowering in `genChildComponent`/`emitProbes` (where `typeArgUse` joins the qualified call target) using the failure output, fix minimally, re-run. Either way the test lands.

- [ ] **Step 3: Commit**

```bash
git add internal/codegen/generic_crosspkg_test.go
git commit -m "test(codegen): pin cross-package generic tag lowering <pkg.Button[T]>"
```

---

### Task 10: Delete orphaned `emitRootElement`/`singleRoot`; fix S1040 (dead code)

IDE diagnostics flag `emitRootElement` (emit.go:382) and `singleRoot` (emit.go:1943) as unused on this branch, and `byo_lsp_test.go:156` has a same-type assertion (S1040). These functions drive child auto-fallthrough — verify they are genuinely orphaned by refactor, not accidentally disconnected.

**Files:**
- Modify: `internal/codegen/emit.go`, `internal/codegen/byo_lsp_test.go:156`

- [ ] **Step 1: Verify orphaned-not-broken**

```bash
grep -n "emitRootElement\|singleRoot" internal/codegen/*.go | grep -v _test
git log --oneline -S emitRootElement main..HEAD
git grep -n "emitRootElement" main -- internal/codegen | head -3
go test ./internal/corpus -run TestCorpus -count=1 2>&1 | tail -3
```

Expected: on this branch the only hits are the two definitions (+ any doc comments); on `main` there are real call sites; the corpus (which contains fallthrough cases — `child_class_fallthrough.txtar` etc.) PASSES, proving the replacement path covers the behavior. **GATE: if any fallthrough corpus case fails or the functions have live call sites, STOP — report instead of deleting (that would mean the branch broke fallthrough, a new finding).**

- [ ] **Step 2: Delete + fix**

Delete both functions (and any helper that `gopls check` then reports as newly unused). Fix `byo_lsp_test.go:156` by removing the redundant `.(goast.Expr)` assertion.

```bash
gopls check -severity=hint internal/codegen/emit.go internal/codegen/byo_lsp_test.go
```

Expected: no `unusedfunc` hints remain for these symbols.

- [ ] **Step 3: Test + commit**

Run: `go test ./internal/codegen ./internal/corpus -count=1`
Expected: PASS.

```bash
git add internal/codegen/emit.go internal/codegen/byo_lsp_test.go
git commit -m "chore(codegen): drop emitRootElement/singleRoot orphaned by the generic-components refactor"
```

---

### Task 11: `//go:` directive pass-through (spec slice 1)

Per `docs/superpowers/specs/2026-07-02-go-directive-comments-and-gotip-lane-design.md`: copy program-significant comment lines (`//go:<directive>`, legacy `// +build`) from the `.gsx` pre-package block (already captured in `ast.File.Doc`) into the generated `.x.go`, between the generated-code marker and the package clause. Prose and `//line` never copy. Byte-identical output for files with no directives.

**Files:**
- Modify: `internal/codegen/emit.go:137` (header emission) + new `goDirectiveLines` func
- Create: `internal/corpus/testdata/cases/components/gobuild_directives.txtar`, `internal/codegen/directive_passthrough_test.go`
- Modify: `docs/guide/syntax.md` (new "Build constraints and //go: directives" section), `docs/ROADMAP.md`

- [ ] **Step 1: Write the failing unit test**

```go
// internal/codegen/directive_passthrough_test.go
package codegen

import (
	"reflect"
	"testing"
)

func TestGoDirectiveLines(t *testing.T) {
	doc := `// Copyright 2026 Prose. Must not copy.
//
//go:build !windows && !never

// +build !windows,!never

//go:generate stringer -type=Kind
//line input.gsx:1
// go:build not-a-directive (space after //)
//golintish prose that merely starts with //go... but no colon-directive
//go:debug panicnil=1`
	want := []string{
		"//go:build !windows && !never",
		"// +build !windows,!never",
		"//go:generate stringer -type=Kind",
		"//go:debug panicnil=1",
	}
	if got := goDirectiveLines(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("goDirectiveLines:\n got %q\nwant %q", got, want)
	}
	if got := goDirectiveLines(""); got != nil {
		t.Fatalf("empty doc: got %q, want nil", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestGoDirectiveLines -v`
Expected: FAIL — `undefined: goDirectiveLines`.

- [ ] **Step 3: Implement**

In `internal/codegen/emit.go`:

```go
// goDirectiveLines extracts the program-significant comment lines from a
// file's pre-package doc block: `//go:<directive>` (no space after `//` —
// the toolchain's own directive rule) and the legacy `// +build` constraint
// spelling. Prose comments stay .gsx-only, and `//line` is deliberately
// excluded — it would corrupt the //line mapping this generator emits.
func goDirectiveLines(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "//go:") && len(l) > len("//go:") && l[len("//go:")] != ' ':
			out = append(out, l)
		case strings.HasPrefix(l, "// +build") || strings.HasPrefix(l, "//+build"):
			out = append(out, l)
		}
	}
	return out
}
```

Replace the header write at emit.go:137:

```go
	b.WriteString("// Code generated by gsx; DO NOT EDIT.\n")
	if dirs := goDirectiveLines(file.Doc); len(dirs) > 0 {
		// Program-significant comments pass through verbatim. Placement rule:
		// after the generated-code marker, blank-line-separated from the
		// package clause, satisfying both the marker convention and the
		// //go:build placement rules.
		b.WriteString("\n")
		for _, d := range dirs {
			b.WriteString(d)
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\npackage %s\n\n", file.Package)
```

(No-directive output is byte-identical to today's `"// Code generated by gsx; DO NOT EDIT.\n\npackage %s\n\n"` — the whole corpus is the regression test for that.) Do NOT touch the skeleton/overlay path — the in-memory type-check must stay build-context-independent (spec: generation emits every file regardless of host GOOS).

- [ ] **Step 4: Corpus case**

`internal/corpus/testdata/cases/components/gobuild_directives.txtar`:

```
-- input.gsx --
// Copyright 2026 The gsx Authors. Prose must NOT be copied.

//go:build !never

//go:generate echo gsx

package views

component Page() {
	<p>ok</p>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<p>ok</p>
-- generated.x.go.golden --
```

Regenerate + verify:

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
```

Expected: PASS; the golden's first lines must read marker → blank → `//go:build !never` → `//go:generate echo gsx` → blank → `package views` (gofmt may normalize blank lines between the two directives — accept what `format.Source` produced, that IS the contract), and the copyright prose must be absent. The `!never` constraint is true everywhere, so the corpus's compile/render step is unaffected.

- [ ] **Step 5: go-build exclusion probe**

Append to `directive_passthrough_test.go` (uses the Go toolchain like the gen/ e2e tests do):

```go
func TestBuildTagExcludesGeneratedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module tagx\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "on.gsx", "package tagx\n\ncomponent On() {\n\t<p>on</p>\n}\n")
	writeFile(t, tmp, "off.gsx", "//go:build never\n\npackage tagx\n\ncomponent Off() {\n\t<p>off</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for gsxPath, src := range out[tmp].Files {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		if werr := os.WriteFile(filepath.Join(tmp, base+".x.go"), src, 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	list := exec.Command("go", "list", "-f", "{{.GoFiles}}")
	list.Dir = tmp
	lout, lerr := list.CombinedOutput()
	if lerr != nil {
		t.Fatalf("go list: %v\n%s", lerr, lout)
	}
	if !strings.Contains(string(lout), "on.x.go") || strings.Contains(string(lout), "off.x.go") {
		t.Fatalf("go list = %s; want on.x.go included, off.x.go tag-excluded", lout)
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = tmp
	if bout, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build: %v\n%s", berr, bout)
	}
}
```

(The `{{.GoFiles}}` in the -f template is a Go template, not VitePress content — no v-pre concern; this is a `.go` test file. Confirm the Files-map key convention — "keyed by .gsx path" per `DirResult` — against one debugger/print run if the base-name derivation misses.)

- [ ] **Step 6: Docs + ROADMAP**

`docs/guide/syntax.md` — add a section:

```markdown
## Build constraints and `//go:` directives

Comment lines the Go toolchain acts on — `//go:build`, `//go:generate`,
`//go:debug`, and legacy `// +build` — written before the `package` clause of
a `.gsx` file are copied verbatim into the generated `.x.go`, so build
constraints work exactly as they do for hand-written Go:

​```gsx
//go:build linux

package views

component LinuxOnly() {
	<p>linux</p>
}
​```

`gsx generate` always generates every `.gsx` file regardless of the host
platform — constraints take effect at `go build`, so one generate pass serves
cross-compilation. Prose comments (license headers, docs) stay in the `.gsx`
only. Note the explicit constraint comment is the only mechanism: generated
file names never acquire Go's implicit `_GOOS` filename constraints.

One current limitation: two `.gsx` files with mutually exclusive constraints
may not declare the same component name — analysis type-checks a package's
`.gsx` files as one unit (see ROADMAP).
```

(Remember the repo rule: if any literal `{{ }}` ends up in this prose, wrap in `::: v-pre`.) Add the ROADMAP.md item: "tag-aware .gsx analysis (duplicate component names across mutually exclusive build constraints)".

- [ ] **Step 7: Test + commit**

Run: `go test ./internal/codegen -run 'TestGoDirective|TestBuildTag' -v -count=1 && go test ./internal/corpus -count=1`
Expected: PASS.

```bash
git add internal/codegen/emit.go internal/codegen/directive_passthrough_test.go internal/corpus/testdata docs/guide/syntax.md docs/ROADMAP.md
git commit -m "feat(codegen): pass //go: directive comments through to generated .x.go"
```

---

### Task 12: gotip lane (spec slice 2)

The `GSX_REQUIRE_GENERIC_METHODS` test gate landed in Task 8. Add the make target and the non-blocking CI job. (Test-only env var — deliberately NOT in `gen/envconfig.go`; it is not user config.)

**Files:**
- Modify: `Makefile`, `.github/workflows/ci.yml`

- [ ] **Step 1: Make target**

Add to `Makefile` (and add `test-gotip` to the `.PHONY` line):

```makefile
# Runs the go1.27-gated generic-methods tests under the gotip toolchain, with
# skip promoted to FAILURE (GSX_REQUIRE_GENERIC_METHODS=1) so the lane can
# never green-light while silently testing nothing. Requires gotip:
#   go install golang.org/dl/gotip@latest && gotip download
test-gotip:
	@command -v gotip >/dev/null 2>&1 || { \
		echo "gotip not found — install with:"; \
		echo "  go install golang.org/dl/gotip@latest && gotip download"; \
		exit 1; }
	GSX_REQUIRE_GENERIC_METHODS=1 gotip test ./internal/codegen -run 'Go127|GenericMethod' -count=1 -v
```

- [ ] **Step 2: Verify the target's failure modes locally**

```bash
make test-gotip            # gotip absent → must print the install hint and exit 1
GSX_REQUIRE_GENERIC_METHODS=1 go test ./internal/codegen -run Go127 -count=1
```

Expected: first command exits 1 with the hint; second FAILS with the "toolchain does not parse generic methods" fatal (on go1.26.1) — proving the promote-skip-to-failure gate works. (Optionally install gotip and run the real lane; if gotip's parser accepts generic methods the go127 test must PASS — if it does not accept them yet, the lane fails loudly, which is the intended signal to revisit.)

- [ ] **Step 3: CI job**

Append to `.github/workflows/ci.yml` under `jobs:` (sibling of `build-test`):

```yaml
  # Non-blocking tip lane: runs the go1.27-gated generic-method tests under
  # gotip with skip promoted to failure. continue-on-error keeps tip breakage
  # from blocking merges; the lane exists so the generic-method path is
  # actually executed somewhere before GO_VERSION reaches 1.27.
  go-tip:
    runs-on: ubuntu-latest
    continue-on-error: true
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - name: Cache gotip SDK
        uses: actions/cache@v4
        with:
          path: ~/sdk/gotip
          key: gotip-${{ runner.os }}-${{ github.run_number }}
          restore-keys: gotip-${{ runner.os }}-
      - name: Install gotip
        run: |
          go install golang.org/dl/gotip@latest
          gotip download
      - name: Run generic-method tests under tip
        run: make test-gotip
```

(Mirror the checkout/setup-go action versions the existing jobs use — read them from the file, don't assume v4/v5.)

- [ ] **Step 4: Commit**

```bash
git add Makefile .github/workflows/ci.yml
git commit -m "ci: gotip lane for generic-method tests (make test-gotip, non-blocking job)"
```

---

### Task 13: Docs-site grammar sibling (finding 7) + sibling commit sweep

Repo: `~/personal/gsxhq/gsxhq.github.io` (currently clean — the only sibling with NO generic-syntax work). Two surfaces: the Shiki grammar for static code blocks (`.vitepress/grammars/gsx.tmLanguage.json`) and the playground's CodeMirror StreamLanguage tokenizer (`.vitepress/theme/GsxPlayground.vue`).

**Files (in the sibling repo):**
- Modify: `.vitepress/grammars/gsx.tmLanguage.json`, `.vitepress/theme/GsxPlayground.vue`

- [ ] **Step 1: Port the tmLanguage changes from vscode-gsx**

`~/personal/gsxhq/vscode-gsx` has in-flight (uncommitted) generic-syntax edits to the SAME grammar format:

```bash
git -C ~/personal/gsxhq/vscode-gsx diff syntaxes/gsx.tmLanguage.json
```

Apply the equivalent edits to `.vitepress/grammars/gsx.tmLanguage.json`. At minimum two rules change:
1. `#component` begin pattern (line ~36) currently requires `name(` immediately: `"^(component)\\s+(?:\\(([^)]*)\\)\\s+)?([A-Za-z_][A-Za-z0-9_]*)\\s*\\(([^{]*)\\)\\s*(\\{)\\s*$"` — insert an optional bracketed type-param group after the name (follow vscode-gsx's exact regex + capture styling so the two grammars stay in lockstep).
2. `#component-tag` (line ~179) — allow `[...]` type args after the tag name before attrs.

- [ ] **Step 2: CodeMirror tokenizer**

In `GsxPlayground.vue`'s `tokenGoish` keyword regex (line ~523), add `any` to the alternation (`…|const|any|true|false…`). The tag regex `/<\/?[A-Za-z][\w.:-]*/` already stops before `[` and the bracket contents fall through to `tokenGoish` (brackets → punctuation, uppercase idents → typeName), so no tag-rule change is needed — verify visually in Step 3.

- [ ] **Step 3: Verify with a real build**

```bash
cd ~/personal/gsxhq/gsxhq.github.io && npm run build
```

Expected: build succeeds (grammar JSON is parsed at config load — a bad regex fails here). Then `npm run dev`, open a page containing the new generic examples (`docs/guide/syntax/composition.md` renders from the gsx repo's docs at deploy time — paste `component Box[T any](v T) { <p>{v}</p> }` and `<Box[int] v={7} />` into the playground) and confirm: `component` keyword-colored, `Box` type-colored, `[T any]` not swallowing the rest of the line, tag `<Box[int]` rendering the tag name + bracketed args sanely in both light/dark.

- [ ] **Step 4: Commit (sibling repo) + sibling sweep**

```bash
cd ~/personal/gsxhq/gsxhq.github.io
git add .vitepress/grammars/gsx.tmLanguage.json .vitepress/theme/GsxPlayground.vue
git commit -m "feat(grammar): generic component type params/args in Shiki + CodeMirror"
```

Then sweep the other two siblings (their edits exist but are uncommitted — they must land when this branch merges):

```bash
git -C ~/personal/gsxhq/tree-sitter-gsx status --porcelain   # grammar.js, src/*, test/corpus/skeleton.txt
git -C ~/personal/gsxhq/vscode-gsx status --porcelain        # syntaxes/gsx.tmLanguage.{json,src.yaml}
```

For each: run its test suite (tree-sitter-gsx: `make test` or `npx tree-sitter test` per its README; vscode-gsx: `npm test` if present, else confirm the JSON regenerates from the .src.yaml per that repo's build script), then commit with message `feat(grammar): generic component type parameters and type arguments`. Do NOT push any sibling (vscode-gsx release is tag-gated; the site deploys on push) — pushing happens with the main-repo merge.

---

### Task 14: Finale — full verification + ROADMAP

- [ ] **Step 1: Full authoritative run**

```bash
cd /Users/jackieli/personal/gsxhq/gsx/.worktrees/generic-components
make ci
```

Expected: PASS end-to-end (build/vet/test both modules, examples drift, gofmt + gsx fmt). Fix anything that surfaces (likely suspects: gofmt on new files, examples drift if docs examples were touched, `gsx fmt -l .` on corpus inputs).

- [ ] **Step 2: Lint sweep of every file this plan touched**

```bash
gopls check -severity=hint parser/boundary.go parser/markup.go parser/component.go internal/codegen/analyze.go internal/codegen/emit.go internal/codegen/toolchain.go writer.go ast/ast.go
```

Expected: no new hints.

- [ ] **Step 3: ROADMAP review**

Update `docs/ROADMAP.md`: mark generic components with their real state (function + cross-package shipped; method components toolchain-gated behind go1.27 with the gotip lane watching), and confirm the Task 11 tag-aware-analysis item is present.

```bash
git add docs/ROADMAP.md
git commit -m "docs: roadmap — generic components status + tag-aware analysis follow-up"
```

- [ ] **Step 4: Independent adversarial review (repo convention)**

Per CLAUDE.md process, before merging this subsystem dispatch one independent adversarial reviewer that BUILDS THROWAWAY PROBE PROGRAMS (not just reads the diff) against at minimum: the headline `component Box[T string | int](value T) { <span>{value}</span> }` end-to-end render, the bad-type-param silent-loss scenario, the generic-method-on-1.26 scenario, `//go:build` pass-through + `go build` exclusion, and the empty-bracket/unterminated-bracket parser errors. Findings go back through the task list; merge only when the reviewer comes back clean.
