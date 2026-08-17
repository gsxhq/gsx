# Module symbol graph — references and definition for every Go symbol, module-wide

**Status:** approved design, 2026-08-17
**Supersedes:** the component-only `CrossIndex`/`NavIndex` cross-boundary index

## Problem

Find-references and go-to-definition across the `.go`↔`.gsx` boundary only
know about *components*. `buildCrossNav` (`internal/codegen/crossnav.go`)
filters `types.Info` down to component funcs (`objKey`), so:

- `gr` on `type Smoke struct{}` declared in `smoke.gsx` → nothing.
- `gd` in `pages.go` on `Home` (a type declared in `home.gsx`) → `null`; the
  editor falls through to gopls and lands in `home.x.go`.
- `gd` from any `.go` file in a package that has no `.gsx` files (e.g. a `main`
  package importing `pages`) → `null`, because `handleGoDefinition` only knows
  packages that were analyzed as gsx packages.
- `gr` from a `<Card/>` tag cursor is "deferred" (`references.go`) because
  the old index only had approximate `//line`-derived tag columns.

Meanwhile a general, exact per-package symbol index already exists:
`sourceintel.Index` records every `Defs`/`Uses` identifier of a package's
`.gsx` skeleton files with exact authored byte spans, and it is what
go-to-definition *from* `.gsx` reads today. Two indexes — one partial — is
the root cause. This design keeps the general one and deletes the partial one.

## Goals

- gopls parity for symbols authored in `.gsx`: every Go object declared or
  used in a `.gsx` (package-level types/funcs/vars/consts, methods, fields,
  params, type params, locals) and every component participates in
  references and definition, from a `.gsx` **or** `.go` cursor.
- Reach: the whole module, including `.go` packages that contain no `.gsx`
  files but (transitively) import gsx packages.
- One index. `CrossRef`, `NavRef`, `CrossIndex`, `NavIndex`, `buildCrossNav`,
  `addLocalComponentCallRefs`, and the merge phases in `gen/lsp.go`
  `AnalyzeModule` are removed, not wrapped.
- Positions in the graph are exact; nothing approximate is admitted.

## Non-goals

- Rename beyond today's component-parameter rename (a later projection of
  this graph).
- Test files / test package variants (the shared world does not load them).
- Build-tag variant *loser* declarations of non-component Go objects (see §6).
- Hover/completion on `.go` buffers (gopls owns those).
- Suppressing duplicate results when gopls is also attached to `.go`
  buffers. Editors merge multi-server results; the user configures ordering.
- Go-to-definition from an embedded-field ident **authored in a `.gsx`**: the
  ident is both a field definition and a use of the embedded type, and
  `Index.At`'s tie-break prefers the definition, so the jump is a no-op. The
  `.go` side is fixed (`SymbolGraph.UseAt`, consulted first by
  `handleGoDefinition`); the `.gsx` side keeps the pre-existing behavior
  because the whole `.gsx` cascade resolves through `Index.At`, whose tie-break
  hover, rename and completion also depend on.

## Design

### 1. Data model

**Per package** — `PackageResult.SourceIndex` (`*sourceintel.Index`) is
widened to cover every file the package type-check saw:

- authored `.gsx`: mapped through the skeleton `SourceMap` (unchanged);
- hand-written `.go` siblings: identity-mapped (`sourceintel.IdentitySourceMap`
  — one whole-file segment, all capabilities, `sourcePath == generated path`).

Every `Defs`/`Uses` identifier with an exact authored span becomes an
`Occurrence`; every defined object gets a definition span. No object-kind
filter.

Three **gsx-only reference edges** — occurrences the type checker cannot
produce — are added from facts codegen already publishes:

| authored site | target object | source of the fact |
|---|---|---|
| `<Tag/>`, `<pkg.Tag/>`, `<recv.Tag/>` tag name | the resolved target object — a package `*types.Func`, a package function `*types.Var`, or a concrete bound method | the **discovery pass**: `callSiteRegistry.records` (disposition `componentSitePlanned`) + `targetFacts[id].origin`, at `el.TagPos` + the local-name segment. Deliberately **not** `ComponentCalls`: positional planning is gated on `targetPlanningReady`, so one unrelated type error anywhere in the package would take every tag edge in it down (see §6) |
| `attr=` on a component call | the parameter's `*types.Var` | `ComponentCalls[el].Params[attr].Var` + attr name pos. This one legitimately follows the plan: *which* parameter an attribute binds IS the plan's answer, so with planning skipped the edge goes quiet rather than guessing |
| `\|> name` pipe filter | the filter's `*types.Func` | structural: walk the lowered skeleton expression (`ExprMap`) stage by stage (`internal/pipeshape`, the same walk the LSP's pipe definition uses); span = the authored `PipeStage.NamePos`+len(Name). The skeleton emits `alias.Func(`, whose bytes differ from the authored name, so byte-identical mapping is impossible by construction |

These are ordinary `IdentifierUse` occurrences on the same objects.

**Stable object identity** — `sourceintel.ObjectKey`:

- within a package: `Origin(obj)` pointer identity, valid only against that
  package's own `types.Info`;
- across packages: `(pkg import path, objectpath.Path)`. `objectpath.For`
  failing means the object is not addressable from outside its package
  (local, unexported-unreachable), which is exactly the set that cannot be
  referenced cross-package, so it needs no cross-package key.

**Module graph** — `codegen.SymbolGraph`: for each key,
`{Definitions []sourceintel.Span, References []sourceintel.Span}`, built by
merging every package's index. Cross-package uses (an occurrence whose
object's package differs from the indexing package) are attached to the
declaring package's key by `(path, objectpath)`; intra-package occurrences
by pointer. Component call sites and param attrs are just more references on
the component func / param var keys.

### 2. Coverage — reverse-dependency Go packages

The Module currently type-checks only gsx dirs (with sibling `.go`). To
reach `.go`-only packages, the Module gains **reverse-dependency package
analysis**:

- Candidate set: every package in the shared world's `./...` load whose
  transitive imports include a gsx dir. The world already carries the import
  graph (`NeedImports|NeedDeps`).
- File list: the world package's `CompiledGoFiles` (cmd/go remains the
  build-tag/cgo authority). Bytes: the manifest source view, so `.go` editor
  overrides apply. Parsed into the Module fset.
- Type-check: `go/types` with the existing `moduleImporter` — gsx dirs
  resolve to their skeleton-checked `*types.Package`, everything else to the
  world. **No additional `packages.Load`.**
- Errors are collected, not fatal; whatever `Info.Uses`/`Defs` resolved is
  indexed (identity-mapped, like sibling `.go`).
- Result cached per dir. Invalidation joins the existing reverse closure:
  the dir's imports are recorded (`recordImports`) so a change in a gsx dir
  it depends on invalidates it; a `.go` change inside the dir invalidates it
  directly (`RefreshDisk`/`SetOverride`/`ClearOverride` on a `.go` path).
- Built lazily on the first request that needs the module graph and reused
  until invalidated.

### 3. LSP consumers

`Analyzer.AnalyzeModule` returns `*SymbolGraph` (was `[]CrossRef`). The
server caches it exactly as `moduleRefs`/`moduleRefsValid` are cached today
(invalidated by any document mutation or watched change).

- **references** — cursor in `.gsx` or `.go` (any module dir): occurrence at
  `(path, offset)` from the per-package index (or the graph's per-file
  occurrence table for reverse-dep dirs) → key → all definitions (when
  `includeDeclaration`) + references, module-wide. The tag-cursor deferral
  is removed.
- **definition from `.go`** — any dir in the module: occurrence → key →
  definition spans. Uniform rule: *the graph answers with everything it
  knows* — `.gsx` and `.go` targets alike; no "only if declared in `.gsx`"
  branch.
- **definition/hover from `.gsx`** — unchanged (already reads the
  per-package index).
- Degradation: when the module graph is unavailable (analysis error) the
  per-package `SourceIndex` answers same-package requests, replacing the
  old `CrossIndex` fallback.

### 4. Error handling

- A gsx package that fails analysis is skipped from the graph (partial
  results, as today).
- A reverse-dep package with type errors contributes what resolved.
- Cursor on a file the graph does not know (outside the module, a `.x.go`,
  a test file): empty / `null`, never an error.
- Only exact SourceMap segments produce spans; approximate positions never
  enter the graph.

### 5. Deletions

`internal/codegen`: `CrossRef`, `NavRef`, `PackageResult.CrossIndex`,
`PackageResult.NavIndex`, `crossnav.go` (`buildCrossNav`,
`addLocalComponentCallRefs`, helpers), the `compByKey`/`objKey`
construction in `module_importer.go` where it exists only for the index.
`internal/lsp`: `CrossRef`, `NavRef`, `Package.CrossIndex`, `Package.NavIndex`,
`identifyCrossRef`, `posCoversCursor`-based matching in
`handleGoDefinition`. `gen/lsp.go`: `AnalyzeModule` phases 2–5 and the
`CrossIndex`/`NavIndex` adaptation.

### 6. Plan-time checks (must be resolved during planning, not skipped)

Resolved during planning (2026-08-17):
- Pipe names: byte-identical mapping impossible; structural walk (above).
- `attr=` position: `attr.Pos()` is the first byte of the attribute name
  (parser `attrStartPos`); length = `componentInputAttrName(attr)`.
- `objectpath.For`: succeeds for exported-reachable objects, unexported
  types + their members, params, fields, type params; fails for unexported
  package-level funcs/vars/consts (→ bare-name key, identical to what
  objectpath would emit) and locals (→ per-package ordinal). Generic
  instance uses return `Origin() != obj`; `Origin` first.
- Build-tag variant *loser* declarations get no `types.Object` at all
  (go/types drops redeclarations), so component variant declaration spans
  enter as extra definition occurrences from `ComponentDecls`; loser
  declarations of other Go objects are not indexed (limitation).
- Split public/body component funcs: the body func and its params are
  canonicalised onto the public declaration's objects at index build
  (`BuildOptions.Canonical`), so tag sites, attr bindings, Go callers and
  body uses share one key.

Found during implementation (2026-08-17), **fixed**:
- **The tag edge must not depend on positional planning.** Sourcing it from
  `ComponentCalls` made it a hostage of `targetPlanningReady`
  (`module_importer.go`): any type error outside a component-target marker
  skips `planComponentPositionalCalls` for the WHOLE package, so
  `ComponentCalls` is empty and every `<Tag/>` in it stopped answering `gd`
  and `gr` — precisely mid-edit, the state an editor spends its time in. A
  live probe confirmed the regression against `main`, whose AST-derived
  index answered through it. The fix projects the edge from the **discovery
  pass** instead (`discoverComponentTargets` + `finalizeComponentIdentity`,
  both outside that gate, the same pair that stamps `Element.IsComponent`),
  reading `targetFacts[record.id].origin`. That covers every accepted
  provenance — package func, package function variable, concrete bound
  method — with no per-shape resolver and no name-resolution guesswork, and
  it is the same plan-free records+facts projection
  `componentTargetQualifiers` already does for unused-import analysis.
  Agreement with `ComponentCalls[el].Target` on a cleanly-planning package is
  asserted per tag shape in `TestPackageSymbolIndex`.

## Testing

- `internal/sourceintel`: identity SourceMap; `ObjectKey` stability across
  two independent type-checks of the same package; graph merge.
- `internal/codegen`: extend an **existing** Module-backed fixture (a new
  Module costs ~0.3 s forever — see CLAUDE.md test-performance rule) with a
  table covering: `.gsx`-declared type / func / var / const / method /
  field / param / type param / local; refs from a sibling `.go`, from
  another gsx package, from a non-gsx reverse-dep `main` package, from
  `<Tag/>`, `<pkg.Tag/>`, `attr=`, `|> pipe`; build-tag variants;
  invalidation after a `.go` edit in the reverse-dep dir and after a `.gsx`
  edit in a dependency.
- `internal/lsp`: references + definition matrix over the same fixture,
  `.gsx` and `.go` cursors, `includeDeclaration` on/off; a stdio probe
  reproducing the original template-repo cases (`gr` on `Smoke`, `gd` on
  `Home` in `pages.go`, `gd` from a `main.go` on `pages.Smoke`).
- Independent adversarial review with live probes before merge.
