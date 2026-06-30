# Remove Unused Attrs Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the generated `_ = attrs` guard so explicit attribute forwarding must produce a real use of every synthesized `attrs` binding.

**Architecture:** Keep `usesAttrs` as the single gate for synthesizing the `Attrs` field and local binding. Add a generator-level shape test, remove the fallback emission, then refresh pinned generated output.

**Tech Stack:** Go, generated `.x.go` output, txtar corpus fixtures.

---

### Task 1: Pin and remove the guard

**Files:**
- Modify: `internal/codegen/emit_test.go`
- Modify: `internal/codegen/emit.go`

- [ ] **Step 1: Write the failing test**

Add a code-generation test that compiles a component containing
`<div { attrs... }/>`, then asserts the output contains
`attrs := _gsxp.Attrs` and does not contain `_ = attrs`.

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `go test ./internal/codegen -run TestGeneratedExplicitAttrsHasNoUnusedGuard -count=1`

Expected: FAIL because generated output contains `_ = attrs`.

- [ ] **Step 3: Remove the fallback emission**

Change the manual binding emission to:

```go
b.WriteString("\t\tattrs := _gsxp.Attrs\n")
```

Update its comment to state that `usesAttrs` guarantees a lowered use.

- [ ] **Step 4: Run the focused test**

Run: `go test ./internal/codegen -run TestGeneratedExplicitAttrsHasNoUnusedGuard -count=1`

Expected: PASS.

### Task 2: Refresh generated expectations

**Files:**
- Modify: corpus `.txtar` golden output containing `_ = attrs`
- Modify: `examples/tailwind-merge/views/card.x.go`

- [ ] **Step 1: Regenerate pinned output**

Run the repository's corpus update and example-generation commands used by
`make check`, then verify `rg -n '_ = attrs' internal/corpus/testdata examples`
returns no matches.

- [ ] **Step 2: Run verification**

Run: `go test ./internal/codegen ./internal/corpus`

Expected: PASS.

Run: `make check`

Expected: PASS.

- [ ] **Step 3: Commit**

Stage the emitter, test, refreshed generated output, and this plan. Commit with:

```text
codegen: remove unused attrs guard
```
