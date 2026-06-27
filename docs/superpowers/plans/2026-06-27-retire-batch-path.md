# Phase 4 — Retire the go-list Batch Codegen Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Delete `codegen.GeneratePackagesWithFilters` + `GeneratePackages` by laying a shared one-shot Module façade (`GenerateDirs`) and migrating every consumer onto it (or onto `Module.Package` for analysis-only consumers) — one go-list codegen path. `CachedResolver` (WASM) untouched.

**Architecture:** `GenerateDirs(moduleRoot, dirs, opts, override)` opens a fresh warm Module and `Generate`s each dir, returning per-dir `.gsx`-keyed bytes + diags (the one-shot façade). Generate-bytes consumers use it; analysis-only consumers (`fmt`, `AnalyzeModule`) use `Module.Package`. A prerequisite fix makes `analyze` surface `buildSkeleton` `attrError`s as diagnostics (like batch) so the façade is a true drop-in.

## Global Constraints
- **Byte-identical `.x.go`**; goldens unchanged; `TestCorpus` passes with NO `-update` (a moved golden = STOP, real divergence). 
- **Safety net `Module ≡ batch ≡ goldens`** retained through Tasks 1–6; the equivalence test + batch path deleted only in Task 7.
- **Do NOT touch** `CachedResolver`/`GeneratePackagesWithResolver` or `buildCrossNav`.
- **Minify:** Open/`GenOptions` with matching `CSSMinify/JSMinify`(+`CSSMin/JSMin`) wherever the prior caller got minified output (batch's `true,true`). Zero-value = no minify.
- No "simple heuristics"; gofmt + `gsx fmt`; run `make ci`-relevant gates before the final review.

## File Structure
- `internal/codegen/generate_dirs.go` (new) — `GenerateDirs`, `GenOptions`, `DirResult`.
- `internal/codegen/module_importer.go` — `analyze` handles `buildSkeleton` `attrError` (diagnostic + skip, not fatal).
- `gen/cache.go`, `gen/fmt.go`, `gen/lsp.go` — consumers migrate.
- `internal/corpus/codegen.go` (+ `batch.go` caller) — golden test → `GenerateDirs`.
- `internal/codegen/*_test.go` (~13) + `gen/resolver_test.go` + `gen/perf_test.go` comment — migrate/delete.
- `internal/codegen/batch.go`, `internal/corpus/module_equiv_test.go` — deleted in Task 7.

---

## Task 1: Foundation — `attrError` fix + `GenerateDirs` façade
**Files:** Create `internal/codegen/generate_dirs.go`; modify `internal/codegen/module_importer.go` (`analyze`); test `internal/codegen/generate_dirs_test.go`.

- [ ] **Step 1: Read** `internal/codegen/batch.go` — exactly how it handles a `buildSkeleton` `*attrError` (converts to a positioned diagnostic in the per-dir bag, excludes the file). Read `internal/codegen/module_importer.go` `analyze` where `buildSkeleton` is called (it currently returns the error fatally) and where `a.gsxFiles`/the bag live.
- [ ] **Step 2:** Make `analyze` mirror batch for a `buildSkeleton` `attrError`: add the positioned diagnostic to `bag` and SKIP that file (remove it from `gsxFiles` / don't add its skeleton), instead of `return nil, …, err`. Other `buildSkeleton` errors keep current behavior unless they too are the soft `attrError` type — match batch precisely (read its switch). The bag is returned by `Generate`, so the diagnostic surfaces.
- [ ] **Step 3:** Write `GenerateDirs`/`GenOptions`/`DirResult` (spec §3.0) in `generate_dirs.go`: Open a Module with the opts (incl. minify), `SetOverride` each `override` entry, then for each dir `out, diags, err := m.Generate(dir)`; on a hard `err` return it; else `result[dir] = DirResult{Files: out, Diags: diags}`. (`Files` stays `.gsx`-keyed.)
- [ ] **Step 4: Test** `generate_dirs_test.go`: (a) `GenerateDirs` over `setupChainModule`'s dirs produces non-empty `.gsx`-keyed `Files` + the components resolve cross-package; (b) the **attr-error equivalence** — a package whose component triggers a bad field-match `attrError` returns a DIAGNOSTIC in `DirResult.Diags` (not a hard error), matching batch. (Find the bad-field-match fixture from `gen/fieldmatcher_e2e_test.go` `TestWithFieldMatcherBadMapper`.) (c) minify: `CSSMinify:false` vs `true` differ on a `<style>` fixture.
- [ ] **Step 5:** `go build ./...` clean; `gofmt -l internal/codegen` empty; `go test ./internal/codegen -run 'GenerateDirs|FieldMatch|Attr' -count=1` PASS; `go test ./internal/codegen -count=1` (no regression — the `attrError` change must not break existing tests). Also run `go test ./internal/corpus -count=1` (equivalence + golden) to confirm the `analyze` change is output-neutral.
- [ ] **Step 6: Commit** `feat(codegen): GenerateDirs one-shot Module façade + analyze surfaces attrError diagnostics`.

---

## Task 2: One-shot `generate` → `GenerateDirs`
**Files:** `gen/cache.go`. Tests: `gen` generate/cache suites stay green.
- [ ] **Step 1: Read** `gen/cache.go` `generateCached` — both `GeneratePackagesWithFilters` sites and how `.Files`(.gsx-keyed)/`.Diags` are written to disk + folded into the error.
- [ ] **Step 2:** Replace each with `codegen.GenerateDirs(root, dirs, codegen.GenOptions{FilterPkgs, Aliases, Classifier: cls, FieldMatcher: fm, CSSMin, JSMin, CSSMinify, JSMinify}, nil)`. Map `DirResult.Files` (.gsx path) → `.x.go` at the write site (as `gen/watchsession.go regenDir` does). Preserve the diags→error folding (`anyErrorDiag`). Group by root if a callsite can span roots.
- [ ] **Step 3:** `go build ./...`; `gofmt -l gen` empty; `go test ./gen -run 'Generate|Cache|Gen|Fmt|Minify|Field' -count=1` PASS; then `go test ./gen -count=1`.
- [ ] **Step 4: Commit** `refactor(gen): one-shot generate via GenerateDirs (drop batch)`.

---

## Task 3: `fmt` → `Module.Package`
**Files:** `gen/fmt.go`.
- [ ] **Step 1: Read** the `GeneratePackagesWithFilters(root, [absDir], …, true, true, nil)` call (~`:180`); confirm only `pr.UnusedImports` is used.
- [ ] **Step 2:** Replace with `m, _ := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: <modPath>, Classifier: attrclass.Builtin()})`; `pr, err := m.Package(absDir)`; read `pr.UnusedImports`. Keep the `continue`-on-error fallbacks.
- [ ] **Step 3:** `go build ./...`; `go test ./gen -run 'Fmt|Unused|Import' -count=1` and `go test ./internal/gsxfmt -count=1` PASS.
- [ ] **Step 4: Commit** `refactor(fmt): unused-import analysis via Module.Package (drop batch)`.

---

## Task 4: `AnalyzeModule` find-references → `Module.Package`
**Files:** `gen/lsp.go` (`AnalyzeModule`).
- [ ] **Step 1: Read** `AnalyzeModule` (~`:143`) + the find-references tests exercising it (whole-module cross-package refs with overrides).
- [ ] **Step 2:** Reuse the warm per-root Module `a.module(root, modPath, merged)`; `SetOverride` each `override`; for each `dir` in `discoverDirs([root])`: `pr, err := m.Package(dir)`; flatten `pr.CrossIndex` into `[]lsp.CrossRef`. Shared fset preserves cross-package routing. Match the return shape.
- [ ] **Step 3:** `go build ./...`; `go test ./gen ./internal/lsp -run 'Reference|CrossPkg|Module|Nav' -count=1` PASS.
- [ ] **Step 4: Commit** `refactor(lsp): AnalyzeModule find-references via the warm Module (drop batch)`.

---

## Task 5: Golden corpus test → `GenerateDirs`
**Files:** `internal/corpus/codegen.go` (+ its `batch.go` caller).
- [ ] **Step 1: Read** `internal/corpus/codegen.go` (`codegenGeneratePackages`→`codegen.GeneratePackages`) + `internal/corpus/batch.go` — confirm it reads ONLY `.Files` (.gsx-keyed) and `.Diags`.
- [ ] **Step 2:** Replace `codegenGeneratePackages(tmp, dirs)` with `codegen.GenerateDirs(tmp, dirs, codegen.GenOptions{FilterPkgs: []string{codegen.StdImportPath}, CSSMinify: true, JSMinify: true}, nil)`, adapting `map[dir]DirResult` to what the corpus step consumes (Files+Diags). NO `Package`, NO `.x.go` remap, NO `*PackageResult` rebuild.
- [ ] **Step 3:** `go test ./internal/corpus -run TestCorpus -count=1` — MUST pass with ZERO golden changes (no `-update`). If a golden moves, STOP and report.
- [ ] **Step 4:** `gofmt -l internal/corpus` empty; `go build ./...`.
- [ ] **Step 5: Commit** `refactor(corpus): golden codegen via GenerateDirs (drop batch)`.

---

## Task 6: Migrate the batch-path unit tests
**Files:** ~13 `internal/codegen/*_test.go` + `gen/resolver_test.go` + `gen/perf_test.go` comment.
- [ ] **Step 1:** `grep -rln "GeneratePackagesWithFilters\|GeneratePackages\b" --include=*_test.go .` — the full list. For EACH: read what it asserts; migrate to `GenerateDirs` (output/minify/diag/column/override tests — use `override` for srcOverride, `CSSMinify:true` for minify) or `Module.Package` (CrossIndex/NavIndex/unused-imports tests), or DELETE if a Module test already covers it (name it in the commit).
- [ ] **Step 2:** Fix the stale `gen/perf_test.go` comment. Migrate `gen/resolver_test.go`'s call (it asserts the default path surfaces diagnostics → `GenerateDirs` with default opts).
- [ ] **Step 3:** `go build ./...`; `gofmt -l internal/codegen gen` empty; `go test ./internal/codegen ./gen -count=1` PASS. Confirm the ONLY remaining batch references are `batch.go` (the defn) + `module_equiv_test.go`.
- [ ] **Step 4: Commit** `test: migrate batch-path unit tests onto GenerateDirs/Module`.

---

## Task 7: Delete the batch path + final sweep
**Files:** `internal/codegen/batch.go`, `internal/corpus/module_equiv_test.go`, any orphan.
- [ ] **Step 1:** `grep -rn "GeneratePackagesWithFilters\|GeneratePackages\b" --include=*.go .` → only `batch.go` defn + `TestModuleMatchesBatchOverCorpus`.
- [ ] **Step 2:** Delete `GeneratePackagesWithFilters` + `GeneratePackages` from `batch.go`; delete now-dead helpers it uniquely used (use `gopls check -severity=hint` / the compiler; KEEP `buildCrossNav` + anything `module*.go`/`resolver.go` call — grep each). If `batch.go` is empty, delete it.
- [ ] **Step 3:** Delete `TestModuleMatchesBatchOverCorpus` (`module_equiv_test.go`) — `TestCorpus` (now Module-driven) is the source-of-truth. Delete the file if that's all it held.
- [ ] **Step 4:** `grep -rn "GeneratePackagesWithFilters\|GeneratePackages\b" --include=*.go .` → empty. Confirm `CachedResolver`/`GeneratePackagesWithResolver`/`buildCrossNav` remain.
- [ ] **Step 5: Full gate:** `go build ./... && go vet ./...`; `go test ./gen ./internal/codegen ./internal/lsp -count=1`; `go test ./internal/corpus -count=1 -timeout 540s`; `gofmt -l .` empty.
- [ ] **Step 6: Commit** `refactor(codegen): delete the go-list batch path; one codegen path`.

---

## Final Whole-Branch Review
Dispatch the final reviewer on `git merge-base main HEAD`..HEAD; then `go build ./... && go vet ./...`; per-package tests; `go test ./internal/corpus -count=1 -timeout 540s`; `make ci` (examples drift + gofmt + `gsx fmt`). Focus: byte-identical output (goldens unchanged); the `attrError` fix makes Module match batch (no silent diagnostic loss); no batch refs remain; `CachedResolver`/WASM + `buildCrossNav` untouched; no orphaned dead code; equivalence-test deletion is safe (golden test now drives the Module).

## Self-Review (coverage)
Foundation+attrError+façade → T1. generate → T2. fmt → T3. AnalyzeModule → T4. golden → T5. units (~13+resolver_test+perf comment) → T6. delete+equiv-removal → T7. Safety net retained through T6; removed in T7. Names: `GenerateDirs`/`GenOptions`/`DirResult`, `Module.Generate`/`Package`/`SetOverride`, `pr.UnusedImports`/`CrossIndex`/`Files`(.gsx-keyed)/`Diags`.
