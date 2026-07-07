# LSP documentSymbol + workspaceSymbol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `textDocument/documentSymbol` (per-file Outline) and `workspace/symbol` (module-wide symbol search) to the gsx language server.

**Architecture:** A single pure extractor `lsp.FileSymbols(path, *gsxast.File, *token.FileSet) []Symbol` produces symbols for one parsed `.gsx` file (component decls + top-level Go decls inside `GoChunk`s). `documentSymbol` runs it over the already-warm analyzed package; `workspace/symbol` adds one `Analyzer.ModuleSymbols` method (implemented in `gen/lsp.go` by walking the warm Module and calling `FileSymbols`), caching the result like find-references.

**Tech Stack:** Go, `go/parser`/`go/ast`/`go/token`, gsx `ast`/`parser`/`codegen` packages, JSON-RPC LSP over stdio.

## Global Constraints

- Runtime (root package) stays standard-library only; tooling (`internal/lsp`, `gen`) may use `golang.org/x/tools`. This feature touches only `internal/lsp` and `gen`, so no new root-package deps.
- Go pinned to `GO_VERSION` in `.github/workflows/ci.yml` (1.26.1).
- `make ci` is the merge gate (build/vet/test both modules, gofmt, gsx fmt). For the inner loop use `make check`.
- Not a syntax/codegen change: **no** txtar corpus case, **no** sibling-repo updates (tree-sitter-gsx, vscode-gsx, docs syntax).
- No "simple heuristics" — the GoChunk position mapping is exact byte arithmetic over a verbatim source copy (confirmed: `parser/goexpr.go:241` builds `GoChunk{Src: src}` with span `[base, base+len(src))`).
- Prefer unexported identifiers; `Symbol`, `FileSymbols`, and `ModuleSymbols` are exported only because `gen` (which imports `internal/lsp`) calls them across the package boundary.

---

### Task 1: `Symbol` type + `SymbolKind` constants + `FileSymbols` (components)

**Files:**
- Create: `internal/lsp/symbols.go`
- Test: `internal/lsp/symbols_test.go`

**Interfaces:**
- Produces:
  - `type Symbol struct { Name string; Kind int; Container string; NamePos token.Position; DeclStart token.Position; DeclEnd token.Position }`
  - `func FileSymbols(path string, file *gsxast.File, fset *token.FileSet) []Symbol`
  - `SymbolKind` constants: `symKindFunction = 12`, `symKindMethod = 6`, `symKindStruct = 23`, `symKindInterface = 11`, `symKindClass = 5`, `symKindConstant = 14`, `symKindVariable = 13`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"go/token"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseGSX parses src into a gsx File and its FileSet for symbol tests.
func parseGSX(t *testing.T, name, src string) (*gsxast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, name, []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	return f, fset
}

func symByName(syms []Symbol, name string) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return Symbol{}, false
}

func TestFileSymbolsComponents(t *testing.T) {
	src := "package page\n\ncomponent Card(title string) {\n\t<div>{title}</div>\n}\n\ncomponent (f *Form) Field() {\n\t<input/>\n}\n"
	f, fset := parseGSX(t, "/m/page.gsx", src)
	syms := FileSymbols("/m/page.gsx", f, fset)

	card, ok := symByName(syms, "Card")
	if !ok {
		t.Fatalf("Card not found in %+v", syms)
	}
	if card.Kind != symKindFunction {
		t.Errorf("Card kind = %d, want %d", card.Kind, symKindFunction)
	}
	if card.Container != "page" {
		t.Errorf("Card container = %q, want %q", card.Container, "page")
	}
	if card.NamePos.Line != 3 || card.NamePos.Column != 11 {
		t.Errorf("Card NamePos = %d:%d, want 3:11", card.NamePos.Line, card.NamePos.Column)
	}

	field, ok := symByName(syms, "Field")
	if !ok {
		t.Fatalf("Field not found")
	}
	if field.Kind != symKindMethod {
		t.Errorf("Field kind = %d, want %d (method)", field.Kind, symKindMethod)
	}
	if field.Container != "Form" {
		t.Errorf("Field container = %q, want receiver type %q", field.Container, "Form")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestFileSymbolsComponents -v`
Expected: FAIL — `undefined: FileSymbols`, `undefined: symKindFunction`.

- [ ] **Step 3: Write minimal implementation**

```go
package lsp

import (
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// LSP SymbolKind numeric constants (subset gsx emits).
const (
	symKindClass     = 5
	symKindMethod    = 6
	symKindInterface = 11
	symKindFunction  = 12
	symKindVariable  = 13
	symKindConstant  = 14
	symKindStruct    = 23
)

// Symbol is one navigable declaration in a .gsx file: a component or a
// top-level Go declaration inside a GoChunk. Positions are resolved against the
// file's FileSet (byte columns, 1-based line/column).
type Symbol struct {
	Name      string
	Kind      int            // LSP SymbolKind
	Container string         // package name, or receiver type name for methods
	NamePos   token.Position // start of the name (selectionRange / workspace Location)
	DeclStart token.Position // start of the whole declaration (documentSymbol range)
	DeclEnd   token.Position // end of the whole declaration
}

// FileSymbols extracts the symbols declared in one parsed .gsx file. fset
// resolves gsx node positions (the package's GSXFset or the module-shared fset).
// A nil file yields no symbols.
func FileSymbols(path string, file *gsxast.File, fset *token.FileSet) []Symbol {
	if file == nil {
		return nil
	}
	var out []Symbol
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *gsxast.Component:
			out = append(out, componentSymbol(file, fset, decl))
		}
	}
	return out
}

func componentSymbol(file *gsxast.File, fset *token.FileSet, c *gsxast.Component) Symbol {
	kind := symKindFunction
	container := file.Package
	if c.Recv != "" {
		kind = symKindMethod
		container = receiverTypeName(c.Recv)
	}
	return Symbol{
		Name:      c.Name,
		Kind:      kind,
		Container: container,
		NamePos:   fset.Position(c.NamePos),
		DeclStart: fset.Position(c.Pos()),
		DeclEnd:   fset.Position(c.End()),
	}
}

// receiverTypeName extracts the type name from a component receiver source like
// "(f *Form)" or "(p UsersPage)" → "Form" / "UsersPage". Falls back to the raw
// trimmed text if it cannot parse the shape.
func receiverTypeName(recv string) string {
	s := strings.TrimSpace(recv)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return strings.TrimSpace(recv)
	}
	typ := fields[len(fields)-1] // last field is the type (recv may be name+type or type-only)
	return strings.TrimPrefix(typ, "*")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestFileSymbolsComponents -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/symbols.go internal/lsp/symbols_test.go
git commit -m "feat(lsp): FileSymbols extractor for component declarations"
```

---

### Task 2: `FileSymbols` — top-level Go declarations inside `GoChunk`s

**Files:**
- Modify: `internal/lsp/symbols.go`
- Test: `internal/lsp/symbols_test.go`

**Interfaces:**
- Consumes: `Symbol`, `FileSymbols`, `symKind*` from Task 1.
- Produces: `FileSymbols` now also returns symbols for `func`/`type`/`const`/`var` decls inside `*gsxast.GoChunk`.

- [ ] **Step 1: Write the failing test**

```go
func TestFileSymbolsGoChunkDecls(t *testing.T) {
	src := "package page\n\n" +
		"type Widget struct{ N int }\n\n" +
		"type Reader interface{ Read() }\n\n" +
		"type ID string\n\n" +
		"const Max = 10\n\n" +
		"var count int\n\n" +
		"func helper() int { return 1 }\n\n" +
		"func (w Widget) Size() int { return w.N }\n\n" +
		"component Card() {\n\t<div/>\n}\n"
	f, fset := parseGSX(t, "/m/page.gsx", src)
	syms := FileSymbols("/m/page.gsx", f, fset)

	cases := map[string]int{
		"Widget":  symKindStruct,
		"Reader":  symKindInterface,
		"ID":      symKindClass,
		"Max":     symKindConstant,
		"count":   symKindVariable,
		"helper":  symKindFunction,
		"Size":    symKindMethod,
		"Card":    symKindFunction,
	}
	for name, wantKind := range cases {
		s, ok := symByName(syms, name)
		if !ok {
			t.Errorf("%s not found in %+v", name, syms)
			continue
		}
		if s.Kind != wantKind {
			t.Errorf("%s kind = %d, want %d", name, s.Kind, wantKind)
		}
	}

	// Position mapping is exact: "Widget" name starts at line 3, column 6.
	w, _ := symByName(syms, "Widget")
	if w.NamePos.Line != 3 || w.NamePos.Column != 6 {
		t.Errorf("Widget NamePos = %d:%d, want 3:6", w.NamePos.Line, w.NamePos.Column)
	}
	// Method receiver becomes the container.
	size, _ := symByName(syms, "Size")
	if size.Container != "Widget" {
		t.Errorf("Size container = %q, want %q", size.Container, "Widget")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestFileSymbolsGoChunkDecls -v`
Expected: FAIL — only `Card` found (Go-chunk decls not yet extracted).

- [ ] **Step 3: Write minimal implementation**

Add the `*gsxast.GoChunk` case to the `switch` in `FileSymbols`:

```go
		case *gsxast.GoChunk:
			out = append(out, goChunkSymbols(file, fset, decl)...)
```

Add these functions and imports (`go/ast`, `go/parser`) to `symbols.go`:

```go
// goWrapPrefix wraps a GoChunk's verbatim source so go/parser accepts it as a
// file. Its byte length is subtracted when mapping parsed offsets back into the
// .gsx (the chunk's Src is the source verbatim, so offsets align 1:1).
const goWrapPrefix = "package p\n"

// goChunkSymbols parses a GoChunk's verbatim Go source and returns a Symbol for
// each top-level func/type/const/var declaration, with positions mapped back
// into the .gsx file. A chunk whose Src does not parse yields no symbols.
func goChunkSymbols(file *gsxast.File, fset *token.FileSet, gc *gsxast.GoChunk) []Symbol {
	gfset := token.NewFileSet()
	gf, err := parser.ParseFile(gfset, "chunk.go", goWrapPrefix+gc.Src, 0)
	if err != nil {
		return nil // incomplete fragment; skip (tolerant)
	}
	tf := fset.File(gc.Pos())
	if tf == nil {
		return nil
	}
	chunkOff := tf.Offset(gc.Pos())

	// mapPos converts a token.Pos in the wrapped parse to a resolved position in
	// the .gsx file via exact byte arithmetic.
	mapPos := func(p token.Pos) token.Position {
		w := gfset.Position(p).Offset
		gsxOff := chunkOff + (w - len(goWrapPrefix))
		return fset.Position(tf.Pos(gsxOff))
	}

	var out []Symbol
	for _, d := range gf.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			kind := symKindFunction
			container := file.Package
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind = symKindMethod
				container = exprTypeName(decl.Recv.List[0].Type)
			}
			out = append(out, Symbol{
				Name: decl.Name.Name, Kind: kind, Container: container,
				NamePos: mapPos(decl.Name.Pos()), DeclStart: mapPos(decl.Pos()), DeclEnd: mapPos(decl.End()),
			})
		case *ast.GenDecl:
			switch decl.Tok {
			case token.TYPE:
				for _, sp := range decl.Specs {
					ts := sp.(*ast.TypeSpec)
					out = append(out, Symbol{
						Name: ts.Name.Name, Kind: typeSpecKind(ts), Container: file.Package,
						NamePos: mapPos(ts.Name.Pos()), DeclStart: mapPos(ts.Pos()), DeclEnd: mapPos(ts.End()),
					})
				}
			case token.CONST, token.VAR:
				kind := symKindVariable
				if decl.Tok == token.CONST {
					kind = symKindConstant
				}
				for _, sp := range decl.Specs {
					vs := sp.(*ast.ValueSpec)
					for _, n := range vs.Names {
						if n.Name == "_" {
							continue
						}
						out = append(out, Symbol{
							Name: n.Name, Kind: kind, Container: file.Package,
							NamePos: mapPos(n.Pos()), DeclStart: mapPos(vs.Pos()), DeclEnd: mapPos(vs.End()),
						})
					}
				}
			}
		}
	}
	return out
}

func typeSpecKind(ts *ast.TypeSpec) int {
	switch ts.Type.(type) {
	case *ast.StructType:
		return symKindStruct
	case *ast.InterfaceType:
		return symKindInterface
	default:
		return symKindClass
	}
}

// exprTypeName extracts the base type name from a receiver type expression:
// `Widget` → "Widget", `*Widget` → "Widget", `Box[T]` → "Box".
func exprTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return exprTypeName(t.X)
	case *ast.IndexExpr:
		return exprTypeName(t.X)
	case *ast.IndexListExpr:
		return exprTypeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run 'TestFileSymbols' -v`
Expected: PASS (both Task 1 and Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/symbols.go internal/lsp/symbols_test.go
git commit -m "feat(lsp): FileSymbols extracts top-level Go decls from GoChunks"
```

---

### Task 3: `textDocument/documentSymbol` handler

**Files:**
- Modify: `internal/lsp/protocol.go` (wire types + capability field)
- Modify: `internal/lsp/server.go` (`handleInitialize`, `handle` dispatch)
- Create: `internal/lsp/documentsymbol.go`
- Test: `internal/lsp/documentsymbol_test.go`

**Interfaces:**
- Consumes: `FileSymbols`, `Symbol` (Tasks 1–2); `s.pkgs`, `s.docs`, `convertPos`, `lineAtFunc`, `uriToPath`, `pathToURI` (existing).
- Produces: `handleDocumentSymbol(f frame) error`; wire types `documentSymbolParams`, `DocumentSymbol`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"encoding/json"
	"strings"
	"testing"
)

// docSymFrame builds a textDocument/documentSymbol request frame.
func docSymFrame(id int, uri string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/documentSymbol",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
}

// symbolFileAnalyzer returns a Package built by parsing the open buffer, so
// s.pkgs[dir] carries Files + GSXFset for documentSymbol.
type symbolFileAnalyzer struct{}

func (symbolFileAnalyzer) Analyze(_ string, override map[string][]byte) (*Package, error) {
	fset := token.NewFileSet()
	files := map[string]*gsxast.File{}
	for path, src := range override {
		if f, err := gsxparser.ParseFile(fset, path, src, 0); err == nil {
			files[path] = f
		}
	}
	return &Package{GSXFset: fset, Files: files}, nil
}
func (symbolFileAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (symbolFileAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}
func (symbolFileAnalyzer) PrintWidth(string) int { return 80 }

func TestDocumentSymbol(t *testing.T) {
	uri := "file:///m/page.gsx"
	text := "package page\n\ncomponent Card() {\n\t<div/>\n}\n"
	out := drive(t, symbolFileAnalyzer{}, initFrame()+didOpenFrame(uri, text)+docSymFrame(2, uri)+exitFrame())
	if !strings.Contains(out, `"name":"Card"`) {
		t.Fatalf("documentSymbol missing Card:\n%s", out)
	}
	if !strings.Contains(out, `"selectionRange"`) {
		t.Fatalf("documentSymbol missing selectionRange:\n%s", out)
	}
}
```

Add the imports `"go/token"`, `gsxast "github.com/gsxhq/gsx/ast"`, `gsxparser "github.com/gsxhq/gsx/parser"` to this test file (json/strings/testing already listed above).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestDocumentSymbol -v`
Expected: FAIL — `ModuleSymbols` not in interface yet (compile error) **and** method not handled. (This test defines `ModuleSymbols` on its fake in anticipation of Task 4; the interface does not yet require it, so the fake simply has an extra method — that compiles. The failure is the missing handler: the reply is `method not found`.)

- [ ] **Step 3: Add wire types + capability**

In `internal/lsp/protocol.go` add:

```go
type documentSymbolParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

// DocumentSymbol is the hierarchical textDocument/documentSymbol result. gsx
// decls do not nest, so Children is always omitted.
type DocumentSymbol struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
}
```

In `serverCapabilities` add the field:

```go
	DocumentSymbolProvider     bool   `json:"documentSymbolProvider"`
```

In `handleInitialize`'s `serverCapabilities{...}` literal set `DocumentSymbolProvider: true`.

- [ ] **Step 4: Write the handler + dispatch**

Create `internal/lsp/documentsymbol.go`:

```go
package lsp

import (
	"encoding/json"
	"path/filepath"
)

// handleDocumentSymbol returns the component and top-level Go declarations in
// the requested .gsx file as a flat DocumentSymbol list (gsx decls do not
// nest). It reads from the already-analyzed package (s.pkgs); an unknown or
// unanalyzed file replies with an empty list.
func (s *Server) handleDocumentSymbol(f frame) error {
	var p documentSymbolParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || pkg.Files == nil || pkg.GSXFset == nil {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	file := pkg.Files[path]
	if file == nil {
		return s.reply(f.ID, []DocumentSymbol{})
	}
	text, _ := s.docs.text(uri) // "" if not open; positions still map best-effort
	lineAt := lineAtFunc(text)

	syms := FileSymbols(path, file, pkg.GSXFset)
	out := make([]DocumentSymbol, 0, len(syms))
	for _, sym := range syms {
		out = append(out, DocumentSymbol{
			Name:           sym.Name,
			Kind:           sym.Kind,
			Range:          Range{Start: convertPos(sym.DeclStart, lineAt, s.enc), End: convertPos(sym.DeclEnd, lineAt, s.enc)},
			SelectionRange: nameSelectionRange(sym, lineAt, s.enc),
		})
	}
	return s.reply(f.ID, out)
}

// nameSelectionRange builds the range covering just the symbol's name on its
// declaration line.
func nameSelectionRange(sym Symbol, lineAt func(int) string, enc encoding) Range {
	start := convertPos(sym.NamePos, lineAt, enc)
	endCol := sym.NamePos.Column + len(sym.Name)
	end := convertPos(tokenPosAtColumn(sym.NamePos, endCol), lineAt, enc)
	return Range{Start: start, End: end}
}

// tokenPosAtColumn returns a copy of p with the given 1-based byte column.
func tokenPosAtColumn(p tokenPosition, col int) tokenPosition {
	p.Column = col
	return p
}
```

Replace `tokenPosition` with the real type `token.Position` (add `"go/token"` import) — written here as a note so the implementer uses `token.Position`. Final code:

```go
import (
	"encoding/json"
	"go/token"
	"path/filepath"
)
// ...
func tokenPosAtColumn(p token.Position, col int) token.Position {
	p.Column = col
	return p
}
```
and change `nameSelectionRange`'s signature accordingly (it already takes `Symbol`).

In `internal/lsp/server.go`, add to the `handle` switch:

```go
	case "textDocument/documentSymbol":
		return s.handleDocumentSymbol(f)
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestDocumentSymbol -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/server.go internal/lsp/documentsymbol.go internal/lsp/documentsymbol_test.go
git commit -m "feat(lsp): textDocument/documentSymbol handler"
```

---

### Task 4: `Analyzer.ModuleSymbols` — interface, fakes, real implementation

**Files:**
- Modify: `internal/lsp/server.go` (add method to `Analyzer` interface)
- Modify: `internal/lsp/server_lifecycle_test.go`, `server_async_test.go`, `server_debounce_test.go`, `server_sync_test.go`, `references_cache_test.go` (add stub to each fake analyzer)
- Modify: `gen/lsp.go` (real implementation)
- Test: `gen/lsp_test.go`

**Interfaces:**
- Consumes: `FileSymbols`, `Symbol` (Tasks 1–2); `discoverDirs`, `moduleRoot`, `a.module`, `m.Package`, `pr.GSXFiles`, `pr.GSXFset` (existing in `gen`).
- Produces: `ModuleSymbols(dir string, override map[string][]byte) ([]Symbol, error)` on the `Analyzer` interface and on `lspAnalyzer`.

- [ ] **Step 1: Add the interface method**

In `internal/lsp/server.go`, add to the `Analyzer` interface:

```go
	// ModuleSymbols returns every symbol (component + top-level Go decl) declared
	// in every .gsx package in the module containing dir. override supplies
	// unsaved buffers (abs path -> bytes). Used by workspace/symbol.
	ModuleSymbols(dir string, override map[string][]byte) ([]Symbol, error)
```

- [ ] **Step 2: Add stubs to every fake analyzer, then run build to confirm the gap**

Add this method to each fake (matching each type's receiver style):

```go
// nilAnalyzer (server_lifecycle_test.go)
func (nilAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }

// blockingAnalyzer (server_async_test.go)
func (a *blockingAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }

// countingAnalyzer (server_debounce_test.go)
func (a *countingAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }

// fakeAnalyzer (server_sync_test.go)
func (a fakeAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }

// moduleRefsAnalyzer (references_cache_test.go)
func (a *moduleRefsAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }
```

`symbolFileAnalyzer` (documentsymbol_test.go, Task 3) already defines `ModuleSymbols`.

Run: `go build ./... && go vet ./internal/lsp/`
Expected: BUILD FAILS on `gen` — `lspAnalyzer` does not implement `lsp.Analyzer` (missing `ModuleSymbols`). This confirms Step 3 is needed. (`internal/lsp` itself builds; `gen` does not.)

- [ ] **Step 3: Write the failing test for the real implementation**

In `gen/lsp_test.go`, add (reuse the file's existing helpers for building a temp module — mirror an existing `AnalyzeModule`/`Analyze` test's setup):

```go
func TestModuleSymbols(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, filepath.Join(root, "page"), "page.gsx",
		"package page\n\ntype Widget struct{ N int }\n\ncomponent Card() {\n\t<div/>\n}\n")
	writeFile(t, filepath.Join(root, "ui"), "ui.gsx",
		"package ui\n\ncomponent Button() {\n\t<button/>\n}\n")

	a := newLSPAnalyzer(config{}, io.Discard)
	syms, err := a.ModuleSymbols(filepath.Join(root, "page"), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	for _, want := range []string{"Card", "Widget", "Button"} {
		if !names[want] {
			t.Errorf("ModuleSymbols missing %q; got %+v", want, syms)
		}
	}
}
```

`writeFile(t, dir, name, content string) string` already exists in `gen/gen_test.go` (same package). Add imports `io`, `path/filepath` if not present. Note the real analyzer needs a resolvable `github.com/gsxhq/gsx` — if `newLSPAnalyzer`/`m.Package` fails on the minimal `go.mod`, mirror the `replace` directive used in `internal/lsp/definition_test.go:45` (`require github.com/gsxhq/gsx v0.0.0` + `replace … => <repoRoot>`), computing `repoRoot` via `filepath.Abs("..")`.

Run: `go test ./gen/ -run TestModuleSymbols -v`
Expected: FAIL to compile — `a.ModuleSymbols` undefined.

- [ ] **Step 4: Implement `ModuleSymbols` in `gen/lsp.go`**

```go
// ModuleSymbols returns every symbol declared in every .gsx package in the
// module containing dir, for workspace/symbol. It reuses the warm per-root
// Module (same instance Analyze/AnalyzeModule use) and calls lsp.FileSymbols on
// each package's parsed files. Un-analyzable dirs are skipped (partial results
// tolerated). override supplies unsaved buffers (abs path -> bytes).
func (a lspAnalyzer) ModuleSymbols(dir string, override map[string][]byte) ([]lsp.Symbol, error) {
	root, modPath, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	m, err := a.module(root, modPath, merged)
	if err != nil {
		return nil, err
	}
	for p, src := range override {
		m.SetOverride(p, src)
	}
	var syms []lsp.Symbol
	for _, d := range dirs {
		pr, err := m.Package(d)
		if err != nil || pr == nil {
			continue
		}
		for path, file := range pr.GSXFiles {
			syms = append(syms, lsp.FileSymbols(path, file, pr.GSXFset)...)
		}
	}
	return syms, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./gen/ -run TestModuleSymbols -v && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/server.go internal/lsp/server_lifecycle_test.go internal/lsp/server_async_test.go internal/lsp/server_debounce_test.go internal/lsp/server_sync_test.go internal/lsp/references_cache_test.go gen/lsp.go gen/lsp_test.go
git commit -m "feat(lsp): Analyzer.ModuleSymbols walks the module for symbols"
```

---

### Task 5: `workspace/symbol` handler + caching

**Files:**
- Modify: `internal/lsp/protocol.go` (wire types + capability field)
- Modify: `internal/lsp/server.go` (`Server` fields, `handleInitialize`, `handle` dispatch, cache invalidation)
- Modify: `internal/lsp/definition.go` OR create in a new file: `locationForNameSpan` helper
- Create: `internal/lsp/workspacesymbol.go`
- Test: `internal/lsp/workspacesymbol_test.go`

**Interfaces:**
- Consumes: `Symbol`, `ModuleSymbols`, `s.docs.allOpenGSX()`, `pathToURI`, `charForByteCol`, `lineAtFunc`, `uriToPath` (existing).
- Produces: `handleWorkspaceSymbol(f frame) error`; wire types `workspaceSymbolParams`, `SymbolInformation`; `Server` fields `moduleSyms []Symbol`, `moduleSymsValid bool`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"strings"
	"testing"
)

func wsSymFrame(id int, query string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "workspace/symbol",
		"params": map[string]any{"query": query},
	})
}

// wsSymAnalyzer serves a fixed symbol list and counts ModuleSymbols calls.
type wsSymAnalyzer struct {
	calls int
	syms  []Symbol
}

func (a *wsSymAnalyzer) Analyze(string, map[string][]byte) (*Package, error) { return &Package{}, nil }
func (a *wsSymAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (a *wsSymAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	a.calls++
	return a.syms, nil
}
func (a *wsSymAnalyzer) PrintWidth(string) int { return 80 }

func TestWorkspaceSymbolQueryAndCache(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	pos := func(line, col int) token.Position {
		return token.Position{Filename: "/m/a.gsx", Line: line, Column: col}
	}
	a := &wsSymAnalyzer{syms: []Symbol{
		{Name: "Card", Kind: symKindFunction, Container: "x", NamePos: pos(3, 11), DeclStart: pos(3, 1), DeclEnd: pos(5, 2)},
		{Name: "Button", Kind: symKindFunction, Container: "x", NamePos: pos(1, 1), DeclStart: pos(1, 1), DeclEnd: pos(1, 1)},
	}}

	// Query "car" (case-insensitive substring) → only Card. Two queries with no
	// edit between → one ModuleSymbols call (cached).
	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		wsSymFrame(2, "car")+wsSymFrame(3, "car")+exitFrame())
	if !strings.Contains(out, `"name":"Card"`) {
		t.Fatalf("query 'car' should match Card:\n%s", out)
	}
	if strings.Contains(out, `"name":"Button"`) {
		t.Fatalf("query 'car' should not match Button:\n%s", out)
	}
	if a.calls != 1 {
		t.Fatalf("cached: want 1 ModuleSymbols call, got %d", a.calls)
	}

	// A didChange between two queries → two calls (cache invalidated).
	a2 := &wsSymAnalyzer{syms: a.syms}
	drive(t, a2, initFrame()+didOpenFrame(uri, text)+
		wsSymFrame(2, "")+didChangeFrame(uri, text+"\n")+wsSymFrame(3, "")+exitFrame())
	if a2.calls != 2 {
		t.Fatalf("invalidated: want 2 ModuleSymbols calls, got %d", a2.calls)
	}
}
```

Add import `"go/token"` to this test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestWorkspaceSymbol -v`
Expected: FAIL — `workspace/symbol` returns `method not found`.

- [ ] **Step 3: Add wire types + capability + server fields**

In `internal/lsp/protocol.go`:

```go
type workspaceSymbolParams struct {
	Query string `json:"query"`
}

// SymbolInformation is the workspace/symbol result entry (the flat, universally
// supported form): a named symbol with its location and containing scope.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	ContainerName string   `json:"containerName,omitempty"`
	Location      Location `json:"location"`
}
```

In `serverCapabilities` add:

```go
	WorkspaceSymbolProvider    bool   `json:"workspaceSymbolProvider"`
```

Set `WorkspaceSymbolProvider: true` in `handleInitialize`.

In `internal/lsp/server.go`, add fields to `Server`:

```go
	moduleSyms      []Symbol // whole-module symbol index (lazy; workspace/symbol)
	moduleSymsValid bool     // false ⇒ rebuild on next workspace/symbol request
```

Extend `invalidateModuleRefs` to also clear the symbol cache (it is already called on every document mutation — didOpen/didChange/didClose):

```go
func (s *Server) invalidateModuleRefs() {
	s.moduleRefs = nil
	s.moduleRefsValid = false
	s.moduleSyms = nil
	s.moduleSymsValid = false
}
```

Add the dispatch case in `handle`:

```go
	case "workspace/symbol":
		return s.handleWorkspaceSymbol(f)
```

- [ ] **Step 4: Write the handler + location helper**

Create `internal/lsp/workspacesymbol.go`:

```go
package lsp

import (
	"encoding/json"
	"os"
	"strings"
)

// handleWorkspaceSymbol returns every module-wide symbol whose name contains the
// query (case-insensitive substring; empty query returns all). The module symbol
// list is built lazily via ModuleSymbols, cached, and invalidated on any document
// mutation. On ModuleSymbols error it replies with an empty list.
func (s *Server) handleWorkspaceSymbol(f frame) error {
	var p workspaceSymbolParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []SymbolInformation{})
	}
	if !s.moduleSymsValid {
		dir := s.anyOpenDir()
		syms, err := s.analyzer.ModuleSymbols(dir, s.docs.allOpenGSX())
		if err != nil {
			return s.reply(f.ID, []SymbolInformation{})
		}
		s.moduleSyms = syms
		s.moduleSymsValid = true
	}
	q := strings.ToLower(p.Query)
	out := make([]SymbolInformation, 0, len(s.moduleSyms))
	for _, sym := range s.moduleSyms {
		if q != "" && !strings.Contains(strings.ToLower(sym.Name), q) {
			continue
		}
		out = append(out, SymbolInformation{
			Name:          sym.Name,
			Kind:          sym.Kind,
			ContainerName: sym.Container,
			Location:      s.locationForNameSpan(sym.NamePos, len(sym.Name)),
		})
	}
	return s.reply(f.ID, out)
}

// anyOpenDir returns the directory of some open document (any is fine — the
// module root is derived from it). Falls back to "." when nothing is open.
func (s *Server) anyOpenDir() string {
	for path := range s.docs.allOpenGSX() {
		return filepathDir(path)
	}
	return "."
}
```

Add the `locationForNameSpan` helper to `internal/lsp/definition.go` (next to `locationForPos`):

```go
// locationForNameSpan builds a Location covering the name (nameLen bytes) that
// begins at dp, encoding columns against the target file's on-disk text.
func (s *Server) locationForNameSpan(dp token.Position, nameLen int) Location {
	startChar, endChar := dp.Column-1, dp.Column-1+nameLen
	if data, err := os.ReadFile(dp.Filename); err == nil {
		lineText := lineAtFunc(string(data))(dp.Line)
		startChar = charForByteCol(lineText, dp.Column, s.enc)
		endChar = charForByteCol(lineText, dp.Column+nameLen, s.enc)
	}
	line := max(dp.Line-1, 0)
	return Location{URI: pathToURI(dp.Filename), Range: Range{
		Start: Position{Line: line, Character: startChar},
		End:   Position{Line: line, Character: endChar},
	}}
}
```

For `filepathDir` in `workspacesymbol.go`: use `path/filepath`'s `Dir` — import `"path/filepath"` and call `filepath.Dir(path)` directly instead of the placeholder `filepathDir`. Final imports for `workspacesymbol.go`: `"encoding/json"`, `"path/filepath"`, `"strings"`. (`os` is used only in `definition.go`; it is already imported there.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestWorkspaceSymbol -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/server.go internal/lsp/definition.go internal/lsp/workspacesymbol.go internal/lsp/workspacesymbol_test.go
git commit -m "feat(lsp): workspace/symbol handler with module-wide cache"
```

---

### Task 6: Full-suite verification + capability assertion

**Files:**
- Modify: `internal/lsp/server_sync_test.go` (or wherever the initialize-capabilities test lives — search for `DefinitionProvider` in tests)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Add a capabilities assertion test (if one does not already assert providers)**

Search: `grep -rn "definitionProvider\|DefinitionProvider" internal/lsp/*_test.go`. If an initialize-response test exists, extend it; otherwise add:

```go
func TestInitializeAdvertisesSymbolProviders(t *testing.T) {
	out := drive(t, nilAnalyzer{}, initFrame()+exitFrame())
	for _, want := range []string{`"documentSymbolProvider":true`, `"workspaceSymbolProvider":true`} {
		if !strings.Contains(out, want) {
			t.Errorf("initialize result missing %s:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run the LSP + gen package suites**

Run: `go test ./internal/lsp/ ./gen/`
Expected: PASS.

- [ ] **Step 3: Run the full merge gate**

Run: `make check`
Expected: PASS (build/vet/test both modules, gofmt, gsx fmt). Fix any gofmt drift with `gofmt -w` on touched files.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "test(lsp): assert documentSymbol/workspaceSymbol capabilities"
```

- [ ] **Step 5: Final authoritative CI run before merge**

Run: `make ci`
Expected: PASS. This is the merge gate mirrored from `.github/workflows/ci.yml`.

---

## Self-Review

**Spec coverage:**
- Shared `FileSymbols` extractor → Tasks 1–2. ✓
- Component symbols (function/method kind, container) → Task 1. ✓
- GoChunk Go-decl symbols with exact position mapping → Task 2. ✓
- `GoWithElements` skipped → Task 1/2 `switch` has no case for it (falls through), matching the documented limitation. ✓
- `textDocument/documentSymbol` (DocumentSymbol[], range + selectionRange) → Task 3. ✓
- `Analyzer.ModuleSymbols` interface + gen impl + all fakes → Task 4. ✓
- `workspace/symbol` (SymbolInformation[], substring query, module cache + invalidation) → Task 5. ✓
- Capabilities `documentSymbolProvider`/`workspaceSymbolProvider` → Tasks 3, 5; asserted in Task 6. ✓
- SymbolKind mapping table → constants in Task 1, applied in Tasks 1–2. ✓
- Testing (internal/lsp document + workspace, gen ModuleSymbols) → Tasks 3, 4, 5. ✓
- Not a syntax change (no corpus/sibling updates) → Global Constraints; gate is `make ci` (Task 6). ✓

**Placeholder scan:** The `tokenPosition`/`filepathDir` placeholders in Tasks 3 and 5 are explicitly called out with their real replacements (`token.Position`, `filepath.Dir`) in the same step. No `TBD`/`TODO` remain.

**Type consistency:** `Symbol` fields (`Name`, `Kind`, `Container`, `NamePos`, `DeclStart`, `DeclEnd`) are used identically across Tasks 1–5. `ModuleSymbols(dir string, override map[string][]byte) ([]Symbol, error)` signature matches between the interface (Task 4), the real impl (Task 4), every fake stub (Tasks 3–5), and the caller (Task 5). `FileSymbols(path, file, fset)` matches between definition (Task 1) and callers (Tasks 3, 4). `symKind*` constant names are consistent.

## Known Limitations (from spec)

- `GoWithElements` top-level Go decls are not surfaced (no `switch` case) — rare, documented.
- `workspace/symbol` uses substring (not fuzzy) matching.
- `locationForNameSpan`/`documentSymbol` encode columns against on-disk (documentSymbol: open-buffer) text; an unsaved edit can make workspace-symbol UTF-16 columns lag until save — same limitation as existing `locationForPos`.
