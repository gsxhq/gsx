# `gsx init` v2 — Interactive + Polished Starter

**Status:** Approved (2026-06-24)
**Goal:** Turn `gsx init` into an `npm create vite`-style experience: interactive,
runs the setup steps for the user, and scaffolds a polished Vite-quality starter
(nice default styles, configurable ports via `.env`).

## Context

`gsx init` (v1, shipped) scaffolds the `simple` template and prints next steps for
the user to run by hand. This enhancement makes the command friendly and the
starter look like a real Vite app. It builds on the v1 design
(`docs/superpowers/specs/2026-06-24-gsx-init-design.md`): same `--template`
registry, same `«»`-delimited template mechanism, same `github.com/gsxhq/vite`
(v0.2.0) integration. This spec changes the **command UX** and the **`simple`
template contents** — not the registry/writer architecture.

## A. Interactive setup flow

`runInit` gains an interactive path, gated on whether **stdin is a TTY**:

- **Interactive (TTY):**
  1. If no `[dir]`/project name was given, prompt for it (default `gsx-app`).
  2. Scaffold the template (unchanged writer).
  3. For each setup step, print the command and ask `Run this? [Y/n]`, then run it
     streaming output. Steps, in order:
     - `go get -tool github.com/gsxhq/gsx/cmd/gsx@latest`
     - `go mod tidy`
     - `npm install`
     On a declined step, skip it and continue (note it in the final summary). On a
     **failed** step (non-zero exit), stop, print the error and the remaining
     commands for the user to run manually, and exit 1.
  4. Print: `✓ Done!  Run: task dev` (and `cd <dir>` first when not `.`).
- **`--yes` / `-y`:** run all steps without prompting (unattended; still TTY-or-not).
- **Non-TTY (CI, pipes, the existing tests) without `--yes`:** the v1 behavior —
  scaffold + print the steps, run nothing. This keeps CI/tests deterministic.

`gsx init` never launches `task dev` (it is long-running); the browser is opened
by the dev server itself (§B).

**Testability:** the side-effecting parts are injected, so tests need no real TTY
or subprocess:
- a `prompt(question) (yes bool)` reading from an `io.Reader` (real: stdin),
- a `runStep(cmd []string, dir string) error` (real: `exec.Command` with streamed
  output; tests: a stub recording invocations and returning scripted errors),
- an `interactive bool` computed from `isTTY(stdin)` (overridable in tests).

`runInit` stays the orchestrator; the loop over the three steps calls `prompt`
then `runStep`. New flags: `-y`/`--yes`.

## B. Browser opens via the dev server

The scaffold's `package.json` `dev` script is `vite --open`, so `task dev` (→
`npm run dev`) opens the browser to the Vite front door. No browser handling in
`gsx init` itself.

## C. Configurable ports via `.env` (Vite best practice)

Ports move out of hardcoded literals into env, loaded the Vite way:

- **`.env.example`** (committed) and **`.env`** (scaffold writes an initial copy
  with the same values) contain:
  ```
  GO_PORT=7777
  VITE_PORT=5173
  VITE_DEV_URL=http://localhost:5173
  ```
  The template `.gitignore` adds `.env` (so the local copy stays out of the user's
  git; `.env.example` is the committed reference).
- **`vite.config.ts`** reads them with Vite's `loadEnv`:
  ```ts
  import { defineConfig, loadEnv } from "vite";
  import { gsx } from "@gsxhq/vite-plugin-gsx";

  export default defineConfig(({ mode }) => {
    const env = loadEnv(mode, process.cwd(), "");
    const goPort = env.GO_PORT || "7777";
    return {
      plugins: [gsx()],
      server: {
        port: Number(env.VITE_PORT || 5173),
        proxy: {
          "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload).*": {
            target: `http://localhost:${goPort}`,
            changeOrigin: true,
            ws: true,
          },
        },
      },
      build: { manifest: true, outDir: "dist", rollupOptions: { input: "web/main.js" } },
    };
  });
  ```
- **`main.go`** reads the Go port and dev URL from env, with defaults:
  ```go
  devURL := os.Getenv("VITE_DEV_URL")        // "" in prod
  port := cmp.Or(os.Getenv("GO_PORT"), "7777")
  // ... http.ListenAndServe(":"+port, v.Middleware(mux))
  ```
- **`Taskfile.yml`** loads `.env` so the Go process gets the vars (no Go env-lib
  dependency):
  ```yaml
  version: '3'
  dotenv: ['.env']
  tasks:
    dev:
      deps: [dev:vite, dev:server]
    dev:vite:
      cmds: ['npm run dev']
    dev:server:
      cmds:
        - 'go tool wgo -file=.go -xdir=tmp -xdir=node_modules -xdir=dist go build -o tmp/app . :: tmp/app'
  ```
  (The `dev:server` env block from v1 is replaced by the global `dotenv`. The wgo
  command stays single-quoted — the `::` colon-space YAML fix from v1.)

Bonus: port collisions (like the e2e hit) are now a one-line `.env` edit.

## D. Polished look (Vite `vanilla` + gsx)

Adapt the official `create-vite` `vanilla` template (MIT) to gsx SSR. Because the
page is server-rendered, the logos are **inline SVG** in the gsx markup (no Vite
`public/`/manifest path needed — works identically in dev and prod).

- **`web/style.css`** — vanilla's `style.css` verbatim (light/dark via
  `prefers-color-scheme`, `#app` centered layout, `.logo` hover-glow, `.card`,
  `button`, `.read-the-docs`).
- **`app.gsx`** — `Index` renders a `<div id="app">` containing: a Vite-logo
  `<a>`/inline-SVG, a `{gsx}`-logo `<a href="https://github.com/gsxhq/gsx">`/inline
  SVG, `<h1>gsx + Vite</h1>`, a `<div class="card"><button id="counter"
  type="button">count is 0</button></div>`, and `<p class="read-the-docs">Edit
  <code>app.gsx</code> and save — it live-reloads.</p>`. `Layout` is unchanged
  (it still pulls the asset bundle from context and renders `<head>` tags).
- **`web/main.js`** — `import "./style.css"; import { setupCounter } from
  "./counter.js"; setupCounter(document.querySelector("#counter"));`
- **`web/counter.js`** — the vanilla `setupCounter` (sets `count is N`).
- **The `{gsx}` logo** — an inline `<svg>` `<text>` wordmark matching the site
  brand: slate braces `{` `}` (`~#5c6b7a`) around bold teal `gsx` (`~#2c7da0`),
  with the `.logo` class so it gets the hover-glow. The Vite logo is its official
  SVG, inlined.

This shows gsx SSR (the markup), client JS (the counter), and Vite HMR (editing
`style.css`/`counter.js` hot-updates; editing `app.gsx` full-reloads) in one page.

## E. Testing

- **Unchanged:** the v1 non-interactive tests (no TTY → fallback path) and the
  unknown-template / `--force` / flag-position tests.
- **New — interactive runner:** drive `runInit` with `interactive=true`, a scripted
  stdin reader (e.g. `"y\ny\ny\n"` / `"n\n…"`), and a stub `runStep` that records
  the commands and returns success or a scripted error. Assert: (1) all three
  commands run in order on all-yes; (2) a `n` skips that command; (3) a failing
  step stops the sequence and the remaining commands are printed; (4) the final
  `Run: task dev` line prints on success.
- **Template gates still apply:** the render test asserts the new files exist
  (`.env.example`, `.env`, `web/counter.js`, the logos) with substitution and no
  stray `«»`; the scaffold-compiles gate compiles the richer `main.go`
  (`GO_PORT`/`cmp.Or`) + generated `app.x.go`. A lightweight check parses the
  scaffolded `Taskfile.yml` is out of scope for unit tests but is covered by the
  manual/e2e smoke.

## Out of scope (YAGNI)

- No TUI framework — plain stdlib prompts (`bufio` over stdin, `[Y/n]`).
- No arrow-key template selection (one template; a numbered prompt is enough when
  a second template lands).
- No Vite `public/` asset pipeline for SSR (logos are inline SVG); serving
  `public/` favicons/images in prod is a documented future step.
- `gsx init` still does not run `task dev` or generate; the dev server (via the
  plugin's `generateOnStart`) performs the first generate.
