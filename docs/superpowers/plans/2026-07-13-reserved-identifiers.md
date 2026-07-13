# Reserved Identifiers (ctx/children/attrs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make "reference `attrs`" precise (free-use, not token), fix all three false rejections (struct keys, range-var shadows, func-param shadows), and turn body-scope declarations of `ctx`/`children`/`attrs` into worded `reserved-identifier` diagnostics.

**Architecture:** A new syntactic free-use walker (`internal/codegen/freeuse.go`) parses Go fragments with a hand-rolled scope stack and threads a bound-names environment through the markup tree; `usesAttrs` becomes two-stage (existing token scan as pre-filter → free-use confirmation on hit). A new best-effort reservation pass (`internal/codegen/reserved_bindings.go`, modeled on `reserved_scan.go`) reports body-scope bindings of the three names; the Go compiler stays the backstop for anything it misses.

**Tech Stack:** Go 1.26.1, `go/parser` + `go/ast` (syntactic only — NO `go/types`, NO deprecated `ast.Object` resolution), txtar corpus.

**Spec:** `docs/superpowers/specs/2026-07-13-reserved-identifiers-design.md` — read it first; its "Design principle" (soundness over completeness; false rejections are the bug class) governs every judgment call.

## Global Constraints

- Go pinned to **1.26.1**. Runtime (root `gsx` package) untouched.
- **Never hand-edit** `*.x.go`/`*.golden` (except authoring a new case's render.golden BEFORE `-update`, which must then survive byte-identical). Regenerate: `go test ./internal/corpus -run TestCorpus -update`, verify without `-update`; `coverage.golden` bump rides `-update`.
- **Zero churn to existing corpus goldens** in every task: the two-stage trigger must answer identically to the token scan for every existing case. Any existing-golden diff = STOP, report BLOCKED.
- Emit ≡ probe: `usesAttrs` stays ONE shared predicate (consumers: emit.go:~650, analyze.go:113/1214/3984-region, variantcollide.go:51 — grep, offsets drift). Never fork emit-vs-probe logic.
- The free-use walker and reservation pass are **syntactic**: no `packages.Load`, no `go/types`, no `ast.Object` (deprecated). Unparseable fragment → token fallback (trigger) / silence (reservation) — the component is broken anyway; never let a parse failure reject or mask.
- `gsx` on PATH is Ghostscript → `go run ./cmd/gsx`. Inner loop `make check`; pre-merge `make ci` + `make lint`.
- Work in a git worktree branch `reserved-identifiers` (superpowers:using-git-worktrees), based on current `main`.
- Perf gate: `gsx generate` wall time on a multi-file package within noise of pre-feature (5 alternating runs — the lowertags precedent); stage 2 may only run on token hits.

---

### Task 1: The free-use walker (`freeuse.go`) + exhaustive unit tests

**Files:**
- Create: `internal/codegen/freeuse.go`
- Create: `internal/codegen/freeuse_test.go`

**Interfaces:**
- Produces: `func freeUseAttrs(body []ast.Markup) bool` — reports whether any Go fragment in the component body uses the identifier `attrs` **free** (not bound by a fragment-local scope or a markup-inherited binding). Called by Task 2 from `usesAttrs`. Also produces the internal helpers Task 3 reuses: `func fragmentBindings(src string, kind fragKind) []boundIdent` (top-level binding idents of a parsed fragment, with byte offsets) where `type fragKind uint8; const (fragStmts fragKind = iota; fragClause; fragExpr)` and `type boundIdent struct { name string; off int }`.
- Consumes: `ast` markup/attr node types (same set `usesAttrs` walks today), `go/parser`, `go/ast`.

- [ ] **Step 1: Write the failing unit tests**

`freeuse_test.go`, table-driven over synthetic bodies built with the gsx parser (follow an existing `internal/codegen` test that parses a `.gsx` source string — grep `parser.ParseFile` in `*_test.go` for the harness pattern; wrap each case's body in `component T() { ... }`). Cases (name / body / want):

```go
var freeUseCases = []struct {
	name string
	body string
	want bool
}{
	// free uses — trigger
	{"spread", `<div { attrs... }>x</div>`, true},
	{"method_in_goblock", `{{ d := attrs.Has("x") }}<div data-d={ d }>y</div>`, true},
	{"closure_over_bag", "{{ f := func() string { return attrs.Class() } }}<div class={ f() }>y</div>", true},
	{"reassign_is_use", `{{ attrs = attrs.Without("id") }}<div { attrs... }>x</div>`, true},
	{"nested_decl_rhs_free", `{ if true { <div data-x={ func() string { attrs := attrs.Class(); return attrs }() }>x</div> } }`, true}, // := RHS evaluates before the new name binds
	{"mixed_shadow_and_free", `<div { attrs... }>{ for _, attrs := range bags() { <span { attrs... }>i</span> } }</div>`, true},
	// bound / not-occurrences — no trigger
	{"range_shadow_only", `{ for _, attrs := range bags() { <span { attrs... }>i</span> } }`, false},
	{"funclit_param_shadow", `{{ f := func(attrs []string) int { return len(attrs) } }}<div data-n={ f(nil) }>x</div>`, false},
	{"struct_key", `{{ o := opt{attrs: 1} }}<div data-n={ o.attrs }>x</div>`, false},
	{"selector_only", `<div data-n={ o.attrs }>x</div>`, false},
	{"string_and_comment", `{{ s := "attrs" /* attrs */ }}<div data-s={ s }>x</div>`, false},
	{"longer_ident", `<div data-n={ attrsList }>x</div>`, false},
	{"label_stmt", `{{ attrs: for { break attrs } }}<div>x</div>`, false},
	{"if_init_shadow", `{ if attrs := bags(); len(attrs) > 0 { <span data-n={ len(attrs) }>i</span> } }`, false},
	{"goblock_toplevel_bind_then_use", `{{ attrs := 1 }}<div data-n={ attrs }>x</div>`, false}, // body-scope bind (Task 3 diagnoses it); walker treats later uses as bound — single backstop error, no double-report
	// fallback
	{"unparseable_fragment_falls_back_to_token", `{{ attrs ++!garbage }}<div>x</div>`, true},
}
```

Assert `freeUseAttrs(comp.Body) == want` per case. Also table-test `fragmentBindings` directly: `("attrs := 1", fragStmts) → [{attrs,0}]`, `("attrs, ok := f()", fragStmts) → [{attrs,0}]`, `("const attrs = \"x\"", fragStmts) → [{attrs,6}]`, `("_, attrs := range bags()"... via fragClause "for _, attrs := range bags()") → [{attrs,7}]`, `("x := 1", fragStmts) → []`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run 'TestFreeUse|TestFragmentBindings' -v`
Expected: FAIL — `undefined: freeUseAttrs` (compile error).

- [ ] **Step 3: Implement the walker**

`freeuse.go` structure (implement fully; the scope rules below are the contract):

```go
// fragKind selects the parse wrapper for a Go fragment.
type fragKind uint8

const (
	fragStmts  fragKind = iota // GoBlock body: wrap `package p; func _f() { ... }`
	fragClause                 // for/if/switch header: wrap `package p; func _f() { for|if|switch ... { } }`
	fragExpr                   // interp/attr/spread/arg expression: parser.ParseExpr
)
```

- `parseFragment(src string, kind fragKind) (goast.Node, bool)` — returns the parsed body/clause/expr node; false when unparseable. For `fragClause`, try `for `, then `if `, then `switch ` wrappers (the caller knows which markup node it came from — pass the keyword instead of guessing: signature `parseClause(keyword, src string)`).
- `freeIn(node goast.Node, name string, bound map[string]bool) bool` — the manual scope walk. Rules (each is a unit-test row above):
  - Maintain a scope stack (slice of `map[string]bool`), seeded with `bound`.
  - `*goast.Ident` named `name`: free iff no scope on the stack binds it — EXCEPT skip idents that are (a) the `Sel` of a `*goast.SelectorExpr`, (b) the `Key` ident of a `*goast.KeyValueExpr` whose parent is a `*goast.CompositeLit` (struct keys; safe for `attrs` — a slice is not comparable, so no valid map-key/case use exists; documented in the spec), (c) a `*goast.LabeledStmt` label or `break`/`continue`/`goto` label target.
  - `*goast.AssignStmt` with `Tok == token.DEFINE`: walk RHS FIRST (RHS evaluates in the outer scope — `attrs := attrs.Class()` has a free RHS use), then add LHS idents to the CURRENT scope.
  - `*goast.GenDecl` (var/const in a block): walk each spec's values first, then add names.
  - `*goast.FuncLit`: push a scope with all param/result names; walk body; pop.
  - `*goast.BlockStmt`, `*goast.IfStmt`, `*goast.ForStmt`, `*goast.RangeStmt`, `*goast.SwitchStmt`, `*goast.TypeSwitchStmt`, `*goast.CommClause`/`*goast.CaseClause`: push/pop scopes per Go's rules; `RangeStmt.Key/Value` (when `Tok == DEFINE`) and init-stmt bindings go in the pushed scope; range `X` / init RHS walk before the bindings apply.
  - go/ast Inspect cannot express walk-order + scope-pop cleanly — write an explicit recursive `walk(node)`; it is ~120 lines and this contract is the unit-test table.
- `freeUseAttrs(body []ast.Markup) bool` — mirrors `usesAttrs`'s markup recursion EXACTLY (same node cases; keep the two functions adjacent in review), threading `env map[string]bool`:
  - `*ast.GoBlock`: parse `fragStmts`; unparseable → token fallback for THIS fragment (`valueIdents(code)["attrs"]`); else `freeIn(block, "attrs", env)`; then add the block's TOP-LEVEL bindings (`fragmentBindings`) to `env` for subsequent siblings.
  - `*ast.ForMarkup` / `*ast.IfMarkup` / `*ast.SwitchMarkup`: check the clause's own exprs free against `env` (parse with the right keyword; fallback = token); compute clause bindings; recurse into body/else/cases with `env ∪ bindings`.
  - `*ast.Interp`/`ExprAttr`/`SpreadAttr`/class parts/value-form arms/ordered pairs/pipe stage args/embedded segments: `fragExpr` parse → `freeIn`; fallback = token. (Reuse ONE helper for "expr fragment free?" so all attr positions stay uniform; `attrsRefAttrs`'s existing case list is the position inventory — mirror it.)
  - Env is COPIED when descending into a scoped subtree (for/if bodies), APPENDED-in-place across GoBlock siblings — matching Go scope semantics at the markup level.
- `fragmentBindings(src string, kind fragKind) []boundIdent` — top-level `:=` LHS idents, `var`/`const` spec names, clause range/init bindings, with byte offsets into `src` (from `token.FileSet` positions minus the wrapper prefix length). Task 3's diagnostic positions come from these offsets.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/codegen -run 'TestFreeUse|TestFragmentBindings' -v`
Expected: PASS, every row.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/freeuse.go internal/codegen/freeuse_test.go
git commit -m "feat: syntactic free-use walker for reserved identifiers"
```

---

### Task 2: Two-stage `usesAttrs` + false-rejection corpus positives

**Files:**
- Modify: `internal/codegen/emit.go` (`usesAttrs`, emit.go:4198 — one-line change plus comment)
- Create: `internal/corpus/testdata/cases/reserved/{struct_key_not_trigger,range_shadow_ok,funclit_param_shadow_ok,shadow_and_free_mixed,closure_over_attrs,goblock_consumes_attrs}.txtar`

**Interfaces:**
- Consumes: Task 1's `freeUseAttrs`.
- Produces: `usesAttrs(body) == tokenScan(body) && freeUseAttrs(body)` — same signature, all five consumers unchanged.

- [ ] **Step 1: Write the failing corpus cases**

Six cases per the spec's Test cases §1-6 (input shapes are in the spec verbatim; conventions per `nestedforward/spread_basic.txtar`). Hand-write every render.golden. The three shadow/key cases must ALSO pin the **absence** of synthesis: after `-update`, grep each generated golden for `Attrs` — `struct_key_not_trigger`/`range_shadow_ok`/`funclit_param_shadow_ok` must have NO `Attrs` field in their props structs (state this in each case's comment header).

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | grep -A4 'reserved/'`
Expected: the three shadow/key cases FAIL with today's false rejections (`declared and not used: attrs`); the three free-use positives PASS already (they pin current behavior against regression).

- [ ] **Step 2: Wire the two-stage trigger**

In `usesAttrs` (emit.go:4198): rename the existing walk to `attrsTokenScan` (unexported, unchanged logic), and make `usesAttrs` return `attrsTokenScan(body) && freeUseAttrs(body)`. Update the doc comment: token scan = pre-filter (no-token → no cost), free-use walk = the semantic answer; unparseable fragments fall back per-fragment to the token answer.

- [ ] **Step 3: Regenerate, verify, zero-churn check**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && git status --porcelain internal/corpus`
Expected: PASS; new files + coverage.golden ONLY. Hand-written renders byte-identical. Then `go test ./internal/codegen ./internal/gsxfmt ./gen` — PASS.

- [ ] **Step 4: Perf check**

Build pre/post binaries (`git stash` dance or build from HEAD~1) and run 5 alternating `gsx generate` over `internal/corpus/testdata` or a multi-file example package; wall times within noise. Record numbers in the commit message body.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen internal/corpus
git commit -m "feat: attrs trigger fires on free use only (fixes shadow/key false rejections)"
```

---

### Task 3: Reservation pass + receiver extension + rejection corpus

**Files:**
- Create: `internal/codegen/reserved_bindings.go`
- Modify: `internal/codegen/analyze.go` (`checkReservedRecvVar`, analyze.go:3744 — add `children`/`attrs` arms with the meanings from `checkReservedParams` at analyze.go:3894)
- Modify: `internal/codegen/module_importer.go` (wire the pass where `checkReservedDecls` runs, module_importer.go:~857)
- Create: `internal/corpus/testdata/cases/reserved/{attrs_shortvar_rejected,attrs_tuple_rejected,attrs_const_rejected,attrs_receiver_rejected,children_shortvar_rejected,children_shortvar_unplaced_rejected,ctx_shortvar_rejected,children_value_unplaced}.txtar`
- Test: `internal/codegen/reserved_bindings_test.go`

**Interfaces:**
- Consumes: Task 1's `fragmentBindings` + `parseFragment`.
- Produces: `func checkReservedBodyBindings(c *ast.Component) []reservedDecl` (reuse the `reservedDecl{name, pos}` shape from reserved_scan.go:22); wired into the same bag.Errorf loop as `checkReservedDecls` but with code `reserved-identifier` and message `identifier %q is reserved (%s) — rename the variable` where the meaning strings match checkReservedParams: `the ambient context` / `the implicit children slot` / `the implicit fallthrough bag`.

- [ ] **Step 1: Failing corpus cases** — the 8 rejection cases per spec §10-17 (empty diagnostics.golden; `-update` captures; `children_value_unplaced` pins the RAW `undefined: children`, not a worded one). Run TestCorpus: today they capture raw collision errors (or, for the tuple case, SILENTLY PASS with an Attrs prop — write its comment header to say the case exists to kill that).

- [ ] **Step 2: Implement the pass**

`checkReservedBodyBindings`: walk the component body's GoBlocks and CF clauses ONLY at body scope — i.e. `fragmentBindings(goBlock.Code, fragStmts)` for every GoBlock at ANY markup depth (all GoBlocks share the render closure's scope — a GoBlock inside `{ if }` markup still emits into… **verify this**: read how `IfMarkup` bodies emit; if markup-if bodies emit inside a Go `if { }` block, a GoBlock there is NESTED scope and must NOT be flagged — adjust to only flag GoBlocks whose emitted scope is the closure top level, and document which markup contexts that is, with the emitter code as the oracle, not assumption). Filter to the three names; report each with position = fragment base pos + `boundIdent.off` (the position-mapping precedent is reserved_scan.go's scanner-offset mapping). Do NOT flag clause bindings (nested by construction) or func-literal params (nested). Best-effort per the spec: shapes the walker can't see fall to Go's errors.

Extend `checkReservedRecvVar` (both its callers are already wired — analyze.go:1146, emit.go:578).

- [ ] **Step 3: Wire, capture, verify** — add the pass beside checkReservedDecls (module_importer.go:857 region, per-component); `-update`; read every captured diagnostics.golden: code `reserved-identifier`, position on the binding ident, message text exact. Unit-test the detector table per spec. Zero churn elsewhere. `make check`.

- [ ] **Step 4: Commit**

```bash
git add internal/codegen internal/corpus
git commit -m "feat: reserved-identifier diagnostics for body-scope ctx/children/attrs bindings"
```

---

### Task 4: Remaining positives + docs + ROADMAP

**Files:**
- Create: `internal/corpus/testdata/cases/reserved/{attrs_reassign,children_value_after_placement,plain_func_attrs_param_ok}.txtar`
- Modify: `docs/guide/syntax/props.md` (Reserved variables section — or `basic-syntax.md` if props.md flow fights it; ONE place), `docs/guide/syntax/composition.md` (one-line cross-ref), `docs/ROADMAP.md`

- [ ] **Step 1:** The three positive cases (spec §7-9; `attrs_reassign`'s render proves the `Without`-dropped key is gone; `plain_func_attrs_param_ok` is the one-learning `ds/icon/named.gsx` shape — func-literal `attrs` param OUTSIDE a component). Hand-written renders; `-update`; byte-identical.
- [ ] **Step 2:** Docs — Reserved variables section: one table (name / what it is / what brings it into scope), the declaration rule (+ "nested scopes shadow like ordinary Go"), ONE diagnostic example. ≤20 lines, v-pre-guard any literal `{{ }}` in prose. ROADMAP: one dated entry under Tracked debts (define-reference-attrs → resolved, spec pointer).
- [ ] **Step 3:** `make check`; commit `docs: reserved variables (ctx/children/attrs) + remaining corpus pins`.

---

### Task 5: CI + adversarial review

- [ ] **Step 1:** `make ci && make lint` — green, uncached.
- [ ] **Step 2:** Independent adversarial reviewer, live probes. The probe modules from the design session still exist under the scratchpad (`attrs-def/v1..v11`, each a one-dir repro of a matrix row) — rerun ALL against the branch and check each row's NEW expected outcome (v2/v3/v7 now generate + render with no Attrs prop; v1/v4/v8 → `reserved-identifier`; v5 receiver → worded; v9/v10 → worded; v11 unchanged raw). Additional probe directions: scope-order corners (`attrs := attrs.Class()` in a nested block — RHS free), a GoBlock inside markup-if (the Task 3 Step 2 verification point), for-init (`{ for attrs := 0; attrs < 2; attrs++ }` — clause binding, must shadow not flag), mixed env across sibling GoBlocks, and a differential sweep: for every existing corpus case, assert `usesAttrs` == old token answer (build both predicates in a throwaway test).
- [ ] **Step 3:** Fix findings, re-verify, then superpowers:finishing-a-development-branch.
