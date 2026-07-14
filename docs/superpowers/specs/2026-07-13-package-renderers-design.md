# Package renderers — module-local `.gsx` renderer functions

**Date:** 2026-07-13
**Status:** Approved design; pending implementation plan

## Problem

The `[renderers]` registry resolves every configured target from compiled Go
package types before it analyzes the package being generated. That works when a
renderer is already present in a hand-written `.go` file or an existing generated
`.x.go`, but it cannot bootstrap a renderer implemented in `.gsx`:

```gsx
package renderers

import (
	"github.com/gsxhq/gsx"
	"github.com/jackc/pgx/v5/pgtype"
)

func Timestamptz(v pgtype.Timestamptz) gsx.Node {
	return <time></time>
}
```

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Timestamptz" =
  "github.com/tespkg/one-learning/ds/renderers.Timestamptz"
```

On a clean checkout, `go tool gsx generate ./ds/renderers` fails with:

```text
renderer for "github.com/jackc/pgx/v5/pgtype.Timestamptz":
func "Timestamptz" not found in package
"github.com/tespkg/one-learning/ds/renderers"
```

The function exists in the `.gsx` source, but its `.x.go` declaration is the
output of the generation that renderer resolution currently blocks. Writing a
Go shim or checking in generated output would only hide the compiler lifecycle
defect.

There is a second same-package problem after resolution: the current renderer
entry always carries an import alias. Generating the renderer package itself
must emit `Timestamptz(v)`, not import its own package and emit
`_gsxfN.Timestamptz(v)`.

## Decision

Support renderer functions implemented in any `.gsx` package in the active Go
module. Resolve their declarations from in-memory package skeletons before normal
analysis, then build the complete module-wide renderer table. This is independent
of requested-directory order and never writes an intermediate `.x.go`.

The ownership boundary is the active module:

- A renderer package outside the active module is **external**. It must expose a
  buildable Go declaration through ordinary `.go` source, including generated Go
  that its owning module has already produced. gsx does not inspect or generate
  another module's `.gsx` source.
- A module-local package containing build-active `.gsx` files is resolved through
  gsx's in-memory declaration overlay. Hand-written `.go` companions in that
  package participate in the same type-check.
- A module-local Go-only package keeps the existing `go/packages` path.

This is a source-model distinction, not a fallback heuristic. Module ownership,
package-directory mapping, build constraints, and active `.gsx` selection use the
same rules as normal module generation.

## Resolution lifecycle

### 1. Partition renderer packages

Before the first typed package analysis, classify each configured renderer target
as one of:

1. external or module-local Go-only; or
2. module-local with `.gsx` source.

The first group remains in the existing external package harvest. The second group
is removed from external renderer validation: a stale or absent `.x.go` must not
make its partial `go/packages` view authoritative.

The existing external importer remains the single `packages.Load`. Package
renderers must not add a second load.

### 2. Build declaration overlays for local `.gsx` packages

For each module-local renderer package, build an in-memory declaration view using
the existing parser, package source selection, FileSet, and module importer.

The view preserves the declarations that generated Go exposes:

- package name and imports;
- ordinary top-level Go declarations from `.gsx`;
- functions containing embedded elements or fragments, with their real parameter
  and result types;
- generated component/props declarations needed to type-check the package; and
- hand-written build-active `.go` companions.

Embedded markup bodies are replaced with typed `gsx.Node` placeholders for this
bootstrap pass. No renderer is applied and no markup-body semantic result is
accepted from the declaration pass. The purpose is only to let `go/types` resolve
real package declarations without requiring those declarations' generated bodies
to exist already.

This must be implemented as a focused declaration mode of the existing skeleton
pipeline, not by extracting type strings or constructing approximate signatures.
Imports, aliases, named types, pointers, build tags, and cross-file declarations
continue to be decided by Go syntax and `go/types`.

The configured target remains an ordinary top-level Go function. This feature does
not extend the renderer contract to `component` declarations.

### 3. Harvest and validate the complete registry

Harvest local targets from the declaration packages and external targets from the
existing Go package set. Then run the renderer contract checks once over the
complete, last-wins registry:

- the target exists and is a function;
- its parameter exactly matches the registered named or pointer type;
- its signature is one of the supported context/error shapes;
- its result is natively renderable; and
- its result does not have a registered renderer, so renderers still apply once
  and never chain.

Cross-package chain validation must see local and external entries together.

### 4. Build a renderer table for each consuming package

Renderer registrations remain module-wide, while import aliases remain
package-specific because filter-package overrides can affect reserved alias
allocation.

For a renderer whose package differs from the package being emitted, retain the
existing reserved alias and tracked import. For a renderer in the package being
emitted, mark the entry local: `applyRenderer` emits an unqualified function call
and does not add an import.

This local-call decision is derived from exact package identity, not path text in
an expression.

### 5. Run normal analysis and emission

After registry resolution, the ordinary skeleton, type-check, diagnostics, and
emitter run with the complete renderer table. This pass validates all renderer
markup bodies and applies renderers inside them exactly as it would in any other
`.gsx` source.

The bootstrap view is never emitted and never substitutes for full package
validation.

## Batch, watch, LSP, formatter, and bundle behavior

### Batch generation

A clean batched generation resolves all module-local package renderers before it
generates any requested directory. The result cannot depend on alphabetical or
caller-supplied directory ordering. The renderer directory need not have a
pre-existing `.x.go`.

Like any other imported GSX package, a renderer package is emitted only when it is
within the requested generation paths. Registry resolution does not broaden a
targeted command into generation of unrelated directories.

### Watch and LSP

Module-local renderer declarations are module-wide code-generation dependencies.
Changing one invalidates the resolved renderer registry and all retained package
analyses that may have classified output through it. The next analysis rebuilds
the declaration package and registry before emitting or returning diagnostics.

This applies equally to disk changes and LSP overrides. A stale renderer table may
not survive a local renderer edit.

The conservative module-wide invalidation is intentional: changing a renderer's
result type, context/error shape, or target declaration can change generated code
in any package that renders the registered type, even though the package never
mentions the renderer import in its source.

### Formatter

`gsx fmt` remains the syntactic fast lane. Formatting and syntactic unused-import
work do not need renderer result classification, so they must not bootstrap local
renderer declarations or add a package load. A clean package renderer must be
formattable before its first generation.

### Bundle / WASM

Bundle mode continues to consume the renderer table supplied by its prebuilt
bundle. It has no active filesystem module to inspect and therefore does not
bootstrap module-local `.gsx` renderer packages. Existing bundle behavior and its
zero-`packages.Load` guarantee remain unchanged.

## Diagnostics and failure semantics

Existing renderer diagnostics and contract wording remain authoritative.

- An external package whose renderer declaration is not available as buildable Go
  still reports package type-resolution or function-not-found failure. There is no
  cross-module `.gsx` fallback.
- A local declaration package reports the same missing target, non-function,
  parameter mismatch, signature, unsupported result, and chain diagnostics as an
  external package.
- Parse, import, or declaration type errors that prevent a trustworthy local
  package scope fail renderer resolution. Markup-body diagnostics are deferred to
  the normal full analysis, because markup is deliberately elided from the
  bootstrap view.
- Same-package emission never reports or creates an import cycle; it emits a
  direct call.

No handwritten shim, intermediate generation, disk staging, second
`packages.Load`, signature-string parser, or directory-order workaround is
permitted.

## Cache and performance

Renderer configuration remains part of the generation cache key. The codegen
output also depends on the source of a module-local renderer package, so cache-key
dependency discovery must include that package even when the consuming source has
no explicit Go import of it.

Within a warm `Module`, declaration packages and the completed renderer metadata
are cached. Ordinary warm regeneration reuses them. Editing a local renderer
package clears that metadata and the dependent package analyses; unrelated edits
retain the warm table.

The implementation must preserve:

- one external `packages.Load` per cold module;
- zero external reloads for an unrelated warm regeneration; and
- no typed load for `gsx fmt`.

## Documentation pattern

Add `docs/guide/patterns/package-renderers.md` as the recommended integration
pattern for third-party value types. The page will show a dedicated
`ds/renderers` package and a `pgtype.Timestamptz` renderer returning semantic
`<time>` markup.

The pattern owns application display policy explicitly:

- NULL and invalid-value behavior;
- timestamp and timezone formatting;
- pgx infinity modifiers;
- the machine-readable `datetime` value; and
- the human-readable label.

It will include:

- the package `.gsx` source;
- matching `[renderers]` configuration;
- `go tool gsx generate ./ds/renderers` as the clean first-generation command;
- an example of direct typed interpolation at a consuming site;
- the local-versus-external ownership boundary; and
- guidance to keep database/display policy in the application renderer package,
  not in gsx.

Link the page from `docs/guide/patterns.md` and the `[renderers]` section of
`docs/guide/config.md`. Update `docs/ROADMAP.md` to record local `.gsx` package
renderer bootstrapping and replace the pending pgx recipe with the shipped pattern.

## Testing

### Canonical corpus

Add a semantic corpus case whose `[renderers]` target is an ordinary function in
the same `input.gsx` package. The case must begin without generated Go and pin:

- declaration resolution succeeds;
- the function has shape `func(Timestamptz) gsx.Node`;
- generated code calls the renderer directly without importing its own package;
- interpolation of the registered type renders the returned `<time>` node; and
- `generated.x.go.golden`, `render.golden`, diagnostics, and coverage manifest are
  regenerated through the corpus update command, never hand-edited.

Add a cross-package corpus/integration fixture in which a renderer lives in one
module-local `.gsx` package and a consumer lives in another. Generate both from a
clean source tree in an order where the consumer cannot rely on the renderer's
output already being written. Pin the reserved import alias in the consumer and
the direct local call in the renderer package.

### Focused integration and regression tests

Cover:

- a local mixed package whose renderer target is in a hand-written `.go` companion;
- an external renderer continuing to resolve only from buildable Go;
- missing and malformed local targets retaining existing diagnostics;
- global chain validation spanning local and external registrations;
- formatter operation with no renderer `.x.go`;
- LSP/disk override invalidation of a local renderer declaration;
- watch regeneration of affected packages after a renderer signature change;
- cache dependency changes when local renderer source changes; and
- cold/warm external-load counters preserving the performance invariants.

### Verification gates

Run focused codegen, corpus, gen/watch, LSP, and formatter tests first. Run
`gopls check -severity=hint` on changed Go files, then `make check`, `make lint`,
and authoritative `make ci`.

Because the change touches `docs/guide/**`, also run the VitePress docs build using
the sibling `gsxhq.github.io` source-of-truth workflow. Literal `{{ }}` in prose
must remain inside `v-pre` blocks.

## Out of scope

- Bootstrapping `.gsx` source owned by another Go module.
- Automatically generating renderer directories outside the command's requested
  paths.
- Runtime renderer registration or reflection.
- Renderer chaining or runtime-`any` dispatch.
- Making component declarations renderer targets.
- Changing renderer matching, escaping, sanitization, or context semantics.
- Tree-sitter, editor grammar, or CodeMirror changes; this adds no syntax.
