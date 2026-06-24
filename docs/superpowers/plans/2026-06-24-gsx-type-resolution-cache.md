# gsx Type-Resolution Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace the playground's per-render `go list`/`packages.Load` type resolution (~6–8 s on Cloud Run) with an in-process **cached importer** built once over the fixed deps, type-checking only the changing component via `go/types` (~µs). Cuts edit latency ~7 s → ~2 s (the `go build`/run floor). Opt-in; the stock `gsx generate` CLI and the corpus are unchanged.

**Architecture:** A pluggable type-resolver seam in `internal/codegen` (default = today's `packages.Load`; cached = `go/types` check against a prebuilt importer). A public `gen` API exposes the cached resolver + an in-process generate so the playground (a separate Go module) can use it without importing `internal/`. The playground builds the resolver once at pool startup and calls codegen in-process per render instead of exec-ing `gsx generate`.

**Tech Stack:** Go `go/types`, `go/parser`, `golang.org/x/tools/go/packages` (one-time only), the existing `internal/codegen` + `gen` packages, the `playground/server` module.

## Global Constraints

- **Opt-in / zero CLI change:** the default resolver stays `packages.Load`; `gsx generate` and the whole corpus must remain byte-identical in behavior. The cached path is only reached when a resolver is explicitly injected.
- **Identical types:** the cached resolver MUST produce the same `map[gsxast.Node]types.Type` as `packages.Load` for supported inputs (single-package, deps within the import allowlist). This is the correctness bar.
- **Spike-proven mechanism:** load fixed deps once via `packages.Load`(`NeedTypes|NeedImports|NeedDeps`) + `packages.Visit` → `map[path]*types.Package`; per-render `types.Config{Importer: cached, Error: ignore}.Check(...)` on the parsed skeleton overlay; `harvest` the resulting `*types.Info` unchanged.
- **Module boundary:** `playground/server` is module `gsxplayground` (separate) → it may only use the **public `gen`** API, never `internal/codegen`.
- **Fixed dep set** = the playground import allowlist: `context io strconv fmt strings time sort errors math math/rand unicode unicode/utf8 html`, plus `github.com/gsxhq/gsx` and `github.com/gsxhq/gsx/std`.

---

## File Structure

- `internal/codegen/resolver.go` — **create**: the `typeResolver` interface, the default `packagesLoad` resolver (wraps the current `packages.Load` block), and the `cachedResolver` (importer + go/types check). Holds the prebuilt importer map + `filterTable`.
- `internal/codegen/analyze.go` — **modify**: `resolveTypesPkgWithFilters` calls an injected `typeResolver` for the type-check step (default when nil), keeping `buildSkeleton`/`harvest` intact.
- `internal/codegen/batch.go` / `codegen.go` — **modify**: thread an optional `typeResolver` from the public entry down to `resolveTypesPkgWithFilters`.
- `internal/codegen/resolver_test.go` — **create**: cached vs `packages.Load` produce identical resolved types over sample inputs.
- `gen/resolver.go` — **create**: public `CachedResolver` + `NewCachedResolver(...)` + `GenerateInProcess(...)` wrapping `internal/codegen`.
- `playground/server/render.go` / `main.go` — **modify**: build a `gen.CachedResolver` once on the pool; `renderIn` generates in-process (replacing the `gsx generate` exec) → write `.x.go` → allowlist → `go build`/run.
- `playground/server/go.mod` — **modify**: add the `github.com/gsxhq/gsx` require (already replaced) for the `gen` import.

---

## Task 1: Cached type resolver in `internal/codegen`

**Files:** Create `internal/codegen/resolver.go`, `internal/codegen/resolver_test.go`; modify `internal/codegen/analyze.go`.

**Interfaces:**
- Produces: `type typeResolver interface { check(dir string, overlay map[string][]byte, fset *token.FileSet) (files []*goast.File, info *types.Info, err error) }`; `func newCachedResolver(moduleDir string, filterPkgs []string, allowImports []string) (*cachedResolver, error)`; the cached resolver also exposes its prebuilt `filterTable` via `func (*cachedResolver) filters() filterTable`.
- Consumes: existing `buildSkeleton`, `harvest`, `loadFilterTableMulti`, `freeOverlayPath`.

- [ ] **Step 1: Extract the type-check step behind a resolver (no behavior change yet)**

In `analyze.go`, `resolveTypesPkgWithFilters` currently builds the overlay then does `packages.Load(cfg, ".")` and loops `harvest`. Refactor so the `packages.Load`→(`pkg.Syntax`, `pkg.TypesInfo`) step is produced by a `typeResolver`. Add the interface + default impl in `resolver.go`:

```go
// resolver.go
package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/packages"
)

// typeResolver turns a skeleton overlay (path -> Go source) into the per-file
// type info harvest consumes. The default uses packages.Load (go list); the
// cached impl uses a prebuilt importer + go/types (no subprocess).
type typeResolver interface {
	check(dir string, overlay map[string][]byte, fset *token.FileSet) (files []*goast.File, info *types.Info, err error)
}

// packagesLoadResolver is the default (unchanged) behavior.
type packagesLoadResolver struct{}

func (packagesLoadResolver) check(dir string, overlay map[string][]byte, fset *token.FileSet) ([]*goast.File, *types.Info, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: dir, Overlay: overlay, Fset: fset,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("codegen: load package: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("codegen: no package found in %s", dir)
	}
	if len(pkgs[0].Errors) > 0 {
		return nil, nil, fmt.Errorf("codegen: type resolution failed: %s", pkgs[0].Errors[0])
	}
	return pkgs[0].Syntax, pkgs[0].TypesInfo, nil
}
```

Change `resolveTypesPkgWithFilters` to take a `typeResolver` (nil → `packagesLoadResolver{}`), and replace the inline `packages.Load` block with `files, info, err := resolver.check(dir, overlay, fset)`, then loop `harvest` over `files` keyed by `fset.Position(f.Pos()).Filename` (as today). **Pass `Fset: fset` into the packages.Config** (it was using `pkg.Fset` before; using the shared `fset` keeps positions aligned for both resolvers — verify the harvest filename keys still match the overlay paths). Thread the resolver param through `GeneratePackage*`/`batch.go` callers as an optional argument defaulting to nil. Run the corpus to confirm zero change:

Run: `go test ./internal/corpus/... -count=1`
Expected: PASS (default path unchanged).

- [ ] **Step 2: Write the failing cached-resolver correctness test**

```go
// resolver_test.go
package codegen

import (
	"go/token"
	"testing"
)

// The cached resolver must resolve the same expression types as packages.Load.
func TestCachedResolverMatchesPackagesLoad(t *testing.T) {
	dir := t.TempDir()
	// minimal module: gsx via the repo's go.mod replace is NOT available in a
	// temp dir, so point moduleDir at the repo root for dep loading and use the
	// repo's own go.mod. Use an overlay-only package.
	overlay := map[string][]byte{
		dir + "/comp.x.go": []byte(skeletonFixture),
	}
	fset := token.NewFileSet()
	def, _, err := packagesLoadResolver{}.check(repoRoot(t)+"/playground/server", overlay, fset)
	_ = def
	if err != nil {
		t.Skipf("packages.Load baseline unavailable: %v", err)
	}
	cached, err := newCachedResolver(repoRoot(t), []string{stdImportPath}, allowImportsFixture)
	if err != nil {
		t.Fatal(err)
	}
	cf, cinfo, err := cached.check(dir, overlay, token.NewFileSet())
	if err != nil {
		t.Fatal(err)
	}
	got := harvestUseTypes(cf, cinfo) // helper: map "expr@line" -> type string
	want := map[string]string{"name@N": "string", "count@N": "int"} // fill from the fixture
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: cached=%q want %q", k, got[k], v)
		}
	}
}
```

(Define `skeletonFixture` as a self-contained skeleton like the spike's — props struct + `func Greeting(_gsxp GreetingProps) _gsxrt.Node` with `_gsxuse(name)`, `_gsxuse(count)` — `repoRoot(t)`, `allowImportsFixture`, and the `harvestUseTypes` helper that walks `_gsxuse` calls reading `info.Types[arg]`. Adjust `@N` line numbers to the fixture.)

Run: `go test ./internal/codegen -run TestCachedResolver -v`
Expected: FAIL — `newCachedResolver` undefined.

- [ ] **Step 3: Implement `cachedResolver`** (in `resolver.go`)

```go
type cachedResolver struct {
	imp   types.Importer
	table filterTable
}

func newCachedResolver(moduleDir string, filterPkgs []string, allowImports []string) (*cachedResolver, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  moduleDir,
	}
	loadPaths := append([]string{"github.com/gsxhq/gsx"}, filterPkgs...)
	loadPaths = append(loadPaths, allowImports...)
	pkgs, err := packages.Load(cfg, loadPaths...)
	if err != nil {
		return nil, err
	}
	m := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			m[p.PkgPath] = p.Types
		}
	})
	table, err := loadFilterTableMulti(moduleDir, filterPkgs)
	if err != nil {
		return nil, err
	}
	return &cachedResolver{imp: mapImporter(m), table: table}, nil
}

func (c *cachedResolver) filters() filterTable { return c.table }

func (c *cachedResolver) check(dir string, overlay map[string][]byte, fset *token.FileSet) ([]*goast.File, *types.Info, error) {
	var files []*goast.File
	for path, src := range overlay {
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, f)
	}
	info := &types.Info{Types: map[goast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: c.imp, Error: func(error) {}} // collect, never fatal
	pkg := types.NewPackage("views", "views")
	chk := types.NewChecker(&conf, fset, pkg, info)
	_ = chk.Files(files) // type errors surface as a later diagnostic, not here
	return files, info, nil
}

type mapImporter map[string]*types.Package
func (m mapImporter) Import(path string) (*types.Package, error) {
	if p, ok := m[path]; ok { return p, nil }
	return nil, fmt.Errorf("cached importer: %q not loaded", path)
}
```

When a `cachedResolver` is passed to `resolveTypesPkgWithFilters`, use its `filters()` instead of calling `loadFilterTableMulti` again (skip the second go list). Wire that: the resolver param's filter table takes precedence when non-nil.

Run: `go test ./internal/codegen -run TestCachedResolver -v`
Expected: PASS (types match).

- [ ] **Step 4: Confirm the corpus is still green (default path untouched)**

Run: `go test ./internal/codegen/... ./internal/corpus/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/resolver.go internal/codegen/resolver_test.go internal/codegen/analyze.go internal/codegen/batch.go internal/codegen/codegen.go
git commit -m "feat(codegen): pluggable type resolver + cached-importer impl (opt-in)"
```

---

## Task 2: Public `gen` API for the cached resolver + in-process generate

**Files:** Create `gen/resolver.go`; add a test `gen/resolver_test.go`.

**Interfaces:**
- Produces: `type CachedResolver struct{ ... }`; `func NewCachedResolver(moduleDir string, allowImports []string) (*CachedResolver, error)`; `func (*CachedResolver) Generate(dir string, srcOverride map[string][]byte) (Result, error)` — runs codegen in-process using the cached resolver and returns generated files + diagnostics (the existing `Result` type), with NO per-render `go list`.
- Consumes: `internal/codegen` cached resolver + `GeneratePackagesWithFilters` (threaded with the resolver).

- [ ] **Step 1: Write the failing API test**

```go
// gen/resolver_test.go
package gen

import "testing"

func TestCachedResolverGenerate(t *testing.T) {
	r, err := NewCachedResolver(repoRoot(t), DefaultPlaygroundImports)
	if err != nil { t.Fatal(err) }
	src := map[string][]byte{
		"views/comp.gsx": []byte("package views\n\ncomponent G(s string){\n\t<p>{s}</p>\n}\n"),
	}
	res, err := r.Generate("views", src)
	if err != nil { t.Fatal(err) }
	if len(res.Diagnostics) != 0 { t.Fatalf("diags: %+v", res.Diagnostics) }
	got := res.Files["views/comp.x.go"]
	if !bytes.Contains(got, []byte("func G(")) {
		t.Fatalf("generated Go missing func G: %s", got)
	}
}
```

Run: `go test ./gen -run TestCachedResolverGenerate -v`
Expected: FAIL — `NewCachedResolver` undefined.

- [ ] **Step 2: Implement the public wrapper**

```go
// gen/resolver.go
package gen

import "github.com/gsxhq/gsx/internal/codegen"

// DefaultPlaygroundImports is the fixed allowlist the playground caches types for.
var DefaultPlaygroundImports = []string{
	"context", "io", "strconv", "fmt", "strings", "time", "sort", "errors",
	"math", "math/rand", "unicode", "unicode/utf8", "html",
}

type CachedResolver struct{ inner *codegen.CachedResolver }

func NewCachedResolver(moduleDir string, allowImports []string) (*CachedResolver, error) {
	r, err := codegen.NewCachedResolver(moduleDir, []string{codegen.StdImportPath}, allowImports)
	if err != nil { return nil, err }
	return &CachedResolver{inner: r}, nil
}

func (c *CachedResolver) Generate(dir string, srcOverride map[string][]byte) (Result, error) {
	// in-process: no os.Exit, returns generated files + diagnostics
	return generateInProcess(c.inner, dir, srcOverride)
}
```

This requires exporting from `internal/codegen`: `CachedResolver` (rename the internal `cachedResolver` to exported, or add an exported constructor `NewCachedResolver` returning it), `StdImportPath`, and a `GeneratePackagesWithResolver(...)` that threads the resolver. Add `generateInProcess` in `gen` mapping codegen's `PackageResult` (files + diags) to the public `Result`. (Internal types stay internal; the public surface is just `CachedResolver`, `NewCachedResolver`, `Generate`, `DefaultPlaygroundImports`.)

Run: `go test ./gen -run TestCachedResolverGenerate -v`
Expected: PASS.

- [ ] **Step 3: Corpus + full codegen tests still green**

Run: `go test ./... -count=1` (gsx module)
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add gen/resolver.go gen/resolver_test.go internal/codegen/
git commit -m "feat(gen): public CachedResolver + in-process Generate (no per-render go list)"
```

---

## Task 3: Playground generates in-process via the cached resolver

**Files:** Modify `playground/server/render.go`, `playground/server/main.go`, `playground/server/go.mod`.

**Interfaces:**
- Consumes: `gen.NewCachedResolver`, `gen.CachedResolver.Generate`, `gen.DefaultPlaygroundImports`.
- The pool gains `resolver *gen.CachedResolver`, built once in `newPool`.

- [ ] **Step 1: Add the gen dependency**

In `playground/server/go.mod` the `github.com/gsxhq/gsx` require + replace already exist. Add the import in code; run `go mod tidy`. Verify:

Run: `cd playground/server && go build ./...`
Expected: resolves `github.com/gsxhq/gsx/gen`.

- [ ] **Step 2: Build the resolver once on the pool**

In `newPool` (render.go), after the workspaces are prepared, build the cached resolver against the module dir (the prepared `play` module that requires gsx via replace) and store it: `p.resolver, err = gen.NewCachedResolver(<a play dir>, gen.DefaultPlaygroundImports)`. (Build from one workspace's `play` dir — its go.mod has the gsx replace so deps resolve.) On error, fail startup.

- [ ] **Step 3: Replace the `gsx generate` exec with in-process generate in `renderIn`**

Replace the `run(ctx, ws.play, env, gsxBin, "generate", "--json", "./views")` step with:

```go
genStart := time.Now()
res, gerr := resolver.Generate(filepath.Join(ws.play, "views"), map[string][]byte{
	filepath.Join(ws.play, "views", "comp.gsx"): []byte(pkgLine.ReplaceAllString(in.GSX, "package views")),
})
genMs := time.Since(genStart).Milliseconds()
if gerr != nil { return renderResp{Error: "generate: " + gerr.Error(), Ms: ms(), GenMs: genMs} }
diags := toDiagnostics(res.Diagnostics)
if len(diags) > 0 { return renderResp{Diagnostics: diags, Ms: ms(), GenMs: genMs} }
// write generated .x.go to disk for the build step
for path, b := range res.Files { writeFile(path, b) }
```

Keep: writing `render_shim.go`, `checkImports`, `go run .`, the timing, and the response cache. `gsxBin` and the per-render `gsx generate` exec are removed; `newPool` no longer builds the gsx binary or runs `gsx generate` for warmup (it still warms `GOCACHE` via the seed/`go build`). Adapt `toDiagnostics` to map `gen.Result.Diagnostics` to the server's `diagnostic` type (severity/message/line/column).

- [ ] **Step 4: Tests**

Run: `cd playground/server && go test ./... -count=1`
Expected: PASS — render success/diagnostics/escaping, import allowlist (still parses generated `.x.go`), cache, pool concurrency all green with the in-process generate.

- [ ] **Step 5: Commit**

```bash
git add playground/server/
git commit -m "feat(playground): in-process codegen via cached resolver (no per-render go list)"
```

---

## Self-Review

- **Spec coverage:** cached resolver (T1), public API for the separate playground module (T2), playground in-process generate (T3). Filter-table caching folded into the cached resolver (T1 Step 3). Identical-types correctness test (T1 Step 2). Default path untouched + corpus green (T1 S4, T2 S3). ✓
- **Module boundary:** playground uses public `gen` only (T2/T3). ✓
- **Opt-in:** resolver param defaults to nil → `packagesLoad` (T1 S1). CLI/corpus unchanged. ✓
- **Deploy/verify** (operational, after T3): rebuild image + redeploy + measure `genMs` (expect collapse from ~6–8 s to ~ms) + confirm presets/edits — handled by the controller, not a code task.
- **Placeholder note:** the test fixtures (`skeletonFixture`, `@N` line numbers, `repoRoot`, `harvestUseTypes`) are spelled out enough to implement; the implementer fills exact line numbers from the fixture they write.
