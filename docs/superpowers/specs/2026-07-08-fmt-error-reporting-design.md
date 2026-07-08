# `gsx fmt` reports the Go errors its analyzer already found

**Status:** design · **Date:** 2026-07-08

## Problem

`gsx fmt` silently succeeds on a `.gsx` file whose Go is invalid. It exits 0,
`fmt -l` reports the file as already clean, and `fmt -w` rewrites it — markup
reformatted, broken Go passed through verbatim — while claiming success.

The same file, same module, through the analyzer:

```
$ gsx generate .
input.gsx:9:1: error[parse-error]: imports must appear before other declarations

$ gsx fmt -l .
(silence, exit 0)
```

A gsx *syntax* error does report (exit 1). Only Go errors are silent.

## Why it is silent — the load-bearing fallback

gsx treats Go as an opaque blob, so the formatter never parses Go. It hands Go
*fragments* to `go/format` and, when `format.Source` fails, falls back to relaying
the fragment verbatim.

That fallback is not an oversight. Instrumenting all seven fallback sites and
running them over the 614 `.gsx` files in `internal/corpus/testdata/cases` +
`examples` produced 24 fallback events. **Twenty-one are legitimate, shipped
syntax:**

| site | case | go/format says |
|---|---|---|
| `fmtExprPreserving` | `goexpr-interp-tag/*` | `expected operand, found '<'` |
| `parseWrapped` | `goexpr-f-literal/*` | `missing ',' in argument list` |
| `parseWrapped` | `goexpr-valueform-init/*` | `expected ')', found ':='` |
| `parseWrapped` | `props/byo_splat_*` | `expected ';', found ':'` |

`{ wrap(<Badge/>) }`, `` f`…` ``, `{ if x := ready(); x { "on" } }`, `{ data... }`
— none of these are Go, and `go/format` rightly rejects them.

Only three events are genuinely invalid Go
(`xpkg/interleaved_imports_error`, `element-literals/stray-import-after-func`,
`goexpr-f-literal/js_value_unsupported`).

So the formatter **uses `go/format`'s failure as a classifier** — "this fragment
contains gsx, leave it alone" — and cannot distinguish that from "this Go is
broken." This is the same conflation as the `goWithElements` bug fixed in #49
(*"gsx cannot parse this Go" ≠ "Go cannot format this Go"*), one level up.

Making the formatter itself report Go errors would mean disambiguating at all
seven sites (placeholder substitution for embedded gsx values, AST routing for
structurally-gsx constructs, plus wrapper→file position mapping at each). That is
rejected: it is large, and it would put diagnostics inside the formatter.

## Key observation: diagnostics already do not live in the formatter

`internal/gsxfmt` and `internal/printer` know nothing about diagnostics. The LSP
already separates the two channels:

| layer | produces | consumes |
|---|---|---|
| analyzer (`codegen.Module`) | positioned `.gsx` diagnostics | — |
| formatter (`gsxfmt`, `printer`) | canonical source | — |
| LSP | — | `publishDiags` (`server.go:443`) ⟂ `handleFormatting` (`format.go`) |
| CLI `fmt` | — | **nothing** ← the hole |

`handleFormatting` only formats; on error it returns no edits. Diagnostics arrive
on a separate, debounced channel. In an editor the broken file already gets a
squiggle *and* still formats.

Meanwhile `gsx fmt` **already runs the analyzer** — `analyzeUnusedImports`
(`gen/fmt.go`) opens a `codegen.Module` per module for unused-import removal, and
that path lowers every `.gsx` to a skeleton carrying `//line` directives and parses
it with `go/parser`. A Go parse error is detected there and thrown away, inside
`buildPackageSkeletons` (`internal/codegen/unused_imports_syntactic.go:153`):

```go
gf, perr := goparser.ParseFile(fset, absXpath, skel, goparser.SkipObjectResolution)
if perr != nil {
    continue        // the parse error dies here; UnusedImports never sees it
}
```

`skeletonParseError` (`internal/codegen/module_importer.go:133`) already converts
exactly this `scanner.ErrorList` into `.gsx`-positioned diagnostics — but it serves
the *type-checking* analyze path that `generate` uses. The syntactic import path
never calls it, which is why `generate` reports and `fmt` does not.

**The missing piece is not in the formatter. The CLI lacks the diagnostic channel
the LSP has, and the analyzer's syntactic path drops the diagnostic on the floor
before the CLI could ask for it.**

## Design

### Boundary

The formatter is untouched. `internal/printer`, `internal/gsxfmt` and `parser`
get zero lines. Diagnostics belong to the analyzer and are surfaced by the CLI —
the same split the LSP uses, with a different transport (stderr instead of
`publishDiagnostics`).

### Behavior

When the analyzer reports a Go parse error for a file being formatted, `gsx fmt`
prints the positioned diagnostic to stderr and exits nonzero — **and still
formats, writes, lists and diffs exactly as before.**

This deliberately diverges from gofmt. gofmt refuses to write because it produced
*nothing*: an unparseable file yields no output. gsx produced correct output (the
broken Go relays verbatim, no data loss); refusing to write would discard work it
successfully did, and would make `gsx fmt` disagree with format-on-save, which
formats the same buffer. What is adopted from gofmt is the part that matters:
**never silently succeed.**

This matches the existing precedent in `TestFmtParseError`: a file with a gsx
parse error reports and exits 1, while a good sibling is still formatted to stdout.

### Changes

**`internal/codegen/unused_imports_syntactic.go`** — stop dropping the parse error.
`buildPackageSkeletons` collects it into `packageSkeletons.goParseDiags`, reusing the
existing `skeletonParseError` → `diagnosticsFromParseError` conversion, and still
`continue`s (that file keeps all its imports; its siblings are still analyzed).
`Module.UnusedImports` returns them alongside its map:

```go
func (m *Module) UnusedImports(dir string) (map[string][]UnusedImport, []diag.Diagnostic, error)
```

They are **diagnostics, not an error**. The `error` return stays reserved for faults
that make the whole package unanalyzable.

**`gen/fmt.go`** — `analyzeUnusedImports` buckets those diagnostics by the absolute
`.gsx` path each one points at, and returns them alongside the import map. They are
collected even when `UnusedImports` also errored.

**`runFmt`** — `reportGoDiagnostics` renders the diagnostics for the files in the
format set (sorted by file/line/column, as `gen/main.go:405` does), choosing
`diag.RenderRich` when stderr is a TTY and `diag.RenderCompact` otherwise — the same
choice `gen/main.go:416-429` makes. Sets `exit = 1` if any is error-severity.
Formatting then proceeds unchanged.

### Error discrimination — what makes this safe

Only a `scanner.ErrorList` from the skeleton parse becomes a diagnostic;
`skeletonParseError` returns any other fault unchanged and `diagnosticsFromParseError`
then reports `ok=false`, so it is dropped exactly as before. A failure to
`codegen.Open` a module — unresolved dependencies, network, not a module — keeps its
`continue` and produces no diagnostics at all. A project that cannot load must never
start failing `gsx fmt`.

### Scope limits

These are accepted, not oversights:

- **Best-effort, not gofmt's guarantee.** Detection happens only where the
  analyzer runs: inside a loadable module, without `-no-imports`. A `.gsx` outside
  a module stays silent — exactly as the LSP shows no squiggles without analysis.
  Buying the unconditional guarantee costs the seven-site formatter change,
  rejected above.
- **The skeleton is per-package.** A broken sibling produces diagnostics
  attributed to *that* file. If it is not in the format set, its diagnostics are
  not printed (and, as today, that directory gets no import removal).
- **Exit 1, not gofmt's 2.** `runFmt` already uses `2` for usage and I/O faults
  and `1` for per-file problems. A diagnostic is a per-file problem.

## Testing

- Broken Go in a loadable module → positioned diagnostic on stderr, exit 1, file
  still formatted (stdout) / written (`-w`).
- Module that cannot load (missing `require`) → no diagnostic, exit unchanged.
  *This is the regression that would otherwise break every CI.*
- `-no-imports` → no diagnostic, exit unchanged.
- Clean file in a loadable module → byte-identical behavior, exit 0, empty stderr.
- Diagnostics are attributed per file: a broken file and a clean sibling in one
  `fmt` invocation report only the broken one, and the sibling still formats.

Corpus already carries the true-positive inputs (`xpkg/interleaved_imports_error`,
`element-literals/stray-import-after-func`); the new coverage is CLI-level in
`gen/fmt_test.go`, whose `fmtCapture` + `repoRoot` helpers already build real
modules.

## Behavior change

`gsx fmt` on a tree containing a Go syntax error now exits 1 where it exited 0.
That is the point of the change, but it breaks anyone gating CI on `gsx fmt -l`
with such a tree. Note it in `docs/ROADMAP.md`.

## Out of scope

- Teaching the formatter to distinguish gsx syntax from broken Go (the seven-site
  placeholder change). Would close the no-module gap and let the printer stop
  using `format.Source` failure as a classifier — but it belongs in the formatter,
  which must stay diagnostic-free.
- `fmt --json`. `generate` has `--format=ndjson`; `fmt` has no JSON mode today and
  does not gain one here.
