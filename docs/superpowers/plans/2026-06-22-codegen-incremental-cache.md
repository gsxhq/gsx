# Incremental Codegen Cache (Tier 0 + Tier 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make repeated/watch-mode `gsx generate` fast: generate the whole project in ONE `go/packages` load (Tier 0), then skip packages whose inputs are unchanged via a content-addressed system cache (Tier 2).

**Architecture:** Tier 0 adds `codegen.GeneratePackagesWithFilters` (one load over a *subset* of package dirs, with custom filter packages) and rewires prod `gen.generate()` to call it once. Tier 2 adds a cache layer in `gen/`: compute a per-package source-hash key (no compile), look it up in a content-addressed store under `os.UserCacheDir()/gsx`, restore hits to disk, generate only misses (one load), store outputs.

**Tech Stack:** Go 1.26; `golang.org/x/tools/go/packages`; `os/exec` + `encoding/json` for `go list`; `crypto/sha256`; `os.UserCacheDir`.

## Global Constraints

- Generated `.x.go` source must stay byte-identical to today's output — the cache never alters codegen output, only decides whether to (re)generate or restore.
- Source files stay pure: NO cache state (hashes, manifests) written into `.x.go` or the repo. Cache lives only in the system cache dir.
- Default cache dir: `os.UserCacheDir()/gsx`. `GSXCACHE=<dir>` overrides; `GSXCACHE=off` and the `--no-cache` flag bypass the cache entirely (always generate).
- Correctness posture: when anything is uncertain (graph build fails, cache entry unreadable, non-standard module layout), treat the package as a MISS and regenerate. The cache only ever skips work it is certain is unchanged.
- A package is the unit of caching; its `.x.go` files are generated together.
- `stdImportPath = "github.com/gsxhq/gsx/std"` (the default filter package).
- Existing `GeneratePackage` / `GeneratePackageWithFilters` behavior must NOT change (used by the `gen` CLI today and elsewhere).
- Run all `go` commands from the worktree root `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/codegen-perf`.

---

## File Structure

- `internal/codegen/batch.go` — MODIFY: add `GeneratePackagesWithFilters(moduleDir, dirs, filterPkgs)`; reimplement `GeneratePackages` as a std-default wrapper; change the load to explicit dir patterns (not `"./..."`).
- `internal/codegen/version.go` — CREATE: `const Version` codegen-version tag for the cache key.
- `gen/modroot.go` — CREATE: `moduleRoot(dir)` — find the module root (the dir containing `go.mod`) for a target dir, plus the module path.
- `gen/cachestore.go` — CREATE: content-addressed store (`cacheDir()`, `cacheEnabled()`, `(store).get/put`, output blob encode/decode).
- `gen/cachekey.go` — CREATE: import-graph build (`go list -json`) + `computeKey(pkgDir, …)`.
- `gen/cache.go` — CREATE: the runner — `generateCached(paths, filterPkgs, useCache)` (partition → restore → generate misses → store).
- `gen/gen.go` — MODIFY: `generate()` delegates to the runner; add `--no-cache` plumbing.
- `gen/main.go` — MODIFY: `--no-cache` flag; `gsx clean --cache` subcommand.
- Tests: `internal/codegen/batch_test.go` (extend), `gen/cachestore_test.go`, `gen/cachekey_test.go`, `gen/cache_test.go`, `gen/modroot_test.go`.

---

## Phase A — Tier 0 (single-load prod codegen)

### Task 1: `GeneratePackagesWithFilters` (custom filters + scoped load)

**Files:**
- Modify: `internal/codegen/batch.go`
- Modify: `internal/corpus/codegen.go` (the `codegenGeneratePackages` wrapper — no signature change needed; see Step 4)
- Test: `internal/codegen/batch_test.go`

**Interfaces:**
- Consumes: `loadFilterTableMulti(dir string, pkgPaths []string) (filterTable, error)`, `dedupFilterPkgs([]string) []string`, `stdImportPath`, existing `GeneratePackages` body.
- Produces:
  - `func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string) (map[string]*PackageResult, error)` — same semantics as `GeneratePackages` but (a) resolves pipelines against `filterPkgs` (empty ⇒ `[]string{stdImportPath}`), and (b) loads ONLY `dirs` (as explicit patterns), so a real module is not source-type-checked wholesale.
  - `func GeneratePackages(moduleDir string, dirs []string) (map[string]*PackageResult, error)` — now `= GeneratePackagesWithFilters(moduleDir, dirs, nil)`.

- [ ] **Step 1: Write the failing test (custom filter + scoped load)**

Add to `internal/codegen/batch_test.go`:

```go
func TestGeneratePackagesWithFilters_CustomFilter(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxbatchf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// a filter package
	os.MkdirAll(filepath.Join(tmp, "myf"), 0o755)
	writeFile(t, filepath.Join(tmp, "myf"), "f.go", "package myf\n\nfunc Shout(s string) string { return s + \"!\" }\n")
	// a gsx package using the custom filter
	os.MkdirAll(filepath.Join(tmp, "v"), 0o755)
	writeFile(t, filepath.Join(tmp, "v"), "v.gsx", "package v\n\ncomponent C(name string) { <p>{ name |> Shout }</p> }\n")

	dirV := filepath.Join(tmp, "v")
	res, err := GeneratePackagesWithFilters(tmp, []string{dirV}, []string{"gsxbatchf/myf"})
	if err != nil {
		t.Fatal(err)
	}
	r := res[mustAbs(t, dirV)]
	if r == nil || r.Err != nil {
		t.Fatalf("expected clean result, got %+v", r)
	}
	if len(r.Files) == 0 {
		t.Fatalf("expected generated files")
	}
}

func mustAbs(t *testing.T, p string) string { t.Helper(); a, _ := filepath.Abs(p); return a }
```

(If `writeFile` is not visible in this test file, reuse the existing helper in the `codegen` package test files; it is defined in `codegen_test.go`.)

- [ ] **Step 2: Run it, verify it fails**

Run: `go test ./internal/codegen/ -run TestGeneratePackagesWithFilters_CustomFilter -v`
Expected: FAIL — `undefined: GeneratePackagesWithFilters`.

- [ ] **Step 3: Implement**

In `internal/codegen/batch.go`:
1. Rename the existing `func GeneratePackages(moduleDir string, dirs []string) (...)` to `func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string) (...)`.
2. At the top of the renamed function, default + dedup filters:
```go
filterPkgs = dedupFilterPkgs(filterPkgs) // dedupFilterPkgs treats empty as [stdImportPath]
```
3. Change the filter-table load from `loadFilterTable(moduleDir)` to:
```go
table, err := loadFilterTableMulti(moduleDir, filterPkgs)
```
4. Change the package load from the whole-module pattern to explicit dir patterns. Replace:
```go
pkgs, err := packages.Load(cfg, "./...")
```
with (build patterns from the absolute target dirs — `go list`/`packages.Load` accept absolute directory paths as patterns):
```go
patterns := make([]string, 0, len(absDirs))
for _, d := range absDirs {
	patterns = append(patterns, d)
}
if len(patterns) == 0 {
	return result, nil
}
pkgs, err := packages.Load(cfg, patterns...)
```
5. Add the std-default wrapper preserving the old name/signature:
```go
// GeneratePackages is GeneratePackagesWithFilters with the built-in std filter
// package (kept for the test corpus and any std-only caller).
func GeneratePackages(moduleDir string, dirs []string) (map[string]*PackageResult, error) {
	return GeneratePackagesWithFilters(moduleDir, dirs, nil)
}
```

Confirm `dedupFilterPkgs` maps empty→`[stdImportPath]` (it does — same helper `GeneratePackageWithFilters` uses). If not, pass `[]string{stdImportPath}` when `filterPkgs` is empty.

- [ ] **Step 4: Verify corpus + new test pass**

Run: `go test ./internal/codegen/ -run 'TestGeneratePackages' -v` → PASS (existing equivalence/error-isolation/cross-package tests still pass; new custom-filter test passes).
Run: `go test ./internal/corpus/ -run TestCorpus -count=1` → PASS (the corpus wrapper `codegenGeneratePackages` calls `GeneratePackages`, unchanged; goldens unchanged — no `-update`).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/batch.go internal/codegen/batch_test.go
git commit -m "codegen: GeneratePackagesWithFilters (custom filters + scoped dir patterns)"
```

---

### Task 2: module-root helper + wire `gen.generate()` to one load

**Files:**
- Create: `gen/modroot.go`, `gen/modroot_test.go`
- Modify: `gen/gen.go`
- Test: `gen/gen_test.go` (existing — must still pass)

**Interfaces:**
- Produces: `func moduleRoot(dir string) (root string, modPath string, err error)` — walks up from `dir` to the nearest `go.mod`; returns its dir and declared module path.
- Consumes: `codegen.GeneratePackagesWithFilters` (Task 1).

- [ ] **Step 1: Write the failing test**

`gen/modroot_test.go`:
```go
package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModuleRoot(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644)
	sub := filepath.Join(tmp, "a", "b")
	os.MkdirAll(sub, 0o755)
	root, modPath, err := moduleRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if root != tmp {
		t.Errorf("root = %q, want %q", root, tmp)
	}
	if modPath != "example.com/app" {
		t.Errorf("modPath = %q, want example.com/app", modPath)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./gen/ -run TestModuleRoot -v` → FAIL (`undefined: moduleRoot`).

- [ ] **Step 3: Implement `gen/modroot.go`**

```go
package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// moduleRoot walks up from dir to the nearest go.mod, returning its directory
// and the declared module path.
func moduleRoot(dir string) (string, string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		gomod := filepath.Join(d, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			return d, modulePathFromGoMod(data), nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", fmt.Errorf("gen: no go.mod found above %s", dir)
		}
		d = parent
	}
}

func modulePathFromGoMod(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./gen/ -run TestModuleRoot -v` → PASS.

- [ ] **Step 5: Rewire `generate()` in `gen/gen.go`**

Replace the per-dir loop body of `generate` (the `for _, dir := range dirs { codegen.GeneratePackageWithFilters(...) }` block) with a single batched call:

```go
func generate(paths []string, filterPkgs []string) (Result, error) {
	var res Result
	dirs, err := discoverDirs(paths)
	if err != nil {
		return res, err
	}
	if len(dirs) == 0 {
		return res, nil
	}
	root, _, err := moduleRoot(dirs[0])
	if err != nil {
		return res, err
	}
	out, err := codegen.GeneratePackagesWithFilters(root, dirs, filterPkgs)
	if err != nil {
		return res, err
	}
	for _, dir := range dirs {
		pr := out[dir] // dirs are already absolute (discoverDirs); GeneratePackages keys by abs dir
		if pr == nil {
			continue
		}
		if pr.Err != nil {
			res.Errs = append(res.Errs, fmt.Errorf("%s: %w", dir, pr.Err))
			continue
		}
		for gsxPath, src := range pr.Files {
			base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
			target := filepath.Join(dir, base+".x.go")
			if werr := os.WriteFile(target, src, 0o644); werr != nil {
				res.Errs = append(res.Errs, fmt.Errorf("%s: %w", target, werr))
				continue
			}
			res.Written = append(res.Written, target)
		}
	}
	sort.Strings(res.Written)
	if len(res.Errs) > 0 {
		return res, errors.Join(res.Errs...)
	}
	return res, nil
}
```

(`discoverDirs` already returns absolute dirs, and `GeneratePackagesWithFilters` keys its result map by the normalized absolute dir, so `out[dir]` matches. If a lookup misses due to symlinks, normalize with `filepath.Abs` on both sides.)

- [ ] **Step 6: Verify the whole gen suite + corpus**

Run: `go test ./gen/ -count=1` → PASS (existing `gen_test.go`, `options_e2e_test.go`, etc. assert the same generated `.x.go`; output is unchanged).
Run: `go test ./... -count=1` → all green.

- [ ] **Step 7: Commit**

```bash
git add gen/modroot.go gen/modroot_test.go gen/gen.go
git commit -m "gen: generate the whole project in one GeneratePackages load (Tier 0)"
```

---

## Phase B — Tier 2 (incremental content-hash cache)

### Task 3: codegen version tag

**Files:**
- Create: `internal/codegen/version.go`
- Test: none (a constant; exercised via the cache-key test in Task 5).

**Interfaces:**
- Produces: `func Version() string` — a tag bumped whenever codegen lowering changes; folded into the cache key so a compiler change invalidates all cached output.

- [ ] **Step 1: Implement `internal/codegen/version.go`**

```go
package codegen

// Version is the codegen-output version tag. BUMP THIS whenever a change to
// lowering/emit alters generated .x.go for unchanged input. The gsx incremental
// cache folds it into every cache key, so bumping it invalidates all cached
// output project-wide.
const version = "1"

// Version returns the codegen-output version tag (see the version constant).
func Version() string { return version }
```

- [ ] **Step 2: Build**

Run: `go build ./internal/codegen/` → clean.

- [ ] **Step 3: Commit**

```bash
git add internal/codegen/version.go
git commit -m "codegen: Version() tag for the incremental cache key"
```

---

### Task 4: content-addressed cache store

**Files:**
- Create: `gen/cachestore.go`, `gen/cachestore_test.go`

**Interfaces:**
- Produces:
  - `func cacheDir() (string, bool)` — returns the cache directory and whether caching is enabled. Disabled (`false`) when `GSXCACHE=off`. Otherwise `GSXCACHE` (if set) else `os.UserCacheDir()/gsx`.
  - `type pkgOutput map[string][]byte` — relative-path → generated `.x.go` bytes for one package.
  - `func storeGet(dir, key string) (pkgOutput, bool)` — read+decode the entry for `key`; ok=false on miss/corrupt.
  - `func storePut(dir, key string, out pkgOutput) error` — atomically write the entry (temp file + rename), sharded `dir/<key[:2]>/<key>`.
  - Blob encode/decode helpers (length-prefixed; see code).

- [ ] **Step 1: Write the failing test**

`gen/cachestore_test.go`:
```go
package gen

import (
	"os"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := pkgOutput{"a.x.go": []byte("package a\n"), "b.x.go": []byte("package b\n")}
	if err := storePut(dir, "deadbeefkey", out); err != nil {
		t.Fatal(err)
	}
	got, ok := storeGet(dir, "deadbeefkey")
	if !ok {
		t.Fatal("expected hit")
	}
	if string(got["a.x.go"]) != "package a\n" || string(got["b.x.go"]) != "package b\n" {
		t.Errorf("round-trip mismatch: %v", got)
	}
	if _, ok := storeGet(dir, "missingkey"); ok {
		t.Error("expected miss for unknown key")
	}
}

func TestCacheDirOff(t *testing.T) {
	t.Setenv("GSXCACHE", "off")
	if _, enabled := cacheDir(); enabled {
		t.Error("GSXCACHE=off must disable the cache")
	}
	t.Setenv("GSXCACHE", t.TempDir())
	if d, enabled := cacheDir(); !enabled || d == "" {
		t.Error("GSXCACHE=<dir> must enable + point at dir")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./gen/ -run 'TestStoreRoundTrip|TestCacheDirOff' -v` → FAIL (undefined).

- [ ] **Step 3: Implement `gen/cachestore.go`**

```go
package gen

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
)

type pkgOutput map[string][]byte

// cacheDir returns the cache directory and whether caching is enabled.
func cacheDir() (string, bool) {
	switch v := os.Getenv("GSXCACHE"); {
	case v == "off":
		return "", false
	case v != "":
		return v, true
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", false // no usable cache dir → behave as disabled
	}
	return filepath.Join(base, "gsx"), true
}

func entryPath(dir, key string) string {
	shard := key
	if len(key) >= 2 {
		shard = key[:2]
	}
	return filepath.Join(dir, shard, key)
}

func storeGet(dir, key string) (pkgOutput, bool) {
	data, err := os.ReadFile(entryPath(dir, key))
	if err != nil {
		return nil, false
	}
	out, ok := decodeOutput(data)
	return out, ok
}

func storePut(dir, key string, out pkgOutput) error {
	p := entryPath(dir, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(encodeOutput(out)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p) // atomic
}

// encodeOutput: count, then for each (sorted by name): len(name) name len(data) data.
func encodeOutput(out pkgOutput) []byte {
	names := make([]string, 0, len(out))
	for n := range out {
		names = append(names, n)
	}
	sort.Strings(names)
	var b []byte
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(len(names)))
	b = append(b, tmp[:]...)
	for _, n := range names {
		binary.BigEndian.PutUint64(tmp[:], uint64(len(n)))
		b = append(b, tmp[:]...)
		b = append(b, n...)
		d := out[n]
		binary.BigEndian.PutUint64(tmp[:], uint64(len(d)))
		b = append(b, tmp[:]...)
		b = append(b, d...)
	}
	return b
}

func decodeOutput(b []byte) (pkgOutput, bool) {
	read := func(n int) ([]byte, bool) {
		if len(b) < n {
			return nil, false
		}
		v := b[:n]
		b = b[n:]
		return v, true
	}
	readU64 := func() (int, bool) {
		v, ok := read(8)
		if !ok {
			return 0, false
		}
		return int(binary.BigEndian.Uint64(v)), true
	}
	count, ok := readU64()
	if !ok {
		return nil, false
	}
	out := make(pkgOutput, count)
	for i := 0; i < count; i++ {
		nl, ok := readU64()
		if !ok {
			return nil, false
		}
		name, ok := read(nl)
		if !ok {
			return nil, false
		}
		dl, ok := readU64()
		if !ok {
			return nil, false
		}
		data, ok := read(dl)
		if !ok {
			return nil, false
		}
		out[string(name)] = append([]byte(nil), data...)
	}
	return out, true
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./gen/ -run 'TestStoreRoundTrip|TestCacheDirOff' -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add gen/cachestore.go gen/cachestore_test.go
git commit -m "gen: content-addressed cache store (system dir, GSXCACHE override/off)"
```

---

### Task 5: cache key (import graph + per-package source hash)

**Files:**
- Create: `gen/cachekey.go`, `gen/cachekey_test.go`

**Interfaces:**
- Consumes: `moduleRoot` (Task 2), `codegen.Version()` (Task 3), `stdImportPath` is internal to codegen — pass the resolved filter list in instead.
- Produces:
  - `type pkgInfo struct { ImportPath, Dir string; Deps []string }`
  - `func loadGraph(moduleRoot string) (map[string]pkgInfo, error)` — `go list -json ./...` parsed into importPath→info (Dir + transitive Deps).
  - `func computeKey(dir string, graph map[string]pkgInfo, modPath, goSumHash, goModHash, goVersion string, filterPkgs []string) (string, error)` — the per-package key (§3 of the spec).
  - `func dirSourceHash(dir string) (string, error)` — sha256 over the dir's `*.gsx` + `*.go` (NOT `*.x.go`) contents, name-sorted.

- [ ] **Step 1: Write the failing test (stability + sensitivity + closure)**

`gen/cachekey_test.go`:
```go
package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSourceHashStableAndSensitive(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "a.gsx"), []byte("package v\ncomponent A(){<p>x</p>}\n"), 0o644)
	h1, err := dirSourceHash(d)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := dirSourceHash(d)
	if h1 != h2 {
		t.Fatal("hash not stable for identical inputs")
	}
	// generated .x.go must NOT affect the hash
	os.WriteFile(filepath.Join(d, "a.x.go"), []byte("package v // generated\n"), 0o644)
	h3, _ := dirSourceHash(d)
	if h3 != h1 {
		t.Errorf(".x.go must be excluded from source hash")
	}
	// editing source MUST change the hash
	os.WriteFile(filepath.Join(d, "a.gsx"), []byte("package v\ncomponent A(){<p>y</p>}\n"), 0o644)
	h4, _ := dirSourceHash(d)
	if h4 == h1 {
		t.Errorf("source edit must change the hash")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./gen/ -run TestDirSourceHashStableAndSensitive -v` → FAIL (undefined).

- [ ] **Step 3: Implement `gen/cachekey.go`**

```go
package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gsxhq/gsx/internal/codegen"
)

type pkgInfo struct {
	ImportPath string
	Dir        string
	Deps       []string
}

// loadGraph runs `go list -json ./...` (metadata only — NO -export, no compile)
// from moduleRoot and returns importPath -> info (Dir + transitive Deps).
func loadGraph(moduleRoot string) (map[string]pkgInfo, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gen: go list: %w", err)
	}
	graph := map[string]pkgInfo{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkgInfo
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("gen: decode go list: %w", err)
		}
		graph[p.ImportPath] = p
	}
	return graph, nil
}

// dirSourceHash hashes a package dir's .gsx + .go source (excluding generated
// .x.go), name-sorted, content-addressed.
func dirSourceHash(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".x.go") {
			continue // generated output is not an input
		}
		if strings.HasSuffix(n, ".gsx") || strings.HasSuffix(n, ".go") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// computeKey is the per-package cache key. dir is the absolute package dir;
// graph maps import paths to info; modPath is the module path; goModHash/
// goSumHash/goVersion/filterPkgs are the version pins.
func computeKey(dir string, graph map[string]pkgInfo, modPath, goModHash, goSumHash, goVersion string, filterPkgs []string) (string, error) {
	own, err := dirSourceHash(dir)
	if err != nil {
		return "", err
	}
	// find this package's import path by matching Dir.
	var self pkgInfo
	for _, p := range graph {
		if p.Dir == dir {
			self = p
			break
		}
	}
	// transitive in-module deps: self.Deps filtered to the module prefix.
	var depHashes []string
	for _, dep := range self.Deps {
		if dep == self.ImportPath {
			continue
		}
		if dep != modPath && !strings.HasPrefix(dep, modPath+"/") {
			continue // external / stdlib — pinned by goMod/goSum/goVersion
		}
		dp, ok := graph[dep]
		if !ok || dp.Dir == "" {
			continue
		}
		dh, err := dirSourceHash(dp.Dir)
		if err != nil {
			return "", err
		}
		depHashes = append(depHashes, dep+":"+dh)
	}
	sort.Strings(depHashes)

	pins := dedupSorted(filterPkgs)
	h := sha256.New()
	fmt.Fprintf(h, "gsxcache-v1\x00%s\x00%s\x00%s\x00%s\x00", codegen.Version(), goVersion, goModHash, goSumHash)
	fmt.Fprintf(h, "filters=%s\x00own=%s\x00", strings.Join(pins, ","), own)
	for _, d := range depHashes {
		fmt.Fprintf(h, "dep=%s\x00", d)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func dedupSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// fileHashOrEmpty hashes a file's bytes, returning "" if absent (go.sum may not exist).
func fileHashOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./gen/ -run TestDirSourceHashStableAndSensitive -v` → PASS.

- [ ] **Step 5: Add closure + key-sensitivity test, verify**

Append to `gen/cachekey_test.go` a test that builds a 2-dir temp module (`a` imports nothing; `b` imports `a`), runs `loadGraph` + `computeKey` for `b`, edits `a`'s source, recomputes `b`'s key, and asserts it changed; edits an unrelated dir and asserts `b`'s key did NOT change. (Use a real temp module with `go.mod` so `go list` works; mirror the temp-module setup from `batch_test.go`.)

```go
func TestComputeKeyDepClosure(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/app\n\ngo 1.26\n"), 0o644)
	mk := func(p, content string) { os.MkdirAll(filepath.Join(tmp, p), 0o755); os.WriteFile(filepath.Join(tmp, p, p+".go"), []byte(content), 0o644) }
	mk("a", "package a\n\nfunc A() string { return \"a\" }\n")
	mk("b", "package b\n\nimport \"ex/app/a\"\n\nfunc B() string { return a.A() }\n")
	mk("c", "package c\n\nfunc C() string { return \"c\" }\n")
	graph, err := loadGraph(tmp)
	if err != nil { t.Fatal(err) }
	bDir := filepath.Join(tmp, "b")
	key1, err := computeKey(bDir, graph, "ex/app", "", "", "go1.26", nil)
	if err != nil { t.Fatal(err) }
	// edit dependency a -> b's key must change
	os.WriteFile(filepath.Join(tmp, "a", "a.go"), []byte("package a\n\nfunc A() string { return \"A2\" }\n"), 0o644)
	key2, _ := computeKey(bDir, loadGraphMust(t, tmp), "ex/app", "", "", "go1.26", nil)
	if key1 == key2 { t.Error("editing dependency a must change b's key") }
	// edit unrelated c -> b's key must NOT change
	os.WriteFile(filepath.Join(tmp, "c", "c.go"), []byte("package c\n\nfunc C() string { return \"C2\" }\n"), 0o644)
	key3, _ := computeKey(bDir, loadGraphMust(t, tmp), "ex/app", "", "", "go1.26", nil)
	if key3 != key2 { t.Error("editing unrelated c must NOT change b's key") }
}

func loadGraphMust(t *testing.T, root string) map[string]pkgInfo { t.Helper(); g, err := loadGraph(root); if err != nil { t.Fatal(err) }; return g }
```

Run: `go test ./gen/ -run 'TestComputeKey|TestDirSourceHash' -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add gen/cachekey.go gen/cachekey_test.go
git commit -m "gen: per-package cache key (go list graph + source hash + version pins)"
```

---

### Task 6: cache runner + wire into `generate()`

**Files:**
- Create: `gen/cache.go`, `gen/cache_test.go`
- Modify: `gen/gen.go` (route `generate` through the runner), `gen/main.go` (`--no-cache` flag → bypass)

**Interfaces:**
- Consumes: `moduleRoot`, `loadGraph`, `computeKey`, `fileHashOrEmpty`, `cacheDir`, `storeGet`, `storePut`, `pkgOutput`, `codegen.GeneratePackagesWithFilters`.
- Produces: `func generateCached(paths, filterPkgs []string, useCache bool) (Result, error)` — the §6 run flow. `generate()` becomes `generateCached(paths, filterPkgs, true)`.

- [ ] **Step 1: Write the failing integration test**

`gen/cache_test.go` (temp module like `batch_test.go`; point `GSXCACHE` at a temp dir):
```go
func TestCacheColdWarmEdit(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex/c\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644)
	mkgsx := func(p, body string) { os.MkdirAll(filepath.Join(tmp, p), 0o755); os.WriteFile(filepath.Join(tmp, p, p+".gsx"), []byte(body), 0o644) }
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>{name}</p> }\n")
	mkgsx("w", "package w\n\ncomponent B() { <div>hi</div> }\n")
	t.Setenv("GSXCACHE", t.TempDir())

	// cold: both generate
	res, err := generateCached([]string{tmp}, nil, true)
	if err != nil { t.Fatal(err) }
	if len(res.Written) != 2 { t.Fatalf("cold: want 2 written, got %v", res.Written) }

	// warm no-op: nothing regenerated (Written empty — restores are skipped when on-disk matches)
	res, err = generateCached([]string{tmp}, nil, true)
	if err != nil { t.Fatal(err) }
	if len(res.Written) != 0 { t.Fatalf("warm no-op: want 0 written, got %v", res.Written) }

	// edit only v -> only v regenerates
	mkgsx("v", "package v\n\ncomponent A(name string) { <p>Hi {name}</p> }\n")
	res, err = generateCached([]string{tmp}, nil, true)
	if err != nil { t.Fatal(err) }
	if len(res.Written) != 1 || filepath.Base(filepath.Dir(res.Written[0])) != "v" {
		t.Fatalf("edit v: want only v written, got %v", res.Written)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./gen/ -run TestCacheColdWarmEdit -v` → FAIL (`undefined: generateCached`).

- [ ] **Step 3: Implement `gen/cache.go`**

```go
package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/gsxhq/gsx/internal/codegen"
)

func generateCached(paths, filterPkgs []string, useCache bool) (Result, error) {
	var res Result
	dirs, err := discoverDirs(paths)
	if err != nil {
		return res, err
	}
	if len(dirs) == 0 {
		return res, nil
	}
	root, modPath, err := moduleRoot(dirs[0])
	if err != nil {
		return res, err
	}

	cdir, enabled := cacheDir()
	if !useCache {
		enabled = false
	}

	// No cache: one batched generate (Tier 0 path).
	if !enabled {
		return writeAll(dirs, mustGen(root, dirs, filterPkgs, &res), &res)
	}

	graph, gerr := loadGraph(root)
	goVer := runtime.Version()
	goModH := fileHashOrEmpty(filepath.Join(root, "go.mod"))
	goSumH := fileHashOrEmpty(filepath.Join(root, "go.sum"))

	keys := map[string]string{} // dir -> key (only when computable)
	var miss []string
	for _, dir := range dirs {
		if gerr != nil {
			miss = append(miss, dir) // graph failed → regenerate everything (safe)
			continue
		}
		k, err := computeKey(dir, graph, modPath, goModH, goSumH, goVer, filterPkgs)
		if err != nil {
			miss = append(miss, dir) // uncertain → MISS
			continue
		}
		keys[dir] = k
		if _, ok := storeGet(cdir, k); ok {
			continue // HIT
		}
		miss = append(miss, dir)
	}

	// RESTORE phase: write every HIT's cached output to disk (hash-gated), BEFORE generating.
	for _, dir := range dirs {
		k, ok := keys[dir]
		if !ok {
			continue
		}
		if contains(miss, dir) {
			continue
		}
		out, ok := storeGet(cdir, k)
		if !ok {
			continue
		}
		written, werr := restore(dir, out)
		if werr != nil {
			return res, werr
		}
		res.Written = append(res.Written, written...)
	}

	// GENERATE phase: only the miss set, in ONE load.
	if len(miss) > 0 {
		out, err := codegen.GeneratePackagesWithFilters(root, miss, filterPkgs)
		if err != nil {
			return res, err
		}
		for _, dir := range miss {
			pr := out[dir]
			if pr == nil {
				continue
			}
			if pr.Err != nil {
				res.Errs = append(res.Errs, fmt.Errorf("%s: %w", dir, pr.Err))
				continue
			}
			po := toPkgOutput(dir, pr.Files)
			written, werr := restore(dir, po) // write generated bytes (hash-gated)
			if werr != nil {
				return res, werr
			}
			res.Written = append(res.Written, written...)
			if k, ok := keys[dir]; ok {
				_ = storePut(cdir, k, po) // best-effort cache write
			}
		}
	}

	sort.Strings(res.Written)
	if len(res.Errs) > 0 {
		return res, errors.Join(res.Errs...)
	}
	return res, nil
}

// restore writes a package's output to disk, skipping files whose bytes already
// match (hash-gated). Returns the paths it actually wrote.
func restore(dir string, out pkgOutput) ([]string, error) {
	var written []string
	for rel, data := range out {
		target := filepath.Join(dir, rel)
		if existing, err := os.ReadFile(target); err == nil && bytesEqual(existing, data) {
			continue // already current — no write
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return written, fmt.Errorf("%s: %w", target, err)
		}
		written = append(written, target)
	}
	return written, nil
}

// toPkgOutput converts codegen's gsxPath->bytes (absolute .gsx paths) into the
// store's relative-path (<base>.x.go) -> bytes form for this dir.
func toPkgOutput(dir string, files map[string][]byte) pkgOutput {
	po := pkgOutput{}
	for gsxPath, src := range files {
		base := filepath.Base(gsxPath)
		base = base[:len(base)-len(".gsx")]
		po[base+".x.go"] = src
	}
	return po
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mustGen / writeAll: the no-cache fallback (Tier 0 path) reused by generateCached.
func mustGen(root string, dirs, filterPkgs []string, res *Result) map[string]*codegen.PackageResult {
	out, err := codegen.GeneratePackagesWithFilters(root, dirs, filterPkgs)
	if err != nil {
		res.Errs = append(res.Errs, err)
		return nil
	}
	return out
}

func writeAll(dirs []string, out map[string]*codegen.PackageResult, res *Result) (Result, error) {
	if out != nil {
		for _, dir := range dirs {
			pr := out[dir]
			if pr == nil {
				continue
			}
			if pr.Err != nil {
				res.Errs = append(res.Errs, fmt.Errorf("%s: %w", dir, pr.Err))
				continue
			}
			written, werr := restore(dir, toPkgOutput(dir, pr.Files))
			if werr != nil {
				res.Errs = append(res.Errs, werr)
				continue
			}
			res.Written = append(res.Written, written...)
		}
	}
	sort.Strings(res.Written)
	if len(res.Errs) > 0 {
		return *res, errors.Join(res.Errs...)
	}
	return *res, nil
}
```

- [ ] **Step 3b: Test hermeticity — never touch the real user cache**

Because the cache is now ON by default, add a `TestMain` to the `gen` package so existing/other tests don't read or write `~/.cache/gsx`. Create `gen/main_testmain_test.go`:

```go
package gen

import (
	"os"
	"testing"
)

// TestMain isolates every gen test from the real user cache. Tests that
// specifically exercise caching override GSXCACHE with t.Setenv to a temp dir.
func TestMain(m *testing.M) {
	if os.Getenv("GSXCACHE") == "" {
		os.Setenv("GSXCACHE", "off") // default: cache disabled in tests
	}
	os.Exit(m.Run())
}
```

The caching tests (`TestCacheColdWarmEdit`, the `--no-cache` test) use `t.Setenv("GSXCACHE", t.TempDir())` to opt INTO a real (but temp, isolated) cache. Existing `gen` tests run on the cache-off path → output identical to before.

- [ ] **Step 4: Route `generate()` through the runner**

In `gen/gen.go`, replace the Task-2 body of `generate` with:
```go
func generate(paths []string, filterPkgs []string) (Result, error) {
	return generateCached(paths, filterPkgs, true)
}
```
(Keep `Generate(paths)` calling `generate(paths, nil)`.) Remove now-unused imports if any. The Task-2 batched-write logic now lives in `generateCached`'s no-cache path / `writeAll`.

- [ ] **Step 5: Run the integration test + full suite**

Run: `go test ./gen/ -run TestCacheColdWarmEdit -v` → PASS.
Run: `go test ./... -count=1` → all green.

- [ ] **Step 6: Add `--no-cache` flag (bypass)**

In `gen/main.go`: parse a `--no-cache` boolean for the `generate` command; thread it to a `generateCached(paths, filterPkgs, !noCache)` call. (If the existing flag plumbing is in `runGenerate`, add the bool there and call `generateCached` directly, or set `GSXCACHE=off` equivalently. Keep `Generate`/`generate` defaulting to cache-on.) Add a focused test asserting `--no-cache` regenerates even on a warm cache.

- [ ] **Step 7: Commit**

```bash
git add gen/cache.go gen/cache_test.go gen/gen.go gen/main.go
git commit -m "gen: incremental content-hash cache runner (Tier 2) + --no-cache"
```

---

### Task 7: `gsx clean --cache`

**Files:**
- Modify: `gen/main.go`
- Test: `gen/main_test.go` (existing)

- [ ] **Step 1: Write the failing test**

Add to `gen/main_test.go` a test that sets `GSXCACHE` to a temp dir, writes a dummy entry, runs the `clean --cache` dispatch, and asserts the dir is emptied. (Mirror the existing `main_test.go` invocation style — call `run([]string{"clean", "--cache"}, ...)`.)

- [ ] **Step 2–4: Implement + verify**

In `gen/main.go`'s command dispatch, add a `clean` command: when `--cache` is passed, resolve `cacheDir()` and `os.RemoveAll` it (only when enabled), print a one-line summary. Run the test → PASS. Run `go test ./gen/ -count=1` → green.

- [ ] **Step 5: Commit**

```bash
git add gen/main.go gen/main_test.go
git commit -m "gen: gsx clean --cache"
```

---

### Task 8: Final verification

- [ ] **Step 1:** `go test ./... -count=1` → all green.
- [ ] **Step 2:** `go vet ./... ` and `gopls check -severity=hint gen/*.go internal/codegen/version.go` → no findings.
- [ ] **Step 3:** Manual smoke (temp module): run `gsx generate` twice; confirm the second run writes nothing (warm no-op); edit one `.gsx`; confirm only that package regenerates. Record the observed behavior in the commit message.
- [ ] **Step 4:** Confirm `GSXCACHE=off go test ./gen/ -run TestCache` still passes (cache-disabled path equivalent output).
- [ ] **Step 5: Commit** any final tweaks: `git commit -am "gen: incremental cache — final verification"`.

---

## Notes / Deferred (from the spec §9)
- `.gsx`-import graph augmentation (for cold-run key stability when `.x.go` are absent) is deferred; `go list` graph is correct for the steady-state watch loop (a dependency edit changes both the dep's and dependent's keys once `.x.go` exist). Add augmentation only if cold→warm churn is observed.
- Automatic cache GC/size cap deferred; v1 ships manual `gsx clean --cache`.
- Multi-module / `replace`-to-local-dir layouts: `loadGraph` via `go list` handles `replace`; nested modules fall back to MISS (regenerate) — acceptable for v1.
