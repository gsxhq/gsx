# Universal Element-Spread Sanitization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every element spread `{ x... }` sanitize URL-classified keys by context (element tag + attr name) regardless of the bag's provenance, per `docs/superpowers/specs/2026-07-11-universal-spread-sanitization-design.md` — closing the four demonstrated XSS holes in #75.

**Architecture:** The sanitizing machinery (`emitManualSpreadElement` → `SpreadForwarding`) already exists and works on any `gsx.Attrs` expression. The fix routes *every* element spread through it by making `bagSpreadIndex` match any `SpreadAttr` (not just recognized forwarding bases), then removes what becomes dead: the plain unsanitizing `Spread` emit paths + the exported `gw.Spread` method, and the now-unused `bagBases` recognition machinery. Behavior change first (reviewable, corpus-pinned), mechanical deletions after (goldens unchanged).

**Tech Stack:** Go, `internal/codegen` (emit.go), root `gsx` runtime, txtar corpus.

## Global Constraints
- Worktree `.claude/worktrees/spread-sanitize-vuln`, branch `worktree-spread-sanitize-vuln`. EVERY bash command starts with `cd /Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/spread-sanitize-vuln && `; verify `git branch --show-current` before committing.
- Runtime (root `gsx`) is stdlib-only. `SpreadForwarding` is the sole spread emission after this work; `gw.Spread` is deleted (no unsanitizing primitive left — pre-adoption tech-debt rule).
- Stale Read/Edit cache has bitten prior agents in these worktrees: if a Read's line count disagrees with `wc -l`, re-Read or verify with `sed -n`/`grep` before editing.
- Never hand-edit goldens: regen `go test ./internal/corpus -run TestCorpus -update`, verify without `-update`.
- Security is the gate: after Task 1, `grep -rn 'javascript:' internal/corpus/testdata/cases/**/render.golden` must return ZERO matches on `href`/`src`/`action`/etc. (every URL sink neutralized to `about:invalid#gsx`), except values an author explicitly wrapped in `RawURL`.
- Inner loop `go test ./internal/codegen ./internal/corpus .`; per task `make check`; before merge `make ci` + `make lint`.
- Commit per task, conventional messages + trailer `Claude-Session: https://claude.ai/code/session_01CNYE1sfyXuf7taK4mfUiqa`.

## Anchors (current at branch HEAD 0e701fa)
- Element dispatch: `emit.go:1749` (`bagSpreadIndex(t.Attrs, bagBases)`); `found` branch + second-spread error loop `1752-1766`; `emitManualSpreadElement` call `1767`; normal-attr loop (no spread) `1769-1774`.
- `bagSpreadIndex` `emit.go:1416-1428`; `spreadMatchesAnyBase` `1431-1440`; `spreadMatchesBase` `1450+`.
- `bagBases` build: `emit.go:602` (`[]string{"attrs"}`), `605` (byo `.Attrs`), `621` (named param); threaded through `emitNodeFuncBody`/`genNode`/`emitManualSpreadElement`/`emitNodeValue`/`interpEmitCtx` (field at ~line 1803).
- Dead-after-Task-1 plain-`Spread` emit sites: `emitAttr` SpreadAttr case `emit.go:2651-2670`; `emitFallthroughAttrs` non-splitIdx SpreadAttr `emit.go:1179-1196`.
- Runtime: `Spread` `attrs.go:278`; `SpreadForwarding` (the def, same file); test/bench callers `gsx_test.go:36`, `root_attr_bench_test.go:36`, `root_attr_bench_test.go:69`.
- Existing corpus cases that INVERT: `internal/corpus/testdata/cases/fallthrough/local_bag_inline.txtar` (pins local bags STAYING inline — the exact behavior this reverses), `internal/corpus/testdata/cases/nonce/spread_in_cond_attr_on.txtar` (nonce + spread). Grep all `_gsxgw.Spread(` pinners: `grep -rl '_gsxgw.Spread(' internal/corpus/testdata/cases/`.
- The five demonstration pins live at `internal/corpus/testdata/cases/spread-sanitize/` (four VULN currently rendering raw `javascript:`, one SAFE control rendering `about:invalid#gsx`).

---

### Task 1: universal routing — every element spread sanitizes

**Files:**
- Modify: `internal/codegen/emit.go` (`bagSpreadIndex` 1416-1428; the dispatch `found`/second-spread logic 1752-1766)
- Rewrite: the five `cases/spread-sanitize/*.txtar` (flip VULN→sanitized, drop VULN framing)
- Modify: `cases/fallthrough/local_bag_inline.txtar`, `cases/nonce/spread_in_cond_attr_on.txtar` (they invert)
- Create: `cases/spread-sanitize/{merge_not_duplicate,rawurl_local_bag,derived_local_bag,two_spreads_error}.txtar`

**Interfaces:**
- Produces: `bagSpreadIndex(attrs []ast.Attr) (int, bool, error)` — signature drops `bagBases`; returns the index of THE single element spread, `found=false` if none, error if >1. (Task 3 removes `bagBases` everywhere; Task 1 just stops `bagSpreadIndex` reading it — leave the now-unused `bagBases` param threaded until Task 3 to keep this diff focused. To avoid a Go "unused parameter" issue: `bagBases` stays a *parameter* of the other functions, only `bagSpreadIndex` drops it — parameters may be unused in Go, so no compile break.)

- [ ] **Step 1: Confirm RED.** `go test ./internal/corpus -run 'TestCorpus/spread-sanitize' -count=1` passes today (the 4 VULN goldens pin raw `javascript:`). Record: those four render `href/src="javascript:alert(1)"`; the control renders `about:invalid#gsx`.

- [ ] **Step 2: Change `bagSpreadIndex` to match any spread.** Replace its body (emit.go:1416-1428) so it matches every `*ast.SpreadAttr` regardless of `bagBases`:

```go
// bagSpreadIndex returns the index of THE element spread, and whether one is
// present. Every element spread is a forwarding spread (a sink): it routes
// through emitManualSpreadElement's URL-sanitizing / class-merge machinery
// regardless of what the bag expression is. An element carries at most one
// spread; a second is a precedence-ambiguous error.
func bagSpreadIndex(attrs []ast.Attr) (int, bool, error) {
	idx, found := -1, false
	for i, a := range attrs {
		if _, ok := a.(*ast.SpreadAttr); !ok {
			continue
		}
		if found {
			return 0, false, fmt.Errorf("codegen: more than one spread on an element; precedence is ambiguous")
		}
		idx, found = i, true
	}
	return idx, found, nil
}
```

Update its single call site (emit.go:1749) to drop the `bagBases` arg: `bagSpreadIndex(t.Attrs)`. The `found`-branch second-spread loop (1752-1766) stays — but now note: with `bagSpreadIndex` erroring on >1 spread directly, that loop is redundant for the >1 case; keep it only if it produces a better-positioned message, else simplify. Read it and decide; if kept, its wording ("element with an attrs-forwarding spread cannot carry another spread … merge them into one spread ({ a.Merge(b)... })") is still correct and is what `two_spreads_error.txtar` pins.

- [ ] **Step 3: Flip the four VULN cases + fix inverters.** Regenerate: `go test ./internal/corpus -run TestCorpus -update`. Then:
  - The four `spread-sanitize/*_unsanitized.txtar` render goldens now show `about:invalid#gsx` (nav) / `about:invalid#gsx` (img via URLImage). Rename them (drop `_unsanitized`), rewrite the header comments from "VULN … pins the HOLE" to "a spread onto an element sanitizes URL keys regardless of provenance".
  - `local_bag_inline.txtar`: its whole premise (local bags stay inline, duplicate-emitting) inverts. Rewrite it to pin the NEW behavior (local bag now merges + sanitizes) or delete it and let the new `spread-sanitize` cases cover it — read it first; if it pinned a duplicate-attr artifact, that artifact is now gone.
  - `nonce/spread_in_cond_attr_on.txtar`: verify it still generates + renders correctly (the spread now routes through the forwarding path; the nonce guard must still emit). If its generated golden changed shape, confirm the nonce is still injected.

- [ ] **Step 4: New corpus cases** (each on the plain-element spread path this fixes):
  - `merge_not_duplicate.txtar`: `<a class="base" { b... }>` with a local `b := gsx.Attrs{{Key:"class",Value:"x"}}` → render `<a class="base x">…` (merged, not `class="base" class="x"`).
  - `rawurl_local_bag.txtar`: local bag `{{Key:"href", Value: gsx.RawURL("app://ok")}}` → renders `href="app://ok"` (RawURL opt-out passes).
  - `derived_local_bag.txtar`: `{{ b := gsx.Attrs{…} }}` then `{ b.Without("id")... }` on `<a>` with an `href` → hoisted once (`_gsxvN := b.Without("id")`), sanitized.
  - `two_spreads_error.txtar`: `<a { b1... } { b2... }>` two local bags → the one-spread diagnostic (diagnostics-only case; let `-update` capture wording).

- [ ] **Step 5: Golden audit (load-bearing).** `git status internal/corpus/testdata` + inspect EVERY changed existing golden. Each changed render.golden must be exactly: a duplicate `class`/attr → merged, OR an unsafe URL → `about:invalid#gsx`. Any other movement = STOP, diagnose. Run the security gate grep (Global Constraints). Quote 3-4 representative changed goldens in the report.

- [ ] **Step 6:** `go test ./internal/codegen ./internal/corpus . -count=1` + `make check` green. Commit `feat(codegen): every element spread sanitizes URL keys at the leaf (#75)`.

---

### Task 2: remove `gw.Spread` and the dead emit sites

**Files:**
- Modify: `internal/codegen/emit.go` (delete emitAttr SpreadAttr plain-Spread case 2651-2670; delete emitFallthroughAttrs non-splitIdx SpreadAttr emit 1179-1196)
- Modify: `attrs.go` (delete `Spread` method), `gsx_test.go`, `root_attr_bench_test.go` (migrate callers)

**Interfaces:**
- Consumes: after Task 1, no golden contains `_gsxgw.Spread(` — verify first: `grep -rl '_gsxgw.Spread(' internal/corpus/testdata/cases/` returns EMPTY. If not empty, a spread path still emits plain Spread — STOP, that element spread isn't routing through Task 1's change (investigate before deleting anything).

- [ ] **Step 1: Prove the emit sites are dead.** With Task 1 merged, `grep -rn '_gsxgw.Spread(' internal/corpus/testdata/cases/` → empty. Add a temporary panic in each of the two sites (2667, 1192), run the full corpus (`go test ./internal/corpus -run TestCorpus -count=1`) — it must pass without hitting the panic (confirms unreachable). Remove the panics.
- [ ] **Step 2:** Delete the `emitAttr` `*ast.SpreadAttr` case (emit.go ~2651-2670) entirely — an element spread never reaches `emitAttr` now (it routes to `emitManualSpreadElement`). Delete the `emitFallthroughAttrs` non-splitIdx `SpreadAttr` inline-Spread block (~1179-1196); replace with a `continue` or a positioned internal-error (a second spread should have errored at dispatch) — read the surrounding case and pick the minimal correct handling.
- [ ] **Step 3:** Delete `func (gw *Writer) Spread` from `attrs.go` (keep `SpreadForwarding`, `lastValidAttrIndexes`, `validAttrName`, `attrNameExcluded`, `URLPrefixMatch` — all used by `SpreadForwarding`). Grep confirms no remaining `.Spread(` references except `SpreadForwarding`.
- [ ] **Step 4:** Migrate the three test/bench callers to `SpreadForwarding`: `gsx_test.go:36` (`gw.Spread(ctx, attrs)` → `gw.SpreadForwarding(ctx, attrs, nil, nil, nil, nil)` and update the expected output if the test asserted unsanitized behavior — if the test's purpose was specifically the old unsanitizing contract, replace it with a `SpreadForwarding` behavior test); `root_attr_bench_test.go:36,69` → `SpreadForwarding(ctx, a, nil, nil, nil, []string{"class","style"})` (or delete if redundant with `BenchmarkForwardingLeafNoURL` — note which).
- [ ] **Step 5:** `go build ./...`, `go test ./internal/codegen ./internal/corpus . -count=1`, `make check` green. Commit `refactor(codegen): delete gw.Spread — SpreadForwarding is the sole spread emission`.

---

### Task 3: delete the `bagBases` recognition machinery

**Files:**
- Modify: `internal/codegen/emit.go` (remove `bagBases` build 602-624; `spreadMatchesBase`/`spreadMatchesAnyBase`; the `bagBases` param from every function that threads it + the `interpEmitCtx` field)

**Interfaces:**
- Pure mechanical deletion. No golden changes (Task 1 already made `bagBases` unread; this removes the dead threading). If ANY golden moves, `bagBases` was still doing something — STOP.

- [ ] **Step 1:** Delete `spreadMatchesBase` and `spreadMatchesAnyBase` (emit.go ~1431-1470). Delete the `bagBases` build block in `genComponent` (602-624) — the `[]string{"attrs"}` seed, the byo `.Attrs` append, the named-param loop. (`isGsxQualifiedType`/`gsxQuals` remain — used elsewhere; grep to confirm.)
- [ ] **Step 2:** Remove the `bagBases []string` parameter from every function that carries it — `genNode`, `emitNodeFuncBody`, `emitNodeValue`, `emitManualSpreadElement`, `emitElementValue`, `emitFragmentValue`, `emitSlotClosure`, and the `interpEmitCtx` struct field — and every call site. A missed site is a compile error (good — it surfaces). Grep `bagBases` to zero.
- [ ] **Step 3:** `go build ./...` clean. `go test ./internal/codegen ./internal/corpus . -count=1` — goldens BYTE-IDENTICAL to Task 2's state (regen with `-update`; `git diff` must be empty for testdata). `make check` green. Commit `refactor(codegen): remove dead bagBases forwarding-recognition machinery`.

---

### Task 4: docs + contract

**Files:**
- Modify: `attrs.go` (`Attrs`/`SpreadForwarding` godoc), `docs/guide/syntax/attributes.md`, `props.md`, `composition.md`, `docs/ROADMAP.md`

- [ ] **Step 1:** `attrs.go` — the `Attrs` type's security note ("values are … NOT URL-sanitized … must be written with gw.URL, not passed through Spread") is now false; rewrite to: a spread onto an element sanitizes URL-classified keys by context; `gsx.RawURL` (per value) opts out; there is no unsanitizing spread primitive. Remove any mention of `Spread`.
- [ ] **Step 2:** `attributes.md`/`props.md`/`composition.md` — drop the provenance caveats (local-var / byo-second-field / dot-import "stay inline / unsanitized"). State the universal rule: every element spread sanitizes; `RawURL` opts out. Keep `{{ }}` in `::: v-pre`/fences (grep-verify).
- [ ] **Step 3:** `docs/ROADMAP.md` — strike the #75 items now fixed (local-var bag sanitization, byo second field, dot-import) from the bag-hardening known-gaps list; note them resolved by this change.
- [ ] **Step 4:** `make check` green. Commit `docs: element spreads sanitize universally; gw.Spread removed`.

---

### Task 5: final validation + PR

- [ ] **Step 1:** `make ci` + `make lint` green at HEAD. `git diff origin/main --stat`: only `internal/codegen/emit.go`, `attrs.go` (+ its tests/benches), `internal/corpus/testdata`, `docs/`, spec/plan.
- [ ] **Step 2:** Adversarial probe pass (throwaway probe programs, not diff-reading): the four original XSS forms now neutralized; multi-hop laundering through a local bag; case-variant keys (`HREF`) in a local bag; `RawURL` at each position; an element with a static URL attr + a local-bag URL attr (precedence: which wins, both safe); two-spread rejection; a derived local bag single-eval; a bag with a `class` + static `class` merge; `gsx fmt` idempotence over the new spellings.
- [ ] **Step 3:** one-learning revalidation (throwaway worktree, its `url_presets=["htmx"]`): its templates use local/derived bags; render-diff a few bag-heavy pages old-vs-new — every delta must be safe-URL-neutralization or class-merge; `gsx generate` + `go build ./...` + `go test ./ui/...` parity.
- [ ] **Step 4:** Final whole-branch review (the pattern that keeps catching cross-cutting defects); fix wave if needed.
- [ ] **Step 5:** PR via `superpowers:finishing-a-development-branch`: title `fix: element spreads sanitize URL keys regardless of provenance (#75)`, body = the demonstrated-holes table + the model + the `gw.Spread` removal + the one-spread behavior change. Closes #75 (local-var, byo-second-field, dot-import items).
