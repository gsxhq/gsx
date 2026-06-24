# gsx type-resolution cache (in-process cached importer) — design

**Status:** design · **Date:** 2026-06-24

## Goal

Cut the playground's per-render type-resolution cost from a per-request `go list`
(`packages.Load`, ~6–8 s on Cloud Run under gVisor) to ~nothing, by loading the
**fixed** dependency types **once** and per-render type-checking only the changing
component package against a cached `go/types` importer. Target: edit latency
~7 s → ~2 s (the new floor is `go build`/run, ~1.5 s).

The cached resolver is built as a **general codegen capability** (not
playground-specific); the **playground is consumer #1**. The stock one-shot
`gsx generate` CLI keeps its `packages.Load` path unchanged (a fresh process
can't reuse an in-memory cache). A future **local-dev** consumer can reuse the
same capability — see "Future consumers".

## Spike evidence (proven)

A standalone spike (cached importer + `go/types` check vs `packages.Load`):

| | time |
|---|---|
| one-time cached importer build (81 pkgs closure) | 254 ms local |
| cached per-render type-check | **~30 µs** |
| `packages.Load` (current path) | 702 ms local; ~6–8 s Cloud Run cold |

Types resolved identically (`name→string`, `count→int`, `count>0→bool`,
`strconv.Itoa(count)→string`, `strings.ToUpper(name)→string`). The cached
importer resolves by import **path** (alias-independent), so the skeleton's
`_gsxrt`/`_gsxctx`/`_gsxstd` aliases are irrelevant.

## Architecture

Today (`internal/codegen/analyze.go:51` `resolveTypesPkgWithFilters`):
`buildSkeleton` → overlay → `packages.Load(cfg, ".")` → `harvest(pkg.TypesInfo)` →
`map[gsxast.Node]types.Type`. Emit (`emit.go`) consumes only that map, so HOW
types are resolved is transparent downstream (confirmed by the architecture map).

### The seam: a pluggable type resolver

Introduce an optional injected resolver used in place of `packages.Load`, while
keeping `buildSkeleton` and `harvest` untouched:

- **Default (CLI):** `packages.Load` — unchanged. Corpus unaffected.
- **Cached (playground):** type-check the skeleton overlay with
  `go/types.Config{Importer: cached}` directly (no `go list`), then `harvest` the
  resulting `*types.Info` exactly as today.

Concretely, factor the type-check step behind a small interface, e.g.:

```go
// typeResolver turns a skeleton overlay (path -> Go source) into the per-file
// type info harvest needs. Two impls: packagesLoad (default) and cached.
type typeResolver interface {
	check(dir string, overlay map[string][]byte, fset *token.FileSet) (
		files []*goast.File, info *types.Info, err error)
}
```

`resolveTypesPkgWithFilters` calls `resolver.check(...)` then `harvest`. The
default resolver wraps the current `packages.Load`. The cached resolver parses
the overlay files and runs `types.Config{Importer: cachedImporter}.Check`.

### The cached importer + filter table (built once)

Two fixed things are loaded once per playground instance and reused:

1. **Cached importer** — `packages.Load` over the fixed dep set (gsx runtime,
   `.../std`, the import allowlist) with `NeedTypes|NeedImports|NeedDeps`, then
   `packages.Visit` to capture every `*types.Package` in the closure into
   `map[path]*types.Package`; a `types.Importer` returns from that map.
2. **Cached filter table** — `harvestFilters` (`filters.go`) also runs a
   `packages.Load` (`go list`) over `std`; the std filters are fixed, so build
   the `filterTable` once and reuse it (skip the per-render filter load).

### In-process codegen from the playground

The playground currently **execs** `gsx generate`; a fresh process can't reuse an
in-memory cache. Change it to call codegen **in-process** (import the gsx codegen
package), constructing the cached importer + filter table once at startup and
passing them per render. The existing in-process entry
`GeneratePackagesWithFilters(moduleDir, dirs, filterPkgs, …, srcOverride)`
(used by the LSP) is the basis; add an optional cached-resolver parameter (or a
sibling entry) that threads the cached resolver down to
`resolveTypesPkgWithFilters`.

Per render in the playground:
1. component source → in-process codegen with the cached resolver → generated
   `.x.go` (in memory or written to the workspace).
2. import allowlist check (unchanged).
3. write `.x.go` + `go build`/run (unchanged) → HTML.

The response cache + preset seeding + the import allowlist + offline/cgo-off
build all stay.

## Scope & safety

- **Opt-in.** Only the playground uses the cached resolver. `gsx generate` and the
  corpus keep `packages.Load`. No behavior change to the shipped compiler.
- **Correctness.** The cached resolver must produce the *same*
  `map[gsxast.Node]types.Type` as `packages.Load` for the supported inputs —
  verified by a test that runs both resolvers over the corpus single-package
  cases and asserts identical resolved types.
- **Allowlist alignment.** The cached importer is built over exactly the import
  allowlist; an import outside it is already rejected by `checkImports`, so the
  importer never needs packages it lacks. (If `go/types` ever needs an unlisted
  transitive import, the closure capture via `packages.Visit` already included
  it.)
- **Type identity.** One cached importer instance supplies consistent
  `*types.Package` pointers across checks; `harvest`/`classify` use structural
  type checks, so cached `types.Type` values classify identically.

## Risks / obstacles

- Threading the resolver through `GeneratePackage(s)WithFilters` →
  `resolveTypesPkgWithFilters` touches several signatures; keep it an optional
  param (nil = default) to avoid churn at every call site.
- The playground importing `internal/codegen` directly — `internal/` is
  importable within the gsx module, and the playground server lives in the gsx
  repo (`playground/server`), so this is allowed. It drops the server's
  "stdlib-only" property, which only ever applied to the *shipped* runtime, not
  this infra service.
- Cold-instance one-time importer build is a few seconds on Cloud Run; runs at
  startup (before/with the preset seed) so only the instance's first moments pay
  it, not each render.

## Future consumers (out of this slice)

- **Local dev via the Vite plugin.** `@gsxhq/vite-plugin-gsx` currently `spawn`s a
  fresh `gsx generate` per `.gsx` save, so the edited file always pays full
  `packages.Load` and no in-memory cache survives. To make local dev fast, gsx
  would expose a **persistent generate mode** (a stdio daemon holding this cached
  resolver), and the plugin would spawn it **once** and send per-change requests
  instead of spawning per change. That reuses this capability verbatim; it adds
  only **dep-change invalidation** (rebuild the importer when `go.mod`/`go.sum`
  changes; stay warm across pure `.gsx` edits) since a real project's deps aren't
  fixed like the playground's. This is a separate slice (gsx daemon + plugin
  rework; revises the plugin's approved "spawn-per-change" design).

## Out of scope

- Speeding `go build`/run (the new ~1.5 s floor) — would need a persistent
  compile/exec strategy; separate effort.
- The one-shot `gsx generate` CLI — a fresh process can't reuse the cache; keeps
  `packages.Load`.

## Testing

- New codegen test: cached resolver vs `packages.Load` produce identical resolved
  types for a set of single-package corpus inputs.
- Playground: existing render/cache/allowlist tests stay green with the in-process
  cached path; add a test asserting the cached path renders the presets correctly.
- Corpus: unchanged (default path) — must stay green.
