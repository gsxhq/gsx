# Package Renderers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow a module-local `.gsx` package to provide a configured `[renderers]` function on a clean first generation, including direct same-package calls and order-independent cross-package generation.

**Architecture:** Split renderer harvesting into per-package signature collection plus whole-registry validation. Module-local renderer packages are type-checked from declaration-only GSX skeletons in memory, combined with externally loaded Go packages, and localized per consuming package before normal analysis/emission. External packages remain ordinary buildable Go inputs; no intermediate `.x.go`, second `packages.Load`, runtime dependency, or signature heuristic is introduced.

**Tech Stack:** Go 1.26.1, `go/types`, `golang.org/x/tools/go/packages`, GSX parser/skeleton/codegen, txtar semantic corpus, VitePress docs.

**Spec:** `docs/superpowers/specs/2026-07-13-package-renderers-design.md`

## Global Constraints

- Runtime root package remains standard-library-only and is not changed.
- External renderer packages are resolved only from buildable Go declarations; gsx never bootstraps another module's `.gsx`.
- Module-local renderer resolution uses exact module/package identity and `go/types`; no path guessing, string-parsed signatures, or “simple heuristic”.
- Preserve one external `packages.Load` per cold module, zero reloads for unrelated warm regeneration, and no typed load for `gsx fmt`.
- Renderer contract, last-wins behavior, exact type matching, escaping, sanitization, context injection, error propagation, and no-chain semantics remain unchanged.
- Same-package renderer calls are unqualified and never import their own package.
- Batch output is independent of requested-directory order and no intermediate generated file is written.
- Every semantic codegen change is pinned in `internal/corpus/testdata/cases/**`; regenerate corpus goldens, never hand-edit them.
- Use `GOCACHE=/tmp/gsx-package-renderers-gocache` for Go checks in the sandbox. Localhost-bound tests require the already-approved less-restricted `make check`/`make ci` execution.
- Run `gopls check -severity=hint` on every changed Go file.
- `docs/guide/**` prose containing literal `{{ }}` must use a `::: v-pre` block, and the sibling VitePress build is required.
- Work only in `/Users/jackieli/personal/gsxhq/gsx/.worktrees/package-renderers` for core changes. The final dogfood task touches only the named files in `/Users/jackieli/work/one-learning-gsx`.

## File Structure

- `internal/codegen/renderers.go`: renderer entry harvesting, whole-table validation, per-package localization, and call lowering.
- `internal/codegen/renderer_decls.go` (new): declaration-only local GSX package resolver and recursive importer.
- `internal/codegen/analyze.go`: declaration-only skeleton mode; full analysis remains the default.
- `internal/codegen/module.go`: completed renderer-package/table caches and per-directory table assembly.
- `internal/codegen/module_importer.go`: invalidation and exact implicit renderer dependencies.
- `internal/codegen/unused_imports_syntactic.go`: formatter fast path explicitly excludes renderer resolution.
- `internal/codegen/resolve_functions.go` (new): module-aware info surface using the same renderer resolver while preserving the existing external-only `ResolveFilters` API.
- `gen/cachekey.go`: local renderer package source hashes as cache inputs.
- `gen/info.go`: route `gsx info` through the module-aware resolver.
- `internal/corpus/testdata/cases/renderers/package_local/**`: same-package semantic proof.
- `internal/corpus/testdata/cases/renderers/package_cross/**`: clean cross-package/order-independence proof.
- `docs/guide/patterns/package-renderers.md`: recommended pgx package-renderer recipe.
- `/Users/jackieli/work/one-learning-gsx/{gsx.toml,ds/renderers/renderers.gsx,ds/renderers/renderers_test.go}`: real dogfood proof.

---

### Task 1: Separate renderer harvesting, validation, and per-package call form

**Files:**
- Modify: `internal/codegen/renderers.go`
- Modify: `internal/codegen/renderers_test.go`
- Modify: `internal/codegen/emit.go:121-140`

**Interfaces:**
- Consumes: existing `RendererAlias`, `rendererKey`, `rendererTable`, `classify`, and `applyRenderer`.
- Produces:
  - `func harvestRendererEntries(byPath map[string]*types.Package, renderers []RendererAlias, aliases map[string]string) (rendererTable, error)`
  - `func validateRendererTable(table rendererTable) error`
  - `func (t rendererTable) forPackage(pkgPath string) rendererTable`
  - `rendererEntry.local bool`

- [ ] **Step 1: Write failing unit tests for split validation and localization**

Add tests beside `TestHarvestRenderers` that construct synthetic packages with the existing `rendererFixturePkg`, `rendererSig`, and `namedType` helpers:

```go
func TestHarvestRendererEntriesDefersWholeTableValidation(t *testing.T) {
	text := namedType("example.com/pg", "Text", types.NewStruct(nil, nil))
	wrapped := namedType("example.com/pg", "Wrapped", types.NewStruct(nil, nil))
	pkgA := rendererFixturePkg("example.com/renda", map[string]*types.Signature{
		"Text": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(wrapped)}, false),
	})
	pkgB := rendererFixturePkg("example.com/rendb", map[string]*types.Signature{
		"Wrapped": rendererSig([]*types.Var{rparam(wrapped)}, []*types.Var{rresult(types.Typ[types.String])}, false),
	})
	aliases := map[string]string{"example.com/renda": "_gsxf0", "example.com/rendb": "_gsxf1"}
	entries, err := harvestRendererEntries(map[string]*types.Package{
		"example.com/renda": pkgA,
		"example.com/rendb": pkgB,
	}, []RendererAlias{
		{TypeKey: "example.com/pg.Text", PkgPath: "example.com/renda", FuncName: "Text"},
		{TypeKey: "example.com/pg.Wrapped", PkgPath: "example.com/rendb", FuncName: "Wrapped"},
	}, aliases)
	if err != nil { t.Fatal(err) }
	if err := validateRendererTable(entries); err == nil || !strings.Contains(err.Error(), "never chain") {
		t.Fatalf("validateRendererTable error = %v", err)
	}
}

func TestRendererTableForPackageMarksOnlyOwnerLocal(t *testing.T) {
	table := rendererTable{
		"p.A": {funcName: "A", alias: "_gsxf0", pkgPath: "app/renderers"},
		"p.B": {funcName: "B", alias: "_gsxf1", pkgPath: "external/renderers"},
	}.forPackage("app/renderers")
	if !table["p.A"].local || table["p.B"].local {
		t.Fatalf("localized table = %#v", table)
	}
}

func TestApplyRendererLocalCall(t *testing.T) {
	value := namedType("example.com/pg", "Text", types.NewStruct(nil, nil))
	table := funcTables{renderers: rendererTable{
		"example.com/pg.Text": {
			funcName: "Text", pkgPath: "example.com/app/renderers",
			local: true, result: types.Typ[types.String],
		},
	}}
	imports := map[string]bool{}
	var b bytes.Buffer
	got, _ := applyRenderer(&b, "v", value, table, imports, new(int), "return _gsxerr")
	if got != "Text((v))" { t.Fatalf("call = %q", got) }
	if len(imports) != 0 { t.Fatalf("imports = %v", imports) }
}
```

- [ ] **Step 2: Run the tests and verify the intended failures**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen -run 'Test(HarvestRendererEntriesDefersWholeTableValidation|RendererTableForPackageMarksOnlyOwnerLocal|ApplyRendererLocalCall)' -count=1 -v
```

Expected: compile failure for the undefined functions/`local` field.

- [ ] **Step 3: Refactor harvesting without changing existing diagnostics**

Move the per-registration loop from `harvestRenderers` into `harvestRendererEntries`. Move only the final result-chain/native-renderability loop into `validateRendererTable`, then keep the compatibility wrapper:

```go
func harvestRenderers(byPath map[string]*types.Package, renderers []RendererAlias, aliases map[string]string) (rendererTable, error) {
	table, err := harvestRendererEntries(byPath, renderers, aliases)
	if err != nil {
		return nil, err
	}
	if err := validateRendererTable(table); err != nil {
		return nil, err
	}
	return table, nil
}
```

`harvestRendererEntries` must retain last-wins insertion order, target lookup, signature contract, parameter identity, `hasErr`, `wantsCtx`, `result`, `alias`, and exact current error strings. `validateRendererTable` must retain chain-before-unsupported ordering.

- [ ] **Step 4: Add per-package localization and direct lowering**

Add `local bool` to `rendererEntry` and clone entries rather than mutating the shared table:

```go
func (t rendererTable) forPackage(pkgPath string) rendererTable {
	out := make(rendererTable, len(t))
	for key, entry := range t {
		entry.local = entry.pkgPath == pkgPath
		out[key] = entry
	}
	return out
}
```

In `applyRenderer`, build the call target and import need explicitly:

```go
target := e.funcName
if !e.local {
	imports[e.pkgPath] = true
	target = e.alias + "." + e.funcName
}
call := target + "(" + args + ")"
```

In `generateFile`'s renderer alias/bound-name loop, skip local entries so no empty/self alias is reserved:

```go
for _, e := range table.renderers {
	if e.local {
		continue
	}
	filterAlias[e.pkgPath] = e.alias
	boundNames[e.alias] = e.pkgPath
}
```

- [ ] **Step 5: Run focused and existing renderer tests**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen -run 'Test(HarvestRenderers|HarvestRendererEntries|RendererTableForPackage|ApplyRenderer)' -count=1
```

Expected: PASS with existing diagnostic assertions unchanged.

- [ ] **Step 6: Check and commit**

Run `gofmt -w internal/codegen/renderers.go internal/codegen/renderers_test.go internal/codegen/emit.go`, `gopls check -severity=hint` on those files, and `git diff --check`.

Commit:

```bash
git add internal/codegen/renderers.go internal/codegen/renderers_test.go internal/codegen/emit.go
git commit -m "refactor(codegen): localize renderer call targets"
```

---

### Task 2: Build declaration-only package skeletons for local GSX renderers

**Files:**
- Create: `internal/codegen/renderer_decls.go`
- Create: `internal/codegen/renderer_decls_test.go`
- Modify: `internal/codegen/analyze.go` (`buildSkeleton`, `emitComponentSkeleton`, GoWithElements lowering)
- Modify: existing `buildSkeleton` call sites in `internal/codegen/*_test.go`, `module_importer.go`, and `unused_imports_syntactic.go`

**Interfaces:**
- Consumes: `Module.parsePackageWithFset`, `buildSkeleton`, `checkSkeletonPackage` patterns, `dirForImportPath`, `Module.isGsxPackage`, and the external importer.
- Produces:
  - `type skeletonMode uint8`
  - `const skeletonFull skeletonMode = iota; skeletonDeclarations`
  - final `mode skeletonMode` argument on `buildSkeleton` and `emitComponentSkeleton`
  - `type rendererDeclResolver struct { m *Module; external types.Importer; pkgs map[string]*types.Package; loading map[string]bool }`
  - `func newRendererDeclResolver(m *Module, external types.Importer) *rendererDeclResolver`
  - `func (r *rendererDeclResolver) packageForDir(dir string) (*types.Package, error)`
  - `func (r *rendererDeclResolver) Import(path string) (*types.Package, error)`

- [ ] **Step 1: Write declaration resolver integration tests first**

Create temp-module tests that require and replace the current gsx repo. The primary fixture has no `.x.go`:

```go
func TestRendererDeclResolverSeesGoWithElementsFuncWithoutXGo(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	writeMultiFile(t, dir, "renderers.gsx", `package renderers

import (
	"github.com/gsxhq/gsx"
	"example.com/app/pg"
)

func Timestamptz(v pg.Timestamptz) gsx.Node {
	return <time>{v.Label}</time>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil { t.Fatal(err) }
	ext, err := m.externalImporter()
	if err != nil { t.Fatal(err) }
	pkg, err := newRendererDeclResolver(m, ext).packageForDir(dir)
	if err != nil { t.Fatal(err) }
	fn, ok := pkg.Scope().Lookup("Timestamptz").(*types.Func)
	if !ok { t.Fatalf("Timestamptz = %T", pkg.Scope().Lookup("Timestamptz")) }
	if got := fn.Type().String(); !strings.Contains(got, "func(v example.com/app/pg.Timestamptz) github.com/gsxhq/gsx.Node") {
		t.Fatalf("signature = %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "renderers.x.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renderer declaration resolution wrote output: %v", err)
	}
}
```

Add sibling tests for:

- a renderer function declared in a hand-written `.go` companion of a mixed `.gsx` package;
- a renderer signature importing a named type from another local `.gsx` package, proving recursive declaration imports;
- a real Go import cycle, which returns an import-cycle error rather than recursing;
- a function body containing locals/imports used only by markup, proving declaration mode ignores bodies and does not reject a valid signature.

- [ ] **Step 2: Run and confirm the resolver is undefined**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen -run TestRendererDeclResolver -count=1 -v
```

Expected: compile failure for `newRendererDeclResolver`.

- [ ] **Step 3: Add declaration mode to the existing skeleton pipeline**

Add `skeletonMode` and thread it through every `buildSkeleton` call; all existing calls pass `skeletonFull`.

In `emitComponentSkeleton`, preserve props structs and function/method signatures in both modes. Immediately after the signature/local-binding setup and before `emitProbes`, return a typed empty body in declaration mode:

```go
if mode == skeletonDeclarations {
	sb.WriteString("\treturn nil\n}\n")
	return nil
}
```

Apply this at both BYO and generated-props branches. Parsing/reserved-identifier checks still run so declaration syntax remains real.

In `buildSkeleton`'s top-level `GoWithElements` loop, declaration mode keeps every `GoText` part and replaces value parts without probing:

```go
case *gsxast.Element, *gsxast.Fragment:
	if mode == skeletonDeclarations {
		compBuf.WriteString("func() _gsxrt.Node { return nil }()")
		continue
	}
	// existing full probe path
case *gsxast.EmbeddedInterp:
	if mode == skeletonDeclarations {
		switch p.Lang {
		case gsxast.EmbeddedJS:
			compBuf.WriteString("_gsxrt.RawJS(\"\")")
		case gsxast.EmbeddedCSS:
			compBuf.WriteString("_gsxrt.RawCSS(\"\")")
		default:
			compBuf.WriteString("\"\"")
		}
		continue
	}
	// existing full probe path
```

Do not call `splitInterpEmbedded`, `emitProbes`, renderer application, or inference harvesting for elided declaration bodies.

- [ ] **Step 4: Implement the recursive declaration importer**

In `renderer_decls.go`, implement exact local routing:

```go
func (r *rendererDeclResolver) Import(path string) (*types.Package, error) {
	if dir, ok := dirForImportPath(r.m.opts.ModuleRoot, r.m.opts.ModulePath, path); ok && r.m.isGsxPackage(dir) {
		return r.packageForDir(dir)
	}
	return r.external.Import(path)
}
```

`packageForDir` must:

1. return `pkgs[dir]` on a completed cache hit;
2. fail on `loading[dir]` with `import cycle through <dir>`;
3. parse all package `.gsx` using the Module FileSet and overrides;
4. build each skeleton with `skeletonDeclarations` and a filter-only/empty renderer table;
5. parse skeleton files under their normal `.x.go` overlay paths;
6. include build-active hand-written `.go` companions exactly like `analyze`, excluding paths represented by skeletons;
7. type-check with a dedicated `types.Config{Importer: r, IgnoreFuncBodies: true}`;
8. ignore only exact unused-import diagnostics caused by intentionally elided bodies, using the existing `isUnusedImportMsg`; and
9. return an error for every other declaration/import/type error before caching the package.

Keep the package import path from `importPathForDir`, never the filesystem path.

- [ ] **Step 5: Run declaration and unchanged skeleton tests**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen -run 'Test(RendererDeclResolver|GoWithElements|Infer|UnusedImports)' -count=1
```

Expected: PASS. Existing full-mode skeleton goldens and diagnostics must not change.

- [ ] **Step 6: Check and commit**

Run gofmt, `gopls check -severity=hint` on `renderer_decls.go`, `renderer_decls_test.go`, and `analyze.go`, plus `git diff --check`.

Commit:

```bash
git add internal/codegen/renderer_decls.go internal/codegen/renderer_decls_test.go internal/codegen/analyze.go internal/codegen/module_importer.go internal/codegen/unused_imports_syntactic.go internal/codegen/*_test.go
git commit -m "feat(codegen): type-check local renderer declarations"
```

---

### Task 3: Resolve local and external renderers into every typed Module surface

**Files:**
- Modify: `internal/codegen/module.go`
- Modify: `internal/codegen/module_importer.go`
- Modify: `internal/codegen/filters.go`
- Create: `internal/codegen/module_renderers_test.go`
- Modify: `internal/codegen/module_filtercache_test.go`

**Interfaces:**
- Consumes: Task 1 harvesting/localization and Task 2 `rendererDeclResolver`.
- Produces:
  - `func (m *Module) rendererPackagesFromExt() (map[string]*types.Package, map[string]bool, error)` where the bool map is package-path-locality
  - `func (m *Module) rendererTableFor(dir string, filterPkgs []string) (rendererTable, error)`
  - `funcTablesFromExt(dir string, pkgs []string)` (adds consuming dir)
  - Module cache fields for declaration packages and their resolution error/done state

- [ ] **Step 1: Write failing clean-generation tests**

Create `module_renderers_test.go` with a helper that builds a temp module containing `pg/pg.go`, `renderers/renderers.gsx`, and `views/views.gsx`, with no `.x.go` files.

Same-package assertion:

```go
func TestGenerateLocalRendererPackageWithoutXGo(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	res, err := GenerateDirs(root, []string{rendererDir}, localRendererOptions(), nil)
	if err != nil { t.Fatal(err) }
	dr := res[rendererDir]
	if hasDiagErrors(dr.Diags) { t.Fatalf("diags = %v", dr.Diags) }
	src := generatedFor(t, dr, "renderers.gsx")
	if !strings.Contains(src, "Timestamptz((sample))") { t.Fatalf("generated:\n%s", src) }
	if strings.Contains(src, `"example.com/app/renderers"`) { t.Fatalf("self import:\n%s", src) }
}
```

`localRendererOptions` is fixed, not inferred from the fixture:

```go
func localRendererOptions() Options {
	return Options{
		FilterPkgs: []string{stdImportPath},
		Renderers: []RendererAlias{{
			TypeKey: "example.com/app/pg.Timestamptz",
			PkgPath: "example.com/app/renderers",
			FuncName: "Timestamptz",
		}},
	}
}
```

Cross-package/order assertion calls:

```go
res, err := GenerateDirs(root, []string{viewsDir, rendererDir}, opts, nil)
```

and verifies the consumer contains a reserved import/call while neither `.x.go` existed on disk during the batched call. Repeat with reversed dirs and compare generated maps byte-for-byte.

Add negative tests for missing local target, wrong signature, a chain spanning a local renderer plus a plain-Go external renderer, and an external-module `.gsx` renderer with no generated/plain Go declaration (it must retain the existing failure). Add a Bundle regression proving a prebuilt table remains authoritative and performs no filesystem bootstrap.

- [ ] **Step 2: Run and capture the current bootstrap failure**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen -run 'TestGenerate(LocalRendererPackageWithoutXGo|CrossPackageRendererWithoutXGo)' -count=1 -v
```

Expected: failure containing `func "Timestamptz" not found in package`.

- [ ] **Step 3: Partition and resolve renderer packages once per Module**

For the final last-wins registration set, classify a package as local GSX only when `dirForImportPath` succeeds and `m.isGsxPackage(dir)` is true. External and local Go-only paths keep `m.extPkgs` plus `m.extErrs` validation.

For local GSX paths, call one shared `rendererDeclResolver` and insert its `*types.Package` into the combined `byPath`; ignore the stale/partial external package and its errors for that exact path. Harvest all entries together and call `validateRendererTable` once.

Cache the unlocalized completed table/package set on the Module. The cached types share `m.fset` and must be cleared with it in `rebuildFset`.

- [ ] **Step 4: Assemble per-directory tables and localize exact owners**

Change the typed path to carry `dir`:

```go
func (m *Module) funcTablesFromExt(dir string, pkgs []string) (funcTables, error) {
	filters, err := m.filterTableFromExt(pkgs)
	if err != nil { return funcTables{}, err }
	renderers, err := m.rendererTableFor(dir, pkgs)
	if err != nil { return funcTables{}, err }
	return funcTables{filters: filters, renderers: renderers}, nil
}
```

Inside `rendererTableFor`, derive `pkgPath` with `importPathForDir` and return `base.forPackage(pkgPath)`. Keep alias assignment based on filter paths, explicit aliases, and renderer package paths in existing deterministic order.

Update `filterTableFor`'s per-dir cache key to include the consuming package path if localized tables can differ for two dirs with the same filter set.

- [ ] **Step 5: Preserve formatter fast-path behavior**

`cachedFuncTables`, used only by `buildPackageSkeletons(... withExt=false)`, must load filters/aliases with `renderers=nil` and return an empty renderer table. Update its comment and `module_filtercache_test.go` to assert formatter skeleton building does not resolve or load a clean local renderer package.

- [ ] **Step 6: Run module and renderer suites**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen -run 'Test(Generate.*Renderer|Module.*Renderer|FilterTable|PackageUnusedImportsDoesNotCallGoList)' -count=1
```

Expected: PASS; external load counters remain one cold / zero warm.

- [ ] **Step 7: Check and commit**

Run gofmt, `gopls check -severity=hint` on all changed Go files, and `git diff --check`.

Commit:

```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/filters.go internal/codegen/module_renderers_test.go internal/codegen/module_filtercache_test.go
git commit -m "feat(codegen): bootstrap module-local package renderers"
```

---

### Task 4: Make renderer edits invalidate watch, LSP, and warm Module state

**Files:**
- Modify: `internal/codegen/module.go`
- Modify: `internal/codegen/module_importer.go`
- Modify: `internal/codegen/invalidation_test.go`
- Modify: `gen/watchsession_test.go`

**Interfaces:**
- Consumes: Task 3 cached base renderer table/local package map.
- Produces:
  - `Module.rendererDirs map[string]bool` computed from configured module-local renderer paths
  - `func (m *Module) invalidateRendererStateLocked()`
  - exact implicit dependency edges from analyzed packages to local renderer dirs

- [ ] **Step 1: Write failing override and watch tests**

In `invalidation_test.go`, generate a consumer whose local renderer returns `string`, set an override changing the renderer to return `gsx.Node`, call `Package`/`Generate` again, and assert the emitted call is reclassified rather than served from the old table. Also assert unrelated overrides preserve `externalLoads()` and the renderer declaration cache.

In `watchsession_test.go`, start a session over renderer + consumer dirs, edit only `renderers.gsx`, call `regenPending`, and assert results include both dirs and the consumer output reflects the new signature.

- [ ] **Step 2: Run and verify stale state / missing dependent failures**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen ./gen -run 'Test.*Renderer.*(Invalidate|Override|Watch)' -count=1 -v
```

Expected: the Module test observes stale renderer metadata and/or the watch test regenerates only the renderer dir.

- [ ] **Step 3: Add exact renderer dependency invalidation**

At `Open`, map configured renderer package paths into module dirs with `dirForImportPath`; keep only paths owned by this module. This does not decide GSX-vs-Go-only behavior—the resolver still does that from source—but gives invalidation an exact directory identity before overrides are applied.

When any dirty/invalidation seed is a configured local renderer dir:

- clear declaration-package and completed-renderer-table caches;
- clear `dirFuncTbls` entries containing localized renderer tables;
- drop all retained `pkgTypes`, `pkgResults`, and `depFacts` whose generated classification may depend on the module-wide registry; and
- keep the external importer and filter table warm unless the normal dependency-change path reopens the Module.

In `recordImports`, add every resolved local GSX renderer dir as an implicit dependency of each analyzed dir except itself. This makes `Dependents(rendererDir)` return affected consumers for watch without fabricating source imports.

- [ ] **Step 4: Run invalidation, race, and watch suites**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen ./gen -run 'Test.*(Invalidat|Depend|Watch|Renderer)' -count=1
GOCACHE=/tmp/gsx-package-renderers-gocache go test -race ./internal/codegen -run 'Test.*Renderer' -count=1
```

Expected: PASS; no extra external load on a renderer `.gsx` override.

- [ ] **Step 5: Check and commit**

Run gofmt, gopls hints, and `git diff --check`.

Commit:

```bash
git add internal/codegen/module.go internal/codegen/module_importer.go internal/codegen/invalidation_test.go gen/watchsession_test.go
git commit -m "fix(codegen): invalidate package renderer dependents"
```

---

### Task 5: Include local renderer source in cache keys and `gsx info`

**Files:**
- Modify: `gen/cachekey.go`
- Modify: `gen/cachekey_test.go`
- Modify: `gen/info.go`
- Modify: `gen/info_test.go`
- Create: `internal/codegen/resolve_functions.go`
- Modify: `internal/codegen/filters.go`
- Modify: `internal/codegen/resolve_filters_test.go`

**Interfaces:**
- Consumes: Task 3 Module renderer resolver.
- Produces:
  - `func rendererDepDirs(renderers []codegen.RendererAlias, moduleRoot, modPath string) []string`
  - a module-aware renderer-info resolver used by `gen.runInfo`

- [ ] **Step 1: Write failing cache-key source-dependency tests**

Extend `TestComputeKeyRenderers` with a module-local renderer directory not imported by the consumer. Compute the key, change only `renderers/renderers.gsx`, recompute, and require different keys. Also prove an external renderer path adds no invented local directory hash.

- [ ] **Step 2: Write a failing `gsx info` clean-local-renderer test**

Create a temp module with `[renderers]`, a local `renderers.gsx`, no `.x.go`, and run `runInfo`. Assert exit 0 and output includes the registered type, renderer package, function, the existing `(R, error)` marker when applicable, and no function-not-found error.

- [ ] **Step 3: Run both tests to see current failures**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./gen ./internal/codegen -run 'Test(ComputeKeyRenderers|Info.*Renderer|ResolveFilters.*Renderer)' -count=1 -v
```

Expected: cache key stays equal after source change and info fails on the absent `.x.go`.

- [ ] **Step 4: Hash final local renderer package sources**

Resolve last-wins registrations per `TypeKey`, map renderer package paths under `modPath` to directories under `moduleRoot`, keep existing directories, deduplicate/sort, and add their `dirSourceHash` entries to the same dependency-hash set used by `computeKey`. Exclude `dir` itself because `own` already hashes it.

Use path identity only; do not search sibling directories or infer package names.

- [ ] **Step 5: Route info through the Module resolver**

Keep the public external-only `ResolveFilters` behavior for callers without module metadata. Add a module-aware entry point that opens a Module with `ModuleRoot`, `ModulePath`, filters, aliases, and renderers, reuses the single external importer plus local declaration resolver, and returns the existing sorted `FilterInfo`/`RendererInfo` shapes.

In `gen.runInfo`, resolve the actual module root/path with the existing `moduleRoot` helper and call the module-aware entry point. Do not issue an additional `packages.Load` after Module resolution.

- [ ] **Step 6: Run cache/info tests**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./gen ./internal/codegen -run 'Test(ComputeKeyRenderers|Info.*Renderer|Resolve.*Renderer)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Check and commit**

Run gofmt, gopls hints, and `git diff --check`.

Commit:

```bash
git add gen/cachekey.go gen/cachekey_test.go gen/info.go gen/info_test.go internal/codegen/filters.go internal/codegen/resolve_functions.go internal/codegen/resolve_filters_test.go
git commit -m "feat(gen): track and report package renderers"
```

---

### Task 6: Add canonical same-package and clean cross-package corpus cases

**Files:**
- Create: `internal/corpus/testdata/cases/renderers/package_local.txtar`
- Create: `internal/corpus/testdata/cases/renderers/package_cross.txtar`
- Generated by corpus update: golden sections inside those txtar files and `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: Tasks 1–5 complete generation behavior.
- Produces: authoritative syntax/codegen/render semantic coverage.

- [ ] **Step 1: Add the same-package source-only case**

Use this fixture shape (the corpus loader resolves `./` package references against the case module):

```txtar
-- gsx.toml --
[renderers]
"./pg.Timestamptz" = "corpustest/cases/renderers_package_local.Timestamptz"
-- pg/pg.go --
package pg
type Timestamptz struct { Label string }
-- input.gsx --
package views

import (
	"github.com/gsxhq/gsx"
	"corpustest/cases/renderers_package_local/pg"
)

func Timestamptz(v pg.Timestamptz) gsx.Node {
	return <time>{v.Label}</time>
}

func sample(label string) pg.Timestamptz { return pg.Timestamptz{Label: label} }

component Page(v pg.Timestamptz) {
	<div>{v}</div>
}
-- invoke --
Page(PageProps{V: sample("13 Jul 2026")})
```

- [ ] **Step 2: Confirm the case fails before golden update if implementation is incomplete**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/corpus -run 'TestCorpus/renderers/package_local' -count=1
```

Expected before Tasks 1–5: function-not-found. At this stage it may instead report missing goldens, which is the expected TDD state.

- [ ] **Step 3: Add the cross-package clean-batch case**

Place `rend/renderers.gsx` and `views/input.gsx` in the same txtar, register `./rend.Timestamptz`, and invoke the consumer. The renderer returns `<time>`; the consumer directly interpolates `pg.Timestamptz`. The generated renderer file must contain a direct call where it renders the type itself, while the consumer imports `rend` under a reserved alias.

- [ ] **Step 4: Regenerate canonical goldens**

Run:

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/corpus -run 'TestCorpus/renderers/package_(local|cross)' -update
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/corpus -run 'TestCorpus/renderers/package_(local|cross)' -count=1
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/corpus -run TestCorpus -count=1
```

Expected: generated goldens show no self import, consumer alias import, and render goldens contain the semantic `<time>` output.

- [ ] **Step 5: Inspect generated—not hand-written—goldens and commit**

Run `git diff --check` and inspect both txtar diffs plus `coverage.golden`.

Commit:

```bash
git add internal/corpus/testdata/cases/renderers/package_local.txtar internal/corpus/testdata/cases/renderers/package_cross.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): pin package renderer bootstrapping"
```

---

### Task 7: Publish the package-renderers pattern and roadmap status

**Files:**
- Create: `docs/guide/patterns/package-renderers.md`
- Modify: `docs/guide/patterns.md`
- Modify: `docs/guide/config.md`
- Modify: `docs/ROADMAP.md`

**Interfaces:**
- Consumes: final behavior and commands proven by Tasks 3 and 6.
- Produces: recommended pgx application pattern and discoverable config guidance.

- [ ] **Step 1: Write the pattern page with a complete pgx recipe**

The page must include this application-owned structure and explain each policy branch:

```gsx
package renderers

import (
	"time"

	"github.com/gsxhq/gsx"
	"github.com/jackc/pgx/v5/pgtype"
)

func Timestamptz(v pgtype.Timestamptz) gsx.Node {
	if !v.Valid {
		return <></>
	}
	switch v.InfinityModifier {
	case pgtype.Infinity:
		return <time>infinity</time>
	case pgtype.NegativeInfinity:
		return <time>-infinity</time>
	default:
		return <time datetime={v.Time.Format(time.RFC3339)}>{v.Time.Format(time.DateTime)}</time>
	}
}
```

Show:

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Timestamptz" =
  "example.com/app/ds/renderers.Timestamptz"
```

and:

```bash
go tool gsx generate ./ds/renderers
```

Explain that local `.gsx` targets bootstrap from source, external module targets must already be buildable Go, and consumers write `{row.CreatedAt}` directly. State that NULL, infinity, timezone, machine-readable value, and display label are application policy.

- [ ] **Step 2: Link and update roadmap**

Add “Package renderers” to `docs/guide/patterns.md`. Add a short link after the config section's introductory example. Update the shipped renderer roadmap entry with local `.gsx` bootstrap and change the pending pgx recipe checkbox into a shipped pattern entry linking the new page.

- [ ] **Step 3: Run docs safety and site build**

Run:

```bash
rg -n '\{\{' docs/guide/patterns/package-renderers.md docs/guide/patterns.md docs/guide/config.md
git diff --check
```

Then run the canonical website build from `/Users/jackieli/personal/gsxhq/gsxhq.github.io`, explicitly sourcing docs from this worktree:

```bash
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/package-renderers npm run build
```

Expected: the prebuild sync copies this branch's guide and the VitePress build succeeds with no Vue interpolation error.

- [ ] **Step 4: Commit docs**

```bash
git add docs/guide/patterns/package-renderers.md docs/guide/patterns.md docs/guide/config.md docs/ROADMAP.md
git commit -m "docs: add package renderers pattern"
```

---

### Task 8: Re-run the one-learning dogfood proof against the feature branch

**Files in `/Users/jackieli/work/one-learning-gsx`:**
- Modify: `gsx.toml`
- Create: `ds/renderers/renderers.gsx`
- Create: `ds/renderers/renderers_test.go`

**Interfaces:**
- Consumes: local `go.work` replacement pointing at `/Users/jackieli/personal/gsxhq/gsx/.worktrees/package-renderers` for the proof.
- Produces: the original real-world `pgtype.Timestamptz -> gsx.Node` registration and tests.

- [ ] **Step 1: Verify the dogfood worktree is clean in the three-file scope**

Run:

```bash
git status --short -- gsx.toml ds/renderers
git diff -- gsx.toml ds/renderers
```

Expected: no existing user changes in scope. Stop and report if those files are dirty.

- [ ] **Step 2: Write the failing renderer tests first**

Create tests that render invalid, finite, positive-infinity, and negative-infinity `pgtype.Timestamptz` values. Assert finite output has `<time datetime="...">...</time>`, invalid output is empty, and infinities render their explicit labels. Include one direct interpolation component so the registry—not a direct function call—is exercised.

- [ ] **Step 3: Confirm the pre-generation failure**

Run:

```bash
GOCACHE=/tmp/one-learning-gsx-gocache go test ./ds/renderers -count=1
```

Expected: undefined generated renderer/component symbols before generation.

- [ ] **Step 4: Add config and GSX implementation**

Add before `[formatter]`:

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Timestamptz" = "github.com/tespkg/one-learning/ds/renderers.Timestamptz"
```

Implement the same application policy as the docs pattern in `renderers.gsx` and add `component RenderTimestamptz(v pgtype.Timestamptz) { {v} }` so the tests prove registry interpolation rather than only calling the renderer directly. Do not add a Go shim or generated file to git.

- [ ] **Step 5: Point the workspace at the feature worktree and generate from clean source**

Temporarily update the uncommitted `go.work` use entry to `/Users/jackieli/personal/gsxhq/gsx/.worktrees/package-renderers`, then run:

```bash
GOCACHE=/tmp/one-learning-gsx-gocache go tool gsx fmt -w ./ds/renderers
GOCACHE=/tmp/one-learning-gsx-gocache go tool gsx generate ./ds/renderers
GOCACHE=/tmp/one-learning-gsx-gocache go test ./ds/renderers -count=1
```

Expected: initial generation succeeds without a pre-existing `renderers.x.go`; tests pass and rendered output contains the `<time>` node.

- [ ] **Step 6: Verify generated code and focused repo checks**

Inspect ignored `ds/renderers/renderers.x.go` and require:

- direct `Timestamptz(...)` call inside package `renderers`;
- no self import of `github.com/tespkg/one-learning/ds/renderers`; and
- renderer result lowered through normal `gsx.Node` rendering.

Restore the `go.work` use entry from the feature worktree to its original stable `/Users/jackieli/personal/gsxhq/gsx` path before checking repository status; the proof must not leave workspace plumbing changed.

Run:

```bash
GOCACHE=/tmp/one-learning-gsx-gocache go test ./ds/renderers ./ds/filters -count=1
git diff --exit-code -- go.work
git diff --check -- gsx.toml ds/renderers
```

- [ ] **Step 7: Commit only the dogfood source/config/tests**

Do not stage `go.work` or ignored `.x.go` files.

```bash
git add gsx.toml ds/renderers/renderers.gsx ds/renderers/renderers_test.go
git commit -m "feat(ds): add package renderers"
```

---

### Task 9: Authoritative verification and adversarial subsystem review

**Files:**
- Modify only files required by verified findings.

**Interfaces:**
- Consumes: Tasks 1–8.
- Produces: release-quality verification evidence and an independent probe review.

- [ ] **Step 1: Run focused clean tests and static checks**

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache go test ./internal/codegen ./internal/corpus ./gen -count=1
gopls check -severity=hint internal/codegen/renderers.go internal/codegen/renderer_decls.go internal/codegen/module.go internal/codegen/module_importer.go gen/cachekey.go gen/info.go
git diff --check
```

Expected: PASS/no diagnostics.

- [ ] **Step 2: Run project gates**

```bash
GOCACHE=/tmp/gsx-package-renderers-gocache make check
GOCACHE=/tmp/gsx-package-renderers-gocache make lint
GOCACHE=/tmp/gsx-package-renderers-gocache make ci
```

Expected: PASS. Run localhost-bound gates with the approved less-restricted execution.

- [ ] **Step 3: Dispatch an independent adversarial reviewer**

Give the reviewer only the spec, plan, and branch diff. Require throwaway temp-module probes for:

- same-package first generation with no `.x.go`;
- consumer-first cross-package batch order;
- external `.gsx` package remaining unsupported without generated Go;
- renderer result chain spanning local/external packages;
- same-package self-import absence;
- watch/LSP override invalidation; and
- cold/warm external-load counts.

The reviewer must report findings with exact repro commands and file/line references, not only read the diff.

- [ ] **Step 4: Address findings test-first and re-run affected gates**

For each valid finding, add a failing regression test, make the smallest real fix, run the focused suite, and commit with a scoped message. If there are no findings, make no review-only commit.

- [ ] **Step 5: Final status and branch cleanliness**

Run:

```bash
git status --short
git log --oneline --decorate main..HEAD
```

Expected: core worktree clean with focused commits; report the separate one-learning commit and all verification evidence.
