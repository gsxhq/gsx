# Renderers Follow-ups (#85 + #87) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close issue #85 (component `class={}` probe rejects bare non-string exprs) and issue #87 (ctx-taking renderer variant `func(ctx, T) R`).

**Architecture:** #85 widens the analyze-skeleton stub in `classEntryExpr` so no class-part value expr ever imposes the string constraint in the probe (liveness/type-harvest already ride the counted probes). #87 mirrors the filter ctx contract: harvest classifies a leading `context.Context` param into `rendererEntry.wantsCtx`; `applyRenderer` prepends `pipeCtxIdent` to the call.

**Tech Stack:** Go, go/types, txtar corpus.

**Spec:** `docs/superpowers/specs/2026-07-12-renderers-followups-design.md`

## Global Constraints

- Runtime (root `gsx` package) stays standard-library only. Neither task touches the runtime.
- Every codegen behavior change ships a txtar corpus case pinning `input.gsx` + `generated.x.go.golden` + `render.golden`. Regenerate goldens with `go test ./internal/corpus -run TestCorpus -update` (also rewrites `coverage.golden`), then verify WITHOUT `-update`. Never hand-edit goldens.
- No new `packages.Load` calls anywhere.
- Probe/emit `_gsxvN` temp alignment must be preserved: probe-mode edits in `classEntryExpr` live inside the un-counted `_gsxusen`/`_gsxunwrap` bag expression and must not add or remove counted `_gsxuse`/`_gsxuseq` probes.
- #87's contract mirrors `classifyFilter` (`internal/codegen/filters.go:458`) exactly: optional leading `context.Context` detected with `isContextContext`; ctx variant requires one MORE param (the subject).
- The ctx identifier emitted is `pipeCtxIdent` (`internal/codegen/filters.go:30`), never a literal `"ctx"` string sprinkled anew.
- Docs are concise: state behavior plainly, one or two sentences; rationale lives in the spec.
- Run `make check` before finishing each task; `make ci` before the PR.

---

### Task 1: #85 — probe-mode stubbing of all class-part exprs

**Files:**
- Modify: `internal/codegen/emit.go` (three sites inside `classEntryExpr`, ~lines 5424–5551)
- Modify: `internal/corpus/testdata/cases/renderers/class_part_component.txtar` (comment only — it documents the gap as out of scope; it no longer is)
- Create: `internal/corpus/testdata/cases/renderers/class_bare_ident.txtar`
- Create: `internal/corpus/testdata/cases/renderers/class_bare_cond.txtar`
- Create: `internal/corpus/testdata/cases/renderers/class_bare_cfarm.txtar`
- Test: `internal/codegen/renderers_test.go` (one new test)

**Interfaces:**
- Consumes: `classEntryExpr` (emit.go:5387), `isCallExpr`, existing corpus loader `[renderers]` support (`./`-relative keys).
- Produces: no signature changes; behavior only.

- [ ] **Step 1: Write the failing corpus case (bare ident)**

Create `internal/corpus/testdata/cases/renderers/class_bare_ident.txtar`. Model it on `class_part_component.txtar` (same `pg`/`rend` packages), but the class value is a BARE IDENTIFIER of the registered type — exactly the shape issue #85 says fails the skeleton probe today. Include a second part with a bare SELECTOR of the registered type to cover both non-call expr shapes in one case:

```
# Component class={} with a BARE IDENTIFIER and a BARE SELECTOR of the
# REGISTERED type pg.Text — the issue #85 shapes: the skeleton probe used to
# stub only CALL exprs, so a bare non-string expr flowed into
# _gsxrt.Class(expr) and failed go/types before harvest. Probe mode now stubs
# EVERY class-part value expr; liveness and type harvest ride the counted
# per-part probes, and the renderer applies at emit exactly as it does for a
# call expr (class_part_component.txtar).
-- gsx.toml --
[renderers]
"./pg.Text" = "./rend.PgText"
-- pg/pg.go --
package pg

type Text struct {
	String string
	Valid  bool
}
-- rend/rend.go --
package rend

import "corpustest/cases/renderers_class_bare_ident/pg"

func PgText(t pg.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
-- input.gsx --
package views

import "corpustest/cases/renderers_class_bare_ident/pg"

type box struct{ Cls pg.Text }

component Card(title string) { <div { attrs... }>{title}</div> }

component Page(val pg.Text, b box) {
	<Card title="hi" class={ val b.Cls }/>
}
-- invoke --
Page(PageProps{Val: pg.Text{String: "btn", Valid: true}, B: box{Cls: pg.Text{String: "extra", Valid: true}}})
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
```

(Empty golden sections are placeholders the `-update` run fills; the module path `corpustest/cases/renderers_class_bare_ident` follows the loader's dir-derived naming — copy the exact scheme from `class_part_component.txtar`.)

- [ ] **Step 2: Run the case to verify it fails today**

Run: `go test ./internal/corpus -run 'TestCorpus/renderers/class_bare_ident' -update`
Expected: FAIL with a raw go/types error from the skeleton (something like `cannot use val (variable of struct type pg.Text) as string value`), NOT a golden diff — this is the bug.

- [ ] **Step 3: Widen the probe stubs in `classEntryExpr`**

Three edits in `internal/codegen/emit.go`:

(a) Unconditional plain part (~5503) — stub everything, not just calls:

```go
// old
if probeWrap && isCallExpr(expr) {
	expr = `""`
} else if !probeWrap {
// new
if probeWrap {
	// Probe mode: stub EVERY part value expr (call or not) with "" so the
	// skeleton never imposes gsx.Class's string constraint — a bare
	// identifier/selector of a non-string (e.g. registered) type must not
	// fail the skeleton's own type-check (#85). Liveness and type harvest
	// ride the counted per-part probes; the string constraint is re-imposed
	// by gsx.Class in the emitted code.
	expr = `""`
} else {
```

Update the existing comment block above it (it currently says "in probe mode stub call exprs") to match.

(b) Conditional part (`ClassIf`, ~5545 else-arm) — today nothing is stubbed there in probe mode:

```go
// old
} else {
	if !probeWrap {
		expr = applyClassRenderer(expr, resolved[p])
	}
	parts = append(parts, fmt.Sprintf("%s.ClassIf(%s, %s)", rtPkg, expr, strings.TrimSpace(p.Cond)))
}
// new
} else {
	if probeWrap {
		// Same #85 stub as the unconditional arm: the value expr must not
		// impose the string constraint in the skeleton. The cond expr is a
		// bool guard and stays as-is.
		expr = `""`
	} else {
		expr = applyClassRenderer(expr, resolved[p])
	}
	parts = append(parts, fmt.Sprintf("%s.ClassIf(%s, %s)", rtPkg, expr, strings.TrimSpace(p.Cond)))
}
```

(c) Value-form CF arm (~5438) — calls keep the `_gsxunwrap` wrap (tuple compatibility with the `_gsxvN string` assignment); non-calls are stubbed:

```go
// old
if probeWrap && isCallExpr(expr) {
	expr = fmt.Sprintf("_gsxunwrap(%s)", expr)
} else if !probeWrap {
// new
if probeWrap && isCallExpr(expr) {
	expr = fmt.Sprintf("_gsxunwrap(%s)", expr)
} else if probeWrap {
	// Non-call arm expr: stub with "" (#85) — the _gsxvN string assignment
	// must not impose the string constraint in the skeleton.
	expr = `""`
} else if !probeWrap {
```

(The trailing `else if !probeWrap` collapses to `else`; keep whichever reads cleanest with the surrounding comments.)

- [ ] **Step 4: Regenerate and verify the bare-ident case passes**

Run: `go test ./internal/corpus -run 'TestCorpus/renderers/class_bare_ident' -update`
Then: `go test ./internal/corpus -run 'TestCorpus/renderers/class_bare_ident'`
Expected: PASS. Inspect `generated.x.go.golden`: both parts flow through `rend.PgText(...)` (aliased `_gsxfN`) into `_gsxrt.Class(...)`; `render.golden` shows `class="btn extra"` merged at the leaf.

- [ ] **Step 5: Add the conditional-part and CF-arm corpus cases**

`class_bare_cond.txtar`: same packages; class attr `class={ val cond }` where `val` is a bare `pg.Text` local and `cond` a bool prop (`<Card title="hi" class={ val on }/>` with `component Page(name string, on bool)`); invoke with `On: true`. Expected generated shape: `_gsxrt.ClassIf(<renderer call>, on)`.

`class_bare_cfarm.txtar`: class attr `class={ if on { val } else { other } }` with `val`, `other` bare `pg.Text` locals. Expected: the CF `_gsxvN` assignment arms run the renderer (`applyClassRenderer` at the arm, emit mode).

Run: `go test ./internal/corpus -run 'TestCorpus/renderers' -update` then without `-update`.
Expected: PASS both, goldens pinned.

- [ ] **Step 6: Pin probe-parity for an UNREGISTERED non-string bare ident (unit test)**

In `internal/codegen/renderers_test.go`, add a test following the existing full-generation test pattern in that file / `filters_ctx_test.go` (temp-module fixture): a component `class={ val }` where `val` is a plain struct with NO `[renderers]` registration. Assert generation SUCCEEDS with no diagnostics (the probe no longer fails) and the emitted `.x.go` contains `Class(val)` — the wrong type now surfaces at `go build` of the `.x.go`, parity with call exprs. Also assert the pre-fix behavior is truly gone: generation must not return a raw `go/types` skeleton error mentioning `cannot use val`.

- [ ] **Step 7: Verify no regressions on existing constraints**

Run: `go test ./internal/codegen -run 'TestClass|TestAttrClass|TestRenderer' && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Specifically confirm existing cases `class/`, `renderers/class_part*`, `renderers/valuecf_arm` are byte-identical (no golden churn — the stub only changes SKELETON text, never emitted code).

Also verify the undefined-identifier diagnostic still positions: `go test ./internal/corpus -run 'TestCorpus/diagnostics'` (and if no class-part undefined-ident diagnostic case exists, add one: `class={ nosuch }` on a component → positioned `undefined: nosuch` in `diagnostics.golden`, non-renderable case).

- [ ] **Step 8: Update the stale comment + commit**

Trim the second paragraph of the header comment in `class_part_component.txtar` (the "pre-existing probe-stage gap is out of this task's scope" note) to say the gap was closed by #85 and bare idents are pinned by `class_bare_ident.txtar`. Comment-only txtar edits don't change goldens, but re-run `go test ./internal/corpus -run 'TestCorpus/renderers/class_part_component'` to be sure.

```bash
git add -A . && git commit -m "fix(codegen): stub all class-part exprs in probe mode (#85)"
```

---

### Task 2: #87 — harvest the ctx-taking renderer shapes

**Files:**
- Modify: `internal/codegen/renderers.go` (`rendererEntry`, `harvestRenderers`)
- Test: `internal/codegen/renderers_test.go`

**Interfaces:**
- Consumes: `isContextContext` (already used by `classifyFilter`).
- Produces: `rendererEntry.wantsCtx bool` — Task 3 reads it in `applyRenderer`.

- [ ] **Step 1: Write failing harvest unit tests**

In `renderers_test.go`, extend the existing `harvestRenderers` contract tests using the `rendererFixturePkg`/`rendererSig` helpers. Build a synthetic ctx type the same way `filters_ctx_test.go` / `classify_test.go` does (named type `Context` in package `context`, or reuse an existing helper if one exists — check first). Cases:

```go
// accepted, wantsCtx=true
func(ctx context.Context, t pg.Text) string
func(ctx context.Context, t pg.Text) (string, error)
// rejected (contract error)
func(ctx context.Context) string                    // no subject after ctx
func(t pg.Text, ctx context.Context) string         // ctx not first
func(ctx context.Context, a, b pg.Text) string      // three params
// unchanged
func(t pg.Text) string                              // wantsCtx=false
```

Assert `table[key].wantsCtx` for the accepted shapes and that the rejection message lists the four contract shapes.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestHarvestRenderers -v` (adjust to the actual test names in the file)
Expected: FAIL — two-param renderers are currently rejected outright.

- [ ] **Step 3: Implement classification in `harvestRenderers`**

In `renderers.go`: add `wantsCtx bool` to `rendererEntry` (after `hasErr`). Replace the params clause of the contract check (`sig.Params().Len() != 1`) with:

```go
params := sig.Params()
wantsCtx := params.Len() == 2 && isContextContext(params.At(0).Type())
subjectOK := params.Len() == 1 || wantsCtx
```

and fold `!subjectOK` into the existing single contract-error condition, updating the message to:

```
"codegen: renderer %q for %q does not match the renderer contract func(T) R, func(T) (R, error), func(ctx context.Context, T) R, or func(ctx context.Context, T) (R, error)"
```

The subject param for the key check becomes the LAST param:

```go
subject := params.At(params.Len() - 1)
if pk := rendererKey(subject.Type()); pk != r.TypeKey { ... }
```

Set `wantsCtx: wantsCtx` in the `rendererEntry` literal. Update the doc comments on `RendererAlias`/`rendererEntry`/`harvestRenderers` that state the two-shape contract.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/codegen -run 'Renderer' -v`
Expected: PASS, including all pre-existing renderer tests (one-param shapes unchanged).

- [ ] **Step 5: Commit**

```bash
git add -A . && git commit -m "feat(codegen): harvest ctx-taking renderer shapes (#87)"
```

---

### Task 3: #87 — emit ctx injection + corpus per context class

**Files:**
- Modify: `internal/codegen/renderers.go` (`applyRenderer`)
- Create: `internal/corpus/testdata/cases/renderers/ctx_text.txtar`
- Create: `internal/corpus/testdata/cases/renderers/ctx_attr_url.txtar`
- Create: `internal/corpus/testdata/cases/renderers/ctx_error.txtar`
- Create: `internal/corpus/testdata/cases/renderers/ctx_fallthrough_attr.txtar`
- Create: `internal/corpus/testdata/cases/renderers/ctx_class_part.txtar`
- Create: `internal/corpus/testdata/cases/renderers/ctx_cond_attr.txtar`

**Interfaces:**
- Consumes: `rendererEntry.wantsCtx` (Task 2), `pipeCtxIdent` (filters.go:30).
- Produces: emitted calls `alias.Fn(ctx, (expr))` at every wired boundary.

- [ ] **Step 1: Write the failing corpus case (text hole)**

`ctx_text.txtar`, modeled on `text_basic.txtar` + the ctx-filter precedent `pipeerr/ctx_err_filter.txtar`. The renderer proves ctx is real (non-nil), not just that the code compiles:

```go
-- rend/rend.go --
package rend

import (
	"context"

	"corpustest/cases/renderers_ctx_text/pg"
)

func PgText(ctx context.Context, t pg.Text) string {
	if ctx == nil {
		return "NO-CTX"
	}
	if !t.Valid {
		return ""
	}
	return t.String
}
```

with `input.gsx` rendering `{ val }` for a `pg.Text` prop/local. `render.golden` must show the value (not `NO-CTX`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/corpus -run 'TestCorpus/renderers/ctx_text' -update`
Expected: FAIL at harvest… no — Task 2 already accepts the shape, so this fails at COMPILE of the generated code (`not enough arguments in call to _gsxfN.PgText`) because `applyRenderer` doesn't pass ctx yet. Either failure mode is the RED we want.

- [ ] **Step 3: Implement ctx injection in `applyRenderer`**

In `renderers.go`:

```go
// old
call := e.alias + "." + e.funcName + "((" + expr + "))"
// new
args := "(" + expr + ")"
if e.wantsCtx {
	// Ambient render ctx, same injection as a ctx-taking pipe filter
	// (lowerPipe): every applyRenderer site with a non-empty errReturn sits
	// inside the render closure or an (Attrs, error) thunk nested in it,
	// where pipeCtxIdent is in scope.
	args = pipeCtxIdent + ", " + args
}
call := e.alias + "." + e.funcName + "(" + args + ")"
```

Update `applyRenderer`'s doc comment (mention the ctx variant). `effectiveRenderType` needs no change — assert that in the comment only if it doesn't already say "type-only".

- [ ] **Step 4: Regenerate and verify the text case**

Run: `go test ./internal/corpus -run 'TestCorpus/renderers/ctx_text' -update` then without `-update`.
Expected: PASS; `generated.x.go.golden` contains `_gsxfN.PgText(ctx, (val))`.

- [ ] **Step 5: Add the remaining five corpus cases**

Each reuses the same `pg`/`rend` ctx-renderer pattern (adjust module path per case dir); model the boundary shape on the named existing case:

| case | boundary | model on | pinned expectation |
|---|---|---|---|
| `ctx_attr_url.txtar` | `href={ val }` URL attr | `attr_url.txtar` | renderer runs BEFORE whole-value URL sanitization; `ctx, (` in golden |
| `ctx_error.txtar` | text hole, `func(ctx, T) (string, error)` | `text_error.txtar` | `_gsxvN, _gsxerr := _gsxfN.X(ctx, (…)); if _gsxerr != nil { return _gsxerr }`; render-error capture pinned in the invoke |
| `ctx_fallthrough_attr.txtar` | unmatched attr → fallthrough bag | `fallthrough_attr.txtar` | bag entry value rendered via `(ctx, …)` call |
| `ctx_class_part.txtar` | component `class={ val }` (bare ident — lands on Task 1's fix) | `class_part_component.txtar` | `applyClassRenderer` path emits `(ctx, …)` |
| `ctx_cond_attr.txtar` | `{ if cond { attr={val} } }` on a component | `attr_error_cond.txtar` | ctx used inside the `(Attrs, error)` thunk — proves ctx reaches the thunk arity site |

Run: `go test ./internal/corpus -run 'TestCorpus/renderers' -update` then without `-update`.
Expected: PASS ×6; every golden shows `(ctx, ` at the renderer call.

- [ ] **Step 6: Full-suite check + commit**

Run: `make check`
Expected: PASS (corpus incl. regenerated `coverage.golden` manifest, codegen, gen, root runtime, examples drift, fmt).

```bash
git add -A . && git commit -m "feat(codegen): inject ambient ctx into ctx-taking renderer calls (#87)"
```

---

### Task 4: Docs + ROADMAP

**Files:**
- Modify: `docs/guide/config.md` (`[renderers]` section)
- Modify: `docs/ROADMAP.md`

**Interfaces:** none (prose only).

- [ ] **Step 1: config.md**

In the `[renderers]` section's contract description, extend the accepted shapes to the four forms and add ONE sentence:

> A renderer may take a leading `context.Context`; it receives the render context (`gsx.Render`'s ctx), like a ctx-taking filter.

Show `func(ctx context.Context, T) R` in the shapes list/example. Keep it to that — no rationale, no i18n essay (spec owns the why). No `::: v-pre` concerns unless you add literal `{{ }}` (don't).

- [ ] **Step 2: ROADMAP**

In `docs/ROADMAP.md`, move/mark the two follow-up bullets (classEntryExpr bare-ident probe gap #85; ctx-taking renderers #87) as shipped, following the file's existing convention for done items.

- [ ] **Step 3: Docs job sanity + commit**

`docs/guide/**` changed → confirm no raw `{{ }}` added. Run `make check` once more.

```bash
git add -A . && git commit -m "docs: [renderers] ctx variant + ROADMAP follow-ups shipped (#85, #87)"
```

---

## Final

- `make ci` on the branch (authoritative, uncached).
- Final adversarial review (per repo convention) before PR.
- PR closes #85 and #87 (`Closes #85`, `Closes #87` in the body).
