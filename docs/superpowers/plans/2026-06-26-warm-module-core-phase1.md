# Warm Module-Analysis Core — Phase 1 Implementation Plan (LSP on the core + Bug A/B)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the gsx LSP's per-edit analysis onto the Phase-0 `Module` core so cross-package go-to-definition/diagnostics resolve from in-memory skeletons (no `go list` per edit, no on-disk `.x.go`), and finish the two go-to-definition bugs (component-name column precision; cross-package closing tags).

**Architecture:** `lspAnalyzer` (in `gen/`) holds a warm `*codegen.Module` per module root, reused across `Analyze` calls (keeping the expensive external `packages.Load` warm). Each `Analyze` refreshes open-buffer overrides, coarsely invalidates the project-package cache (Phase-1 correctness; Phase-2 makes it incremental), and returns `Module.Package(dir)` adapted to `lsp.Package`. `Module.Package`/`Generate` now surface type-error diagnostics. The skeleton's component-decl `//line` anchors to the component name column; `crossPkgTagDeclAt` gains a closing-tag branch.

**Tech Stack:** Go, `go/types`, `go/token`, the Phase-0 `Module` (`internal/codegen/module*.go`), `internal/lsp`, `gen/lsp.go`.

## Global Constraints

- Go module `github.com/gsxhq/gsx`; Go `1.26.1`.
- `.x.go`-independent: LSP resolution comes from in-memory skeletons via `Module`, never a dependency's on-disk `.x.go`.
- `internal/lsp` must NOT import `internal/codegen` — the `Analyzer` interface + `gen/lsp.go` keep the boundary. The `*codegen.Module` lives in `gen/` (which already imports `codegen`).
- Every codegen/parser change ships corpus + unit coverage ([[gsx-syntax-change-test-coverage]]); the Phase-0 corpus equivalence gate (`internal/corpus/module_equiv_test.go`) must stay green.
- Unexported by default; export only what `gen/` needs from `codegen`.
- Verify the binary before any manual `gsx lsp` run ([[gsx-binary-name-collides-ghostscript]]).

---

## File Structure

- **Modify `internal/codegen/module_importer.go`** — `analyze` captures `checkSkeletonPackage`'s `[]types.Error` into the bag (Task 1); add `Module.ResetPackageCache()` (Task 2).
- **Modify `internal/codegen/module.go`** — `Module.Package` sets `res.Diags` from the bag (Task 1).
- **Modify `internal/codegen/analyze.go`** — anchor the skeleton component func-decl `//line` to the component **name** column via a new `emitSkeletonComponentNameLine` (Task 4).
- **Modify `internal/lsp/definition_crosspkg.go`** — `crossPkgTagDeclAt` gains an `onClose` branch (Task 5).
- **Modify `gen/lsp.go`** — `lspAnalyzer` holds a warm `*codegen.Module` cache; `Analyze` routes through `Module.Package` (Task 3).
- **Tests:** extend `internal/codegen/line_anchor_test.go`, `internal/lsp/definition_crosspkg_test.go`; new `internal/codegen/module_diag_test.go`; new `gen/lsp_crosspkg_e2e_test.go`.

Ordering: Tasks 4 & 5 (self-contained Bug-A/B fixes) → Tasks 1 & 2 (Module diagnostics + invalidation) → Task 3 (wiring) → Task 6 (payoff e2e).

---

## Task 1: Surface type-error diagnostics in `Module.Package`/`Generate`

**Files:**
- Modify: `internal/codegen/module_importer.go` (`analyze`, ~line 281–284), `internal/codegen/module.go` (`Package`)
- Test: `internal/codegen/module_diag_test.go` (create)

**Interfaces:**
- Consumes: `checkSkeletonPackage(dir, pkgName, goFiles, fset, mi) (*types.Package, *types.Info, []types.Error)`; the `analyzed` struct's `bag *diag.Bag` + `gsxFset *token.FileSet` fields.
- Produces: `Module.Package` returns a `*PackageResult` whose `Diags` includes type errors mapped to `.gsx` positions; `analyze` no longer discards `[]types.Error`.

**Background:** batch.go:384-409 turns `pkg.TypeErrors` into `diag.Diagnostic{Start: e.Fset.Position(e.Pos), Severity: Error, Source: "types"}`. In `Module`, the single shared `fset` is `a.gsxFset`, and a skeleton ident's `Pos()` maps to `.gsx` via `//line` — so `a.gsxFset.Position(e.Pos)` is the right position. The bag (`a.bag`) already exists for parse/script errors.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModulePackageSurfacesTypeErrors(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// `nope` is undefined → a type error inside the interpolation.
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Home() {\n\t<div>{ nope() }</div>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "nope") && strings.HasSuffix(d.Start.Filename, ".gsx") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a .gsx-positioned type-error diagnostic mentioning 'nope'; got %+v", pr.Diags)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestModulePackageSurfacesTypeErrors -v`
Expected: FAIL — `pr.Diags` is empty (no type-error diagnostic).

- [ ] **Step 3: Write minimal implementation**

In `module_importer.go` `analyze`, replace the discard at ~line 281-284:

```go
pkg, info, typeErrs := checkSkeletonPackage(dir, pkgName, goFiles, fset, mi)
for _, e := range typeErrs {
	p := e.Fset.Position(e.Pos) // e.Fset is the shared fset; //line maps skeleton → .gsx
	if strings.HasSuffix(p.Filename, ".x.go") {
		continue // synthetic skeleton position with no //line — skip (as batch/harvest do)
	}
	bag.Add(diag.Diagnostic{Start: p, End: p, Severity: diag.Error, Message: e.Msg, Source: "types"})
}
```

(Confirm `bag` is the `*diag.Bag` already in scope in `analyze` — it is `a.bag` / the local `bag`. `strings` and `diag` are already imported in this file; if not, add them.)

In `module.go` `Package`, set `res.Diags` from the bag (after `analyze` returns `a`):

```go
res.Diags = a.bag.Sorted()
```

> Note: `Generate` already returns `a.bag.Sorted()`, so it now also surfaces type errors — this moves `Generate`'s *diagnostics* closer to batch. `Generate`'s *Files* behavior on type-error packages is unchanged (the Phase-0 documented divergence; corpus gate compares Files only and skips type-error cases, so it stays green).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestModulePackageSurfacesTypeErrors -v`
Expected: PASS.

- [ ] **Step 4b: Confirm the corpus gate + suite still green**

Run: `go test ./internal/codegen/ ./internal/corpus/ -count=1`
Expected: PASS (the gate compares Files, unaffected by diagnostics).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module_importer.go internal/codegen/module.go internal/codegen/module_diag_test.go
git commit -m "feat(codegen): Module surfaces type-error diagnostics mapped to .gsx"
```

---

## Task 2: `Module.ResetPackageCache` (Phase-1 coarse invalidation)

**Files:**
- Modify: `internal/codegen/module_importer.go` (or `module.go` — beside the `pkgTypes` field)
- Test: `internal/codegen/module_diag_test.go` (extend)

**Interfaces:**
- Produces: `func (m *Module) ResetPackageCache()` — drops the cached project-package `*types.Package`s (`pkgTypes`) so the next analysis re-type-checks project packages from fresh skeletons, while keeping the external importer (`ext`) warm.

**Background:** `pkgTypes` caches project `*types.Package` and is never invalidated (Phase-0 gap). The LSP must re-resolve project types after edits; `ext` (the expensive `packages.Load("./...")`) should stay warm. This method gives the analyzer a cheap "make project types fresh" lever for Phase 1; Phase 2 replaces it with reverse-dep invalidation.

- [ ] **Step 1: Write the failing test**

```go
func TestModuleResetPackageCacheKeepsExternalWarm(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "comp")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "comp.gsx", "package comp\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if _, err := m.typesPackage(pkgDir); err != nil {
		t.Fatal(err)
	}
	// edit comp.gsx in-memory: Button now takes an int
	m.SetOverride(filepath.Join(pkgDir, "comp.gsx"), []byte("package comp\n\ncomponent Button(n int) {\n\t<button>{ n }</button>\n}\n"))
	m.ResetPackageCache()
	pkg, err := m.typesPackage(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	// The freshly-rebuilt Button props struct must reflect the int param.
	if obj := pkg.Scope().Lookup("ButtonProps"); obj != nil {
		if !strings.Contains(obj.Type().Underlying().String(), "int") {
			t.Fatalf("ResetPackageCache did not refresh comp types: %s", obj.Type().Underlying())
		}
	}
	// ext importer must still be non-nil (warm, not cleared)
	if m.ext == nil {
		t.Fatalf("ResetPackageCache wrongly cleared the external importer")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestModuleResetPackageCacheKeepsExternalWarm -v`
Expected: FAIL — `m.ResetPackageCache undefined` (and, before the method exists, the stale cache returns the old `string`-param struct).

- [ ] **Step 3: Write minimal implementation**

```go
// ResetPackageCache drops cached project-package type info so the next analysis
// re-type-checks project packages from current (override-aware) skeletons. The
// external importer (ext) — built by an expensive packages.Load — is kept warm.
// Phase-1 coarse invalidation; Phase 2 replaces it with reverse-dependency
// invalidation keyed off the changed package.
func (m *Module) ResetPackageCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pkgTypes = map[string]*types.Package{}
}
```

(Match the existing `pkgTypes` field type/name exactly — read `module.go`/`module_importer.go` first.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestModuleResetPackageCacheKeepsExternalWarm -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module_importer.go internal/codegen/module_diag_test.go
git commit -m "feat(codegen): Module.ResetPackageCache (coarse Phase-1 invalidation, ext stays warm)"
```

---

## Task 3: Wire `lspAnalyzer.Analyze` onto a warm `Module`

**Files:**
- Modify: `gen/lsp.go`
- Test: `gen/lsp_warm_test.go` (create)

**Interfaces:**
- Consumes: `codegen.Open`, `codegen.Options`, `(*codegen.Module).SetOverride`, `.ResetPackageCache` (Task 2), `.Package` (Task 1); `moduleRoot(dir) (root, modPath, err)`.
- Produces: `lspAnalyzer.Analyze` returns the edited package's analysis from a warm per-root `Module` (no `go list` per call). `AnalyzeModule` is unchanged in this task.

**Background:** `lspAnalyzer` is created once in `runLSP` and stored in the `Analyzer` interface; `Analyze` is called from worker goroutines (concurrency → needs a mutex). `moduleRoot` already returns `modPath` (currently discarded as `_`).

- [ ] **Step 1: Write the failing test** (the warm Module path produces the same retained analysis, with no `.x.go` on disk)

```go
package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLSPAnalyzeUsesWarmModule(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pkgDir, "page.gsx"), "package page\n\ncomponent Home(name string) {\n\t<h1>Hi {name}</h1>\n}\n")

	a := newLSPAnalyzer(config{}, nil) // constructor introduced in Step 3
	pkg, err := a.Analyze(pkgDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Info == nil || pkg.ExprMap == nil || pkg.GSXFset == nil {
		t.Fatalf("warm-module Analyze returned empty analysis: %+v", pkg)
	}
	if _, ok := pkg.CrossIndex[".Home"]; !ok {
		t.Fatalf("CrossIndex missing .Home: %v", pkg.CrossIndex)
	}
	// No .x.go was written to disk by analysis.
	if _, err := os.Stat(filepath.Join(pkgDir, "page.x.go")); err == nil {
		t.Fatalf("Analyze must not write .x.go to disk")
	}
}

// writeTestFile is a local helper if the gen test package lacks one; otherwise reuse the existing helper.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

(Before writing, check `gen/`'s existing test helpers — if a `writeFile`-style helper exists, reuse it and drop the local `writeTestFile`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestLSPAnalyzeUsesWarmModule -v`
Expected: FAIL — `newLSPAnalyzer` undefined.

- [ ] **Step 3: Write minimal implementation**

In `gen/lsp.go`, give `lspAnalyzer` a warm Module cache and a constructor, and rewrite `Analyze`:

```go
import (
	"sync"
	// existing imports...
	"github.com/gsxhq/gsx/internal/codegen"
)

type lspAnalyzer struct {
	optCfg config
	warnw  io.Writer
	mods   *moduleSet // pointer so the value stored in the Analyzer interface shares state
}

// moduleSet holds one warm *codegen.Module per module root, reused across Analyze
// calls so the expensive external packages.Load stays warm.
type moduleSet struct {
	mu     sync.Mutex
	byRoot map[string]*codegen.Module
}

func newLSPAnalyzer(cfg config, warnw io.Writer) lspAnalyzer {
	return lspAnalyzer{optCfg: cfg, warnw: warnw, mods: &moduleSet{byRoot: map[string]*codegen.Module{}}}
}

func (a lspAnalyzer) module(root, modPath string, merged config) (*codegen.Module, error) {
	a.mods.mu.Lock()
	defer a.mods.mu.Unlock()
	if m, ok := a.mods.byRoot[root]; ok {
		return m, nil
	}
	m, err := codegen.Open(codegen.Options{
		ModuleRoot:   root,
		ModulePath:   modPath,
		FilterPkgs:   merged.filterPkgs,
		Aliases:      merged.aliases,
		FieldMatcher: merged.fieldMatcher,
		Classifier:   merged.classifier(),
	})
	if err != nil {
		return nil, err
	}
	a.mods.byRoot[root] = m
	return m, nil
}

func (a lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, modPath, err := moduleRoot(dir)
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
	m.ResetPackageCache() // Phase-1: project types fresh per edit; ext stays warm
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pr, err := m.Package(abs)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return &lsp.Package{}, nil
	}
	return adaptPackageResult(pr), nil // extract the existing field-by-field copy into a helper
}
```

Extract the existing `PackageResult → lsp.Package` field-by-field copy (currently inline in `Analyze`) into `func adaptPackageResult(pr *codegen.PackageResult) *lsp.Package { ... }` and call it from both the new `Analyze` and wherever else it's needed. Update `runLSP` (`gen/lsp.go:105`) to construct via `newLSPAnalyzer(cfg, stderr)` instead of the struct literal.

> Concurrency: `module()` locks `mods.mu`. `Module`'s own methods are serialized by its internal mutex; the server already runs one analysis per dir at a time. Document on `moduleSet` that callers may invoke concurrently across roots.
> Override scope: `override` is the open buffers in `dir` (the server's `snapshotOverride(dir)`); sibling packages' unsaved buffers aren't included (read from disk). Acceptable for Phase 1; whole-module override is `AnalyzeModule`'s job.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./gen/ -run TestLSPAnalyzeUsesWarmModule -v`
Expected: PASS.

- [ ] **Step 4b: Confirm the LSP + gen suites still green**

Run: `go test ./gen/ ./internal/lsp/ -count=1`
Expected: PASS (existing LSP analyze/diagnostic tests must still pass through the new path).

- [ ] **Step 5: Commit**

```bash
git add gen/lsp.go gen/lsp_warm_test.go
git commit -m "feat(lsp): route Analyze through a warm per-root Module (no go list per edit, .x.go-free)"
```

---

## Task 4: Skeleton component-name `//line` column precision (Bug A)

**Files:**
- Modify: `internal/codegen/analyze.go` (the two `emitSkeletonLine(sb, fset, c.Pos())` func-decl anchors, ~lines 577 & 639)
- Test: `internal/codegen/line_anchor_test.go` (extend)

**Interfaces:**
- Produces: `func emitSkeletonComponentNameLine(sb *strings.Builder, fset *token.FileSet, c *gsxast.Component)` — emits the skeleton func-decl `//line` anchored so the component NAME maps to `c.NamePos` column-precisely.

**Background:** The skeleton emits `func <Name>(` (function) or `func <Recv> <Name>(` (method). The name sits at a fixed column within the generated line: 6 for `func <Name>` (after `"func "`), or `7 + len(c.Recv)` for `func <Recv> <Name>` (after `"func " + Recv + " "`). A `//line file:L:C` directive maps the next line's first char to source column C, so the name (at generated column `genNameCol`) maps to `C + genNameCol - 1`. To make that equal the source name column, set `C = nameCol - genNameCol + 1`. Only the SKELETON needs this (cross-package go-to-def now resolves via the in-memory skeleton, not the dependency's `.x.go`); the emit-side `emitLine` anchor stays at `c.Pos()` (compiler-error messages). This does NOT touch corpus goldens (skeleton isn't emitted output).

- [ ] **Step 1: Write the failing test** (assert the skeleton `//line` COLUMN maps to the name)

```go
// assertFuncNameAnchorColumn asserts the //line before the generated `func … name(`
// maps to the column of `name` in the .gsx source (not the `component` keyword).
func assertFuncNameAnchorColumn(t *testing.T, skel, src, name string) {
	t.Helper()
	lines := strings.Split(skel, "\n")
	// component name column in source (1-based): find "component " then the name.
	srcLines := strings.Split(src, "\n")
	wantCol := 0
	wantLine := 0
	for i, l := range srcLines {
		if idx := strings.Index(l, name); idx >= 0 && strings.Contains(l, "component ") {
			wantCol = idx + 1
			wantLine = i + 1
			break
		}
	}
	if wantCol == 0 {
		t.Fatalf("component %q not found in src", name)
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "func ") && strings.Contains(l, name+"(") {
			prev := strings.TrimSpace(lines[i-1])
			want := fmt.Sprintf(":%d:%d", wantLine, wantCol)
			if !strings.HasPrefix(prev, "//line ") || !strings.Contains(prev, want) {
				t.Errorf("func %s: anchor = %q, want a //line ending %s (name col)", name, prev, want)
			}
			return
		}
	}
	t.Fatalf("generated func for %q not found", name)
}

func TestComponentFuncNameColumnAnchorSkeleton(t *testing.T) {
	// Reuse the existing skeleton-building harness from TestComponentFuncLineAnchorSkeleton
	// (lineAnchorSrc, buildSkeleton over each file). For each component, call
	// assertFuncNameAnchorColumn(t, skel, lineAnchorSrc, name) for "First" and "Last"
	// (function components) and "Page" (method component on receiver h).
}
```

Fill in the test body by mirroring `TestComponentFuncLineAnchorSkeleton` (same `lineAnchorSrc`, same `buildSkeleton` setup) and calling `assertFuncNameAnchorColumn` for `First`, `Page`, `Last`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestComponentFuncNameColumnAnchorSkeleton -v`
Expected: FAIL — the anchor currently uses `c.Pos()` (the `component` keyword column), not the name column.

- [ ] **Step 3: Write minimal implementation**

Add the helper in `analyze.go` (near `emitSkeletonLine`):

```go
// emitSkeletonComponentNameLine anchors the skeleton's `func … Name(` declaration
// so the Name token maps to the component's .gsx NamePos column-precisely, letting
// LSP go-to-definition land on the component name. genNameCol is the name's column
// within the generated func line: 6 for `func <Name>` (after "func "), or
// 7+len(Recv) for `func <Recv> <Name>`. The directive is shifted left by that
// prefix. (Only the skeleton needs this; the emit-side anchor stays at c.Pos().)
func emitSkeletonComponentNameLine(sb *strings.Builder, fset *token.FileSet, c *gsxast.Component) {
	if fset == nil || !c.NamePos.IsValid() {
		return
	}
	genNameCol := 6 // func <Name>
	if c.Recv != "" {
		genNameCol = 7 + len(c.Recv) // func <Recv> <Name>
	}
	p := fset.Position(c.NamePos)
	col := p.Column - genNameCol + 1
	if col < 1 {
		col = 1
	}
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}
```

Replace BOTH func-decl anchors in `emitComponentSkeleton` — change `emitSkeletonLine(sb, fset, c.Pos())` at the BYO site (~577) and the normal site (~639) to `emitSkeletonComponentNameLine(sb, fset, c)`. Leave the param-binding resets (`emitSkeletonLine(..., c.Pos())` at ~590 and ~670) unchanged.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run 'TestComponentFuncNameColumnAnchorSkeleton|TestComponentFuncLineAnchorSkeleton' -v`
Expected: PASS (column-precise AND the existing line-anchor test still passes).

- [ ] **Step 4b: Confirm no diagnostics/corpus regression**

Run: `go test ./internal/codegen/ ./internal/corpus/ -count=1`
Expected: PASS. (The skeleton column shift can only move a diagnostic that lands exactly on a component-decl line; if any `diagnostics.golden` changes, verify the new column points at the component name and regenerate with `go test ./internal/corpus/ -update` — document the change.)

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/analyze.go internal/codegen/line_anchor_test.go
git commit -m "feat(codegen): skeleton component-decl //line anchors to the name column (go-to-def precision)"
```

---

## Task 5: `crossPkgTagDeclAt` closing-tag resolution (Bug B, cross-package)

**Files:**
- Modify: `internal/lsp/definition_crosspkg.go` (`crossPkgTagDeclAt`, ~lines 104–139)
- Test: `internal/lsp/definition_crosspkg_test.go` (extend)

**Interfaces:**
- Consumes: `gsxast.Element.CloseNamePos`, `splitDottedTag`, `resolveCrossPkgComponent` (existing).
- Produces: `crossPkgTagDeclAt` resolves a cursor on a dotted closing tag (`</ui.Button>`) the same as the opening tag.

**Background:** `componentTagDeclAt` already has the `onClose` pattern (definition.go:287-293). `crossPkgTagDeclAt` only checks `el.Pos()` (opening). Mirror the onClose check using `el.CloseNamePos`.

- [ ] **Step 1: Write the failing test**

```go
func TestCrossPkgTagDeclAtClosingTag(t *testing.T) {
	// Build a Package whose Files has an element <ui.Button>…</ui.Button> and whose
	// imports resolve `ui` to a dep package declaring component Button.
	// Mirror the setup of the existing cross-pkg opening-tag test (if present) or
	// TestComponentTagDeclAtClosingTag in definition_test.go, but with a dotted tag.
	// Assert: an offset on the CLOSING `ui.Button` resolves to the same decl
	// position as an offset on the OPENING tag.
	pkg, path, openOff, closeOff := setupCrossPkgClosingFixture(t) // helper you write, mirroring existing fixtures
	openPos, okOpen := crossPkgTagDeclAt(pkg, path, openOff)
	closePos, okClose := crossPkgTagDeclAt(pkg, path, closeOff)
	if !okOpen {
		t.Fatalf("opening dotted tag did not resolve")
	}
	if !okClose {
		t.Fatalf("closing dotted tag did not resolve (Bug B cross-package)")
	}
	if openPos != closePos {
		t.Fatalf("closing tag resolved to %v, want same as opening %v", closePos, openPos)
	}
}
```

Build `setupCrossPkgClosingFixture` by mirroring how the existing cross-package tests construct a `*Package` with `Files`, `GSXFset`, `Types`, `Fset` (look at `definition_crosspkg_test.go` and `definition_test.go` `TestComponentTagDeclAtClosingTag` for the pattern). The element must carry a valid `CloseNamePos` (parse a `<ui.Button>…</ui.Button>` source through the gsx parser so the parser sets it).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestCrossPkgTagDeclAtClosingTag -v`
Expected: FAIL — the closing offset does not resolve (`okClose` is false).

- [ ] **Step 3: Write minimal implementation**

In `crossPkgTagDeclAt`, replace the opening-only offset check with an open-or-close check mirroring `componentTagDeclAt`:

```go
nameStart := pkg.GSXFset.Position(el.Pos()).Offset + 1 // skip '<'
onOpen := off >= nameStart && off < nameStart+len(el.Tag)
onClose := false
if el.CloseNamePos.IsValid() {
	closeStart := pkg.GSXFset.Position(el.CloseNamePos).Offset
	onClose = off >= closeStart && off < closeStart+len(el.Tag)
}
if !onOpen && !onClose {
	return true
}
```

(Keep the rest of the function — `splitDottedTag` + `resolveCrossPkgComponent` + setting `result`/`found` — unchanged.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestCrossPkgTagDeclAtClosingTag -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/definition_crosspkg.go internal/lsp/definition_crosspkg_test.go
git commit -m "fix(lsp): cross-package closing-tag go-to-definition (</ui.Button>)"
```

---

## Task 6: Phase-1 payoff e2e — cross-package go-to-def in-memory, no `.x.go`

**Files:**
- Test: `gen/lsp_crosspkg_e2e_test.go` (create)

**Interfaces:**
- Consumes: the warm-Module `Analyze` (Task 3), `lsp` go-to-definition handling. This is an integration test only — no production code.

**Goal:** Prove the headline Phase-1 outcome end-to-end: with NO `.x.go` on disk, go-to-definition on a cross-package component invocation (`{ components.Pagination(...) }`) AND on a cross-package closing tag (`</components.Pagination>`) resolves to the dependency's `.gsx` declaration.

- [ ] **Step 1: Write the test**

```go
package gen

// Build a two-package module on disk (NO .x.go):
//   components/pagination.gsx: package components; component Pagination(pages int) { <nav>…</nav> }
//   category/category.gsx:     package category; import ".../components"
//                              component Page() { <div>{ components.Pagination(3) }</div> <components.Pagination pages={3}></components.Pagination> }
// Drive the LSP server (or the analyzer + definition handler) over JSON-RPC / direct calls:
//   - open category.gsx
//   - textDocument/definition with the cursor on "Pagination" in the { } invocation
//   - assert the result Location URI ends with components/pagination.gsx and the line
//     is the `component Pagination` decl line (and column = the name column).
//   - repeat with the cursor on the CLOSING </components.Pagination> tag name → same target.
// Mirror the existing references/definition e2e harness in gen/ (e.g.
// references_crosspkg_e2e_test.go) for server setup + request plumbing.
```

Implement by copying the server/request scaffolding from `gen/references_crosspkg_e2e_test.go` (it already drives `textDocument/*` over the JSON-RPC server with a real `lspAnalyzer`). Use `newLSPAnalyzer` so the warm-Module path is exercised. Assert NO `.x.go` exists on disk at any point.

- [ ] **Step 2: Run the e2e**

Run: `go test ./gen/ -run TestCrossPkgGoToDefInMemory -v`
Expected: PASS — both the invocation cursor and the closing-tag cursor resolve to `components/pagination.gsx` at the decl name.

- [ ] **Step 3: Full suite**

Run: `go build ./... && go test ./... -count=1`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add gen/lsp_crosspkg_e2e_test.go
git commit -m "test(lsp): cross-package go-to-def (invocation + closing tag) resolves in-memory, no .x.go"
```

---

## Self-Review notes (for the implementer)

- **Spec coverage:** This plan implements the design spec's Phase 1 (LSP on the core; Bug A/B). It closes two Phase-0 deferred items: type-error diagnostics (Task 1) and a coarse cache-invalidation lever (Task 2 — full reverse-dep invalidation remains Phase 2). It does NOT migrate `generate`/`--watch`/`fmt`/playground (Phase 3), nor build the metadata graph (Phase 2).
- **Known Phase-1 simplifications (deliberate, documented in code):** per-edit `ResetPackageCache` re-type-checks the edited package's closure (correct, not yet incremental); `override` is dir-scoped (sibling unsaved buffers read from disk); the first `Analyze` per root pays the cold external `packages.Load` (consider eager warming later).
- **Boundary:** keep `*codegen.Module` in `gen/`; `internal/lsp` stays codegen-free via the `Analyzer` interface.
- **Don't touch corpus goldens** in Tasks 1–3,5–6; Task 4 only regenerates a `diagnostics.golden` if a type error lands exactly on a component-decl line (verify the new column is the name).
