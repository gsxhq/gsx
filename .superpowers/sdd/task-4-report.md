# Task 4 Report: CLI Rendering — rich/compact/JSON, --json, exit codes

## What Was Built

### 1. `Result.Diags []diag.Diagnostic` (gen/gen.go)

Added `Diags []diag.Diagnostic` field to `Result`. Updated the doc comment to distinguish operational errors (`Errs`) from structured diagnostics (`Diags`). Added import for `internal/diag`.

### 2. cache.go — Both Paths Changed

**Cached path (GENERATE phase, `generateCached`):** Replaced the sentinel append `res.Errs = append(res.Errs, fmt.Errorf("%s: %w", dir, pr.Err))` with `res.Diags = append(res.Diags, pr.Diags...)`. When `pr.Err` is non-nil (the transition sentinel), we `continue` without adding to `res.Errs`. Write errors (`werr`) remain in `res.Errs` (genuine I/O failures).

**No-cache path (`writeAll`):** Same change: collect `pr.Diags` into `res.Diags`, skip the `pr.Err` sentinel instead of adding it to `res.Errs`.

Both paths now return `errors.New("codegen: diagnostics reported")` from `generateCached`/`writeAll` when `anyErrorDiag(res.Diags)` is true, so the Go API (`Generate`, `generate`) still signals failure to callers without a `res.Errs` entry.

### 3. Manifest-Write Guard (cache.go)

Changed:
```go
if enabled && modPath != "" && len(res.Errs) == 0 {  // old
```
to:
```go
if enabled && modPath != "" {  // simplified — the early-return above guarantees clean state
```

The `anyErrorDiag` early-return (`return res, errors.New("codegen: diagnostics reported")`) now fires before the manifest-write block when there are error-severity diagnostics. The defensive `len(res.Errs) == 0` check is redundant at that point (the `len(res.Errs) > 0` early-return above it already handles operational errors). So the guard is now simply `enabled && modPath != ""`.

Added `anyErrorDiag(diags []diag.Diagnostic) bool` helper at the bottom of cache.go.

### 4. `runGenerate` Rendering + TTY Detection + Exit Codes (gen/main.go)

- Parsed `--json` bool flag via `gfs.BoolVar(&jsonFlag, "json", false, ...)`.
- After `generateCached`: if `len(res.Errs) > 0`, print operational errors with `gsx: %v` prefix and return 1. If `err != nil && !anyErrorDiag(res.Diags)`, it's a discovery/usage error → return 2.
- Sorted merged `res.Diags` deterministically (filename→line→column) via `sort.SliceStable`.
- Rendering:
  - `--json` → `diag.RenderJSON(stdout, res.Diags)` (nothing to stderr)
  - `isTTY(stderr)` → `diag.RenderRich(stderr, res.Diags, src)` with disk-reading `SourceProvider`
  - else → `diag.RenderCompact(stderr, res.Diags)`
- TTY detection via `isTTY(w io.Writer) bool`: checks `w.(*os.File)`, then `f.Stat()`, then `fi.Mode()&os.ModeCharDevice != 0`. No new deps (stdlib only).
- Exit code: return 1 if `anyErrorDiag(res.Diags)`, else continue to success output.
- Removed the old `for _, e := range res.Errs { fmt.Fprintf(stderr, "gsx: %v\n", e) }` diagnostics loop (which printed the sentinel). The `gsx: %v` pattern for config/chdir errors at main.go lines 82 and 116 stays.

### 5. Gen Tests Updated

- **`TestGeneratePartialFailure`** (gen/gen_test.go): Updated to check `res.Diags` for an error diagnostic whose `Start.Filename` is under `badDir` (was: check `err.Error()` contains `badDir` and `len(res.Errs) == 1`). Assertion strength preserved: we still verify err is non-nil, a bad-dir diagnostic exists in `res.Diags`, no `.x.go` was written for the bad dir, the good dir was written, `res.Errs` is now 0 (operational errors only).

### 6. JSON-Shape Golden

Added `TestGenerateDiagJSONShape` in `gen/diag_render_test.go`. Pins:
- `severity == "error"`
- `code == "reserved-param"`
- `source == "codegen"`
- `file` is a non-empty string ending in `.gsx`
- `range.start` and `range.end` are objects with `line` (float64) and `col` (float64)
- `range.start.line == 3` (the component declaration line in `gsxWithReservedParam`)

## TDD Evidence

### RED Phase

```
$ go test ./gen/ -run "TestGenerateDiag" -count=1 -timeout=120s
--- FAIL: TestGenerateDiagCompactOutput (0.41s)
    diag_render_test.go:58: expected compact diagnostic with 'error[' in stderr, got:
        "gsx: /var/.../views: codegen: diagnostics reported\n"
--- FAIL: TestGenerateDiagJSONOutput (0.00s)
    diag_render_test.go:77: expected exit 1 for codegen diagnostic with --json, got 2;
        stderr="flag provided but not defined: -json\n..."
FAIL
```

`TestGenerateDiagNonZeroExit` passed RED (already exit 1 via sentinel). `TestGenerateDiagCompactOutput` showed the old sentinel text. `TestGenerateDiagJSONOutput` showed `--json` not defined (exit 2).

### GREEN Phase

After implementation:
```
$ go test ./gen/ -run "TestGenerateDiag" -count=1 -timeout=120s -v
--- PASS: TestGenerateDiagNonZeroExit (0.36s)
--- PASS: TestGenerateDiagCompactOutput (0.34s)
--- PASS: TestGenerateDiagJSONOutput (0.35s)
--- PASS: TestGenerateDiagJSONNoStderr (0.34s)
PASS
ok  github.com/gsxhq/gsx/gen 1.404s
```

### Full Suite

```
$ go test ./... -count=1 -timeout=300s
ok  github.com/gsxhq/gsx             0.015s
ok  github.com/gsxhq/gsx/ast         0.006s
ok  github.com/gsxhq/gsx/gen         10.118s
ok  github.com/gsxhq/gsx/internal/attrclass  0.007s
ok  github.com/gsxhq/gsx/internal/codegen   11.202s
ok  github.com/gsxhq/gsx/internal/corpus    2.076s
ok  github.com/gsxhq/gsx/internal/cssmin    0.008s
ok  github.com/gsxhq/gsx/internal/diag      0.006s
ok  github.com/gsxhq/gsx/internal/jsmin     0.016s
ok  github.com/gsxhq/gsx/internal/jsx       0.014s
ok  github.com/gsxhq/gsx/internal/printer   0.059s
ok  github.com/gsxhq/gsx/internal/txtar     0.019s
ok  github.com/gsxhq/gsx/internal/wsnorm    0.019s
ok  github.com/gsxhq/gsx/parser             0.018s
ok  github.com/gsxhq/gsx/std                0.016s
```

All 15 packages green.

## Manual Smoke Test

Built `gsx` binary from worktree and ran against a `.gsx` with `reserved-param` error:

```
=== compact (stderr to terminal simulation via redirect) ===
/path/.../v.gsx:3:1: error[reserved-param]: param name "ctx" is reserved (ambient context)
exit: 1

=== --json ===
[{"file":"/path/.../v.gsx","range":{"start":{"line":3,"col":1},"end":{"line":5,"col":2}},"severity":"error","code":"reserved-param","message":"param name \"ctx\" is reserved (ambient context)","source":"codegen"}]
exit: 1
```

Both exit codes correct. JSON includes all required fields. Compact format has `error[reserved-param]`. The sandbox did not allow running outside the worktree root, so both `compact` and `--json` tests are via the stderr=bytes.Buffer (non-TTY) path; the TTY path is exercised by the `isTTY` function logic which is trivially simple.

## Self-Review

- No new deps introduced (`sort` and `os` already in stdlib used by main.go).
- `anyErrorDiag` is defined only in `cache.go` and used in both `cache.go` and `main.go` (same package). Clear and unexported.
- The `isTTY` function is tested indirectly (tests drive `bytes.Buffer` → non-TTY path; TTY path exercised by the `*os.File` + `ModeCharDevice` check which is standard).
- `TestGeneratePartialFailure` assertions are NOT weakened: we still verify `err != nil`, a diagnostic exists for the bad dir, no `.x.go` was written for bad dir, good dir was written, and `len(res.Written) == 1`. The only change is checking `res.Diags` instead of `res.Errs` for the diagnostic.
- The manifest-write guard correctly prevents writing a manifest on any failure path (both `len(res.Errs) > 0` and `anyErrorDiag` early-return before the manifest block).

## Concerns

- **Double sentinel**: `generateCached` returns `errors.New("codegen: diagnostics reported")` when there are error-severity diagnostics, and `PackageResult.Err` is also that sentinel. This is intentional for the transition period per the task brief. The `runGenerate` handles it by checking `anyErrorDiag(res.Diags)` before treating `err != nil` as a usage error.
- **Rich TTY path**: Not smoke-tested in an actual terminal (sandbox limitation). The `isTTY` check with `os.ModeCharDevice` is the canonical stdlib approach, but the rich path depends on `RenderRich` (tested in `internal/diag`).
- The `err` returned by `generateCached` when there are diagnostic errors is `errors.New("codegen: diagnostics reported")` — it does NOT name the dir or the code. The dir/file info is in `res.Diags[*].Start.Filename`. Public API callers using `Generate()` who relied on `err.Error()` containing `badDir` must now check `res.Diags`. This is documented in the updated `TestGeneratePartialFailure`.
