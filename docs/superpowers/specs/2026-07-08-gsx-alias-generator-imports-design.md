# `_gsx`-alias generator-emitted imports

**Date:** 2026-07-08
**Status:** designed
**Roadmap:** closes `docs/ROADMAP.md` → Tracked debts → *"`_gsx`-alias
generator-emitted imports — robust form of the import-shadow guard (currently
`gsx`/`strconv` are reserved param names as a stopgap)."*

## The rule

> In a `.gsx` file, `github.com/gsxhq/gsx` is an **ordinary Go package**.
> Reference `gsx.X` in your Go → import it. Don't reference it → don't import
> it. An unused import is an error.

Exactly the rule for `fmt`. Nothing about markup, components, element literals,
or `f"…"` literals changes whether you need the import — only whether *your own
Go* names the package.

The generator's runtime imports are a separate, invisible concern: it emits
whatever it needs under reserved `_gsx*` aliases that can never collide with,
satisfy, or be satisfied by the user's imports.

Today the rule holds in one direction and fails in the other. The skeleton
already treats `gsx` as ordinary (it imports the runtime as `_gsxrt`, so a
user's unused `import "github.com/gsxhq/gsx"` correctly errors). The **generated
file** breaks it: it imports `context`, `io`, and `gsx` unconditionally,
regardless of what the source contains, under the bare identifiers.

## Evidence

Probed against `cmd/gsx` at `2abc981`. Each row is a `.gsx` file whose
`gsx generate` **exits 0** and whose `.x.go` then **fails `go build`**:

| `.gsx` source | `generate` | `go build` |
|---|---|---|
| `package p` (nothing else) | exit 0 | `"context"`, `"io"`, `"github.com/gsxhq/gsx"` imported and not used |
| `var S = f"hello world"` | exit 0 | same three unused |
| `func Helper() int { return 1 }` | exit 0 | same three unused |
| `func wrap(n gsx.Node) gsx.Node` + `import` (no component) | exit 0 | `"context"`, `"io"` imported and not used |
| `import gsx "strings"` + a component | exit 0 | `gsx redeclared in this block` |
| `var gsx = 1` + a component | exit 0 | `gsx already declared through import of package gsx` |

Six ways to make `generate` emit non-compiling output while reporting success —
against the invariant `emit.go:3900` states for itself: *"the emitted .x.go would
fail to compile even though generate exited 0, violating the hard invariant that
generate never emits non-compiling output."*

By symmetry `import context "mypkg"`, `var io = 1`, and `var strconv = 1` are
the same defect; `strconv` is the fourth generator-owned identifier (numeric
interpolation fast path).

## Root cause: two questions answered by one constant

`internal/codegen/emit.go:65`

```go
imports := map[string]bool{
    "context":              true,
    "io":                   true,
    "github.com/gsxhq/gsx": true,
}
```

That single literal asserts two independent things, both sometimes false:

1. **Need** — *these imports are required*. False for any file the generator
   emits no runtime references into.
2. **Binding** — *the identifiers `context`, `io`, `gsx` are free*. False for any
   file whose user Go binds one of them.

`boundNames` (`emit.go:108`) seeds `"gsx": "github.com/gsxhq/gsx"` and is used by
the existing collision machinery for filter packages (`_gsxf<i>`) and inferred
type arguments (`_gsxti<N>`) — but it is seeded from the **import region only**,
never from top-level declarations, which is why `var gsx = 1` slips through.

The stopgap, `checkReservedParams` / `emittedImportIdent`
(`internal/codegen/analyze.go:3589`), guards only **parameter names**:

```go
// The runtime import and strconv are the only package idents emitted into
// bodies today; a more robust fix would _gsx-alias generator-emitted imports
// — tracked for phase 2.
var emittedImportIdent = map[string]bool{"gsx": true, "strconv": true}
```

It cannot see file-scope bindings, so it stops `component Foo(gsx string)` while
letting `var gsx = 1` through. This spec is that phase-2 fix.

## Design

### D1 — Need: record at the emission site

`strconv` **already does this correctly** (`emit.go:2798`):

```go
imports["strconv"] = true
return "strconv.FormatInt(int64(" + expr + "), 10)", true
```

The need is recorded exactly where the reference is written. This is not a new
mechanism — `context`, `io`, and `gsx` are simply the three that skip it.

**Change:** the three seeds are removed and each generator-owned import is
recorded at its emission site.

**Representation matters here.** The existing `imports` map is keyed by *path*
(`emit.go:170` merges the user's plain Go-chunk imports into it), so it
structurally cannot hold both `_gsxrt "github.com/gsxhq/gsx"` and a user's plain
`"github.com/gsxhq/gsx"` — the case D3 requires. Generator imports therefore live
in their own small fixed set, disjoint from the user's path-keyed `imports`:

```go
type rtImports struct{ rt, ctx, io, sc bool }   // generator-owned, alias-fixed
```

Both facts (need + name) are produced by a single accessor per import, so they
cannot drift:

```go
func (e *emitter) rt() string  { e.rtim.rt = true;  return "_gsxrt"  }
func (e *emitter) ctxT() string { e.rtim.ctx = true; return "_gsxctx" }
func (e *emitter) ioT() string  { e.rtim.io = true;  return "_gsxio"  }
func (e *emitter) sc() string   { e.rtim.sc = true;  return "_gsxsc"  }
```

Every one of the 16+ hardcoded `"gsx."` sites and the `context.Context` /
`io.Writer` closure preambles routes through these. `writeImports` emits the set
bits as aliased lines, then the user's `imports` as today. Both empty → **no
import block at all**.

Consequences, directly matching the evidence table:

- no gsx parts (empty file, plain Go, `f"…"`) → no import block
- `gsx.Node` in user Go, no component → the user's own `gsx` import only; no
  `context`, no `io`
- any component / element literal / fragment literal → `_gsxrt`, `_gsxctx`,
  `_gsxio` (the closure preamble)
- numeric interpolation → `_gsxsc`

Note `needGsx` without `needCtxIO` is reachable and must be, per row 4.

### D2 — Binding: always `_gsx`-alias

| path | alias | generated references |
|---|---|---|
| `github.com/gsxhq/gsx` | `_gsxrt` | `_gsxrt.Func`, `_gsxrt.W`, `_gsxrt.Class`, `_gsxrt.Node`, `_gsxrt.Attrs`, `_gsxrt.DefaultClassMerge`, … |
| `context` | `_gsxctx` | `_gsxctx.Context` |
| `io` | `_gsxio` | `_gsxio.Writer` |
| `strconv` | `_gsxsc` | `_gsxsc.FormatInt`, `FormatUint`, `FormatFloat` |

**Always**, not on-collision. Uniform and collision-proof by construction, at the
cost of one mechanical golden regeneration. Chosen over conditional aliasing
because conditional aliasing leaves a rarely-exercised second code path through
the emitter — precisely the kind of path these six bugs hid in.

This also aligns emit with the probe: `analyze.go` already writes `_gsxrt` and
`_gsxctx` in 40 places. After this change, generated code and skeleton code name
the runtime identically.

The closure's `ctx` parameter keeps its bare name — it is the documented ambient
context that user interpolations reference (`ctx` stays in
`checkReservedParams`). Only its *type* becomes `_gsxctx.Context`.

**Deletions:**

- `emittedImportIdent` and its `checkReservedParams` branch. `component
  Foo(gsx string)` and `component Bar(strconv int)` become legal.
- The `"gsx"` seed in `boundNames`, and `context`/`io` with it.

**The `_gsx` prefix becomes the reserved identifier space.** Already enforced for
params; extend the check to file-scope declarations (`var`/`const`/`func`/`type`
and import names beginning `_gsx`). This is the one thing a user may not do, and
it replaces four ad-hoc reservations with one documented rule.

### D3 — The user's own `gsx` import

Emitted **plain**, verbatim from the Go chunk, when and only when the user's Go
references it. A file with both a component and `gsx.Node` in user Go therefore
imports one path under two names:

```go
import (
	_gsxctx "context"
	_gsxio  "io"
	_gsxrt  "github.com/gsxhq/gsx"

	"github.com/gsxhq/gsx"
)
```

Legal Go, and the point: generator and user namespaces are strictly separate. No
dedup, no interaction — which is exactly why the generator's imports must not
share the path-keyed `imports` map (see D1). A user's unused `gsx` import errors
from the skeleton type-check exactly as it does today — that is the rule,
working.

**This pattern already exists in the emitter.** `userPlainImports`
(`emit.go:85`) handles the case where a filter package is *also* plain-imported
by the user: `writeImports` emits **both** the reserved-alias line (`_gsxf<i>`)
and the plain line, because *"Go allows the same path under different names…
This mirrors the probe skeleton (analyze.go), keeping emit ≡ probe."* The runtime
import becomes one more instance of the rule the emitter already follows for
filters — extending an established mechanism, not inventing one.

Blank (`_ "…/gsx"`) and dot (`. "…/gsx"`) imports pass through verbatim and bind
nothing named `gsx`; they neither satisfy nor conflict with anything.

### D4 — Close the corpus blind spot

`internal/corpus/batch.go:215`

```go
for _, c := range candidates {
    if !c.renderable() {
        continue          // ← non-renderable cases are NEVER compiled
    }
```

Only cases with an `-- invoke --` section reach `go run .`. A `.gsx` with no
component has nothing to invoke, so its generated output is golden-pinned but
never built. **Every bug in the evidence table lives in that blind spot** —
which is why a corpus of 540 cases (269 gen-pinned) never caught six ways to emit
non-compiling code.

**Change:** after writing `.x.go` for all cases, run one `go build ./...` over
the tmp module, covering non-renderable cases. Skip cases with expected
diagnostics (`diag(error)`), which are not meant to compile.

This is load-bearing: without it, matrix rows 1–3 below would pass while emitting
broken code.

**Risk:** enabling it may surface pre-existing breakage in other non-renderable
corpus cases. Triage each explicitly — fix, or quarantine with a recorded reason.
Do not silence.

## Non-goals

- **Auto-adding the `gsx` import.** Under the rule, a user who writes `gsx.Node`
  without importing gets `undefined: gsx` and adds the import by hand; gopls does
  not run on `.gsx`, and `gsx fmt`'s goimports mode deliberately uses
  `imports.Process` with `FormatOnly: true`, which *"skips goimports' usage-based
  add/remove logic"* (`2026-07-07-organize-imports-design.md` §Reuse) because a
  chunk body does not reference the template's imports. gsx *could* add it later
  — the type-check already knows `gsx` is undefined — but that is a separate
  feature against the organize-imports spec, not this one. Recorded as a
  follow-up.
- **Renaming `ctx`.** It is public, ambient, and referenced by user code.
- **Aliasing user or filter imports.** Filter packages already have `_gsxf<i>`;
  user imports stay verbatim.

## Test matrix

Corpus cases (`.txtar`), each pinning generated output **and**, via D4, that it
compiles:

| # | source | expected imports in `.x.go` |
|---|---|---|
| 1 | `package p` only | none — no import block |
| 2 | plain Go + `import "fmt"` | `fmt` only |
| 3 | `var S = f"hello world"` | none |
| 4 | `func wrap(n gsx.Node) gsx.Node` + user import, no component | `gsx` (plain) only |
| 5 | component only | `_gsxrt`, `_gsxctx`, `_gsxio` |
| 6 | component + `gsx.Node` in user Go + user import | the three aliases **and** plain `gsx` |
| 7 | component + unused `import "…/gsx"` | error: imported and not used (pins the rule) |
| 8 | component + `import g "…/gsx"` used | three aliases + `g` |
| 9 | component + `import gsx "strings"` used | three aliases + `gsx "strings"` |
| 10 | component + `var gsx = 1` | three aliases; `var gsx` intact |
| 11 | component + `import context "mypkg"` used | three aliases + `context "mypkg"` |
| 12 | component + `var io = 1` | three aliases |
| 13 | numeric interp + `var strconv = 1` | three aliases + `_gsxsc` |
| 14 | element literal in `var`, no component | `_gsxrt`, `_gsxctx`, `_gsxio` |
| 15 | `component Foo(gsx string)` | legal now — pins the deleted stopgap |

Unit tests: blank (`_`) and dot (`.`) imports of the gsx path; a file-scope decl
named `_gsxrt` rejected by the extended reserved-prefix check.

## Blast radius

- **269 corpus `generated.x.go.golden` sections** (270 once PR #48 lands) —
  mechanical `go test ./internal/corpus -run TestCorpus -update`, then verify
  without `-update`. Also rewrites `coverage.golden`.
- **5 codegen unit tests** asserting literal `"gsx."` in output — hand-fix.
- `examples/*.txtar` pin `render.golden`, not generated code — unaffected.
- Docs `_generated/**` snippets show `.gsx` source, not `.x.go` — unaffected.
- Deletions: `emittedImportIdent`, its `checkReservedParams` branch, the three
  `boundNames` seeds, the `imports` constant seed.

Generated code becomes less pretty (`_gsxrt.Func` over `gsx.Func`). Accepted: it
is generated, `//line`-mapped, marked `DO NOT EDIT`, and the roadmap already
chose this trade.

## Risks

1. **D4 surfaces pre-existing corpus breakage.** Likely, and the point. Triage
   explicitly; a quarantined case needs a recorded reason.
2. **A user Go chunk referencing `_gsx*`.** Newly rejected by the extended
   reserved-prefix check. Previously it would have miscompiled silently, so this
   converts a latent break into a clean diagnostic.
3. **Golden regeneration hides a real regression.** Mitigate: regenerate in a
   commit that touches *only* goldens, and review its diff for anything other
   than `gsx.`→`_gsxrt.`, `context.`→`_gsxctx.`, `io.`→`_gsxio.`,
   `strconv.`→`_gsxsc.`, and removed import blocks.
4. **`//line` column accuracy.** `column_accuracy_test.go` pins `.gsx` positions,
   which are unaffected by generated-side identifier width. Verify, don't assume.

## Sequencing

Independent of, and after, the `nextTopLevelComponent` prose-scanning fix
(PR #48) — unrelated in mechanism, though both surfaced from the same
`var X = <div>…</div>` report.

Feature work in a git worktree per repo convention.
