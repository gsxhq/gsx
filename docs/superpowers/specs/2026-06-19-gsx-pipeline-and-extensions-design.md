# gsx — Pipeline Operator & Generation Extension Architecture

**Date:** 2026-06-19
**Status:** Approved (design)
**Extends:** §11 of `2026-06-18-gsx-templating-design.md` (Extensions — direction
only). This doc materializes that direction by designing the first concrete
extension (a filter resolver) and the library seam every extension plugs into.
**Module:** `github.com/gsxhq/gsx`

## Summary

Two coupled additions, designed together because the second is *discovered from*
the first (the §11 dogfooding principle):

1. **A `|>` pipeline operator** inside interpolation/attribute values, for
   left-to-right chaining of transformations: `{ name |> upper |> truncate(20) }`.
   It lowers to plain Go function application — `a |> R` becomes `R(a)` — so it is
   pure syntax over type-checked Go, no new runtime.
2. **Generation exposed as an extensible library** (`gsx/gen`), so a project
   supplies its own *central* transformation packages and other hooks by writing a
   tiny `cmd/gsx/main.go` — **code-level registration, not config** (the decision
   already locked in the CLI spec, §4). The pipeline's filters are the first
   consumer of this seam.

### Motivation (the itch)

templ lets you write helper functions (`{ upper(name) }`), but two pains remain:

- **No ergonomic chaining in Go.** `truncate(upper(name), 20)` reads inside-out;
  Go has no pipe. Go's own `text/template` pipeline (`{{ .Name | upper | truncate
  20 }}`) reads in data-flow order and is "much easier."
- **Helpers have no home.** You re-import or re-define transformation helpers in
  every file. There is no *central registry* of transformations.

This design gives both: the pipeline restores left-to-right chaining, and the
library seam gives transformations a single, ambient home.

## Part A — The `|>` pipeline operator

### A.1 Semantics: `a |> R ≡ R(a)`

The operator is reverse function application. Each stage's right-hand side is an
ordinary Go expression that evaluates to a callable; the piped value becomes its
argument:

```go
{ name |> upper }              →  upper(name)
{ name |> upper |> trim }      →  trim(upper(name))
{ name |> truncate(20) }       →  truncate(20)(name)        // partial application
{ x |> a |> b |> c }           →  c(b(a(x)))
```

There is **one** rule, applied left-to-right. No splice, no "value-first vs
value-last" argument-position question — the partial-application form removes it.

### A.2 Filter shape: the partial-application contract

A transformation ("filter") is a Go function in one of two shapes:

| Form | Go signature | Use |
|------|--------------|-----|
| **bare** | `func(T) R` | `x \|> upper` → `upper(x)` |
| **parameterized** | `func(Args…) func(T) R` | `x \|> truncate(20)` → `truncate(20)(x)` |

The presence of `(...)` after the name selects the lowering, purely
syntactically — `upper` → `upper(x)`, `truncate(20)` → `truncate(20)(x)`. The Go
compiler type-checks the result; a non-curried function used with arguments simply
fails to compile (a clear, normal Go error via the `//line`-mapped generated code).

Consequence accepted as the intended workflow: an existing unary func like
`strings.ToUpper` (`func(string) string`) is a drop-in bare filter; a multi-arg
func like `strings.Repeat(s, n)` needs a one-line curried wrapper
(`func Repeat(n int) func(string) string`) — written **once**, in your central
filter package (Part B), which is exactly the point.

### A.3 Why `|>`, not `|`

`|` is valid Go (bitwise-OR), so reusing it forces either type-resolution to
disambiguate or a "top-level `|` always means pipe; parenthesize for bitwise"
rule whose failure mode is *silent reinterpretation* of a real bitwise-OR. That
violates the project's "correct, not surprising" bar. `|>` is **never** valid Go,
so:

- Zero collision, ever. `|` keeps its normal Go meaning untouched.
- No reserved operator inside `{ }`, no escape-hatch rule to teach.
- The parser stays a trivial depth-0 split (A.6).

The only thing `|` offered was Go-template muscle memory, which loses to
correctness here.

### A.4 Scope: top-level within an interpolation only

A pipeline is a top-level construct inside a single `{ }` (or attribute `{ }`). It
is **not** nestable inside a larger Go sub-expression:

```go
{ "Hi " + name |> upper }     →  upper("Hi " + name)   // whole seed is piped
```

To pipe a sub-expression, lift it: compute it in a `{{ }}` block as a local, then
use the local. **Rationale:** locating a pipeline *inside* arbitrary Go
(`{ "Hi " + (name |> upper) }`) would require gsx to parse Go expression
structure; today the interpolation interior is an opaque string handed to the Go
compiler (`parser/markup.go`), and we keep it that way. Parentheses therefore mean
ordinary Go grouping/precedence, nothing pipeline-specific.

Nesting across interpolation *boundaries* is free and needs no special rule:

```go
{ for _, x := range xs { <li>{ x |> upper }</li> } }
```

A pipeline as a **filter argument** (`items |> join(sep |> upper)`) is
implementable later if wanted (we control the post-`|>` grammar) but is **deferred
(YAGNI)**.

### A.5 Interactions

- **`?` try-marker (error propagation).** Each stage may be failable. A failable
  filter returns `(R, error)`; `?` unwraps it, short-circuiting the component's
  `Render` exactly as today (`2026-06-18-gsx-codegen-walkthrough.md` §2):

  ```go
  { name |> validate()? |> upper }
  // v0, err := validate()(name); if err != nil { return err }
  // ... upper(v0)
  ```

- **Type-aware rendering.** The pipeline produces a value; that value is then
  rendered by the existing type-aware interpolation rules (templating-design §5
  table). `{ x |> upper }` yields a `string` → HTML-escaped text in body context,
  attribute-escaped in attribute context, etc. The pipeline composes *before* the
  escaper, never around it.

- **Attribute values.** Identical grammar, so pipelines work in attributes:
  `href={ u |> absolute }`, `class={ name |> slug }`.

### A.6 Parser & codegen implementation

No change to any Go-parsing path. The interpolation interior is already an opaque
string (`parser/markup.go:parseInterp`), and `?` is already a lightweight suffix
marker — pipelines are the same kind of pre-processing, one level up, and reuse the
**depth-0 split** technique gsx already uses for the `class`/`style` comma-list
(templating-design §3, "split at bracket depth 0"):

1. After `goExprEnd` yields the interior, split on `|>` at **bracket depth 0**
   (using the `go/scanner`-based depth tracking already in `parser/boundary.go`, so
   `|>` inside strings/runes/comments/brackets is ignored).
2. Produce a new AST node:
   ```go
   // ast.Pipe is `{ seed |> f1 |> f2(args) ... }`.
   type Pipe struct {
       Seed    string       // opaque Go expression, compiler-validated
       Stages  []PipeStage  // each: Name + optional Args (opaque Go), Try bool
       // span...
   }
   ```
   Each stage carries its own `?` (split first, then per-stage suffix check).
3. Codegen lowers `Pipe` to nested `R(a)` calls, resolving each stage's `Name` via
   the filter resolver (Part B) into a qualified call (`std.Upper(...)`), threading
   `?` stages through temp + error-check as today.

The seed and stage arguments remain opaque Go strings handed to the compiler; only
the stage **name** is a token gsx resolves.

## Part B — Filters & resolution

### B.1 Resolver: name → qualified Go func (compile-time)

gsx resolves each pipeline stage **name** to a concrete, qualified Go function at
codegen and emits the direct typed call into `.x.go` (plus the precise import). gsx
is a compiler, not an interpreter — there is **no runtime FuncMap, no reflection in
generated code, no `any`**. The Go compiler type-checks every emitted call. This is
the type-aware-where-it-pays principle (templating-design Principles): gsx already
runs `go/types`, so it builds the name→func table from real typed functions.

### B.2 Harvest-by-contract

A filter package is **ordinary Go**. gsx harvests every exported function whose
signature matches the filter contract (A.2: `func(T) R` or
`func(Args…) func(T) R`) and names it by the §3-style first-letter rule, **lowered**
for the template feel: `Upper` → `upper`, `Truncate` → `truncate`. Authoring a
filter is therefore "write a Go function" — zero registration boilerplate.

- **Naming friction (known, same as §3).** First-letter-lower is clean for the
  common cases. Go **initialisms** are the friction (`URLEncode` → `uRLEncode` is
  ugly); an initialism-aware name mapper is the *same* pluggable concern as the
  §3 attribute→field mapper and is **deferred**. Verbatim-name mode (`|> Upper`)
  is a possible alternative, not the default.
- **Accidental exposure** is contained because a filter package is opt-in and
  dedicated; you do not dump unrelated helpers there.

### B.3 Built-in stdlib

gsx ships `github.com/gsxhq/gsx/std`, a filter package of common transformations
(`upper`, `lower`, `trim`, `title`, `truncate`, `join`, `default`, …) authored in
the same harvest-by-contract style. It is registered like any other filter package
(B/Part C) — **explicitly**, so nothing is hidden (CLI spec "nothing hidden from an
agent").

### B.4 Collision & precedence

Filter packages are an **ordered** list (registration order, Part C). Resolution is
**last-wins**: a later package shadows an earlier same-named filter. This makes
overriding a built-in deterministic and intentional (put your package after
`std`). There is no ambiguity — order always decides. Discoverability backstops
surprise:

- **`gsx info`** lists the resolved filter table (name → owning package).
- **`gsx vet`** warns when a user filter shadows a `std` built-in.

### B.5 Discoverability

`gsx info` reports the active filter set (name, package, signature), so the
ambient namespace is always inspectable from the CLI — the "correct, not
surprising" guarantee for an otherwise-implicit set.

## Part C — Generation as an extensible library

### C.1 The `cmd/gsx` pattern (code-level registration)

Generation is exposed as a library with a composition-root entry point. A project
that needs extensions writes its own `cmd/gsx/main.go`:

```go
// yourmod/cmd/gsx/main.go — your project's gsx, with extensions
package main

import (
    "github.com/gsxhq/gsx/gen"
    gsxstd "github.com/gsxhq/gsx/std"
    "yourmod/filters"
    tailwindmerge "github.com/jackielii/tailwind-merge-go"
)

func main() {
    gen.Main(
        gen.WithFilters(gsxstd.Pkg, filters.Pkg),  // DATA extension (Part B)
        gen.WithClassMerger(tailwindmerge.Merge),  // CODE hook (§11)
    )
}
```

```go
//go:generate go run ./cmd/gsx generate
```

This satisfies every constraint the design hit: **code-level registration, not
config** (CLI spec §4) — real Go, type-checked, gopls-navigable, refactor-safe; and
**nothing hidden** — `cmd/gsx/main.go` is the single readable source of truth for
what is active. Prior art is overwhelming: **ent** (`entc.Generate(..., entc.
Extensions(...))` driven by `//go:generate go run`), **`go/analysis`**
(`multichecker.Main(a1, a2, …)`), wire, sqlboiler, goa.

The stock `go install github.com/gsxhq/gsx/cmd/gsx` binary is just `gen.Main()`
with `std` registered and no other extensions — the zero-extension path, unchanged.

### C.2 `gen.Main` is the whole CLI

`gen.Main(...Option)` **is** gsx, parameterized by extensions — it dispatches the
full command set (`generate`/`fmt`/`vet`/`lsp`/`render`/…), not just codegen. So
`go run ./cmd/gsx lsp` is an editor server that knows your filters,
`go run ./cmd/gsx vet` lints with your rules, and `fmt` (needing no extensions)
behaves identically. Cost, stated honestly: a project *with code hooks* points its
editor/`go:generate` at `go run ./cmd/gsx …` rather than stock `gsx` — the same
trade ent and sqlc make.

### C.3 Public surface (layout change)

Only the **composition root** is promoted to public; the internal stages stay
internal and free to churn (mirroring how `multichecker` is public while its guts
are not). Building on the CLI spec layout:

```
github.com/gsxhq/gsx/gen   PUBLIC  — Main(...Option) + option constructors
                                     (WithFilters, WithClassMerger, WithTransform, …)
github.com/gsxhq/gsx/std   PUBLIC  — built-in filter package (Part B.3)
github.com/gsxhq/gsx/ast   PUBLIC  — unchanged
github.com/gsxhq/gsx/parser PUBLIC — unchanged
internal/cli, internal/analyzer, internal/codegen, internal/printer, internal/diag
                                   — internal (today's internal/gen renamed to
                                     internal/codegen to free the `gen` name)
```

The option set is intentionally **not finalized**: `WithFilters` ships first as the
dogfood case; further options (class merger, attr→field mapper, raw AST transform)
are added as those built-ins are implemented as transformations over the same
pipeline — the §11 "discover the API from the built-ins" principle, now concrete.

### C.4 Harvesting under `cmd/gsx`

`WithFilters` takes per-package markers (e.g. `std.Pkg`, a zero-value of a
package-exported marker type). The running `cmd/gsx` recovers each package's import
path via `reflect.TypeOf(marker).PkgPath()`, then `go/types`-loads that package
from module source and harvests its filter-contract exports (B.2). Reflection only
bridges *value → import path*, keeping registration refactor-safe; no func is
called via reflection and no generated code uses reflection.

## Part D — Data vs code extensions, and the LSP

### D.1 Why not a config file

A config file (`gsx.toml`) was reconsidered to solve LSP discovery and rejected,
for a decisive structural reason: **config cannot carry a code hook.** A custom
class merger or AST transform *is a Go function*; it cannot live in TOML. So
config would not remove the need for `cmd/gsx` — it would add a *second* mechanism
(config for data, `cmd/gsx` for code) alongside it. One mechanism is better.

### D.2 The split

Extensions divide by what they are, and that decides who must run them:

| | Reads filter packages (**data**) | Runs merger/transform (**code**) |
|---|---|---|
| **stock `gsx`** (generate/vet/lsp/fmt) | ✅ via `go/types` from the `WithFilters` declaration | ❌ not linked in |
| **`go run ./cmd/gsx`** | ✅ | ✅ |

- **Filter-only projects** (the common case, the itch) → stock `gsx` does
  everything, **including LSP**. No `cmd/gsx` needed.
- **Code-hook projects** → write `cmd/gsx`; run `go run ./cmd/gsx generate`. The
  editor still uses stock `gsx lsp` (correct, since hooks do not affect
  completion).

### D.3 The LSP needs only the data

`gsx lsp` needs the *filter table* (for completion/diagnostics), not the code
hooks (merging is a codegen concern). The filter table is recoverable **without
executing user code**: a stock `gsx lsp` runs `go/types` over the module (which the
analyzer already does — it holds the markup AST and `go/types` in one position
space, CLI spec §3), finds the `WithFilters(std.Pkg, myfilters.Pkg)` declaration,
recovers the package paths from the marker arguments, and harvests. Filter
completion is **best-effort**: when the module type-checks you get it; mid-edit you
transiently don't — exactly how every Go LSP already degrades. Authoritative
resolution always happens at `generate` time in the real binary, so correctness is
never at stake in the editor.

### D.4 Fail-fast guard

Stock `gsx generate` **statically detects** a declared code hook (it can see
`WithClassMerger(...)` in the source) and **errors**:

> `custom class merger configured — run `go run ./cmd/gsx generate``

So you can never silently generate with the wrong merger. Correct, not surprising —
without config.

## Relationship to §11 (dogfooding)

This design *is* the §11 plan executed, not a departure from it:

- The filter resolver is implemented as a **compile-time transformation** over the
  shared pipeline (the §11 preference over runtime calls), structurally identical to
  the class merger and attribute→field mapper.
- The public extension API is **"expose that seam"** — `gen.Main(...Option)` — and
  it is grown one option at a time as built-ins are dogfooded, not designed up
  front.
- Registration is **code-level, not config**, as the CLI spec §4 pre-committed.

## Open questions / deferred

- **Initialism-aware filter naming** — deferred; same pluggable concern as the §3
  attribute→field mapper.
- **Pipeline as a filter argument** (`items |> join(sep |> upper)`) — implementable,
  deferred (YAGNI).
- **The full `Option` set** (class merger, attr→field mapper, raw AST transform
  signatures) — discovered as each built-in is implemented; only `WithFilters` is
  specified now.
- **Marker type shape** for `WithFilters` (dedicated `gen.Pkg` marker vs any
  package-exported sentinel) — implementation detail.
- **`std` filter inventory** — the exact starter set of built-in filters.

## Out of scope

- Routing/HTTP — gsx is templating only (unchanged).
- The eventual stable, third-party-frozen extension API surface — still deferred per
  §11; this doc fixes the *seam* (`gen.Main` options) and the first consumer, not a
  frozen public contract.
