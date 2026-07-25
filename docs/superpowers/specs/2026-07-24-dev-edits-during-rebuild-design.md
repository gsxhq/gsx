# Edits during an in-flight rebuild

Real projects rebuild slowly — ~20s on one-learning today, minutes on bigger
trees. Saving two or three times inside that window is normal editing, not an
edge case. This documents what the dev loop does with those saves today, why
the existing debounce does not cover the case, and the direction to fix it.

## What exists today

Two loops share one pattern:

| loop | debounce | event loop |
|---|---|---|
| `gsx dev` | 120 ms (`gen/dev.go:296`) | `gen/dev.go:388` |
| `gsx generate --watch` | 100 ms (`gen/watch.go:129`) | `gen/watch.go:141` |

`schedule()` resets a trailing `time.AfterFunc`; the timer's send lands in a
capacity-1 `fire` channel with a dropping `default`. So a burst of events
collapses into a single cycle. What each cycle covers is accumulated
separately in `watchDirtySet` (`gen/watch_dirty.go`), a transactional dirty-dir
set that clears only on a fully successful cycle and is retained for retry
otherwise.

**The cycle runs synchronously on the event-loop goroutine.** `cycle()`
(`gen/dev.go:312`) does generate → `srv.rebuild(ctx)` → health-wait inline, so
for the whole ~20s nothing drains `w.Events`. The `ctx` passed to
`exec.CommandContext` (`gen/devserver.go:486`) is the dev-process shutdown
context — it is never cancelled per-edit.

## Consequence

The debounce is tuned for the sub-second regime (editor save bursts, atomic
rename write+chmod pairs). It cannot help once a cycle has started: there is no
cancellation, and no coalescing *into* the running build. Saves at t=2s and
t=8s of a 20s build produce:

- t=0 — cycle 1 starts, snapshots the source as it was at t=0.
- t=2, t=8 — events queue in fsnotify. Nothing is lost (they stay pending in
  the backend/kernel until the loop returns to `select`; repeated writes to one
  file may coalesce, which is harmless — the dirty set is per-directory).
- t≈20 — cycle 1 completes, restarts the server and **reloads the browser with
  output that is already two edits stale**.
- t≈20.12 — events drain, one dirty set, one `schedule()`, cycle 2 starts.
- t≈40 — the t=8s edit finally appears.

So: bounded waste (at most one doomed build plus one catch-up cycle, never one
per save), but unbounded latency — worst case ~2× build time to see an edit,
with a misleading intermediate reload that looks like the loop responded.

## Secondary defect

`schedule()` stops the timer but never drains `fire` (`gen/dev.go:296-306`,
`gen/watch.go:129-139`). A token left there by a timer that fired *during* a
long cycle survives the reset, so the next cycle can start the instant the loop
re-enters `select`, racing the still-draining `w.Events` (Go picks among ready
cases at random). Safe — the dirty set is transactional and leftover events
schedule another cycle — but it can produce an extra partial cycle right after
a slow build, exactly when cycles are most expensive. The quiet-period
guarantee only actually holds if `schedule()` drains `fire` before arming.

## Direction

Two layers, in order. The first is cheap and independently useful; the second
needs the first.

**1. Keep draining events during a cycle.** Move the cycle body to its own
goroutine and let the select loop keep servicing `w.Events`. Two immediate
wins: the dirty set is already non-empty when cycle 1 finishes, so the loop can
(a) **suppress the stale reload** — no point telling the browser about output
we know is superseded — and (b) chain straight into cycle 2 without waiting out
another debounce. Latency is unchanged, but the loop stops lying about it, and
the panel can honestly report "2 edits queued" while building.

**2. Cancel the doomed build.** Give each cycle its own context, cancel it when
a new relevant event arrives, and restart. `go build` is safe to kill (the
build cache absorbs the partial work; the old binary is untouched on failure,
per `rebuild`'s contract). Cancellation must be scoped to the generate+build
phases only — once we have stopped the old server and started the new one
(`restartNoBuild`), the cycle has to finish or we leave the server in an
indeterminate state. With this, the t=8s edit lands at ~t=28 instead of ~t=40.

Open questions before this becomes a plan: whether cancellation needs a
minimum-progress guard (a save every 5s against a 20s build would otherwise
starve — never finishing any build), and whether `gsx generate --watch` wants
the same treatment or only `gsx dev` (warm regenerate is ~1–2 ms there; the
20s is the Go build, which `--watch` does not run — likely dev-only, with only
the `fire`-drain fix shared).
