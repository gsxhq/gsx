# `github.com/gsxhq/vite` — Design

**Status:** Approved (2026-06-24)
**Goal:** A small, generic Go library that integrates a Vite-built frontend into a
Go server: resolve a Vite build manifest to hashed asset URLs in prod, point at
the Vite dev server in dev, serve the embedded prod assets, and notify the Vite
dev server to reload — all behind one `dev` boolean.

## Context

A Go server that renders HTML (gsx, templ, `html/template`, or hand-written) and
uses Vite for its frontend assets needs the same plumbing every time:
- **Dev:** emit `<script src="/@vite/client">` + the entry module so Vite serves
  them with HMR; assets come from the running Vite dev server.
- **Prod:** `vite build` emits content-hashed files plus `dist/.vite/manifest.json`;
  the server must read that manifest, resolve an entry to its hashed JS/CSS (and
  transitively imported chunk CSS), and serve those files.

This is **not gsx-specific** — it only depends on Vite's manifest format, a
documented Vite standard ([Backend Integration](https://vite.dev/guide/backend-integration)).
So it is its own generic module, `github.com/gsxhq/vite`, with **zero dependency
on gsx or templ** (stdlib only). It is consumed by the `gsx init` scaffold and
usable standalone by any Go+Vite project.

This is sub-project 1 of two. Sub-project 2 (`gsx init`, in core gsx) scaffolds a
project that imports this library rather than copy-pasting the integration.

## Non-goals (YAGNI)

- No CSS/JS processing — Vite owns the build; this only reads its output.
- No framework rendering — returns plain URL lists; the caller renders tags.
- No dev-server process management — that's the Vite plugin / `wgo`.
- No FOUC-guard scripting (his-project's elaborate reveal logic) — a starter
  accepts a brief dev-only flash; out of scope here.
- No multi-entry orchestration beyond resolving each named entry independently.

## Public API

```go
package vite

// Config configures a Vite integration. The zero DevURL selects prod mode.
type Config struct {
    // DevURL is the running Vite dev server origin (e.g. "http://localhost:5173").
    // Empty selects prod mode (resolve from the embedded manifest).
    DevURL string
    // DevBase is the URL base path Vite serves under in dev (vite.config `base`).
    // Default "/". Must have a leading and trailing slash when non-default.
    DevBase string
    // Dist is the embedded prod build output containing the manifest and assets
    // (e.g. //go:embed all:dist). Required in prod, ignored in dev.
    Dist fs.FS
    // DistDir is the subpath within Dist that holds ".vite/manifest.json" and the
    // hashed assets. Default ".". Note: //go:embed all:dist yields an fs.FS whose
    // paths are under "dist/", so consumers embedding that way pass DistDir "dist".
    DistDir string
    // StaticURL is the URL prefix prod assets are served under. Default "/static/".
    StaticURL string
}

// Vite resolves Vite entries to asset URLs. Safe for concurrent use; build once
// at startup and share across requests.
type Vite struct { /* unexported */ }

// New builds a *Vite. In prod (DevURL == "") it reads and parses
// <DistDir>/.vite/manifest.json from Dist and returns an error if absent or
// malformed. In dev it performs no I/O.
func New(cfg Config) (*Vite, error)

// Dev reports whether the integration is in dev mode (DevURL set).
func (v *Vite) Dev() bool

// Entry resolves one Vite entry (the manifest key / source path, e.g.
// "web/main.js") to its asset URLs.
//
//   - Dev:  JS = [DevBase+"@vite/client", DevBase+name], CSS = nil, Preloads = nil.
//   - Prod: JS = [StaticURL+manifest[name].file];
//           CSS = StaticURL + (entry.css ∪ transitively-imported chunks' css);
//           Preloads = StaticURL + (transitively-imported chunks' files).
//
// A name absent from the prod manifest yields an empty Bundle (logged by the
// caller if desired); Entry never panics.
func (v *Vite) Entry(name string) Bundle

// Bundle is the resolved asset URL list for one entry. The caller renders these
// into <link>/<script>/<link rel=modulepreload> tags however it likes.
type Bundle struct {
    JS       []string
    CSS      []string
    Preloads []string
}

// StaticHandler serves the embedded prod assets (Config.Dist, rooted at DistDir)
// under Config.StaticURL. In dev it is unused (Vite serves assets) — callers
// gate mounting on !v.Dev(), or mount it always (the /static/ path is never hit
// in dev). Returns a handler that StripPrefix(StaticURL)es onto an fs file server.
func (v *Vite) StaticHandler() http.Handler

// NotifyReload POSTs to <devURL>/__reload so a Vite plugin exposing that endpoint
// broadcasts a browser full-reload. Call it once after the HTTP server's
// listeners are up. Dev-only by construction: a "" devURL is a no-op. Runs in a
// goroutine with a brief retry loop (covers the cold-start race where the Go
// server beats Vite to the port).
func NotifyReload(devURL string)
```

## Prod manifest resolution

Vite's `manifest.json` maps each source/chunk key to a record:

```jsonc
{
  "web/main.js": {
    "file": "assets/main-a1b2c3.js",
    "src": "web/main.js",
    "isEntry": true,
    "css": ["assets/main-d4e5f6.css"],
    "imports": ["_shared-7g8h9i.js"]
  },
  "_shared-7g8h9i.js": {
    "file": "assets/shared-7g8h9i.js",
    "css": ["assets/shared-0j1k2l.css"]
  }
}
```

`Entry(name)` in prod follows Vite's documented backend-integration algorithm:

1. Look up `chunk = manifest[name]`. If absent → empty `Bundle`.
2. `JS = [StaticURL + chunk.file]`.
3. Walk `chunk.imports` transitively (depth-first, with a `visited` set keyed on
   manifest key to break cycles): for each imported chunk, append its `file` to
   `Preloads` and append its `css` to the CSS set.
4. `CSS = chunk.css ++ collected-imported-css`, de-duplicated in encounter order.
5. Prefix every `Preloads` and `CSS` entry with `StaticURL`.

The manifest record type carries `File string`, `Src string`, `IsEntry bool`,
`CSS []string`, `Imports []string` (other fields ignored). Resolution is pure
over the parsed manifest map — directly unit-testable.

## Components (files)

Repo at `/Users/jackieli/personal/gsxhq/vite`, module `github.com/gsxhq/vite`,
stdlib only.

- **`vite.go`** — `Config`, `Vite`, `New`, `Dev`, `Entry`, `Bundle`. Holds the
  parsed manifest (prod) or just the dev config. Applies Config defaults
  (`DevBase` "/", `DistDir` ".", `StaticURL` "/static/").
- **`manifest.go`** — manifest record type, `parseManifest(fs.FS, distDir)`, and
  the pure prod resolver `resolve(manifest, name, staticURL) Bundle` with the
  transitive import walk + de-dup.
- **`static.go`** — `StaticHandler`: `http.StripPrefix(StaticURL,
  http.FileServerFS(sub))` where `sub` is `Dist` rooted at `DistDir`.
- **`reload.go`** — `NotifyReload(devURL)`: goroutine, 2s context, up to 10 POSTs
  to `devURL+"/__reload"` at 150ms intervals, stops on first success.
- **`README.md`**, **`LICENSE`** (MIT), **`.gitignore`**.

## Error handling

- `New` (prod) returns a wrapped error when the manifest is missing or invalid
  JSON — fail fast at startup, not per request.
- `New` (dev) never errors on missing `Dist`.
- `Entry` never panics: an unknown name → empty `Bundle`.
- `NotifyReload` swallows transport errors (dev convenience); never blocks boot.

## Testing

- **`manifest_test.go`** — feed a fake `manifest.json` via `fstest.MapFS` and
  assert `Entry` resolves: (a) entry `file` → JS; (b) entry `css` → CSS; (c)
  transitively imported chunk `css` collected into CSS and chunk `file` into
  Preloads; (d) cyclic imports terminate; (e) de-dup of a CSS file referenced by
  two chunks; (f) StaticURL prefixing; (g) unknown name → empty Bundle.
- **`vite_test.go`** — dev mode: `Entry("web/main.js")` →
  `JS == ["/@vite/client", "/web/main.js"]` with default `DevBase`, and a custom
  `DevBase "/__vite/"` shifts both; `Dev()` true/false; default application.
- **`static_test.go`** — `httptest` request to a `StaticHandler` over an
  `fstest.MapFS` returns the embedded asset bytes at `StaticURL+path`.
- **`reload_test.go`** — `httptest.Server` capturing `POST /__reload`; assert
  `NotifyReload` hits it; assert `NotifyReload("")` makes no request.

## Versioning / publishing

New public GitHub repo `gsxhq/vite`, Go module `github.com/gsxhq/vite`, MIT.
Tagging a release (`v0.1.0`) so `go get` resolves a stable version is a follow-up
(like the npm publish): until then consumers use a pseudo-version or, for the
local e2e, a `replace` to the local checkout. Go floor: `go 1.23` (needs
`http.FileServerFS`).
