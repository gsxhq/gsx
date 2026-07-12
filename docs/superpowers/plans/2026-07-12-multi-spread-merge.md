# Multi-spread merge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow more than one attribute spread on one element/component; multiple spreads (plus any interposed statics and conditional spreads/statics) merge by strict source-order last-wins.

**Architecture:** For an element with **≥2 spreads** (counting cond-nested), fold *all* the element's attributes into one `_gsxrt.ConcatAttrs(...)` expression in strict source order (adjacent statics coalesced into `Attrs{}` literals, spreads as bag exprs, cond-attrs as `AttrsCond` temps) and render it through the existing single-bag leaf `emitManualSpreadElement` via a synthetic one-spread element (`splitIdx=0`). The 0- and 1-spread paths are unchanged. Components already fold via `ConcatAttrs`; the one gap (a spread inside a cond branch) is closed by extending `condBranchAttrs`. No new runtime API.

**Tech Stack:** Go; `internal/codegen` (emit.go, filters.go); root `gsx` runtime (`ConcatAttrs`, `AttrsCond`, `Spread` — all already exist); txtar corpus (`internal/corpus`); `testing.F` fuzz.

**Spec:** `docs/superpowers/specs/2026-07-12-multi-spread-merge-design.md`.

> **Refinement vs spec §Design.** The spec describes folding only the *spread-span* and keeping pre/post compile-time zones for multi-spread. This plan instead does a **full fold** for ≥2 spreads (all attributes, `splitIdx=0`) because the component composer does not preserve source order for a leading static bag, and the full fold *is* the reference semantics from the spec's Governing principle — so it is correct by construction and simpler. Pre/post compile-time zones are retained only for the 0/1-spread fast paths (dispatch rows E0–E3), exactly as the principle prescribes.

## Global Constraints

- Go pinned to `GO_VERSION` in `.github/workflows/ci.yml` (currently **1.26.1**); a different minor reintroduces gofmt drift.
- Runtime (root `gsx` package) is **standard-library only** — no new deps. No new runtime API is needed (`ConcatAttrs`/`AttrsCond`/`Spread` already exist).
- **Every syntax/codegen change ships a corpus case**; new syntax valid in multiple contexts needs a case per context (text/attr/style/script/JS/child-prop — here: element-attr and component contexts).
- **Never hand-edit `.x.go` or golden files** — regenerate: `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.
- Prefer **unexported** identifiers unless serialization needs export.
- No "simple heuristics" — real implementations only.
- Before merge: `make ci` (authoritative, uncached). Inner loop: `make check`.
- Corpus `.txtar` section headers are literal: `-- input.gsx --`, `-- invoke --`, `-- diagnostics.golden --`, `-- render.golden --`, `-- generated.x.go.golden --`. A **render** case has an `invoke` section + empty `diagnostics.golden`; a **rejection** case omits `invoke` and populates `diagnostics.golden`.

---

## Reference: key existing code (verified line numbers on the rebased tree)

- `genNode` `case *ast.Element:` — `internal/codegen/emit.go:1651-1733`. The multi-spread **reject block** is `1664-1697` (`if first, second, … := firstTwoSpreadAttrs(t.Attrs); second != nil { … bag.Errorf … return false }`). The single-spread dispatch is the `else if splitIdx, found := bagSpreadIndex(t.Attrs); found { return emitManualSpreadElement(...) }` at `1698-1700`.
- `firstTwoSpreadAttrs(attrs []ast.Attr) (first, second *ast.SpreadAttr, firstCond, secondCond string)` — `emit.go:1383-1405`. Descends into `*ast.CondAttr` branches. **Reused as the fold trigger** (`second != nil`).
- `bagSpreadIndex(attrs []ast.Attr) (idx int, found bool)` — `emit.go:1358-1365`. Top-level only.
- `emitManualSpreadElement(b *bytes.Buffer, el *ast.Element, splitIdx int, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool` — `emit.go:1299`. Reads `el.Attrs`, `el.Attrs[splitIdx].(*ast.SpreadAttr)`, `el.Tag`, `el.Void`, `el.Children`. Hoists a non-`"attrs"` spread `Expr` into a temp automatically.
- `condAttrsExpr(t *ast.CondAttr, rtPkg, tag, mergeExpr string, table funcTables, probeWrap bool, resolved map[ast.Node]types.Type, interpTemp *int) (string, map[string]string, error)` — `emit.go:5602-5637`. Emits `rtPkg.AttrsCond(cond, thenThunk, elseArg)`; each thunk body built by `condBranchAttrs`.
- `condBranchAttrs(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type) (string, map[string]string, error)` — `emit.go:5665`. `switch` at `5669`; **default rejection** (SpreadAttr / nested CondAttr) at `5760-5762` (code `unsupported-component-attr`). Non-`class` ClassAttr rejection at `5739-5741` (keep). Returns `rtPkg.Attrs{…}` literal.
- `childPropsLiteral` (component bag builder) — `emit.go:4978`; `segments`/`bag` accumulation `5038-5041`, `SpreadAttr` case `5197-5208`, `CondAttr` case `5209-5225`, join `5306-5340`.
- Helpers: `spreadAttrExpr(a *ast.SpreadAttr, table funcTables, imports map[string]bool, b *bytes.Buffer, interpTemp *int, bag *diag.Bag) (string, bool)` — `emit.go:2553`; `lowerPipe(seed string, stages []ast.PipeStage, table funcTables, wrap func(call string) string) (expr string, usedPkgs map[string]string, err error)` — `filters.go:64`; `hoistTuple(b, expr, interpTemp) string` — `emit.go:2025` (wraps `hoistTupleReturning(..., "return _gsxerr")`); `classEntryExpr(b, interpTemp, a *ast.ClassAttr, rtPkg, mergeExpr string, table, resolved, probeWrap bool, wrap func(string) string, errReturn string) (string, map[string]string, error)` — `emit.go:5387`; `emitPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string` (emit-mode wrap; used by `spreadAttrExpr`), `thunkPipeWrap(&tb, interpTemp)` (thunk-local).
- AST (`ast/ast.go`): `Element` (`200-236`; embeds `span`, has `Tag/Void/Attrs/Children`), `SpreadAttr{span; Expr string; ExprPos token.Pos; Stages []PipeStage}` (`378-388`), `CondAttr{span; Cond string; CondPos; Then, Else []Attr}` (`522-530`), `PipeStage` (`339-345`), `StaticAttr`, `ExprAttr`, `BoolAttr`, `ClassAttr`.
- `rtImports.rt() string` returns the runtime import alias (e.g. `_gsxrt`); use `rt.rt()` for `ConcatAttrs`/`AttrsCond`/`Attrs`.

## Corpus cases to flip (rejection → render) — exact paths

1. `internal/corpus/testdata/cases/spread-sanitize/two_spreads_error.txtar` (E4, two local bags) — **rename** to `.../multispread/two_bags_merge.txtar`.
2. `internal/corpus/testdata/cases/fallthrough/second_spread_rejected.txtar` (E4, `{attrs...}{extra...}`).
3. `internal/corpus/testdata/cases/fallthrough/byo_bag_two_spreads.txtar` (E4, byo).
4. `internal/corpus/testdata/cases/jsattr/manual_multi_spread_rejected.txtar` (E4, manual mode).
5. `internal/corpus/testdata/cases/fallthrough/cond_attr_nested_spread_rejected.txtar` (E6, `{if a{{extra...}}}{attrs...}`).
6. `internal/corpus/testdata/cases/spread-sanitize/cond_nested_two_spreads.txtar` (E6, two cond spreads).
7. `internal/corpus/testdata/cases/spread-sanitize/cond_nested_spread_after_toplevel.txtar` (E6, top-level + cond spread).
8. `internal/corpus/testdata/cases/components/component_cond_branch_spread_rejected.txtar` (C2, component spread-in-branch) — flipped in **Task 2**; the rest in **Task 3**.

Do NOT touch these (already-passing render cases): `spread-sanitize/cond_nested_spread.txtar`, `spread-sanitize/cond_nested_spread_class.txtar`, `srcset-sanitize/cond_nested_spread.txtar`.

---

## Task 1: Extract `composeBag` from `condBranchAttrs` (pure refactor)

Pull the per-attr-kind "attrs → `rtPkg.Attrs{…}` literal" lowering out of `condBranchAttrs` into a standalone `composeBag`, so the element fold (Task 3) and the cond-branch path share one copy. **No behavior change; all goldens unchanged.**

**Files:**
- Modify: `internal/codegen/emit.go` (`condBranchAttrs` at `5665`; add `composeBag` nearby)
- Test: existing corpus + codegen suites (regression only)

**Interfaces:**
- Produces: `composeBag(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type, errReturn string) (expr string, used map[string]string, err error)` — returns a single Go expression that evaluates to `rtPkg.Attrs` (currently always a `rtPkg.Attrs{…}` literal; Task 2 extends it to emit `ConcatAttrs(...)` when spreads are present).

- [ ] **Step 1: Capture the current behavior as the baseline**

Run the two suites that exercise `condBranchAttrs` and save output; these must be byte-identical after the refactor.

Run: `go test ./internal/corpus ./internal/codegen -count=1`
Expected: PASS (record the pass count).

- [ ] **Step 2: Add `composeBag`, moving the loop body verbatim**

Copy the `switch t := a.(type)` body from `condBranchAttrs` (`emit.go:5669-5762`) into a new function `composeBag` with the signature in **Interfaces** above. Keep the exact per-kind logic (static→`{Key,Value}`; expr→`lowerPipe`+tuple-unwrap+`applyRenderer`; bool→`{Key,Value:true}`; class→`classEntryExpr`; the non-`class` ClassAttr rejection at `5739-5741`; the default rejection at `5760-5762`). Assemble entries into `fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(entries, ", "))` exactly as `condBranchAttrs` does today (`~5765`). Pass `errReturn` through to `classEntryExpr` in place of the value it currently receives.

- [ ] **Step 3: Make `condBranchAttrs` delegate to `composeBag`**

Replace `condBranchAttrs`' body with a call:

```go
func condBranchAttrs(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type) (string, map[string]string, error) {
	return composeBag(b, interpTemp, wrap, probeWrap, attrs, rtPkg, tag, mergeExpr, table, resolved, "return _gsxerr")
}
```

(The thunk previously used its own error-return convention via `wrap`; `classEntryExpr`'s `errReturn` arg was `"return _gsxerr"` in this path — confirm against `emit.go:5665-5765` and preserve whatever string it passed. If it passed something else, thread that exact string.)

- [ ] **Step 4: Verify goldens unchanged**

Run: `go test ./internal/corpus ./internal/codegen -count=1`
Expected: PASS, identical count to Step 1. If any golden changed, the refactor altered behavior — revert and redo minimally.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/codegen/emit.go
git add internal/codegen/emit.go
git commit -m "refactor(codegen): extract composeBag from condBranchAttrs

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 2: Extend `composeBag` for spreads + nested conds in a branch (closes C2)

Let a branch (and, in Task 3, the element span) contain spreads and nested cond-attrs. `composeBag` coalesces adjacent statics into `Attrs{…}` and emits `ConcatAttrs(...)` when any spread/cond is present.

**Files:**
- Modify: `internal/codegen/emit.go` (`composeBag`)
- Corpus: flip `components/component_cond_branch_spread_rejected.txtar`; add cond-branch-spread render cases
- Test: `internal/corpus`

**Interfaces:**
- Consumes: `composeBag` (Task 1), `spreadAttrExpr`, `condAttrsExpr`, `hoistTuple`.
- Produces: `composeBag` now returns either a single `rtPkg.Attrs{…}` literal (statics only — unchanged), a single bare spread/cond expr (one entry, no statics), or `rtPkg.ConcatAttrs(part₁, …)` (mixed / multiple), preserving source order.

- [ ] **Step 1: Write the failing corpus case (component spread-in-branch)**

Rewrite `internal/corpus/testdata/cases/components/component_cond_branch_spread_rejected.txtar` to a render case and rename to `components/component_cond_branch_spread.txtar`:

```
# A spread inside a component conditional branch composes as AttrsCond over
# ConcatAttrs(branch statics, spread) — no longer rejected.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Card(title gsx.Node) { <div class="card">{title}</div> }

component Page(hot bool, extra gsx.Attrs) {
	<Card title="Hi" { if hot { data-h="1" { extra... } } } />
}
-- invoke --
Page(PageProps{Hot: true, Extra: gsx.Attrs{{Key: "id", Value: "x"}}})
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 2: Run it — expect failure (still rejected)**

Run: `go test ./internal/corpus -run 'TestCorpus/components/component_cond_branch_spread' -count=1`
Expected: FAIL with `unsupported attribute *ast.SpreadAttr in a conditional branch (<Card>)`.

- [ ] **Step 3: Extend `composeBag`**

Restructure the loop to accumulate a `parts []string` where a maximal run of static/expr/bool/class attrs flushes to one `rtPkg.Attrs{…}` part, and each `*ast.SpreadAttr` / `*ast.CondAttr` becomes its own part. Remove the two default-case rejections for `*ast.SpreadAttr` and `*ast.CondAttr` (keep the non-`class` ClassAttr rejection). Sketch:

```go
func composeBag(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type, errReturn string) (string, map[string]string, error) {
	used := map[string]string{}
	var parts []string
	var entries []string // pending static/expr/bool/class {Key,Value} entries
	flush := func() {
		if len(entries) > 0 {
			parts = append(parts, fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(entries, ", ")))
			entries = nil
		}
	}
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.SpreadAttr:
			flush()
			expr := strings.TrimSpace(t.Expr)
			if len(t.Stages) > 0 {
				lowered, u, perr := lowerPipe(t.Expr, t.Stages, table, wrap)
				if perr != nil {
					return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: strings.TrimPrefix(perr.Error(), "codegen: ")}
				}
				maps.Copy(used, u)
				expr = lowered
			}
			parts = append(parts, expr)
		case *ast.CondAttr:
			flush()
			condExpr, u, cerr := condAttrsExpr(t, rtPkg, tag, mergeExpr, table, probeWrap, resolved, interpTemp)
			if cerr != nil {
				return "", nil, cerr
			}
			maps.Copy(used, u)
			if probeWrap {
				condExpr = "_gsxunwrap(" + condExpr + ")"
			} else {
				condExpr = hoistTuple(b, condExpr, interpTemp)
			}
			parts = append(parts, condExpr)
		// ... existing StaticAttr / ExprAttr / BoolAttr / ClassAttr / CommentAttr / EmbeddedAttr
		//     cases unchanged, but each appends to `entries` (not a local slice)
		//     and records `used`.
		default:
			msg := fmt.Sprintf("unsupported attribute %T in a conditional branch (<%s>)", a, tag)
			return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unsupported-component-attr", msg: msg}
		}
	}
	flush()
	switch len(parts) {
	case 0:
		return fmt.Sprintf("%s.Attrs{}", rtPkg), used, nil
	case 1:
		return parts[0], used, nil
	default:
		return fmt.Sprintf("%s.ConcatAttrs(%s)", rtPkg, strings.Join(parts, ", ")), used, nil
	}
}
```

Note: `condAttrsExpr` recurses into `composeBag` via `condBranchAttrs`, so nested cond-attrs now compose too. `maps` is already imported (`condAttrsExpr` uses `maps.Copy`).

- [ ] **Step 4: Regenerate + verify the flipped case**

```bash
go test ./internal/corpus -run 'TestCorpus/components/component_cond_branch_spread' -update
go test ./internal/corpus -run 'TestCorpus/components/component_cond_branch_spread' -count=1
```
Expected: PASS; `render.golden` shows `<div class="card">Hi</div>` (the `extra`/`data-h` land on the div only if `Card` forwarded them — here `Card` does not spread, so assert the *generated* `ConcatAttrs(gsx.Attrs{{Key:"data-h",...}}, extra)` appears in `generated.x.go.golden`). Adjust `Card` to forward (`<div class="card" { attrs... }>`) if you want the attrs observable in `render.golden` — prefer that so the render is meaningful.

- [ ] **Step 5: Full regression + commit**

```bash
go test ./internal/corpus ./internal/codegen -count=1
gofmt -w internal/codegen/emit.go
git add -A
git commit -m "feat(codegen): compose spreads + nested conds inside a cond branch

Closes the component 'cond branches stay shallow' gap (#88-adjacent C2).

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 3: Element ≥2-spread full fold

Replace the reject block with a full fold: build `ConcatAttrs(...)` over **all** the element's attributes via `composeBag`, render through a synthetic one-spread element.

**Files:**
- Modify: `internal/codegen/emit.go` (`genNode` element case `1664-1697`; add `foldElementSpreads`)
- Corpus: flip cases 1–7 (above); add E4/E5/E6/E7 render cases under `multispread/`
- Test: `internal/corpus`

**Interfaces:**
- Consumes: `composeBag` (Tasks 1–2), `emitManualSpreadElement`, `firstTwoSpreadAttrs`, `emitPipeWrap`.
- Produces: `foldElementSpreads(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool`

- [ ] **Step 1: Write the failing corpus case (E4 — line 91 shape)**

Create `internal/corpus/testdata/cases/multispread/two_bags_source_order.txtar`:

```
# Two spreads on one element merge by source order: the later bag wins per key,
# class aggregates. (Reverses the old at-most-one-spread error.)
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Field(sync gsx.Attrs, extra gsx.Attrs) {
	<input type="hidden" name="n" class="base" { sync... } { extra... }/>
}
-- invoke --
Field(FieldProps{
	Sync:  gsx.Attrs{{Key: "data-a", Value: "1"}, {Key: "class", Value: "s"}},
	Extra: gsx.Attrs{{Key: "data-a", Value: "2"}, {Key: "class", Value: "e"}},
})
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

- [ ] **Step 2: Run it — expect the rejection**

Run: `go test ./internal/corpus -run 'TestCorpus/multispread/two_bags_source_order' -count=1`
Expected: FAIL with `element with a spread { sync... } cannot carry another spread { extra... }`.

- [ ] **Step 3: Add `foldElementSpreads`**

```go
// foldElementSpreads renders a non-component element carrying ≥2 spreads
// (counting cond-nested) by folding ALL its attributes into one source-ordered
// ConcatAttrs(...) bag and rendering that through the single-bag leaf. This is
// the reference full-fold: last writer wins per key, class/style aggregate.
func foldElementSpreads(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	expr, used, err := composeBag(b, interpTemp, emitPipeWrap(b, interpTemp), false, el.Attrs, rt.rt(), el.Tag, classMergeExpr(mergeExpr, rt), table, resolved, "return _gsxerr")
	if err != nil {
		if ae, ok := errors.AsType[*attrError](err); ok {
			bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
		} else {
			bag.Errorf(el.Pos(), el.End(), "spread-fold", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		}
		return false
	}
	for path := range used {
		imports[path] = true
	}
	synth := &ast.Element{
		Tag:      el.Tag,
		Void:     el.Void,
		Children: el.Children,
		Attrs:    []ast.Attr{&ast.SpreadAttr{Expr: expr}},
	}
	// preserve span for diagnostics/emitLine — copy el's positions.
	*synthSpan(synth) = *elemSpan(el) // see note below
	return emitManualSpreadElement(b, synth, 0, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
}
```

Note on `span`: `ast.Element` embeds an unexported `span`. If it cannot be set from `internal/codegen`, instead reuse `el` itself by temporarily swapping its `Attrs`: save `orig := el.Attrs`, set `el.Attrs = []ast.Attr{synthSpread}`, call `emitManualSpreadElement(b, el, 0, …)`, then `el.Attrs = orig`. This keeps the real `span`, `Pos()`, `Void`, `Children`. Prefer the swap — it avoids fabricating a `span`. Verify the swap does not leak (restore in a `defer`).

The synthesized `SpreadAttr.Expr` is a full Go expression (`_gsxrt.ConcatAttrs(...)` or a lone bag temp), not the literal `"attrs"`, so `emitManualSpreadElement` hoists it into a temp automatically (`emit.go:1301-1310`). `composeBag` has already emitted any `AttrsCond`/pipe hoists into `b` beforehand, so the temp references resolve in order.

- [ ] **Step 4: Wire the fold into `genNode`; delete the reject block**

Replace `emit.go:1664-1697` (the whole `if first, second, … := firstTwoSpreadAttrs(...); second != nil { … return false }` block) with:

```go
		if _, second, _, _ := firstTwoSpreadAttrs(t.Attrs); second != nil {
			// ≥2 spreads (counting cond-nested): full source-order fold.
			return foldElementSpreads(b, t, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		} else if splitIdx, found := bagSpreadIndex(t.Attrs); found {
			return emitManualSpreadElement(b, t, splitIdx, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		}
```

`firstTwoSpreadAttrs`, `firstCond`, `secondCond` are no longer used for messaging; keep `firstTwoSpreadAttrs` (still the trigger). Remove now-dead helpers only if `gopls check -severity=hint internal/codegen/emit.go` flags them.

- [ ] **Step 5: Regenerate + verify E4 case**

```bash
go test ./internal/corpus -run 'TestCorpus/multispread/two_bags_source_order' -update
go test ./internal/corpus -run 'TestCorpus/multispread/two_bags_source_order' -count=1
```
Expected: PASS; `render.golden` = `<input type="hidden" name="n" class="base s e" data-a="2"/>` (later bag wins `data-a`; class aggregates `base`+`s`+`e`). Confirm the exact class string against `DefaultClassMerge` output; if the merger dedups/reorders, pin whatever it emits.

- [ ] **Step 6: Flip element reject cases 1–7**

For each of paths 1–7 in the "Corpus cases to flip" list: remove the `diagnostics.golden` body, add an `-- invoke --` calling the component, add empty `-- render.golden --` and `-- generated.x.go.golden --`. Example for `fallthrough/second_spread_rejected.txtar`:

```
# An element carries two spreads; they merge by source order (later wins).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Link(extra gsx.Attrs) {
	<a href="/base" { attrs... } { extra... }>x</a>
}
-- invoke --
Link(LinkProps{Attrs: gsx.Attrs{{Key: "data-y", Value: "1"}}, Extra: gsx.Attrs{{Key: "data-y", Value: "2"}}})
-- diagnostics.golden --
-- render.golden --
-- generated.x.go.golden --
```

(For cases 5–7, which are E6, ensure Task 2's cond-branch support is present — it is.) Then:

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
```
Expected: PASS. Inspect each new `render.golden` and confirm it matches source-order last-wins by hand before trusting `-update`.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/codegen/emit.go
git add -A
git commit -m "feat(codegen): multiple element spreads merge by source order (full fold)

Reverses the at-most-one-spread rule (E4-E7). ConcatAttrs fold rendered via the
existing single-bag leaf; last writer wins per key, class/style aggregate.

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 4: Differential test — fast paths ≡ full fold (governing principle)

Prove the 0/1-spread optimized paths and the fold produce identical bytes to a naive full-fold reference, plus a runtime `testing.F` fuzz on the merge semantics.

**Files:**
- Create: `internal/codegen/spread_fold_diff_test.go` (batched codegen matrix)
- Create: `attrs_fold_fuzz_test.go` (root pkg; runtime leaf fuzz)
- Test: both

- [ ] **Step 1: Runtime leaf fuzz (`testing.F`) — merge semantics**

Create `attrs_fold_fuzz_test.go` in package `gsx`. Fuzz a random sequence of contributors encoded from the fuzz bytes: each contributor is either a bag (list of `key=val` from a 4-key alphabet incl. `class`,`style`,`href`) or a conditional bag (taken/untaken). Build `got := renderBag(ConcatAttrs(contributors...))` and `want := renderBag(referenceLastWins(contributors...))` where `renderBag` is `W(&buf).Spread(ctx, bag, nav, nil, nil, nil, nil)` (nav = `[]string{"href"}`) and `referenceLastWins` computes source-order last-wins + class/style aggregation independently (ported from the rule, NOT from `ConcatAttrs`). Assert `got == want`. Seed with `f.Add` for: two disjoint bags, two colliding bags, class-in-both, an untaken conditional, empty bags.

```go
func FuzzAttrsFoldMatchesReference(f *testing.F) {
	f.Add([]byte{0x01, 0x11, 0x22})
	f.Add([]byte{0xff, 0x00, 0xaa, 0x55})
	f.Fuzz(func(t *testing.T, data []byte) {
		contribs := decodeContribs(data) // []Attrs, deterministic from bytes
		var g, w bytes.Buffer
		W(&g).Spread(context.Background(), ConcatAttrs(contribs...), []string{"href"}, nil, nil, nil, nil)
		W(&w).Spread(context.Background(), referenceLastWins(contribs), []string{"href"}, nil, nil, nil, nil)
		if g.String() != w.String() {
			t.Fatalf("fold != reference\ncontribs=%v\n got=%q\nwant=%q", contribs, g.String(), w.String())
		}
	})
}
```

Write `decodeContribs` (bytes → `[]Attrs` over a fixed key alphabet) and `referenceLastWins([]Attrs) Attrs` (independent last-wins + class join `" "` / style join `"; "`). Run: `go test . -run '^$' -fuzz '^FuzzAttrsFoldMatchesReference$' -fuzztime 30s` → expect no failures; then a normal `go test .` runs the seed corpus.

- [ ] **Step 2: Batched codegen differential matrix**

Create `internal/codegen/spread_fold_diff_test.go`. Emit ONE `.gsx` package containing many components spanning E0–E7 (0/1/2/3 spreads × interposed static × cond spread × class/style × `href`), generate + build + render each (reuse the corpus batch harness helpers, or a minimal in-test generate+render). For each, compute the reference render by folding that component's attributes into one `ConcatAttrs` and rendering via `W(&buf).Spread(...)`, and assert byte equality. `packages.Load` runs once for the whole package (per the corpus `batchCodegen` pattern — do NOT loop `testing.F` here).

- [ ] **Step 3: Run + commit**

```bash
go test . -run 'FuzzAttrsFoldMatchesReference' -count=1
go test ./internal/codegen -run 'TestSpreadFoldDiff' -count=1
git add -A && git commit -m "test: differential fold≡reference (runtime fuzz + codegen matrix)

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 5: Per-context corpus + O1 probe + fmt corpus

**Files:**
- Create: cases under `internal/corpus/testdata/cases/multispread/`
- Create: `internal/gsxfmt/testdata/cases/multispread_layout.txtar`
- Test: `internal/corpus`, `internal/gsxfmt`

- [ ] **Step 1: Per-context render cases (one per row)**

Add cases (each `input.gsx` + `invoke` + empty goldens, then `-update`). Pin source-order last-wins + leaf escaping. Minimum set:
- `multispread/interposed_url.txtar` — `<a { p... } href="/mid" { q... }>` with `q` carrying a `javascript:` href → asserts leaf sanitization (`about:invalid#gsx` when q wins) AND byte-identity vs the constant when q lacks href.
- `multispread/interposed_class_style.txtar` — interposed `class="x"` and `style="color:red"` aggregate across bags.
- `multispread/interposed_bool_scalar.txtar` — interposed `disabled` (bool) + `data-n={5}` (numeric).
- `multispread/interposed_pipe_hole.txtar` — `href={u |> someFilter}` between two spreads (needs a filter in `gsx.toml` `filter_packages`; mirror an existing pipe corpus case's config).
- `multispread/cond_spread_else.txtar` — `{ if c { {x...} } else { {y...} } }` alongside a spread (E6, both branches via two `invoke`? use one; add a second case for the untaken branch).
- `multispread/cond_interposed_static.txtar` — `{ if c { title="t" } }` between spreads (E7).
- `multispread/three_spreads.txtar` — `{ a... } { b... } { c... }` (last wins across all three).
- `multispread/nonce_script_two_spreads.txtar` — two spreads on `<script>` (nonce inherited); include `gsx.toml` enabling nonce like an existing `nonce/` case.
- `multispread/void_and_children.txtar` — a void `<input>` and a non-void with children.

Regenerate + verify:
```bash
go test ./internal/corpus -run 'TestCorpus/multispread' -update
go test ./internal/corpus -run TestCorpus -count=1
```

- [ ] **Step 2: O1 probe — lone cond-spread + root class**

Add `internal/corpus/testdata/cases/multispread/O1_lone_cond_spread_root_class.txtar`: `<div class="root" { if on { { extra... } } }>` where `extra` carries `class`. Render with `on=true`. Inspect: does the root `class="root"` aggregate with `extra`'s class, or emit a duplicate `class` attribute? If duplicate → the lone-cond-spread fast path diverges from the fold; **route the lone cond-spread case (exactly 1 spread, cond-nested) through `foldElementSpreads` too** (change the Task 3 Step 4 trigger to also fire when the single spread is cond-nested), accept its golden churn, and re-run the differential (Task 4) to confirm equivalence. If it aggregates correctly → leave the fast path; record the case as the equivalence witness.

- [ ] **Step 3: fmt corpus case (layout only)**

Create `internal/gsxfmt/testdata/cases/multispread_layout.txtar` with `input.gsx` = a multi-spread element (`{ a... } { b... }` and one with an interposed static) and generate `fmt.golden`:
```bash
go test ./internal/gsxfmt -run TestFmtCorpus -update
go test ./internal/gsxfmt -run TestFmtCorpus -count=1
```

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "test: per-context multispread corpus + O1 probe + fmt layout

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 6: Docs + ROADMAP + memory

**Files:**
- Modify: `docs/guide/syntax/composition.md:231-235`
- Modify: `docs/ROADMAP.md`
- Modify: sibling: none (multiple spreads already parse — no grammar change)

- [ ] **Step 1: Rewrite the composition guide paragraph**

Replace the "An element carries **one** forwarding spread…" text (`composition.md:231-235`) with a concise statement of the merge rule (behavior only — rationale stays in the spec, per the docs-concise rule):

```md
An element may carry multiple attribute spreads. They merge by source order —
later spreads win per key, `class`/`style` aggregate — the same rule as any two
attributes of the same name. `{ a... } { b... }` is `b` overriding `a`.
```

Wrap any literal `{{ }}` in a `::: v-pre` block if introduced (none here). Do NOT run the docs build unless editing `docs/guide/**` further; note the CI `docs` job is separate from `make ci`.

- [ ] **Step 2: ROADMAP + verify**

Update `docs/ROADMAP.md`: remove the one-spread-per-element line(s) and note multi-spread merge shipped. Run: `git grep -n "one-spread\|one forwarding spread\|at-most-one-spread" docs/` → reconcile any stale prose.

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "docs: multi-spread merge — guide + ROADMAP

Claude-Session: https://claude.ai/code/session_01XVXtvft6e4MvspzvacpUx9"
```

---

## Task 7: `make ci` + independent adversarial review

- [ ] **Step 1: Full CI**

Run: `make ci`
Expected: PASS (build/vet/test both modules, examples drift, gofmt + gsx fmt). Fix any drift; regenerate goldens if codegen output legitimately changed; never hand-edit goldens.

- [ ] **Step 2: `make lint`**

Run: `make lint`
Expected: clean.

- [ ] **Step 3: Independent adversarial review**

Dispatch one independent reviewer (per CLAUDE.md process) that **builds throwaway probe programs**, not just reads the diff. Probes to write: (a) a `.gsx` with three spreads where all three collide on one key + `class` + a URL key, generate and assert the rendered attribute equals hand-computed source-order last-wins; (b) a spread whose bag carries `HREF` (uppercase) after a lowercase `href` static, confirm case-fold last-wins + sanitization; (c) untaken conditional spread referencing a nil pointer (`{ if u != nil { {u.Attrs...} } }`, `u == nil`) — confirm no panic; (d) an element with a spread before and after an interposed URL static, confirm the static sanitizes and ordering matches. Fix anything found; re-run `make ci`.

- [ ] **Step 4: Finish the branch**

Use `superpowers:finishing-a-development-branch` to open the PR. Body: the dispatch table (E0–E7, C0–C2), the D2-stale note, the full-fold refinement, the deleted rejections + flipped cases, and the differential/fuzz strategy. `Closes` the multi-spread issue; note #88 is untouched.

---

## Self-review checklist (run before execution)

- **Spec coverage:** E4/E5/E7 → Task 3; E6 → Tasks 2+3; C2 → Task 2; C0/C1/E0–E3 unchanged (differential-verified Task 4); deleted rejections + flipped cases → Tasks 2–3; differential + fuzz → Task 4; per-context + O1 + fmt → Task 5; docs/ROADMAP → Task 6; CI/review → Task 7. ✓
- **Placeholder scan:** the one deliberate open branch is O1 (Task 5 Step 2) — it has a concrete probe + both outcomes specified, not a TODO. `composeBag`'s unchanged static/expr/bool/class cases are referenced, not re-pasted, because Task 1 moves them verbatim (the engineer sees them in the moved code).
- **Type consistency:** `composeBag` signature identical in Tasks 1–3; `foldElementSpreads` param list mirrors `emitManualSpreadElement`; `rt.rt()`, `classMergeExpr(mergeExpr, rt)`, `emitPipeWrap(b, interpTemp)`, `hoistTuple(b, expr, interpTemp)` used as defined in the Reference section.
