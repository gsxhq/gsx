# Multi-Spread Source Evaluation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make PR #91 evaluate folded attribute contributors in strict source order, not merely merge their final values in source order.

**Architecture:** Keep `composeBag` as the single fold implementation. Add one local materialization boundary that collapses all already-accumulated parts into a generated `gsx.Attrs` temporary immediately before any later contributor can emit a hoisted statement; wrap pipeline hoists through that boundary and invoke it before lowering a conditional contributor. The final `ConcatAttrs` remains the last-writer-wins merge, while generated statements now preserve Go-visible side effects, panic order, and error selection.

**Tech Stack:** Go, gsx codegen, canonical txtar corpus.

## Global Constraints

- Runtime remains standard-library-only and unchanged; add no dependency and no `packages.Load` call.
- Attribute contributors evaluate in strict source order; final scalar precedence remains last-writer-wins and `class`/`style` continue aggregating in source order.
- Untaken `AttrsCond` branches remain lazy.
- Probe and emit lowering must remain structurally aligned and compile for both single-value and `(T, error)` pipeline stages.
- Generated files and corpus goldens are regenerated, never hand-edited.
- Keep the 0/1-top-level-spread fast paths unchanged.

---

### Task 1: Pin and restore source-ordered fold evaluation

**Files:**
- Modify: `internal/codegen/emit.go` — `composeBag`, plus stale comments on `foldElementSpreads`, `bagSpreadIndex`, and `firstTwoSpreadAttrs`.
- Create: `internal/corpus/testdata/cases/multispread/evaluation_order.txtar`.
- Create: `internal/corpus/testdata/cases/multispread/evaluation_order_error.txtar`.
- Modify generated manifest: `internal/corpus/testdata/coverage.golden` via corpus regeneration.

**Interfaces:**
- Consumes: `composeBag(..., wrap func(string) string, ..., interpTemp *int, ...)` and its existing ordered `parts []string` / `entries []string` accumulation.
- Produces: a local `materializePrior()` helper and an `orderedWrap(expr string) string`; no exported API or runtime change.

- [ ] **Step 1: Write the failing corpus probe**

Create `multispread/evaluation_order.txtar` with helpers that append markers before returning bags:

```gsx
package views

import "github.com/gsxhq/gsx"

var calls []string

func bag(mark string) gsx.Attrs {
	calls = append(calls, mark)
	return gsx.Attrs{{Key: "data-k", Value: mark}}
}

component Page(on bool) {
	<div { bag("a")... } { if on { { bag("b")... } } } { bag("c")... }>x</div>
}
```

The invoke must reset `calls`, render `Page(PageProps{On: true})`, and return an error unless `strings.Join(calls, ",") == "a,b,c"`; include `strings` and `fmt` in the invoke imports. Pin the rendered result as `<div data-k="c">x</div>`.

- [ ] **Step 2: Run RED and confirm the defect**

Run:

```bash
GOCACHE=/tmp/gsx-pr91-gocache go test ./internal/corpus -run 'TestCorpus/multispread/evaluation_order' -count=1
```

Expected: FAIL from the invoke with recorded order `b,a,c`. A parse failure or golden-only mismatch is not the required RED.

- [ ] **Step 3: Add the ordered materialization boundary**

Inside `composeBag`, after `flush`, add a local helper that evaluates accumulated parts before a subsequent hoist:

```go
materializePrior := func() {
	flush()
	if len(parts) == 0 {
		return
	}
	expr := parts[0]
	if len(parts) > 1 {
		expr = fmt.Sprintf("%s.ConcatAttrs(%s)", rtPkg, strings.Join(parts, ", "))
	}
	name := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\t%s := %s\n", name, expr)
	parts = []string{name}
}
```

Use valid Go syntax for incrementing the pointed counter (`*interpTemp = *interpTemp + 1`) in the implementation.

Wrap the caller-provided hoist callback so every pipeline/error hoist first evaluates preceding contributors:

```go
orderedWrap := func(expr string) string {
	materializePrior()
	return wrap(expr)
}
```

Pass `orderedWrap`, not `wrap`, to `lowerPipe`, `classEntryExpr`, and any other lowering inside `composeBag` that may emit a hoist through the callback. Immediately before calling `condAttrsExpr`, call `materializePrior()` because lowering a nested conditional can emit statements before returning its expression. Do not materialize after the conditional merely for shape; ordinary later expressions stay as later `ConcatAttrs` operands and therefore retain Go's left-to-right call evaluation.

In probe mode, keep the existing `_gsxunwrap` structure and avoid emitting assignment statements that would perturb counted probe alignment: `materializePrior` must be a no-op when `probeWrap` is true. Probe code is never executed, so runtime ordering does not apply there.

- [ ] **Step 4: Run GREEN and inspect generated order**

Regenerate and verify:

```bash
GOCACHE=/tmp/gsx-pr91-gocache go test ./internal/corpus -run 'TestCorpus/multispread/evaluation_order' -update
GOCACHE=/tmp/gsx-pr91-gocache go test ./internal/corpus -run 'TestCorpus/multispread/evaluation_order' -count=1
```

Expected generated shape evaluates `bag("a")` into a temporary before the `AttrsCond(...bag("b")...)` hoist, then evaluates `bag("c")` in the final `ConcatAttrs` call. Expected runtime marker order: `a,b,c`; expected output: `data-k="c"`.

- [ ] **Step 5: Extend the runtime probes for laziness and error ordering**

In `evaluation_order.txtar`, invoke a second component or render after resetting
`calls` with `on=false`; fail unless the markers are exactly `a,c`, proving the
untaken branch never calls `bag("b")`.

Create `evaluation_order_error.txtar` with a first spread expression that
returns `gsx.Attrs`, followed by a conditional attribute whose taken branch
contains an error-returning pipeline/spread using the established
`pipeerr/cond_attr_branch_attr_tuple.txtar` syntax, followed by
`{ bag("late")... }`. Its invoke must expect the sentinel error and fail if
`calls` contains `late`. This is a runtime assertion; a generated-code string
check is insufficient.

- [ ] **Step 6: Correct stale implementation comments**

Update comments to match shipped dispatch:

- `foldElementSpreads`: a lone conditional spread folds only when a root `class`/`style` requires aggregation.
- `bagSpreadIndex`: remove the claim that multiple spreads are rejected upstream.
- `firstTwoSpreadAttrs`: describe the second spread as the full-fold trigger, not an already-rejected state.

- [ ] **Step 7: Verify the affected subsystem**

Run:

```bash
GOCACHE=/tmp/gsx-pr91-gocache go test ./internal/codegen ./internal/corpus -count=1
gopls check -severity=hint internal/codegen/emit.go
git diff --check
```

Expected: all commands exit 0; no unused helper or generated drift.

- [ ] **Step 8: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/multispread/evaluation_order.txtar internal/corpus/testdata/cases/multispread/evaluation_order_error.txtar internal/corpus/testdata/coverage.golden
git commit -m "fix(codegen): preserve source evaluation in spread folds"
```

---

### Task 2: Authoritative verification and adversarial re-review

**Files:**
- Modify only if verification exposes a defect.

**Interfaces:**
- Consumes: Task 1's source-order regression and materialization boundary.
- Produces: review evidence that precedence, escaping, laziness, and evaluation order all hold together.

- [ ] **Step 1: Run repository gates**

```bash
make ci
make lint
```

Expected: both exit 0.

- [ ] **Step 2: Independent adversarial probe**

Dispatch a fresh reviewer to build a throwaway program with at least: plain spread → taken conditional spread → plain spread; plain spread → untaken conditional spread → plain spread; and an earlier failing contributor followed by a side-effecting contributor. The reviewer must report observed call order and error behavior, and re-check URL/srcset sanitization through the resulting fold.

- [ ] **Step 3: Publish onto PR #91**

Push the reviewed commits to PR #91's head branch `worktree-multi-spread-merge`, refresh `gh pr checks 91`, and do not start #92/#93 until the corrected PR is green.

---

## Self-review

- Spec coverage: strict evaluation order, error selection, panic/side-effect order, and untaken-branch laziness are covered by Task 1; final merge semantics and escaping are rechecked in Task 2.
- Placeholder scan: no TBD/TODO steps or conditional implementation choices.
- Type consistency: all helpers remain local to `composeBag`; no signature or exported API changes.
