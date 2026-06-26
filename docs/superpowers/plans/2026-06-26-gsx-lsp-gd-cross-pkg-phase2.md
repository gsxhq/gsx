# gsx LSP — `gd` on cross-package component tags + their attributes (Phase 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `gd` on a dotted/cross-package component tag (`<components.Input>`, `<layout.PublicShell>`) jumps to that component's declaration in the *other* package's `.gsx`; and `gd` on such a tag's attribute name jumps to the cross-package component's parameter.

**Architecture:** Resolve the dotted tag's qualifier to its imported `types.Package` (via the analyzed package's `Types.Imports()`, matched by package name), look up the component object in that package's scope, derive the **dependency directory** from that object's position filename, then parse the dependency's `.gsx` files **in memory** (gsx parser) to find the component declaration — the decl position comes from `.gsx` source, never the generated `.x.go` (whose `//line` directives map body statements, not decl lines). Reuses Phase 1's `paramOffsetIn` for the attribute→param case. Entirely in `internal/lsp` + stdlib; **no analyzer change**.

**Tech Stack:** Go 1.26.1; `internal/lsp` (`definition.go`, `definition_attr.go`, new `definition_crosspkg.go`); gsx `parser.ParseFile` + `ast` package; `go/types` (`Package.Imports()`, `Scope().Lookup`). E2e via the `gen` package (`runLSP` + `gen.Generate` to produce the dependency's `.x.go`).

This is **Phase 2** of `docs/superpowers/specs/2026-06-26-gsx-lsp-gd-attr-and-cross-pkg-design.md` (gaps B + C), building on the merged Phase 1 (`componentAttrParamAt`, `paramOffsetIn`, `firstUpper`).

## Global Constraints

- `internal/lsp` must **NOT** import `internal/codegen`. All new code is in `internal/lsp` (+ a `gen` e2e test that may use `gen.Generate`).
- The gsx runtime is **standard-library only**; `internal/lsp` may use `go/types`/`go/token` (stdlib) and the gsx `parser`/`ast` packages.
- **Decl position from `.gsx` source, never `.x.go`:** the component declaration position must be obtained by parsing the dependency's `.gsx` with `parser.ParseFile`. The imported object's `.x.go` position is used **only** to locate the dependency *directory* (`filepath.Dir`), not as the result.
- **Dependency must be importable:** cross-package resolution requires the dependency package to type-check in the analyzed (importer) package — i.e. the dependency has been generated (`.x.go` on disk), as any Go import must. (Truly dep-`.x.go`-free navigation would require overlaying dependency `.gsx` skeletons in the analyzer — a separate future enhancement, out of scope.) The e2e generates the dependency with `gen.Generate` to reflect this.
- **Best-effort, never panics:** any miss (no `Types`, qualifier not an imported package, object not found, no dep dir, no `.gsx` decl, unparseable params) returns `false`/null `gd`.
- **Attr→param match rule:** the Phase 1 default `firstUpper(attr) == firstUpper(param)` via `paramOffsetIn`. Custom `FieldMatcher` out of scope.
- **Aliased imports** (`import comp "…/components"` → `<comp.Input>`): the qualifier won't match the package's `Name()`, so resolution returns false (clean null, never a wrong jump). Handling import aliases is a noted follow-up, not in this slice.
- Module-resolution e2e tests must be guarded: `if testing.Short() { t.Skip("skipping module-resolution test in -short mode") }`.
- Commit messages must end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Prefer unexported identifiers.

---

## File Structure

- **`internal/lsp/definition_crosspkg.go`** (create) — the cross-package resolver and tag dispatch: `resolveCrossPkgComponent`, `splitDottedTag`, `crossPkgTagDeclAt`.
- **`internal/lsp/definition.go`** (modify) — one dispatch block in `handleDefinition` after the same-package D2 (`componentTagDeclAt`) block, routing dotted tags to `crossPkgTagDeclAt`.
- **`internal/lsp/definition_attr.go`** (modify) — generalize `componentAttrParamAt` to also handle a dotted-tag attribute (resolve cross-package, position via the dependency's FileSet); add `isComponentTag` (accepts simple *and* dotted).
- **`gen/definition_crosspkg_e2e_test.go`** (create) — two e2e tests (tag→decl, attr→param) over a two-package temp module, generating the dependency with `gen.Generate`.

---

## Task 1: cross-package tag → declaration (gap B)

**Files:**
- Create: `internal/lsp/definition_crosspkg.go`
- Modify: `internal/lsp/definition.go` (dispatch)
- Test: `gen/definition_crosspkg_e2e_test.go` (the tag→decl test)

**Interfaces:**
- Consumes: `Package{Types *types.Package, GSXFset *token.FileSet, Files map[string]*gsxast.File}`; `gsxast.Inspect`, `gsxast.Element`, `gsxast.Component{Name, NamePos, Recv}`; `parser.ParseFile(fset, filename string, src any, mode) (*ast.File, error)` from `github.com/gsxhq/gsx/parser`; `go/types.Package.Imports() []*types.Package`, `(*types.Package).Name()/Scope()`, `(*types.Scope).Lookup(string) types.Object`; `(*Server).locationForPos`.
- Produces (for Task 2): `resolveCrossPkgComponent(pkg *Package, qualifier, name string) (*gsxast.Component, *token.FileSet, bool)`; `splitDottedTag(tag string) (qualifier, name string, ok bool)`.

- [ ] **Step 1: Write the failing e2e test**

Create `gen/definition_crosspkg_e2e_test.go`:

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

// gd on the cross-package tag `components.Input` in <components.Input .../>
// resolves to `component Input(...)` in the imported package's .gsx.
func TestDefinitionCrossPkgTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("ui/components/comp.gsx", "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n")
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"/>\n}\n"
	mk("page.gsx", page)

	// Generate the dependency so the importer type-checks against it (deps must be
	// importable; the decl position itself comes from the dep .gsx, not its .x.go).
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	uri := "file://" + filepath.Join(dir, "page.gsx")
	lines := strings.Split(page, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "components.Input"); c >= 0 {
			line, character = i, c+len("components.")+1 // a column on "Input"
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": page}}})
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
	if !strings.HasSuffix(loc.URI, filepath.Join("ui", "components", "comp.gsx")) {
		t.Fatalf("resolved to %q, want ui/components/comp.gsx", loc.URI)
	}
	// `component Input(...)` is on line index 2 of comp.gsx; expect the `Input` name.
	if loc.Range.Start.Line != 2 {
		t.Fatalf("landed on line %d, want 2 (the Input decl)", loc.Range.Start.Line)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./gen/ -run TestDefinitionCrossPkgTag -count=1`
Expected: FAIL with `definition returned null` — dotted tags are excluded by the existing `componentTagDeclAt`, and no cross-package case exists yet. (The test references only existing symbols — `runLSP`, `Generate`, `config{}`, `definitionResult` — so it compiles and runs.)

- [ ] **Step 3: Implement the cross-package resolver + tag dispatch**

Create `internal/lsp/definition_crosspkg.go`:

```go
package lsp

import (
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// splitDottedTag splits a dotted component tag "qualifier.Name" into its parts,
// requiring a single dot and an upper-initial Name (a component, not a field
// access). "components.Input" → ("components","Input",true);
// "p.Content" → ("p","Content",true) — the qualifier just won't match an import.
func splitDottedTag(tag string) (qualifier, name string, ok bool) {
	i := strings.LastIndex(tag, ".")
	if i <= 0 || i == len(tag)-1 {
		return "", "", false
	}
	qualifier, name = tag[:i], tag[i+1:]
	if strings.Contains(qualifier, ".") || name[0] < 'A' || name[0] > 'Z' {
		return "", "", false
	}
	return qualifier, name, true
}

// resolveCrossPkgComponent resolves a dotted tag's (qualifier, name) to the
// function-component declaration in the imported package's .gsx. It finds the
// imported types.Package by name, locates the dependency DIRECTORY from the
// component object's position filename (the only use of the dep's compiled
// form), then parses the dependency's .gsx files IN MEMORY to return the decl
// node and the FileSet its positions belong to. Returns false on any miss.
func resolveCrossPkgComponent(pkg *Package, qualifier, name string) (*gsxast.Component, *token.FileSet, bool) {
	if pkg == nil || pkg.Types == nil || pkg.Fset == nil {
		return nil, nil, false
	}
	var imp *types.Package
	for _, p := range pkg.Types.Imports() {
		if p.Name() == qualifier {
			imp = p
			break
		}
	}
	if imp == nil {
		return nil, nil, false
	}
	obj := imp.Scope().Lookup(name)
	if obj == nil || !obj.Pos().IsValid() {
		return nil, nil, false
	}
	depFile := pkg.Fset.Position(obj.Pos()).Filename
	if depFile == "" {
		return nil, nil, false
	}
	dir := filepath.Dir(depFile)
	matches, err := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if err != nil {
		return nil, nil, false
	}
	fset := token.NewFileSet()
	for _, m := range matches {
		f, err := gsxparser.ParseFile(fset, m, nil, 0)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" && c.Name == name {
				return c, fset, true
			}
		}
	}
	return nil, nil, false
}

// crossPkgTagDeclAt resolves a cursor on a dotted component tag NAME to that
// component's .gsx declaration in the imported package. Returns false when the
// cursor is not on such a tag or the component can't be resolved.
func crossPkgTagDeclAt(pkg *Package, path string, off int) (token.Position, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return token.Position{}, false
	}
	f := pkg.Files[path]
	if f == nil {
		return token.Position{}, false
	}
	var result token.Position
	found := false
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if found {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok || !strings.Contains(el.Tag, ".") {
			return true
		}
		nameStart := pkg.GSXFset.Position(el.Pos()).Offset + 1 // skip '<'
		if off < nameStart || off >= nameStart+len(el.Tag) {
			return true
		}
		qualifier, name, ok := splitDottedTag(el.Tag)
		if !ok {
			return true
		}
		comp, fset, ok := resolveCrossPkgComponent(pkg, qualifier, name)
		if !ok || !comp.NamePos.IsValid() {
			return true
		}
		result = fset.Position(comp.NamePos)
		found = true
		return false
	})
	return result, found
}
```

- [ ] **Step 4: Wire the dispatch in `handleDefinition`**

In `internal/lsp/definition.go`, immediately after the same-package D2 block (`if decl, ok := componentTagDeclAt(...)` … `}`) and before the Phase-1 attr block, insert:

```go
	// B: cursor on a dotted/cross-package component tag → its declaration in the
	// imported package's .gsx.
	if dp, ok := crossPkgTagDeclAt(pkg, path, off); ok {
		return s.reply(f.ID, s.locationForPos(dp))
	}
```

- [ ] **Step 5: Run the e2e test to verify it passes**

Run: `go test ./gen/ -run TestDefinitionCrossPkgTag -count=1`
Expected: PASS.

- [ ] **Step 6: Run the lsp + gen suites**

Run: `go test ./internal/lsp/ ./gen/ -count=1`
Expected: PASS (existing `gd` cases unaffected — the new case only fires on a dotted tag-name cursor, previously null).

- [ ] **Step 7: Commit**

```bash
git add internal/lsp/definition_crosspkg.go internal/lsp/definition.go gen/definition_crosspkg_e2e_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): gd on a cross-package component tag jumps to its .gsx decl

Resolve a dotted tag (<components.Input>) to the imported component's
declaration: find the imported types.Package by name, derive the dependency
dir from the component object's position, and parse the dependency's .gsx in
memory for the decl position (never the .x.go //line, which maps bodies not
decls). internal/lsp only; no analyzer change.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Task 2: cross-package attribute name → parameter (gap C)

**Files:**
- Modify: `internal/lsp/definition_attr.go` (`componentAttrParamAt` handles dotted tags; add `isComponentTag`)
- Test: `gen/definition_crosspkg_e2e_test.go` (add the attr→param test)

**Interfaces:**
- Consumes: `resolveCrossPkgComponent`, `splitDottedTag` (Task 1); `paramOffsetIn` (Phase 1); `isSimpleComponentTag` (Phase 1).
- Produces: generalized `componentAttrParamAt` (same-package and cross-package); `isComponentTag(tag string) bool`.

- [ ] **Step 1: Write the failing e2e test**

Append to `gen/definition_crosspkg_e2e_test.go`:

```go
// gd on the `name` attribute of <components.Input name="a"/> resolves to the
// `name` parameter of `component Input(name string)` in the imported package.
func TestDefinitionCrossPkgAttrParam(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	depSrc := "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n"
	mk("ui/components/comp.gsx", depSrc)
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"/>\n}\n"
	mk("page.gsx", page)
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	uri := "file://" + filepath.Join(dir, "page.gsx")
	pageLines := strings.Split(page, "\n")
	var line, character int
	for i, l := range pageLines {
		if c := strings.Index(l, "name=\""); c >= 0 {
			line, character = i, c // the 'n' of the `name` attribute
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": page}}})
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
	if !strings.HasSuffix(loc.URI, filepath.Join("ui", "components", "comp.gsx")) {
		t.Fatalf("resolved to %q, want ui/components/comp.gsx", loc.URI)
	}
	// `component Input(name string)` is line index 2; expect the `name` PARAM.
	depLines := strings.Split(depSrc, "\n")
	wantCol := strings.Index(depLines[2], "name string")
	if loc.Range.Start.Line != 2 || loc.Range.Start.Character != wantCol {
		t.Fatalf("landed at L%d:C%d, want L2:C%d (the name param)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantCol)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./gen/ -run TestDefinitionCrossPkgAttrParam -count=1`
Expected: FAIL with `definition returned null` — `componentAttrParamAt` rejects dotted tags (`isSimpleComponentTag`), so the cursor on a cross-package attribute name resolves to nothing.

- [ ] **Step 3: Generalize `componentAttrParamAt` for dotted tags**

In `internal/lsp/definition_attr.go`, add the broader tag test:

```go
// isComponentTag reports whether tag names a component invocation — a simple
// upper-initial tag (Card) or a dotted qualifier.Name (components.Input).
func isComponentTag(tag string) bool {
	if isSimpleComponentTag(tag) {
		return true
	}
	_, _, ok := splitDottedTag(tag)
	return ok
}
```

Then change `componentAttrParamAt` to (a) match component tags via `isComponentTag` during the walk, and (b) resolve the param position per tag kind. Replace the walk's tag guard and the post-walk resolution:

- In the `gsxast.Inspect` closure, change `if !ok || !isSimpleComponentTag(el.Tag)` to `if !ok || !isComponentTag(el.Tag)`.
- Replace the post-walk block (currently `comp := findComponentDecl(pkg, tag)` … `return pkg.GSXFset.Position(comp.ParamsPos + token.Pos(rel)), true`) with:

```go
	if qualifier, name, ok := splitDottedTag(tag); ok {
		// Cross-package: resolve the imported component's decl + its FileSet.
		comp, fset, ok := resolveCrossPkgComponent(pkg, qualifier, name)
		if !ok || !comp.ParamsPos.IsValid() {
			return token.Position{}, false
		}
		rel, ok := paramOffsetIn(comp.Params, attr)
		if !ok {
			return token.Position{}, false
		}
		return fset.Position(comp.ParamsPos + token.Pos(rel)), true
	}
	// Same-package function component (Phase 1).
	comp := findComponentDecl(pkg, tag)
	if comp == nil || !comp.ParamsPos.IsValid() {
		return token.Position{}, false
	}
	rel, ok := paramOffsetIn(comp.Params, attr)
	if !ok {
		return token.Position{}, false
	}
	return pkg.GSXFset.Position(comp.ParamsPos + token.Pos(rel)), true
```

(The same-package branch is byte-for-byte the existing logic; only the dotted branch is new. `attr` and `tag` are the values captured by the walk.)

- [ ] **Step 4: Run the e2e test to verify it passes**

Run: `go test ./gen/ -run TestDefinitionCrossPkgAttrParam -count=1`
Expected: PASS.

- [ ] **Step 5: Run the lsp + gen suites + the Phase-1 attr test (no regression)**

Run: `go test ./internal/lsp/ ./gen/ -run 'TestDefinitionAttrParam|TestDefinitionCrossPkg|TestParamOffsetIn|TestFirstUpper' -count=1 && go test ./internal/lsp/ ./gen/ -count=1`
Expected: PASS (the Phase-1 same-package `TestDefinitionAttrParam` still passes — the same-package branch is unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/definition_attr.go gen/definition_crosspkg_e2e_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): gd on a cross-package component attribute jumps to its param

Generalize componentAttrParamAt to dotted tags: resolve the imported
component via resolveCrossPkgComponent and locate the param with the Phase-1
paramOffsetIn, positioned in the dependency's .gsx FileSet. Same-package
behavior unchanged.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Self-Review

**1. Spec coverage** (against the design's Phase 2, §4–6):
- §4 B (dotted tag → decl via qualifier→import→dir→in-memory `.gsx` parse) → Task 1. The dep dir comes from the imported object's position filename (`filepath.Dir`), refining §4.1's "import path → dir" without the analyzer-side map the spec floated — simpler and `internal/lsp`-only. ✓
- §5 C (cross-package attr → param, reusing `paramOffsetIn`) → Task 2. ✓
- §6 invariants: decl position parsed from `.gsx` (not `.x.go`) → `resolveCrossPkgComponent` parses `*.gsx`; `internal/lsp ⊄ internal/codegen` (the e2e's `gen.Generate` is in `gen`, not imported by `lsp`); best-effort/never-panics → every miss returns false. ✓ The "dep must be generated to be importable" reality is documented in Global Constraints (a narrowing of the spec's broad `.x.go`-independence claim, which strictly holds only for the edited package). ✓
- §8 testing: tag→decl and attr→param e2e over a two-package module, decl position asserted in the dep `.gsx` (not `.x.go`), dep generated → both tasks. The aliased-import and `<p.Content/>` method-receiver edges resolve to null (no wrong jump) by construction (qualifier name-match / `splitDottedTag`). ✓

**2. Placeholder scan:** No TBD/TODO; every code step is complete; every run step has the exact command + expected outcome.

**3. Type consistency:** `resolveCrossPkgComponent(pkg *Package, qualifier, name string) (*gsxast.Component, *token.FileSet, bool)`, `splitDottedTag(string) (string, string, bool)`, `crossPkgTagDeclAt(pkg *Package, path string, off int) (token.Position, bool)`, `isComponentTag(string) bool` are used identically across tasks. `comp.ParamsPos + token.Pos(rel)` matches `paramOffsetIn`'s `int` return (Phase 1). `runLSP(…, config{}, nil)` matches the 5-arg signature. `Generate([]string) (Result, error)` and `definitionResult` are existing `gen`-package symbols.
