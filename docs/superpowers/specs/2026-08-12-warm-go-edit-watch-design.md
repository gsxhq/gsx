# Warm `.go`-edit handling in watch sessions

**Date:** 2026-08-12
**Status:** Approved (brainstormed with Jackie; approach A chosen)
**Prior art:** PR #184 (batched refresh + incremental manifest derivation), PR #178 (shared external world), `docs/superpowers/specs/2026-07-31-shared-external-world-design.md`

## Problem

The dev watch loop classifies **every** authored `.go` byte-change as a
dependency-surface change (`isDepFile`, `gen/watch.go`) and answers with
`reopen()`: discard every warm Module, redo the full `packages.Load`, and
regenerate all dirs. On gsxui (1,169 `.gsx` / 78 dirs) that is ~4.6s per `.go`
save after #184 — and the cost scales with module size, while gsx is being
adopted by ever-bigger projects. The Module layer already distinguishes
body-only Go changes (warm) from genuine graph changes
(`sourceview.ReloadReasonFor`: membership / package clause / import outside
the published world → atomic importer rebuild at next analysis); the LSP's
watched-file path lives on that machinery today. The dev loop predates it.

## Goals

- A body-only `.go` edit costs what a `.gsx` edit costs: refresh + invalidate
  the owning dir, regenerate its dependent closure warm. No `packages.Load`.
- Genuine graph changes escalate **once**, decided by the Module — never by
  the watch loop, never from fsnotify op kinds — and the reason is visible.
- Design point: 10× gsxui (~10k files / ~800 dirs). Warm-path cost scales
  with the edited package's dependent closure, never with module size.
  Gates pin operation counts (Inspect, packages.Load), not wall-clock.
- Correctness bar: unsure → escalate. Stale generated output is never an
  acceptable trade.

## Non-goals

- Keeping `.go` file creates/deletes warm (Phase 1 escalates them; the
  retained cold load's compiled-file selection cannot absorb membership
  changes without new machinery — revisit only after the verdict API exists).
- Parallel/incremental per-dir generation (the 100× lever; separate project).
- Any change to batch `gsx generate` or the on-disk cache.

## Approach (chosen: A — Module-decided verdict)

Rejected alternatives: (B) watch-side classification — duplicates the
classifier outside the Module, two authorities that drift; (C) optimistic
warm with output-diff fallback — "looks wrong" is a heuristic, and staleness
would be the discovery mechanism.

### Phase 1 — warm `.go` path with verdicts

**Verdict API (internal/codegen).** `RefreshDiskSourcesAndInvalidate` grows
an explicit verdict in its return:

```go
type RefreshVerdict struct {
    WorldReloadPending bool
    Reason sourceview.ReloadReason // ReloadNone when warm
    Path   string                  // file that forced the reload
}
```

computed from the existing `ReloadReasonFor` classification plus one new
comparison: per-dir authored-`.go` membership diffed against the **published
cold world's** per-dir selection — not the rolling manifest, because the
reload-pending state must persist across further refreshes until the world
rebuild actually lands (the same published-baseline discipline
`sourceReloadReasons` already applies to `.gsx` facts). A `.go` file
appearing or vanishing (or a `.gsx` membership/package/import change per the
existing rules) escalates.
Internal API; LSP call sites update mechanically and gain the verdict for
free. The verdict is observational: analysis already self-escalates at the
next `Generate` — the watch session never orchestrates the rebuild, it only
learns that this cycle will pay for one, and why.

**Watch routing (gen/watch.go, gen/watchsession.go).** `isDepFile` shrinks
to `go.mod`/`go.sum`. Authored `.go` writes become ordinary source events:
queue the owning dir into the pending set exactly like `.gsx` edits. No
fsnotify-op guessing: creates/deletes are caught by the Module's membership
diff. `regenPending` refreshes pending dirs (batched, #184 per-dir error
isolation preserved), regenerates the affected reverse closure warm
(`Dependents()` walks through go-only dirs — verified), and collects
verdicts. Structural events (dir create/delete) and `go.mod`/`go.sum` keep
today's session-level reopen.

**Preserved invariant.** Any authored-`.go` event still sets `goChanged`, so
the server binary rebuilds and restarts even when zero `.x.go` bytes change.
Signature-level `.go` edits propagate because dependents re-type-check from
current source after invalidation; hash-gated writes drop no-op disk churn.

**Observability (chosen: console + panel).** The verdict reason threads into
the `generated`/status events and one console line, e.g.
`full reload: new import "x/y" in merge/merge.go`. Panel display is a
`vite-plugin-gsx` bump (sibling repo).

### Phase 2 — cheap escalation

**Measure first.** Instrument where the 4.6s reopen goes on gsxui
(main-module `packages.Load` vs external closure vs regeneration). #178's
shared external world may already cover part of dev's cost in-process; the
phase's exact shape depends on the split.

**Core.** Cache the external (non-main-module) world across Module
generations in-process, keyed exactly like #178: go.mod/go.sum content plus
resolved replace-dir targets. An escalated importer rebuild (or reopen)
reloads only main-module packages against the cached external importer.
`go.mod`/`go.sum` edits change the key and rebuild the external world — the
one case that legitimately pays full price. **Named risk:** vendor/ bypasses
go.mod keys (found in the #178 adversarial review); the cache key must
account for vendor mode/content before any reuse.

**Gate.** Escalated reload work scales with main-module package count only;
the external closure is never re-loaded while its key is unchanged. Asserted
via load counters, not wall-clock.

## Error handling

- Unsure → escalate; never stale.
- Refresh failures: #184's per-dir fallback isolates the culprit dir.
- Escalated-rebuild failures inside `Generate`: today's diagnostics/poison
  path, unchanged.
- `go.mod` parse failures: today's visible reopen-failure behavior, unchanged.

## Testing

- **Verdict truth table** on one shared Module fixture (rows are ~free,
  module opens are not): body edit / import add within published world /
  import add outside it / package-clause change / `.go` create / `.go`
  delete / `.gsx` membership change.
- **Watch integration:** body edit regenerates dependents with **zero**
  `packages.Load` (new counter hook, same pattern as
  `sourceview.InspectCalls`); import-add escalates exactly once; `.go`
  create escalates; `goChanged` still rebuilds the server; signature edit
  regenerates dependents with changed bytes.
- **Complexity gates** at the design point: operation-count assertions in
  the `TestWatchSession_ColdStartParseWorkIsLinear` style (min-of-two on
  breach, non-parallel).
- **Real-world A/B** on a gsxui clone recorded in each PR.
- **Independent adversarial review** with live probes before each merge.

## Sequencing

1. Phase 1 PR: verdict API + watch routing + observability + fmt/lint/ci
   gates + vite-plugin-gsx panel bump.
2. Phase 2 PR: external-world reuse across generations (after measurement).
3. Docs dev-guide + ROADMAP updates ride the PRs.
