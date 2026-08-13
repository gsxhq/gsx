# Project-scoped shared world (Phase 2a of warm dev cycles)

**Date:** 2026-08-13
**Status:** Implemented (pending review + CI). Design derived from the Phase-2 measurement.
**Prior art:** PR #178 (shared external world), PR #186 (warm `.go`-edit watch cycles), `docs/superpowers/specs/2026-08-12-warm-go-edit-watch-design.md`, the Phase-2 measurement (2026-08-12: one reload = 436 pkgs / 2.0–2.35s flat; gsxui disqualified from the shared world outright).

## Problem

`sharedWorldEligible` (internal/codegen/sharedworld.go) disqualifies any Module
configured with filters, renderers, aliases, or a class merger, because those
packages are harvested **from the loaded types** and the reduced project half
carries no types. Real projects always configure at least a class merger —
gsxui's `class_merger = gsxui/merge.Merge` plus the structpages `url` filter
mean every world reload is a full uncached 436-package load (~2.0–2.35s),
every `.go` edit cycle, every escalation, every dev cold start. The #178
shared world helps only unconfigured modules — mostly tests.

## Non-negotiable invariant

**Generated output is byte-identical.** The corpus goldens do not change; a
full `GSXCACHE=off gsx generate` over gsxui reports everything up to date
under the new path. This is an explicit acceptance gate, not a hope.

## Goals

- A configured module (filters / renderers / aliases / class merger) reuses a
  process-cached world across Module generations: dev world reloads and cold
  starts stop paying the runtime + std + config closure (~1.0–1.3s/cycle on
  gsxui per the measurement's isolated probe; re-measure at the end).
- One type universe, always. No second full-mode load beside the world —
  cross-universe `*types.Package` identity is the failure mode this design
  exists to avoid (renderer/merger type matching is pointer-identity based).
- Correctness fallbacks unchanged: the unaccounted-import check and back-edge
  rejection keep falling back to the single full load. Unsure → full load.

## Non-goals

- The generate phase (type-check + emit over the closure; dominates wide
  cycles at ~10s) — separate, larger project.
- Caching packages the CONFIGURATION does not name — a `.gsx` load root or a Go
  import reaching outside the configured closure. This was briefly implemented
  as an extension tier and then descoped; a module that needs such types takes
  the single full load, and the phase's job is to make that refusal free. See
  "Extension tier, descoped".
- Any change to `gsx generate` batch semantics or the on-disk cache.

## Design

**World composition.** The shared world for a Module becomes the runtime
closure PLUS the module's resolved OUT-OF-MODULE config packages: `FilterPkgs`
(beyond std), `LoadPkgs`, alias packages, renderer packages, and the
class-merger package, minus any of those that live in the main module itself
(see "the hard case, dissolved"). One `packages.Load` builds it; one universe
serves all types.

**Keying.** `sharedWorldKey` hashes the composed path set, the build env, the
toolchain identity, and an ORIGIN describing how this root resolves modules.
The origin is every resolution directive of go.mod — the `go` and `toolchain`
lines, every require, every replace (filesystem targets resolved to absolute
paths), every exclude — plus the whole go.sum. Not a filtered subset: the
adversarial review probed a version bump of a DEPENDENCY of a composed package
and found the world stayed keyed and stayed fresh while serving types the
compiler no longer agreed with, emitting different `.x.go` bytes depending only
on cache warmth. The main module's own `module` line is the one directive left
out — no world holds main-module code, so the name a root gives itself cannot
change a world's contents, and including it would end cross-root sharing
entirely.

Two situations forfeit content keying and bind the key to the module root,
losing sharing but never correctness: a Go workspace, and vendoring. Vendored
packages resolve out of `vendor/`, which go.mod does not constrain — two
worktrees of one vendored project have byte-identical go.mod files and may hold
different code. The review probed it and it emitted the wrong bytes; PR #178's
commit message claimed this guard, but it was never in the tree.

**Cache lifetime.** One cache, unbounded and never evicted, keyed as above:
that set follows CONFIGURATION, which changes when gsx.toml or go.mod does, so
a CLI or test process holds one or two entries and a long-lived LSP one per
open project.

Only HEALTHY worlds are published. A world whose load produced any
error-carrying package, or any module-owned package with no compiled files, is
served — its errors must surface loudly, as #178 established — but not cached.
Fileless brokenness (build constraints excluding every file, an empty or
not-yet-created package dir) is the shape that forced this: go/packages returns
a non-nil EMPTY types.Package for it and there is nothing to stamp, so
`fresh()` can never fail and no key component moves when the developer fixes
the file. The review wedged both a config package and a dependency that way,
permanently, in a process that would otherwise have healed on the next cycle.
Not caching costs one reload per cycle while broken — the pre-shared-world cost
for exactly the broken shape — and the first healthy cycle caches normally.

**Main-module config packages — the hard case, dissolved.** *(Revised after the
Task-5 A/B; this section previously argued the opposite and the reversal is the
point.)* gsxui's merger lives in the main module. The original plan composed it
into the world and accepted the consequence: the world stamps every file it
loads, so editing the merger marked the world stale — "rare, correct, and
self-healing". Measurement disagreed with "rare": the class merger is a routine
edit target, and once the world also had an extension tier, one merger edit
rebuilt BOTH tiers — 5171ms → 6001ms, +16% against main, on the very cycle the
phase exists to speed up.

The rebuild bought nothing. A module-local config package's types NEVER come
from the world: `configuredSourcePackages` routes module-local paths to the
source resolver, and `externalImporter` drops every local path from the
published importer. The world's copy was a load-set member and a set of file
stamps, nothing more.

So **main-module code never enters a shared world.** `sharedWorldComposition`
drops any config path under the module's own import path (by prefix, erring
toward exclusion), and the extension tier composes what the project half
references, so the merger's own dependencies (tailwind-merge-go,
csscolorparser) still arrive in the one universe. Three consequences:

- **Freshness for config behavior rides retained source.** A merger edit is an
  ordinary main-module `.go` edit: the project half reloads, the merger is
  re-read and re-type-checked from source, the rebuilt program renders the new
  behavior. No world reload, no world-side staleness rule at all.
- **Retention is the type authority**, and always was — which is why the
  retention pin needed no change when this landed. `./...` covers the merger's
  dir in the reduced project half; that syntax is what both the merger's own
  type resolution and LSP nav read.
- **The back-edge guard gets simpler and stays load-bearing.** With no
  legitimate main-module package in any world, the rule is flat: a world
  carrying code owned by the module being served got it as a DEPENDENCY of an
  external package — the one-way boundary `externalBackedgePackages` rejects on
  the full load — and that Module falls back to the full load, which is where
  the hard configuration error is produced. The exemption-plus-reachability
  pair the composed-merger design needed is gone with the case that required
  it.

**Synthetic-entry harvest.** Unchanged in shape: the project half stays a
reduced load; world packages surface as synthetic entries carrying their
Errors (the #178 review lesson — a broken runtime/config package must fail
loudly on the fast path). Retention of main-module dirs needs nothing from the
world: the project half loads `./...`, which covers every main-module dir
whether or not it is named in the configuration.

**Extension tier, descoped.** Between the Task-5 rerun and the adversarial
review, this design also composed a SECOND world tier from the packages the
project half referenced but the configuration did not name — gsxui's `.gsx`
import of `github.com/gsxhq/vite`, a dependency imported only from Go files, a
nested module named as a load root. It worked, it was fast, and it is gone.

The reason is not any single bug but the identity model: a tier keyed on the
project's import set cannot capture what determines its contents. The review
confirmed the consequences — an entry keyed that way needs an eviction policy
to stay bounded, and the policy could delete another root's config world
("Extension-seat eviction can delete another module's live CONFIG-TIER
world"); a sibling module inside the tier made every sibling edit rebuild the
whole tier, +17–20% against the pre-phase load ("Sibling-edit cycles cost more
than the pre-branch full load"); and the same key was blind to how its members
resolved ("Stale world served after go.mod resolution change of a dep-of-
composed module"). Each has a fix; together they say the tier needs its own
design phase, with these findings as its inputs, rather than a patch inside
this one.

What remains is the CONFIG world, whose membership is declared rather than
inferred, plus a rule that refuses everything else early — see below. The
honest consequence for the flagship case: gsxui imports `vite` from a `.gsx`,
so it does not ride the world at all. It pays exactly the single full-mode load
it paid before this phase, and measures at parity (see gate 4). The projects
this phase does speed up are those whose external surface is what their
configuration names — every corpus fixture, every test module, and any project
whose `.gsx` files import only the stdlib and their own packages.

**Eligibility.** `sharedWorldEligible` is no longer a boolean gate on config
presence: `sharedWorldComposition` returns the world path set, and a Module is
refused only when it cannot be served. Three refusals, in the order they can be
decided, each on its own counter:

- **From configuration alone, before any load** (`SharedWorldIneligibleModules`):
  per-dir config variance wants a world per directory, which this phase does
  not support; and an empty composition, which happens only when the module
  being built IS the gsx runtime.
- **From the load paths, before any load** (`SharedWorldPreloadFallbacks`): a
  manifest load root outside the configured closure. This is the rule that
  replaced the extension tier, and deciding it up front is what makes the
  refusal cost exactly the one full-mode load the pre-phase code paid. Load
  roots that LOOK standard-library (no dot in the first path element) are
  admitted here, because the world carries the stdlib the runtime closure
  reaches and no pre-load test knows which — a `.gsx` importing "strings" must
  stay on the fast path.
- **From the project half's references, after both loads**
  (`SharedWorldCoverageFallbacks`): a Go file importing a package outside the
  closure, or a dependency that came back without types. Nothing sees these
  until the module is loaded, so reaching this verdict costs three loads. The
  Module therefore REMEMBERS it: a dev loop re-runs this on every `.go` edit,
  and the second cycle is back to one load. The latch is conservative — a
  module that becomes servable again keeps taking the full load until it is
  reopened, which config and go.mod changes do anyway.

A back-edging config package (`SharedWorldBackedgeFallbacks`) is refused the
same way, and latched for the same reason.

## Acceptance gates

1. Corpus suite untouched: no golden churn (`make ci` corpus job), and
   `GSXCACHE=off go tool gsx generate` over the gsxui clone reports all up
   to date with the new binary.
2. `TestWatchSession_EditLoadBudget` budgets hold; a new gate pins that a
   CONFIGURED module (class merger + non-std filter fixture) takes the
   shared-world path: second Module open in a process performs 0 world
   loads (counter: extend `ProjectLoadCalls` discrimination or add
   `SharedWorldLoads`/`SharedWorldHits` counters).
3. Type-identity proof: a renderer/merger fixture whose registered type is
   matched in a `.gsx` expression must behave identically on the fast path —
   the #178-era equivalence tests extended to configured modules.
4. A/B on gsxui: dev cold start and `.go`-edit cycles re-measured; expected
   ~1.0–1.3s/cycle off the load share (narrow cycle ≈ 2.0s → ~1s; wide and
   go.mod reopen keep their generate-dominated floor). Honest numbers in the
   PR whatever they turn out to be.

   **Measured (Task 5, 2026-08-13):** byte-identity holds
   (`GSXCACHE=off generate` over gsxui: 1169 up to date, zero writes,
   `git status` clean). gsxui itself never reaches the fast path: `document.gsx`
   directly imports `github.com/gsxhq/vite`, a manifest LoadRoot outside the
   composed {runtime, std, structpages, merge} closure — the exact
   unaccounted-LoadRoot fallback this design's own Non-goals section names as
   intentional. But the fallback is not cost-neutral as designed: it now pays
   for the shared-world load AND the reduced project-half load before falling
   through to the same full load as before (`ProjectLoadCalls` 3 vs. 1 on the
   pre-Phase-2a path), a real regression, not a wash. Dev-loop A/B (2 samples
   main, 4 samples this branch, `--no-web` event-sink method) on gsxui: narrow
   `.go` edit 2626ms → 3109ms (+18%), wide (`merge/merge.go`) 5175ms → 6116ms
   (+18%), go.mod touch 4940ms → 5546ms (+12%); cold start ~6.27s either way
   (parity — build time dominates). All three edit cycles regress >10%. See
   `.superpowers/sdd/2026-08-13-project-shared-world/task-5-report.md` for the
   full methodology and per-sample numbers.
   **Final measurement (2026-08-13, after the extension tier was descoped):**
   byte identity holds — `GSXCACHE=off generate` over gsxui reports 1169 up to
   date, zero files written, clean `git status`. gsxui does not ride the world
   (`preload=1 loads=0 projectLoadCalls=1`): its `.gsx` import of
   `github.com/gsxhq/vite` is outside the configured closure, so it takes the
   single full-mode load, refused before anything else loads. Parity is
   therefore the expected result, and is what was measured — dev-loop A/B, 2
   samples per binary interleaved in one session:

   | cycle | main | this branch | delta |
   |---|---:|---:|---:|
   | cold start | 6255ms | 5596ms | −659 (−10.5%) |
   | narrow (`site/examples/kbd.go`) | 2564ms | 2581ms | +18 (+0.7%) |
   | wide (`merge/merge.go`) | 5130ms | 5125ms | −4 (−0.1%) |
   | go.mod touch | 4946ms | 4882ms | −64 (−1.3%) |

   The three edit cycles are parity to within noise, which is the honest claim:
   gsxui pays what it always paid. The cold-start delta is inside the ±1.5s
   Go-build-cache variance band every session of this measurement has shown and
   should not be read as a win.

   Where the phase does pay is a process that opens many Modules over one
   configuration. `internal/corpus`, measured on this branch against `origin/main`
   (7c1f2897), twice each: **19.4s → 12.8s (−34%)**. (An earlier report quoted
   30.0s → 12.5s; the 30.0s baseline was this branch mid-work, not main. The
   number above is the honest comparison.)

5. Adversarial review with live probes before merge — **done, 8 findings
   confirmed (3 critical).** Resolved here: vendored cross-root aliasing (root-
   bound origin), stale worlds after a resolution change of a composed
   package's dependency (whole-resolution origin), and fileless-broken packages
   cached and served forever (unhealthy worlds are not published). Mooted by
   descoping the extension tier: cross-tier eviction of a config world,
   sibling-edit cycle economics, and dep-churn key thrash. The remaining
   findings are inputs to the extension tier's own design phase, recorded in
   `.superpowers/sdd/2026-08-13-project-shared-world/adversarial-findings.md`.

   Still open for a future probe, now that main-module code never composes: the
   coverage counter's documentation once claimed to catch broken dependencies
   that in practice reach the fast path instead — with unhealthy worlds no
   longer cached the wedge is closed, but the classification is worth
   re-probing under the descoped design.

## Sequencing

One PR. Tasks: (1) world-composition eligibility + keying; (2) config
packages in the world build + synthetic harvest + retention; (3) back-edge
guard on the world path; (4) gates (counters + configured-module fixtures +
type-identity equivalence); (5) gsxui A/B + docs; (6) adversarial review +
`make ci`.
