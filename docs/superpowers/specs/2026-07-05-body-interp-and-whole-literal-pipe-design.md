# Body backtick literals + whole-literal pipe

**Status:** design / awaiting review
**Date:** 2026-07-05
**Branch:** `attr-interp-body-pipe` (follow-up to the merged PR #33 attr-interp literals)

## Motivation

PR #33 added interpolating backtick literals `` name=`…@{expr}…` `` in **attribute-value**
position, with **per-hole** pipelines (`@{ v |> upper }`). Two gaps remain — and they're
linked:

1. **The literal is inert in body/child position.** `<p>{`row-@{id}-@{n}`}</p>` today parses the
   backtick as an ordinary Go raw string and renders the literal text `row-@{id}-@{n}` — the
   `@{ }` holes are *not* interpolated. A user reaching for the same syntax they use in attributes
   gets silent wrong output. (Verified: no existing gsx source anywhere uses a backtick literal in
   a body/child `{ }`, so closing this cannot break anything.)

2. **The whole-literal pipeline was never built.** The original spec described *two* pipeline
   levels — per-hole (`@{ v |> f }`, shipped) and **whole-literal** (`` `…` |> f ``, never wired).
   `` `btn-@{v}` |> upper `` is a hard parse error today, in body *and* attribute position.

These are linked because #1 alone is redundant: `{`row-@{id}`}` renders identically to the native
`row-{id}` interleave. What makes the body form *earn its place* is #2 — piping the **whole
assembled string** as a unit:

```gsx
<p>{`item-@{id}-@{n}` |> upper}</p>   →   ITEM-<ID>-<N>
```

You cannot express that with interleave (`item-{id |> upper}-{n}` only uppercases `id`, leaving
`item-`/`-` untouched), and it reads far better than `{ fmt.Sprintf("item-%s-%d", id, n) |> upper }`.
This is the "it's a composable string expression" property from the original brainstorm.

## Feature 1 — body/child backtick literal interpolates

`` <p>{`row-@{id}-@{n}`}</p> `` interpolates its `@{ }` holes, **text-escaped** (same escaping as
`<p>row-{id}-{n}</p>` — `_gsxgw.Text`), and the **form is preserved** by `gsx fmt` (it stays
`{`row-@{id}-@{n}`}`, consistent with how gsx preserves the attribute backtick form rather than
canonicalizing it).

- **Trigger:** a child/body interpolation `{ EXPR }` whose EXPR is exactly a bare backtick literal
  (optionally followed by a whole-literal `|>` pipeline — Feature 2). A backtick used as a
  *sub-expression* (`{ `a` + b }`, `{ f(`a`) }`) stays an ordinary Go raw string — only a **lone**
  backtick literal (whole `{ }` value) is treated as `EmbeddedText`.
- **No-pipe zero-alloc:** without a pipeline, holes lower to per-segment writes exactly like native
  body interleave (`S("row-")`, `Text`-render of `id`, …) — no materialized string.
- **Escaping (`\@{`, `` \` ``)** carry over unchanged.

## Feature 2 — whole-literal pipeline `` `…` |> f ``

The whole interpolated string is **assembled**, piped through the filter chain as a unit, then
escaped/sanitized for its context.

- **Positions:** the `{ }`-delimited expression positions — **body** `{ `…` |> f }` and **braced
  attribute** `attr={`…` |> f}`. *(Open sub-decision: also the bare direct attribute
  `attr=`…` |> f`? A floating `|>` in the attr list is less clean; recommend requiring the braced
  form for a whole-literal pipe on attributes. Per-hole pipes stay available in the direct form.)*
- **Codegen:** reuse the URL path's assembly — build one Go string expression from the segments
  (`"item-" + string(id) + "-" + strconv.Itoa(n)`, via the existing `holeStringExpr`), use it as
  the `lowerPipe` seed, then emit the piped result for the context:
  - body → `_gsxgw.Text(<piped>)`
  - plain attr → `_gsxgw.AttrValue(<piped>)`
  - **URL attr → `_gsxgw.URL(<piped>)`** — sanitize **after** the pipe. Safe by construction: a
    filter that returns `javascript:…` is still blocked to `about:invalid#gsx`.
- **AST:** the embedded-literal node gains a `Stages []PipeStage` field for the whole-literal
  pipeline, distinct from each hole's own `Interp.Stages`.

### Security note

No new escaping surface. The **final** value (assembled → piped) is always run through the
context's existing escaper/sanitizer (`Text`/`AttrValue`/`URL`) before output — identical to how a
piped `{ expr |> f }` is handled today. In particular the URL invariant (whole-value
`urlSanitize`, fail-closed) holds because sanitization happens on the pipe's output.

## Parser

- Child/body path (`parser/markup.go`, around `parseInterp`): when a `{ }` child interpolation's
  content is a lone bare backtick literal (± trailing `|>`), parse it as `EmbeddedText` segments
  (+ optional whole-literal stages) and produce the body-embedded node. Otherwise fall back to the
  existing Go-expression parse (backtick = raw string). Detection must handle the fallback cleanly
  (lookahead or rewind) so `{ `a` + b }` stays a Go expression.
- Attribute braced path (`parser/attrs.go` `parseBracedEmbeddedAttrValue`): after the embedded
  literal, accept an optional `|>` pipeline before the closing `}`.

## AST / codegen / printer / LSP

- **AST:** add `Stages []PipeStage` to the embedded-literal representation (attr + a new body node,
  or a shared carrier). Holes remain `*ast.Interp` (per-hole stages unchanged).
- **Codegen:** the whole-literal assemble-and-pipe helper is shared across body + attr contexts;
  reuses `holeStringExpr` + `lowerPipe`.
- **Printer:** body node prints `{ `…@{}…` [|> stages] }`; braced attr prints `attr={`…` |> f}`.
  Preserve the form (no canonicalization).
- **LSP:** body-embedded holes participate in gd/hover — add the new body node to `ast.Inspect`
  (mirroring the `EmbeddedAttr` case added in PR #33) so its `Interp` children are visited; whole-
  literal pipeline stage names navigate like other pipeline stages.

## Testing (corpus, per context)

- body: interp (string + int holes) · per-hole pipe · whole-literal pipe · `\@{` escape ·
  lone-vs-sub-expression (`{ `a` + b }` stays a Go raw string)
- braced attr: whole-literal pipe · **URL attr whole-literal pipe** (filter output still sanitized
  → dangerous scheme blocked)
- formatter idempotence for the body form and the whole-literal pipe
- (extend the URL fuzzer seed corpus with a whole-literal-piped href)

## Non-goals

- Direct-attribute whole-literal pipe (`attr=`…` |> f`) — deferred unless we decide the floating
  `|>` reads acceptably; braced form covers it.
- Whole-literal pipe in `<script>`/`<style>` bodies (they already interpolate via `@{ }`; a
  whole-literal pipe there is out of scope).

## Open sub-decisions (confirm on review)

1. Whole-literal pipe on the **bare direct attribute** form, or braced-only? (recommend braced-only)
2. Branch/PR: land as its own PR on `attr-interp-body-pipe` (recommended).
