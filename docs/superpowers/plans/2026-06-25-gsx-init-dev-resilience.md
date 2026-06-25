# gsx init Dev-Resilience Template Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update the `gsx init` `simple` template to serve logos as static files, expose a `/healthz`, tee a combined `tmp/dev.log`, and wire the `@gsxhq/vite-plugin-gsx` v0.2.0 `devFallback` (backend-restart interstitial).

**Architecture:** Two changes to the (merged) `gsx init` template. (1) Logos move from inlined `gsx.Raw` consts to real `public/*.svg` files that Go embeds + serves at `/public/`, plus a `/healthz` liveness endpoint. (2) `vite.config` wires `devFallback` (plugin + proxy `configure`), the proxy regex excludes `/__dev`, and the Taskfile tees combined output to `tmp/dev.log`.

**Tech Stack:** Go (`gen/templates/init/simple/*`), Vite config, Taskfile; consumes `@gsxhq/vite-plugin-gsx@^0.2.0`.

**Spec:** `docs/superpowers/specs/2026-06-25-gsx-dev-fallback-design.md` (sub-project B).

## Global Constraints

- Templates use `«»` delims; `.tmpl` strip + `dot-`→`.` filename rules. New files: `public/vite.svg`, `public/gsx.svg` (plain SVG, no `«»`, no `.tmpl`). Removed: `logos.go.tmpl`.
- `gsx.svg` braces MUST be vector paths (a monospace `<text>` `{}` renders as square-bracket-like glyphs). Use the verified SVG in Task 1.
- `main.go` adds `//go:embed all:public` + serves `/public/` via `http.FileServerFS` and `GET /healthz` → 200. `app.gsx` uses `<img src="/public/…" class="logo">` (drops `gsx.Raw` + the `github.com/gsxhq/gsx` import). `vite.config` adds `publicDir: false`.
- `vite.config` imports `{ gsx, devFallback }`, builds `devFallback({ target: \`http://localhost:${goPort}\`, logFile: "tmp/dev.log" })`, adds `fallback.plugin` + `configure: fallback.configureProxy`, and the proxy regex gains `/__dev`. `package.json` devDep → `@gsxhq/vite-plugin-gsx ^0.2.0`.
- Taskfile tees combined output to `tmp/dev.log` (seeded once via an internal `dev:setup` dep); terminal keeps `output: prefixed`. `/tmp` is already gitignored.

---

### Task 1: Static logos + `/healthz`

**Files:**
- Create: `gen/templates/init/simple/public/vite.svg`
- Create: `gen/templates/init/simple/public/gsx.svg`
- Modify: `gen/templates/init/simple/main.go.tmpl`
- Modify: `gen/templates/init/simple/app.gsx`
- Modify: `gen/templates/init/simple/vite.config.ts`
- Delete: `gen/templates/init/simple/logos.go.tmpl`
- Modify: `gen/init_test.go` (render-test file list)

**Interfaces:**
- Consumes: the existing scaffold writer / compile gate.

- [ ] **Step 1: Write `public/vite.svg`** (the official Vite mark)

```
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" aria-hidden="true" role="img" class="iconify iconify--logos" width="31.88" height="32" preserveAspectRatio="xMidYMid meet" viewBox="0 0 256 257"><defs><linearGradient id="IconifyId1813088fe1fbc01fb466" x1="-.828%" x2="57.636%" y1="7.652%" y2="78.411%"><stop offset="0%" stop-color="#41D1FF"></stop><stop offset="100%" stop-color="#BD34FE"></stop></linearGradient><linearGradient id="IconifyId1813088fe1fbc01fb467" x1="43.376%" x2="50.316%" y1="2.242%" y2="89.03%"><stop offset="0%" stop-color="#FFEA83"></stop><stop offset="8.333%" stop-color="#FFDD35"></stop><stop offset="100%" stop-color="#FFA800"></stop></linearGradient></defs><path fill="url(#IconifyId1813088fe1fbc01fb466)" d="M255.153 37.938L134.897 252.976c-2.483 4.44-8.862 4.466-11.382.048L.875 37.958c-2.746-4.814 1.371-10.646 6.827-9.67l120.385 21.517a6.537 6.537 0 0 0 2.322-.004l117.867-21.483c5.438-.991 9.574 4.796 6.877 9.62Z"></path><path fill="url(#IconifyId1813088fe1fbc01fb467)" d="M185.432.063L96.44 17.501a3.268 3.268 0 0 0-2.634 3.014l-5.474 92.456a3.268 3.268 0 0 0 3.997 3.378l24.777-5.718c2.318-.535 4.413 1.507 3.936 3.838l-7.361 36.047c-.495 2.426 1.782 4.5 4.151 3.78l15.304-4.649c2.372-.72 4.652 1.36 4.15 3.788l-11.698 56.621c-.732 3.542 3.979 5.473 5.943 2.437l1.313-2.028l72.516-144.72c1.215-2.423-.88-5.186-3.54-4.672l-25.505 4.922c-2.396.462-4.435-1.77-3.759-4.114l16.646-57.705c.677-2.35-1.37-4.583-3.769-4.113Z"></path></svg>
```

- [ ] **Step 2: Write `public/gsx.svg`** (verified vector curly braces + mono `gsx`)

```
<svg width="180" height="64" viewBox="0 0 180 64" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="gsx logo">
  <g fill="none" stroke="#5c6b7a" stroke-width="5" stroke-linecap="round" stroke-linejoin="round">
    <path d="M 34 12 q -11 0 -11 11 l 0 7 q 0 6 -8 6 q 8 0 8 6 l 0 7 q 0 11 11 11"/>
    <path d="M 146 12 q 11 0 11 11 l 0 7 q 0 6 8 6 q -8 0 -8 6 l 0 7 q 0 11 -11 11"/>
  </g>
  <text x="90" y="34" dominant-baseline="central" text-anchor="middle" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" font-size="40" font-weight="700" fill="#2c7da0">gsx</text>
</svg>
```

- [ ] **Step 3: Replace `app.gsx`** (logos via `<img>`, drop the gsx import)

```
package main

import "github.com/gsxhq/vite"

component Layout(title string) {
  <!DOCTYPE html>
  <html lang="en">
    <head>
      <meta charset="UTF-8" />
      <meta name="viewport" content="width=device-width, initial-scale=1.0" />
      <title>{title}</title>
      {{ assets := vite.FromContext(ctx).Entry("web/main.js") }}
      { for _, href := range assets.CSS { <link rel="stylesheet" href={href} /> } }
      { for _, src := range assets.Preloads { <link rel="modulepreload" href={src} /> } }
      { for _, src := range assets.JS { <script type="module" src={src}></script> } }
    </head>
    <body>{children}</body>
  </html>
}

component Index(title string) {
  <Layout title={title}>
    <div id="app">
      <a href="https://vite.dev" target="_blank" rel="noreferrer"><img src="/public/vite.svg" class="logo" alt="Vite logo" /></a>
      <a href="https://github.com/gsxhq/gsx" target="_blank" rel="noreferrer"><img src="/public/gsx.svg" class="logo gsx" alt="gsx logo" /></a>
      <h1>gsx + Vite</h1>
      <div class="card">
        <button id="counter" type="button">count is 0</button>
      </div>
      <p class="read-the-docs">
        Edit <code>app.gsx</code> and save — the page live-reloads.
      </p>
    </div>
  </Layout>
}
```

- [ ] **Step 4: Delete `logos.go.tmpl`**

Run: `cd /Users/jackieli/personal/gsxhq/gsx && git rm gen/templates/init/simple/logos.go.tmpl`

- [ ] **Step 5: Update `main.go.tmpl`** (embed + serve `/public/` + `/healthz`)

Add a second embed under the existing `distFS`:
```go
//go:embed all:public
var publicFS embed.FS
```
And in the `main` mux setup, BEFORE the `if !v.Dev()` block, add:
```go
	mux := http.NewServeMux()
	mux.Handle("/public/", http.FileServerFS(publicFS))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if !v.Dev() {
		mux.Handle("/static/", v.StaticHandler())
	}
```
(Everything else in `main.go.tmpl` — the `/` handler, graceful shutdown, `GO_PORT` — stays exactly as-is.)

- [ ] **Step 6: Update `vite.config.ts`** (publicDir off — Go owns `public/`)

Add `publicDir: false,` to the returned config object, right after `clearScreen: false,`:
```ts
  return {
    // Don't wipe the terminal — keep the combined Go + Vite log readable.
    clearScreen: false,
    // Go serves the logos from its embedded public/ (at /public/), so Vite must
    // not also process public/ (no dev/prod path ambiguity, no dist duplication).
    publicDir: false,
    plugins: [gsx()],
    server: {
```

- [ ] **Step 7: Update the render test** — `gen/init_test.go`

In `TestScaffoldSimpleTemplate`, **add** `"public/vite.svg"` and `"public/gsx.svg"` to the expected-files list, and **remove** `"logos.go"`.

- [ ] **Step 8: Run the render + compile gates**

Run:
```bash
go test ./gen/ -run 'TestScaffoldSimpleTemplate'
go test ./gen/ -run TestInitScaffoldCompiles   # compiles main.go (public embed + /healthz) + app.gsx (<img>, no gsx.Raw)
go build ./...
```
Expected: render test passes (`public/*.svg` present, `logos.go` gone, no stray delimiters); the scaffold generates `app.x.go` and `go build`s (the `//go:embed all:public` resolves against the scaffolded `public/`); `go build ./...` clean. If gsx rejects the `<img>` markup, fix `app.gsx` and re-run.

- [ ] **Step 9: Commit**

```bash
git add gen/templates/init/simple gen/init_test.go
git commit -m "feat(init): logos as embedded static files (/public) + /healthz"
```

---

### Task 2: Wire devFallback + central log

**Files:**
- Modify: `gen/templates/init/simple/vite.config.ts`
- Modify: `gen/templates/init/simple/package.json.tmpl`
- Modify: `gen/templates/init/simple/Taskfile.yml`

**Interfaces:**
- Consumes: `devFallback` from `@gsxhq/vite-plugin-gsx@^0.2.0` (sub-project A); the Task-1 `vite.config` (with `publicDir: false`).

- [ ] **Step 1: Wire `devFallback` in `vite.config.ts`**

Update the import, build the fallback, add it to `plugins`, add `/__dev` to the proxy regex, and add `configure`:
```ts
import { defineConfig, loadEnv } from "vite";
import { gsx, devFallback } from "@gsxhq/vite-plugin-gsx";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const goPort = env.GO_PORT || "7777";
  const vitePort = parseInt(env.VITE_PORT || "5173", 10);
  // Serve a self-recovering interstitial (tails tmp/dev.log, polls /__dev/status)
  // while the Go server is down/restarting, instead of a raw proxy error.
  const fallback = devFallback({ target: `http://localhost:${goPort}`, logFile: "tmp/dev.log" });
  return {
    clearScreen: false,
    publicDir: false,
    plugins: [gsx(), fallback.plugin],
    server: {
      port: vitePort,
      proxy: {
        // Everything except Vite-owned namespaces (and /__dev/status, served by
        // the fallback plugin) goes to the Go server. No `ws: true` — the Go
        // server has no WebSocket; proxying ws would capture Vite's HMR socket.
        "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload|/__dev).*": {
          target: `http://localhost:${goPort}`,
          changeOrigin: true,
          configure: fallback.configureProxy,
        },
      },
    },
    build: {
      manifest: true,
      outDir: "dist",
      rollupOptions: { input: "web/main.js" },
    },
  };
});
```

- [ ] **Step 2: Bump the plugin devDep in `package.json.tmpl`**

Change `"@gsxhq/vite-plugin-gsx": "^0.1.0"` to `"@gsxhq/vite-plugin-gsx": "^0.2.0"`.

- [ ] **Step 3: Replace `Taskfile.yml`** (tee a combined `tmp/dev.log`)

```yaml
version: '3'

dotenv: ['.env']

# Prefix each parallel task's lines ([dev:vite] / [dev:server]) into one readable
# combined terminal log, and tee the same stream to tmp/dev.log for the
# dev-fallback page to display while the Go server restarts.
output: prefixed

tasks:
  dev:
    desc: Run Vite + the Go server with live reload.
    deps: [dev:vite, dev:server]

  dev:setup:
    internal: true
    cmds:
      - mkdir -p tmp
      - 'echo "=== task dev ===" > tmp/dev.log'

  dev:vite:
    desc: Vite dev server (front door; runs the gsx plugin).
    deps: [dev:setup]
    cmds:
      - 'npm run dev 2>&1 | tee -a tmp/dev.log'

  dev:server:
    desc: Rebuild + restart the Go server on .go changes.
    deps: [dev:setup]
    cmds:
      # Single-quoted: the wgo `::` separator + the pipe are one shell command;
      # unquoted YAML would misparse the colon-space.
      - 'go tool wgo -file=.go -xdir=tmp -xdir=node_modules -xdir=dist go build -o tmp/app . :: tmp/app 2>&1 | tee -a tmp/dev.log'
```

- [ ] **Step 4: Run the Taskfile-parse + render gates**

Run:
```bash
go test ./gen/ -run 'TestInitTaskfileParses|TestScaffoldSimpleTemplate'
go build ./...
```
Expected: `TestInitTaskfileParses` passes (the new Taskfile with `dev:setup`/`tee` parses via `task --list`, or skips if `task` absent); the render test still passes (vite.config/package.json/Taskfile present, no stray delimiters); `go build ./...` clean. (The `vite.config` `devFallback` wiring is validated at runtime by the end-to-end test, which needs `@gsxhq/vite-plugin-gsx@0.2.0` published.)

- [ ] **Step 5: Commit**

```bash
git add gen/templates/init/simple
git commit -m "feat(init): wire devFallback (backend-restart interstitial) + central tmp/dev.log"
```

---

## Final verification (after all tasks)

- [ ] `go test ./gen/` (full, incl. compile + Taskfile-parse gates), `go vet ./gen/`, `go build ./...` — all green.
- [ ] Manual smoke: `go run ./cmd/gsx init /tmp/gsxfb < /dev/null` (non-interactive) writes `public/vite.svg`, `public/gsx.svg`, no `logos.go`; `main.go` has the `public` embed + `/healthz`; `Taskfile.yml` has the `tee`. Clean up.
- [ ] `git log --oneline` shows the two task commits; tree clean.

The **end-to-end test** (requires `@gsxhq/vite-plugin-gsx@0.2.0` published): scaffold → `task dev` → confirm the logos render from `/public/…` as proper curly braces; kill the Go server → the **interstitial** appears with the live `tmp/dev.log` tail; bring Go back → it **auto-reloads**. Assisted via Claude-in-Chrome.
