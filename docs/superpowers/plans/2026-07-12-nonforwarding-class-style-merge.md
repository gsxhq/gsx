# Non-Forwarding Class/Style Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close #96 by merging every same-name class/style contribution into one attribute on non-forwarding elements.

**Architecture:** Add a source-tree contributor counter to `elementFolds`; fold when class or style has more than one possible contributor, even without spreads. Reuse `composeBag`, `AttrsCond`, `Attrs.Class`, and `StyleMerged`; keep single-contributor fast paths unchanged.

**Tech Stack:** Go, gsx codegen, txtar corpus.

## Global Constraints

- Class aggregates tokens; style aggregates declarations with source-order last-wins per property.
- Emit at most one class and one style attribute.
- Contextual escaping, URL-sink diagnostics, RawCSS, renderer, tuple-error, and evaluation-order semantics remain unchanged.
- `elementFolds` stays shared with numeric prescan.
- Runtime and parser APIs remain unchanged; no dependency or `packages.Load` addition.
- Generated goldens are regenerated, never hand-edited.

---

### Task 1: Fold same-name class/style contributors

**Files:**
- Modify: `internal/codegen/emit.go`.
- Replace/extend: `internal/corpus/testdata/cases/condmerge/nonforwarding_unchanged.txtar`.
- Create: `internal/corpus/testdata/cases/condmerge/nonforwarding_merge.txtar`, `nonforwarding_merge_forms.txtar`, `nonforwarding_merge_order.txtar`.
- Modify generated coverage/goldens.

**Interfaces:**
- Produce `classStyleContributorCounts(attrs []ast.Attr) (class, style int)` or an equivalent typed result.
- `elementFolds` adds `class > 1 || style > 1` to existing spread triggers.

- [ ] **Step 1: RED issue reproduction**

Add `<div style="color:red" { if a { style="margin:0" } }>x</div>` and class analog. Invoke taken/untaken states. Run targeted corpus; expect the taken render to contain duplicate attributes.

- [ ] **Step 2: Implement real contributor counting**

Walk top-level attrs in source order and recurse through `CondAttr` Then/Else. Count named `class`/`style` leaves across `StaticAttr`, `ClassAttr`, and every `EmbeddedAttr` language. Use same-name counts, not a broad root+conditional boolean. Add `class > 1 || style > 1` to `elementFolds`; update its documentation and preserve existing spread conditions.

- [ ] **Step 3: GREEN base cases**

Regenerate targeted cases and verify exact single attributes: `class="base on"`, `style="color:red; margin:0"`. Pin generated `AttrsCond` + single folded leaf.

- [ ] **Step 4: Full form matrix**

Add exact render/generated coverage for: three root styles with duplicate property; else/else-if; embedded CSS static/hole; composable class/style; renderer; pipeline; `(T,error)`; untaken branch laziness; prior/error/late evaluation order; numeric sibling. Add negative controls proving one conditional contributor and root class + conditional style remain inline.

- [ ] **Step 5: Differential and prescan tests**

Extend `internal/codegen/spread_fold_diff_test.go` with no-spread same-name shapes and independent expected renders. Add a generated-code assertion that the numeric sibling does not declare unused `_gsxnum`.

- [ ] **Step 6: Regenerate and verify**

Run full corpus update, inspect churn, then `go test ./internal/codegen ./internal/corpus -count=1`, `gopls check -severity=hint internal/codegen/emit.go`, and `git diff --check`.

- [ ] **Step 7: Commit**

`git add internal/codegen internal/corpus/testdata && git commit -m "fix(codegen): merge non-forwarding class/style contributors"`

---

### Task 2: Benchmark, docs, and final verification

**Files:**
- Modify: `cond_merge_bench_test.go`, `docs/guide/syntax/composition.md`, `docs/ROADMAP.md`.

- [ ] Add benchmark cases for unchanged single-style inline path and new no-spread conditional merge path; assert allocations and report relative evidence without adding thresholds.
- [ ] Update composition guide with uniform class/style rule and ROADMAP with #96 fixed.
- [ ] Run `make ci` outside sandbox and `make lint`.
- [ ] Dispatch an independent adversarial reviewer with throwaway static/composable/embedded/renderer/tuple, duplicates, else-if, escaping, URL-sink, and evaluation probes.
- [ ] Fix findings, rerun gates, push branch, and open a draft PR closing #96.

## Self-review

- Spec coverage: contributor semantics and all forms Task 1; performance/docs/adversarial publish Task 2.
- No placeholders or conditional design choices.
- Contributor counts are same-name and source-tree based; unrelated class/style do not trigger folding.
