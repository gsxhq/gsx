# gsx warm module-analysis core (shared by LSP, generate, watch, fmt, playground)

**Status:** approved design (brainstormed 2026-06-26), ready for an implementation plan.

**One-line:** Replace gsx's two separate analysis paths with a single warm,
incremental, in-memory module-analysis core that every tool consumes — making
cross-package go-to-definition / diagnostics / references resolve from in-memory
skeletons (never on-disk `.x.go`), and making a large single-package codebase
(e.g. a converted `one-learning/ui`, ~74k lines) editable without a `go list`
subprocess per keystroke.

---

## 1. Goal & non-goals

### Goal
A reusable **module-analysis core** that:

1. **Analyzes a gsx package lazily and caches the result** — parse → skeleton →
   in-process type-check → harvest (`resolved` types, `ExprMap`, `CrossIndex`,
   `NavIndex`, diagnostics, unused imports).
2. **Resolves cross-package symbols from in-memory skeletons**, via a
   skeleton-based composite importer — never the dependency's on-disk `.x.go`.
   Works *before any generation* and *never goes stale*.
3. **Re-checks only what changed** on an edit (per-package; incremental
   invalidation via a metadata graph + reverse-dependency closure).
4. Is **consumed by every tool** — `gsx generate`, `gsx --watch`, the LSP,
   `gsx fmt`, and the (external, in-memory) **playground server** — so the
   expensive analysis is written and tuned once.

### Non-goals (this slice)
- **Persistent cross-process file cache** (gopls `filecache`/`gcexportdata` warm
  restart). Designed-for, deferred to a later slice; slots onto the same
  metadata graph.
- **`typerefs`-style precise pruning** (re-check a reverse-dep only if the
  *exported surface it reaches* changed). Conservative reverse-dep invalidation
  first; precise pruning is a later optimization.
- **Intra-package incrementality.** Go type-checks a package as a unit; editing
  one file in package `P` re-checks all of `P`. The mitigation is *splitting* a
  monolith into sub-packages (a rewrite-time decision the core rewards), not
  partial-package checking.
- **Async references over a worker with reply-by-id.** Out of scope; the core is
  synchronous and the LSP keeps its existing worker/debounce scaffolding.

---

## 2. Background — why this rewrite

### 2.1 Two analysis paths today
- **`GeneratePackagesWithFilters`** (`internal/codegen/batch.go`) — a `go list`
  (`packages.Load`) batch with a per-package skeleton overlay. Used by `gsx
  generate` (cold), `gsx fmt` (for unused-import detection), and the **LSP**
  (`gen/lsp.go` `Analyze`/`AnalyzeModule`).
- **`GeneratePackagesWithResolver` + `CachedResolver`** (`internal/codegen/resolver.go`)
  — an in-process type-check against a prebuilt importer (`mapImporter`), no
  subprocess. Used by `gsx --watch` and the **playground** (external, in-memory).

The LSP is on the **slow** path: a `go list` + full reload on every settled
keystroke. On a single ~74k-line package that is seconds of latency — unusable.
`gsx fmt` pays a full package load *per format* just to detect unused imports
(`gen/fmt.go:73` → `analyzeUnusedImports` → `GeneratePackagesWithFilters`).

### 2.2 Cross-package resolution leans on `.x.go`
The LSP's single-dir `Analyze` overlays only the edited package; dependencies
load from their on-disk `.x.go`. So cross-package go-to-definition resolves
through the dependency's generated `.x.go` (and its `//line` directives) — stale
if `.x.go` lags the `.gsx`, and **broken before first generation**.

Main's `AnalyzeModule` (whole-module in-memory batch → flat `[]CrossRef`) already
proved the in-memory multi-dir mechanism, but only for **find-references**.
Go-to-definition and diagnostics still flow through the `.x.go`-reliant single-dir
path.

### 2.3 The warm path resolves project packages from `.x.go`
`--watch`'s `newModuleResolver` builds its importer by `packages.Load("./...")`,
which reads project gsx packages from their **on-disk `.x.go`** (hence "needs
`.x.go` on disk", and why `--watch` cold-generates first). The LSP/core cannot do
this — project packages must come from **in-memory skeletons**.

### 2.4 This is gopls's model
gopls: per-package independent type-checking; dependencies consumed via cached
export data; a cheap **metadata graph** (`imports` / `importedBy`) drives
invalidation via the reverse-reflexive-transitive closure of the changed package;
features read a consistent snapshot. We borrow the *design* (documented in
[go.dev/blog/gopls-scalability] and gopls `implementation.md`) and the public
`x/tools` primitives; gopls's `cache`/`metadata`/`typerefs` are `internal/` and
not importable. gsx already has the seed: `CachedResolver`/`mapImporter` is
"type-check one package against a cached importer," and `--watch` maintains a
module-wide warm resolver. The one new idea is a **skeleton-based** composite
importer (gopls's "workspace packages from source", with skeletons as source).

---

## 3. The core: `Module`

A warm, mutable analysis graph for **one module root**. Tool-agnostic. Lives
at/below `internal/codegen` with a **public façade** (so the external playground
can consume it); `internal/lsp` continues to reach it only through the `Analyzer`
interface via `gen/`.

### 3.1 Responsibilities & state
- **File store.** Absolute path → source bytes, sourced from disk **or
  overrides**. Supports a **fully in-memory module** (override-only, no disk
  `.gsx`) for the playground, and per-file overrides for unsaved LSP buffers.
- **Per-package cache.** `dir → *Package` holding: parsed `.gsx` files, per-file
  skeletons, the type-checked `*types.Package` + `*types.Info`, `resolved
  map[ast.Node]types.Type`, `ExprMap`, `CrossIndex`, `NavIndex`, diagnostics,
  unused imports. Built lazily; reused until invalidated.
- **Composite importer** (§3.3).
- **Metadata graph** (§4): `dir → imports`, `dir → importedBy`.
- **External-dep types**: a one-time `packages.Load` result for non-gsx
  dependencies (stdlib, third-party, hand-written `.go`-only packages), or a
  **pluggable importer** (playground supplies a curated allowlist —
  `DefaultPlaygroundImports`).

### 3.2 Surface (sketch — finalized in the plan)
```go
type Options struct {
    ModuleRoot   string
    FilterPkgs   []string
    Aliases      []FilterAlias
    FieldMatcher FieldMatcher
    Classifier   *attrclass.Classifier
    // Importer config: full module load (default) OR a curated allowlist (playground).
    ExternalImports []string // e.g. "./..." (default) or DefaultPlaygroundImports
}

func Open(opts Options) (*Module, error)

func (m *Module) SetOverride(absPath string, src []byte) // unsaved buffer / in-memory source
func (m *Module) Invalidate(absPath string)              // mark the owning package (and reverse-deps) dirty

func (m *Module) Package(dir string) (*Package, error)   // lazy, cached: full harvested analysis
func (m *Module) Generate(dir string) (map[string][]byte, error) // = Package(dir) + emit .x.go
func (m *Module) ModuleRefs() ([]CrossRef, error)        // whole-module cross-references (find-references)
```
`Generate` makes explicit that **generation is analysis + emit** — the emit step
(`generateFile`) consumes `Package(dir)`'s `resolved` map, unchanged.

### 3.3 Composite skeleton-based importer (the crux)
A `types.Importer` layered by package origin:

- **Project gsx package** (a dir under the module root containing `.gsx`):
  return its `*types.Package` from the warm graph — `m.Package(depDir)` —
  type-checking its skeletons on demand, **recursively through this same
  importer**. In-memory, fresh, `.x.go`-free.
- **Everything else** (stdlib, third-party, `.go`-only packages): return from the
  prebuilt external-deps map / pluggable importer. Real on-disk `.go`, no
  staleness; refreshed only on import-graph changes.

The gsx import graph is a **DAG** (Go forbids import cycles), so on-demand
recursive checking terminates. A hand-written `.go`-only package that *imports* a
gsx package is resolved by the external load as usual; its references into gsx
packages are out of scope for the index (consistent with today's `discoverDirs`
limitation).

---

## 4. Incremental invalidation (Phase 2)

- **Metadata graph** from one `packages.Load` (or parsed `.gsx`/`.go` imports):
  `imports` and the reverse `importedBy`. Refreshed only when a file's import set
  or build config changes (cheap to detect: compare parsed import lists).
- **`Invalidate(path)`**: mark the owning package dirty, plus — conservatively —
  its **reverse-reflexive-transitive closure** (everyone who transitively imports
  it). Recompute lazily on the next `Package`/`Generate`/`ModuleRefs` that needs a
  dirty entry.
- **Per-file skeleton cache**: only the edited file's skeleton is rebuilt; the
  package is re-type-checked as a whole (Go's unit), but skeleton-building is
  incremental.
- Conservative-then-tighten: precise pruning (`typerefs`) is deferred; correctness
  first.

---

## 5. Consumers (thin)

| Consumer | Uses core for | Adds on top | Notes |
|---|---|---|---|
| `gsx generate` (CLI/CI) | `Generate(dir)` per discovered dir | write `.x.go`, cache key | Cold = analyze+emit all; correctness gate (§7). |
| `gsx --watch` | warm `Module` + `Invalidate` on fs event | re-emit changed dirs | Replaces the bespoke `CachedResolver` rebuild. |
| **LSP** | `Package(dir)`, `ModuleRefs()` | protocol, go-to-def/hover/refs, debounce/worker | Per-edit re-check of edited package only; `.x.go`-free. |
| `gsx fmt` | `Package(dir).UnusedImports` (or parse-only fast path) | Doc-IR printer | Stops paying a full load per format. |
| **playground** (external, in-memory) | `Open` with curated importer + override-only source; `Generate` | HTTP handler | Public façade; `ExternalImports = DefaultPlaygroundImports`; no disk. |

`internal/lsp` does not import `internal/codegen`; `gen/lsp.go` adapts `*Package`
→ `lsp.Package` exactly as today.

---

## 6. Bug A / Bug B as the first LSP queries on the core

These land in Phase 1 as the first features exercising the warm graph, both
`.x.go`-free:

- **Bug A — go-to-def on a component invocation** (`{ components.Pagination(...) }`):
  resolves via the edited package's `pkg.Info`/`pkg.Fset` from the multi-package
  in-memory batch; cross-package `obj.Pos()` maps to the dep's `.gsx` via the
  overlaid skeleton's `//line`. Add the **skeleton column-precision** fix so it
  lands on the component *name* (not the `component` keyword): anchor the skeleton
  func-decl `//line` to `c.NamePos` with prefix compensation
  (`col = nameCol - genNameCol + 1`; `genNameCol = 6` for `func <Name>`,
  `7 + len(Recv)` for a method).
- **Bug B — go-to-def on closing tags** (`</Card>`, `</ui.Button>`): `CloseNamePos`
  + `componentTagDeclAt` onClose are already in; add the **`crossPkgTagDeclAt`
  onClose** branch for dotted/cross-package closing tags.
- The `emit.go` **column** arithmetic is **dropped** (it was only load-bearing for
  the `.x.go`-mediated jump). The `emit.go` *line* anchor stays — it still improves
  real-build compiler-error messages.

---

## 7. Phasing (slowly; `generate`'s corpus is the correctness gate)

- **Phase 0 — core foundation (new code beside existing paths).** Build `Module`
  + composite skeleton-based importer + lazy per-package analysis. **Acceptance
  gate:** a test drives **generation through the core** (`Module.Generate`) and
  reproduces **every corpus golden `.x.go`** (74+ cases). Nothing else changes;
  all existing tools and tests untouched.
- **Phase 1 — LSP on the core.** `Analyze`/`AnalyzeModule` consume the core.
  Removes `go list`-per-edit and `.x.go` reliance; ships Bug A/B. Importer built
  once over the edited package's analysis set (whole-module vs import-closure
  scope is the §11 decision); per edit re-skeleton the changed file and re-check
  only the edited package. Also: implement proper type-error emission semantics
  (batch-equivalence on type-error packages) and surface `checkSkeletonPackage`'s
  `[]types.Error` as diagnostics in `Generate`/`Package`. Close the transitive
  `.x.go` boundary (gsx → Go-only → gsx) via skeleton-graph routing.
- **Phase 2 — incremental.** Metadata graph + reverse-dep invalidation → the
  scaling lever for a split `ui`. Per-file skeleton cache.
- **Phase 3 — consolidate.** Migrate `generate`, `--watch`, `fmt`, and the
  playground façade onto the core; retire the duplicate `GeneratePackagesWithFilters`
  / `GeneratePackagesWithResolver` paths (or keep go-list batch as a cold-CI
  cross-check until confidence is high). Deferred: persistent file cache,
  `typerefs` pruning.

Each phase ships value and keeps `generate` correct throughout (Phase 0 proves
equivalence; `generate` only *migrates* in Phase 3).

---

## 8. Invariants

- **`.x.go`-independent** for resolution: project-package types come from
  skeletons; positions map to `.gsx` via `//line`; any `.x.go`-filename position
  is treated as synthetic and skipped (the existing `definition.go` guard stays).
- **In-memory capable**: a module with zero on-disk `.gsx` (override-only) must
  analyze and generate (playground).
- **Public façade**: external consumers (playground) reach the core through
  exported API; `internal/lsp` reaches it only via the `Analyzer` interface.
- **Generation equivalence**: `Module.Generate` is byte-for-byte equal to the
  current generate output across the corpus (Phase 0 gate). Scope: single-package,
  non-type-error corpus cases only. On packages that fail to type-check, batch emits
  nothing while Generate emits best-effort output; type-error emission semantics and
  surfacing type-error diagnostics are deferred to Phase 1.
- **`.x.go`-independence boundary**: the invariant holds for direct project gsx
  imports. The narrow (gsx → Go-only → gsx) transitive path resolves the leaf gsx
  package from disk `.x.go` (unexercised by the corpus); closing this boundary is
  deferred to Phase 1/2.
- **DAG assumption**: recursive skeleton importing relies on Go's no-import-cycle
  rule; guard with a `seen` set regardless.

---

## 9. Risks

- **Invalidation correctness** (Phase 2) — the classic hard part; mitigated by
  conservative reverse-dep invalidation + lazy recompute, tightened later.
- **Cold first-analysis latency** on a big module (one external `packages.Load` +
  skeleton-check the closure) — Phase 3's persistent cache is the eventual answer;
  acceptable interim, and far better than `go list` *per edit*.
- **Shared `FileSet` growth** over a long session — bounded by periodic rebuild.
- **Importer completeness** — every import of an analyzed package must resolve
  (project-gsx via graph, external via load); a miss must degrade gracefully
  (best-effort `TypesInfo`, never a panic), as the batch path does today.
- **We reimplement gopls's patterns**, not import them.

---

## 10. Testing (per [[gsx-syntax-change-test-coverage]])

- **Phase 0 equivalence**: drive `Module.Generate` over every corpus case; assert
  byte-equality with the existing golden `.x.go`. This is the core correctness
  gate.
- **Skeleton-importer cross-package** (no `.x.go` on disk): a 2-package module
  where `blog` uses `components.Pagination`; assert `Module.Package("blog")`'s
  `TypesInfo` resolves the use to `components`' decl with a `.gsx` position.
- **In-memory module**: override-only source (no disk `.gsx`) analyzes + generates
  (playground shape).
- **LSP go-to-def cross-package** (Phase 1, no `.x.go`): cursor on
  `{ components.Pagination(...) }` and on `</components.Pagination>` resolves to the
  dep's `.gsx` decl *name* (column-precise).
- **Incremental invalidation** (Phase 2): editing a dep re-checks reverse-deps;
  editing a leaf does not re-check unrelated packages.
- **No regression**: existing generate corpus, LSP go-to-def/refs/diagnostics,
  fmt, and watch tests stay green; full `go test ./...` green.

---

## 11. Open questions for the implementation plan

- Exact package home + name for the public façade (extend `gen`, or a new public
  `gsx`/`analysis` package?).
- Whether Phase 1 builds the importer over the **whole module** or the **import
  closure** of the edited package (closure is cheaper cold; whole-module shares
  with `ModuleRefs`). Decide when wiring Phase 1.
- How `--watch` reconciles its **disk-`.x.go` warm resolver** with the core's
  **skeleton** importer during Phase 3 (likely: `--watch` stops needing the cold
  pre-generate once on the core).

[go.dev/blog/gopls-scalability]: https://go.dev/blog/gopls-scalability
