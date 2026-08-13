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
- ~~Caching manifest LoadRoots (packages imported only from `.gsx`) beyond what
  the config set already covers.~~ **Revised (Task 5 rerun, 2026-08-13):** this
  non-goal was the reason the phase served nobody real. gsxui's
  `document.gsx` imports `github.com/gsxhq/vite`, so its every reload took the
  fallback — and the fallback was not cost-neutral, it was three
  `packages.Load` calls where the pre-phase code took one. The world now covers
  what the project references; see "Extending the world" below.
- Any change to `gsx generate` batch semantics or the on-disk cache.

## Design

**World composition.** The shared world for a Module becomes the runtime
closure PLUS the module's resolved config packages: `FilterPkgs` (beyond
std), `LoadPkgs`, alias packages, renderer packages, and the class-merger
package. One `packages.Load` builds it; one universe serves all types.

**Keying.** `sharedWorldKey` already hashes the load-path set, build env,
toolchain, and origin (go.mod/go.sum content + resolved replace-dirs). The
config packages join the path set, so two modules with different config get
different worlds, and a config change (gsx.toml edit) naturally re-keys. The
process cache may hold multiple worlds (it is already keyed); memory stays
bounded by the number of distinct (module-config) pairs in a process — in
practice one for dev, a handful for tests.

**Main-module config packages (the hard case).** gsxui's merger lives in the
main module. The world's freshness stamps (file + dir stamps) already cover
every loaded file, so an edit to the merger package — or anything in its
loaded closure — marks the world stale and rebuilds it on next use: rare,
correct, and self-healing. Two guards make this sound rather than hopeful:
- **Back-edges:** a config package that imports other main-module packages
  drags them into the world. That is exactly the shape
  `externalBackedgePackages` exists to reject — the world build must run the
  same back-edge detection the full load runs, and a world whose closure
  back-edges into main-module packages OUTSIDE the config closure falls back
  to the full load (permanently for that key; record why in the world entry
  so the fallback is visible, not silent). A config package whose closure
  stays within itself + external deps (gsxui/merge → tailwind-merge-go,
  csscolorparser) is servable.
- **Staleness discipline:** the watch session's `.go`-edit path must treat a
  world-stale verdict like any world reload — the existing goSourceReload
  machinery already forces the next analysis through `externalImporter`,
  which re-checks world freshness. No new watch-side logic.

**Synthetic-entry harvest.** Unchanged in shape: the project half stays a
reduced load; world packages surface as synthetic entries carrying their
Errors (the #178 review lesson — a broken runtime/config package must fail
loudly on the fast path). Config packages' synthetic entries additionally
carry their Module identity where `projectSourcePackages` needs to retain
main-module members (the merger's own dir must remain a retained source
package for the LSP/nav features — verify against `logicalProjectPath`
handling; if retention requires the full project-half view of that dir, the
project half's path set must include the config package dirs too, in reduced
mode, with the types still served by the world).

**Extending the world (added in the Task-5 rerun).** Configuration is not the
only thing a project needs types for. The project half is loaded WITHOUT types,
so every package it references from outside the main module must come from the
world: `.gsx` load roots (gsxui's `vite`), dependencies imported only from the
module's Go files, nested modules named as load roots. The world is therefore
composed in two tiers.

- **Tier one — the config world** ({runtime, std} + config packages). Identical
  for every module with the same configuration, so it is the entry a whole
  process shares. A `.gsx` importing `strings` needs nothing more: composing
  such paths as roots would mint a distinct world per distinct import set while
  loading byte-identical contents.
- **Tier two — the extension** (tier one + the references tier one does not
  carry). Its key is a pure function of (config paths, tier-one contents,
  project references), so a body edit never moves it; only adding or removing
  an import of a package the config world lacks does. That churn is rare, and
  self-healing when it happens.

Order matters: the project half loads FIRST. It is the cheap load and the only
thing that can say what the world must cover; deciding that after the world
load — the original order — made every unservable module pay for a world it was
about to discard.

Two rules keep this honest. Import paths are compared in RESOLVED form: the
`Imports` map is keyed as written, and the stdlib's own vendored dependencies
resolve to `vendor/…` paths, so the written form made every project whose load
roots include `net/http` unservable. And the coverage checks stay as a safety
net for packages that come back from the world without types.

**Eligibility rewrite.** `sharedWorldEligible` stops being a boolean
gate on config presence and becomes the world-composition function: it
returns the world path set. Modules remain INELIGIBLE only where the world
cannot be composed at all (per-dir config variance — `PerDir` overrides
with distinct mergers/filters produce per-dir worlds; keep those on the full
load in this phase and record the reason).

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
   **Re-measured after the fix (Task-5 rerun, 2026-08-13):** gsxui now takes
   the fast path (`fast=1 fellback=0`) and byte identity still holds (1169 up
   to date, zero writes, clean `git status`). Dev-loop A/B, 2 samples each,
   same event-sink method: narrow `.go` edit 2614ms → 2431ms (**−7.0%**),
   go.mod touch 4959ms → 4935ms (parity), cold start 6190ms → 6105ms (parity).
   The across-the-board 12–18% regression is gone and the narrow cycle is now
   faster than main — but by ~180ms, not the ~1.0–1.3s this gate projected:
   that estimate came from an isolated COLD load probe, while a warm dev
   process's full load is far cheaper than 2s, so the cacheable share is
   correspondingly smaller.

   One cycle still regresses: editing the class merger — a main-module config
   package — costs 5171ms → 6001ms (**+16%**), because the merger's files are
   stamped into BOTH tiers, so both rebuild (measured: 2 world loads for a
   merger edit, 0 for any other `.go` edit). The merger's own types never come
   from the world (module-local config packages resolve from retained source),
   so the fix is to stop composing main-module config packages into the world
   at all and let the extension tier cover their dependencies — now possible
   only because the extension tier exists. That reverses this document's
   "Main-module config packages (the hard case)" decision and changes two
   pinned test contracts, so it is proposed, not taken.
5. Adversarial review with live probes before merge (staleness of
   main-module config edits, back-edge shapes, multi-world processes,
   vendor). **The fallback-path regression Task 5 found is fixed (see gate 4);
   what remains for review is the merger-edit cycle above.**

## Sequencing

One PR. Tasks: (1) world-composition eligibility + keying; (2) config
packages in the world build + synthetic harvest + retention; (3) back-edge
guard on the world path; (4) gates (counters + configured-module fixtures +
type-identity equivalence); (5) gsxui A/B + docs; (6) adversarial review +
`make ci`.
