# gsx LSP — hover parity (attribute names + cross-package tags) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `textDocument/hover` answers on a component-invocation attribute name (→ the param's type) and on a dotted/cross-package component tag (→ the component signature), matching the go-to-definition cases shipped this session.

**Architecture:** A behavior-preserving refactor extracts the gd attr-detection walk and same/cross-package component resolution into shared helpers (`componentAttrAtOffset`, `resolveTagComponent`), then `handleHover` gains an attribute-name case (H1, via a new `paramDeclIn`) and its existing tag case is generalized to dotted tags (H2). Entirely in `internal/lsp` + stdlib; reuses `resolveCrossPkgComponent`/`findComponentDecl`/`renderComponentSig`; `.x.go`-independent.

**Tech Stack:** Go 1.26.1; `internal/lsp` (`hover.go`, `definition_attr.go`, `definition_crosspkg.go`); gsx `ast`; stdlib `go/parser`/`go/ast`/`go/token`/`go/types`. E2e via the `gen` package (`runLSP`, `hoverAt`, `Generate`).

Implements `docs/superpowers/specs/2026-06-26-gsx-lsp-hover-parity-design.md`, building on the merged gd work (`componentAttrParamAt`, `resolveCrossPkgComponent`, `splitDottedTag`, `paramOffsetIn`, `findComponentDecl`, `renderComponentSig`, `componentAtTag`).

## Global Constraints

- `internal/lsp` must **NOT** import `internal/codegen`. All new code is in `internal/lsp` (+ a `gen` e2e test).
- The gsx runtime is standard-library only; `internal/lsp` may use `go/parser`/`go/ast`/`go/token`/`go/types`.
- **`.x.go`-independent:** hover content renders from in-memory `.gsx` and the raw `Params` string; the dep `.x.go` is consulted (inside the existing `resolveCrossPkgComponent`) only to locate the dep directory and type-resolve the qualifier.
- **gd behavior must not change:** the §3 refactor is verified by the existing gd e2e tests (`TestDefinitionAttrParam`, `TestDefinitionCrossPkgTag`, `TestDefinitionCrossPkgAttrParam`) staying green.
- **Best-effort, never panics:** every miss → null hover.
- **Attr→param match rule:** the default `firstUpper(attr) == firstUpper(param)`.
- Module-resolution e2e tests guarded with `if testing.Short() { t.Skip("skipping module-resolution test in -short mode") }`.
- Commit messages must end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Prefer unexported identifiers.

---

## File Structure

- **`internal/lsp/definition_attr.go`** (modify) — extract `componentAttrAtOffset` (the attr-detection walk) and rewrite `componentAttrParamAt` to use it + `resolveTagComponent`; add `paramDeclIn`.
- **`internal/lsp/definition_crosspkg.go`** (modify) — add `resolveTagComponent` (same/cross-package resolution dispatch), next to `resolveCrossPkgComponent`.
- **`internal/lsp/definition_attr_test.go`** (modify) — add `paramDeclIn` unit table.
- **`internal/lsp/hover.go`** (modify) — generalize `componentAtTag` to dotted tags (via `resolveTagComponent`); add the H1 attribute-name hover block.
- **`gen/hover_attr_e2e_test.go`** (create) — H1 (same- and cross-package) and H2 e2e over `runLSP`.

---

## Task 1: shared-helper refactor + `paramDeclIn`

Behavior-preserving for gd; adds the pure helper hover needs. No hover wiring yet.

**Files:**
- Modify: `internal/lsp/definition_attr.go`, `internal/lsp/definition_crosspkg.go`
- Test: `internal/lsp/definition_attr_test.go`

**Interfaces:**
- Consumes: `splitDottedTag`, `resolveCrossPkgComponent`, `findComponentDecl`, `isComponentTag`, `attrName`, `paramOffsetIn`, `firstUpper`; `gsxast.Inspect`/`Element`/`Component`/`Attr`; `Package{Files, GSXFset}`; stdlib `go/parser`/`go/ast`/`go/token`/`go/types`.
- Produces (for Task 2): `componentAttrAtOffset(pkg *Package, path string, off int) (tag, attr string, attrStart int, ok bool)`; `resolveTagComponent(pkg *Package, tag string) (*gsxast.Component, *token.FileSet, bool)`; `paramDeclIn(params, attr string) (string, bool)`.

- [ ] **Step 1: Write the failing `paramDeclIn` unit test**

In `internal/lsp/definition_attr_test.go`, add:

```go
func TestParamDeclIn(t *testing.T) {
	tests := []struct {
		params, attr, want string
		ok                 bool
	}{
		{"comments []store.Comment", "comments", "comments []store.Comment", true},
		{"title string, featured bool", "featured", "featured bool", true},
		{"a, b string", "b", "b string", true},
		{"Title string", "title", "Title string", true}, // firstUpper match
		{"x int", "y", "", false},                        // no match
		{"", "x", "", false},                             // empty
		{"][", "x", "", false},                           // unparseable
	}
	for _, tc := range tests {
		got, ok := paramDeclIn(tc.params, tc.attr)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("paramDeclIn(%q,%q)=(%q,%v) want (%q,%v)",
				tc.params, tc.attr, got, ok, tc.want, tc.ok)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lsp/ -run TestParamDeclIn -count=1`
Expected: COMPILE FAIL — `paramDeclIn` undefined.

- [ ] **Step 3: Add `paramDeclIn`**

In `internal/lsp/definition_attr.go`, add (the `go/types` import is needed — add it to the import block):

```go
// paramDeclIn parses a gsx component's raw parameter-list source and returns the
// matched parameter's declaration as "name type" (e.g. "comments []store.Comment",
// grouped "b string"), matched to attr by firstUpper(name)==firstUpper(attr). ok
// is false when params is empty, unparseable, or has no matching parameter. Never
// panics.
func paramDeclIn(params, attr string) (string, bool) {
	if strings.TrimSpace(params) == "" {
		return "", false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", "package p\nfunc _("+params+"){}", 0)
	if err != nil {
		return "", false
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok {
			fn = f
			break
		}
	}
	if fn == nil || fn.Type.Params == nil {
		return "", false
	}
	want := firstUpper(attr)
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if firstUpper(name.Name) == want {
				return name.Name + " " + types.ExprString(field.Type), true
			}
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/lsp/ -run TestParamDeclIn -count=1`
Expected: PASS.

- [ ] **Step 5: Extract `componentAttrAtOffset` and add `resolveTagComponent`; rewrite `componentAttrParamAt`**

In `internal/lsp/definition_attr.go`, replace the body of `componentAttrParamAt` (the inline walk + same/cross-package branches) with calls to two new helpers. Add `componentAttrAtOffset`:

```go
// componentAttrAtOffset finds a cursor on a component-invocation attribute NAME.
// It walks the in-memory gsx AST for an element whose tag is a component (simple
// or dotted) and whose named attr's name span [attr.Pos(), +len(name)) covers off.
// Returns the tag, the attr name, and the attr-name byte start (edited-file offset).
func componentAttrAtOffset(pkg *Package, path string, off int) (tag, attr string, attrStart int, ok bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return "", "", 0, false
	}
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false
		}
		el, isEl := n.(*gsxast.Element)
		if !isEl || !isComponentTag(el.Tag) {
			return true
		}
		for _, a := range el.Attrs {
			name, named := attrName(a)
			if !named || name == "" {
				continue
			}
			start := pkg.GSXFset.Position(a.Pos()).Offset
			if off >= start && off < start+len(name) {
				tag, attr, attrStart = el.Tag, name, start
				return false
			}
		}
		return true
	})
	return tag, attr, attrStart, tag != ""
}
```

Rewrite `componentAttrParamAt` to:

```go
// componentAttrParamAt resolves a cursor on a component-invocation attribute name
// to that component's matching parameter position (same-package and cross-package).
func componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool) {
	tag, attr, _, ok := componentAttrAtOffset(pkg, path, off)
	if !ok {
		return token.Position{}, false
	}
	comp, fset, ok := resolveTagComponent(pkg, tag)
	if !ok || !comp.ParamsPos.IsValid() {
		return token.Position{}, false
	}
	rel, ok := paramOffsetIn(comp.Params, attr)
	if !ok {
		return token.Position{}, false
	}
	return fset.Position(comp.ParamsPos + token.Pos(rel)), true
}
```

In `internal/lsp/definition_crosspkg.go`, add `resolveTagComponent`:

```go
// resolveTagComponent resolves a component tag to its declaration, unifying the
// same-package and cross-package paths. It returns the component and the FileSet
// its positions belong to: pkg.GSXFset for a same-package function component, or
// the dependency's parse FileSet for a dotted/cross-package tag.
func resolveTagComponent(pkg *Package, tag string) (*gsxast.Component, *token.FileSet, bool) {
	if qualifier, name, ok := splitDottedTag(tag); ok {
		return resolveCrossPkgComponent(pkg, qualifier, name)
	}
	c := findComponentDecl(pkg, tag)
	if c == nil {
		return nil, nil, false
	}
	return c, pkg.GSXFset, true
}
```

- [ ] **Step 6: Run the gd suite to confirm the refactor preserved behavior**

Run: `go test ./internal/lsp/ ./gen/ -run 'TestParamOffsetIn|TestParamDeclIn|TestSplitDottedTag|TestIsComponentTag|TestDefinitionAttrParam|TestDefinitionCrossPkg' -count=1`
Expected: PASS — the gd attr→param and cross-package tests still pass against the refactored helpers.

- [ ] **Step 7: Full lsp + gen suites**

Run: `go test ./internal/lsp/ ./gen/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/lsp/definition_attr.go internal/lsp/definition_crosspkg.go internal/lsp/definition_attr_test.go
git commit -m "$(cat <<'EOF'
refactor(lsp): extract componentAttrAtOffset + resolveTagComponent; add paramDeclIn

Factor the gd attr-detection walk and same/cross-package component resolution
into shared helpers (behavior-preserving — gd e2e green), and add paramDeclIn
(matched param rendered as "name type") for upcoming hover parity.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Task 2: hover H1 (attr → param type) + H2 (cross-package tag → signature)

**Files:**
- Modify: `internal/lsp/hover.go`
- Test: `gen/hover_attr_e2e_test.go` (create)

**Interfaces:**
- Consumes: `componentAttrAtOffset`, `resolveTagComponent`, `paramDeclIn` (Task 1); `renderComponentSig`, `markdownGo`, `rangeForSpan`, `Hover` (existing hover.go); `splitDottedTag`, `isSimpleComponentTag` (existing).
- Produces: generalized `componentAtTag` (simple + dotted); a new H1 dispatch block in `handleHover`.

- [ ] **Step 1: Write the failing e2e tests**

Create `gen/hover_attr_e2e_test.go`:

```go
package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// H1 same-package: hover on the `comments` attribute → "comments []store.Comment".
func TestHoverAttrParamSamePkg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("types.go", "package x\n\ntype Comment struct{ Body string }\n")
	src := "package x\n\ncomponent CommentsList(comments []Comment) {\n\t<ul></ul>\n}\n\ncomponent Page() {\n\t<CommentsList comments={nil}/>\n}\n"
	mk("comp.gsx", src)

	h := hoverAt(t, dir, "comp.gsx", src, "comments={", 0)
	if h == nil || !strings.Contains(h.Contents.Value, "comments []Comment") {
		t.Fatalf("hover on attr name = %+v, want contains 'comments []Comment'", h)
	}
}

// H1 cross-package + H2: hover on `name` attr → "name string"; hover on the
// `components.Input` tag → "component Input(".
func TestHoverCrossPkgAttrAndTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("ui/components/comp.gsx", "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n")
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"/>\n}\n"
	mk("page.gsx", page)
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	// H1 cross-package: cursor on the `name` attribute.
	h := hoverAt(t, dir, "page.gsx", page, "name=\"a\"", 0)
	if h == nil || !strings.Contains(h.Contents.Value, "name string") {
		t.Fatalf("cross-pkg attr hover = %+v, want 'name string'", h)
	}
	// H2: cursor on the `Input` of `components.Input`.
	h = hoverAt(t, dir, "page.gsx", page, "components.Input", len("components.")+1)
	if h == nil || !strings.Contains(h.Contents.Value, "component Input(") {
		t.Fatalf("cross-pkg tag hover = %+v, want 'component Input('", h)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./gen/ -run 'TestHoverAttrParamSamePkg|TestHoverCrossPkgAttrAndTag' -count=1`
Expected: FAIL — attr-name hover returns nil (no case), and the dotted-tag hover returns nil (`componentAtTag` excludes dotted tags). The tests reference only existing symbols (`hoverAt`, `Generate`), so they compile.

- [ ] **Step 3: Generalize `componentAtTag` for dotted tags (H2)**

In `internal/lsp/hover.go`, replace `componentAtTag`'s body so it accepts dotted tags and resolves via `resolveTagComponent`. It now also returns the tag length (so the hover range covers the full tag, including the `pkg.` qualifier of a dotted tag):

```go
// componentAtTag reports whether off sits on the name of a component tag (simple
// <Card/> or dotted <pkg.Comp/>) and returns the resolved component declaration,
// the byte offset of the tag name, and the tag length. Cross-package tags resolve
// via the imported package's .gsx. Method-receiver tags (<p.Content/>) resolve false.
func componentAtTag(pkg *Package, path string, off int) (comp *gsxast.Component, nameStart, nameLen int, ok bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return nil, 0, 0, false
	}
	f := pkg.Files[path]
	if f == nil {
		return nil, 0, 0, false
	}
	tag := ""
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false
		}
		el, isEl := n.(*gsxast.Element)
		if !isEl || !isComponentTag(el.Tag) {
			return true
		}
		start := pkg.GSXFset.Position(el.Pos()).Offset + 1 // skip '<'
		if off >= start && off < start+len(el.Tag) {
			tag, nameStart, nameLen = el.Tag, start, len(el.Tag)
		}
		return true
	})
	if tag == "" {
		return nil, 0, 0, false
	}
	c, _, found := resolveTagComponent(pkg, tag)
	if !found {
		return nil, 0, 0, false
	}
	return c, nameStart, nameLen, true
}
```

Update the call site at hover.go:38-41 to take the new return and range over `nameLen`:

```go
	if c, nameStart, nameLen, ok := componentAtTag(pkg, path, off); ok {
		rng := rangeForSpan(text, nameStart, nameStart+nameLen, s.enc)
		return s.reply(f.ID, Hover{Contents: markdownGo(renderComponentSig(c)), Range: &rng})
	}
```

(`renderComponentSig(c)` renders the resolved component unchanged; the range now covers the whole tag — `Card` for a simple tag, `components.Input` for a dotted one.)

- [ ] **Step 4: Add the H1 attribute-name hover block**

In `internal/lsp/hover.go`, in `handleHover`, immediately after the `componentAtTag` block (hover.go:38-41) and before the `if pkg.Info == nil` guard (hover.go:43), insert:

```go
	// H1: a component-invocation attribute name → the matching param's type.
	// AST-only (no type info needed), so it answers mid-edit too.
	if tag, attr, attrStart, ok := componentAttrAtOffset(pkg, path, off); ok {
		if comp, _, ok := resolveTagComponent(pkg, tag); ok {
			if decl, ok := paramDeclIn(comp.Params, attr); ok {
				rng := rangeForSpan(text, attrStart, attrStart+len(attr), s.enc)
				return s.reply(f.ID, Hover{Contents: markdownGo(decl), Range: &rng})
			}
		}
	}
```

- [ ] **Step 5: Run the new e2e to verify they pass**

Run: `go test ./gen/ -run 'TestHoverAttrParamSamePkg|TestHoverCrossPkgAttrAndTag' -count=1`
Expected: PASS.

- [ ] **Step 6: Run hover + gd e2e for no regression, then full lsp+gen**

Run: `go test ./gen/ -run 'TestHover|TestDefinition' -count=1 && go test ./internal/lsp/ ./gen/ -count=1`
Expected: PASS — existing hover tests (simple tag, expr, pipeline) and gd tests unaffected; the simple-tag hover still works because `isComponentTag` accepts simple tags and `resolveTagComponent` routes them to `findComponentDecl`.

- [ ] **Step 7: Commit**

```bash
git add internal/lsp/hover.go gen/hover_attr_e2e_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): hover on attribute names + cross-package component tags

H1: hover on a component attr name shows the matching param's type
("comments []store.Comment"). H2: generalize componentAtTag to dotted tags so
hover on <components.Input> shows the cross-package component signature. Reuses
the gd resolvers; AST-only, answers mid-edit; .x.go-independent.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Self-Review

**1. Spec coverage** (against the hover-parity design):
- §3.1 `componentAttrAtOffset` extraction + `componentAttrParamAt` rewrite → Task 1 Step 5. ✓
- §3.2 `resolveTagComponent` (same/cross dispatch) → Task 1 Step 5. ✓
- §4 H1 (`paramDeclIn` + the attr hover block, AST-only, before the `pkg.Info` guard) → Task 1 Step 3 + Task 2 Step 4. ✓
- §5 H2 (generalize `componentAtTag` to dotted via `resolveTagComponent`, reuse `renderComponentSig`) → Task 2 Step 3. ✓
- §6 dispatch order (tag → attr → expr) → Task 2 Steps 3-4 (attr block placed after the tag block, before `pkg.Info` guard). ✓
- §7 invariants (no codegen import, `.x.go`-independent, best-effort, gd unchanged) → constraints + Task 1 Step 6 (gd e2e green). ✓
- §8 testing (`paramDeclIn` unit; H1 same/cross + H2 e2e; no-regression) → Task 1 Step 1 + Task 2 Step 1 + Steps 6/7. ✓

**2. Placeholder scan:** No TBD/TODO; every code step complete; every run step has the exact command + expected outcome.

**3. Type consistency:** `componentAttrAtOffset(...) (tag, attr string, attrStart int, ok bool)`, `resolveTagComponent(...) (*gsxast.Component, *token.FileSet, bool)`, `paramDeclIn(params, attr string) (string, bool)` are used identically in `componentAttrParamAt`, the H1 hover block, and `componentAtTag`. `hoverAt(t, dir, file, text, needle, cursorOff)` and `h.Contents.Value` match the existing `gen/hover_e2e_test.go` harness. `types.ExprString(field.Type)` is the stdlib renderer (go/types). `runLSP(…, config{}, nil)` is the current signature (used inside `hoverAt`).
