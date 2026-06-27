# Phase 3 — Warm `--watch` on the Module Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate `gsx generate --watch`'s warm regeneration from the legacy `CachedResolver` to the warm `Module` core, regenerating a changed package's reverse-closure importers (closing the `.x.go` staleness gap) — output byte-equivalent.

**Architecture:** Thread minify config through `Module.Generate` (prerequisite). Add `Module.Dependents` (public reverse-closure). Rebuild `watchSession` on one `*codegen.Module` per module root: cold-start via `Module.Generate` (writes all `.x.go` + populates the import graph); on a saved-file change, `Invalidate(dir)` then regenerate the deduped union of every changed dir's `Dependents`. Retire `CachedResolver` for watch.

**Tech Stack:** Go; the warm `Module` (`internal/codegen`); `gen/watch.go`/`watchsession.go`; fsnotify.

## Global Constraints

- **Output byte-equivalence.** Every regenerated `.x.go` must be byte-identical to one-shot `gsx generate` on the same sources/config. The corpus equivalence gate (`go test ./internal/corpus`) MUST stay green. The reverse-closure changes *which* files are rewritten, never their bytes. (Spec §1, §6.)
- **Minify fidelity.** `Module.Generate` must honor the configured minify level (full/default/off) + custom minifiers, exactly as one-shot generate — not the old hardcoded default-minify. (Spec §3.0.)
- **`.x.go`-independent resolution.** Watch regen resolves cross-package types from skeletons via the Module, never from disk `.x.go` (though it still *writes* `.x.go`). (Spec §6.)
- **No stale importer.** After a change to D, every package in D's reverse-closure is regenerated in the same cycle. (Spec §6.)
- **No "simple heuristics."** Real implementations only (CLAUDE.md).
- **gofmt + gsx fmt.** Run `gofmt -w` on edited files; confirm `gofmt -l` empty. Run `make ci`-relevant gates (build/vet/test) before the final review.
- **Every change ships test coverage** (CLAUDE.md / [[gsx-syntax-change-test-coverage]]).

---

## File Structure

- `internal/codegen/module.go` — `Options` gains `CSSMin/JSMin/CSSMinify/JSMinify`; `Generate`'s emit loop uses them; new `Dependents`.
- `internal/corpus/module_equiv_test.go`, `internal/codegen/module_test.go`, `internal/codegen/module_stale_xgo_test.go` — Open the Module with `CSSMinify:true,JSMinify:true` where they compare against minified batch output (audit; only where a fixture has static CSS/JS or compares to batch).
- `gen/watchsession.go` — `watchSession.modules map[string]*codegen.Module`; `newWatchSession`/cold-gen/`regenDir`/`depDirty` rewritten onto the Module; `CachedResolver`/`isCachedImporterMiss`/`newModuleResolver` removed.
- `gen/watch.go` — fire handler does Invalidate + reverse-closure union regen.
- `gen/watchsession_test.go`, `gen/watch_test.go` (+ new `gen/watch_revclosure_test.go`) — adapt to the Module path; add reverse-closure/equivalence/minify tests.

---

## Task 1: Thread minify config through `Module.Generate`

**Files:**
- Modify: `internal/codegen/module.go` (`Options`, `Generate`)
- Modify/audit: `internal/corpus/module_equiv_test.go`, `internal/codegen/module_test.go`, `internal/codegen/module_stale_xgo_test.go`
- Test: `internal/codegen/module_test.go` (a minify-threading test)

**Interfaces:**
- Produces: `Options{ …, CSSMin, JSMin func(string)(string,error), CSSMinify, JSMinify bool }`. `Module.Generate` emits with these instead of hardcoded `nil,nil,true,true`. Zero-value (`CSSMinify:false`) = no minify; callers opt in.

- [ ] **Step 1: Add Options fields.** In `module.go`'s `Options` struct, append:
```go
	CSSMin    func(string) (string, error) // custom static-CSS minifier (nil = built-in when CSSMinify)
	JSMin     func(string) (string, error) // custom static-JS minifier (nil = built-in when JSMinify)
	CSSMinify bool                         // minify static <style> CSS
	JSMinify  bool                         // minify static <script> JS
```
`gofmt -w`.

- [ ] **Step 2: Use them in `Generate`.** In `Module.Generate`, the emit loop calls
`generateFile(f, a.resolved, a.table, a.propFields, a.nodeProps, a.byo, a.gsxFset, m.opts.Classifier, m.opts.FieldMatcher, bag, nil, nil, true, true)`. Replace the trailing `nil, nil, true, true` with `m.opts.CSSMin, m.opts.JSMin, m.opts.CSSMinify, m.opts.JSMinify`. (Leave `Package`'s diagnostic-emit loop's hardcoded `nil, nil, true, true` UNCHANGED — its output is discarded.)

- [ ] **Step 3: Fix the corpus equivalence test.** In `internal/corpus/module_equiv_test.go`, the Module is `codegen.Open(codegen.Options{ModuleRoot: tmp, ModulePath: modulePath, FilterPkgs: []string{codegen.StdImportPath}})`. The batch oracle it compares against passes `…, nil, nil, true, true` (minify on). Add `CSSMinify: true, JSMinify: true` to the Module's Options so both sides minify identically.

- [ ] **Step 4: Audit the other Module.Generate test callers.** For `internal/codegen/module_test.go` (lines ~180, ~246) and `internal/codegen/module_stale_xgo_test.go` (~84): read each test's fixture. If the fixture has a static `<style>`/`<script>` AND the test asserts exact generated bytes, add `CSSMinify: true, JSMinify: true` to its `Open` to preserve the prior (minified) output. If the fixture has no static CSS/JS (minify is a no-op), no change is needed — note which in the task report. (`fset_growth_test.go`'s `TestGenerateOutputIdenticalAcrossRebuild` asserts before==after on the SAME Module, so it is minify-default-agnostic — leave it.)

- [ ] **Step 5: Write the minify-threading test.** Add to `internal/codegen/module_test.go` (or a new `minify_thread_test.go`). Build a 1-package module whose component has a static `<style>` with collapsible whitespace, e.g. `<style>.a {  color : red ;  }</style>`. Assert: Open with `CSSMinify:true` → generated bytes contain the minified CSS (no double spaces); Open with `CSSMinify:false` → generated bytes contain the original un-minified CSS; Open with `CSSMin: fullmin.CSS, CSSMinify:true` → full-minified. (Import `github.com/gsxhq/gsx/internal/fullmin`. Inspect the generated `.x.go` string for the CSS literal.) Make it non-tautological — assert the three outputs actually differ where expected.

- [ ] **Step 6: Run.** `go build ./...` clean; `gofmt -l` empty; `go test ./internal/codegen -run 'Minify|Generate' -count=1` PASS; `go test ./internal/corpus -run TestCorpus -count=1` PASS. (The full `TestModuleMatchesBatchOverCorpus` runs in the final gate — but you MAY run it here to confirm Step 3, ~500s.)

- [ ] **Step 7: Commit.**
```bash
git add internal/codegen/module.go internal/corpus/module_equiv_test.go internal/codegen/module_test.go internal/codegen/module_stale_xgo_test.go
git commit -m "feat(codegen): thread minify config through Module.Generate (Options)"
```

---

## Task 2: `Module.Dependents` public reverse-closure

**Files:**
- Modify: `internal/codegen/module_importer.go` (new `Dependents`)
- Test: `internal/codegen/invalidation_test.go` (or `snapshot_cache_test.go`)

**Interfaces:**
- Produces: `(*Module).Dependents(dir string) []string` — the reverse-reflexive-transitive closure of `dir` over the import graph (dir + every transitive importer). Returns `[dir]` when nothing imports it.

- [ ] **Step 1: Add `Dependents`** in `module_importer.go` (near `Invalidate`):
```go
// Dependents returns the reverse-reflexive-transitive closure of dir over the import
// graph: dir plus every project gsx package that transitively imports it. Watch uses it
// to regenerate every package affected by a change to dir. Returns just dir when nothing
// imports it (or dir is unknown to the graph).
func (m *Module) Dependents(dir string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cl := m.reverseClosure([]string{dir})
	out := make([]string, 0, len(cl))
	for d := range cl {
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 2: Write the test** (reuse `setupChainModule`: util←components←pages + solo):
```go
func TestDependentsReverseClosure(t *testing.T) {
	if testing.Short() { t.Skip() }
	m, root := setupChainModule(t)
	if _, err := m.Package(filepath.Join(root, "pages")); err != nil { t.Fatal(err) } // warm graph
	if _, err := m.Package(filepath.Join(root, "solo")); err != nil { t.Fatal(err) }
	got := m.Dependents(filepath.Join(root, "util"))
	// util + everything that transitively imports it: components, pages. NOT solo.
	want := map[string]bool{
		filepath.Join(root, "util"): true, filepath.Join(root, "components"): true, filepath.Join(root, "pages"): true,
	}
	if len(got) != len(want) { t.Fatalf("Dependents(util) = %v, want keys %v", got, want) }
	for _, d := range got { if !want[d] { t.Errorf("unexpected dependent %s", d) } }
	// A leaf nothing imports: just itself.
	if d := m.Dependents(filepath.Join(root, "solo")); len(d) != 1 || d[0] != filepath.Join(root, "solo") {
		t.Errorf("Dependents(solo) = %v, want [solo]", d)
	}
}
```

- [ ] **Step 3: Run.** `go test ./internal/codegen -run TestDependentsReverseClosure -v` → PASS. `go build ./...` clean.

- [ ] **Step 4: Commit.**
```bash
git add internal/codegen/module_importer.go internal/codegen/invalidation_test.go
git commit -m "feat(codegen): Module.Dependents (public reverse-closure for watch)"
```

---

## Task 3: `watchSession` on the warm Module

**Files:**
- Modify: `gen/watchsession.go` (the core rewrite)
- Test: `gen/watchsession_test.go` (adapt)

**Interfaces:**
- Consumes: `codegen.Open`/`Options` (Task 1), `Module.Generate`, `Module.Invalidate`.
- Produces: `watchSession{ modules map[string]*codegen.Module }`; `(*watchSession).moduleForDir(dir) (*codegen.Module, error)`; `(*watchSession).regenDir(dir) cycleResult` (Module.Generate + write). `CachedResolver`/`isCachedImporterMiss`/`newModuleResolver` removed from watch.

- [ ] **Step 1: Rewrite the session struct + constructor.** Replace `resolvers map[string]*CachedResolver` with `modules map[string]*codegen.Module`. Add a helper to build a Module per root from `cfg`:
```go
func (s *watchSession) openModule(root string) (*codegen.Module, error) {
	_, modPath, err := moduleRootPath(root) // root's module path; see Step 1a
	if err != nil {
		return nil, err
	}
	return codegen.Open(codegen.Options{
		ModuleRoot: root, ModulePath: modPath,
		FilterPkgs: s.cfg.filterPkgs, Aliases: s.cfg.aliases,
		FieldMatcher: s.cfg.fm, Classifier: s.cfg.cls,
		CSSMin: s.cfg.cssMin, JSMin: s.cfg.jsMin,
		CSSMinify: s.cfg.cssMinify, JSMinify: s.cfg.jsMinify,
	})
}
```
- **Step 1a:** `moduleRoot(dir)` returns `(root, modPath, err)`. For a known root you still need its module path: call `moduleRoot(root)` (a root is a dir under itself) — it returns the same root + its modPath. Use that; no new function needed (inline `_, modPath, err := moduleRoot(root)`).

- [ ] **Step 2: `newWatchSession` cold-start via Module.** Replace the `generateCached` cold pass + resolver build with: discover dirs, `groupByModule`; for each root `s.modules[root] = openModule(root)`; for every discovered dir, `s.regenDir(dir)` (writes `.x.go` + populates the graph). Accumulate the per-dir `cycleResult`s as `startup`. Keep the early "no watchable dirs" / "no enclosing module" guards. (The graph is fully populated because every dir is analyzed by `Module.Generate` here.)

- [ ] **Step 3: `regenDir`.** Replace `regen`/`writeFiles` with a Module-based regen:
```go
func (s *watchSession) regenDir(dir string) cycleResult {
	start := time.Now()
	m, err := s.moduleForDir(dir) // resolves root → Module; registers a new root if unseen
	if err != nil {
		return cycleResult{Dir: dir, Err: err, DurMs: time.Since(start).Milliseconds()}
	}
	out, diags, gerr := m.Generate(dir)
	files := make(map[string][]byte, len(out))
	for gsxPath, b := range out {
		files[strings.TrimSuffix(gsxPath, ".gsx")+".x.go"] = b
	}
	written, werr := writeFiles(dir, files) // existing hash-gated restore
	var finalErr error
	switch {
	case gerr != nil && !anyErrorDiag(diags):
		finalErr = gerr
	case werr != nil:
		finalErr = werr
	}
	return cycleResult{
		Dir: dir, Written: written, Diags: diags,
		OK:  gerr == nil && !anyErrorDiag(diags) && werr == nil,
		Err: finalErr, DurMs: time.Since(start).Milliseconds(),
	}
}
```
`writeFiles` (existing) already maps `filepath.Base(absXGo)`→bytes and calls `restore`; keep it.

- [ ] **Step 4: `moduleForDir` + `depDirty` re-Open.** Add `moduleForDir(dir)`: resolve `root` via `moduleRoot(dir)`; if `s.modules[root]==nil`, `openModule(root)` and store; return it. Replace `rebuild()` (resolver rebuild) with `reopen()`: for each root, `s.modules[root] = openModule(root)` (fresh ext on next Generate) and re-run the cold generate for that root's dirs so the graph repopulates. (The `depDirty` fire path calls `reopen()`.)

- [ ] **Step 5: Delete the dead code.** Remove `newModuleResolver`, `isCachedImporterMiss`, and the `CachedResolver` field/usages from `watchsession.go`. Run `grep -rn "CachedResolver\|isCachedImporterMiss\|newModuleResolver" gen/` and confirm only intended references remain (the gen-level `CachedResolver` wrapper type may still be used by one-shot generate — do NOT delete the type, only watch's use of it). `gofmt -w`.

- [ ] **Step 6: Adapt `watchsession_test.go`.** The existing tests call `r.Generate(blogDir, nil)` on a resolver. Rewrite them to drive a `watchSession` (or a Module) instead, asserting the same regen outcome. Keep their intent (warm regen produces correct `.x.go`). Run `go test ./gen -run 'Watch|watchSession' -count=1` → PASS.

- [ ] **Step 7: Build + suites.** `go build ./...` clean; `gofmt -l` empty; `go test ./gen -count=1` PASS.

- [ ] **Step 8: Commit.**
```bash
git add gen/watchsession.go gen/watchsession_test.go
git commit -m "feat(watch): drive warm regeneration from the Module core (retire CachedResolver)"
```

---

## Task 4: Reverse-closure regeneration in the fire handler

**Files:**
- Modify: `gen/watch.go` (fire handler)
- Test: `gen/watch_revclosure_test.go` (create)

**Interfaces:**
- Consumes: `Module.Invalidate`, `Module.Dependents` (Task 2), `watchSession.regenDir`/`moduleForDir` (Task 3).

- [ ] **Step 1: Rewrite the non-`depDirty` fire branch.** In `gen/watch.go`, the current loop is `for dir := range pending { if onlyGeneratedRemains(dir) { continue }; em.cycle(sess.regen(dir)) }`. Replace with Invalidate + reverse-closure union:
```go
affected := map[string]bool{}
for dir := range pending {
	if onlyGeneratedRemains(dir) {
		continue
	}
	m, err := sess.moduleForDir(dir)
	if err != nil {
		em.cycle(cycleResult{Dir: dir, Err: err})
		continue
	}
	m.Invalidate(dir) // saved-file change: drop dir + its closure from caches
	for _, dep := range m.Dependents(dir) {
		affected[dep] = true
	}
}
for dir := range affected {
	em.cycle(sess.regenDir(dir))
}
pending = map[string]bool{}
```
(`Invalidate(dir)` runs before `Dependents(dir)`; Invalidate does not touch the graph, so `Dependents` returns the pre-change importer set — correct, since a change to `dir` cannot add new importers of `dir`.)

- [ ] **Step 2: Keep `depDirty` handling.** The `depDirty` branch now calls `sess.reopen()` (Task 3 Step 4) instead of `sess.rebuild()`. After `reopen()`, fall through to the same affected-union regen for `pending` (the graph is repopulated by `reopen`'s cold generate, so `Dependents` is complete).

- [ ] **Step 3: Write the reverse-closure test.** Create `gen/watch_revclosure_test.go`. Two-package fixture: `widgets` (component `Badge(label string)` rendering `<span>{label}</span>`), `home` imports `example.com/x/widgets` and renders `<widgets.Badge label="hi"/>`. Use the watch test harness (model on `gen/watch_test.go` — read it for how it starts a session, fires events, and reads results; if it uses a `stop` channel + injected events, mirror that; otherwise drive `newWatchSession` + the fire logic directly). Steps:
  1. Start the session (cold generate writes both `.x.go`; capture their bytes/mtimes).
  2. Modify `widgets/badge.gsx` so the generated call surface changes (e.g. change the `<span>` to `<b>`, or add a prop `Badge(label, kind string)` and update home to pass it — pick a change that alters BOTH widgets' and home's generated bytes; simplest: change the element widgets renders, which changes widgets' bytes, AND verify home is regenerated even though home's source is unchanged).
  3. Fire the change for `widgets`'s dir.
  4. Assert **both** `widgets/badge.x.go` AND `home/home.x.go` were rewritten (e.g. both appear in the cycle `Written` sets / both mtimes advanced), proving reverse-closure regen. Document in a comment that pre-Phase-3 only `widgets` would have regenerated.
  - If "home's bytes don't change when only widgets' element changes" (home's generated call to `Badge` may be identical regardless of Badge's body), make the change one that DOES affect home's output — e.g. change `Badge`'s prop name/signature so home's generated call site changes, OR assert at minimum that home's `.x.go` is RE-EMITTED (re-analyzed against the new widgets) even if bytes match. The non-negotiable assertion: `home` is in the regenerated set after editing `widgets`.

- [ ] **Step 4: Run.** `go test ./gen -run 'TestWatch.*RevClosure|TestWatchReverseClosure' -v` → PASS. `go build ./...` clean; `gofmt -l` empty.

- [ ] **Step 5: Commit.**
```bash
git add gen/watch.go gen/watch_revclosure_test.go
git commit -m "feat(watch): regenerate a changed package's reverse-closure importers"
```

---

## Task 5: Equivalence, dep-change, multi-module, corpus

**Files:**
- Test: `gen/watch_equiv_test.go` (create), `gen/watchsession_test.go` / `gen/watch_test.go` (extend)

**Interfaces:**
- Consumes: the migrated watch (Tasks 3-4).

- [ ] **Step 1: Output-equivalence test.** Create `gen/watch_equiv_test.go`. For a fixture module: run one-shot `Generate([]string{root})` (or `generateCached`) capturing every `.x.go`'s bytes; separately, cold-start a watch session over the same sources; assert each `.x.go` the watch wrote is byte-identical to the one-shot output. Then modify a file, fire, and assert the regenerated files still match a fresh one-shot generate. Run it once with **default** minify and once with a **non-default** level (e.g. construct the watchConfig with `cssMinify:false`) to prove the minify threading (Task 1) carries through watch.

- [ ] **Step 2: Multi-module test.** A temp tree with TWO module roots (each its own `go.mod`), each with a gsx package. Start a watch spanning both; edit one package in each; assert each regenerates against its own Module (no cross-module bleed; both `.x.go` correct). (Model on any existing multi-module watch test; if none, build the two-root fixture inline.)

- [ ] **Step 3: Dep-change test.** Start a watch; change a companion `.go` (or go.mod) in a gsx package such that `isDepFile` flags it; fire; assert `reopen()` ran and the affected package regenerated with correct output (no `cached importer` error path — that logic is gone). If constructing a real dep change is heavy, at minimum assert that a `.go` change to a package with a companion model still regenerates correctly.

- [ ] **Step 4: Full gate.**
```bash
go build ./...
go test ./gen ./internal/codegen ./internal/lsp -count=1
go test ./internal/corpus -count=1 -timeout 560s   # incl. TestModuleMatchesBatchOverCorpus (~500s) — minify-equivalence
gofmt -l gen internal/codegen
```
All PASS / empty.

- [ ] **Step 5: Commit.**
```bash
git add gen/watch_equiv_test.go gen/watchsession_test.go gen/watch_test.go
git commit -m "test(watch): output-equivalence, multi-module, dep-change on the Module path"
```

---

## Final Whole-Branch Review

After all 5 tasks: dispatch the final reviewer (superpowers:requesting-code-review) on `git merge-base main HEAD`..HEAD. Then the full gate (per-package, NOT one oversubscribed `go test ./...`):
```bash
go build ./... && go vet ./...
go test ./gen ./internal/codegen ./internal/lsp -count=1
go test ./internal/corpus -count=1 -timeout 560s
```
Review focus: minify threading correctness (watch honors the configured level; corpus equivalence still green at `CSSMinify:true`); the reverse-closure regen (Invalidate-before-Dependents ordering; deduped union; no stale importer); output byte-equivalence vs one-shot generate; `CachedResolver` fully retired from watch with no dead/contradictory code; the cold-startup graph is complete before the first warm cycle; `depDirty` `reopen` repopulates the graph.

## Self-Review notes (spec coverage)

- Spec §3.0 (minify) → Task 1. §3.1 (Dependents) → Task 2. §3.2 (watchSession on Module) → Task 3. §3.3 (reverse-closure fire handler) → Task 4. §3.4 (output mapping) → Task 3 Step 3.
- Spec §7 tests: minify threading (T1), Dependents (T2), reverse-closure regen (T4), equivalence + multi-module + dep-change (T5), existing watch tests adapted (T3/T5), corpus (T1/T5).
- Names: `Options.CSSMin/JSMin/CSSMinify/JSMinify`, `Module.Dependents`, `watchSession.modules`, `openModule`, `moduleForDir`, `regenDir`, `reopen`. Consistent.
