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
(`gen/fmt.go:167`) opens a `codegen.Module` per module for unused-import removal —
and `skeletonParseError` (`internal/codegen/module_importer.go:133`) has already
converted the skeleton's `scanner.ErrorList` into `.gsx`-positioned diagnostics
through the skeleton's `//line` directives.

The work is done. Nobody reads the result:

```go
byPath, err := m.UnusedImports(absDir)
if err != nil {
    continue        // the positioned parse diagnostic dies here
}
```

**The missing piece is not in the formatter. The CLI lacks the diagnostic channel
the LSP has.**

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

**`internal/codegen`** — export the extractor that already exists:

```go
// ParseDiagnostics reports the positioned .gsx diagnostics carried by err, if err
// is a skeleton parse failure. Thin export of diagnosticsFromParseError.
func ParseDiagnostics(err error) ([]diag.Diagnostic, bool)
```

**`gen/fmt.go`** — `analyzeUnusedImports` additionally returns diagnostics keyed
by absolute `.gsx` path:

```go
byPath, err := m.UnusedImports(absDir)
if err != nil {
    if diags, ok := codegen.ParseDiagnostics(err); ok {
        bucketByFilename(diags)   // a real Go syntax error, already positioned
    }
    continue                      // every other failure stays silent, as today
}
```

**`runFmt`** — render the diagnostics for the files in the format set (sorted by
file/line/column, as `gen/main.go:405` does), choosing `diag.RenderRich` when
stderr is a TTY and `diag.RenderCompact` otherwise — the same choice
`gen/main.go:416-429` makes. Set `exit = 1` if any diagnostic is error-severity.
Then proceed with formatting unchanged.

### Error discrimination — what makes this safe

Only `parseDiagnosticsError` is reported. Every other failure from
`codegen.Open` / `Module.UnusedImports` — unresolved dependencies, network,
directory not in a module — keeps its `continue`. A project that cannot load must
never start failing `gsx fmt`. `errors.As` makes the distinction exact.

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
