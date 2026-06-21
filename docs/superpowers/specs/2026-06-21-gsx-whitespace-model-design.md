# Design: gsx whitespace model + `gsx fmt` foundation

**Date:** 2026-06-21
**Status:** Approved (design)
**Module:** `github.com/gsxhq/gsx`
**Unblocks:** a re-indenting `gsx fmt`; fixes today's verbatim-whitespace rendering.

## Problem

gsx currently renders inter-element whitespace **verbatim**: the parser keeps
whitespace-only `Text` nodes (e.g. `"\n\t\t"` between `<p>` and `<span>`) and codegen
emits them unchanged (`genNode` `Text` → `emitS(t.Value)`). Consequences:

- Authors indent markup like JSX, but that indentation is *rendered* — surprising,
  and bloats output.
- A real (re-indenting) formatter is impossible: changing indentation would change
  rendered output. `fmt` is therefore entangled with an undefined whitespace contract.

This design defines that contract (JSX-style), centralizes it in one pass, and shows
how codegen and `fmt` consume it so `fmt` is provably render-safe.

## The model — JSX whitespace algorithm

Within any **children list** (component body, element children, fragment,
control-flow body, `MarkupAttr` slot), whitespace in/around `Text` nodes normalizes
exactly as React/Babel handle JSXText:

1. **Per line:** split each `Text` run into lines; trim each line's leading/trailing
   whitespace; drop empty (whitespace-only) lines; join the surviving lines with a
   single space.
2. **Edge whitespace by newline:** whitespace at the start/end of a text run is
   **removed when it contains a newline** (cosmetic indentation) and **collapsed to a
   single significant space when it does not** (inline spacing).

Litmus tests:
```
<div>                          <p>a</p><span>b</span>
  <p>a</p>             →        (indentation newlines removed)
  <span>b</span>
</div>

<b>x</b> y             →        <b>x</b> y          (no newline → space kept)
<b>x</b>\ny            →        <b>x</b>y           (newline at edge → space removed)
Hello,   {name}!       →        Hello, {name}!      (inline run → one space)
```

Net: **indentation becomes cosmetic** (unlocking `fmt`); genuine inline spacing
between content survives as a single space.

## Preserved-verbatim contexts (collapse suspended)

`Normalize` keeps text byte-for-byte inside:

1. **`<pre>` and `<textarea>`** — whitespace is semantically significant. A `preserve`
   flag turns on when entering an element whose lowercased tag is `pre`/`textarea`
   and stays on for the whole subtree. *(Conservative and standard: it does not model
   CSS `white-space` overrides — no static formatter can. `pre`/`textarea` are the
   HTML-defined preserving elements; the line JSX/Prettier draw.)*
2. **Raw-text `<script>` / `<style>`** — already one verbatim `Text` from the parser
   (`parseRawTextBody`); untouched.
3. **Static attribute values** (`name="…"`) — literal quoted strings; never normalized.
4. **Go fragments** — interp/attr exprs, `{{ }}`, control-flow clauses, params,
   `GoChunk`. Not text; untouched (gofmt is `fmt`'s separate concern).
5. **`Doctype` / `HTMLComment`** — verbatim (already).

## Architecture — one shared `Normalize` pass

A pure, in-place AST transform in its own package so the rule lives in exactly one
testable place and `ast` stays pure data:

```go
// package internal/wsnorm
func Normalize(f *ast.File)
```

- Walks every children list (`Component.Body`, `Element.Children`, `Fragment.Children`,
  `IfMarkup.Then/Else`, `ForMarkup.Body`, `CaseClause.Body`) AND every attribute list
  (`Element.Attrs`, recursing `CondAttr.Then/Else`) to reach each `MarkupAttr.Value`
  (a named-slot markup list, normalized like any children list). Threads `preserve
  bool` (on inside `pre`/`textarea` subtrees; a `MarkupAttr` slot starts fresh —
  `preserve` does not leak across the expression boundary into a slot's markup).
- For each non-preserve children list, rewrites the `Text` nodes per the model —
  operating on the **whole list** (edge-newline decisions need neighbor context),
  dropping emptied text nodes and re-spanning survivors via `ast.SetSpan`.
- Skips raw-text `<script>`/`<style>` bodies and all Go fragments.
- **Idempotent by construction:** `Normalize(Normalize(f)) == Normalize(f)`.

## How codegen and `fmt` consume it (the faithfulness contract)

Both run the **same** `Normalize`, so they cannot disagree on significance.

**Codegen** (`GeneratePackage`): after parse, before emit, `wsnorm.Normalize(file)`.
`genNode`'s `Text` case then emits already-collapsed text. This is the behavior change
that rewrites render output (and goldens).

**`fmt`** (`internal/printer` + `gsx fmt`): `parse → Normalize → pretty-print`. The
printer emits the normalized AST with its **own** cosmetic indentation between block
markup; re-parsing + `Normalize` collapses that indentation right back.

Two contracts (the printer's acceptance/property tests over the corpus):

1. **Render-faithfulness:** `render(fmt(S)) ≡ render(S)` for all `S`.
   `render(X) = codegen(Normalize(parse(X)))`; `fmt(S) = print(Normalize(parse(S)))`;
   since `print` only adds collapsible whitespace and preserves normalized content,
   `Normalize(parse(fmt(S))) == Normalize(parse(S))`. **`fmt` never changes the page.**
2. **Idempotence:** `fmt(fmt(S)) == fmt(S)` — printing an already-normalized AST is
   stable.

`gsx fmt` modes (gofmt ergonomics): rewrite in place (default), `-l` (list files that
would change), `--check`/`-d` (exit 1 + diff). Go fragments inside the printer are run
through `go/format` for full canonicalization.

## Sequencing (load-bearing)

`wsnorm` + its codegen wiring **rewrites rendered output and every render golden** —
the exact surface the in-flight test-corpus reorg owns. Therefore:

1. **Build `wsnorm` as a standalone package + unit tests first** (pure, no codegen
   wiring → zero collision with the test reorg).
2. **Wire into codegen + add `internal/printer` + `gsx fmt`** only **after** the
   corpus migration lands, so the goldens regenerate once, in the new format, against
   the new behavior — no double-churn, no merge fights. Coordinate the cutover.

## Scope / non-goals

**In scope:** the JSX model; `wsnorm.Normalize`; codegen wiring; `internal/printer` +
`gsx fmt` (`-l`/`-w`/`--check`) with `go/format` on Go fragments; the
render-faithfulness + idempotence property tests.

**Deferred:**
- **`<script>`/`<style>` interpolation** (its own design) — `<script>`/`<style>` are
  JS/CSS contexts; a bare `{ x }` must fail closed (consistent with `onclick=`/`style=`
  today), and interpolation is allowed only via safe-type pipeline filters. The
  headline is `<script>const P = { data |> json }</script>` (HTML-safe JSON — roadmap
  security #6); `|> js` / `|> css` are the author-vouched escape hatches; CSP nonce is
  v2. Parser shape: `<script>`/`<style>` become raw-text with `{ }` interpolation
  holes. The whitespace model here is unaffected (their literal text stays verbatim).
- CSS-`white-space`-aware preservation (only `pre`/`textarea`).
- Comment reflow / max-line-width wrapping (re-indent structure + gofmt Go only).
- Configurability (zero-config, one canonical style — gofmt/CLI-spec philosophy).

## Risks

- **Edge-list normalization correctness** — the per-list, neighbor-aware rewrite is
  the subtle core; property tests (faithfulness + idempotence) over the whole corpus
  are the guard.
- **Golden churn timing** — must land after the corpus reorg; `wsnorm` ships first,
  decoupled, to de-risk.
- **`pre`/`textarea` nesting** — the `preserve` flag must stay on through arbitrary
  nested descendants; tested with `pre` wrapping elements + interps.
