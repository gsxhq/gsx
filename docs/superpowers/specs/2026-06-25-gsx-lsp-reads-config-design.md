# gsx LSP — resolve project filters from `gsx.toml` in-process

**Status:** approved design (brainstormed 2026-06-25), ready for an implementation plan.

## 1. Goal & non-goals

**Goal.** Make a project's declarative pipeline filters resolve in the language
server, so `gd`/hover/diagnostics on `{ x |> url(…) }`, `{ … |> id }`, etc. work
in the editor — completing pipeline-nav end-to-end. The LSP resolves config the
same way `generate`/`info` already do — `mergeConfig(gsx.toml, opts)` — but
**in-process and best-effort**: no subprocess, no delegation, no oracle. The
stock `~/bin/gsx lsp` reads the project's `gsx.toml`; a project filter declared
as `[aliases] url = "structpages.URLFor"` then resolves cleanly in the editor.

**Design principle: the LSP spawns nothing.** The previous design iterations
(an `gsx info --json` oracle subprocess; `syscall.Exec` delegation to the
project binary) were rejected because they put process ownership and cleanup on
the LSP — orphaned, memory-heavy LSP children accumulating across editor
restarts, plus `go run` signal-forwarding hazards and a fallback maze. By
resolving config **in the LSP's own process**, there is exactly one process —
the one the editor launched and already manages — and nothing that can leak.
"Good enough" symbol resolution (most filters resolve) is the bar; full fidelity
for code-only options is explicitly out of scope (§6).

**Non-goals (this slice).**
- **Any subprocess** — no `gsx info --json` oracle, no `go run`/binary exec. The
  LSP never spawns a child.
- **`[gsx] command` declaration / generate-delegation** — the launcher idea
  (stock gsx re-execs the project binary) stays a roadmap item (§7); it is not
  needed for declarative filters and is where the orphan/`go run` problems live.
- **Code-only options in the *stock* LSP** — a `WithFilter` func-value, a
  `WithAttrClassifier` predicate, a `WithFieldMatcher`: these exist only in a
  compiled binary, never in `gsx.toml` data (§6 covers the two honest outs,
  neither of which this slice builds).
- **Caching / re-resolution machinery** — config is re-read per `Analyze` (a
  cheap file read + TOML parse, negligible beside the `go/packages` load), so a
  `gsx.toml` edit is picked up live with no cache to invalidate.

## 2. Background — the gap and the two config sources

`lspAnalyzer.Analyze` (`gen/lsp.go`) calls `GeneratePackagesWithFilters(root,
dir, nil /*filterPkgs*/, nil /*aliases*/, attrclass.Builtin(), nil /*fm*/, …)` —
it discards the `config` entirely and only ever knows the built-in `std`
filters. A project filter (`url`) is an *unknown filter*, so that component fails
analysis and `gd` on `|> url` returns null even though the pipeline-nav
reverse-mapper (already in this worktree) would resolve the lowered selector once
the filter type-checks.

gsx has two config sources, already merged for `generate`/`info` by
`resolveConfig` → `mergeConfig(fileCfg /*base*/, optCfg /*on top*/)`:

1. **Programmatic opts** (`gen.Main(With…)`, compiled into a binary) → a `config`
   with `filterPkgs`, `aliases`, `*Rules`, `attrPred`/`predLabel`,
   `fieldMatcher`, `cssMin`/`jsMin`. The func-valued ones (a `WithFilter`
   func-value, a predicate, a field matcher, minifiers) are **code** — only a
   binary that compiled them has them.
2. **`gsx.toml`** (`loadConfig`) → the **data-expressible subset**: `filterPkgs`,
   `aliases` (`name = "pkg.Func"`), `*Rules`. No func-valued fields.

This slice makes the LSP consume the SAME merge `generate`/`info` consume — just
in-process and best-effort.

## 3. Architecture

All changes are in `gen/lsp.go` + one line in `gen/main.go`. `internal/lsp`,
`internal/codegen`, and the `gsx.toml` schema (`gen/configfile.go`) are
untouched — pipeline-nav already resolves the lowered `|> url` selector once the
filter type-checks.

```
case "lsp": runLSP(…, cfg, …)        # the missing one line — pass the resolved opts through
        │
        ▼
lspAnalyzer{optCfg: cfg, warnw: stderr}
        │  Analyze(dir, override):
        │    merged = mergeConfig(loadConfig(gsx.toml@dir), optCfg)   # best-effort, in-process
        ▼
GeneratePackagesWithFilters(root, dir,
    merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher, nil, nil, override)
```

**`gen/main.go`** — `case "lsp"` threads the resolved `cfg` (it is already in
`runConfig`'s scope; `cfg.errs` are already surfaced at the top of `runConfig`
before dispatch, so a misconfigured custom binary still fails clearly):

```go
case "lsp":
    return runLSP(os.Stdin, stdout, stderr, cfg, cmdArgs)
```

**`gen/lsp.go`** — `runLSP` gains a `cfg config` parameter and builds an analyzer
that carries it plus the stderr warn-sink:

```go
func runLSP(stdin io.Reader, stdout, stderr io.Writer, cfg config, _ []string) int {
    srv := lsp.NewServer(stdin, stdout, lspAnalyzer{optCfg: cfg, warnw: stderr})
    // … unchanged …
}

type lspAnalyzer struct {
    optCfg config
    warnw  io.Writer // best-effort sink for a malformed gsx.toml; never fatal
}
```

**`Analyze`** resolves config best-effort, then feeds the merged values to the
codegen call (the only changed line is the resolve + the four argument slots):

```go
func (a lspAnalyzer) Analyze(dir string, override map[string][]byte) (*lsp.Package, error) {
    root, _, err := moduleRoot(dir)
    if err != nil {
        return nil, err
    }
    merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)   // NEW
    out, err := codegen.GeneratePackagesWithFilters(
        root, []string{dir},
        merged.filterPkgs, merged.aliases, merged.classifier(), merged.fieldMatcher,
        nil, nil, // cssMin/jsMin irrelevant to analysis (the LSP never emits)
        override)
    if err != nil {
        return nil, err
    }
    // … unchanged: build cross/nav/unused, return *lsp.Package …
}
```

**`resolveConfigBestEffort`** — the best-effort sibling of `resolveConfig`:
anchored at the analyzed dir (the LSP is not chdir'd, unlike `generate`/`info`
which resolve from `"."`), and **never fatal** — a load error logs and falls back
to opts-only instead of returning an error:

```go
// resolveConfigBestEffort discovers a gsx.toml from dir (walking up, bounded by
// .git/module root) and merges it under optCfg, exactly as resolveConfig does for
// generate/info — but for the LSP it must NEVER break analysis: a malformed/typo'd
// gsx.toml is logged to warnw and the optCfg baseline is used. With no gsx.toml it
// returns optCfg unchanged.
func resolveConfigBestEffort(dir string, optCfg config, warnw io.Writer) config {
    path, ok := discoverConfig(dir)
    if !ok {
        return optCfg
    }
    fileCfg, err := loadConfig(path)
    if err != nil {
        fmt.Fprintf(warnw, "gsx: lsp: ignoring %s: %v\n", path, err)
        return optCfg
    }
    return mergeConfig(fileCfg, optCfg)
}
```

`config{}.classifier()` is behaviorally identical to `attrclass.Builtin()` (the
built-ins are the floor, checked first; an empty `config` carries no user rules
and a nil predicate — verified against `attrclass.New`/`Builtin`), so the
no-config path is byte-for-byte today's behavior.

## 4. Why aliases/filterPkgs reproduce the project's resolution

`GeneratePackagesWithFilters` applies the `[std]` baseline first
(`batch.go` `dedupFilterPkgs`), then `merged.filterPkgs` (last-wins by name) and
the explicit `merged.aliases` (appended after, last-wins). This is the identical
input `generate`/`info` pass, so the LSP resolves filters exactly as the build
does: a project alias `url → structpages.URLFor` registers, std names are
harmless same-func duplicates of the baseline, and a `gsx.toml` that intentionally
shadows a std name resolves to the project's func. The analyzer loads the named
packages via `go/types` and re-derives the seed-first contract (ctx-injection,
variadic, `(R, error)`) from the live signatures — so a ctx-taking alias
(`func URL(ctx, page, args…) (string, error)`) lowers correctly with no
codegen change.

## 5. Behavior & selection

- **`gsx.toml` present** (declarative aliases/filters/rules): resolved in-process;
  `{ x |> url(…) }` type-checks → `gd`/hover land on the alias's func, the
  unknown-filter diagnostic is gone. The common case, fully covered.
- **Custom binary run *as* the LSP** (`./cmd/gsx lsp`, opts compiled in): `optCfg`
  is non-empty, so its `WithFilter`/predicate/`FieldMatcher` are honored too —
  full fidelity, for free, because `case "lsp"` now threads `cfg`. The editor
  owns that process (it launched it) and cleans it up; gsx spawns nothing.
- **No `gsx.toml`, stock binary**: `optCfg` empty, no file → std baseline —
  byte-identical to today. Zero overhead, zero regression.
- **Malformed `gsx.toml`**: logged to the editor's LSP stderr log, opts/std
  baseline used. The LSP never errors or degrades below today.

## 6. The accepted gap (and the two honest outs)

The *stock* LSP cannot see code-only options — a `WithFilter` func-value, a
`WithAttrClassifier` predicate, a `WithFieldMatcher` — because they live only in a
compiled binary, never in `gsx.toml` data. This is accepted: "good enough" symbol
resolution is the bar, and the dominant pain (filters like `url`/`id`/`target`) is
data-expressible. Neither out is built or owned by this slice:

1. **Declare filters as `gsx.toml` aliases** (the data form) — resolved
   in-process. Covers the common case.
2. **Point the editor at the project binary's `lsp`** (`./bin/gsx lsp`) — works
   for free because `case "lsp"` honors in-process opts, and the **editor** owns
   and reaps that single process. No gsx-side spawning, so no orphan risk on us.

## 7. Roadmap / future (not in this spec)

- **`[gsx] command` + generate/info/lsp delegation** — a `gsx.toml`
  `[gsx] command = ["./bin/gsx"]` declaring the project's gsx, so the stock binary
  can `syscall.Exec` into it (single process, full fidelity) for any command.
  Deferred: it reintroduces process-ownership questions (the `go run` orphan
  hazard, a build-failure fallback) that the in-process design avoids, and is
  unnecessary for declarative filters. Tracked in `docs/ROADMAP.md`.
- **Attr-classifier reconstruction** for editor parity on custom attr names
  (declarative `*Attrs` rules already flow via `gsx.toml`; code predicates never
  can).

## 8. Testing (per [[gsx-syntax-change-test-coverage]])

`gen` package (the analyzer + config are unexported, so tests live here). All
tests are hermetic — **no subprocess** — since resolution is in-process. Existing
LSP e2e tests use temp modules with unique paths and no `gsx.toml`, so they are
unaffected (discovery finds nothing → optCfg/std baseline).

- **`gsx.toml` alias resolves in the LSP**: a temp module with a local filter
  sub-package (a seed-first `func Shout(s string) string`) and a `.gsx` using
  `{ name |> shout }`. Write a `gsx.toml` with `[aliases] shout = "<mod>/myf.Shout"`
  at the module root, then `lspAnalyzer{}.Analyze(dir, nil)` → the package has
  **no** "unknown filter shout" diagnostic and the `{ name |> shout }` interp
  resolves (its type is present in `Info`/`ExprMap`).
- **ctx-injected alias**: a local `func URL(ctx context.Context, page any, args
  ...any) (string, error)` declared as `[aliases] url = "<mod>/myf.URL"`;
  `{ page{} |> url("id", x) }` resolves with no diagnostic — proving the analyzer
  re-derives ctx + variadic + `(R,error)` from the alias's live signature.
- **In-process opts honored (the opt path, no subprocess)**:
  `lspAnalyzer{optCfg: <config carrying an alias shout → <mod>/myf.Shout>}.Analyze`
  on a module **without** a `gsx.toml` → `{ name |> shout }` resolves, proving
  `case "lsp"` threading `cfg` works and opts feed the analyzer.
- **Malformed `gsx.toml` → fallback, logged** (the critical robustness test): a
  `gsx.toml` with an unknown key (or a bad alias) + a `{ name |> upper }` std
  pipeline → `Analyze` returns **no error**, `upper` still resolves, the project's
  `{ name |> shout }` does **not** (unknown-filter diagnostic present), and
  `warnw` received a line. Proves a broken `gsx.toml` never breaks the LSP.
- **No `gsx.toml` → std baseline (no regression)**: same module without a
  `gsx.toml` → `{ name |> upper }` resolves, `shout` is unknown.
- **`resolveConfigBestEffort` unit**: no file → returns optCfg unchanged; valid
  file → `mergeConfig` result (base ++ opts); load error → optCfg + a `warnw`
  line, no panic.
- Full `go test ./...` green. **Manual smoke:** re-run the `pipecheck` project
  (real `gsx.toml` alias) and confirm `gd` on `|> link` now resolves in an editor.
