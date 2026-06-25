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
The only way past it is a long-lived process that keeps the analysis warm.

**Prior art — `gen.CachedResolver` (the playground server).** This already
exists and is proven: `playground/server/render.go` builds a `gen.CachedResolver`
**once** (`gen.NewCachedResolver(moduleDir, imports)` — the single
`packages.Load`), then renders each request via `resolver.Generate(dir, src)`,
which runs **fully in-process with no per-render `go list`** (tracked as
`genMs`). The watch daemon should reuse this engine rather than reinvent it. The
cold `Generate(paths)` path re-runs `go/packages.Load` (~383 ms) every call; the
warm resolver pays that once at startup and amortizes it across every save.

**The catch the playground didn't face.** The playground uses the resolver in a
constrained setting: a **single** generated package (`views`) and a **fixed,
immutable import allowlist**. Its cached importer (`mapImporter`) returns
`"cached importer: %q not loaded"` for anything not pre-loaded, and assumes the
loaded dependency types never change. A real project has (a) a **dynamic import
set** — a `.gsx` can add an import the resolver never loaded, (b) **multiple
in-module packages** with cross-package component refs, and (c) **mutable
`.go`** files. So the daemon must pre-load the whole module and **rebuild the
resolver when the dependency surface changes** (see Component A). True
fine-grained incremental invalidation (re-check only the dirty package, keep
everything else warm across signature changes) remains the slice-2 lever.

## Architecture

```
.gsx save
   │  (fsnotify, debounced)
   ▼
gsx generate --watch  ── warm regenerate (gen.CachedResolver) ──▶ .x.go on disk
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

**Warm regenerate via `gen.CachedResolver`.** The daemon holds a `watchSession`
that owns one `gen.CachedResolver`, built at startup over the **whole module**
(load `./...` once → pre-load every in-module package + its imports, so
cross-package component refs resolve and the cached importer doesn't miss).
On a settled `.gsx` change, the session calls `resolver.Generate(dir, nil)`
(read from disk) for each dirty package — **fully in-process, no per-save
`go list`**. Generated `.x.go` are written with the file-cache's existing
hash-gated `restore` (identical bytes → no write → no spurious wgo rebuild).
Each cycle is **instrumented** (`durationMs`).

**Rebuild policy (the staleness guard).** The cached resolver is only valid
while the dependency surface is unchanged. The session rebuilds it (one cold
`packages.Load`) when:
- a watched `*.go`, `go.mod`, or `go.sum` changes, or
- `resolver.Generate` fails with a cached-importer miss (a `.gsx` added an
  import the resolver never loaded).
On a cached-importer miss the session rebuilds once and retries the cycle, so a
newly-added import self-heals. Pure `.gsx` markup/logic edits never trigger a
rebuild — they take the warm path.

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

## Code structure & reuse (DRY)

The daemon is a **thin orchestrator over existing pieces** — it adds watching
and a warm-session lifecycle, and reuses everything else. No generate logic,
diagnostic encoding, or file-writing is duplicated.

| Need | Reuse (existing) | New |
|---|---|---|
| Warm in-process codegen | `gen.CachedResolver` (`NewCachedResolver` / `Generate`) — the same engine the playground uses | — |
| Resolve target dirs | `discoverDirs(paths)` (gen) | — |
| Hash-gated `.x.go` write | `restore()` (gen/cache.go) | — (extract to a shared helper if still unexported) |
| Diagnostics → JSON | the **one** encoder already behind `gsx generate --json` / `internal/diag` | — |
| Whole-module import/dir discovery | `go/packages` load (as the resolver build already does) | thin helper to collect the dir + import set |
| Command dispatch | `gen/main.go` `case "generate"` | `--watch`/`--format` flags → `runWatch` |
| File watching | — | `fsnotify` watcher + debounce (new generator-side dep) |

New, focused units (small files, one responsibility each):

- **`gen/watch.go` — `runWatch`.** Flag parsing (`--watch`, `--format`), wiring
  the watcher → debounce → session, signal handling, shutdown. Pure
  orchestration; no codegen knowledge.
- **`gen/watchsession.go` — `watchSession`.** Owns the `*gen.CachedResolver` +
  the module's import/dir set. `regenerate(dirtyDirs)` (warm `resolver.Generate`
  + `restore`) and `rebuild()` (the rebuild policy). This is the only unit with
  real logic, and it sits **on top of** `gen.CachedResolver` — it does not fork it.
- **`gen/watchemit.go` — the event renderer.** Human lines vs NDJSON, reusing
  the existing diag-JSON encoder so the `diagnostics` field is byte-identical to
  `gsx generate --json` (and the LSP). One diagnostic JSON shape, three emitters.

**Relationship to the playground.** Both consume `gen.CachedResolver` — that is
the shared engine, and the DRY win. The watch-specific parts (whole-module
pre-load, rebuild-on-dep-change) live in `watchSession` because the playground
deliberately doesn't need them (fixed allowlist, immutable deps, single
package). We reuse the genuinely shared engine without forcing a premature
shared abstraction over two different lifecycles (YAGNI); if the playground
later wants a module-wide resolver, it can lift `watchSession`'s builder.

## Correctness boundary (slice 1)

The warm resolver is **fully correct** for the dominant workflows:
single-package projects (the `gsx init` scaffold) and leaf edits, where the
edited package's types don't feed another package's resolution. The one known
slice-1 staleness: editing a `.gsx` so a component's **prop signature** changes
while *another* package references `<a.Component>` — the other package keeps the
old view of A until it is itself regenerated (or the resolver is rebuilt). The
rebuild policy already covers the `.go`/go.mod/import triggers; closing this
cross-package-signature edge needs fine-grained invalidation, which is **slice
2**. Until then it is a documented limitation, not a silent wrong answer (a
stale cross-package ref surfaces as a Go build error at most).

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

**In (slice 1):** `--watch` mode (watcher + debounce + `watchSession` warm
regenerate over `gen.CachedResolver` + rebuild policy + instrumentation), the
human + NDJSON renderers, plugin supervision, the recovery-reload wiring, the
measurement e2e.

**Out (deferred):**
- **Slice 2 — fine-grained incremental invalidation** (re-check only the dirty
  package across cross-package signature changes, instead of rebuilding the
  whole resolver). Closes the correctness-boundary edge above. Gated on slice-1
  measurements.
- Daemon auto-restart-on-crash in the plugin.
- Any IPC beyond NDJSON-on-stdout (no socket, no request/response).
- Watching outside the module / cross-module.

## Decisions resolved during design

- **Watch `*.gsx` and non-generated `*.go`** (not `.gsx`-only). A `.go`/go.mod
  change both invalidates the cached resolver *and* can change generated output,
  so it must trigger a rebuild + regenerate of the affected package(s). The cost
  (extra cycles during ordinary Go editing) is bounded by the debounce window.
- **Warm engine is `gen.CachedResolver`**, not a warm wrapper around the cold
  `Generate(paths)` path — reusing the proven playground engine, with a
  whole-module pre-load and a rebuild policy for the cases the playground's fixed
  allowlist never hit.
