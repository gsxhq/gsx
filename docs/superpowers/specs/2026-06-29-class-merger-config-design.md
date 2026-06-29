# Class Merger as Configuration — Design

- **Date:** 2026-06-29
- **Status:** Draft (awaiting review)
- **Topic:** Make the class-merge strategy a declarative extension (gen option + `gsx.toml`), emitted by codegen, with no runtime global.

## Problem

gsx composes `class` attributes from static strings, `clsx`-style toggles, and
caller fallthrough, then runs the flattened tokens through a *merge strategy*.
The default is last-wins dedup. The intended real-world strategy is a
Tailwind-aware merge (e.g. `github.com/jackielii/tailwind-merge-go`) that
collapses conflicting utilities (`px-4 px-8` → `px-8`).

Today the only seam is a **mutable runtime global**, `gsx.ClassMerger`
(`class.go:22`), which an app overwrites in `init()`. This was always a
placeholder. It has three problems:

1. **Process-global, last-init-wins.** An app's merger silently rewrites the
   classes of any imported library whose components were generated expecting a
   different (or no) merger. Wrong output, no clean fix.
2. **Hand-written runtime extension work.** The user must remember to install it;
   gen knows nothing about it (no cache-key participation, no validation).
3. **It contradicts the project's config model.** Extensions belong in
   `gen.Main` options or `gsx.toml`, not in runtime globals.

## Goals

- Declare the class merger two ways, both gen-driven, mirroring `filters`:
  - **Option route** (custom binary / `gen.Main`): `gen.WithClassMerger(fn)`.
  - **Config route** (data): `class_merger = "<pkgPath>.<Func>"` in `gsx.toml`.
- **Runtime does no extension work.** No mutable global; the merger is a value
  threaded into the runtime helpers by generated code. The stdlib-only runtime
  keeps only the built-in default.
- **Per-module scope.** Each generated `.x.go` bakes in the merger it was
  generated with. A library keeps its merger; the app keeps its own.
- Keep the runtime root **dependency-free**; the third-party merger appears only
  in the user's generated code and `go.mod`.
- **Keep the merger entirely out of the gsx runtime path.** The merger function,
  its custom configuration, and its dependency version all live in the user's own
  package + `go.mod`. gsx's runtime has no compile-time coupling to any merger
  library, so a user upgrades (or swaps) `tailwind-merge-go` with a plain `go.mod`
  bump and a regenerate — no gsx release required.
- Support **custom-configured** mergers (e.g. `pkg/twmerge` with a custom
  `Config`/cache/prefix), not just the default top-level `Merge`.
- Ship docs (`docs/guide/config.md`) and a runnable example wiring
  `tailwind-merge-go`.

## Non-goals

- Per-call or per-component merger selection. One merger per project.
- Runtime-swappable merger / ad-hoc override. Explicitly dropped — a global
  "doesn't cut it" and isn't required (tests configure via the gen option).
- A gsx-shipped Tailwind merger implementation. We only provide the seam; the
  merger is a user dependency.
- An env override (`GSX_CLASS_MERGER`). The merge strategy does not vary
  dev↔prod, so it stays option+config only. (Revisit only if a real dev/prod
  divergence appears.)

## Chosen approach: thread the merger, drop the global (Approach A)

Considered alternatives:

- **B — keep the global, gen-emit an `init()` that sets it.** Smaller change,
  but retains the process-global semantics (library-leak bug) and an
  install-ordering wart (which generated package carries the `init()`?). Rejected
  on correctness grounds.
- **C — fully inline the merge at each call site.** Duplicates non-trivial token
  assembly (conditional parts, flatten, dedup) into every site. Collapses into A
  once you keep the runtime helpers for assembly. Rejected.

A is correct-by-construction (per-module scope, no ordering, no global) and
matches the stated principle. Its cost is changed runtime helper signatures and
a one-time golden regeneration.

### Configuration surface

A single resolved value (unlike `filters`, which is a list): the merger is one
function reference, or absent.

The config value names an **exported package-level identifier** that is callable
as the merger (validated by go/types — see contract below). The identifier may be
either a **func declaration** (`func Merge([]string) string`) or a **package-level
var of func type** (`var Merge twmerge.TwMergeFn`). Both emit identically as
`pkg.Merge(<tokens, adapted>)`.

- `gsx.toml`: a new top-level key

  ```toml
  class_merger = "github.com/jackielii/tailwind-merge-go.Merge"  # func or var ref
  ```

  Parsed by the existing `splitPkgFunc`, identical to a filter alias value. Added
  to `tomlConfig` as `ClassMerger string \`toml:"class_merger"\``; strict decode
  still rejects typos. This route accepts **func or var** identifiers.
- `gen.WithClassMerger(fn any) Option` — `fn` is a function *value* (e.g.
  `twmerge.Merge`, or a wrapper func). Resolved to `(pkgPath, funcName)` via the
  existing `resolveFilterFunc` (`runtime.FuncForPC` → `splitPkgFunc`). Reflection
  can only recover a stable name for a **top-level func declaration** — a closure
  / runtime-constructed `TwMergeFn` value (e.g. the result of
  `twmerge.CreateTwMerge(...)`) resolves to tailwind's internals, not the user's
  symbol, and is rejected with a clear error pointing at the wrapper-func idiom
  (below) or the `gsx.toml` string form. Same `WithFilter` rejection rules
  otherwise (no method value, unexported target).
- **Precedence:** `option > config` (the standard layer model). If both are set,
  the option wins. Stored on the resolved `config` as a single
  `*codegen.ClassMergerRef{PkgPath, FuncName}` (nil ⇒ default).
- **Cache key:** the resolved `(pkgPath, funcName)` MUST fold into `computeKey`
  (`gen/cachekey.go`) — it changes generated output. Absent merger contributes a
  stable empty marker.

### Signature contract and adapter emission

The runtime seam is `func([]string) string`. The configured merger is validated
and adapted at **generate time** via `go/types` (the same package-loading the
filter harvest already performs), never guessed:

- **Native** `func([]string) string` → emitted by direct reference, no adapter:
  `mypkg.Merge`.
- **Variadic `...T` where `[]string` is assignable to a single `T`** (covers
  Tailwind's `func(...ClassNameValue) string` where `ClassNameValue = any`, and
  `func(...any) string`) → adapter calls with the slice as one argument:
  `func(t []string) string { return mypkg.Merge(t) }`.
- **Variadic `...string`** → adapter spreads:
  `func(t []string) string { return mypkg.Merge(t...) }`.
- Anything else (wrong arity, non-string return, not callable with tokens) →
  **generate-time error** naming the configured ref and the required contract.
  No silent fallback.

The adapter, when needed, is emitted **once per generated file** as an unexported
package-level func (e.g. `_gsxClassMerge`). When no adapter is needed the merger
is referenced directly. The merger package is imported under a **reserved alias**
(e.g. `_gsxcm`), reusing the filter-import alias machinery (`writeImports`,
collision handling with user/std/filter imports).

#### Custom-configured mergers: the wrapper idiom

A custom Tailwind merger from `pkg/twmerge` is a **runtime-constructed value**,
not a named function:

```go
var merger = twmerge.CreateTwMerge(twmerge.GetDefaultConfig(), twmerge.WithCache(myCache))
// or twmerge.ExtendTailwindMerge(&twmerge.ConfigExtension{Prefix: ptr("tw-"), ...})
```

Such a closure has no stable package-qualified name, so the recommended idiom —
which `Goals` mandates keeping out of the runtime path — is a **thin top-level
wrapper in the user's own utilities / filter-like package**, presenting gsx's
canonical seam:

```go
// package myapp/twcfg
var merger = twmerge.CreateTwMerge(twmerge.GetDefaultConfig(), twmerge.WithCache(myCache))

// Merge is what gsx names (gsx.toml or gen.WithClassMerger). Canonical signature.
func Merge(classes []string) string { return merger(classes) }
```

```toml
class_merger = "myapp/twcfg.Merge"
```

Because the wrapper already has signature `func([]string) string`, gsx emits a
**direct reference with no adapter**, and the wrapper is a real top-level func so
**both** the config and option routes resolve it. The merger library, its custom
config, its cache, and its version all live in `myapp/twcfg` + the user's
`go.mod` — gsx neither imports nor pins it, so upgrades are a user-side
`go.mod`/regenerate, never a gsx release. (Validated: a `CreateTwMerge`-built
wrapper merges `px-4 px-8` → `px-8` through gsx's seam.)

### Runtime API changes (`class.go`, `attrs.go`)

- **Remove** the mutable global `var ClassMerger` and the package-private
  `defaultClassMerge` indirection.
- **Export** the default as `func DefaultClassMerge(tokens []string) string`
  (today's last-wins dedup, unchanged behavior). Stdlib-only.
- **Thread a `merge func([]string) string` parameter** into the helpers that
  currently read the global:
  - `ClassString(merge, parts...) string`
  - `(*Writer) Class(merge, parts...)`
  - `(*Writer) ClassMerged(merge, extra, parts...)`
  - (uniform: every class-writing helper takes the merger as its first argument)
- **`Attrs.Class()` stops merging internally.** It returns the *raw* joined class
  string from the bag (no dedup). The single outer codegen-emitted site applies
  the configured merger exactly once. This:
  - removes today's hidden double-merge,
  - keeps `Attrs.Class()` a zero-arg method, so user-facing `{ attrs.Class() }`
    interpolations are unaffected by the API change.
- Codegen **always** passes a merger explicitly — `gsx.DefaultClassMerge` when
  none is configured, the adapter/reference otherwise. (Decision: uniform
  threading over a separate `ClassWith` variant. Costs a one-time golden regen;
  keeps the runtime class API to one method each. Revisit at review if the
  corpus churn is judged not worth it.)

### Codegen changes (`internal/codegen`)

The four emit sites that produce class calls
(`emit.go`: `emitRootComposedClass`, `emitRootStaticClass`, `emitClassAttr`,
`emitSpread`, plus the `ClassString` interp builder ~line 2222) prepend the
merger argument. New `codegen.Options.ClassMerger *ClassMergerRef`; threaded from
`gen` like `FilterPkgs`/aliases. When set, register the reserved import alias and
(if needed) emit the adapter func per file.

### Generated code: before / after

Source:

```gsx
component Card() { <section class="card">{children}</section> }
```

**Today:**

```go
_gsxgw.Class(gsx.Class("card"), gsx.Class(_gsxp.Attrs.Class()))
```

**Approach A, no merger configured:**

```go
_gsxgw.Class(gsx.DefaultClassMerge, gsx.Class("card"), gsx.Class(_gsxp.Attrs.Class()))
```

**Approach A, `class_merger = "github.com/jackielii/tailwind-merge-go.Merge"`:**

```go
import (
	"context"
	"io"
	"github.com/gsxhq/gsx"
	_gsxcm "github.com/jackielii/tailwind-merge-go"
)

func _gsxClassMerge(_gsxtokens []string) string { return _gsxcm.Merge(_gsxtokens) }

// ...
_gsxgw.Class(_gsxClassMerge, gsx.Class("card"), gsx.Class(_gsxp.Attrs.Class()))
```

**Approach A, custom wrapper `class_merger = "myapp/twcfg.Merge"`** (native
`func([]string) string` ⇒ no adapter, direct reference):

```go
import (
	"context"
	"io"
	"github.com/gsxhq/gsx"
	_gsxcm "myapp/twcfg"
)

// ...
_gsxgw.Class(_gsxcm.Merge, gsx.Class("card"), gsx.Class(_gsxp.Attrs.Class()))
```

## Testing

- **Runtime unit tests** (root `gsx`): `DefaultClassMerge` behavior;
  `Attrs.Class()` returns raw join (no merge); helpers apply the passed merger;
  `ClassMerged` empty-set no-op preserved. Replace the existing
  `TestClassMergerOverride` (global-swap) with a passed-merger test.
- **Codegen unit tests** (`internal/codegen`): emit sites prepend the merger;
  adapter emission per signature class (native / `...any` / `...string`);
  import-alias collision handling; the generate-time error for a non-conforming
  merger signature and for an unresolvable ref.
- **Corpus case(s)** (canonical, per CLAUDE.md). Needs harness support:
  - Add `ClassMerger` to the corpus `codegen.Options` (`internal/corpus/codegen.go`)
    and a per-case way to set it (a case directive or a `gsx.toml` section the
    harness reads).
  - Use a **case-local merger package** (a small in-repo `func Merge([]string) string`,
    leveraging existing multi-package case support + import rewriting) so the
    repo stays dependency-free. The case pins `generated.x.go.golden` (import
    alias + adapter + threaded calls) and `render.golden` (merge behavior).
  - Cover the contexts where class merge appears: composable `class={…}`, static
    root class + fallthrough, and `Attrs.Class()` interpolation.
- **Runnable example** (real `tailwind-merge-go`): self-contained module (own
  `go.mod`, can carry the dep) under `examples/tailwind-merge/`. It demonstrates a
  **custom-configured merger via the wrapper idiom** (a `twcfg` package with a
  `pkg/twmerge.CreateTwMerge`/`ExtendTailwindMerge` instance behind a
  `func Merge([]string) string`), referenced from `gsx.toml` as
  `"<module>/twcfg.Merge"`. Includes the component, committed generated `.x.go`
  (showing the direct, adapter-free reference), and a test asserting Tailwind
  merge (`px-4 px-8` → `px-8`). Ensure `make ci` covers it without breaking the
  txtar examples-drift check (placement/CI wiring is an implementation detail to
  settle in the plan).

## Docs

- `docs/guide/config.md`: document `class_merger` — value form (func or var
  ref), precedence, what it emits, the signature contract, the **custom-config
  wrapper idiom**, and that the dep + its version live in the user's `go.mod`
  (upgrade = user-side bump, no gsx release).
- Cross-reference from the class/attribute guide where merge behavior is
  described.

## Breaking changes / migration

Pre-1.0. Breaking:

- `gsx.ClassMerger` global removed. Apps that swapped it migrate to
  `class_merger` in `gsx.toml` (or `gen.WithClassMerger` for a custom binary).
- Runtime helper signatures (`Class`, `ClassMerged`, `ClassString`) gain a
  leading merger parameter. These are primarily called by generated code;
  regeneration handles the change. Hand callers (rare) update the call.
- All goldens regenerate (the default case gains `gsx.DefaultClassMerge`).

## Open questions

1. **Uniform threading vs `ClassWith` variant** — recorded as uniform; confirm at
   review (trades golden churn for a smaller runtime API).
2. **Runnable example placement & CI wiring** — `examples/tailwind-merge/` as its
   own module; confirm it integrates with `make ci` cleanly.
3. **Corpus per-case merger mechanism** — case directive vs a real `gsx.toml`
   read by the harness; pick the lower-friction option in the plan.
