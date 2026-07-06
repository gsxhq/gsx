# Patterns docs section — Render once

**Date:** 2026-07-06
**Status:** Approved, executing

## Goal

Introduce a new top-level **Patterns** area in the guide for reusable tricks &
integrations (once-handle, htmx, structpages, …), seeded with the **Render once**
pattern. The pattern is a faithful userland port of templ's `OnceHandle` —
gsx ships no built-in render-once primitive, so this documents the recommended
recipe and pins it with a corpus case so it can't rot.

Source of the recipe: `~/work/one-learning-gsx/ui/once.gsx` (structpages middleware
stripped; net/http-only per the decision below).

## Decisions

1. **IA placement** — a *new top-level* sidebar group **"Patterns"**, not a fill-in
   of the pre-existing dead `Render once` link under "Interop and Gaps". That dead
   link is removed.
2. **Middleware framing** — **plain `net/http` only** (`func(http.Handler) http.Handler`).
   No structpages on this page; a structpages integration gets its own future
   Patterns page.
3. **Verification** — ship a **corpus case** proving the recipe generates, renders,
   and dedups (second render of the same handle is empty). Docs snippets are lifted
   from the verified case.

## Scope

### gsx repo (`docs/guide/`)

- **`patterns.md`** — Patterns overview: what the section is, a short list of current
  (Render once) + planned (htmx, structpages) patterns.
- **`patterns/render-once.md`** — the Once pattern:
  - Problem: emit a per-request singleton (dialog container, dev-mode asset
    preamble, one-time CSS/JS block) exactly once even when its owning component is
    invoked from many call sites.
  - Attribution: faithful port of **templ's `OnceHandle`** (`a-h/templ`, `once.go` /
    `templ.Once`). Explicit callout of the one gsx difference — templ auto-runs
    `InitializeContext` at the root of every generated `Render`; gsx does not, so the
    app installs a per-request scope itself via middleware.
  - Recipe (verified, lifted from the corpus case): `OnceHandle` + load-bearing `id`
    note, `NewOnce`, the `onceState`/context-key/`withOnceScope`/`firstRender`
    machinery, a plain `net/http` scope-install middleware, the `<Once>` component,
    and a usage snippet. Note the graceful degrade (no scope installed → always
    render, never crash).
  - No structpages here (deferred).

### gsx repo (`internal/corpus/testdata/cases/patterns/render_once.txtar`)

- `input.gsx`: the `OnceHandle`/`withOnceScope`/`firstRender`/`NewOnce` Go, the
  `<Once>` component, and a `Demo` component doing `{{ ctx := withOnceScope(ctx) }}`
  then rendering `<Once handle={demoOnce}>…</Once>` **twice**.
- Pins `generated.x.go.golden` + `render.golden` (second render empty). Regenerate
  `coverage.golden` + manifest.
- Feasibility confirmed by `docs/guide/syntax/raw-go.md:18`: a GoBlock-assigned
  variable is available to all subsequent children in the same scope, so the
  `ctx :=` rebind reaches both `<Once>` children. The corpus has no HTTP layer, so
  this GoBlock is how it simulates the middleware's scope install.

### website repo (`gsxhq.github.io/.vitepress/config.mts`)

- Add top-level sidebar group **"Patterns"** (after **Tooling**): Overview →
  `/guide/patterns`, Render once → `/guide/patterns/render-once`.
- Remove the dead `Render once` entry under **"Interop and Gaps"**.

### Conditional

- `docs/guide/status.md` — if it frames "no render-once primitive" as a known gap,
  point it at the new pattern; otherwise leave it.

## Non-goals (YAGNI)

- No structpages middleware variant on this page.
- No htmx page yet.
- No new runtime primitive in the gsx core — the Once machinery stays userland,
  exactly as in `once.gsx`.
- tree-sitter-gsx / vscode-gsx untouched (no new syntax).

## Verification

- `go test ./internal/corpus -run TestCorpus -update`, then re-run **without**
  `-update`.
- `make check` (build/vet/test/gofmt/gsx fmt).
- `{{ }}` shown in *prose* (outside code fences) must be wrapped in `::: v-pre`;
  GoBlock examples stay inside ```gsx fences and need no wrapping.
