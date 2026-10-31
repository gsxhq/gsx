# `gsx dev` Fast Start Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the time from `gsx dev` start to a healthy server bounded by the build, not by gsx, with no change to what is generated, written, reported or served.

**Architecture:** The watch session's initial generate restores cache hits from the persistent generate cache and runs only misses through its Module. Merger validation and a full warm-up of the Module move to background goroutines that every Module-touching loop path waits on. `gsx dev` starts the configured build speculatively right after arming, and uses its result only if the initial generate wrote and removed nothing.

**Tech Stack:** Go 1.27.1 (repo pin); existing `gen` cache pipeline (`prepareCache`, `classifyCache`, `restore`); `codegen.Module`.

**Spec:** `docs/superpowers/specs/2026-10-31-dev-fast-start-design.md`

## Global Constraints

- **Unchanged output.** What is generated, written, reported and served must not change: diagnostics, written/removed files, overlay and panel events.
- **No stale binaries.** Never serve a binary built from `.x.go` that changed after the build started.
- **No cache stores from the watch session.** `gsx dev` and `generate --watch` never store to the generate cache.
- **Merger failures stay fatal.** A bad class merger exits 1 with today's error text. In `gsx dev`, the server's `run` is never started. In `generate --watch`, startup results are not emitted before validation passes.
- **The build starts only after `armWatchSession` returns.**
- **Edit cycles wait for warm-up.** Every loop path that uses the session Module waits for the warm-up to finish: edit cycles, the panel `rebuild` command, reopen and `.env` restarts. Ctrl-C/`ctx` and `restart-server` do not wait.
- **The warm-up posts and emits nothing** unless it writes or removes a file. If it does, it is handled exactly like an edit cycle.
- **Applies to both modes.** These rules apply to `gsx dev` and `gsx generate --watch` alike: they share `watchSession`.
- **Test cost.** No new test may open more `codegen.Module`s than necessary. Extend existing fixtures and tables. Watch `TestWatchSession_ColdStartParseWorkIsLinear` and `TestWatchSession_ConfiguredModuleWorldBudget`.

## Review Focus

- **Disabled cache.** With `GSXCACHE=off`, or an uncacheable Go context such as an active `GOWORK`, startup must behave exactly as today, with every dir a miss (Task 2 test row).
- **Error at startup.** A dir with a codegen error is never a cache hit. Its poison `.x.go` and error overlay must survive the warm-up (Task 4 test).
- **Edit during the speculative build.** A `.go` edit saved while the speculative build runs must lead to a fresh build, never the speculative binary (Task 3 test).
- **Ctrl-C mid-startup.** Ctrl-C during the speculative build or the warm-up exits promptly, with no orphaned build process (Task 3 and Task 4 tests).
- **Multi-module sessions.** A watch session spanning two module roots classifies, restores and warms each module with its own config (Task 2 test row).

---

### Task 1: Background merger validation

**Files:**
- Modify: `gen/watchsession.go` (`prepareWatchSession`, `watchSession`)
- Modify: `gen/watch.go` (the `generate --watch` loop waits before emitting startup results)
- Test: `gen/watchsession_test.go` (update `TestWatchSession_BadMergerRefused`)

**Interfaces:**
- Produces:
  - `func (s *watchSession) startMergerValidation()`: starts the existing `ValidateConfiguredMergers` (and its `annotateModuleError` wrapping) once, in a goroutine. It is a no-op when `s.configs[s.root].classMerger == nil`.
  - `func (s *watchSession) awaitMergerValidation() error`: blocks until that goroutine finishes and returns its error. It returns nil immediately if validation was never needed. It is safe to call more than once.
- `prepareWatchSession` no longer validates synchronously. `armWatchSession` calls `startMergerValidation()` just before it returns.

- [ ] **Step 1: Update the bad-merger test.** `TestWatchSession_BadMergerRefused` now asserts that `prepareWatchSession` succeeds, and that `startMergerValidation()` followed by `awaitMergerValidation()` returns an error whose text equals the text the test asserts today. Add a row: with no merger, `awaitMergerValidation()` returns nil without blocking.
- [ ] **Step 2: Run** `go test ./gen -run 'TestWatchSession_BadMergerRefused' -count=1`. Expected: FAIL (undefined methods).
- [ ] **Step 3: Implement the two methods.** Use a `sync.Once` start, and a done channel plus a stored error. Wire them into `armWatchSession`. In `gen/watch.go`, call `awaitMergerValidation()` before `em.cycleBatch(...)` for startup, and on error emit the error and return 1, as `initialGenerate` errors do today.
- [ ] **Step 4: Run** the test above plus `go test ./gen -run 'TestWatch|TestWatchSession_ConfiguredModuleWorldBudget' -count=1`. Expected: PASS. The world budget is unchanged, because validation still shares the Module's world.
- [ ] **Step 5: Commit** `refactor(gen): validate class mergers in the background`.

### Task 2: Cached initial generate

**Files:**
- Modify: `gen/cache_pipeline.go`. Extract the hit-restore half of `commitCache` into a shared helper; `commitCache` calls it, and its behaviour is byte-identical.
- Modify: `gen/watchsession.go` (`initialGenerate`)
- Test: `gen/watchsession_test.go`

**Interfaces:**
- Consumes: `prepareCache`, `classifyCache`, `restore`, `absWarnings`, `groupByModule`, and the session's `s.configs` / `moduleConfig`.
- Produces:
  - `func restoreCacheHits(prep cachePreparation, classification cacheClassification) (hits map[string]restoredDir, err error)` in `gen/cache_pipeline.go`, where `type restoredDir struct { written []string; upToDate int; diags []diag.Diagnostic }`. `commitCache` uses it in place of its inline loop.
  - The session field `s.warmDirs []string` holds the dirs restored from the cache at startup. Task 4 consumes it.
- `initialGenerate`'s new flow:
  1. Run `sweepOrphanStartup` exactly as today.
  2. For each module group, run `prepareCache` + `classifyCache` with that module's `moduleGenerateConfig`.
  3. Turn each hit into a `cycleResult{Dir, Written, Diags, OK: true, DurMs}` via `restoreCacheHits`.
  4. Run `regenDirs` on misses and uncacheable dirs exactly as today. There is no `storeGeneratedOutputs`.
  5. Return results in the same per-dir order today's `initialGenerate` uses.
- A `prepareCache` or classify error for a module falls back to treating all of its dirs as misses, which is today's path.

- [ ] **Step 1: Write the failing equivalence test `TestWatchSessionStartup_CachedMatchesUncached`.**
  - Fixture, one module: three GSX dirs (one emitting a warning such as `child-element-spacing`), one dir whose only `.gsx` was deleted (an orphan), and one dir edited after a warming `gsx generate` (a miss).
  - Warm the cache with `generateCachedWithReport`, then make the edit.
  - Compare startup results from two fresh sessions, one with `GSXCACHE=off` and one with the warm cache, minus `DurMs`. They must be `reflect.DeepEqual`, and the files on disk must be byte-identical.
  - Rows:
    - a two-module session (Review Focus);
    - an uncacheable context (set `GOWORK` to a temp `go.work`): every dir goes through `regenDirs`;
    - a codegen-error dir: never a hit, with the same result as uncached.
  - Assert `s.warmDirs` equals the set of hit dirs.
- [ ] **Step 2: Run** `go test ./gen -run 'TestWatchSessionStartup_CachedMatchesUncached' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement `restoreCacheHits` and the new `initialGenerate` flow.** Keep `commitCache`'s observable behaviour identical; `go test ./gen -run 'TestCache|TestGenerate' -count=1` must stay green.
- [ ] **Step 4: Run** the new test, `go test ./gen -run 'TestWatchSession|TestWatchSessionStartup|TestCache|TestGenerate' -count=1` and `go test ./gen -run 'TestWatchSession_ColdStartParseWorkIsLinear' -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `perf(gen): restore cache hits at watch startup`.

### Task 3: Speculative build in `gsx dev`

**Files:**
- Modify: `gen/devserver.go`. Split `rebuild` into `build(ctx) (string, error)` and the existing `restartNoBuild`. `rebuild` calls both and is unchanged for every caller.
- Modify: `gen/dev.go` (startup sequence)
- Test: `gen/dev_test.go`

**Interfaces:**
- Consumes: Task 1 `awaitMergerValidation`, Task 2's startup results.
- Produces: `func (d *devServer) build(ctx context.Context) (string, error)`. It runs the build command with today's dir, env and output wiring, and writes a build failure exactly as `rebuild` does.
- New startup sequence in `runDev`:
  1. `armWatchSession`.
  2. Start the speculative build in a goroutine, with a child context `specCtx` derived from `ctx`.
  3. `initialGenerate`.
  4. If `startOK` is false, cancel `specCtx`, wait for the goroutine to exit, and run no initial build (today's behaviour).
  5. Otherwise, if any startup result has a non-empty `Written` or `Removed`, cancel `specCtx`, wait, then run `srv.rebuild(ctx)` as today.
  6. Otherwise, wait for the speculative result. A build error is posted with `buildErrorEvent` exactly as today, without a second build.
  7. On success, `awaitMergerValidation()`. An error is printed as today and exits 1, and `run` is never started.
  8. Then `restartNoBuild`, `setPhase("starting")`, `waitHealthy`, as today.
  9. The phase is `"building"` from the speculative start until build completion.

- [ ] **Step 1: Write the failing test `TestDevSpeculativeBuild`.** Extend an existing dev fixture. Its `build` command appends a line to a counter file, then compiles. Its `run` command writes a "started" marker. Rows:
  - (a) Up-to-date tree: the counter is 1 and the server starts.
  - (b) A `.gsx` edited after the cache was warmed: the initial generate writes, the speculative build is cancelled, and the counter is 2 with only the second build completing.
  - (c) A codegen error at startup: the server never starts, and no build error is posted.
  - (d) The build command fails on an up-to-date tree: exactly one build-error event is posted.
  - (e) A bad merger: exit 1, and the "started" marker is absent.
  - (f) Review Focus: a `.go` edit written during the speculative build leads to a later rebuild cycle, and the served binary includes the edit (check a value the server prints).
  - (g) Review Focus: Ctrl-C during the speculative build exits promptly, and no build process is left behind (`pgrep` of the fixture's build marker).
- [ ] **Step 2: Run** `go test ./gen -run 'TestDevSpeculativeBuild' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement the split and the new startup sequence.**
- [ ] **Step 4: Run** `go test ./gen -run 'TestDev' -count=1`. Expected: PASS. If `TestDevStopsPostingAfterWebExit` or `TestDevPanelCommands` fail, first rerun them alone on a quiet machine; they are load-sensitive.
- [ ] **Step 5: Commit** `perf(gen): start the dev build alongside startup generation`.

### Task 4: Background warm-up

**Files:**
- Modify: `gen/watchsession.go`
- Modify: `gen/dev.go` (the loop waits for warm-up)
- Modify: `gen/watch.go` (the loop waits for warm-up)
- Test: `gen/dev_test.go`, `gen/watchsession_test.go`

**Interfaces:**
- Consumes: Task 2 `s.warmDirs`.
- Produces:
  - `func (s *watchSession) startWarmup()`: once, in a goroutine, runs `regenDirs(s.warmDirs)` and stores the results. It is a no-op when `warmDirs` is empty.
  - `func (s *watchSession) awaitWarmup() []cycleResult`: blocks until the warm-up finishes and returns its results the first time. Later calls return nil.
- `gsx dev` calls `startWarmup()` once startup has finished: after `waitHealthy` returns, or when startup ends without a server. `generate --watch` calls it after emitting the startup batch.
- Every loop path that touches the session calls `awaitWarmup()` first: `cycle(...)`, the panel `rebuild` command, `reopen`, `.env` restarts, and `--watch`'s `dirty.regenerate`.
  - If the returned results contain any `Written` or `Removed`, they are merged into that cycle's results, so that cycle reports them and rebuilds.
  - Otherwise they are dropped silently.
- The `ctx.Done()` and `restart-server` cases never wait.

- [ ] **Step 1: Write the failing tests.**
  - `TestWatchSession_WarmupThenSharedEdit`: start from a warm cache, so every dir hits. Start the warm-up, edit a shared component's `.gsx` while it runs (use a test hook that blocks the warm-up), and release it. The edit cycle regenerates every importer of the component.
  - `TestDevWarmupIsSilent`, on the dev fixture:
    - with an up-to-date tree, no `generated` overlay or reload event is posted after `idle`;
    - with a startup codegen error (Review Focus), the error overlay is still present after the warm-up completes, and the poison `.x.go` is unchanged.
  - `TestDevCtrlCDuringWarmup` (Review Focus): SIGINT while the warm-up is blocked by the hook. Dev exits within 2s.
- [ ] **Step 2: Run** `go test ./gen -run 'TestWatchSession_WarmupThenSharedEdit|TestDevWarmupIsSilent|TestDevCtrlCDuringWarmup' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement `startWarmup` and `awaitWarmup`, and wire every listed loop path.** The test hook is a package-level `var warmupHook func()`, called at the start of the warm-up goroutine.
- [ ] **Step 4: Run** the new tests plus `go test ./gen -run 'TestDev|TestWatch' -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `perf(gen): warm the dev Module in the background`.

### Task 5: Docs, eos measurement, gate

**Files:**
- Modify: `CHANGELOG.md`: add an `## Unreleased` bullet of at most 4 lines. `gsx dev` restores unchanged dirs from the generate cache and starts the build alongside startup generation. The first edit after start may wait for a background warm-up.
- Modify: `docs/guide/dev-loop.md`: one sentence, only if it describes the startup order.

- [ ] **Step 1: Measure on eos.** Create a fresh eos linked worktree (`git -C ~/work/eos worktree add …`), point its `go.mod` gsx dependency at this branch with a temporary `replace` (not committed), run `task dev` once to provision, stop it, then measure `task dev` restarts.
  - Measure time from command to `/healthz` on an idle machine. Take 3 runs each with v0.4.4 and with this branch, and record the medians.
  - Confirm the `.x.go` shasums are identical.
  - Tear down with `task dev:stop`, `go run ./cmd/worktree rm <name>`, `git worktree remove` and branch delete.
- [ ] **Step 2: Run** `make ci`. Expected: exit 0. Check the exit status itself.
- [ ] **Step 3: Independent adversarial review.** Brief the reviewer to break the Global Constraints: a stale binary served, startup results differing from uncached, an edit lost or applied without its dependents, a bad merger not fatal, or a deadlock.
- [ ] **Step 4: Commit** `docs: gsx dev fast start in CHANGELOG`.
