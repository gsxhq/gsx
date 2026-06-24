# gsx LSP — `.go → .gsx` Navigation + Find-References

**Status:** approved design, ready to plan
**Date:** 2026-06-24
**Builds on:** Slice 1 (diagnostics) + Slice 2a (`.gsx` go-to-definition, D1+D3) — the in-process `go/types` LSP.

## 1. Goal & non-goals

Make gsx components navigable **from Go code**, entirely in memory:

- **Component nav (`.go → .gsx`):** go-to-definition on a component reference in a
  `.go` file (`Card` in `main.go`) jumps to its `component Card` declaration in
  `card.gsx`.
- **Find-references (bidirectional, in-package):** `references` on a component
  returns every use across both `.go` call sites and `.gsx` `<Card/>` tag sites.

**Hard constraint — no `.x.go` on disk.** This feature must work using gsx's
**in-memory representation only**: the skeleton overlay gsx-LSP already feeds to
`go/packages`, type-checked in memory. **No `.x.go` is generated or required**,
and gopls is not involved in `.go → .gsx`. This explicitly **supersedes the
slice-2a §4 plan** (which would emit `//line` into on-disk `.x.go` for gopls to
read) — we do the resolution in-process instead.

**Non-goals (this slice):** cross-package references (a `Card` used from a
*different* package than the one analyzed); prop-field nav (`CardProps.Title` →
the `.gsx` param); hover/completion on `.go` files; any diagnostics on `.go`
files (gopls owns those). Performance optimization is **out of scope as
implementation** but **in scope as measurement** (§7).

## 2. Background (what slices 1–2a give us)

- gsx-LSP type-checks a package with an **in-memory skeleton overlay** via
  `go/packages` and **retains** the result on `lsp.Package`: `Fset`, `Info`
  (`*types.Info` — covers the package's `.go` files too, since `go/packages`
  type-checks the whole package), the gsx→skeleton `ExprMap`, and the parsed
  `.gsx` `Files`. It never writes `.x.go`.
- `Info.Uses`/`Info.Defs` already contain entries for identifiers in the real
  `.go` files of the package (e.g. `Card` in `main.go`).
- The skeleton emits `//line` directives mapping skeleton positions back to
  `.gsx` (params via slice-2a's param `//line`; child-component tags via the
  existing element `//line`). It does **not** yet emit a `//line` at the
  component **func declaration** — the one codegen addition this slice needs.
- `lsp.Package` does **not** retain the `.go` files' `go/ast` — needed to find
  the identifier under a `.go` cursor.

## 3. Architecture: gsx-LSP as a co-server on `.go` files

The editor routes `.go` files to **both** gopls and gsx-LSP. gopls answers all
normal Go requests; gsx-LSP answers **only** `definition`/`references` for
identifiers that resolve to gsx components, and returns **null/empty for
everything else** so it never competes with gopls. This is two independent LSP
servers on one buffer with editor-side result aggregation — a standard pattern,
and *not* a gopls extension (gopls has no extension point).

All gsx-LSP analysis remains the existing in-memory overlay. A `.go` file merely
becomes another trigger to analyze (and another cursor context to resolve), with
**zero disk artifacts**.

## 4. Feature 1 — Component nav (`.go → .gsx`)

`textDocument/definition` with the cursor in a `.go` file:

1. cursor `(uri, line, char)` → byte offset in the `.go` buffer.
2. Find the `*ast.Ident` at that offset in the retained `.go` AST
   (`astutil.PathEnclosingInterval` over the file's `go/ast`).
3. `obj := Info.Uses[ident]` (fall back to `Defs`).
4. `dp := Fset.Position(obj.Pos())`:
   - maps to a `.gsx` (via the new skeleton func-decl `//line`) → return that
     `Location` (the `component Card` declaration). ✓
   - real `.go` file → return **null** (gopls handles ordinary Go).
   - bare `.x.go` overlay path (no `//line`) → **null** (never surface generated
     code; consistent with slice-2a's guard).

**The one codegen change:** emit `//line card.gsx:<componentLine>:<col>` before
the skeleton's component `func` declaration, so `Card`'s object resolves back to
`component Card`. Same mechanism (and column-compensation approach) as slice-2a's
param `//line`. In-memory skeleton only — never written to disk.

## 5. Feature 2 — Find-references (bidirectional, in-package)

`textDocument/references` with the cursor on a component (in its `.gsx`
declaration, a `.go` call, or a `.gsx` `<Card/>` tag):

1. Resolve the target `types.Object`:
   - `.go` cursor → ident in the `.go` AST → `Uses`/`Defs`.
   - `.gsx` cursor → the slice-2a reverse mapper → skeleton ident → `Uses`/`Defs`.
2. Scan `Info.Uses` and `Info.Defs` for every ident bound to that **same object**
   (object identity is the map's semantics).
3. Map each ident's position through `Fset`:
   - real `.go` position → pass through (a `.go` call site).
   - skeleton position → `//line` maps it to the `.gsx` (`<Card/>` tag site, or
     the `component Card` declaration).
   - bare `.x.go` overlay position with no `//line` → skip (synthetic).
4. Return the deduplicated list of `Location`s — `.go` and `.gsx` sites together.
   Honor the `includeDeclaration` flag in the request params.

**Limitation (stated, not hidden):** references cover only the **analyzed
package(s)** held in `s.pkgs`. A component used from another package is not found
without loading that package — deferred (it needs multi-package loading, a
heavier change). The handler returns what it can and does not error.

## 6. What changes (components & boundaries)

- **`internal/codegen`** (in-memory only):
  - emit a `//line` before each skeleton component `func` declaration (Feature 1).
  - retain the package's real `.go` ASTs: add `GoFiles map[string]*goast.File`
    to `PackageResult`, populated from `pkg.Syntax` (the non-skeleton files).
- **`internal/lsp`:**
  - `Package` gains `GoFiles map[string]*ast.File`.
  - **document lifecycle** accepts `.go` URIs: `didOpen`/`didChange` on a `.go`
    file in a gsx package analyzes that package (so `s.pkgs[dir]` is current) and
    overlays the unsaved `.go` buffer into the analysis — but **publishes no
    diagnostics** for `.go` files (gopls owns those).
  - **definition handler** gains a `.go`-cursor branch (§4); the existing
    `.gsx` branch is unchanged.
  - **references handler** (new, `textDocument/references`), serving both `.gsx`
    and `.go` cursors (§5); advertise `referencesProvider`.
  - **scoping:** for a `.go` file, gsx-LSP works only when its package contains
    `.gsx` files (`s.pkgs[dir]` has non-empty `Files`/`GSXFiles`); otherwise it
    is a no-op returning null/empty, so non-gsx Go code costs nothing.
- **editor (Neovim config):** register gsx-LSP for the `go` filetype in addition
  to `gsx`, scoped to a project/root that contains `.gsx` files (so it doesn't
  attach in pure-Go repos).

## 7. Performance — measure first (a required task, not premature optimization)

Attaching to `.go` files means gsx-LSP analyzes a package whenever a `.go` file
in a gsx package is opened or changed. On a large project this cost is real and
currently **unknown**. This slice therefore includes a dedicated
**measurement** task (no optimization unless the numbers justify it):

- Build/obtain a **representative large gsx project** fixture (many packages,
  many `.gsx` + `.go` files).
- Measure and record baselines:
  - analysis latency per package (cold and warm) on `.go` `didOpen`/`didChange`;
  - definition/references latency end-to-end;
  - **memory** of retained `*lsp.Package` values across many open packages;
  - editor-perceived responsiveness (co-server overhead alongside gopls).
- Identify the dominant cost (almost certainly `go/packages.Load`) and **document
  findings** in the plan/spec.
- **Only then** decide whether any optimization is warranted (candidates to
  evaluate, not pre-commit: debounced re-analysis, an LRU cap on retained
  packages, reusing the slice-1 incremental cache, lazy `.go` attachment). Record
  the decision and rationale. Silent unbounded growth (retaining every package
  forever) is the specific risk to watch.

## 8. Testing

- **Component nav e2e** (temp module, real analyzer, no `.x.go` on disk): a
  `main.go` calling `Card(...)`; `definition` on `Card` resolves to `card.gsx` at
  the `component Card` line. Assert the exact `Location`. A second assertion: the
  temp dir contains **no `.x.go` file** after the request (proves in-memory-only).
- **Find-references e2e:** a `Card` used from `main.go` and as `<Card/>` in a
  second `.gsx`; `references` returns both the `.go` call site and the `.gsx` tag
  site (and the declaration when `includeDeclaration`). Assert the set of
  `Location`s by file+line.
- **Scoping:** `definition`/`references` on a symbol in a `.go` file whose package
  has **no** `.gsx` returns null/empty (gsx-LSP defers to gopls).
- **Codegen:** the skeleton carries a `//line` at each component func decl mapping
  to the `.gsx` component line; corpus goldens unaffected (skeleton-only change);
  printer faithfulness unaffected.
- **Performance:** the measurement task (§7) produces recorded numbers, not a
  pass/fail assertion.

## 9. Risks

- **Unbounded memory** from retaining a `*lsp.Package` per open package — the
  measurement task (§7) quantifies it; an LRU cap is the likely mitigation if
  needed.
- **Co-server confusion:** two servers answering `definition` could surprise a
  user if gsx-LSP returned wrong/extra results. Mitigation: gsx-LSP returns
  **null** for anything not resolving to a gsx component, so it only ever *adds*
  component jumps gopls can't provide.
- **`.go` buffer freshness:** the analysis must overlay the unsaved `.go` buffer,
  not read stale disk. Mitigation: pass open `.go` buffers through the
  `go/packages` overlay (gsx-LSP already overlays skeletons; `.go` files are
  passed through directly, no skeleton).
- **`//line` at func decl shifting diagnostics:** as in slice-2a, the skeleton
  `//line` is positional metadata; the corpus goldens guard against emit changes
  (emit path is untouched). Verify.

## 10. What ships

In an editor: from a `.go` file that uses a gsx component, go-to-definition on the
component jumps into its `.gsx` declaration, and find-references lists every use
across `.go` and `.gsx` — with **no `.x.go` on disk**, purely from gsx's
in-memory analysis. Plus a recorded performance baseline that tells us whether
large projects need optimization before we write any.
