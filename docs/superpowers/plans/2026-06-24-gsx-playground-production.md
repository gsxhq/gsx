# gsx Docs Playground — Production & Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the local `playground/server` render-API prototype into a hardened, containerized service deployed on Cloud Run (free tier), wired to the docs site, with CI verifying the playground's examples stay valid.

**Architecture:** A single stateless Go service holds a *pool* of pre-warmed fixed-module workspaces. `POST /render` writes the visitor's component + invoke into a free workspace, runs the authentic gsx pipeline (`gsx generate --json` → `go build`/run), and returns rendered HTML, generated Go, and structured diagnostics. It ships as a `golang`-based container with a pre-populated `GOCACHE` (so cold starts only compile the one changed file) and runs in Cloud Run's gVisor sandbox. The static VitePress frontend already calls it via `VITE_GSX_PLAYGROUND_API`.

**Tech Stack:** Go (stdlib `net/http`, `os/exec`), the gsx CLI, Docker (`golang:1.23` base), Cloud Run, VitePress/Vue (frontend, already built).

## Global Constraints

- Go version floor: **1.23** (matches `playground/server/go.mod` and the prepared module).
- Backend imports: **standard library only** (no third-party deps in `playground/server`). It *execs* `gsx` and `go`; it does not import gsx.
- Safety model is **fixed module**: callers supply only a component body + an invoke expression; `go.mod` is server-written (gsx + stdlib). Never accept a caller-supplied `go.mod` or import path.
- Render must stay **authentic** (real `gsx generate` + `go build`/run) — no interpreter substitute.
- The gsx module is referenced via a `replace` directive to an on-disk path; in the container that path is where the gsx source is copied.
- Repo homes: backend in `gsx` repo under `playground/`; frontend in `gsxhq.github.io`. Work on gsx branch `feat/gsx-playground`.
- Docs site origin (for CORS): `https://gsxhq.github.io`.

---

## File Structure

- `playground/server/main.go` — **modify**: HTTP wiring, flags, middleware (CORS, limits), `main()`. Render logic moves out.
- `playground/server/render.go` — **create**: `Renderer` (workspace pool) + `render()` + module setup + helpers (moved from `main.go`).
- `playground/server/render_test.go` — **create**: integration tests (build gsx + pool once in `TestMain`).
- `playground/server/Dockerfile` — **create**: `golang`-based image with baked `GOCACHE`.
- `playground/server/.dockerignore` — **create**.
- `playground/server/deploy.md` — **create**: exact Cloud Run deploy commands (run by the maintainer).
- `internal/corpus/testdata/cases/playground/*.txtar` — **create**: the playground presets as CI-checked corpus cases.
- `gsxhq.github.io/.github/workflows/deploy.yml` — **modify**: pass `VITE_GSX_PLAYGROUND_API` to the build.

---

## Task 1: Split render logic into `render.go` + add integration tests

Make the renderer testable without HTTP, and lock in a file split before behavior changes.

**Files:**
- Modify: `playground/server/main.go` (remove render logic; keep `main`, flags, HTTP, helpers `writeJSON`/`cors`/`oneline`)
- Create: `playground/server/render.go` (move `renderer`, `renderReq`, `diagnostic`, `renderResp`, `newRenderer`, `writeShim`, `render`, `parseDiags`, `readGenerated`, `run`, `writeFile`, `pkgLine`, `defaultGsxMod`)
- Create: `playground/server/render_test.go`

**Interfaces:**
- Produces: `newRenderer(gsxMod, work string) (*renderer, error)` — note the **new explicit signature** (no globals); `(*renderer).render(renderReq) renderResp`; types `renderReq{GSX, Invoke string}`, `renderResp{HTML, GeneratedGo string; Diagnostics []diagnostic; Error string; Ms int64}`, `diagnostic{Severity, Message string; Line, Column int}`.

- [ ] **Step 1: Move render code into `render.go`**

Move these declarations **verbatim** from `main.go` into a new `playground/server/render.go` (package `main`): `pkgLine`, `renderer`, `renderReq`, `diagnostic`, `renderResp`, `newRenderer`, `(*renderer).writeShim`, `(*renderer).handleRender`'s *body logic stays in main but* `(*renderer).render`, `parseDiags`, `readGenerated`, `run`, `writeFile`, `defaultGsxMod`. Keep `main()`, the `flag` vars, `handleRender`, `writeJSON`, `cors`, `oneline` in `main.go`.

Then change `newRenderer` from reading globals to explicit params:

```go
// render.go
func newRenderer(gsxMod, work string) (*renderer, error) {
	if work == "" {
		var err error
		work, err = os.MkdirTemp("", "gsxplay-")
		if err != nil {
			return nil, err
		}
	}
	r := &renderer{work: work, play: filepath.Join(work, "play")}
	r.viewDir = filepath.Join(r.play, "views")
	r.gsxBin = filepath.Join(work, "gsx")
	if out, err := run(context.Background(), gsxMod, "go", "build", "-o", r.gsxBin, "./cmd/gsx"); err != nil {
		return nil, fmt.Errorf("build gsx: %v: %s", err, out)
	}
	if err := os.MkdirAll(r.viewDir, 0o755); err != nil {
		return nil, err
	}
	writeFile(filepath.Join(r.play, "go.mod"), fmt.Sprintf("module gsxplay\n\ngo 1.23\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n", gsxMod))
	writeFile(filepath.Join(r.play, "main.go"), "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n\n\t_ \"github.com/gsxhq/gsx\"\n\t\"gsxplay/views\"\n)\n\nfunc main() {\n\tif err := views.Render(context.Background(), os.Stdout); err != nil {\n\t\tpanic(err)\n\t}\n}\n")
	writeFile(filepath.Join(r.viewDir, "comp.gsx"), "package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n")
	r.writeShim("Hello(HelloProps{})")
	if out, err := run(context.Background(), r.play, "go", "mod", "tidy"); err != nil {
		return nil, fmt.Errorf("mod tidy: %v: %s", err, out)
	}
	if out, err := run(context.Background(), r.play, r.gsxBin, "generate", "./views"); err != nil {
		return nil, fmt.Errorf("seed generate: %v: %s", err, out)
	}
	if out, err := run(context.Background(), r.play, "go", "build", "-o", filepath.Join(work, "play-bin"), "."); err != nil {
		return nil, fmt.Errorf("warm build: %v: %s", err, out)
	}
	return r, nil
}
```

Update `main.go`'s `main()` to call it with the flag values:

```go
// main.go — inside main()
r, err := newRenderer(*gsxMod, *workIn)
if err != nil {
	log.Fatalf("setup: %v", err)
}
```

- [ ] **Step 2: Write the failing integration test**

```go
// playground/server/render_test.go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testRenderer *renderer

func TestMain(m *testing.M) {
	// gsxMod is the module root two levels up from playground/server.
	wd, _ := os.Getwd()
	gsxMod := filepath.Clean(filepath.Join(wd, "..", ".."))
	r, err := newRenderer(gsxMod, "")
	if err != nil {
		println("setup failed:", err.Error())
		os.Exit(1)
	}
	testRenderer = r
	os.Exit(m.Run())
}

func TestRenderSuccess(t *testing.T) {
	resp := testRenderer.render(renderReq{
		GSX:    "package views\n\ncomponent Greeting(name string) {\n\t<p>Hi {name}</p>\n}\n",
		Invoke: `Greeting(GreetingProps{Name: "World"})`,
	})
	if resp.Error != "" || len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected error/diags: %q %+v", resp.Error, resp.Diagnostics)
	}
	if got := strings.TrimSpace(resp.HTML); got != "<p>Hi World</p>" {
		t.Fatalf("html = %q", got)
	}
	if !strings.Contains(resp.GeneratedGo, "func Greeting(") {
		t.Fatalf("generatedGo missing func: %q", resp.GeneratedGo)
	}
}

func TestRenderDiagnostic(t *testing.T) {
	resp := testRenderer.render(renderReq{
		GSX:    "package views\n\ncomponent Bad() {\n\t<p>{missing}</p>\n}\n",
		Invoke: "Bad(BadProps{})",
	})
	if len(resp.Diagnostics) == 0 {
		t.Fatalf("expected a diagnostic, got none (err=%q)", resp.Error)
	}
	d := resp.Diagnostics[0]
	if d.Severity != "error" || d.Line == 0 {
		t.Fatalf("bad diagnostic: %+v", d)
	}
}

func TestRenderEscaping(t *testing.T) {
	resp := testRenderer.render(renderReq{
		GSX:    "package views\n\ncomponent C(s string) {\n\t<div>{s}</div>\n}\n",
		Invoke: `C(CProps{S: "<script>alert(1)<\/script>"})`,
	})
	if strings.Contains(resp.HTML, "<script>") {
		t.Fatalf("unescaped output: %q", resp.HTML)
	}
	if !strings.Contains(resp.HTML, "&lt;script&gt;") {
		t.Fatalf("expected escaped output, got: %q", resp.HTML)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd playground/server && go test ./... -run TestRender -v`
Expected: FAIL initially if `render.go` split is incomplete (compile error) — fix the split until it compiles, then tests should drive correctness.

- [ ] **Step 4: Make it compile and pass**

Ensure `render.go` + `main.go` compile (`go build ./...`) and the moved `newRenderer` matches the new signature. Re-run:

Run: `cd playground/server && go test ./... -run TestRender -v`
Expected: PASS (3 tests). First run is slow (builds gsx); that's expected.

- [ ] **Step 5: Commit**

```bash
git add playground/server/main.go playground/server/render.go playground/server/render_test.go
git commit -m "refactor(playground): split renderer into render.go + integration tests"
```

---

## Task 2: Per-request isolation via a workspace pool

Replace the single shared `viewDir` + global mutex with a bounded pool of independent workspaces (each its own `play/` module dir) sharing one `GOCACHE`, so concurrent requests can't clobber each other.

**Files:**
- Modify: `playground/server/render.go`
- Modify: `playground/server/render_test.go` (add concurrency test)

**Interfaces:**
- Consumes: `newRenderer(gsxMod, work string)` from Task 1.
- Produces: `newPool(gsxMod, work string, size int) (*pool, error)`; `(*pool).render(renderReq) renderResp`. A `workspace` is one `play/` dir; `pool` hands out a free workspace per request via a buffered channel. `GOCACHE` is set to `<work>/gocache` and shared by all workspaces (passed via `run`'s env).

- [ ] **Step 1: Write the failing concurrency test**

```go
// add to render_test.go
func TestPoolConcurrent(t *testing.T) {
	wd, _ := os.Getwd()
	gsxMod := filepath.Clean(filepath.Join(wd, "..", ".."))
	p, err := newPool(gsxMod, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "U" + strconv.Itoa(i)
			resp := p.render(renderReq{
				GSX:    "package views\n\ncomponent G(name string) {\n\t<p>{name}</p>\n}\n",
				Invoke: `G(GProps{Name: "` + name + `"})`,
			})
			results[i] = strings.TrimSpace(resp.HTML)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		want := "<p>U" + strconv.Itoa(i) + "</p>"
		if results[i] != want {
			t.Errorf("req %d: got %q want %q", i, results[i], want)
		}
	}
}
```

Add imports `sync`, `strconv` to the test file.

- [ ] **Step 2: Run to verify it fails**

Run: `cd playground/server && go test ./... -run TestPoolConcurrent -v`
Expected: FAIL with "undefined: newPool".

- [ ] **Step 3: Implement the pool**

Refactor `render.go`: a `workspace` owns a `play` dir, `viewDir`, and shares the `gsxBin` + `GOCACHE`. Add `run` env support and the pool.

```go
// render.go — add near the top
type workspace struct {
	play    string
	viewDir string
}

type pool struct {
	gsxBin string
	gocache string
	free   chan *workspace
}

// newPool builds gsx once, sets up `size` prepared workspaces sharing one
// GOCACHE, and pre-warms the build cache. Workspaces are handed out per request.
func newPool(gsxMod, work string, size int) (*pool, error) {
	if work == "" {
		var err error
		work, err = os.MkdirTemp("", "gsxpool-")
		if err != nil {
			return nil, err
		}
	}
	gsxBin := filepath.Join(work, "gsx")
	if out, err := run(context.Background(), gsxMod, nil, "go", "build", "-o", gsxBin, "./cmd/gsx"); err != nil {
		return nil, fmt.Errorf("build gsx: %v: %s", err, out)
	}
	gocache := filepath.Join(work, "gocache")
	if err := os.MkdirAll(gocache, 0o755); err != nil {
		return nil, err
	}
	env := []string{"GOCACHE=" + gocache}
	p := &pool{gsxBin: gsxBin, gocache: gocache, free: make(chan *workspace, size)}
	for i := 0; i < size; i++ {
		ws := &workspace{play: filepath.Join(work, fmt.Sprintf("play%d", i))}
		ws.viewDir = filepath.Join(ws.play, "views")
		if err := os.MkdirAll(ws.viewDir, 0o755); err != nil {
			return nil, err
		}
		writeFile(filepath.Join(ws.play, "go.mod"), fmt.Sprintf("module gsxplay\n\ngo 1.23\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n", gsxMod))
		writeFile(filepath.Join(ws.play, "main.go"), "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n\n\t_ \"github.com/gsxhq/gsx\"\n\t\"gsxplay/views\"\n)\n\nfunc main() {\n\tif err := views.Render(context.Background(), os.Stdout); err != nil {\n\t\tpanic(err)\n\t}\n}\n")
		writeFile(filepath.Join(ws.viewDir, "comp.gsx"), "package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n")
		writeShim(ws.viewDir, "Hello(HelloProps{})")
		if out, err := run(context.Background(), ws.play, env, "go", "mod", "tidy"); err != nil {
			return nil, fmt.Errorf("mod tidy: %v: %s", err, out)
		}
		if out, err := run(context.Background(), ws.play, env, gsxBin, "generate", "./views"); err != nil {
			return nil, fmt.Errorf("seed generate: %v: %s", err, out)
		}
		if out, err := run(context.Background(), ws.play, env, "go", "build", "-o", filepath.Join(ws.play, "play-bin"), "."); err != nil {
			return nil, fmt.Errorf("warm build: %v: %s", err, out)
		}
		p.free <- ws
	}
	return p, nil
}

func (p *pool) render(in renderReq) renderResp {
	ws := <-p.free // block until a workspace is free (back-pressure)
	defer func() { p.free <- ws }()
	return renderIn(p.gsxBin, p.gocache, ws, in)
}
```

Change `writeShim` to a free function `writeShim(viewDir, invoke string)` and `run` to take an `env []string` param (append to `os.Environ()`); move the old `(*renderer).render` body into `renderIn(gsxBin, gocache string, ws *workspace, in renderReq) renderResp` using `ws.viewDir`, `ws.play`, and passing `env := []string{"GOCACHE=" + gocache}` to each `run`. Delete the now-unused `renderer`, `newRenderer`, and `(*renderer).writeShim`/`(*renderer).render` (or keep `renderer` only if other code needs it — it does not). Update `run`:

```go
func run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
```

Update Task 1's tests to use the pool: replace `testRenderer *renderer` with `testPool *pool`, build via `newPool(gsxMod, "", 2)` in `TestMain`, and call `testPool.render(...)`.

- [ ] **Step 4: Run all tests**

Run: `cd playground/server && go test ./... -v`
Expected: PASS (TestRenderSuccess, TestRenderDiagnostic, TestRenderEscaping, TestPoolConcurrent).

- [ ] **Step 5: Update `main.go` to use the pool**

```go
// main.go — main(): replace newRenderer with the pool
poolSize := *concurrency
p, err := newPool(*gsxMod, *workIn, poolSize)
if err != nil {
	log.Fatalf("setup: %v", err)
}
mux.HandleFunc("/render", makeRenderHandler(p))
```

Change `handleRender` to a closure `makeRenderHandler(p *pool) http.HandlerFunc` that calls `p.render(in)` (move the existing request-decode/validation logic in). Add a `concurrency` flag (default 4). Build to confirm:

Run: `cd playground/server && go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add playground/server/
git commit -m "feat(playground): per-request isolation via a prewarmed workspace pool"
```

---

## Task 3: Request limits, hardening & structured logging

Bound resource use and make failures observable.

**Files:**
- Modify: `playground/server/main.go`
- Modify: `playground/server/render_test.go` (limit tests via `httptest`)

**Interfaces:**
- Consumes: `makeRenderHandler(p *pool)` from Task 2.
- Produces: `withLimits(next http.Handler, maxBody int64, sem chan struct{}) http.Handler`; CORS origin from env `ALLOWED_ORIGIN` (default `*`).

- [ ] **Step 1: Write failing limit tests**

```go
// add to render_test.go
import (
	"bytes"
	"net/http"
	"net/http/httptest"
)

func TestOversizeRejected(t *testing.T) {
	h := makeRenderHandler(testPool)
	big := bytes.Repeat([]byte("x"), 70*1024)
	body := `{"gsx":"` + string(big) + `","invoke":"Hello(HelloProps{})"}`
	req := httptest.NewRequest("POST", "/render", strings.NewReader(body))
	rec := httptest.NewRecorder()
	withLimits(h, 64*1024, make(chan struct{}, 1)).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}

func TestMethodGuard(t *testing.T) {
	h := makeRenderHandler(testPool)
	req := httptest.NewRequest("GET", "/render", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd playground/server && go test ./... -run 'TestOversize|TestMethodGuard' -v`
Expected: FAIL with "undefined: withLimits" (and 405 may already pass).

- [ ] **Step 3: Implement limits middleware + env CORS**

```go
// main.go
func withLimits(next http.Handler, maxBody int64, sem chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

In `makeRenderHandler`, when `json.Decode` fails due to the body limit, return `http.StatusRequestEntityTooLarge` (check `err.Error()` contains "request body too large"), else 400. Keep the existing 64 KB guard as defense in depth. Add structured request logging in `main` (method, path, ms, outcome). Change `cors` to read `ALLOWED_ORIGIN`:

```go
func cors(h http.Handler) http.Handler {
	origin := os.Getenv("ALLOWED_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		h.ServeHTTP(w, r)
	})
}
```

Wire in `main`: `sem := make(chan struct{}, *concurrency)` and serve `cors(withLimits(mux, 64*1024, sem))`. Read `PORT` env (Cloud Run sets it) falling back to the `-addr` flag.

- [ ] **Step 4: Run tests**

Run: `cd playground/server && go test ./... -v`
Expected: PASS (all, including the two new limit tests).

- [ ] **Step 5: Commit**

```bash
git add playground/server/
git commit -m "feat(playground): request limits, env CORS, structured logging, PORT support"
```

---

## Task 4: Container image with baked GOCACHE

Package the service so a cold instance only recompiles the visitor's one file.

**Files:**
- Create: `playground/server/Dockerfile`
- Create: `playground/server/.dockerignore`

- [ ] **Step 1: Write the Dockerfile**

The image needs the full Go toolchain at runtime (it runs `go build` per request), so it is `golang`-based, not distroless. The build copies the whole gsx repo, builds the server, and pre-warms the pool's `GOCACHE` by running the server's setup once at build time.

```dockerfile
# playground/server/Dockerfile — build context is the gsx repo root.
FROM golang:1.23-bookworm

WORKDIR /gsx
# Copy the whole module so the playground's replace => /gsx resolves.
COPY . /gsx

ENV GOCACHE=/gocache
ENV PLAYGROUND_WORK=/work

# Build the server.
RUN cd /gsx/playground/server && go build -o /usr/local/bin/gsxplayground .

# Pre-warm: build gsx + the prepared module so /gocache is populated in the image
# layer. This script runs the pool setup once and exits.
RUN cd /gsx/playground/server && GSX_PREWARM=1 /usr/local/bin/gsxplayground -prewarm -gsxmod /gsx -work /work || true

EXPOSE 8080
ENV ALLOWED_ORIGIN=https://gsxhq.github.io
ENTRYPOINT ["/usr/local/bin/gsxplayground", "-gsxmod", "/gsx", "-work", "/work"]
```

Add a `-prewarm` flag to `main.go`: when set, construct the pool (which warms `GOCACHE` and writes `/work`) and exit 0 without serving. Ensure the runtime entrypoint **reuses** the same `-work /work` so the prewarmed workspaces + cache are used (do not re-create on boot if present; `newPool` already (re)writes fixed files cheaply and the build cache hits).

```go
// main.go — after flag.Parse()
if *prewarm {
	if _, err := newPool(*gsxMod, *workIn, *concurrency); err != nil {
		log.Fatalf("prewarm: %v", err)
	}
	log.Println("prewarm complete")
	return
}
```

- [ ] **Step 2: Write `.dockerignore`**

```
# playground/server/.dockerignore is relative to the build context (repo root)
node_modules
.git
**/*.x.go
playground/server/*-bin
docs/superpowers
```

- [ ] **Step 3: Build the image**

Run (from the gsx repo root): `docker build -f playground/server/Dockerfile -t gsx-playground:dev .`
Expected: build succeeds; the prewarm RUN logs "prewarm complete".

- [ ] **Step 4: Run and smoke-test the container**

```bash
docker run --rm -p 8088:8080 gsx-playground:dev &
sleep 5
curl -s -X POST http://localhost:8088/render -H 'Content-Type: application/json' \
  -d '{"gsx":"package views\n\ncomponent G(n string){\n\t<p>{n}</p>\n}\n","invoke":"G(GProps{N:\"hi\"})"}'
```
Expected: JSON with `"html":"<p>hi</p>"`. Stop the container afterward.

- [ ] **Step 5: Commit**

```bash
git add playground/server/Dockerfile playground/server/.dockerignore playground/server/main.go
git commit -m "feat(playground): container image with prewarmed GOCACHE + -prewarm mode"
```

---

## Task 5: Cloud Run deploy instructions + frontend wiring

Document the exact deploy (run by the maintainer with GCP access) and point the site build at the deployed URL.

**Files:**
- Create: `playground/server/deploy.md`
- Modify: `gsxhq.github.io/.github/workflows/deploy.yml`

- [ ] **Step 1: Write `deploy.md`**

```markdown
# Deploying the gsx playground to Cloud Run (free tier)

Prereqs: `gcloud` authenticated, a project set, Cloud Run + Cloud Build APIs enabled.

```bash
PROJECT=<your-project>
REGION=us-central1            # free-tier eligible
SERVICE=gsx-playground

# Build & deploy from the gsx repo root (Cloud Build uses the Dockerfile).
gcloud builds submit --project "$PROJECT" \
  --tag "gcr.io/$PROJECT/$SERVICE" \
  --gcs-source-staging-dir "gs://${PROJECT}_cloudbuild/source" \
  .   # ensure -f playground/server/Dockerfile via cloudbuild or rename

gcloud run deploy "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --image "gcr.io/$PROJECT/$SERVICE" \
  --allow-unauthenticated \
  --memory 1Gi --cpu 1 \
  --concurrency 4 \
  --timeout 30 \
  --min-instances 0 --max-instances 3 \
  --set-env-vars ALLOWED_ORIGIN=https://gsxhq.github.io
```

`--min-instances 0` keeps it on the free tier (scale-to-zero). The printed
service URL is the value for `VITE_GSX_PLAYGROUND_API` in the site build.

Cold start (first request after idle) takes a few seconds; warm requests are
sub-second. Watch logs: `gcloud run services logs read $SERVICE --region $REGION`.
```

(If `gcloud builds submit` needs the Dockerfile path, add a minimal `cloudbuild.yaml` that runs `docker build -f playground/server/Dockerfile -t $_IMAGE .`; note that in `deploy.md`.)

- [ ] **Step 2: Wire the frontend build env**

In `gsxhq.github.io/.github/workflows/deploy.yml`, set the API URL for the build step:

```yaml
      - name: Build
        run: npm run build
        env:
          VITE_GSX_PLAYGROUND_API: ${{ vars.GSX_PLAYGROUND_API }}
```

Document that the maintainer sets repo Actions **variable** `GSX_PLAYGROUND_API` to the Cloud Run URL. Until set, `import.meta.env` is undefined and the component falls back to `http://localhost:8088` (playground shows the "API not reachable" message on the deployed site — acceptable until the var is set).

- [ ] **Step 3: Verify the workflow file parses**

Run: `cd gsxhq.github.io && npx --yes js-yaml .github/workflows/deploy.yml >/dev/null && echo OK`
Expected: `OK` (valid YAML).

- [ ] **Step 4: Commit (both repos)**

```bash
# gsx repo
git add playground/server/deploy.md
git commit -m "docs(playground): Cloud Run free-tier deploy instructions"
# site repo
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
git add .github/workflows/deploy.yml
git commit -m "ci(playground): pass VITE_GSX_PLAYGROUND_API to the site build"
```

---

## Task 6: CI check that playground presets stay valid

Guarantee the examples shown in the playground keep compiling + rendering, via the existing corpus harness.

**Files:**
- Create: `internal/corpus/testdata/cases/playground/interpolation.txtar`
- Create: `internal/corpus/testdata/cases/playground/control_flow.txtar`
- Create: `internal/corpus/testdata/cases/playground/composable_class.txtar`
- Create: `internal/corpus/testdata/cases/playground/auto_escaping.txtar`
- Create: `internal/corpus/testdata/cases/playground/composition.txtar`

**Interfaces:**
- Consumes: the corpus harness (`internal/corpus`, `TestCorpus`, `-update`). Each case mirrors a preset in `GsxPlayground.vue` (keep the two in sync; this is the canonical check).

- [ ] **Step 1: Create the five case files**

Each `.txtar` has `input.gsx`, `invoke`, an empty `diagnostics.golden`, and a `render.golden` (filled by `-update`). Mirror the presets exactly. Example for `interpolation.txtar`:

```
-- input.gsx --
package views

component Greeting(name string, count int) {
	<p>Hello, {name}! You have {count} messages.</p>
}
-- invoke --
Greeting(GreetingProps{Name: "World", Count: 3})
-- diagnostics.golden --
```

`control_flow.txtar` (invoke `Inbox(InboxProps{Name: "World", Count: 2})`), `composable_class.txtar` (invoke `Tag(TagProps{Label: "stable", Active: true})`), `auto_escaping.txtar` (invoke `Comment(CommentProps{Body: "<script>alert(1)</script>"})`), `composition.txtar` (invoke `Card(CardProps{Title: "Hello", Children: gsx.Raw("<em>composed</em>")})`) — copy the component bodies verbatim from the presets in `GsxPlayground.vue`.

- [ ] **Step 2: Generate goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Expected: writes `render.golden` (and updates `coverage.golden`); review with `git diff`.

- [ ] **Step 3: Verify they pass without -update**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS — the five `playground/*` cases render and match.

- [ ] **Step 4: Commit**

```bash
git add internal/corpus/testdata/cases/playground/ internal/corpus/testdata/coverage.golden
git commit -m "test(playground): CI-verify playground presets via corpus cases"
```

---

## Self-Review

**Spec coverage:**
- Authentic render — Tasks 1–4 keep the real pipeline; Task 6 verifies fidelity. ✓
- Cloud Run free-tier (scale-to-zero, gVisor) — Task 5 (`--min-instances 0`). ✓
- Fixed-module safety — preserved in Tasks 1–2 (server-written `go.mod`); inputs capped in Task 3. ✓
- API contract — unchanged from prototype; exercised by tests (Task 1). ✓
- Per-request isolation — Task 2 (pool). ✓
- Baked GOCACHE image — Task 4 (`-prewarm` + Dockerfile). ✓
- Request limits / observability — Task 3. ✓
- Frontend wiring — Task 5 (`VITE_GSX_PLAYGROUND_API`). ✓
- CI preset check — Task 6. ✓
- Cold-start perf claim — addressed structurally by Task 4 (baked cache); no separate task needed.

**Open items deferred to the maintainer (from the spec's Open Questions):** Cloud Run region/project (Task 5 placeholders), pool size tuning (Task 2 `-concurrency` default 4, adjustable), backend repo home (stays in gsx repo).

**Placeholder scan:** no TBD/TODO; deploy values are real `gcloud` flags with `<your-project>`/region called out as maintainer inputs.

**Type consistency:** `newPool(gsxMod, work string, size int)`, `(*pool).render(renderReq) renderResp`, `run(ctx, dir, env, name, args...)`, `writeShim(viewDir, invoke)`, `makeRenderHandler(p *pool)`, `withLimits(next, maxBody, sem)` are used consistently across Tasks 2–4.
