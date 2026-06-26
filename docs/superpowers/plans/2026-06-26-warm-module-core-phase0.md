# Warm Module-Analysis Core — Phase 0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a warm, in-process `Module` analysis core in `internal/codegen` whose composite **skeleton-based importer** resolves cross-package symbols from in-memory skeletons (never on-disk `.x.go`), and prove it by reproducing every corpus `generated.x.go.golden` through `Module.Generate`.

**Architecture:** A `Module` owns a per-package analysis cache and a `moduleImporter`. For a project gsx package, the importer type-checks that package's skeletons in-process (recursively, through itself) and returns the resulting `*types.Package`; for everything else it returns from a one-time `packages.Load`. `Module.Package(dir)` runs the existing pipeline (parse → propFields → buildSkeleton → in-process type-check → `harvest`) in-process and populates the same retained-analysis fields the go-list batch path produces. `Module.Generate(dir)` = `Package(dir)` + the existing `generateFile` emit.

**Tech Stack:** Go, `go/types`, `go/token`, `golang.org/x/tools/go/packages`, existing `internal/codegen` helpers (`buildSkeleton`, `harvest`, `generateFile`, `componentPropFieldsFor`, `loadFilterTableMulti`).

## Global Constraints

- Go module: `github.com/gsxhq/gsx`; Go `1.26.1` (verbatim from `go.mod`).
- Unexported by default; export only what an external consumer needs (none in Phase 0 — the public façade is Phase 3). Per user rule: types/fields/methods start lowercase unless they need serialization or cross-package use.
- `.x.go`-independent: project-package types come from skeletons; never read a dependency's on-disk `.x.go` for type resolution.
- Generation equivalence: `Module.Generate` output must be **byte-for-byte equal** to the existing batch output across the corpus (the Phase 0 gate).
- Every codegen change ships corpus + unit coverage ([[gsx-syntax-change-test-coverage]]).
- The gsx import graph is a DAG (Go forbids import cycles); still guard recursion with a `seen` set.
- Verify the binary before any manual `gsx` run: `gsx` can be shadowed by Ghostscript ([[gsx-binary-name-collides-ghostscript]]).

---

## File Structure

- **Create `internal/codegen/module.go`** — the `Module` type, `Options`, `Open`, file store (disk + override), `SetOverride`, `Package`, `Generate`. One responsibility: orchestrate per-package analysis + caching for one module root.
- **Create `internal/codegen/module_importer.go`** — `moduleImporter` (the composite `types.Importer`), the path↔dir helpers, and `checkSkeletonPackage` (in-process type-check that **returns** the `*types.Package`). One responsibility: produce a `*types.Package` for any import path, project or external.
- **Create `internal/codegen/module_test.go`** — unit tests for the file store, path↔dir helpers, single-package analysis, and cross-package no-`.x.go` resolution.
- **Create `internal/codegen/module_equiv_test.go`** — the corpus-equivalence gate (drives `Module.Generate` over the corpus dirs and diffs against the batch output).

- **Modify `internal/codegen/batch.go`** — extract the inline cross-index/nav-index block (`batch.go:371-473`) into the new shared `buildCrossNav` helper and call it (Task 5). This is the only existing-file change in Phase 0: a behavior-preserving extraction covered by the existing corpus + batch tests **and** the new equivalence gate.
- **Create `internal/codegen/crossnav.go`** — the shared `buildCrossNav` helper, called by both `batch.go` and `Module.Package` (one source of truth; no duplication).

---

## Task 1: Path ↔ import-path helpers

**Files:**
- Create: `internal/codegen/module_importer.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Produces:
  - `func importPathForDir(moduleRoot, modulePath, dir string) (string, bool)` — maps an absolute dir under `moduleRoot` to its Go import path; `ok=false` if `dir` is not under `moduleRoot`.
  - `func dirForImportPath(moduleRoot, modulePath, importPath string) (string, bool)` — inverse; `ok=false` if `importPath` is not under `modulePath`.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import "testing"

func TestImportPathDirRoundTrip(t *testing.T) {
	root := "/m"
	mod := "example.com/app"
	// dir under root → import path
	if got, ok := importPathForDir(root, mod, "/m/ui/admin"); !ok || got != "example.com/app/ui/admin" {
		t.Fatalf("importPathForDir = %q,%v; want example.com/app/ui/admin,true", got, ok)
	}
	// module root dir → bare module path
	if got, ok := importPathForDir(root, mod, "/m"); !ok || got != "example.com/app" {
		t.Fatalf("root dir: got %q,%v", got, ok)
	}
	// dir outside the module → not ok
	if _, ok := importPathForDir(root, mod, "/other/x"); ok {
		t.Fatalf("outside dir should be !ok")
	}
	// inverse
	if got, ok := dirForImportPath(root, mod, "example.com/app/ui/admin"); !ok || got != "/m/ui/admin" {
		t.Fatalf("dirForImportPath = %q,%v; want /m/ui/admin,true", got, ok)
	}
	if _, ok := dirForImportPath(root, mod, "fmt"); ok {
		t.Fatalf("stdlib path should be !ok")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestImportPathDirRoundTrip -v`
Expected: FAIL — `undefined: importPathForDir`.

- [ ] **Step 3: Write minimal implementation**

```go
package codegen

import (
	"go/types"
	"path/filepath"
	"strings"
	"sync"
)

// importPathForDir maps an absolute package dir under moduleRoot to its Go
// import path. ok is false when dir is not within moduleRoot.
func importPathForDir(moduleRoot, modulePath, dir string) (string, bool) {
	rel, err := filepath.Rel(moduleRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return modulePath, true
	}
	return modulePath + "/" + filepath.ToSlash(rel), true
}

// dirForImportPath is the inverse of importPathForDir. ok is false when
// importPath is not under modulePath (e.g. stdlib or third-party).
func dirForImportPath(moduleRoot, modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return moduleRoot, true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(importPath, prefix)
	return filepath.Join(moduleRoot, filepath.FromSlash(rel)), true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestImportPathDirRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module_importer.go internal/codegen/module_test.go
git commit -m "feat(codegen): import-path <-> dir helpers for the module core"
```

---

## Task 2: `checkSkeletonPackage` — in-process type-check that returns the package

**Files:**
- Modify: `internal/codegen/module_importer.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Consumes: existing `CachedResolver.check` body as the reference (`resolver.go:81-143`).
- Produces:
  - `func checkSkeletonPackage(dir, pkgName string, files []*goast.File, fset *token.FileSet, imp types.Importer) (*types.Package, *types.Info, []types.Error)` — type-checks an already-parsed set of skeleton (+ sibling `.go`) files against `imp`, returning the package, its info, and any type errors (never an `error`; a checker run always completes best-effort).

**Background:** `CachedResolver.check` builds `pkg := types.NewPackage(dir, pkgName)` and discards it. The importer needs that `*types.Package`. This function is the same checker run, parameterized by the importer and returning the package.

- [ ] **Step 1: Write the failing test**

```go
func TestCheckSkeletonPackageReturnsPkg(t *testing.T) {
	src := "package p\n\nfunc F() int { return 1 }\n"
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "/m/p/p.go", src, goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	pkg, info, errs := checkSkeletonPackage("/m/p", "p", []*goast.File{f}, fset, importer.Default())
	if len(errs) != 0 {
		t.Fatalf("unexpected type errors: %v", errs)
	}
	if pkg == nil || pkg.Scope().Lookup("F") == nil {
		t.Fatalf("pkg missing F")
	}
	if info == nil || info.Defs == nil {
		t.Fatalf("info not populated")
	}
}
```

Add imports to the test file: `"go/importer"`, `"go/token"`, `goparser "go/parser"`, `goast "go/ast"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestCheckSkeletonPackageReturnsPkg -v`
Expected: FAIL — `undefined: checkSkeletonPackage`.

- [ ] **Step 3: Write minimal implementation**

```go
// checkSkeletonPackage type-checks already-parsed package files against imp and
// returns the resulting *types.Package + *types.Info. Type errors are collected
// (not fatal): go/types fills Info best-effort even when some files don't check,
// matching the existing CachedResolver.check behaviour.
func checkSkeletonPackage(dir, pkgName string, files []*goast.File, fset *token.FileSet, imp types.Importer) (*types.Package, *types.Info, []types.Error) {
	info := &types.Info{
		Types: map[goast.Expr]types.TypeAndValue{},
		Defs:  map[*goast.Ident]types.Object{},
		Uses:  map[*goast.Ident]types.Object{},
	}
	var errs []types.Error
	conf := types.Config{
		Importer: imp,
		Error: func(e error) {
			if te, ok := e.(types.Error); ok {
				errs = append(errs, te)
			}
		},
	}
	pkg := types.NewPackage(dir, pkgName)
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files)
	return pkg, info, errs
}
```

Add to the file's imports: `goast "go/ast"`, `"go/token"`. (`types`, `sync` already imported from Task 1; drop `sync` if unused until Task 4.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestCheckSkeletonPackageReturnsPkg -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module_importer.go internal/codegen/module_test.go
git commit -m "feat(codegen): checkSkeletonPackage returns *types.Package for the importer"
```

---

## Task 3: `Module` skeleton + file store + `SetOverride`

**Files:**
- Create: `internal/codegen/module.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Produces:
  - `type Options struct { ModuleRoot, ModulePath string; FilterPkgs []string; Aliases []FilterAlias; FieldMatcher FieldMatcher; Classifier *attrclass.Classifier }`
  - `func Open(opts Options) (*Module, error)`
  - `func (m *Module) SetOverride(absPath string, src []byte)`
  - `func (m *Module) source(absPath string) ([]byte, bool)` — override first, else disk; unexported.

- [ ] **Step 1: Write the failing test**

```go
func TestModuleSourceOverrideThenDisk(t *testing.T) {
	dir := t.TempDir()
	onDisk := filepath.Join(dir, "a.gsx")
	if err := os.WriteFile(onDisk, []byte("DISK"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := m.source(onDisk); !ok || string(b) != "DISK" {
		t.Fatalf("disk read: %q,%v", b, ok)
	}
	m.SetOverride(onDisk, []byte("BUF"))
	if b, ok := m.source(onDisk); !ok || string(b) != "BUF" {
		t.Fatalf("override read: %q,%v", b, ok)
	}
	// in-memory-only path (no disk file) resolves from override
	mem := filepath.Join(dir, "mem.gsx")
	m.SetOverride(mem, []byte("MEM"))
	if b, ok := m.source(mem); !ok || string(b) != "MEM" {
		t.Fatalf("in-memory read: %q,%v", b, ok)
	}
}
```

Add test imports: `"os"`, `"path/filepath"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestModuleSourceOverrideThenDisk -v`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 3: Write minimal implementation**

```go
package codegen

import (
	"os"
	"sync"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// Options configures a Module. ModuleRoot is the absolute module root (dir
// containing go.mod); ModulePath is its declared module path (from go.mod).
type Options struct {
	ModuleRoot   string
	ModulePath   string
	FilterPkgs   []string
	Aliases      []FilterAlias
	FieldMatcher FieldMatcher
	Classifier   *attrclass.Classifier
}

// Module is a warm, in-process analysis graph for one module root. It is the
// single analysis core consumed by generate, watch, the LSP, fmt, and the
// playground. Not safe for concurrent mutation; callers serialize edits.
type Module struct {
	opts      Options
	overrides map[string][]byte // abs .gsx path -> in-memory source
	mu        sync.Mutex
}

// Open constructs a Module. It does not load anything yet; analysis is lazy.
func Open(opts Options) (*Module, error) {
	cls := opts.Classifier
	if cls == nil {
		cls = attrclass.Builtin()
		opts.Classifier = cls
	}
	return &Module{opts: opts, overrides: map[string][]byte{}}, nil
}

// SetOverride records in-memory source for a .gsx path (an unsaved editor buffer
// or playground source), shadowing disk content.
func (m *Module) SetOverride(absPath string, src []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides[absPath] = src
}

// source returns the bytes for absPath: override first, else disk.
func (m *Module) source(absPath string) ([]byte, bool) {
	m.mu.Lock()
	ov, ok := m.overrides[absPath]
	m.mu.Unlock()
	if ok {
		return ov, true
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, false
	}
	return b, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestModuleSourceOverrideThenDisk -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module.go internal/codegen/module_test.go
git commit -m "feat(codegen): Module type + override/disk file store"
```

---

## Task 4: `moduleImporter` — composite skeleton-based importer

**Files:**
- Modify: `internal/codegen/module_importer.go`, `internal/codegen/module.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Consumes: `checkSkeletonPackage` (Task 2), `importPathForDir`/`dirForImportPath` (Task 1), `Module.typesPackage` (defined here).
- Produces:
  - `type moduleImporter struct { m *Module; external types.Importer; seen map[string]bool }`
  - `func (mi *moduleImporter) Import(path string) (*types.Package, error)` — project gsx dir → `m.typesPackage(dir)`; else → `external.Import(path)`.
  - `func (m *Module) typesPackage(dir string) (*types.Package, error)` — type-check `dir`'s skeletons (recursively, via a `moduleImporter`) and cache the resulting `*types.Package`. **For Task 4 this may be a thin stub** that returns `external` for everything; the real body lands in Task 6 once `Package` exists. To keep Task 4 self-contained and testable, implement `typesPackage` here directly (it is small) and have `Package` reuse it in Task 6.

To avoid a forward dependency, Task 4 implements `typesPackage` against a **local** parse+skeleton+check (not the full `Package` harvest). Task 6's `Package` calls `typesPackage` for the requested dir and adds harvesting on top.

- [ ] **Step 1: Write the failing test** (cross-package, NO `.x.go` on disk)

```go
func TestModuleImporterCrossPackageNoXGo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs(t)+"\n")
	// dependency package `comp` with a component Button
	mkdirAll(t, filepath.Join(root, "comp"))
	writeFile(t, filepath.Join(root, "comp"), "comp.gsx", "package comp\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")
	// importer package `page` that calls comp.Button in a hole
	mkdirAll(t, filepath.Join(root, "page"))
	writeFile(t, filepath.Join(root, "page"), "page.gsx",
		"package page\n\nimport \"example.com/app/comp\"\n\ncomponent Home() {\n\t<div>{ comp.Button(\"hi\") }</div>\n}\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	// NOTE: no .x.go exists anywhere on disk.
	pkg, err := m.typesPackage(filepath.Join(root, "comp"))
	if err != nil {
		t.Fatalf("typesPackage(comp): %v", err)
	}
	if pkg.Scope().Lookup("Button") == nil {
		t.Fatalf("comp package missing Button (skeleton import failed)")
	}
	// page must type-check against comp's in-memory skeleton (the importer payoff)
	pagePkg, err := m.typesPackage(filepath.Join(root, "page"))
	if err != nil {
		t.Fatalf("typesPackage(page): %v", err)
	}
	if pagePkg == nil {
		t.Fatalf("page failed to type-check against in-memory comp")
	}
}
```

Add test helpers (in `module_test.go`): `writeFile`, `mkdirAll`, `repoRootAbs` (mirror the helpers already used by `line_anchor_test.go` / existing codegen tests — reuse the existing `writeFile` if present in the package's test files; otherwise add a local one).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestModuleImporterCrossPackageNoXGo -v`
Expected: FAIL — `m.typesPackage undefined`.

- [ ] **Step 3: Write minimal implementation**

In `module.go`, add the external-deps importer construction and `typesPackage`:

```go
import (
	// add:
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	gsxparser "github.com/gsxhq/gsx/parser"
	"golang.org/x/tools/go/packages"
)

// externalImporter lazily loads non-project dependency types once (stdlib,
// third-party, .go-only packages) and caches them. Project gsx packages never
// reach it (moduleImporter routes those to typesPackage).
func (m *Module) externalImporter() (types.Importer, error) {
	m.mu.Lock()
	if m.ext != nil {
		defer m.mu.Unlock()
		return m.ext, nil
	}
	m.mu.Unlock()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  m.opts.ModuleRoot,
	}
	loadPaths := append([]string{stdImportPath}, m.opts.FilterPkgs...)
	loadPaths = append(loadPaths, "./...")
	pkgs, err := packages.Load(cfg, loadPaths...)
	if err != nil {
		return nil, err
	}
	mp := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			mp[p.PkgPath] = p.Types
		}
	})
	m.mu.Lock()
	m.ext = mapImporter(mp)
	m.mu.Unlock()
	return m.ext, nil
}
```

Add fields to `Module`: `ext types.Importer` and `pkgTypes map[string]*types.Package` (cache).

In `module_importer.go`, add the composite importer + `typesPackage`:

```go
// moduleImporter resolves a project gsx package from the warm graph (skeletons)
// and everything else from external. seen breaks the (DAG-guaranteed-acyclic)
// recursion defensively.
type moduleImporter struct {
	m        *Module
	external types.Importer
	seen     map[string]bool
}

func (mi *moduleImporter) Import(path string) (*types.Package, error) {
	if dir, ok := dirForImportPath(mi.m.opts.ModuleRoot, mi.m.opts.ModulePath, path); ok {
		if mi.m.isGsxPackage(dir) {
			if mi.seen[dir] {
				// cycle guard (shouldn't happen — Go forbids import cycles)
				if p, ok := mi.m.pkgTypes[dir]; ok {
					return p, nil
				}
			}
			return mi.m.typesPackageWith(dir, mi)
		}
	}
	return mi.external.Import(path)
}

// isGsxPackage reports whether dir contains at least one .gsx file (disk or
// override).
func (m *Module) isGsxPackage(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if len(matches) > 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for p := range m.overrides {
		if filepath.Dir(p) == dir && strings.HasSuffix(p, ".gsx") {
			return true
		}
	}
	return false
}

// typesPackage type-checks dir's skeletons (building a fresh importer rooted at
// dir) and returns/caches the *types.Package. Entry point for external callers.
func (m *Module) typesPackage(dir string) (*types.Package, error) {
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	return m.typesPackageWith(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
}

// typesPackageWith does the work, threading the recursive importer.
func (m *Module) typesPackageWith(dir string, mi *moduleImporter) (*types.Package, error) {
	m.mu.Lock()
	if p, ok := m.pkgTypes[dir]; ok {
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()
	mi.seen[dir] = true

	gsxFiles, pkgName, err := m.parsePackage(dir)
	if err != nil {
		return nil, err
	}
	table, err := loadFilterTableMulti(m.opts.ModuleRoot, m.opts.FilterPkgs, m.opts.Aliases)
	if err != nil {
		return nil, err
	}
	propFields, nodeProps, byo, err := componentPropFieldsFor(dir, gsxFiles)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var goFiles []*goast.File
	for path, f := range gsxFiles {
		skel, _, _, berr := buildSkeleton(f, table, propFields, nodeProps, byo, m.opts.FieldMatcher, fset)
		if berr != nil {
			return nil, berr
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		gf, perr := goparser.ParseFile(fset, filepath.Join(dir, base+".x.go"), skel, goparser.SkipObjectResolution)
		if perr != nil {
			return nil, perr
		}
		goFiles = append(goFiles, gf)
	}
	// shared _gsxuse/_gsxcompsig helpers, mirroring the batch overlay.
	helper, _ := goparser.ParseFile(fset, filepath.Join(dir, "_gsxshared.x.go"),
		"package "+pkgName+"\n\nfunc _gsxuse(...any) {}\nfunc _gsxcompsig(any) {}\n", goparser.SkipObjectResolution)
	goFiles = append(goFiles, helper)

	pkg, _, _ := checkSkeletonPackage(dir, pkgName, goFiles, fset, mi)
	m.mu.Lock()
	if m.pkgTypes == nil {
		m.pkgTypes = map[string]*types.Package{}
	}
	m.pkgTypes[dir] = pkg
	m.mu.Unlock()
	return pkg, nil
}

// parsePackage parses every .gsx in dir (override-aware) and returns the parsed
// files + package name.
func (m *Module) parsePackage(dir string) (map[string]*gsxast.File, string, error) {
	paths := map[string]struct{}{}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.gsx"))
	for _, p := range matches {
		paths[p] = struct{}{}
	}
	m.mu.Lock()
	for p := range m.overrides {
		if filepath.Dir(p) == dir && strings.HasSuffix(p, ".gsx") {
			paths[p] = struct{}{}
		}
	}
	m.mu.Unlock()
	files := map[string]*gsxast.File{}
	fset := token.NewFileSet()
	pkgName := ""
	for p := range paths {
		src, ok := m.source(p)
		if !ok {
			continue
		}
		f, perrs := gsxparser.ParseFileWithClassifier(fset, p, src, 0, m.opts.Classifier)
		if len(perrs) > 0 {
			return nil, "", perrs[0]
		}
		wsnorm.Normalize(f)
		files[p] = f
		pkgName = f.Package
	}
	return files, pkgName, nil
}
```

> Note: `jsx.ResolveScripts` is intentionally omitted in `parsePackage` for Phase 0 (script resolution affects emit, not cross-package type identity). It is added in Task 6 where `Package`/`Generate` need emit-faithful files. If a corpus case in Task 7 reveals a skeleton difference, add `jsx.ResolveScripts(f, bag)` here.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestModuleImporterCrossPackageNoXGo -v`
Expected: PASS (cross-package types resolve from skeletons with no `.x.go` on disk).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/module_test.go
git commit -m "feat(codegen): composite skeleton-based importer (cross-pkg, no .x.go)"
```

---

## Task 5: `Module.Package(dir)` — full retained analysis (harvest)

**Files:**
- Modify: `internal/codegen/module.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Consumes: `typesPackageWith`, `harvest` (`analyze.go:983`), `buildSkeleton`, the batch's retained-analysis assembly (`batch.go:327-473`) as the reference for which fields to populate.
- Produces:
  - `func (m *Module) Package(dir string) (*PackageResult, error)` — returns a `*PackageResult` with `Files` empty (Generate fills them), and `GSXFset`, `Fset`, `Info`, `Types`, `ExprMap`, `GSXFiles`, `CrossIndex`, `NavIndex`, `Diags` populated, equivalent to the batch path's retained analysis for a single package.

This task refactors `typesPackageWith` to also retain `*types.Info`, the skeleton `*goast.File`s, their `[]*gsxast.Component`, and the gsx `*token.FileSet`, so `Package` can `harvest` and build the cross/nav indexes exactly as `batch.go` does. Extract the shared parse→skeleton→check work into an internal `analyzed` struct so both `typesPackage` (importer path, needs only `*types.Package`) and `Package` (needs everything) reuse it.

- [ ] **Step 1: Write the failing test**

```go
func TestModulePackageRetainsAnalysis(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs(t)+"\n")
	mkdirAll(t, filepath.Join(root, "page"))
	writeFile(t, filepath.Join(root, "page"), "page.gsx",
		"package page\n\ncomponent Home(name string) {\n\t<h1>Hi {name}</h1>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(filepath.Join(root, "page"))
	if err != nil {
		t.Fatal(err)
	}
	if pr.Info == nil || pr.Types == nil || pr.ExprMap == nil || pr.GSXFset == nil || pr.Fset == nil {
		t.Fatalf("retained analysis not populated: %+v", pr)
	}
	if _, ok := pr.CrossIndex[".Home"]; !ok {
		t.Fatalf("CrossIndex missing .Home: %v", pr.CrossIndex)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestModulePackageRetainsAnalysis -v`
Expected: FAIL — `m.Package undefined`.

- [ ] **Step 3: Write minimal implementation**

Refactor the body of `typesPackageWith` into a shared `func (m *Module) analyze(dir string, mi *moduleImporter) (*analyzed, error)` returning:

```go
type analyzed struct {
	pkgName    string
	gsxFiles   map[string]*gsxast.File   // gsx path -> parsed file
	gsxFset    *token.FileSet            // gsx positions
	skelFset   *token.FileSet            // skeleton positions
	goFiles    []*goast.File             // parsed skeletons + shared helper
	compsByXGo map[string][]*gsxast.Component // skeleton abs path -> components
	table      filterTable
	propFields map[string]map[string]bool
	nodeProps  map[string]map[string]bool
	byo        *byoData
	resolved   map[gsxast.Node]types.Type
	pkg        *types.Package
	info       *types.Info
	compByKey  map[string]*gsxast.Component  // componentKey -> component (for Name + NamePos)
	objKey     map[types.Object]string       // component func object -> componentKey
}
```

`analyze` must also build `compByKey` and `objKey` (the batch path builds these at `batch.go:328-369`) so `buildCrossNav` can run from an `analyzed`.

`typesPackage`/`typesPackageWith` keep returning `(*types.Package, error)` by calling `analyze` and returning `a.pkg`. (Importantly, `analyze` must parse the gsx files with the **gsx** fset it returns, and skeletons with the **skeleton** fset, matching `batch.go`'s two-fileset split.)

Then `Package` calls `analyze`, harvests, and assembles the result — mirroring `batch.go:325-473`:

```go
func (m *Module) Package(dir string) (*PackageResult, error) {
	ext, err := m.externalImporter()
	if err != nil {
		return nil, err
	}
	a, err := m.analyze(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
	if err != nil {
		return nil, err
	}
	res := &PackageResult{
		Files:    map[string][]byte{},
		GSXFset:  a.gsxFset,
		Fset:     a.skelFset,
		Info:     a.info,
		Types:    a.pkg,
		GSXFiles: a.gsxFiles,
		ExprMap:  map[gsxast.Node]goast.Expr{},
	}
	for _, gf := range a.goFiles {
		fname := a.skelFset.Position(gf.Pos()).Filename
		comps, ok := a.compsByXGo[fname]
		if !ok {
			continue
		}
		harvest(gf, comps, a.info, a.resolved, res.ExprMap)
	}
	res.CrossIndex, res.NavIndex = buildCrossNav(a.compByKey, a.objKey, a.gsxFset, a.skelFset, a.info, a.pkg)
	return res, nil
}
```

Extract the index/navIndex construction at `batch.go:371-473` into a shared
helper in the new `internal/codegen/crossnav.go`:

```go
// buildCrossNav builds a package's component cross-index (componentKey ->
// CrossRef with .gsx Decl + in-package Refs) and the navigable-reference index.
// gsxFset resolves .gsx declaration positions; skelFset (the skeleton fset)
// resolves use positions (//line-mapped back to .gsx for skeleton refs, real
// .go for hand-written refs). Shared by the go-list batch path and the Module
// core so both produce identical indexes.
func buildCrossNav(
	compByKey map[string]*gsxast.Component,
	objKey map[types.Object]string,
	gsxFset, skelFset *token.FileSet,
	info *types.Info,
	pkgTypes *types.Package,
) (map[string]CrossRef, []NavRef)
```

Move the body of `batch.go:371-473` into it **verbatim**, substituting the
parameters for the batch-local names (`fset`→`gsxFset`, `pkg.Fset`→`skelFset`,
`pkg.TypesInfo`→`info`, `pkg.Types`→`pkgTypes`). Then in `batch.go`, replace that
inline block with `res.CrossIndex, res.NavIndex = buildCrossNav(compByKey,
objKey, fset, pkg.Fset, pkg.TypesInfo, pkg.Types)`. `Module.Package` calls
`buildCrossNav(a.compByKey, a.objKey, a.gsxFset, a.skelFset, a.info, a.pkg)`.
The cross-package second pass (`batch.go` post-loop, `compObjOwner`) stays in
`batch.go` — it spans packages and is not part of single-package `buildCrossNav`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestModulePackageRetainsAnalysis -v`
Expected: PASS.

- [ ] **Step 4b: Verify the batch.go extraction is behavior-preserving**

The `buildCrossNav` extraction modified `batch.go`. Confirm the existing
cross-reference + corpus tests still pass:

Run: `go test ./internal/codegen/ ./internal/corpus/ -count=1`
Expected: all PASS (the extraction must not change batch output).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/crossnav.go internal/codegen/batch.go internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/module_test.go
git commit -m "refactor(codegen): extract shared buildCrossNav; Module.Package uses it"
```

---

## Task 6: `Module.Generate(dir)` — analysis + emit

**Files:**
- Modify: `internal/codegen/module.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Consumes: `analyze` (Task 5), `generateFile` (`emit.go:28`), `jsx.ResolveScripts`.
- Produces:
  - `func (m *Module) Generate(dir string) (map[string][]byte, []diag.Diagnostic, error)` — returns `gsxPath -> .x.go bytes` plus diagnostics.

Add `jsx.ResolveScripts(f, bag)` into `analyze`'s parse loop now (emit needs script-resolved files); guard so a script-resolution error surfaces as a diagnostic, not a panic.

- [ ] **Step 1: Write the failing test**

```go
func TestModuleGenerateProducesXGo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRootAbs(t)+"\n")
	mkdirAll(t, filepath.Join(root, "page"))
	gsxPath := filepath.Join(root, "page", "page.gsx")
	writeFile(t, filepath.Join(root, "page"), "page.gsx", "package page\n\ncomponent Home() {\n\t<h1>hello</h1>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	out, diags, err := m.Generate(filepath.Join(root, "page"))
	if err != nil {
		t.Fatalf("Generate: %v (diags=%v)", err, diags)
	}
	got := string(out[gsxPath])
	if !strings.Contains(got, "package page") || !strings.Contains(got, "func Home(") {
		t.Fatalf("unexpected generated output:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestModuleGenerateProducesXGo -v`
Expected: FAIL — `m.Generate undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
func (m *Module) Generate(dir string) (map[string][]byte, []diag.Diagnostic, error) {
	ext, err := m.externalImporter()
	if err != nil {
		return nil, nil, err
	}
	a, err := m.analyze(dir, &moduleImporter{m: m, external: ext, seen: map[string]bool{}})
	if err != nil {
		return nil, nil, err
	}
	bag := diag.NewBag(a.gsxFset)
	out := map[string][]byte{}
	for path, f := range a.gsxFiles {
		gen, ok := generateFile(f, a.resolved, a.table, a.propFields, a.nodeProps, a.byo,
			a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, bag, nil, nil)
		if !ok {
			continue
		}
		out[path] = gen
	}
	return out, bag.Sorted(), nil
}
```

> `generateFile` takes the **gsx** fset (`a.gsxFset`) — the same fset the gsx files were parsed with — matching `GeneratePackagesWithResolver`'s call (`resolver.go:362`, which passes the single `fset` it parsed `.gsx` with). Confirm the resolved map keys (gsx nodes) belong to `a.gsxFset`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestModuleGenerateProducesXGo -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/module.go internal/codegen/module_test.go
git commit -m "feat(codegen): Module.Generate emits .x.go from in-process analysis"
```

---

## Task 7: Corpus-equivalence gate

**Files:**
- Create: `internal/corpus/module_equiv_test.go` (lives in `internal/corpus`, which already imports `internal/codegen` and owns the case-materialization harness — do **not** reinvent it).
- Test: itself.

**Interfaces:**
- Consumes (from `internal/corpus`, read `internal/corpus/batch.go` to confirm signatures before writing):
  - the case loader that `TestCorpus`/`TestBatchCodegenSingleAndMulti` use to get `[]*caseDoc`;
  - `mustTempModule(repoRoot) string` — makes a temp module dir with a `go.mod` (`replace github.com/gsxhq/gsx => <repoRoot>`);
  - `writeCaseSources(moduleDir string, c *caseDoc) error` — writes a case's files under `caseModuleDir(moduleDir, c)`;
  - `caseModuleDir(moduleDir string, c *caseDoc) string`;
  - the per-case package dirs (the same `pkgDirs` `batchCodegen` builds at `batch.go:71-81`).
- Consumes (from `internal/codegen`): `codegen.Open`, `codegen.Options`, `codegen.Module.Generate`, `codegen.GeneratePackagesWithFilters` (oracle), `codegen.StdImportPath`.

**Goal:** For every corpus single-package codegen case, assert `Module.Generate` produces **byte-identical** `.x.go` to the existing batch path. This is the Phase 0 correctness gate.

- [ ] **Step 1: Write the test** (this IS the deliverable; it should fail only on real divergence)

```go
package corpus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestModuleMatchesBatchOverCorpus generates each single-package corpus case two
// ways — the legacy go-list batch and the new in-process Module core — and
// asserts byte-identical .x.go. The Module path reads NO on-disk .x.go.
func TestModuleMatchesBatchOverCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("equivalence gate loads packages; skipped in -short")
	}
	repoRoot, _ := filepath.Abs("../..")
	cases := loadCases(t) // SAME loader TestCorpus uses; confirm its name in corpus_test.go

	tmp := mustTempModule(repoRoot)
	defer os.RemoveAll(tmp)
	modulePath := modulePathOf(filepath.Join(tmp, "go.mod")) // read module path from the temp go.mod

	for _, c := range cases {
		c := c
		if !isSinglePackageCodegenCase(c) { // one .gsx, has generated.x.go.golden, no parse error
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			if err := writeCaseSources(tmp, c); err != nil {
				t.Fatalf("writeCaseSources: %v", err)
			}
			pkgDir := caseModuleDir(tmp, c) // single-package case → its one dir

			batchOut, err := codegen.GeneratePackagesWithFilters(tmp, []string{pkgDir},
				[]string{codegen.StdImportPath}, nil, nil, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("batch: %v", err)
			}
			br := batchOut[pkgDir]
			if br == nil || len(br.Files) == 0 {
				t.Skip("no codegen output (diagnostic-only case)")
			}

			m, _ := codegen.Open(codegen.Options{
				ModuleRoot: tmp, ModulePath: modulePath,
				FilterPkgs: []string{codegen.StdImportPath},
			})
			modOut, _, err := m.Generate(pkgDir)
			if err != nil {
				t.Fatalf("module: %v", err)
			}
			for gsxPath, want := range br.Files {
				if got := modOut[gsxPath]; string(got) != string(want) {
					t.Errorf("%s: Module.Generate != batch\n--- batch ---\n%s\n--- module ---\n%s",
						filepath.Base(gsxPath), want, got)
				}
			}
		})
	}
}
```

Before writing, read `internal/corpus/corpus_test.go` + `internal/corpus/batch.go` and substitute the real loader name (the function producing `[]*caseDoc`), the real single-package predicate (mirror `corpus_test.go:60-70` `single && !hasAstGolden && no parserDiag`), and `modulePathOf` (use `modulePathFromGoMod` if reachable, else parse the `module ` line). Start by gating to **3 representative cases** (`codegen-shape/greeting`, `components/component_spread`, `slots/node_prop_promotion`) via a name allowlist, prove green, then drop the allowlist.

- [ ] **Step 2: Run the gate (representative subset)**

Run: `go test ./internal/corpus/ -run TestModuleMatchesBatchOverCorpus -v`
Expected: PASS on the 3 representative cases. Any diff is a real Module bug — fix `analyze`/`buildCrossNav`/`Generate` until identical. Likely culprits: missing `jsx.ResolveScripts`, wrong fset into `generateFile`, missing `_gsxshared` helper, or skeleton/overlay path-name mismatch.

- [ ] **Step 3: Widen to all single-package corpus cases**

Drop the name allowlist. Run:
`go test ./internal/corpus/ -run TestModuleMatchesBatchOverCorpus`
Expected: PASS across all single-package cases.

- [ ] **Step 4: Full suite green**

Run: `go build ./... && go vet ./internal/codegen/ ./internal/corpus/ && go test ./internal/codegen/ ./internal/corpus/ -count=1`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/corpus/module_equiv_test.go
git commit -m "test(corpus): Module.Generate matches batch byte-for-byte (Phase 0 gate)"
```

---

## Self-Review notes (for the implementer)

- **Spec coverage:** This plan covers spec §3 (the `Module` + composite importer), §8 invariants (`.x.go`-independence via skeleton importer; in-memory via override store; generation equivalence via Task 7), and the §7 **Phase 0** gate. It does NOT cover `ModuleRefs`, `Invalidate`/metadata graph (Phase 2), the LSP wiring + Bug A/B (Phase 1), or consumer migration / public façade (Phase 3) — those are later plans.
- **Generation equivalence scope:** byte-for-byte equivalence holds for single-package, non-type-error corpus cases only. The corpus gate (Task 7) skips type-error cases. On packages that fail to type-check, batch emits nothing while Module.Generate emits best-effort output; proper type-error emission semantics are Phase 1.
- **Deferred for Phase 1:** (a) type-error emission parity with batch + surfacing `checkSkeletonPackage`'s `[]types.Error` as diagnostics; (b) closing the transitive `.x.go` boundary (gsx → Go-only → gsx) so all gsx-reachable symbols come from skeletons; (c) true concurrent-analysis support (current contract is one analysis at a time); (d) cache invalidation via Invalidate (pkgTypes/ext are not cleared on SetOverride).
- **Known approximation to resolve during execution:** Tasks 4–6 evolve `typesPackageWith` → `analyze`; ensure the final `analyze` is the single source both the importer and `Package`/`Generate` use (DRY). If the Task-4 stub shape fights the Task-5 refactor, implement `analyze` first and have Task 4's importer call it (reorder is fine).
- **Two filesets:** keep the gsx parse fset and the skeleton parse fset distinct, exactly as `batch.go` does; `harvest`/cross-index use skeleton positions (`//line`-mapped), `generateFile` uses the gsx fset.
- **No DRY debt:** `buildCrossNav` is extracted as a shared helper (Task 5) and called by **both** `batch.go` and `Module.Package` — one source of truth. The extraction is behavior-preserving (Step 4b runs the corpus + batch tests). This was a deliberate decision (batch.go is the only existing file Phase 0 modifies).
```
