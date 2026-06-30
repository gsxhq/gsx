# Style Contribution Evaluation Order Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve strict left-to-right evaluation across composed class/style contributions when value-form control flow or tuple unwrapping requires statement-level hoisting.

**Architecture:** Add corpus cases that make an earlier contribution mutate state consumed by a later value-form condition, plus a mixed plain/tuple call case. Refactor the five composed-part builders around one ordered lowering helper for element/root class/style; retain the child-component-specific builder but apply the same “hoist all parts in order” rule there. When ordered lowering is active, every plain value and guard is assigned to a temp in source order; tuple values use `hoistTuple`, and value-form parts use `hoistValueCF`.

**Tech Stack:** Go, gsx corpus txtar tests, existing codegen type-harvest and temp-hoisting helpers.

---

### Task 1: Pin value-form ordering

**Files:**
- Create: `internal/corpus/testdata/cases/class/value_form_evaluation_order.txtar`
- Create: `internal/corpus/testdata/cases/style/value_form_evaluation_order.txtar`
- Create: `internal/corpus/testdata/cases/components/child_class_evaluation_order.txtar`
- Modify: `internal/corpus/testdata/coverage.golden`

- [ ] **Step 1: Add failing corpus cases**

Each case defines a package variable `state`, an earlier `setState() string` contribution that sets it, and a subsequent value-form `if state { ... } else { ... }`. Cover:

```gsx
<span class={ setState(), if state { "on" } else { "off" } }>x</span>
```

on a non-root element, the analogous `style` form on a component root, and a `class` value passed to a child component. Expected renders must contain the selected `"on"` contribution, proving the earlier call ran first.

- [ ] **Step 2: Run the new cases and verify RED**

Run:

```bash
GOCACHE=/tmp/gsx-style-order-gocache go test ./internal/corpus -run 'TestCorpus/(class/value_form_evaluation_order|style/value_form_evaluation_order|components/child_class_evaluation_order)' -count=1
```

Expected: FAIL because generated code evaluates the value-form before `setState()`, rendering the `"off"` arm.

- [ ] **Step 3: Regenerate only after implementation**

Do not update goldens during RED. Generated Go goldens and coverage are regenerated in Task 3 after behavior is correct.

### Task 2: Pin mixed tuple/plain-call ordering

**Files:**
- Modify: `internal/corpus/testdata/cases/class/multi_part_tuple_order.txtar`

- [ ] **Step 1: Strengthen the existing test**

Replace the literal-only prefix with stateful calls:

```gsx
class={ first(), tuplePart(), last() }
```

`first` appends `"first"` to a package-level trace and returns a string; `tuplePart` checks/appends after it and returns `(string, error)`; `last` checks/appends last. Make rendered class output depend on correct order so the corpus test observes behavior rather than only generated statement placement.

- [ ] **Step 2: Run the case and verify RED**

Run:

```bash
GOCACHE=/tmp/gsx-style-order-gocache go test ./internal/corpus -run 'TestCorpus/class/multi_part_tuple_order' -count=1
```

Expected: FAIL because `tuplePart()` is currently hoisted ahead of `first()`.

### Task 3: Lower composed contributions in source order

**Files:**
- Modify: `internal/codegen/emit.go`

- [ ] **Step 1: Add an ordered-lowering predicate**

Add a helper that returns true when any part has `CF != nil` or resolves to a tuple. This is the point at which inline argument evaluation can no longer preserve source order.

- [ ] **Step 2: Add a shared ordered part-list helper**

The helper must iterate parts once, in source order:

```go
for i := range a.Parts {
	p := &a.Parts[i]
	switch {
	case p.CF != nil:
		// Emit the value-form now and append its result temp.
	case resolved[p] is a tuple:
		// Validate (T,error), call hoistTuple now, append its result temp.
	default:
		// Emit `valueTemp := expr` now.
		// For a guarded part, then emit `guardTemp := cond`.
		// Append Class(valueTemp) or ClassIf(valueTemp, guardTemp).
	}
}
```

Use configurable runtime package/style transformation parameters so `emitRootComposedClass`, `rootStyleString`, `emitClassAttr`, and `emitStyleAttr` share the traversal without changing escaping or merge behavior. If ordered lowering is not required, preserve the existing inline output.

- [ ] **Step 3: Apply the same rule to child-component class lowering**

In `classEntryExpr`, replace the tuple-only `anyTuplePart` decision with the ordered-lowering predicate. In real emit mode, hoist every plain value and guard in source order whenever a tuple or CF part exists. Keep probe mode structurally valid without changing harvest alignment.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
GOCACHE=/tmp/gsx-style-order-gocache go test ./internal/corpus -run 'TestCorpus/(class/value_form_evaluation_order|style/value_form_evaluation_order|components/child_class_evaluation_order|class/multi_part_tuple_order)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Regenerate corpus goldens**

Run:

```bash
GOCACHE=/tmp/gsx-style-order-gocache go test ./internal/corpus -run TestCorpus -update
GOCACHE=/tmp/gsx-style-order-gocache go test ./internal/corpus -run TestCorpus -count=1
```

Expected: PASS, with generated code showing temps in source order.

### Task 4: Verify and commit

**Files:**
- Modify: files from Tasks 1–3

- [ ] **Step 1: Run formatting and static checks**

Run:

```bash
gofmt -w internal/codegen/emit.go
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 2: Run the complete suite**

Run:

```bash
GOCACHE=/tmp/gsx-style-order-gocache go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases internal/corpus/testdata/coverage.golden
git commit -m "fix(codegen): preserve class style evaluation order"
```
