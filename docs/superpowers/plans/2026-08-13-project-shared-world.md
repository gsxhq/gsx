# Project-Scoped Shared World Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Configured modules (filters / renderers / aliases / class merger) reuse a process-cached shared world, eliminating the ~2s uncached closure load from every dev world reload — with generated output byte-identical.

**Architecture:** Spec `docs/superpowers/specs/2026-08-13-project-shared-world-design.md`. The world grows from "fixed gsx-runtime closure" to "runtime + the module's config packages", keyed by the path set; one type universe always; existing unaccounted-import and back-edge fallbacks preserved; a new back-edge guard covers main-module config packages.

**Tech Stack:** Go 1.26.1 (`GO_VERSION` in ci.yml).

## Global Constraints

- **Byte-identity invariant (the user's one condition): corpus goldens unchanged; `GSXCACHE=off gsx generate` over the gsxui clone stays all-up-to-date under the new binary.** Any task whose change could alter emitted bytes must state why it cannot.
- One type universe per Module: never introduce a second full-mode `packages.Load` beside the world. All loads go through `loadPackages` (the counter wrapper).
- Unsure → full load. Every new fallback records a visible reason (counter or world-entry field), never silent.
- Narrow tests only in the loop; `make ci` once at the end, exit code read directly. Module-opening tests ~0.3s each — extend existing fixtures where possible.
- Work in worktree `worktree-project-shared-world`. Never cd to the main checkout or live gsxui. Commit per task.
- The #178 review lessons bind: synthetic packages must carry Errors; fast-path eligibility must consult world coverage; vendor/ bypasses go.mod keys (already keyed via replace-dirs + vendor semantics — do not regress).

---

### Task 1: World composition replaces the boolean gate

**Files:**
- Modify: `internal/codegen/sharedworld.go` (`sharedWorldEligible` → composition; `loadExternalGraph` shared-path assembly)
- Test: `internal/codegen/sharedworld_composition_test.go` (new; pure — no `packages.Load`)

**Interfaces:**
- Produces: `func (m *Module) sharedWorldComposition() (paths []string, ok bool)` — ok=false ONLY for per-dir config variance (`PerDir` entries carrying their own ClassMerger/non-std FilterPkgs); otherwise paths = sorted unique {gsxRuntimeImportPath, stdImportPath} ∪ non-std FilterPkgs ∪ LoadPkgs ∪ alias packages ∪ `finalRendererAliases(o.Renderers)` packages ∪ ClassMerger package. `loadExternalGraph` consumes it: `ok==false` → `sharedWorldIneligible.Add(1)` + full load (unchanged behavior); otherwise sharedPaths = the composition and the project-half split excludes them.

- [ ] **Step 1: Failing test** — table over Options shapes: unconfigured → {runtime, std}; class merger `example.com/m/merge.Merge` → +`example.com/m/merge`; filter `github.com/jackielii/structpages.URLFor` → +structpages; renderer/alias/LoadPkgs each; per-dir merger → ok=false; std-only FilterPkgs → no extra. Derive each package path the same way the harvest does (grep how ClassMergerRef/FilterAlias/RendererAlias store their package paths — reuse those accessors, do not re-parse strings).
- [ ] **Step 2: RED** — `go test ./internal/codegen -run TestSharedWorldComposition -count=1` fails to compile (function absent).
- [ ] **Step 3: Implement** composition; rewrite `loadExternalGraph`'s `sharedPaths`/`shared` from it. Keep `sharedWorldKey` unchanged — it already hashes the path set, so composed worlds key correctly for free.
- [ ] **Step 4: GREEN** + `go test ./internal/codegen -run 'TestSharedWorld' -count=1 -parallel 4` (existing shared-world suite must still pass — unconfigured modules compose to exactly the old pair, so behavior is identical there).
- [ ] **Step 5: Commit** `feat(codegen): shared-world composition — config packages join the world path set`.

---

### Task 2: Config packages through the world build — harvest, retention, back-edge guard

The deep task. The world now loads packages that may live in the MAIN module (gsxui's `merge/`). Three sub-invariants, each with its own test:

**Files:**
- Modify: `internal/codegen/sharedworld.go` (`loadSharedWorld` back-edge guard, synthetic-entry construction), `internal/codegen/module.go` (`externalImporter` harvest path if retention needs it)
- Test: extend `internal/codegen/sharedworld_test.go` fixtures (grep for the existing world tests first; reuse their module fixtures where possible — Module opens are the cost unit)

**Interfaces:**
- Consumes: Task 1's composition.
- Produces: behavior only — a configured module takes the world path with: (a) config types served from the world universe (merger validation + filter/renderer harvest read `world.types`); (b) main-module config package dirs still retained as source packages (`projectSourcePackages` sees them — the project half's REDUCED load must include those dirs' patterns when they are main-module-local, since synthetic world entries carry no CompiledGoFiles for retention; verify against how `targetSourcePackage`/LSP nav use retention for the merger dir); (c) a world whose closure back-edges into main-module packages OUTSIDE the composed config set falls back to the full load with `sharedWorldBackedge.Add(1)`.

- [ ] **Step 1: Failing tests** (three, on one configured-module fixture — class merger in-module + structpages-style external filter):
  1. `TestSharedWorldServesConfiguredModule` — open Module, Generate a dir using the merger + filter; assert output bytes EQUAL a control Module forced down the full-load path (build the control by making composition return ok=false via a per-dir override, or by a test hook — prefer comparing against a pre-branch golden fixture if one exists). This is the byte-identity proof at unit scale.
  2. `TestSharedWorldRetainsMainModuleConfigDir` — after the world-path Generate, `targetSourcePackage(mergeDir)` (via its public consumer — gd/hover path or the existing test helper) still resolves the merger's syntax.
  3. `TestSharedWorldBackedgeFallsBack` — merger package importing another main-module package outside the config closure → full load taken (assert via `ProjectLoadCalls`/new `sharedWorldBackedge` counter), generation still correct.
- [ ] **Step 2: RED** for all three.
- [ ] **Step 3: Implement.** Study `loadSharedWorld` + `loadExternalGraph`'s synthetic-entry harvest and `externalBackedgePackages` FIRST; the back-edge guard runs on the world's closure at build time (world packages with `Module.Main` + path outside the composed set → record + fall back). Synthetic entries for config packages carry Errors AND enough identity for the harvest (mirror how runtime packages surface).
- [ ] **Step 4: GREEN** + the full shared-world cluster + `go test ./internal/codegen -run 'TestRenderer|TestClassMerger|TestFilter' -count=1 -parallel 4` (type-identity consumers) + `go test ./gen -run 'TestWatchSession_EditLoadBudget|TestWatchSession_ColdStartParseWorkIsLinear' -count=1`.
- [ ] **Step 5: Commit** `feat(codegen): configured modules ride the shared world — config types load once per process`.

---

### Task 3: Freshness + watch integration

**Files:**
- Modify: only if Step 1 finds gaps (expectation: none — stamps already cover all loaded files; goSourceReload → externalImporter → freshness re-check is existing machinery)
- Test: `gen/watch_sharedworld_test.go` (new; one watch-session fixture)

- [ ] **Step 1: Failing test** — configured-module watch session: (a) edit the MERGER package's `.go` → next cycle regenerates with fresh merger behavior (assert output reflects the change — e.g. merger switches from joining with space to comma; corpus-style assertion on emitted class attr); (b) edit an UNRELATED `.go` → world NOT rebuilt (SharedWorldLoads counter delta 0; cycle stays ~1-load); (c) `.gsx` edit → no world activity.
- [ ] **Step 2: RED only if broken** — if (a)–(c) pass immediately, the machinery composes as designed; keep the test as the regression gate and say so in the report (no fix needed).
- [ ] **Step 3–4:** Fix gaps if any; GREEN; run `go test ./gen -run 'TestWatch' -count=1 -parallel 4`.
- [ ] **Step 5: Commit** `test(gen): pin shared-world freshness under watch cycles`.

---

### Task 4: Counters + gates

**Files:**
- Modify: `internal/codegen/sharedworld.go` (add `SharedWorldLoads`, `SharedWorldHits`, `sharedWorldBackedge` counters, exported accessors in the `ProjectLoadCalls` idiom)
- Test: extend `gen/watch_perf_test.go`

- [ ] **Step 1:** Gate test `TestWatchSession_ConfiguredModuleWorldBudget` (non-parallel, min-of-two): configured fixture; cold start = 1 world load; `.go`-edit cycle = 0 world loads + the existing 1 project load; second process-simulated Module open (same process) = 0 world loads (hit).
- [ ] **Step 2–4:** RED (counters absent) → implement → GREEN + existing budget gates.
- [ ] **Step 5: Commit** `test(gen): pin world-load budgets for configured modules`.

---

### Task 5: gsxui A/B + byte-identity sweep + docs

- [ ] **Step 1:** In a scratchpad `git clone --local` of gsxui (NEVER the live checkout; kill only recorded pids): `GSXCACHE=off <new-binary> generate` → MUST report all up to date (byte identity at scale — the user's gate). Then dev A/B vs main: cold start, narrow `.go` cycle, wide `.go` cycle, go.mod reopen. Expected from the measurement: ~1.0–1.3s off load-bound cycles; report honest numbers.
- [ ] **Step 2:** `docs/ROADMAP.md` one-line update (Phase 2a shipped); spec Status flip. No guide changes (internal perf).
- [ ] **Step 3: Commit** `docs: project shared world — roadmap + status`.

---

### Task 6: Adversarial review + gates + PR

- [ ] **Step 1:** Workflow adversarial review (lenses: type-universe identity under config worlds; main-module-edit staleness; back-edge shapes incl. vendor + replace; multi-world process memory; byte-identity). Fix confirmed findings via the wave protocol.
- [ ] **Step 2:** `make ci` + `make lint`, exit codes read directly.
- [ ] **Step 3:** PR with the A/B table + byte-identity evidence; merge only on a green board.
