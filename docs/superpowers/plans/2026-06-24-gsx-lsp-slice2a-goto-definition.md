# gsx LSP — Slice 2a Core: Go-to-Definition (D1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `textDocument/definition` in `gsx lsp` for the common case (D1): from a cursor on a Go symbol inside a `.gsx` interpolation/attr expression, jump to that symbol's definition in a real `.go` file.

**Architecture:** Add a parser position field (`ExprPos`) marking each embedded Go expression's start. Refactor codegen to *retain* the type-checked package (Fset, `types.Info`, a gsx-expr→skeleton-expr map, parsed gsx files) instead of discarding it. Evolve the LSP analyzer from `Diagnose`→`Analyze` returning a `Package` handle, hold the latest package per directory in the server, and answer `definition` by mapping the cursor — via relative offset in byte-identical expression text — to the skeleton `*ast.Ident`, then `types.Info.Uses[ident].Pos()`.

**Tech Stack:** Go 1.26.1, stdlib (`go/ast`, `go/types`, `go/token`, `go/parser`); reuses `internal/codegen` (which uses `golang.org/x/tools/go/packages`).

## Global Constraints

- Module `github.com/gsxhq/gsx`, Go 1.26.1.
- `internal/lsp` imports only stdlib + `internal/diag` + `github.com/gsxhq/gsx/ast` (gsxast). It MUST NOT import `gen` or `internal/codegen` (cycle-free; `gen` imports `internal/lsp`, never the reverse).
- The two FileSets are SEPARATE: the gsx parse fset (`internal/codegen/batch.go:64`) and the `go/packages` skeleton fset (`pkg.Fset`). gsx node positions resolve via the former; skeleton/object positions via the latter. The reverse mapper bridges them with byte-offset deltas (the gsx Go-expression text and its skeleton counterpart are byte-identical).
- LSP wire positions are 0-based, characters in the negotiated encoding (utf-16 default, utf-8 if negotiated). All position↔offset conversion goes through `internal/lsp/convert.go` helpers.
- The LSP never writes `.x.go` to disk — it reads codegen results in memory only.
- Scope is D1 only. D2 (component tags), D3 (params), and emit `//line` (`.go→.gsx`) are explicitly OUT (a follow-up plan). Do not implement them.
- Unexported by default; exported only what another package needs.
- Each task ends green (`go test ./...` for touched packages) and is committed.

---

### Task 1: Parser — `ExprPos` on `Interp` and `ExprAttr`

**Files:**
- Modify: `ast/ast.go` (add `ExprPos token.Pos` to `Interp` and `ExprAttr`)
- Modify: `parser/markup.go` (`parseInterp` sets it; `parseAttrBraceValue` copies it)
- Test: `parser/exprpos_test.go`

**Interfaces:**
- Produces: `ast.Interp.ExprPos token.Pos` and `ast.ExprAttr.ExprPos token.Pos` — the `token.Pos` of the first character of the embedded Go expression (after `{` and leading whitespace), distinct from the node's `Pos()` (which is the `{` / attribute start).

- [ ] **Step 1: Write the failing test**

```go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func firstInterp(f *ast.File) *ast.Interp {
	var got *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if got != nil {
			return false
		}
		if in, ok := n.(*ast.Interp); ok {
			got = in
			return false
		}
		return true
	})
	return got
}

func TestInterpExprPos(t *testing.T) {
	src := []byte("package p\n\ncomponent C() {\n\t<div>{ user.Name }</div>\n}\n")
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	in := firstInterp(f)
	if in == nil {
		t.Fatal("no Interp parsed")
	}
	p := fset.Position(in.ExprPos)
	if src[p.Offset] != 'u' {
		t.Fatalf("ExprPos at offset %d byte %q, want 'u' (start of `user.Name`)", p.Offset, src[p.Offset])
	}
}

func TestExprAttrExprPos(t *testing.T) {
	src := []byte("package p\n\ncomponent C() {\n\t<a href={ dest }>x</a>\n}\n")
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ea *ast.ExprAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if e, ok := n.(*ast.ExprAttr); ok {
			ea = e
			return false
		}
		return true
	})
	if ea == nil {
		t.Fatal("no ExprAttr parsed")
	}
	p := fset.Position(ea.ExprPos)
	if src[p.Offset] != 'd' {
		t.Fatalf("ExprAttr.ExprPos at byte %q, want 'd' (start of `dest`)", src[p.Offset])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser/ -run "ExprPos" -v`
Expected: FAIL — `in.ExprPos undefined` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `ast/ast.go`, add the field to `Interp` (currently lines ~177–184):

```go
type Interp struct {
	span
	Expr    string
	ExprPos token.Pos // position of the first char of Expr in source (after `{` + ws)
	Try     bool
	Stages  []PipeStage
	JSCtx   JSCtx
}
```

And to `ExprAttr` (currently lines ~220–225):

```go
type ExprAttr struct {
	span
	Name, Expr string
	ExprPos    token.Pos // position of the first char of Expr in source
	Try        bool
	Stages     []PipeStage
}
```

In `parser/markup.go`, replace `parseInterp` (lines 12–28) so it computes the expression start by skipping the leading whitespace `TrimSpace` removed:

```go
func (p *parser) parseInterp() (*ast.Interp, error) {
	start := p.i
	startPos := p.posAt(start)
	end, ok := goExprEnd(p.src, p.i)
	if !ok {
		return nil, p.errorf(startPos, "unterminated `{`")
	}
	raw := p.src[p.i+1 : end]
	inner := strings.TrimSpace(raw)
	// ExprPos: first non-whitespace byte after '{' — the start of the Go expression.
	lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
	exprPos := p.posAt(p.i + 1 + lead)
	seed, seedTry, stages, perr := parsePipe(inner)
	if perr != nil {
		return nil, p.errorf(startPos, "%v", perr)
	}
	p.i = end + 1
	n := &ast.Interp{Expr: seed, ExprPos: exprPos, Try: seedTry, Stages: stages}
	ast.SetSpan(n, startPos, p.posAt(p.i))
	return n, nil
}
```

In `parser/markup.go`, `parseAttrBraceValue` builds the `ExprAttr` from the parsed inner interp (line 510) — copy the position:

```go
	ea := &ast.ExprAttr{Name: name, Expr: in.Expr, ExprPos: in.ExprPos, Try: in.Try, Stages: in.Stages}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./parser/ -run "ExprPos" -v`
Then guard against regressions: `go test ./parser/ ./ast/`
Expected: PASS; existing parser tests still green (the new field defaults to `token.NoPos` everywhere it isn't set).

- [ ] **Step 5: Commit**

```bash
git add ast/ast.go parser/markup.go parser/exprpos_test.go
git commit -m "feat(parser): record ExprPos (Go-expression start) on Interp and ExprAttr"
```

---

### Task 2: codegen retention — `harvest` keeps skeleton exprs; `PackageResult` carries the analyzed package

**Files:**
- Modify: `internal/codegen/analyze.go` (`harvest` gains an `exprOut` param)
- Modify: `internal/codegen/batch.go` (`PackageResult` fields; populate retention even on the type-error path; single up-front `harvest`)
- Test: `internal/codegen/retention_test.go`

**Interfaces:**
- Consumes: `ast.Interp`/`ast.ExprAttr` (Task 1 is not strictly required here, but the retained exprs pair with those nodes).
- Produces — new `PackageResult` fields (keep the existing `Files`/`Diags`/`Err`):
  - `GSXFset *token.FileSet` — the gsx parse fset (resolve gsx node positions → `.gsx` byte offsets).
  - `Fset *token.FileSet` — `pkg.Fset` (resolve object/skeleton positions → `.go`/`.gsx` via `//line`).
  - `Info *types.Info` — `pkg.TypesInfo` (`Uses`/`Defs`/`Types`).
  - `ExprMap map[gsxast.Node]goast.Expr` — gsx `Interp`/`ExprAttr` node → its skeleton `_gsxuse` argument expression.
  - `GSXFiles map[string]*gsxast.File` — `.gsx` path → parsed gsx AST.
- `harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr)` — `exprOut` may be nil; when non-nil, records the `_gsxuse` argument node per gsx node.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	goast "go/ast"
	"os"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

// TestRetentionPopulated: the package result carries Fset/Info and an ExprMap
// whose entry for the `{ title }` interp is the skeleton ident `title`.
func TestRetentionPopulated(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, filepath.Join(dir, "go.mod"),
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, filepath.Join(dir, "card.gsx"),
		"package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	if pr.Fset == nil || pr.GSXFset == nil || pr.Info == nil {
		t.Fatalf("retention not populated: Fset=%v GSXFset=%v Info=%v", pr.Fset, pr.GSXFset, pr.Info)
	}
	if len(pr.GSXFiles) == 0 {
		t.Fatalf("GSXFiles empty")
	}
	// Find the Interp node and check its skeleton expr is ident "title".
	var interp *gsxast.Interp
	for _, f := range pr.GSXFiles {
		gsxast.Inspect(f, func(n gsxast.Node) bool {
			if in, ok := n.(*gsxast.Interp); ok {
				interp = in
				return false
			}
			return true
		})
	}
	if interp == nil {
		t.Fatal("no Interp in GSXFiles")
	}
	se := pr.ExprMap[interp]
	if se == nil {
		t.Fatalf("ExprMap has no entry for the interp node")
	}
	id, ok := se.(*goast.Ident)
	if !ok || id.Name != "title" {
		t.Fatalf("skeleton expr = %#v, want ident `title`", se)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

NOTE: `writeFile` may already exist in the `codegen` test package (Task-7 of slice 1 noted a `writeFile` helper). If the build reports `writeFile` redeclared, delete the helper above and reuse the existing one.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestRetentionPopulated -v`
Expected: FAIL — `pr.Fset undefined` (the fields don't exist yet).

- [ ] **Step 3: Write minimal implementation**

In `internal/codegen/batch.go`, extend `PackageResult` (currently lines 22–27):

```go
type PackageResult struct {
	Files map[string][]byte
	Diags []diag.Diagnostic
	Err   error

	// Retained analysis for the language server (read-only; nil when the package
	// failed before type-checking). The two FileSets are distinct: GSXFset is the
	// gsx parse fset; Fset is the go/packages skeleton fset.
	GSXFset  *token.FileSet
	Fset     *token.FileSet
	Info     *types.Info
	ExprMap  map[gsxast.Node]goast.Expr
	GSXFiles map[string]*gsxast.File
}
```

Add imports to `batch.go` if missing: `goast "go/ast"`, `"go/types"`, `"go/token"` (token is already imported).

In `internal/codegen/analyze.go`, change `harvest` (lines 662–699) to also fill `exprOut` when non-nil. Replace its signature and the recording line:

```go
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr) {
	byKey := map[string]*gsxast.Component{}
	for _, c := range comps {
		byKey[componentKey(c)] = c
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		c, ok := byKey[funcDeclKey(fd)]
		if !ok || fd.Body == nil {
			continue
		}
		nodes := componentExprs(c)
		k := 0
		goast.Inspect(fd.Body, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxuse" || len(call.Args) != 1 {
				return true
			}
			if k < len(nodes) {
				out[nodes[k]] = info.Types[call.Args[0]].Type
				if exprOut != nil {
					exprOut[nodes[k]] = call.Args[0]
				}
				k++
			}
			return true
		})
	}
}
```

In `internal/codegen/batch.go`, find the Step-6 loop (`for _, pkg := range pkgs {` around line 228) and its inner harvest (the `for _, f := range pkg.Syntax { ... harvest(f, comps, pkg.TypesInfo, resolved) }` block around lines 298–304). Restructure so retention is populated for the matched dir **before** the type-error early-`continue`, and harvest runs exactly once (filling both `resolved` and the per-package `ExprMap`):

```go
	for _, pkg := range pkgs {
		// ... existing code that determines pkgDir from pkg.CompiledGoFiles/GoFiles ...
		if pkgDir == "" {
			continue
		}
		if filesByDir[pkgDir] == nil {
			continue
		}

		// Retain the analyzed package for the LSP, even if it has type errors
		// (go/types fills TypesInfo best-effort; the skeleton AST is intact).
		res := result[pkgDir]
		res.GSXFset = fset
		res.Fset = pkg.Fset
		res.Info = pkg.TypesInfo
		res.GSXFiles = filesByDir[pkgDir]
		res.ExprMap = map[gsxast.Node]goast.Expr{}
		for _, f := range pkg.Syntax {
			fname := pkg.Fset.Position(f.Pos()).Filename
			comps, ok := skelCompsByPath[fname]
			if !ok {
				continue
			}
			harvest(f, comps, pkg.TypesInfo, resolved, res.ExprMap)
		}

		// ... existing type-error handling (collect diags, delete(filesByDir,...), continue) ...
		// ... existing pkg.Errors handling ...
		// REMOVE the old trailing `for _, f := range pkg.Syntax { ... harvest(...) }`
		// block — harvest now runs once, above.
	}
```

IMPORTANT: this is a surgical restructure of an existing loop. Preserve every existing branch (type-error collection, `pkg.Errors` collection, the `delete(filesByDir, pkgDir)` calls, and the `continue`s). The only changes are: (a) insert the retention block right after the `filesByDir[pkgDir] == nil` guard; (b) delete the now-duplicate trailing harvest loop. Read the current function fully before editing.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestRetentionPopulated -v`
Then full regression on the heavy suites: `go test ./internal/codegen/ ./gen/`
Expected: PASS; no regressions (generated output and diagnostics are unchanged — only new fields are populated).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/batch.go internal/codegen/retention_test.go
git commit -m "feat(codegen): retain Fset/TypesInfo/expr-map/gsx-AST on PackageResult for the LSP"
```

---

### Task 3: LSP `Package` handle + `Analyzer` evolves `Diagnose`→`Analyze`

**Files:**
- Create: `internal/lsp/analysis.go` (the `Package` type)
- Modify: `internal/lsp/server.go` (`Analyzer` interface; `Server` holds latest packages; `analyzeAndPublish` uses `Analyze`)
- Modify: `internal/lsp/server_lifecycle_test.go` and `internal/lsp/server_sync_test.go` (fakes implement `Analyze`)
- Modify: `gen/lsp.go` (`lspAnalyzer.Analyze` builds `*lsp.Package`)
- Test: covered by the adapted existing sync test (diagnostics still publish)

**Interfaces:**
- Produces:
  - `type Package struct { Diags []diag.Diagnostic; GSXFset, Fset *token.FileSet; Info *types.Info; ExprMap map[gsxast.Node]ast.Expr; Files map[string]*gsxast.File }`.
  - `type Analyzer interface { Analyze(dir string, override map[string][]byte) (*Package, error) }`.
  - `Server` gains `pkgs map[string]*Package` (dir → latest analyzed package), populated in `analyzeAndPublish`.

- [ ] **Step 1: Write the failing test**

Adapt the existing fake in `internal/lsp/server_sync_test.go`. Replace the `fakeAnalyzer` definition and its method with one that returns a `*Package`:

```go
// fakeAnalyzer returns one error diagnostic for the file it is told about.
type fakeAnalyzer struct {
	file string // abs .gsx path to attach the diagnostic to
}

func (a fakeAnalyzer) Analyze(dir string, override map[string][]byte) (*Package, error) {
	if _, ok := override[a.file]; !ok {
		return &Package{}, nil // the open buffer must reach the analyzer
	}
	return &Package{Diags: []diag.Diagnostic{{
		Start:    token.Position{Filename: a.file, Line: 1, Column: 3},
		End:      token.Position{Filename: a.file, Line: 1, Column: 6},
		Severity: diag.Error,
		Code:     "type-error",
		Source:   "types",
		Message:  "undefined: foo",
	}}}, nil
}
```

Also update `nilAnalyzer` in `internal/lsp/server_lifecycle_test.go`:

```go
type nilAnalyzer struct{}

func (nilAnalyzer) Analyze(string, map[string][]byte) (*Package, error) { return &Package{}, nil }
```

(The existing assertions in `TestDidOpenPublishesDiagnostics` / `TestDidCloseClearsDiagnostics` stay unchanged — they still expect the same published diagnostic.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run "TestDidOpen|TestServerInitialize" -v`
Expected: FAIL — the server still calls `s.analyzer.Diagnose`, and `Package` is undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/lsp/analysis.go`:

```go
package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// Package is the retained, read-only result of analyzing one .gsx package: the
// diagnostics plus everything the read-intelligence features need. The two
// FileSets are distinct — GSXFset resolves gsx node positions; Fset resolves
// skeleton/object positions (honoring //line).
type Package struct {
	Diags   []diag.Diagnostic
	GSXFset  *token.FileSet
	Fset     *token.FileSet
	Info     *types.Info
	ExprMap  map[gsxast.Node]ast.Expr // gsx Interp/ExprAttr → skeleton go/ast expr
	Files    map[string]*gsxast.File  // .gsx path → parsed gsx AST
}
```

In `internal/lsp/server.go`: change the `Analyzer` interface (lines 12–16):

```go
type Analyzer interface {
	Analyze(dir string, override map[string][]byte) (*Package, error)
}
```

Add a `pkgs` field to `Server` (struct at lines 18–27) and initialize it in `NewServer`:

```go
type Server struct {
	conn     *conn
	docs     *docStore
	analyzer Analyzer
	pkgs     map[string]*Package // dir → latest analyzed package
	enc      encoding
	shutdown bool
	exited   bool
}

func NewServer(r io.Reader, w io.Writer, a Analyzer) *Server {
	return &Server{conn: newConn(r, w), docs: newDocStore(), analyzer: a, pkgs: map[string]*Package{}, enc: encUTF16}
}
```

In `analyzeAndPublish` (lines 163–217), change the analyzer call and store the package:

```go
	pkg, err := s.analyzer.Analyze(dir, override)
	if err != nil || pkg == nil {
		return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: changedURI, Diagnostics: []Diagnostic{}})
	}
	s.pkgs[dir] = pkg
	diags := pkg.Diags
```

(The rest of `analyzeAndPublish` — grouping `diags` by filename, the `targets` set, publishing — is unchanged.)

In `gen/lsp.go`, replace `lspAnalyzer.Diagnose` with `Analyze` building the handle:

```go
func (lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir}, nil, attrclass.Builtin(), nil, nil, override)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pr := out[abs]
	if pr == nil {
		return &lsp.Package{}, nil
	}
	return &lsp.Package{
		Diags:   pr.Diags,
		GSXFset: pr.GSXFset,
		Fset:    pr.Fset,
		Info:    pr.Info,
		ExprMap: pr.ExprMap,
		Files:   pr.GSXFiles,
	}, nil
}
```

Update `gen/lsp.go` imports: add `"github.com/gsxhq/gsx/internal/lsp"` if not present (it is, for `lsp.NewServer`). The return type `*lsp.Package` and field types line up because `pr.ExprMap` is `map[gsxast.Node]goast.Expr` and `lsp.Package.ExprMap` is `map[gsxast.Node]ast.Expr` (same underlying type — `go/ast`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ ./gen/`
Expected: PASS — diagnostics still publish through the new `Analyze` path; the slice-1 e2e (`gen/lsp_test.go`) still passes (it asserts a published diagnostic, which is unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/analysis.go internal/lsp/server.go internal/lsp/server_lifecycle_test.go internal/lsp/server_sync_test.go gen/lsp.go
git commit -m "refactor(lsp): Analyzer returns a retained Package handle (Diagnose -> Analyze)"
```

---

### Task 4: Reverse-map core — position↔offset converter and skeleton ident finder

**Files:**
- Modify: `internal/lsp/convert.go` (add `byteOffsetForPosition`)
- Create: `internal/lsp/mapping.go` (`innermostIdent`)
- Test: `internal/lsp/mapping_test.go`

**Interfaces:**
- Consumes: `encoding`/`encUTF16`/`encUTF8`, `utf16Len` (Task-4 slice 1, in `convert.go`).
- Produces:
  - `func byteOffsetForPosition(text string, line, character int, enc encoding) int` — LSP (0-based line, encoded character) → byte offset in `text`. Inverse of `charForByteCol`.
  - `func innermostIdent(expr ast.Expr, pos token.Pos) *ast.Ident` — the innermost `*ast.Ident` in `expr`'s subtree whose `[Pos,End)` contains `pos`, or nil.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestByteOffsetForPosition(t *testing.T) {
	text := "line one\nhéllo world\n" // line 1 has a 2-byte 'é'
	// UTF-16: line 1 (0-based), char 6 → after "héllo " → byte offset of 'w'.
	off := byteOffsetForPosition(text, 1, 6, encUTF16)
	if text[off] != 'w' {
		t.Fatalf("utf16: byte %q at off %d, want 'w'", text[off], off)
	}
	// UTF-8: char counts bytes → char 7 lands on 'w' too ("héllo " is 7 bytes).
	off8 := byteOffsetForPosition(text, 1, 7, encUTF8)
	if text[off8] != 'w' {
		t.Fatalf("utf8: byte %q, want 'w'", text[off8])
	}
}

func TestInnermostIdent(t *testing.T) {
	// Parse a standalone expression; its node positions start at 1 (token.Pos).
	expr, err := parser.ParseExpr("user.Name")
	if err != nil {
		t.Fatal(err)
	}
	base := expr.Pos() // position of 'u'
	// offset 0 → "user"
	if id := innermostIdent(expr, base+token.Pos(0)); id == nil || id.Name != "user" {
		t.Fatalf("at 0 got %v, want user", id)
	}
	// offset 5 → 'N' of "Name" (u(0)s(1)e(2)r(3).(4)N(5))
	if id := innermostIdent(expr, base+token.Pos(5)); id == nil || id.Name != "Name" {
		t.Fatalf("at 5 got %v, want Name", id)
	}
	// offset 4 → the '.', no ident
	if id := innermostIdent(expr, base+token.Pos(4)); id != nil {
		t.Fatalf("at 4 got %v, want nil (on the dot)", id)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run "TestByteOffsetForPosition|TestInnermostIdent" -v`
Expected: FAIL — `undefined: byteOffsetForPosition`, `undefined: innermostIdent`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/lsp/convert.go`:

```go
import "strings" // ensure strings is imported (add if not already)

// byteOffsetForPosition maps a 0-based LSP position (character counted in enc)
// to a byte offset in text. It is the inverse of charForByteCol. A line or
// character past the end clamps to the end of the line / text.
func byteOffsetForPosition(text string, line, character int, enc encoding) int {
	off := 0
	for l := 0; l < line; l++ {
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			return len(text)
		}
		off += nl + 1
	}
	lineText := text[off:]
	if nl := strings.IndexByte(lineText, '\n'); nl >= 0 {
		lineText = lineText[:nl]
	}
	return off + byteForChar(lineText, character, enc)
}

// byteForChar returns the byte offset within lineText for the 0-based character
// index in enc.
func byteForChar(lineText string, character int, enc encoding) int {
	if enc == encUTF8 {
		if character > len(lineText) {
			return len(lineText)
		}
		return character
	}
	units := 0
	for i, r := range lineText {
		if units >= character {
			return i
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return len(lineText)
}
```

Create `internal/lsp/mapping.go`:

```go
package lsp

import (
	"go/ast"
	"go/token"
)

// innermostIdent returns the innermost *ast.Ident in expr's subtree whose
// [Pos, End) contains pos, or nil if pos falls on no identifier (e.g. on a '.'
// or operator).
func innermostIdent(expr ast.Expr, pos token.Pos) *ast.Ident {
	var found *ast.Ident
	ast.Inspect(expr, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if pos < n.Pos() || pos >= n.End() {
			return false // pos not in this node; prune
		}
		if id, ok := n.(*ast.Ident); ok {
			found = id
		}
		return true // descend: a child ident (e.g. selector Sel) may be tighter
	})
	return found
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run "TestByteOffsetForPosition|TestInnermostIdent" -v`
Then: `go test ./internal/lsp/`
Expected: PASS (whole package).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/convert.go internal/lsp/mapping.go internal/lsp/mapping_test.go
git commit -m "feat(lsp): position->offset converter and skeleton ident finder for go-to-def"
```

---

### Task 5: `textDocument/definition` (D1) — handler, dispatch, capability, e2e

**Files:**
- Modify: `internal/lsp/protocol.go` (definition params + `Location`)
- Modify: `internal/lsp/server.go` (advertise `definitionProvider`; dispatch; `handleDefinition`)
- Create: `internal/lsp/definition.go` (the handler + gsx-node lookup + target-position conversion)
- Test: `internal/lsp/definition_test.go` (e2e against a temp module via the real analyzer)

**Interfaces:**
- Consumes: `Package` (Task 3), `byteOffsetForPosition`/`innermostIdent` (Task 4), `uriToPath`/`pathToURI` (slice 1), `charForByteCol`/`lineAtFunc` (slice 1), `ast.Interp`/`ast.ExprAttr.ExprPos` (Task 1).
- Produces: `textDocument/definition` returns a single `Location` (or JSON `null` when nothing resolves).

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Reuse the real analyzer through gen? No — internal/lsp must not import gen.
// Instead drive the server with a real Package built by a tiny in-test analyzer
// that shells the codegen pipeline is NOT allowed here (cycle). So this e2e lives
// in gen/ where the real analyzer is available. See gen/definition_e2e_test.go.
//
// This file unit-tests the gsx-node lookup helper in isolation.

func TestExprNodeAtOffset(t *testing.T) {
	// Build a Package with one .gsx file parsed by the gsx parser, no types.
	src := "package x\n\ncomponent C(u User) {\n\t<div>{ u.Name }</div>\n}\n"
	pkg, path := parseOnlyPackage(t, "c.gsx", src)
	// offset of 'N' in "u.Name"
	off := strings.Index(src, "u.Name") + 2
	node := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		t.Fatalf("no expr node found at offset %d", off)
	}
	in, ok := node.(*gsxInterp)
	_ = in
	if !ok {
		t.Fatalf("node = %T, want *ast.Interp", node)
	}
}
```

NOTE: the helper `parseOnlyPackage` and the `gsxInterp` alias are scaffolding the implementer must replace with the real gsx AST types — `parseOnlyPackage` should parse `src` with `gsxparser.ParseFile` into a `*Package` with only `GSXFset` and `Files` set (no types needed for the lookup test), and the assertion should be `node.(*gsxast.Interp)`. Write it concretely against the real imports:

```go
import (
	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
	"go/token"
)

func parseOnlyPackage(t *testing.T, name, src string) (*Package, string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, name, []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	return &Package{GSXFset: fset, Files: map[string]*gsxast.File{name: f}}, name
}
```

and the body assertion: `if _, ok := node.(*gsxast.Interp); !ok { t.Fatalf(...) }`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestExprNodeAtOffset -v`
Expected: FAIL — `undefined: exprNodeAtOffset`.

- [ ] **Step 3: Write minimal implementation**

In `internal/lsp/protocol.go`, add:

```go
type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}
```

Create `internal/lsp/definition.go`:

```go
package lsp

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"

	gsxast "github.com/gsxhq/gsx/ast"
)

// exprNodeAtOffset returns the innermost gsx Interp/ExprAttr whose Go-expression
// span [ExprPos, ExprPos+len(Expr)) contains the byte offset, or nil.
func exprNodeAtOffset(pkg *Package, path string, off int) gsxast.Node {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return nil
	}
	var found gsxast.Node
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		var exprPos token.Pos
		var exprLen int
		switch e := n.(type) {
		case *gsxast.Interp:
			exprPos, exprLen = e.ExprPos, len(e.Expr)
		case *gsxast.ExprAttr:
			exprPos, exprLen = e.ExprPos, len(e.Expr)
		default:
			return true
		}
		if !exprPos.IsValid() {
			return true
		}
		start := pkg.GSXFset.Position(exprPos).Offset
		if off >= start && off < start+exprLen {
			found = n
		}
		return true
	})
	return found
}

// handleDefinition answers textDocument/definition for D1: a Go symbol under the
// cursor that resolves to a definition in a real .go file.
func (s *Server) handleDefinition(f frame) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || pkg.Info == nil {
		return s.reply(f.ID, nil)
	}

	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)
	node := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		return s.reply(f.ID, nil)
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		return s.reply(f.ID, nil)
	}

	// Map the cursor into the skeleton expr by relative byte offset (the gsx and
	// skeleton expression texts are byte-identical).
	var exprPos token.Pos
	switch e := node.(type) {
	case *gsxast.Interp:
		exprPos = e.ExprPos
	case *gsxast.ExprAttr:
		exprPos = e.ExprPos
	}
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)

	id := innermostIdent(skel, skelPos)
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
	if dp.Filename == "" {
		return s.reply(f.ID, nil)
	}
	loc := s.locationFor(dp)
	return s.reply(f.ID, loc)
}

// locationFor builds an LSP Location from a resolved definition position,
// converting its 1-based byte column to the negotiated encoding using the target
// file's own line text (read from disk; the target is a real file).
func (s *Server) locationFor(dp token.Position) Location {
	char := dp.Column - 1
	if data, err := os.ReadFile(dp.Filename); err == nil {
		char = charForByteCol(lineAtFunc(string(data))(dp.Line), dp.Column, s.enc)
	}
	line := dp.Line - 1
	if line < 0 {
		line = 0
	}
	pos := Position{Line: line, Character: char}
	return Location{URI: pathToURI(dp.Filename), Range: Range{Start: pos, End: pos}}
}
```

In `internal/lsp/server.go`, advertise the capability in `handleInitialize`'s `serverCapabilities` (add a field to the struct in `protocol.go` and set it):

In `protocol.go`, extend `serverCapabilities`:

```go
type serverCapabilities struct {
	PositionEncoding   string `json:"positionEncoding"`
	TextDocumentSync   int    `json:"textDocumentSync"`
	DefinitionProvider bool   `json:"definitionProvider"`
}
```

In `server.go` `handleInitialize`, set `DefinitionProvider: true` in the returned `serverCapabilities`. And add the dispatch case in `handle`:

```go
	case "textDocument/definition":
		return s.handleDefinition(f)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestExprNodeAtOffset -v`
Then the whole package: `go test ./internal/lsp/`
Expected: PASS.

- [ ] **Step 5: Write the end-to-end test (real analyzer, in `gen/`)**

Create `gen/definition_e2e_test.go` — this lives in `gen` because the real analyzer is there (internal/lsp can't import gen):

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

// TestDefinitionD1 drives the real analyzer through lsp.Server: go-to-def on the
// `Name` field in `{ u.Name }` resolves to its declaration in user.go.
func TestDefinitionD1(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("user.go", "package x\n\ntype User struct {\n\tName string\n}\n")
	cardSrc := "package x\n\ncomponent Card(u User) {\n\t<div>{ u.Name }</div>\n}\n"
	must("card.gsx", cardSrc)
	uri := "file://" + filepath.Join(dir, "card.gsx")

	// position of 'N' in "u.Name" within card.gsx (line index, char index).
	lines := strings.Split(cardSrc, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "u.Name"); c >= 0 {
			line, character = i, c+2 // the 'N'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": cardSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "user.go") {
		t.Fatalf("definition did not resolve to user.go; out:\n%s", s)
	}
	_ = lsp.Package{} // ensure the lsp import is used
}
```

NOTE: confirm `gsx` component/param syntax against `examples/04_components.gsx` before running; adjust `card.gsx`/`user.go` if the grammar differs. The contract: definition on the `Name` field resolves to `user.go`.

- [ ] **Step 6: Run the e2e and the whole tree**

Run: `go test ./gen/ -run TestDefinitionD1 -v`
Then: `go test ./...`
Expected: PASS; `go build ./...` clean.

- [ ] **Step 7: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/server.go internal/lsp/definition.go internal/lsp/definition_test.go gen/definition_e2e_test.go
git commit -m "feat(lsp): textDocument/definition (D1) — Go symbol in .gsx -> .go definition"
```

---

## Out of scope (deferred to the slice-2a follow-up plan)

- **D2** (component tag `<Card/>` → `component Card`) — needs the gsx-AST tag→component lookup.
- **D3** (component param → param decl) — needs `Component.ParamsPos` and a skeleton `//line` for param bindings.
- **`.go → .gsx`** — needs emit `//line` at component func decls + props fields, plus a golden rebaseline.
- **hover** — a near-free follow-on reusing the reverse mapper (`Info.Types[node]`).
- Error-tolerant nicety: definition works only when the package type-checks enough for `Info.Uses` to be populated at the cursor; transient incomplete states may return null. Acceptable for D1.

## Self-Review

**Spec coverage** (against `2026-06-24-gsx-lsp-slice2a-goto-definition-design.md`, scoped to the user's "core first: parser pos + machinery + D1" decision):
- §3.1 retention refactor → Task 2 (codegen) + Task 3 (lsp `Package` + `Analyze`). ✓
- §3.2 reverse mapper (relative offset, anchor at Go-expr start) → Task 1 (`ExprPos`) + Task 4 (`innermostIdent`, offset converter) + Task 5 (`handleDefinition` wiring). ✓
- §3.3 path (a) + D1 → Task 5. D2/D3 explicitly deferred. ✓
- §5 boundaries (mapping, definition handler, codegen-free `internal/lsp`) → Tasks 3–5; `Package` uses only stdlib + gsxast. ✓
- §6 testing (reverse-mapper unit tests; definition e2e) → Task 4 units, Task 5 e2e. ✓
- §4 `.go→.gsx`, hover, references, symbols, formatting → out of scope (recorded above). ✓

**Placeholder scan:** the two NOTE callouts (Task 2 `writeFile` possible redeclare; Task 5 scaffolding→real types and grammar check) are concrete instructions, not deferrals of plan content. No "TBD"/"handle errors appropriately".

**Type consistency:** `Analyze(dir string, override map[string][]byte) (*Package, error)` is identical in Task 3 (interface, fakes, `gen` impl) and consumed in Task 5. `Package` fields (`GSXFset`,`Fset`,`Info`,`ExprMap`,`Files`) match between Task 3 (definition) and Task 5 (use). `harvest(..., exprOut)` signature matches Task 2 def and its single caller. `byteOffsetForPosition`/`innermostIdent`/`exprNodeAtOffset` names match across Tasks 4–5. `PackageResult` new fields (Task 2) ↔ `lsp.Package` fields (Task 3) line up in `gen/lsp.go`. ✓
