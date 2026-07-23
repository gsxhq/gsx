# Dev panel + front-door resilience

Browser dev panel (vite-overlay-style) toggled by Cmd-D/Ctrl-D with Rebuild /
Restart-server buttons and a live status view, plus auto-restart of the managed
front door. Spans `gsx` (`gen/`) and `vite-plugin-gsx`. Builds on branch
`dev-event-fixes` (the `webExited` push gate).

## Problem

The dev loop is opaque and uncontrollable from the browser: no way to force a
rebuild or restart the Go server without touching the terminal, and no
visibility into dev-loop state (the front-door incident surfaced as "tabs
reload at random" instead of "front door exited 20 minutes ago"). When the
managed front door dies, the session stays half-alive with pushes suspended.

The panel is served *by* the front door, so it cannot fix front-door death —
auto-restart in `gsx dev` covers that; the panel covers control + visibility.

## Protocol

Three parties: panel (browser) ↔ vite plugin ↔ `gsx dev`. `gsx dev` keeps a
single outbound relationship with vite — no new listeners or ports; restarts on
either side heal by reconnection.

**Commands (browser → gsx dev).** Panel sends
`import.meta.hot.send('gsx:cmd', {cmd})`. Plugin appends to a FIFO mailbox
(cap 16, consecutive duplicates collapsed). `gsx dev` long-polls
`GET {viteURL}/__gsx/cmd?wait=25s`: immediate `200 {"cmds":[…]}` when the
mailbox is non-empty, `204` on timeout. Commands: `rebuild`, `restart-server`.
Unknown commands are logged and dropped.

**Status (gsx dev → browser).** New `{"event":"status"}` payload on the
existing `/__gsx/event` POST:

- `phase`: `idle` | `generating` | `building` | `starting`
- Go server: healthy/unhealthy + port
- last cycle: ok, error count, timestamp
- front door: `up` | `restarting` | `given-up` | `external`, restart count

Posted on startup, after every cycle, and on server/front-door transitions.
Plugin caches the latest, replays it to each new ws client (same pattern as
the error-overlay replay), broadcasts `gsx:status` on change.

## gsx dev (`gen/`)

- **Command intake goroutine**: long-poll loop (stdlib HTTP), gated by `webUp`
  like pushes, backoff while the front door is down. Feeds `cmds chan
  devCommand`, consumed by the existing select loop like watcher events:
  - `rebuild`: force full dirty-all regenerate → build → restart → reload.
  - `restart-server`: stop + restart current binary (no rebuild), then reload.
- **Status struct** maintained in the loop, posted via the existing `post()`.
- **Front-door auto-restart**: on unexpected exit (not `shuttingDown`) the
  monitor respawns vite with backoff 500ms → 2s → 5s. Three failed restart
  attempts (every instance lived < 30s) = crash-looping: give up, log, fall
  back to suspend-pushes.
  - Refactor: `webExited` becomes per-instance; push/poll gate reads the
    current instance's state; shutdown kills the current instance.
  - **Respawn verification**: a respawned vite can fail to bind (port taken by
    a foreign process → posting would recreate the original incident) or
    drift to port+1 (vite auto-increments without `strictPort`, making the
    startup-resolved URL stale). After a respawn the push/poll gate therefore
    stays shut until a probe of `GET {viteURL}/__gsx/cmd?wait=0` returns the
    `x-gsx: 1` response header (stamped by our plugin; a foreign listener or
    SPA fallback lacks it). An instance that never verifies within ~5s is
    killed and counted as a rapid exit. The first instance keeps today's
    gate-open-from-start semantics (`portAvailable` vetted its port;
    `postBest` retries cover the cold start).

## vite-plugin-gsx

- `src/client.ts`: custom element, shadow DOM (vite's overlay technique).
  Cmd-D/Ctrl-D toggles (`preventDefault`; ignored when focus is in
  input/textarea/contenteditable). Buttons disable while a command is in
  flight, re-arm on the next status event — and stay disabled until the FIRST
  status event arrives ("waiting for gsx dev…"), so the panel degrades
  honestly in daemon/standalone-vite mode where nothing consumes commands.
- **Delivery — explicit entry import** (gsx apps have no vite `index.html`;
  their HTML streams from the Go server, so `transformIndexHtml` can never
  fire; and a raw-served script would lack vite's HMR transform). The app's
  client entry imports `virtual:gsx-devpanel`; the `gsx init` template ships
  the line, existing apps add it (documented in the dev-loop guide). `gsx()`
  returns a plugin array: the serve-only main plugin, plus a tiny
  always-applied resolver that maps the virtual id to the built panel client
  in dev (transformed by vite → real `import.meta.hot`) and to an empty
  module in prod builds (the main plugin is `apply: "serve"`, so without the
  resolver a `vite build` of the entry import would fail). The
  `github.com/gsxhq/vite` Go helper is deliberately untouched — no coupling
  from the URL-resolver lib to this plugin. (Monkey-patching the served
  `@vite/client` was considered and rejected: vite-internal seam, also loads
  in web workers; see vitejs/vite#17644.)
- Server side: `/__gsx/cmd` long-poll middleware + mailbox,
  `server.ws.on('gsx:cmd')` intake, status cache + broadcast. All `/__gsx/cmd`
  responses (200 and 204) carry the `x-gsx: 1` header — the respawn
  verification handshake.
- `--no-web`: an externally-run vite loads the plugin, so panel and mailbox
  work identically; auto-restart is N/A (front door reported `external`).

## Security

Dev-only, localhost. Foreign origins cannot read cross-origin responses, but a
hostile local page CAN fire no-cors GETs at `/__gsx/cmd`, displacing gsx dev's
long-poll and eating queued commands — a dev-only denial-of-service nuisance,
no data exposure; both commands are non-destructive. Accepted for v1. The
planned follow-up (per-session token in the front door's child env, echoed as
the `x-gsx` header value) closes both this and the gsx-vs-gsx respawn
verification gap.

## Testing

- gsx unit: long-poll client against a fake vite (immediate, delayed, 204,
  down/backoff, gate-suspended cases).
- gsx integration (`gen/dev_test.go`, recorder pattern from
  `TestDevStopsPostingAfterWebExit`): mailbox command → observable cycle
  (`/gen2` trick); front door killed → respawned stub observed; crash-loop →
  give-up; status events observed at the recorder.
- plugin (vitest): mailbox semantics (cap, dedupe, drain), long-poll endpoint,
  ws intake, status cache/replay. Panel client stays logic-lean.
- No corpus changes (no syntax change). Docs: dev-loop guide section.

## Out of scope

- Panel actions beyond `rebuild` / `restart-server` (e.g. reload-browser
  button, log tail).
- Any production-build presence of the panel.
- Authenticating the command channel (see Security).
