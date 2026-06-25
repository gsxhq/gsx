# LSP Hover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `textDocument/hover` in a `.gsx` file shows the gopls-style Go signature/type of the symbol or expression under the cursor (and the component signature on a `<Card/>` tag).

**Architecture:** Hover reuses go-to-definition's reverse-mapper (`exprNodeAtOffset` → `ExprMap` → relative byte offset → `innermostIdent`) but returns the symbol's *type* (`types.ObjectString`/`types.TypeString`) instead of its location. Reads only the already-retained analysis (`Info`, `ExprMap`, `Files`); the one new analysis value is the analyzed `*types.Package` (for a clean qualifier).

**Tech Stack:** Go, `go/types` (ObjectString/TypeString/Qualifier), the existing `internal/lsp` server + `gen` e2e harness (`runLSP`).

## Global Constraints

- `.go` files → hover returns null (gopls owns `.go`; same stance as diagnostics/formatting).
- Hover renders a markdown fenced ` ```go ` block via `types.ObjectString(obj, qf)` / `types.TypeString(t, qf)`; `qf` renders the analyzed package's own types unqualified and imported types by package name.
- Reuse the definition reverse-mapper verbatim; do NOT add a new bridge. Piped expressions (`{ x |> f }`) → null (the byte-identical bridge does not hold), mirroring definition.
- No `.x.go` guard (hover shows a type, never navigates — synthesized bindings resolving to a real type are fine).
- Every lookup is nil-checked; hover never panics on a malformed/mid-edit buffer (returns null on any miss).
- Commit messages end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Module-resolution tests are guarded by `testing.Short()`.

---

## File Structure

- `internal/codegen/batch.go` — add `PackageResult.Types *types.Package`; set `res.Types = pkg.Types`.
- `gen/lsp.go` — copy `pr.Types` onto the returned `lsp.Package`.
- `internal/lsp/analysis.go` — add `Package.Types *types.Package`.
- `internal/lsp/protocol.go` — `Hover`, `MarkupContent`, `serverCapabilities.HoverProvider`.
- `internal/lsp/server.go` — dispatch `textDocument/hover`; advertise `hoverProvider`.
- `internal/lsp/hover.go` — **new**: `handleHover` + helpers (`qualifierFor`, `markdownGo`, `exprText`, `rangeForSpan`, `positionForByteOffset`); Task 2 adds `componentAtTag` + `renderComponentSig`.
- `gen/hover_e2e_test.go` — **new**: e2e tests via `runLSP`.

---

## Task 1: Expression hover + wiring

**Files:**
- Modify: `internal/codegen/batch.go` (PackageResult.Types + set it)
- Modify: `gen/lsp.go` (copy Types onto lsp.Package)
- Modify: `internal/lsp/analysis.go` (Package.Types)
- Modify: `internal/lsp/protocol.go` (Hover/MarkupContent/HoverProvider)
- Modify: `internal/lsp/server.go` (dispatch + capability)
- Create: `internal/lsp/hover.go`
- Create: `gen/hover_e2e_test.go`

**Interfaces:**
- Produces (Task 2 consumes): `handleHover` (the dispatch target), `qualifierFor(pkg *Package) types.Qualifier`, `markdownGo(s string) MarkupContent`, `rangeForSpan(text string, startOff, endOff int, enc encoding) Range`, `Hover`, `MarkupContent`, `Package.Types`.
- Consumes (already exist): `exprNodeAtOffset(pkg, path, off) (gsxast.Node, token.Pos)`, `hasPipeStages(node) bool`, `innermostIdent(expr ast.Expr, pos token.Pos) *ast.Ident`, `byteOffsetForPosition`, `charForByteCol`, `s.docs.text`, `s.pkgs`, `s.reply`, `uriToPath`.

- [ ] **Step 1: Write the failing e2e tests**

Create `gen/hover_e2e_test.go`:

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

// hoverModule writes a temp module (card.gsx + user.go) and returns dir + the
// card.gsx source.
func hoverModule(t *testing.T) (dir, cardSrc string) {
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
	must("go.mod", "module example.com/h\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("user.go", "package h\n\ntype User struct {\n\tName  string\n\tEmail string\n}\n\nfunc Greeting(name string) string {\n\treturn \"Hello, \" + name\n}\n")
	cardSrc = "package h\n\ncomponent Card(u User) {\n\t<div>{ u.Name }</div>\n\t<p>{ Greeting(u.Email) }</p>\n\t<i>{ \"hi\" }</i>\n}\n"
	must("card.gsx", cardSrc)
	return dir, cardSrc
}

// hoverAt opens uri with text and returns the hover result for a cursor at the
// first occurrence of needle plus cursorOff bytes into it (or nil if null).
func hoverAt(t *testing.T, dir, file, text, needle string, cursorOff int) *lsp.Hover {
	t.Helper()
	uri := "file://" + filepath.Join(dir, file)
	var line, character int
	for i, l := range strings.Split(text, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			line, character = i, c+cursorOff
			break
		}
	}
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": text}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if strings.Contains(out.String(), ".x.go") {
		t.Fatalf("hover leaked a generated-code path; out:\n%s", out.String())
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

func TestHoverField(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "{ u.Name }", len("{ u."))
	if h == nil || !strings.Contains(h.Contents.Value, "field Name string") {
		t.Fatalf("want 'field Name string', got %+v", h)
	}
	if !strings.Contains(h.Contents.Value, "```go") {
		t.Fatalf("want a go fenced block, got %q", h.Contents.Value)
	}
}

func TestHoverVar(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "{ u.Name }", len("{ ")) // on 'u'
	if h == nil || !strings.Contains(h.Contents.Value, "var u User") {
		t.Fatalf("want 'var u User', got %+v", h)
	}
}

func TestHoverFunc(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "Greeting(u.Email)", 0) // on 'Greeting'
	if h == nil || !strings.Contains(h.Contents.Value, "func Greeting(name string) string") {
		t.Fatalf("want 'func Greeting(name string) string', got %+v", h)
	}
}

func TestHoverWholeExprType(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, `{ "hi" }`, len("{ ")) // on the string literal
	if h == nil || !strings.Contains(h.Contents.Value, "string") {
		t.Fatalf("want type 'string' for the literal expression, got %+v", h)
	}
}

func TestHoverGoFileNull(t *testing.T) {
	dir, _ := hoverModule(t)
	goSrc, _ := os.ReadFile(filepath.Join(dir, "user.go"))
	h := hoverAt(t, dir, "user.go", string(goSrc), "Greeting", 0)
	if h != nil {
		t.Fatalf("hover on a .go file must be null (gopls owns it), got %+v", h)
	}
}

func TestHoverNonExprNull(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "<div>", 1) // on the 'div' tag text, not an expr
	if h != nil {
		t.Fatalf("hover on plain markup must be null, got %+v", h)
	}
}

func TestHoverPipedNull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/h\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("user.go", "package h\n\ntype User struct {\n\tName string\n}\n")
	src := "package h\n\ncomponent Card(u User) {\n\t<div>{ u.Name |> upper }</div>\n}\n"
	must("card.gsx", src)
	h := hoverAt(t, dir, "card.gsx", src, "{ u.Name", len("{ u.")) // on 'Name' inside a piped expr
	if h != nil {
		t.Fatalf("hover on a piped expression must be null, got %+v", h)
	}
}

func TestHoverCapabilityAdvertised(t *testing.T) {
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"hoverProvider":true`) {
		t.Fatalf("initialize did not advertise hoverProvider:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen/ -run TestHover -count=1`
Expected: build/compile failure — `lsp.Hover` undefined (and the handler/capability not present).

- [ ] **Step 3: Expose the analyzed `*types.Package`**

In `internal/codegen/batch.go`, add a field to `PackageResult` (after `UnusedImports`):

```go
	// Types is the analyzed package's go/types.Package, retained for the LSP
	// (e.g. hover's qualifier). nil when the package failed before type-checking.
	Types *types.Package
```

Set it next to `res.Info` (currently around line 308, `res.Info = pkg.TypesInfo`):

```go
		res.Info = pkg.TypesInfo
		res.Types = pkg.Types
```

(`types` is already imported in `batch.go`.)

In `internal/lsp/analysis.go`, add to the `Package` struct (after `Info`):

```go
	Types      *types.Package
```

(`go/types` is already imported — `Info *types.Info`.)

In `gen/lsp.go`, add `Types: pr.Types,` to the returned `&lsp.Package{...}` literal (alongside `Info: pr.Info,`).

- [ ] **Step 4: Add protocol types + capability**

In `internal/lsp/protocol.go`, add after the `TextEdit` type:

```go
// Hover is the textDocument/hover result. Range (the span the editor highlights)
// is optional.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// MarkupContent is LSP markup content; Kind is "markdown" or "plaintext".
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
```

Add `HoverProvider` to `serverCapabilities`:

```go
type serverCapabilities struct {
	PositionEncoding           string `json:"positionEncoding"`
	TextDocumentSync           int    `json:"textDocumentSync"`
	DefinitionProvider         bool   `json:"definitionProvider"`
	ReferencesProvider         bool   `json:"referencesProvider"`
	DocumentFormattingProvider bool   `json:"documentFormattingProvider"`
	HoverProvider              bool   `json:"hoverProvider"`
}
```

- [ ] **Step 5: Wire dispatch + advertise the capability**

In `internal/lsp/server.go`, add a case in `handle` (after `textDocument/references`):

```go
	case "textDocument/hover":
		return s.handleHover(f)
```

In `handleInitialize`, add `HoverProvider: true,` to the `serverCapabilities{...}` literal.

- [ ] **Step 6: Create the hover handler**

Create `internal/lsp/hover.go`:

```go
package lsp

import (
	"encoding/json"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// handleHover answers textDocument/hover for a .gsx file: it shows the Go
// type/signature of the symbol or expression under the cursor. .go files are
// gopls's to hover (null).
func (s *Server) handleHover(f frame) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	path := uriToPath(p.TextDocument.URI)
	if strings.HasSuffix(path, ".go") {
		return s.reply(f.ID, nil) // gopls owns .go hover
	}
	text, ok := s.docs.text(p.TextDocument.URI)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || pkg.Info == nil {
		return s.reply(f.ID, nil)
	}
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		return s.reply(f.ID, nil)
	}
	if hasPipeStages(node) {
		// A piped expr lowers to a wrapped call, so the byte-identical
		// relative-offset bridge does not hold and the cursor cannot be reliably
		// mapped (mirrors definition). Honest null.
		return s.reply(f.ID, nil)
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		return s.reply(f.ID, nil)
	}
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)
	qf := qualifierFor(pkg)

	// On an identifier → show the resolved object's signature.
	if id := innermostIdent(skel, skelPos); id != nil {
		obj := pkg.Info.Uses[id]
		if obj == nil {
			obj = pkg.Info.Defs[id]
		}
		if obj != nil {
			identStart := exprStart + int(id.Pos()-skel.Pos())
			rng := rangeForSpan(text, identStart, identStart+len(id.Name), s.enc)
			return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qf)), Range: &rng})
		}
	}
	// Otherwise → the whole expression's type.
	if tv, ok := pkg.Info.Types[skel]; ok && tv.Type != nil {
		rng := rangeForSpan(text, exprStart, exprStart+len(exprText(node)), s.enc)
		return s.reply(f.ID, Hover{Contents: markdownGo(types.TypeString(tv.Type, qf)), Range: &rng})
	}
	return s.reply(f.ID, nil)
}

// qualifierFor renders the analyzed package's own types unqualified and imported
// types by package name (gopls-style: `User`, `store.User`).
func qualifierFor(pkg *Package) types.Qualifier {
	return func(p *types.Package) string {
		if pkg.Types != nil && p == pkg.Types {
			return ""
		}
		return p.Name()
	}
}

// markdownGo wraps a Go signature/type string in a fenced go code block.
func markdownGo(s string) MarkupContent {
	return MarkupContent{Kind: "markdown", Value: "```go\n" + s + "\n```"}
}

// exprText returns the Go-expression source of an Interp / ExprAttr node.
func exprText(n gsxast.Node) string {
	switch e := n.(type) {
	case *gsxast.Interp:
		return e.Expr
	case *gsxast.ExprAttr:
		return e.Expr
	}
	return ""
}

// rangeForSpan converts a [startOff, endOff) byte span in text to an LSP Range
// (characters counted in the negotiated encoding).
func rangeForSpan(text string, startOff, endOff int, enc encoding) Range {
	return Range{
		Start: positionForByteOffset(text, startOff, enc),
		End:   positionForByteOffset(text, endOff, enc),
	}
}

// positionForByteOffset is the inverse of byteOffsetForPosition: a byte offset in
// text → a 0-based LSP position (character counted in enc).
func positionForByteOffset(text string, off int, enc encoding) Position {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	line := strings.Count(text[:off], "\n")
	lineStart := strings.LastIndexByte(text[:off], '\n') + 1 // 0 when no newline precedes
	char := charForByteCol(text[lineStart:off], (off-lineStart)+1, enc)
	return Position{Line: line, Character: char}
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./gen/ -run TestHover -count=1`
Expected: PASS (TestHoverField, Var, Func, WholeExprType, GoFileNull, NonExprNull, PipedNull, CapabilityAdvertised).

- [ ] **Step 8: Run the lsp + codegen package tests (no regression)**

Run: `go test ./internal/lsp/ ./internal/codegen/ ./gen/ -count=1`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/codegen/batch.go gen/lsp.go internal/lsp/analysis.go internal/lsp/protocol.go internal/lsp/server.go internal/lsp/hover.go gen/hover_e2e_test.go
git commit -m "feat(lsp): textDocument/hover — gopls-style signatures for .gsx expressions

Reuses the definition reverse-mapper to resolve the symbol/expression under the
cursor and renders its type via go/types (ObjectString/TypeString) with a
package-name qualifier. Piped exprs and .go files return null. Exposes the
analyzed *types.Package for the qualifier.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Task 2: Component-tag hover

**Files:**
- Modify: `internal/lsp/hover.go` (add `componentAtTag` + `renderComponentSig`; insert the tag check into `handleHover`)
- Modify: `gen/hover_e2e_test.go` (add the tag e2e test)
- Create: `internal/lsp/hover_test.go` (unit test for `renderComponentSig`)

**Interfaces:**
- Consumes: `markdownGo`, `rangeForSpan`, `Hover` (Task 1); `pkg.Files`, `pkg.GSXFset` (existing); `gsxast.Element`, `gsxast.Component` (`Tag`, `Name`, `Recv`, `Params`).
- Produces: `componentAtTag(pkg *Package, path string, off int) (*gsxast.Component, int, bool)` (the component + the tag-name start byte offset), `renderComponentSig(c *gsxast.Component) string`.

- [ ] **Step 1: Write the failing tests**

Append to `gen/hover_e2e_test.go`:

```go
func TestHoverComponentTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/h\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("card.gsx", "package h\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	pageSrc := "package h\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n"
	must("page.gsx", pageSrc)
	h := hoverAt(t, dir, "page.gsx", pageSrc, "<Card", len("<")) // on the 'Card' tag name
	if h == nil || !strings.Contains(h.Contents.Value, "component Card(title string)") {
		t.Fatalf("want 'component Card(title string)', got %+v", h)
	}
}
```

Create `internal/lsp/hover_test.go`:

```go
package lsp

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestRenderComponentSig(t *testing.T) {
	fn := &gsxast.Component{Name: "Card", Params: "title string"}
	if got, want := renderComponentSig(fn), "component Card(title string)"; got != want {
		t.Errorf("func component: got %q, want %q", got, want)
	}
	method := &gsxast.Component{Recv: "(p UsersPage)", Name: "Row", Params: "u User"}
	if got, want := renderComponentSig(method), "component (p UsersPage) Row(u User)"; got != want {
		t.Errorf("method component: got %q, want %q", got, want)
	}
	nullary := &gsxast.Component{Name: "Page"}
	if got, want := renderComponentSig(nullary), "component Page()"; got != want {
		t.Errorf("nullary component: got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./gen/ -run TestHoverComponentTag -count=1` and `go test ./internal/lsp/ -run TestRenderComponentSig -count=1`
Expected: FAIL — `TestHoverComponentTag` returns null (no tag handling yet); `TestRenderComponentSig` fails to compile (`renderComponentSig` undefined).

- [ ] **Step 3: Add the tag helpers**

In `internal/lsp/hover.go`, add:

```go
// componentAtTag reports whether off sits on the name of a simple (non-dotted,
// capitalized) component tag in the .gsx file, and if so returns the matching
// function component declared anywhere in the package, plus the byte offset of
// the tag name. Dotted tags (method/cross-package) are out of scope.
func componentAtTag(pkg *Package, path string, off int) (*gsxast.Component, int, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return nil, 0, false
	}
	f := pkg.Files[path]
	if f == nil {
		return nil, 0, false
	}
	tag, nameStart := "", 0
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok {
			return true
		}
		t := el.Tag
		if t == "" || strings.Contains(t, ".") || t[0] < 'A' || t[0] > 'Z' {
			return true // not a simple function-component tag
		}
		start := pkg.GSXFset.Position(el.Pos()).Offset + 1 // skip '<'
		if off >= start && off < start+len(t) {
			tag, nameStart = t, start
		}
		return true
	})
	if tag == "" {
		return nil, 0, false
	}
	for _, file := range pkg.Files {
		for _, d := range file.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Name == tag && c.Recv == "" {
				return c, nameStart, true
			}
		}
	}
	return nil, 0, false
}

// renderComponentSig renders a component declaration's signature, e.g.
// "component Card(title string)" or "component (p UsersPage) Row(u User)".
func renderComponentSig(c *gsxast.Component) string {
	var b strings.Builder
	b.WriteString("component ")
	if c.Recv != "" {
		b.WriteString(c.Recv)
		b.WriteByte(' ')
	}
	b.WriteString(c.Name)
	b.WriteByte('(')
	b.WriteString(c.Params)
	b.WriteByte(')')
	return b.String()
}
```

- [ ] **Step 4: Insert the tag check into `handleHover`**

In `internal/lsp/hover.go`, in `handleHover`, immediately after the `off := byteOffsetForPosition(...)` line and BEFORE `node, exprPos := exprNodeAtOffset(...)`, add:

```go
	// A component tag (`<Card/>`) → the component's signature.
	if c, nameStart, ok := componentAtTag(pkg, path, off); ok {
		rng := rangeForSpan(text, nameStart, nameStart+len(c.Name), s.enc)
		return s.reply(f.ID, Hover{Contents: markdownGo(renderComponentSig(c)), Range: &rng})
	}
```

- [ ] **Step 5: Run the tag tests to verify they pass**

Run: `go test ./gen/ -run TestHoverComponentTag -count=1` and `go test ./internal/lsp/ -run TestRenderComponentSig -count=1`
Expected: PASS.

- [ ] **Step 6: Run the full hover suite + package tests + full build**

Run: `go test ./internal/lsp/ ./gen/ -count=1` then `go test ./... -count=1`
Expected: PASS (all `TestHover*`, `TestRenderComponentSig`, and no regression).

- [ ] **Step 7: Commit**

```bash
git add internal/lsp/hover.go internal/lsp/hover_test.go gen/hover_e2e_test.go
git commit -m "feat(lsp): hover on a component tag shows its signature

componentAtTag resolves a simple <Card/> tag to its function component declared
anywhere in the package; renderComponentSig prints 'component Card(title string)'.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Self-Review notes (addressed)

- **Spec coverage:** content rendering (§3) → Task 1 `markdownGo`/`qualifierFor` + `types.ObjectString`/`TypeString`; the three cursor cases (§3) → Task 1 (identifier + whole-expr fallback) and Task 2 (component tag); `Range` per case (§5) → `rangeForSpan` (ident span / expr span in Task 1, tag-name span in Task 2); piped-null + `.go`-null + no-`.x.go`-guard (§6) → Task 1 handler + tests; expose `*types.Package` (§4, §8) → Task 1 Step 3; testing (§7) → both tasks' tests.
- **Type consistency:** `Package.Types`/`PackageResult.Types` (`*types.Package`) defined Task 1 Step 3, consumed by `qualifierFor` (Task 1) — names match. `componentAtTag` returns `(*gsxast.Component, int, bool)` (Task 2) and its caller in `handleHover` destructures `(c, nameStart, ok)` — match. `markdownGo`/`rangeForSpan`/`Hover` defined Task 1, reused Task 2 — match.
- **Placeholder scan:** every code step has complete code; no TBD/TODO.
- **Reuse, not re-bridge:** `handleHover` calls the existing `exprNodeAtOffset`/`hasPipeStages`/`innermostIdent` — no duplicated mapping logic.
