# gsx Incremental Codegen Cache (Tier 2) — Design

**Date:** 2026-06-21
**Status:** Approved (brainstorm), pending implementation plan
**Depends on:** Tier 0 (`codegen.GeneratePackagesWithFilters` — single load over a *subset* of package dirs, with custom filter packages).
**Background / rationale:** [2026-06-21-codegen-perf-and-caching.md](2026-06-21-codegen-perf-and-caching.md) (measured bottleneck, cost model, prior art, why Tier 2 is the priority for watch-mode usage).

---

## 1. Goal & motivation

gsx's real workflow is **repeated, watch-driven regeneration** — a file watcher (`wgo`, or a Vite plugin that still can't prevent cold runs) re-runs `gsx generate` on every `.gsx` save, then `go build`. The generator's cost is dominated by `go/packages` type resolution, *not* compilation.

Tier 2 makes the steady-state loop cheap: **after editing one `.gsx`, regenerate only the affected package(s) and skip the rest**, with a stateless, content-addressed cache so the win survives across separate `gsx generate` processes (which is exactly how `wgo`-style watchers run — process-per-save, no in-memory daemon state).

**Success criteria:**
- A no-op `gsx generate` (nothing changed) does no codegen and writes no files.
- Editing one `.gsx` regenerates only that package and its in-module dependents.
- Output is byte-identical to an uncached run.
- Robust across branch switches / `git clean` / file deletion (content-addressed store restores prior outputs without regenerating).
- Pure source — no cache state written into `.x.go` or the repo; cache lives in the system cache dir.

**Non-goals (explicit):** a long-lived daemon / `gsx watch` in-memory cache (that is Tier 3, future); automatic cache GC/trimming (v1 ships a manual `gsx clean --cache`); multi-module / nested-module projects beyond the standard single-module layout (fall back to MISS/regenerate when unsure).

---

## 2. Architecture

A new **cache layer in `gen/`**, wrapping codegen. It does NOT modify `internal/codegen` output (source stays pure) and builds on Tier 0's `GeneratePackagesWithFilters`, which generates a *subset* of package dirs in a single `go/packages` load. The cache's entire value is "compute the MISS set, then generate only those — in one load."

```
gen/ (cache layer)
  ├─ discover gsx package dirs            (existing discoverDirs)
  ├─ build in-module import graph         (go list -json ./... + .gsx Go-block imports)
  ├─ compute per-package key              (pure hashing; no compile)
  ├─ partition HIT / MISS against store
  ├─ RESTORE hits to disk                 (content-addressed store → .x.go)
  ├─ GENERATE misses                      (codegen.GeneratePackagesWithFilters, ONE load)
  └─ STORE generated outputs              (content-addressed)
internal/codegen
  └─ GeneratePackagesWithFilters(moduleRoot, missDirs, filterPkgs)   (Tier 0; unchanged by cache)
```

Units:
- **graph**: resolve each gsx package's in-module dependency closure.
- **key**: deterministic per-package input hash.
- **store**: content-addressed get/put of a package's generated output, in the system cache dir.
- **runner**: the orchestration above (partition → restore → generate → store).

Each is independently testable.

---

## 3. The cache key

A *package* is the unit (its `.x.go` files are generated together). The key captures everything that can change a package's generated output:

```
key(P) = sha256(
    "gsxcache-v1\0"                       // format/version tag
  + gsxCodegenVersion                     // bump on ANY codegen-logic change
  + goVersion                             // `go env GOVERSION` (stdlib identity)
  + sortedFilterPkgs                      // custom WithFilters set
  + goModHash + goSumHash                 // external dependency versions (pinned)
  + ownSourceHash(P)                      // P's .gsx + sibling .go bytes, path-sorted
  + transitiveInModuleDepSourceHash(P)    // closure of P's in-module imports' source
)
```

- **ownSourceHash(P):** hash of P's `*.gsx` and `*.go` file *contents* (sorted by base name, names + bytes), excluding generated `*.x.go`.
- **transitiveInModuleDepSourceHash(P):** for each package P transitively imports that resolves *inside this module*, fold in that package's ownSource bytes. External-module and stdlib imports are NOT followed (pinned by goSum/goVersion). Computed over the import graph (§4). Deterministic order (sort by import path).
- **gsxCodegenVersion:** a constant in the codegen package; the implementer bumps it whenever lowering changes. (A safety net beyond source hashing, since codegen logic isn't an input file.)

Rationale: we never need *type information* to decide invalidation — only whether any *source that contributes types* changed. Hashing source captures that without a compile. Over-invalidation is mild and acceptable (a comment edit in a dependency regenerates its dependents; regeneration is cheap).

---

## 4. The in-module import graph

Needed to compute `transitiveInModuleDepSourceHash`. Two sources, combined:

1. **`go list -json ./...`** (metadata only — NO `-export`, no compile; ~0.01–0.1s warm). Authoritative for the existing Go files: handles `replace` directives, build tags, and the import graph of hand-written `.go` and already-generated `.x.go`.
2. **gsx's own parsed `.gsx` Go-block imports.** A gsx package expresses its cross-package dependencies (incl. cross-gsx-package component refs like `<ui.Button/>`) as imports in its `.gsx` Go-blocks, which codegen hoists into `.x.go`. Before a package is generated, `go list` can't see those edges; gsx parses them directly from the `.gsx` source. (gsx already parses these during codegen.)

A package's import is "in-module" iff its path has the module path (from `go.mod`) as a prefix; its directory is `moduleRoot + importPath[len(modulePath):]` (standard layout). Anything else (external module, stdlib) is out-of-module and not followed.

**Cold vs warm consistency:** on a cold first run no `.x.go` exist, so `go list`'s graph for gsx packages is incomplete — but a cold run is all-MISS and regenerates everything regardless, so this is benign. Augmenting with parsed `.gsx` imports (source 2) keeps keys stable from the first run; without it, the first warm run might regenerate a bit more than necessary, then stabilize. (Implementer may start with `go list` only and add `.gsx`-import augmentation if cold→warm churn is observed — both are correct.)

**Uncertainty → MISS:** any failure to build the graph for a package (parse error, a `replace` we can't classify, nested module) means that package (and its dependents) is treated as a MISS and regenerated. The cache only skips work it is certain is unchanged.

---

## 5. The content-addressed store

Like the Go build cache. Default location `os.UserCacheDir()/gsx/` (respects `XDG_CACHE_HOME` and OS conventions). `GSXCACHE=<dir>` overrides the location; `GSXCACHE=off` (and `--no-cache`) bypasses the cache entirely (always generate — the current behavior, preserved as the escape hatch).

```
<cacheDir>/<key[:2]>/<key>        // sharded by key prefix
   value: the package's generated output — the set of { relPath → .x.go bytes }
          for that package (a small encoded blob; one entry per package key)
```

- **get(key) → output | miss.**
- **put(key, output):** write to a temp file in the cache dir, then atomic rename (safe under concurrent `gsx generate`).
- Content-addressed: identical inputs → identical key → reuse. Cannot desync from source (the cache holds outputs, not a claim about on-disk files).

---

## 6. Run flow (the runner)

```
1. dirs        := discoverDirs(paths)                       // existing
2. graph       := buildImportGraph(moduleRoot, dirs)        // §4 (go list -json + .gsx imports)
3. for P in dirs: key[P] := computeKey(P, graph, env)       // §3, pure hashing
4. HIT  := { P : store.has(key[P]) } ; MISS := dirs \ HIT
5. RESTORE: for P in HIT:
       out := store.get(key[P])
       for each (relPath, bytes) in out:
           if on-disk file != bytes: write it (hash-gated; record as Written)
6. GENERATE (only if MISS non-empty):
       results := codegen.GeneratePackagesWithFilters(moduleRoot, MISS, filterPkgs)  // ONE load
       for P in MISS:
           if results[P].Err: record Err
           else: write results[P].Files (hash-gated); store.put(key[P], results[P].Files)
7. report Result{Written, Errs}                              // same shape as today
```

**Ordering invariant (correctness):** RESTORE (5) runs entirely *before* GENERATE (6). So when a MISS package B imports a HIT gsx package A, A's current `.x.go` is already on disk and B's type resolution finds A's symbols via normal Go export-data resolution — no skeleton needed for A. (On a cold run everything is MISS → `GeneratePackages` over the full set resolves cross-package via skeletons, as the corpus proves.) A dependency change makes the dependent's key change too, so dependents land in MISS automatically.

**Hash-gated writes:** never rewrite an `.x.go` whose bytes are unchanged (avoid spurious downstream `go build` cache invalidation).

---

## 7. Tier 0 prerequisite (folded into the plan)

`codegen.GeneratePackages` exists (one load over `"./..."`, std filters only — built for the test corpus). Tier 2 needs:
- **`GeneratePackagesWithFilters(moduleDir, dirs, filterPkgs)`** — custom filter packages (mirror `GeneratePackageWithFilters`), via `loadFilterTableMulti`.
- **Load only the given `dirs` as explicit patterns**, not `"./..."` — otherwise a real project source-type-checks its whole module (the `NeedSyntax`-over-everything trap). Deps then come from export data.
- `wsnorm.Normalize` is already mirrored in `GeneratePackages`; keep it.
- Wire `gen.generate()` to call it once (replacing the per-dir loop) as the uncached baseline; the cache layer then sits in front.

This must land before/with the cache layer.

---

## 8. Testing

**Unit — key (`computeKey`):**
- Stable: identical inputs → identical key across runs.
- Sensitive: changing each component independently changes the key — own source, a dependency's source, `go.mod`, `go.sum`, Go version, gsx codegen version, filter-package set.
- Graph closure: editing dependency C invalidates dependent B but NOT unrelated D.

**Unit — store:** get/put round-trip; miss on absent key; atomic write; honors `GSXCACHE` dir + `off`.

**Integration (temp module, like the corpus harness):**
- Cold run: generates all, populates store.
- Warm no-op run: zero codegen, zero file writes (assert generate-count 0 / no mtime changes).
- Edit one package: only it (+ its in-module dependents) regenerate; unrelated packages untouched.
- Branch-switch simulation: revert sources to a prior state → outputs restored from cache with no codegen.
- `GSXCACHE=off` / `--no-cache`: always generates (cache bypassed).
- Equivalence: cached output byte-identical to an uncached `gsx generate`.

---

## 9. Open questions deferred to the plan (not blocking)

- Exact encoding of a store entry's multi-file output blob (e.g. a simple length-prefixed or txtar-like format).
- Whether to ship the `.gsx`-import graph augmentation in v1 or start with `go list` only (both correct; affects only cold→warm churn).
- `gsx clean --cache` command surface and whether to print cache stats.
- Automatic GC/size cap (deferred; manual clean in v1).
