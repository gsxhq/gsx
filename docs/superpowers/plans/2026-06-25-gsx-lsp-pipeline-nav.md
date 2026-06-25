# LSP Go-to-def & Hover inside Pipelines — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make go-to-definition and hover resolve every cursor region of a piped interpolation/attr expression — the seed, each `|> filter` name, and symbols inside filter args — for the new seed-first lowering.

**Architecture:** Add source positions (`NamePos`/`ArgsPos`) to `ast.PipeStage` for cursor detection, then a defensive LSP router (`pipedTarget`) that walks the already-type-checked lowered call chain (`ExprMap[node]`), routes the cursor by region, and resolves via `Info.Uses`. Definition and hover both call it in place of the `hasPipeStages → null` guard.

**Tech Stack:** Go; the gsx parser/AST, `internal/lsp` reverse-mapper + hover renderers, `go/types`, the `gen` e2e LSP harness (`runLSP`).

## Global Constraints

- **No codegen change.** The LSP keeps a local `const ctxIdent = "ctx"` (it must NOT import `internal/codegen`); the value must match codegen's reserved `pipeCtxIdent`, guarded by the ctx walk test.
- The router is **fully defensive**: every type assertion on the lowered AST is comma-ok; any mismatch → `ok=false` (→ null), never a panic.
- Reuse the existing reverse-mapper (`innermostIdent`) and hover renderers (`markdownGo`, `rangeForSpan`, `qualifierFor`, `exprText`); do NOT add a new bridge.
- `.x.go` guard on the definition path only (hover shows a type, never navigates).
- Per [[gsx-syntax-change-test-coverage]], the parser change ships unit + corpus-faithfulness coverage.
- Commit messages end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Module-resolution tests are guarded by `testing.Short()`.

---

## File Structure

- `ast/ast.go` — add `NamePos`, `ArgsPos` to `PipeStage`.
- `parser/pipe.go` — `parsePipe(inner, base)` computes per-segment offsets; `parsePipeStage(seg, segBase)` sets the positions. `splitPipe` is **unchanged**.
- `parser/markup.go` — the one `parsePipe` call site passes `exprPos`.
- `parser/pipe_test.go` — update the `parsePipeStage` call signature; add a positions test.
- `internal/printer/corpus_test.go` — `zeroSpans` zeros the two new positions.
- `internal/lsp/pipe.go` — **new**: `pipedTarget` + `walkPipe` + helpers + `ctxIdent`.
- `internal/lsp/definition.go` — replace the `hasPipeStages` guard body with a `pipedTarget` call.
- `internal/lsp/hover.go` — same, on the hover path.
- `internal/lsp/pipe_test.go` — **new**: `walkPipe` unit test (incl. ctx).
- `gen/pipe_nav_e2e_test.go` — **new**: def + hover e2e for seed/filter/args.

---

## Task 1: Parser — `PipeStage` source positions

**Files:**
- Modify: `ast/ast.go`, `parser/pipe.go`, `parser/markup.go`, `parser/pipe_test.go`, `internal/printer/corpus_test.go`

**Interfaces:**
- Produces: `ast.PipeStage` gains `NamePos token.Pos`, `ArgsPos token.Pos`; `parsePipe(inner string, base token.Pos)`; `parsePipeStage(seg string, segBase token.Pos)`.

- [ ] **Step 1: Write the failing positions test**

In `parser/pipe_test.go`, add:

```go
func TestPipeStagePositions(t *testing.T) {
	base := token.Pos(100) // inner[k] is at base+k
	// { x |> upper } : "x |> upper", upper at offset 5
	_, stages, err := parsePipe("x |> upper", base)
	if err != nil || len(stages) != 1 {
		t.Fatalf("parse: %v stages=%+v", err, stages)
	}
	if stages[0].NamePos != base+5 {
		t.Errorf("upper NamePos = %d, want %d", stages[0].NamePos, base+5)
	}
	// { x |> truncate(5) } : truncate at 5, '(' at 13, '5' at 14
	_, st2, _ := parsePipe("x |> truncate(5)", base)
	if st2[0].NamePos != base+5 {
		t.Errorf("truncate NamePos = %d, want %d", st2[0].NamePos, base+5)
	}
	if st2[0].ArgsPos != base+14 {
		t.Errorf("truncate ArgsPos = %d, want %d", st2[0].ArgsPos, base+14)
	}
	// whitespace: "x |>  upper |> truncate( 5 )" → upper@6, truncate@15, '5'@25
	_, st3, _ := parsePipe("x |>  upper |> truncate( 5 )", base)
	if st3[0].NamePos != base+6 {
		t.Errorf("ws upper NamePos = %d, want %d", st3[0].NamePos, base+6)
	}
	if st3[1].NamePos != base+15 {
		t.Errorf("ws truncate NamePos = %d, want %d", st3[1].NamePos, base+15)
	}
	if st3[1].ArgsPos != base+25 {
		t.Errorf("ws truncate ArgsPos = %d, want %d", st3[1].ArgsPos, base+25)
	}
}
```

- [ ] **Step 2: Run it to verify it fails to compile**

Run: `go test ./parser/ -run TestPipeStagePositions -count=1`
Expected: build failure — `PipeStage` has no `NamePos`/`ArgsPos`, and `parsePipe` takes one arg.

- [ ] **Step 3: Add the AST fields**

In `ast/ast.go`, in `type PipeStage struct`:

```go
type PipeStage struct {
	Name    string
	Args    string
	HasArgs bool
	NamePos token.Pos // position of the first char of Name in source
	ArgsPos token.Pos // position of the first char of Args (after `(`); NoPos when !HasArgs
}
```

- [ ] **Step 4: Thread positions through `parsePipe` / `parsePipeStage`**

In `parser/pipe.go`, change `parsePipeStage` to take the segment's base position and set `NamePos`/`ArgsPos`:

```go
// parsePipeStage parses one filter segment: `name` or `name(args)`. segBase is
// the source position of seg[0], so NamePos/ArgsPos resolve to real source.
func parsePipeStage(seg string, segBase token.Pos) (ast.PipeStage, error) {
	leadWS := len(seg) - len(strings.TrimLeft(seg, " \t\r\n"))
	namePos := segBase + token.Pos(leadWS) // first non-ws char = the name's first char
	s := strings.TrimSpace(seg)
	if strings.HasSuffix(s, "?") {
		return ast.PipeStage{}, errTryMarker
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
		rawArgs := s[i+1 : end]
		argsLead := len(rawArgs) - len(strings.TrimLeft(rawArgs, " \t\r\n"))
		// s[k] is at namePos+k; args' first char is s[i+1+argsLead].
		argsPos := namePos + token.Pos(i+1+argsLead)
		return ast.PipeStage{Name: name, Args: strings.TrimSpace(rawArgs), HasArgs: true, NamePos: namePos, ArgsPos: argsPos}, nil
	}
	if !isStageName(s) {
		return ast.PipeStage{}, fmt.Errorf("invalid pipeline filter name %q", s)
	}
	return ast.PipeStage{Name: s, NamePos: namePos}, nil
}
```

Change `parsePipe` to take `base` and compute each segment's offset cumulatively (`splitPipe` joins segments with the 2-byte `|>`, so offsets are exact):

```go
// parsePipe splits inner into a seed and its filter stages. base is the source
// position of inner[0]; stage positions are derived from it.
func parsePipe(inner string, base token.Pos) (seed string, stages []ast.PipeStage, err error) {
	segs := splitPipe(inner)
	seed = strings.TrimSpace(segs[0])
	if strings.HasSuffix(seed, "?") {
		return "", nil, errTryMarker
	}
	segOff := len(segs[0]) + 2 // segs[1] starts after segs[0] + "|>"
	for _, seg := range segs[1:] {
		st, e := parsePipeStage(seg, base+token.Pos(segOff))
		if e != nil {
			return "", nil, e
		}
		stages = append(stages, st)
		segOff += len(seg) + 2
	}
	return seed, stages, nil
}
```

- [ ] **Step 5: Update the `markup.go` call site**

In `parser/markup.go`, the `Interp` parse (~line 23): pass `exprPos` (already computed just above as the seed's position):

```go
	seed, stages, perr := parsePipe(inner, exprPos)
```

- [ ] **Step 6: Update the existing `parsePipeStage` test**

In `parser/pipe_test.go`, `TestParsePipeStage` calls `parsePipeStage(c.in)`. Update the call to pass a base and ignore positions (the new `TestPipeStagePositions` covers positions):

```go
		got, err := parsePipeStage(c.in, 0)
		got.NamePos, got.ArgsPos = 0, 0 // positions are covered by TestPipeStagePositions
```

(Apply the same `parsePipeStage(c.in, 0)` + position-zeroing to any error-case calls in that test. Leave the `want` `ast.PipeStage{Name: …}` values unchanged — their positions are already zero.)

- [ ] **Step 7: Update corpus `zeroSpans`**

In `internal/printer/corpus_test.go`, in the `*ast.Interp`/`*ast.ExprAttr` `Stages` handling of `zeroSpans` (where `v.Stages[i].Args = fmtArgs(...)` already runs), zero the new positions:

```go
		for i := range v.Stages {
			v.Stages[i].NamePos = 0
			v.Stages[i].ArgsPos = 0
			if v.Stages[i].HasArgs {
				v.Stages[i].Args = fmtArgs(v.Stages[i].Args)
			}
		}
```

(Apply to BOTH the `Interp` and `ExprAttr` arms — search `zeroSpans` for `v.Stages`.)

- [ ] **Step 8: Run parser + printer tests**

Run: `go test ./parser/ ./internal/printer/ -count=1`
Expected: PASS — `TestPipeStagePositions`, the updated `TestParsePipeStage`, the `splitPipe` tests/fuzz (unchanged), and printer faithfulness/idempotence over the corpus.

- [ ] **Step 9: Commit**

```bash
git add ast/ast.go parser/pipe.go parser/markup.go parser/pipe_test.go internal/printer/corpus_test.go
git commit -m "feat(parser): PipeStage NamePos/ArgsPos source positions

For LSP cursor detection on pipeline filter names and args. parsePipe derives
per-segment offsets from the contiguous split; splitPipe is unchanged.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Task 2: LSP — piped router + go-to-definition

**Files:**
- Create: `internal/lsp/pipe.go`, `internal/lsp/pipe_test.go`
- Modify: `internal/lsp/definition.go`
- Test: `gen/pipe_nav_e2e_test.go` (new — def cases; hover cases added in Task 3)

**Interfaces:**
- Consumes: `ast.PipeStage.NamePos/ArgsPos` (Task 1); `Package.{ExprMap, Info, GSXFset, Fset}`; `innermostIdent`, `exprText`, `hasPipeStages`, `s.locationForPos`.
- Produces: `pipedTarget(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (types.Object, [2]int, bool)`; `walkPipe(skel ast.Expr, n int) ([]*ast.Ident, [][]ast.Expr, ast.Expr, bool)`; `const ctxIdent = "ctx"`.

- [ ] **Step 1: Write the failing `walkPipe` unit test**

Create `internal/lsp/pipe_test.go`:

```go
package lsp

import (
	"go/ast"
	"go/parser"
	"testing"
)

func mustExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	e, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", src, err)
	}
	return e
}

func TestWalkPipe(t *testing.T) {
	// non-ctx, bare + args: Upper(Truncate((seed), 5)) — stage0=Truncate, stage1=Upper.
	sel, args, seed, ok := walkPipe(mustExpr(t, `p.Upper(p.Truncate((seed), 5))`), 2)
	if !ok {
		t.Fatal("walkPipe ok=false")
	}
	if sel[0].Name != "Truncate" || sel[1].Name != "Upper" {
		t.Fatalf("sels = %q, %q; want Truncate, Upper", sel[0].Name, sel[1].Name)
	}
	if len(args[0]) != 1 || len(args[1]) != 0 {
		t.Fatalf("args = %v; want stage0 one arg, stage1 none", args)
	}
	if id, _ := seed.(*ast.Ident); id == nil || id.Name != "seed" {
		t.Fatalf("seed = %#v; want ident `seed`", seed)
	}

	// ctx-injected: URLFor(ctx, (seed), "id", x) — subject at args[1], stage args after.
	sel2, args2, seed2, ok := walkPipe(mustExpr(t, `p.URLFor(ctx, (seed), "id", x)`), 1)
	if !ok || sel2[0].Name != "URLFor" {
		t.Fatalf("ctx walk: ok=%v sel=%v", ok, sel2)
	}
	if len(args2[0]) != 2 { // "id", x — the user stage args, after the subject
		t.Fatalf("ctx stage args = %v; want 2", args2[0])
	}
	if id, _ := seed2.(*ast.Ident); id == nil || id.Name != "seed" {
		t.Fatalf("ctx seed = %#v; want ident `seed`", seed2)
	}

	// shape mismatch → ok=false, no panic.
	if _, _, _, ok := walkPipe(mustExpr(t, `1 + 2`), 1); ok {
		t.Fatal("non-call walk should be ok=false")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/lsp/ -run TestWalkPipe -count=1`
Expected: build failure — `walkPipe` / `ctxIdent` undefined.

- [ ] **Step 3: Create the router**

Create `internal/lsp/pipe.go`:

```go
package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
)

// ctxIdent is the reserved ambient render-context identifier the codegen lowering
// injects as the first argument of a ctx-taking filter. It MUST match codegen's
// pipeCtxIdent ("ctx"); the ctx-injected pipeline e2e test guards this.
const ctxIdent = "ctx"

// walkPipe peels the N seed-first filter layers of a lowered pipeline expression.
// The lowering shape is `Func([ctx,] subject, args…)` nested via the subject, so
// for stage i it returns the filter's Sel ident and its user stage args, and at
// the bottom the (unwrapped) seed expression. ok=false on any unexpected shape.
func walkPipe(skel ast.Expr, n int) (selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
	selSel = make([]*ast.Ident, n)
	selArgs = make([][]ast.Expr, n)
	cur := skel
	for i := n - 1; i >= 0; i-- {
		call, isCall := cur.(*ast.CallExpr)
		if !isCall {
			return nil, nil, nil, false
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || len(call.Args) == 0 {
			return nil, nil, nil, false
		}
		selSel[i] = sel.Sel
		subjIdx := 0
		if id, isID := call.Args[0].(*ast.Ident); isID && id.Name == ctxIdent {
			subjIdx = 1 // ctx injected at args[0]
		}
		if subjIdx >= len(call.Args) {
			return nil, nil, nil, false
		}
		selArgs[i] = call.Args[subjIdx+1:]
		cur = call.Args[subjIdx]
	}
	return selSel, selArgs, unwrapParens(cur), true
}

func unwrapParens(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func pipeStages(node gsxast.Node) []gsxast.PipeStage {
	switch e := node.(type) {
	case *gsxast.Interp:
		return e.Stages
	case *gsxast.ExprAttr:
		return e.Stages
	}
	return nil
}

func useObj(pkg *Package, id *ast.Ident) types.Object {
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	return obj
}

func identInArgs(args []ast.Expr, pos token.Pos) *ast.Ident {
	for _, a := range args {
		if a.Pos() <= pos && pos < a.End() {
			return innermostIdent(a, pos)
		}
	}
	return nil
}

// pipedTarget resolves the go/types object under the cursor inside a piped node,
// plus the .gsx byte span of the hovered region (for a Range). ok=false (→ null)
// when the cursor is on no resolvable region or the lowered shape is unexpected.
// It never panics — every assertion is guarded.
func pipedTarget(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (types.Object, [2]int, bool) {
	stages := pipeStages(node)
	skel := pkg.ExprMap[node]
	if skel == nil || len(stages) == 0 || pkg.Info == nil || pkg.GSXFset == nil {
		return nil, [2]int{}, false
	}
	selSel, selArgs, seedExpr, ok := walkPipe(skel, len(stages))
	if !ok {
		return nil, [2]int{}, false
	}

	// seed region: [seedStart, seedStart+len(seedText)); byte-identical to seedExpr.
	seedStart := pkg.GSXFset.Position(exprPos).Offset
	seedText := exprText(node)
	if seedExpr != nil && off >= seedStart && off < seedStart+len(seedText) {
		if id := innermostIdent(seedExpr, seedExpr.Pos()+token.Pos(off-seedStart)); id != nil {
			if obj := useObj(pkg, id); obj != nil {
				start := seedStart + int(id.Pos()-seedExpr.Pos())
				return obj, [2]int{start, start + len(id.Name)}, true
			}
		}
		return nil, [2]int{}, false
	}

	for i, st := range stages {
		// filter name region.
		if st.NamePos.IsValid() {
			nameStart := pkg.GSXFset.Position(st.NamePos).Offset
			if off >= nameStart && off < nameStart+len(st.Name) {
				if selSel[i] != nil {
					if obj := useObj(pkg, selSel[i]); obj != nil {
						return obj, [2]int{nameStart, nameStart + len(st.Name)}, true
					}
				}
				return nil, [2]int{}, false
			}
		}
		// filter args region: byte-identical to the skeleton args.
		if st.HasArgs && st.ArgsPos.IsValid() && len(selArgs[i]) > 0 {
			argsStart := pkg.GSXFset.Position(st.ArgsPos).Offset
			if off >= argsStart && off < argsStart+len(st.Args) {
				base := selArgs[i][0].Pos()
				if id := identInArgs(selArgs[i], base+token.Pos(off-argsStart)); id != nil {
					if obj := useObj(pkg, id); obj != nil {
						start := argsStart + int(id.Pos()-base)
						return obj, [2]int{start, start + len(id.Name)}, true
					}
				}
				return nil, [2]int{}, false
			}
		}
	}
	return nil, [2]int{}, false
}
```

- [ ] **Step 4: Run the `walkPipe` unit test**

Run: `go test ./internal/lsp/ -run TestWalkPipe -count=1`
Expected: PASS.

- [ ] **Step 5: Wire `pipedTarget` into definition**

In `internal/lsp/definition.go`, replace the `hasPipeStages(node)` guard body:

```go
	if hasPipeStages(node) {
		if obj, _, ok := pipedTarget(pkg, node, exprPos, off); ok && obj.Pos().IsValid() {
			dp := pkg.Fset.Position(obj.Pos())
			if dp.Filename != "" && !strings.HasSuffix(dp.Filename, ".x.go") {
				return s.reply(f.ID, s.locationForPos(dp))
			}
		}
		return s.reply(f.ID, nil)
	}
```

(`strings` is already imported in `definition.go`.)

- [ ] **Step 6: Write the failing definition e2e tests**

Create `gen/pipe_nav_e2e_test.go`:

```go
package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// pipeNavModule writes a temp module with a local `greeting` and a .gsx using
// std `truncate`/`upper`: { greeting() |> truncate(5) |> upper }. Returns dir + src.
func pipeNavModule(t *testing.T) (dir, cardSrc string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/p\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("u.go", "package p\n\nfunc Greeting(name string) string { return name }\n")
	cardSrc = "package p\n\ncomponent Card(name string) {\n\t<div>{ Greeting(name) |> truncate(5) |> upper }</div>\n}\n"
	must("card.gsx", cardSrc)
	return dir, cardSrc
}

// pipeDefAt opens card.gsx and returns the definition result for a cursor at the
// first occurrence of needle + off (or nil for null).
func pipeDefAt(t *testing.T, dir, src, needle string, off int) *lsp.Location {
	t.Helper()
	uri := "file://" + filepath.Join(dir, "card.gsx")
	var line, ch int
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			line, ch = i, c+off
			break
		}
	}
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": src}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": ch}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if strings.Contains(out.String(), ".x.go") {
		t.Fatalf("piped definition leaked a generated path; out:\n%s", out.String())
	}
	return definitionResult(t, out.String(), 2) // helper from definition_e2e_test.go
}

func TestPipeDefSeed(t *testing.T) {
	dir, src := pipeNavModule(t)
	loc := pipeDefAt(t, dir, src, "Greeting(name)", 0) // on `Greeting` (the seed call)
	if loc == nil || !strings.HasSuffix(loc.URI, "u.go") {
		t.Fatalf("seed def → %v, want u.go", loc)
	}
}

func TestPipeDefFilter(t *testing.T) {
	dir, src := pipeNavModule(t)
	loc := pipeDefAt(t, dir, src, "|> upper", len("|> ")) // on `upper`
	if loc == nil || !strings.HasSuffix(loc.URI, "std.go") {
		t.Fatalf("filter def → %v, want std/std.go", loc)
	}
}

func TestPipeDefArg(t *testing.T) {
	dir, _ := pipeNavModule(t)
	// { name |> truncate(n) } with a param n.
	src := "package p\n\ncomponent Card(name string, n int) {\n\t<div>{ name |> truncate(n) }</div>\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "card.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	loc := pipeDefAt(t, dir, src, "truncate(n)", len("truncate(")) // on `n`
	if loc == nil || !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("arg def → %v, want card.gsx (the n param)", loc)
	}
}

func TestPipeDefOnOperatorNull(t *testing.T) {
	dir, src := pipeNavModule(t)
	loc := pipeDefAt(t, dir, src, "|> upper", 0) // on the `|` of `|>`
	if loc != nil {
		t.Fatalf("def on `|>` must be null, got %v", loc)
	}
}
```

- [ ] **Step 7: Run the def e2e + lsp unit + regression**

Run: `go test ./internal/lsp/ ./gen/ -run 'TestWalkPipe|TestPipeDef|TestDefinition' -count=1`
Expected: PASS — `TestWalkPipe`, the four `TestPipeDef*`, and the existing `TestDefinition*` (non-piped regression).

- [ ] **Step 8: Commit**

```bash
git add internal/lsp/pipe.go internal/lsp/pipe_test.go internal/lsp/definition.go gen/pipe_nav_e2e_test.go
git commit -m "feat(lsp): go-to-definition inside pipelines (seed/filter/args)

pipedTarget walks the seed-first lowered call chain, routes the cursor by region,
and resolves via Info.Uses; replaces the hasPipeStages→null guard on definition.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Task 3: LSP — hover inside pipelines

**Files:**
- Modify: `internal/lsp/hover.go`
- Test: `gen/pipe_nav_e2e_test.go` (add hover cases)

**Interfaces:**
- Consumes: `pipedTarget` (Task 2); `markdownGo`, `rangeForSpan`, `qualifierFor`, `Hover` (hover slice).

- [ ] **Step 1: Write the failing hover e2e tests**

Append to `gen/pipe_nav_e2e_test.go`:

```go
// pipeHoverAt mirrors pipeDefAt for textDocument/hover.
func pipeHoverAt(t *testing.T, dir, src, needle string, off int) *lsp.Hover {
	t.Helper()
	uri := "file://" + filepath.Join(dir, "card.gsx")
	var line, ch int
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			line, ch = i, c+off
			break
		}
	}
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": src}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": ch}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	marker := `"id":2,`
	for _, part := range strings.Split(out.String(), "Content-Length:") {
		i := strings.Index(part, "\r\n\r\n")
		if i < 0 {
			continue
		}
		body := part[i+4:]
		if !strings.Contains(body, marker) {
			continue
		}
		var resp struct {
			Result *lsp.Hover `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode hover: %v\nbody=%q", err, body)
		}
		return resp.Result
	}
	t.Fatalf("no hover response (id 2) in:\n%s", out.String())
	return nil
}

func TestPipeHoverFilter(t *testing.T) {
	dir, src := pipeNavModule(t)
	h := pipeHoverAt(t, dir, src, "|> upper", len("|> ")) // on `upper`
	if h == nil || !strings.Contains(h.Contents.Value, "func Upper(") {
		t.Fatalf("hover on filter `upper` → %+v, want func Upper(...)", h)
	}
}

func TestPipeHoverSeed(t *testing.T) {
	dir, src := pipeNavModule(t)
	h := pipeHoverAt(t, dir, src, "Greeting(name)", 0) // on `Greeting`
	if h == nil || !strings.Contains(h.Contents.Value, "func Greeting(name string) string") {
		t.Fatalf("hover on seed `Greeting` → %+v", h)
	}
}

func TestPipeHoverArg(t *testing.T) {
	dir, _ := pipeNavModule(t)
	src := "package p\n\ncomponent Card(name string, n int) {\n\t<div>{ name |> truncate(n) }</div>\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "card.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	h := pipeHoverAt(t, dir, src, "truncate(n)", len("truncate(")) // on `n`
	if h == nil || !strings.Contains(h.Contents.Value, "var n int") {
		t.Fatalf("hover on arg `n` → %+v, want var n int", h)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./gen/ -run TestPipeHover -count=1`
Expected: FAIL — hover returns null on piped nodes (the guard still bails).

- [ ] **Step 3: Wire `pipedTarget` into hover**

In `internal/lsp/hover.go`, replace the `hasPipeStages(node)` guard body (which currently returns null):

```go
	if hasPipeStages(node) {
		if obj, span, ok := pipedTarget(pkg, node, exprPos, off); ok {
			rng := rangeForSpan(text, span[0], span[1], s.enc)
			return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qualifierFor(pkg))), Range: &rng})
		}
		return s.reply(f.ID, nil)
	}
```

(`text`, `exprPos`, `off` are in scope at this point in `handleHover`; `go/types` is already imported.)

- [ ] **Step 4: Run the hover e2e + full hover suite**

Run: `go test ./gen/ -run 'TestPipeHover|TestHover' -count=1`
Expected: PASS — the three `TestPipeHover*` plus the existing `TestHover*` (non-piped regression).

- [ ] **Step 5: Run the full suite + build**

Run: `go test ./... -count=1` then `go build ./...`
Expected: PASS / clean.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/hover.go gen/pipe_nav_e2e_test.go
git commit -m "feat(lsp): hover inside pipelines (seed/filter/args)

handleHover reuses pipedTarget to show the signature of the seed, a filter func,
or a symbol inside filter args; replaces the hasPipeStages→null guard.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Self-Review notes (addressed)

- **Spec coverage:** parser positions (§3.1) ↔ Task 1; the walk (§3.2) ↔ `walkPipe` (Task 2) + `TestWalkPipe` (incl. ctx); region detection (§3.3) ↔ `pipedTarget` (Task 2); ctx-ident decoupling (§3.4) ↔ local `ctxIdent` + the walk test; def + hover share the router (§3) ↔ Tasks 2 & 3; defensive/edges (§4) ↔ guarded asserts + `TestPipeDefOnOperatorNull` + `.x.go` leak check; testing (§5) ↔ parser unit + corpus faithfulness + def/hover e2e + walk unit.
- **ctx end-to-end note:** the *fully end-to-end* ctx-injected case (`|> url` → `structpages.URLFor`) needs the LSP to resolve a ctx filter, which requires the cache-manifest/`gsx.toml` filter wiring (separate, not yet merged). Here the **ctx walk is covered by `TestWalkPipe`** (the `URLFor(ctx, (seed), …)` case), and the std e2e covers seed/filter/args end-to-end. This is called out so the reviewer does not expect a std-only ctx e2e (std has no ctx filter).
- **Type consistency:** `pipedTarget(...) (types.Object, [2]int, bool)` and `walkPipe(skel ast.Expr, n int) ([]*ast.Ident, [][]ast.Expr, ast.Expr, bool)` are defined in Task 2 and used by Tasks 2 (def) and 3 (hover) identically. `ast.PipeStage.NamePos/ArgsPos` (Task 1) are read in Task 2. `exprText`/`innermostIdent`/`markdownGo`/`rangeForSpan`/`qualifierFor` are existing helpers.
- **No placeholders:** every step carries complete code.
