# Persistent `go list` Graph Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Skip the warm-path `go list -deps` in `gsx generate` whenever a stored, module-scoped graph is provably what `go list` would return now.

**Architecture:** `prepareCache` computes a graph key, then validates stored variants against a *witness* (file names, Go headers, embed lines, `go.mod` boundaries of the graph's main-module dirs) derived from the in-memory manifest. A hit rebases the stored module-relative graph onto the run's `GOMOD` dir. A miss runs `go list` as today, re-verifies the witness from disk, and stores a new variant.

**Tech Stack:** Go 1.27.1 (repo pin), stdlib `go/parser` (`ImportsOnly`), existing `gen` cache dir and atomic-rename store.

**Spec:** `docs/superpowers/specs/2026-10-30-go-list-graph-cache-design.md`

## Global Constraints

- A false hit is a correctness bug; any doubt is a miss (spec "Anti-goal"). That includes cases where a live `go list` would fail or the projection would reject the graph.
- Cache keys of generated output, `.x.go` output, and the corpus must not change.
- Graph cache is shared across checkouts: main-module paths stored relative to the module root, rebased onto `filepath.Dir(GOMOD)`.
- Up to **4** variants per graph key, most recently used first.
- Directory-replaced (mutable) modules in the graph ⇒ uncacheable (always run `go list`).
- A graph main-module dir not covered by the manifest walk ⇒ uncacheable.
- Hits must perform no file reads beyond the manifest (and `Stat` of stored `EmbedFiles`); the extra disk re-read happens only on a miss.
- `GSXCACHE=off`, an uncacheable Go context, and an unusable cache dir all keep today's behaviour (live `go list`).
- Never call `packages.Load` in these tests; one fixture module per test, many table rows.

## Review Focus

- A checkout reached through a symlinked root, and a second checkout at another path: a hit must equal a live `go list` byte for byte, including `Dir`, `Module.Dir`, `Module.GoMod` (Task 6 test).
- Concurrent `generate` processes writing the same key, or a truncated or garbage entry file: the result is a miss, never an error or a partial graph (Task 5 test).
- An entry written by an older format version: a miss (Task 5 test).
- A source file made unreadable after `Build`: a hit still succeeds, which proves hits read nothing (Task 6 test).
- A cache dir that can't be written: generate still succeeds with a live `go list`, and `-v` reports why (Task 6 test).

---

### Task 1: `GoCommandContext.GoMod`

**Files:**
- Modify: `internal/codegen/go_overlay.go` (`frozenGoEnvironment`, `freezeGoCommandEnvironment`, `GoCommandContext`)
- Test: `internal/codegen/go_overlay_test.go`

**Interfaces:**
- Produces: `func (context *GoCommandContext) GoMod() (string, error)`. It returns the absolute `go.mod` path exactly as `go env GOMOD` reported it in this context. It errors when capture failed, or when `GOMOD` is empty or `/dev/null` (not module mode).

- [ ] **Step 1: Write the failing test.** In `TestGoCommandContextGoModMatchesGoListSpelling`, make a temp module `real/` with `real/a/a.go` and a symlink `link -> real`. For each root (`real`, `link`):
  - capture a context with `CaptureGoCommandContext(root)`;
  - run `context.Run("list", "-f", "{{.Dir}}", "./a")`;
  - assert `strings.TrimSpace(out) == filepath.Join(filepath.Dir(gomod), "a")`, where `gomod, _ := context.GoMod()`.
- [ ] **Step 2: Run it and check it fails to compile:** `go test ./internal/codegen -run TestGoCommandContextGoModMatchesGoListSpelling -count=1`. Expected: `GoMod undefined`.
- [ ] **Step 3: Implement it.** Keep `environment["GOMOD"]` in `frozenGoEnvironment.goMod` before `canonicalGoEnvironment` deletes it, and store it on the context. Do not add it back to `cacheEnv`.
- [ ] **Step 4: Run the test and check it passes.** If the `link` row fails, `go env` and `go list` derive the root differently. Stop and report; don't paper over it.
- [ ] **Step 5: Commit** `feat(codegen): expose the captured GOMOD path`.

### Task 2: The manifest records directory listings

**Files:**
- Modify: `internal/sourceview/manifest.go` (`Manifest`, `Build` walk, `newManifest`, `RefreshDirs`, `WithOverrides`, `WithFileSnapshots`)
- Test: `internal/sourceview/manifest_test.go`

**Interfaces:**
- Produces, unexported, used by Task 4:
  - `manifest.dirListings map[string]dirListing`, keyed by the module-relative slash path (`"."` for the root), covering every directory the walk entered;
  - `type dirListing struct { files []string; goMod bool }`, where `files` holds the sorted names of regular files (every extension) and `goMod` says whether the directory has a `go.mod` (true for the root);
  - `manifest.skippedDirs map[string]skipKind` for directories the walk did not enter, with `skipNestedModule` and `skipUnimportable`;
  - `manifest.witnessable bool`: true from `Build` and `RefreshDirs`, false for any manifest derived through `WithOverrides`/`WithFileSnapshots`, because overrides change the file set without a disk listing.
- Produces: `func (manifest *Manifest) Witnessable() bool`.

- [ ] **Step 1: Write the failing tests.**
  - `TestBuildRecordsDirListings`: a fixture with `a/x.go`, `a/x.c`, `a/notes.txt`, `nested/go.mod` (holding `nested/n.go`) and `testdata/t.go`. Assert:
    - `dirListings["a"].files == {"notes.txt","x.c","x.go"}`;
    - `skippedDirs["nested"] == skipNestedModule`;
    - `skippedDirs["testdata"] == skipUnimportable`;
    - `dirListings["."].goMod`;
    - `Witnessable()`.
  - `TestRefreshDirsUpdatesListings`: add `a/y.go` and call `RefreshDirs([a])`. The listing then contains `y.go`.
  - `TestOverridesAreNotWitnessable`: `WithOverrides(...)` gives `Witnessable() == false`.
- [ ] **Step 2: Run** `go test ./internal/sourceview -run 'TestBuildRecordsDirListings|TestRefreshDirsUpdatesListings|TestOverridesAreNotWitnessable' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement it.** Record listings inside the existing `WalkDir` callback: append each non-dir entry's name to its parent's listing, and record skips where the walk returns `SkipDir`. Pass the result through `newManifest` (add parameters; `RefreshDirs` replaces the listings of the dirs it refreshes). Don't add a second walk.
- [ ] **Step 4: Run** the new tests plus `go test ./internal/sourceview -count=1 -race`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(sourceview): record walked directory listings`.

### Task 3: Go header and embed-line extraction

**Files:**
- Create: `internal/sourceview/goheader.go`
- Test: `internal/sourceview/goheader_test.go`

**Interfaces:**
- Produces:
  - `func goFileHeader(src []byte) []byte`: the bytes from offset 0 through the end of the last import declaration, or through the end of the package clause when there are none. If `parser.ParseFile(..., parser.ImportsOnly|parser.ParseComments)` errors, the whole of `src`.
  - `func goEmbedLines(src []byte) [][]byte`: every line whose trimmed text starts with `//go:embed`, in order.

- [ ] **Step 1: Write the failing table test `TestGoFileHeader`.** Each row checks whether `goFileHeader(a)` equals `goFileHeader(b)`:
  - a body edit: equal;
  - an added import: differs;
  - reordered imports: differs;
  - a `//go:build` line changed: differs;
  - the package name changed: differs;
  - the cgo preamble changed before `import "C"`: differs;
  - a file with no imports where only the body changes: equal;
  - an unparseable header: the result is the whole `src`.

  `TestGoEmbedLines` covers two directives with a code line between them.
- [ ] **Step 2: Run** `go test ./internal/sourceview -run 'TestGoFileHeader|TestGoEmbedLines' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement both functions.** Use `fset.Position(decl.End()).Offset` of the last `*ast.GenDecl` whose `Tok == token.IMPORT`; otherwise use `file.Name.End()`.
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(sourceview): extract Go file headers and embed lines`.

### Task 4: Graph witness digest

**Files:**
- Create: `internal/sourceview/graphwitness.go`
- Test: `internal/sourceview/graphwitness_test.go`

**Interfaces:**
- Consumes: Task 2 listings, Task 3 extractors, `manifest.helperGoFiles`, `manifest.overlay`.
- Produces:
  - `type GraphWitnessInput struct { Dirs []string; EmbedFiles []string }`. Both are module-relative slash paths. `Dirs` includes the absent "shadow" dirs of spec item 5.
  - `var ErrWitnessUnavailable = errors.New("sourceview: graph witness unavailable")`
  - `func (manifest *Manifest) GraphWitness(input GraphWitnessInput) (string, error)`: a hex SHA-256, computed from the manifest only.
  - `func (manifest *Manifest) DiskGraphWitness(input GraphWitnessInput) (string, error)`: the same digest from a fresh read of disk listings and files. Overlaid paths still use overlay bytes.

  Both share one unexported `witnessDigest(input, src witnessSource)`, so their outputs are identical by construction.

**Digest rule.** For each dir in sorted `Dirs`, write its state:
- **present:** listing file names; then, for each non-`_test.go` `.go` name, `goFileHeader` and `goEmbedLines` of its bytes. Overlay bytes are used if the path is overlaid; otherwise helper snapshot bytes. A `FileUnreadable` snapshot gives `ErrWitnessUnavailable`.
- **absent:** the dir is in no listing, and its nearest recorded ancestor is a listing.
- **under nested module:** its nearest ancestor is `skipNestedModule`.
- **unavailable:** the nearest ancestor is `skipUnimportable`, or the dir is an unwalked directory that exists. This returns `ErrWitnessUnavailable`.

Then, for each dir, write the `goMod` flag of the dir and of every ancestor to `"."`. Then, for each `EmbedFiles` path, write whether it exists (`os.Lstat`; this is the only disk access on a hit). A manifest with `!Witnessable()` gives `ErrWitnessUnavailable`.

- [ ] **Step 1: Write the failing table test `TestGraphWitnessDetectsGraphInputs`.** It uses one fixture and builds a fresh manifest per row. Each row checks the digest for `Dirs={"a","b"}` against the base:
  - a body edit to `a/x.go`: same;
  - an `a/x.go` import added: differs;
  - `a/y.go` added: differs;
  - `a/x.c` added: differs;
  - `a/x_test.go` added or edited: same;
  - a `//go:embed` line edited: differs;
  - an embed target deleted (with `EmbedFiles={"a/data.txt"}`): differs;
  - `a/go.mod` added: differs;
  - shadow dir `c` created (with `Dirs` including `c`): differs;
  - a regenerated `.x.go` paired output: same.

  `TestDiskGraphWitnessMatchesManifest`: on an unchanged tree the two methods are equal; editing a header on disk after `Build` makes them differ. `TestGraphWitnessUnavailable` covers `testdata/x`, an overrides-derived manifest, and an unreadable `.go`.
- [ ] **Step 2: Run** `go test ./internal/sourceview -run 'TestGraphWitness|TestDiskGraphWitness' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement it.** Write length-prefixed fields so that concatenation can't be ambiguous.
- [ ] **Step 4: Run the tests plus the whole package with `-race`.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(sourceview): graph witness digest`.

### Task 5: Graph cache store

**Files:**
- Modify: `internal/sourceview/cache.go`. Add `EmbedFiles []string` to `PackageMetadata`; `DecodeGraph` and `cloneGraph` carry it. The projection ignores it.
- Create: `gen/graphcache.go`
- Test: `gen/graphcache_test.go`

**Interfaces:**
- Consumes: Task 4 `GraphWitnessInput`, `GraphWitness`; `cacheDir()`; `entryPath`.
- Produces:
  - `type graphCacheReason string`, with constants:
    - `graphHit = "graph-hit"`
    - `graphEntryMissing = "graph-entry-missing"`
    - `graphEntryCorrupt = "graph-entry-corrupt"`
    - `graphWitnessChanged = "graph-witness-changed"`
    - `graphUncacheable = "graph-uncacheable"`
    - `graphStoreSkipped = "graph-store-skipped"` (files changed during `go list`)
    - `graphStoreFailed = "graph-store-failed"`
  - `type graphCacheOutcome struct { Reason graphCacheReason; Detail string }`, with `func (o graphCacheOutcome) Hit() bool`.
  - `func graphKey(fingerprint string, manifest *sourceview.Manifest, patterns []string) (string, error)`: SHA-256 over the format version `"gsx-graph-v1"`, the fingerprint, `manifest.ModulePath()`, the patterns (already module-relative `./…` or import paths), the module-relative overlay paths and bytes from `manifest.Overlay()`, and the bytes of `go.mod` and `go.sum` under `manifest.ModuleRoot()` (`go.sum` may be absent).
  - `func witnessInputForGraph(graph sourceview.Graph, manifest *sourceview.Manifest) (sourceview.GraphWitnessInput, error)`:
    - `Dirs` holds main-module packages' dirs, made relative to `manifest.PhysicalRoot()` or `ModuleRoot()`, whichever contains them, plus shadow dirs: packages whose `ImportPath` has prefix `modulePath+"/"` but which are `!Module.Main`, giving rel `strings.TrimPrefix`.
    - `EmbedFiles` holds the main-module packages' `EmbedFiles`, made relative.
    - It wraps `ErrWitnessUnavailable` when any package has a directory-replaced module (`Module.Replace != nil && Replace.Version == ""`, or `Version == "" && !Main`).
  - `func relativizeGraph(graph sourceview.Graph, goModDir string) (sourceview.Graph, error)` and `func rebaseGraph(graph sourceview.Graph, goModDir string) sourceview.Graph`. These rewrite only `Dir`, `Module.Dir` and `Module.GoMod` of `Module.Main` packages, and drop `Deps`.
  - `func lookupGraph(dir, key string, manifest *sourceview.Manifest, goModDir string) (sourceview.Graph, graphCacheOutcome)`
  - `func storeGraph(dir, key string, variant graphVariant) error`
  - `type graphVariant struct { Witness sourceview.GraphWitnessInput; Digest string; Graph sourceview.Graph /* relative */ }`
  - The on-disk form is JSON `{"version":1,"variants":[…]}` at `entryPath(filepath.Join(dir,"graph"), key)`. It is written by temp file plus rename, with the stored variant moved to the front and the list capped at 4.

- [ ] **Step 1: Write the failing tests.** These use synthetic graphs and manifests; there is no `go list`.
  - `TestGraphStoreRoundTripRebases`: store under root A, look up with a manifest for root B with the same content. It hits, and every main-module `Dir` is under B.
  - `TestGraphStoreVariantsMRU`: storing 5 variants keeps the newest 4, and a lookup matching any of them hits.
  - `TestGraphStoreCorruptIsMiss`: truncated JSON, garbage bytes and `{"version":0}` each give `graphEntryCorrupt`, with no panic.
  - `TestWitnessInputForGraph`: covers shadow dirs, `EmbedFiles`, and a mutable module giving `ErrWitnessUnavailable`.
  - `TestRebaseDropsDepsOnly`: no read field other than paths changes.
- [ ] **Step 2: Run** `go test ./gen -run 'TestGraphStore|TestWitnessInputForGraph|TestRebaseDropsDepsOnly' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement `gen/graphcache.go` and the `EmbedFiles` field.**
- [ ] **Step 4: Run the tests.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(gen): module-scoped go list graph store`.

### Task 6: Wire into `prepareCache`, report, and differential gate

**Files:**
- Modify: `gen/cachekey.go`: split `loadGraphWithContext` into `graphQueryPatterns` (already separate) and `listGraph(context, manifest, patterns) (sourceview.Graph, error)`.
- Modify: `gen/cache_pipeline.go`: add `loadGraphCached`.
- Modify: `gen/cache_report.go`: add `moduleCacheReport.Graph graphCacheOutcome`, and a verbose line.
- Test: `gen/graphcache_integration_test.go`

**Interfaces:**
- Consumes: Tasks 1, 4 and 5.
- Produces: `func loadGraphCached(prep cachePreparation, roots []string) (sourceview.Graph, graphCacheOutcome, error)`. Its flow:
  1. Compute the patterns and the key.
  2. `lookupGraph`.
  3. On a miss, run `listGraph`, then `witnessInputForGraph`, then `GraphWitness`. Store only when `DiskGraphWitness` equals it; otherwise the outcome is `graphStoreSkipped`.
  4. Uncacheable inputs fall through to `listGraph`.

  An error from `listGraph` behaves exactly as today (graph-query-failed bypass).
- Verbose output, appended to `verboseLines` once per module: `gsx cache: graph hit`, or `gsx cache: graph miss (<reason>[: <detail>])`.

- [ ] **Step 1: Write the failing differential test `TestGraphCacheDifferential`.**
  - **Fixture:** one fixture module with packages `a` (a `.go` file and a `.gsx` file), `b` (imported by `a`), `e` (with `//go:embed data.txt`), and a nested module `nested/`. Use a temp `GSXCACHE`.
  - **Rows:** each row applies an edit, then calls `loadGraphCached` twice. The first call stores; the second must either miss or return a graph `graphReadFieldsEqual` (`ImportPath`, `Dir`, `Imports`, `GoFiles`, `CompiledGoFiles`, `CgoFiles`, `EmbedFiles`, `Standard`, `Module`) to a live `listGraph` run at the same moment. Edits:
    - a body edit (must **hit**);
    - an import added, removed or reordered;
    - a build tag changed;
    - a file added, deleted or renamed, including `x_darwin.go`;
    - the package clause changed;
    - `a/go.mod` added;
    - go.mod edited (add a `require` of the in-tree nested module through a `replace` → uncacheable);
    - a new `.gsx` package;
    - an unparseable header;
    - a `.c` file added to `b`;
    - the embed target deleted (the live `listGraph` errors, so the cached call must miss, not hit);
    - the embed pattern edited.
  - Two spec rows can't run hermetically, and are pinned at unit level instead:
    - The ambiguous-import row needs a required, non-replaced module served by a proxy. Task 5 `TestWitnessInputForGraph` checks the shadow dir is derived, and Task 4's shadow-dir row checks that creating it changes the digest.
    - The `.swig` row needs the swig tool. Task 4's `a/x.c` row covers it, because the listing hashes every file name.
  - `TestGraphCacheSharedAcrossCheckouts`: copy the fixture to a second path, and expose it through a symlink as a third. A warm entry from the first hits at both, and equals the live `listGraph` there.
  - `TestGraphCacheHitReadsNothing`: after `Build`, `chmod 000` a body-only `.go` file. The lookup still hits.
  - `TestGraphCacheStoreSkippedWhenFilesChange`: use a test hook `var afterListGraph func()` in `loadGraphCached` that edits a header. The outcome is `graphStoreSkipped`, and the next run misses.
  - `TestGraphCacheUnwritableDir`: `GSXCACHE` points at a read-only dir. The graph equals the live one, and the outcome is `graphStoreFailed`.
- [ ] **Step 2: Run** `go test ./gen -run 'TestGraphCache' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement it.** Replace the `loadGraphWithContext` call in `prepareCache` with `loadGraphCached`, and record the outcome in the report. `loadGraph`/`loadGraphWithContext` used by tests keep their behaviour, through `listGraph`.
- [ ] **Step 4: Run the new tests, then the narrow regression clusters.** Commands:
  - `go test ./gen -run 'TestGraphCache|TestCache|TestPrepareCache|TestGenerate' -count=1`
  - `go test ./internal/corpus -count=1`

  Expected: PASS.
- [ ] **Step 5: Commit** `feat(gen): reuse the go list graph across warm runs`.

### Task 7: Docs, eos measurement, gate

**Files:**
- Modify: `CHANGELOG.md`, adding an `## Unreleased` entry of at most 4 lines: a warm `generate` skips `go list` when no package header, file set or `go.mod` changed, and `-v` shows `graph hit`/`graph miss (reason)`.
- Modify: `docs/guide/` CLI page (where `generate -v` cache lines are documented, if any). One sentence, kept concise.

- [ ] **Step 1: Run eos with `GOWORK=off` on an idle machine.** Build the branch binary and run `generate -v` three times. Expected: run 1 `graph miss (graph-entry-missing)`, then runs 2–3 `graph hit`. The `.x.go` shasums must be identical to v0.4.3. Record the median warm wall time against v0.4.3 (7 alternating runs, idle).
- [ ] **Step 2: Run** `make ci`. Expected: exit 0. Read the exit status itself, not just the tail of the output.
- [ ] **Step 3: Get an independent adversarial review** whose explicit brief is to construct a false hit (a graph that differs from a live `go list`, or a hit where `go list` fails). Fix confirmed findings with a differential row each.
- [ ] **Step 4: Commit** `docs: graph cache in CHANGELOG and CLI guide`.
