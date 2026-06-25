# `gsx generate --watch` + Vite-plugin supervision — design

**Status:** approved design (brainstorming), pre-plan.
**Date:** 2026-06-25.
**Repos touched:** `github.com/gsxhq/gsx` (Go, this worktree) and
`@gsxhq/vite-plugin-gsx` (TS, `~/personal/gsxhq/vite-plugin-gsx`).

## Goal

Make the `.gsx` edit loop fast by replacing the per-save cold `gsx generate`
(fresh process + `go/packages.Load` every time) with a **warm, long-lived
`gsx generate --watch` process**, and have the Vite plugin **supervise** that
process instead of shelling out per change. Slice 1 is **measure-first**: keep
the warm process simple (full reload per change), instrument it, and gate any
scoped re-type-checking (slice 2) on the measured numbers.

## Motivation — why the file cache can't do this

`gen/cache.go` is a content-addressed **file** cache (sha256 of each package's
`.gsx`+`.go` source + deps + `codegen.Version()` + go.mod/sum, stored under the
user cache dir). It skips re-running codegen for *unchanged* packages across
invocations, but it structurally cannot speed the edit loop:

1. Every `gsx generate` is a **fresh process** that runs `loadGraph(root)` —
   `go/packages.Load`, the dominant cost (~383 ms/pkg in the synthetic perf
   note) — just to compute cache keys, even when every package hits.
2. The package you just edited is **always a cache miss** → a full cold
   `go/packages.Load` + type-check on every save.

So the file cache helps everything *except* the package being actively edited.
The only way past it is a long-lived process that avoids per-save startup and
keeps OS/Go build caches warm. (True analysis warmth — retaining dependency
`*types.Package` and re-checking only the dirty package — is the slice-2 lever,
deliberately out of scope here.)

## Architecture

```
.gsx save
   │  (fsnotify, debounced)
   ▼
gsx generate --watch  ── regenerates .x.go (existing Generate path) ──▶ disk
   │  emits NDJSON on stdout                                              │
   ▼                                                                      ▼
vite-plugin-gsx  ── overlay show/clear ──▶ browser              wgo sees .x.go change
                                                                          │
                                                                  rebuilds Go binary
                                                                          │
                                                                  Go POSTs /__reload
                                                                          │
                                                                vite full-reload ──▶ browser
```

Two coordinated components, each independently testable.

### Component A — `gsx generate --watch` (Go, `gen` package)

A new long-lived mode behind `gsx generate --watch [--format=ndjson]`.

**Watcher.** `fsnotify` over the resolved target dirs, recursive. Matches
`*.gsx` **and** non-generated `*.go` (a `.go` type change can change generated
output). **Excludes** `*.x.go` (never watch our own output — prevents a
regenerate→write→event loop), and the `tmp/`, `dist/`, `node_modules/`, `.git/`
directories. Newly-created subdirectories under a watched root are added to the
watch set.

**Debounce.** A settle window (~100 ms, single tunable constant) coalesces the
burst of events editors emit on a single save into one regenerate cycle. The
timer resets on each event and fires once quiet.

**Regenerate cycle.** On a settled change, run the **existing `Generate(paths)`
path in-process** (no new codegen logic). The content-hash file cache already
scopes codegen to the dirty package (siblings hit), so slice-1 warmth =
no process startup + warm OS/Go/cache-store. Each cycle is **instrumented**:
wall-clock duration is recorded and reported.

**Lifecycle.** Runs until SIGINT/SIGTERM, then exits cleanly (stop the watcher,
flush). A regenerate that returns error-severity diagnostics does **not** stop
the loop — it reports and waits for the next change. The very first cycle runs
at startup (equivalent to `generateOnStart`) so `.x.go` exist before the Go
build.

### Component B — output / protocol

Two renderers, selected by `--format`:

**Default (human, TTY).** Same rich diagnostics as `gsx generate`, plus a
one-line per-cycle summary: `regenerated <pkg> — <n> file(s), <ms>ms` on
success, or the rich diagnostic block on failure. This makes `--watch` a
first-class standalone tool (usable under `wgo`, or alone, with no Vite).

**`--format=ndjson` (machine, for the plugin).** One JSON object per line on
stdout, flushed per line:

- `{"event":"start","root":"<abs>","watching":["<dir>",…]}` — once, after the
  initial generate.
- `{"event":"generated","ok":true,"durationMs":142,"written":["app.x.go",…],"diagnostics":[]}`
- `{"event":"generated","ok":false,"durationMs":98,"written":[],"diagnostics":[{file,range,severity,code,message,help}]}`
  — `diagnostics` reuses the existing `internal/diag` JSON shape verbatim.
- `{"event":"error","message":"<text>"}` — operational failure (bad module
  root, watcher error) distinct from a compile diagnostic.

`ok` is `true` iff there were no error-severity diagnostics and no operational
error. `durationMs` is the measured cycle wall-clock (the measurement
deliverable). Non-event diagnostic text and logs go to **stderr** so stdout
stays a clean NDJSON stream.

### Component C — Vite-plugin supervision (`@gsxhq/vite-plugin-gsx`)

**Spawn once, consume the stream.** In `configureServer`, instead of
registering a chokidar `.gsx` watcher and running `runGenerate` per change,
**spawn `gsx generate --watch --format=ndjson` once** and read its stdout
line-by-line (newline-delimited JSON). The plugin no longer owns `.gsx`
watching or generation — gsx does.

**Overlay, driven by the stream.** Keep the existing `errorShown` state:
- `generated ok:false` → log the diagnostics and `server.ws.send({type:"error", err})`; set `errorShown = true`.
- `generated ok:true` while `errorShown` → `server.ws.send({type:"full-reload"})`; clear `errorShown`. This is the **exact v0.2.1 recovery-reload**, now driven by the daemon event instead of an in-process generate result. It is the case that matters for identical-output recovery (fix → same `.x.go` → wgo does not rebuild → Go never POSTs `/__reload`), which is the bug already fixed in v0.2.1.

**Reload on success is unchanged.** gsx writes `.x.go` → wgo rebuilds the Go
binary → Go POSTs `/__reload` → the plugin's existing `/__reload` middleware
broadcasts a full-reload. The plugin does **not** trigger a reload on a clean
`generated` event (except the recovery case above) — the Go-POST owns success.

**Supervision.** On daemon stdout EOF / process exit, log and surface a notice
in the Vite log (auto-restart is a later nicety, not slice 1). `apply: "serve"`
keeps it dev-only; the daemon is never spawned for `vite build`.

**Compatibility.** The plugin option surface stays backward-compatible: the
existing `command`/`paths`/`watch`/`cwd`/`debounce`/`generateOnStart` options
are reinterpreted to configure the spawned `--watch` process (e.g. `command`
becomes the base argv with `generate --watch --format=ndjson` appended;
`debounce` is passed through; `generateOnStart` is implicit in watch mode).
Version bump (minor).

## Reload-chain interaction (the subtle part)

| Outcome | `.x.go` written? | wgo rebuild? | Go `/__reload`? | Plugin action |
|---|---|---|---|---|
| Clean change, output differs | yes | yes | yes | none (Go-POST reloads); clear overlay if shown |
| Clean change, **identical output** | no | no | no | **recovery full-reload** (clears overlay) |
| Compile error | no | no | no | show overlay from diagnostics |
| Recover from error, identical output | no | no | no | recovery full-reload + clear overlay |

The identical-output rows are why the v0.2.1 recovery-reload logic must survive
into the daemon-driven design — they are exactly the original FOUC/stale-overlay
bug class.

## Error handling

- **Regenerate diagnostics** — reported per cycle (human or NDJSON); loop
  continues; no `.x.go` written for a failed package (all-or-nothing, unchanged).
- **Operational failure** (bad go.mod, unreadable dir) — `error` event /
  stderr; loop continues if recoverable, exits non-zero if the root is unusable.
- **Watcher failure** — logged; attempt to continue watching remaining paths.
- **Daemon death (plugin side)** — EOF detected, logged, notice surfaced.

## Testing

**gsx side (`gen`):**
- Start `--watch` on a temp module; touch a good `.gsx` → assert the `.x.go`
  regenerates on disk **and** a `generated ok:true` NDJSON line is emitted with
  the written file listed.
- Touch a broken `.gsx` → `generated ok:false` with the expected diagnostic and
  **no** `.x.go` write; then fix it → `generated ok:true` (recovery).
- Debounce: emit a burst of N events within the window → assert **one**
  regenerate cycle.
- Exclusion: writing an `.x.go` does not trigger a cycle (no self-loop).

**plugin side (`@gsxhq/vite-plugin-gsx`, vitest):**
- Extend the existing fake into a **streaming** fake `gsx --watch` that emits
  canned NDJSON; assert: `ok:false` → `ws.send({type:"error"})`; subsequent
  `ok:true` → `ws.send({type:"full-reload"})` (recovery); `ok:true` with no
  prior error → no reload.
- Daemon EOF → notice path exercised.

**e2e (measurement deliverable):** scaffold via `gsx init`, `task dev`, edit a
`.gsx` in a loop; record the per-save latency (the `durationMs` stream) and
compare against the old per-save `gsx generate`. This number decides whether
slice 2 (scoped re-check) is worth building.

## Scope boundaries

**In (slice 1):** `--watch` mode (watcher + debounce + warm-process regenerate +
instrumentation), the human + NDJSON renderers, plugin supervision, the
recovery-reload wiring, the measurement e2e.

**Out (deferred):**
- **Slice 2 — scoped re-type-checking** (retain dependency `*types.Package`,
  re-check only the dirty package). Gated on slice-1 measurements.
- Daemon auto-restart-on-crash in the plugin.
- Any IPC beyond NDJSON-on-stdout (no socket, no request/response).
- Watching outside the module / cross-module.

## Open question for review

Watching non-generated `*.go` (not just `*.gsx`) is the correct-but-broader
choice: a `.go` type change *can* change generated output, so ignoring `.go`
would let `.x.go` go stale until the next `.gsx` edit. The cost is more
regenerate cycles during ordinary Go editing (debounce mitigates). Alternative:
watch `.gsx` only in slice 1 and accept that a `.go`-only change needs a manual
regen / the next `.gsx` save. **Recommendation: watch both** (correctness), but
flag for the reviewer.
