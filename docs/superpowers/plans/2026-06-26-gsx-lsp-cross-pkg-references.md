# gsx LSP — cross-package find-references Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `textDocument/references` on a gsx component returns usages across the whole module, not just the declaring package.

**Architecture:** Codegen routes cross-package component references into the declaring component's `CrossRef` (object identity, available because one `packages.Load` over all module dirs shares types). A new `lspAnalyzer.AnalyzeModule` runs that whole-module batch and returns a flat cross-reference list. The server caches it lazily, invalidates on edits, and `handleReferences` queries it (falling back to the single-package index on miss/error).

**Tech Stack:** Go, `golang.org/x/tools/go/packages`, `go/types`, the existing gsx codegen + LSP.

**Design:** `docs/superpowers/specs/2026-06-26-gsx-lsp-cross-pkg-references-design.md`

## Global Constraints

- `internal/lsp` MUST NOT import `internal/codegen` (the `Analyzer` interface + `gen/lsp.go` keep the boundary).
- References resolve from in-memory analysis (`TypesInfo.Uses` + `//line`-mapped positions), never on-disk `.x.go` content. Uses whose position filename ends in `.x.go` are skipped.
- The codegen change MUST be additive: a single-dir batch (every per-file `Analyze`) is byte-for-byte unchanged, so existing in-package find-references and go-to-definition e2e tests stay green.
- Best-effort, never panics or regresses: any module-analysis failure falls back to the single-package path; references never return empty where they returned results before.
- Module-resolution tests (those calling `packages.Load` / `runLSP` over a real module) are guarded by `testing.Short()`.
- Prefer unexported identifiers; commit messages end with `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`.

---

### Task 1: Codegen — route cross-package references

**Files:**
- Modify: `internal/codegen/batch.go` (add owner accumulation in the analysis loop ~line 359; add a second pass after the loop closes ~line 494)
- Test: `internal/codegen/batch_crosspkg_test.go` (Create)

**Interfaces:**
- Consumes: existing `GeneratePackagesWithFilters(moduleDir, dirs, …) (map[string]*PackageResult, error)`; `PackageResult.CrossIndex map[string]CrossRef`; `CrossRef{Name string; Decl token.Position; Refs []token.Position}`; the loop locals `compObjByKey map[string]types.Object`, `pkgDir string`, and the `*packages.Package` loop var `pkg` (giving `pkg.Fset *token.FileSet`, `pkg.TypesInfo *types.Info`).
- Produces: after a multi-dir batch, each component's `CrossRef.Refs` includes cross-package reference positions (`//line`-mapped to `.gsx`, real `.go` sites kept). No new exported symbols.

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/batch_crosspkg_test.go`. It builds a two-package module on disk (root `package x` using an imported `components.Input`; subdir `components` declaring `Input`), runs a whole-module batch over both dirs, and asserts `Input`'s `CrossRef.Refs` contains the root-package use sites. A second sub-test runs a single-dir batch over `components` alone and asserts `Input` has zero refs (no regression).

```go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCrossPkgModule writes a 2-package module: root package x imports
// example.com/x/components and references Input from a .gsx tag and a .go call
// site; subdir components declares Input. Returns (root, componentsDir).
func writeCrossPkgModule(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot) // internal/codegen -> repo root
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n")
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n")
	return root, filepath.Join(root, "components")
}

func inputRefs(t *testing.T, out map[string]*PackageResult, componentsDir string) []string {
	t.Helper()
	pr := out[componentsDir]
	if pr == nil {
		t.Fatalf("no result for components dir %s; keys=%v", componentsDir, keysOf(out))
	}
	var files []string
	for _, cr := range pr.CrossIndex {
		if cr.Name != "Input" {
			continue
		}
		for _, r := range cr.Refs {
			files = append(files, filepath.Base(r.Filename))
		}
	}
	return files
}

func keysOf(m map[string]*PackageResult) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}

func TestCrossPkgReferencesRouted(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root, componentsDir := writeCrossPkgModule(t)
	out, err := GeneratePackages(root, []string{root, componentsDir})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(inputRefs(t, out, componentsDir), ",")
	if !strings.Contains(got, "post.gsx") {
		t.Errorf("Input refs missing post.gsx (cross-pkg .gsx tag); got %q", got)
	}
	if !strings.Contains(got, "use.go") {
		t.Errorf("Input refs missing use.go (cross-pkg .go site); got %q", got)
	}
}

func TestSingleDirReferencesNoRegression(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root, componentsDir := writeCrossPkgModule(t)
	out, err := GeneratePackages(root, []string{componentsDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := inputRefs(t, out, componentsDir); len(got) != 0 {
		t.Errorf("single-dir batch over components alone should have no Input refs; got %v", got)
	}
}
```

Note: confirm `GeneratePackages(moduleDir, dirs)` (batch.go:590) is the no-filter wrapper over `GeneratePackagesWithFilters`; if its signature differs, call `GeneratePackagesWithFilters(root, dirs, nil, nil, nil, nil, nil, nil, nil)` directly. Verify `repoRoot` resolves to the gsx repo root (two levels up from `internal/codegen`) so the `replace` directive points at this checkout.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/codegen/ -run 'TestCrossPkgReferencesRouted|TestSingleDirReferencesNoRegression' -v`
Expected: `TestCrossPkgReferencesRouted` FAILS ("missing post.gsx" / "missing use.go" — cross-pkg refs not yet routed); `TestSingleDirReferencesNoRegression` PASSES (already true).

- [ ] **Step 3: Add owner accumulation in the analysis loop**

In `internal/codegen/batch.go`, add these unexported types near `CrossRef` (top of file, ~line 27):

```go
// compOwner identifies the package directory and componentKey that own a
// component func object, for routing cross-package references (see the
// cross-package find-references design).
type compOwner struct{ dir, key string }

// analyzedPkg retains one analyzed package's fileset and use info for the
// post-loop cross-package reference pass.
type analyzedPkg struct {
	dir  string
	fset *token.FileSet
	info *types.Info
}
```

Declare the accumulators just before the `for _, pkg := range pkgs {` loop (~line 279):

```go
compObjOwner := map[types.Object]compOwner{}
var analyzed []analyzedPkg
```

Inside the loop, immediately after `index` is built (after batch.go:359 `index[key] = CrossRef{...}` loop), insert:

```go
		// Accumulate this package's component objects and use info for the
		// cross-package reference pass (design §3). Done before the type-error
		// `continue`s below so every indexed package participates.
		for key, obj := range compObjByKey {
			compObjOwner[obj] = compOwner{dir: pkgDir, key: key}
		}
		analyzed = append(analyzed, analyzedPkg{dir: pkgDir, fset: pkg.Fset, info: pkg.TypesInfo})
```

- [ ] **Step 4: Add the cross-package routing pass after the loop**

Immediately after the analysis loop closes (the `}` at ~line 494, before `// Step 7: generateFile…`), insert:

```go
	// Cross-package reference pass: route a use of an imported component into
	// the DECLARING component's CrossRef. In-package refs were already added by
	// Case 1 above (owner.dir == ap.dir, skipped here). For a single-dir batch
	// compObjOwner holds one dir, so nothing is appended. See design §3.
	for _, ap := range analyzed {
		for id, obj := range ap.info.Uses {
			owner, ok := compObjOwner[obj]
			if !ok || owner.dir == ap.dir {
				continue
			}
			p := ap.fset.Position(id.Pos())
			if strings.HasSuffix(p.Filename, ".x.go") {
				continue // synthetic skeleton position, no //line
			}
			res := result[owner.dir]
			if res == nil || res.CrossIndex == nil {
				continue
			}
			cr := res.CrossIndex[owner.key]
			cr.Refs = append(cr.Refs, p)
			res.CrossIndex[owner.key] = cr
		}
	}
```

(`strings`, `token`, and `types` are already imported in batch.go.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/codegen/ -run 'TestCrossPkgReferencesRouted|TestSingleDirReferencesNoRegression' -v`
Expected: both PASS.

- [ ] **Step 6: Run the codegen package suite for regressions**

Run: `go test ./internal/codegen/`
Expected: PASS (the additive pass is a no-op for existing single-dir tests).

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/batch.go internal/codegen/batch_crosspkg_test.go
git commit -m "feat(codegen): route cross-package component references into CrossRef

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

### Task 2: gen — `AnalyzeModule` whole-module analysis + `Analyzer` interface

**Files:**
- Modify: `internal/lsp/server.go` (extend the `Analyzer` interface, ~line 20)
- Modify: `gen/lsp.go` (add `lspAnalyzer.AnalyzeModule`)
- Modify: `internal/lsp/server_lifecycle_test.go`, `internal/lsp/server_async_test.go`, `internal/lsp/server_debounce_test.go`, `internal/lsp/server_sync_test.go` (add `AnalyzeModule` stubs to the four test doubles)
- Test: `gen/references_crosspkg_test.go` (Create — gen-level `AnalyzeModule` test)

**Interfaces:**
- Consumes: `moduleRoot(dir) (root, _, err)` and `discoverDirs([]string) ([]string, error)` (both in package `gen`); `resolveConfigBestEffort`; `codegen.GeneratePackagesWithFilters`; `lsp.CrossRef`.
- Produces: `AnalyzeModule(dir string, override map[string][]byte) ([]lsp.CrossRef, error)` on `lspAnalyzer`, and the matching method on the `lsp.Analyzer` interface. Returns one flat `[]CrossRef` — each component once, its `Refs` covering the whole module.

- [ ] **Step 1: Extend the `Analyzer` interface**

In `internal/lsp/server.go`, change the `Analyzer` interface to:

```go
type Analyzer interface {
	Analyze(dir string, override map[string][]byte) (*Package, error)
	// AnalyzeModule analyzes every gsx package in the module containing dir and
	// returns one flat cross-reference list (each component once; Refs span the
	// whole module). Used by find-references; failure is non-fatal (the server
	// falls back to the per-package CrossIndex).
	AnalyzeModule(dir string, override map[string][]byte) ([]CrossRef, error)
}
```

- [ ] **Step 2: Add stubs to the four test doubles, run to verify compile failure is fixed**

Add to each test double (return `nil, nil` — these tests drive the single-package path):

```go
// server_lifecycle_test.go
func (nilAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) { return nil, nil }
// server_async_test.go
func (a *blockingAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) { return nil, nil }
// server_debounce_test.go
func (a *countingAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) { return nil, nil }
// server_sync_test.go
func (a fakeAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) { return nil, nil }
```

Run: `go build ./internal/lsp/ && go vet ./internal/lsp/`
Expected: compiles (interface satisfied by all doubles). `gen` will not build yet (lspAnalyzer lacks the method) — that is Step 4.

- [ ] **Step 3: Write the failing gen-level test**

Create `gen/references_crosspkg_test.go`. Reuse the two-package fixture shape from Task 1's `writeCrossPkgModule` (inline it here in package `gen`; `repoRoot` is one level up from `gen`). Call `lspAnalyzer{}.AnalyzeModule(componentsDir, nil)` and assert the `Input` `CrossRef` carries refs in `post.gsx` and `use.go`.

```go
package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeModuleCrossPkg(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n")
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n")

	componentsDir := filepath.Join(root, "components")
	refs, err := (lspAnalyzer{}).AnalyzeModule(componentsDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, cr := range refs {
		if cr.Name == "Input" {
			for _, r := range cr.Refs {
				files = append(files, filepath.Base(r.Filename))
			}
		}
	}
	got := strings.Join(files, ",")
	if !strings.Contains(got, "post.gsx") || !strings.Contains(got, "use.go") {
		t.Errorf("AnalyzeModule Input refs missing cross-pkg sites; got %q", got)
	}
}
```

Run: `go test ./gen/ -run TestAnalyzeModuleCrossPkg -v`
Expected: FAIL to COMPILE (`lspAnalyzer` has no `AnalyzeModule`).

- [ ] **Step 4: Implement `AnalyzeModule`**

In `gen/lsp.go`, add:

```go
// AnalyzeModule analyzes every gsx package in the module containing dir and
// returns a flat cross-reference list. It runs ONE whole-module codegen batch
// (so cross-package component references route into the declaring component's
// CrossRef — see the cross-package find-references design) and flattens every
// package's CrossIndex. override supplies unsaved buffers (abs path -> bytes).
func (a lspAnalyzer) AnalyzeModule(dir string, override map[string][]byte) ([]lsp.CrossRef, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := discoverDirs([]string{root})
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	out, err := codegen.GeneratePackagesWithFilters(root, dirs,
		merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher,
		nil, nil, override)
	if err != nil {
		return nil, err
	}
	var refs []lsp.CrossRef
	for _, pr := range out {
		if pr == nil {
			continue
		}
		for _, v := range pr.CrossIndex {
			refs = append(refs, lsp.CrossRef{Name: v.Name, Decl: v.Decl, Refs: v.Refs})
		}
	}
	return refs, nil
}
```

- [ ] **Step 5: Run the gen test + lsp build**

Run: `go test ./gen/ -run TestAnalyzeModuleCrossPkg -v && go build ./...`
Expected: PASS; everything builds.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/server.go gen/lsp.go internal/lsp/server_lifecycle_test.go internal/lsp/server_async_test.go internal/lsp/server_debounce_test.go internal/lsp/server_sync_test.go gen/references_crosspkg_test.go
git commit -m "feat(lsp): AnalyzeModule whole-module cross-reference analysis

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

### Task 3: server — cache, invalidation, and cross-package query

**Files:**
- Modify: `internal/lsp/documents.go` (add `allOpenGSX`)
- Modify: `internal/lsp/server.go` (add cache fields; invalidate in didOpen/didChange/didClose)
- Modify: `internal/lsp/references.go` (query moduleRefs, fall back to single-package; factor identification)
- Test: `gen/references_crosspkg_e2e_test.go` (Create — JSON-RPC e2e), `internal/lsp/references_cache_test.go` (Create — cache/invalidation/fallback units)

**Interfaces:**
- Consumes: `s.analyzer.AnalyzeModule`; `s.docs`; existing `posCoversCursor`, `s.locationForPos`, `byteOffsetForPosition`, `lineStartOffset`; `CrossRef`.
- Produces: `handleReferences` returns whole-module references; identification factored into `identifyCrossRef(refs []CrossRef, path string, curLine, curCol int) *CrossRef`.

- [ ] **Step 1: Add `allOpenGSX` to docStore**

In `internal/lsp/documents.go`:

```go
// allOpenGSX returns abs-file-path -> bytes for every open .gsx document, for
// whole-module analysis overrides (unsaved buffers across the module).
func (s *docStore) allOpenGSX() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for uri, d := range s.docs {
		p := uriToPath(uri)
		if strings.HasSuffix(p, ".gsx") {
			out[p] = []byte(d.text)
		}
	}
	return out
}
```

(`strings` is already imported in documents.go.)

- [ ] **Step 2: Add cache fields + invalidation**

In `internal/lsp/server.go`, add to the `Server` struct (near `pkgs`):

```go
	moduleRefs      []CrossRef // whole-module cross-reference index (lazy; find-references)
	moduleRefsValid bool       // false ⇒ rebuild on next references request
```

Add an unexported helper and call it from the three document handlers:

```go
// invalidateModuleRefs drops the cached whole-module reference index; the next
// references request rebuilds it. Any document mutation may change references.
func (s *Server) invalidateModuleRefs() {
	s.moduleRefs = nil
	s.moduleRefsValid = false
}
```

Call `s.invalidateModuleRefs()` at the top of `handleDidOpen`, `handleDidChange`, and `handleDidClose` (after each parses/validates its params — placing it once per handler is sufficient; it is cheap and idempotent).

- [ ] **Step 3: Write the failing cache/invalidation/fallback unit tests**

Create `internal/lsp/references_cache_test.go`. A `moduleRefsAnalyzer` double counts `AnalyzeModule` calls and returns a configurable result; `Analyze` returns a `Package` with a crafted single-package `CrossIndex` for the fallback test. Use a one-line component decl so cursor math is trivial.

```go
package lsp

import (
	"bytes"
	"go/token"
	"strings"
	"testing"
)

type moduleRefsAnalyzer struct {
	moduleCalls int
	moduleRefs  []CrossRef
	moduleErr   error
	pkg         *Package
}

func (a *moduleRefsAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	if a.pkg != nil {
		return a.pkg, nil
	}
	return &Package{}, nil
}
func (a *moduleRefsAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	a.moduleCalls++
	return a.moduleRefs, a.moduleErr
}

// drive runs the given frames through a fresh server over the analyzer and
// returns the raw output. Helper mirrors the existing server_*_test harness.
func drive(t *testing.T, a Analyzer, frames string) string {
	t.Helper()
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(frames), &out, a)
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}
```

Add two tests. **Invalidation** — two references requests with a `didChange` between trigger two `AnalyzeModule` calls; without the change, one (cached). **Fallback** — `AnalyzeModule` returns an error, and references still returns the single-package `CrossIndex` result.

```go
func TestReferencesCacheInvalidation(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	a := &moduleRefsAnalyzer{moduleRefs: nil} // nil result is valid (cached)
	// Two references with no change between → one AnalyzeModule call.
	frames := initFrame() + didOpenFrame(uri, text) +
		refsFrame(2, uri, 2, 10) + refsFrame(3, uri, 2, 10) + exitFrame()
	drive(t, a, frames)
	if a.moduleCalls != 1 {
		t.Fatalf("cached: want 1 AnalyzeModule call, got %d", a.moduleCalls)
	}
	// A didChange between two references → two AnalyzeModule calls.
	a2 := &moduleRefsAnalyzer{}
	frames2 := initFrame() + didOpenFrame(uri, text) +
		refsFrame(2, uri, 2, 10) + didChangeFrame(uri, text+"\n") +
		refsFrame(3, uri, 2, 10) + exitFrame()
	drive(t, a2, frames2)
	if a2.moduleCalls != 2 {
		t.Fatalf("invalidated: want 2 AnalyzeModule calls, got %d", a2.moduleCalls)
	}
}

func TestReferencesFallbackOnModuleError(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	// component Card starts at line 3 (1-based), the name at column 11.
	decl := token.Position{Filename: "/m/a.gsx", Line: 3, Column: 11}
	ref := token.Position{Filename: "/m/other.go", Line: 5, Column: 2}
	a := &moduleRefsAnalyzer{
		moduleErr: errFake,
		pkg: &Package{CrossIndex: map[string]CrossRef{
			"Card": {Name: "Card", Decl: decl, Refs: []token.Position{ref}},
		}},
	}
	// Cursor on "Card" (0-based line 2, character 10).
	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+refsFrame(2, uri, 2, 10)+exitFrame())
	if !strings.Contains(out, "other.go") {
		t.Fatalf("fallback path should return single-package ref; out:\n%s", out)
	}
}
```

You must supply the small frame helpers (`initFrame`, `didOpenFrame`, `didChangeFrame`, `refsFrame(id, uri, line, char)`, `exitFrame`) and `errFake` — model them on the existing `frame(...)`/JSON-RPC construction in `server_sync_test.go` / `gen/references_e2e_test.go`. `refsFrame` sends `textDocument/references` with `includeDeclaration:false`. After `didOpen`, the server populates `s.pkgs[dir]` from `Analyze`; for the fallback test that returns the crafted `pkg`. Confirm the cursor coordinates land on the component name by checking against `byteOffsetForPosition`/`lineStartOffset` (the `component ` keyword is 10 chars, so `Card` begins at 0-based character 10 on line 2).

Run: `go test ./internal/lsp/ -run 'TestReferencesCacheInvalidation|TestReferencesFallbackOnModuleError' -v`
Expected: FAIL (handleReferences does not yet call AnalyzeModule or fall back).

- [ ] **Step 4: Rewrite `handleReferences` to query the module index with fallback**

Replace the identification block in `internal/lsp/references.go`. Factor identification into a helper and try the module index first, then the single package:

```go
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, []Location{})
	}
	curLine := p.Position.Line + 1
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1

	// Whole-module index (lazy, cached, invalidated on edits). A successful
	// AnalyzeModule — even an empty result — is cached; an error leaves the
	// cache invalid so the single-package fallback below still answers.
	if !s.moduleRefsValid {
		if refs, err := s.analyzer.AnalyzeModule(filepath.Dir(path), s.docs.allOpenGSX()); err == nil {
			s.moduleRefs = refs
			s.moduleRefsValid = true
		}
	}

	found := identifyCrossRef(s.moduleRefs, path, curLine, curCol)
	if found == nil {
		// Fall back to the single-package index (covers AnalyzeModule errors and
		// any cursor the module index did not resolve).
		if pkg := s.pkgs[filepath.Dir(path)]; pkg != nil && len(pkg.CrossIndex) > 0 {
			vals := make([]CrossRef, 0, len(pkg.CrossIndex))
			for k := range pkg.CrossIndex {
				vals = append(vals, pkg.CrossIndex[k])
			}
			found = identifyCrossRef(vals, path, curLine, curCol)
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

// identifyCrossRef finds the component whose declaration (exact NamePos) or a
// .go reference covers the cursor. .gsx-file refs are skipped for identification
// — their //line-derived columns are approximate (see the original references
// comment), so a tag cursor resolves to "no match" rather than an off-column hit.
func identifyCrossRef(refs []CrossRef, path string, curLine, curCol int) *CrossRef {
	for i := range refs {
		cr := refs[i]
		if posCoversCursor(cr.Decl, path, curLine, curCol, len(cr.Name)) {
			return &refs[i]
		}
		for _, r := range cr.Refs {
			if strings.HasSuffix(r.Filename, ".go") &&
				posCoversCursor(r, path, curLine, curCol, len(cr.Name)) {
				return &refs[i]
			}
		}
	}
	return nil
}
```

Keep the existing package doc-comment on `handleReferences` (update it to note the whole-module index + single-package fallback). `filepath` and `strings` remain imported.

- [ ] **Step 5: Run the unit tests**

Run: `go test ./internal/lsp/ -run 'TestReferencesCacheInvalidation|TestReferencesFallbackOnModuleError' -v`
Expected: PASS. Then `go test ./internal/lsp/` → PASS (existing lifecycle/async/debounce/sync tests unaffected).

- [ ] **Step 6: Write the cross-package references e2e (JSON-RPC)**

Create `gen/references_crosspkg_e2e_test.go`. Mirror `gen/references_e2e_test.go`'s harness (`runLSP`, `frame`). Write the two-package fixture, `didOpen` `components/input.gsx`, and invoke `textDocument/references` with the cursor on the `Input` declaration; assert the output contains `post.gsx` (cross-package `.gsx` tag) and `use.go` (cross-package `.go` site). Add a second test invoking from the `use.go` cursor (cursor on `components.Input`) → same cross-package set.

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

func crossPkgModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n")
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n")
	return root
}

func TestReferencesCrossPkgFromDecl(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := crossPkgModule(t)
	inputSrc, _ := os.ReadFile(filepath.Join(root, "components", "input.gsx"))
	inputURI := "file://" + filepath.Join(root, "components", "input.gsx")
	// Cursor on "Input" in "component Input(...)": line 2 (0-based), char 10.
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": inputURI, "version": 1, "text": string(inputSrc)}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": inputURI},
			"position": map[string]any{"line": 2, "character": 10},
			"context":  map[string]any{"includeDeclaration": false}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "post.gsx") {
		t.Errorf("cross-pkg references missing post.gsx; out:\n%s", s)
	}
	if !strings.Contains(s, "use.go") {
		t.Errorf("cross-pkg references missing use.go; out:\n%s", s)
	}
}
```

Confirm the cursor coordinates resolve onto `Input` (the `component ` prefix is 10 chars, so the name starts at 0-based character 10 of line 2). Add `TestReferencesCrossPkgFromGoCursor` analogously, opening `use.go` and placing the cursor on `Input` in `components.Input` (compute its line/char the way `gen/references_e2e_test.go`'s `.go`-cursor test does).

Run: `go test ./gen/ -run 'TestReferencesCrossPkg' -v`
Expected: PASS.

- [ ] **Step 7: Run the full module suite**

Run: `go test ./...`
Expected: PASS (allow the usual multi-minute `gen`/`codegen` runtime).

- [ ] **Step 8: Commit**

```bash
git add internal/lsp/documents.go internal/lsp/server.go internal/lsp/references.go internal/lsp/references_cache_test.go gen/references_crosspkg_e2e_test.go
git commit -m "feat(lsp): cross-package find-references via cached whole-module index

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU"
```

---

## Self-Review

- **Spec coverage:** §3 codegen routing → Task 1; §4 AnalyzeModule + §5 interface → Task 2; §6 server cache/invalidation/query + §6.3 override → Task 3. §1 non-goals (tag cursor, sync build, `.x.go`-independence) are preserved by reusing the existing identification (tag stays empty) and the additive design. §8 test matrix → Tasks 1/2/3 tests.
- **Type consistency:** `AnalyzeModule(dir string, override map[string][]byte) ([]CrossRef, error)` identical across the interface (Task 2 Step 1), the four doubles (Task 2 Step 2), the real impl (Task 2 Step 4), and the cache double (Task 3 Step 3). `CrossRef{Name, Decl, Refs}` used consistently. `identifyCrossRef` signature matches both call sites.
- **Placeholder scan:** none — every step carries concrete code or an exact command. Two coordinate/`repoRoot` confirmations are called out explicitly as verification steps, not deferred work.
