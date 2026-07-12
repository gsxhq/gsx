# Lift D3 — conditional class/style merge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A `class`/`style` inside an `{ if … }` cond-attr on a forwarding element merges (via the fold) instead of being a generate-time error (D3).

**Architecture:** Extend the `elementFolds` predicate so a forwarding element carrying a cond-attr with a `class`/`style` leaf routes through the existing fold (`foldElementSpreads`/`composeBag`), where the conditional becomes an `AttrsCond` bag entry aggregated at the leaf via `Attrs.Class()`/`Attrs.Style()`. The D3 static validation then becomes dead and is deleted. No new lowering logic — the fold already merges all forms (verified by probe). Builds on #91 (merged) incl. its source-evaluation-order fix.

**Tech Stack:** Go; `internal/codegen/emit.go`; txtar corpus; `testing.F` differential fuzz.

**Spec:** `docs/superpowers/specs/2026-07-12-lift-d3-conditional-class-merge-design.md`.

## Global Constraints

- Go pinned to `GO_VERSION` in `.github/workflows/ci.yml` (1.26.1).
- Runtime is std-lib only; **no new runtime API** (reuses `ConcatAttrs`/`AttrsCond`/`Spread`).
- Every codegen change ships a corpus case; per context (class + style; forwarding + non-forwarding).
- Never hand-edit `.x.go`/golden files: `go test ./internal/corpus -run TestCorpus -update`, then verify `-count=1`. **After adding a renderer/filter-bearing case, run a FULL `-update` (never filtered) — `_gsxfN` alias numbering is batch-global.**
- Prefer unexported identifiers; no "simple heuristics".
- Before merge: `make ci` (uncached; capture true exit via `make ci > log 2>&1; echo $?`, never pipe to `tail`) + `make lint`.
- Corpus: a **render** case has `-- invoke --` + empty `diagnostics.golden`; a **rejection** case omits `invoke` and populates `diagnostics.golden`.

## Reference (verified line numbers on base e344d83)

- `elementFolds(attrs []ast.Attr) bool` — `emit.go:1454`. Currently: `second != nil || (first != nil && firstCond != "" && hasRootClassStyle(attrs))`. Shared with the numeric prescan `scopeUsesNumeric` at `emit.go:2313` (`!elementFolds(t.Attrs) && attrsUseNumericScratch(...)`). Fold dispatch at `emit.go:1751` (`if elementFolds(t.Attrs) { return foldElementSpreads(...) }`).
- `firstTwoSpreadAttrs(attrs) (first, second *ast.SpreadAttr, firstCond string)` — `emit.go:1414`. `first != nil` ⇔ the element has ≥1 spread.
- `hasRootClassStyle(attrs) bool` — `emit.go:1468` (top-level class/style scan; does NOT recurse cond branches).
- **D3 validation** — `validateCondBranch` closure at `emit.go:876-909` + its driver loop `emit.go:910-916`, inside the single-spread forwarding emit path (`emitFallthroughAttrs`/`emitManualSpreadElement`). It errors `"conditional %s inside { if } … cannot join the %s merge…"` for a class/style leaf in a cond-attr branch. The fold path never calls it (proven: ≥2-spread conditional class merges).
- `foldElementSpreads` — `emit.go:1369`; `composeBag(..., ctx bagContext)` — `emit.go:5778` (handles ClassAttr/StaticAttr/EmbeddedText/CondAttr→AttrsCond; lowers composable `class={…}` incl. `:bool` ClassIf + `switch` ValueSwitch via `classEntryExpr`).
- `ast.CondAttr{Then, Else []Attr}`; `ast.ClassAttr{Name}`; `ast.StaticAttr{Name}`; `ast.EmbeddedAttr{Lang, Name}`.
- Corpus reject case to flip: `internal/corpus/testdata/cases/fallthrough/cond_attr_class_rejected.txtar`.

---

## Task 1: Extend the fold trigger + delete the dead D3 validation

**Files:**
- Modify: `internal/codegen/emit.go` (`elementFolds`; add `hasCondClassStyle`; delete `validateCondBranch` + driver)
- Corpus: flip `fallthrough/cond_attr_class_rejected.txtar` → render
- Test: `internal/corpus`, `internal/codegen`

**Interfaces:**
- Produces: `hasCondClassStyle(attrs []ast.Attr) bool` — true if a top-level `*ast.CondAttr` has (recursively, incl. else-if) a `class`/`style` leaf (`ClassAttr`/`StaticAttr`/`EmbeddedText` named class/style).

- [ ] **Step 1: Write the failing (currently-rejecting) corpus case**

Rewrite `internal/corpus/testdata/cases/fallthrough/cond_attr_class_rejected.txtar` to a render case (rename to `fallthrough/cond_attr_class_merges.txtar`):

```
# A class inside an { if } cond-attr on a forwarding element MERGES via the fold
# (D3 lifted: its per-context-escaping premise died with #79/#91).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Box(active bool, attrs gsx.Attrs) {
	<div class="base" { attrs... } { if active { class="on" } else { class="off" } }>x</div>
}
-- invoke --
Box(BoxProps{Active: true, Attrs: gsx.Attrs{{Key: "class", Value: "sp"}}})
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 2: Run it — expect the D3 rejection (RED)**

Run: `go test ./internal/corpus -run 'TestCorpus/fallthrough/cond_attr_class' -count=1`
Expected: FAIL — `conditional class inside { if } on an element with attribute forwarding cannot join the class merge; use the composable form (class={ "on": active }) instead`.

- [ ] **Step 3: Add `hasCondClassStyle` and extend `elementFolds`**

```go
// hasCondClassStyle reports whether attrs carries a class/style leaf inside a
// cond-attr branch (any depth, incl. else-if). Such a shape is what D3 used to
// reject on a forwarding element; routing it through the fold merges it via an
// AttrsCond bag entry aggregated at the leaf.
func hasCondClassStyle(attrs []ast.Attr) bool {
	var walk func(as []ast.Attr) bool
	walk = func(as []ast.Attr) bool {
		for _, a := range as {
			switch t := a.(type) {
			case *ast.CondAttr:
				if walk(t.Then) || walk(t.Else) {
					return true
				}
			case *ast.ClassAttr:
				if t.Name == "class" || t.Name == "style" {
					return true
				}
			case *ast.StaticAttr:
				if t.Name == "class" || t.Name == "style" {
					return true
				}
			case *ast.EmbeddedAttr:
				if t.Lang == ast.EmbeddedText && (t.Name == "class" || t.Name == "style") {
					return true
				}
			}
		}
		return false
	}
	// Only cond-attr-nested class/style counts (a top-level class/style is not a
	// D3 case); so walk begins one level down, at each top-level CondAttr.
	for _, a := range attrs {
		if c, ok := a.(*ast.CondAttr); ok {
			if walk(c.Then) || walk(c.Else) {
				return true
			}
		}
	}
	return false
}
```

Extend `elementFolds` (`emit.go:1456`):

```go
func elementFolds(attrs []ast.Attr) bool {
	first, second, firstCond := firstTwoSpreadAttrs(attrs)
	return second != nil ||
		(first != nil && firstCond != "" && hasRootClassStyle(attrs)) ||
		(first != nil && hasCondClassStyle(attrs)) // D3 lift: spread + cond-attr class/style
}
```

(`first != nil` ⇔ a spread is present, i.e. a forwarding element.)

- [ ] **Step 4: Delete the now-dead D3 validation**

Delete `validateCondBranch` (`emit.go:876-909`) and its driver loop (`emit.go:910-916`). After Step 3, every forwarding element with a cond-attr class/style folds, so this validation is unreachable. Confirm with `gopls check -severity=hint internal/codegen/emit.go` (no unused func / no dead code). If the closure references locals now unused, remove them too.

- [ ] **Step 5: Regenerate + verify the flipped case (GREEN)**

```bash
go test ./internal/corpus -run 'TestCorpus/fallthrough/cond_attr_class' -update
go test ./internal/corpus -run 'TestCorpus/fallthrough/cond_attr_class' -count=1
```
Expected: PASS; `render.golden` = `<div class="base sp on">x</div>` (base + spread's `sp` + conditional `on`, merged — confirm the exact order/dedup against `DefaultClassMerge` by hand before trusting).

- [ ] **Step 6: Prescan consistency probe**

Add a temporary scratch check (do NOT commit): a forwarding element with a cond-attr class AND a numeric attr — `<div class="b" data-n={n} { a... } { if c { class="on" } }>` — generate + `go build`; MUST compile (folds → numeric baked into bag, no `_gsxnum`; `scopeUsesNumeric` skips via the shared `elementFolds`). If it leaves an unused/undefined `_gsxnum`, the prescan tie broke — fix before proceeding.

- [ ] **Step 7: Full regression + differential + commit**

```bash
go test ./internal/corpus ./internal/codegen -count=1
go test ./internal/codegen -run TestSpreadFoldDiff -count=1
go test . -run FuzzAttrsFoldMatchesReference -count=1
gofmt -w internal/codegen/emit.go
git add -A
git commit -m "feat(codegen): conditional class/style on a forwarding element merges (lift D3)

Extends elementFolds so a forwarding element with a cond-attr class/style folds
(merging via AttrsCond -> leaf) instead of the D3 rejection; deletes the dead
validateCondBranch. D3's per-context-escaping premise died with #79/#91.

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 2: Comprehensive corpus — all forms, combinations, controls

**Files:**
- Create: cases under `internal/corpus/testdata/cases/condmerge/`
- Test: `internal/corpus`

- [ ] **Step 1: Add cases (each `input.gsx` + `invoke` + empty goldens; hand-verify render before `-update`)**

- `condmerge/cond_class_else.txtar` — `{ if a { class="on" } else { class="off" } }` + spread; both branches (two invokes or two components) → root+cond+spread class merge.
- `condmerge/cond_style.txtar` — same with `style` (property-level `; ` merge).
- `condmerge/all_forms.txtar` — composable `class={ "base", "on": active, switch { case v>0: "pos" default: "neg" } }` + a Form-2 `{ if hot { class="hot" } }` + a spread → one merged class (the `base on pos sp hot` shape). Style analog if practical.
- `condmerge/else_if.txtar` — `{ if a { class="x" } else if b { class="y" } else { class="z" } }` + spread (exercises the recursive else-if walk in `hasCondClassStyle`).
- `condmerge/nonforwarding_unchanged.txtar` — `<div { if a { class="on" } }>x</div>` (NO spread) → still the static path; assert `generated.x.go.golden` is byte-identical to base behavior (it must NOT fold — `first == nil`).
- `condmerge/cond_class_plus_forced_attr.txtar` — `{ if a { class="on" aria-current="page" } }` + spread → class merges AND `aria-current` renders on the taken branch.
- `condmerge/jscss_hole_edge.txtar` — `<button onclick=js"go(@{u})" { a... } { if active { class="on" } }>` → now FOLDS (cond class) → composeBag rejects the js hole with the #92 element-context diagnostic (a diagnostics-only case pinning that this combo is the known follow-up, not a silent miscompile).

- [ ] **Step 2: Regenerate + verify**
```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
```
Hand-verify each render is source-order last-wins + class/style aggregate. Commit.

---

## Task 3: Differential extension + perf benchmark + docs

**Files:**
- Modify: `internal/codegen/spread_fold_diff_test.go` (add a Form-2 shape)
- Create: a benchmark (e.g. `internal/codegen/cond_merge_bench_test.go` or a root-pkg render bench)
- Modify: `docs/guide/syntax/composition.md`, `docs/ROADMAP.md`

- [ ] **Step 1: Extend the codegen differential matrix**
Add a component whose element has a Form-2 conditional class + a spread to `spread_fold_diff_test.go`'s matrix; assert its render equals the naive `ConcatAttrs` fold reference (byte-identical). Run `go test ./internal/codegen -run TestSpreadFoldDiff -count=1`.

- [ ] **Step 2: Benchmark the per-render fold cost**
Add a benchmark rendering a nav-tab-style component (root class + spread + Form-2 conditional class) N times, and a comparison variant expressed via the composable form (which does NOT fold). Report ns/op + B/op + allocs/op for both. Record the delta in the report; the spec expects it small (one bag alloc). Run `go test ./internal/codegen -run '^$' -bench . -benchmem` (or the appropriate package).

- [ ] **Step 3: Docs**
`docs/guide/syntax/composition.md`: replace any "conditional class must use the composable form" note with a concise statement that a conditional `class`/`style` in `{ if }` merges. Keep it terse (docs-concise rule). `docs/ROADMAP.md`: note D3 lifted. `git grep -n "composable form\|cannot join" docs/` — reconcile stale prose. Commit.

---

## Task 4: make ci + adversarial review + PR

- [ ] **Step 1:** `make ci > /tmp/ci.log 2>&1; echo $?` (true exit) + `grep -cE "FAIL|Error [0-9]" /tmp/ci.log` (want 0); `make lint`. Fix any drift by regenerating (never hand-edit goldens).

- [ ] **Step 2: Independent adversarial review (build probes).** Dispatch a reviewer that generates + `go build` + renders throwaway `.gsx`: (a) a conditional class whose value is a `javascript:`-ish string routed through the fold — confirm leaf sanitization applies where relevant and no injection; (b) source-order last-wins across static + composable(`:bool`+`switch`) + conditional(`if/else`) + spread on one element; (c) a conditional class + numeric + nonce `<script>` folded together — compiles, renders; (d) `hasCondClassStyle` false-negative hunt: a cond-attr class nested two levels deep / in an `else if` — must fold (not fall to a deleted-validation gap). Fix findings, re-run `make ci`.

- [ ] **Step 3:** `superpowers:finishing-a-development-branch` → push, open PR (base `main`, now includes #91). Body: the D3-is-stale-post-#91 rationale, the three-forms table, the trigger extension, the benchmark delta, the deleted validation. Note the js/css-hole edge trades to #92.

## Self-review checklist

- **Spec coverage:** trigger extension + D3 deletion → Task 1; all 3 forms + combinations + non-forwarding control + js/css edge + style → Tasks 1-2; differential + perf → Task 3; docs → Task 3; ci + adversarial → Task 4. ✓
- **Placeholder scan:** benchmark and probes are concrete deliverables, not TODOs.
- **Type consistency:** `hasCondClassStyle` signature identical across tasks; `elementFolds` extension matches the shared-predicate contract with `scopeUsesNumeric`.
