# Body-Local `attrs` Shadow Corpus Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the canonical corpus prove that `attrs` is an ordinary body-local identifier which may be shadowed inside a component child closure, without adding an `attrs` parameter to the component ABI.

**Architecture:** This is a corpus correction, not a compiler change. The existing reserved-binding implementation already reserves only ambient `ctx`; the fixture will declare an outer body-local `attrs`, shadow it inside `Wrap`'s generated child closure, and render both values from their proper Go scopes.

**Tech Stack:** GSX txtar semantic corpus, Go test runner, generated Go/render goldens.

## Global Constraints

- `attrs` is a reserved component parameter role only when a callable parameter is literally named `attrs` and has an accepted attrs-bag type.
- `attrs` is never a reserved identifier inside a component body; body declarations and shadowing follow ordinary Go lexical scope.
- Do not add an implicit attrs parameter, restore synthesized Props, or change production code for this corpus correction.
- Do not hand-edit `generated.x.go.golden`, `render.golden`, or `coverage.golden`; regenerate them through the corpus updater.
- Preserve the test's mixed-scope proof and rendered output: inner child content uses `data-inner="1"`, while the outer sibling uses `data-outer="9"`.

---

### Task 1: Correct the mixed-scope body-local attrs fixture

**Files:**
- Modify: `internal/corpus/testdata/cases/reserved/component_child_shadow_with_outer_free.txtar`

**Interfaces:**
- Consumes: ordinary Go lexical scoping in GSX bodies and the existing component-child slot closure boundary.
- Produces: a canonical corpus fixture with `component C()`, `C()`, an outer local `attrs`, and a nested shadowing local `attrs`.

- [ ] **Step 1: Change only the authored fixture and invocation**

Update the case comment to describe an outer body-local binding rather than a free reserved binding. Change the source and invocation to this shape, leaving all goldens untouched:

```gsx
component C() {
	{{ attrs := gsx.Attrs{{Key: "data-outer", Value: "9"}} }}
	<Wrap>
		{{ attrs := gsx.Attrs{{Key: "data-inner", Value: "1"}} }}
		<div { attrs... }>in</div>
	</Wrap>
	<span { attrs... }>out</span>
}
```

```go
C()
```

- [ ] **Step 2: Run the focused corpus test and verify RED**

Run:

```bash
GOCACHE=/tmp/gsx-verbatim-corpus-local-attrs go test ./internal/corpus -run 'TestCorpus/reserved/component_child_shadow_with_outer_free$' -count=1
```

Expected: FAIL because the existing generated golden still contains `func C(attrs gsx.Attrs)` and the old invocation contract. The failure must be golden drift, not an `attrs` reservation or undefined-name diagnostic.

- [ ] **Step 3: Regenerate canonical goldens**

Run:

```bash
GOCACHE=/tmp/gsx-verbatim-corpus-local-attrs go test ./internal/corpus -run TestCorpus -update
```

Expected: PASS and regenerated output contains `func C()`, an outer `attrs := ...data-outer...`, a nested child-closure `attrs := ...data-inner...`, and no synthesized or explicit attrs parameter.

- [ ] **Step 4: Verify the focused case and relevant reserved-binding unit tests**

Run:

```bash
GOCACHE=/tmp/gsx-verbatim-corpus-local-attrs go test ./internal/corpus -run 'TestCorpus/reserved/component_child_shadow_with_outer_free$' -count=1
GOCACHE=/tmp/gsx-verbatim-corpus-local-attrs go test ./internal/codegen -run 'TestCheckReservedBodyBindings' -count=1
```

Expected: both commands PASS. The render remains `<section><div data-inner="1">in</div></section><span data-outer="9">out</span>`.

- [ ] **Step 5: Check generated diff and static analysis**

Run:

```bash
git diff --check
gopls check -severity=hint internal/codegen/reserved_bindings.go
git diff -- internal/corpus/testdata/cases/reserved/component_child_shadow_with_outer_free.txtar internal/corpus/testdata/coverage.golden
```

Expected: no whitespace or gopls diagnostics; only the intended fixture/golden changes appear, with no production-code edit and no unrelated coverage drift.

- [ ] **Step 6: Commit the correction**

```bash
git add docs/superpowers/plans/2026-07-16-corpus-body-attrs-shadow.md internal/corpus/testdata/cases/reserved/component_child_shadow_with_outer_free.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): make attrs shadowing body-local"
```
