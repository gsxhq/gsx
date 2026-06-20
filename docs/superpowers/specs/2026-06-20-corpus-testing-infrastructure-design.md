# Corpus Testing Infrastructure — Design

**Date:** 2026-06-20
**Status:** Approved (brainstorm), pending implementation plan
**Scope:** Restructure gsx's codegen/render test suite onto a single, navigable, fast,
fully-covered fixture spine. Borrows established practice from the compiler / transpiler /
JSX ecosystem (rustc, Go compiler, TypeScript baselines, Babel/SWC snapshots, Go's txtar
testscript).

---

## 1. Motivation — what the audit found

The test suite has three layers; two are healthy, one has drifted.

| Layer | Where | Shape | Health |
|---|---|---|---|
| Unit | `parser/*_test.go` (~80 tests), `internal/codegen/analyze_test.go`, `filters_test.go` | Go unit tests + `TestGoldenCore` (AST) + 3 fuzzers + soundness/position | Solid, fast |
| Pipeline corpus | `internal/corpus/` | txtar (18 cases + 5 errors), checks `ast.golden` + `diagnostics.golden`; `examples_coverage.golden` | Half-built |
| Codegen + render | `internal/codegen/` | `e2e_test.go` = **2378 lines, 115 funcs** | The scatter |

**Problems:**

1. **Two fixture systems that don't talk.** The txtar corpus was *designed* to grow into a
   full-pipeline tracker (its own comments say cases get "promoted into full
   `testdata/pipeline/*.txtar` cases (with ast/diagnostics, then `generated.x.go` and
   `render.golden`)"). Codegen never went that route; it grew **inline Go-string fixtures**
   in one giant file. The corpus only ever tests *parsing*.
2. **`e2e_test.go` is a 2378-line monolith** mixing HTML-diff helpers, the render harness,
   ~90 render tests, and ~22 error-path tests across ~12 feature areas, with no grouping.
3. **91 separate `go run` invocations** — each spins up and compiles its own throwaway
   module. Brute-force coverage; the bulk of suite wall-clock.
4. **Coverage is invisible.** No matrix or index of feature × scenario. The corpus "living
   coverage tracker" only tracks whether things *parse*.

**Goals (all four, equally weighted):** coverage confidence, navigable structure, speed,
and a unified fixture spine.

---

## 2. Architecture — three layers, tested at their boundaries

| Layer | Mechanism | Owns | Change |
|---|---|---|---|
| Unit | Go tests (`parser/*_test.go`, `analyze_test.go`, `filters_test.go`) | Internal invariants: parser productions, type resolution, filter harvest | Stay as-is |
| Parser snapshot | `parser` golden (`TestGoldenCore`) | AST shape — the parser's product | Stays in parser layer |
| **Pipeline corpus** | `internal/corpus` txtar + batch render | The unified spine: input → diagnostics → generated Go (curated) → rendered HTML | **This work** |

The corpus becomes the single home for end-to-end pipeline cases. AST snapshotting stays
in the parser layer on purpose (it's the parser's output, not the corpus's). The two error
systems unify: today parser diagnostics and codegen errors live apart; in the corpus,
`diagnostics.golden` captures both, concatenated.

A few genuinely meta Go tests that can't be expressed as fixtures (e.g. asserting
`//line` directives appear in generated source) stay as a small Go test file in
`internal/codegen`. These are identified during migration, not bulk-kept.

---

## 3. Case format — one txtar per case

```
-- input.gsx --              (required, single-package shorthand) the source, package `views`
-- model.go --               (optional, 0+) sibling Go: models, helpers
-- invoke --                 (required for render cases) Go expr producing a gsx.Node
                             single-package: in-package, e.g. Profile(ProfileProps{...})
                             multi-package:  package-qualified, e.g. pages.Home(pages.HomeProps{})
-- diagnostics.golden --     ALWAYS checked. Empty ⇒ expect no parser/codegen errors.
-- render.golden --          raw rendered HTML; compared STRUCTURALLY (whitespace-insensitive)
-- generated.x.go.golden --  OPTIONAL — present only for the curated codegen-shape subset
-- ast.golden --             OPTIONAL — present only if a case wants to pin AST
```

**Facets are presence-based**, with one safety rule: a case with an `invoke` that produces
no diagnostics **must** have a `render.golden` — a success case can never silently skip
render verification. Error cases have a non-empty `diagnostics.golden` and no `invoke`.

**Facet policy (tiered, per the rustc / Go-compiler instinct):**
- `diagnostics.golden` — every case.
- `render.golden` — every success case.
- `generated.x.go.golden` — a deliberate curated subset where the *lowering shape itself* is
  under test (e.g. fallthrough class-merge, the greeting baseline). Kept small because
  generated-Go goldens churn on every formatting/helper change and `-update` makes
  rubber-stamping easy.
- `ast.golden` — optional escape hatch; AST is normally the parser layer's concern.

---

## 4. Directory layout — the file tree *is* the coverage matrix

Directory = feature area; file = scenario. Browsable, greppable; a missing scenario is a
visible gap. Names map ~1:1 onto the existing 115 test funcs, so migration is mechanical.

```
internal/corpus/testdata/cases/
  elements/        basic, self_closing, fragment, raw_text, doctype_comment
  interpolation/   field_access, interp_types, mixed_chunk, multi_value_expr
  attrs/           static_bool, expr_attrs, cond_attr_bool, cond_attr_else, ...
  class/           composable, escaping, spread, style_rejected
  pipelines/       bare, chain, param, join, try_unwrap, unknown_filter(err), ...
  control_flow/    if, switch, for, goblock
  components/      child_props, children_slot, named_slot, multi_named_slot, ...
  methods/         nullary, with_param, pointer_recv, invocation_chain, ...
  fallthrough/     composed_class_merge, root_wins, manual_placement, not_eligible, ...
  security/        xss, js_rejected, url_blocked
  diagnostics/     reserved_param_ctx, param_collision, clean_errors   (error cases)
  xpkg/            button, type_import, ...                            (cross-package)
  codegen-shape/   fallthrough_merge, greeting, ...                    (generated.x.go subset)
```

---

## 5. Coverage index — the "what's covered" answer

A generated golden, `testdata/coverage.golden`, regenerated under `-update`: one line per
case with the facets it pins. The single at-a-glance coverage report; its diff makes every
added/removed case and facet visible in review.

```
attrs/cond_attr_bool             diag render
attrs/expr_attrs                 diag render
codegen-shape/fallthrough_merge  diag render gen
diagnostics/reserved_param_ctx   diag(error)
pipelines/unknown_filter         diag(error)
xpkg/button                      diag render
...
TOTAL: 112 cases  (render: 90, error: 22, gen-pinned: 8)
```

`examples_coverage.golden` stays — it tracks whether real `examples/*.gsx` *parse*, a
distinct parser-level tracker. It is now clearly one of two complementary trackers.

---

## 6. The unified batch render harness — speed + cross-package, one path

Borrowed from Go's txtar/testscript: a txtar archive's file paths encode a full directory
tree (subdirectory paths define packages); an archive mounts as a real filesystem
(`txtar.FS`). gsx confirms the fit: `.gsx` files carry Go `import` declarations in their
Go-blocks (codegen *hoists* them into the generated `.x.go`), and a cross-package ref like
`<ui.Button/>` requires an `import "example.com/app/ui"` in source. So a case's module-path
prefix appears only in **quoted import strings** — a narrow, reliable rewrite surface.

A single `TestCorpus` test runs in phases:

1. **Load & glob** all `cases/**/*.txtar`.
2. **Parse + codegen each case in-process** (cheap): `parser.ParseFile` → `GeneratePackage`
   per package directory, concatenate parser+codegen diagnostics, capture generated source.
   Compare `diagnostics.golden` (always) and `generated.x.go.golden` / `ast.golden` (if
   present). **Error cases finish here — never compiled.**
3. **Assemble ONE shared batch module** for all renderable cases. Every case — single- or
   multi-package — folds into one build:
   - Assign each case a unique import root `corpustest/cases/<casedir>`.
   - Rewrite the case's module path `M` (from its `go.mod`, or the synthetic default for
     single-package shorthand) → that root, **in quoted import strings only**, across `.gsx`
     and `.go` files, *before* codegen. Because the rewrite happens first, `GeneratePackage`
     resolves cross-package types within the shared module and hoists already-correct import
     paths into the generated `.x.go` — no post-codegen fixup.
   - Place generated `.x.go` + sibling `.go` under `cases/<casedir>/...`.
   - Generate a per-case `entry.go` that wraps the `invoke` expression:
     `func Render(ctx context.Context, w io.Writer) error { return (<invoke>).Render(ctx, w) }`.
     For single-package cases it lives in the case package, so the bare in-package `invoke`
     resolves; for multi-package cases it lives in the package the `invoke` is qualified
     against, importing the sibling packages it references.
   - Generate one fan-in `main.go` importing every case's entry package (aliased
     `case<N>`, so duplicate package names — two `ui`s — never clash) and calling each
     `Render`, printing `\x00CASE <name>\x00` then the rendered HTML.
4. **One `go build`, one run.** Split stdout on the NUL-delimited markers; compare each
   case's HTML to its `render.golden` via the existing structural `compareNodes`
   (whitespace-insensitive, so formatting never churns goldens).

Resulting temp module:

```
corpustest/                         (one go.mod, one gsx replace, one build)
  cases/elements_basic/views.x.go              (single-pkg: M synthetic)
  cases/components_named_slot/views.x.go
  cases/xpkg_button/ui/button.x.go             (multi-pkg: M="example.com/app"
  cases/xpkg_button/pages/home.x.go             rewritten → corpustest/cases/xpkg_button)
  main.go   → runs all entries, NUL-delimited output
```

**Cross-package is first-class in the fast path — no second-class / deferred mode.**
Cross-package cases additionally exercise go/packages + Overlay type resolution across
package boundaries, a path the current single-package tests never touch.

**Why rewrite, not `go.work`:** a workspace (each case its own module) would still need
rewriting (authors reuse `example.com/app`; go.work forbids duplicate module paths) *plus*
add ~90 `go.mod` files and a large `go.work`. The single-module rewrite is less machinery
and the rewrite surface is provably narrow (quoted import paths).

**Failure localization:** a batch build failure names the offending subpackage directory,
which maps directly to a case. (A per-case fallback build is a trivial later add if this
proves annoying — not built now.)

The `assertHTMLEqual` / `compareNodes` helpers move from `e2e_test.go` into the harness.
`-update` regenerates `render.golden`, `generated.x.go.golden`, `diagnostics.golden`, and
`coverage.golden` in place.

---

## 7. Migration — incremental by feature area

Coverage must survive the move; the existing 115 tests encode hard-won edge cases.

- **Phase 0 — infra.** Build the case loader (new sections, presence-based facets,
  multi-package trees, module-path rewrite), the batch render harness, and the coverage
  index. Migrate the existing 18+5 corpus cases to the new format. Prove the harness on a
  few hand-migrated codegen cases.
- **Phases 1..n — per feature area.** Convert e2e tests → txtar cases (input + invoke +
  goldens via `-update`), deleting the corresponding Go test funcs. Each phase is a
  reviewable PR. Verify count parity: N e2e tests removed ⇒ N cases added.
- **Final.** `e2e_test.go` empty → deleted; helpers live in the harness. Keep only the
  small set of genuinely-meta Go tests identified along the way.

**Out of scope:** multi-*module* fixtures (gsx codegen is per-package via go/packages;
cross-package already covers what gsx needs). Integrating a third-party diff library for
golden mismatches (current diff output is adequate).

---

## 8. Success criteria

- One fixture format (`testdata/cases/**/*.txtar`) for the whole pipeline; `e2e_test.go`
  deleted.
- `coverage.golden` lists every case and its facets; gaps are visible in the file tree.
- All renderable cases build+run in a single `go build` + run (no per-case `go run`).
- Cross-package cases are authored the same way as single-package and run in the same fast
  path.
- No coverage regression: every behavior asserted by the old 115 tests is asserted by a
  corpus case (verified by count parity per migration phase).
