# gsx warm core — Phase 2d: full-result snapshot cache

**Status:** approved design (brainstormed 2026-06-27), ready for an implementation plan.

**One-line:** Cache the immutable `PackageResult` per package in the warm `Module`, dropped
in lockstep with `pkgTypes` by the same reverse-closure invalidation and fset rebuild, so a
repeat `Package(dir)` for an **unchanged** package returns instantly without re-analysis —
gopls's carried-across-snapshots result layer, **`.x.go`-free** and output-neutral.

## 1. Goal & non-goals

### Goal
`Package(dir)` returns a cached `*PackageResult` when `dir` has not changed since it was last
analyzed, skipping the full `analyze` pipeline (parse + type-check + harvest + emit-for-
diagnostics + `buildCrossNav`). Repeat LSP requests for the same unchanged package — a hover,
then a definition, then a didOpen of a sibling — stop re-analyzing from scratch. The win
compounds on a multi-package module (the `one-learning/ui` driver split into sub-packages):
navigating into an unchanged sub-package is free.

### Non-goals
- **Caching the `Generate` or batch paths.** The cache is `Package`-only (the LSP-target
  path). `Generate` (codegen) and `GeneratePackagesWithFilters` (batch / corpus) are
  untouched — they are not hot repeat-request paths, and the corpus byte-equivalence gate
  must stay exactly as is.
- **Caching the adapted `lsp.Package`.** The `gen/lsp.go` `adaptPackageResult` conversion
  (cheap map copies) re-runs per `Analyze`; only the expensive `analyze` is skipped. Caching
  at the `codegen.PackageResult` level keeps the cache where the cost is.
- **A cross-restart / on-disk result cache** — deferred (roadmap).
- **Changing what counts as "changed."** Invalidation reuses Phase-2b's exact reverse-
  reflexive-transitive closure over the import graph; this phase adds no new change-detection.

## 2. Background

Today (`module.go`), `Package(dir)` runs `m.applyDirty()` + `m.maybeRebuildFset()`, then
unconditionally `m.analyze(dir, …)`, builds the `PackageResult` (emit-for-diagnostics loop +
`buildCrossNav` + `detectUnusedImports`), and returns it — **without caching it**. Phase 2b
caches `pkgTypes[dir]` (the `*types.Package` importers consume) and invalidates it by reverse
closure; Phase 2c rebuilds `fset`+`ext`+`pkgTypes` together on growth. The full `PackageResult`
(`Diags`, `GSXFset`, `Fset`, `Info`, `Types`, `ExprMap`, `CtrlMap`, `GSXFiles`, `CrossIndex`,
`NavIndex`, `UnusedImports`) is rebuilt every call. This phase caches it, invalidated by the
same mechanisms — the natural completion of the gopls snapshot model already half-built.

## 3. Design

One new cache field plus three small drop-sites, all in `internal/codegen`
(`module.go` / `module_importer.go`), guarded by the existing locks.

### 3.1 The cache
New `Module` field `pkgResults map[string]*PackageResult` (guarded by `m.mu`; init in `Open`).
Populated **only** by `Package` (the path that builds a `PackageResult`); the dependency path
(`typesPackageWith`) builds no `PackageResult`, so a package that is only ever an import never
gets an entry — correct and memory-lean.

### 3.2 `Package` cache check
`Package(dir)` becomes:
```
m.analysisMu.Lock(); defer m.analysisMu.Unlock()
m.maybeRebuildFset()      // may clear pkgResults (3.4)
m.applyDirty()            // drops the dirty reverse-closure from pkgResults + pkgTypes (3.3)
m.mu.Lock(); cached := m.pkgResults[dir]; m.mu.Unlock()
if cached != nil { return cached, nil }
… existing externalImporter + analyze + emit + buildCrossNav … → res
m.mu.Lock(); m.pkgResults[dir] = res; m.mu.Unlock()
return res, nil
```
Order matters: `maybeRebuildFset` and `applyDirty` run **before** the cache read, so a rebuild
or a dirty edit has already dropped the stale entry — the read only ever returns a still-valid
result. Because an edit to `dir` marks `dir` dirty (Phase 2b `SetOverride`), `applyDirty` drops
`pkgResults[dir]`, so the **edited package always re-analyzes**; an unchanged `dir` is not in
any dirty closure, so it hits the cache.

### 3.3 Invalidation in lockstep with `pkgTypes`
`invalidateLocked(dirs)` (the shared helper behind `applyDirty` and `Invalidate`) deletes the
reverse-reflexive-transitive closure from **both** maps:
```
for d := range m.reverseClosure(dirs) {
    delete(m.pkgTypes, d)
    delete(m.pkgResults, d)
}
```
So editing package `B` (or analyzing `B` after its buffer changed) drops `pkgResults[A]` for
every `A` that transitively imports `B` — `A`'s cached result, built against the old `B`, is
gone and recomputed on next request. The LSP only analyzes the edited file's dir, but the
closure drop reaches the importers even though they are not themselves re-analyzed yet.

### 3.4 Cleared by `rebuildFset`
`rebuildFset` already resets the position-bearing caches (`fset`/`ext`/`pkgTypes`); add
`m.pkgResults = map[string]*PackageResult{}`. This is **mandatory, not optional**:
`PackageResult.Info`/`Fset`/`ExprMap`/`CtrlMap` index into `m.fset`, so a result retained
across a fset rebuild would resolve positions into the discarded fset — exactly the orphaning
Phase 2c forbids. Resetting `pkgResults` with the fset keeps the no-orphaned-positions
invariant whole.

## 4. Why it is safe

- **Immutable post-construction.** `emit` mutates the gsx AST (`a.gsxFiles`) **during**
  `Package`'s build; once the `PackageResult` is returned, nothing re-runs `emit` on it (a
  cache hit returns the stored pointer; the LSP only reads `Info`/`ExprMap`/AST positions).
  So a cached `PackageResult` is effectively immutable and safe to hand out repeatedly.
- **No stale dependency view.** `pkgResults` shares `pkgTypes`'s invalidation closure, so a
  result is dropped whenever its package or any transitive gsx dependency changes (same
  boundaries as `pkgTypes`, including the documented gsx→Go-only→gsx gap — no new boundary).
- **No cross-fset mixing.** A rebuild clears `pkgResults`; the LSP server replaces its stored
  `lsp.Package` each `Analyze`, so a single request never mixes a cached result with a new
  fset. Old results the server still references stay internally consistent (old fset) until
  replaced — Go GC keeps them alive; no use-after-free.
- **Concurrency.** `pkgResults` is `m.mu`-guarded; `Package` runs under `analysisMu` and takes
  `m.mu` only for the read and the write. `SetOverride`/`Invalidate` already take `m.mu`.

## 5. Components & boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `pkgResults` field + `Open` init | hold per-package results | — |
| `Package` cache read/populate | return cached result; cache the built one | `applyDirty`, `maybeRebuildFset` |
| `invalidateLocked` drop | delete closure from pkgResults too | `reverseClosure` (Phase 2b) |
| `rebuildFset` clear | reset pkgResults with the fset | Phase 2c |

## 6. Invariants

- **Coherent with `pkgTypes`:** every site that drops/clears `pkgTypes` (invalidateLocked,
  rebuildFset) drops/clears `pkgResults` identically. A grep for `pkgTypes` mutation sites must
  show a matching `pkgResults` mutation.
- **Edited package always fresh:** the edited dir is in its own dirty closure → its result is
  dropped before the cache read → re-analyzed.
- **No orphaned positions:** `pkgResults` is position-bearing and is cleared by `rebuildFset`
  together with the fset.
- **Output-neutral / `.x.go`-independent:** `Generate`/batch untouched; the corpus gate
  (`Module.Generate ≡ batch`) is unaffected; resolution still flows through skeletons/`//line`.
- **Bounded:** entries are keyed by dir; re-analysis replaces, invalidation/rebuild remove. The
  cache size tracks the number of analyzed packages, not the edit count.

## 7. Testing (per [[gsx-syntax-change-test-coverage]])

- **Cache hit (the core signal, non-tautological):** `Package(A)` twice with no intervening
  edit returns the **same `*PackageResult` pointer**; after a `SetOverride` content change to
  `A`, `Package(A)` returns a **different** pointer (re-analyzed). Pointer identity is the
  observable; add a `cachedResultDirs()` test hook if a membership view is also useful.
- **Dependency invalidation:** warm `Package(A)` (A imports B); edit `B` via `SetOverride`;
  `Package(A)` returns a **new** pointer (closure drop reached A) and resolves against the new
  B. Editing an **unrelated** package leaves `Package(A)`'s pointer **unchanged** (cache hit).
- **Rebuild clears it:** force a fset rebuild (tiny `fsetRebuildBytes`); `Package(A)` returns a
  **new** pointer **and** cross-pkg go-to-def still resolves to the correct `.gsx` position
  (cached-then-rebuilt must not serve orphaned positions).
- **Correctness of a hit:** a cache-hit `PackageResult` answers go-to-def correctly, and its
  `Diags` equal a fresh (cache-bypassed) analysis's `Diags` — no stale diagnostics served.
- **Concurrency (`-race`):** concurrent `Package` on one Module (hits + misses interleaved).
- **No regression:** corpus gate green (`Generate`/batch path unchanged); existing LSP/codegen
  suites green.

## 8. Out of scope / follow-ups (later)
- Cross-restart on-disk result cache.
- gsx→Go-only→gsx transitive `.x.go` boundary; `didChangeWatchedFiles` disk-edit invalidation;
  wiring `typesPackage` to `maybeRebuildFset`.
- Phase 3: migrate `generate`/`watch`/`fmt`/playground onto the core + public façade.
