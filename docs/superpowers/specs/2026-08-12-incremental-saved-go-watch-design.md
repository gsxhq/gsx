# Incremental saved-Go watch invalidation (Phase 3)

**Date:** 2026-08-12 (shipped 2026-08-14)
**Status:** Shipped
**Origin:** PR #185, *"fix(dev): incrementally refresh saved Go edits"* (Hossein
Bahmani, based on #184). Integrated onto main's #186/#188 model per
`.pr185-overlap-report.md`; this document describes what SHIPPED, keeping the
original's design statements wherever they survived the integration.
**Phase 1/2:** `2026-08-12-warm-go-edit-watch-design.md` (#186 warm `.go` path
with Module-decided verdicts, #188 project shared world).

## Goal

An authored `.go` save must preserve `gsx dev` correctness while regenerating
only GSX packages in the saved package's reverse dependency closure — and,
for the common case (editing the body of a file cmd/go has already selected),
without reloading the world at all. Module metadata changes (`go.mod`,
`go.sum`) remain full session reopens.

## Starting point (after Phase 1)

Phase 1 already split authored `.go` off `depDirty`: the watch loop refreshed
the edited dir, regenerated its dependent closure, and paid **one** in-place
world reload per cycle, announced by a `RefreshVerdict`. That reload is a
`packages.Load` of the whole project half — the dominant cost of a save, and
it is paid even when the edit changed one line inside one function.

## Design

### Two lanes in the dirty set

`watchDirtySet` gains `goDirs map[string]bool` beside `dirs`. `classifyDirtyFile`
routes each changed watched file into exactly one lane:

| file | lane |
| --- | --- |
| `go.mod` / `go.sum` | `depDirty` (full session reopen) |
| authored `.go` in a dir the in-place path cannot serve (nested consumed module, vendor, no enclosing module) | `depDirty` — Phase 1's `goEditNeedsReopen`, unchanged |
| any other authored `.go` | `goDirs` + the `goDirty` rebuild latch |
| `.gsx` | `dirs` |

The lanes are exclusive: routing a Go dir into `dirs` as well would refresh
the directory a second time through the `.gsx` path and walk a Go-only dir
through the empty-dir orphan branch every cycle. A directory saved on both
paths at once (an editor save-all over `helper.go` and `page.gsx`) lands in
both maps from its own two events, which `regenPending` resolves to one
refresh.

`goDirty` (the bool) stays as the **rebuild latch**: `regenerate` still
computes `rebuild = depDirty || goDirty` before the cycle and never clears it
on failure. It became load-bearing in Phase 3 rather than belt-and-braces —
a warm cycle can now produce no `cycleResult` at all, so the latch is the only
thing that still rebuilds the server binary.

**Transaction model: main's, not the PR's.** An operational failure retains
the *complete* transaction — `dirs`, `goDirs` and `depDirty` together — for
the next relevant event. PR #185 proposed a per-directory partial commit
(successful dirs publish immediately, only failed inputs are retained on the
classification they arrived with). That model is orthogonal to incremental Go
refresh and weakens main's rebuild-preservation guarantee as written (a
committed-with-diagnostics partial commit can skip the server rebuild
entirely), so it is **excluded and left as a follow-up**: it should be
designed on its own merits, with the rebuild interaction settled first.

### `Module.RefreshGoSourcesAndInvalidate` (internal/codegen)

Serializes against analysis, computes the exact **pre-change** reverse closure
before any graph or cache reset, refreshes the saved source snapshots for the
changed directories, and invalidates that closure. It returns the GSX
projection of the closure plus the same `RefreshVerdict` the `.gsx` entry
point returns.

For an existing active non-cgo file whose package name, build constraints, and
imports remain compatible with the retained cmd/go selection, the transition
parses the new bytes into the retained `FileSet` and swaps that file into the
retained package's syntax — **no `packages.Load` at all**. Every uncertain or
structural transition instead marks the semantic source inventory for the
existing authoritative cold reload. That fallback preserves correctness for
file membership, cgo, new imports, package clauses, and the frozen Go command
context. In both tiers the watcher regenerates only the returned GSX closure;
unrelated committed `.x.go` output remains valid.

### `regenPending`

```go
func (s *watchSession) regenPending(pending, goDirs map[string]bool, depDirty bool) ([]cycleResult, error)
```

1. **Go lane** (`refreshGoDirs`): group `goDirs` by module root; a dir with no
   enclosing module is *skipped* (`errNoEnclosingModule`) — authored Go outside
   every module cannot participate in any watched module's build, and failing
   would retain the dirty set forever and wedge every later cycle on a stray
   script. One `RefreshGoSourcesAndInvalidate(dirs...)` per module; its GSX
   projection joins the affected set.
2. **Cross-module consumers**: for a multi-module session with a non-escalated
   Go edit, `reopenConsumerModules` reopens every `replace`/`go.work`-linked
   consumer module and adds its dirs to the affected set (see Phase 1's
   *Known limits*, now closed).
3. **GSX lane**: main's loop, with two additions — it skips its own refresh for
   a dir the Go lane already committed (reusing that lane's verdict for the
   emptied-dir note) and records what it swept, so the regeneration step below
   cannot sweep or report the same dir twice. Otherwise unchanged: refresh each
   pending dir, add `Dependents(dir)`, sweep an emptied dir's orphans.
4. **Regenerate**: the affected set goes through `regenDirs` (batched refresh,
   `Reload` stamping, refresh-time charging, per-dir error isolation). An
   affected dir with no `.gsx` left is swept instead of generated — a deleted
   package the retained graph still names, or a Go-only dir reached through
   the pre-inventory closure fallback; generating it would fail the whole
   cycle on a package the inventory no longer selects.

**Refresh de-duplication.** A dir dirtied on both lanes is refreshed once, on
the Go lane, which commits the whole directory's facts. `regenDirs` then
re-inventories the dirs it regenerates as part of its own batch, so a dir that
is both an edited Go dir and a GSX dir is scanned twice per cycle. This is
accepted deliberately: it is the same second refresh every `.gsx` cycle has
always paid, it carries no `packages.Load` (measured 0.25ms for a single-file
dir, 1.2ms for a 12-source dir, against the ~250ms load it is no longer
performing), and it is what recomputes and stamps the module-wide reload note.
Threading a skip-refresh set into `regenDirs` would buy back that millisecond
and forfeit the observability.

**Reload attribution.** `cycleResult.Reload` keeps main's convention: the
module-wide cause lands on `regenDirs`' first generated result per Module per
cycle. It works unchanged across the Go lane because a scheduled reload stays
*pending* until an analysis consumes it — `regenDirs`' own refresh therefore
re-reports the same cause through `refreshVerdictLocked`'s persisted
attribution. The one shape with no carrier is a Go-only leaf with no GSX
dependent at all (an unreferenced `cmd/` package): its module contributes
nothing to `regenDirs`, so the Go lane stamps the module's first seed dir
itself. A warm cycle has no cause and stamps nothing — the cycle is silent
and the rebuild latch carries it.

### Closure discipline (verified, 2026-08-14)

The original design computes the reverse closure *before* the refresh,
"preserving importer edges the cache reset discards". Probed on main: no edge
loss exists to preserve against. `invalidateScopeLocked` evicts
`pkgTypes`/`pkgResults`/decl caches and never touches the import graphs
`reverseClosure` walks; `refreshDiskSources` mutates only source snapshots,
inventory facts and retained syntax; and the cold reload that *does* rebuild
the graphs happens later, inside `Generate`, after the cycle's affected set is
fixed. `Dependents(blog)` and `Dependents(model)` came back byte-identical
across a refresh that scheduled a reload. Computing the closure first is
therefore a one-walk optimization and a lock-scope nicety here, not a
correctness fix — and main's `.gsx` lane (refresh → invalidate → `Dependents`)
is sound as written.

The Go lane's projection legitimately differs from `Dependents` in one way:
`Dependents` always retains its seed, while the GSX projection drops a Go-only
seed. That is the intended projection, not a lost edge.

## Invariants

- The affected closure is computed from the pre-change graph.
- Go source selection is still delegated to cmd/go; the warm tier only reuses
  a previously published selection when its membership constraints are
  unchanged.
- Unrelated GSX directories are not regenerated.
- A Go-only package may be a seed or intermediary in the retained graph.
- `go.mod`/`go.sum` changes continue to reopen the complete watched module
  universe.
- Failed cycles retain the complete dirty transaction for a later retry.
- Every authored-Go cycle rebuilds the server binary, warm or not.

## Review hardening (carried from PR #185, re-verified on main)

- **Every refresh owns Go coherence.** The authored-Go fast-path/reload
  decision lives in `refreshDiskSources` itself, not only in the Go-path entry
  point. A `.gsx` save in the same debounce window as a helper edit commits
  the helper's new bytes through the GSX path first; without this the later
  Go-path diff sees before==after and retains stale syntax indefinitely.
- **Unknown package dirs are structural.** A saved Go dir the cold inventory
  has never seen (a newly created package) has no retained edges — the GSX
  package poisoned by importing it while missing is exactly the consumer the
  change repairs — so the affected set falls back to every authoritative GSX
  dir and the authoritative reload handles selection.
- **No published inventory falls back to the closure.** Before the first cold
  load (or after a failed reload) the GSX projection would be empty; the
  complete closure is returned unfiltered, mirroring `Dependents`' fallback,
  and the watcher sweeps affected dirs with no `.gsx` on disk instead of
  failing the cycle on deleted packages.
- **Cross-module consumers reopen.** Decided by the `GOWORK` value frozen into
  the consumer Module's Go command universe (`Module.GoWorkFile`), never by a
  filesystem walk. Both sides of every comparison are symlink-normalized —
  `go env GOWORK` answers resolved, session roots are lexical, and darwin's
  `/var` is a symlink.
- **Faithful constraint detection.** The warm tier detects build constraints
  with the same go/build header parse as the variant-family analysis, so a
  directive after a `/* */` block or a tab-separated legacy `+build` line is
  never mistaken for an unconstrained file.
- **Paired-output exclusion uses BOTH manifests.** A `.gsx` deleted in the same
  refresh leaves its orphaned `.x.go` paired only in the before manifest;
  consulting either side alone reads that orphan as an authored file appearing
  or disappearing and schedules a spurious reload.
- **Warm swaps claim no authorship.** The verdict is computed from the reload
  this call *scheduled*, not from the raw Go diff, so a warm-swapped edit
  cannot overwrite the persisted attribution of a reload that was already
  pending.
- **One refresh per dir per cycle** on the lanes themselves (see the
  de-duplication note above for `regenDirs`' own batch).
- **The warm tier's dir contract is self-enforcing.** `refreshGoSyntaxLocked`
  canonicalizes every dir (`Abs`+`Clean`, refusing on error) and refuses a dir
  that resolves into neither manifest. Both matter because the failure mode is
  silent: a dir that misses the manifest keying yields empty snapshot maps on
  both sides, which reads as "nothing to swap" and returns WARM with the
  retained syntax left stale. Probed by injecting non-manifest-keyed dirs at
  the call site — without the guards the codegen suite reports "stale-blind:
  removed dep.Value produced no diagnostic"; with them the same input takes
  the conservative reload.

## Testing

Shipped coverage, `gen` side:

- Same-package Go helper edit regenerates its GSX package and its importer,
  never an unrelated package.
- Go-only dependency through a Go-only intermediary (`model` → `bridge` →
  `blog` → `site`) regenerates exactly the GSX projection.
- Warm output is byte-identical to a fresh session's over the same disk.
- A mixed Go+GSX save-all in one cycle refreshes the shared dir once —
  asserted, not asserted-by-comment: `sourceview.RefreshedDirs` counts
  directory refreshes, and the mixed save must perform the same **two** (its
  lane's, then `regenDirs`') as the identical `.gsx`-only save, never three.
  The counter measures refreshes rather than `Inspect` calls because since
  #184's incremental derivation a re-refresh of an unchanged dir Inspects
  nothing, which would make an Inspect-delta test vacuous (verified by
  breaking the de-duplication).
- A deleted directory dirtied through both lanes is swept, not regenerated,
  and its importer regenerates with missing-package diagnostics — one
  committed cycle, no operational error.
- Cross-module: a `replace`-linked consumer regenerates through
  `regenPending`; an unlinked sibling does not; authored Go with no enclosing
  module is skipped rather than wedging the cycle.
- Load budgets: `.gsx` body edit = 0 loads, `.go` body edit = 0 loads, second
  `.go` body edit = 0 loads, `.go` file ADDED = exactly 1 (the refused
  transition, which keeps the zeros honest). Same table for a configured
  module (class merger + out-of-module filter package).
- Reload observability: a warm body edit carries no note; a membership change
  carries `changed Go source <path>`, including for a zero-dependents Go leaf.

`internal/codegen` side: the warm-transition acts (exact pre-change closure
through a Go-only bridge; zero-load signature edit observable through
diagnostics; save-all coherence; metadata-error baseline forcing a reload;
new-package all-GSX-dirs fallback), the pre-inventory closure fallback, and
build-constraint placement edges.

## Follow-ups

- **Per-directory partial commit** (PR #185's transaction model) — excluded
  above; needs its own design settling the `gsx dev` rebuild interaction.
- Structural transitions may later use a reduced cmd/go metadata load instead
  of the complete semantic reload.
- A `_test.go` edit still forces the authoritative reload (test files are not
  in `CompiledGoFiles`, so the warm tier refuses). Same as pre-Phase-3
  behaviour, not a regression.
