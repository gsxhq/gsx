# Nested literals in every Go-expression position — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lower f/js/css literals and element literals nested inside Go expressions in every position PR #213 gates, plus element literals in `{{ }}`, with body-interpolation semantics.

**Architecture:** Each gated field gets a codegen-only `…Embedded []ast.GoPart` overlay filled by `materializeEmbeddedMarkup`. One probe helper (extracted from the `Interp.Embedded` case of `emitProbes`) and one emit helper (generalizing `assembleHoleSeed` + `genInterp`'s element splice) serve every consumer. Families are switched from the #213 gate to lowering one task at a time.

**Tech Stack:** Go 1.27.1, gsx codegen (`internal/codegen`), parser (`parser`), AST (`ast`), LSP (`internal/lsp`), txtar corpus (`internal/corpus`).

**Spec:** `docs/superpowers/specs/2026-09-29-nested-literal-lowering-design.md`

## Global Constraints

- Worktree: `/Users/jackieli/personal/gsxhq/gsx/.worktrees/nested-literal-lowering`, branch `nested-literal-lowering`. Every command runs there. Never commit to `main` or `gate-nested-literals`.
- Go must be exactly 1.27.1 (`go version`). Never add a `toolchain` directive.
- Runtime (root package) stays standard-library only.
- Emit ≡ probe: for every nested construct the skeleton probe and the emitted code have the same static type.
- Inner loop runs only the narrow test (`go test ./internal/corpus -run 'TestCorpus/<dir>' -count=1`, `go test ./parser -run X`, `go test ./internal/codegen -run X`). Never `make ci`/`go test ./...` except in the final task.
- Corpus goldens are regenerated with `-update`, then re-run without it. Never hand-edit goldens or `.x.go`.
- No "simple heuristics": lowering reuses the existing escaping/hoisting machinery; nothing string-matches Go.
- Adding a test that opens a new `codegen.Module` costs ~0.3s forever — prefer corpus cases and existing tables.
- Commit after each task with a message ending in `Claude-Session: https://claude.ai/code/session_01Yabds1nVHqDXsJsy1LLnmh`.

## Review Focus

1. A nested literal whose value reaches a URL sink through a component's forwarded `attrs` bag (`<Link href={wrap(f`javascript:@{x}`)}/>` with `href` spread onto `<a>`) must still be URL-sanitized — pinned in Task 3.
2. Recursion: an element literal inside an attribute value whose own attribute nests a literal (`h={wrapN(<a href={wrap(f`/x/@{id}`)}>x</a>)}`) must lower both levels — pinned in Task 3.
3. Laziness under nil: a nested literal whose hole dereferences a nil pointer, in a false cond-attr branch / value-form arm / `else if` after a true branch / unmatched case, must not panic — pinned in Tasks 3, 4, 5.
4. Reserved identifiers (`ctx`, `attrs`, `children`) used inside a nested hole in an attribute or header position resolve exactly as in a body interpolation — pinned in Task 3.
5. A Go type error inside a nested hole in an attribute value is reported at the hole's source position (not a skeleton line) — pinned in Task 7.

---

### Task 1: AST overlays, split, clone and walkers (no behaviour change)

**Files:**
- Modify: `ast/ast.go` (fields), `ast/clone.go`, `ast/walk_exhaustive_test.go` (`goPartSwitchFunctions`)
- Modify: `parser/attrs.go` (`splitOrderedPairs` → set `OrderedPair.ValuePos`)
- Modify: `internal/codegen/analyze.go` (`materializeEmbeddedMarkup`)
- Modify walkers: `internal/codegen/component_target.go` (`collectMaterializedComponentCandidates`, `callSiteRegistry.collectFile`), `internal/jsx/jsx.go`, `internal/codegen/rebase.go`, `internal/jsmin/file.go`, `internal/cssmin/file.go`, `internal/codegen/reserved_scan.go`, `internal/codegen/component_target.go` (`harvestElementQualifierRoots`), `internal/lsp/mapping.go` (`inspectWithEmbedded`)
- Test: `ast/clone_test.go`, `parser/nested_construct_test.go`

**Interfaces:**
- Produces (AST, all `[]GoPart`, nil unless split):
  `ExprAttr.Embedded`, `SpreadAttr.Embedded`, `OrderedPair.Embedded` + `OrderedPair.ValuePos token.Pos`, `ComposedPart.ExprEmbedded`, `ComposedPart.CondEmbedded`, `ValueArm.Embedded`, `ValueIf.CondEmbedded`, `ValueSwitch.TagEmbedded`, `ValueSwitchCase.ListEmbedded`, `CondAttr.CondEmbedded`, `SwitchAttr.TagEmbedded`, `AttrCaseClause.ListEmbedded`, `IfMarkup.CondEmbedded`, `ForMarkup.ClauseEmbedded`, `SwitchMarkup.TagEmbedded`, `CaseClause.ListEmbedded`.
  Each field's doc comment: "codegen-only overlay; see Interp.Embedded".
- Produces (codegen): `func splitGoField(src string, pos token.Pos) ([]gsxast.GoPart, bool)` inside `materializeEmbeddedMarkup` (closure or file-local func using the same `maySplit`, `SplitGoExprElements`, diagnostics and `syntaxOK` handling as `splitInterp`).
- The #213 gate (`internal/codegen/nested_literal_gate.go`) stays active for every family in this task.

- [ ] **Step 1: Failing tests.** In `ast/clone_test.go` add a table test that builds each node type above with a non-nil overlay holding one `GoText` and one `*EmbeddedInterp`, clones it, mutates the clone's overlay slice element, and asserts the original is unchanged. In `parser/nested_construct_test.go` add `TestOrderedPairValuePos`: parse `<C attrs={{ "k": wrap(f`a`) }}/>` and assert `src[ValuePos offset:]` starts with `wrap(`.
- [ ] **Step 2:** `go test ./ast ./parser -run 'Clone|OrderedPairValuePos' -count=1` → FAIL (fields don't exist).
- [ ] **Step 3: Implement** the fields, `cloneGoParts` deep copies in every clone function that copies these nodes, and `ValuePos` in `splitOrderedPairs` (offset of the first non-space byte of the value).
- [ ] **Step 4: Split.** In `materializeEmbeddedMarkup`, for each Element's attrs (recursing into CondAttr/SwitchAttr bodies, ComposedPart CF arms) and each control-flow node, set the overlay with `splitGoField(field, fieldPos)` **before** the gate call. Walk the new overlays' markup parts with the existing `walkParts` so nested elements get split and gated too. Skip `ExprAttr` whose whole value is one literal (same whole-literal test the gate uses; factor `isWholeLiteral(src string) bool` out of `gateNestedLiteral`).
- [ ] **Step 5: Walkers.** Every walker that today descends `Interp.Embedded`/`GoBlock.Embedded` descends the new overlays too (list under Files). Register each new GoPart switch in `goPartSwitchFunctions`.
- [ ] **Step 6:** `go test ./ast ./parser -count=1 && go test ./internal/corpus -count=1 && go test ./internal/printer ./internal/lsp -count=1` → PASS; the gate's corpus cases (`nested-literal-gate/*`) unchanged.
- [ ] **Step 7: Commit** `feat(ast,codegen): codegen-only Embedded overlays for every Go-expression field`.

### Task 2: Shared probe and emit helpers (refactor, no behaviour change)

**Files:**
- Modify: `internal/codegen/analyze.go` (`emitProbes` Interp case → helper; `writeSkeletonProbeExpr`)
- Modify: `internal/codegen/emit.go` (`genInterp`, `assembleHoleSeed`, `emitGoExprEmbeddedInterp`, GoBlock splice)
- Create: `internal/codegen/goparts_probe.go`, `internal/codegen/goparts_emit.go`

**Interfaces:**
- Produces (probe):
  ```go
  // probeScope is everything emitProbes threads into a nested probe.
  type probeScope struct {
  	table          funcTables
  	recvVar        string
  	recvTypeName   string
  	usedFilters    map[string]string
  	fset           *token.FileSet
  	ctrlOff        map[gsxast.Node]int
  	targetRegistry *componentTargetMarkerRegistry
  	gw             *[][]gsxast.Markup
  	bag            *diag.Bag
  	cfTemp         *int
  	enclosingAttrsBound bool
  }
  // writeProbeGoParts writes parts as one Go expression into eb: GoText
  // through writeSkeletonAuthoredAt, elements/fragments as _gsxelem(N)
  // IIFEs, literals via probeEmbeddedInterpIIFE.
  func writeProbeGoParts(eb skeletonWriter, parts []gsxast.GoPart, ps probeScope) error
  // writeEmbeddedProbe writes `parts |> stages` to sb with the mapped-seed and
  // target-marker boundary handling the Interp case uses today.
  func writeEmbeddedProbe(sb skeletonWriter, parts []gsxast.GoPart, stages []gsxast.PipeStage, owner gsxast.Node, ps probeScope) error
  ```
- Produces (emit):
  ```go
  // lowerCtx is what lowering a nested construct needs at one position.
  type lowerCtx struct {
  	resolved   map[ast.Node]types.Type
  	table      funcTables
  	imports    map[string]bool
  	rt         rtImports
  	interpTemp *int
  	fset       *token.FileSet
  	bag        *diag.Bag
  	ec         interpEmitCtx // element/fragment emission; zero → elements rejected
  	hasCtx     bool
  	canHoist   bool
  	errReturn  string // "return _gsxerr" or "return nil, _gsxerr"
  }
  // lowerGoParts returns parts as one Go expression; hoists go to hoistBuf.
  func lowerGoParts(hoistBuf *bytes.Buffer, parts []ast.GoPart, lc lowerCtx) (string, bool)
  ```
- `emitGoExprEmbeddedInterp` gains an `errReturn string` parameter (replacing the hard-coded `"return _gsxerr"`); all existing callers pass `"return _gsxerr"`.

- [ ] **Step 1:** Extract `writeProbeGoParts`/`writeEmbeddedProbe` from `emitProbes`' `Interp.Embedded` branch; the branch becomes a call. Extract `lowerGoParts` from `genInterp`'s loop; `genInterp`, `assembleHoleSeed` (with `ec` zero → element error message unchanged) and the GoBlock splice call it.
- [ ] **Step 2:** `go test ./internal/corpus -count=1 && go test ./internal/codegen -run 'Embedded|Literal|GoExpr|Interp' -count=1` → PASS with **no golden changes** (`git status` shows no testdata diffs).
- [ ] **Step 3: Commit** `refactor(codegen): shared probe/emit helpers for embedded Go parts`.

### Task 3: Attribute values, spreads, ordered pairs, marker names

**Files:**
- Modify: `internal/codegen/analyze.go` (probe sites: `walkAttrExprs` ExprAttr/SpreadAttr, component-tag attr probes incl. `walkBranchAttrExprs`, ordered pairs, Marker/MarkerRegion name — all via `writeSkeletonProbeExpr`; give it an `embedded []gsxast.GoPart` + `probeScope` path that calls `writeEmbeddedProbe` when non-nil; the spread `_gsxuseq(probeExpr(...))` site too)
- Modify: `internal/codegen/emit.go` (`emitExprAttr`, `composeBag` ExprAttr/SpreadAttr, `spreadAttrExpr`, `emitPIName`), `internal/codegen/component_positional_emit.go` (`positionalValueExpr`, pipeline path, `positionalAttrsValueExpr`, `positionalOrderedAttrsExpr`)
- Modify: `internal/codegen/nested_literal_gate.go` (stop gating these fields)
- Test: `internal/corpus/testdata/cases/nested-literal/attrs.txtar` (new dir `nested-literal/`), delete `nested-literal-gate/attrs.txtar`

**Interfaces:**
- Consumes: Task 1 overlays; Task 2 `writeEmbeddedProbe`, `lowerGoParts`, `lowerCtx`.
- Each emit site: `expr := strings.TrimSpace(a.Expr)` becomes
  `expr, ok := lowerGoParts(hoist, a.Embedded, lc)` when `a.Embedded != nil`, where `hoist` is the site's statement buffer (inside cond-attr branch / `AttrsCond` thunk when there is one) and `lc.errReturn` matches the enclosing function.

- [ ] **Step 1: Corpus case** `nested-literal/attrs.txtar`:
  ```
  -- input.gsx --
  package demo

  import (
  	"context"
  	"errors"

  	"github.com/gsxhq/gsx"
  )

  type U struct{ Name string }

  func wrap(s string) string      { return s }
  func wrapN(n gsx.Node) gsx.Node { return n }
  func bag(v string) gsx.Attrs    { return gsx.Attrs{{Key: "data-v", Value: v}} }
  func ctxTag(ctx context.Context) string { return "ctx" }
  func lookup(id int) (string, error) {
  	if id < 0 {
  		return "", errors.New("bad id")
  	}
  	return "L", nil
  }

  component Slot(h gsx.Node, t string, attrs gsx.Attrs) {
  	<div { attrs... }>{ h }{ t }</div>
  }

  component Link(attrs gsx.Attrs) {
  	<a { attrs... }>go</a>
  }

  component Page(id int, u *U) {
  	<i title={wrap(f`/z/@{id}`)} data-go={wrap(js`go(@{id})`)} style={string(css`w:@{id}px`)}></i>
  	<Slot t={wrap(f`t-@{id}`)} data-q={wrap(f`q-@{id}`)}/>
  	<Slot h={wrapN(<b>{ id }</b>)}/>
  	<Slot h={wrapN(<a href={wrap(f`/x/@{id}`)}>x</a>)}/>
  	<i { bag(f`v-@{id}`)... }></i>
  	<Slot attrs={{ "data-k": wrap(f`k-@{id}`) }}/>
  	<i { if u != nil { title={wrap(f`n-@{u.Name}`)} } }></i>
  	<Slot { if u != nil { data-n={wrap(f`n-@{u.Name}`)} } }/>
  	<i data-l={wrap(f`l-@{lookup(id)}`)}></i>
  	<Link href={wrap(f`javascript:@{id}`)}/>
  	<i data-c={wrap(f`c-@{ctxTag(ctx)}`)}></i>
  }
  -- invoke --
  Page(1, nil)
  -- diagnostics.golden --
  -- render.golden --
  -- generated.x.go.golden --
  ```
  (Covers: native/component/bag values, element in slot, recursion, spread, ordered pair, laziness under nil in native and component cond branches, error hole hoist, URL sanitization through a forwarded bag, `ctx` in a nested hole.)
- [ ] **Step 2:** `go test ./internal/corpus -run 'TestCorpus/nested-literal/attrs' -update -count=1` → the diagnostics golden contains `nested-literal` errors (gate still on) — this is the failing state.
- [ ] **Step 3: Implement** probe + emit wiring; remove these fields from the gate.
- [ ] **Step 4:** Re-run with `-update`, then without. Expected render golden: `<i title="/z/1" data-go="go( 1 )" style="w:1px"></i><div>t-1</div>`… (inspect: no diagnostics; `href="about:invalid#gsx"` on the Link; no title on the nil-guarded `<i>`). `go build` of the generated code is exercised by the corpus.
- [ ] **Step 5:** Add `nested-literal/attrs_error.txtar` invoking `Page(-1, nil)`-equivalent to pin that the hoisted `lookup` error propagates (render golden empty, the corpus harness records the render error — follow `tuple/` cases for the error-render format).
- [ ] **Step 6: Commit** `feat(codegen): lower nested literals in attribute values, spreads and attrs literals`.

### Task 4: class/style parts and value-form

**Files:**
- Modify: `internal/codegen/analyze.go` (ComposedPart Expr probe via `writeSkeletonProbeExpr`; `emitCondLiveness`/`emitSwitchLiveness`/`emitValueCFControl` accept an overlay and write it via `writeProbeGoParts` instead of `writeSkeletonAuthoredAt`)
- Modify: `internal/codegen/emit.go` (`lowerComposedPartSeed`, `cssLiteralStylePartExpr`, `composedParts` Cond splices ×2, `classEntryExpr` Cond ×2 + arms, `emitValueIf` (emit an `else if` as `} else {` + hoist + `if …` so its hoist runs only when reached; keep the output identical when there is no hoist), `emitValueSwitch` tag (`canHoist` true) and case lists (`canHoist` false))
- Modify: `internal/codegen/nested_literal_gate.go`
- Test: `internal/corpus/testdata/cases/nested-literal/class_style.txtar`, delete `nested-literal-gate/class_style.txtar`

- [ ] **Step 1: Corpus case**:
  ```
  -- input.gsx --
  package demo

  type U struct{ Name string }

  func wrap(s string) string { return s }
  func ok(s string) bool     { return s != "" }

  component Page(id int, u *U) {
  	<i class={ "x", wrap(f`c-@{id}`), "on": ok(f`d-@{id}`) }></i>
  	<i class={ "x", if ok(f`e-@{id}`) { "a" } }></i>
  	<i class={ "x", if u != nil { wrap(f`g-@{u.Name}`) } }></i>
  	<i class={ "x", if id > 0 { "p" } else if ok(f`h-@{u.Name}`) { "q" } }></i>
  	<i class={ "x", switch wrap(f`s-@{id}`) { case "s-1": "b" } }></i>
  	<i class={ "x", switch { case id == 1: "n" case ok(f`k-@{u.Name}`): "m" } }></i>
  	<i style={ "color: red", wrap(css`width:@{id}px`) }></i>
  }
  -- invoke --
  Page(1, nil)
  -- diagnostics.golden --
  -- render.golden --
  -- generated.x.go.golden --
  ```
  Laziness pins: the `if u != nil` arm, the `else if` after a true `if`, and the second case (the first matches) all dereference nil `u` and must never evaluate.
- [ ] **Step 2:** `-update` → gate diagnostics (failing state).
- [ ] **Step 3: Implement**; remove these fields from the gate. Error-carrying hole in a value-form case list → positioned `goexpr-literal-error` (add `nested-literal/class_case_error.txtar` with `case ok(f`@{lookup(id)}`):` and a diagnostics golden).
- [ ] **Step 4:** `-update`, then without; render has no diagnostics and no panic.
- [ ] **Step 5: Commit** `feat(codegen): lower nested literals in class/style parts and value-form`.

### Task 5: Conditional attributes and control flow headers

**Files:**
- Modify: `internal/codegen/analyze.go` (`emitProbes` If/For/Switch/Case cases, `emitSwitchAttrControl`, CondAttr liveness; `buildCtrlMap`/`ctrlOff` must account for lowered header text — record the offset of each `GoText` part rather than of the whole clause)
- Modify: `internal/codegen/emit.go` (`genNode` If/For/Switch/Case, `emitAttr` CondAttr/SwitchAttr, `emitCondGuarded`, `planPostCond`, `condAttrsExpr`), `internal/codegen/component_positional_emit.go` (`positionalConditionalAttrsExpr`, `positionalSwitchAttrsExpr`)
- Hoist rules: first `if` condition and every `else if` (already nested inside `else {}` for IfMarkup/CondAttr) → `canHoist=true` with the hoist written immediately before its `if`; switch tag → true; case lists and `for` clauses → false.
- Modify: `internal/codegen/nested_literal_gate.go`
- Test: `internal/corpus/testdata/cases/nested-literal/control_flow.txtar`, `nested-literal/cond_attrs.txtar`, `nested-literal/for_error.txtar`; delete `nested-literal-gate/control_flow.txtar`

- [ ] **Step 1: Corpus cases.** `control_flow.txtar`:
  ```
  -- input.gsx --
  package demo

  type U struct{ Name string }

  func wrap(s string) string { return s }
  func ok(s string) bool     { return s != "" }

  component Page(id int, u *U) {
  	{ if s := wrap(f`q-@{id}`); s != "" {
  		<b>{ s }</b>
  	} else if ok(f`r-@{u.Name}`) {
  		<i>never</i>
  	} }
  	{ for _, s := range []string{wrap(f`r-@{id}`), wrap(css`c:@{id}`)} {
  		<b>{ s }</b>
  	} }
  	{ switch wrap(js`s(@{id})`) {
  	case "s( 1 )":
  		<b>js</b>
  	} }
  	{ switch {
  	case id == 1:
  		<b>first</b>
  	case ok(f`t-@{u.Name}`):
  		<b>never</b>
  	} }
  }
  -- invoke --
  Page(1, nil)
  -- diagnostics.golden --
  -- render.golden --
  -- generated.x.go.golden --
  ```
  `cond_attrs.txtar`: native and component `{ if ok(f`@{id}`) { title="y" } else if ok(f`@{u.Name}`) { title="z" } }`, `{ switch wrap(f`s-@{id}`) { case "s-1": data-s="y" } }`, invoked with `u=nil`. `for_error.txtar`: `{ for i := 0; ok(f`@{lookup(i)}`); i++ { … } }` → diagnostics golden with one positioned `goexpr-literal-error`.
- [ ] **Step 2:** `-update` → gate diagnostics (failing).
- [ ] **Step 3: Implement**; remove the fields from the gate.
- [ ] **Step 4:** `-update`, then without. `control_flow` renders `<b>q-1</b><b>r-1</b><b>c:1</b><b>js</b><b>first</b>` with no panic.
- [ ] **Step 5: Commit** `feat(codegen): lower nested literals in conditional-attribute and control-flow headers`.

### Task 6: Element literals in `{{ }}` and gate removal

**Files:**
- Modify: `internal/codegen/analyze.go` (`emitProbes` GoBlock case: element parts probe via `writeProbeGoParts`), `internal/codegen/emit.go` (GoBlock splice: element parts via `lowerGoParts` with `ec` set), `internal/codegen/component_target.go:~1368` (drop the "not supported yet" diagnostic and `UnsupportedMarkup` registration), `internal/codegen/analyze.go` (`firstDirectGoBlockMarkup` no longer marks elements unsupported)
- Delete: `internal/codegen/nested_literal_gate.go` and its calls; delete `nested-literal-gate/` corpus dir (move `supported_forms.txtar` to `nested-literal/`)
- Test: `internal/corpus/testdata/cases/nested-literal/goblock_element.txtar`; update any existing corpus case that pinned the GoBlock "not supported yet" diagnostic (find with `grep -rl 'not supported yet' internal/corpus/testdata/cases`)

- [ ] **Step 1: Corpus case**:
  ```
  -- input.gsx --
  package demo

  import "github.com/gsxhq/gsx"

  func wrapN(n gsx.Node) gsx.Node { return n }

  component Page(id int) {
  	{{ n := wrapN(<b>{ id }</b>) }}
  	<p>{ n }</p>
  	{{ m := <><i>a</i><i>{ id }</i></> }}
  	<p>{ m }</p>
  }
  -- invoke --
  Page(7)
  -- diagnostics.golden --
  -- render.golden --
  -- generated.x.go.golden --
  ```
- [ ] **Step 2:** `-update` → "not supported yet" diagnostic (failing).
- [ ] **Step 3: Implement**; delete the gate.
- [ ] **Step 4:** `-update`, then without → renders `<p><b>7</b></p><p><i>a</i><i>7</i></p>`. `go test ./internal/corpus ./internal/printer ./parser -count=1` PASS.
- [ ] **Step 5: Commit** `feat(codegen): element literals in {{ }} blocks; remove the nested-literal gate`.

### Task 7: LSP navigation inside lowered fields

**Files:**
- Modify: `internal/lsp/definition.go` (`nodeNavSpans`), `internal/codegen/component_lsp_facts.go` (`componentExpressionSource`), `internal/codegen/analyze.go` (`buildCtrlMap` if not already done in Task 5)
- Test: extend the existing LSP definition test table (find it with `grep -n 'func TestDefinition' internal/lsp/*_test.go`) — add rows, do not create a new module-opening test

- [ ] **Step 1: Failing rows** (one fixture file, several cursor rows): go-to-definition on `id` inside `@{id}` in `title={wrap(f`/z/@{id}`)}`; on `wrap` in the same attribute; on `ok` after a nested literal in `{ if ok(f`@{id}`) && ok(x) {` (cursor on the second `ok`); on `wrapN` in `h={wrapN(<b/>)}`; on an identifier inside a nested element's attribute. Each expects the declaration position.
- [ ] **Step 2:** Run the LSP test → the rows after a nested construct FAIL (offset arithmetic by `len(text)`).
- [ ] **Step 3: Implement:** when a field has an overlay, nav spans come from its `GoText` parts (each part's own `Pos()` and `len(Src)`) and nested nodes are reached via `inspectWithEmbedded`; skeleton mapping uses the per-part mapped segments written by `writeSkeletonAuthoredAt`.
- [ ] **Step 4:** Add a corpus case `nested-literal/type_error.txtar`: `title={wrap(f`@{undefinedVar}`)}` → diagnostics golden pins the error at the hole's line:column (Review Focus 5).
- [ ] **Step 5:** `go test ./internal/lsp -count=1` and the corpus case PASS.
- [ ] **Step 6: Commit** `feat(lsp): navigation inside nested literals in every Go-expression position`.

### Task 8: Docs, ROADMAP, full gate

**Files:**
- Modify: `docs/guide/syntax/interpolation.md` (replace the #213 caveat with one sentence: literals and element literals work inside any Go expression; error-carrying holes are not allowed in case lists and `for` clauses), `docs/ROADMAP.md` (close the deferral with date and corpus dir; remove the GoBlock-element deferral), `docs/guide/syntax/elements.md` (if it states element literals are unsupported in `{{ }}` — check with grep and fix)

- [ ] **Step 1:** Update docs (keep them concise; wrap any literal `{{ }}` prose in `::: v-pre`).
- [ ] **Step 2:** `make ci` (exit status must be 0 — check it, do not pipe through `tail` without `PIPESTATUS`), then `make lint`.
- [ ] **Step 3: Commit** `docs: nested literals work in every Go-expression position`.
