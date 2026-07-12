# [renderers] Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `[renderers]` table in `gsx.toml` mapping a fully-qualified named type to a user func, applied automatically at every render boundary so `{item.SourceType}` works for wrapper types like `pgtype.Text` without per-site `.String` reaches or pipes.

**Architecture:** Renderers are resolved at codegen time into a `rendererTable` (canonical type key → typed entry) harvested in the SAME `packages.Load` as filter packages via the single `harvestFromTypes` seam. At emit, each render boundary calls a new `applyRenderer` helper right before its `emitRender`-family switch: on a registry hit the expression is wrapped in the renderer call, `(R, error)` renderers hoist through the existing `hoistTupleReturning` machinery, and the *result* type is classified through the unchanged rules — so per-context sanitization is untouched by construction. `resolved[n]` is never rewritten (LSP hover stays truthful).

**Tech Stack:** Go, `go/types`, `golang.org/x/tools/go/packages` (tooling only — the gsx runtime package is NOT touched), txtar corpus.

**Spec:** `docs/superpowers/specs/2026-07-11-renderers-registry-design.md` — read it first.

## Global Constraints

- **Runtime untouched:** no changes to the root `gsx` package, parser, or formatter. No syntax change → tree-sitter-gsx / vscode-gsx / CodeMirror unaffected.
- **One `packages.Load`:** renderer packages join the filter packages in the existing load at `internal/codegen/filters.go:255` (`harvestFilters`). Adding a second Load is a hard failure of this plan (`packages.Load` is expensive — CLAUDE.md).
- **`resolved[n]` stays truthful:** the harvest/probe phase is NOT modified; renderers apply only at emit. LSP hover keeps showing the raw hole type.
- **Renderer signature contract:** `func(T) R` or `func(T) (R, error)`; no ctx param, no variadic, no type params, no methods (v1).
- **Matching:** canonical key from go/types — `"pkgPath.TypeName"` for a named type, `"*pkgPath.TypeName"` for pointer-to-named; aliases unaliased first; generic instantiations and type params never match (v1).
- **Apply once, never chain:** a renderer whose result type is itself registered (including its own param type) is a generation-time error.
- **Registration always wins** over native renderability (e.g. a type that also implements `fmt.Stringer` uses its renderer).
- **Every corpus golden regen:** `go test ./internal/corpus -run TestCorpus -update` (also rewrites `coverage.golden` — a forgotten manifest bump fails the suite), then verify WITHOUT `-update`.
- **Worktree:** implement on a feature branch in a git worktree (`superpowers:using-git-worktrees`). Subagents: `cd` into the worktree and verify the branch before ANY commit.
- Inner loop `make check`; before merge `make ci` + `make lint`.

---

### Task 1: codegen types — `RendererAlias`, `rendererKey`, `rendererEntry`/`rendererTable`, harvest + validation

**Files:**
- Create: `internal/codegen/renderers.go`
- Create: `internal/codegen/renderers_test.go`
- Modify: `internal/codegen/bundle.go` (extend the single harvest seam)
- Modify: `internal/codegen/filters.go:239-` (`harvestFilters` — renderer pkgs join the Load list)

**Interfaces:**
- Consumes: `classifyFilter`-style validation patterns (`bundle.go:64-97`), `classify(t types.Type) category` (`analyze.go:2713`, `catUnsupported`), `filterAliases(pkgPaths []string) map[string]string` (`filters.go:176`).
- Produces (later tasks rely on these EXACT names):
  - `type RendererAlias struct { TypeKey, PkgPath, FuncName string }` — exported, config layer constructs it.
  - `func rendererKey(t types.Type) string` — canonical key or `""`.
  - `type rendererEntry struct { funcName, alias, pkgPath string; hasErr bool; result types.Type }`
  - `type rendererTable map[string]rendererEntry` — keyed by canonical type key.
  - `func harvestRenderers(byPath map[string]*types.Package, renderers []RendererAlias, aliases map[string]string) (rendererTable, error)`

- [ ] **Step 1: Write failing unit tests for `rendererKey`**

In `internal/codegen/renderers_test.go`, build types synthetically with `go/types` (no Load needed):

```go
package codegen

import (
	"go/token"
	"go/types"
	"testing"
)

func namedType(pkgPath, name string, underlying types.Type) *types.Named {
	pkg := types.NewPackage(pkgPath, "x")
	tn := types.NewTypeName(token.NoPos, pkg, name, nil)
	return types.NewNamed(tn, underlying, nil)
}

func TestRendererKey(t *testing.T) {
	text := namedType("github.com/jackc/pgx/v5/pgtype", "Text", types.NewStruct(nil, nil))
	cases := []struct {
		name string
		t    types.Type
		want string
	}{
		{"named", text, "github.com/jackc/pgx/v5/pgtype.Text"},
		{"pointer", types.NewPointer(text), "*github.com/jackc/pgx/v5/pgtype.Text"},
		{"alias", types.NewAlias(types.NewTypeName(token.NoPos, types.NewPackage("p", "p"), "A", nil), text), "github.com/jackc/pgx/v5/pgtype.Text"},
		{"basic", types.Typ[types.String], ""},
		{"unnamed struct", types.NewStruct(nil, nil), ""},
	}
	for _, c := range cases {
		if got := rendererKey(c.t); got != c.want {
			t.Errorf("%s: rendererKey = %q, want %q", c.name, got, c.want)
		}
	}
}
```

Also add a generic-instantiation case (build a `*types.Named` with type params via `types.NewTypeParam` + instantiate with `types.Instantiate`, expect `""`) and a type-param case (`types.NewTypeParam` itself, expect `""`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestRendererKey -v`
Expected: FAIL — `undefined: rendererKey`.

- [ ] **Step 3: Implement `renderers.go` (types + key + harvest)**

```go
package codegen

import (
	"fmt"
	"go/types"
)

// RendererAlias is one [renderers] registration: the canonical registered type
// key ("pkgPath.TypeName", optionally *-prefixed for a pointer type) and the
// resolved Go target func. Duplicate TypeKeys are last-wins, like FilterAlias.
type RendererAlias struct {
	TypeKey  string
	PkgPath  string
	FuncName string
}

// rendererEntry is one harvested renderer. result is the func's first result
// type from the renderer package's type-check universe — classify() and the
// emitRender conversions are purely structural/syntactic, so a cross-universe
// types.Type is safe there; it is never compared with types.Identical.
type rendererEntry struct {
	funcName string
	alias    string
	pkgPath  string
	hasErr   bool
	result   types.Type
}

// rendererTable maps a canonical type key (rendererKey) to its renderer.
type rendererTable map[string]rendererEntry

// rendererKey returns the canonical registry key for t: "pkgPath.TypeName" for
// a named type, "*pkgPath.TypeName" for a pointer to one, "" for anything that
// can never match a registration (basic/unnamed types, generic instantiations,
// type params, universe-scope names). Aliases are unwrapped on both levels.
func rendererKey(t types.Type) string {
	t = types.Unalias(t)
	prefix := ""
	if p, ok := t.(*types.Pointer); ok {
		prefix = "*"
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok || n.TypeArgs().Len() > 0 || n.Obj().Pkg() == nil {
		return ""
	}
	return prefix + n.Obj().Pkg().Path() + "." + n.Obj().Name()
}

// harvestRenderers validates and harvests every registered renderer from
// already-loaded packages. It is called from the same seam as
// harvestFromTypes, so every resolver path (go list, warm Module, WASM
// bundle) agrees. Duplicate TypeKeys are last-wins (registration order).
func harvestRenderers(byPath map[string]*types.Package, renderers []RendererAlias, aliases map[string]string) (rendererTable, error) {
	if len(renderers) == 0 {
		return rendererTable{}, nil
	}
	table := rendererTable{}
	for _, r := range renderers {
		pkg, ok := byPath[r.PkgPath]
		if !ok || pkg == nil {
			return nil, fmt.Errorf("codegen: renderer for %q: package %q was not loaded", r.TypeKey, r.PkgPath)
		}
		obj := pkg.Scope().Lookup(r.FuncName)
		if obj == nil {
			return nil, fmt.Errorf("codegen: renderer for %q: func %q not found in package %q", r.TypeKey, r.FuncName, r.PkgPath)
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return nil, fmt.Errorf("codegen: renderer for %q: %q in package %q is not a function", r.TypeKey, r.FuncName, r.PkgPath)
		}
		sig := fn.Type().(*types.Signature)
		if sig.Recv() != nil || sig.Variadic() || sig.TypeParams().Len() != 0 ||
			sig.Params().Len() != 1 || sig.Results().Len() < 1 || sig.Results().Len() > 2 ||
			(sig.Results().Len() == 2 && sig.Results().At(1).Type().String() != "error") {
			return nil, fmt.Errorf("codegen: renderer %q for %q does not match the renderer contract func(T) R or func(T) (R, error)", r.FuncName, r.TypeKey)
		}
		if pk := rendererKey(sig.Params().At(0).Type()); pk != r.TypeKey {
			return nil, fmt.Errorf("codegen: renderer %q takes %s; registered for %q", r.FuncName, sig.Params().At(0).Type(), r.TypeKey)
		}
		res := sig.Results().At(0).Type()
		if classify(res) == catUnsupported {
			return nil, fmt.Errorf("codegen: renderer %q for %q returns %s, which is not a renderable type", r.FuncName, r.TypeKey, res)
		}
		table[r.TypeKey] = rendererEntry{
			funcName: r.FuncName,
			alias:    aliases[r.PkgPath],
			pkgPath:  r.PkgPath,
			hasErr:   sig.Results().Len() == 2,
			result:   res,
		}
	}
	// Renderers apply exactly once: a result type that is itself registered
	// (including the renderer's own param type) would silently chain or loop,
	// so it is rejected here where all registrations are visible.
	for key, e := range table {
		rk := rendererKey(e.result)
		if rk == "" {
			continue
		}
		if _, chained := table[rk]; chained {
			if rk == key {
				return nil, fmt.Errorf("codegen: renderer %q for %q returns its own registered type; renderers apply once and never chain", e.funcName, key)
			}
			return nil, fmt.Errorf("codegen: renderer %q for %q returns %q, which has its own renderer; renderers apply once and never chain — return a natively renderable type", e.funcName, key, rk)
		}
	}
	return table, nil
}
```

- [ ] **Step 4: Run `TestRendererKey`**

Run: `go test ./internal/codegen -run TestRendererKey -v`
Expected: PASS.

- [ ] **Step 5: Write failing tests for `harvestRenderers`**

Build a synthetic `*types.Package` the same way (a scope with `types.NewFunc` entries created via `types.NewSignatureType`). Cover, each asserting the exact error substring or the harvested entry fields:

- valid `func(T) string` → entry with `hasErr: false`, `alias` from the aliases map, `result` is `string`.
- valid `func(T) (string, error)` → `hasErr: true`.
- param type key ≠ registered key → error contains `"registered for"`.
- variadic / two params / zero results / second result not error / generic func → error contains `"renderer contract"`.
- result `catUnsupported` (e.g. returns an unnamed struct) → error contains `"not a renderable type"`.
- chain: renderer A returns registered type B → error contains `"never chain"`.
- self-chain: `func(T) T` → error contains `"returns its own registered type"`.
- duplicate TypeKey → last registration wins.
- package not in `byPath` → error contains `"was not loaded"`.

Run: `go test ./internal/codegen -run TestHarvestRenderers -v` — expected FAIL (tests not yet passing against implementation gaps), then fix until PASS. (`harvestRenderers` above should already satisfy them; the tests pin the messages.)

- [ ] **Step 6: Wire renderer packages into the one Load**

In `harvestFilters` (`internal/codegen/filters.go:239`): the function receives filter pkg paths and builds `aliasPaths` for `packages.Load` (filters.go:255). Renderer pkg paths must join that list and the `filterAliases` allocation so a renderer package shares its alias with a same-path filter package (one-learning's `ds/filters` is both). Thread `renderers []RendererAlias` down from the callers (`ResolveFilters` filters.go:325, `loadFilterTableMulti` filters.go:200) — compiler-guided; every constructor changes in Task 3 anyway, so in THIS task only extend `harvestFilters`/`loadFilterTableMulti` signatures with `renderers []RendererAlias` and return the harvested `rendererTable` alongside the `filterTable`, passing `nil, rendererTable{}` where callers don't have renderers yet. Mirror the same extension in `loadFilterTableFromTypes` (`bundle.go:103`): append renderer pkg paths to `aliasPaths` before `filterAliases(aliasPaths)`, call `harvestRenderers` after `harvestFromTypes`.

- [ ] **Step 7: Full package test + commit**

Run: `go test ./internal/codegen` — expected PASS (existing tests unaffected; new params threaded with empty values).

```bash
git add internal/codegen/renderers.go internal/codegen/renderers_test.go internal/codegen/bundle.go internal/codegen/filters.go
git commit -m "feat(codegen): renderer registry types, canonical key, harvest + validation"
```

---

### Task 2: gen config layer — `[renderers]` TOML, `WithRenderer`, computeKey

**Files:**
- Modify: `gen/configfile.go` (`tomlConfig` @29, `loadConfig` @139, `mergeConfig` @242)
- Modify: `gen/options.go` (`splitPkgFunc` @115 as the model; new `splitPkgType`, `WithRenderer`)
- Modify: `gen/cachekey.go` (`computeKey` @189, hash writes @231-239)
- Test: `gen/configfile_test.go`, `gen/cachekey_test.go` (extend existing test files; create if the package splits tests differently — follow the existing layout)

**Interfaces:**
- Consumes: `codegen.RendererAlias` (Task 1), `splitPkgFunc` (`gen/options.go:115`).
- Produces:
  - `tomlConfig.Renderers map[string]string` toml:`"renderers"`.
  - `func splitPkgType(s string) (key string, err error)` — validates `[*]pkgPath.TypeName`, returns the canonical key.
  - `func WithRenderer(typeKey string, fn any) Option` — reflection-resolved like `WithFilter` (`options.go:70`, `resolveFilterFunc`).
  - `computeKey` gains `renderers []codegen.RendererAlias`, hashed as a `renderers=` pin.

- [ ] **Step 1: Write failing config-decode test**

In the gen package's config test file, following its existing table style:

```go
func TestLoadConfigRenderers(t *testing.T) {
	dir := t.TempDir()
	toml := `
[renderers]
"github.com/jackc/pgx/v5/pgtype.Text" = "example.com/app/filters.PgText"
"*github.com/jackc/pgx/v5/pgtype.Int4" = "example.com/app/filters.PgInt4Ptr"
`
	os.WriteFile(filepath.Join(dir, "gsx.toml"), []byte(toml), 0o644)
	cfg, err := loadConfig(dir) // match the real loadConfig signature at gen/configfile.go:139
	if err != nil {
		t.Fatal(err)
	}
	want := []codegen.RendererAlias{
		{TypeKey: "*github.com/jackc/pgx/v5/pgtype.Int4", PkgPath: "example.com/app/filters", FuncName: "PgInt4Ptr"},
		{TypeKey: "github.com/jackc/pgx/v5/pgtype.Text", PkgPath: "example.com/app/filters", FuncName: "PgText"},
	}
	if !reflect.DeepEqual(cfg.renderers, want) {
		t.Errorf("renderers = %#v, want %#v", cfg.renderers, want)
	}
}
```

(Registrations are read in sorted-key order for determinism, mirroring the named-filters loop @161-172.) Add failing cases: value that isn't `pkgPath.Func` (reuses `splitPkgFunc` errors), key with no dot, key with non-identifier type name, key that is only `*` — each `loadConfig` returns an error naming the bad key.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./gen -run TestLoadConfigRenderers -v`
Expected: FAIL — `cfg.renderers` undefined / TOML key rejected by strict decoding.

- [ ] **Step 3: Implement config decode + `splitPkgType` + `WithRenderer`**

`tomlConfig` gains:

```go
Renderers map[string]string `toml:"renderers"`
```

`splitPkgType` in `gen/options.go`, next to `splitPkgFunc` (reuse its identifier validation — read `splitPkgFunc` first and share the ident-check helper rather than duplicating it):

```go
// splitPkgType validates a [renderers] key: "pkgPath.TypeName" with an
// optional leading * registering the pointer type. The returned key is the
// canonical form codegen.rendererKey produces, so config keys and resolved
// hole types meet on the same string.
func splitPkgType(s string) (string, error) {
	t := strings.TrimPrefix(s, "*")
	i := strings.LastIndex(t, ".")
	if i <= 0 || i == len(t)-1 {
		return "", fmt.Errorf("gsx: renderer type %q must be \"pkgPath.TypeName\" (optionally *-prefixed)", s)
	}
	if !isGoIdent(t[i+1:]) { // share splitPkgFunc's identifier validation
		return "", fmt.Errorf("gsx: renderer type %q: %q is not a valid type name", s, t[i+1:])
	}
	return s, nil
}
```

`loadConfig` (after the named-filters loop @161-172, same shape):

```go
for _, k := range slices.Sorted(maps.Keys(tc.Renderers)) {
	key, err := splitPkgType(k)
	if err != nil {
		return nil, err
	}
	pkgPath, funcName, err := splitPkgFunc(tc.Renderers[k]) // match splitPkgFunc's real return shape
	if err != nil {
		return nil, fmt.Errorf("gsx: renderer for %q: %w", k, err)
	}
	cfg.renderers = append(cfg.renderers, codegen.RendererAlias{TypeKey: key, PkgPath: pkgPath, FuncName: funcName})
}
```

`WithRenderer` next to `WithFilter` (`options.go:70`), reusing `resolveFilterFunc`:

```go
// WithRenderer registers a renderer for typeKey ("pkgPath.TypeName",
// optionally *-prefixed): at every render boundary a value of that exact type
// is passed through fn (func(T) R or func(T) (R, error)) before rendering.
// Option-layer registrations are appended after file-layer ones (last-wins per
// TypeKey at harvest).
func WithRenderer(typeKey string, fn any) Option {
	return func(cfg *config) error {
		key, err := splitPkgType(typeKey)
		if err != nil {
			return err
		}
		pkgPath, funcName, err := resolveFilterFunc(fn) // match its real signature
		if err != nil {
			return fmt.Errorf("gsx: WithRenderer %q: %w", typeKey, err)
		}
		cfg.renderers = append(cfg.renderers, codegen.RendererAlias{TypeKey: key, PkgPath: pkgPath, FuncName: funcName})
		return nil
	}
}
```

(Adapt the `Option`/`config` shapes to the real ones in `options.go` — the pattern is `WithFilter`'s.) In `mergeConfig` (`configfile.go:242`): concatenate `base.renderers` then `opts.renderers` — options-last so last-wins-per-TypeKey at harvest matches the filters convention.

- [ ] **Step 4: Run config tests**

Run: `go test ./gen -run TestLoadConfigRenderers -v`
Expected: PASS.

- [ ] **Step 5: Fold into computeKey (failing test first)**

Test: two configs differing only in a renderer registration produce different keys; identical registrations (regardless of file/option split) produce identical keys.

```go
func TestComputeKeyRenderers(t *testing.T) {
	// call computeKey twice with identical args except renderers; assert keys differ.
	// Then swap registration order of two distinct TypeKeys; assert keys EQUAL
	// (the pin is sorted by TypeKey — the table is a per-key map, order is not meaning).
}
```

Implementation in `computeKey` (@189, new param `renderers []codegen.RendererAlias`; hash writes near @231-239):

```go
// renderers= pin: last-wins per TypeKey resolved first, then sorted by
// TypeKey — unlike aliases= (order IS meaning there), the renderer table is
// a per-key map, so two configs with the same final table hash identically.
final := map[string]codegen.RendererAlias{}
for _, r := range renderers {
	final[r.TypeKey] = r
}
fmt.Fprintf(h, "renderers=")
for _, k := range slices.Sorted(maps.Keys(final)) {
	fmt.Fprintf(h, "%s=%s.%s;", k, final[k].PkgPath, final[k].FuncName)
}
```

Update every `computeKey` caller (compiler-guided) to pass the merged renderers.

- [ ] **Step 6: Run + commit**

Run: `go test ./gen`
Expected: PASS.

```bash
git add gen/configfile.go gen/options.go gen/cachekey.go gen/*_test.go
git commit -m "feat(gen): [renderers] config table, WithRenderer option, computeKey pin"
```

---

### Task 3: `funcTables` threading + corpus loader `[renderers]` — zero behavior change

This is the mechanical enabler: the emit layer currently threads `table filterTable` through ~20 signatures (`emit.go` — `generateFile` @39, `genComponent` @528, `genInterp` @1776, `emitCSSInterp` @2301, `emitJSInterp` @2381, `hoistValueCF` @1982, etc.). Wrap both tables in one struct so no signature ever grows again.

**Files:**
- Modify: `internal/codegen/filters.go` (introduce `funcTables`; `aliasForPath` covers both maps)
- Modify: `internal/codegen/emit.go`, `internal/codegen/analyze.go`, `internal/codegen/module.go` (@380 `cachedFilterTable`), `internal/codegen/bundle.go`, `internal/codegen/generate_dirs.go` (Options gains `Renderers []RendererAlias`) — compiler-guided
- Modify: `internal/corpus/loader.go` (@32 `caseToml`, parse block @111-129), `internal/corpus/batch.go` (pass renderers through Options)
- Modify: `gen/` call sites passing filter config into codegen (thread `cfg.renderers`)

**Interfaces:**
- Produces:

```go
// funcTables carries every configured func table the emit layer consults:
// pipe filters (by template name) and renderers (by canonical type key).
type funcTables struct {
	filters   filterTable
	renderers rendererTable
}
```

- `funcTables.aliasForPath(path string) (string, bool)` — checks filters then renderers (writeImports' `filterAlias` map must include renderer packages).
- `codegen.Options.Renderers []RendererAlias`.
- corpus `caseToml.Renderers map[string]string` with `./`-relative resolution on BOTH the key's package part and the value's package part (mirroring `filterPackages` @125-126 via `caseImportRoot`).

- [ ] **Step 1: Introduce `funcTables` and swap the threaded type**

Change every `table filterTable` parameter in `emit.go`/`analyze.go` to `table funcTables`; inside `lowerPipe` (@64), `finalStageErr` (@100), `probeExpr` and other lookup sites, use `table.filters.lookup(...)`. Constructors (`loadFilterTableMulti`, `loadFilterTableFromTypes`, `ResolveFilters` path, `Module.cachedFilterTable`, `NewCachedResolverFromTypes`) return/hold `funcTables`, populating `renderers` from Task 1's harvest (empty when no registrations). Let the compiler enumerate the sites — do NOT change any behavior. Where `writeImports` (@438) builds its `filterAlias`/`usedFilterPkg` inputs from the table, build them from BOTH maps via the new `aliasForPath`.

- [ ] **Step 2: Prove zero behavior change**

Run: `go test ./internal/codegen ./internal/corpus ./gen`
Expected: PASS with NO golden changes (`git status` shows no modified `.golden` files).

- [ ] **Step 3: Corpus loader `[renderers]` (failing corpus case first)**

Add to `caseToml` (@32): `Renderers map[string]string` toml:`"renderers"`. In the parse block (@111-129), resolve `./`-relative package parts against `caseImportRoot(c)` on both sides:

```go
for k, v := range ct.Renderers {
	c.renderers = append(c.renderers, resolveRendererAlias(k, v, caseImportRoot(c)))
}
```

where `resolveRendererAlias` splits `k` (respecting a `*` prefix) and `v` at their last `.`, and rewrites a leading `./` on the package part to `caseImportRoot(c) + "/..."` exactly as the `filterPackages` block does. Sort `c.renderers` by TypeKey for determinism. Thread into `codegen.Options.Renderers` in `batch.go` next to `ClassMerger`.

- [ ] **Step 4: Run corpus + commit**

Run: `go test ./internal/corpus -run TestCorpus`
Expected: PASS, no golden drift.

```bash
git add internal/codegen internal/corpus gen
git commit -m "refactor(codegen): thread funcTables{filters,renderers}; corpus [renderers] support"
```

---

### Task 4: `applyRenderer` + text context (core semantics)

**Files:**
- Modify: `internal/codegen/renderers.go` (add `applyRenderer`)
- Modify: `internal/codegen/emit.go` — `genInterp` (@1841-1856), `emitEmbeddedInterp` (@1911-1925)
- Create: corpus cases under `internal/corpus/testdata/cases/renderers/`

**Interfaces:**
- Consumes: `hoistTupleReturning` (`emit.go:1941`), `rendererKey`, `funcTables`.
- Produces:

```go
// applyRenderer wraps expr in its registered renderer call when t's canonical
// key is registered, marking the renderer package as imported. An error
// renderer hoists through hoistTupleReturning with the caller's error-return
// statement (the same per-context shapes pipe filters use: "return _gsxerr"
// in a render closure, "return nil, _gsxerr" in an (Attrs, error) thunk).
// Returns the (possibly hoisted) expr and the type the boundary classifies;
// a registry miss returns the inputs unchanged. Renderers apply exactly once
// (harvest rejects chains), so this never recurses.
func applyRenderer(b *bytes.Buffer, expr string, t types.Type, table funcTables, imports map[string]bool, interpTemp *int, errReturn string) (string, types.Type) {
	e, ok := table.renderers[rendererKey(t)]
	if !ok {
		return expr, t
	}
	imports[e.pkgPath] = true
	call := e.alias + "." + e.funcName + "((" + expr + "))"
	if e.hasErr {
		return hoistTupleReturning(b, call, interpTemp, errReturn), e.result
	}
	return call, e.result
}
```

- [ ] **Step 1: Write the basic corpus case (failing)**

`internal/corpus/testdata/cases/renderers/text_basic.txtar`, modeled on `filterimport/user_plain_import_and_filter.txtar`:

```
# A text hole whose type has a registered renderer: {p.Val} of type pg.Text
# emits _gsxf0.PgText((p.Val)) and renders the renderer's string result —
# no .String reach, no pipe. resolved[n] (LSP hover) stays pg.Text.
-- gsx.toml --
[renderers]
"./pg.Text" = "./rend.PgText"
-- pg/pg.go --
package pg

type Text struct {
	String string
	Valid  bool
}
-- rend/rend.go --
package rend

import "corpustest/cases/renderers_text_basic/pg"

func PgText(t pg.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
-- input.gsx --
package views

import "corpustest/cases/renderers_text_basic/pg"

component Tag(val pg.Text) {
	<div>{ val }</div>
}
-- invoke --
Tag(TagProps{Val: pg.Text{String: "<b>hi</b>", Valid: true}})
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
```

(The `invoke` file may need a `pg` import — follow how existing cases with case-local packages invoke; check a `filterimport` sibling. The rendered value deliberately contains `<b>` to pin that the renderer's output is HTML-escaped like any string.)

- [ ] **Step 2: Regenerate goldens, verify failure first**

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | head -30`
Expected: FAIL — `unrenderable` diagnostic for `{ val }` (renderer not yet applied at emit).

- [ ] **Step 3: Wire `applyRenderer` into the text boundary**

In `genInterp`, replace the boundary tail (@1846-1856) so BOTH paths (tuple-hoisted and plain) pass through the renderer before `emitRender`:

```go
if _, isTuple := t.(*types.Tuple); isTuple {
	elemT, ok := tupleUnwrapType(t)
	if !ok {
		bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "interpolation %q returns %s; only (T, error) is supported", expr, t)
		return false
	}
	expr = hoistTuple(b, expr, interpTemp)
	t = elemT
}
expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
return emitRender(b, expr, t, rt, n, bag)
```

Mirror the same change in `emitEmbeddedInterp` (@1916-1925).

- [ ] **Step 4: Regenerate + verify goldens**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Inspect `text_basic.txtar`'s regenerated `generated.x.go.golden`: the hole emits `_gsxgw.Text(string(_gsxf0.PgText((val))))` (alias exact name may differ — verify it is a `_gsxf<i>` from the shared allocation) and `render.golden` shows the escaped output `&lt;b&gt;hi&lt;/b&gt;`. Check `coverage.golden` picked up the new case.

- [ ] **Step 5: Add the remaining core-semantics cases**

Same structure, one txtar each (regen + verify after each; every case gets a header comment saying what it pins):

- `text_error.txtar` — renderer `func(pg.Text) (string, error)`; invoke once with a value that errors. Golden pins the `_gsxvN, _gsxerr := …; if _gsxerr != nil { return _gsxerr }` hoist and the corpus render-error capture (see `pipeerr/*.txtar` for how a render error is asserted).
- `wins_over_stringer.txtar` — the case-local type ALSO has a `String() string` method returning a sentinel like `"STRINGER"`; render.golden proves the renderer output (not the method) is used.
- `pipe_result.txtar` — `{ x |> wrap }` where filter `wrap` returns the registered type; golden pins renderer applied to the pipe result: `PgText((Wrap((x))))` shape.
- `component_arg_negative.txtar` — a component param of the registered type passed through to a child component tag (`<Child val={val}/>`); the child renders `{val.String}`. Pins that renderers do NOT fire on component arguments (the generated caller passes the raw value).
- `pointer.txtar` — register `"*./pg.Text"` and render a `*pg.Text` hole; also a NON-registered `pg.Text` value hole in the same file using `.String` explicitly, pinning that pointer and value registrations are distinct.

- [ ] **Step 6: Verify without -update, run codegen tests, commit**

Run: `go test ./internal/corpus -run TestCorpus && go test ./internal/codegen`
Expected: PASS.

```bash
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): applyRenderer at the text render boundary + core corpus semantics"
```

---

### Task 5: attribute + URL-attribute contexts

**Files:**
- Modify: `internal/codegen/emit.go` — the attr-value boundary (`emitAttrValue` switch @2273 and its callers where `resolved[n]` + lowered expr meet; cond-attr thunk positions use `errReturn: "return nil, _gsxerr"` — find them by the existing `thunkPipeWrap` call sites)
- Create: corpus cases `internal/corpus/testdata/cases/renderers/attr_basic.txtar`, `attr_url.txtar`, `attr_error.txtar`

**Interfaces:**
- Consumes: `applyRenderer` (Task 4). No new names produced.

- [ ] **Step 1: Write `attr_basic.txtar` (failing)** — same packages as `text_basic`, template `<div data-x={ val } title={ val }>ok</div>`; expected: renderer applies, attr-escaped output.
- [ ] **Step 2: Run corpus, confirm the `unrenderable`-family failure for attr position.**
- [ ] **Step 3: Wire `applyRenderer` at the attr boundary.** Locate every place an attr-value hole's `resolved[n]` feeds its classify switch (@2273 region and the attr callers around it — same pattern as `genInterp`: unwrap `(T, error)` tuple first if present, then `applyRenderer`, then the switch). In positions inside an `(Attrs, error)` cond-attr thunk, pass `"return nil, _gsxerr"` — mirror the `thunkPipeWrap` usage at those sites.
- [ ] **Step 4: `attr_url.txtar`** — `<a href={ val }>x</a>` with the renderer returning `"javascript:alert(1)"` for one invoke and a normal URL for another. render.golden MUST show the sanitizer neutralizing the dangerous value (`about:invalid` per the existing URL-sink behavior — compare with existing URL-attr cases under the corpus) — this is the security-order pin: renderer FIRST, whole-value URL sanitization AFTER.
- [ ] **Step 5: `attr_error.txtar`** — error-returning renderer in attr position (and one inside a cond-attr `{ if cond { attr } }` branch if the boundary there is a thunk) pinning both error-return shapes.
- [ ] **Step 6: Regen, verify, commit**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && go test ./internal/codegen`

```bash
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): renderers at attribute and URL-attribute boundaries"
```

---

### Task 6: CSS (`<style>`) + JS (`<script>`) contexts

**Files:**
- Modify: `internal/codegen/emit.go` — `emitCSSInterp` (@2301, switch @2339), `emitJSInterp` (@2381, switch @2439)
- Create: corpus cases `renderers/style_hole.txtar`, `script_hole.txtar`

- [ ] **Step 1: Write both corpus cases (failing).** `style_hole`: `<style>` child with `{ val }` in a declaration value; `script_hole`: `<script>` child with `{ val }`. Expected diagnostics before the fix: `unrenderable-css` / `unrenderable-js`.
- [ ] **Step 2: Wire `applyRenderer`** at the top of each function's boundary (after that function's existing tuple handling, matching how Task 4 ordered it; check each function's existing `(T, error)` treatment and error-return shape — both are inside the render closure, so `"return _gsxerr"`).
- [ ] **Step 3: Regen + verify.** The script case's render.golden pins JS-context escaping of the renderer output (use a value like `</script>` in the invoke); the style case pins CSS-value filtering.
- [ ] **Step 4: Commit**

```bash
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): renderers at style/script boundaries"
```

---

### Task 7: f-literal and class/style part boundaries

**Files:**
- Modify: `internal/codegen/emit.go` — the interpolated-literal and class/style helpers that classify hole types: @2861, @2893, @2910, @3060, @3372, @3453, @3462, @3483, @3492, and `hoistValueCF` arm handling (@1982-)
- Create: corpus cases `renderers/fliteral_text.txtar`, `fliteral_attr.txtar`, `class_part.txtar`, `valuecf_arm.txtar`

- [ ] **Step 1: Enumerate the remaining boundaries.** Run `grep -n "classify(" internal/codegen/emit.go` and diff against the sites already covered (Tasks 4-6). Every remaining site that classifies a HOLE's `resolved[n]` type (not a param/prop structural check) is a render boundary this task covers. List them in the task commit message.
- [ ] **Step 2: Corpus cases first (failing):**
  - `fliteral_text.txtar` — body ``{f`v=@{val}`}`` (adjust to the real f-literal syntax from existing corpus cases under the interp-literal case dirs — copy a passing case and swap the hole's type).
  - `fliteral_attr.txtar` — attr ``title={f`v=@{val}`}``.
  - `class_part.txtar` — `class={ val }` (class-part boundary; the merge path must see the renderer's string).
  - `valuecf_arm.txtar` — `class={ if cond { val } else { other } }` with `val` of the registered type (the `hoistValueCF` arm boundary).
- [ ] **Step 3: Wire `applyRenderer` at each enumerated site**, matching each site's tuple handling and error-return shape as in Tasks 4-6.
- [ ] **Step 4: Regen + verify + full check**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && make check`

- [ ] **Step 5: Commit**

```bash
git add internal/codegen internal/corpus/testdata
git commit -m "feat(codegen): renderers at f-literal and class/style part boundaries"
```

---

### Task 8: docs, roadmap, full CI

**Files:**
- Modify: `docs/guide/config.md` — new `[renderers]` section
- Modify: `docs/ROADMAP.md` — record shipped feature + deferred follow-ups
- Modify: `README.md` ONLY if it enumerates gsx.toml tables (check first)

- [ ] **Step 1: Write the `[renderers]` guide section.** Place it next to the `[filters]` section, structured: motivation (wrapper types like `pgtype.Text`), the table syntax, the renderer contract (`func(T) R` / `func(T) (R, error)`), the five rules in one list (render-boundary only; registration always wins; apply once/never chain; go/types identity incl. `*T` distinctness; type params + runtime `any` values excluded), and the worked pgtype example:

````markdown
## [renderers]

Third-party wrapper types — `pgtype.Text`, `sql.NullString` — are not
renderable and cannot be given a `String()` method you don't own. A renderer
teaches gsx how to display such a type everywhere, once:

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Text" = "example.com/app/filters.PgText"
```

```go
func PgText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
```

With that registration `{item.SourceType}` renders through `PgText` in every
context (text, attributes, URL attributes, style, script, interpolated
literals) and the result is escaped/sanitized for that context exactly like a
pipe filter's output. Renderers may return `(R, error)`; the error propagates
like a failing pipe stage.
````

(No literal `{{ }}` in the prose — VitePress; not needed here anyway.)
- [ ] **Step 2: ROADMAP** — add the shipped entry; record the deferred follow-ups from the spec (LSP hover showing the applied renderer; type-param holes; pgx preset).
- [ ] **Step 3: Full gate**

Run: `make ci && make lint`
Expected: PASS (uncached; includes examples drift + gofmt + gsx fmt).

- [ ] **Step 4: Commit**

```bash
git add docs
git commit -m "docs: [renderers] guide section + roadmap"
```

- [ ] **Step 5: Manual end-to-end validation (report, don't commit):** in `~/work/one-learning-gsx`, point `go.work`/`go.mod` at the branch, add a `[renderers]` entry for `pgtype.Text` → `ds/filters.PgText`, regenerate one page's `.x.go`, and confirm a `.String` reach can be dropped. (Note: that repo's `.x.go` is stale vs gsx head — PR #79 `gw.Spread` signature — so a full regen is needed regardless.) Report findings back for the merge decision.

---

## Final verification (before merge)

Per repo convention: one independent adversarial review of the whole branch — the reviewer builds throwaway probe programs (a real module with a registered renderer; try to smuggle `javascript:` URLs through a renderer, register chains via config layering, race `-count=1 -race` the corpus) — not just a diff read. Then `superpowers:finishing-a-development-branch`.
