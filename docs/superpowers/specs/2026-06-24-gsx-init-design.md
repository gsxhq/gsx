# `gsx init` — Design

**Status:** Approved in pieces (2026-06-24); this doc consolidates for review.
**Goal:** A `gsx init` subcommand that scaffolds a complete, working Vite
dev-loop gsx web app — `task dev` gives live reload immediately, and the same
templates build for production with content-hashed assets.

## Context

The gsx extension/dev story has three pieces, two of which now ship:
1. **`@gsxhq/vite-plugin-gsx`** (npm, v0.1.0) — watches `.gsx`, regenerates,
   error overlay, `/__reload` receiver.
2. **`github.com/gsxhq/vite`** (Go, v0.1.0) — generic manifest resolver +
   dev/prod asset switch + `/static` server + `NotifyReload`.
3. **`gsx init`** (this spec) — scaffolds a project that wires 1 + 2 together so
   a newcomer gets the whole loop without hand-assembling it.

This is sub-project 2 of two. It depends on sub-project 1 (`github.com/gsxhq/vite`).

## Command

A new subcommand in the gsx CLI (`gen/` dispatch, alongside generate/fmt/info/…):

```
gsx init [dir]
```
- Scaffolds into `dir` (default `.`). Creates `dir` if absent.
- `--module <path>` — the Go module path. Default: the basename of the absolute
  target dir (e.g. `gsx init myapp` → `module myapp`; `gsx init .` in `~/foo` →
  `module foo`).
- `--force` — proceed even if the target already contains a `go.mod` or
  `package.json`. Without it, an existing `go.mod`/`package.json` is a usage
  error (exit 2) so we never clobber a real project.
- On success, prints next steps: `cd <dir>` (when not `.`), `go mod tidy`,
  `npm install`, `task dev`.

## Template mechanism

Templates are embedded in the gsx binary via `go:embed` (a `gen/templates/init/`
tree) and rendered with `text/template` using **custom delimiters** `«` / `»`
(so the default `{{ }}` never clashes with Go, JS, YAML, or gsx `{ }`/`@{ }`
braces). The only substituted value is the module path (`«.Module»`). Files
whose names would confuse `go:embed` or tooling are stored with a `.tmpl` suffix
and written to their real name (e.g. `go.mod.tmpl` → `go.mod`). A small
`scaffold` function walks the embedded tree, renders each file, and writes it
under the target dir, refusing to overwrite unless `--force`.

## Scaffolded tree

```
<dir>/
  go.mod          module «.Module»; require gsx + gsxhq/vite + wgo; tool gsx; tool wgo
  main.go         net/http server; //go:embed all:dist; vite.New; NotifyReload on boot
  app.gsx         Layout(title, assets) + Index — iterates assets for asset tags
  web/main.js     import "./style.css"   (Vite entry → CSS HMR in dev)
  web/style.css   minimal CSS
  dist/.gitkeep   placeholder so //go:embed all:dist compiles before `vite build`
  vite.config.ts  gsx() plugin + proxy to Go + build.manifest + input web/main.js
  package.json    devDeps: @gsxhq/vite-plugin-gsx ^0.1.0, vite ^6; scripts dev/build
  Taskfile.yml    dev: parallel dev:vite + dev:server (wgo)
  .gitignore      node_modules, dist/* (keep .gitkeep), tmp/, *.x.go
  README.md       prereqs, install, task dev, prod build, how it works
```

### `main.go` (shape)

```go
package main

import (
    "context"
    "embed"
    "log"
    "net/http"
    "os"

    "github.com/gsxhq/vite"
)

//go:embed all:dist
var distFS embed.FS

func main() {
    devURL := os.Getenv("VITE_DEV_URL") // "" in prod
    v, err := vite.New(vite.Config{DevURL: devURL, Dist: distFS, DistDir: "dist"})
    if err != nil {
        log.Fatal(err)
    }
    mux := http.NewServeMux()
    if !v.Dev() {
        mux.Handle("/static/", v.StaticHandler())
    }
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        page := Index(IndexProps{Title: "gsx + Vite", Assets: v.Entry("web/main.js")})
        if err := page.Render(r.Context(), w); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
        }
    })
    vite.NotifyReload(devURL) // dev-only; no-op when devURL == ""
    log.Println("listening on :7777")
    http.ListenAndServe(":7777", mux)
}
```

### `app.gsx` (shape)

The component takes the resolved `vite.Bundle` and renders the asset tags by
iterating — the `.gsx` does not branch on dev/prod (Go computed the lists):

```gsx
package main

import "github.com/gsxhq/vite"

component Layout(title string, assets vite.Bundle) {
  <!DOCTYPE html>
  <html lang="en">
    <head>
      <meta charset="UTF-8" />
      <title>{title}</title>
      for _, href := range assets.CSS { <link rel="stylesheet" href={href} /> }
      for _, src := range assets.Preloads { <link rel="modulepreload" href={src} /> }
      for _, src := range assets.JS { <script type="module" src={src}></script> }
    </head>
    <body>{children}</body>
  </html>
}

component Index(title string, assets vite.Bundle) {
  <Layout title={title} assets={assets}>
    <h1>Hello from gsx + Vite</h1>
    <p>Edit <code>app.gsx</code> and save — the page live-reloads.</p>
  </Layout>
}
```

(The exact prop-passing form — positional vs a generated `IndexProps` struct —
follows gsx's existing component conventions; the implementation plan pins it
against the real codegen, and the scaffold-compiles test is the gate.)

### Dev loop (`task dev`)

- **`dev:vite`** → `npm run dev` (vite): front door, proxies non-Vite routes to
  Go `:7777`, runs the `gsx()` plugin (watch `.gsx` → `go tool gsx generate`).
- **`dev:server`** → `go tool wgo -file=.go -xdir=tmp -xdir=node_modules
  -xdir=dist go build -o tmp/app . :: tmp/app`, with `VITE_DEV_URL` set in the
  task env (e.g. `http://localhost:5173`). `wgo` rebuilds+restarts Go on any
  `.go` change (including the `.x.go` the plugin writes — `.x.go` ends in `.go`).
- **Chain:** edit `app.gsx` → plugin regenerates `.x.go` → wgo rebuilds+restarts
  Go → Go boots → `vite.NotifyReload` POSTs `/__reload` → browser full-reloads.

### Dev vs prod assets

One `dev` boolean (`VITE_DEV_URL != ""`) drives `github.com/gsxhq/vite`:
- **Dev:** `v.Entry("web/main.js")` → `["/@vite/client", "/web/main.js"]`
  (Vite-served, HMR; CSS injected by the JS module).
- **Prod:** `vite build` writes `dist/` (hashed assets + `.vite/manifest.json`);
  `go build` embeds `dist/` via `//go:embed all:dist`; `v.Entry` resolves the
  hashed JS/CSS/preloads from the manifest under `/static/`, served by
  `v.StaticHandler()`.

### Prod build (the teachable two-step, in the README)

```
npm run build   # vite build → dist/ (hashed assets + manifest)
go build        # embeds dist/, serves /static/, reads manifest
```

## Module resolution

The scaffold's `go.mod` `require`s `github.com/gsxhq/gsx`, `github.com/gsxhq/vite`
(v0.1.0, published), and `github.com/bokwoon95/wgo` (tool), plus the `tool`
directives for `github.com/gsxhq/gsx/cmd/gsx` and `github.com/bokwoon95/wgo`. It
emits **no `replace`** — end users run `go mod tidy`. `gsx` itself is resolved as
whatever `go mod tidy` finds for `github.com/gsxhq/gsx` (a published tag once
gsx is tagged; a pseudo-version from the default branch until then — a known
follow-up). For the **local e2e test**, the harness adds `replace` directives to
the local `gsx` (and optionally local `vite`) checkout.

## Testing

- **`gsx init` unit tests** (in `gen/`): scaffold into a `t.TempDir()`; assert
  every expected file exists; assert `go.mod` contains `module <substituted>`;
  assert `--force` is required to overwrite an existing `go.mod` (exit 2 without
  it, success with it); assert the rendered files contain no stray `«`/`»`
  delimiters (proves substitution ran).
- **Scaffold-compiles test:** scaffold into a temp dir, write `replace` directives
  to the local `gsx`/`vite` checkouts, run `go generate ./...` (or `go tool gsx
  generate`) then `go build ./...` to prove the generated project is valid Go.
  Network-gated/optional (needs the module graph); skipped in `-short`.
- **E2E (the "test the flow" deliverable):** scaffold a project, `npm install`,
  `task dev`, drive a browser to the page, edit `app.gsx`, and confirm the browser
  live-reloads — assisted via Claude-in-Chrome (optionally a GIF). Manual, not CI.

## Out of scope (YAGNI)

- No custom `cmd/gsx` / extension wiring — the starter uses the stock
  `go tool gsx` (filters/predicates are an advanced opt-in, mentioned in the
  README, not scaffolded).
- No multi-page routing, no CSS framework (minimal plain CSS), no auth/db.
- No `--template` variants (one opinionated starter; more templates are a later
  feature if wanted).
- `gsx init` does not run `go mod tidy` / `npm install` / generate itself — it
  writes files and prints next steps (offline-friendly, deterministic).
