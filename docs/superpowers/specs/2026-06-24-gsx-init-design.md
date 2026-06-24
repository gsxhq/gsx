# `gsx init` — Design

**Status:** Approved in pieces (2026-06-24); this doc consolidates for review.
**Goal:** A `gsx init` subcommand that scaffolds a complete, working Vite
dev-loop gsx web app — `task dev` gives live reload immediately, and the same
templates build for production with content-hashed assets.

## Context

The gsx extension/dev story has three pieces, two of which now ship:
1. **`@gsxhq/vite-plugin-gsx`** (npm, v0.1.0) — watches `.gsx`, regenerates,
   error overlay, `/__reload` receiver.
2. **`github.com/gsxhq/vite`** (Go, v0.1.0 shipped; **v0.2.0 adds context
   helpers**, see below) — generic manifest resolver + dev/prod asset switch +
   `/static` server + `NotifyReload` + `NewContext`/`FromContext`/`Middleware`.
3. **`gsx init`** (this spec) — scaffolds a project that wires 1 + 2 together so
   a newcomer gets the whole loop without hand-assembling it.

This is sub-project 2 of two. It depends on sub-project 1 (`github.com/gsxhq/vite`).

## Command

A new subcommand in the gsx CLI (`gen/` dispatch, alongside generate/fmt/info/…):

```
gsx init [dir]
```
- Scaffolds into `dir` (default `.`). Creates `dir` if absent.
- `--template <name>` — which starter to scaffold. Default: `simple`. An unknown
  name (or `--template` with no value resolvable) prints the available templates
  and their descriptions and exits 2.
- `--module <path>` — the Go module path. Default: the basename of the absolute
  target dir (e.g. `gsx init myapp` → `module myapp`; `gsx init .` in `~/foo` →
  `module foo`).
- `--force` — proceed even if the target already contains a `go.mod` or
  `package.json`. Without it, an existing `go.mod`/`package.json` is a usage
  error (exit 2) so we never clobber a real project.
- On success, prints next steps: `cd <dir>` (when not `.`), `go mod tidy`,
  `npm install`, `task dev`.

## Templates and the registry

`gsx init` is built around a small template **registry**, not a single hardcoded
starter, so new starters are added by dropping a template tree and one registry
entry — no command rewiring. Each registry entry has a `name`, a one-line
`description`, the embedded FS subtree it renders, and the list of substituted
values it needs (currently just the module path). The registry also names the
**default** template.

Templates shipped / planned:
- **`simple`** (built now; current default) — a stock `net/http.ServeMux` server.
  The full tree is specified below. Minimal dependencies, easiest to read.
- **`structpages`** (planned, not built in this spec; intended to become a
  first-class option and likely the default once it lands) — a
  [`github.com/jackielii/structpages`](https://github.com/jackielii/structpages)
  struct-routed app whose page `Page()` methods render gsx components, aligned
  with the existing `create-structpages` starter but using gsx instead of templ.
  It slots into the registry as a new `gen/templates/init/structpages/` tree plus
  one registry entry; the dev-loop plumbing (vite plugin, `github.com/gsxhq/vite`,
  Taskfile, dev/prod asset switch) is identical and is the part this spec proves
  out with `simple`.

The registry seam is the deliverable here; `simple` is the concrete template that
exercises it end to end.

## Template mechanism

Templates are embedded in the gsx binary via `go:embed` (a
`gen/templates/init/` tree with one subdir per template, e.g.
`gen/templates/init/simple/`) and rendered with `text/template` using **custom
delimiters** `«` / `»` (so the default `{{ }}` never clashes with Go, JS, YAML,
or gsx `{ }`/`@{ }` braces). The only substituted value is the module path
(`«.Module»`). Files whose names would confuse `go:embed` or tooling are stored
with a `.tmpl` suffix and written to their real name (e.g. `go.mod.tmpl` →
`go.mod`). A small `scaffold` function resolves the chosen template from the
registry, walks that template's embedded subtree, renders each file, and writes
it under the target dir, refusing to overwrite unless `--force`.

## The `simple` template — scaffolded tree

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
        if err := Index(IndexProps{Title: "gsx + Vite"}).Render(r.Context(), w); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
        }
    })
    vite.NotifyReload(devURL) // dev-only; no-op when devURL == ""
    log.Println("listening on :7777")
    // v.Middleware injects *vite.Vite into each request's context, so components
    // read the asset bundle from ctx instead of receiving it as a prop.
    http.ListenAndServe(":7777", v.Middleware(mux))
}
```

### `app.gsx` (shape)

The Layout pulls the resolved `vite.Bundle` from the request **context** (not a
prop) using a `{{ … }}` statement binding, then iterates to render the tags — so
no component below it has to thread assets through. The `.gsx` does not branch on
dev/prod (Go computed the lists):

```gsx
package main

import "github.com/gsxhq/vite"

component Layout(title string) {
  {{ assets := vite.FromContext(ctx).Entry("web/main.js") }}
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

component Index(title string) {
  <Layout title={title}>
    <h1>Hello from gsx + Vite</h1>
    <p>Edit <code>app.gsx</code> and save — the page live-reloads.</p>
  </Layout>
}
```

`ctx` is the render context in scope inside every component body (the generated
closure parameter); `v.Middleware` put `*vite.Vite` there per request. `{{ … }}`
is gsx's Go-statement escape hatch, so `assets` is bound once and the three
`for` loops iterate it. The exact prop form (the generated `IndexProps` struct)
follows gsx's existing conventions; the scaffold-compiles test is the gate.

### Context-based asset access — `gsxhq/vite` v0.2.0 (prerequisite)

This pattern requires a small, generic addition to `github.com/gsxhq/vite`
(built and tagged **v0.2.0** before `gsx init`, since the scaffold's `go.mod`
will `require` it):

```go
// context.go in github.com/gsxhq/vite
func NewContext(ctx context.Context, v *Vite) context.Context // stash *Vite
func FromContext(ctx context.Context) *Vite                   // retrieve (nil if absent)
func (v *Vite) Middleware(next http.Handler) http.Handler     // r = r.WithContext(NewContext(...))
```

It is the standard "dependency in context" pattern (cf. his-project's per-request
asset resolver); keeping it in the generic lib means every Go+Vite app — gsx or
not — gets it, and gsx components stay free of asset plumbing.

### Dev loop (`task dev`)

- **`dev:vite`** → `npm run dev` (vite): front door, proxies non-Vite routes to
  Go `:7777`, runs the `gsx()` plugin (watch `.gsx` → `go tool gsx generate`).
- **`dev:server`** → `go tool wgo -file=.go -xdir=tmp -xdir=node_modules
  -xdir=dist go build -o tmp/app . :: tmp/app`, with `VITE_DEV_URL` set in the
  task env (e.g. `http://localhost:5173`). `wgo` rebuilds+restarts Go on any
  `.go` change (including the `.x.go` the plugin writes — `.x.go` ends in `.go`).
- **Chain:** edit `app.gsx` → plugin regenerates `.x.go` → wgo rebuilds+restarts
  Go → Go boots → `vite.NotifyReload` POSTs `/__reload` → browser full-reloads.

### Dev vs prod Vite loading (must be exactly this)

One `dev` boolean — `VITE_DEV_URL != ""` — drives `github.com/gsxhq/vite`. The
two modes load assets from entirely different origins, so the wiring must be
precise:

**Dev** (`task dev`; two processes):
- **Vite is the front door** on `:5173` (Vite's default). The Go server runs on
  `:7777`. The developer opens `http://localhost:5173/`.
- Vite's `server.proxy` forwards every route **except Vite-owned namespaces** to
  Go `:7777` with `ws: true`. Vite-owned (NOT proxied): `/@vite`, `/@id`,
  `/@fs`, `/web/` (the entry + source under the project root), `/node_modules`,
  and `/__reload` (the plugin endpoint). So `GET /` is proxied to Go; the page's
  asset URLs are served by Vite.
- Go runs with `VITE_DEV_URL=http://localhost:5173` in its env (set by the
  Taskfile's `dev:server`). So `v.Dev()` is true; `v.Entry("web/main.js")` →
  `["/@vite/client", "/web/main.js"]`. The browser, whose page origin is Vite
  `:5173`, loads both from Vite — `/@vite/client` (HMR client) and `/web/main.js`
  (transformed; it `import`s `./style.css`, which Vite injects with CSS HMR).
- `v.StaticHandler()` is **not mounted** in dev (gated on `!v.Dev()`); `/static/`
  is never requested.
- After boot, `vite.NotifyReload("http://localhost:5173")` POSTs `/__reload`
  (through Vite, which owns that path) → the gsx plugin broadcasts `full-reload`.

**Prod** (single Go binary, no Vite, `VITE_DEV_URL` unset):
- `npm run build` (`vite build`) writes `dist/` (hashed assets +
  `.vite/manifest.json`); `go build` embeds it via `//go:embed all:dist`.
- `v.Dev()` is false. `v.StaticHandler()` **is mounted** at `/static/`. Go serves
  both the HTML and the assets — there is no Vite process.
- `v.Entry("web/main.js")` resolves the hashed JS/CSS/preloads from the embedded
  manifest, prefixed `/static/`, which `StaticHandler` serves from `dist/`.
- `vite.NotifyReload("")` is a no-op.

The e2e test MUST exercise **both** paths: the dev live-reload loop, and a prod
build (`vite build` → `go build` → run with `VITE_DEV_URL` unset) serving a page
whose asset URLs are `/static/...` hashed files that actually resolve.

### Prod build (the teachable two-step, in the README)

```
npm run build   # vite build → dist/ (hashed assets + manifest)
go build        # embeds dist/, serves /static/, reads manifest
```

## Module resolution

The scaffold's `go.mod` `require`s `github.com/gsxhq/gsx`, `github.com/gsxhq/vite`
(v0.2.0 — adds the context helpers above), and `github.com/bokwoon95/wgo` (tool), plus the `tool`
directives for `github.com/gsxhq/gsx/cmd/gsx` and `github.com/bokwoon95/wgo`. It
emits **no `replace`** — end users run `go mod tidy`. `gsx` itself is resolved as
whatever `go mod tidy` finds for `github.com/gsxhq/gsx` (a published tag once
gsx is tagged; a pseudo-version from the default branch until then — a known
follow-up). For the **local e2e test**, the harness adds `replace` directives to
the local `gsx` (and optionally local `vite`) checkout.

## Testing

- **`gsx init` unit tests** (in `gen/`): scaffold the `simple` template into a
  `t.TempDir()`; assert every expected file exists; assert `go.mod` contains
  `module <substituted>`; assert `--force` is required to overwrite an existing
  `go.mod` (exit 2 without it, success with it); assert the rendered files
  contain no stray `«`/`»` delimiters (proves substitution ran). **Registry:**
  assert `--template simple` works and an unknown `--template bogus` exits 2 and
  lists the available templates.
- **Scaffold-compiles test:** scaffold into a temp dir, write `replace` directives
  to the local `gsx`/`vite` checkouts, run `go generate ./...` (or `go tool gsx
  generate`) then `go build ./...` to prove the generated project is valid Go.
  Network-gated/optional (needs the module graph); skipped in `-short`.
- **E2E (the "test the flow" deliverable) — both modes:**
  - *Dev:* scaffold, `npm install`, `task dev`, drive a browser to the page, edit
    `app.gsx`, confirm the browser live-reloads — assisted via Claude-in-Chrome
    (optionally a GIF).
  - *Prod:* `npm run build` then `go build`, run the binary with `VITE_DEV_URL`
    unset, and confirm the served page references `/static/...` hashed asset URLs
    that resolve (200) from the embedded `dist/`.
  Manual/assisted, not CI.

## Out of scope (YAGNI)

- No custom `cmd/gsx` / extension wiring — the starter uses the stock
  `go tool gsx` (filters/predicates are an advanced opt-in, mentioned in the
  README, not scaffolded).
- No multi-page routing, no CSS framework (minimal plain CSS), no auth/db.
- The **`structpages` template itself is out of scope for this spec** — we build
  the `--template` registry seam and the `simple` template now; the structpages
  starter is a follow-up that adds one template subtree + one registry entry (and
  may then become the default).
- `gsx init` does not run `go mod tidy` / `npm install` / generate itself — it
  writes files and prints next steps (offline-friendly, deterministic).
