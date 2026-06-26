# Control-Flow Go-To-Definition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make go-to-definition work for identifiers inside `{ for … }` clauses, `{ if … }` conds, and `{{ … }}` Go blocks, resolving through the in-memory skeleton (never `.x.go`).

**Architecture:** Mirror the existing `Interp` bridge. Add gsx-source positions for the clause/cond/code text; record each one's skeleton byte-offset during emission, convert to a skeleton `token.Pos` after parse, and store `CtrlMap[gsxNode] = {clauseStart token.Pos, containing skeleton goast.Node}` parallel to `ExprMap`. The LSP's `exprNodeAtOffset` recognizes control-flow nodes and `handleDefinition` bridges via `CtrlMap` → `innermostIdent` → `Info.Uses/Defs`.

**Tech Stack:** Go, `go/types`, `go/token`, `go/ast`; gsx `parser`, `internal/codegen`, `internal/lsp`, `gen/lsp.go`.

## Global Constraints

- Go module `github.com/gsxhq/gsx`; Go `1.26.1`.
- `.x.go`-independent: resolution uses the in-memory skeleton's `Info` + `//line`-mapped positions; the existing `.x.go`-suffix guard (`definition.go`) stays and rejects generated-code positions.
- Byte-faithful: gsx `Clause`/`Cond`/`Code` text is byte-identical to its skeleton emission (verbatim) — the relative-offset bridge depends on it.
- Additive to generated `.x.go`: the Phase-0 corpus equivalence gate (Files) stays green. The new clause `//line` may shift a `diagnostics.golden` column for a type error *in a clause* — regenerate + verify if so.
- Every parser/codegen change ships corpus + unit coverage ([[gsx-syntax-change-test-coverage]]).
- The `{{ }}` node type is `gsxast.GoBlock` (field `Code`), NOT `Code`.

---

## File Structure

- **Modify `ast/ast.go`** — add `ClausePos`/`CondPos`/`CodePos token.Pos` to `ForMarkup`/`IfMarkup`/`GoBlock` (Task 1).
- **Modify `parser/markup.go`** — set the three positions in `parseForMarkup`/`parseIfTail`/`parseGoBlock` (Task 1).
- **Modify `internal/printer/corpus_test.go`** — `zeroSpans` arms for the three new fields (Task 1).
- **Modify `internal/codegen/analyze.go`** — `emitProbes` emits a compensated clause `//line` and records skeleton byte-offsets; `buildSkeleton` returns the offset map; `harvest` builds `CtrlMap` (Tasks 2, 3).
- **Modify `internal/codegen/batch.go`, `internal/codegen/module.go`, `internal/codegen/module_importer.go`** — thread the offset map + `CtrlMap` field through `PackageResult` and both analysis paths (Tasks 2, 3).
- **Modify `internal/lsp/analysis.go`, `gen/lsp.go`** — add `CtrlMap` to `lsp.Package` + `adaptPackageResult` (Task 3).
- **Modify `internal/lsp/mapping.go`** — broaden `innermostIdent` to any `ast.Node` (Task 4).
- **Modify `internal/lsp/definition.go`** — `exprNodeAtOffset` recognizes control-flow nodes; `handleDefinition` bridges via `CtrlMap` (Task 4).
- **Tests:** `parser/position_test.go`, `internal/codegen/*_test.go`, `internal/lsp/definition_test.go`, `gen/*_e2e_test.go`.

---

## Task 1: AST positions + parser + faithfulness normalization

**Files:**
- Modify: `ast/ast.go` (`ForMarkup` ~311, `IfMarkup` ~299, `GoBlock` ~290), `parser/markup.go` (`parseForMarkup` ~238, `parseIfTail` ~286, `parseGoBlock` ~130), `internal/printer/corpus_test.go` (`zeroSpans` ~99)
- Test: `parser/position_test.go`

**Interfaces:**
- Produces: `ForMarkup.ClausePos`, `IfMarkup.CondPos`, `GoBlock.CodePos` (`token.Pos`) — the first non-whitespace char of the clause/cond/code text in source (mirroring `Interp.ExprPos`; `token.NoPos` if unavailable).

- [ ] **Step 1: Write the failing test**

```go
// parser/position_test.go
func TestControlFlowClausePositions(t *testing.T) {
	src := "package x\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n\t{ if props.Featured { <b>f</b> } }\n\t{{ total := len(props.Posts) }}\n}\n"
	fset := token.NewFileSet()
	f, errs := gsxparser.ParseFileWithClassifier(fset, "p.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	var forM *gsxast.ForMarkup
	var ifM *gsxast.IfMarkup
	var gb *gsxast.GoBlock
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		switch t := n.(type) {
		case *gsxast.ForMarkup:
			forM = t
		case *gsxast.IfMarkup:
			ifM = t
		case *gsxast.GoBlock:
			gb = t
		}
		return true
	})
	// Each position must point at the first char of the (trimmed) clause/cond/code.
	check := func(name string, pos token.Pos, text string) {
		if !pos.IsValid() {
			t.Fatalf("%s position invalid", name)
		}
		off := fset.Position(pos).Offset
		if got := src[off : off+len(text)]; got != text {
			t.Errorf("%s: src at pos = %q, want %q", name, got, text)
		}
	}
	check("ForMarkup.ClausePos", forM.ClausePos, "_, post := range props.Posts")
	check("IfMarkup.CondPos", ifM.CondPos, "props.Featured")
	check("GoBlock.CodePos", gb.CodePos, "total := len(props.Posts)")
}
```

(Confirm the gsx parser import alias used in `parser/position_test.go`; reuse the existing one. `gsxast` is `github.com/gsxhq/gsx/ast`.)

- [ ] **Step 2: Run to verify fail**

Run: `go test ./parser/ -run TestControlFlowClausePositions -v`
Expected: FAIL — `forM.ClausePos undefined`.

- [ ] **Step 3: Implement**

In `ast/ast.go`, add the fields (keep the `span` embedding):
```go
type ForMarkup struct {
	span
	Clause    string
	ClausePos token.Pos // first char of Clause text in source (NoPos if unavailable)
	Body      []Markup
}
type IfMarkup struct {
	span
	Cond    string
	CondPos token.Pos // first char of Cond text in source
	Then    []Markup
	Else    []Markup
}
type GoBlock struct {
	span
	Code    string
	CodePos token.Pos // first char of Code text in source
}
```

In `parser/markup.go` `parseForMarkup`, after `clause := strings.TrimSpace(p.src[clauseStart:braceOff])`:
```go
rawClause := p.src[clauseStart:braceOff]
lead := len(rawClause) - len(strings.TrimLeft(rawClause, " \t\r\n"))
clausePos := p.posAt(clauseStart + lead)
// ...
n := &ast.ForMarkup{Clause: clause, ClausePos: clausePos, Body: body}
```

In `parseIfTail`, after `cond := strings.TrimSpace(p.src[condStart:braceOff])`:
```go
rawCond := p.src[condStart:braceOff]
lead := len(rawCond) - len(strings.TrimLeft(rawCond, " \t\r\n"))
condPos := p.posAt(condStart + lead)
// ...
n := &ast.IfMarkup{Cond: cond, CondPos: condPos, Then: body}
```

In `parseGoBlock`, after `code := strings.TrimSpace(p.src[p.i+2 : innerEnd])`:
```go
rawCode := p.src[p.i+2 : innerEnd]
lead := len(rawCode) - len(strings.TrimLeft(rawCode, " \t\r\n"))
codePos := p.posAt(p.i + 2 + lead)
// ...
n := &ast.GoBlock{Code: code, CodePos: codePos}
```

In `internal/printer/corpus_test.go` `zeroSpans`, add arms to the type switch:
```go
case *ast.ForMarkup:
	v.ClausePos = 0
case *ast.IfMarkup:
	v.CondPos = 0
case *ast.GoBlock:
	v.CodePos = 0
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./parser/ -run TestControlFlowClausePositions -v && go test ./internal/printer/ -count=1`
Expected: PASS (parser test passes; printer faithfulness stays green with the new fields zeroed).

- [ ] **Step 5: Commit**

```bash
git add ast/ast.go parser/markup.go parser/position_test.go internal/printer/corpus_test.go
git commit -m "feat(ast,parser): clause/cond/code positions for ForMarkup/IfMarkup/GoBlock"
```

---

## Task 2: Skeleton records clause byte-offsets + emits clause //line

**Files:**
- Modify: `internal/codegen/analyze.go` (`emitProbes` ForMarkup/IfMarkup/GoBlock cases ~856-892; `buildSkeleton` ~370 signature/return)
- Modify call sites that consume `buildSkeleton`'s return: `internal/codegen/batch.go` (~221), `internal/codegen/module_importer.go` (the `buildSkeleton` call in `analyze`)
- Test: `internal/codegen/controlflow_test.go` (create)

**Interfaces:**
- Consumes: `emitSkeletonLine`/`emitSkeletonComponentNameLine` patterns (existing `//line` emitters), `gsxast.ForMarkup.ClausePos` etc. (Task 1).
- Produces: `buildSkeleton` gains a 5th return value `ctrlOff map[gsxast.Node]int` — skeleton byte-offset of each control-flow node's clause/cond/code text start. New signature:
  `func buildSkeleton(...) (string, []*gsxast.Component, []importSpec, map[gsxast.Node]int, error)`.
  `emitProbes` gains a `ctrlOff map[gsxast.Node]int` parameter it writes into.

**Background:** `emitProbes` recurses; `sb` is the shared builder, so `sb.Len()` is the current file-level byte offset. The clause starts at `sb.Len()+len("for ")` after a `//line` is emitted; cond at `+len("if ")`; code at `sb.Len()` (no prefix). A compensated `//line` before each clause maps clause positions back to `.gsx` (so go-to-def TO a loop-var binding works), mirroring the `_gsxuse` `//line` compensation already in `emitProbes`.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"

	"go/token"
)

func TestBuildSkeletonRecordsCtrlOffsets(t *testing.T) {
	src := "package v\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n}\n"
	fset := token.NewFileSet()
	file, errs := gsxparser.ParseFileWithClassifier(fset, "p.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	// minimal props/byo for buildSkeleton; an empty table/maps is fine for a no-import component.
	table, _ := loadFilterTable(t.TempDir())
	pf, np, byo, err := componentPropFieldsFor(t.TempDir(), map[string]*gsxast.File{"p.gsx": file})
	if err != nil {
		t.Fatalf("propFields: %v", err)
	}
	skel, _, _, ctrlOff, err := buildSkeleton(file, table, pf, np, byo, nil, fset)
	if err != nil {
		t.Fatalf("buildSkeleton: %v", err)
	}
	// Find the ForMarkup node and assert its recorded offset lands on the clause text.
	var forM *gsxast.ForMarkup
	gsxast.Inspect(file, func(n gsxast.Node) bool {
		if f, ok := n.(*gsxast.ForMarkup); ok {
			forM = f
		}
		return true
	})
	off, ok := ctrlOff[forM]
	if !ok {
		t.Fatalf("ctrlOff has no entry for the ForMarkup")
	}
	if got := skel[off : off+len(forM.Clause)]; got != forM.Clause {
		t.Errorf("skeleton at ctrlOff = %q, want clause %q (byte-faithful)", got, forM.Clause)
	}
	// A compensated //line precedes the `for` in the skeleton.
	forIdx := strings.Index(skel, "for "+forM.Clause)
	if forIdx < 0 {
		t.Fatalf("skeleton missing `for <clause>`")
	}
	pre := skel[:forIdx]
	if li := strings.LastIndex(pre, "//line "); li < 0 || strings.Contains(pre[li:], "\n}") {
		t.Errorf("expected a //line directive immediately before the for clause")
	}
}
```

(If `componentPropFieldsFor`/`loadFilterTable` signatures differ, adjust per their definitions in `analyze.go`/`filters.go`.)

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/codegen/ -run TestBuildSkeletonRecordsCtrlOffsets -v`
Expected: FAIL — `buildSkeleton` returns 4 values, not 5 (compile error / arity).

- [ ] **Step 3: Implement**

Thread `ctrlOff map[gsxast.Node]int` through `emitProbes` (add the param to its signature and every recursive call). In the three control-flow cases, emit a compensated `//line` and record the offset. Add a small helper for the clause `//line` (compensated for the keyword prefix), near `emitSkeletonLine`:

```go
// emitSkeletonClauseLine emits a //line anchored so the clause/cond/code text
// (which the skeleton emits verbatim starting `prefixLen` bytes into the line)
// maps to its .gsx position pos. col = clauseCol - prefixLen + 1.
func emitSkeletonClauseLine(sb *strings.Builder, fset *token.FileSet, pos token.Pos, prefixLen int) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	col := p.Column - prefixLen + 1
	if col < 1 {
		col = 1
	}
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}
```

Update the cases (replace the existing three cases at ~856-892):
```go
case *gsxast.ForMarkup:
	emitSkeletonClauseLine(sb, fset, t.ClausePos, len("for ")) // 4
	ctrlOff[t] = sb.Len() + len("for ")
	fmt.Fprintf(sb, "for %s {\n", t.Clause)
	if err := emitProbes(sb, t.Body, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff); err != nil {
		return err
	}
	sb.WriteString("}\n")
case *gsxast.IfMarkup:
	emitSkeletonClauseLine(sb, fset, t.CondPos, len("if ")) // 3
	ctrlOff[t] = sb.Len() + len("if ")
	fmt.Fprintf(sb, "if %s {\n", t.Cond)
	if err := emitProbes(sb, t.Then, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff); err != nil {
		return err
	}
	sb.WriteString("}")
	if t.Else != nil {
		sb.WriteString(" else {\n")
		if err := emitProbes(sb, t.Else, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff); err != nil {
			return err
		}
		sb.WriteString("}")
	}
	sb.WriteString("\n")
case *gsxast.GoBlock:
	emitSkeletonClauseLine(sb, fset, t.CodePos, 0)
	ctrlOff[t] = sb.Len()
	sb.WriteString(t.Code)
	sb.WriteString("\n")
```

In `buildSkeleton`: create `ctrlOff := map[gsxast.Node]int{}`, pass it into the top-level `emitProbes` call(s), and add it to the return tuple. Update the signature to `(string, []*gsxast.Component, []importSpec, map[gsxast.Node]int, error)` and every `return` accordingly.

Update the two `buildSkeleton` call sites to receive the extra value:
- `batch.go` ~221: `skel, comps, imps, ctrlOff, err := buildSkeleton(...)` — accumulate `ctrlOff` per skeleton file into a per-dir map keyed by the absolute `.x.go` path (alongside `skelCompsByPath`), e.g. `ctrlOffByXGo[absXpath] = ctrlOff`.
- `module_importer.go` `analyze`: `skel, _, _, ctrlOff, berr := buildSkeleton(...)` — collect `ctrlOff` per file similarly for use in Task 3.

(Reset note: the existing control-flow emission established the next statement's baseline implicitly; the recursive `emitProbes` calls inside the body re-anchor via their own `//line`s for interps. No explicit reset needed because the body interps emit their own `//line`; but if a body interp lacks one, it would inherit the clause `//line`. Confirm existing interp emission always `//line`s; it does for `ExprPos`-valid interps.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/codegen/ -run TestBuildSkeletonRecordsCtrlOffsets -v && go build ./...`
Expected: PASS + build clean (all `buildSkeleton` callers updated).

- [ ] **Step 4b: Corpus gate**

Run: `go test ./internal/codegen/ ./internal/corpus/ -count=1`
Expected: PASS. If a `diagnostics.golden` shifts (a type error on a clause line now maps to the clause column), verify the new column is the clause and regenerate with `go test ./internal/corpus/ -update`; document which goldens changed. Generated `.x.go` (Files) must be unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/batch.go internal/codegen/module_importer.go internal/codegen/controlflow_test.go
git commit -m "feat(codegen): record control-flow clause byte-offsets + emit clause //line"
```

---

## Task 3: Build CtrlMap + thread through to lsp.Package

**Files:**
- Modify: `internal/codegen/analyze.go` (`harvest` or a new `buildCtrlMap`), `internal/codegen/batch.go` (`PackageResult` struct + population), `internal/codegen/module.go`/`module_importer.go` (population), `internal/lsp/analysis.go` (`lsp.Package`), `gen/lsp.go` (`adaptPackageResult`)
- Test: `internal/codegen/controlflow_test.go` (extend)

**Interfaces:**
- Produces:
  - `type ctrlRef struct { ClauseStart token.Pos; Node goast.Node }`
  - `PackageResult.CtrlMap map[gsxast.Node]ctrlRef` and `lsp.Package.CtrlMap map[gsxast.Node]CtrlRef` (lsp mirror type).
  - `func buildCtrlMap(f *goast.File, fset *token.FileSet, ctrlOff map[gsxast.Node]int, clauseText map[gsxast.Node]string) map[gsxast.Node]ctrlRef` — converts byte offsets to `token.Pos` (`fset.File(f.Pos()).Pos(off)`) and finds the smallest `f` node whose `[Pos,End)` contains `[clauseStart, clauseStart+len(text))`.

**Background:** `clauseText[node]` is the node's `Clause`/`Cond`/`Code` (so the region length is known). The "smallest containing node" scopes `innermostIdent` (for/if → the ForStmt/IfStmt; a multi-statement GoBlock → the enclosing block/func body, which still resolves the right ident at the precise position).

- [ ] **Step 1: Write the failing test**

```go
func TestModulePackageBuildsCtrlMap(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "page.gsx", "package page\n\ntype Props struct{ Posts []Post }\ntype Post struct{ Title string }\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	var forM *gsxast.ForMarkup
	for _, gf := range pr.GSXFiles {
		gsxast.Inspect(gf, func(n gsxast.Node) bool {
			if f, ok := n.(*gsxast.ForMarkup); ok {
				forM = f
			}
			return true
		})
	}
	cr, ok := pr.CtrlMap[forM]
	if !ok || !cr.ClauseStart.IsValid() || cr.Node == nil {
		t.Fatalf("CtrlMap missing/invalid for ForMarkup: %+v ok=%v", cr, ok)
	}
	// ClauseStart maps (via //line) back to the .gsx clause.
	dp := pr.Fset.Position(cr.ClauseStart)
	if !strings.HasSuffix(dp.Filename, ".gsx") {
		t.Errorf("ClauseStart maps to %s, want a .gsx position", dp.Filename)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/codegen/ -run TestModulePackageBuildsCtrlMap -v`
Expected: FAIL — `pr.CtrlMap undefined`.

- [ ] **Step 3: Implement**

Add to `internal/codegen/batch.go` `PackageResult` (beside `ExprMap`):
```go
CtrlMap map[gsxast.Node]ctrlRef
```
Define `ctrlRef` + `buildCtrlMap` in `analyze.go`:
```go
type ctrlRef struct {
	ClauseStart token.Pos
	Node        goast.Node
}

// buildCtrlMap converts each control-flow node's skeleton byte-offset to a
// token.Pos and finds the smallest skeleton node spanning its clause/cond/code
// region, so the LSP can bridge a cursor into the clause and innermostIdent it.
func buildCtrlMap(f *goast.File, fset *token.FileSet, ctrlOff map[gsxast.Node]int, clauseText map[gsxast.Node]string) map[gsxast.Node]ctrlRef {
	tf := fset.File(f.Pos())
	if tf == nil {
		return nil
	}
	out := map[gsxast.Node]ctrlRef{}
	for node, off := range ctrlOff {
		start := tf.Pos(off)
		end := tf.Pos(off + len(clauseText[node]))
		var smallest goast.Node
		goast.Inspect(f, func(n goast.Node) bool {
			if n == nil {
				return false
			}
			if n.Pos() <= start && end <= n.End() {
				smallest = n // tighter container; keep descending
				return true
			}
			return false
		})
		out[node] = ctrlRef{ClauseStart: start, Node: smallest}
	}
	return out
}
```

`clauseText[node]` is built from the gsx node's text. Add a helper `ctrlClauseText(n gsxast.Node) string` returning `Clause`/`Cond`/`Code` per type, and build the `clauseText` map from `ctrlOff`'s keys.

Populate `CtrlMap`:
- **Module path** (`module.go`/`module_importer.go`): in `Package`, after harvest, for each skeleton file `gf` with its `ctrlOff` (collected in Task 2), call `buildCtrlMap(gf, fset, ctrlOff, clauseText)` and merge into `res.CtrlMap`.
- **Batch path** (`batch.go`): same, using `pkg.Fset` and `pkg.Syntax` files with `ctrlOffByXGo`.

Add `CtrlMap` to `lsp.Package` (`internal/lsp/analysis.go`) with an `lsp.CtrlRef` mirror type:
```go
type CtrlRef struct {
	ClauseStart token.Pos
	Node        goast.Node // skeleton node scoping innermostIdent
}
// in Package:
CtrlMap map[gsxast.Node]CtrlRef
```
Thread it in `adaptPackageResult` (`gen/lsp.go`):
```go
ctrl := make(map[gsxast.Node]lsp.CtrlRef, len(pr.CtrlMap))
for k, v := range pr.CtrlMap {
	ctrl[k] = lsp.CtrlRef{ClauseStart: v.ClauseStart, Node: v.Node}
}
// add to the returned &lsp.Package{...}: CtrlMap: ctrl,
```
(`gen/lsp.go` already imports `gsxast` and `codegen`; add `goast "go/ast"` to `internal/lsp/analysis.go` if not present — it imports `go/ast` as `ast`. Use `ast.Node` there to match the package's existing alias.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/codegen/ -run TestModulePackageBuildsCtrlMap -v && go build ./...`
Expected: PASS + build clean.

- [ ] **Step 4b: Regression**

Run: `go test ./internal/codegen/ ./internal/lsp/ ./gen/ -count=1`
Expected: PASS (no existing behavior changed).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/batch.go internal/codegen/module.go internal/codegen/module_importer.go internal/lsp/analysis.go gen/lsp.go internal/codegen/controlflow_test.go
git commit -m "feat(codegen,lsp): CtrlMap (control-flow node -> skeleton position) threaded to lsp.Package"
```

---

## Task 4: LSP go-to-def in control-flow regions

**Files:**
- Modify: `internal/lsp/mapping.go` (`innermostIdent`), `internal/lsp/definition.go` (`exprNodeAtOffset`, `handleDefinition`)
- Test: `internal/lsp/definition_test.go`

**Interfaces:**
- Consumes: `lsp.Package.CtrlMap` (Task 3), `pkg.GSXFset`, `pkg.Info`, `pkg.Fset`.
- Produces: go-to-def resolves identifiers in `ForMarkup.Clause`/`IfMarkup.Cond`/`GoBlock.Code`.

- [ ] **Step 1: Write the failing test** (unit over `handleDefinition`'s helper, mirroring existing definition unit tests)

```go
func TestExprNodeAtOffsetControlFlow(t *testing.T) {
	src := "package x\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts { <li>x</li> } }\n}\n"
	pkg, path := parseOnlyPackage(t, "p.gsx", src) // existing helper in definition_test.go
	// offset of "Posts" inside the for-clause
	off := strings.Index(src, "props.Posts") + len("props.")
	node, _ := exprNodeAtOffset(pkg, path, off)
	if _, ok := node.(*gsxast.ForMarkup); !ok {
		t.Fatalf("exprNodeAtOffset on a for-clause = %T, want *ForMarkup", node)
	}
}
```

(If `parseOnlyPackage` doesn't populate `CtrlMap`, this unit test only needs `exprNodeAtOffset` to recognize the node by `ClausePos`; the full resolution is covered by the e2e Task 5. Keep this unit focused on the node-recognition extension.)

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/lsp/ -run TestExprNodeAtOffsetControlFlow -v`
Expected: FAIL — `exprNodeAtOffset` returns nil for a for-clause cursor.

- [ ] **Step 3: Implement**

In `internal/lsp/mapping.go`, broaden `innermostIdent` to accept any `ast.Node` (callers pass `ast.Expr`, which satisfies `ast.Node`):
```go
func innermostIdent(n ast.Node, pos token.Pos) *ast.Ident { /* body unchanged: ast.Inspect(n, ...) */ }
```

In `internal/lsp/definition.go` `exprNodeAtOffset`, add control-flow cases to the type switch (return the node; the caller bridges via `CtrlMap`). Extend the switch:
```go
case *gsxast.ForMarkup:
	exprPos, exprLen = e.ClausePos, len(e.Clause)
case *gsxast.IfMarkup:
	exprPos, exprLen = e.CondPos, len(e.Cond)
case *gsxast.GoBlock:
	exprPos, exprLen = e.CodePos, len(e.Code)
```
(These have no `stages`; leave `stages` nil for them.)

In `handleDefinition`, after `node, exprPos := exprNodeAtOffset(...)` and the `hasPipeStages` branch, handle control-flow nodes before the `ExprMap` lookup:
```go
switch node.(type) {
case *gsxast.ForMarkup, *gsxast.IfMarkup, *gsxast.GoBlock:
	cr, ok := pkg.CtrlMap[node]
	if !ok || cr.Node == nil {
		return s.reply(f.ID, nil)
	}
	clauseStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := cr.ClauseStart + token.Pos(off-clauseStart)
	id := innermostIdent(cr.Node, skelPos)
	if id == nil {
		return s.reply(f.ID, nil)
	}
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	if obj == nil || !obj.Pos().IsValid() {
		return s.reply(f.ID, nil)
	}
	dp := pkg.Fset.Position(obj.Pos())
	if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") {
		return s.reply(f.ID, nil)
	}
	return s.reply(f.ID, s.locationForPos(dp))
}
```
(Place this switch right after the `hasPipeStages` block and before `skel := pkg.ExprMap[node]`. `cr.Node` is `ast.Node`; `innermostIdent` now accepts it.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/lsp/ -run TestExprNodeAtOffsetControlFlow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/mapping.go internal/lsp/definition.go internal/lsp/definition_test.go
git commit -m "feat(lsp): go-to-def recognizes control-flow clauses (for/if/{{ }})"
```

---

## Task 5: End-to-end — the reported bug

**Files:**
- Test: `gen/definition_controlflow_e2e_test.go` (create)

**Interfaces:**
- Consumes: the warm-Module `runLSP` harness (mirror `gen/definition_crosspkg_e2e_test.go`).

**Goal:** prove `{ for _, post := range props.Posts {` go-to-def on `props`/`Posts`/`post` resolves end-to-end with NO `.x.go` on disk.

- [ ] **Step 1: Write the test**

```go
package gen

// Mirror gen/definition_crosspkg_e2e_test.go's runLSP harness: write a single
// package on disk (NO .x.go), open it, send textDocument/definition with the
// cursor on "Posts" inside `{ for _, post := range props.Posts {`, assert the
// resolved Location is the .gsx Posts-field declaration line; repeat for
// "props" (-> the props param) and "post" (-> its binding in the clause).
func TestDefinitionControlFlowForClause(t *testing.T) {
	if testing.Short() {
		t.Skip("module-resolution test")
	}
	// page.gsx with:
	//   type Props struct{ Posts []Post }
	//   type Post struct{ Title string }
	//   component P(props Props) { { for _, post := range props.Posts { <li>{post.Title}</li> } } }
	// ... build runLSP input (initialize, didOpen, definition at the Posts offset, exit) ...
	// assert loc.URI ends with page.gsx and loc.Range.Start.Line is the Posts field decl line.
}
```

Implement by copying the request/harness scaffolding from `gen/definition_crosspkg_e2e_test.go` (it drives `runLSP` over JSON-RPC with a real `lspAnalyzer`). Compute the cursor (line, character) from the page source. Assert NO `.x.go` exists on disk.

- [ ] **Step 2: Run**

Run: `go test ./gen/ -run TestDefinitionControlFlowForClause -v`
Expected: PASS — `Posts` resolves to the field decl, `props` to the param, `post` to the loop binding.

- [ ] **Step 3: Full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add gen/definition_controlflow_e2e_test.go
git commit -m "test(lsp): control-flow go-to-def e2e (for clause, no .x.go)"
```

---

## Self-Review notes (for the implementer)

- **Spec coverage:** Task 1 = §3.1 (AST positions); Task 2 = §3.2(a emission-offset, b clause //line); Task 3 = §3.2 CtrlMap retention; Task 4 = §3.3 (LSP bridge); Task 5 = §4/§7 e2e. Code blocks (`GoBlock`) covered throughout.
- **`.x.go`-independence:** Task 4's resolution is identical to the Interp path including the `.x.go` guard — no generated-code dependence.
- **Byte-faithful invariant:** Task 2's test asserts `skel[off:off+len(Clause)] == Clause`. The clause `//line` and `ctrlOff` are recorded with the skeleton parsed verbatim (no gofmt), so offsets are stable.
- **Known nuance (GoBlock containing node):** a multi-statement `{{ a; b }}` has the func-body block as its smallest containing node; `innermostIdent(funcBody, skelPos)` still finds the precise ident — broader walk, correct result. If profiling ever matters, narrow later.
- **`go/ast` alias:** `internal/lsp` imports `go/ast` as `ast`; `internal/codegen` aliases it `goast`. Use each package's existing alias.
