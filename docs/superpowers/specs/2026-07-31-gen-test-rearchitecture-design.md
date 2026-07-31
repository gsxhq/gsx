# gen test rearchitecture — kill waiting, warm the analyzers, stop re-spawning

**Status:** Design, adversarially verified (6 evidence readers + 4 skeptics, every
seam checked against real code, key numbers independently reproduced). Not
implemented.

**Date:** 2026-07-31

**Goal:** `gen` is the suite's wall (171s of the 181s local suite; 373s of the
380s CI test step). This plan takes the local suite to **~90s** and CI
`build-test` to **~4.5–5.5min**. It also states plainly what it can NOT do: 2min
CI is unreachable on the free 4-vCPU runner (arithmetic below).

Prior context: `2026-07-30-test-suite-performance-design.md` (277→181s banked),
`2026-07-31-shared-external-world-design.md` (codegen −46%, WIP branch).

## The model (verified twice)

`gen` wall at `-parallel 4` = serial_sum + parallel_sum/4 = 132 + 118/4 ≈ 162s —
reproduced independently by a skeptic from raw test2json (131.7 + 118.5/4) and by
a fresh run (168.7s, within 4%). The dev cluster alone is **84.9s wall at 1.30
cores average utilization** — idle-dominated, which is why no amount of load
removal moved it.

On CI the same package decomposes as ~15% machine-invariant idle (sleeps run at
the same absolute speed everywhere) and ~85% CPU-limited work priced at ~2.8×
local (slower cores × 4-vCPU contention). This classification decides what helps
where: **CPU cuts help CI unconditionally; idle cuts help ~1:1 because they sit
on gen's single-threaded serial chain; overlap plays (parallelism, splits) help
the 32-core machine and do little on a saturated runner.**

## The plan — seven work items, amended by the skeptic panel

### W1. Build the gsx test binary once (trivial, do first)

14 sites in `dev_test.go` each run `go build -o <t.TempDir()>/gsx ./cmd/gsx`
(~0.59s warm each, link-dominated — links are never build-cached). All 14 builds
are byte-identical (verified: no -tags/-ldflags/env mutation at any site). Build
once from TestMain into a TestMain-owned dir (never a test's TempDir).
**−7.7s local serial; −13–25s CI (contended links); fewer concurrent `go build`s
= less build-cache-corruption exposure.**

### W2. Virtualize dev-loop delays at the two existing seams

- `devcmd.go:38`: the `sleep` closure is already the single choke point for every
  `pollCommands` wait (gate-down poll, transport/non-200/malformed backoffs at
  :57/:69/:86/:97). Hoist to a parameter; a recording fake advances through
  sleeps and asserts the exact backoff schedule (1s,2s,4s) — a *stronger*
  assertion than today's elapsed-time windows.
- `frontdoor.go`: `restartPolicy` (0.5/2/5s) + rapid/verify windows + the 250ms
  verify poll become `frontDoor` fields defaulted in `newFrontDoor`; tests
  construct with millisecond values. The rapid-exit window must scale with the
  delays.
- `dev_test.go:1476`: the fixture's `[dev].build = "sleep 2"` (runs twice)
  becomes `sleep 1`; assertions `>=2000ms` become `>=1000`.

**This is not a pure parameter hoist** (skeptic finding): six test-side
real-time windows must be rewritten as attempt-count bounds, and two tests become
tautologies under naive porting. Keep as real-time smoke: one default-closure
ctx-awareness test, `TestFrontDoorRestartsAndVerifies`' real verify round-trip,
and — untouched — the three child-process quiesce sleeps (`dev_test.go:929/
1377/2004`): they drain *spawned gsx binaries* whose `postBest` retries
(devserver.go:516, an unjoined goroutine) no in-process clock can reach.

**−18..25s serial (the reader's 49/23/26 buckets overlap; the union, not the
sum, is the budget), ~1:1 on CI.**

### W3. Shared warm-analyzer fixture families for read-only LSP e2e tests

The biggest CI lever, so it runs early (a skeptic re-ordered it). The LSP family
is 108–115 tests averaging 0.59s, each fully cold: fresh temp module → fresh
`lspAnalyzer` → cold Module open, per test, even for a pure hover query. The
injection seam already exists: `lsp.NewServer(in, out, analyzer)` is used by
`lsp_rename_e2e_test.go:197` and `lsp_close_e2e_test.go:90` today, and nearly
every e2e test passes a zero-value `config{}`.

Build 3–5 canonical fixture modules once (TestMain/sync.Once), one warm analyzer
each; convert the ~85–90 read-only definition/hover/completion/symbol tests to
parallel queries against them. Fold `definition_crosspkg`'s ~8 per-test
`Generate()` pre-passes into the pre-generated fixtures. The ~30–35 mutating
tests (override transfer, watched files, config reload, didClose, disk poking)
keep private instances.

**Mandatory guards** (each maps to a demonstrated leak vector):
1. Every `didOpen` pairs with `didClose` — 22 of 25 e2e files never close, and
   analyzer buffer overrides are path-keyed and survive server exit; leakage
   manifests as **silent wrong-passes** (disk reads served from a leaked
   identical buffer), not flakes.
2. `chmod a-w` the fixture trees + post-suite checksum against a build-time
   manifest.
3. Parallel tests on one fixture open disjoint files, or operate buffer-only.
4. `TestDefinitionSurvivesFsetRebuild` keeps a private analyzer —
   `GSX_FSET_REBUILD_BYTES` is frozen at Module open, so on a shared analyzer it
   would assert nothing.

**Elapsed cut ~32–36s → local wall −18..22s; ~70 CPU-s off CI (the largest
single CI item).** Lands best after W5 (warm analyzer construction currently
pays the go-env capture cost W5 memoizes).

### W4. Demote compile-only goBuild assertions to in-process type-checks

11 `goBuild` sites (not 10): gen_test ×3, poison_e2e ×4, multimodule ×2,
orphan_e2e ×2, plus `goBuildExpectFail`. None of the fixtures use cgo, embed,
build tags, or meaningful linking (orphan_e2e:101 builds a module with zero
packages — vacuous today). Replace the compile-only ones with a checker that
**enumerates and parses the real on-disk .go files** (including `.x.go`) and
type-checks against the shared-world importer.

Two traps the panel found: the checker must NOT reuse the analyzer's module
machinery — `module_importer.go:1331` deliberately replaces on-disk `.x.go` with
synthetic skeletons, which would make poison assertions pass vacuously. And keep
real builds at: `poison_e2e:69` (asserts compiler-output `//line` redirection —
a UX contract only a real compiler exercises), `gen_test.go:95` (one clean-build
smoke), and `goBuildExpectFail` (asserts failure shape).

**−5s local, −10–20s CI, and removes the corruption class that sank the
previous t.Parallel sweep.**

### W5. Process-wide frozen-Go-environment memo (the 899-spawn fix)

Every `CaptureGoCommandContext` spawns exactly 3 `go env`
(go_overlay.go:235/257/275); gen makes ~290 captures per run; a warm capture
still costs 54ms of pure spawn. `go env -changed -json` is byte-identical across
module dirs (verified twice, independently).

Memoize `freezeGoCommandEnvironment`'s outputs. The panel found four holes in my
draft key and the corrected key is load-bearing:

- **Full-environment hash** (not an allowlist — HOME/XDG redirect the GOENV path;
  demonstrated live), minus a provable output-only denylist.
- **GOENV resolved path + content hash** (content, not mtime).
- **go.work content hash** — go.work *content* changes toolchain selection with
  an unchanged path (demonstrated live); without this the memo would silently
  mask today's loud "toolchain switching is not supported" error
  (go_overlay.go:283).
- **Launcher path + digest** (digest alone lets `RequireLocalToolchain` pass for
  a byte-identical copy at a new path).
- Hoist the `gopackagesdriver` PATH check out of the memoized region; never
  memoize error results; `SealToolchain` and vendor-dir detection stay
  per-capture.

**Explicit-decision entry required** (same treatment as the digest cache): this
memo composes with the launcher digest cache into a blind spot neither has alone
— a stealth mtime-restored toolchain rewrite today self-corrects at the next
capture's live `go` execution; with the memo it does not. Optional near-closure:
fold ctime into `digestEntry.fresh`. Mutation tests first: bump go.mod
`toolchain` directive between captures; change GOENV content; edit go.work —
each must force a re-freeze or a loud error.

Scope honesty: only in-process captures are serviceable (~820 of the ~880
spawns); captures inside spawned gsx child binaries are unreachable.
**−8..12s + fork-contention relief; also removes ~54ms from every warm `gsx
generate`/LSP analysis in production.**

### W6. Cache location becomes an option (GSXCACHE stops forcing serial)

`GSXCACHE` is read at exactly one production site (`cachestore.go:23`,
`os.Getenv` inside `cacheDir()`), despite the repo's option > env > config
doctrine. Thread `cacheDir`/`cacheOff` through `moduleGenerateConfig` into
`prepareCache` — **and through the runMain path too** (main_test.go ×8 and
configfile_e2e ×2 Setenv sites; without that leg the estimate shrinks by a
third). Decide precedence explicitly: an explicit option beats `GSXCACHE=off`
(main_testmain_test.go's comment updates accordingly). Keep the env path
covered: the cachestore unit tests and one end-to-end warm-hit test stay on env.

Unlocks `t.Parallel` for ~14.7s of Setenv-serial cache tests including all 13
`TestRealisticCacheInvalidation` subtests. **−10s serial.**

### W7. Targeted t.Parallel, last, with a real taint scanner

After W2–W6 shrink and de-risk the pool: AST-based, call-graph-transitive taint
scan (the previous regex attempt attributed a fixture string-literal's
`t.Setenv` to the wrong function), with the blocker list extended beyond
env/chdir to: spawning `go` (shared build cache), `net.Listen`/port literals,
and time-window assertions. Fixed-port dev e2e tests stay serial; the six
fixed-port tests (7811/7813/7821/7823/7825/7829) migrate to the existing
`freePort(t)` pattern regardless. **Net −8..12s** (the LSP share is already
counted in W3 — do not double-credit).

## Dropped, with reasons (do not re-litigate without new evidence)

- **internal/devloop production extraction — refuted.** `runDev` needs core's
  unexported `config`/`tomlDev`; gen must import the new package for dispatch
  (main.go:242) while it needs ~10 watch-session internals → import cycle; watch
  is forced to move too, and watch needs `pkgOutput`/`anyErrorDiag`/
  `writeDirOutcome` from the cache layer (the do-not-extract zone, a true
  two-way cycle with core), and `writeDirOutcome` takes `*gen.Result` — a public
  type consumed cross-module by playground/server. Not effort-M, not
  doctrine-compatible.
- **LSP e2e tests-only package — refuted.** The e2e surface is only 5 unexported
  symbols (`runLSP` ×61, `newLSPAnalyzer`, `lspAnalyzer`, `config`, `formatGsx`),
  but exporting `runLSP`/`newLSPAnalyzer` is test-only surface leaking into gen's
  public API. W3 captures the same win without it.
- **Package split in general:** post-W1–W7 gen lands at/below codegen's own
  wall, so splitting moves the local suite ≈ 0 and CI marginally (bounded by
  codegen underneath). Re-evaluate only if gen becomes the long pole again —
  and then as a cohesion argument (frontdoor/devcmd/devstatus), not a
  test-speed line item.
- **Fixture copy-per-case for cache tests:** measured no-op — the cost is the
  generate calls, not the 14 file writes; and `CacheFingerprint` folds `GOMOD`
  (an absolute path) into the key, so identical content at two paths can never
  share entries anyway.
- **Forking x/tools' 3-`go list`-per-Load shape:** ~0.8s, disproportionate.
- **Revisiting `-parallel 4` because "P3 removes spawns":** wrong spawn type —
  the cap exists for `go list` core-saturation, which W5 does not touch.
  Re-measure 6/8 only after W3; expected ≤13s local, 0 CI.

## Production warts surfaced (flag, don't silently fix)

1. **`GOMOD` in the cache key** — two checkouts of the same project never share
   a cache entry. Candidate fix: delete GOMOD from `canonicalGoEnvironment`
   (go_overlay.go:348) — go.mod *content* is already hashed as a cache input.
   Changes persistent cache-key semantics; needs its own decision.
2. **`postBest` is an unjoined goroutine** (devserver.go:516) — no clean
   shutdown join in production either; a WaitGroup seam is ordinary hygiene.
3. **`runDev` traps SIGINT process-wide with no ctx/stop seam** (dev.go:113) —
   blocks any future in-process dev-loop testing; worth a seam on its own
   merits.

## End state, honestly

| | today | after W1–W7 |
| --- | --- | --- |
| gen local wall | ~171s | **~85–95s** |
| suite local wall | ~181s | **~90s** (floor becomes codegen ~78s) |
| CI build-test | ~7min | **~4.5–5.5min** (arithmetic floor ~4.3min) |

The CI arithmetic (skeptic-derived, calibrated against CI run 30616881562):
post-plan CPU demand ≈ 870 CI-CPU-s → 218s test-step at impossible 100% packing,
250–270s realistic, + ~55s fixed job overhead. **3min CI requires an 8-vCPU
runner** (paid, even for public repos) **or further corpus/codegen CPU cuts;
2min requires both.** The remaining CI budget the plan does not touch: corpus
93s and internal/lsp 71s CI, running under the long pole.

Prerequisite: land the shared-external-world branch (2 position-routing failures
outstanding) — W3's importer and codegen's CI number both depend on it.

Sequencing: **W1 → W2 → W5 → W3 → W4/W6 → W7**, with W7's candidate list
re-derived after W3. W2's seams live inside the files any hypothetical future
extraction would move wholesale — zero rework risk in either direction.

## Verification doctrine for this plan

Every W-item ships its own falsifier, in the repo's break-the-code tradition:
W2's fake sleep asserts the exact backoff schedule (stronger than elapsed
windows); W3's guards are tested by deliberately leaking a buffer override and
asserting the harness catches it; W4's checker must fail on a poisoned `.x.go`
(prove it isn't reading skeletons); W5 ships the three mutation tests before the
memo; W6 keeps env-path coverage. The previous rounds produced two guard tests
that each caught real bugs before merge — that pattern is the reason this plan's
numbers are trustworthy at all.
