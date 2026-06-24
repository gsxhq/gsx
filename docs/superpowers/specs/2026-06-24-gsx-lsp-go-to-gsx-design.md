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

**Design principle — record only the cross-boundary graph, not everything.**
Unlike gopls (which retains full type info for every symbol to serve every Go
feature), the `.go → .gsx` features need only one narrow thing: the
**cross-boundary index** — for each gsx component, its `.gsx` declaration and
every reference (`.go` call sites + `.gsx` tag sites). We **extract that slim
index at analysis time** (from the momentarily-available `*types.Info` + the
parsed `.go` ASTs) and retain *only* it; the full `Info` and the `.go` ASTs are
discarded after extraction. The query path (definition/references) touches **only
the slim index**, never the full type info. This keeps cross-boundary memory
proportional to *(components × references)* — tiny — and decouples it from the
heavier `.gsx`-side D1/D3 retention (see §2).

**Non-goals (this slice):** cross-package references (a `Card` used from a
*different* package than the one analyzed); prop-field nav (`CardProps.Title` →
the `.gsx` param); hover/completion on `.go` files; any diagnostics on `.go`
files (gopls owns those). Performance optimization is **out of scope as
implementation** but **in scope as measurement** (§8).

## 2. Background (what slices 1–2a give us)

- gsx-LSP type-checks a package with an **in-memory skeleton overlay** via
  `go/packages` and **retains** the result on `lsp.Package`: `Fset`, `Info`
  (`*types.Info` — covers the package's `.go` files too, since `go/packages`
  type-checks the whole package), the gsx→skeleton `ExprMap`, and the parsed
  `.gsx` `Files`. It never writes `.x.go`.
- `Info.Uses`/`Info.Defs` already contain entries for identifiers in the real
  `.go` files of the package (e.g. `Card` in `main.go`) — available transiently
  during analysis. (Slice-2a retains the full `Info` for `.gsx`-side D1/D3, which
  resolves *arbitrary* symbols at a cursor and so genuinely needs it. The
  cross-boundary features here do **not** — they build and keep only the slim
  index, per the design principle in §1; whether the `.gsx`-side full retention
  should also be slimmed is a question for the perf task, §8.)
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

## 4. The cross-boundary index (built once per analysis, retained)

The single data structure behind both features:

```go
type crossRef struct {
    Decl Location   // the .gsx `component Card` declaration
    Refs []Location // every reference: .go call sites + .gsx <Card/> tag sites
}
// componentKey ("recvType.Name") → crossRef
type crossIndex map[string]crossRef
```

**Built at analysis time, then the heavy inputs are discarded:**

1. The component objects are known (the skeleton's component funcs). For each, its
   declaration `.gsx` position comes from `Fset.Position(funcObj.Pos())` mapped by
   the **new skeleton func-decl `//line`** (§7).
2. Iterate `Info.Uses`/`Info.Defs` **once**; for every ident bound to a component
   object, record `Fset.Position(ident.Pos())` as a `Ref` — real `.go` positions
   stay `.go`; skeleton positions (`<Card/>` lowered to `Card(...)`) map via
   `//line` to `.gsx`; bare `.x.go` positions (no `//line`) are skipped.
3. Retain only the resulting `crossIndex`. **Discard `Info` and the `.go` ASTs.**

Memory is *O(components × refs)* — tiny — and the query path never touches the
full type info.

## 5. Feature 1 — Component nav (`.go → .gsx`)

`textDocument/definition` with the cursor in a `.go` file:

1. cursor `(uri, line, char)` → a `.go` Location.
2. Look it up in the `crossIndex`: find the component whose `Refs` contains a
   reference covering the cursor (a small range-containment scan).
3. Found → return that component's `Decl` (`component Card` in `card.gsx`). ✓
4. Not found → **null** (gopls handles ordinary Go; gsx-LSP never surfaces
   generated code, since `.x.go` positions were never indexed).

**The one codegen change:** emit `//line card.gsx:<componentLine>:<col>` before
the skeleton's component `func` declaration, so the component's declaration and
references map back to `.gsx`. Same mechanism (and column-compensation) as
slice-2a's param `//line`. In-memory skeleton only — never written to disk.

## 6. Feature 2 — Find-references (bidirectional, in-package)

`textDocument/references` is served **entirely from the same `crossIndex`** — no
full `Info` at query time:

1. Identify the component the cursor is on — find the component whose `Decl` or
   one of whose `Refs` covers the cursor `Location` (works for a `.go` cursor, a
   `.gsx` `<Card/>` tag, or the `.gsx` declaration).
2. Return that component's `Refs` (already a mix of `.go` and `.gsx` Locations),
   plus its `Decl` when `includeDeclaration` is set.

Because references were resolved and `//line`-mapped when the index was built,
the handler is a lookup — no scanning of type info per request.

**Limitation (stated, not hidden):** references cover only the **analyzed
package(s)** held in `s.pkgs`. A component used from another package is not found
without loading that package — deferred (it needs multi-package loading, a
heavier change). The handler returns what it can and does not error.

## 7. What changes (components & boundaries)

- **`internal/codegen`** (in-memory only):
  - emit a `//line` before each skeleton component `func` declaration (§4).
  - build the **`crossIndex`** during analysis (it has the component objects,
    `Info`, and the parsed `.go` ASTs in hand) and return it on `PackageResult`.
    The full `Info` and `.go` ASTs are **not** added to the LSP-facing handle —
    only the slim index. (Codegen exposes them transiently to the index builder.)
- **`internal/lsp`:**
  - `Package` gains the slim `crossIndex` (component → `{Decl, Refs}`). It does
    **not** gain the `.go` ASTs or rely on the retained full `Info` for these
    features.
  - **document lifecycle** accepts `.go` URIs: `didOpen`/`didChange` on a `.go`
    file in a gsx package analyzes that package (so `s.pkgs[dir]` is current,
    including the rebuilt index) and overlays the unsaved `.go` buffer into the
    analysis — but **publishes no diagnostics** for `.go` files (gopls owns those).
  - **definition handler** gains a `.go`-cursor branch (§5), a pure `crossIndex`
    lookup; the existing `.gsx` branch is unchanged.
  - **references handler** (new, `textDocument/references`), a `crossIndex` lookup
    serving both `.gsx` and `.go` cursors (§6); advertise `referencesProvider`.
  - **scoping:** for a `.go` file, gsx-LSP works only when its package contains
    `.gsx` files (the `crossIndex` is empty otherwise); a `.go` file in a non-gsx
    package is a no-op returning null/empty, so non-gsx Go code costs nothing.
- **editor (Neovim config):** register gsx-LSP for the `go` filetype in addition
  to `gsx`, scoped to a project/root that contains `.gsx` files (so it doesn't
  attach in pure-Go repos).

## 8. Performance — measure first (a required task, not premature optimization)

Attaching to `.go` files means gsx-LSP analyzes a package whenever a `.go` file
in a gsx package is opened or changed. On a large project this cost is real and
currently **unknown**. This slice therefore includes a dedicated
**measurement** task (no optimization unless the numbers justify it):

- Build/obtain a **representative large gsx project** fixture (many packages,
  many `.gsx` + `.go` files).
- Measure and record baselines:
  - analysis latency per package (cold and warm) on `.go` `didOpen`/`didChange`,
    **including the `crossIndex` build cost** (it iterates `Info.Uses` once);
  - definition/references latency end-to-end (these are slim-index lookups, so
    expected to be negligible — confirm);
  - **memory** of retained state across many open packages, split into: the slim
    `crossIndex` (expected tiny, *O(components × refs)*) vs. the **`.gsx`-side
    full-`Info` retention inherited from slice-2a** (expected to dominate);
  - editor-perceived responsiveness (co-server overhead alongside gopls).
- Identify the dominant cost (almost certainly `go/packages.Load`, then possibly
  the slice-2a full-`Info` retention) and **document findings** in the plan/spec.
- **Only then** decide whether any optimization is warranted (candidates to
  evaluate, not pre-commit: debounced re-analysis; an LRU cap on retained
  packages; **slimming the `.gsx`-side retention too** — re-analyze on demand
  instead of holding full `Info`, if memory dominates; reusing the slice-1
  incremental cache; lazy `.go` attachment). Record the decision and rationale.
  The cross-boundary features are slim **by design** (§1); the open question the
  measurement answers is whether the *`.gsx`-side* retention needs the same
  treatment.

## 9. Testing

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
- **Performance:** the measurement task (§8) produces recorded numbers, not a
  pass/fail assertion.

## 10. Risks

- **Unbounded memory** from retaining state per open package. The cross-boundary
  `crossIndex` is slim by design, so the risk is concentrated in the slice-2a
  `.gsx`-side full-`Info` retention. The measurement task (§8) quantifies it; an
  LRU cap and/or slimming the `.gsx`-side retention are the likely mitigations.
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

## 11. What ships

In an editor: from a `.go` file that uses a gsx component, go-to-definition on the
component jumps into its `.gsx` declaration, and find-references lists every use
across `.go` and `.gsx` — with **no `.x.go` on disk**, purely from gsx's
in-memory analysis. Plus a recorded performance baseline that tells us whether
large projects need optimization before we write any.
