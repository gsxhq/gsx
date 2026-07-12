# Element-level conditional class/style part harvest (#88) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close issue #88 — registered renderers (and `(T, error)` auto-unwrap) apply to *conditional* class/style parts on elements, matching unconditional parts and the component-side fix from #85.

**Architecture (design, in lieu of a separate spec — this extends the merged
`2026-07-12-renderers-followups-design.md` #85 mechanism to the element
branch):** The emit path needs NO change — `composedParts`
(internal/codegen/emit.go:3339-3399) already runs `resolved[p]` through the
tuple-unwrap + `applyRenderer` pipeline for every plain part, conditional or
not; conditional parts merely arrive with `resolved[p] == nil` because the
element-branch harvest never probes them. The fix is harvest-only: probe
conditional plain parts with counted `_gsxuse`, exactly as unconditional
parts and CF arms already are, in BOTH walks (`emitProbes` +
`collectExprs`) so the k-th-probe/k-th-node alignment is preserved by
construction — the same lockstep widening #85 applied to the component
branch. Consequence for free: `(T, error)` conditional parts, which today
break the skeleton via an illegal single-value liveness reference, harvest a
tuple type that `composedParts` already unwraps.

**Tech Stack:** Go, go/types, txtar corpus.

## Global Constraints

- Runtime (root `gsx` package) untouched; no new `packages.Load`.
- `emitProbes` and `collectExprs` must use the IDENTICAL part filter — a
  probe emitted by one walk but not counted by the other desyncs every later
  harvested type in the file.
- `walkLivenessAttrExprs` currently emits `_ = (expr)` liveness statements
  for conditional part exprs ("CF arms and unconditional plain parts are
  excluded (they have _gsxuse probes above)" — analyze.go ~1923). Once
  conditional parts have probes, they MUST be excluded there too: not just
  tidiness — `_ = (f())` is illegal for a two-value `(T, error)` call, so
  leaving it would keep the skeleton broken for tuple conditional parts. The
  cond GUARD expressions must KEEP their liveness references.
- Emit-mode output for existing cases must be byte-identical except
  coverage.golden and (mechanical) `_gsxfN` alias renumbering from added
  cases; goldens regenerated via `go test ./internal/corpus -run TestCorpus
  -update`, verified without, never hand-edited.
- Run `make check` per task; `make ci` before the PR.

---

### Task 1: probe conditional plain parts at the element branch

**Files:**
- Modify: `internal/codegen/analyze.go` — the element-branch class-part probe
  loop in `emitProbes` (~1888-1912, condition `ca.Parts[i].Cond == "" &&
  ca.Parts[i].CSSSegments == nil`) and its `collectExprs` counterpart
  (~3014, same condition); the liveness exclusion in `walkLivenessAttrExprs`
  (or its caller-side filter) so probed conditional part exprs are no longer
  `_ = (expr)`-referenced while their cond guards still are.
- Create: `internal/corpus/testdata/cases/renderers/elem_class_cond.txtar`
- Create: `internal/corpus/testdata/cases/renderers/elem_style_cond.txtar`
- Create: `internal/corpus/testdata/cases/renderers/elem_class_cond_error.txtar`
- Create (non-renderer regression, pipeerr dir): a case pinning a plain
  `(T, error)` call conditional part on an element if none exists — check
  `pipeerr/class_part_mid_stage.txtar` first; if it already covers the
  conditional shape, skip.
- Test: extend `internal/codegen/renderers_test.go` only if a non-corpus
  assertion is needed (corpus should carry this).

**Interfaces:**
- Consumes: `composedParts`' existing `resolved[p]` tuple-unwrap +
  `applyRenderer` pipeline (emit.go:3339-3399) — unchanged.
- Produces: `resolved[p]` populated for element conditional plain parts.

- [ ] **Step 1: Failing corpus case (renderer, class)**

`elem_class_cond.txtar`, modeled on the merged `class_bare_cond.txtar` but on
an ELEMENT, using the same `pg`/`rend` packages (module path
`corpustest/cases/renderers_elem_class_cond`):

```
-- input.gsx --
package views

import "corpustest/cases/renderers_elem_class_cond/pg"

component Page(val pg.Text, on bool) {
	<div class={ val: on }>x</div>
}
-- invoke --
Page(PageProps{Val: pg.Text{String: "btn", Valid: true}, On: true})
```

Run `go test ./internal/corpus -run 'TestCorpus/renderers/elem_class_cond' -update`.
Expected RED: generated-code compile failure `cannot use val ... as string
value in argument to _gsxrt.ClassIf` (the issue #88 repro).

- [ ] **Step 2: Widen both harvest walks in lockstep**

In `emitProbes`' element-branch loop, change the plain-part condition from
`ca.Parts[i].Cond == "" && ca.Parts[i].CSSSegments == nil` to
`ca.Parts[i].CSSSegments == nil` (CF arms remain the first branch), updating
the comment ("Unconditional plain part" → plain part, conditional or not —
harvests the type for renderer application and (T, error) unwrap, #88).
Make the IDENTICAL change at the `collectExprs` counterpart. Then adjust the
liveness walk so conditional part EXPRS are excluded (their probes now keep
them live) while cond guards keep their `_ = (cond)` / recorded references.
Grep first: the exclusion may live inside `walkLivenessAttrExprs` itself or
in what the callback receives — keep component and element contexts
consistent (the component branch's bag expr embeds parts un-probed by this
walk; verify with the existing component corpus staying green).

- [ ] **Step 3: GREEN + goldens**

`go test ./internal/corpus -run 'TestCorpus/renderers/elem_class_cond' -update`
then without `-update`. Golden must show
`_gsxrt.ClassIf(_gsxfN.PgText((val)), on)` (or the ordered-capture temp
variant) and render `class="btn"`.

- [ ] **Step 4: The style and error siblings**

`elem_style_cond.txtar`: `<div style={ val: on }>` — expect the renderer
inside `StyleValue(...)` per composedParts' style wrapping, `ClassIf`
lowering, rendered `style="..."`.
`elem_class_cond_error.txtar`: renderer `func(pg.Text) (string, error)` —
expect the hoist (`_gsxvN, _gsxerr := ...; if _gsxerr != nil { return
_gsxerr }`) BEFORE the class call, plus a render-error invoke pin (model on
`renderers/attr_error_cond.txtar`'s error-capture invoke).
Also cover the renderer-less `(T, error)` conditional part (Step 1's Files
note): a plain filter/helper returning `(string, error)` used as
`class={ f(v): on }` on an element — previously a broken skeleton, now
unwrapped. Place it in `pipeerr/` following that dir's naming.
Regenerate all (`-run 'TestCorpus' -update`), verify without.

- [ ] **Step 5: Alignment + regression sweep**

- Craft-check alignment: at least one new case must have OTHER interps
  before AND after the class attr in the same component (mirroring the
  branch's earlier alignment probes) so a probe-count desync would mis-type
  a neighbor and fail loudly.
- `go test ./internal/corpus -run TestCorpus -count=1` full: existing goldens
  byte-identical modulo alias renumbering + coverage.golden.
- `go test ./internal/codegen -count=1` and `make check`.

- [ ] **Step 6: Docs + commit**

ROADMAP: mark the #88 bullet (added in PR #89's ROADMAP edit as "remains
open - issue #88") as fixed, one sentence, same style as the #85 bullet.

```bash
git add -A . && git commit -m "fix(codegen): harvest element conditional class/style parts — renderers + (T,error) apply (#88)"
```

---

## Final

- `make ci` + `make lint`.
- Reviewer must live-probe (temp corpus case or scratch module), not just
  read the diff.
- PR closes #88.
