# LSP unused imports: syntactic classifier, no `go list` on the hot path

## Problem

`gsx` has two unused-import implementations, and the LSP is wired to the fragile one.

| Surface | Source of "unused imports" | Robustness |
|---|---|---|
| CLI `gsx fmt` | `Module.UnusedImports` → `classifyUnusedImports` (syntactic, per-file) | Robust |
| LSP formatting + `source.organizeImports` | `PackageResult.UnusedImports` ← `detectUnusedImports` (`results.go:104`), set at `module.go:637` | Fragile |

`detectUnusedImports` correlates raw `go/types` errors with import specs by file+line, and
**returns `nil` the moment it sees any error that is not a cleanly position-correlated
`"imported and not used"`** (`results.go:125`). A clean single-file package is fine; a real
multi-file package's skeleton always carries other benign/suppressed type errors, so it bails
to `nil`. Then `internal/lsp/format.go:44` and `internal/lsp/codeaction.go:50` both see a nil
`Unused`, the organized text equals the buffer, and both handlers return `[]` — the LSP
silently offers nothing.

Verified: on a real multi-file package `detectUnusedImports` → `nil`, while
`Module.UnusedImports` on the same package correctly returns `[context, io]`, and
`gsxfmt.FormatWith` given those refs strips both. Every piece works except the heuristic
feeding the LSP.

## The rejected fix

The obvious patch is to call the syntactic classifier from `Package()`:

```go
if unused, _, err := m.UnusedImports(dir); err == nil {
	res.UnusedImports = unused
}
```

**This must not land.** Three independent defects, all reproduced:

1. **Self-deadlock (Critical).** `Package()` acquires `m.analysisMu` (`module.go:583`) and holds
   it via `defer`. `m.UnusedImports` → `buildPackageSkeletons` re-acquires the same mutex
   (`unused_imports_syntactic.go:120`). `sync.Mutex` is not reentrant. Both
   `./internal/codegen` and `./internal/lsp` hang to the test timeout.
2. **`go list` on the LSP hot path (Critical for perf).** `resolvePackageNames` calls
   `packages.Load` (a `go list` subprocess), uncached. Measured on a 6-file package:
   **158 µs** with no candidate, **19.3 ms** when an unused *default* import exists — a 122×
   regression, on exactly the scenario being fixed, once per debounced analysis.
3. **Duplicated work.** `buildPackageSkeletons` re-parses the package and rebuilds the filter
   table and prop fields — all of which `analyze` just did — and re-runs
   `maybeRebuildFset`/`applyDirty` mid-`Package`.

Plus: `detectUnusedImports` and its only other caller `pickImportByPath` go unused, so
`make lint` fails.

## Design: compute it inside `analyze`, from data it already has

`analyze`'s per-file loop (`module_importer.go:840-885`) already holds, for each `.gsx` path:
its per-file `imps []importSpec`, its parsed skeleton `gf *goast.File`, and
`sunkImports[path]`. That is precisely a `fileSkeleton`. Capture the mapping there — no extra
parse, no lock, no `applyDirty` re-entry.

After type-checking, compute unused imports from those skeletons using the **same**
`skeletonUsedNames` + `classifyUnusedImports` the CLI trusts, and store on `analyzed`.
`Package()` then reads the field.

### Resolving candidates without `go list`

`classifyUnusedImports` returns *candidates*: default imports whose path base is not
referenced, whose real package name must be known before they can be called unused
(`math/rand/v2` declares package `rand`). The CLI resolves these with a `NeedName`
`packages.Load` because it deliberately never type-checks.

`analyze` **has already type-checked the package**. `types.Package.Imports()` lists every
directly-imported package — *including unused ones* — with its declared name. Verified:

```
pkg.Imports() for a file where BOTH imports are unused:
  path="context"        name="context"
  path="math/rand/v2"   name="rand"
```

`emit.go:374` already reads declared names this way. So the LSP path resolves candidates from
`a.pkg.Imports()` and never shells out.

### Behavior when the type-check fails, or an import is unresolved

If `pkg` is nil, the name map is empty and every candidate is **conservatively kept**
(`if !ok { continue }`, mirroring `Module.UnusedImports`). Imports with an explicit alias, and
default imports whose base *is* referenced, are still classified syntactically.

**Per-import gating is required, and it is `Complete()`, not "the import list is incomplete."**
`pkg.Imports()` lists every direct import whether or not `pkg` overall type-checked cleanly, but
an individual entry can itself be a placeholder after an importer failure or when a bounded
importer cannot supply it. `go/types` still needs *some* `*types.Package` to continue checking;
it can fabricate one named after the import path's last segment (`"math/rand/v2"` → `"v2"`,
while the real name is `"rand"`) and leave it `Complete() == false`.

That is the false positive an adversarial review caught (Critical): with `math/rand/v2` used as
`rand.IntN(3)`, `classifyUnusedImports` makes it a removal candidate because its path base `"v2"`
is unreferenced; resolving that candidate's name from the fabricated `"v2"` (instead of the real
`"rand"`) makes the live reference invisible, and `Package()` reports a USED import unused — the
LSP then deletes it via `gsxfmt.FormatWith`.

The fix: `importNamesFromTypes` skips any `imp` with `imp.Complete() == false` before adding it to
the map, so an unresolvable import's real name is never guessed — it is simply absent, and
`unusedImportsCore`'s `if !ok { continue }` conservatively keeps it, exactly like a nil `pkg`.

**2026-07-15 source-inventory update:** normal mode now makes every valid GSX-authored import an
explicit root of the authoritative cold load. Packages such as `math/rand/v2` and
`container/ring` are therefore complete, exact inputs even when referenced only from `.gsx`;
`Package()` safely reports a genuinely unused import and agrees with `Module.UnusedImports`.
The `Complete()` gate remains required for actual importer failures and bounded importers, and is
pinned directly rather than by deliberately withholding valid GSX imports from the cold graph.

**Divergence B (the opposite direction, and it is safe):** `Package()` can also **remove** an
import `Module.UnusedImports` **keeps** — the reverse of Divergence A. An unused sibling gsx
package imported only via `.gsx` files (no `.go` files in that directory, e.g. `import
"testmod/foo"` where `foo/` is gsx-only) routes through `moduleImporter` (not `externalImporter`):
analyze type-checks it from its skeletons like any project package, so `importNamesFromTypes`
resolves its real name and `Package()` correctly reports it unused. `Module.UnusedImports`, having
no type information, asks `go list -f NeedName` for the same path; `go list` cannot name a package
with zero `.go` files (verified: `no Go files in <dir>`) and the CLI conservatively **keeps** it.
This is safe because it only ever fires on a genuinely unused import: `Package()` still correctly
KEEPS a sibling gsx import that IS used, in every shape tested (tag `<foo.Box/>`, a plain
`foo.Helper()` call inside an interpolation, and the other used-shapes in the corpus/test suite).
See `TestPackageRemovesUnusedSiblingGsxImportModuleKeeps` and
`TestPackageKeepsUsedSiblingGsxImport` in `internal/codegen/unused_imports_lsp_test.go`.

This is still strictly more robust than `detectUnusedImports`, which returned nil for the whole
package on any unrelated error.

## Implementation

- `internal/codegen/module_importer.go`
  - In the per-file loop, build `skelByGsx map[string]fileSkeleton{skel: gf, imps: imps, sunk: sunkImports[path]}`.
  - After type-checking, set `a.unusedImports = unusedFromSkeletons(skelByGsx, fset, pkg)`.
  - `analyzed` gains `unusedImports map[string][]UnusedImport`.
- `internal/codegen/unused_imports_syntactic.go`
  - Add `importNamesFromTypes(pkg *types.Package) map[string]string` (path → declared name; nil pkg → empty map; entries with `imp.Complete() == false` are skipped — see "Behavior when the type-check fails, or an import is unresolved" above).
  - Add `unusedFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, pkg *types.Package) map[string][]UnusedImport` — the shared core: per file, `skeletonUsedNames` → `classifyUnusedImports` → resolve candidates from the name map.
  - `Module.UnusedImports` (CLI, no types) keeps `resolvePackageNames`; refactor it to call `unusedFromSkeletons`-shaped logic with a name map from `packages.Load` so the two surfaces share one classifier body.
- `internal/codegen/module.go`
  - `res.UnusedImports = a.unusedImports`. No call to `m.UnusedImports`.
- `internal/codegen/results.go`
  - Delete `detectUnusedImports` and `pickImportByPath` (dead; `make lint` enforces).

`Module.UnusedImports` keeps its own skeleton pass and its `packages.Load` — the CLI runs once
per invocation and has no type information. Unchanged.

## Testing

- **Regression (the bug):** a multi-file package whose skeleton carries other type errors AND has
  unused imports → `Package().UnusedImports` is non-empty. This is the exact case
  `detectUnusedImports` returned nil for. It must go through `Package()` — the gap that let the
  deadlock through.
- **Deadlock guard:** the same `Package()` test hangs if the lock bug returns; the package
  `-timeout` catches it.
- **Parity:** `Package(dir).UnusedImports` equals `Module.UnusedImports(dir)` across fixtures
  (default import, aliased import, blank `_`, dot `.`, sunk import), including authoritative
  GSX-only roots whose declared name differs from the path base (`math/rand/v2`).
- **Authoritative GSX-only imports:** `TestModuleAndPackageResolveGsxOnlyImportName` and
  `TestPackageRemovesUnusedGsxOnlyImportNameEqualsBase` assert exact types-only removal for both
  name-different and name-equal paths.
- **Documented divergence B (sibling gsx-only import, the opposite direction):**
  `TestPackageRemovesUnusedSiblingGsxImportModuleKeeps` asserts `Package()` *removes* an unused
  `.gsx`-only sibling import that `Module.UnusedImports` *keeps* (go list cannot name a package with
  no `.go` files); `TestPackageKeepsUsedSiblingGsxImport` asserts the safety property — the same
  import, used via a plain function call, is never reported unused.
- **No `go list` on the hot path (deterministic, not timing-based):** a test-only counter
  incremented in `resolvePackageNames`; assert it stays **0** across `Package()`, and is
  non-zero for the CLI `Module.UnusedImports` path (proving the test can actually observe it).
- **Critical false-positive regression:** `TestPackageUnusedImportsDoesNotDeleteUsedRandV2` — a
  `.gsx` file that uses `math/rand/v2` as `rand.IntN(3)` must not have it reported unused by
  `Package()`, whether the importer supplies an authoritative package or an incomplete placeholder.
- **Headline case still removed:** `context` and `io`, both fully resolvable
  (`Complete() == true`, since the gsx runtime itself imports them), unused, are still reported
  unused by `Package()` via types alone (`TestPackageUnusedImportsHeadlineCaseStillRemoved`) — the
  `Complete()` gate must not over-correct into never removing anything.
- **Incomplete candidates are never guessed:**
  `TestImportNamesFromTypesSkipsIncompletePackage` directly pins the `Complete()` boundary.
- `make ci` and `make lint` green.

## Non-goals

- Caching `resolvePackageNames` for the CLI. `gsx fmt` opens one `Module` per run; a per-module
  path→name cache is a separate, unrelated win.
- Changing `Module.UnusedImports`' public signature or the CLI's behavior.
