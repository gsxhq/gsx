# Nested Fallthrough Forwarding Implementation Plan

> **Superseded implementation plan:** The forwarding behavior remains, but the
> implicit/synthesized implementation described here is removed. See
> `2026-07-14-verbatim-component-signatures.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `<Inner { attrs... }/>` (and every other `attrs` reference in nested component-call positions) forwards the enclosing component's implicit bag into the callee's synthesized bag; component-call bags align to element source-order merge; fallthrough onto a bag-less component gets a worded diagnostic.

**Architecture:** One shared predicate (`usesAttrs`/`attrsRefAttrs` in `internal/codegen/emit.go`) gains comprehensive coverage of component-element attr Go fragments, which binds the `attrs` local in both emit and probe; the existing `childPropsLiteral` bag assembly then forwards with zero new lowering. Bag assembly is restructured from coalesce-bare-first to source-order interleaving. A `component-missing-attrs` diagnostic mirrors the existing `byo-missing-attrs` guard.

**Tech Stack:** Go 1.26.1, `go/types`-probed codegen, txtar corpus harness (`internal/corpus`).

**Spec:** `docs/superpowers/specs/2026-07-13-nested-fallthrough-forwarding-design.md` — read it before starting any task.

## Global Constraints

- Go pinned to **1.26.1** (`GO_VERSION` in `.github/workflows/ci.yml`); a different minor reintroduces gofmt drift.
- Runtime (root `gsx` package) is standard-library only — this feature touches **no runtime code**; if you find yourself editing root-package `.go` files, stop and re-read the spec.
- **Never hand-edit** `*.x.go` or `*.golden` files. Regenerate: `go test ./internal/corpus -run TestCorpus -update`, then verify **without** `-update`. A forgotten `coverage.golden` manifest bump fails the suite — `-update` rewrites it.
- The `gsx` binary on PATH is Ghostscript. Use `go run ./cmd/gsx …`.
- Emit ≡ probe: any expression the emitter writes must appear identically in the analyze skeleton. The shared functions (`usesAttrs`, `childPropsLiteral`) enforce this — do not fork logic between `emit.go` and `analyze.go`.
- Corpus is canonical: every behavior in this plan is pinned by a txtar case, not a bespoke unit test (repo convention).
- Inner dev loop: `make check`. Before any merge: `make ci` (authoritative, uncached) and `make lint`.
- Work in a **git worktree** branch (e.g. `nested-forwarding`), created via the `superpowers:using-git-worktrees` skill. **Before Task 1:** merge open docs-only PR #103 into `main` and branch from the result (this plan's ROADMAP edits build on its entry).

---

### Task 1: Uniform `attrs` binding — comprehensive `attrsRefAttrs` + unified `usesAttrs` element branch

**Files:**
- Modify: `internal/codegen/emit.go:4182-4303` (`usesAttrs`, `attrsRefAttrs`)
- Create: `internal/corpus/testdata/cases/nestedforward/spread_basic.txtar`

**Interfaces:**
- Consumes: `ast` attr node types (`SpreadAttr{Expr,Stages}`, `ClassAttr{Parts []ClassPart}`, `ClassPart{Expr,Cond,Stages,CSSSegments,CF}`, `ValueCF{If *ValueIf, Switch *ValueSwitch}`, `ValueIf{Cond,Then,ElseIf,Else}`, `ValueSwitch{Tag,Cases}`, `ValueSwitchCase{List,Value}`, `ValueArm{Expr,Stages}`, `ExprAttr{Expr,Stages}`, `CondAttr{Cond,Then,Else}`, `EmbeddedAttr{Segments,Stages}`, `MarkupAttr{Value}`, `OrderedAttrsAttr{Pairs []OrderedPair}` — all in `ast/ast.go`), `valueIdents(src string) map[string]bool` (existing token-based ident scan).
- Produces: `attrsRefAttrs(attrs []ast.Attr) bool` covering **every** Go fragment an attr list can carry; `usesAttrs` treating component and plain elements identically. All five existing consumers (`emit.go:633`, `analyze.go:113`, `analyze.go:1214`, `analyze.go:3984`, `variantcollide.go:51`) pick this up with no signature change.

- [ ] **Step 1: Write the failing corpus case**

Create `internal/corpus/testdata/cases/nestedforward/spread_basic.txtar`:

```txtar
# A wrapper component forwards its implicit attrs bag into a nested
# component call: <Icon ... { attrs... }/>. The bag concatenates at the
# spread's source position; all resolution (class merge, URL sanitization)
# happens at Icon's own leaf element.
-- input.gsx --
package views

component Icon(name string) {
	<span class="icon" data-name={name} { attrs... }>i</span>
}

component SearchIcon() {
	<Icon name="search" class="w-5 h-5" { attrs... }/>
}

component Page() {
	<SearchIcon class="text-red" aria-label="Search"/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<span class="icon w-5 h-5 text-red" data-name="search" aria-label="Search">i</span>
```

- [ ] **Step 2: Run the corpus to verify it fails**

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | grep -A5 spread_basic`
Expected: FAIL — a diagnostic containing `undefined: attrs` for `SearchIcon` (the case has an empty `diagnostics.golden`, so the unexpected diagnostic fails the case).

- [ ] **Step 3: Rewrite `attrsRefAttrs` comprehensively**

Replace the body of `attrsRefAttrs` (emit.go:4267) so every Go fragment is scanned. Keep the function comment style; update it to state it now covers pipeline stage args, value-form CF, CSS-literal segments, markup-attr values, and ordered-attrs pair values:

```go
func attrsRefAttrs(attrs []ast.Attr) bool {
	refsAttrs := func(src string) bool { return valueIdents(src)["attrs"] }
	refsStages := func(stages []ast.PipeStage) bool {
		for _, st := range stages {
			if st.Args != "" && refsAttrs(st.Args) {
				return true
			}
		}
		return false
	}
	refsValueArm := func(arm *ast.ValueArm) bool {
		return arm != nil && (refsAttrs(arm.Expr) || refsStages(arm.Stages))
	}
	var refsValueIf func(vi *ast.ValueIf) bool
	refsValueIf = func(vi *ast.ValueIf) bool {
		if vi == nil {
			return false
		}
		return refsAttrs(vi.Cond) || refsValueArm(vi.Then) || refsValueIf(vi.ElseIf) || refsValueArm(vi.Else)
	}
	refsValueSwitch := func(vs *ast.ValueSwitch) bool {
		if vs == nil {
			return false
		}
		if vs.Tag != "" && refsAttrs(vs.Tag) {
			return true
		}
		for _, c := range vs.Cases {
			if (c.List != "" && refsAttrs(c.List)) || refsValueArm(c.Value) {
				return true
			}
		}
		return false
	}
	for _, a := range attrs {
		switch at := a.(type) {
		case *ast.SpreadAttr:
			if refsAttrs(at.Expr) || refsStages(at.Stages) {
				return true
			}
		case *ast.ClassAttr:
			for i := range at.Parts {
				p := &at.Parts[i]
				if refsAttrs(p.Expr) || (p.Cond != "" && refsAttrs(p.Cond)) || refsStages(p.Stages) {
					return true
				}
				if p.CSSSegments != nil && usesAttrs(p.CSSSegments) {
					return true
				}
				if p.CF != nil && (refsValueIf(p.CF.If) || refsValueSwitch(p.CF.Switch)) {
					return true
				}
			}
		case *ast.ExprAttr:
			if refsAttrs(at.Expr) || refsStages(at.Stages) {
				return true
			}
		case *ast.CondAttr:
			if refsAttrs(at.Cond) || attrsRefAttrs(at.Then) || attrsRefAttrs(at.Else) {
				return true
			}
		case *ast.EmbeddedAttr:
			if usesAttrs(at.Segments) || refsStages(at.Stages) {
				return true
			}
		case *ast.MarkupAttr:
			if usesAttrs(at.Value) {
				return true
			}
		case *ast.OrderedAttrsAttr:
			for i := range at.Pairs {
				if refsAttrs(at.Pairs[i].Value) {
					return true
				}
			}
		}
	}
	return false
}
```

- [ ] **Step 4: Unify `usesAttrs`'s element branch**

In `usesAttrs` (emit.go:4208-4230), replace the `*ast.Element` case's component/non-component split with one path — `attrsRefAttrs` now covers markup-attr values itself, so the `walkMarkupAttrs` closure is gone:

```go
		case *ast.Element:
			// Every attr Go fragment — on a plain element AND on a nested
			// component invocation — evaluates in THIS component's scope, so an
			// `attrs` reference anywhere in the attr list binds the implicit
			// bag. Named-slot markup values and children render in this scope
			// too (attrsRefAttrs recurses MarkupAttr values; children below).
			if attrsRefAttrs(t.Attrs) {
				return true
			}
			if usesAttrs(t.Children) {
				return true
			}
```

Update the `usesAttrs` doc comment: delete the sentence "A component's SIMPLE attrs (props) are NOT walked — those are the caller's prop exprs, not this component's body" and state the new invariant: *any* `attrs` reference in a component's body, in any position, synthesizes the fallthrough bag.

- [ ] **Step 5: Regenerate goldens and verify**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Inspect `spread_basic.txtar`'s regenerated `generated.x.go.golden`: `SearchIconProps` must contain `Attrs _gsxrt.Attrs`; the `Icon(...)` call must contain `Attrs: _gsxrt.ConcatAttrs(_gsxrt.Attrs{{Key: "class", Value: _gsxrt.ClassJoin(...)}}, attrs)` (class entry then the forwarded bag). The hand-written `render.golden` from Step 1 must be byte-identical after `-update` — if `-update` changed it, the merge semantics are wrong; stop and investigate.

Also run: `go test ./internal/codegen ./internal/gsxfmt ./gen`
Expected: PASS (no existing golden churn — every newly-detected position was previously a hard error, so no existing case changes).

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat: bind implicit attrs in nested component-call positions"
```

---

### Task 2: Source-order bag assembly in `childPropsLiteral`

**Files:**
- Modify: `internal/codegen/emit.go` (`childPropsLiteral` collection + assembly, `propFieldEntry.bagPairs/bagSegments` consumers, `oaMergePrefix` construction; enumerate rebuild sites first — Step 1)
- Modify: `internal/corpus/testdata/cases/attrsonly/merge_order.txtar` (comment correction; goldens regen)
- Create: `internal/corpus/testdata/cases/nestedforward/bare_after_spread.txtar`

**Interfaces:**
- Consumes: `propFieldEntry{bagPairs []oaPairEntry, bagSegments []string, oaLit, oaMergePrefix}` (emit.go:5087), `componentValueEntry{bagIndex, pairIndex, segmentIndex}` (emit.go:5110), `attrsPairsLiteral(rtPkg, pairs)` (emit.go:5132).
- Produces: `propFieldEntry` gains `bagPairSource []int` and `bagSegmentSource []int` (parallel source-index slices; `bagPairs`/`bagSegments` storage and all existing index semantics — `componentValueEntry.pairIndex/segmentIndex` — stay untouched); new helper `assembleBagParts(rtPkg string, pairs []oaPairEntry, pairSrc []int, segments []string, segSrc []int) []string` returning `ConcatAttrs` argument strings in source order with adjacent bare attrs grouped into one literal run. Task 3 builds on the resulting assembly block.

- [ ] **Step 1: Enumerate every assembly/rebuild site**

Run: `grep -n "attrsPairsLiteral\|bagPairs\|bagSegments\|oaMergePrefix" internal/codegen/*.go | grep -v _test`
Record each hit. Expect: the collection loop + final assembly in `childPropsLiteral` (~emit.go:5610-5950), the post-hoist literal rebuild in `genChildComponent` (~emit.go:4512+), and `materializeComponentValuePlan`-adjacent code. Every site that turns `bagPairs`+`bagSegments` into a `ConcatAttrs(...)` string must switch to the Step 3 helper — a site left on the old "pairs literal first" order is a miscompile.

- [ ] **Step 2: Write the failing corpus case**

Create `internal/corpus/testdata/cases/nestedforward/bare_after_spread.txtar`:

```txtar
# Component-call bags follow element source-order merge (2026-07-13
# alignment): a bare fallthrough attr AFTER a spread wins per key — the
# force position, exactly as on an element. data-v="root" is written after
# the spread carrying data-v="bag", so root wins; data-pre is before the
# spread, so the bag's value wins.
-- input.gsx --
package views

component Panel() {
	<section { attrs... }>p</section>
}

component Page(extra gsx.Attrs) {
	<Panel data-pre="root" { extra... } data-v="root"/>
}
-- invoke --
Page(PageProps{Extra: gsx.Attrs{{Key: "data-pre", Value: "bag"}, {Key: "data-v", Value: "bag"}}})
-- diagnostics.golden --
-- render.golden --
<section data-pre="bag" data-v="root">p</section>
```

(`import "github.com/gsxhq/gsx"` line in input.gsx — match `attrs_literal_merge.txtar`'s header exactly.)

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | grep -B2 -A8 bare_after_spread`
Expected: FAIL — rendered output is `<section data-pre="bag" data-v="bag">p</section>` (today `data-v="root"` coalesces into the leading literal, so the bag's later entry wins).

- [ ] **Step 3: Restructure collection + add the assembly helper**

In `childPropsLiteral`, alongside the existing `var bag []oaPairEntry` / `var segments []string` (emit.go:5611-5612), add parallel source-index recording — every `bag = append(bag, …)` site also does `bagPairSrc = append(bagPairSrc, sourceIndex)`, every `segments = append(segments, …)` site also does `segmentSrc = append(segmentSrc, sourceIndex)` (the loop variable `sourceIndex` already exists at emit.go:5618). Store both slices on the synthesized-Attrs `propFieldEntry` next to `bagPairs`/`bagSegments`.

Add the helper next to `attrsPairsLiteral`:

```go
// assembleBagParts interleaves bare-attr literal runs and spread/cond
// segments by their source position, grouping ADJACENT bare attrs into one
// gsx.Attrs literal. Source-order alignment (2026-07-13): a bare attr after
// a spread lands in a LATER ConcatAttrs argument and therefore wins per key
// at the leaf, matching element multi-spread merge. pairSrc/segSrc are the
// attrs' indices in el.Attrs, strictly increasing within each slice.
func assembleBagParts(rtPkg string, pairs []oaPairEntry, pairSrc []int, segments []string, segSrc []int) []string {
	var parts []string
	var run []oaPairEntry
	flush := func() {
		if len(run) > 0 {
			parts = append(parts, attrsPairsLiteral(rtPkg, run))
			run = nil
		}
	}
	pi, si := 0, 0
	for pi < len(pairs) || si < len(segments) {
		if si >= len(segments) || (pi < len(pairs) && pairSrc[pi] < segSrc[si]) {
			run = append(run, pairs[pi])
			pi++
			continue
		}
		flush()
		parts = append(parts, segments[si])
		si++
	}
	flush()
	return parts
}
```

Replace the final assembly (emit.go:5927-5931: `parts = [attrsPairsLiteral(bag)] ++ segments`) with `parts := assembleBagParts(rtPkg, bag, bagPairSrc, segments, segmentSrc)`; keep the `attrsLitIdx >= 0` literal-last handling and the single-part no-`ConcatAttrs` collapse exactly as they are. Convert every rebuild site found in Step 1 the same way (they read the mutated `bagPairs` entries post-hoist, so ordering is index-independent — only the final string assembly changes). `oaMergePrefix` becomes `strings.Join(assembleBagParts(...), ", ")`.

- [ ] **Step 4: Regenerate and inspect the fallout**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Then: `git diff --stat internal/corpus`
Inspect **every** changed golden: only cases where a bare fallthrough attr follows a spread/cond on a component call may change (expect `attrsonly/merge_order.txtar`; possibly a small number of others). For each, confirm the new render is the source-order semantics, not collateral damage. `bare_after_spread.txtar`'s `render.golden` must be byte-identical to Step 2's hand-written expectation.

- [ ] **Step 5: Correct `merge_order.txtar`'s comment**

Its comment currently claims source order while its golden coalesced. Update the comment to state the now-true rule: all call-site attr forms merge in source order (bare attrs group per adjacent run); the `attrs={{ }}` literal merges last regardless of position. Re-run `go test ./internal/corpus -run TestCorpus` — PASS.

- [ ] **Step 6: Run the wider suite and commit**

Run: `make check`
Expected: PASS (examples drift check included — if `examples/*.txtar` renders changed, regenerate per the failure message).

```bash
git add internal/codegen internal/corpus examples docs
git commit -m "feat: align component-call bag assembly to source-order merge"
```

---

### Task 3: `component-missing-attrs` diagnostic + splat special-case

**Files:**
- Modify: `internal/codegen/emit.go` (`childPropsLiteral` signature + splat branch + bag guard; callers at emit.go:4512, emit.go:4931, `attrsOnlyBagExpr` emit.go:5419), `internal/codegen/analyze.go` (caller at 1542)
- Create: `internal/corpus/testdata/cases/nestedforward/missing_attrs_rejected.txtar`, `internal/corpus/testdata/cases/nestedforward/missing_attrs_spread_rejected.txtar`, `internal/corpus/testdata/cases/nestedforward/xpkg_missing_attrs_rejected.txtar`

**Interfaces:**
- Consumes: `isKnownPropsType(propFields, propsType) bool` + `hasAttrsBag(propFields, propsType, byoStr) bool` (`internal/codegen/byo.go:179/191`), `attrError{pos, end, code, msg}`, Task 2's assembly block.
- Produces: `childPropsLiteral(..., enclosingAttrsBound bool)` — new final param; diagnostic code string `component-missing-attrs`. Callers pass: `genChildComponent`/skipped-tag-sink → whether the enclosing component is manual mode (the same `usesAttrs(c.Body)` result `genComponent` computed — thread it down; grep `genChildComponent(` for its call chain); probe (analyze.go:1542) → its own `manual` computation for the enclosing component; `attrsOnlyBagExpr` → a pass-through param from the same two caller kinds; element-literal emission paths (no enclosing component) → `false`.

- [ ] **Step 1: Write the failing rejection cases**

`missing_attrs_rejected.txtar` — bare fallthrough attr onto a bag-less generated component (PR #103's original case):

```txtar
# Fallthrough onto a generated component whose body never references
# `attrs` is a worded, positioned diagnostic (component-missing-attrs) —
# not the raw go/types `unknown field Attrs in struct literal`.
-- input.gsx --
package views

component Plain(title string) {
	<h1>{title}</h1>
}

component Page() {
	<Plain title="t" data-x="1"/>
}
-- invoke --
Page()
-- diagnostics.golden --
<fill from actual output in Step 3 — must contain code component-missing-attrs,
position at data-x, and the "reference `attrs` in the body" guidance>
-- render.golden --
```

`missing_attrs_spread_rejected.txtar` — sole `{ attrs... }` onto the same callee from inside a forwarding wrapper (the splat special-case; without it this lowers to `Plain(attrs)` and a raw type mismatch):

```txtar
-- input.gsx --
package views

component Plain(title string) {
	<h1>{title}</h1>
}

component Wrapper() {
	<Plain { attrs... }/>
}

component Page() {
	<Wrapper data-x="1"/>
}
-- invoke --
Page()
-- diagnostics.golden --
<fill from actual output in Step 3 — component-missing-attrs at the spread>
-- render.golden --
```

`xpkg_missing_attrs_rejected.txtar` — same as the first case but the callee lives in a sibling package (`-- go.mod --` + `-- ui/plain.gsx --` + `-- pages/home.gsx --` multi-package layout; copy the header shape from `cases/xpkg/imported_attrs_literal_merge.txtar`, and the diagnostics.golden formatting from `cases/xpkg/go_component_attrs_literal_missing.txtar`). This pins the PR #103 "cross-package generate exits 0" hole closed.

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | grep -A6 missing_attrs`
Expected: FAIL — today the first and third produce the raw `unknown field Attrs` / build-time failure shapes, the second a type-mismatch, none matching the placeholder diagnostics.

- [ ] **Step 2: Thread `enclosingAttrsBound` and add both checks**

Add the `enclosingAttrsBound bool` parameter to `childPropsLiteral` and update the four call sites per the Interfaces block. Then:

**(a) Splat branch** (emit.go:5582, inside the `if isByoChild || (isKnownPropsType && !hasAttrsBag)` loop, before the existing all-or-nothing validation):

```go
			if enclosingAttrsBound && strings.TrimSpace(s.Expr) == "attrs" && len(s.Stages) == 0 {
				// The enclosing component's implicit bag spread onto a bag-less
				// callee: never a whole-struct splat — the author is forwarding.
				msg := fmt.Sprintf("cannot forward `{ attrs... }` to <%s>: %s has no fallthrough bag; reference `attrs` in <%s>'s body to accept forwarded attributes", el.Tag, propsType, el.Tag)
				if isByoChild {
					msg = fmt.Sprintf("cannot forward `{ attrs... }` to <%s>: its Props type %s has no `Attrs gsx.Attrs` field", el.Tag, propsType)
				}
				return nil, nil, "", nil, &attrError{pos: s.Pos(), end: s.End(), code: "component-missing-attrs", msg: msg}
			}
```

**(b) Bag guard** (the `if len(bag) > 0 || len(segments) > 0` block, directly after the existing `byo-missing-attrs` check at emit.go:5918-5921). Track the first fallthrough attr while collecting — `var firstFallthrough ast.Attr`, set on the first append to `bag` or `segments`:

```go
		if !isByoChild && isKnownPropsType(propFields, propsType) && !hasAttrsBag(propFields, propsType, byoStr) {
			msg := fmt.Sprintf("attribute on <%s> matches no declared prop of component %s, and its body does not reference `attrs` (no fallthrough bag); reference `attrs` in <%s>'s body to accept forwarded attributes", el.Tag, el.Tag, el.Tag)
			return nil, nil, "", nil, &attrError{pos: firstFallthrough.Pos(), end: firstFallthrough.End(), code: "component-missing-attrs", msg: msg}
		}
```

Note: guard (b) is reachable only via cond-attr segments or `attrsLitIdx`-less bags on a bag-less KNOWN component — bare attrs on such a component reach it directly; a spread reaches (a) first. `attrsOnlyBagExpr`'s synthetic field set always has `Attrs`, so neither check can fire there (its existing "cannot happen" splat guard stays).

- [ ] **Step 3: Capture real diagnostics into the goldens**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Read the three regenerated `diagnostics.golden` files and confirm: code `component-missing-attrs`, position at `data-x`/the spread (not the tag), message text matches Step 2, and the xpkg case fails at **generate time** (non-empty diagnostics, no `go build` involvement). If the xpkg case still exits clean, the cross-package prop map lacks the synthesized-bag bit — stop and investigate `propFields` population for imported components before proceeding.

- [ ] **Step 4: Confirm no existing splat case regressed**

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | tail -3` then `git diff --stat internal/corpus/testdata/cases/props internal/corpus/testdata/cases/xpkg`
Expected: PASS; no changes under `props/` splat cases (`byo_splat*`, `splat_composition`, `interop_splat`) — the special-case is gated on the literal token `attrs` + `enclosingAttrsBound`.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen internal/corpus
git commit -m "feat: worded component-missing-attrs diagnostic (closes the PR #103 debt)"
```

---

### Task 4: Corpus matrix — positions, callee kinds, chain, security, latent element gaps

**Files:**
- Create (all under `internal/corpus/testdata/cases/nestedforward/`): `prop_value.txtar`, `cond_attr.txtar`, `class_prop.txtar`, `ordered_literal_value.txtar`, `derived_bag.txtar`, `with_children.txtar`, `chain.txtar`, `xpkg_dotted.txtar`, `attrsonly_callee.txtar`, `byo_callee.txtar`, `method_callee.txtar`, `url_sanitized_at_leaf.txtar`, `shadows_package_var.txtar`
- Create (under `internal/corpus/testdata/cases/class/`): `value_arm_attrs_ref.txtar`

**Interfaces:**
- Consumes: Tasks 1-3 behavior. No production code changes expected; if any case fails, the fix belongs in the Task 1-3 code, not in special-casing here.

- [ ] **Step 1: Add the uniform-binding position cases**

Each is a minimal wrapper forwarding into the Task 1 `Icon`/`Panel` shape; hand-write `render.golden`, run `-update`, and verify the hand-written render survives byte-identical. Sketches (full txtar structure as in Task 1 Step 1):

- `prop_value.txtar`: `<Icon name={ attrs.Get("n") }/>` inside a wrapper; `Page` passes `n="search"`. Render: `data-name="search"`.
- `cond_attr.txtar`: `<Icon name="x" { if attrs.Has("hot") { data-hot="1" } }/>`; caller passes `hot`. Render includes `data-hot="1"`; a second invoke-less branch case is unnecessary (AttrsCond thunk shape is pinned by the golden).
- `class_prop.txtar`: callee with a declared `class string` prop; wrapper passes `class={ attrs.Class() }`.
- `ordered_literal_value.txtar`: `<Panel attrs={{ "data-n": attrs.Get("n") }}/>`.
- `derived_bag.txtar`: `<Panel { attrs.Without("id")... }/>`; caller passes `id` + `data-keep`. Render: no `id`, `data-keep` present.
- `with_children.txtar`: `<Panel { attrs... }>text child</Panel>` where `Panel` places `{children}`.
- `shadows_package_var.txtar`: package-level `var attrs = gsx.Attrs{{Key: "data-pkg", Value: "1"}}` in a sibling `-- helpers.go --` file + a wrapper referencing `attrs` on a nested tag. Pins reserved-by-usage: the implicit bag wins (render shows the caller's attr, never `data-pkg`).

- [ ] **Step 2: Add the callee-kind + chain + security cases**

- `xpkg_dotted.txtar`: multi-package (`go.mod` + `ui/` + `pages/`); wrapper in `pages` forwards into `<ui.Panel { attrs... }/>`.
- `attrsonly_callee.txtar`: `var Dot = func(attrs gsx.Attrs) gsx.Node {...}`-style attrs-only value (copy a `cases/attrsonly/` input shape); wrapper: `<Dot class="w-5" { attrs... }/>`. This is the spec's driving icon shape.
- `byo_callee.txtar`: hand-written props struct with `Attrs gsx.Attrs` (copy the `boxProps` shape from `attrsonly/merge_order.txtar`); wrapper forwards into it.
- `method_callee.txtar`: `component (p page) Item() { <li { attrs... }>x</li> }` + a method-component wrapper body `<p.Item { attrs... }/>` (receiver-qualified dotted tag).
- `chain.txtar`: A forwards to B forwards to C (plain element leaf); `Page` sets `class` + a scalar; render proves one merged class (`c-root b-add a-add page-cls` orderings per source) and outermost-caller-wins scalar.
- `url_sanitized_at_leaf.txtar`: `Page` passes `href="javascript:alert(1)"` through a forwarding wrapper into a `<a { attrs... }>`-leaf callee. Render: `href="about:invalid#gsx"` (copy the expectation style from `cases/spread-sanitize/`).

- [ ] **Step 3: Add the latent element-side gap case**

`cases/class/value_arm_attrs_ref.txtar`: a **plain element** whose only `attrs` reference is inside a value-form arm — `<div class={ if attrs.Has("on") { "on" } }>x</div>` in a component; caller passes `on`. Today this was `undefined: attrs`; Task 1's scan fixes it. Render: `class="on"`.

- [ ] **Step 4: Regenerate, verify, commit**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && make check`
Expected: PASS; every hand-written `render.golden` byte-identical through `-update`.

```bash
git add internal/corpus
git commit -m "test: nested-forwarding corpus matrix (positions, callees, chain, security)"
```

---

### Task 5: Documentation + ROADMAP + playground example

**Files:**
- Modify: `docs/guide/syntax/composition.md`, `docs/guide/syntax/props.md`, `docs/guide/syntax/attributes.md`, `docs/ROADMAP.md`
- Create: one `examples/*.txtar` (numbering: run `ls examples/ | sort | tail -5` and take the next free number, e.g. `305-attrs-forwarding-wrapper.txtar`)

**Interfaces:**
- Consumes: shipped behavior from Tasks 1-4.
- Produces: user-facing docs. Keep them **concise** — behavior stated plainly, rationale stays in the spec (standing feedback, given twice).

- [ ] **Step 1: `composition.md` — add the "Forwarding through components" subsection**

Place it next to the existing forwarding/§Derived bags material:

```markdown
## Forwarding through components

A component can forward its own fallthrough bag into a component it calls —
`attrs` works on a nested invocation exactly as it does on an element:

    component SearchIcon() {
        <Icon name="search" class="w-5 h-5" { attrs... }/>
    }

    <SearchIcon class="text-red" aria-label="Search"/>

The bag concatenates into `Icon`'s bag at the spread's position; duplicates
resolve at the final element as usual (later wins per key, `class`/`style`
aggregate), so the outermost caller's attributes win and classes merge once.
Forwarding onto a component whose body never references `attrs` is a
generate-time error.
```

- [ ] **Step 2: Cross-references + ROADMAP**

- `props.md`: one line in the fallthrough/`Attrs` bag material: forwarding works through nested component calls too — link to composition.md §Forwarding through components.
- `attributes.md`: same one-liner where `{ x... }` spreads on components are described, plus one sentence stating component-call bags merge in source order (post-spread attrs win) matching elements.
- `ROADMAP.md`: (1) move the "Fallthrough forwarding through nested component calls" tracked-debt entry to a `[x]` with date + spec pointer; (2) mark the PR #103 "Fallthrough onto a component with no `Attrs` field" entry `[x]`, resolved by `component-missing-attrs`; (3) note the source-order alignment under the item-7/multi-spread lineage (one sentence, dated); (4) update item 18's follow-up pointer.
- If any edited guide page contains literal `{{ }}` in prose, keep/wrap it in a `::: v-pre` block (VitePress).

- [ ] **Step 3: Playground example**

Create the `examples/` txtar (doc block + `package views` `.gsx` + `-- invoke --` + `-- render.golden --`) showing the SearchIcon wrapper. Run: `make examples` and commit the regenerated `docs/guide/syntax/_generated/**` + preset JSON alongside.

- [ ] **Step 4: Verify docs build inputs + commit**

Run: `go test ./internal/examplegen/... 2>/dev/null; make check`
Expected: PASS (examples drift gate green).

```bash
git add docs examples internal
git commit -m "docs: nested fallthrough forwarding guide + ROADMAP"
```

---

### Task 6: Full verification + adversarial review gate

**Files:** none (verification only)

- [ ] **Step 1: Authoritative CI mirror**

Run: `make ci && make lint`
Expected: PASS, uncached (`-count=1`). Fix anything it surfaces before review.

- [ ] **Step 2: Independent adversarial review**

Dispatch the independent adversarial reviewer per repo convention: builds **throwaway probe programs** (a scratch module under the scratchpad dir exercising forwarding chains, the splat special-case, shadowing, cross-package rejection, URL sanitization through two hops), not just a diff read. Known hot spots to direct it at: the cond-attr thunk `errReturn` shape (has bitten 3 prior PRs — `AttrsCond` thunks inside forwarded branches), the Step-1-Task-2 rebuild-site enumeration (a missed `attrsPairsLiteral` site = ordering miscompile), and probe/emit skeleton drift for the newly-bound positions.

- [ ] **Step 3: Merge readiness**

All reviewer findings fixed and re-verified (`make ci` again after any fix). Then follow the `superpowers:finishing-a-development-branch` skill (PR against `main`).
