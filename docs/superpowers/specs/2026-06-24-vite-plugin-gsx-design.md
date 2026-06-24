# `@gsxhq/vite-plugin-gsx` — Design

**Status:** Approved (2026-06-24)
**Goal:** A dev-only Vite plugin that integrates gsx's `.gsx → .x.go` compile
into the Vite dev loop: watch `.gsx`, regenerate, surface gsx diagnostics as a
Vite error overlay, and own the browser full-reload — so a Go-SSR gsx app gets
live reload with minimal wiring.

## Context

gsx is a JSX-like HTML templating compiler for Go: `.gsx` files compile to
`.x.go` Go source that is linked into the server binary (the same shape as
[templ](https://templ.guide): `.templ → _templ.go`). Because the HTML is
rendered **server-side**, "live reload" is not a JS hot-swap — it inherently
requires **regenerate → `go build` → restart server → reload the browser**.

A proven reference for this loop exists in a real project (Go SSR + Vite):
- Vite is the dev front door; a `server.proxy` rule forwards non-Vite routes to
  the Go backend, and Vite's HMR WebSocket (`@vite/client`, injected into the
  server-rendered page) stays connected across Go rebuilds.
- A generic file watcher (`wgo`) rebuilds+restarts the Go binary on `.go`
  changes; a second watcher runs the template codegen on `.templ` changes.
- After the rebuilt Go binary boots, it POSTs `/__reload` to a tiny Vite plugin,
  which broadcasts `{ type: 'full-reload' }` over the existing WS. Posting
  **after boot** is what makes the reload correct (it never reloads against the
  stale binary).

This plugin productizes that loop for gsx. Building it is preferred over a
`gsx generate --watch` subcommand: a `--watch` mode could only own the
regenerate step (it cannot rebuild Go, restart the server, or deliver a browser
reload), so it would duplicate mature tools (`wgo`/`air`, Vite's WS) while
solving none of the hard parts.

## Scope decision

The plugin is a **sidecar that runs `gsx generate` and owns the browser
reload**. It does **not** manage the Go process (that stays with the user's
`wgo`/`air`), and it does **not** ship a companion Go library — the Go-side
reload glue is small, lives naturally in the user's app/template, and is
documented rather than packaged. Core gsx is untouched by this project.

## The dev loop (data flow)

```
vite starts → plugin (configureServer):
    register /__reload middleware · initial generate · watch **/*.gsx
   │
you save Foo.gsx
   │  Vite watcher → debounce → `go tool gsx generate --json .`
   ├─ success → writes Foo.x.go (gsx content-hash cache skips unchanged);
   │            plugin does NOT broadcast reload
   └─ failure → parse --json diagnostics → ws.send({type:'error', err})
                error overlay + log via Vite's logger
   │
your wgo/air (separate watcher on .go) sees new Foo.x.go → `go build` + restart
   │
Go boots → main() POSTs <viteURL>/__reload   ← documented project glue
   │
plugin /__reload middleware → ws.send({type:'full-reload', path:'*'})
   │
browser reloads against the new binary; the overlay clears on reload
```

**Key invariant:** the plugin never broadcasts a reload as a result of a
successful `gsx generate`. Reload is driven solely by the Go-POST-after-boot, so
the browser only refreshes once the new binary is actually listening. This is
the race that a naive "reload on file change" design gets wrong.

## Components

A single ESM npm package, TypeScript source compiled to ESM + `.d.ts`.

- **`src/index.ts`** — the plugin factory `gsx(options?): Plugin`. `name:
  "vite-plugin-gsx"`, `apply: "serve"`. Implements `configureServer(server)`:
  - registers the `/__reload` middleware (POST → `server.ws.send({ type:
    "full-reload", path: "*" })`, respond 204; non-POST → 405);
  - if `generateOnStart`, runs one generate so `.x.go` exist on first boot;
  - watches the configured `.gsx` globs (via `server.watcher`), and on
    `change`/`add`/`unlink` debounces and runs a generate;
  - on generate failure, pushes the error overlay; on success, clears its
    failed state (the overlay is cleared by the subsequent reload).
- **`src/generate.ts`** — `runGenerate(opts): Promise<{ ok: boolean;
  diagnostics: GsxDiagnostic[]; raw: string }>`. Spawns `command` + `["--json",
  ...paths]` in `cwd`, captures stdout/stderr. Exit 0 → ok; non-zero → parse the
  JSON diagnostics array from stdout. A spawn failure (e.g. `go` not found, or
  the `gsx` tool directive missing) returns a synthetic diagnostic with a clear
  remediation message.
- **`src/diagnostics.ts`** — the gsx-JSON type and a **pure** mapper to Vite's
  `ErrorPayload["err"]`. Pure so it is trivially unit-tested.
- **`src/options.ts`** — `GsxOptions` and `resolveOptions(user, viteConfig)`
  applying defaults.

## Options API

```ts
interface GsxOptions {
  /** Command + leading args to invoke gsx. Default: ["go","tool","gsx","generate"]. */
  command?: string[];
  /** Path args passed to generate. Default: ["."] — regenerate the whole
   *  module each save; gsx's content-hash cache skips unchanged packages, so
   *  this is cheap and avoids changed-file→package mapping. */
  paths?: string[];
  /** Globs whose changes trigger regeneration. Default: all `.gsx` files
   *  (the `*.gsx` recursive glob). */
  watch?: string | string[];
  /** Working directory for the command. Default: Vite config `root`. */
  cwd?: string;
  /** Endpoint the Go server POSTs to trigger reload. Default: "/__reload". */
  reloadEndpoint?: string;
  /** Debounce window for rapid saves, ms. Default: 50. */
  debounce?: number;
  /** Run an initial generate when the dev server starts. Default: true. */
  generateOnStart?: boolean;
}
```

The default `command` is `go tool gsx generate` — the recommended invocation,
which version-pins gsx via the `tool` directive in `go.mod` and needs no global
install. Projects with a custom `cmd/gsx` (extension model) override to e.g.
`["go","run","./cmd/gsx","generate"]`.

## Error overlay mapping

`gsx generate --json` emits a JSON array of diagnostics (1-based positions):

```jsonc
{
  "file": "views/foo.gsx",
  "range": { "start": {"line": 12, "col": 5}, "end": {"line": 12, "col": 9} },
  "severity": "error",          // also "warning" etc.
  "code": "syntax",             // optional
  "message": "mismatched close tag",
  "help": "…",                  // optional
  "source": "…"                 // optional
}
```

Mapping for each error-severity diagnostic → Vite `ErrorPayload["err"]`:
- `message` = `code ? \`${code}: ${message}\` : message`, with `help` appended
  after a blank line when present;
- `loc` = `{ file, line: range.start.line, column: range.start.col }`;
- `id` = `file`;
- `frame` = the source line read from `file` plus a `^` caret under
  `range.start.col` (Vite renders this as the code frame);
- `plugin` = `"vite-plugin-gsx"`.

The first error becomes the overlay payload (`ws.send({ type: "error", err })`);
all diagnostics are logged via `server.config.logger.error`. Warnings alone do
not raise an overlay.

## Documented project glue (NOT in the package)

The README documents the three app-side pieces that complete the loop, since
they live in the user's project, not the plugin:

1. **Proxy** — a `server.proxy` rule in the user's `vite.config` forwarding
   non-Vite routes to the Go backend (`ws: true`), making Vite the dev front
   door so the injected `@vite/client` socket survives Go rebuilds.
2. **Client script** — `<script type="module" src="/@vite/client"></script>` in
   the layout `<head>`, dev-gated (e.g. behind an env check), so server-rendered
   pages hold the Vite WS.
3. **Reload notify** — a ~15-line helper the Go server calls on boot that POSTs
   `<viteURL>/__reload` (with a brief retry to cover the cold-start race where
   Go wins against Vite). Dev-only by construction (no-ops when the Vite URL env
   is unset).

## Packaging & testing

- **Name:** `@gsxhq/vite-plugin-gsx` (scoped to the org). **Repo:**
  `gsxhq/vite-plugin-gsx` (new, separate — mirrors the tree-sitter-gsx
  separate-repo precedent). Published to npm.
- **Build:** TypeScript → ESM + type declarations (`tsup` or `tsc`).
- **Tests (`vitest`):**
  - `diagnostics.test.ts` — pure mapper: gsx JSON → overlay payload, including
    `code`-prefixing, `help` appending, caret frame, and the missing-`code`
    case.
  - `generate.test.ts` — `runGenerate` against a **fake `gsx` shell script**
    that emits a known JSON array (failure) or exits 0 (success), plus the
    spawn-failure remediation path.
  - `index.test.ts` — boot Vite in `middlewareMode` with the plugin + the fake
    gsx: assert `POST /__reload` triggers a `full-reload` broadcast (spy on
    `server.ws.send`), and that a failing generate sends an `error` payload.

## Out of scope (YAGNI)

- Managing the Go process (build/run/restart) — that is `wgo`/`air`.
- The proxy configuration — that is the user's `vite.config`.
- Partial/granular HMR — server-rendered HTML only supports full-reload.
- Production-build integration — `//go:generate` / CI owns generate for builds;
  the plugin is `apply: "serve"` only.
- Any change to core gsx.
- A templ-style "load template text from disk in dev to skip `go build`" mode —
  a large codegen change, deferred.
