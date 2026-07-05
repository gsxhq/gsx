# `gsx fmt` — syntactic unused-import detection (goimports-style)

**Status:** design
**Date:** 2026-07-05

## Problem

`gsx fmt` is ~300× slower than it should be. On `one-learning-gsx` (91 `.gsx`
files, 8 packages, one module):

| Command | Wall | User CPU |
|---|---|---|
| `generate` (cached, warm) | 0.45s | 0.74s |
| `fmt -l` | 16.3s | 103s |
| `fmt -l -no-imports` | **0.05s** | 0.06s |

The formatting itself (parse → whitespace-normalize → print) of the whole tree is
~50ms. **~99.7% of `fmt -l`'s time is unused-import analysis**
(`analyzeUnusedImports`, `gen/fmt.go`).

### Why it is slow

Two stacked causes, both rooted in the same wrong tool:

1. **Per-directory module `Open`.** `analyzeUnusedImports` calls
   `codegen.Open(...)` *inside* its per-directory loop (`gen/fmt.go:171-184`).
   Each `Open`, on first analysis, drives a full `go/packages.Load` of the entire
   external dependency graph (temporal, clickhouse, prometheus, …). For 8
   directories in one module that is **8 complete dependency loads** — which is
   why `fmt` (16s) is even slower than a *cold* full `generate` (6.1s, which does
   that heavy load once via a single batched `GenerateDirs`).

2. **Type-checking at all.** Detection is driven by `go/types` "imported and not
   used" errors: `detectUnusedImports` (`internal/codegen/results.go:99`)
   correlates the raw type errors from `checkSkeletonPackage`
   (`internal/codegen/module_importer.go:57`) with the hoisted import specs. To
   produce those errors, `checkSkeletonPackage` must resolve *every* dependency
   (recursively for project gsx packages, `packages.Load` for external ones).
   That dependency resolution is the entire cost.

### How goimports is fast

Unused-import detection is a **local, syntactic** fact: an import is unused iff
its local package name is never referenced in the file. goimports walks the
file's AST (`astutil.UsesImport`) and never resolves a single dependency, never
type-checks. That is why it runs in milliseconds.

## Approach

Do what goimports does, on the artifact we already build.

We already lower each `.gsx` file to a **skeleton** — valid Go where every gsx
import usage (interpolations, attribute expressions, pipelines, and
`<pkg.Comp>` component/element tags) becomes a plain Go reference, and the
hoisted user imports are emitted as a normal import block. Building the skeleton
is **importer-free** (`buildSkeleton`, `analyze.go:335`); the dependency
resolution only happens later, in `checkSkeletonPackage`.

So instead of type-checking the skeleton to read off "imported and not used"
errors, **scan the skeleton AST syntactically** for import usage — pure
`go/ast`, zero dependency loads. This is the canonical, real implementation of
import-usage (it is how the Go frontend itself defines it), not a heuristic, and
it reuses our own lowering, so it cannot drift from what `generate` emits.

Fixing detection this way **subsumes both slow causes**: with no type-check there
is no external load to duplicate and nothing per-directory to redo. `fmt` with
imports becomes ~as fast as `fmt -no-imports`.

### Validation (spike, `one-learning-gsx` `ui/`, warm caches)

| Load | Time | Note |
|---|---|---|
| Full `NeedTypes` `./ui/...` (933 pkgs incl deps) | 1.1s | the per-dir cost, ×8 + skeleton checks ≈ 16s — **eliminated** |
| Filter+merger packages only | 344ms | residual, once per module (see optimization) |
| `NeedSyntax` only `./ui/...` | 290ms | parse-only reference |

Confirmed: the 16s is type-checking the graph eight times. Syntactic detection
removes all of it; the only residual is a one-time ~344ms filter/merger load the
skeleton prerequisites currently perform.

## Design

### 1. Importer-free detector in `codegen`

Add a skeleton-only analysis path that runs `analyze`'s front half — parse →
prerequisites (`cachedFilterTable`, `componentPropFieldsFor`, `genericSigsFor`) →
`buildSkeleton` per file, collecting `goFiles` (parsed skeleton `*goast.File`s),
`allImportSpecs`, and `sunkImports` — and **stops before `checkSkeletonPackage`**.
`analyze` already produces all three before the type-check
(`module_importer.go:769-820`), so the split is at an existing boundary.

Expose it as a `Module` method, e.g.:

```go
// UnusedImports returns, per .gsx file in dir, the imports the file declares but
// never references — determined syntactically from the skeleton, with NO
// type-checking and NO dependency resolution.
func (m *Module) UnusedImports(dir string) (map[string][]UnusedImport, []diag.Diagnostic, error)
```

The syntactic detector, per skeleton file `gf` and that file's import specs:

- An import is **unused** iff its effective local name is never referenced in
  `gf` (walk for selector-qualifier identifiers; equivalently
  `astutil.UsesImport(gf, path)` — the skeleton emits the user imports so
  `gf.Imports` is populated).
- **Never** remove `_` (blank) or `.` (dot) imports.
- **Never** remove an import in that file's `sunkImports` set — a
  requalification-failed generic tag can drop a real reference from the skeleton
  (`module_importer.go:788-808`); the `sunkImports` signal is computed
  importer-free during `buildSkeleton`, so the scan consults it directly.
- Aliased imports (`f "foo"`) are keyed by their **local** name.
- Imports are **file-scoped**: an import used only in a sibling file of the same
  package is still unused in this file (matches Go and goimports).
- If a file's (or the package's) skeleton fails to build (parse error,
  `attrError`) → **remove nothing** for it (keep all imports). Matches today's
  skip-on-error posture.

`generate` is **untouched** — it still type-checks (it needs full type info for
emit). Only `fmt`'s detection path changes.

### 2. `gen/fmt.go` — one module `Open`, not one per directory

Replace `analyzeUnusedImports`'s per-directory `codegen.Open` loop with:

- Group the target files by directory, then group directories by module (reuse
  `groupByModule`).
- **`Open` one `Module` per module root**, then iterate its directories calling
  `m.UnusedImports(dir)`, reusing the warm module across directories.

### 3. Config for a faithful skeleton

To build a skeleton that matches what `generate` emits, the `Module` needs the
real `Options` — the configured filter table (`gsx.toml [filters]`), class
merger, classifier, and field matcher. `fmt` today builds with
`attrclass.Builtin()` only, which is a latent inconsistency. `fmt` will resolve
config **best-effort** (the same `resolveConfig` path): on success it uses the
real config; on failure (malformed `gsx.toml`) it falls back to
builtin/empty config, and any file whose skeleton fails to build under that
fallback keeps all its imports (no removal). This preserves fmt's existing
"tolerates a malformed config" property while staying fast.

### 4. Optimization (deferred, measured before adopting)

The skeleton **tolerates unknown filters** — an unresolved named filter falls
back to the bare seed (`analyze.go:1553`). Because named filters (`url`, …)
resolve to *config-injected* imports (structpages, ds/filters), not the file's
own hoisted imports, a degraded (config-only, signature-less) filter table
should still capture every **user** import reference. If verified equivalent for
user-import detection, `fmt` can skip the ~344ms filter/merger load entirely and
land in the tens-of-milliseconds range. Baseline design uses the real config
(correct-by-construction, sub-second); this optimization is a follow-up gated on
an equivalence check.

## Behavior change

Today's detector is ultra-conservative: `detectUnusedImports` returns **nothing**
if the package has *any* non-import type error ("analysis unreliable, remove
nothing"). The syntactic scan removes unused imports **regardless** of unrelated
errors — exactly like `gofmt`/`goimports`. For a formatter this is more correct
(you can clean imports in a file that has an unrelated error elsewhere), but it
is a real behavior change and is called out here deliberately.

## What this replaces

The previously-considered "reuse `generate`'s warm on-disk cache for fmt" plan is
**dropped**. Caching a slow computation is strictly worse than making it fast;
with syntactic detection at sub-second there is nothing worth caching. No change
to the cache format, `computeKey`, or `generate`.

## Testing

Detection lives in `codegen`, so its tests do too. The txtar corpus is unchanged
(fmt output for already-clean files is identical; this is detection behavior, not
codegen output).

**`internal/codegen` — syntactic detector (one case per usage context, per the
project's per-context discipline):**
- Unused user import → removed.
- Used import kept when referenced via: interpolation `{pkg.X}`, attribute
  expression, pipeline-qualified filter `{x |> pkg.Fn}`, component tag
  `<pkg.Comp>`, and generic type argument.
- `_` and `.` imports never removed.
- Aliased import (`f "foo"`) detected by local name.
- Multi-file: import used only in a sibling file is still unused here.
- Sunk generic-tag import kept (reuse an existing generic-tag scenario).
- Skeleton build failure (parse error) → no removal.
- **Equivalence oracle:** for a set of real packages, assert the syntactic
  removal set equals a full type-check `detectUnusedImports` run — except the
  documented "unrelated error" divergence. This is the safety net proving the
  scan matches `go/types` on real files and guards against future skeleton/scan
  drift.

**`gen` — fmt integration:**
- `fmt -l` / `fmt -w` on a fixture with unused imports removes exactly the right
  ones; a used import (each context) is preserved.
- Malformed `gsx.toml` → fmt still formats and tolerates it (fallback path).
- One `Module` opened per module (not per directory): the skeleton
  prerequisites (filter table etc.) are cached on the `Module`, so a
  multi-directory module must load them **once**, not once per directory. Assert
  the per-module load count is 1 (e.g. via the module's load counter,
  `module.go:273`) for a multi-directory fixture.

## Scope boundaries

- `generate`, the cache, and `computeKey` are untouched.
- No new syntax; no corpus golden changes.
- The filter/merger-skip optimization (§4) is out of scope for the first cut;
  ship the correct-by-construction version, then measure.
