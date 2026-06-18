# gsx — CLI Skeleton & Conventions Design

**Date:** 2026-06-18
**Status:** Approved (design)
**Module:** `github.com/gsxhq/gsx`
**Depends on:** the language design (`2026-06-18-gsx-templating-design.md`) and the
core parser/codegen (in progress, `feat/parser-core`).

## Summary

This spec defines the **foundation that every gsx tool shares**: the `gsx` binary,
its subcommand structure, the unified diagnostic model, the source-mapping
convention, the core↔front-end package boundary, and the conventions that make the
whole toolchain **agent-friendly** (deterministic, machine-readable, source-mapped,
self-documenting).

It is the *first* tooling spec. It deliberately does **not** design any individual
command's behavior — `generate`, `fmt`, `vet`, `render`, `lsp`, `init` each get their
own spec. What this spec fixes is the **contract** those commands plug into, so they
can be built in parallel and behave consistently.

### Guiding inspirations

- **The Go toolchain** — one static binary, hand-rolled subcommand dispatch (the `go`
  tool's own model), `file:line:col: message` errors, `-C <dir>`, `go generate`
  integration, zero config files (gofmt), shared `go/parser`+`go/types` reused by
  every tool.
- **templ** — `//line` directives in generated code so the Go compiler reports errors
  at template-source locations; subcommand layout (`generate`, `fmt`, `lsp`, `version`).
- **Vite** — `npm create`-style scaffolding (`gsx init`), and (in a later spec) Vite
  as the dev front door rather than a tool-owned proxy.
- **rustc** — stable diagnostic codes with `--explain`.

### The agent-friendliness thesis

Four properties, each realized by a concrete mechanism below:

1. **Verification loop** — `gsx render <pkg.Component>` → HTML on stdout (own spec).
2. **Machine-readable errors** — one `Diagnostic` type, `--json` envelope, stable codes (§2).
3. **Discoverable / self-documenting** — `gsx help --json`, `gsx explain <code>` (§4).
4. **Deterministic & scriptable** — stable exit codes, stream discipline, no interactive
   prompts, idempotent output (§1).

## 1. Binary, Dispatch, Exit Codes

### One binary, hand-rolled dispatch

A single static binary `gsx`. Subcommands are dispatched by a small table — the `go`
tool's own model — using **stdlib `flag` only**. No cobra/urfave/cli. This keeps the
dependency surface near-zero (small binary, fast, easy to vendor) and matches the
"stay close to Go" principle.

A command is a small interface (final shape is an implementation detail; the contract
is what matters):

```go
type Command interface {
    Name() string                          // "generate"
    Synopsis() string                      // one-line help
    SetFlags(fs *flag.FlagSet)             // register command-specific flags
    Run(ctx context.Context, args []string) (exitCode int, err error)
}
```

`internal/cli` owns: the registry, global-flag parsing, dispatch, and translating a
command's `(exitCode, error)` into process exit + rendered diagnostics.

### Command table (reserved at skeleton time)

Each is a registered contract here; behavior is specified separately.

| command | role | own spec |
|---------|------|----------|
| `generate` | lower `.gsx` → `.x.go` (`[--watch]`) | yes |
| `fmt` | canonical formatter (zero-config, idempotent) | yes |
| `vet` | gsx-specific semantic lints | yes |
| `render` | render a component → HTML on stdout (verify loop) | yes |
| `lsp` | language server | yes |
| `init` | scaffold a Vite + Go + gsx starter (source of truth) | Vite spec |
| `version` | build info via `debug.ReadBuildInfo` | here |
| `info` | environment diagnostics | here |
| `help` | help; `help --json` emits the command/flag catalog | here |
| `explain` | long-form docs for a diagnostic code | here |

### Stream discipline (Unix + agent-friendly)

- **stdout = results.** Formatted source from `fmt`, HTML from `render`, the JSON
  document in `--json` mode. Always clean, always pipeable.
- **stderr = diagnostics & progress.** Human-rendered errors/warnings and any
  progress chatter.

Consequence: `gsx fmt file.gsx | …` and `gsx render X > out.html` work without an
agent untangling errors from output.

### Exit-code taxonomy (stable, documented)

| code | meaning |
|------|---------|
| `0` | success, no problems |
| `1` | ran, but found problems (vet findings, `fmt --check` would change files, generate/render compile errors) |
| `2` | usage error (bad flag/arg) |
| `3` | internal error (a gsx bug) — distinct so an agent distinguishes "my input was bad" from "the tool broke" |

### Global flags (on every command)

| flag | meaning |
|------|---------|
| `--json` | emit the machine-readable envelope (§2) on stdout instead of human output |
| `-C <dir>` | run as if started in `<dir>` (like `go -C`) |
| `-q` / `-v` | quiet / verbose progress on stderr |

Positional path args default to `.` and accept files or directories; directories are
walked for `.gsx` (templ-style).

## 2. The Diagnostic Model

The single most important agent-friendly mechanism. One `diag.Diagnostic` type is
produced by the core and rendered by front-ends in two modes.

### Type

```go
package diag

type Severity int // Error, Warning, Info, Hint

type Pos struct {
    Line int // 1-based
    Col  int // 1-based, rune column
    Byte int // 0-based byte offset
}

type Range struct{ Start, End Pos }

type Diagnostic struct {
    Severity    Severity
    Code        string       // stable, e.g. "GSX1003"
    File        string       // always a .gsx path (never .x.go) — see §2 source mapping
    Range       Range
    Message     string
    Related     []Related    // secondary locations ("declared here", etc.)
    Suggestions []Suggestion // optional machine-applicable fixes
}
```

`Related` and `Suggestion` shapes are implementation details; `Suggestion` carries a
text edit (range + replacement) so a future `--fix`/LSP code-action can apply it.

### Human rendering (default → stderr)

Go convention: `file.gsx:line:col: message`, optionally followed by a source snippet
with a caret underlining `Range`. Secondary `Related` locations print as additional
`file:line:col: note:` lines.

### `--json` rendering (→ stdout)

A stable per-command envelope:

```json
{
  "command": "vet",
  "version": "v0.1.0",
  "diagnostics": [
    {
      "severity": "error",
      "code": "GSX1003",
      "file": "card.gsx",
      "range": { "start": {"line":7,"col":5,"byte":142},
                 "end":   {"line":7,"col":18,"byte":155} },
      "message": "component places no {children} but caller passes children",
      "related": [],
      "suggestions": []
    }
  ],
  "summary": { "errors": 1, "warnings": 0 }
}
```

Commands with their own result payload (e.g. `render` HTML, `fmt` file list) add a
command-specific field to this same envelope; `diagnostics` and `summary` are always
present.

### Stable diagnostic codes (`GSXnnnn`) are the linchpin

Every diagnostic carries a stable `code`. An agent matches on the code (not the prose
message, which may change), and `gsx explain GSX1003` (§4) documents it. The code
registry is the single source of truth shared by the diagnostic emitters, `explain`,
and the docs.

### Source-mapping decision: `//line` directives (borrowed from templ)

Generated `.x.go` carries `//line card.gsx:7` directives mapping each emitted span
back to its `.gsx` origin. Consequences:

- **`go build`, `go vet`, and gopls report errors at `.gsx` locations for free** — no
  custom translation layer over the Go toolchain's output.
- gsx's *own* diagnostics (parse, vet) are natively at `.gsx` positions.
- **Net invariant: no tool in the chain ever surfaces a `.x.go` path to the user or
  an agent.** `Diagnostic.File` is always a `.gsx` path.

This spec fixes `//line` as a **binding convention**; `gsx generate` implements it.

## 3. Core ↔ Front-end Boundary

Every front-end is a thin shell over a shared core (the Go toolchain's model, and the
gsx design's §11 dogfooding principle applied to tooling).

### Package layout: public AST/parser, internal everything-else

The AST + parser are a **public, ecosystem-facing API** — the model the entire Go
tooling ecosystem (including `golang.org/x/tools/go/analysis`) is built to consume.
Everything else is internal and free to churn.

```
github.com/gsxhq/gsx/ast       PUBLIC, stable  — node types (promoted from internal)
github.com/gsxhq/gsx/parser    PUBLIC, stable  — ParseFile(fset, name, src) (*ast.File, error)
github.com/gsxhq/gsx           PUBLIC          — runtime: Node, Func, Attrs, Raw/SafeURL, gw helpers
github.com/gsxhq/gsx/analysis  PLANNED (deferred) — go/analysis bridge (gsx AST + go/types)

cmd/gsx/main.go     entry; registers commands, dispatches
internal/cli        Command interface, dispatch, global flags, exit codes
internal/analyzer   semantic + type-aware pass (go/packages, go/types)
internal/gen        codegen → .x.go + //line directives
internal/printer    canonical formatter (fmt)
internal/diag       Diagnostic, codes, severity, human + JSON rendering
```

### Why public AST: don't repeat templ's re-parsing tax

templ never exposed a consumable AST, so external tools that need the *template's own
structure* must re-parse `.templ` by hand (the structpages linter's `templscan`
re-runs `go/parser` on extracted snippets). That tax — and the grammar drift it
invites — is exactly what gsx avoids. An external linter or extension, in a separate
module, consumes gsx like the Go standard library:

```go
import (
    "go/token"
    "github.com/gsxhq/gsx/ast"
    "github.com/gsxhq/gsx/parser"
)
fset := token.NewFileSet()
f, err := parser.ParseFile(fset, "card.gsx", src, 0)
ast.Inspect(f, func(n ast.Node) bool { /* walk components, attrs, interpolations */ })
```

**Reuse `go/token` (FileSet/Pos), do not invent `gsx/token`.** This is the key interop
move: gsx positions live in the *same* `*token.FileSet` as the generated Go, so a
single analyzer can hold the gsx markup AST **and** `go/types` info and map between them
in one position space — something templscan could not do.

`parser.ParseFile` is signature-compatible with `go/parser.ParseFile`. The public AST
is a stability contract (go/ast-style); during v0 it may still evolve, which we will
signal in release notes.

### The contract (internal stages)

Each internal core stage returns `([]diag.Diagnostic, result)` rather than ad-hoc
`error`s:

- parser → AST + diagnostics (public API also offers the `go/parser`-style `error`
  form for std-lib parity; the diagnostic form is what front-ends consume)
- analyzer → resolved type info + diagnostics
- printer → formatted bytes + diagnostics
- gen → `.x.go` bytes + source map + diagnostics

Because every stage speaks the same diagnostic vocabulary, **every front-end renders
results identically** (§2) and adding a command is genuinely just writing a shell that
calls core stages and hands their diagnostics to the renderer.

### Dogfooding proof + the go/analysis bridge

1. **`gsx vet` is built on the public `ast` + `parser`** (the internal `analyzer` layers
   `go/types` on top). If gsx's own linter cannot ride the public API, the API is not
   good enough — the same dogfooding principle as §11's built-ins-as-transforms.
2. **`gsx/analysis` (planned, deferred):** loads a gsx package = parse `.gsx` → public
   AST **plus** load generated `.x.go` → `go/types`, and hands a gsx-aware analyzer both,
   the way the structpages analyzer gets `PageTree` + types together. This is where
   extension/lint authors plug in. The §11 in-process markup-AST transforms consume the
   *same* public `ast` package, so external linters and in-process extensions share one
   tree. The adapter's surface is deferred until `vet` and the first extension exist to
   validate it.

## 4. Config, `go:generate`, Self-documentation

### Zero config file (gofmt philosophy)

No `gsx.toml` in v1 — everything is a flag. Nothing is hidden from an agent, and it
stays Go-aligned. The §11 extension hooks (class merger, attribute→field mapper) are
deferred *and* will be **code-level registration, not config**, so this holds.

### `go generate` integration

`//go:generate gsx generate` is the documented convention, so `go generate ./...`
drives generation; `gsx generate` also walks directories on its own. Both paths, one
tool.

### Self-documentation (the "discoverable" pillar)

- **`gsx explain <CODE>`** — long-form markdown for any diagnostic code (rustc
  `--explain`), from the same code registry that powers §2's `code` field.
- **`gsx help --json`** — emits the full command + flag catalog as JSON, so an agent
  can introspect the entire CLI without scraping `-h` text. A self-describing CLI.
- **`gsx version`** — build info via `debug.ReadBuildInfo`.
- **`gsx info`** — environment diagnostics: go version, gsx version, module path,
  detected `.gsx` files.

## Roadmap (context — specified separately)

This skeleton unblocks, in rough dependency order:

1. **`gsx fmt`** — needs only parser + printer (no codegen/types); can proceed in
   parallel with the codegen agent.
2. **`gsx generate`** — needs analyzer + gen; implements the `//line` convention.
3. **`gsx vet`** — needs analyzer; gsx-specific semantic lints.
4. **`gsx render`** — needs generate; generate→compile→run a harness; the verify loop.
5. **Vite integration** — three deliverables: `vite-plugin-gsx` (npm, generate-on-change
   + reload), `gsx/vite` (Go pkg: manifest reader, asset `gsx.Node`, reload notifier),
   and the **`gsx init` starter template** (front-door `vite.config.js` + proxy +
   backend-guard, `package.json` entryPoints, `gsx/vite` wired into an AppShell,
   example `.gsx`, orchestration entry). gsx **never** owns the dev front door — Vite
   does; gsx stays a fast codegen watcher + integration points.
6. **`gsx lsp` + tree-sitter grammar** — editor highlighting (editors + GitHub) and
   the language server, reusing the core incrementally.

## Out of Scope (this spec)

- Each command's concrete behavior, flags, and algorithms (own specs).
- The Vite plugin / Go package / starter-template internals (Vite spec).
- The tree-sitter grammar and LSP feature set (LSP spec).
- The §11 extension API (deferred by the language design until core exists).
- The `gsx/analysis` go/analysis adapter surface (planned; designed once `vet` and the
  first extension exist to validate it). The public `gsx/ast` + `gsx/parser` API *is*
  in scope here (§3).

## Open Questions

- Whether `--json` should ever be a streaming NDJSON event mode for long-running
  commands (`generate --watch`); the per-command envelope above covers one-shot
  commands, and `lsp` uses LSP/JSON-RPC regardless.
- The exact `Related`/`Suggestion` shapes (fixed when the first `--fix`/code-action
  consumer is built).
- Initial diagnostic-code ranges/namespacing (e.g. `GSX1xxx` parse, `GSX2xxx` analyze,
  `GSX3xxx` vet) — settle when the first emitters land.
