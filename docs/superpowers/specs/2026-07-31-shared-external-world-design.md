# Shared external world — one dependency load per process, not per Module

**Status:** Design. Load-split probe done and positive; not implemented.

**Date:** 2026-07-31

**Goal:** CI `build-test` from ~7m to ~4m, and the local suite from 181s toward
~120s, by removing a genuine algorithmic redundancy rather than by adding
runners.

Prior context: `docs/superpowers/specs/2026-07-30-test-suite-performance-design.md`
(277s → 181s already banked; batching test corpora and `t.Parallel` sweeps both
investigated and rejected there).

## The redundancy

`codegen.Open` performs one `packages.Load` per Module, and that load does two
different jobs at once (`module.go`, `externalImporter`):

1. **External types** — `github.com/gsxhq/gsx`, `github.com/gsxhq/gsx/std`,
   filter/renderer/class-merger packages, and their transitive stdlib closure.
   Result: `mp` → `externalBackedgeImporter`.
2. **Project inventory** — `./...` plus `manifest.LoadRoots()`, feeding
   `projectSourcePackages` and `externalBackedgePackages`.

Job 1 is **identical for every test Module in the repo**: all test modules
`replace github.com/gsxhq/gsx => <repo root>`, under one toolchain and one build
env. It is 85 packages / 667 files / **205,468 lines**, re-parsed and
re-type-checked **803 times per suite** (337 in `gen`, 466 in `internal/codegen`).

Measured shape of a load (batching probe, 2026-07-31): **~230ms fixed +
~1.3ms per additional package**. For a one-package test module, over 99% of the
load is job 1.

## Probe result — the split is achievable

Twelve one-package modules, each `replace`d to the repo root:

| | cost | shareable |
| --- | --- | --- |
| **A — today**: one load, full mode, `[gsx, gsx/std, ./...]` | **179ms/module** | no |
| **C — external only**: full mode, `[gsx, gsx/std]`, 85 pkgs | **182ms once** | **yes** |
| **B — project only**: `NeedName\|NeedCompiledGoFiles\|NeedSyntax\|NeedTypesSizes\|NeedModule`, `["./..."]` | **51ms/module** | no |

`C + N×B` vs `N×A` = **2.7× at n=12**, approaching **3.5×** as N grows.

Why B can drop `NeedTypes|NeedDeps`: codegen already re-type-checks project
packages itself. From `externalImporter`'s own doc comment — *"Semantic importers
never retain its module-local type packages: they re-check every local directory
from retained source in their own declaration universe and use this importer only
outside the module."* The dependency type-checking that job 1 performs for the
project's sake is therefore largely thrown away.

## Design

### Tier 1 — split the load (mechanical, low risk)

Two loads per Module instead of one:

- **External** — `[gsx, gsx/std, FilterPkgs, LoadPkgs, Aliases, Renderers,
  ClassMerger, PerDir…]` in full mode, into a process-wide cache.
- **Project** — `["./...", manifest.LoadRoots()…]` in reduced mode, per Module.

Cache key for the external half must cover everything that can change those
types: the resolved load-path set, the frozen build env (`GoCommandContext`'s
`buildEnv`), the toolchain identity (`Launcher.Digest()` + `CompilerIdentity()`,
both already computed), and a content digest of the resolved external module
sources. `gsx dev` edits the gsx runtime itself, so the last one is not optional.

**Open question, must be settled first:** `externalBackedgePackages` detects
external packages whose dependency graph re-enters the main module. That is
inherently module-specific, so it cannot live in the shared half. For the pure
runtime closure a back-edge is impossible (the gsx runtime cannot import
`example.com/testmod`), but a *user filter package inside the module* can create
one. Either keep such packages in the per-module half, or key the shared entry so
module-local filter packages never enter it.

Expected: 179 → 51ms per Module. Removes ~100s of the ~144s of summed load work.

### Tier 2 — shared in-process Bundle (removes the remaining 51ms)

`Options.Bundle` already runs codegen with **no `packages.Load` and no
subprocess** — the WASM playground depends on it. Its documented limitation is
that bundle `types.Package` values live in a foreign FileSet, so imported-object
positions do not resolve against `m.fset`, making it generation-only.

That limitation belongs to the *serialized typebundle*, not to bundle mode.
`harvestFromTypes(..., fset *token.FileSet)` already takes a real FileSet and
only accepts nil "when no real Fset is available at this call site (e.g. the
WASM/typebundle path)". A Bundle built from an in-process `packages.Load` over a
**shared, process-wide `token.FileSet`** carries real positions, so `Package()`,
hover and go-to-definition keep working.

The project inventory then comes from `sourceview.Manifest` (already built before
the load, and already the sole source in the playground's SourceOnly mode) rather
than from `projectSourcePackages(pkgs, …)`.

Expected: per-Module load cost → ~0.

### Blocker found 2026-07-31: FileSet ownership

The back-edge question resolved cleanly (below), but a second one did not, and it
changes the size of the work.

`rebuildFset` sets `m.fset = token.NewFileSet()` **and nils `m.ext`**, on purpose:
*"discards the grown FileSet and the caches that hold positions into it — ext,
pkgTypes, targetDeclTypes, and pkgResults — together, so nothing live references
the old fset (no orphaned positions)."*

So the external world is deliberately coupled to the Module's fset lifetime. And
`maybeRebuildFset` fires not only on growth but on **`m.sourceInventoryDirty`** —
i.e. on every source edit. Two consequences:

1. **The production win is larger than estimated.** `gsx dev` and the LSP
   currently discard and reload the entire 85-package / 205k-line closure on
   *every* edit-triggered regen, not just on cold start.
2. **A shared world cannot simply outlive `rebuildFset`.** Its `types.Package`
   values hold positions into the fset it was loaded with; reusing it after a
   rebuild orphans every imported-object position. That is a correctness hazard
   (wrong go-to-def targets), not a memory one.

The real fix is to **split FileSet ownership**: the shared external world owns a
stable, never-rebuilt fset (external deps do not change during a session), while
each Module keeps its own rebuildable fset for project packages. Every position
lookup then has to choose a fset by package path — `externalImportPaths` already
records exactly that set, so the routing is available, but the audit is
**61 `m.fset` sites and 84 `.Position(` calls across 14 files**.

**Blocker resolved 2026-07-31 — disjoint Pos ranges.** A probe confirms the two
FileSets can be given non-overlapping `Pos` ranges: reserve the shared world's
range up front with `shared.AddFile("gsx:shared-world-base", 1<<40, 0)`, then
`packages.Load` into it. Measured:

```
shared fset Base() after reservation: 1099511627777
external object "Attr" Pos=1099517625050   (>= sharedBase)
  shared.Position -> /…/attrs.go:16:6      (correct)
module fset Base()=4098, module Pos=21
disjoint? module max Pos (4097) < sharedBase: true
asking the module fset for a shared Pos -> "-"   (invalid, NOT a wrong file)
```

Two consequences that shrink the work:

- **Routing is a numeric range check, not a package-path lookup.** `positionOf(pos)`
  is `if pos >= sharedWorldBase { shared } else { m.fset }`. No need to know an
  object's package, so the 84 call sites become a mechanical substitution of
  `m.fset.Position(x)` → `m.position(x)`.
- **A missed site fails loudly.** Asking the wrong fset returns an invalid
  position rather than a plausible wrong one, so an incomplete audit surfaces as
  a test failure instead of silent corruption. This is what makes the full split
  safe to attempt.

A module would need ~1TB of source to collide with the reserved base.

**Narrower alternative (not taken; recorded for context).** `Options.Bundle` is already
documented as generation-only for precisely this reason. Sharing the external
world only on the `Generate` path — leaving `Package()`/LSP on today's path —
respects an existing sanctioned boundary, needs no position-routing audit, and
still captures the whole `gsx dev` regen win plus the `Generate`-side test win.
It does not help the LSP, which is where `gen`'s 118 LSP tests live.

### Risks

- **Shared FileSet growth.** One process-wide fset accumulating every external
  package is a retention change. `m.fsetBaseline` / `rebuildFset` exist because
  fset growth already matters here; a long-lived LSP process is the case to
  watch. (See `gsx-lsp-completion-shipped`: RSS was a real problem before.)
- **Invalidation correctness.** A stale shared world would silently generate
  against the wrong runtime types — worse than slow. This needs a test that
  mutates the gsx runtime between two Opens and asserts the second sees it.
- **Concurrency.** The cache is read by parallel analyses; `-race` over `gen` and
  `internal/codegen` is the gate, as it was for the digest cache.
- **Back-edges** (above) — the one design unknown that could shrink Tier 1.

## Expected outcome, stated honestly

Summed load work is ~144s of the suite (337×~179ms + 466×~179ms ≈ 144s, matching
the 104s + 115s measured directly per package).

- Tier 1: removes ~100s of it → local suite ~181s → **~150s**, CI ~7m → **~5m**.
- Tier 1+2: removes ~144s → local suite → **~120s**, CI → **~4m**.

CI is the harsher case and benefits more: `ubuntu-latest` is 4 vCPU, where `gen`
(373s) and `internal/codegen` (333s) currently contend for the same cores, so
removing work helps more than it does on a 32-core machine.

**This does not reach 2 minutes.** The residue is the `dev`/`watch` cluster —
66.5s of wall-clock waits that assert an *absence* ("nothing was posted", "the
loop gave up") and are not load-bound. Those need an injectable clock, which is a
separate piece of work with its own risk to what those tests actually prove.

## Verification plan

1. Land Tier 1 behind the existing `extLoads` counter; assert loads-per-Generate
   drops in a test.
2. Runtime-mutation test for invalidation (above) — write it first, watch it fail
   against a naive cache with no content digest.
3. `-race` over `gen` + `internal/codegen`.
4. `make ci` exit 0 unpiped, then re-time the three-way A/B on a quiet machine
   (check `uptime` first — a background process once tripled a measurement here).
