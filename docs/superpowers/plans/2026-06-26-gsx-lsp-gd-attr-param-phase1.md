# gsx LSP — `gd` on attribute name → component param (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `gd` on a component-invocation attribute name (e.g. `comments` in `<CommentsList comments={…}/>`) jumps to the matching component parameter (`comments` in `component CommentsList(comments []store.Comment)`).

**Architecture:** A new dispatch case in the LSP definition handler. When the cursor sits on a same-package function-component tag's attribute name, find that component's declaration in the in-memory gsx AST (`pkg.Files`), parse its raw `Params` string with `go/parser`, match the attribute to a parameter by the default exported-field rule, and return the parameter's source position. Entirely in `internal/lsp` + stdlib; no parser/`ast` change, no `internal/codegen` import, no reliance on generated `.x.go`.

**Tech Stack:** Go 1.26.1; `internal/lsp` (`definition.go`, new `definition_attr.go`); gsx `ast` package (`gsxast.Inspect`, `Element`, `Component`, `Attr`); stdlib `go/parser`, `go/ast`, `go/token`, `unicode`, `strings`. E2e via `gen` package `runLSP` over JSON-RPC.

This is **Phase 1** of `docs/superpowers/specs/2026-06-26-gsx-lsp-gd-attr-and-cross-pkg-design.md` (gap A). Phase 2 (cross-package tags + attrs, B/C) is a separate plan after this ships and is reviewed.

## Global Constraints

- `internal/lsp` must **NOT** import `internal/codegen`. All new code is in `internal/lsp` (+ a `gen` e2e test).
- The gsx runtime is **standard-library only**; `internal/lsp` may use `go/parser`/`go/ast`/`go/token`/`unicode` (stdlib) and `golang.org/x/tools` is not needed here.
- **`.x.go`-independent / in-memory:** resolution must use the in-memory parsed gsx AST (`pkg.Files`), never read a generated `.x.go`. (Phase-1 code only touches `pkg.Files`, satisfying this.)
- **Best-effort, never panics:** any miss (no component, empty/unparseable `Params`, no matching param, cursor not on an attr name) returns `false`/null `gd` — never a crash.
- **Attr→param match rule:** default exported-field rule `firstUpper(attrName) == firstUpper(paramName)` (so `comments`↔`comments`, `title`↔`Title`). Custom `WithFieldMatcher` is out of scope.
- Module-resolution e2e tests (those calling `runLSP`, which runs `go/packages.Load`) must be guarded: `if testing.Short() { t.Skip("skipping module-resolution test in -short mode") }`.
- Commit messages must end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Prefer unexported identifiers.

---

## File Structure

- **`internal/lsp/definition_attr.go`** (create) — the new feature, one responsibility (attr-name → param resolution): `componentAttrParamAt` (AST walk + dispatch helper), `paramOffsetIn` (pure `go/parser` param-string → offset), and small helpers `isSimpleComponentTag`, `attrName`, `findComponentDecl`, `firstUpper`.
- **`internal/lsp/definition.go`** (modify) — add one dispatch block in `handleDefinition`, after the D2 `componentTagDeclAt` check (definition.go:113-116) and before `exprNodeAtOffset` (definition.go:118).
- **`internal/lsp/definition_attr_test.go`** (create) — fast unit tests for `paramOffsetIn` and `firstUpper` (no module load; pure functions).
- **`gen/definition_attr_e2e_test.go`** (create) — real-analyzer e2e: drive `gd` on an attribute name through `runLSP` (JSON-RPC), like `gen/definition_e2e_test.go`.

---

## Task 1: `paramOffsetIn` — locate a parameter in a raw param string

The pure, independently-testable core: given a component's raw `Params` source and an attribute name, return the byte offset (within `Params`) of the matching parameter's name.

**Files:**
- Create: `internal/lsp/definition_attr.go`
- Test: `internal/lsp/definition_attr_test.go`

**Interfaces:**
- Consumes: stdlib `go/parser`, `go/ast`, `go/token`, `unicode`.
- Produces (for Task 2): `paramOffsetIn(params, attr string) (int, bool)` and `firstUpper(s string) string`.

- [ ] **Step 1: Write the failing unit tests**

Create `internal/lsp/definition_attr_test.go`:

```go
package lsp

import "testing"

func TestFirstUpper(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"comments", "Comments"},
		{"Title", "Title"},
		{"", ""},
		{"x", "X"},
	} {
		if got := firstUpper(c.in); got != c.want {
			t.Errorf("firstUpper(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestParamOffsetIn(t *testing.T) {
	tests := []struct {
		params, attr string
		wantOff      int
		wantOK       bool
	}{
		{"comments []store.Comment", "comments", 0, true},
		{"title string, featured bool", "featured", 14, true}, // "title string, " is 14 bytes
		{"a, b string", "b", 3, true},                         // grouped params: a=0, ", "=1..2, b=3
		{"Title string", "title", 0, true},                    // firstUpper match (attr lower, param upper)
		{"x int", "y", 0, false},                              // no matching param
		{"", "x", 0, false},                                   // no params
		{"][", "x", 0, false},                                 // unparseable → false, no panic
	}
	for _, tc := range tests {
		off, ok := paramOffsetIn(tc.params, tc.attr)
		if ok != tc.wantOK || (ok && off != tc.wantOff) {
			t.Errorf("paramOffsetIn(%q,%q)=(%d,%v) want (%d,%v)",
				tc.params, tc.attr, off, ok, tc.wantOff, tc.wantOK)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lsp/ -run 'TestFirstUpper|TestParamOffsetIn' -count=1`
Expected: COMPILE FAIL — `firstUpper` and `paramOffsetIn` are undefined.

- [ ] **Step 3: Implement `paramOffsetIn` + `firstUpper`**

Create `internal/lsp/definition_attr.go`:

```go
package lsp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"unicode"
)

// firstUpper returns s with its first rune upper-cased (the gsx exported-field
// rule: attr name `title` ↔ field/param `Title`). "" stays "".
func firstUpper(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// paramOffsetIn parses a gsx component's raw parameter-list source (e.g.
// "comments []store.Comment" or grouped "a, b string") with go/parser and
// returns the byte offset, WITHIN params, of the name of the parameter matching
// attr under the default exported-field rule firstUpper(name)==firstUpper(attr).
// ok is false when params is empty, unparseable, or has no matching parameter —
// the caller falls through to a null definition. It never panics.
func paramOffsetIn(params, attr string) (int, bool) {
	if strings.TrimSpace(params) == "" {
		return 0, false
	}
	const prefix = "package p\nfunc _("
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", prefix+params+"){}", 0)
	if err != nil {
		return 0, false
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok {
			fn = f
			break
		}
	}
	if fn == nil || fn.Type.Params == nil {
		return 0, false
	}
	// params starts immediately after the prefix, so a name's offset within
	// params is its offset in the synthetic source minus len(prefix).
	want := firstUpper(attr)
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if firstUpper(name.Name) == want {
				return fset.Position(name.Pos()).Offset - len(prefix), true
			}
		}
	}
	return 0, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/lsp/ -run 'TestFirstUpper|TestParamOffsetIn' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/definition_attr.go internal/lsp/definition_attr_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): paramOffsetIn — locate a component param in a raw param string

Pure go/parser-based resolution of a parameter's byte offset within a gsx
component's raw Params source, matched to an attribute name by the default
exported-field rule (firstUpper). Foundation for gd on attribute names.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Task 2: `componentAttrParamAt` + dispatch — `gd` on an attribute name

Wire the AST walk and dispatch: detect the cursor on a same-package function-component tag's attribute name, resolve the component decl, and return the param position via Task 1's `paramOffsetIn`.

**Files:**
- Modify: `internal/lsp/definition_attr.go` (add `componentAttrParamAt` + helpers)
- Modify: `internal/lsp/definition.go` (dispatch block in `handleDefinition`)
- Test: `gen/definition_attr_e2e_test.go` (create)

**Interfaces:**
- Consumes: `paramOffsetIn`, `firstUpper` (Task 1); `Package{Files map[string]*gsxast.File, GSXFset *token.FileSet}`; `gsxast.Inspect`, `gsxast.Element{Tag string, Attrs []gsxast.Attr}`, `gsxast.Component{Recv, Name, Params string, ParamsPos token.Pos}`, `gsxast.File{Decls []gsxast.Decl}`; the attr concrete types `*gsxast.ExprAttr/StaticAttr/BoolAttr/MarkupAttr/JSAttr` (each has `Name` and `Pos()`); `(*Server).locationForPos(token.Position) Location`; `pkg.GSXFset.Position(token.Pos).Offset`.
- Produces: `componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool)`.

- [ ] **Step 1: Write the failing e2e test**

Create `gen/definition_attr_e2e_test.go` (mirrors `gen/definition_e2e_test.go`'s harness — temp module, `runLSP` over JSON-RPC, `definitionResult` helper which already exists in that package):

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
)

// gd on the `comments` attribute name in `<CommentsList comments={nil}/>`
// resolves to the `comments` parameter of `component CommentsList(comments []Comment)`.
func TestDefinitionAttrParam(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("types.go", "package x\n\ntype Comment struct{ Body string }\n")
	src := "package x\n\ncomponent CommentsList(comments []Comment) {\n\t<ul></ul>\n}\n\ncomponent Page() {\n\t<CommentsList comments={nil}/>\n}\n"
	must("comp.gsx", src)
	uri := "file://" + filepath.Join(dir, "comp.gsx")

	// Cursor on the 'c' of the `comments` ATTRIBUTE (the one followed by '={').
	lines := strings.Split(src, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "comments={"); c >= 0 {
			line, character = i, c
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
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "comp.gsx") {
		t.Fatalf("resolved to %q, want comp.gsx", loc.URI)
	}
	// The decl line is "component CommentsList(comments []Comment) {" (line index 2).
	// Expect the cursor to land on the `comments` PARAMETER there.
	declLine := 2
	declCol := strings.Index(lines[declLine], "comments")
	if loc.Range.Start.Line != declLine || loc.Range.Start.Character != declCol {
		t.Fatalf("landed at L%d:C%d, want L%d:C%d (the comments param)",
			loc.Range.Start.Line, loc.Range.Start.Character, declLine, declCol)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./gen/ -run TestDefinitionAttrParam -count=1`
Expected: FAIL with `definition returned null`. The test references only existing symbols (`runLSP`, `config{}`, `definitionResult`), so it compiles and runs; `gd` on the attribute name returns null because no dispatch case handles it yet. That null is the expected RED.

- [ ] **Step 3: Implement `componentAttrParamAt` + helpers**

Append to `internal/lsp/definition_attr.go` (add `"go/token"` is already imported; add the gsx ast import):

```go
// (add to the existing import block:)
//   gsxast "github.com/gsxhq/gsx/ast"

// componentAttrParamAt resolves a cursor on a same-package function-component
// tag's attribute NAME to that component's matching parameter position. It walks
// the in-memory gsx AST (pkg.Files) — never the generated .x.go. Returns false
// when the cursor is not on such an attribute name, the component or a matching
// param can't be found, or the param list is unparseable.
func componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return token.Position{}, false
	}
	var tag, attr string
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false // already found
		}
		el, ok := n.(*gsxast.Element)
		if !ok || !isSimpleComponentTag(el.Tag) {
			return true
		}
		for _, a := range el.Attrs {
			name, ok := attrName(a)
			if !ok || name == "" {
				continue
			}
			start := pkg.GSXFset.Position(a.Pos()).Offset
			if off >= start && off < start+len(name) {
				tag, attr = el.Tag, name
				return false
			}
		}
		return true
	})
	if tag == "" {
		return token.Position{}, false
	}
	comp := findComponentDecl(pkg, tag)
	if comp == nil || !comp.ParamsPos.IsValid() {
		return token.Position{}, false
	}
	rel, ok := paramOffsetIn(comp.Params, attr)
	if !ok {
		return token.Position{}, false
	}
	return pkg.GSXFset.Position(comp.ParamsPos + token.Pos(rel)), true
}

// isSimpleComponentTag reports whether tag is a same-package function-component
// tag (non-empty, undotted, upper-initial) — the inverse of the dotted/lowercase
// exclusion in componentTagDeclAt. Dotted (cross-package) tags are Phase 2.
func isSimpleComponentTag(tag string) bool {
	return tag != "" && !strings.Contains(tag, ".") && tag[0] >= 'A' && tag[0] <= 'Z'
}

// attrName returns the attribute's name and true for the named attr kinds; a
// SpreadAttr (no name) returns ("", false).
func attrName(a gsxast.Attr) (string, bool) {
	switch t := a.(type) {
	case *gsxast.ExprAttr:
		return t.Name, true
	case *gsxast.StaticAttr:
		return t.Name, true
	case *gsxast.BoolAttr:
		return t.Name, true
	case *gsxast.MarkupAttr:
		return t.Name, true
	case *gsxast.JSAttr:
		return t.Name, true
	default:
		return "", false
	}
}

// findComponentDecl returns the function-component (no receiver) named name from
// any .gsx file in the package, or nil.
func findComponentDecl(pkg *Package, name string) *gsxast.Component {
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" && c.Name == name {
				return c
			}
		}
	}
	return nil
}
```

Update the import block at the top of `internal/lsp/definition_attr.go` to:

```go
import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"unicode"

	gsxast "github.com/gsxhq/gsx/ast"
)
```

- [ ] **Step 4: Wire the dispatch in `handleDefinition`**

In `internal/lsp/definition.go`, in `handleDefinition`, immediately after the D2 block (the `if decl, ok := componentTagDeclAt(...)` at lines 113-116) and before `node, exprPos := exprNodeAtOffset(...)` (line 118), insert:

```go
	// A: cursor on a component-invocation attribute name → the matching component
	// parameter (same-package function components; cross-package is Phase 2).
	if dp, ok := componentAttrParamAt(pkg, path, off); ok {
		return s.reply(f.ID, s.locationForPos(dp))
	}
```

- [ ] **Step 5: Run the e2e test to verify it passes**

Run: `go test ./gen/ -run TestDefinitionAttrParam -count=1`
Expected: PASS.

- [ ] **Step 6: Run the lsp + gen suites to confirm no regression**

Run: `go test ./internal/lsp/ ./gen/ -count=1`
Expected: PASS. (Existing `gd` e2e — D1/D2/D3, pipe-nav — are unaffected: the new case only fires when the cursor is on an attribute name, which previously returned null.)

- [ ] **Step 7: Commit**

```bash
git add internal/lsp/definition_attr.go internal/lsp/definition.go gen/definition_attr_e2e_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): gd on a component attribute name jumps to the component param

A new definition dispatch case: when the cursor is on a same-package
function-component tag's attribute name (e.g. `comments` in
<CommentsList comments={…}/>), resolve the component decl from the in-memory
gsx AST and jump to the matching parameter (firstUpper match) via
paramOffsetIn. .x.go-independent; internal/lsp only.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Self-Review

**1. Spec coverage** (against the design's Phase 1 / gap A, §3):
- §3.1 dispatch after D2, before `exprNodeAtOffset` → Task 2 Step 4. ✓
- §3.2 `componentAttrParamAt` (walk Elements, same-pkg simple tag, attr-name span via `a.Pos()`+`len(name)`, find decl, return position) → Task 2 Step 3. ✓
- §3.3 `paramOffsetIn` (go/parser parse of `Params`, `firstUpper` match, offset from `ParamsPos`) → Task 1 + Task 2's `comp.ParamsPos + rel`. ✓
- Named-attr kinds incl. `ExprAttr/StaticAttr/BoolAttr/MarkupAttr/JSAttr`, `SpreadAttr` excluded → `attrName`. ✓
- `.x.go`-independent (only `pkg.Files`), best-effort (every miss → `false`), no `internal/codegen` import → constraints honored. ✓
- Testing §7: `paramOffsetIn` unit table (incl. grouped params, firstUpper case, malformed→false) → Task 1; attr→param e2e (real `.gd`, asserts the exact param line/col, not null, not `.x.go`) → Task 2; cursor-not-on-attr falls through (covered by existing D1/D2 e2e staying green) → Task 2 Step 6. ✓

**2. Placeholder scan:** No TBD/TODO; every code step shows complete code; every run step has the exact command + expected outcome.

**3. Type consistency:** `paramOffsetIn(params, attr string) (int, bool)`, `firstUpper(s string) string`, `componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool)`, `isSimpleComponentTag(string) bool`, `attrName(gsxast.Attr) (string, bool)`, `findComponentDecl(*Package, string) *gsxast.Component` are used identically across tasks. `comp.ParamsPos + token.Pos(rel)` matches `paramOffsetIn`'s `int` return. `runLSP(…, config{}, nil)` matches the post-merge 5-arg signature. `definitionResult` and the `Location.Range.Start.{Line,Character}` shape match the existing `gen/definition_e2e_test.go` harness.
