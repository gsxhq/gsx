# gsx warm core — Phase 2b: incremental reverse-dependency invalidation

**Status:** approved design (brainstormed 2026-06-26), ready for an implementation plan.

**One-line:** Replace the warm `Module`'s coarse `ResetPackageCache()` (wipes *all*
project type info every keystroke) with gopls-style **reverse-reflexive-transitive
closure** invalidation, so editing package A re-type-checks only A and its transitive
importers — never unrelated siblings — with **zero dependence on generated `.x.go`**.

## 1. Goal & non-goals

### Goal
When a `.gsx` buffer in package A changes, the next analysis re-type-checks exactly
**A and the set of project gsx packages that transitively import A** (the
reverse-reflexive-transitive closure of A over the import graph). Every package
*outside* that closure keeps its cached `*types.Package` (`pkgTypes[dir]`) warm and is
not re-checked. This is the scaling lever that makes splitting a large package (the
`one-learning/ui` driver) into sub-packages pay off: editing one leaf sub-package no
longer cascades into re-checking unrelated siblings.

### Non-goals
- **`.x.go` dependence.** Invalidation operates purely on the in-memory `pkgTypes`
  cache and a skeleton-derived import graph. No generated `.x.go` is read or written.
- **Full-result snapshot cache.** Caching the immutable `PackageResult`
  (Info/ExprMap/CtrlMap/CrossIndex) per package — gopls's carried-across-snapshots
  result — is a *separable perf layer* deferred to a later phase. Phase 2b caches only
  `pkgTypes` (the `*types.Package` importers consume), which is our export-data analog
  and is where gopls's edit-locality and correctness live. The edited package always
  re-analyzes fully; only its unchanged *dependencies* are reused.
- **fset-growth bounding** and the **`interpTemp` cross-Module race** — Phase 2c, each
  with its own spec/plan.
- **Persistent (cross-restart) cache** — deferred (roadmap).
- **gsx → Go-only → gsx transitive boundary** — pre-existing Phase-0/1 gap (a project
  gsx dep reached *through* a Go-only intermediary package resolves via the external
  importer / disk `.x.go`, not skeletons). Invalidation cannot cross that boundary
  because the intermediary is not in `pkgTypes`. Documented, unchanged, out of scope.

## 2. Background — how invalidation works today

`gen/lsp.go:Analyze` does, every call: `SetOverride(buffers)` → `ResetPackageCache()`
→ `Package(dir)`. `ResetPackageCache` (`module_importer.go`) sets
`m.pkgTypes = map[string]*types.Package{}` — it drops **every** project package's
cached type info. Consequence: analyzing A re-type-checks all of A's transitive gsx
dependencies from skeletons on every keystroke, even when only A's buffer changed.

Two latent issues the coarse reset currently masks:

1. **Stale edited-package cache.** `Package(dir)` calls `m.analyze(dir, …)` directly
   (it needs the full `analyzed`, not just `.pkg`) and **never writes `pkgTypes[dir]`**
   — only `typesPackageWith` does. So after `Package(A)`, `pkgTypes[A]` still holds the
   *previous* A (or is absent). A later importer C (`mi.Import(A)` →
   `typesPackageWith(A)`) would resolve against that stale A. The coarse reset hides
   this by wiping `pkgTypes` every call; fine-grained invalidation must fix it
   explicitly.
2. **No import graph.** There is no project-internal imports/importedBy structure, so
   reverse-dependency invalidation has nothing to traverse.

The skeleton is parsed verbatim into the single module-wide `m.fset`; `analyze`
already extracts `importSpecs` per package. These are the raw materials for the graph.

## 3. How gopls does it (the model we port)

gopls is built on **immutable snapshots**: each edit clones the prior snapshot and
reuses everything except what the edit invalidated. Three layers:

1. **Metadata graph** (`metadata.Graph`) — per-package name/files/`imports`/
   `importedBy`, built from `go/packages` metadata *without type-checking*. gopls
   distinguishes **type-check invalidation** (common; edit within a file, imports
   unchanged → keep metadata, drop results) from **metadata invalidation** (rare;
   import set or build config changed → reload `go list`).
2. **Reverse-closure invalidation** — on a type-check invalidation, gopls drops the
   changed package **and its reverse-reflexive-transitive closure** via
   `metadata.Graph.ReverseReflexiveTransitiveClosure`; everything else carries forward.
3. **Dependencies are read, not re-checked** — type-checking A feeds A's imports from
   their **export data** (`*types.Package`), never re-checking dep source.

**Mapping to gsx:**

| gopls | gsx analog | status |
|---|---|---|
| `metadata.Graph` imports/importedBy | derived from skeleton `importSpecs` | **Phase 2b** |
| export data (dep `*types.Package`) | `pkgTypes[dir]` checked package | exists (Phase 1) |
| `ReverseReflexiveTransitiveClosure` | `Invalidate(dirs)` closure | **Phase 2b** |
| snapshot's cached full `*Package` | `PackageResult` | deferred (perf layer) |
| persistent filecache | — | deferred |

Our `pkgTypes[dir]` **is** the export-data layer: a checked `*types.Package` handed to
an importer via the `Importer` interface serves an import directly — identical effect
to gopls reading export data, no re-check. We have no serialized export data (in-memory,
no `.x.go`), so on a *miss* we re-type-check the skeleton; on a *hit* `pkgTypes` gives
gopls's dep-reuse. Phase 2b adds the graph + closure invalidation on top.

## 4. Design

Four additions, all in `internal/codegen` (`module.go` / `module_importer.go`), plus a
one-line simplification in `gen/lsp.go`. All guarded by the existing locks
(`analysisMu` serializes Package/Generate/typesPackage; `mu` guards the maps).

### 4.1 Metadata import graph
New `Module` fields (guarded by `m.mu`):
- `imports map[string][]string` — dir → its project-gsx dependency dirs.
- `importedBy map[string]map[string]bool` — reverse edges (dep dir → set of importer dirs).

Populated by a new helper `recordImports(dir string, specs []importSpec)` called from
`analyze` (after `importSpecs` is known), which:
1. Maps each import path to a dir via `dirForImportPath(moduleRoot, modulePath, path)`;
   keeps only dirs where `isGsxPackage(dir)` is true (project gsx packages — the only
   things in `pkgTypes`). External/stdlib/Go-only edges are ignored. **The paths come
   from both the `.gsx`-hoisted import specs AND the imports of the package's
   hand-written `.go` files** (collected in `analyze` from each parsed `realGF.Imports`):
   a gsx package that imports a sibling gsx package *solely* through a companion `.go`
   (e.g. a `model.go`) is still type-checked against that sibling's skeleton, so its
   reverse edge must be recorded or editing the sibling would not invalidate it.
2. **Replaces** `dir`'s edges: removes `dir` from the `importedBy` set of each of its
   *previous* deps, sets `imports[dir]` to the new dep list, and adds `dir` to the
   `importedBy` set of each new dep. Replacement keeps the graph precise when an edit
   adds or removes an import — and because the edited package always re-analyzes in the
   same turn, its outgoing edges are refreshed before any later edit could consult them.

The graph only ever contains edges for packages that have been analyzed — which is
exactly the set that can be in `pkgTypes`, so graph and cache stay consistent: a
package not yet analyzed is neither cached nor in the graph, and when first analyzed it
builds fresh against current dependencies.

### 4.2 Content-diff dirtiness
New `Module` field: `dirty map[string]bool` (pending changed dirs, guarded by `m.mu`).

`SetOverride(path, src)` gains change detection:
```
base, haveBase := m.currentSource(path)   // override-or-disk; reads disk only if no override yet
// A real change: a base that differs from src, OR a brand-new path with non-empty
// content. A new path with empty src is not a meaningful change (a nonexistent file
// registered with no content), so it does not mark dirty.
changed := haveBase && !bytes.Equal(base, src) || !haveBase && len(src) > 0
if changed {
    m.dirty[filepath.Dir(path)] = true
}
m.overrides[path] = src
```
- Identical bytes (navigation re-request, or didOpen where buffer == disk) → no dirt →
  no invalidation, so read-only requests never churn the cache.
- Disk is read (via `currentSource`) only when no override exists for `path` yet (first
  open), never on the per-keystroke path where a prior override is already in memory.
- `currentSource` is a non-locking internal variant (or reads disk outside the lock) to
  avoid re-entering `m.mu`, which `SetOverride` holds.

### 4.3 Reverse-closure invalidation
New method `Invalidate(dirs ...string)` (exported; guarded by `m.mu`): computes the
reverse-reflexive-transitive closure of `dirs` over `importedBy` (BFS/DFS from each
seed, following `importedBy` edges, the seed itself included) and deletes each closure
member from `pkgTypes`. Graph edges are **not** dropped (they are refreshed on
re-analyze; retaining them is harmless and lets a later edit to a shared dep still find
its importers).

New internal `applyDirty()` (guarded by `m.mu`): `Invalidate(keys(m.dirty)...)` then
`m.dirty = map[string]bool{}`. Called at the start of `Package` and `Generate` (already
under `analysisMu`), before `externalImporter`/`analyze`, so each analysis run consumes
the accumulated pending edits exactly once.

`ResetPackageCache` is **removed** (no remaining caller after 4.5); its coarse semantics
are superseded. If a coarse "drop everything" is ever needed (tests), `Invalidate` over
all cached dirs covers it; the closure machinery makes a dedicated coarse method
unnecessary.

### 4.4 Edited-package consistency fix
`Package(dir)` and `Generate(dir)`, after a successful `m.analyze(dir, mi)`, write the
fresh result into the cache and graph exactly as the importer path does:
```
m.mu.Lock()
if m.pkgTypes == nil { m.pkgTypes = map[string]*types.Package{} }
m.pkgTypes[dir] = a.pkg
m.mu.Unlock()
m.recordImports(dir, a.importSpecs)
```
(`recordImports` is also called from `analyze`/`typesPackageWith` for the dep path, so
this may be a single call sited in `analyze` itself — see plan. The invariant: every
analyzed package, whether reached as an LSP target or as a transitive import, updates
both `pkgTypes` and the graph.) This removes the stale-`pkgTypes[A]` path: after
`Package(A)`, an importer of A sees the fresh A.

`a.importSpecs` must be reachable on the `analyzed` struct (it already carries the
parsed skeleton + harvested maps; confirm/expose `importSpecs`).

### 4.5 LSP wiring
`gen/lsp.go:Analyze` drops the `m.ResetPackageCache()` line entirely. The flow becomes
`SetOverride(buffers) → Package(dir)`. The Module self-invalidates from `SetOverride`
content diffs, so every consumer (LSP now; generate/watch/playground later) gets correct
incremental behavior without manual cache management.

## 5. Data flow (the win)

Module split `pages` → `components` → `util` (pages imports components imports util).
After all three are warm (analyzed once):

- **Edit a `components` file** → `SetOverride` diffs it → `dirty = {components}` →
  `applyDirty` closure over `importedBy` = `{components, pages}` (pages imports
  components; util does not) → drop `pkgTypes[components]`, `pkgTypes[pages]`;
  **`pkgTypes[util]` stays warm** → `Package(components)` re-checks components reusing
  warm util, caches fresh `pkgTypes[components]`. Next `pages` request re-checks pages
  against fresh components.
- **Edit `util`** → closure `{util, components, pages}` (all transitively import util).
- **Edit a leaf** nothing imports → closure = `{leaf}` only.
- **Navigate (no edit)** in any package → no dirt → nothing invalidated.

## 6. Components & boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `recordImports(dir, specs)` | maintain imports/importedBy (replace dir's edges) | `dirForImportPath`, `isGsxPackage` |
| `SetOverride` change detection | mark `dir` dirty on real content change | `currentSource`, `bytes.Equal` |
| `Invalidate(dirs…)` | reverse-closure over `importedBy` → drop `pkgTypes` | `importedBy` |
| `applyDirty()` | consume `dirty` → `Invalidate` → clear | `Invalidate`, `dirty` |
| `Package`/`Generate` | `applyDirty` at start; cache fresh `pkgTypes[dir]` + edges | `applyDirty`, `recordImports` |
| `gen/lsp.go:Analyze` | drop `ResetPackageCache`; SetOverride + Package | self-invalidation |

## 7. Invariants

- **`.x.go`-independent:** invalidation reads/writes only `pkgTypes` + the
  skeleton-derived graph; no generated code consulted.
- **No coarse churn:** an identical-content (no-op) analysis invalidates nothing; an
  unrelated package's `pkgTypes` survives an edit to another package. (Test-asserted via
  a cache-membership hook.)
- **Closure-correct:** editing A makes every transitive gsx importer of A re-resolve
  against fresh A on its next analysis (its `pkgTypes` was dropped → re-check); the
  consistency fix removes the stale-`pkgTypes[A]` path.
- **Graph ⊆ cache domain:** the graph only holds edges for analyzed packages; an
  unanalyzed package is neither cached nor in the graph and builds fresh on first use.
- **Concurrency:** `imports`/`importedBy`/`dirty`/`pkgTypes` are all guarded by `m.mu`;
  `applyDirty`/`Package`/`Generate` run under `analysisMu`. Concurrent `SetOverride` +
  `Package` is race-free (verified under `-race`).
- **Additive to codegen output:** generated `.x.go` bytes unchanged; corpus equivalence
  gate stays green.

## 8. Testing (per [[gsx-syntax-change-test-coverage]])

- **codegen unit (`internal/codegen`):**
  - Build a 3-package chain `util ← components ← pages` plus an unrelated `D`; warm all
    (analyze `pages` + `D`). Edit a `components` file (`SetOverride` with changed bytes);
    assert the closure `{components, pages}` is dropped from `pkgTypes` and `util` + `D`
    stay cached (via a test-only `cachedDirs()` membership hook).
  - Editing `util` invalidates `{util, components, pages}`; editing a leaf nothing
    imports invalidates only itself.
  - **No-op:** `SetOverride` with byte-identical content marks nothing dirty; a sibling's
    `pkgTypes` survives a `Package` call.
  - **Re-resolution:** add/change an exported symbol in `util` via override; after
    invalidation, analyzing `components` resolves the new symbol's type (proves the
    transitive re-check actually re-reads `util`).
  - **Edge replacement:** a package that drops an import no longer appears in the old
    dep's `importedBy` after re-analyze (editing the old dep no longer invalidates it).
  - **Consistency fix:** `Package(A)` populates `pkgTypes[A]`; a subsequent import of A
    (analyze a C that imports A) sees the fresh A, not a stale one — a regression test
    that fails before 4.4.
- **concurrency:** concurrent `SetOverride` + `Package` on one Module under `-race`
  (extends the existing `TestModuleConcurrentPackage`).
- **gen e2e (`gen/…_e2e_test.go`):** two project packages where `pages` imports
  `components`; open both as buffers; edit a `components` symbol via the buffer; assert a
  go-to-def / diagnostic in `pages` reflects the change — with **no `.x.go` on disk**.
- **no regression:** corpus gate green; existing LSP go-to-def/hover/references + the
  Phase-1/2a suites green; full `go test ./...` (and `-race` on `internal/codegen`)
  green.

## 9. Out of scope / follow-ups (Phase 2c and later)
- Full-result `PackageResult` snapshot cache (repeat-request reuse for unchanged
  packages).
- Bounding module-wide `m.fset` growth (orphaned positions from invalidated packages
  accumulate; reverse-dep invalidation slows but does not stop this).
- `interpTemp` cross-Module global race.
- gsx → Go-only → gsx transitive `.x.go` boundary.
- Persistent (cross-restart) cache.
