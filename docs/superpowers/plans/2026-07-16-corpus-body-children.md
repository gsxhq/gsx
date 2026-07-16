# Body-Local `children` Corpus Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the canonical corpus distinguish an ordinary body-local `children` identifier from the explicit `children` component parameter role.

**Architecture:** This is corpus cleanup only. A nullary component will prove that a body-local `children` declaration renders normally; a separate case will retain the ordinary Go same-scope redeclaration error when an explicit `children` parameter already exists; the obsolete reserved-parameter rejection case will be removed.

**Tech Stack:** GSX txtar semantic corpus, Go test runner, generated Go/render goldens.

## Global Constraints

- `children` is a reserved component parameter role only when it is literally the callable parameter populated from authored component body markup.
- `children` is never a reserved identifier inside a component body; body declarations and shadowing follow ordinary Go lexical scope.
- An explicit parameter and `children :=` in the same function scope still produce Go's native redeclaration/type diagnostics; do not suppress or relabel them as a reserved-name error.
- Do not add implicit children synthesis, restore generated Props, or change production code.
- Do not hand-edit generated or coverage goldens; regenerate through the corpus updater.
- Remove obsolete rejection fixtures rather than retaining misleading filenames or empty diagnostics in the diagnostics corpus.

---

### Task 1: Correct body-local and explicit-parameter children fixtures

**Files:**
- Rename and modify: `internal/corpus/testdata/cases/reserved/children_shortvar_unplaced_rejected.txtar` → `internal/corpus/testdata/cases/reserved/children_body_local.txtar`
- Rename and modify: `internal/corpus/testdata/cases/reserved/children_shortvar_rejected.txtar` → `internal/corpus/testdata/cases/reserved/children_param_shortvar_collision.txtar`
- Delete: `internal/corpus/testdata/cases/diagnostics/reserved_param_children.txtar`
- Regenerate: `internal/corpus/testdata/coverage.golden`

**Interfaces:**
- Consumes: ordinary Go lexical scope and the explicit semantic `children gsx.Node` parameter role.
- Produces: one positive local-binding case and one native same-scope collision case, with no reserved-name compatibility diagnostic.

- [ ] **Step 1: Turn the nullary local-binding case into the positive test**

Rename the file to `children_body_local.txtar`. Rewrite its comment and authored sections to:

```txtar
# `children` is an ordinary body-local identifier when the component has no
# explicit children parameter. The local binding renders like any other Go
# value; no reserved-name rule applies inside the body.
-- input.gsx --
package views

component Page() {
	{{ children := "hi" }}
	<div>{children}</div>
}
-- invoke --
Page()
-- diagnostics.golden --
```

Leave generated/render goldens absent or stale until the updater runs; do not author them manually.

- [ ] **Step 2: Verify RED**

Run:

```bash
GOCACHE=/tmp/gsx-verbatim-corpus-local-children go test ./internal/corpus -run 'TestCorpus/reserved/children_body_local$' -count=1
```

Expected: FAIL because the positive case has no regenerated generated/render golden and the coverage manifest still names the old case. The generated source itself must contain `children := "hi"` and must not report a reserved-name diagnostic.

- [ ] **Step 3: Reclassify the explicit-parameter collision and delete the obsolete diagnostic**

Rename `children_shortvar_rejected.txtar` to `children_param_shortvar_collision.txtar`. Keep its input and native diagnostics, but replace the comment with:

```txt
# An explicit children parameter and `children :=` occupy the same Go function
# scope. This is an ordinary Go redeclaration/type error, not a GSX reserved-name
# diagnostic; nested Go scopes may shadow the parameter normally.
```

Delete `diagnostics/reserved_param_children.txtar`: explicit `children gsx.Node` is valid and already exercised by successful render cases, so the old rejection fixture has no diagnostic or unique behavior left.

- [ ] **Step 4: Regenerate canonical goldens**

Run:

```bash
GOCACHE=/tmp/gsx-verbatim-corpus-local-children go test ./internal/corpus -run TestCorpus -update
```

Expected: PASS; `children_body_local` renders `<div>hi</div>`, the collision case retains only native Go diagnostics, and `coverage.golden` contains the new names but not the deleted/old names.

- [ ] **Step 5: Verify focused semantics and static checks**

Run:

```bash
GOCACHE=/tmp/gsx-verbatim-corpus-local-children go test ./internal/corpus -run 'TestCorpus/reserved/(children_body_local|children_param_shortvar_collision)$' -count=1
GOCACHE=/tmp/gsx-verbatim-corpus-local-children go test ./internal/codegen -run 'TestCheckReservedBodyBindings' -count=1
gopls check -severity=hint internal/codegen/reserved_bindings.go
git diff --check
```

Expected: all commands PASS with pristine output. Inspect the diff to confirm no production-code change and no unrelated golden drift.

- [ ] **Step 6: Commit**

```bash
git add docs/superpowers/plans/2026-07-16-corpus-body-children.md internal/corpus/testdata/cases/reserved/children_body_local.txtar internal/corpus/testdata/cases/reserved/children_param_shortvar_collision.txtar internal/corpus/testdata/cases/diagnostics/reserved_param_children.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): make children body-local"
```
