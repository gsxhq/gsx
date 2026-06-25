# gsx LSP — resolve project filters via the `gsx info` oracle

**Status:** approved design (brainstormed 2026-06-25), ready for an implementation plan.

## 1. Goal & non-goals

**Goal.** Make a project's real pipeline filters resolve in the stock language
server, so `gd`/hover/diagnostics on `{ x |> url(…) }`, `{ … |> id }`, etc. work
in the editor — completing pipeline-nav end-to-end. The stock `~/bin/gsx lsp`
(what the editor launches) learns the project's filters by treating
`gsx info --json` as a **filter-resolution oracle**: it asks the project's own
gsx "what filters do you resolve?" and feeds the answer to the analyzer. A new
`gsx.toml` section, `[gsx] command`, declares how to invoke the project's gsx
when it is a custom binary (compiled-in `WithFilters`); without it, the stock
binary self-execs `info --json` to resolve the declarative `gsx.toml` filters.

**Why an oracle, not in-LSP config parsing.** The resolved filter set can come
from two sources the LSP cannot see by re-reading `gsx.toml`: a project binary's
compiled-in `gen.WithFilters`/`WithFilter` registrations (code, not data). The
`gsx info` command already resolves *both* (gsx.toml aliases + compiled-in) and
already emits them as JSON (`buildManifest`). Shelling out to it makes the LSP
agnostic to *how* filters are defined — it just consumes the resolved list. The
subprocess boundary is the decoupling: the resolver can evolve (gsx.toml today,
compiled-in tomorrow) without the LSP changing. No "detect the project pattern"
heuristic is needed — the project *declares* its command.

**Non-goals (this slice).**
- **`gsx generate`/`info` delegation** — making the stock `gsx generate` re-exec
  the project's binary when `[gsx] command` is set (so a teammate with only the
  stock binary gets correct output). Valuable, but generate already runs the
  project binary directly in the dev loop (wgo/Taskfile), so only the LSP is
  blocked today. Deferred to the roadmap (§9), built on the *same* declaration.
- **Reconstructing the attr classifier from the manifest.** `info --json` carries
  `UserRules`/`HasPredicate`, but a code predicate / custom `FieldMatcher` cannot
  be rebuilt from data, and declarative attr-rule reconstruction is independent
  of the filter problem. The LSP keeps `attrclass.Builtin()` this slice.
- **Re-resolution on `gsx.toml` change.** The oracle runs once per module root
  per session (cached); a config edit is picked up on LSP restart. A file-watch
  refresh is a follow-up (§8 limitation).
- **Editor/Neovim config changes.** The whole point is the stock `gsx lsp` works
  unchanged; the project declares its command in `gsx.toml`.

## 2. Background — the gap

`lspAnalyzer.Analyze` (`gen/lsp.go`) calls `GeneratePackagesWithFilters(root,
dir, nil /*filterPkgs*/, nil /*aliases*/, attrclass.Builtin(), …)` — the LSP only
ever knows the built-in `std` filters. A project filter (`url` →
`structpages.URLFor`, whether via a `gsx.toml` alias or compiled-in
`WithFilter`) is an *unknown filter*, so that component fails analysis and `gd`
on `|> url` returns null even though the pipeline-nav reverse-mapper (already in
this worktree) would resolve the lowered selector if it type-checked.

`gsx info --json` already produces exactly the missing data. From `gen/info.go`:
it calls `codegen.ResolveFilters(dir, filterPkgs, aliases)` and marshals a
`manifest` (`gen/manifest.go`):

```go
type manifest struct {
    SchemaVersion   int
    Module          string
    UserRules       attrclass.Rules
    HasPredicate    bool
    PredicateLabel  string
    HasFieldMatcher bool
    Filters         []manifestFilter // {Name, Pkg, Func}
}
```

`manifestFilter{Name, Pkg, Func}` maps 1:1 onto the analyzer's
`codegen.FilterAlias{Name, PkgPath, FuncName}`. The data is already produced; we
wire up the consumer.

## 3. The `[gsx] command` declaration

A new section in the `gsx.toml` schema (`gen/configfile.go`), declaring how
tooling should invoke *this project's* gsx — the command **prefix**; each tool
appends its own subcommand:

```toml
[gsx]
# How tooling invokes THIS project's gsx (with its registered filters). The LSP
# runs `<command> info --json`. Omit it and the stock gsx self-execs `info --json`,
# resolving the declarative gsx.toml filters. Argv array — no shell splitting.
command = ["go", "run", "./cmd/gsx"]
```

**Array form (argv), not a string** — so there is no shell-splitting guesswork
(no heuristic): the LSP execs `command` + `["info", "--json"]` directly.

**Schema wiring.** `tomlConfig` gains:

```go
type tomlConfig struct {
    // … existing fields …
    GSX *tomlGSX `toml:"gsx"`
}
type tomlGSX struct {
    Command []string `toml:"command"`
}
```

`config` gains `gsxCommand []string`; `loadConfig` populates it
(`if tc.GSX != nil { cfg.gsxCommand = tc.GSX.Command }`); `mergeConfig` carries it
(base, unless opts set it — there is no programmatic option yet, so base wins).
Strict decoding still rejects typos elsewhere. `generate`/`info` parse `[gsx]`
but ignore it this slice (only the LSP reads `gsxCommand`).

**Generic, not LSP-private.** `command` is "the project's gsx," readable by any
future tool (a formatter, a CI helper). `[gsx]` groups process-invocation config,
distinct from the codegen rules (`filters`, `[aliases]`, `*Attrs`) it sits beside.

## 4. Architecture — the oracle in `lspAnalyzer`

All changes are in `gen/lsp.go` + the `gsx.toml` schema (`gen/configfile.go`) +
one line in `gen/main.go`. `internal/lsp` and `internal/codegen` are untouched —
pipeline-nav already resolves the lowered `|> url` selector once the filter
type-checks.

```
gsx.toml [gsx] command  ──read──►  lspAnalyzer (cached per module root)
                                         │  run `<command-or-self> info --json` in root
                                         ▼
                                   manifest JSON  ──Filters──►  []FilterAlias
                                         │
                                         ▼
                       GeneratePackagesWithFilters(root, dir, nil, aliases, …)
                          (loads pkgs, re-derives ctx/variadic/(R,error), lowers |>)
```

**`lspAnalyzer` gains two fields + a per-root cache:**

```go
type lspAnalyzer struct {
    warnw io.Writer            // best-effort sink for oracle problems; never fatal
    mu    sync.Mutex
    cache map[string][]codegen.FilterAlias // module root → resolved aliases (nil = std)
}
```

`runLSP` constructs `lspAnalyzer{warnw: stderr, cache: map[...]{}}` and passes it
to `lsp.NewServer`. `case "lsp"` is unchanged except it keeps passing `cmdArgs`
(no config threading needed — the oracle reads `gsx.toml` itself).

**`Analyze(dir, override)`** gains one line before the codegen call:

```go
root, _, err := moduleRoot(dir)
if err != nil { return nil, err }
aliases := a.filtersFor(root)          // NEW — oracle, cached, best-effort
out, err := codegen.GeneratePackagesWithFilters(
    root, []string{dir}, nil, aliases, attrclass.Builtin(), nil, nil, nil, override)
```

Passing the resolved filters as **aliases** (not `filterPkgs`) reproduces the
project's exact resolution: the `[std]` baseline is applied first by the analyzer
(`batch.go` `dedupFilterPkgs`), then each manifest filter is registered as an
explicit alias under last-wins, so a std name shadowed by the project resolves to
the project's func, and std entries (`upper` → `std.Upper`) are harmless
same-func duplicates of the baseline.

## 5. Resolution flow (`filtersFor`)

Cached, mutex-guarded, best-effort — **every failure mode falls back to the std
baseline and never errors `Analyze` or hangs the server.**

```
filtersFor(root):
  if root in cache: return cache[root]
  path, ok = discoverConfig(root)          // walks up, bounded by .git/module root
  if !ok:                                   // no gsx.toml
      cache[root] = nil; return nil         //   → std baseline, NO subprocess (zero overhead)
  command = oracleCommand(path)             // [gsx] command, else [os.Executable()]
  if len(command) == 0:                     // os.Executable() errored — cannot run an oracle
      cache[root] = nil; return nil         //   → std baseline (no exec)
  aliases, err = runInfoOracle(command, root)
  if err != nil:
      warn(warnw, "gsx: lsp: filter resolution via %v failed: %v", command, err)
      cache[root] = nil; return nil         //   → std baseline, logged
  cache[root] = aliases; return aliases
```

**`oracleCommand(path) []string`** — a tolerant read of just `[gsx] command`:
`toml.DecodeFile(path, &struct{ GSX *tomlGSX })` **without** the strict
`Undecoded` check (unknown keys elsewhere are ignored here). If the file is
syntactically broken (decode error) or `command` is empty, fall back to
`[]string{os.Executable()}` (self-exec). If `os.Executable()` itself errors
(rare), return nil → caller treats nil command as "skip oracle, std baseline."

**`runInfoOracle(command, dir) ([]FilterAlias, error)`** —
`exec.CommandContext(ctx, command[0], append(command[1:], "info", "--json")...)`
with `Dir = dir` and a generous timeout (120s — a cold `go run` compiles). It
captures stdout (the JSON) separately from stderr (build noise / errors).
Non-zero exit, timeout, or empty stdout → error (stderr tail included in the
message for the LSP log). On success, `json.Unmarshal` stdout into `manifest`;
reject a `SchemaVersion` the reader does not understand; map `m.Filters` →
`[]FilterAlias{Name, PkgPath: f.Pkg, FuncName: f.Func}` preserving order.

## 6. Behavior & selection

- **`[gsx] command` present** (custom project binary): the LSP runs
  `go run ./cmd/gsx info --json`; full fidelity (compiled-in `WithFilters` +
  `gsx.toml`). Pipelines over project filters resolve; `gd`/hover land on the
  filter func. The first `Analyze` for that root pays the `go run` cold-build
  cost once; subsequent edits hit the cache.
- **`gsx.toml` present, no `[gsx] command`** (declarative aliases only): the LSP
  self-execs `<stock gsx> info --json`, which resolves the `gsx.toml` aliases —
  same result the running binary would compute, via the uniform oracle path.
- **No `gsx.toml`**: nil aliases, no subprocess — byte-identical to today's
  std-only behavior. Zero overhead, zero regression.
- The cache is keyed by module root, so a multi-module workspace resolves each
  module's filters independently.

## 7. Edges, errors, risks

- **Best-effort, never fatal, never hangs:** any oracle problem (missing/broken
  command, non-zero exit, malformed/empty JSON, unknown schema version, timeout)
  → logged to `warnw` (the editor's LSP stderr log) → std baseline. `Analyze`
  never returns an oracle error and never blocks past the timeout.
- **A project filter package that fails to load** (manifest names a since-removed
  package): `GeneratePackagesWithFilters` surfaces a positioned per-file codegen
  error for the affected component — the same path as any unresolvable filter,
  not a crash.
- **Staleness:** the oracle runs once per session per root; a `gsx.toml`/filter
  edit is picked up on LSP restart. Acceptable for v1 (the dev loop restarts the
  editor's LSP often enough); a file-watch refresh is a follow-up.
- **First-edit latency:** with `[gsx] command = go run …`, the first `Analyze`
  blocks on the cold build (seconds). One-time per session, then cached. Noted as
  a known cost; an async warm-up at `initialize` is a possible follow-up.
- **No recursion this slice:** `info` does not act on `[gsx] command` (only the
  LSP does), so `<command> info --json` cannot re-trigger the oracle. The
  recursion guard (`GSX_DELEGATED`) is only needed when generate/info delegation
  lands (§9) and is specified there, not built here.
- **Discovery bound:** `discoverConfig` is bounded by `.git`/module root — no
  `$HOME`/filesystem-root escape.
- **Self-exec untestable in unit tests:** `os.Executable()` under `go test` is
  the test binary, so the *self-exec default* cannot emit a manifest in a
  hermetic test (§8 covers the gap honestly: integration via an explicit helper
  command + a manual smoke for self-exec).

## 8. Testing (per [[gsx-syntax-change-test-coverage]])

`gen` package (the analyzer + config are unexported, so tests live here). The
exec boundary is tested with the stdlib `TestHelperProcess` pattern: a guarded
test func that, when its env var is set, prints a fixed `manifest` JSON to stdout
and exits — the oracle's `command` is set to re-exec the test binary at that
func. No external binary, fully deterministic.

- **`tomlConfig` parses `[gsx] command`** (unit): `loadConfig` on a `gsx.toml`
  with `[gsx] command = ["go","run","./cmd/gsx"]` → `cfg.gsxCommand` equals it;
  a `gsx.toml` without `[gsx]` → `cfg.gsxCommand == nil`; strict decode still
  rejects an unknown key.
- **`oracleCommand`** (unit): returns the declared command when present; returns
  `[os.Executable()]` when `[gsx]` is absent or `command` empty; returns the
  self-exec default (not an error) when the TOML is syntactically broken.
- **Oracle resolves a project filter** (integration, helper-process): a temp
  module with a local filter subpkg (`func Shout(s string) string`), a `.gsx`
  using `{ name |> shout }`, and a `gsx.toml` whose `[gsx] command` re-execs the
  test helper; the helper emits a manifest listing `shout → <mod>/myf.Shout`.
  `lspAnalyzer.Analyze(dir, nil)` → **no** "unknown filter shout" diagnostic, and
  the `{ name |> shout }` interp type-checks (present in `Info`/`ExprMap`).
- **ctx-injected alias** (integration): the helper lists
  `url → <mod>/myf.URL` for `func URL(ctx context.Context, page any, args ...any)
  (string, error)`; `{ page{} |> url("id", x) }` resolves with no diagnostic —
  proving the analyzer re-derives ctx + variadic + `(R,error)` from the
  manifest-named func's *live* signature.
- **Oracle failure → std baseline, logged** (integration, the critical
  robustness test): the helper exits non-zero (or emits garbage); `Analyze`
  returns **no error**, a `{ name |> upper }` std pipeline still resolves, the
  project's `{ name |> shout }` does **not** (unknown-filter diagnostic present),
  and `warnw` received a line. Proves a broken oracle never breaks the LSP.
- **No `gsx.toml` → std baseline, no subprocess** (integration): same module
  without a `gsx.toml` → `{ name |> upper }` resolves, `shout` is unknown, and
  the cache holds nil for the root.
- **Caching** (unit/integration): two `Analyze` calls for the same root invoke
  the oracle once (assert via a helper that records invocations, or a counter).
- Full `go test ./...` green; existing LSP e2e (temp modules, no `gsx.toml`)
  unaffected. **Manual smoke:** re-run the `pipecheck` project (a real `gsx.toml`
  alias, built `gsx` on `PATH`) and confirm `gd` on `|> link` now resolves —
  exercising the self-exec path that unit tests cannot.

## 9. Roadmap / future (not in this spec)

- **`gsx generate`/`info` delegation** — make the stock gsx a launcher: when
  `[gsx] command` is set, `gsx generate|info|lsp` re-exec `<command> <subcommand>`
  so a teammate with only the stock binary gets correct, full-fidelity output and
  the Taskfile drops the `go run ./cmd/gsx` ceremony. Needs a one-env-var
  recursion guard (`GSX_DELEGATED=1` set before re-exec; the child skips
  delegation when it sees it — deterministic, not a heuristic) and a decision on
  generate's fail-fast (fatal) vs the LSP's best-effort (fallback) on a failed
  delegate. Builds on the identical `[gsx] command` declaration this slice adds.
  Tracked in `docs/ROADMAP.md`.
- **Attr-classifier reconstruction** from the manifest `UserRules` (declarative
  rules only; never code predicates), so editor attr-classification matches
  `generate`. Independent of the filter problem.
- **Re-resolution on `gsx.toml` change** (file-watch) + **async warm-up at
  `initialize`** to hide the first-edit cold-build latency.
