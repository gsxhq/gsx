# AttrsCond → (Attrs, error) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `AttrsCond` thunks return `(Attrs, error)` so error hoists are legal inside cond-attr branches — one uniform thunk lowering (statement form deleted), and branch positions join the probe type-harvest so the entire class-parts-in-cond-attr-branch ROADMAP gap closes.

**Architecture:** Runtime signature change + uniform emit lowering (thunk bodies become self-contained statement contexts; the `AttrsCond(...)` call site is a `(Attrs, error)` expression hoisted by the existing tuple machinery; skeleton wraps the same call in `_gsxunwrap(...)` to stay expression-only — emit ≡ probe). Then `collectExprs`/`emitProbes` extend their documented k-th-probe ↔ k-th-node recipe to branch positions so `resolved` covers them, and the branch-mode rejections in `classEntryExpr`/`condBranchAttrs` are deleted.

**Tech Stack:** Go 1.26.1 (pin: `GO_VERSION` in ci.yml), stdlib-only runtime, txtar corpus.

**Spec:** `docs/superpowers/specs/2026-07-03-attrscond-error-design.md`

## Global Constraints

- Work in THIS worktree: `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/attrscond-error`, branch `worktree-attrscond-error`. NEVER touch the main checkout.
- Runtime (root package) stays **stdlib-only**. The ONLY runtime change is the `AttrsCond` signature.
- **No syntax change** — parser/AST/formatter/tree-sitter/vscode-gsx untouched.
- **emit ≡ probe**: skeleton and emitter derive cond-attr lowering from the same `condAttrsExpr`; probe mode differs only by tolerance wraps (`_gsxunwrap`, `probePipeWrap`), never by structure that changes harvested types.
- Never hand-edit `.x.go`/`*.golden`; regenerate via `go test ./internal/corpus -run TestCorpus -update`, verify without `-update` (also rewrites `coverage.golden`).
- Golden churn is EXPECTED this time (uniform thunk form) but must be exactly scoped: only cond-attr-bearing goldens change shape; each task states which. Inspect, don't rubber-stamp.
- Error semantics: taken branch's failing stage halts that branch, error propagates out of the render closure; untaken branch NEVER evaluates.
- Inner loop: focused `go test` runs; full `make ci` in the final task.

## Reference: key existing code (verify anchors by grep; they were taken at 5263f91)

- `attrs.go:145` `AttrsCond(cond bool, then, els func() Attrs) Attrs` — the runtime target.
- `internal/codegen/emit.go:3848-3851` — the emit call site choosing `condAttrsStmt` (via `condBranchNeedsHoist`) vs `condAttrsExpr(t, rtPkg, el.Tag, mergeExpr, table, probeWrap)`.
- `emit.go:4135` `condAttrsExpr`, `:4178` `condAttrsStmt`, `:4220` `condBranchNeedsHoist`, and `condBranchAttrs` just below — the dual lowering this plan unifies.
- `emit.go:4041` — `errors.Is(err, errFailingStageUnsupported)` re-wording for branch class pipes (delete in Task 3). Sentinel: `internal/codegen/filters.go:22` (KEEP — lowerPipe's nil-wrap guard stays defensive).
- `emit.go` `hoistTuple` (grep) — emits `tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`; Task 1 refactors it to parameterize the return statement.
- `emit.go` `classEntryExpr` — branch mode is `b == nil`; contains three "not supported yet" attrErrors (grep `conditional-attr branch`) — deleted in Task 3.
- `internal/codegen/analyze.go:2026` `componentExprs`/`collectExprs` and `:858` `emitProbes` — the k-th-probe ↔ k-th-node machinery; their comments ARE the recipe. Element-level CondAttrs are already probed (analyze.go:1192, :2088); the gap is COMPONENT-tag cond-attr branches.
- Corpus cases: `components/component_conditional_attr{,_else}.txtar` (thunk-form goldens), `pipeerr/cond_attr_branch_{mid_stage,untaken,class_pipe_rejected}.txtar`.

---

### Task 1: Runtime signature + uniform thunk lowering + call-site hoist

**Files:**
- Modify: `attrs.go:145` (AttrsCond)
- Test: `attrs_test.go` (or the root package's existing attrs test file — follow its naming)
- Modify: `internal/codegen/emit.go` — `hoistTuple` refactor; `condAttrsExpr`/`condBranchAttrs` emit `(Attrs, error)` thunks with thunk-local hoists; call site at :3848-3851 loses the stmt/expr split; DELETE `condAttrsStmt` + `condBranchNeedsHoist` (and `pipeStagesHaveErr` if now unused)
- Regenerate: all cond-attr goldens

**Interfaces:**
- Consumes: existing `emitPipeWrap`, `probePipeWrap`, `lowerPipe(…, wrap)`.
- Produces (later tasks rely on): runtime `func AttrsCond(cond bool, then, els func() (Attrs, error)) (Attrs, error)`; `hoistTupleReturning(b *bytes.Buffer, expr string, interpTemp *int, errReturn string) string`; `thunkPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string` (hoists with `return nil, _gsxerr`); `condAttrsExpr(t *ast.CondAttr, rtPkg, tag, mergeExpr string, table filterTable, probeWrap bool, resolved map[ast.Node]types.Type, interpTemp *int) (string, map[string]string, error)` — returns a single expression whose thunk bodies may contain statements; in emit mode the CALLER hoists it with `hoistTuple`; in probe mode the caller wraps it `_gsxunwrap(...)`.

- [x] **Step 1: Write the failing runtime test**

In the root package's attrs test file add:

```go
func TestAttrsCondError(t *testing.T) {
	boom := errors.New("boom")
	ok := Attrs{{Key: "class", Value: "hot"}}

	a, err := AttrsCond(true, func() (Attrs, error) { return ok, nil }, nil)
	if err != nil || len(a) != 1 {
		t.Fatalf("taken then = %v, %v", a, err)
	}
	if _, err := AttrsCond(true, func() (Attrs, error) { return nil, boom }, nil); !errors.Is(err, boom) {
		t.Fatalf("then error not propagated: %v", err)
	}
	if _, err := AttrsCond(false, nil, func() (Attrs, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("els error not propagated: %v", err)
	}
	// Untaken branch must never run; nil els yields (nil, nil).
	a, err = AttrsCond(false, func() (Attrs, error) { panic("untaken branch ran") }, nil)
	if a != nil || err != nil {
		t.Fatalf("untaken = %v, %v", a, err)
	}
}
```

- [x] **Step 2: Run to verify it fails**

Run: `go test . -run TestAttrsCondError -v`
Expected: FAIL — compile error (thunk signatures don't match `func() Attrs`).

- [x] **Step 3: Change the runtime**

```go
// AttrsCond selects one of two attribute-bag thunks for a conditional component
// attribute: it calls and returns then() when cond is true, otherwise els(). The
// branches are THUNKS so the untaken branch is never evaluated — mirroring a real
// Go if/else. The thunks return (Attrs, error) so a branch body may hoist
// (T, error) values (e.g. a pipeline stage that can fail) and propagate the
// error; the generated call site unwraps it like any other (T, error) value.
// els may be nil (no else branch); an untaken or nil branch yields (nil, nil).
func AttrsCond(cond bool, then, els func() (Attrs, error)) (Attrs, error) {
	if cond {
		if then != nil {
			return then()
		}
	} else if els != nil {
		return els()
	}
	return nil, nil
}
```

Run: `go test . -run TestAttrsCondError -v` → PASS. (`go build ./...` now fails in internal/codegen goldens? No — generated code lives only in corpus temp dirs; the repo itself builds. Verify: `go build ./...` → PASS.)

- [x] **Step 4: Refactor hoistTuple + add the thunk wrap (emit.go, next to hoistTuple)**

```go
// hoistTupleReturning is hoistTuple with a caller-chosen error-return statement:
// the render closure returns `return _gsxerr`; an (Attrs, error) cond-attr thunk
// returns `return nil, _gsxerr`.
func hoistTupleReturning(b *bytes.Buffer, expr string, interpTemp *int, errReturn string) string {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\t%s\n\t\t}\n", tmp, expr, errReturn)
	return tmp
}

func hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) string {
	return hoistTupleReturning(b, expr, interpTemp, "return _gsxerr")
}

// thunkPipeWrap is emitPipeWrap for statement positions INSIDE an (Attrs, error)
// cond-attr thunk: same hoist, two-value error return.
func thunkPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string {
	return func(call string) string {
		return hoistTupleReturning(b, call, interpTemp, "return nil, _gsxerr")
	}
}
```

- [x] **Step 5: Unify the lowering**

In `condAttrsExpr` (emit.go:4135): build each branch with a **thunk-local** `var tb bytes.Buffer`; pass `&tb`, `interpTemp`, and (emit mode) `thunkPipeWrap(&tb, interpTemp)` down to `condBranchAttrs`; emit the thunk as

```go
thunk := fmt.Sprintf("func() (%s.Attrs, error) {\n%s\t\treturn %s, nil\n\t}", rtPkg, tb.String(), branchLit)
```

(`branchLit` is the existing `%s.Attrs{…}` literal; no-else keeps `elseArg = "nil"`). `condBranchAttrs` gains `(b *bytes.Buffer, interpTemp *int, wrap func(string) string)` params and passes them through; its ExprAttr path replaces the old `wrap == nil` pre-check rejection with real lowering via `wrap` — in probe mode pass `probePipeWrap` (function value, no buffer needed). KEEP `classEntryExpr`'s branch-mode rejections untouched this task (class parts lift in Task 3 — continue passing `nil, nil, nil` for the class path only).

At the call site (emit.go:3848-3851): delete the `condBranchNeedsHoist` split; always call the new `condAttrsExpr`; in emit mode hoist the result — `condExpr = hoistTuple(b, condExpr, interpTemp)` — and splice the temp into the Merge chain; in probe mode wrap it: `condExpr = "_gsxunwrap(" + condExpr + ")"`. Delete `condAttrsStmt` and `condBranchNeedsHoist` (+ `pipeStagesHaveErr` if unreferenced — `gopls check` / `go vet` will tell).

- [x] **Step 6: Regenerate goldens, inspect, verify scope**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && go test ./internal/codegen -count=1`
Expected: PASS. `git diff --stat` shows ONLY cond-attr-bearing goldens changed (grep the diff for `AttrsCond` — every changed golden should show the `func() (gsx.Attrs, error)` form + call-site hoist; `pipeerr/cond_attr_branch_mid_stage` flips from statement form to thunk form with hoists INSIDE the thunk returning `nil, _gsxerr`; `cond_attr_branch_untaken` still renders `<div>Hi</div>` with no error — laziness preserved; `cond_attr_branch_class_pipe_rejected` diagnostics UNCHANGED). Inspect and quote the new `cond_attr_branch_mid_stage` golden in your report.

- [x] **Step 7: Commit**

```bash
git add -A && git commit -m "feat(runtime,codegen): AttrsCond thunks return (Attrs, error); one uniform cond-attr lowering"
```

---

### Task 2: Branch positions join the probe type-harvest

**Files:**
- Modify: `internal/codegen/analyze.go` — `collectExprs` (:2036) and `emitProbes` (:858): component-tag cond-attr branches contribute probe nodes; also check `walkLivenessAttrExprs`' CondAttr case for duplicate-liveness conflicts with the new probes
- Test: `internal/codegen/analyze_test.go` — skeleton snapshot + resolved-harvest assertions

**Interfaces:**
- Consumes: Task 1's `condAttrsExpr` probe form (skeleton embeds `_gsxunwrap(gsx.AttrsCond(...))`).
- Produces: `resolved[node]` populated for, inside COMPONENT cond-attr branches: `*ast.ExprAttr` values, plain `*ast.ClassPart`s, and `*ast.ValueArm`s — same node classes the non-branch positions already harvest. Task 3 consumes `resolved` for tuple detection.

- [x] **Step 1: Write the failing harvest test**

In `analyze_test.go`, following the existing skeleton/analyze test harness pattern (grep `TestSkeletonProbeMidStageErrFilter` for the Task-3-era harness; reuse its fixture builder):

```go
// Branch positions must be type-harvested: a plain tuple-returning call as a
// class part / expr-attr inside a COMPONENT cond-attr branch gets a resolved
// entry (a *types.Tuple), enabling emit-time hoisting (Task 3 consumer).
func TestResolveCondAttrBranchParts(t *testing.T) {
	src := `package views

import "github.com/gsxhq/gsx"

func cls(v string) (string, error) { return v, nil }

component Card(title gsx.Node) { <div { attrs... }>{title}</div> }

component Page(hot bool, csv string) {
	<Card title="Hi" { if hot { class={ cls(csv) } data-x={ cls(csv) } } } />
}
`
	resolved := analyzeFixture(t, src) // reuse/adapt the existing harness helper
	var gotClassPart, gotExprAttr bool
	for node, typ := range resolved {
		if _, isTuple := typ.(*types.Tuple); !isTuple {
			continue
		}
		switch node.(type) {
		case *gsxast.ClassPart:
			gotClassPart = true
		case *gsxast.ExprAttr:
			gotExprAttr = true
		}
	}
	if !gotClassPart || !gotExprAttr {
		t.Fatalf("branch positions not harvested: classPart=%v exprAttr=%v", gotClassPart, gotExprAttr)
	}
}
```

(Adapt the fixture/harness invocation to what `analyze_test.go` actually exposes — the assertion contract is what matters: tuple-typed `resolved` entries exist for both node classes in a component cond-attr branch.)

- [x] **Step 2: Run to verify it fails**

Run: `go test ./internal/codegen -run TestResolveCondAttrBranchParts -v`
Expected: FAIL — no tuple entries for branch nodes (positions not probed). NOTE: if the skeleton currently fails to TYPE-CHECK on this fixture (the plain tuple class part is the known pre-existing leak), the harness may surface a diagnostic instead — that's still RED; record which.

- [x] **Step 3: Extend collection + probes in matched order**

In `collectExprs`'s component-tag case (analyze.go:2042-2066): after the existing ExprAttr → OrderedPair → parts passes, walk `*gsxast.CondAttr` attrs (Then, then Else) collecting, per branch, ExprAttrs first, then class-attr plain parts and CF arms — document the ordering in the same comment style as the surrounding passes. In `emitProbes`' matching component branch: emit `_gsxuseq(...)` probes for exactly those nodes in exactly that order (probe expressions use the same probe lowering as their non-branch counterparts: `probeExpr`/`probePipeWrap` for pipes, `_gsxunwrap`-tolerance where the neighboring class-part probes use it — READ the adjacent probe emission for parts and mirror it). Skeleton probes are top-level statements — the skeleton is compile-only, so probing branch expressions unconditionally is safe (no evaluation ever happens); note this in a comment. Check `walkLivenessAttrExprs`' CondAttr case (analyze.go:2236+): if it already liveness-refs the same exprs, ensure no "declared and not used"/duplicate-probe conflict (duplicates of `_ = (x)` alongside `_gsxuseq(x)` are harmless for single-value exprs but `_ = (tuple)` is illegal for multi-value — mirror the existing skip logic at analyze.go:2219-2223 if it applies).

- [x] **Step 4: Run tests**

Run: `go test ./internal/codegen -run 'TestResolveCondAttrBranchParts|TestSkeleton' -v` → PASS.
Then zero-regression: `go test ./internal/codegen -count=1 && go test ./internal/corpus -run TestCorpus`
Expected: PASS with **zero golden changes** (probes change the skeleton, not emitted code; `resolved` gains entries nobody consumes yet). If any golden changes, the collection order broke k-th alignment for EXISTING probes — fix before proceeding.

- [x] **Step 5: Commit**

```bash
git add internal/codegen && git commit -m "feat(codegen): type-harvest probes for component cond-attr branch positions"
```

---

### Task 3: Lift the branch class-part edges; delete the rejections

**Files:**
- Modify: `internal/codegen/emit.go` — `classEntryExpr` branch mode; `condBranchAttrs` class path passes the thunk-local `b`/`interpTemp`/`thunkPipeWrap`/`resolved`; delete the three "not supported yet" attrErrors (grep `conditional-attr branch` in emit.go), the `errors.Is(err, errFailingStageUnsupported)` re-wording block (emit.go:4041 area), and the "pipeline in a conditional attribute branch … not supported yet" ExprAttr rejection if any remnant survived Task 1
- Corpus: rename + flip `pipeerr/cond_attr_branch_class_pipe_rejected.txtar` → `pipeerr/cond_attr_branch_class_pipe.txtar`; new cases below

**Interfaces:**
- Consumes: Task 1's `thunkPipeWrap` + thunk-local buffer plumbing; Task 2's `resolved` entries for branch nodes.
- Produces: no new API — branch class parts lower exactly like element-level class parts.

- [x] **Step 1: Flip the rejected case (failing corpus case first)**

`git mv internal/corpus/testdata/cases/pipeerr/cond_attr_branch_class_pipe_rejected.txtar internal/corpus/testdata/cases/pipeerr/cond_attr_branch_class_pipe.txtar`, rewrite its header comment (now pins the WORKING lowering: hoists inside the `(Attrs, error)` thunk), empty its `diagnostics.golden`, set `render.golden` to `<div class="a">Hi</div>` (invoke stays `Csv: "a,b"` — `parse` → `["a","b"]`, `pick(0)` → `"a"`). Run `go test ./internal/corpus -run 'TestCorpus/pipeerr/cond_attr_branch_class_pipe' ` → FAIL (still rejected) = RED.

- [x] **Step 2: Wire the class path through**

In `condBranchAttrs`' `*ast.ClassAttr` case: pass the thunk-local `b`, `interpTemp`, `resolved`, and `thunkPipeWrap(b, interpTemp)` into `classEntryExpr` (emit mode) instead of `nil`s. In `classEntryExpr`: delete the three branch-mode (`b == nil`) attrError edges — with a real `b` they're unreachable; simplify accordingly (the `b == nil` state disappears entirely — assert/panic-free: make `b` required and remove the nil checks). Delete the emit.go:4041 `errors.Is` message-rewording block. CF arms and ordered parts inside branches now use the same `hoistValueCF`/ordered-hoist machinery with the thunk-local buffer — their `return` statements must be the two-value form: audit every `hoistTuple` call reachable from branch context and route it through `hoistTupleReturning(…, "return nil, _gsxerr")` (thread the errReturn choice, or the wrap, from `condBranchAttrs` — mirror how Task 1 did it for ExprAttrs).

- [x] **Step 3: Add the remaining matrix cases**

Under `internal/corpus/testdata/cases/pipeerr/` (each mirroring `cond_attr_branch_mid_stage.txtar`'s syntax + a case-local `filters/filters.go`; add a package-level `func cls(v string) (string, error) { return "c-" + v, nil }` helper in `helpers.go` where a direct tuple call is needed):

- `cond_attr_branch_class_tuple.txtar` — `class={ cls(csv) }` (the old raw-leak; now renders `<div class="c-a,b">Hi</div>`)
- `cond_attr_branch_attr_tuple.txtar` — `data-x={ cls(csv) }` plain tuple ExprAttr in a branch
- `cond_attr_branch_class_pipe_noerr.txtar` — `class={ csv |> upper }` (no-error pipe in a branch class part — previously rejected)
- `cond_attr_branch_else_pipe.txtar` — else-branch carries the error pipe; invoke with cond FALSE → else evaluates, renders; a second invoke-variant case is unnecessary (laziness of the untaken THEN branch is the existing untaken case's job)
- `cond_attr_branch_cf_arm.txtar` — `class={ if ok { csv |> parse |> pick(0) } else { "z" } }` inside a component cond-attr branch (value-form CF arm; previously "not supported yet")

- [x] **Step 4: Regenerate, inspect, verify**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && go test ./internal/codegen -count=1`
Expected: PASS. Inspect each new golden: hoists inside the thunk with `return nil, _gsxerr`; CF-arm hoists inside their if/case blocks inside the thunk; call-site `_gsxvN, _gsxerr := gsx.AttrsCond(...)` unwrap. `git diff --stat` scope: only `pipeerr/` + `coverage.golden` + emit.go (+ analyze.go only if Step 2 revealed a probe gap — if so, say so in the report).

- [x] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(codegen): (R, error) class parts and tuples inside cond-attr branches"
```

---

### Task 4: Docs, ROADMAP close-out, full CI

**Files:**
- Modify: `docs/ROADMAP.md` (close the known-gap row), `docs/guide/syntax/pipelines.md` (remove the branch-class-parts exception sentence)
- Verify: `make ci`

- [ ] **Step 1: ROADMAP** — find the known-gap entry (grep `_gsxrt.Class` in docs/ROADMAP.md); mark it CLOSED by this change with a one-line pointer to `docs/superpowers/specs/2026-07-03-attrscond-error-design.md`; also update the shipped pipe-error bullet if it references the statement form.

- [ ] **Step 2: pipelines.md** — remove/replace the exception sentence added by PR #29's final fix (grep `conditional-attribute` in docs/guide/syntax/pipelines.md); error-returning filters now work in class parts inside cond-attr branches too. Match page voice; no bare `{{ }}` outside `::: v-pre`.

- [ ] **Step 3: Full gate** — `make ci` → PASS (report tail). Also `make lint`.

- [ ] **Step 4: Commit**

```bash
git add docs && git commit -m "docs: close the cond-attr branch class-parts gap (AttrsCond error thunks)"
```

- [ ] **Step 5 (controller, not implementer): final adversarial review + finishing-a-development-branch** — probes must include: error pipe in BOTH branches with both conds; nested component in a branch (Card inside Card's branch? cond-attr on inner); `AttrsCond` direct-call compile check for any repo-internal usage; `-race` on codegen; untaken-branch side-effect proof under the new form; a template using cond-attrs WITHOUT any filters (plain path golden shape).

---

## Self-review notes

- Spec §1→Task 1, §2→Tasks 1+3, §3→Task 2, §5→Tasks 1/3/4, §6→Task 4 + controller step. No gaps found.
- Type consistency: `hoistTupleReturning`/`thunkPipeWrap`/`condAttrsExpr` signatures match across Tasks 1–3.
- Anchors from 5263f91; re-grep on drift.
- Deliberate sequencing: the rejected-case golden stays UNCHANGED through Tasks 1–2 (class path keeps nils until Task 3), so each task's golden scope is a meaningful review gate.
