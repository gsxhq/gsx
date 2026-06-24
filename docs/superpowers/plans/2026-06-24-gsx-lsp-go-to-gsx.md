# gsx LSP — `.go → .gsx` Nav + Find-References Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From a `.go` file, go-to-definition on a gsx component jumps to its `.gsx` `component` declaration, and find-references lists every use across `.go` call sites and `.gsx` `<Card/>` tags — entirely from gsx's in-memory analysis, with **no `.x.go` on disk**.

**Architecture:** gsx-LSP runs as a co-server on `.go` files alongside gopls, answering only component `definition`/`references` (null otherwise). At analysis time it extracts a **slim cross-boundary index** (`componentKey → {Decl, Refs}`) from the momentarily-available `*types.Info` and discards the heavy inputs. Both features are pure index lookups — the query path never touches full type info. The skeleton's existing child-tag `//line` already maps `.gsx` references back; only the component's `.gsx` *declaration* position is new (`Component.NamePos`).

**Tech Stack:** Go 1.26.1, stdlib (`go/types`, `go/token`, `go/ast`); reuses `internal/codegen` (`golang.org/x/tools/go/packages`) and the slice-1/2a `internal/lsp`.

## Global Constraints

- Module `github.com/gsxhq/gsx`, Go 1.26.1.
- **No `.x.go` on disk.** All analysis is the in-memory `go/packages` overlay; the LSP never writes `.x.go`. A test asserts none is produced.
- **Slim cross-boundary representation.** The `.go → .gsx` features retain **only** the cross-index (`componentKey → {Decl token.Position, Refs []token.Position}`). The full `*types.Info` and `.go` ASTs are used transiently to build it, then discarded; the definition/references query path touches **only the index**.
- `internal/lsp` MUST NOT import `gen`/`internal/codegen` (cycle-free). Cross-index types are defined in `internal/lsp` (stdlib `go/token` only); `gen` converts the codegen result into them.
- gsx-LSP publishes **no diagnostics** for `.go` files (gopls owns those) and returns **null/empty** for any `.go` symbol not resolving to a gsx component.
- References are **in-package only** this slice (cross-package deferred).
- Each task ends green (`go test ./...` for touched packages) and is committed.

---

## Sub-slice 1 — Cross-boundary index (codegen foundation)

### Task 1: Parser — `Component.NamePos`

**Files:**
- Modify: `ast/ast.go` (add `NamePos token.Pos` to `Component`)
- Modify: `parser/component.go` (set it at the name start)
- Test: `parser/component_namepos_test.go`

**Interfaces:**
- Produces: `ast.Component.NamePos token.Pos` — the position of the first character of the component's `Name` in source (the `Card` in `component Card(...)`).

- [ ] **Step 1: Write the failing test**

```go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestComponentNamePos(t *testing.T) {
	src := []byte("package p\n\ncomponent Card(title string) {\n\t<div/>\n}\n")
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "c.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var c *ast.Component
	ast.Inspect(f, func(n ast.Node) bool {
		if comp, ok := n.(*ast.Component); ok {
			c = comp
			return false
		}
		return true
	})
	if c == nil {
		t.Fatal("no component parsed")
	}
	p := fset.Position(c.NamePos)
	if src[p.Offset] != 'C' { // the 'C' of "Card"
		t.Fatalf("NamePos at byte %q, want 'C' (start of `Card`)", src[p.Offset])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser/ -run TestComponentNamePos -v`
Expected: FAIL — `c.NamePos undefined`.

- [ ] **Step 3: Write minimal implementation**

In `ast/ast.go`, add `NamePos` to `Component` (after `Name`):

```go
type Component struct {
	span
	Recv      string
	Name      string
	NamePos   token.Pos // position of the first char of Name in source
	Params    string
	ParamsPos token.Pos
	Body      []Markup
}
```

In `parser/component.go`, the name is parsed at `nameStart` (the loop that builds `c.Name = p.src[nameStart:p.i]`). Set `NamePos` from `nameStart`:

```go
	// name
	nameStart := p.i
	for !p.eof() && isTagNameByte(p.src[p.i]) && p.src[p.i] != '.' && p.src[p.i] != '-' {
		p.i++
	}
	c.Name = p.src[nameStart:p.i]
	c.NamePos = p.posAt(nameStart)
	if c.Name == "" {
		return nil, p.errorf(p.pos(), "expected component name")
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./parser/ -run TestComponentNamePos -v`
Then: `go test ./parser/ ./ast/`
Expected: PASS; no regressions (new field defaults to `token.NoPos`).

NOTE: the printer faithfulness test (`internal/printer/corpus_test.go`) zeros position fields outside `span` in `zeroSpans` — add `*ast.Component`'s `NamePos` there too (it already zeros `ParamsPos`). If `go test ./internal/printer/` fails with "fmt changed the normalized AST", add `v.NamePos = 0` in the `case *ast.Component:` of `zeroSpans`.

- [ ] **Step 5: Commit**

```bash
git add ast/ast.go parser/component.go parser/component_namepos_test.go internal/printer/corpus_test.go
git commit -m "feat(parser): record Component.NamePos (component name position)"
```

---

### Task 2: codegen — build the cross-boundary index on `PackageResult`

**Files:**
- Modify: `internal/codegen/batch.go` (`PackageResult` field + build in the Step-6 loop)
- Test: `internal/codegen/crossindex_test.go`

**Interfaces:**
- Consumes: `harvest`/`componentKey`/`funcDeclKey` (analyze.go), the gsx parse `fset` and `pkg.Fset`/`pkg.TypesInfo`/`pkg.Syntax`/`skelCompsByPath` (in scope in the loop).
- Produces — new `PackageResult` field:
  - `CrossIndex map[string]CrossRef` where
    `type CrossRef struct { Decl token.Position; Refs []token.Position }`.
  - `Decl` = the component's `.gsx` declaration (`fset.Position(c.NamePos)`).
  - `Refs` = every reference to the component object, each resolved via
    `pkg.Fset.Position(ident.Pos())` (real `.go` positions stay `.go`; skeleton
    `Card(...)` calls map to `.gsx` via the existing child-tag `//line`). Bare
    `.x.go` positions (no `//line`) are skipped.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCrossIndex: a component Card declared in card.gsx, called from main.go and
// used as <Card/> in page.gsx, is indexed with its .gsx Decl and both refs.
func TestCrossIndex(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, filepath.Join(dir, "go.mod"),
		"module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, filepath.Join(dir, "card.gsx"),
		"package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	writeFile(t, filepath.Join(dir, "page.gsx"),
		"package x\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n")
	writeFile(t, filepath.Join(dir, "main.go"),
		"package x\n\nvar _ = Card\n")

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s", dir)
	}
	cr, ok := pr.CrossIndex[".Card"]
	if !ok {
		t.Fatalf("CrossIndex missing .Card; keys=%v", keysOfCross(pr.CrossIndex))
	}
	if !strings.HasSuffix(cr.Decl.Filename, "card.gsx") {
		t.Fatalf("Decl filename = %q, want card.gsx", cr.Decl.Filename)
	}
	var goRef, gsxRef bool
	for _, r := range cr.Refs {
		if strings.HasSuffix(r.Filename, "main.go") {
			goRef = true
		}
		if strings.HasSuffix(r.Filename, "page.gsx") {
			gsxRef = true
		}
	}
	if !goRef {
		t.Errorf("no main.go reference in Refs: %+v", cr.Refs)
	}
	if !gsxRef {
		t.Errorf("no page.gsx (<Card/>) reference in Refs: %+v", cr.Refs)
	}
}

func keysOfCross(m map[string]CrossRef) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}
```

NOTE: a `writeFile(t, path, content)` helper already exists in the codegen test package (used by other tests). If `writeFile` is reported redeclared, delete any local copy and reuse the existing one.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestCrossIndex -v`
Expected: FAIL — `pr.CrossIndex undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/codegen/batch.go`, add the type and the `PackageResult` field:

```go
// CrossRef is one component's cross-boundary entry: its name, its .gsx
// declaration, and every reference (resolved positions — .go call sites stay
// .go; .gsx <Card/> tags map to .gsx via the skeleton's child-tag //line).
// Name's length bounds the cursor-on-reference span check in the LSP.
type CrossRef struct {
	Name string
	Decl token.Position
	Refs []token.Position
}
```

Add to `PackageResult` (alongside the slice-2a retention fields):

```go
	CrossIndex map[string]CrossRef
```

In the Step-6 loop, **right after** the `harvest` loop (after the `for _, f := range pkg.Syntax { ... harvest(...) }` block, ~line 281), build the index:

```go
		// Build the slim cross-boundary index: component objects (by componentKey)
		// → their .gsx declaration + every reference, resolved through pkg.Fset
		// (//line maps skeleton refs to .gsx; real .go refs stay .go).
		compByKey := map[string]*gsxast.Component{} // componentKey → component (for Name + NamePos)
		compObjByKey := map[string]types.Object{}   // componentKey → the component's func object
		for _, f := range pkg.Syntax {
			fname := pkg.Fset.Position(f.Pos()).Filename
			comps, ok := skelCompsByPath[fname]
			if !ok {
				continue
			}
			for _, c := range comps {
				compByKey[componentKey(c)] = c
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*goast.FuncDecl)
				if !ok {
					continue
				}
				if _, ok := compByKey[funcDeclKey(fd)]; !ok {
					continue
				}
				if obj := pkg.TypesInfo.Defs[fd.Name]; obj != nil {
					compObjByKey[funcDeclKey(fd)] = obj
				}
			}
		}
		objKey := map[types.Object]string{} // reverse: object → componentKey
		for key, obj := range compObjByKey {
			objKey[obj] = key
		}
		index := map[string]CrossRef{}
		for key, c := range compByKey {
			index[key] = CrossRef{Name: c.Name, Decl: fset.Position(c.NamePos)} // gsx fset → .gsx position
		}
		for id, obj := range pkg.TypesInfo.Uses {
			key, ok := objKey[obj]
			if !ok {
				continue
			}
			p := pkg.Fset.Position(id.Pos())
			if strings.HasSuffix(p.Filename, ".x.go") {
				continue // synthetic skeleton position with no //line — skip
			}
			cr := index[key]
			cr.Refs = append(cr.Refs, p)
			index[key] = cr
		}
		res.CrossIndex = index
```

Ensure `batch.go` imports `goast "go/ast"`, `"go/types"`, `"go/token"`, `"strings"` (most already present from slice-2a; add any missing).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestCrossIndex -v`
Then full codegen + gen: `go test ./internal/codegen/ ./gen/`
Expected: PASS; no regressions (additive — generated output and diagnostics unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/batch.go internal/codegen/crossindex_test.go
git commit -m "feat(codegen): build slim cross-boundary index (component -> decl + refs)"
```

---

### Task 3: LSP `Package` carries the cross-index; `gen` populates it

**Files:**
- Modify: `internal/lsp/analysis.go` (`Package` + `CrossRef` type)
- Modify: `gen/lsp.go` (convert `codegen.CrossRef` → `lsp.CrossRef`)
- Test: covered by Task 4's e2e (no standalone test)

**Interfaces:**
- Produces: `lsp.CrossRef struct { Decl token.Position; Refs []token.Position }` and `Package.CrossIndex map[string]CrossRef`.

- [ ] **Step 1: Implement**

In `internal/lsp/analysis.go`, add the type and field:

```go
// CrossRef is one component's cross-boundary entry (see the .go->.gsx design):
// its name, its .gsx declaration, and every reference, as resolved positions.
type CrossRef struct {
	Name string
	Decl token.Position
	Refs []token.Position
}
```

Add to `Package`:

```go
	CrossIndex map[string]CrossRef
```

In `gen/lsp.go`, convert and assign in `Analyze` (after building the rest of the `*lsp.Package`):

```go
	cross := make(map[string]lsp.CrossRef, len(pr.CrossIndex))
	for k, v := range pr.CrossIndex {
		cross[k] = lsp.CrossRef{Name: v.Name, Decl: v.Decl, Refs: v.Refs}
	}
	// ... in the &lsp.Package{...} literal add:
	//     CrossIndex: cross,
```

(Concretely: build `cross` before the `return &lsp.Package{...}` and add the `CrossIndex: cross` field to the literal.)

- [ ] **Step 2: Build & test**

Run: `go build ./...` then `go test ./internal/lsp/ ./gen/`
Expected: compiles (no cycle — `lsp.CrossRef` uses only `go/token`); existing tests pass.

- [ ] **Step 3: Commit**

```bash
git add internal/lsp/analysis.go gen/lsp.go
git commit -m "feat(lsp): Package carries the cross-boundary index (gen converts it)"
```

---

## Sub-slice 2 — `.go`-file lifecycle + component nav

### Task 4: `.go`-file document lifecycle (analyze, no diagnostics) + component nav

**Files:**
- Modify: `internal/lsp/server.go` (didOpen/didChange accept `.go`; analyze without publishing diagnostics for `.go`)
- Modify: `internal/lsp/definition.go` (`.go`-cursor branch)
- Modify: `internal/lsp/convert.go` or `documents.go` (a position helper, if needed)
- Test: `gen/go_definition_e2e_test.go`

**Interfaces:**
- Consumes: `Package.CrossIndex` (Task 3), `byteOffsetForPosition`/`uriToPath`/`pathToURI`/`charForByteCol`/`lineAtFunc` (slice 1/2a), `s.pkgs` (slice 2a).
- Produces: `definition` on a `.go` cursor returns the component's `.gsx` `Decl` Location, or null.

- [ ] **Step 1: Write the failing e2e (in `gen/`, real analyzer, asserts no `.x.go`)**

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

func TestGoToGsxDefinition(t *testing.T) {
	if testing.Short() {
		t.Skip("skips module resolution in -short")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("card.gsx", "package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	mainSrc := "package x\n\nfunc use() { _ = Card }\n"
	must("main.go", mainSrc)
	goURI := "file://" + filepath.Join(dir, "main.go")

	// cursor on `Card` in main.go
	lines := strings.Split(mainSrc, "\n")
	var line, ch int
	for i, l := range lines {
		if c := strings.Index(l, "_ = Card"); c >= 0 {
			line, ch = i, c+4 // the 'C'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI, "version": 1, "text": mainSrc, "languageId": "go"}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI},
			"position": map[string]any{"line": line, "character": ch}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "card.gsx") {
		t.Fatalf("definition from main.go did not resolve to card.gsx; out:\n%s", s)
	}
	if strings.Contains(s, ".x.go") {
		t.Fatalf("leaked a generated-code location; out:\n%s", s)
	}
	// no .x.go written to disk (in-memory only)
	if _, err := os.Stat(filepath.Join(dir, "card.x.go")); !os.IsNotExist(err) {
		t.Fatalf("card.x.go must NOT be written to disk")
	}
	_ = lsp.Package{}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./gen/ -run TestGoToGsxDefinition -v`
Expected: FAIL — the server ignores `.go` files / no `.go` definition branch yet.

- [ ] **Step 3: Implement the `.go` lifecycle + nav**

In `internal/lsp/server.go`, the document lifecycle currently analyzes+publishes for any opened file. Make `.go` files analyze **without** publishing diagnostics. Change `analyzeAndPublish` callers (`handleDidOpen`/`handleDidChange`) to route by extension. Add a helper:

```go
import "path/filepath" // already imported

// analyzeOnly analyzes the package for the changed URI and stores the result in
// s.pkgs, WITHOUT publishing diagnostics. Used for .go files (gopls owns .go
// diagnostics) so gsx-LSP can still answer component definition/references.
func (s *Server) analyzeOnly(changedURI string) {
	dir := filepath.Dir(uriToPath(changedURI))
	openDocs := s.docs.openInDir(dir)
	override := make(map[string][]byte, len(openDocs))
	for path, text := range openDocs {
		override[path] = []byte(text)
	}
	if pkg, err := s.analyzer.Analyze(dir, override); err == nil && pkg != nil {
		s.pkgs[dir] = pkg
	}
}
```

In `handleDidOpen`/`handleDidChange`, after updating the doc, branch:

```go
	if strings.HasSuffix(uriToPath(uri), ".go") {
		s.analyzeOnly(uri) // no diagnostics for .go
		return nil
	}
	return s.analyzeAndPublish(uri)
```

(`uri` is the document URI in each handler; add `"strings"` to imports if missing.)

IMPORTANT: `openInDir`/the analyzer override is keyed by `.gsx` paths for skeleton generation. An open **`.go`** buffer must reach `go/packages` as a real `.go` overlay so the analysis sees unsaved edits. For this slice's tests the `.go` file is on disk (saved), so disk content suffices; **passing the open `.go` buffer through the `go/packages` overlay is a follow-up** (note it in the report). Do NOT generate a skeleton for `.go` files.

In `internal/lsp/definition.go`, add a `.go`-cursor branch at the top of `handleDefinition` (before the existing `.gsx` reverse-mapper flow):

```go
	path := uriToPath(uri)
	if strings.HasSuffix(path, ".go") {
		return s.handleGoDefinition(f, uri, path)
	}
```

And implement `handleGoDefinition` — a pure cross-index lookup:

```go
// handleGoDefinition answers definition for a cursor in a .go file: if the
// cursor sits on a reference to a gsx component (per the cross-index), jump to
// that component's .gsx declaration. Otherwise null (gopls handles real Go).
func (s *Server) handleGoDefinition(f frame, uri, path string) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || len(pkg.CrossIndex) == 0 {
		return s.reply(f.ID, nil) // not a gsx package
	}
	curLine := p.Position.Line + 1 // token.Position is 1-based
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1 // 1-based byte column on the line
	for _, cr := range pkg.CrossIndex {
		for _, r := range cr.Refs {
			if posCoversCursor(r, path, curLine, curCol, len(cr.Name)) {
				return s.reply(f.ID, s.locationForPos(cr.Decl))
			}
		}
	}
	return s.reply(f.ID, nil)
}
```

This references helpers defined here (and `posCoversCursor`, defined in Task 5's
`references.go` — if Task 5 lands after Task 4, define `posCoversCursor` in
`definition.go` instead and have Task 5 reuse it):

```go
// lineStartOffset returns the byte offset of the start of the 0-based line.
func lineStartOffset(text string, line int) int {
	off := 0
	for i := 0; i < line; i++ {
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			return len(text)
		}
		off += nl + 1
	}
	return off
}

// locationForPos converts a resolved token.Position (a .gsx or .go file) to an
// LSP Location, encoding the column against the target file's own text.
func (s *Server) locationForPos(dp token.Position) Location {
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

`posCoversCursor` (filename-base + line + name-span containment) is shared with
Task 5; define it once. Add `os`/`go/token`/`strings` imports to `definition.go`
if missing. The `definitionProvider` capability is already advertised from
slice 2a — nothing new there.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./gen/ -run TestGoToGsxDefinition -v`
Then `go test ./...` and `go build ./...`.
Expected: PASS; whole tree green.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/server.go internal/lsp/definition.go internal/codegen/batch.go internal/lsp/analysis.go gen/lsp.go gen/go_definition_e2e_test.go
git commit -m "feat(lsp): .go-file lifecycle + component go-to-definition (.go -> .gsx)"
```

---

## Sub-slice 3 — Find-references

### Task 5: `textDocument/references`

**Files:**
- Modify: `internal/lsp/protocol.go` (`referenceParams` + `referencesProvider` capability)
- Modify: `internal/lsp/server.go` (dispatch `textDocument/references`; advertise capability)
- Modify: `internal/lsp/references.go` (new handler)
- Test: `gen/references_e2e_test.go`

**Interfaces:**
- Consumes: `Package.CrossIndex` (with `Name`), `locationForPos` (Task 4), the slice-2a reverse mapper for `.gsx` cursors.
- Produces: `references` returns the component's `Refs` (+`Decl` when `includeDeclaration`), serving both `.go` and `.gsx` cursors.

- [ ] **Step 1: Write the failing e2e**

```go
package gen

// TestReferences: references on Card (from its card.gsx declaration) returns the
// main.go call site AND the page.gsx <Card/> tag.
func TestReferences(t *testing.T) {
	if testing.Short() { t.Skip() }
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) { if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil { t.Fatal(err) } }
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	cardSrc := "package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n"
	must("card.gsx", cardSrc)
	must("page.gsx", "package x\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n")
	must("main.go", "package x\n\nfunc use() { _ = Card }\n")
	cardURI := "file://" + filepath.Join(dir, "card.gsx")

	lines := strings.Split(cardSrc, "\n")
	var line, ch int
	for i, l := range lines { if c := strings.Index(l, "component Card"); c >= 0 { line, ch = i, c+len("component ") ; break } }

	frame := func(v any) string { b, _ := json.Marshal(v); return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b) }
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI, "version": 1, "text": cardSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI},
			"position": map[string]any{"line": line, "character": ch},
			"context": map[string]any{"includeDeclaration": false}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 { t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String()) }
	s := out.String()
	if !strings.Contains(s, "main.go") { t.Errorf("references missing main.go; out:\n%s", s) }
	if !strings.Contains(s, "page.gsx") { t.Errorf("references missing page.gsx; out:\n%s", s) }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./gen/ -run TestReferences -v`
Expected: FAIL — `textDocument/references` unhandled (method-not-found).

- [ ] **Step 3: Implement**

In `internal/lsp/protocol.go`, add:

```go
type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}
type referenceParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      referenceContext       `json:"context"`
}
```

And add `ReferencesProvider bool ` json:"referencesProvider" `` to `serverCapabilities`; set `ReferencesProvider: true` in `handleInitialize`.

In `internal/lsp/server.go` `handle`, add the dispatch:

```go
	case "textDocument/references":
		return s.handleReferences(f)
```

Create `internal/lsp/references.go`:

```go
package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// handleReferences returns every reference to the gsx component under the cursor
// — .go call sites and .gsx <Card/> tags — from the cross-index. Cursor may be
// in the component's .gsx declaration, a .gsx tag, or a .go call.
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || len(pkg.CrossIndex) == 0 {
		return s.reply(f.ID, []Location{})
	}
	text, _ := s.docs.text(uri)
	curLine := p.Position.Line + 1
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1

	// Identify the component: the cursor is on its Decl, or on one of its Refs.
	var found *CrossRef
	for k := range pkg.CrossIndex {
		cr := pkg.CrossIndex[k]
		if posCoversCursor(cr.Decl, path, curLine, curCol, len(cr.Name)) {
			found = &cr
			break
		}
		for _, r := range cr.Refs {
			if posCoversCursor(r, path, curLine, curCol, len(cr.Name)) {
				found = &cr
				break
			}
		}
		if found != nil {
			break
		}
	}
	if found == nil {
		return s.reply(f.ID, []Location{})
	}

	locs := make([]Location, 0, len(found.Refs)+1)
	for _, r := range found.Refs {
		locs = append(locs, s.locationForPos(r))
	}
	if p.Context.IncludeDeclaration {
		locs = append(locs, s.locationForPos(found.Decl))
	}
	return s.reply(f.ID, locs)
}

// posCoversCursor reports whether resolved position p (in file `path`, on
// `curLine`) covers the cursor column for a `nameLen`-character name.
func posCoversCursor(p token.Position, path string, curLine, curCol, nameLen int) bool {
	return filepath.Base(p.Filename) == filepath.Base(path) && p.Line == curLine &&
		curCol >= p.Column && curCol < p.Column+nameLen
}
```

Add `"go/token"` to the `references.go` imports.

NOTE on `.gsx`-cursor precision: identifying the component from a `.gsx` cursor
matches the cursor against the `.gsx`-mapped `Decl`/`Refs` columns, which are
`//line`-derived and may be off by the skeleton-indent column nuance (slice-2a).
The match is line + name-span; if a `.gsx`-cursor case proves flaky, fall back to
the slice-2a reverse mapper (cursor → skeleton ident → `Info.Uses[id]` →
`componentKey`) to identify the component, then return its `Refs` from the index.
Confirm the e2e (`.gsx` declaration cursor) passes; if not, apply the fallback.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./gen/ -run TestReferences -v`
Then `go test ./...`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/server.go internal/lsp/references.go gen/references_e2e_test.go
git commit -m "feat(lsp): textDocument/references for gsx components (.go + .gsx sites)"
```

---

## Sub-slice 4 — Editor + performance

### Task 6: Neovim — attach gsx-LSP to `.go` files (scoped to gsx projects)

**Files:**
- Modify: `~/.config/nvim/lua/plugins/gsx.lua` (the user's config — gated behind their confirmation)

**Interfaces:** none (editor config).

- [ ] **Step 1: Update the FileType autocmd**

Change the gsx-LSP `vim.lsp.start` so the client also attaches to `go` buffers in
projects that contain `.gsx` files (so it never attaches in pure-Go repos):

```lua
vim.api.nvim_create_autocmd("FileType", {
  pattern = { "gsx", "go" },
  group = grp,
  callback = function(args)
    if vim.fn.executable(gsx_bin) ~= 1 then return end
    local root = vim.fs.root(args.buf, { "go.mod", ".git" })
    -- only attach to .go buffers when the project actually has .gsx files
    if vim.bo[args.buf].filetype == "go" then
      if not root or vim.fn.glob(root .. "/**/*.gsx") == "" then return end
    end
    vim.lsp.start({ name = "gsx", cmd = { gsx_bin, "lsp" }, root_dir = root })
  end,
})
```

(The `gd` LspAttach binding already covers any buffer the gsx client attaches to.)

- [ ] **Step 2: Verify**

Reinstall the binary (`go install ./cmd/gsx`), restart Neovim, open a `.go` file
in a gsx project, confirm via `:lua =vim.tbl_map(function(c) return c.name end, vim.lsp.get_clients({bufnr=0}))`
that `gsx` is attached, and `gd` on a component jumps to its `.gsx`. (Manual
check — no automated test for editor config.)

- [ ] **Step 3: Commit**

The nvim config is the user's dotfile; do not commit it from this repo. Note the
change in the task report for the user to commit themselves.

---

### Task 7: Performance — measure on a large project (no optimization)

**Files:**
- Create: `internal/lsp/perf_test.go` (a measurement harness, run on demand)
- Create: `docs/superpowers/notes/2026-06-24-go-to-gsx-perf.md` (recorded findings)

**Interfaces:** none — this task produces **recorded numbers**, not a pass/fail gate.

- [ ] **Step 1: Build a large synthetic fixture**

Write a test helper that generates, in a `t.TempDir()`, a module with N packages
(e.g. 50), each with several `.gsx` components and a `.go` file referencing them
(e.g. 200 `.gsx` + 50 `.go` files total). Parameterize N.

- [ ] **Step 2: Measure and record**

In `perf_test.go`, a `TestPerfBaseline` guarded by an env flag (e.g. only runs
when `GSX_PERF=1`), measuring:
- `Analyze(dir, …)` latency per package, cold and warm (`testing` timing or
  `time.Since`); report min/median/max across packages.
- `definition`/`references` latency end-to-end (expect sub-millisecond — index
  lookups).
- retained memory: `runtime.ReadMemStats` before/after analyzing all packages and
  holding the `*lsp.Package` results; report the cross-index size vs total.

Record the numbers and the dominant cost in
`docs/superpowers/notes/2026-06-24-go-to-gsx-perf.md`, with a short conclusion:
is any optimization warranted now, and if so which (debounce / LRU cap on
`s.pkgs` / slim the `.gsx`-side full-`Info` retention / lazy `.go` attach)?

- [ ] **Step 3: Commit**

```bash
git add internal/lsp/perf_test.go docs/superpowers/notes/2026-06-24-go-to-gsx-perf.md
git commit -m "perf(lsp): measurement harness + recorded baseline for .go->.gsx on large projects"
```

---

## Out of scope (deferred)

- Cross-package references (loading dependent packages).
- Prop-field nav (`CardProps.Title` → the `.gsx` param).
- Passing the open **`.go`** buffer through the `go/packages` overlay for unsaved
  edits (this slice reads saved `.go` from disk; note in Task 4).
- Any optimization the perf task does not justify.

## Self-Review

**Spec coverage** (against `2026-06-24-gsx-lsp-go-to-gsx-design.md`):
- §1 in-memory/no-`.x.go` → Task 4 asserts no `.x.go` written. ✓
- §1 slim cross-index, query never touches full `Info` → Tasks 2–5 (handlers use only `CrossIndex`). ✓
- §4 cross-index (Decl + Refs, built once, discard inputs) → Task 2. ✓
- §5 component nav → Task 4. §6 references (bidirectional, in-package) → Task 5. ✓
- §7 changes (codegen index; lsp `Package`+lifecycle+handlers; editor) → Tasks 1–6. ✓
- §8 performance measurement → Task 7. ✓
- §9 testing (nav e2e + no-`.x.go`; references e2e; scoping) → Tasks 4–5; scoping covered by the empty-CrossIndex guard. ✓
- §10 risks (memory) → Task 7 measures. ✓

**Placeholder scan:** no "TBD"/"handle errors". `CrossRef.Name` is defined upfront (Task 2) and used by `posCoversCursor` (Tasks 4–5). The `writeFile`-redeclare and `.gsx`-cursor-fallback NOTEs are concrete contingencies, not gaps. Task 4 flags that passing the open `.go` buffer through the overlay is deferred (tests use saved `.go`) — an explicit, recorded scope cut, not a placeholder.

**Type consistency:** `CrossRef{Decl token.Position; Refs []token.Position; Name string}` is identical in codegen (Task 2), lsp (Task 3), and consumed in Tasks 4–5 (`gen` converts). `crossIndex` keys are `componentKey` strings (".Name" / "Recv.Name") in both codegen and the handlers. `locationForPos`/`lineStartOffset`/`posCoversCursor` are defined once (Task 4/5) and reused. ✓
