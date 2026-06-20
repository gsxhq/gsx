# Plan: gsx CLI — `gen.Main` + `generate` (runnable gsx), slice 1

**Date:** 2026-06-20
**Branch:** `feat/gsx-cli-generate` off `main`
**Design:** `specs/2026-06-18-gsx-cli-skeleton-design.md` (command set, exit codes,
flags), `specs/2026-06-19-gsx-pipeline-and-extensions-design.md` Part C
(`gen.Main(...Option)` composition root, stock `cmd/gsx`).
**Status:** ready for SDD

## Goal

Make gsx RUNNABLE: a `gsx` binary whose `generate` command discovers `.gsx` files,
generates `.x.go` next to them, and writes them to disk — so `//go:generate gsx
generate` works. Today `GeneratePackage(dir)` only returns bytes; nothing writes or
dispatches.

## Scope

**IN:**
- New PUBLIC package `github.com/gsxhq/gsx/gen`:
  - `func Generate(dirs []string) error` (or a small result type) — the engine:
    discover `.gsx` recursively under each dir, group by containing directory
    (= Go package dir), call `internal/codegen.GeneratePackage(dir)` per dir, write
    each returned `.x.go` to disk next to its `.gsx`. (`gen` may import the module's
    own `internal/codegen` — same module.)
  - `func Main(opts ...Option)` — the CLI composition root: parse `os.Args`,
    dispatch the command, set the process exit code. Commands THIS slice:
    `generate [paths…]` (default `.`), `version`, `help`. Unknown command / bad flag
    → exit 2; generation errors → exit 1; success → exit 0.
  - `type Option func(*config)` — the option SHAPE (so `cmd/gsx` looks right and
    `WithFilters` slots in next slice). No real options yet (stock = std, as codegen
    already hardcodes). Document that `WithFilters`/`WithClassMerger` are the next
    slice.
- New `cmd/gsx/main.go` (stock binary): `package main; func main() { gen.Main() }`.
- Global flags this slice: `-C <dir>` (chdir before running), `-q`/`-v` (quiet/
  verbose progress to stderr). `--json` deferred (needs `internal/diag`).
- Exit codes: 0 ok · 1 problems · 2 usage. (3 internal — wrap a panic-recovery if
  cheap, else defer.)

**OUT (deferred — clear "not implemented" stubs where a command is named):**
- `WithFilters`/`WithClassMerger` extension seam (slice 2) — codegen keeps the
  hardcoded `std`.
- `internal/diag` structured diagnostics + `--json` envelope (slice 3) — this slice
  prints plain `error` strings to stderr (already `.gsx`-located via the existing
  error messages / `//line`).
- `fmt` (needs an AST→source printer — separate big task), `vet`, `lsp`, `render`,
  `init`, `info`, `explain` — `Main` may register them as stubs that print
  "not implemented yet" + exit 2, or simply be unknown commands. Pick the cleaner.
- `--watch`, incremental (skip-unchanged), `./...`-style package patterns.

## Tasks

### Task 1: `gen.Generate` engine (discover → codegen → write)

New `gen/gen.go` + `gen/gen_test.go`.
- `discoverDirs(paths []string) ([]string, error)`: for each path (default `["."]`),
  walk recursively (skip `.git`, hidden dirs, `vendor`, `node_modules`,
  `testdata`?) collecting directories that DIRECTLY contain ≥1 `*.gsx` file. Return
  the unique sorted dir set. A path that is a single `.gsx` FILE → its dir. Error if
  a path doesn't exist.
- `Generate(paths []string) error`: discover dirs; for each, `out, err :=
  codegen.GeneratePackage(dir)`; on err, accumulate (`dir: err`) and continue (so
  one bad package doesn't hide others); for each `gsxPath → bytes` in `out`, write
  `<gsxbase>.x.go` next to the `.gsx` (`os.WriteFile`, 0o644). Return a combined
  error (or nil). The bytes are already gofmt'd by `generateFile`. Report which files
  were written (for the verbose path / the CLI summary) — consider returning a small
  result struct `{Written []string; Errs []error}` instead of a bare error so Main
  can print a summary; your call, keep it clean.
- Tests (gen_test.go): build a temp module (mirror e2e_test.go's go.mod + `replace
  github.com/gsxhq/gsx => <repoRoot>` pattern; `repoRoot` via `filepath.Abs("..")`
  since `gen` is one level under root); write a couple of `.gsx` files in a subpkg;
  call `Generate([pkgDir])`; assert the `.x.go` files exist on disk, then `go build`
  the temp module succeeds (the generated code compiles). A nested-dirs case (two
  package dirs, each with a `.gsx`) → both generated. A `.gsx` with a codegen error →
  Generate returns an error naming the dir, writes nothing for that dir, and (if
  multi-dir) still writes the good dir. A no-`.gsx` dir → no-op, no error.

Commit: `gen: Generate engine — discover .gsx, codegen, write .x.go`.

### Task 2: `gen.Main` CLI dispatch + `cmd/gsx` stock binary

- `gen/main.go` (or in gen.go): `Main(opts ...Option)` — parse args (stdlib `flag`
  or hand-rolled; keep it simple), apply `-C` (os.Chdir), `-q`/`-v`. Dispatch:
  - `generate [paths…]` → `Generate(paths or ["."])`; print a summary (verbose: each
    written file; default: `wrote N files` or nothing on `-q`); exit 0 on success, 1
    if any error (print errors to stderr).
  - `version` → build info via `runtime/debug.ReadBuildInfo()` (module version, or
    "(devel)"); exit 0.
  - `help` / `-h` / no args → usage text listing the commands; exit 0.
  - unknown command or bad flag → usage error to stderr, exit 2.
  - Deferred commands (fmt/vet/lsp/render/info/explain/init): either omit (→ unknown,
    exit 2) OR register a stub printing "gsx <cmd>: not implemented yet" + exit 2.
    Pick one; document it.
  - `Main` calls `os.Exit(code)` itself (it IS the process entry). Factor the
    testable logic into `func run(args []string, stdout, stderr io.Writer) int` and
    have `Main` call `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))` — so tests
    drive `run` without exiting.
- `cmd/gsx/main.go`: `package main\n\nimport "github.com/gsxhq/gsx/gen"\n\nfunc main()
  { gen.Main() }`. Confirm `go build ./cmd/gsx` produces a working binary.
- Tests (gen_test.go): drive `run([]string{"generate", pkgDir}, …)` → exit 0, files
  written; `run(["version"], …)` → exit 0, prints something; `run(["help"], …)` →
  exit 0, lists `generate`; `run(["bogus"], …)` → exit 2; `run(["generate",
  "/does/not/exist"], …)` → exit 1 or 2 (decide; a missing path is a usage error → 2,
  a codegen failure → 1). Optionally: `go build ./cmd/gsx` in CI is covered by `go
  build ./...`.

Commit: `gen: Main CLI dispatch (generate/version/help) + cmd/gsx stock binary`.

## After tasks
- Final whole-feature review; independent adversarial review (focus: discovery edge
  cases, partial-failure handling, exit codes, writing the right .x.go path, no
  clobbering non-generated files, the temp-module build proof).
- Merge `--no-ff`; update ROADMAP (CLI `generate` runnable; WithFilters/diag/fmt
  next). Add a note to README/docs if one exists.

## Risks
- **Discovery scope** — recursive walk must skip junk dirs and not descend into
  generated output loops. Keep the skip-list explicit.
- **Partial failure** — one package's codegen error must not abort the whole run nor
  leave a half-written `.x.go`. Write only on success per dir.
- **`gen` importing `internal/codegen`** — allowed (same module). Confirm the build.
- **`-C` + relative paths** — apply `-C` (chdir) BEFORE resolving the path args, or
  resolve to absolute first; pick one and be consistent.
