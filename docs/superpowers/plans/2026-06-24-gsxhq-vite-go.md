# github.com/gsxhq/vite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A small, generic, stdlib-only Go module that integrates a Vite-built frontend into a Go server — manifest-driven prod asset URLs, dev-server URLs in dev, an embedded `/static` asset server, and a reload notifier — all behind one `dev` boolean.

**Architecture:** Single Go package `vite` in a new module. A pure manifest resolver (`manifest.go`) does the prod algorithm; `vite.go` wraps it with `Config`/`New`/`Entry` and the dev/prod switch; `static.go` + `reload.go` are small HTTP helpers. No dependency on gsx, templ, or anything outside the standard library.

**Tech Stack:** Go (stdlib only: `encoding/json`, `io/fs`, `net/http`, `context`, `time`, `path`, `slices`). Tests use `testing/fstest` + `net/http/httptest`.

**Spec:** `docs/superpowers/specs/2026-06-24-gsxhq-vite-go-design.md` (in the gsx repo).

## Global Constraints

- **Module:** `github.com/gsxhq/vite`. **Package:** `vite`. **Repo:** new git repo at `/Users/jackieli/personal/gsxhq/vite` (sibling of `gsx`).
- **Stdlib only** — no third-party dependencies. **Go floor:** `go 1.23` (needs `http.FileServerFS`, range-over-int).
- **License:** MIT.
- **Dev/prod switch:** `Config.DevURL == ""` selects prod (manifest); non-empty selects dev (HMR).
- **Defaults:** `DevBase` `"/"`, `DistDir` `"."`, `StaticURL` `"/static/"`.
- **Dev `Entry(name)`** → `JS = [DevBase+"@vite/client", DevBase+name]`, `CSS`/`Preloads` empty.
- **Prod `Entry(name)`** → JS = `[StaticURL+entry.file]`; CSS = entry.css ∪ transitively-imported chunk css (de-duped, encounter order); Preloads = transitively-imported chunk files; all prefixed `StaticURL`.
- **`Entry` never panics**; unknown name → empty `Bundle`. **`New` (prod)** errors on nil `Dist` or missing/invalid manifest. **`NotifyReload("")`** is a no-op.

---

### Task 1: Module scaffold

Creates the repo and a buildable empty package so later tasks have a test loop.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite/go.mod`
- Create: `/Users/jackieli/personal/gsxhq/vite/doc.go`
- Create: `/Users/jackieli/personal/gsxhq/vite/.gitignore`
- Create: `/Users/jackieli/personal/gsxhq/vite/LICENSE`

**Interfaces:**
- Produces: the `vite` package (empty) in module `github.com/gsxhq/vite`.

- [ ] **Step 1: Create the repo and init**

Run:
```bash
mkdir -p /Users/jackieli/personal/gsxhq/vite
cd /Users/jackieli/personal/gsxhq/vite
git init
go mod init github.com/gsxhq/vite
```
Expected: `go.mod` created with `module github.com/gsxhq/vite`.

- [ ] **Step 2: Pin the Go floor in `go.mod`**

Ensure `go.mod` reads exactly (adjust the `go` line if `go mod init` wrote a higher version):
```
module github.com/gsxhq/vite

go 1.23
```

- [ ] **Step 3: Write `doc.go`**

```go
// Package vite integrates a Vite-built frontend into a Go server: it resolves a
// Vite build manifest to hashed asset URLs in production, points at the Vite dev
// server in development, serves the embedded production assets, and notifies the
// Vite dev server to reload — all behind one dev boolean.
//
// It depends only on the standard library and on Vite's manifest format; it has
// no knowledge of any Go HTML templating library.
package vite
```

- [ ] **Step 4: Write `.gitignore` and `LICENSE`**

`.gitignore`:
```
*.log
.DS_Store
.superpowers/
```

`LICENSE`: MIT license text, copyright holder `gsxhq`, year `2026`.

- [ ] **Step 5: Verify it builds**

Run:
```bash
cd /Users/jackieli/personal/gsxhq/vite
go build ./... && go vet ./...
```
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite
git add -A
git commit -m "chore: scaffold github.com/gsxhq/vite module"
```

---

### Task 2: Manifest types + pure resolver

The prod core: the manifest record type, `parseManifest`, the `Bundle` result type, and the pure `resolve` algorithm (transitive CSS/preload collection with de-dup and cycle-safety).

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite/manifest.go`
- Create: `/Users/jackieli/personal/gsxhq/vite/manifest_test.go`

**Interfaces:**
- Produces:
  ```go
  type Bundle struct { JS, CSS, Preloads []string }
  type manifestEntry struct { File, Src string; IsEntry bool; CSS, Imports []string }
  func parseManifest(fsys fs.FS, distDir string) (map[string]manifestEntry, error)
  func resolve(manifest map[string]manifestEntry, name, staticURL string) Bundle
  ```
- Consumes: nothing.

- [ ] **Step 1: Write the failing test** — `manifest_test.go`

```go
package vite

import (
	"slices"
	"testing"
	"testing/fstest"
)

func TestResolveProd(t *testing.T) {
	m := map[string]manifestEntry{
		"web/main.js": {
			File:    "assets/main-AAA.js",
			Src:     "web/main.js",
			IsEntry: true,
			CSS:     []string{"assets/main-CSS.css"},
			Imports: []string{"_shared-BBB.js"},
		},
		"_shared-BBB.js": {
			File:    "assets/shared-BBB.js",
			CSS:     []string{"assets/shared-CSS.css"},
			Imports: []string{"_dep-CCC.js"},
		},
		"_dep-CCC.js": {
			File: "assets/dep-CCC.js",
			CSS:  []string{"assets/main-CSS.css"}, // duplicate of entry CSS
		},
	}
	b := resolve(m, "web/main.js", "/static/")
	if !slices.Equal(b.JS, []string{"/static/assets/main-AAA.js"}) {
		t.Fatalf("JS = %v", b.JS)
	}
	// entry css + shared css + dep css (dup removed), deduped, prefixed:
	if !slices.Equal(b.CSS, []string{"/static/assets/main-CSS.css", "/static/assets/shared-CSS.css"}) {
		t.Fatalf("CSS = %v", b.CSS)
	}
	// transitively imported chunk files as preloads, prefixed:
	if !slices.Equal(b.Preloads, []string{"/static/assets/shared-BBB.js", "/static/assets/dep-CCC.js"}) {
		t.Fatalf("Preloads = %v", b.Preloads)
	}
}

func TestResolveUnknownEntry(t *testing.T) {
	b := resolve(map[string]manifestEntry{}, "nope", "/static/")
	if len(b.JS) != 0 || len(b.CSS) != 0 || len(b.Preloads) != 0 {
		t.Fatalf("expected empty bundle, got %+v", b)
	}
}

func TestResolveCycleTerminates(t *testing.T) {
	m := map[string]manifestEntry{
		"a.js": {File: "a.js", Imports: []string{"b.js"}},
		"b.js": {File: "b.js", Imports: []string{"a.js"}}, // cycle back to entry
	}
	b := resolve(m, "a.js", "/static/")
	if !slices.Equal(b.Preloads, []string{"/static/b.js"}) {
		t.Fatalf("Preloads = %v", b.Preloads)
	}
}

func TestParseManifest(t *testing.T) {
	fsys := fstest.MapFS{
		"dist/.vite/manifest.json": &fstest.MapFile{
			Data: []byte(`{"web/main.js":{"file":"assets/main-AAA.js","isEntry":true}}`),
		},
	}
	m, err := parseManifest(fsys, "dist")
	if err != nil {
		t.Fatal(err)
	}
	if m["web/main.js"].File != "assets/main-AAA.js" {
		t.Fatalf("got %+v", m)
	}
}

func TestParseManifestMissing(t *testing.T) {
	if _, err := parseManifest(fstest.MapFS{}, "dist"); err == nil {
		t.Fatal("expected error for missing manifest")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go test ./...`
Expected: FAIL to compile — `undefined: manifestEntry`, `undefined: resolve`, `undefined: parseManifest`.

- [ ] **Step 3: Write `manifest.go`**

```go
package vite

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
)

// manifestEntry is one record in Vite's manifest.json. Only the fields used for
// backend asset resolution are decoded.
type manifestEntry struct {
	File    string   `json:"file"`
	Src     string   `json:"src"`
	IsEntry bool     `json:"isEntry"`
	CSS     []string `json:"css"`
	Imports []string `json:"imports"`
}

// Bundle is the resolved asset URL list for one entry. The caller renders these
// into <script>/<link>/<link rel=modulepreload> tags however it likes.
type Bundle struct {
	JS       []string
	CSS      []string
	Preloads []string
}

// parseManifest reads and decodes <distDir>/.vite/manifest.json from fsys.
func parseManifest(fsys fs.FS, distDir string) (map[string]manifestEntry, error) {
	name := path.Join(distDir, ".vite", "manifest.json")
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("vite: read manifest %s: %w", name, err)
	}
	var m map[string]manifestEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("vite: parse manifest %s: %w", name, err)
	}
	return m, nil
}

// resolve walks the manifest for one entry, collecting the entry's JS file, the
// CSS of the entry and all transitively imported chunks (de-duplicated in
// encounter order), and the imported chunk files as module preloads. All URLs
// are prefixed with staticURL. Pure over the parsed manifest; cycle-safe.
func resolve(manifest map[string]manifestEntry, name, staticURL string) Bundle {
	entry, ok := manifest[name]
	if !ok {
		return Bundle{}
	}
	var b Bundle
	b.JS = []string{staticURL + entry.File}

	cssSeen := map[string]bool{}
	addCSS := func(files []string) {
		for _, f := range files {
			if !cssSeen[f] {
				cssSeen[f] = true
				b.CSS = append(b.CSS, staticURL+f)
			}
		}
	}
	addCSS(entry.CSS)

	visited := map[string]bool{name: true}
	var walk func(keys []string)
	walk = func(keys []string) {
		for _, k := range keys {
			if visited[k] {
				continue
			}
			visited[k] = true
			chunk, ok := manifest[k]
			if !ok {
				continue
			}
			b.Preloads = append(b.Preloads, staticURL+chunk.File)
			addCSS(chunk.CSS)
			walk(chunk.Imports)
		}
	}
	walk(entry.Imports)
	return b
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go test ./...`
Expected: PASS — all manifest tests green.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite
git add manifest.go manifest_test.go
git commit -m "feat: vite manifest types + pure prod resolver"
```

---

### Task 3: Config, New, Dev, Entry (the dev/prod switch)

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite/vite.go`
- Create: `/Users/jackieli/personal/gsxhq/vite/vite_test.go`

**Interfaces:**
- Consumes: `Bundle`, `manifestEntry`, `parseManifest`, `resolve` (from `manifest.go`).
- Produces:
  ```go
  type Config struct { DevURL, DevBase string; Dist fs.FS; DistDir, StaticURL string }
  type Vite struct { /* unexported */ }
  func New(cfg Config) (*Vite, error)
  func (v *Vite) Dev() bool
  func (v *Vite) Entry(name string) Bundle
  ```
  (The unexported fields `dev bool`, `devBase`, `staticURL`, `distDir string`, `dist fs.FS`, `manifest map[string]manifestEntry` are used by Task 4's `static.go`.)

- [ ] **Step 1: Write the failing test** — `vite_test.go`

```go
package vite

import (
	"slices"
	"testing"
	"testing/fstest"
)

func TestEntryDev(t *testing.T) {
	v, err := New(Config{DevURL: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Dev() {
		t.Fatal("expected dev mode")
	}
	b := v.Entry("web/main.js")
	if !slices.Equal(b.JS, []string{"/@vite/client", "/web/main.js"}) {
		t.Fatalf("JS = %v", b.JS)
	}
	if len(b.CSS) != 0 {
		t.Fatalf("dev CSS should be empty, got %v", b.CSS)
	}
}

func TestEntryDevCustomBase(t *testing.T) {
	v, _ := New(Config{DevURL: "http://x", DevBase: "/__vite/"})
	b := v.Entry("web/main.js")
	if !slices.Equal(b.JS, []string{"/__vite/@vite/client", "/__vite/web/main.js"}) {
		t.Fatalf("JS = %v", b.JS)
	}
}

func TestNewProdParsesManifestAndResolves(t *testing.T) {
	fsys := fstest.MapFS{
		"dist/.vite/manifest.json": &fstest.MapFile{
			Data: []byte(`{"web/main.js":{"file":"assets/main-AAA.js","css":["assets/main-CSS.css"]}}`),
		},
	}
	v, err := New(Config{Dist: fsys, DistDir: "dist"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Dev() {
		t.Fatal("expected prod mode")
	}
	b := v.Entry("web/main.js")
	if !slices.Equal(b.JS, []string{"/static/assets/main-AAA.js"}) {
		t.Fatalf("JS = %v", b.JS)
	}
	if !slices.Equal(b.CSS, []string{"/static/assets/main-CSS.css"}) {
		t.Fatalf("CSS = %v", b.CSS)
	}
}

func TestNewProdMissingManifestErrors(t *testing.T) {
	if _, err := New(Config{Dist: fstest.MapFS{}, DistDir: "dist"}); err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestNewProdNilDistErrors(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for prod mode without Dist")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go test ./...`
Expected: FAIL to compile — `undefined: New`, `undefined: Config`.

- [ ] **Step 3: Write `vite.go`**

```go
package vite

import (
	"fmt"
	"io/fs"
)

// Config configures a Vite integration. The zero DevURL selects prod mode.
type Config struct {
	DevURL    string // running Vite dev server origin; "" → prod
	DevBase   string // dev base path (vite.config base); default "/"
	Dist      fs.FS  // embedded prod build output (holds .vite/manifest.json); required in prod
	DistDir   string // subpath within Dist for manifest+assets; default "."
	StaticURL string // URL prefix prod assets serve under; default "/static/"
}

// Vite resolves Vite entries to asset URLs. Safe for concurrent use; build once
// at startup and share across requests.
type Vite struct {
	dev       bool
	devBase   string
	staticURL string
	distDir   string
	dist      fs.FS
	manifest  map[string]manifestEntry
}

// New builds a *Vite. In prod (DevURL == "") it reads and parses the manifest
// from Dist and returns an error if Dist is nil or the manifest is missing or
// invalid. In dev it performs no I/O.
func New(cfg Config) (*Vite, error) {
	v := &Vite{
		dev:       cfg.DevURL != "",
		devBase:   orDefault(cfg.DevBase, "/"),
		staticURL: orDefault(cfg.StaticURL, "/static/"),
		distDir:   orDefault(cfg.DistDir, "."),
		dist:      cfg.Dist,
	}
	if !v.dev {
		if cfg.Dist == nil {
			return nil, fmt.Errorf("vite: prod mode (empty DevURL) requires Config.Dist")
		}
		m, err := parseManifest(cfg.Dist, v.distDir)
		if err != nil {
			return nil, err
		}
		v.manifest = m
	}
	return v, nil
}

// Dev reports whether the integration is in dev mode.
func (v *Vite) Dev() bool { return v.dev }

// Entry resolves one Vite entry (the manifest key / source path, e.g.
// "web/main.js") to its asset URLs. Never panics; an unknown prod entry yields
// an empty Bundle.
func (v *Vite) Entry(name string) Bundle {
	if v.dev {
		return Bundle{JS: []string{v.devBase + "@vite/client", v.devBase + name}}
	}
	return resolve(v.manifest, name, v.staticURL)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go test ./...`
Expected: PASS — all vite + manifest tests green.

- [ ] **Step 5: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite
git add vite.go vite_test.go
git commit -m "feat: Config/New/Dev/Entry with dev-prod switch"
```

---

### Task 4: HTTP helpers — StaticHandler + NotifyReload

Two small, independent HTTP helpers.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite/static.go`
- Create: `/Users/jackieli/personal/gsxhq/vite/reload.go`
- Create: `/Users/jackieli/personal/gsxhq/vite/static_test.go`
- Create: `/Users/jackieli/personal/gsxhq/vite/reload_test.go`

**Interfaces:**
- Consumes: the `Vite` struct fields `dist`, `distDir`, `staticURL` (from `vite.go`).
- Produces:
  ```go
  func (v *Vite) StaticHandler() http.Handler
  func NotifyReload(devURL string)
  ```

- [ ] **Step 1: Write the failing tests**

`static_test.go`:
```go
package vite

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestStaticHandlerServesAssets(t *testing.T) {
	fsys := fstest.MapFS{
		"dist/.vite/manifest.json": &fstest.MapFile{Data: []byte(`{}`)},
		"dist/assets/main-AAA.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
	}
	v, err := New(Config{Dist: fsys, DistDir: "dist"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(v.StaticHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/static/assets/main-AAA.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "console.log(1)" {
		t.Fatalf("body = %q", body)
	}
}
```

`reload_test.go`:
```go
package vite

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotifyReloadPosts(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	NotifyReload(srv.URL)
	select {
	case g := <-got:
		if g != "POST /__reload" {
			t.Fatalf("got %q, want POST /__reload", g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyReload did not POST /__reload")
	}
}

func TestNotifyReloadEmptyNoop(t *testing.T) {
	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- struct{}{}
	}))
	defer srv.Close()

	NotifyReload("") // must not POST anywhere
	select {
	case <-got:
		t.Fatal(`NotifyReload("") should not POST`)
	case <-time.After(200 * time.Millisecond):
		// success: nothing received
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go test ./...`
Expected: FAIL to compile — `v.StaticHandler undefined`, `undefined: NotifyReload`.

- [ ] **Step 3: Write `static.go`**

```go
package vite

import (
	"io/fs"
	"net/http"
	"strings"
)

// StaticHandler serves the embedded prod assets (Config.Dist, rooted at DistDir)
// under Config.StaticURL. In dev it has no assets to serve and returns a
// not-found handler; mount it only in prod (or always — /static/ is never hit
// in dev). The StaticURL prefix is stripped without its trailing slash so the
// remaining request path keeps its leading slash for the file server.
func (v *Vite) StaticHandler() http.Handler {
	if v.dist == nil {
		return http.NotFoundHandler()
	}
	sub := v.dist
	if v.distDir != "." {
		if s, err := fs.Sub(v.dist, v.distDir); err == nil {
			sub = s
		}
	}
	prefix := strings.TrimSuffix(v.staticURL, "/")
	return http.StripPrefix(prefix, http.FileServerFS(sub))
}
```

- [ ] **Step 4: Write `reload.go`**

```go
package vite

import (
	"context"
	"net/http"
	"time"
)

// NotifyReload POSTs to <devURL>/__reload so a Vite plugin exposing that endpoint
// broadcasts a browser full-reload. Call it once after the HTTP server's
// listeners are up. Dev-only: a "" devURL is a no-op. Runs in a goroutine with a
// brief retry loop (covers the cold-start race where the Go server beats Vite to
// the port).
func NotifyReload(devURL string) {
	if devURL == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for range 10 {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, devURL+"/__reload", nil)
			if err != nil {
				return
			}
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
	}()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go test ./...`
Expected: PASS — all tests green.

- [ ] **Step 6: Run vet + race on the package**

Run: `cd /Users/jackieli/personal/gsxhq/vite && go vet ./... && go test -race ./...`
Expected: vet clean; race tests pass (NotifyReload's goroutine is exercised).

- [ ] **Step 7: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite
git add static.go reload.go static_test.go reload_test.go
git commit -m "feat: StaticHandler + NotifyReload http helpers"
```

---

### Task 5: README

Documents usage so a consumer (and the `gsx init` scaffold) can wire it correctly — including the `//go:embed all:dist` → `DistDir: "dist"` gotcha and the dev/prod example.

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/vite/README.md`

**Interfaces:**
- Consumes: the public API (`New`, `Config`, `Entry`, `Bundle`, `StaticHandler`, `NotifyReload`).

- [ ] **Step 1: Write `README.md`**

Include these sections with real, compilable snippets:

1. **Title + one-line description** — generic Go ↔ Vite integration (manifest-driven prod assets, dev-server URLs in dev, `/static` server, reload notify), stdlib-only, no framework dependency.
2. **Install:** `go get github.com/gsxhq/vite`.
3. **Usage** — a complete server snippet showing the one-boolean switch:
   ```go
   //go:embed all:dist
   var distFS embed.FS

   func main() {
       devURL := os.Getenv("VITE_DEV_URL") // "" in prod
       v, err := vite.New(vite.Config{
           DevURL:  devURL,
           Dist:    distFS,
           DistDir: "dist", // //go:embed all:dist nests under dist/
       })
       if err != nil {
           log.Fatal(err)
       }
       mux := http.NewServeMux()
       if !v.Dev() {
           mux.Handle("/static/", v.StaticHandler())
       }
       mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
           b := v.Entry("web/main.js")
           // render b.CSS as <link rel=stylesheet>, b.JS as <script type=module>,
           // b.Preloads as <link rel=modulepreload> — in your template of choice.
           _ = b
       })
       go vite.NotifyReload(devURL) // dev-only; no-op when devURL == ""
       http.ListenAndServe(":7777", mux)
   }
   ```
4. **How dev vs prod differ** — the `Entry` table from the spec (dev → `/@vite/client` + entry; prod → hashed JS/CSS/preloads from the manifest), and that one `DevURL` boolean flips it.
5. **The `//go:embed all:dist` note** — paths nest under `dist/`, so pass `DistDir: "dist"`; the prod build order is `vite build` (creates `dist/`) **then** `go build` (embeds it).
6. **API reference** — `Config` fields + defaults, `New`, `Dev`, `Entry`/`Bundle`, `StaticHandler`, `NotifyReload`.
7. **License:** MIT.

- [ ] **Step 2: Commit**

```bash
cd /Users/jackieli/personal/gsxhq/vite
git add README.md
git commit -m "docs: README with usage + dev/prod example"
```

---

## Final verification (after all tasks)

- [ ] Run the whole suite + vet + race:
```bash
cd /Users/jackieli/personal/gsxhq/vite
go build ./... && go vet ./... && go test -race ./...
```
Expected: all green.

- [ ] Confirm `git log --oneline` shows the five task commits and the tree is clean.

Tagging a `v0.1.0` release and creating the GitHub `gsxhq/vite` remote are follow-ups (they need an org/credentials decision) and are intentionally not tasks here. The `gsx init` scaffold (sub-project 2) and the end-to-end test consume this module.
