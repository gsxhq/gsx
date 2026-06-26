# `gsx generate --watch` + Vite-plugin supervision — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A warm, long-lived `gsx generate --watch` that regenerates `.x.go` on `.gsx`/`.go` changes without a per-save `go/packages.Load`, streaming NDJSON diagnostics the Vite plugin consumes.

**Architecture:** A thin watcher/daemon in the `gen` package orchestrates existing pieces — `internal/codegen.CachedResolver` for warm in-process codegen, `discoverDirs`/`moduleRoot` for targets, the file-cache's `restore` for hash-gated writes, and `diag.RenderJSON` for the diagnostic shape. A `watchSession` owns the resolver and a rebuild policy; `@gsxhq/vite-plugin-gsx` spawns the daemon once and drives the overlay/reload from its NDJSON stream.

**Tech Stack:** Go (`gen`, `internal/codegen`, `internal/diag`), `github.com/fsnotify/fsnotify` (new, CLI-only), TypeScript/vitest (`@gsxhq/vite-plugin-gsx`).

**Spec:** `docs/superpowers/specs/2026-06-25-gsx-generate-watch-design.md`.

## Global Constraints

- Runtime stays **standard-library only**. `fsnotify` is imported **only** by `gen` (the CLI/generator), never by the runtime module root (`gsx` package). (Spec: "runtime is standard-library only; the generator/CLI may use external deps.")
- **DRY — reuse, do not reinvent:** `internal/codegen.CachedResolver` (warm codegen), `gen.generateCached` (configured cold path), `gen.discoverDirs` / `gen.moduleRoot`, `gen.restore` (hash-gated write), `diag.RenderJSON` (the one diagnostic JSON shape — must be byte-identical to `gsx generate --json`).
- **NDJSON on stdout, logs/human output on stderr.** stdout must stay a clean newline-delimited JSON stream in `--format=ndjson`.
- New daemon code lives in three focused files: `gen/watch.go` (orchestration), `gen/watchsession.go` (resolver + rebuild policy), `gen/watchemit.go` (renderers).
- Plugin option surface stays **backward-compatible**; bump `@gsxhq/vite-plugin-gsx` a minor version.
- Watcher matches `*.gsx` and non-generated `*.go`; **excludes** `*.x.go`, `tmp/`, `dist/`, `node_modules/`, `.git/`.
- Correctness boundary (slice 1): warm resolver is reused for `.gsx` edits; the resolver is **rebuilt** on `*.go`/`go.mod`/`go.sum` change or a cached-importer miss. Cross-package prop-signature staleness is the documented slice-1 limitation (slice 2).

---

## Task 1: `--watch` / `--format` flags wired to a `runWatch` entry point

**Files:**
- Modify: `gen/main.go:206` (`runGenerate` — add the two flags + delegate)
- Create: `gen/watch.go` (the `runWatch` entry point — stub in this task)
- Test: `gen/watch_test.go`

**Interfaces:**
- Consumes: `runGenerate(args []string, stdout, stderr io.Writer, quiet, verbose, noCache bool, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, predLabel string, fm codegen.FieldMatcher, cssMin, jsMin func(string) (string, error)) int` (existing, `gen/main.go:206`).
- Produces: `func runWatch(cfg watchConfig) int` and `type watchConfig struct { paths []string; format string; stdout, stderr io.Writer; quiet, verbose bool; filterPkgs []string; aliases []codegen.FilterAlias; cls *attrclass.Classifier; predLabel string; fm codegen.FieldMatcher; cssMin, jsMin func(string) (string, error) }`.

- [ ] **Step 1: Write the failing test** — `gen/watch_test.go`

```go
package gen

import (
	"bytes"
	"strings"
	"testing"
)

// --watch with --format=ndjson on an empty/non-existent path set must not block:
// runWatch returns promptly with exit 0 when there are no dirs to watch, and
// writes nothing to stdout that isn't valid (empty is fine here).
func TestRunWatch_NoDirsReturnsCleanly(t *testing.T) {
	dir := t.TempDir() // no .gsx files
	var out, errb bytes.Buffer
	code := runWatch(watchConfig{
		paths:  []string{dir},
		format: "ndjson",
		stdout: &out,
		stderr: &errb,
	})
	if code != 0 {
		t.Fatalf("runWatch exit = %d, want 0; stderr=%s", code, errb.String())
	}
	// stdout in ndjson mode must never contain a non-JSON line.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, "{") {
			t.Fatalf("non-JSON stdout line: %q", line)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./gen/ -run TestRunWatch_NoDirsReturnsCleanly -v`
Expected: FAIL — `undefined: runWatch` / `undefined: watchConfig`.

- [ ] **Step 3: Create `gen/watch.go` with the stub**

```go
package gen

import (
	"io"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// watchConfig carries everything runWatch needs, mirroring runGenerate's
// configured options so watch honors the same filters/classifier/minifiers.
type watchConfig struct {
	paths      []string
	format     string // "" (human) or "ndjson"
	stdout     io.Writer
	stderr     io.Writer
	quiet      bool
	verbose    bool
	filterPkgs []string
	aliases    []codegen.FilterAlias
	cls        *attrclass.Classifier
	predLabel  string
	fm         codegen.FieldMatcher
	cssMin     func(string) (string, error)
	jsMin      func(string) (string, error)
}

// runWatch starts the long-lived generate-on-change daemon. Stub: returns 0 when
// there are no dirs to watch. The watch loop is added in Task 5.
func runWatch(cfg watchConfig) int {
	dirs, err := discoverDirs(cfg.paths)
	if err != nil || len(dirs) == 0 {
		return 0
	}
	return 0
}
```

- [ ] **Step 4: Add the flags + delegation in `runGenerate`** (`gen/main.go`, inside the `flag.NewFlagSet("generate", …)` block near line 207 and after `gfs.Parse`)

In the flag declarations (alongside `jsonFlag`):

```go
	var watchFlag bool
	var formatFlag string
	gfs.BoolVar(&watchFlag, "watch", false, "watch sources and regenerate on change (long-lived)")
	gfs.StringVar(&formatFlag, "format", "", "output format for --watch: \"ndjson\" for machine consumption")
```

Immediately after the flags are parsed and `paths` is resolved (before the one-shot generate), delegate:

```go
	if watchFlag {
		return runWatch(watchConfig{
			paths: paths, format: formatFlag,
			stdout: stdout, stderr: stderr, quiet: quiet, verbose: verbose,
			filterPkgs: filterPkgs, aliases: aliases, cls: cls,
			predLabel: predLabel, fm: fm, cssMin: cssMin, jsMin: jsMin,
		})
	}
```

(Use the same local variable names `runGenerate` already binds for `paths`, `filterPkgs`, `aliases`, `cls`, `predLabel`, `fm`, `cssMin`, `jsMin`. If `paths` is computed after flag parse in the current code, place the delegation right after that point.)

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./gen/ -run TestRunWatch_NoDirsReturnsCleanly -v`
Expected: PASS.

- [ ] **Step 6: Verify the build and flag plumbing**

Run: `go build ./... && go run ./cmd/gsx generate --watch --format=ndjson /tmp/does-not-exist-xyz; echo "exit $?"`
Expected: BUILD OK; command exits 0 promptly (no dirs).

- [ ] **Step 7: Commit**

```bash
git add gen/watch.go gen/watch_test.go gen/main.go
git commit -m "feat(gen): gsx generate --watch/--format flags + runWatch entry stub"
```

---

## Task 2: Filter-aware, whole-module warm resolver

**Files:**
- Create: `gen/watchsession.go` (the resolver constructor only, this task)
- Test: `gen/watchsession_test.go`

**Interfaces:**
- Consumes: `codegen.NewCachedResolver(moduleDir string, filterPkgs []string, aliases []FilterAlias, allowImports []string) (*codegen.CachedResolver, error)`; `codegen.StdImportPath`; the existing `CachedResolver` struct in `gen/resolver.go` (`type CachedResolver struct { inner *codegen.CachedResolver }`) and its `Generate(dir string, srcOverride map[string][]byte) (Result, error)`.
- Produces: `func newModuleResolver(moduleDir string, filterPkgs []string, aliases []codegen.FilterAlias) (*CachedResolver, error)` — builds a resolver whose importer covers the **whole module** (all in-module packages + their transitive deps) so cross-package component refs resolve.

**Why this task exists:** `gen.NewCachedResolver` (the playground wrapper) hardcodes std-only filters and a fixed import allowlist. Watch needs the user's filter packages threaded and the module's *own* packages loaded. This validates the spec's central assumption — that the warm resolver path produces correct output for a **multi-package** project — before any watching is built.

- [ ] **Step 1: Write the failing test** — `gen/watchsession_test.go`

```go
package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A two-package module: pkg `comp` defines a component; pkg `views` references
// it. The whole-module resolver must let `views` regenerate with the cross-
// package ref resolved (no "cached importer: not loaded" error).
func TestNewModuleResolver_CrossPackage(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.23\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	write("comp/card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	write("views/page.gsx", "package views\n\nimport \"example.com/m/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hi\"/>\n}\n")
	mustRun(t, root, "go", "mod", "tidy")

	// Initial cold generate so comp's .x.go exists for the resolver to load.
	if _, err := generateCached([]string{filepath.Join(root, "comp"), filepath.Join(root, "views")}, nil, nil, nil, "", nil, false, nil, nil); err != nil {
		t.Fatalf("initial generate: %v", err)
	}

	r, err := newModuleResolver(root, nil, nil)
	if err != nil {
		t.Fatalf("newModuleResolver: %v", err)
	}
	res, err := r.Generate(filepath.Join(root, "views"), nil)
	if err != nil {
		t.Fatalf("warm Generate(views): %v (diags=%v)", err, res.Diags)
	}
	// The regenerated views/.x.go must call comp.Card.
	var got string
	for path, b := range res.Files {
		if strings.HasSuffix(path, "page.x.go") {
			got = string(b)
		}
	}
	if !strings.Contains(got, "comp.Card") {
		t.Fatalf("regenerated page.x.go does not reference comp.Card:\n%s", got)
	}
}
```

Add these test helpers at the bottom of `gen/watchsession_test.go` (if `mustRun`/`gsxModuleDir` are not already shared in the package's test files — check `grep -rn "func mustRun\|func gsxModuleDir" gen/*_test.go` first and reuse if present):

```go
func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// gsxModuleDir returns the absolute path of this gsx module checkout, for the
// test fixture's replace directive.
func gsxModuleDir(t *testing.T) string {
	t.Helper()
	// gen/ is one level under the module root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}
```

(Imports for the test: `os`, `os/exec`, `path/filepath`, `strings`, `testing`.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./gen/ -run TestNewModuleResolver_CrossPackage -v`
Expected: FAIL — `undefined: newModuleResolver`.

- [ ] **Step 3: Implement `newModuleResolver`** in `gen/watchsession.go`

```go
package gen

import (
	"github.com/gsxhq/gsx/internal/codegen"
)

// newModuleResolver builds a warm CachedResolver whose importer covers the whole
// module: all in-module packages (so cross-package gsx component refs resolve)
// plus their transitive dependencies. filterPkgs/aliases thread the user's
// pipeline filters, exactly as the cold path does. The one-time packages.Load
// happens here; resolver.Generate calls afterwards run fully in-process.
func newModuleResolver(moduleDir string, filterPkgs []string, aliases []codegen.FilterAlias) (*CachedResolver, error) {
	// "./..." expands to every package in the module; packages.Load (NeedDeps)
	// pulls their transitive deps into the importer map. This is what lets a
	// later resolver.Generate of one package see sibling packages' types.
	allow := []string{"./..."}
	inner, err := codegen.NewCachedResolver(moduleDir, append([]string{codegen.StdImportPath}, filterPkgs...), aliases, allow)
	if err != nil {
		return nil, err
	}
	return &CachedResolver{inner: inner}, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./gen/ -run TestNewModuleResolver_CrossPackage -v`
Expected: PASS.

**If it fails with `cached importer: "example.com/m/comp" not loaded`:** `"./..."` did not expand inside `codegen.NewCachedResolver`'s `packages.Load`. Fall back to computing explicit import paths: derive each in-module package path as `modPath + "/" + filepath.ToSlash(rel(moduleDir, dir))` for every dir under `moduleDir` containing a `.gsx`/`.go`, and pass that slice as `allow` instead of `"./..."`. (`modPath` comes from `moduleRoot(moduleDir)`.) Re-run.

- [ ] **Step 5: Commit**

```bash
git add gen/watchsession.go gen/watchsession_test.go
git commit -m "feat(gen): filter-aware whole-module warm resolver (newModuleResolver)"
```

---

## Task 3: `watchSession` — startup, warm regenerate, rebuild policy

**Files:**
- Modify: `gen/watchsession.go` (add the `watchSession` type + methods)
- Test: `gen/watchsession_test.go`

**Interfaces:**
- Consumes: `newModuleResolver` (Task 2); `generateCached(paths, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, predLabel string, fm codegen.FieldMatcher, useCache bool, cssMin, jsMin func(string) (string, error)) (Result, error)` (existing, `gen/cache.go:16`); `restore(dir string, out pkgOutput) ([]string, error)` (existing, `gen/cache.go:171`); `pkgOutput` = `map[string][]byte` keyed `<base>.x.go`; `moduleRoot(dir) (root, modPath string, err error)`.
- Produces:
  - `type cycleResult struct { Dir string; Written []string; Diags []diag.Diagnostic; OK bool; Err error }`
  - `func newWatchSession(cfg watchConfig) (*watchSession, []cycleResult, error)` — runs the initial cold generate (all dirs), builds the resolver, returns the session + startup results.
  - `func (s *watchSession) regen(dir string) cycleResult` — warm path for one dirty dir, with cached-importer-miss → rebuild+retry.
  - `func (s *watchSession) rebuild() error` — rebuild the resolver (dep-surface change).

- [ ] **Step 1: Write the failing test** (append to `gen/watchsession_test.go`)

```go
// A pure .gsx edit regenerates via the warm resolver and updates the .x.go.
func TestWatchSession_WarmRegen(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>one</h1>\n}\n")
	mustRun(t, root, "go", "mod", "tidy")

	s, _, err := newWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	// Edit the source, then warm-regen.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>two</h1>\n}\n")
	r := s.regen(filepath.Join(root, "views"))
	if !r.OK {
		t.Fatalf("regen not OK: err=%v diags=%v", r.Err, r.Diags)
	}
	xgo, _ := os.ReadFile(filepath.Join(root, "views", "page.x.go"))
	if !strings.Contains(string(xgo), `"two"`) {
		t.Fatalf("page.x.go not updated to \"two\":\n%s", xgo)
	}
}

// A broken .gsx yields OK=false with diagnostics and writes no .x.go change.
func TestWatchSession_RegenError(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>{undefinedSym}</h1>\n}\n")
	mustRun(t, root, "go", "mod", "tidy")

	s, _, err := newWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	r := s.regen(filepath.Join(root, "views"))
	if r.OK || len(r.Diags) == 0 {
		t.Fatalf("expected OK=false with diagnostics, got OK=%v diags=%v", r.OK, r.Diags)
	}
}
```

Add shared helpers (only if not already present in the package's tests):

```go
func writeFileT(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMod(t *testing.T, root string) {
	t.Helper()
	writeFileT(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.23\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen/ -run TestWatchSession -v`
Expected: FAIL — `undefined: newWatchSession`.

- [ ] **Step 3: Implement `watchSession`** (append to `gen/watchsession.go`)

```go
import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

type cycleResult struct {
	Dir     string
	Written []string
	Diags   []diag.Diagnostic
	OK      bool
	Err     error
}

type watchSession struct {
	cfg      watchConfig
	root     string
	resolver *CachedResolver
}

// newWatchSession runs the initial cold generate (full cross-package correctness,
// creates every .x.go), then builds the warm resolver over the now-complete
// module. Returns the per-dir startup results so the caller can emit them.
func newWatchSession(cfg watchConfig) (*watchSession, []cycleResult, error) {
	dirs, err := discoverDirs(cfg.paths)
	if err != nil {
		return nil, nil, err
	}
	root, _, err := moduleRoot(dirs[0])
	if err != nil {
		return nil, nil, err
	}
	// 1) Cold generate (existing configured path): all .x.go on disk.
	res, gerr := generateCached(cfg.paths, cfg.filterPkgs, cfg.aliases, cfg.cls, cfg.predLabel, cfg.fm, true, cfg.cssMin, cfg.jsMin)
	startup := []cycleResult{{Dir: root, Written: res.Written, Diags: res.Diags, OK: gerr == nil, Err: opErr(res, gerr)}}

	s := &watchSession{cfg: cfg, root: root}
	// 2) Build the warm resolver over the whole module.
	if err := s.rebuild(); err != nil {
		return nil, startup, err
	}
	return s, startup, nil
}

func (s *watchSession) rebuild() error {
	r, err := newModuleResolver(s.root, s.cfg.filterPkgs, s.cfg.aliases)
	if err != nil {
		return err
	}
	s.resolver = r
	return nil
}

// regen warm-regenerates one dir. On a cached-importer miss (a newly added
// import the resolver never loaded), it rebuilds once and retries.
func (s *watchSession) regen(dir string) cycleResult {
	res, err := s.resolver.Generate(dir, nil)
	if err != nil && isCachedImporterMiss(err, res) {
		if rebuildErr := s.rebuild(); rebuildErr == nil {
			res, err = s.resolver.Generate(dir, nil)
		}
	}
	written := writeFiles(dir, res.Files)
	return cycleResult{
		Dir:     dir,
		Written: written,
		Diags:   res.Diags,
		OK:      err == nil && !anyErrorDiag(res.Diags),
		Err:     opErr(res, err),
	}
}

// writeFiles writes a resolver Result's Files (keyed by absolute .x.go paths)
// hash-gated via restore, returning the paths actually written.
func writeFiles(dir string, files map[string][]byte) []string {
	po := pkgOutput{}
	for absXGo, b := range files {
		po[filepath.Base(absXGo)] = b
	}
	written, _ := restore(dir, po)
	return written
}

// opErr extracts a genuine operational error (not a compile diagnostic) from a
// Result/err pair. Error-severity diagnostics are reported via Diags, not here.
func opErr(res Result, err error) error {
	if err != nil && !anyErrorDiag(res.Diags) {
		return err
	}
	return nil
}

func isCachedImporterMiss(err error, res Result) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "cached importer") {
		return true
	}
	for _, d := range res.Diags {
		if strings.Contains(d.Message, "cached importer") {
			return true
		}
	}
	return false
}

var _ = bytes.Equal // (retain bytes import only if used; remove otherwise)
```

(Remove the unused `bytes` import / the `var _` guard if `bytes` ends up unused.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./gen/ -run TestWatchSession -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add gen/watchsession.go gen/watchsession_test.go
git commit -m "feat(gen): watchSession — cold startup, warm regen, rebuild-on-miss"
```

---

## Task 4: Event emitter — human + NDJSON (reusing `diag.RenderJSON`)

**Files:**
- Create: `gen/watchemit.go`
- Test: `gen/watchemit_test.go`

**Interfaces:**
- Consumes: `cycleResult` (Task 3); `diag.RenderJSON(w io.Writer, diags []diag.Diagnostic) error` (existing, `internal/diag/render.go:55`).
- Produces: `type emitter struct { ndjson bool; stdout, stderr io.Writer }`; `func (e *emitter) start(root string, watching []string)`; `func (e *emitter) cycle(r cycleResult)`.

- [ ] **Step 1: Write the failing test** — `gen/watchemit_test.go`

```go
package gen

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func TestEmitter_NDJSON_GeneratedOK(t *testing.T) {
	var out, errb bytes.Buffer
	e := &emitter{ndjson: true, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/views", Written: []string{"/m/views/page.x.go"}, OK: true})

	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &ev); err != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out.String(), err)
	}
	if ev["event"] != "generated" || ev["ok"] != true {
		t.Fatalf("unexpected event: %v", ev)
	}
	if _, hasDur := ev["durationMs"]; !hasDur {
		t.Fatalf("missing durationMs: %v", ev)
	}
}

func TestEmitter_NDJSON_DiagnosticsShapeMatchesRenderJSON(t *testing.T) {
	d := diag.Diagnostic{Severity: diag.Error, Code: "x", Message: "boom"}
	var want bytes.Buffer
	_ = diag.RenderJSON(&want, []diag.Diagnostic{d})

	var out, errb bytes.Buffer
	e := &emitter{ndjson: true, stdout: &out, stderr: &errb}
	e.cycle(cycleResult{Dir: "/m/views", OK: false, Diags: []diag.Diagnostic{d}})

	var ev map[string]json.RawMessage
	_ = json.Unmarshal([]byte(strings.TrimSpace(out.String())), &ev)
	// The diagnostics field must equal RenderJSON's encoding (same shape, no 3rd copy).
	if strings.TrimSpace(string(ev["diagnostics"])) != strings.TrimSpace(want.String()) {
		t.Fatalf("diagnostics shape drift:\n got=%s\nwant=%s", ev["diagnostics"], want.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen/ -run TestEmitter -v`
Expected: FAIL — `undefined: emitter`.

- [ ] **Step 3: Implement the emitter** — `gen/watchemit.go`

```go
package gen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gsxhq/gsx/internal/diag"
)

type emitter struct {
	ndjson bool
	stdout io.Writer
	stderr io.Writer
}

func (e *emitter) start(root string, watching []string) {
	if e.ndjson {
		e.line(map[string]any{"event": "start", "root": root, "watching": watching})
		return
	}
	fmt.Fprintf(e.stderr, "gsx: watching %d dir(s) under %s\n", len(watching), root)
}

func (e *emitter) cycle(r cycleResult) {
	if e.ndjson {
		ev := map[string]any{
			"event":       "generated",
			"ok":          r.OK,
			"durationMs":  r.durationMs(),
			"written":     baseNames(r.Written),
			"diagnostics": rawDiagnostics(r.Diags),
		}
		e.line(ev)
		return
	}
	if r.OK {
		fmt.Fprintf(e.stderr, "regenerated %s — %d file(s), %dms\n", r.Dir, len(r.Written), r.durationMs())
		return
	}
	// RenderRich's SourceProvider is func(name string) ([]byte, bool); the watch
	// daemon doesn't surface source frames, so return "not found".
	src := func(string) ([]byte, bool) { return nil, false }
	diag.RenderRich(e.stderr, r.Diags, src)
}

func (e *emitter) line(ev map[string]any) {
	b, _ := json.Marshal(ev)
	e.stdout.Write(b)
	e.stdout.Write([]byte("\n"))
}

// rawDiagnostics encodes diags through the canonical RenderJSON so the NDJSON
// diagnostics field is byte-identical to `gsx generate --json`.
func rawDiagnostics(d []diag.Diagnostic) json.RawMessage {
	var buf bytes.Buffer
	_ = diag.RenderJSON(&buf, d)
	return json.RawMessage(bytes.TrimSpace(buf.Bytes()))
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, p)
	}
	return out
}
```

Add a `DurMs` field to `cycleResult` and a `durationMs()` accessor (so the cycle carries its measured time). In `gen/watchsession.go`, extend `cycleResult` with `DurMs int64` and add:

```go
func (r cycleResult) durationMs() int64 { return r.DurMs }
```

and stamp it in `regen` by wrapping the body in `start := time.Now()` … `res.DurMs` … `out.DurMs = time.Since(start).Milliseconds()` before returning (import `time`).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./gen/ -run TestEmitter -v`
Expected: PASS (both). The second test guarantees zero diagnostic-shape drift.

- [ ] **Step 5: Commit**

```bash
git add gen/watchemit.go gen/watchemit_test.go gen/watchsession.go
git commit -m "feat(gen): watch event emitter (human + NDJSON reusing diag.RenderJSON)"
```

---

## Task 5: The watcher + debounce loop (`runWatch` real implementation)

**Files:**
- Modify: `gen/watch.go` (replace the stub `runWatch` with the real loop)
- Modify: `go.mod` / `go.sum` (add `github.com/fsnotify/fsnotify`)
- Test: `gen/watch_integration_test.go`

**Interfaces:**
- Consumes: `newWatchSession` / `watchSession.regen` / `watchSession.rebuild` (Task 3); `emitter` (Task 4); `discoverDirs`, `moduleRoot`.
- Produces: the finished `runWatch` (replacing the Task 1 stub).

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/fsnotify/fsnotify@latest
go mod tidy
```
Expected: `go.mod` now requires `github.com/fsnotify/fsnotify`.

- [ ] **Step 2: Write the failing integration test** — `gen/watch_integration_test.go`

```go
package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// drive runWatch in a goroutine over a temp module; touch a .gsx; assert a
// `generated ok:true` NDJSON line and the updated .x.go. A goroutine-safe buffer
// avoids racing the writer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRunWatch_RegeneratesOnGsxChange(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>one</h1>\n}\n")
	mustRun(t, root, "go", "mod", "tidy")

	out := &syncBuf{}
	errb := &syncBuf{}
	done := make(chan int, 1)
	stop := make(chan struct{})
	go func() {
		done <- runWatchWithStop(watchConfig{
			paths: []string{filepath.Join(root, "views")}, format: "ndjson",
			stdout: out, stderr: errb,
		}, stop)
	}()

	waitFor(t, 5*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })

	// Edit and expect a generated event with the new content compiled in.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>two</h1>\n}\n")
	waitFor(t, 5*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })

	xgo, _ := os.ReadFile(filepath.Join(root, "views", "page.x.go"))
	if !strings.Contains(string(xgo), `"two"`) {
		t.Fatalf("page.x.go not updated:\n%s", xgo)
	}
	close(stop)
	<-done
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func countGenerated(s string, ok bool) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev["event"] == "generated" && ev["ok"] == ok {
			n++
		}
	}
	return n
}
```

(Note: the test calls `runWatchWithStop(cfg, stop)`. Implement `runWatch` as a thin wrapper: `func runWatch(cfg watchConfig) int { return runWatchWithStop(cfg, nil) }`, where a nil stop channel means "until SIGINT/SIGTERM".)

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./gen/ -run TestRunWatch_RegeneratesOnGsxChange -v`
Expected: FAIL — `undefined: runWatchWithStop`.

- [ ] **Step 4: Implement the loop** — replace `runWatch` in `gen/watch.go`

```go
package gen

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

func runWatch(cfg watchConfig) int { return runWatchWithStop(cfg, nil) }

// runWatchWithStop runs the daemon until `stop` is closed (tests) or a SIGINT/
// SIGTERM arrives (nil stop). Returns a process exit code.
func runWatchWithStop(cfg watchConfig, stop <-chan struct{}) int {
	em := &emitter{ndjson: cfg.format == "ndjson", stdout: cfg.stdout, stderr: cfg.stderr}

	sess, startup, err := newWatchSession(cfg)
	for _, r := range startup {
		em.cycle(r)
	}
	if err != nil {
		em.emitError(err)
		return 1
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		em.emitError(err)
		return 1
	}
	defer w.Close()

	roots, _ := discoverDirs(cfg.paths)
	addWatchTree(w, sess.root, roots)
	em.start(sess.root, roots)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	var pending = map[string]bool{} // dirty package dirs
	var depDirty bool               // a .go/go.mod/go.sum changed → rebuild
	var timer *time.Timer
	const debounce = 100 * time.Millisecond
	fire := make(chan struct{}, 1)

	schedule := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounce, func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
	}

	for {
		select {
		case <-stop:
			return 0
		case <-sig:
			return 0
		case ev := <-w.Events:
			if !watchable(ev.Name) {
				continue
			}
			if isDepFile(ev.Name) {
				depDirty = true
			}
			pending[filepath.Dir(ev.Name)] = true
			// Newly created dirs join the watch set.
			if ev.Op&fsnotify.Create != 0 {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() && !excludedDir(ev.Name) {
					_ = w.Add(ev.Name)
				}
			}
			schedule()
		case <-fire:
			if depDirty {
				if rerr := sess.rebuild(); rerr != nil {
					em.emitError(rerr)
				}
				depDirty = false
			}
			for dir := range pending {
				if onlyGeneratedRemains(dir) {
					continue
				}
				em.cycle(sess.regen(dir))
			}
			pending = map[string]bool{}
		case werr := <-w.Errors:
			em.emitError(werr)
		}
	}
}

// addWatchTree adds every non-excluded subdir under each root to the watcher
// (fsnotify is non-recursive).
func addWatchTree(w *fsnotify.Watcher, moduleRoot string, roots []string) {
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if excludedDir(p) {
				return filepath.SkipDir
			}
			_ = w.Add(p)
			return nil
		})
	}
}

func watchable(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".x.go") {
		return false // never react to our own output
	}
	return strings.HasSuffix(base, ".gsx") || strings.HasSuffix(base, ".go") || isDepFile(path)
}

func isDepFile(path string) bool {
	b := filepath.Base(path)
	if b == "go.mod" || b == "go.sum" {
		return true
	}
	return strings.HasSuffix(b, ".go") && !strings.HasSuffix(b, ".x.go")
}

func excludedDir(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		switch seg {
		case "tmp", "dist", "node_modules", ".git":
			return true
		}
	}
	return false
}

// onlyGeneratedRemains reports whether dir has no .gsx (so regenerating is moot,
// e.g. a dir that only held a since-deleted source). Conservative: returns false
// when any .gsx is present.
func onlyGeneratedRemains(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gsx") {
			return false
		}
	}
	return true
}
```

Add `emitError` to the emitter (`gen/watchemit.go`):

```go
func (e *emitter) emitError(err error) {
	if e.ndjson {
		e.line(map[string]any{"event": "error", "message": err.Error()})
		return
	}
	fmt.Fprintf(e.stderr, "gsx: %v\n", err)
}
```

- [ ] **Step 5: Run the integration test to verify it passes**

Run: `go test ./gen/ -run TestRunWatch_RegeneratesOnGsxChange -v`
Expected: PASS.

- [ ] **Step 6: Add the debounce + self-loop guard tests** (append to `gen/watch_integration_test.go`)

```go
// A burst of writes within the debounce window coalesces into a single cycle.
func TestRunWatch_DebounceCoalesces(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>0</h1>\n}\n")
	mustRun(t, root, "go", "mod", "tidy")

	out := &syncBuf{}
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- runWatchWithStop(watchConfig{paths: []string{filepath.Join(root, "views")}, format: "ndjson", stdout: out, stderr: &syncBuf{}}, stop) }()
	waitFor(t, 5*time.Second, func() bool { return strings.Contains(out.String(), `"event":"start"`) })

	for i := 1; i <= 5; i++ { // 5 rapid writes
		writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>"+string(rune('0'+i))+"</h1>\n}\n")
		time.Sleep(10 * time.Millisecond) // all within the 100ms window
	}
	waitFor(t, 5*time.Second, func() bool { return countGenerated(out.String(), true) >= 1 })
	time.Sleep(300 * time.Millisecond) // let any extra cycles flush
	if n := countGenerated(out.String(), true); n > 2 {
		t.Fatalf("debounce failed: %d generated cycles for a 5-write burst (want 1, ≤2 tolerated)", n)
	}
	close(stop)
	<-done
}
```

- [ ] **Step 7: Run the full watch test set + the whole suite**

Run: `go test ./gen/ -run TestRunWatch -v && go test -short ./...`
Expected: PASS; no regressions.

- [ ] **Step 8: Commit**

```bash
git add gen/watch.go gen/watchemit.go gen/watch_integration_test.go go.mod go.sum
git commit -m "feat(gen): gsx generate --watch loop (fsnotify + debounce + session)"
```

---

## Task 6: Plugin supervision — `@gsxhq/vite-plugin-gsx`

**Repo:** `~/personal/gsxhq/vite-plugin-gsx` (NOT the worktree). Branch first: `git -C ~/personal/gsxhq/vite-plugin-gsx checkout -b feat/watch-daemon`.

**Files:**
- Modify: `src/index.ts` (spawn the daemon, consume NDJSON, drive overlay/recovery)
- Modify: `test/index.test.ts` (streaming fake)
- Modify: `test/fixtures/` (add a streaming fake `gsx --watch`)
- Modify: `package.json` (minor version bump)

**Interfaces:**
- Consumes: the daemon's NDJSON contract — lines `{"event":"start",…}`, `{"event":"generated","ok":bool,"durationMs":n,"written":[…],"diagnostics":[…]}`, `{"event":"error","message":…}`; the existing `toViteError(diagnostics, readSource)` and `/__reload` middleware in `src/index.ts`.
- Produces: a daemon-supervising `gsx()` plugin that no longer watches `.gsx` itself.

- [ ] **Step 1: Branch the plugin repo**

```bash
git -C ~/personal/gsxhq/vite-plugin-gsx checkout -b feat/watch-daemon
```

- [ ] **Step 2: Write the failing test** — a streaming fake + an assertion that a `generated ok:false` shows the overlay and a subsequent `ok:true` clears it.

In `test/fixtures/fake-watch.mjs` (new) — emits canned NDJSON on a schedule controlled by sentinel files:

```js
#!/usr/bin/env node
// Streaming fake of `gsx generate --watch --format=ndjson`.
// Emits {event:"start"}, then on each change to ./trigger emits a generated
// event whose ok/diagnostics depend on whether ./failmode exists.
import { existsSync, watchFile } from "node:fs";
import { join } from "node:path";

const cwd = process.cwd();
process.stdout.write(JSON.stringify({ event: "start", root: cwd, watching: [cwd] }) + "\n");

function emit() {
  const fail = existsSync(join(cwd, "failmode"));
  const ev = fail
    ? { event: "generated", ok: false, durationMs: 5, written: [], diagnostics: [{ file: "x.gsx", range: { start: { line: 1, col: 1 }, end: { line: 1, col: 2 } }, severity: "error", code: "syntax", message: "boom" }] }
    : { event: "generated", ok: true, durationMs: 5, written: ["x.x.go"], diagnostics: [] };
  process.stdout.write(JSON.stringify(ev) + "\n");
}
watchFile(join(cwd, "trigger"), { interval: 50 }, emit);
```

In `test/index.test.ts`, add (mirroring the existing recovery test, but driven by the stream):

```ts
it("daemon ok:false shows overlay; subsequent ok:true clears it", async () => {
  // start the plugin pointed at fake-watch.mjs; capture server.ws.send.
  // (Reuse the harness used by existing tests to boot the plugin with a custom command.)
  await startWithWatchCommand(["node", fakeWatch]); // helper analogous to start()
  const send = vi.spyOn(server.ws, "send");

  writeFileSync(join(root, "failmode"), "");
  touch(join(root, "trigger")); // → generated ok:false
  await vi.waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: "error" })), { timeout: 2000 });

  send.mockClear();
  rmSync(join(root, "failmode"));
  touch(join(root, "trigger")); // → generated ok:true (recovery)
  await vi.waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: "full-reload" })), { timeout: 2000 });
});
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd ~/personal/gsxhq/vite-plugin-gsx && npm test -- -t "daemon ok:false"`
Expected: FAIL (no daemon supervision yet / `startWithWatchCommand` undefined).

- [ ] **Step 4: Implement daemon supervision** in `src/index.ts`

Replace the chokidar `.gsx` watcher + per-change `generate()` with a spawned daemon and an NDJSON line reader. Key shape:

```ts
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createInterface } from "node:readline";

// inside configureServer(server):
const argv = [...opts.command, "generate", "--watch", "--format=ndjson", ...opts.paths];
const child = spawn(argv[0], argv.slice(1), { cwd: opts.cwd });
let errorShown = false;

const rl = createInterface({ input: child.stdout });
rl.on("line", (line) => {
  let ev: any;
  try { ev = JSON.parse(line); } catch { return; }
  if (ev.event === "generated") {
    if (!ev.ok) {
      for (const d of ev.diagnostics) logger.error(`[vite-plugin-gsx] ${d.file}: ${d.message}`, { timestamp: true });
      const err = toViteError(ev.diagnostics, readSource);
      if (err) { errorShown = true; server.ws.send({ type: "error", err }); }
    } else if (errorShown) {
      errorShown = false;
      server.ws.send({ type: "full-reload", path: "*" });
    }
  } else if (ev.event === "error") {
    logger.error(`[vite-plugin-gsx] ${ev.message}`, { timestamp: true });
  }
});
child.stderr.on("data", (b) => logger.info(String(b)));
child.on("exit", (code) => logger.warn(`[vite-plugin-gsx] gsx --watch exited (${code})`));
server.httpServer?.on("close", () => child.kill());
```

Keep the `/__reload` middleware exactly as-is (the Go-POST still drives success reloads). Remove the old `server.watcher.on("change"/"add"/"unlink")` registration and the in-process `generate()`/debounce. Add the `startWithWatchCommand` test helper alongside the existing `start` helper.

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd ~/personal/gsxhq/vite-plugin-gsx && npm test`
Expected: PASS (new test + existing suite green; the old `/__reload` test still passes).

- [ ] **Step 6: Bump the version + build**

Run:
```bash
cd ~/personal/gsxhq/vite-plugin-gsx
npm version minor
npm run build
```
Expected: `package.json` version bumped (e.g. 0.3.0); `dist/` rebuilt (prepublishOnly).

- [ ] **Step 7: Commit (plugin repo)**

```bash
git -C ~/personal/gsxhq/vite-plugin-gsx add -A
git -C ~/personal/gsxhq/vite-plugin-gsx commit -m "feat: supervise gsx generate --watch daemon (NDJSON stream) instead of per-save generate"
```

---

## Task 7: End-to-end measurement + docs

**Files:**
- Modify (worktree): `gen/templates/init/simple/Taskfile.yml` (if it invokes `gsx generate` per save — switch the dev loop to the daemon, or confirm the plugin now owns it)
- Modify (worktree): `docs/ROADMAP.md` (mark `--watch` shipped; record the measured number)
- Modify (worktree): `docs/guide/cli.md` (document `gsx generate --watch [--format=ndjson]`)
- Modify (plugin repo): `README.md` (note the daemon model + min gsx version)

**Interfaces:** none (docs + scaffold wiring).

- [ ] **Step 1: Measure the loop**

Scaffold a fresh project and time saves. Run (manually):
```bash
cd /tmp && rm -rf wm && go run <worktree>/cmd/gsx init wm --module wm --yes
# wire local replaces to the worktree gsx + local vite + the feat/watch-daemon plugin (npm link or file:)
cd /tmp/wm && task dev
# edit app.gsx repeatedly; read durationMs from the daemon's ndjson (tmp/dev.log).
```
Record the per-save `durationMs` (warm) and compare to a cold `gsx generate` of the same package. **Deliverable:** the measured numbers, written into the ROADMAP note.

- [ ] **Step 2: Decide slice 2** — if warm `durationMs` is acceptable (e.g. < ~50 ms typical), slice 2 (fine-grained invalidation) stays deferred; if `go/packages.Load` on rebuild still dominates real edits, file slice 2. Record the decision in the ROADMAP.

- [ ] **Step 3: Update docs**

In `docs/ROADMAP.md`, under "Developer experience — Vite + init", add a `[x]` bullet for `gsx generate --watch` (warm daemon + plugin supervision) with the measured number; in the CLI row note `--watch`/`--format` shipped. In `docs/guide/cli.md`, document the flags and the NDJSON contract. In the plugin `README.md`, document the daemon model and the minimum gsx version that provides `--watch`.

- [ ] **Step 4: Commit (worktree)**

```bash
git add docs/ROADMAP.md docs/guide/cli.md gen/templates/init/simple/Taskfile.yml
git commit -m "docs: gsx generate --watch shipped + measured; CLI/ROADMAP/scaffold updates"
```

- [ ] **Step 5: Commit (plugin repo)**

```bash
git -C ~/personal/gsxhq/vite-plugin-gsx add README.md
git -C ~/personal/gsxhq/vite-plugin-gsx commit -m "docs: document the gsx --watch daemon model"
```

---

## Notes for the executor

- **Riskiest assumption is validated in Task 2** (warm regen across a multi-package module). If its test cannot pass even with the explicit-import-path fallback, stop and escalate — the warm-resolver approach needs rework before Tasks 3–5.
- **Two repos:** Tasks 1–5 and 7 (docs/scaffold) are in this worktree; Task 6 and the plugin half of Task 7 are in `~/personal/gsxhq/vite-plugin-gsx`. Do not publish the npm package — the user runs `npm publish`.
- **stdout discipline:** any stray `fmt.Print` to stdout in `--format=ndjson` corrupts the stream. All human/log output goes to stderr.
- **Don't reinvent:** if you find yourself writing diagnostic-JSON encoding, file-writing, dir discovery, or a second codegen path, stop — reuse `diag.RenderJSON`, `restore`, `discoverDirs`, `generateCached`/`CachedResolver`.
