# gsx Codegen Performance & Caching — Findings, Rationale, Plan

**Date:** 2026-06-21
**Status:** Research recorded; Tier 0 ready to wire; Tier 2 (incremental cache) is the priority — design pending.
**Why this doc exists:** so we never redo this research. It records *where the time goes*, *how comparable tools solve it*, *what we decided*, and *why*.

---

## 1. Motivation — the path that must be fast

gsx users won't run `gsx generate` once. The dominant workflow is **repeated, watch-driven regeneration**:

- a file watcher (e.g. `wgo` in a project like `~/work/one-learning`) re-runs `gsx generate` on every `.gsx` save, then `go build`;
- even a future Vite plugin that orchestrates generate→build cannot stop users from running the toolchain **cold** (fresh checkout, CI, first save after a branch switch).

So the bar is: **the repeated/cold `gsx generate` path must stay fast**, and a single file change should cost close to nothing. The `go build` step after us is unavoidable and outside our control; our job is to make the *generate* step cheap and to not redo work that hasn't changed.

This is why **Tier 2 (incremental, content-hash caching) is the most important lever**, more than the one-shot whole-module load (Tier 0). Tier 0 makes a full generate cheap; Tier 2 makes the *steady-state watch loop* cheap.

---

## 2. The bottleneck (measured, not assumed)

gsx is distinctive among Go templating tools: its generator **resolves the Go type of each interpolation expression** (via `go/types` / `go/packages`) to choose a render strategy. That type resolution is the cost center.

**Root cause, confirmed by instrumentation during the corpus-test work:**
- The test corpus (and prod `gen.generate()`) resolved types **per package** — each `codegen.GeneratePackage` does a `go/packages.Load` **plus** a `loadFilterTable` load (2 loads/package). For N packages: **2N loads**.
- Instrumented split of the corpus run (97 renderable packages): the batch `go run` (compile+link+execute everything) was **0.43s**; the ~per-case `go/packages.Load` calls were **~50s** even parallelized across 32 cores (`sys`-dominated — subprocess + export-data churn).
- Fix applied to the corpus: one `go/packages.Load("./...")` for the whole module (`codegen.GeneratePackages`). Corpus suite **~114–140s → 1.46s (~90×)**. **The compile was never the problem; per-package `go/packages` loading was.**

The same per-package pattern still lives in **production** `gen/gen.go`:
```go
for _, dir := range dirs {
    out, gerr := codegen.GeneratePackageWithFilters(dir, filterPkgs) // 2 loads each
}
```

---

## 3. Cost model — where the time actually goes

Benchmarked locally (Go 1.26.1, this 9-package repo, isolated GOCACHE):

| Command | Cold | Warm |
|---|---|---|
| `go list` (metadata only) | 0.10s | 0.01s |
| `go list -export` (single pkg) | 1.67s | 0.03s |
| `go list -export ./...` (whole module) | 2.92s | **0.15s** |

- `go/packages.Load` is **~160× slower than `go/build.Import`** (1.272s vs 0.008s) — [golang/go#31087](https://github.com/golang/go/issues/31087), where maintainers explicitly advise "**keep the number of Load calls low**."
- `packages.Load` itself adds **337ms–1.68s warm** on Apple Silicon — [golang/go#63863](https://github.com/golang/go/issues/63863).
- **Batching** all patterns into one `go list`/`Load` is **~4.8×** faster than per-import calls — [golang/mock#396](https://github.com/golang/mock/issues/396). This is subprocess-spawn reduction, *independent of caching*.

**What `GOCACHE` does NOT cache (paid every run, even warm):**
- **Package metadata collection** — directory scan, import parse, build-constraint eval — is not in GOCACHE; redone every invocation ([golang/go#31417](https://github.com/golang/go/issues/31417); a ~20% prototype never landed).
- **MVS module-graph resolution** — recomputed at the start of every module-aware command (memoized only in-process).
- **Export-data decode into `*types.Package`** — redone every `Load`; "each call to Load uses a new types.Importer."

**Traps:**
- `go list -export` reads as metadata-only but **forces a build/compile** ([golang/go#29667](https://github.com/golang/go/issues/29667)). Plain `go list` (no `-export`/`-compiled`) never touches GOCACHE.
- **LoadMode trap:** `NeedDeps | NeedSyntax | NeedTypesInfo` applied across *all* matched packages source-type-checks the whole set (~10× cost). Dependencies should come from **export data**; source-level syntax/types only for the packages you actually generate from.
- Export format is **unified since Go 1.20** (lazy-decoded index — only needed symbols decoded); `gcexportdata` reads both indexed and unified.

**Practical floor:** one warm `go list -export ./...` ≈ **0.15s** per run for the whole module. That is the cost we cannot avoid in a stateless CLI; everything above it (per-package loads, source type-checking the world) is what we eliminate.

---

## 4. Prior art — how comparable tools solve it

| Tool | Type resolution at gen time? | Speed strategy | Caching | Relevance to gsx |
|---|---|---|---|---|
| **templ** (a-h/templ) | **No** — purely syntactic; emits Go expressions verbatim and lets `go build` type-check the *generated* code | Fast because it never calls `go/types` during generate | Hash-gates output (skips writing unchanged files); LSP proxies **gopls** for types | gsx **can't** go fully syntactic — it needs the resolved type to pick a render strategy. But "defer type-checking to the Go compiler where possible" and "hash-gate output" are stealable. |
| **gqlgen** | Yes (`go/packages` heavily) | Batch loads via an `internal/code` packages wrapper; "load once" | In-process load cache | Confirms batching is the first win. |
| **mockery** v2→v3 | Yes | v2 loaded `packages.Load` **per file**; v3 loads **per package once** → **~10×** | — | Same lesson: collapse Load calls. |
| **ent** | Yes (`packages.Load` + `NeedTypes\|NeedTypesInfo` + `types.Implements`) **+ a `go run` of a generated program** | — | **None** — pays full load + go run every invocation | Closest *structural* precedent (type discovery like gsx's prop-field harvest) but **not** a caching model to copy. |
| **sqlc / oapi-codegen** | No (spec/SQL-driven; sqlc has zero `x/tools` dep) | N/A | — | Not precedents (no Go type loading). |
| **gopls** | Yes — the reference for *incremental* type info | `Cache` (process-shared, memoized) / `Session`+`View`/`Snapshot` (immutable, ref-counted); per-package compilation → compact API summaries | **Own on-disk `filecache`** (separate from GOCACHE) storing *decoded* export data + xrefs + methodsets; pruning invalidation via in-memory symbol-reference graph; **~75% avg startup/memory savings** across 28 repos (v0.12+) | The gold standard for "fast after one file edit." Its `cache` package isn't importable, but the primitives `internal/memoize` and `internal/persistent` and the *architecture* are the model for our Tier 3 daemon — and its filecache is the conceptual model for our Tier 2 disk cache. |
| **unitchecker / `.vetx`** | Yes (analysis facts) | The official way to do incremental type-aware analysis **without a daemon**: ship as a unitchecker-style binary; `go vet`/`go build` drives it and the *build system's* per-compilation-unit cache gives incrementality for free (wrapper: `gostaticanalysis/codegen`) | Via the build cache | Interesting but only fits if our output is expressible as analysis facts — gsx emits `.x.go` files, so this is a poor fit. Recorded for completeness. |

**Distinct techniques extracted:**
- **(a) Batch — one `Load` per module, not per package.** Biggest, safest, stateless win (~5–10×). *We already built this for tests as `GeneratePackages`.*
- **(b) Tier the LoadMode** — source syntax/types only for the gsx packages; deps from export data. Avoids the ~10× whole-module source-typecheck.
- **(c) Content-hash skip cache (stateless).** Hash each package's inputs; regenerate only changed packages; near-instant no-ops. templ-style hash-gating + gopls-style invalidation, simplified. **No daemon.**
- **(d) Warm-restart via export data** — `go list -export` + `gcexportdata` to read pre-compiled dep type info from `$GOCACHE` instead of re-type-checking deps from source. Complements (b).
- **(e) Daemon / watch process** — long-lived, holds one `types.Importer` + shared `FileSet` + loaded packages in memory; fsnotify-driven incremental reload of changed packages + reverse-deps. Sub-second per save; most powerful, hardest invalidation (gopls model).
- **(f) Hash-gate output** — never rewrite an `.x.go` whose bytes didn't change (avoids spurious `go build` cache invalidation downstream). Cheap, always worth doing.
- **(g) LSP** — proxy/query a running gopls rather than building our own type server.

---

## 5. Decision & rationale

**Chosen path, in priority order for the watch-mode reality:**

### Tier 0 — one load per module (quick win, mostly built)
Wire prod `gen.generate()` to call **`codegen.GeneratePackages`** once instead of looping `GeneratePackageWithFilters` per dir. Makes a *full* generate O(1) loads instead of O(2N). Reuses this branch's tested code. **Two adjustments needed for prod-equivalence** (the corpus version was tuned for an all-packages temp module):
1. **Custom filter packages** — `GeneratePackages` is hardcoded to std (`loadFilterTable`); prod needs `loadFilterTableMulti(moduleDir, filterPkgs)` (the `WithFilters` feature). Add `GeneratePackagesWithFilters`.
2. **Scoped load** — it loads `"./..."` (fine when every package is a target, as in the temp corpus). In a real project that would source-type-check the **entire module** (the LoadMode trap). It must load **only the target gsx dirs as explicit patterns**; deps then come from export data.
   (`wsnorm.Normalize` is already mirrored in `GeneratePackages`, so whitespace is consistent with `GeneratePackageWithFilters`.)

### Tier 2 — incremental content-hash cache (THE priority for watch-mode)
A **stateless** disk cache so the watch loop (`wgo` re-running `gsx generate` per save) and cold-ish reruns only pay for *what changed*. This is where the real user experience lives. Design sketch in §6.

### Tier 3 — daemon / `gsx watch` (future, when there's a long-lived host)
Hold loaded type info in memory for truly sub-second per-save regen (gopls architecture). Only worth it once a daemon/LSP host exists; `wgo`-style process-per-change can't benefit from in-memory state, so **Tier 2 is the right fit for the watcher tools users actually run today.**

**Explicitly rejected:**
- **templ-style fully-syntactic generation** — gsx requires the resolved type to choose a render strategy, so it cannot eliminate `go/types`. (A *partial* move — pushing some type dispatch into generated generics / runtime type switches — is a possible long-term lever to *shrink* what must be resolved, but it's a language-design change, out of scope here.)
- **unitchecker/`.vetx` delegation** — our output is `.x.go` files, not analysis facts.
- **Relying on GOCACHE alone** — it does not cache the metadata scan, MVS resolution, export-data decode, or *our* codegen output, so it can't deliver the no-op-fast property. Our own cache layer is required.

---

## 6. Tier 2 design sketch — content-hash incremental cache

> Goal: `gsx generate` re-run after editing ONE `.gsx` re-resolves and regenerates only the affected package(s); a no-op run does ~nothing beyond a single `go list`.

**Mechanism (stateless, per-package skip):**
1. Resolve the set of gsx package dirs (as today).
2. Compute a **cache key per package** = hash of everything that can change its generated output:
   - the package's own `.gsx` + sibling `.go` file contents;
   - the **export/type identity of each imported package** it depends on (so a type change in a dependency invalidates this package) — sourced from `go list -export -deps -json` action IDs / export-file content hashes (the warm `go list` floor, ~0.15s once per run);
   - the **gsx codegen version** (bump invalidates everything on a compiler change);
   - the **filter-package set** (custom `WithFilters` affects output).
3. Load a **manifest** (project-local, e.g. `.gsxcache/manifest` under the module, git-ignored) mapping `packageImportPath → lastInputHash`. The generated `.x.go` already on disk *is* the cached output.
4. **Skip** every package whose current key == manifest key (its `.x.go` is current). **Regenerate** only the packages whose key changed, via `GeneratePackages` over just those dirs. Update the manifest.
5. **Hash-gate writes** (technique f): even for regenerated packages, don't rewrite an `.x.go` whose bytes are unchanged — avoids invalidating the downstream `go build` cache needlessly.

**Why this matches the watch workflow:** `wgo` spawns a fresh `gsx generate` per save. A stateless content-hash cache makes that fresh process fast (no in-memory state needed): one `go list` to get dep hashes, regenerate the 1 changed package, skip the rest.

**Correctness is the hard part — invalidation:**
- A change to package A's *exported* types must invalidate dependent gsx package B. Including A's export-data/action-id hash in B's key handles this. The dependency hashes must be **transitive** to the extent gsx's resolution depends on them (interpolation types can reference transitively-imported types).
- A change to gsx's codegen logic must invalidate all — the version component covers this.
- Build tags / GOOS / GOARCH / cgo can change resolution — fold relevant build context into the key.

**Cost after Tier 2 (watch steady state):** ~one warm `go list -export ./...` (~0.15s) + type-resolution & codegen for the single changed package. The unchanged N-1 packages cost only their hash comparison.

**Open questions for the Tier 2 spec:**
- Manifest location & format (project-local `.gsxcache/` vs `$GOCACHE` via `GOCACHEPROG` vs a sidecar) — leaning project-local, git-ignored, simple JSON.
- Cheapest reliable source of per-dependency invalidation hashes: `go list -export -deps -json` action IDs vs hashing export files vs the build-id. Need to confirm which is both correct and avoids forcing a full compile when we only need hashes.
- Whether to hash raw `.gsx` source or the post-`wsnorm` AST (raw source is simpler and a superset-safe key).
- Interaction with `GeneratePackages` cross-package skeletons: if only package B is regenerated but it imports gsx package A, A's generated `.x.go` must already exist on disk (it will, from a prior run) — confirm the partial-set load resolves A via its committed `.x.go` (export data), not a skeleton.
- A `--no-cache` / cache-bust flag and a clear cache-version bump discipline.

---

## 7. Sources

- go/packages.Load cost & "keep Load calls low": https://github.com/golang/go/issues/31087
- packages.Load warm latency (Apple Silicon): https://github.com/golang/go/issues/63863
- Batching ~4.8× (one `go list` vs per-import): https://github.com/golang/mock/issues/396
- Metadata not cached in GOCACHE (~20% prototype): https://github.com/golang/go/issues/31417
- `go list -export` forces a build: https://github.com/golang/go/issues/29667
- gopls caching architecture / filecache (v0.12, ~75% savings): https://github.com/golang/tools/tree/master/gopls/internal/cache ; design notes under gopls docs
- gcexportdata (reads indexed + unified export data): https://pkg.go.dev/golang.org/x/tools/go/gcexportdata
- go/packages LoadMode guidance: https://pkg.go.dev/golang.org/x/tools/go/packages#LoadMode
- templ generate (syntactic; gopls for LSP): https://github.com/a-h/templ
- gqlgen internal/code packages loading: https://github.com/99designs/gqlgen
- mockery v3 per-package load: https://github.com/vektra/mockery
- ent loader (packages.Load + go run): https://github.com/ent/ent
- unitchecker / codegen-from-analysis: https://pkg.go.dev/golang.org/x/tools/go/analysis/unitchecker ; https://github.com/gostaticanalysis/codegen
- GOCACHEPROG (Go 1.24+) external cache: https://pkg.go.dev/cmd/go/internal/cacheprog

---

## 8. Status / next steps
- [x] Research recorded (this doc).
- [ ] **Tier 0:** `GeneratePackagesWithFilters` (custom filters + explicit-dir patterns) + wire `gen.generate()` to one call; equivalence-test vs `GeneratePackageWithFilters` (incl. whitespace + a custom filter pkg); confirm it loads only target packages.
- [ ] **Tier 2 (priority):** brainstorm + spec the content-hash cache (resolve the §6 open questions), then implement behind a cache dir + version, with a `--no-cache` escape hatch.
- [ ] **Tier 3:** revisit only if/when a `gsx watch`/LSP daemon exists.
