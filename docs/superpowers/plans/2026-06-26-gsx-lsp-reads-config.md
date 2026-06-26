# gsx LSP reads `gsx.toml` in-process — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the gsx language server resolve a project's declarative `gsx.toml` filters (and in-process opts) so `gd`/hover/diagnostics on `{ x |> url(…) }` etc. work in the editor.

**Architecture:** The LSP resolves config the same way `generate`/`info` do — `mergeConfig(gsx.toml, opts)` — but **in-process and best-effort**: a new `resolveConfigBestEffort` discovers and loads `gsx.toml` from the analyzed dir, merges it under the binary's compiled-in opts, and feeds `filterPkgs/aliases/classifier()/fieldMatcher` to the existing codegen pipeline. The LSP **spawns no subprocess** (no oracle, no `syscall.Exec` delegation), so it can never orphan a child. A malformed `gsx.toml` logs one line and falls back to the std baseline.

**Tech Stack:** Go 1.26.1; `gen` package (`gen/lsp.go`, `gen/main.go`, reusing `gen/configfile.go`'s `discoverConfig`/`loadConfig`/`mergeConfig`); `internal/codegen.GeneratePackagesWithFilters`; `internal/lsp.Package`; `github.com/BurntSushi/toml` (already a dep, via `loadConfig`).

## Global Constraints

- The gsx runtime is **standard-library only**; the generator/CLI (`gen`, `internal/codegen`) may use `golang.org/x/tools`. This change touches only `gen` — no new runtime deps.
- `internal/lsp` must **NOT** import `internal/codegen`. This change touches `gen/lsp.go` and `gen/main.go` only; `internal/lsp` is untouched.
- Best-effort config reads must **NEVER** break or regress the LSP: a malformed/typo'd `gsx.toml` must fall back to the opts/std baseline, never return an error from `Analyze`.
- `config{}.classifier()` is behaviorally identical to `attrclass.Builtin()` (built-ins are the floor; an empty `config` has no user rules and a nil predicate) — so the no-config path stays byte-for-byte today's behavior.
- Module-resolution tests (those that call `lspAnalyzer.Analyze`, which runs `go/packages.Load`) must be guarded with `if testing.Short() { t.Skip("skipping module-resolution test in -short mode") }`.
- Commit messages must end with: `Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU`
- Prefer unexported identifiers (lowercase) — `resolveConfigBestEffort`, the `lspAnalyzer` fields, and all test helpers stay unexported.

---

## File Structure

- **`gen/lsp.go`** (modify) — owns the LSP analysis bridge. Gains: two `lspAnalyzer` fields (`optCfg config`, `warnw io.Writer`); a new `resolveConfigBestEffort` helper; `Analyze` resolves config and feeds it to `GeneratePackagesWithFilters`; `runLSP` constructs the analyzer with the warn-sink (Task 1) and the threaded `cfg` (Task 2).
- **`gen/main.go`** (modify, Task 2 only) — one line: `case "lsp"` passes the resolved `cfg` to `runLSP`.
- **`gen/lsp_config_test.go`** (create, Task 1) — all new tests: gsx.toml alias resolution, ctx-injected alias, malformed→fallback, no-config→std, in-process opts honored, `resolveConfigBestEffort` unit. Plus the shared temp-module helper.
- **18 existing `gen/*_e2e_test.go` + `gen/lsp_test.go`** (modify, Task 2 only) — mechanical: update `runLSP(…, nil)` call sites to `runLSP(…, config{}, nil)` for the new signature.

---

## Task 1: `Analyze` resolves `gsx.toml` + opts in-process (best-effort)

Delivers the headline: the stock LSP reads `gsx.toml`. Adds the `lspAnalyzer` fields, the `resolveConfigBestEffort` helper, wires `Analyze`, and sets `warnw` in `runLSP` — **without** changing `runLSP`'s signature (no call-site churn; that is Task 2).

**Files:**
- Modify: `gen/lsp.go` (lines 24–31, 75–82 — the `lspAnalyzer` type, `Analyze` head, `runLSP` body)
- Test: `gen/lsp_config_test.go` (create)

**Interfaces:**
- Consumes (already exist): `discoverConfig(startDir string) (string, bool)`, `loadConfig(path string) (config, error)`, `mergeConfig(base, opts config) config` (all in `gen/configfile.go`); `config` struct with fields `filterPkgs []string`, `aliases []codegen.FilterAlias`, `fieldMatcher codegen.FieldMatcher`, and method `classifier() *attrclass.Classifier` (`gen/main.go`); `codegen.GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, aliases []codegen.FilterAlias, cls *attrclass.Classifier, fm codegen.FieldMatcher, cssMin, jsMin func(string)(string,error), srcOverride map[string][]byte) (map[string]*PackageResult, error)`; `codegen.FilterAlias{Name, PkgPath, FuncName string}`; `*lsp.Package` with field `Diags []diag.Diagnostic` (each `diag.Diagnostic` has a `Message string`).
- Produces (for Task 2): `lspAnalyzer{optCfg config, warnw io.Writer}` struct; `resolveConfigBestEffort(dir string, optCfg config, warnw io.Writer) config`.

- [ ] **Step 1: Write the failing tests**

Create `gen/lsp_config_test.go`:

```go
package gen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspFilterModule writes a temp module (replace-directive'd at the repo root)
// with a local filter sub-package myf exposing Shout (ctx-less) and URL
// (ctx + variadic + (string,error)). Each test writes its own card.gsx and,
// optionally, a gsx.toml. Returns the module dir and a writer for extra files.
func lspFilterModule(t *testing.T) (dir string, must func(p, c string)) {
	t.Helper()
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must = func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("myf/myf.go", "package myf\n\nimport \"context\"\n\nfunc Shout(s string) string { return s + \"!\" }\n\nfunc URL(ctx context.Context, page any, args ...any) (string, error) { return \"/\", nil }\n")
	return dir, must
}

// hasUnknownFilter reports whether pkg carries a codegen "unknown filter %q"
// diagnostic for name — the signal that a pipeline filter did NOT resolve.
func hasUnknownFilter(pkg *lsp.Package, name string) bool {
	for _, d := range pkg.Diags {
		if strings.Contains(d.Message, `unknown filter "`+name+`"`) {
			return true
		}
	}
	return false
}

// A gsx.toml alias resolves a project filter in the LSP.
func TestLSPAnalyzeResolvesTomlAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("gsx.toml", "[aliases]\nshout = \"example.com/x/myf.Shout\"\n")
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> shout }</p>\n}\n")
	pkg, err := lspAnalyzer{}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "shout") {
		t.Fatalf("gsx.toml alias shout not resolved; diags: %v", pkg.Diags)
	}
}

// A ctx-injected, variadic, (R,error) alias resolves — proving the analyzer
// re-derives the seed-first contract from the alias's live signature.
func TestLSPAnalyzeResolvesCtxAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("gsx.toml", "[aliases]\nurl = \"example.com/x/myf.URL\"\n")
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<a href={ name |> url(\"id\", name) }>x</a>\n}\n")
	pkg, err := lspAnalyzer{}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "url") {
		t.Fatalf("ctx alias url not resolved; diags: %v", pkg.Diags)
	}
}

// A malformed gsx.toml is ignored: Analyze never errors, the std baseline still
// resolves (upper), the alias does NOT (shout), and a warning is logged.
func TestLSPAnalyzeMalformedConfigFallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	// Leading unknown TOP-LEVEL key → loadConfig's strict Undecoded check errors.
	must("gsx.toml", "bogusKey = 123\n[aliases]\nshout = \"example.com/x/myf.Shout\"\n")
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> upper }{ name |> shout }</p>\n}\n")
	var warn bytes.Buffer
	pkg, err := lspAnalyzer{warnw: &warn}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze must not error on a malformed gsx.toml: %v", err)
	}
	if hasUnknownFilter(pkg, "upper") {
		t.Fatalf("std upper must still resolve under fallback; diags: %v", pkg.Diags)
	}
	if !hasUnknownFilter(pkg, "shout") {
		t.Fatalf("alias shout must NOT resolve when gsx.toml is ignored; diags: %v", pkg.Diags)
	}
	if !strings.Contains(warn.String(), "gsx.toml") {
		t.Fatalf("expected a warning naming gsx.toml, got: %q", warn.String())
	}
}

// No gsx.toml → std baseline: upper resolves, an undeclared filter is unknown.
func TestLSPAnalyzeNoConfigStdBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> upper }{ name |> shout }</p>\n}\n")
	pkg, err := lspAnalyzer{}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "upper") {
		t.Fatalf("std upper should resolve; diags: %v", pkg.Diags)
	}
	if !hasUnknownFilter(pkg, "shout") {
		t.Fatalf("shout has no alias and must be unknown; diags: %v", pkg.Diags)
	}
}

// In-process opts (as a custom binary built with WithFilter would supply) feed
// the analyzer even with no gsx.toml present.
func TestLSPAnalyzeHonorsInProcessOpts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> shout }</p>\n}\n")
	opt := config{aliases: []codegen.FilterAlias{{Name: "shout", PkgPath: "example.com/x/myf", FuncName: "Shout"}}}
	pkg, err := lspAnalyzer{optCfg: opt}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "shout") {
		t.Fatalf("in-process opt alias not honored; diags: %v", pkg.Diags)
	}
}

// resolveConfigBestEffort: no file → optCfg unchanged; valid file → merged;
// malformed file → optCfg + a warning, no panic.
func TestResolveConfigBestEffort(t *testing.T) {
	dir, must := lspFilterModule(t)
	opt := config{aliases: []codegen.FilterAlias{{Name: "x", PkgPath: "p", FuncName: "F"}}}

	got := resolveConfigBestEffort(dir, opt, io.Discard)
	if len(got.aliases) != 1 || got.aliases[0].Name != "x" {
		t.Fatalf("no-config: want optCfg unchanged, got %+v", got.aliases)
	}

	must("gsx.toml", "[aliases]\nshout = \"example.com/x/myf.Shout\"\n")
	got = resolveConfigBestEffort(dir, opt, io.Discard)
	names := map[string]bool{}
	for _, a := range got.aliases {
		names[a.Name] = true
	}
	if !names["shout"] || !names["x"] {
		t.Fatalf("merged: want both shout (file) and x (opt), got %+v", got.aliases)
	}

	must("gsx.toml", "bogusKey = 1\n")
	var warn bytes.Buffer
	got = resolveConfigBestEffort(dir, opt, &warn)
	if len(got.aliases) != 1 || got.aliases[0].Name != "x" {
		t.Fatalf("malformed: want optCfg fallback, got %+v", got.aliases)
	}
	if !strings.Contains(warn.String(), "gsx.toml") {
		t.Fatalf("malformed: want warning, got %q", warn.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen/ -run 'TestLSPAnalyze|TestResolveConfigBestEffort' -count=1`
Expected: COMPILE FAIL — `lspAnalyzer{warnw: …}` / `lspAnalyzer{optCfg: …}` reference fields that don't exist yet, and `resolveConfigBestEffort` is undefined.

- [ ] **Step 3: Add the `lspAnalyzer` fields and `resolveConfigBestEffort`**

In `gen/lsp.go`, replace the `lspAnalyzer` type declaration (currently `type lspAnalyzer struct{}` around line 24) with:

```go
type lspAnalyzer struct {
	optCfg config    // programmatic opts (empty for the stock binary); merged UNDER gsx.toml
	warnw  io.Writer // best-effort sink for a malformed gsx.toml; nil → discard, never fatal
}
```

Add this helper (place it directly above `runLSP`):

```go
// resolveConfigBestEffort resolves the LSP's effective config: it discovers a
// gsx.toml from dir (walking up, bounded by .git/module root) and merges it under
// optCfg — exactly as resolveConfig does for generate/info — but for the LSP it
// must NEVER break analysis. A malformed/typo'd gsx.toml is logged to warnw (when
// non-nil) and the optCfg baseline is used; with no gsx.toml, optCfg is returned
// unchanged. It loads no packages (TOML + file walk only), so it is cheap enough
// to run per Analyze, which also picks up gsx.toml edits live.
func resolveConfigBestEffort(dir string, optCfg config, warnw io.Writer) config {
	path, ok := discoverConfig(dir)
	if !ok {
		return optCfg
	}
	fileCfg, err := loadConfig(path)
	if err != nil {
		if warnw != nil {
			fmt.Fprintf(warnw, "gsx: lsp: ignoring %s: %v\n", path, err)
		}
		return optCfg
	}
	return mergeConfig(fileCfg, optCfg)
}
```

- [ ] **Step 4: Wire `Analyze` to use the resolved config**

In `gen/lsp.go`, change the `Analyze` method head. Replace:

```go
func (lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir}, nil, nil, attrclass.Builtin(), nil, nil, nil, override)
```

with:

```go
func (a lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir},
		merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher,
		nil, nil, override)
```

(The `attrclass` import is now only used via `merged.classifier()` indirectly — `classifier()` lives in `gen/main.go` and returns `*attrclass.Classifier`; `gen/lsp.go` no longer references `attrclass` directly. Remove the `attrclass` import from `gen/lsp.go` if `go build` flags it as unused.)

- [ ] **Step 5: Set `warnw` in `runLSP` (no signature change)**

In `gen/lsp.go`, in `runLSP`, change the analyzer construction. Replace:

```go
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{})
```

with:

```go
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{warnw: stderr})
```

- [ ] **Step 6: Run the new tests to verify they pass**

Run: `go test ./gen/ -run 'TestLSPAnalyze|TestResolveConfigBestEffort' -count=1`
Expected: PASS (6 tests).

- [ ] **Step 7: Run the full gen suite to confirm no regression**

Run: `go test ./gen/ -count=1`
Expected: PASS. (Existing LSP e2e tests use temp modules without a `gsx.toml`, so `discoverConfig` finds nothing → `optCfg` empty → std baseline → byte-identical behavior.)

- [ ] **Step 8: Commit**

```bash
git add gen/lsp.go gen/lsp_config_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): resolve gsx.toml filters in-process (best-effort)

lspAnalyzer.Analyze now discovers + loads the project's gsx.toml and merges
it under any in-process opts (resolveConfigBestEffort), feeding the resolved
filters/aliases/classifier/fieldMatcher to the codegen pipeline — so gd/hover
on { x |> url } resolve in the editor. A malformed gsx.toml is logged and
falls back to the std baseline; the LSP never errors and spawns nothing.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Task 2: Thread the resolved `cfg` through `runLSP`

Plumbs the binary's compiled-in opts into the LSP so a custom binary run as `gsx lsp` gets full fidelity. Mechanical: a signature change + `case "lsp"` + updating the existing `runLSP` call sites.

**Files:**
- Modify: `gen/lsp.go` (`runLSP` signature + analyzer construction)
- Modify: `gen/main.go:160` (`case "lsp"`)
- Modify (call sites): `gen/api_nav_e2e_test.go`, `gen/formatting_e2e_test.go`, `gen/definition_e2e_test.go`, `gen/hover_e2e_test.go`, `gen/go_definition_e2e_test.go`, `gen/lsp_test.go`, `gen/references_e2e_test.go`, `gen/pipe_nav_e2e_test.go`

**Interfaces:**
- Consumes: `lspAnalyzer{optCfg config, warnw io.Writer}` and `resolveConfigBestEffort` (Task 1); the `config` value `cfg` already in scope in `runConfig` (`gen/main.go`), whose `cfg.errs` are surfaced at the top of `runConfig` before dispatch.
- Produces: `runLSP(stdin io.Reader, stdout, stderr io.Writer, cfg config, _ []string) int`.

- [ ] **Step 1: Change the `runLSP` signature and pass `cfg` into the analyzer**

In `gen/lsp.go`, replace:

```go
func runLSP(stdin io.Reader, stdout, stderr io.Writer, _ []string) int {
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{warnw: stderr})
```

with:

```go
// runLSP runs the gsx language server over stdin/stdout (JSON-RPC), logging
// operational failures to stderr. cfg carries the binary's compiled-in opts
// (empty for the stock binary), merged UNDER the project's gsx.toml per Analyze.
// It returns a process exit code.
func runLSP(stdin io.Reader, stdout, stderr io.Writer, cfg config, _ []string) int {
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{optCfg: cfg, warnw: stderr})
```

- [ ] **Step 2: Run the build to surface every stale call site**

Run: `go vet ./gen/ 2>&1 | head -30`
Expected: FAIL — `not enough arguments in call to runLSP` at `gen/main.go:160` and the test call sites (`gen/api_nav_e2e_test.go`, `gen/formatting_e2e_test.go`, `gen/definition_e2e_test.go`, `gen/hover_e2e_test.go`, `gen/go_definition_e2e_test.go`, `gen/lsp_test.go`, `gen/references_e2e_test.go`, `gen/pipe_nav_e2e_test.go`).

- [ ] **Step 3: Update the `case "lsp"` dispatch in `gen/main.go`**

In `gen/main.go`, replace line 160:

```go
		return runLSP(os.Stdin, stdout, stderr, cmdArgs)
```

with:

```go
		// lsp resolves gsx.toml per-file (best-effort) and merges these compiled-in
		// opts under it; cfg.errs are already surfaced at the top of runConfig.
		return runLSP(os.Stdin, stdout, stderr, cfg, cmdArgs)
```

- [ ] **Step 4: Update all test call sites**

Every test call is `runLSP(strings.NewReader(in), &out, &errBuf, nil)`. Insert `config{}` before the trailing `nil` in each. Run this from the worktree root to do all of them at once:

```bash
grep -rl 'runLSP(strings.NewReader(in), &out, &errBuf, nil)' gen/ \
  | xargs sed -i '' 's/runLSP(strings.NewReader(in), &out, &errBuf, nil)/runLSP(strings.NewReader(in), \&out, \&errBuf, config{}, nil)/g'
```

Then verify no stale 4-arg call remains:

```bash
grep -rn 'runLSP(strings.NewReader(in), &out, &errBuf, nil)' gen/ || echo "all call sites updated"
```

Expected: `all call sites updated`.

- [ ] **Step 5: Build to confirm everything compiles**

Run: `go vet ./gen/`
Expected: no errors.

- [ ] **Step 6: Run the full gen suite**

Run: `go test ./gen/ -count=1`
Expected: PASS. The signature change is covered by every existing LSP e2e test (they now pass `config{}` and still resolve as before); Task 1's tests still pass.

- [ ] **Step 7: Commit**

```bash
git add gen/lsp.go gen/main.go gen/*_test.go
git commit -m "$(cat <<'EOF'
feat(lsp): thread compiled-in opts into the language server

runLSP takes the resolved config and builds lspAnalyzer{optCfg: cfg}, so a
custom binary run as `gsx lsp` resolves its WithFilter/WithFieldMatcher opts
in the editor (merged under the project gsx.toml). Stock binary passes an
empty config — unchanged behavior. Updates the case "lsp" dispatch and the
runLSP test call sites for the new signature.

Claude-Session: https://claude.ai/code/session_01BmdcjM8wGhjPdBqKimNuMU
EOF
)"
```

---

## Final verification (after both tasks)

- [ ] **Whole-module suite:** `go test ./... -count=1` → PASS.
- [ ] **Manual smoke (optional, real editor path):** in the `pipecheck` temp project (a real `gsx.toml` alias, e.g. `link = "example.com/pipecheck/myf.Link"`), reinstall the worktree's gsx (`go install ./cmd/gsx`) and confirm `gd` on `{ … |> link }` resolves to the filter func rather than returning null.

---

## Self-Review

**1. Spec coverage** (against `2026-06-25-gsx-lsp-reads-config-design.md`):
- §3 `case "lsp"` threads `cfg` → Task 2 Step 3. ✓
- §3 `lspAnalyzer{optCfg, warnw}` + `resolveConfigBestEffort` + `Analyze` wiring → Task 1 Steps 3–5. ✓
- §3 `cssMin/jsMin` passed `nil` → Task 1 Step 4 (the `nil, nil` args). ✓
- §4 aliases/filterPkgs reproduce resolution → exercised by `TestLSPAnalyzeResolvesTomlAlias` / `…CtxAlias`. ✓
- §5 malformed → fallback + log; no config → std → `TestLSPAnalyzeMalformedConfigFallsBack`, `…NoConfigStdBaseline`. ✓
- §5 custom-binary opts honored → `TestLSPAnalyzeHonorsInProcessOpts` + Task 2. ✓
- §8 every listed test → Task 1 Step 1 (all six) + the per-task suite runs + the manual smoke. ✓
- Non-goals (no subprocess, no `[gsx] command`, no caching): nothing in the plan adds them. ✓

**2. Placeholder scan:** No TBD/TODO; every code step shows complete code; every run step states the exact command and expected outcome.

**3. Type consistency:** `lspAnalyzer{optCfg config, warnw io.Writer}`, `resolveConfigBestEffort(dir string, optCfg config, warnw io.Writer) config`, and `runLSP(stdin io.Reader, stdout, stderr io.Writer, cfg config, _ []string) int` are used identically across both tasks and the tests. `hasUnknownFilter(*lsp.Package, string) bool` and `lspFilterModule(*testing.T) (string, func(string,string))` match all call sites in Task 1. The codegen call's 9 arguments match the `GeneratePackagesWithFilters` signature (moduleDir, dirs, filterPkgs, aliases, cls, fm, cssMin, jsMin, srcOverride).
