# Fold CachedResolver into the Module: one generation driver

**Date:** 2026-06-28
**Status:** Approved — ready for implementation plan

## Goal

Collapse the two parallel codegen drivers into one. Today the warm `Module`
(`Module.Generate`/`Package`) and the in-process WASM path
(`GeneratePackagesWithResolver`) are independent reimplementations of the same
pipeline — parse `.gsx` → build skeleton → type-check → harvest → emit. Make the
`Module` the single driver, with its external importer and filter table
*pluggable*: loaded via `go list` by default, injected from a prebuilt bundle for
WASM (where the browser has no `go list`). Then delete the parallel driver and the
parallel type-check entirely.

This is the launch-grade "no debt" cleanup: an open-source reviewer reading
`internal/codegen` should find exactly one generation driver and one skeleton
type-check, not two.

## Background — why two paths exist today

- **`Module.analyze`** (`module_importer.go`) is the warm path. It parses `.gsx`
  into the module-shared `m.fset`, builds skeletons, and type-checks them via
  `checkSkeletonPackage` against a `moduleImporter`. The `moduleImporter` wraps an
  external importer (`m.ext`, a `mapImporter` built by `externalImporter()`'s
  `packages.Load`) and routes project gsx packages to skeleton-checking. The filter
  table comes from `cachedFilterTable()` (another `packages.Load`).

- **`GeneratePackagesWithResolver`** (`resolver.go`) is the WASM/in-process path.
  It uses a fresh `token.NewFileSet()` per dir and calls
  `resolveTypesPkgWithFilters(..., resolver)` where `resolver` is a
  `*CachedResolver`. `CachedResolver.check` parses the overlay + hand-written `.go`
  and runs `go/types` against a prebuilt `mapImporter` (the bundle). The filter
  table is prebuilt in the bundle.

The two paths share leaf helpers (`buildSkeleton`, `componentPropFieldsFor`,
`harvest`, `generateFile`) but duplicate the *driver loop* and the *skeleton
type-check*.

### The crux that makes the fold tractable

The `Module` already operates **override-only** by construction:

- `parsePackageWithFset` globs `dir/*.gsx` (returns nothing in a browser — no disk)
  then unions in any `overrides` keys under `dir`.
- `analyze`'s hand-written-`.go` collection uses `build.ImportDir(dir, 0)`, which
  errors with no disk and simply adds nothing.
- `isGsxPackage` globs disk then falls back to checking `overrides`.
- `currentSource`/`source` try `overrides` first, then `os.ReadFile` (a graceful
  not-found in a browser).

The **only** two operations that force a separate WASM driver are
`externalImporter()` and `cachedFilterTable()` — both call `packages.Load`. Inject
those two from a prebuilt bundle and the `Module` *is* the WASM path.

## Design

### 1. `CachedResolver` → passive bundle (`Bundle`)

Rename the internal `codegen.CachedResolver` to `codegen.Bundle` and strip its
`check` method. It becomes passive data:

```go
// Bundle carries a prebuilt external importer and filter table so the Module
// can type-check skeletons without any `go list`/packages.Load subprocess. WASM
// (browser) and other no-toolchain callers build a Bundle once and inject it via
// Options.Bundle. The zero value is invalid; use NewBundle/NewBundleFromTypes.
type Bundle struct {
	imp   types.Importer
	table filterTable
}

func (b *Bundle) importer() types.Importer { return b.imp }
func (b *Bundle) filters() filterTable     { return b.table }
```

Constructors:
- `newBundle(moduleDir, filterPkgs, aliases, allowImports)` — the renamed
  `newCachedResolver` (loads runtime + filter pkgs + allow-imports once via
  `packages.Load`, builds the `mapImporter` + filter table).
- Public `NewCachedResolver(...)` and `NewCachedResolverFromTypes(...)` **keep their
  names and signatures** (the `gen` package and the playground depend on them) but
  now return `*Bundle`.

`mapImporter` is unchanged and reused.

### 2. `Options.Bundle` and pluggable load

```go
type Options struct {
	// ... existing fields ...
	Bundle *Bundle // when non-nil: inject importer+filter table, skip all go list.
}
```

- `externalImporter()`: when `m.opts.Bundle != nil`, return `m.opts.Bundle.importer()`
  directly (do not touch `m.ext`, do not call `packages.Load`, do not set
  `fsetBaseline`). Otherwise unchanged.
- `cachedFilterTable()`: when `m.opts.Bundle != nil`, return
  `m.opts.Bundle.filters()` directly (do not call `loadFilterTableMulti`).
  Otherwise unchanged.

Returning the bundle directly (rather than caching into `m.ext`/`m.filterTbl`)
means `rebuildFset`'s reset of those fields is harmless in bundle mode, and a
long-lived bundle Module's fset growth is still bounded by the existing
`maybeRebuildFset` mechanism (rebuild drops `pkgTypes` and re-checks skeletons
against the same injected bundle).

**Constraint (documented in code):** Bundle mode is **generation-only**. The
bundle's `*types.Package` values were loaded in a foreign `FileSet`, so imported
objects' positions do not resolve against `m.fset`. `Module.Generate` needs only
types, not cross-package positions, so this is correct for WASM. `Module.Package`
(the rich LSP result with cross/nav indexes) MUST NOT be used in bundle mode; the
constraint is documented on `Options.Bundle` and on `Package`. WASM only calls
`Generate`, so nothing violates it.

### 3. WASM entry point via the Module

`gen/resolver.go`'s `generateInProcess` is rewritten to drive the Module:

```go
func generateInProcess(b *codegen.Bundle, dir string, srcOverride map[string][]byte) (Result, error) {
	absDir, _ := filepath.Abs(dir)
	m, err := codegen.Open(codegen.Options{
		ModuleRoot: absDir,           // single playground package; root == dir
		ModulePath: "play",           // synthetic; no cross-package resolution in bundle mode
		FilterPkgs: []string{codegen.StdImportPath},
		Bundle:     b,
	})
	if err != nil { return Result{}, err }
	for p, src := range srcOverride { // abs .gsx path -> source
		m.SetOverride(absPath(p), src)
	}
	out, diags, err := m.Generate(absDir)
	// map out (.gsx-keyed) + diags into gen.Result, exactly as today.
}
```

A **fresh Module per call** keeps the in-process path stateless (matching the
current `GeneratePackagesWithResolver` semantics) and cheap (the expensive
`packages.Load` already happened once when the bundle was built). The exact
`ModulePath`/override-keying details are settled during planning against
`gen/resolver.go`'s current `Result` mapping; the binding requirement is that the
in-process path produces byte-identical generated output and the same diagnostics
as today.

### 4. Deletions

After 1–3, delete:
- `GeneratePackagesWithResolver` (the parallel driver loop).
- `resolveTypesPkgWithFilters`, `resolveTypesPkg` (the parallel type-check).
- `packagesLoadResolver`, the `typeResolver` interface, `CachedResolver.check`,
  `cachedTypeErrors` (its positioned-type-error carrier — the Module already
  surfaces type errors via `checkSkeletonPackage`'s `typeErrs`, added to the bag in
  `analyze`).

Only two tests reference these: `analyze_test.go` (`resolveTypesPkg`) and
`resolver_test.go` (`cached.check`). Both migrate onto the Module
(`Open`+`SetOverride`+`Generate`, or `Package` for the type-map assertions). No
production code outside `resolver.go`/`gen/resolver.go` uses them.

### 5. Bundled "all debt" cleanup (same branch, separate commits)

These ride along because the user asked for a fully clean launch; each is its own
reviewable commit:

- **Diagnostic consistency.** `Module.Package`'s emit-for-diagnostics loop
  (`module.go`) runs `generateFile` unconditionally, while `Module.Generate` gates
  it on `len(a.typeErrs)==0`. Gate `Package` the same way, so the LSP stops showing
  spurious secondary diagnostics (e.g. "could not resolve type of interpolation")
  on packages that already have a type error. The type-error diagnostics themselves
  are unaffected (added in `analyze`).
- **Doc-rot.** Remove stale comments referencing the deleted batch path / the old
  resolver path ("matching the batch path's behavior", etc.) and rename the
  internal `codegenGeneratePackages` symbol in `internal/corpus` to reflect it
  drives `GenerateDirs`.
- **Dead parameters.** Remove unused params flagged by gopls: `gen/cache.go` `dir`,
  `gen/init.go` `force`, `emit.go` `fset` (in `emitCSSInterp`/`emitJSInterp`) and
  `imports` (in `emitJSValue`) — only where removal does not break a deliberate
  signature family; where a param exists for signature symmetry across sibling emit
  functions and removing it would split the family, leave it and note why.
- **Modernize polish.** Apply gopls modernize hints in **production** files only
  (`maps.Copy`, `slices.Contains`, `min`/`max`, `strings.CutPrefix`). Test-file
  modernize hints are out of scope (style noise, not debt).

## Error handling

- Bundle-mode type errors surface exactly as in the go-list path: `analyze` records
  `checkSkeletonPackage`'s `typeErrs` as positioned diagnostics (via the `//line`
  directives in the skeleton) and, when `len(typeErrs) > 0`, emits nothing. This
  replaces `cachedTypeErrors`'s bespoke positioned-error surfacing.
- Parse errors, script-resolution errors, attr-errors, and unknown-filter errors
  all flow through the existing `analyze`/`Generate` bag, identical to the
  on-disk path.
- Infrastructure errors (bundle missing a needed package) surface as the importer's
  `"cached importer: %q not loaded"` error, propagated by `checkSkeletonPackage`'s
  `Error` callback as a type error diagnostic — same as today.

## Testing strategy

1. **Byte-equivalence:** a fixture generated through bundle-mode `Generate` must be
   byte-identical to the same fixture generated through the default go-list
   `Generate`. (New test in `internal/codegen`.)
2. **Override-only:** generation through the Module with all source supplied via
   `SetOverride` and **no `.gsx` on disk** must succeed and produce correct output
   (the WASM shape). (New test.)
3. **Diagnostic consistency:** a package with a deliberate type error, run through
   `Module.Package`, surfaces only the type-error diagnostic(s) — no secondary
   "could not resolve type" diagnostics. (New regression test; prove it fails
   before the gate is added.)
4. **Migrated tests:** `analyze_test.go` and `resolver_test.go` re-expressed on the
   Module, asserting the same type-map / generation facts as before.
5. **Corpus goldens** (`internal/corpus`, `TestCorpus`) remain the byte-equivalence
   backstop for the whole language and must stay unchanged (no `-update`).
6. **`gen/resolver_test.go`** (the public in-process path) must stay green
   unchanged — it is the integration proof that the WASM entry still works.
7. `make check` (build/vet/test both modules, examples drift, gofmt + gsx fmt)
   green before merge; `make ci` before merging to main.

## Global constraints

- **`.x.go`-independence:** the analysis core never reads generated `.x.go` to
  resolve symbols. Bundle mode resolves entirely from the injected importer +
  in-memory skeletons.
- **No "simple heuristics":** real implementations only.
- **Runtime root package stays standard-library-only;** tooling
  (`internal/codegen`, `gen`) may use `golang.org/x/tools`.
- **Don't hand-edit `.x.go`/golden files;** regenerate from source.
- **One generation driver** is the success criterion: after this change,
  `GeneratePackagesWithResolver` and the `typeResolver` abstraction no longer exist,
  and `internal/codegen` contains a single parse→skeleton→type-check→emit pipeline.

## Out of scope (roadmap, not debt)

- `didChangeWatchedFiles` disk-edit invalidation for the LSP.
- Cross-module reverse-closure in watch.
- The transitive `gsx → Go-only → gsx` `.x.go` boundary (documented Phase 0 gap).
- Removing the playground *server-side* seed-generate (separate repo concern; the
  server still has a `/run` endpoint; touching it is not required by this fold).
