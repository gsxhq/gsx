# Warm `.go`-edit Watch Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `.go` saves in `gsx dev` stop tearing down the watch session: the Module marks an in-place world reload (fixing a latent LSP staleness bug), only the edited package's dependent closure regenerates, and the reason is visible on console and panel.

**Architecture:** Spec `docs/superpowers/specs/2026-08-12-warm-go-edit-watch-design.md` (approach A, probe-corrected premise). The disk refresh path learns what the override path already does (`goSourceReload`), `RefreshDiskSourcesAndInvalidate` returns an explicit verdict, and the watch loop routes authored-`.go` writes as ordinary source events instead of `depDirty → reopen()`.

**Tech Stack:** Go 1.26.1 (pin — see `GO_VERSION` in ci.yml), stdlib-only runtime; tooling may use `golang.org/x/tools`.

## Global Constraints

- Never run `make ci`/`go test ./...` as an inner loop; run the single named test. `make ci` runs once at final review and its exit code must reach the merge decision.
- Every behavioral change ships a test in the same task. Watch tests open ~1 Module (~0.3s each); prefer extending a fixture's table over a new module-opening test.
- Correctness bar from the spec: unsure → escalate (reload); stale generated output is never acceptable.
- gsx-owned paired `.x.go` writes must NEVER mark a Go reload (self-write storm).
- All work in the `worktree-warm-go-edits` worktree. Commit after every task.

---

### Task 1: Disk `.go` changes mark the world reload (Module)

Fixes the probe-proven staleness: a `.go` disk edit routed through `RefreshDiskSourcesAndInvalidate` currently leaves warm analysis serving stale types. Mirror the override path (`internal/codegen/module.go:443-445`: `changed .go → m.sourceManifestEpoch++; m.goSourceReload = true`) inside `refreshDiskSources`.

**Files:**
- Modify: `internal/codegen/source_inventory.go` (the `m.mu.Lock()` publish block of `refreshDiskSources`, after `m.helperGoSourceManifest = effective`)
- Test: `internal/codegen/go_disk_reload_test.go` (new; one Module fixture, table-driven)

**Interfaces:**
- Consumes: `sourceview.Manifest.HelperGoFiles(dir) map[string]FileSnapshot` (per-dir authored-Go snapshots, clones on exit), `Manifest.PairedOutputs() []string`, existing fields `m.goSourceReload`, `m.sourceManifestEpoch`.
- Produces: after `RefreshDiskSourcesAndInvalidate(dir)` where dir's authored `.go` content or membership changed on disk, the next `Generate`/`Package` performs the in-place world reload (existing `sourceInventoryDirty` machinery — no new consumer API yet; Task 2 adds the verdict).

- [ ] **Step 1: Write the failing test** — the probe from the design session, promoted:

```go
package codegen

// TestRefreshDiskSourcesMarksGoReload pins the disk counterpart of the
// override rule at module.go:443-445: authored .go content or membership
// changes observed by a disk refresh must reload the cold world at the next
// analysis. Before this task the warm world kept serving stale types (a
// removed exported symbol produced no diagnostic in a dependent's regen).
func TestRefreshDiskSourcesMarksGoReload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDirForTest(t)+"\n")
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v1\" }\n")
	write("page/page.gsx", "package page\n\nimport \"example.com/m/dep\"\n\ncomponent Page() {\n\t<p>{dep.Value()}</p>\n}\n")
	m := openTestModule(t, root) // codegen.Open + first Generate on page/ must succeed

	if out, diags, err := m.Generate(filepath.Join(root, "page")); err != nil || anyErrorDiagFor(t, diags) || len(out) == 0 {
		t.Fatalf("baseline generate: out=%d diags=%v err=%v", len(out), diags, err)
	}

	// Case 1: content change removing the symbol → dependent regen must diagnose.
	write("dep/dep.go", "package dep\n\nfunc Hidden() string { return \"v2\" }\n")
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	_, diags, _ := m.Generate(filepath.Join(root, "page"))
	if !diagMentions(diags, "Value") {
		t.Fatalf("stale-blind: removed dep.Value produced no diagnostic; diags=%v", diags)
	}

	// Case 2: restoring the symbol heals on the same warm module.
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v3\" }\n")
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	if _, diags, _ := m.Generate(filepath.Join(root, "page")); diagMentions(diags, "Value") {
		t.Fatalf("reload did not pick the restored symbol back up: %v", diags)
	}

	// Case 3: byte-identical rewrite must NOT mark a reload (no epoch churn).
	epoch := m.testSourceManifestEpoch() // add tiny export_test.go accessor
	write("dep/dep.go", "package dep\n\nfunc Value() string { return \"v3\" }\n")
	if _, err := m.RefreshDiskSourcesAndInvalidate(filepath.Join(root, "dep")); err != nil {
		t.Fatal(err)
	}
	if got := m.testSourceManifestEpoch(); got != epoch {
		t.Fatalf("byte-identical .go rewrite bumped the manifest epoch %d -> %d", epoch, got)
	}
}
```

Add `internal/codegen/export_test.go` accessors: `func (m *Module) testSourceManifestEpoch() uint64` (lock, return `m.sourceManifestEpoch`) and helpers `diagMentions`, `anyErrorDiagFor`, `gsxModuleDirForTest`, `openTestModule` if not already present in the package's tests (grep first; reuse existing ones — `module_test.go` has equivalents).

- [ ] **Step 2: Run it, verify Case 1 fails** — `go test ./internal/codegen -run TestRefreshDiskSourcesMarksGoReload -count=1 -v` → FAIL at "stale-blind".

- [ ] **Step 3: Implement.** In `refreshDiskSources`'s publish block (after `m.helperGoSourceManifest = effective`), compare per refreshed dir the OLD saved manifest's authored-Go view with the refreshed one, excluding gsx-owned paired outputs:

```go
// Disk counterpart of the override rule at module.go:443-445: a changed or
// added/removed authored .go file invalidates the retained cold world's
// types, which only an inventory reload can refresh. Paired generated
// outputs are excluded — the session's own .x.go writes must never reload.
if goSourceChangedInDirs(saved, refreshed, dirSet) {
	m.sourceManifestEpoch++
	m.goSourceReload = true
	m.sourceInventoryDirty = true
}
```

with (same file):

```go
// goSourceChangedInDirs reports whether any authored .go snapshot in dirs
// differs between two manifests. nil old means first publication: nothing
// was retained yet, so nothing can be stale.
func goSourceChangedInDirs(old, new *sourceview.Manifest, dirs map[string]bool) bool {
	if old == nil {
		return false
	}
	paired := func(m *sourceview.Manifest) map[string]bool {
		out := map[string]bool{}
		for _, p := range m.PairedOutputs() {
			if dirs[filepath.Dir(p)] {
				out[p] = true
			}
		}
		return out
	}
	oldPaired, newPaired := paired(old), paired(new)
	for dir := range dirs {
		oldGo, newGo := old.HelperGoFiles(dir), new.HelperGoFiles(dir)
		for path := range oldGo {
			if oldPaired[path] || newPaired[path] {
				delete(oldGo, path)
			}
		}
		for path := range newGo {
			if oldPaired[path] || newPaired[path] {
				delete(newGo, path)
			}
		}
		if len(oldGo) != len(newGo) {
			return true
		}
		for path, oldSnap := range oldGo {
			newSnap, ok := newGo[path]
			if !ok || oldSnap.State() != newSnap.State() {
				return true
			}
			oldSrc, _ := oldSnap.Source()
			newSrc, _ := newSnap.Source()
			if !bytes.Equal(oldSrc, newSrc) {
				return true
			}
		}
	}
	return false
}
```

Note: `HelperGoFiles`/`Source` clone — per-dir cost only, fine. `paired` must consider BOTH manifests so a just-deleted `.gsx`'s orphaned output doesn't read as an authored-file appearance in the same cycle.

- [ ] **Step 4: All three cases pass** — same command. Also run `go test ./internal/codegen -run 'Refresh|Override' -count=1` and `go test ./internal/lsp -count=1` (the LSP watched-file flow now heals — a behavior fix, watch for newly-failing assumptions in lsp tests and fix the TEST only if it asserted the stale behavior).

- [ ] **Step 5: Commit** — `git add -A && git commit -m "fix(codegen): disk .go changes reload the cold world like override transitions"`.

---

### Task 2: Refresh verdicts

**Files:**
- Modify: `internal/codegen/source_inventory.go` (`RefreshDiskSourcesAndInvalidate` signature), `internal/sourceview/manifest.go` (add `ReloadGoSource` to the `ReloadReason` enum + `func (ReloadReason) String() string`)
- Modify call sites: `gen/lsp.go:553`, `gen/watchsession.go` (`regenDirs` batch + fallback, `regenPending` pending loop)
- Test: extend `internal/codegen/go_disk_reload_test.go` with a verdict table

**Interfaces:**
- Produces (exact, later tasks depend on it):

```go
// internal/codegen
type RefreshVerdict struct {
	WorldReloadPending bool
	Reason             sourceview.ReloadReason // ReloadGoSource for authored Go changes
	Path               string                  // representative file that forced the reload; "" when unknown
}
func (v RefreshVerdict) Describe() string // e.g. `changed Go source dep/dep.go` / `new import outside the loaded world in page/page.gsx`
func (m *Module) RefreshDiskSourcesAndInvalidate(dirs ...string) ([]string, RefreshVerdict, error)
```

- Verdict computation (inside the same critical section that publishes the refresh): `WorldReloadPending = m.goSourceReload || len(m.sourceReloadReasons) != 0`. Reason/Path attribution, deterministic: if this call flipped `goSourceReload`, `ReloadGoSource` + the lexicographically-first differing `.go` path from Task 1's comparison (extend `goSourceChangedInDirs` to also return that path); else the lexicographically-first entry of `m.sourceReloadReasons`. A verdict can be pending from an EARLIER refresh whose reload has not landed yet — that is correct (persistence), and `Describe` still renders the stored attribution: keep the last attribution on the Module (`m.reloadAttribution struct{reason; path}`, cleared where `goSourceReload` is cleared, `module.go:823` and `module.go:1275`).
- `sourceview.ReloadReason` gains `ReloadGoSource` and `String()` (`"none"`, `"membership"`, `"package"`, `"imports"`, `"go-source"`).

- [ ] **Step 1: Failing test** — verdict truth table on the Task 1 fixture (extend, no new Module):

```go
// rows: {edit func, wantPending bool, wantReason sourceview.ReloadReason}
// 1. .go content change            → pending, ReloadGoSource
// 2. byte-identical .go rewrite    → NOT pending (fresh module: no prior pending)
// 3. .go file added                → pending, ReloadGoSource
// 4. .go file removed              → pending, ReloadGoSource
// 5. .gsx body-only edit           → not pending
// 6. .gsx gains import outside the loaded world → pending, ReloadImports
// 7. pending persists: after row 6 with NO Generate, refresh another dir
//    byte-identically → still pending (persistence until the reload lands)
// 8. after Generate (reload lands) → a fresh byte-identical refresh → not pending
```

- [ ] **Step 2: Verify it fails to compile** (two-value return today), then with a temporary shim confirm rows fail.
- [ ] **Step 3: Implement** signature + enum + attribution as specified; update both call sites mechanically (watchsession ignores the verdict for now with `_`; Task 4 consumes it), and update Task 1's test call sites from the two-value to the three-value return.
- [ ] **Step 4: Pass** — `go test ./internal/codegen -run TestRefreshDiskSourcesMarksGoReload -count=1`; `go build ./...`; `go test ./gen -run 'TestWatch' -count=1`; `go test ./internal/lsp -count=1`.
- [ ] **Step 5: Commit** — `feat(codegen): refresh verdicts — the Module reports pending world reloads`.

---

### Task 3: Watch routing — `.go` writes are source events

**Files:**
- Modify: `gen/watch.go` (`isDepFile`, `queueWatchSource` and its callers `applyWatchEvent`/`queueWatchTree`/`queueRequestedBranches`, `sourceTracker.reconcile`), `gen/watch_dirty.go` (`goDirty` flag), `gen/dev.go` (nothing — verify `goChanged` name still flows), `gen/watchsession.go` (no routing change; pending loop already handles go-only dirs: `onlyGeneratedRemains` + `Dependents`)
- Test: `gen/watch_go_edit_test.go` (new)

**Interfaces:**
- Consumes: Task 2's verdict-returning refresh.
- Produces: `watchDirtySet.goDirty bool`; `regenerate`'s second return becomes `rebuild := depDirty || goDirty` (still named `goChanged` at `gen/dev.go:342`); `isDepFile` = `go.mod`/`go.sum` only; authored `.go` writes (non-paired `.x.go` included) queue `pending[filepath.Dir(path)] = true` and set `goDirty`.

- [ ] **Step 1: Failing test:**

```go
// TestWatchSession_GoEditRegeneratesOnlyDependents: module with dep/ (go-only),
// page/ (imports dep), other/ (unrelated gsx dir).
// 1. Start session; startup OK.
// 2. Edit dep/dep.go changing Value's RESULT ("v1"→"v2" is body-only; keep it).
//    Simulate the loop: pending={dep}, goDirty=true → regenPending(pending,false).
// 3. Assert: results cover ONLY dep's reverse closure (page, and dep itself is
//    skipped: onlyGeneratedRemains), NOT other/ — closure-scoped regen.
// 4. Assert page regen OK with zero error diags (world reloaded in place).
// 5. Change dep.Value's signature (string→int) so page's text hole still
//    compiles ({dep.Value()} renders ints); assert page regenerates OK and its
//    generated bytes CHANGED (fresh types actually flowed).
// 6. Assert regenerate()'s second return (rebuild) is true for the goDirty
//    cycle even though depDirty stayed false.
```

Also table-extend for routing: `isDepFile("a/b.go") == false`, `isDepFile("go.mod") == true`, authored orphan `x.x.go` routes as source event not dep (existing `pairedGeneratedOutput` distinction — paired outputs keep being ignored entirely).

- [ ] **Step 2: Verify failure** — with today's routing the simulated `.go` event can only reach `depDirty`, so step-3's closure-scope assertion fails (all dirs regenerate via reopen).
- [ ] **Step 3: Implement:** `isDepFile` shrinks; `queueWatchSource` gains a `goDirty *bool` param threaded from every caller; `.go` classification: paired generated output → ignore (unchanged); `go.mod`/`go.sum` → `*depDirty = true`; other `.go` and `.gsx` → `pending[dir] = true`, and for `.go` also `*goDirty = true`. `watchDirtySet`: add `goDirty bool`, include in `regenerate`'s snapshot/clear/`retainOperational` exactly parallel to `depDirty` (a retained failed go-cycle must retry as a go-cycle).
- [ ] **Step 4: Pass** — `go test ./gen -run 'TestWatchSession_GoEditRegeneratesOnlyDependents|TestWatch' -count=1`; also `go test ./gen -run 'TestDev' -count=1` (dev harness cluster; server-rebuild path).
- [ ] **Step 5: Commit** — `feat(gen): .go edits regenerate the dependent closure in place instead of reopening the session`.

---

### Task 4: Observability — verdict reasons on console and panel

**Files:**
- Modify: `gen/watchsession.go` (`cycleResult` gains `Reload string`; `regenDirs` + `regenPending` stamp it from verdicts, on the module's FIRST result of the cycle, same convention as the refresh-duration charge), `gen/watchemit.go` (console line), `gen/devserver.go` (`aggregateEvent` gains `"reload"`), `gen/devstatus.go` (`cycleStat.Reload string \`json:"reload,omitempty"\``), `gen/dev.go` (thread into `status.LastCycle`)
- Test: extend `gen/watch_go_edit_test.go` + `gen/watchemit_test.go` table

**Interfaces:**
- Consumes: `RefreshVerdict.Describe()` from Task 2.
- Produces: `cycleResult.Reload` (empty when no reload); `generated` NDJSON event and `aggregateEvent` carry `"reload": "<describe>"` when non-empty; human line: `full reload: changed Go source dep/dep.go`.

- [ ] **Step 1: Failing tests** — watchemit table row asserting the human line and NDJSON field for a result with `Reload` set; watch test asserting the `.go`-edit cycle's first result carries `Reload` containing `"go source"` and the `.gsx`-edit cycle carries none.
- [ ] **Step 2: Verify failures.**
- [ ] **Step 3: Implement** (all sites above; `aggregateEvent`: first non-empty `Reload` wins).
- [ ] **Step 4: Pass** — `go test ./gen -run 'TestWatchEmit|TestWatchSession_GoEdit' -count=1`.
- [ ] **Step 5: Commit** — `feat(gen): surface world-reload reasons in watch emit and dev status`.

---

### Task 5: Complexity gates

**Files:**
- Modify: `internal/codegen/sharedworld.go` or the single project-load site (`sharedworld.go:239`) — add `var projectLoads atomic.Uint64` + `func ProjectLoadCalls() uint64` counting every `packages.Load` the codegen package issues (all 5 sites found by `grep -n "packages.Load(" internal/codegen`; one shared counter incremented adjacent to each call)
- Test: extend `gen/watch_perf_test.go`

**Interfaces:**
- Produces: `codegen.ProjectLoadCalls() uint64` (process-wide, same contract as `sourceview.InspectCalls`).

- [ ] **Step 1: Failing test** — in the non-parallel `TestWatchSession_ColdStartParseWorkIsLinear` style (min-of-two on breach):

```go
// TestWatchSession_EditLoadBudget (NOT t.Parallel; counter is process-wide):
// fixture: dep/ + page/ + 10 filler gsx dirs.
// budget assertions after a settled cold start:
//   .gsx body edit cycle        → ProjectLoadCalls delta == 0
//   .go body edit cycle         → delta bounded by the loads ONE in-place
//     reload performs (measure on the fixture, assert an exact small constant,
//     document it in the test — the point is it must not scale with dirs)
//   second .go edit cycle       → same constant (no leak toward reopen-like behavior)
```

- [ ] **Step 2: Verify the `.gsx` row passes and the `.go` rows FAIL before Task 3 is merged** — if executing tasks in order, this task lands after Task 3, so instead verify the budgets hold and pin them.
- [ ] **Step 3: Implement counter + budgets.**
- [ ] **Step 4: Pass** — `go test ./gen -run 'TestWatchSession_EditLoadBudget|TestWatchSession_ColdStartParseWorkIsLinear' -count=1`.
- [ ] **Step 5: Commit** — `test(gen): pin load budgets for edit cycles`.

---

### Task 6: Panel display (sibling repo `../vite-plugin-gsx`)

**Files:**
- Modify: `../vite-plugin-gsx/src/client.ts` (status line rendering — the `lastCycle`/`durationMs` formatter found via `grep -rn "last cycle" src/`) and the changelog; version bump per that repo's release convention (0.x caret — see repo docs).

**Interfaces:**
- Consumes: `status.lastCycle.reload` and `generated.reload` from Task 4.

- [ ] **Step 1:** Render `— full reload: <reason>` after the cycle duration when `reload` is non-empty (both the status line and the generated-event toast, same formatter).
- [ ] **Step 2:** `npm test` / repo's check script; manual: run `gsx dev` against a scratch project, edit a `.go` file, confirm the panel shows the reason.
- [ ] **Step 3:** Commit in the sibling repo; do NOT tag/release without the user (release workflow is tag-gated).

---

### Task 7: Real-world A/B + docs + gates + review

- [ ] **Step 1:** gsxui A/B in a scratchpad `git clone --local` (never the live checkout): baseline main gsx vs this branch; measure cold start, `.gsx` edit, `.go` body edit, `.go` signature edit via the event-sink technique (`evsink` pattern from the 2026-08-12 session: dev with `--no-web`, sink attached after boot on VITE_PORT). Record numbers in the PR body. Expected: `.go` edit 4.6s → ~2.5–3s, closure-scoped results, reasons visible.
- [ ] **Step 2:** Docs: `docs/guide/` dev-loop page — one short paragraph on reload reasons (keep concise per feedback memory); `docs/ROADMAP.md` — mark this shipped, note Phase 2 next.
- [ ] **Step 3:** Independent adversarial review with live probes (workflow, 4 lenses: staleness/reload-marking, routing/goDirty transactions, verdict attribution & persistence, emit/protocol), fix confirmed findings.
- [ ] **Step 4:** `make ci` (capture exit code directly — never through a pipe) and `make lint`; then PR with the A/B table; merge only on green.

---

### Task 8 (Phase 2 opener): reopen-cost measurement

- [ ] **Step 1:** Instrumented build printing per-phase timings of one in-place reload on the gsxui clone: shared-world hit/miss, project `packages.Load` (`sharedworld.go:239`) wall time and package count split main-module vs external-not-in-shared-world, per-dir regen sum.
- [ ] **Step 2:** Write findings + proposed Phase-2 shape (extend world sharing to the full external closure, keyed go.mod/go.sum + replace-dirs + vendor mode — the #178 vendor caveat) into `docs/superpowers/specs/2026-08-12-warm-go-edit-watch-design.md` as a dated addendum, and STOP for user review before any Phase-2 implementation.
