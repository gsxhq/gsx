# Phase 4 — Retire the go-list Batch Codegen Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete `codegen.GeneratePackagesWithFilters` + `GeneratePackages` by migrating their last consumers (one-shot `generate`, `fmt`, `AnalyzeModule`, the golden corpus test, ~8 codegen unit tests) onto the warm `Module` — one go-list codegen path. `CachedResolver` (WASM) untouched.

**Architecture:** `Module.Generate ≡ batch` is corpus-proven and `Module.Generate` already threads minify (Phase 3), so it is a byte-identical drop-in. Migrate every caller, keeping the equivalence + golden tests as the safety net until the final deletion.

**Tech Stack:** Go; `internal/codegen` warm `Module`; `gen/` (cache, fmt, lsp); `internal/corpus`.

## Global Constraints

- **Byte-identical `.x.go` at every step.** Goldens (`internal/corpus/testdata`) must not change; `TestCorpus` passes with NO `-update`. If a golden moves, STOP — it's a real divergence, not an expected change. (Spec §1, §5.)
- **`Module ≡ batch ≡ goldens`** is the safety net for tasks 1–5; do NOT delete `TestModuleMatchesBatchOverCorpus` or the batch path until task 6, after all callers are migrated and green. (Spec §5.)
- **Do NOT touch `CachedResolver`/`GeneratePackagesWithResolver`** (the bundle-based WASM path) or `buildCrossNav` (shared). (Spec §1.)
- **Minify fidelity:** when a migrated caller/test previously got minified output (batch's `true,true` or a configured level), Open the Module with the matching `CSSMinify/JSMinify` (+ `CSSMin/JSMin`). Zero-value Options = no minify. (Phase 3 Options.)
- **No "simple heuristics."** Real implementations only (CLAUDE.md).
- **gofmt + gsx fmt.** `gofmt -w` edited files; run `make ci`-relevant gates before the final review.
- **Every change keeps/ships test coverage** (CLAUDE.md / [[gsx-syntax-change-test-coverage]]).

---

## File Structure
- `gen/cache.go` — `generateCached`'s two `GeneratePackagesWithFilters` sites → warm `Module` per root.
- `gen/fmt.go` — the one call → `Module.Package().UnusedImports`.
- `gen/lsp.go` — `AnalyzeModule` → warm Module `Package` per dir, aggregate `CrossIndex`.
- `internal/corpus/codegen.go` (+ `batch.go` caller) — `codegenGeneratePackages` → `Module`.
- `internal/codegen/{batch_test,crossindex_test,navindex_test,retention_test,minify_gate_test,byo_lsp_test,unused_imports_test,batch_override_test}.go` — migrate or delete.
- `internal/codegen/batch.go` — delete `GeneratePackagesWithFilters` + `GeneratePackages` (task 6).
- `internal/corpus/module_equiv_test.go` — delete `TestModuleMatchesBatchOverCorpus` (task 6).

---

## Task 1: One-shot `generate` → warm Module

**Files:** Modify `gen/cache.go`. Tests: existing `gen` generate tests (`gen_test.go`, `cacheinval_test.go`, `perf_test.go`) stay green.

**Interfaces:** Produces a small helper, e.g. `generateViaModule(root string, dirs []string, cfg ...) (map[string]*codegen.PackageResult, error)` OR an inline per-root Module loop, returning the same shape `generateCached`'s callers expect from the batch call.

- [ ] **Step 1: Read** `gen/cache.go` fully — both `GeneratePackagesWithFilters` callsites (the cache-miss path ~`:140` and the no-cache fallback ~`:235`), what they pass, and how the returned `map[dir]*PackageResult`/`.Files` is consumed (written to disk via `restore`/`writeAll`). Note the exact filter/alias/classifier/fieldmatcher/minify args threaded in.
- [ ] **Step 2:** Replace each `GeneratePackagesWithFilters(root, dirs, filterPkgs, aliases, cls, fm, cssMin, jsMin, cssMinify, jsMinify, nil)` with: `m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: <modPath via moduleRoot(root)>, FilterPkgs: filterPkgs, Aliases: aliases, FieldMatcher: fm, Classifier: cls, CSSMin: cssMin, JSMin: jsMin, CSSMinify: cssMinify, JSMinify: jsMinify})` then for each dir `out, diags, gerr := m.Generate(dir)`, assembling the same `*PackageResult`-shaped result OR directly the `.x.go` bytes the caller writes. (If the caller only needs `.Files` for `restore`, map `out` gsxPath→.x.go like watch's `regenDir` does — read `gen/watchsession.go regenDir` for the exact mapping.) Handle multiple roots if `dirs` can span roots (group by root; the batch call took a single `moduleDir` so callers already pass per-root dirs — confirm).
- [ ] **Step 3:** Preserve diagnostics/error semantics: `generateCached` folds error-severity diags into its returned error — keep that behavior (collect `diags` across dirs, apply the same `anyErrorDiag`/error logic the batch path produced).
- [ ] **Step 4:** `go build ./...` clean; `gofmt -l gen` empty; `go test ./gen -run 'Generate|Cache|Gen' -count=1` PASS (these assert generated output + caching). Spot-check: a generated `.x.go` from `gsx generate` is byte-identical to before (the gen tests + corpus cover this).
- [ ] **Step 5: Commit** `git commit -m "refactor(gen): one-shot generate uses the warm Module (drop batch)"`.

---

## Task 2: `fmt` → `Module.Package`

**Files:** Modify `gen/fmt.go`. Tests: existing fmt/unused-import tests stay green.

- [ ] **Step 1: Read** `gen/fmt.go` around the `GeneratePackagesWithFilters(root, []string{absDir}, nil, nil, attrclass.Builtin(), nil, nil, nil, true, true, nil)` call (~`:180`) and confirm it consumes ONLY `pr.UnusedImports`.
- [ ] **Step 2:** Replace with `m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: <modPath>, Classifier: attrclass.Builtin()})` then `pr, err := m.Package(absDir)`; read `pr.UnusedImports`. (No minify needed — `Package` doesn't emit output; `UnusedImports` is identical.) Preserve the surrounding `continue`-on-error fallbacks.
- [ ] **Step 3:** `go build ./...` clean; `go test ./gen -run 'Fmt|Unused|Import' -count=1` and `go test ./internal/gsxfmt -count=1` PASS.
- [ ] **Step 4: Commit** `git commit -m "refactor(fmt): unused-import analysis via Module.Package (drop batch)"`.

---

## Task 3: `AnalyzeModule` find-references → warm Module

**Files:** Modify `gen/lsp.go` (`AnalyzeModule`). Tests: existing find-references tests stay green.

- [ ] **Step 1: Read** `gen/lsp.go` `AnalyzeModule` (~`:143`) and the find-references tests in `gen`/`internal/lsp` that exercise it (whole-module cross-package refs, with unsaved-buffer overrides).
- [ ] **Step 2:** Rewrite using the warm per-root Module the analyzer already builds: `m, err := a.module(root, modPath, merged)`; for `p, src := range override { m.SetOverride(p, src) }`; for each `dir` in `discoverDirs([root])`: `pr, err := m.Package(dir)`; flatten `pr.CrossIndex` into `[]lsp.CrossRef`. The Module's shared fset across `Package` calls preserves cross-package CrossRef routing (the batch call relied on one shared load — the warm Module gives the same). Match the existing return shape exactly.
- [ ] **Step 3:** `go build ./...` clean; `go test ./gen ./internal/lsp -run 'Reference|CrossPkg|Module|Nav' -count=1` PASS (find-references across packages, with overrides).
- [ ] **Step 4: Commit** `git commit -m "refactor(lsp): AnalyzeModule find-references via the warm Module (drop batch)"`.

---

## Task 4: Golden corpus test → Module (the crux)

**Files:** Modify `internal/corpus/codegen.go` (+ its caller in `batch.go`). Tests: `TestCorpus` (golden) must stay byte-identical with NO `-update`.

- [ ] **Step 1: Read** `internal/corpus/codegen.go` (`codegenGeneratePackages` → `codegen.GeneratePackages(moduleDir, dirs)`) and `internal/corpus/batch.go` (the `batchCodegen` step that calls it once for all dirs, then builds + runs renderable cases). Understand the `map[dir]*PackageResult` it returns and how downstream consumes `.Files`/`.Diags`.
- [ ] **Step 2:** Rewrite `codegenGeneratePackages(moduleDir, dirs)` to: `m, err := codegen.Open(codegen.Options{ModuleRoot: moduleDir, FilterPkgs: []string{codegen.StdImportPath}, CSSMinify: true, JSMinify: true})` then for each dir produce the same `*PackageResult` the batch returned. NOTE: `Module.Generate` returns `(map[gsxpath][]byte, diags, err)` — but the corpus consumes `*PackageResult` (with `.Files` keyed by abs `.x.go` and `.Diags`). Use `Module.Package(dir)` to get the `*PackageResult` (Info/CrossIndex/etc.) AND `Module.Generate(dir)` for the `.Files` bytes — OR, simpler, check what the corpus actually reads off the result: if it only needs `.Files` (generated bytes) + `.Diags`, build a `*PackageResult{Files: <gsxpath→.x.go mapped from Generate>, Diags: diags}`. READ `batch.go` to see exactly which `PackageResult` fields the corpus uses, and populate those. Match `GeneratePackages`'s default args (no filters beyond std, minify on → `CSSMinify:true`).
- [ ] **Step 3:** Run the golden gate WITHOUT update: `go test ./internal/corpus -run TestCorpus -count=1`. It MUST pass with zero golden changes. If any `.x.go.golden`/`render.golden` differs, STOP and report — that is a real divergence to investigate, not a regen.
- [ ] **Step 4:** `gofmt -l internal/corpus` empty; `go build ./...` clean.
- [ ] **Step 5: Commit** `git commit -m "refactor(corpus): golden codegen via the warm Module (drop batch)"`.

---

## Task 5: Migrate/delete the ~8 codegen unit tests

**Files:** `internal/codegen/{batch_test,crossindex_test,navindex_test,retention_test,minify_gate_test,byo_lsp_test,unused_imports_test,batch_override_test}.go`.

- [ ] **Step 1:** For EACH file, read its `GeneratePackagesWithFilters` usage and what it asserts. Decide per file: (a) MIGRATE to `Module.Generate`/`Package` (thread `CSSMinify:true` if it asserted minified output; `SetOverride` if it passed `srcOverride`); or (b) DELETE if an existing `internal/codegen` Module test already asserts the same property — name that test in the commit. Default to MIGRATE unless clearly redundant.
- [ ] **Step 2:** Migrate/delete each. Keep the asserted property intact (CrossIndex, NavIndex, retention, minify-gate, byo-lsp, unused-imports, src-override behavior). For `batch_override_test` → `SetOverride` + `Package`/`Generate`. For `minify_gate_test` → Open with the minify level under test.
- [ ] **Step 3:** `go build ./...` clean; `gofmt -l internal/codegen` empty; `go test ./internal/codegen -count=1` PASS. Confirm via `grep` that the ONLY remaining `GeneratePackagesWithFilters`/`GeneratePackages` references are the batch definition itself + `module_equiv_test.go` (deleted in task 6) + the corpus (migrated in task 4).
- [ ] **Step 4: Commit** `git commit -m "test(codegen): migrate batch-path unit tests onto the Module"`.

---

## Task 6: Delete the batch path + final sweep

**Files:** `internal/codegen/batch.go`, `internal/corpus/module_equiv_test.go`, plus any orphaned helper.

- [ ] **Step 1:** `grep -rn "GeneratePackagesWithFilters\|GeneratePackages\b" --include=*.go .` — confirm the ONLY references are the definitions in `batch.go` and `TestModuleMatchesBatchOverCorpus`.
- [ ] **Step 2:** Delete `GeneratePackagesWithFilters` and `GeneratePackages` from `batch.go`. Then delete any function in `batch.go` that becomes unused (use `gopls check -severity=hint internal/codegen/batch.go` or the compiler/`go vet` to find now-dead helpers) — BUT keep `buildCrossNav` and anything `module.go`/`module_importer.go`/`resolver.go` still call (grep each before removing). If `batch.go` becomes empty, delete the file.
- [ ] **Step 3:** Delete `TestModuleMatchesBatchOverCorpus` (`internal/corpus/module_equiv_test.go`) — there is no batch to compare against; `TestCorpus` (task 4, now Module-driven) is the codegen source-of-truth. If the file has only that test, delete the file.
- [ ] **Step 4:** `grep -rn "GeneratePackagesWithFilters\|GeneratePackages\b" --include=*.go .` → empty (no matches). Confirm `CachedResolver`/`GeneratePackagesWithResolver`/`buildCrossNav` still present and referenced.
- [ ] **Step 5: Full gate:** `go build ./... && go vet ./...`; `go test ./gen ./internal/codegen ./internal/lsp -count=1`; `go test ./internal/corpus -count=1 -timeout 540s` (golden); `gofmt -l . | head` empty; (the final review runs `make ci` for examples-drift + `gsx fmt`).
- [ ] **Step 6: Commit** `git commit -m "refactor(codegen): delete the go-list batch path; one codegen path"`.

---

## Final Whole-Branch Review
Dispatch the final reviewer (superpowers:requesting-code-review) on `git merge-base main HEAD`..HEAD. Then the full gate (per-package, NOT one oversubscribed `go test ./...`):
```
go build ./... && go vet ./...
go test ./gen ./internal/codegen ./internal/lsp -count=1
go test ./internal/corpus -count=1 -timeout 540s
make ci   # examples drift + gofmt + gsx fmt (CLAUDE.md before-merge gate)
```
Review focus: every migrated consumer produces byte-identical output (goldens unchanged, gen/fmt/find-refs tests green); no batch references remain; `CachedResolver`/WASM + `buildCrossNav` untouched; no orphaned dead code left in `batch.go`; the deletion of the equivalence test is safe because the golden test now drives the Module directly.

## Self-Review notes (spec coverage)
- §3.1→T1, §3.2→T2, §3.3→T3, §3.4→T4, §3.5→T5, §3.6→T6. Order (§4) preserved: production → golden → units → delete.
- Safety net (§5): equivalence + golden tests retained through T1–T5; equivalence removed only in T6 after the golden test is Module-driven.
- Names: `codegen.Open`/`Options` (Phase 3 incl. `CSSMin/JSMin/CSSMinify/JSMinify`), `Module.Generate`/`Package`, `SetOverride`, `pr.UnusedImports`/`pr.CrossIndex`/`pr.Files`/`pr.Diags`.
