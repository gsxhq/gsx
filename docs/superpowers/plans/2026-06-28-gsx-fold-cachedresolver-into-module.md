# Fold CachedResolver into the Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the parallel WASM codegen driver (`GeneratePackagesWithResolver` + `resolveTypesPkgWithFilters`) into the warm `Module` by making its external importer and filter table pluggable, then delete the duplicate driver and type-check.

**Architecture:** The `Module` already runs override-only (disk globs return empty in a browser; overrides supply all source). Only `externalImporter()` and `cachedFilterTable()` force a separate path — both call `packages.Load`. Inject those two from a prebuilt bundle via `Options.Bundle`; the Module then IS the WASM path. WASM generation goes through a fresh per-call Module. Delete the parallel driver/type-check and migrate the two tests that referenced them.

**Tech Stack:** Go 1.26.1, `go/types`, `golang.org/x/tools/go/packages`, the gsx `internal/codegen` package.

## Global Constraints

- **`.x.go`-independence:** the analysis core never reads generated `.x.go` to resolve symbols; bundle mode resolves from the injected importer + in-memory skeletons.
- **No "simple heuristics":** real implementations only.
- **Runtime root package stays standard-library-only;** tooling (`internal/codegen`, `gen`) may use `golang.org/x/tools`.
- **Don't hand-edit `.x.go` or golden files;** regenerate from source via `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- **One generation driver is the success criterion:** after this change `GeneratePackagesWithResolver` and the `typeResolver` abstraction no longer exist; `internal/codegen` has a single parse→skeleton→type-check→emit pipeline.
- **Run all commands from the worktree root** `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/lsp-gotodef`. Do NOT `cd` to the original repo root.
- **`gsx` binary collides with Ghostscript;** invoke the tool as `go run ./cmd/gsx …`.
- **Pin Go to 1.26.1** (gofmt drift on a different minor).
- **Inner-loop check:** `make check`. **Before merge to main:** `make ci`.
- The corpus goldens (`internal/corpus`, `TestCorpus`) and `gen/resolver_test.go` are byte/behavior oracles: they MUST stay green **unchanged** (no `-update`, no edits) through Tasks 1–5.

---

### Task 1: Inject a prebuilt bundle into the Module

Make `externalImporter()`/`cachedFilterTable()` return injected values when `Options.Bundle` is set, skipping `packages.Load`. This is the keystone: it proves the Module can type-check skeletons against a bundle importer, end to end, with zero `go list`.

**Files:**
- Modify: `internal/codegen/module.go` (add `Bundle` to `Options`; branch in `externalImporter` and `cachedFilterTable`)
- Modify: `internal/codegen/resolver.go` (add an `importer()` accessor to `CachedResolver`, next to the existing `filters()` at line 74)
- Test: `internal/codegen/bundle_module_test.go` (new)

**Interfaces:**
- Consumes: existing `CachedResolver` (`resolver.go`) with fields `imp types.Importer`, `table filterTable`, method `filters() filterTable`; `newCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*CachedResolver, error)`; `StdImportPath` const; `Open(Options) (*Module, error)`; `(*Module).SetOverride(absPath string, src []byte)`; `(*Module).Generate(dir string) (map[string][]byte, []diag.Diagnostic, error)` (map keyed by abs `.gsx` path).
- Produces: `Options.Bundle *CachedResolver`; `(*CachedResolver).importer() types.Importer`. When `Bundle != nil`, the Module performs no `packages.Load`.

- [ ] **Step 1: Write the failing test**

Create `internal/codegen/bundle_module_test.go`:

```go
package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestBundleModeMatchesGoList proves a Module driven by an injected Bundle
// (no packages.Load at analyze time) generates byte-identical .x.go to a Module
// using the default go-list importer. This is the end-to-end proof the WASM path
// can be the Module.
func TestBundleModeMatchesGoList(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent Hi(name string, count int) {\n\t<div data-n={count}>{name}</div>\n}\n"
	gsxPath := filepath.Join(pkgDir, "views.gsx")
	writeFile(t, pkgDir, "views.gsx", src)

	// Default go-list Module.
	mGo, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	goOut, goDiags, err := mGo.Generate(pkgDir)
	if err != nil {
		t.Fatalf("go-list generate: %v", err)
	}
	if len(goDiags) != 0 {
		t.Fatalf("go-list diags: %v", goDiags)
	}

	// Bundle Module: build the bundle once via packages.Load, then inject it.
	bundle, err := newCachedResolver(tmp, []string{StdImportPath}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mB, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}, Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if got := mB.externalLoads(); got != 0 {
		t.Fatalf("bundle Module did an external packages.Load (extLoads=%d), want 0", got)
	}
	bOut, bDiags, err := mB.Generate(pkgDir)
	if err != nil {
		t.Fatalf("bundle generate: %v", err)
	}
	if len(bDiags) != 0 {
		t.Fatalf("bundle diags: %v", bDiags)
	}

	if !bytes.Equal(goOut[gsxPath], bOut[gsxPath]) {
		t.Fatalf("bundle output differs from go-list output\n--- go-list ---\n%s\n--- bundle ---\n%s", goOut[gsxPath], bOut[gsxPath])
	}
	if len(bOut[gsxPath]) == 0 {
		t.Fatal("bundle produced empty output")
	}
}
```

- [ ] **Step 2: Run it to confirm it fails to compile**

Run: `go test ./internal/codegen -run TestBundleModeMatchesGoList -count=1`
Expected: FAIL — `unknown field 'Bundle' in struct literal` and `mB.externalLoads undefined`? (`externalLoads` exists.) The `Bundle` field and `importer()` accessor do not exist yet.

- [ ] **Step 3: Add the `importer()` accessor**

In `internal/codegen/resolver.go`, directly below the existing `filters()` method (line 74):

```go
// importer returns the prebuilt external importer so the Module can type-check
// skeletons against it without packages.Load (bundle mode).
func (c *CachedResolver) importer() types.Importer { return c.imp }
```

- [ ] **Step 4: Add `Bundle` to `Options`**

In `internal/codegen/module.go`, add to the `Options` struct (after `JSMinify bool`):

```go
	// Bundle, when non-nil, supplies the external importer and filter table
	// directly (a prebuilt CachedResolver) so the Module type-checks skeletons
	// with NO packages.Load / `go list` — the mode a WASM build uses. The Module
	// then operates override-only (callers SetOverride all source). Bundle mode is
	// GENERATION-ONLY: the bundle's *types.Package values live in a foreign
	// FileSet, so imported-object positions do not resolve against m.fset; use
	// Generate, not Package, in this mode.
	Bundle *CachedResolver
```

- [ ] **Step 5: Branch in `externalImporter` and `cachedFilterTable`**

In `internal/codegen/module.go`, at the very top of `externalImporter()` (before the `m.mu.Lock()` that checks `m.ext`):

```go
func (m *Module) externalImporter() (types.Importer, error) {
	if m.opts.Bundle != nil {
		// Bundle mode: the importer is prebuilt; no packages.Load. Returned
		// directly (not cached into m.ext) so rebuildFset's reset is harmless.
		return m.opts.Bundle.importer(), nil
	}
	m.mu.Lock()
	// ... unchanged ...
```

At the top of `cachedFilterTable()` (before the `m.mu.Lock()` that checks `m.filterTblDone`):

```go
func (m *Module) cachedFilterTable() (filterTable, error) {
	if m.opts.Bundle != nil {
		return m.opts.Bundle.filters(), nil
	}
	m.mu.Lock()
	// ... unchanged ...
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/codegen -run TestBundleModeMatchesGoList -count=1`
Expected: PASS.

- [ ] **Step 7: Run the codegen package to confirm no regressions**

Run: `go test ./internal/codegen -count=1`
Expected: PASS (all existing tests still green; the resolver path is untouched so far).

- [ ] **Step 8: Commit**

```bash
git add internal/codegen/module.go internal/codegen/resolver.go internal/codegen/bundle_module_test.go
git commit -m "feat(codegen): inject prebuilt bundle into the Module (Options.Bundle)"
```

---

### Task 2: Route the in-process (WASM) generation through the Module

Rewrite `gen/resolver.go`'s `generateInProcess` to drive a fresh per-call Module with the bundle, instead of calling `GeneratePackagesWithResolver`. `gen/resolver_test.go` is the oracle and must stay green unchanged.

**Files:**
- Modify: `gen/resolver.go:113-176` (rewrite `generateInProcess`)
- Read for reference: `gen/cache.go` (for `anyErrorDiag` — the error-severity helper) and `gen/gen.go` (`Result` type)
- Test: `gen/resolver_test.go` (existing, unchanged — the oracle)

**Interfaces:**
- Consumes: `Options.Bundle` and `(*CachedResolver).importer()` from Task 1; `Open`, `SetOverride`, `Generate`; `codegen.StdImportPath`; `gen.anyErrorDiag(diags []diag.Diagnostic) bool` (already used in `gen/cache.go`; if its exact name differs, grep `gen/cache.go` for the error-severity check and reuse it).
- Produces: `generateInProcess(b *codegen.CachedResolver, dir string, srcOverride map[string][]byte) (Result, error)` with identical external behavior (same `Result.Files` keying, same `Diags`, error non-nil iff an error-severity diagnostic was produced or an I/O failure occurred).

- [ ] **Step 1: Confirm the oracle currently passes**

Run: `go test ./gen -run TestCachedResolver -count=1` and `go test ./gen -run Generate -count=1`
Expected: PASS. (Record which `gen/resolver_test.go` tests exercise this path: `grep -n "func Test" gen/resolver_test.go`.)

- [ ] **Step 2: Rewrite `generateInProcess`**

Replace the body of `generateInProcess` in `gen/resolver.go` (lines 113-176). Keep the absolute-path and override-key resolution (lines 117-135) exactly as-is; replace the `codegen.GeneratePackagesWithResolver` call and result mapping:

```go
// generateInProcess drives a fresh per-call Module with the prebuilt bundle (no
// packages.Load / subprocess) and maps its output to the public gen.Result. A
// fresh Module per call keeps the in-process path stateless; the expensive load
// already happened once when the bundle was built.
func generateInProcess(bundle *codegen.CachedResolver, dir string, srcOverride map[string][]byte) (Result, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}

	// Resolve srcOverride keys to absolute paths (unchanged): relative keys like
	// "views/comp.gsx" resolve against the directory CONTAINING dir; absolute keys
	// pass through.
	absOverride := make(map[string][]byte, len(srcOverride))
	for k, v := range srcOverride {
		if filepath.IsAbs(k) {
			absOverride[k] = v
		} else {
			absOverride[filepath.Join(filepath.Dir(absDir), filepath.FromSlash(k))] = v
		}
	}

	// ModulePath == absDir reproduces the old types.NewPackage(dir, …) package
	// path exactly, so diagnostic type qualification is byte-identical to the
	// former CachedResolver path. The single playground package has no project
	// siblings, so nothing recurses through the skeleton importer.
	m, err := codegen.Open(codegen.Options{
		ModuleRoot: absDir,
		ModulePath: absDir,
		FilterPkgs: []string{codegen.StdImportPath},
		Bundle:     bundle,
	})
	if err != nil {
		return Result{}, err
	}
	for p, srcBytes := range absOverride {
		m.SetOverride(p, srcBytes)
	}

	out, diags, err := m.Generate(absDir)
	if err != nil {
		return Result{}, err
	}

	// Map out (abs .gsx path -> .x.go bytes) to Result.Files, preferring the
	// relative key form when the caller used relative keys (unchanged mapping).
	files := make(map[string][]byte, len(out))
	for absPath, content := range out {
		base := strings.TrimSuffix(absPath, ".gsx")
		absXGo := base + ".x.go"
		if len(srcOverride) > 0 {
			rel, relErr := filepath.Rel(filepath.Dir(absDir), absXGo)
			if relErr == nil && !strings.HasPrefix(rel, "..") {
				files[rel] = content
				continue
			}
		}
		files[absXGo] = content
	}

	var retErr error
	if anyErrorDiag(diags) {
		retErr = errInProcessDiagnostics
	}
	return Result{Files: files, Diags: diags}, retErr
}

// errInProcessDiagnostics is the sentinel returned when in-process generation
// produced at least one error-severity diagnostic (mirrors the old PackageResult.Err
// contract).
var errInProcessDiagnostics = errors.New("gen: diagnostics reported")
```

Add `"errors"` to the imports if not present, and confirm `anyErrorDiag` exists in package `gen` (it is used in `gen/cache.go`). If the helper has a different name, use that name; do NOT add a second copy.

- [ ] **Step 3: Update the call site signature**

The four public wrappers (`Generate`, `GenerateSource`, `GenerateSources`) call `generateInProcess(c.inner, …)`. `c.inner` is `*codegen.CachedResolver`, matching the rewritten signature's first param. No wrapper change needed. Verify with: `grep -n "generateInProcess" gen/resolver.go`.

- [ ] **Step 4: Build and run the oracle**

Run: `go build ./... && go test ./gen -run 'TestCachedResolver|Generate' -count=1`
Expected: PASS — byte-identical behavior. If a diagnostic string differs, the cause is almost certainly `ModulePath`; confirm it is set to `absDir`.

- [ ] **Step 5: Run the whole gen package**

Run: `go test ./gen -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gen/resolver.go
git commit -m "refactor(gen): route in-process generation through the Module"
```

---

### Task 3: Delete the parallel driver and parallel type-check; migrate the two orphaned tests

With no production caller left, remove `GeneratePackagesWithResolver` and the entire `resolveTypesPkg*`/`typeResolver`/`CachedResolver.check` machinery. Migrate the two tests that used them onto the surviving APIs.

**Files:**
- Modify: `internal/codegen/resolver.go` (delete `typeResolver`, `packagesLoadResolver`, `CachedResolver.check`, `cachedTypeErrors`, `GeneratePackagesWithResolver`; KEEP `CachedResolver` struct + fields + `filters()`/`importer()`, `newCachedResolver`/`NewCachedResolver`, `mapImporter`, `StdImportPath`)
- Modify: `internal/codegen/analyze.go` (delete `resolveTypesPkg` at lines 41-43 and `resolveTypesPkgWithFilters` at lines 45-117)
- Modify: `internal/codegen/resolver_test.go` (migrate `TestCachedResolverMatchesPackagesLoad` onto `checkSkeletonPackage` + `bundle.importer()`)
- Modify: `internal/codegen/analyze_test.go` (migrate `TestResolveAttrExprType` onto `Module.Package`)

**Interfaces:**
- Consumes: `checkSkeletonPackage(dir, pkgName string, files []*goast.File, fset *token.FileSet, imp types.Importer) (*types.Package, *types.Info, []types.Error)`; `(*CachedResolver).importer()`; `Module.Package(dir) (*PackageResult, error)` returning `PackageResult{GSXFiles map[string]*gsxast.File, ExprMap map[gsxast.Node]goast.Expr, Info *types.Info, …}`.
- Produces: nothing new (deletions + test migrations).

- [ ] **Step 1: Migrate `TestCachedResolverMatchesPackagesLoad`**

In `internal/codegen/resolver_test.go`, replace the body that calls `cached.check` (lines 121-156). Parse the skeleton fixture and call `checkSkeletonPackage` with the bundle importer instead:

```go
func TestCachedResolverMatchesPackagesLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	root := repoRoot(t)
	bundle, err := newCachedResolver(root, []string{stdImportPath}, nil, allowImportsFixture)
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	gf, err := goparser.ParseFile(fset, dir+"/comp.x.go", []byte(skeletonFixture), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	files := []*goast.File{gf}
	_, info, errs := checkSkeletonPackage(dir, "views", files, fset, bundle.importer())
	if len(errs) != 0 {
		t.Fatalf("unexpected type errors: %v", errs)
	}

	got := harvestUseTypes(files, info, fset)
	want := map[string]string{
		"name@18":  "string",
		"count@19": "int",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("type mismatch %s: cached=%q want %q", k, got[k], v)
		}
	}
	if t.Failed() {
		t.Logf("full harvest: %v", got)
	}
}
```

Add `goparser "go/parser"` to the test file's imports.

- [ ] **Step 2: Migrate `TestResolveAttrExprType`**

In `internal/codegen/analyze_test.go`, replace the `resolveTypesPkg` call (lines 29-60) with a `Module.Package` drive. After the existing fixture setup (go.mod + `views.gsx` written to `pkgDir`, lines 19-27), replace from the `fset :=` line down:

```go
	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	// Find the ExprAttr in the package's own parsed AST (Package re-parses).
	var attr *gsxast.ExprAttr
	for _, file := range pr.GSXFiles {
		gsxast.Inspect(file, func(n gsxast.Node) bool {
			if a, ok := n.(*gsxast.ExprAttr); ok {
				attr = a
			}
			return true
		})
	}
	if attr == nil {
		t.Fatal("no ExprAttr in AST")
	}
	goExpr, ok := pr.ExprMap[attr]
	if !ok || goExpr == nil {
		t.Fatalf("attr expr not mapped (ExprMap has %d entries)", len(pr.ExprMap))
	}
	tv := pr.Info.Types[goExpr]
	if tv.Type == nil {
		t.Fatal("attr expr type not resolved")
	}
	if b, ok := tv.Type.Underlying().(*types.Basic); !ok || b.Info()&types.IsString == 0 {
		t.Fatalf("attr expr type = %s, want string", tv.Type)
	}
```

Remove now-unused imports from `analyze_test.go` if `token`/`gsxparser` become unused (the compiler will tell you; only remove what's genuinely unused — other tests in the file may still use them).

- [ ] **Step 3: Run the two migrated tests (still compiling against the old code)**

Run: `go test ./internal/codegen -run 'TestCachedResolverMatchesPackagesLoad|TestResolveAttrExprType' -count=1`
Expected: PASS — the migrations are correct before any deletion.

- [ ] **Step 4: Delete the parallel type-check from `analyze.go`**

Delete `resolveTypesPkg` (lines 41-43) and `resolveTypesPkgWithFilters` (lines 45-117) entirely from `internal/codegen/analyze.go`.

- [ ] **Step 5: Delete the parallel driver from `resolver.go`**

In `internal/codegen/resolver.go` delete: the `typeResolver` interface (lines 31-36), `packagesLoadResolver` + its `check` (lines 38-59), `CachedResolver.check` (lines 76-144), `cachedTypeErrors` + its `Error()` (lines 146-159), and `GeneratePackagesWithResolver` (lines 212-374). KEEP: `StdImportPath`, the `CachedResolver` struct + `imp`/`table` fields + `filters()`/`importer()`, `newCachedResolver`, `NewCachedResolver`, `mapImporter`. Remove imports that become unused (`go/build`, `go/parser`, `os`, `errors`, `gsxast`, `jsx`, `wsnorm`, `gsxparser`, `diag` — keep only what the survivors use: `fmt`, `go/types`, `golang.org/x/tools/go/packages`, and whatever `newCachedResolver` needs).

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: clean compile. Fix any leftover references (there should be none in production code).

- [ ] **Step 7: Run codegen + gen**

Run: `go test ./internal/codegen ./gen -count=1`
Expected: PASS.

- [ ] **Step 8: Run the corpus golden as the byte oracle**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS with NO golden changes (do not pass `-update`).

- [ ] **Step 9: Commit**

```bash
git add internal/codegen/resolver.go internal/codegen/analyze.go internal/codegen/resolver_test.go internal/codegen/analyze_test.go
git commit -m "refactor(codegen): delete the parallel WASM driver and type-check; one driver"
```

---

### Task 4: Rename `CachedResolver` → `Bundle` (internal passive holder)

The internal type no longer resolves anything — it is a passive importer + filter-table holder. Rename it for honesty. The PUBLIC `gen.CachedResolver` wrapper name and the constructor names `NewCachedResolver`/`NewCachedResolverFromTypes` stay (external API the playground depends on).

**Files:**
- Modify: `internal/codegen/resolver.go` (`CachedResolver` → `Bundle`; constructors return `*Bundle`)
- Modify: `internal/codegen/bundle.go` (`NewCachedResolverFromTypes` returns `*Bundle`; struct literal `&CachedResolver{…}` → `&Bundle{…}`)
- Modify: `internal/codegen/module.go` (`Options.Bundle *CachedResolver` → `*Bundle`)
- Modify: `gen/resolver.go` (`inner *codegen.CachedResolver` → `*codegen.Bundle`; `generateInProcess(bundle *codegen.CachedResolver …)` → `*codegen.Bundle`)

**Interfaces:**
- Consumes: everything from Tasks 1–3.
- Produces: `codegen.Bundle` (was `codegen.CachedResolver`); `codegen.NewCachedResolver(...) (*Bundle, error)`; `codegen.NewCachedResolverFromTypes(...) (*Bundle, error)`. Public `gen` names unchanged.

- [ ] **Step 1: Rename the type and all internal references**

This is a mechanical rename of the identifier `CachedResolver` → `Bundle` within `internal/codegen` ONLY (do NOT touch the string `NewCachedResolver`/`NewCachedResolverFromTypes` function names, and do NOT touch the `gen` package's own `CachedResolver` type). Targeted edits:
- `resolver.go`: `type CachedResolver struct` → `type Bundle struct`; receivers `(c *CachedResolver)` → `(b *Bundle)` (rename the receiver var to `b` and update method bodies `c.imp`→`b.imp`, `c.table`→`b.table`); `func newCachedResolver(...) (*CachedResolver, error)` → `(*Bundle, error)` and its `return &CachedResolver{…}` → `&Bundle{…}`; `func NewCachedResolver(...) (*CachedResolver, error)` → `(*Bundle, error)`.
- `bundle.go`: `func NewCachedResolverFromTypes(...) (*CachedResolver, error)` → `(*Bundle, error)`; `return &CachedResolver{…}` → `&Bundle{…}`; update the file's top doc comment ("builds a CachedResolver" → "builds a Bundle").
- `module.go`: `Bundle *CachedResolver` → `Bundle *Bundle`.
- `gen/resolver.go`: `inner *codegen.CachedResolver` → `inner *codegen.Bundle`; `generateInProcess(bundle *codegen.CachedResolver …)` → `*codegen.Bundle`.

- [ ] **Step 2: Update the doc comment on `Bundle`**

Ensure the struct doc reads (in `resolver.go`):

```go
// Bundle carries a prebuilt external importer and filter table so the Module can
// type-check skeletons with no `go list`/packages.Load. A WASM build (browser,
// no toolchain) constructs a Bundle once via NewCachedResolver/NewCachedResolverFromTypes
// and injects it through Options.Bundle. Passive data — it resolves nothing itself.
// The zero value is invalid.
type Bundle struct {
	imp   types.Importer
	table filterTable
}
```

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: clean compile.

- [ ] **Step 4: Test codegen + gen**

Run: `go test ./internal/codegen ./gen -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/resolver.go internal/codegen/bundle.go internal/codegen/module.go gen/resolver.go
git commit -m "refactor(codegen): rename CachedResolver -> Bundle (passive holder)"
```

---

### Task 5: Diagnostic consistency — gate `Package` emit on type errors

`Module.Generate` skips `generateFile` for type-error packages (`module.go:422`), but `Module.Package` runs it unconditionally (`module.go:375-378`), producing spurious secondary diagnostics in the LSP. Gate `Package` the same way.

**Files:**
- Modify: `internal/codegen/module.go:375-378` (the emit-for-diagnostics loop in `Package`)
- Test: `internal/codegen/module_diag_test.go` (add a regression test; this file already exists)

**Interfaces:**
- Consumes: `analyzed.typeErrs []types.Error` (already threaded; `Generate` reads `len(a.typeErrs)==0`).
- Produces: no API change — `Package` no longer emits secondary diagnostics for type-error packages.

- [ ] **Step 1: Write the failing regression test**

Append to `internal/codegen/module_diag_test.go`:

```go
// TestPackageOmitsSecondaryDiagsOnTypeError proves Package surfaces ONLY the
// type-error diagnostic for a package that fails to type-check — not the spurious
// secondary "could not resolve type of interpolation" diagnostics that running
// generateFile on a type-error package would add. Mirrors Generate's gate.
func TestPackageOmitsSecondaryDiagsOnTypeError(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// {missing} references an undefined identifier -> a single type error. Running
	// generateFile anyway would add a secondary "could not resolve type" diag.
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent A() {\n\t<div>{missing}</div>\n}\n")

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	var errCount, secondary int
	for _, d := range pr.Diags {
		if d.Severity != diag.Error {
			continue
		}
		errCount++
		if strings.Contains(d.Message, "could not resolve type") {
			secondary++
		}
	}
	if errCount == 0 {
		t.Fatal("expected at least one type-error diagnostic")
	}
	if secondary != 0 {
		t.Fatalf("Package emitted %d secondary 'could not resolve type' diagnostics; want 0\nall diags: %v", secondary, pr.Diags)
	}
}
```

Ensure the test file imports `"os"`, `"path/filepath"`, `"strings"`, and `"github.com/gsxhq/gsx/internal/diag"` (add any missing).

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/codegen -run TestPackageOmitsSecondaryDiagsOnTypeError -count=1`
Expected: FAIL — `Package emitted N secondary 'could not resolve type' diagnostics; want 0`.

- [ ] **Step 3: Gate the emit loop**

In `internal/codegen/module.go`, wrap the emit-for-diagnostics loop in `Package` (currently lines 375-378) with the same gate `Generate` uses, and update the comment:

```go
	// Run emit for side-effect diagnostics only (unknown filter, attr-error, etc.).
	// Gated on len(a.typeErrs)==0, exactly like Generate: running generateFile on a
	// type-error package adds spurious secondary diagnostics (e.g. "could not resolve
	// type of interpolation") because resolved lacks entries for identifiers the
	// type-checker flagged. The type-error diagnostics themselves are already in the
	// bag (added in analyze). We discard the generated bytes; only bag side-effects matter.
	if len(a.typeErrs) == 0 {
		for _, f := range a.gsxFiles {
			generateFile(f, a.resolved, a.table, a.propFields, a.nodeProps, a.byo,
				a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, a.bag, nil, nil, true, true)
		}
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/codegen -run TestPackageOmitsSecondaryDiagsOnTypeError -count=1`
Expected: PASS.

- [ ] **Step 5: Run codegen + the LSP package to confirm no diagnostic-count regressions**

Run: `go test ./internal/codegen ./gen -count=1`
Expected: PASS. (If a `gen` LSP test asserted the OLD spurious diagnostics, that was asserting a bug — update it to expect only the type error, and note the change in the commit body.)

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/module.go internal/codegen/module_diag_test.go
git commit -m "fix(codegen): gate Package emit on type errors (no spurious LSP diags)"
```

---

### Task 6: Canonical go.mod module-path parser (fixes a cache-correctness bug)

`modulePathFromGoMod` is duplicated byte-for-byte in `gen/modroot.go` and `internal/codegen/generate_dirs.go`, and both use a naive `strings.HasPrefix(line, "module ") + TrimPrefix` that returns the WRONG path on two legal go.mod forms:
- `module example.com/foo // vanity` → returns `example.com/foo // vanity` (comment included)
- `module "example.com/foo"` → returns `"example.com/foo"` (quotes included)

The module path is load-bearing: `computeKey` (`gen/cachekey.go`) classifies in-module deps by it, so a wrong value silently corrupts incremental-cache invalidation. Replace both with one canonical helper backed by `golang.org/x/mod/modfile.ModulePath` (already in `go.mod`; `internal/codegen` is TOOLING, so the dependency is allowed — the stdlib-only rule binds the runtime root package, not the generator).

**Files:**
- Create: `internal/codegen/modpath.go` (the canonical helper)
- Create: `internal/codegen/modpath_test.go` (table test)
- Modify: `internal/codegen/generate_dirs.go` (`readModulePath` uses the helper; delete the local naive `modulePathFromGoMod`)
- Modify: `gen/modroot.go` (`moduleRoot` calls `codegen.ModulePathFromGoMod`; delete the local naive `modulePathFromGoMod`; drop the now-unused `strings` import if it becomes unused)

**Interfaces:**
- Produces: `codegen.ModulePathFromGoMod(data []byte) string` — exported (called from package `gen`).

- [ ] **Step 1: Write the failing table test**

Create `internal/codegen/modpath_test.go`:

```go
package codegen

import "testing"

func TestModulePathFromGoMod(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, src, want string }{
		{"plain", "module example.com/foo\n\ngo 1.26.1\n", "example.com/foo"},
		{"inline comment", "module example.com/foo // vanity import\n", "example.com/foo"},
		{"quoted path", "module \"example.com/foo\"\n", "example.com/foo"},
		{"with require block", "module example.com/foo\n\ngo 1.26.1\n\nrequire golang.org/x/mod v0.37.0\n", "example.com/foo"},
		{"no module directive", "go 1.26.1\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModulePathFromGoMod([]byte(tc.src)); got != tc.want {
				t.Errorf("ModulePathFromGoMod(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Prove the naive parser is wrong (strong RED)**

First create `internal/codegen/modpath.go` with `ModulePathFromGoMod` TEMPORARILY delegating to the existing naive local function, so the test exercises the current (buggy) behavior:

```go
package codegen

// ModulePathFromGoMod returns the module path declared in go.mod content.
func ModulePathFromGoMod(data []byte) string {
	return modulePathFromGoMod(data) // TEMPORARY: naive impl, replaced in Step 3
}
```

Run: `go test ./internal/codegen -run TestModulePathFromGoMod -count=1`
Expected: FAIL on `inline comment` (got `example.com/foo // vanity import`) and `quoted path` (got `"example.com/foo"`) — this concretely demonstrates the bug.

- [ ] **Step 3: Implement via modfile.ModulePath (GREEN)**

Replace `internal/codegen/modpath.go` with the real implementation:

```go
package codegen

import "golang.org/x/mod/modfile"

// ModulePathFromGoMod returns the module path declared in go.mod content, or ""
// if the content has no module directive. It delegates to modfile.ModulePath,
// which correctly handles inline comments (module x // c) and quoted module
// paths (module "x") — both of which a naive strings.TrimPrefix(line, "module ")
// mishandles. The module path is load-bearing for computeKey, so correctness here
// matters for incremental-cache invalidation.
func ModulePathFromGoMod(data []byte) string {
	return modfile.ModulePath(data)
}
```

Run: `go mod tidy` (promotes `golang.org/x/mod` from indirect to a direct require).
Run: `go test ./internal/codegen -run TestModulePathFromGoMod -count=1`
Expected: PASS (all five cases).

- [ ] **Step 4: Route both call sites through the helper; delete the duplicates**

In `internal/codegen/generate_dirs.go`, change `readModulePath` to call `ModulePathFromGoMod(data)` and DELETE the local `modulePathFromGoMod` function (and drop the now-unused `strings` import if nothing else in the file uses it).

In `gen/modroot.go`, change `moduleRoot`'s `return d, modulePathFromGoMod(data), nil` to `return d, codegen.ModulePathFromGoMod(data), nil`, DELETE the local `modulePathFromGoMod`, add the import `"github.com/gsxhq/gsx/internal/codegen"`, and drop the now-unused `strings` import (verify `strings` is not used elsewhere in `modroot.go` — it is not after the deletion).

- [ ] **Step 5: Build + test + corpus oracle**

Run: `go build ./... && go test ./internal/codegen ./gen -count=1 && go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS, no golden change.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/modpath.go internal/codegen/modpath_test.go internal/codegen/generate_dirs.go gen/modroot.go go.mod go.sum
git commit -m "fix(codegen): parse go.mod module path via modfile (correct on comments/quotes)"
```

---

### Task 7: Collapse GenOptions into Options; thread the known module path

`GenOptions` (`generate_dirs.go`) mirrors a 9-field subset of `Options` (`module.go`), and `GenerateDirs` copies it field-by-field into `Open(Options{…})`. Adding any codegen knob means editing four sites (Options, GenOptions, the copy block, callers). Delete `GenOptions`; have `GenerateDirs` take `Options` directly. Also thread the module path the caller already knows (`gen/cache.go` computes `modPath` via `groupByModule`, then `GenerateDirs` re-reads and re-parses the same go.mod and discards the result) so the redundant parse is dropped.

Every `GenOptions{…}` field name already exists on `Options` with the identical name, so the call-site change is a mechanical `GenOptions{` → `Options{` rename — no field renames, no new fields for existing callers.

**Files:**
- Modify: `internal/codegen/generate_dirs.go` (delete `GenOptions`; change `GenerateDirs` signature to take `Options`; derive `ModulePath` only when the caller left it empty)
- Modify: `gen/cache.go` (pass `codegen.Options{ModulePath: modPath, …}` so the re-parse is skipped)
- Modify: `internal/corpus/codegen.go` and ~28 `internal/codegen/*_test.go` + `gen/resolver_test.go` call sites (`GenOptions{` → `Options{`)

**Interfaces:**
- Produces: `GenerateDirs(moduleRoot string, dirs []string, opts Options, override map[string][]byte) (map[string]DirResult, error)`. `GenOptions` no longer exists.
- The `moduleRoot` parameter remains authoritative: `GenerateDirs` sets `opts.ModuleRoot = moduleRoot` unconditionally and sets `opts.ModulePath` via `readModulePath(moduleRoot)` ONLY when `opts.ModulePath == ""`. So existing callers that pass no module path keep working; `gen/cache.go` can pass a known `ModulePath` to skip the read.

- [ ] **Step 1: Change `GenerateDirs` to take `Options`; delete `GenOptions`**

In `internal/codegen/generate_dirs.go`, delete the `GenOptions` struct entirely and rewrite `GenerateDirs`:

```go
// GenerateDirs opens a fresh Module rooted at moduleRoot, applies any override
// bytes, and calls Module.Generate on each dir. opts carries the codegen knobs;
// GenerateDirs fills opts.ModuleRoot from moduleRoot and derives opts.ModulePath
// from go.mod only when the caller left it empty (callers that already know the
// module path pass it to skip the re-read). On a hard (non-diagnostic) error it
// returns immediately; otherwise each dir's result accumulates in the returned
// map, keyed by the same dir strings passed in. override maps absolute .gsx paths
// to in-memory source bytes; pass nil when no overrides are needed.
func GenerateDirs(moduleRoot string, dirs []string, opts Options, override map[string][]byte) (map[string]DirResult, error) {
	opts.ModuleRoot = moduleRoot
	if opts.ModulePath == "" {
		modPath, err := readModulePath(moduleRoot)
		if err != nil {
			return nil, fmt.Errorf("codegen: GenerateDirs: %w", err)
		}
		opts.ModulePath = modPath
	}
	m, err := Open(opts)
	if err != nil {
		return nil, fmt.Errorf("codegen: GenerateDirs: open module: %w", err)
	}
	for path, src := range override {
		m.SetOverride(path, src)
	}
	result := make(map[string]DirResult, len(dirs))
	for _, dir := range dirs {
		out, diags, err := m.Generate(dir)
		if err != nil {
			return nil, fmt.Errorf("codegen: GenerateDirs: generate %s: %w", dir, err)
		}
		result[dir] = DirResult{Files: out, Diags: diags}
	}
	return result, nil
}
```

- [ ] **Step 2: Update all call sites (`GenOptions{` → `Options{`)**

Mechanically replace `GenOptions{` with `Options{` (and `codegen.GenOptions{` with `codegen.Options{`) at every call site. Find them all:

```bash
grep -rln "GenOptions{" --include="*.go" internal gen
```

These are `internal/corpus/codegen.go`, `gen/resolver_test.go`, and the `internal/codegen/*_test.go` files. The field names are identical, so no field edits are needed. Do NOT leave any `GenOptions` reference behind: `grep -rn "GenOptions" --include="*.go" .` must return nothing after this step.

- [ ] **Step 3: Thread the known module path in `gen/cache.go`**

In `gen/cache.go`, the per-group generation already has `modPath` in scope (from `groupByModule`). Where it builds the codegen options (currently `genOpts := codegen.GenOptions{…}`), change it to `codegen.Options{ModulePath: modPath, …}` (keep all existing knob fields), so `GenerateDirs` skips the redundant go.mod read. Update both `GenerateDirs` call sites in `gen/cache.go` (the `miss` regeneration and `mustGen`) to pass the `Options` value.

- [ ] **Step 4: Build + full test + corpus oracle**

Run: `go build ./... && go test ./internal/codegen ./gen -count=1 && go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS, no golden change.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/generate_dirs.go gen/cache.go internal/corpus/codegen.go internal/codegen/*_test.go gen/resolver_test.go
git commit -m "refactor(codegen): GenerateDirs takes Options; drop GenOptions + redundant go.mod parse"
```

---

### Task 8: Debt sweep — dead field, doc-rot, dead params, modernize

Mechanical, low-risk cleanups for a launch-grade tree, in separate commits (one review unit).

**Files:**
- Modify: `internal/codegen/results.go` (delete the dead `PackageResult.Err` field)
- Modify: `internal/corpus/codegen.go`, `internal/corpus/batch.go` (rename `codegenGeneratePackages`)
- Modify: comment-only edits across `internal/codegen` AND `gen` (stale "batch path" / resolver references)
- Modify: `gen/cache.go`, `gen/init.go`, `internal/codegen/emit.go` (dead params, with care)
- Modify: production `.go` files flagged by `gopls check` modernize hints (NOT test files)

**Interfaces:** removes `PackageResult.Err`. Confirm it is dead first (Step 1).

- [ ] **Step 1: Delete the dead `PackageResult.Err` field**

`PackageResult.Err` (`internal/codegen/results.go`, documented as a "transition sentinel") now has ZERO writers and ZERO readers: the resolver path that set it was deleted in Task 3, and `gen/resolver.go` was rerouted off it in Task 2. Confirm, then delete:

```bash
grep -rn "\.Err\b" --include="*.go" internal/codegen gen/lsp.go gen/fmt.go   # PackageResult.Err — expect no hits referencing it (gen/cache.go .Errs is a different field)
```

Delete the `Err   error` field line from the `PackageResult` struct in `results.go`. Run `go build ./...` — a clean build proves nothing read it.

- [ ] **Step 2: Doc-rot + corpus rename**

Rename the internal corpus helper `codegenGeneratePackages` → `codegenDirs` in `internal/corpus/codegen.go` (definition) and `internal/corpus/batch.go` (call site + comments). Update its doc comment to say it drives `codegen.GenerateDirs`.

Then grep for stale references to the deleted batch/resolver paths and fix the COMMENTS (not behavior) across BOTH packages, including test files:

```bash
grep -rn "batch path\|GeneratePackagesWithResolver\|resolveTypesPkg\|the batch overlay\|matching batch\|cachedResolver.check\|batch.go" --include="*.go" internal/codegen gen
```

Reword each comment to describe current reality (e.g. "matching the batch overlay" → "the shared _gsxuse/_gsxcompsig helpers"; "mirroring resolveTypesPkg" → drop or point at `checkSkeletonPackage`; the `resolver_test.go` `TestCachedResolverMatchesPackagesLoad` docstring "verifies that cachedResolver.check …" → "verifies that checkSkeletonPackage + bundle.importer() …"). Leave code unchanged. Do NOT touch the `docs/superpowers/**` spec/plan files.

- [ ] **Step 3: Build + corpus, commit dead-field + doc-rot**

Run: `go build ./... && go test ./internal/codegen ./gen -count=1 && go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS, no golden change.

```bash
git add internal/codegen/results.go internal/corpus/codegen.go internal/corpus/batch.go internal/codegen gen
git commit -m "refactor(codegen): delete dead PackageResult.Err; purge stale batch/resolver comments"
```

- [ ] **Step 4: Dead parameters**

Remove the unused parameters gopls flags, one at a time (find by `gopls check -severity=hint`, not by stale line number), updating each call site:
- `gen/cache.go` unused `dir` — remove from signature + all callers.
- `gen/init.go` unused `force` — remove from signature + all callers.
- `internal/codegen/emit.go`: `emitCSSInterp`/`emitJSInterp` unused `fset`, `emitJSValue` unused `imports`. **Caution:** these emit helpers form a signature family. Before removing, check sibling emitters (`emitInterp`, `emitStyleInterp`, etc.): if they all carry `fset`/`imports` for uniform dispatch, removing it from one splits the family — in that case LEAVE it and skip (note in your report why). Only remove where the param is genuinely orphaned and no sibling symmetry is lost. Run `gopls check -severity=hint internal/codegen/emit.go` after to confirm.

For each removal: edit signature, fix callers, `go build ./...`.

- [ ] **Step 5: Build + test, commit dead params**

Run: `go build ./... && go test ./internal/codegen ./gen -count=1`
Expected: PASS.

```bash
git add gen/cache.go gen/init.go internal/codegen/emit.go
git commit -m "refactor: remove dead parameters flagged by gopls"
```

- [ ] **Step 6: Modernize production files**

Apply gopls modernize hints in PRODUCTION (non-`_test.go`) files only. Re-run to get the current list:

```bash
gopls check -severity=hint internal/codegen/*.go gen/*.go 2>&1 | grep -v "_test.go" | grep -v "\[js,wasm\]"
```

Apply each (`maps.Copy` for `m[k]=v` loops, `slices.Contains` for find loops, `min`/`max` for the if-assignments, `strings.CutPrefix` for HasPrefix+TrimPrefix, `strings.SplitSeq` for range-over-Split). Add the needed imports (`maps`, `slices`). Skip any hint whose rewrite would reduce clarity; modernization is polish, not a mandate.

- [ ] **Step 7: gofmt, build, full check, commit modernize**

Run: `gofmt -w internal/codegen gen && go build ./... && go test ./internal/codegen ./gen -count=1`
Expected: PASS.

```bash
git add internal/codegen gen
git commit -m "refactor: apply gopls modernize hints in production code"
```

- [ ] **Step 8: Full inner-loop check**

Run: `make check`
Expected: PASS (build/vet/test both modules, examples drift, gofmt + gsx fmt). Fix any drift before finishing.

---

## Self-Review

**1. Spec coverage:**
- Bundle injection (`Options.Bundle`, externalImporter/cachedFilterTable branch) → Task 1. ✓
- WASM entry via the Module → Task 2. ✓
- Delete `GeneratePackagesWithResolver`, `resolveTypesPkg*`, `packagesLoadResolver`, `typeResolver`, `CachedResolver.check`, `cachedTypeErrors` → Task 3. ✓
- Migrate the two orphaned tests → Task 3. ✓
- `CachedResolver` → `Bundle` rename → Task 4. ✓
- Diagnostic consistency → Task 5. ✓
- Canonical go.mod parser (cache-correctness bug; modfile.ModulePath) → Task 6. ✓
- Collapse `GenOptions` into `Options` + thread known modPath (drop redundant parse) → Task 7. ✓
- Dead `PackageResult.Err` field, doc-rot (incl. `gen/`), dead params, modernize → Task 8. ✓
- Generation-only bundle constraint documented → Task 1 Step 4 (Options doc) + Task 4 Step 2 (Bundle doc). ✓
- Testing strategy (byte-equivalence, override-only, diag-consistency, migrated tests, corpus backstop, gen oracle) → Tasks 1, 3, 5 + the unchanged corpus/gen oracles. Note: the byte-equivalence test (Task 1) also exercises an override-only-capable Module against a disk fixture; a pure no-disk override path is exercised by the unchanged `gen/resolver_test.go` (memDir). ✓

**2. Placeholder scan:** No TBD/TODO/"handle edge cases". Each code step carries full code. The one discretion point (emit.go dead-param signature family) has an explicit decision rule. ✓

**3. Type consistency:** `Options.Bundle` is `*CachedResolver` in Tasks 1–3, renamed to `*Bundle` in Task 4 (both the field and `gen/resolver.go`'s `inner`/param tracked together). `importer()`/`filters()` accessors consistent. `Module.Generate` returns `(map[string][]byte, []diag.Diagnostic, error)` used consistently in Tasks 1–2. `checkSkeletonPackage` signature matches Task 3 usage. `anyErrorDiag` reused, not duplicated. ✓
