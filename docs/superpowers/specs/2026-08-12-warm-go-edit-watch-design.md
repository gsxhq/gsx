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
adopted by ever-bigger projects. The Module layer already classifies `.gsx` fact changes
(`sourceview.ReloadReasonFor`: membership / package clause / import outside
the published world → atomic importer rebuild at next analysis), and Go
*override* transitions (editor buffers) mark `goSourceReload` for the same
in-place rebuild. **Probe-verified premise correction (2026-08-12):** the
*disk* refresh path marks neither — a `.go` disk edit routed through
`RefreshDiskSourcesAndInvalidate` leaves warm analysis serving stale types
(removing an exported symbol produced no diagnostic in a dependent's warm
regen). Today's coarse `reopen` is therefore load-bearing for correctness,
and the LSP watched-files flow has a latent staleness bug for `.go` changes
with no open buffer. This design fixes that bug and replaces the reopen with
an in-place, Module-decided reload.

## Goals

- A `.go` edit keeps the session and its Modules warm: refresh + invalidate
  the owning dir, regenerate **only its dependent closure**, and pay exactly
  one in-place world reload inside that cycle — no session teardown, no
  rediscovery, no regenerate-all. Expected on gsxui: ~4.6s → ~2.5–3s
  (verified by A/B). **Superseded by Phase 3 (shipped):** a body edit to an
  already-compiled file now pays *no* reload at all — the retained syntax is
  re-parsed and swapped in place, and the cycle issues zero `packages.Load`
  calls. See `2026-08-12-incremental-saved-go-watch-design.md`. The
  "one in-place reload" figure below is now the cost of a transition the warm
  tier REFUSES (membership, cgo, build constraints, a new import), not of an
  ordinary save.
- Fix the latent LSP watched-files staleness end to end: disk `.go` changes
  must mark the world reload exactly as override transitions already do, and
  must actually *reach* that marking through the LSP — the watched-file
  registration, the relevance filter, and `RefreshDisk`'s classification all
  carry `.go` (paired generated `.x.go` excluded).
- Whether the world is stale is decided by the Module — never by the watch
  loop, never from fsnotify op kinds — and the reason is visible. WHICH
  Module gets asked is the loop's, since a Module can only ever speak for its
  own root: an authored `.go` file inside a nested module that another
  session module consumes through a `replace` is escalated to the reopen by
  the loop, because no single Module can observe it.
- Design point: 10× gsxui (~10k files / ~800 dirs). Warm-path cost scales
  with the edited package's dependent closure, never with module size.
  Gates pin operation counts (Inspect, packages.Load), not wall-clock.
- Correctness bar: unsure → escalate. Stale generated output is never an
  acceptable trade — see **Known limits** for the two shapes this design does
  not reach (one at pre-branch parity, one a knowing narrowing).

## Non-goals

- Keeping `.go` file creates/deletes warm (Phase 1 escalates them; the
  retained cold load's compiled-file selection cannot absorb membership
  changes without new machinery — revisit only after the verdict API exists).
  Phase 3 kept this non-goal: cmd/go remains the authority on source
  selection, so a membership change still reloads.
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

computed from the existing `ReloadReasonFor` classification plus the Go rule
this design adds to the disk path: **any change to authored `.go` content or
membership in a refreshed dir marks `goSourceReload`** — exactly what the
override path already does at `module.go:443-445` — with gsx-owned paired
`.x.go` outputs excluded from the comparison (the session's own generated
writes must never trigger a reload storm). The pending state persists across
further refreshes until the in-place rebuild lands (the discipline
`goSourceReload`/`sourceReloadReasons` already implement). In Phase 1 every
authored-`.go` content change therefore verdicts reload-pending; the verdict
earns its keep through closure-scoped regeneration, observability, the LSP
staleness fix, and as Phase 2's hook point.
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

**Nested-module escalation.** So does an authored `.go` file whose owning
module is not the session module that CONTAINS it — the shape a go.mod
`replace example.com/x => ./x` produces. The in-place path refreshes only the
Module rooted at the file's nearest `go.mod`, i.e. the nested one, which
nothing generates against, while the consuming outer module structurally
cannot observe the file (`sourceview` stops at every nested `go.mod`, and
`RefreshDiskSourcesAndInvalidate` rejects a dir the outer root does not own).
Escalating on containment keeps the reopen these edits always paid; sibling
module roots are independent and stay warm, and `.gsx` edits inside a nested
module stay warm too (`watchSession.goEditNeedsReopen`).

The same predicate consults `sourceview.OwnsDir`, whose boundary is not only
the nested `go.mod`: a `vendor` path segment is one too. A vendored `.go`
write is attributed by `moduleRoot` to the enclosing module that provably
cannot refresh it, so routing it in place returns an error, retains the dirty
transaction, and wedges every later cycle. It escalates instead — a vendored
write is dependency-surface movement anyway. The LSP's `RefreshDisk` applies
the same oracle but SKIPS such paths rather than escalating: an error there
disables saved-source intelligence until the server restarts, and per-module
batching would let one vendored path discard the legitimate refreshes
delivered with it. A `.go` path with no `go.mod` above it takes the same skip.

**Known limits (one parity, one closed).** Escalation is decided from tree
containment, so a shape outside it can still generate against stale types
until something else forces a reopen. A `replace` pointing at a module
OUTSIDE the watched trees is invisible to the session — it was unwatched
before this design and is unwatched now, so nothing changes.

The in-tree `go.work`/`replace` sibling-consumer shape — two module roots
where neither contains the other, one consuming the other's types — is now
closed: `moduleConsumesModule` resolves a consumer's `go.mod` `replace`
directives (filesystem targets only; a versioned replace resolves through
the proxy/cache and cannot observe a local edit) and `workspaceUsesModule`
resolves its authoritative `go.work` `use` list, read from the Module's own
frozen `GOWORK` rather than a filesystem walk. Both sides are
symlink-normalized before comparing (`EvalSymlinks`, falling back to `Clean`
when the path doesn't exist yet) — `go env GOWORK`'s answer and the
session's own lexical module roots resolve differently on darwin's
symlinked `/var`. `reopenConsumerModules` reopens every linked consumer
(discarding its retained analysis) and queues its dirs for regeneration.
Unreadable or unparseable consumer `go.mod` metadata fails toward
"consuming" (reopen), matching "unsure → escalate"; `reopenConsumerModules`
itself surfaces a per-module error result rather than silently skipping a
consumer whose own metadata just broke. Where both mechanisms could fire —
a nested, contained module that is also a replace/go.work consumer —
containment escalation wins outright; the consumer pass runs only for
non-escalated Go edits. **Wired in Phase 3:** `regenPending`'s `goDirs` lane
groups authored-Go dirs by module root and calls `reopenConsumerModules` for
every non-escalated Go cycle in a multi-module session, so the live loop now
heals this shape on the edit itself
(`TestWatchGoEditPropagatesAcrossReplaceLink`).

**Preserved invariant.** Any authored-`.go` event still sets `goChanged`
(via a new `goDirty` flag on the dirty set, since `depDirty` no longer
covers it), so the server binary rebuilds and restarts even when zero
`.x.go` bytes change. Signature-level `.go` edits propagate through the
in-place world reload the verdict announces; dependents then regenerate
against fresh types, and hash-gated writes drop no-op disk churn. Phase 3
makes the flag load-bearing rather than belt-and-braces: a warm cycle can now
legitimately produce no `cycleResult` at all (a Go-only leaf with no GSX
dependent), and `goDirty` is then the only thing that still rebuilds the
server. Signature edits still propagate — the warm swap replaces the retained
syntax, so dependents re-type-check against the new signature without a
reload (`TestWatchSession_GoEditRegeneratesOnlyDependents` asserts the
`IntInto` writer change).

**Observability (chosen: console + panel).** The verdict reason threads into
the `generated`/status events and one console line, e.g.
`full reload: new import "x/y" in merge/merge.go`. Panel display is a
`vite-plugin-gsx` bump (sibling repo).

### Phase 2 — cheap escalation

**Measure first.** Instrument where the 4.6s reopen goes on gsxui
(main-module `packages.Load` vs external closure vs regeneration). #178's
shared world covers only the closure every Module resolves identically — the
gsx runtime, gsx/std, and their transitive stdlib (`sharedworld.go`); a
project's third-party deps and main module still reload each time. Phase 2
extends world sharing to the project's full external (non-main-module)
closure; the measurement fixes the expected win before any code.

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
   gates + vite-plugin-gsx panel bump. **Shipped, #186.**
2. Phase 2 PR: external-world reuse across generations (after measurement).
   **Shipped, #188** (config-tier project shared world).
3. Phase 3: warm Go-syntax swap — a body edit reloads nothing at all — plus
   the cross-module consumer reopen. Designed in
   `2026-08-12-incremental-saved-go-watch-design.md`, originating in PR #185
   (Hossein Bahmani). **Shipped.**
4. Docs dev-guide + ROADMAP updates ride the PRs.
