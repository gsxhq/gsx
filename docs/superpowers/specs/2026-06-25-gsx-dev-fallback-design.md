# gsx Dev Resilience — backend-restart fallback + static logos + central log

**Status:** Approved (2026-06-25)
**Goal:** Make the `gsx init` dev loop resilient and polished: a backend-down
interstitial page (instead of a raw proxy 502) that tails the dev log and
auto-reloads, logos served as static files, and a central combined log the
interstitial can display.

## Context

The `gsx init` `simple` template runs Vite (front door) proxying to a Go server
that `wgo` rebuilds+restarts. During a restart the proxy briefly hits a down
backend; today that surfaces a bare error. his-project solves this with a
`backendGuardPlugin` (interstitial + `/__dev/status`) and a combined `tmp/dev.log`
the interstitial tails. This spec ports that pattern, plus moves the logos from
inlined `gsx.Raw` consts to real static files.

It decomposes into **two sub-projects, built in order**:
- **A. `@gsxhq/vite-plugin-gsx` v0.2.0** — adds a `devFallback` export. The dependency.
- **B. `gsx init` template updates** — static logos + `/healthz` + central log +
  wiring `devFallback`. Consumes A.

## Sub-project A: `devFallback` export (`@gsxhq/vite-plugin-gsx` v0.2.0)

A factory that returns a Vite plugin (for `/__dev/status`) plus a proxy
`configure` hook (serves the interstitial on a proxy error):

```ts
interface DevFallbackOptions {
  target: string;       // Go upstream, e.g. "http://localhost:7777"
  logFile?: string;     // path to the combined dev log; default "tmp/dev.log"
  healthPath?: string;  // backend health endpoint; default "/healthz"
  statusPath?: string;  // status endpoint to register; default "/__dev/status"
}
interface DevFallback {
  plugin: Plugin;                         // registers GET <statusPath> → { up, log }
  configureProxy: (proxy: any) => void;   // proxy.on("error", …) → serve interstitial
}
export function devFallback(opts: DevFallbackOptions): DevFallback;
```

**`plugin`** — `apply: "serve"`; `configureServer` registers `GET <statusPath>`
returning JSON `{ up: boolean, log: string }`:
- `up` ← a `GET <target><healthPath>` with an 800ms timeout; true iff the response
  status is 2xx–4xx (a 5xx or transport error ⇒ down). Registered as a Vite
  middleware so it is answered by Vite, never proxied to the (possibly down) Go.
- `log` ← the last ~20KB of `logFile` (or a short note if unreadable).

**`configureProxy`** — attaches `proxy.on("error", (err, req, res) => …)`:
- If `res` has `writeHead` (an HTTP response): write `503` + the **interstitial**
  HTML (`content-type: text/html`, `cache-control: no-store`).
- Else (`res` is a `net.Socket` from a failed WS upgrade): `res.destroy()` — the
  client reconnects on its own.

**The interstitial** (dark, monospace; adapted from his-project, parameterized by
`statusPath`): `<title>Backend restarting…</title>`, a status line, a `<pre>` log
tail, and `<script type="module" src="/@vite/client">`. Its inline script polls
`<statusPath>` every 1s; on `{ up: true }` it `location.reload()`s; otherwise it
updates the status + log tail. The `@vite/client` also makes the Go server's
`/__reload` push reload it instantly on a clean restart. Dev-only — never in the
prod build.

**Boundaries:** `devFallback` is independent of the `gsx()` regenerate plugin
(separate concern, separate export). Stdlib-only Node (`node:fs`, `node:http`,
`node:path`); no new deps.

**Tests (vitest):**
- `/__dev/status` returns `{ up: true }` against a fake upstream that answers
  `healthPath` 200, and `{ up: false }` when the upstream is down/5xx; `log`
  contains the tail of a temp log file.
- `configureProxy` registers an `error` handler that, given a mock HTTP `res`,
  writes 503 + HTML containing the status path; given a socket-like `res` (no
  `writeHead`), calls `destroy()`.

Bump to **v0.2.0**, publish to npm.

## Sub-project B: `gsx init` template updates

### B1. Logos as static files
- Create `gen/templates/init/simple/public/vite.svg` (the official Vite mark) and
  `public/gsx.svg` (the `{gsx}` wordmark). Plain SVG files, no CSS class (the
  class goes on the `<img>`).
- **`gsx.svg` — the braces MUST be vector paths, not `<text>` braces.** A `<text>`
  `{`/`}` in a monospace font renders as harsh, squared-off glyphs that read as
  square brackets (verified in-browser); vector paths render proper rounded curly
  braces. Use this verified SVG (slate `#5c6b7a` curly-brace paths + teal
  `#2c7da0` monospace `gsx`):
  ```svg
  <svg width="180" height="64" viewBox="0 0 180 64" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="gsx logo">
    <g fill="none" stroke="#5c6b7a" stroke-width="5" stroke-linecap="round" stroke-linejoin="round">
      <path d="M 34 12 q -11 0 -11 11 l 0 7 q 0 6 -8 6 q 8 0 8 6 l 0 7 q 0 11 11 11"/>
      <path d="M 146 12 q 11 0 11 11 l 0 7 q 0 6 8 6 q -8 0 -8 6 l 0 7 q 0 11 -11 11"/>
    </g>
    <text x="90" y="34" dominant-baseline="central" text-anchor="middle" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" font-size="40" font-weight="700" fill="#2c7da0">gsx</text>
  </svg>
  ```
- `main.go`: `//go:embed all:public` → `var publicFS embed.FS`; serve at
  `/public/` with `mux.Handle("/public/", http.FileServerFS(publicFS))` (the
  embed paths are `public/…`, matching the `/public/` URL prefix). Served in dev
  (proxy → Go) and prod alike.
- `app.gsx`: replace the `gsx.Raw(...)` logo calls with
  `<img src="/public/vite.svg" class="logo" alt="Vite logo" />` and
  `<img src="/public/gsx.svg" class="logo gsx" alt="gsx logo" />`. Drop the
  `github.com/gsxhq/gsx` import (no longer needed) — keep `vite`.
- **Delete** `gen/templates/init/simple/logos.go.tmpl`.
- `vite.config.ts`: add `publicDir: false` so Vite doesn't also process `public/`
  (Go solely owns it; no dev/prod path ambiguity, no dist duplication).

### B2. Central combined log
- `Taskfile.yml`: the `dev` task gains `mkdir -p tmp` and seeds the log
  (`echo "=== task dev ===" > tmp/dev.log`); `dev:vite` and `dev:server` pipe
  `2>&1 | tee -a tmp/dev.log`. The terminal still shows the `output: prefixed`
  combined stream; the file is the combined log the interstitial tails. (`/tmp`
  is already gitignored.)

### B3. Go `/healthz`
- `main.go`: `mux.HandleFunc("/healthz", func(w, r) { w.WriteHeader(200) })` — a
  trivial liveness endpoint the interstitial's status check hits.

### B4. Wire `devFallback`
- `vite.config.ts`: import `{ gsx, devFallback }`; build
  `const fallback = devFallback({ target: \`http://localhost:${goPort}\`, logFile: "tmp/dev.log" })`;
  add `fallback.plugin` to `plugins`, and `configure: fallback.configureProxy` to
  the proxy rule (keeping `changeOrigin: true`, no `ws: true`). Bump the
  `@gsxhq/vite-plugin-gsx` devDependency to `^0.2.0`.
- **Add `/__dev` to the proxy's exclusion regex** so the status endpoint is
  answered by the plugin (Vite), not proxied to the possibly-down Go:
  `"^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload|/__dev).*"`. (`/public/`
  stays NON-excluded — it must reach Go, which serves the embedded logos.)

### B5. Tests
- Render test: add `public/vite.svg`, `public/gsx.svg` to the expected files;
  remove `logos.go` from it.
- Scaffold-compiles gate: still compiles `main.go` (now with the `public` embed +
  `/healthz`) and `app.gsx` (now `<img>` tags, no `gsx.Raw`).

## Out of scope (YAGNI)

- No prod use of the interstitial / status (dev-only).
- No hashing/processing of the logo SVGs (static files, served as-is).
- No log rotation (the seed truncates per `task dev` run).
- `devFallback` does not own the proxy rules — the scaffold's `vite.config` keeps
  its explicit proxy block (only adds the `configure` hook).

## Build order & e2e validation

Build + publish **A** (v0.2.0) first, then **B**. E2e: run `task dev`, open the
page; kill the Go server (or trigger a slow rebuild) → the **interstitial appears
with the live `tmp/dev.log` tail**; bring Go back → it **auto-reloads** to the
app. Plus the existing checks (logos render from `/public/…`, no ws errors,
combined prefixed log, reliable restarts).
