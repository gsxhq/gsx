# Renderers follow-ups: probe-safe class props (#85) + ctx-taking renderers (#87)

**Date:** 2026-07-12
**Status:** Approved direction (both designs recorded in the issues during PR #84; batched on one branch per the PR #83 precedent)

Two independent items, one branch, one PR closing both.

## Issue #85 — component `class={}` rejects bare non-string exprs at the probe

### Problem

The analyze skeleton builds the same component-class expression the emitter
will (`classEntryExpr` with `probeWrap=true`), but stubs **only call
expressions** to `""`. A bare identifier (or selector, index expr, …) of any
non-string type flows unstubbed into `_gsxrt.Class(expr)` in the skeleton,
which fails Go's own type-check **before harvest/emit runs** — a raw go/types
error instead of a positioned gsx diagnostic. With renderers merged this also
blocks `<Card class={ val }/>` where `val` is a registered wrapper type,
unless the author wraps it in a call.

### Decision

In probe mode, `classEntryExpr` never imposes the string constraint on any
part value expression: **every** part value expr is stubbed in the skeleton,
not just calls. The skeleton's existing division of labor is unchanged —
liveness, type harvest (`resolved[p]` / `resolved[arm]`), filter-package
imports, and positioned errors for undefined identifiers all already ride the
separate counted per-part probes, exactly as the existing call-stub comment
documents ("the stub only defers that check out of the skeleton").

Per part shape:

- **Unconditional plain part** (`emit.go:5503`): stub condition widens from
  `probeWrap && isCallExpr(expr)` to `probeWrap` — every expr becomes `""`.
- **Conditional part** (`ClassIf`, `emit.go:5545` else-arm): today *nothing*
  is stubbed in probe mode (same gap, both calls and non-calls). Stub the
  value expr to `""`; the cond expr is a bool and stays.
- **Value-form CF arm** (`emit.go:5438`): calls keep the existing
  `_gsxunwrap(...)` wrap (unchanged — it preserves (T, error) tuple
  compatibility with the `_gsxvN string` assignment). Non-call exprs are
  stubbed to `""`.

Emit mode is untouched: registered types render via `applyClassRenderer`;
unregistered non-string types keep failing at `go build` of the `.x.go`
(`gsx.Class(s string)` re-imposes the constraint), exactly as call exprs
behave today.

### Constraints to preserve (each pinned by a test)

1. An **undefined identifier** in a class part still produces a positioned
   diagnostic (counted probes carry it).
2. No unused-local regression: an identifier used *only* in a class part must
   not be flagged unused by Go in the skeleton (counted probes keep it live).
3. Probe/emit `_gsxvN` temp alignment is unaffected (the stub lives inside
   the un-counted `_gsxusen` bag expression).

### Tests

Corpus (`internal/corpus/testdata/cases/renderers/` and the component-class
cases dir as appropriate):

- `class={ val }` bare ident of a **registered** wrapper type → renderer
  applies (the unblocking case).
- `class={ s.Field }` selector of a registered type → renderer applies.
- `class={ val cond }` conditional part, registered type → renderer applies.
- `class={ if c { val } }` CF arm, registered type → renderer applies.
- Regression pin: bare **string** ident part (works today, must keep working).

Unit (codegen): skeleton probe succeeds (no raw go/types error) for a bare
ident of a plain non-string struct; failure surfaces at `.x.go` build, parity
with calls.

## Issue #87 — ctx-taking renderer variant

### Problem

The v1 renderer contract is deliberately ctx-free, but display concerns are
often request-scoped (locale, timezone, user preferences) — which lives in
the render `ctx`. Primary motivation: locale-aware formatting and typed
message-key translation (i18n).

### Decision

Accept two additional shapes, mirroring the filter contract exactly:

```go
func(ctx context.Context, T) R
func(ctx context.Context, T) (R, error)
```

- **Harvest** (`harvestRenderers`): params len 1 or 2. Len 2 → param 0 must
  be `context.Context` (`isContextContext`, shared with `classifyFilter`) and
  param 1 is the subject `T`; len 1 → param 0 is `T`, except a lone
  `context.Context` param, which is rejected as ctx-without-subject (same as
  `classifyFilter`). `rendererEntry` gains `wantsCtx bool`.
  The contract diagnostic message lists all four shapes.
- **Emit** (`applyRenderer`): when `wantsCtx`, the call becomes
  `alias.Fn(ctx, (expr))` using `pipeCtxIdent` — every applyRenderer site
  with a non-empty errReturn sits inside the render closure or an
  `(Attrs, error)` thunk nested in it, where the ambient `ctx` is already in
  scope (proven by ctx-taking pipe filters at the same positions).
- **Unchanged**: `effectiveRenderType` (type-only), `rendererKey`, computeKey
  folding (registration strings are the same; renderer *signature* changes
  invalidate exactly as filter signature changes do), probe path and
  `errReturn == ""` disabled sites (renderers never evaluated there), and
  `gsx info` output (lists type → func, signature-agnostic).

### Tests

Unit (codegen, harvest): accept `func(ctx, T) R` and
`func(ctx, T) (R, error)`; reject `func(ctx) R` (no subject),
`func(T, ctx) R` (ctx not first), three params.

Corpus — one case per context class, ctx renderer applied (issue minimum plus
the boundary classes with distinct errReturn arities):

- text hole; URL attribute hole (sanitization pinned).
- ctx + error renderer with render-error capture pinned.
- component fallthrough-bag attr entry.
- component class part (`classEntryExpr` boundary).
- cond-attr branch (`(Attrs, error)` thunk arity).

### Docs

- `docs/guide/config.md` `[renderers]` section: one or two sentences — a
  renderer may take a leading `context.Context` and receives the render
  context. Concise (standing feedback).
- `docs/ROADMAP.md`: mark both follow-ups shipped.
- No syntax change → no sibling-repo (tree-sitter/vscode/CodeMirror) impact.

## Out of scope

- Positioned emit-time diagnostic for a non-string, unregistered class part
  (today it fails at `go build`, same as calls; unchanged).
- The other recorded renderer follow-ups: LSP hover renderer display,
  type-param holes, pgx preset, `gsx info --json` renderers field.
