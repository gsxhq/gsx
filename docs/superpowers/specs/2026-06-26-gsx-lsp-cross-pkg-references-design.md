# gsx LSP — cross-package find-references

**Status:** approved design (brainstormed 2026-06-26), ready for an implementation plan.

## 1. Goal & non-goals

**Goal.** `textDocument/references` on a gsx component returns every reference
across the whole module — not just the declaring package. Today
`handleReferences` (`internal/lsp/references.go`) consults only
`s.pkgs[dirOfCursorFile].CrossIndex`, so a component declared in package
`components` and used as `<components.Input/>` (or `components.Input(...)`) from
package `blog` reports only its in-package references. After this slice,
invoking find-references on the `Input` declaration (or on a `.go` reference to
it) lists usages in `blog` and every other package in the module.

**Search scope: lazy whole-module index (decision B).** The server analyzes a
package only when a file in it is opened, so a union over already-open packages
would silently miss callers — unacceptable for find-references, where a missed
caller misleads a refactor. Instead the first references request that needs it
triggers a one-time whole-module analysis, cached and invalidated on edits.

**Non-goals.**
- **`<Card/>` tag-cursor invocation.** As today (references.go:9-15), invoking
  find-references with the cursor *on a component tag* stays deferred (the tag's
  `//line` column is approximate). Invocation is from the `.gsx` declaration or a
  `.go` reference; the *result* spans `.go` and `.gsx` sites in all packages.
- **Async request handling.** The module index is built synchronously on the Run
  goroutine on a cache miss (see §6). find-references is user-initiated and rare,
  the result is cached per edit-cycle, and the module is the user's own (bounded).
  Routing the build through a worker with reply-by-id is a documented follow-up.
- **Unsaved `.go` edits.** Codegen reads `.go` from disk; `.gsx` open buffers are
  supplied as overrides. Unsaved `.go` edits are gopls's domain, not ours.
- **`.x.go` reliance.** References resolve from in-memory analysis (`TypesInfo` +
  `//line`-mapped positions), never the on-disk `.x.go` content, as everywhere
  else in the LSP.

## 2. Background — how references work today

The codegen analysis (`internal/codegen/batch.go`) builds, per analyzed package,
a `CrossIndex map[componentKey]CrossRef`. `CrossRef{Name, Decl, Refs}` holds the
component's `.gsx` declaration (`Decl`, via the gsx `FileSet`) and every
reference (`Refs`, via `pkg.Fset` so `//line` maps `<Card/>` tags and
`Card(...)` skeleton calls back to `.gsx`, while real `.go` call sites stay
`.go`). References are harvested by scanning `pkg.TypesInfo.Uses`: Case 1 (loop
at batch.go:404) matches a use whose object is in the package's own
`objKey` (object → componentKey) and appends the use position to that
component's `Refs`.

The per-package loop skips dependencies (`pkgDir == ""` → continue), so a
dependency's components never enter `objKey` and an importer's uses of them are
never harvested — that is exactly the cross-package gap.

`gen/lsp.go`'s `lspAnalyzer.Analyze(dir, override)` runs
`GeneratePackagesWithFilters(root, []string{dir}, …)` (one dir) and converts the
result's `CrossIndex` into `lsp.Package.CrossIndex`. `internal/lsp` defines the
`Analyzer` interface (server.go:20) with the single method `Analyze`.

**Key enabler.** A single `packages.Load` over multiple dirs shares one `Fset`
and one set of `types.Object`s across all loaded packages (the importer's use of
`components.Input` *is* the same `types.Object` as `components`' own definition).
So a whole-module batch needs no string-keyed identity: object identity already
links a cross-package use to its declaring component.

## 3. Codegen — route cross-package references (internal/codegen/batch.go)

Add a cross-package harvest pass that reuses the existing machinery. The change
is **additive** and a no-op for single-dir batches, so existing in-package
find-references and go-to-definition behavior is unchanged.

**3.1 Accumulate analyzed packages and a global owner map.** During the existing
per-package loop, record for each analyzed package (those in `dirSet`) a tuple
`{dir, fset, info}` into a slice, and merge its `compObjByKey` into a global map:

```go
type compOwner struct{ dir, key string }
compObjOwner := map[types.Object]compOwner{} // component func object → owning dir + key
// in the loop, after compObjByKey is built for this pkgDir:
for key, obj := range compObjByKey {
    compObjOwner[obj] = compOwner{dir: pkgDir, key: key}
}
analyzed = append(analyzed, analyzedPkg{dir: pkgDir, fset: pkg.Fset, info: pkg.TypesInfo})
```

**3.2 Second pass — append cross-package refs.** After the loop, for each
analyzed package scan its uses and route foreign-owned component refs to the
declaring component's `CrossRef`:

```go
for _, ap := range analyzed {
    for id, obj := range ap.info.Uses {
        owner, ok := compObjOwner[obj]
        if !ok || owner.dir == ap.dir {
            continue // not a component, or in-package (already handled by Case 1)
        }
        p := ap.fset.Position(id.Pos())
        if strings.HasSuffix(p.Filename, ".x.go") {
            continue // synthetic skeleton position, no //line — skip (as Case 1 does)
        }
        cr := result[owner.dir].CrossIndex[owner.key]
        cr.Refs = append(cr.Refs, p)
        result[owner.dir].CrossIndex[owner.key] = cr
    }
}
```

In-package refs (`owner.dir == ap.dir`) are skipped here — Case 1 already added
them — so there is no double counting. For a single-dir batch `compObjOwner`
holds one dir, every use is in-package or unowned, and the pass appends nothing.

## 4. gen — whole-module analysis (gen/lsp.go)

Add `AnalyzeModule(dir, override)` to `lspAnalyzer`:

1. `root, _, err := moduleRoot(dir)`.
2. `dirs, err := discoverDirs([]string{root})` — the existing `gen/gen.go`
   walker (skips `.git`, `vendor`, `node_modules`, `testdata`, hidden dirs),
   returning every dir under the module that directly contains a `.gsx` file.
3. `merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)` (same config the
   per-file `Analyze` uses).
4. `out, err := codegen.GeneratePackagesWithFilters(root, dirs, merged.filterPkgs,
   merged.aliases, merged.classifier(), merged.fieldMatcher, nil, nil, override)`.
5. Merge into a flat `[]lsp.CrossRef`: append every `out[d].CrossIndex` value
   across all dirs. Each component appears once (under its owning dir); its
   `Refs` now hold in-package *and* cross-package sites (routed in §3). Return the
   slice.

If `discoverDirs` or the load fails, return the error (the server falls back to
the single-package path — §6 — so references never regress to empty).

## 5. Analyzer interface (internal/lsp/server.go)

Extend the `Analyzer` interface with:

```go
AnalyzeModule(dir string, override map[string][]byte) ([]CrossRef, error)
```

Test doubles in `internal/lsp` that implement `Analyzer` gain a stub returning
`(nil, nil)` (their references tests already drive the single-package path via a
pre-populated `pkgs`; a nil module result makes the server fall back to it).

## 6. Server — cache, invalidation, query (internal/lsp)

**6.1 State.** Add to `Server`:

```go
moduleRefs      []CrossRef // whole-module cross-reference index (lazy)
moduleRefsValid bool       // false ⇒ rebuild on next references request
```

**6.2 Invalidation.** Any document mutation may change references, so set
`moduleRefsValid = false` (and `moduleRefs = nil`) in `handleDidOpen`,
`handleDidChange`, and `handleDidClose`. Rebuild cost is paid only on the next
references request, not per keystroke.

**6.3 Module-wide override.** Add `allOpenOverride() map[string][]byte`: snapshot
every open `.gsx` document in `s.docs`, keyed by absolute path, so the
whole-module analysis reflects unsaved buffers (mirroring how `snapshotOverride`
feeds per-dir analysis).

**6.4 Query (`handleReferences`).** On a references request:
1. If `!moduleRefsValid`, call `s.analyzer.AnalyzeModule(filepath.Dir(path),
   s.allOpenOverride())`. On success, store the slice and set
   `moduleRefsValid = true`. On error (or a nil slice from a test double), leave
   the cache invalid and fall back to the existing single-package path
   (`s.pkgs[dir].CrossIndex`), so references never regress.
2. Identify the target component by the **same** exact-cursor logic as today
   (`posCoversCursor` on `cr.Decl`, or on a `.go` entry in `cr.Refs`), but over
   `moduleRefs` instead of one package's `CrossIndex`.
3. Return the target's `Refs` (plus `Decl` when `IncludeDeclaration`), via
   `s.locationForPos`, exactly as today.

Building synchronously on the Run goroutine briefly blocks other messages during
the rebuild; this is the §1 documented tradeoff (rare, cached, bounded module).

## 7. Invariants

- `internal/lsp` does not import `internal/codegen` (the `Analyzer` interface and
  `gen/lsp.go` keep the boundary).
- `.x.go`-independent: refs come from `TypesInfo.Uses` with `//line`-mapped
  positions; `.x.go`-filename uses are skipped.
- Additive codegen change: single-dir batches (every per-file `Analyze`) are
  byte-for-byte unchanged, so existing in-package find-references and go-to-def
  e2e tests stay green.
- Best-effort, never panics or regresses: any failure falls back to the
  single-package path; a tag-cursor still returns predictably empty.

## 8. Testing (per [[gsx-syntax-change-test-coverage]])

- **Codegen unit/e2e** (`internal/codegen` or `gen`, `Short`-guarded module
  load): a two-package module (`components` declaring `Input`; `blog` using
  `<components.Input/>` and/or `components.Input(...)`); a whole-module
  `GeneratePackagesWithFilters` over both dirs yields a `components.Input`
  `CrossRef` whose `Refs` include the `blog` site(s). A single-dir generate over
  `components` alone yields only in-package refs (no regression).
- **References e2e cross-package** (`gen`, `Short`): drive
  `textDocument/references` over JSON-RPC with the cursor on the `Input`
  declaration in `components/input.gsx`; assert the result contains the `blog`
  `.gsx` tag site (and `.go` site if present) plus the in-package sites.
- **References e2e from a `.go` reference** (`gen`, `Short`): cursor on a `.go`
  `components.Input` call site → same cross-package result set.
- **Invalidation** (`internal/lsp`, fake analyzer): a `didChange` between two
  references requests forces a second `AnalyzeModule` call (cache invalidated).
- **Fallback** (`internal/lsp`, fake analyzer returning an error/nil from
  `AnalyzeModule`): references still returns the single-package `CrossIndex`
  result (no regression to empty).
- **No regression**: existing `references` tests (in-package) and the gd e2e stay
  green. Full `go test ./...` green.
