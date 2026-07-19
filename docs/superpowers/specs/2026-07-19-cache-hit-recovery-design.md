# Cached CLI No-Op Recovery and Optimisation

**Date:** 2026-07-19
**Status:** Approved design

## Goal

Make an unchanged `gsx generate` invocation take the persistent-cache hit path
on a large real application, with no semantic generation or type loading. Keep
cache invalidation exact, generated output byte-identical, and cache failures
observable instead of silently degrading into an expensive full generation.

The initial `one-learning-gsx` target is a second unchanged run at or below
1.5 seconds and 250 MB peak RSS on the development machine used for the
baseline.

## Evidence and problem statement

The migrated `one-learning-gsx` repository contains 116 `.gsx` files totalling
about 1.6 MB. Against current `gsx` main:

- a cold full generation took 4.53 seconds and about 4.41 GB peak RSS;
- a subsequent cache-enabled run took about 2.5-2.7 seconds and about 4.6 GB
  peak RSS;
- a direct `go list -deps -json -compiled ./...` took 1.12 seconds and about
  88 MB peak RSS;
- generated output remained byte-identical.

The cache-enabled number was initially mistaken for a cache hit. A fresh
`GSXCACHE` remained empty, and a GC trace showed the heap staying small during
metadata preparation before growing to roughly 4 GB when fallback generation
started. The current pipeline can therefore fail to compute usable keys and
silently regenerate everything. The exact current bypass reason is discarded
by `generateModule`, so the first implementation step must expose and pin that
reason before changing behaviour.

Runtime rendering is not the priority for this slice. Existing runtime
benchmarks show the common empty/root paths are already zero-allocation and in
the single-digit-nanosecond range.

## Constraints

- Cache correctness is more important than a hit. Unrepresentable state must
  miss or fail closed, never reuse stale output.
- No timestamp-, size-, or path-shape heuristics.
- `.x.go` files are outputs and transport overlays, not cache inputs.
- Preserve exact build-tag, cgo, module/workspace, local-replacement,
  configuration, toolchain, compiler, and generator identity.
- Preserve byte-identical generated output and existing default CLI output.
- The root runtime remains standard-library only.
- Do not change the warm `codegen.Module` architecture or revisit rejected
  runtime `Node` representation experiments.

## Architecture

`generateModule` remains the single-module coordinator but loses its mixed
cache responsibilities. Three unexported stages own distinct contracts.

### `prepareCache`

`prepareCache` freezes the source manifest and Go-command context, fingerprints
the effective toolchain/build environment, loads the selected package graph,
and constructs one immutable cache projection. It returns either a usable
preparation or a structured shared failure. Preparation errors are not silently
discarded.

The graph query is rooted at:

- the requested module-owned GSX directories;
- module-owned packages reached only through authored GSX imports;
- external packages reached only through authored GSX imports;
- configured filter, renderer, and class-merger packages; and
- `github.com/gsxhq/gsx`.

Each root is passed to `go list -deps`; the unconditional `./...` root is
removed. This still selects the full transitive Go dependency closure while
preventing unrelated packages in the same module from blocking or inflating a
scoped generation. Overlay-backed sentinel packages remain filesystem patterns
so cmd/go can select them.

Projection validation applies to the selected manifest package closure, not
every GSX package discovered elsewhere in the module.

### `classifyCache`

`classifyCache` computes the exact key for each requested directory and returns
one of three decisions:

- `hit`: a valid cache entry was decoded;
- `miss`: the key is valid but no usable entry exists; or
- `uncacheable`: exact inputs for this directory cannot be represented.

One directory's input error stays local. Only failure of the shared frozen Go
context or selected graph prevents all cache consumption.

The source digest includes the target package plus transitive active Go and
GSX-only dependencies, mutable local replacements, module provenance,
go.mod/go.sum inputs, build context, generator/binary identity, attribute
classifier identity, configured filters/renderers/class merger, and minify
configuration.

Standard-library packages and immutable versioned module packages are pinned
by toolchain/build identity and module provenance respectively. Their cgo files
do not require source hashing and must not make a directory uncacheable. A cgo
package owned by the main module or a mutable local replacement remains
uncacheable until gsx can represent its generated inputs exactly.

### `commitCache`

`commitCache` revalidates the frozen Go-command provenance immediately before
publishing any result. It then:

1. restores hits through the existing byte-comparison/atomic-rename path;
2. batches only misses and uncacheable directories through one `GenerateDirs`
   call; and
3. stores successful output only for directories with valid keys.

A full hit never opens a codegen `Module`, calls `GenerateDirs`, or performs a
semantic `packages.Load`.

## Observability

An unexported `cacheReport` records:

- preparation, classification, restore, and generation durations;
- hit, miss, and uncacheable counts;
- stable reason codes for shared and per-directory bypasses; and
- whether semantic generation ran.

Tests call the internal orchestration path and inspect this report directly.
Existing `-v` output prints a concise human-readable summary and bypass reasons.
Normal CLI output and the public Go API remain unchanged. No new environment
variable or debug flag is introduced.

## Failure semantics

- A missing cache entry is an ordinary miss.
- A corrupt or undecodable entry is a miss and is replaced after successful
  generation.
- A per-directory representation failure is an `uncacheable` miss with a
  reason; unrelated directories can still hit.
- A graph-selection failure, source-view inconsistency, or Go-command
  provenance change is shared and fail-closed. No cached bytes are restored and
  no entries are written under the stale preparation.
- Existing source/codegen diagnostics retain their current formatting,
  severity, and output behaviour.
- Best-effort cache-store write failures do not fail successful generation, but
  appear in verbose cache reporting.

## Verification

### Deterministic tests

Tests must prove:

- the first run seeds entries and the unchanged second run is a full hit;
- a full hit performs zero `GenerateDirs` calls and zero semantic loads;
- requested graph roots do not include unconditional `./...`;
- an unrelated broken or main-module cgo package cannot disable caching for a
  requested independent package;
- standard-library and immutable-module cgo dependencies remain cacheable;
- main-module and mutable-replacement cgo dependencies are explicitly
  uncacheable;
- corrupt entries regenerate and are replaced;
- one package's uncacheable state does not prevent another package from
  hitting; and
- GSX, Go, go.mod, go.sum, local replacement, build context, toolchain,
  generator, classifier, renderer/filter/class-merger, and minify changes
  invalidate the correct directories.

A realistic integration fixture includes multiple GSX packages, GSX-only
dependency edges, build tags, a local replacement, cgo in an unrelated package,
and a nested-module boundary. CI guards structural invariants and allocation
benchmarks; it does not enforce machine-specific wall-clock thresholds.

### Real-workload gate

Run the built feature binary against a clean clone of the current
`one-learning-gsx` branch:

1. use a fresh dedicated `GSXCACHE` and run generation once;
2. prove cache entries were created;
3. run unchanged generation again and capture verbose cache reporting;
4. prove all requested directories hit, semantic generation did not run, and
   `git diff` is empty;
5. measure wall time and peak RSS, targeting no more than 1.5 seconds and
   250 MB; and
6. repeat after one representative `.gsx` edit to prove only the correct
   dependency closure misses, then discard the clone.

Repository verification is `make check` during development, followed by
`make ci`, `make lint`, `git diff --check`, focused race tests for changed
concurrent code, and `gopls check -severity=hint` on changed Go files.

## Documentation and status

Update `docs/ROADMAP.md`'s realistic tooling-performance item with the measured
before/after results and the structural regression guard. This feature does not
change user-facing syntax or canonical guide content, so no sibling grammar or
website sync is required.

## Non-goals

- A persisted module snapshot or daemon protocol.
- Changes to watch/LSP warm-module behaviour.
- Runtime rendering optimisation.
- Cache eviction or storage-format redesign beyond what correctness requires.
- Making main-module or mutable-replacement cgo inputs cacheable through an
  approximate model.
- Public cache-reporting APIs.
