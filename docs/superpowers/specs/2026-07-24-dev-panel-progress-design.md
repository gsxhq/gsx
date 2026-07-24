# Dev panel build progress

Long rebuilds (minutes in big projects — observed in one-learning) give zero
feedback: the last-good binary keeps serving, the browser changes nothing
until the completion reload. The panel's `phase` enum already exists but is
only posted at cycle ends, so even an open panel shows stale `idle` all the
way through a build.

## gsx side

Post status at every phase transition inside the cycle (save → `generating` →
`building` → `starting` → `idle`), not just at cycle ends. The status payload
gains:

- `phaseSince` — RFC3339 timestamp of the current phase's start (present on
  every status; the panel ticks elapsed time locally — no added polling).
- `lastCycle.durationMs` — how long the previous cycle took, for
  expectation-setting ("last: 2m10s").

`restart-server` and rebuild commands already post around their work; they
adopt `phaseSince` for free.

## Panel side (vite-plugin-gsx)

- **Auto-show**: on transition to non-idle, start a local timer
  (`devPanel: { autoShow: 3000 }` ms, default 3000; `autoShow: false`
  disables auto-show, Cmd-D unaffected). If still non-idle when it fires, the
  full panel appears. An auto-shown panel hides itself on `idle`; a
  manually-opened one stays. Successful cycles usually end in a full reload
  (fresh, closed panel) — the auto-hide covers cycles that end without one.
- **Phase view**: `building… started 42s ago · last cycle 2m10s`, ticking
  from `phaseSince`.
- **Log box**: while phase is `building`/`starting` and `/__gsx/log` serves
  (probe once per page; 404/absence → no box, no error), the panel expands to
  a larger layout with a scrolling tail. Poll `/__gsx/log` ~1s, strictly
  gated on panel-visible AND phase-non-idle — an idle page makes zero
  requests. Truncation shown honestly via `x-gsx-log-start`.
- Everything respects `devPanel: false` (no auto-show, no polling, no panel).

## Dependencies / sequencing

Builds on `/__gsx/log` (vite-plugin-gsx PR #4) and `[dev].log` env injection
(gsx PR #158). Plugin half ships in the release after #4; gsx half merges
after the plugin release, pin bumped npm-verified (0.x caret discipline).

## Testing

- gen: phase-transition posts observed at a recorder mid-cycle (a slow build
  command in the fixture makes the `building` post observable); `phaseSince`
  monotonic per phase; `durationMs` populated.
- plugin: auto-show timer (fires only if still non-idle; cancelled on idle;
  `autoShow: false`); auto-shown-hides / manually-opened-stays; log polling
  gate (zero requests when idle or hidden); 404 log probe degrades to no box.
- Live smoke: artificially slow build (sleep in [dev].build) → panel
  auto-appears at 3s with ticking elapsed + log box; quick save → nothing
  appears.

## Out of scope

- Streaming build output beyond the log tail; build progress percentages
  (go build exposes none).
- Panel state persistence across the completion reload.
