# Final Whole-Branch Review — Fix Report

**Status:** COMPLETE

**Commit:** (see below — committed after this report was written)

**Tests + drift:**
- `go test ./internal/corpus -run TestExamples` (no `-update`) → PASS
- `make ci-examples` → exit 0, no drift (after staging all generated artifacts)
- `go build ./...` → clean

---

## Fix 1 — Content comments `{/* … */}` documented on the Comments page

**Applied.**

**Fixture created:** `examples/216-content-comment.txtar`
- `page: comments`, `pageOrder: 20`, `category: Elements`
- Source: `component Note() { <p>Visible{/* hidden note */} text</p> }`
- Invoke: `Note()`

**render.golden output (verified):**
```
<p>Visible text</p>
```
Comment stripped, only `Visible text` remains. Confirmed `{/* hidden note */}` is dropped at parse time.

**Generated partial:** `docs/guide/syntax/_generated/comments/020-content-comment.md`
Note: `gen.Format` (used by the example generator) normalises formatted source via parse→print, which drops the braced comment from the displayed gsx source (because the parser consumes it before printing). The prose section therefore includes an explicit `gsx` code fence showing the `{/* … */}` syntax, followed by the generated partial (which confirms the rendered output). This is the correct pattern when the formatter itself elides the feature being documented.

**Comments page updated:** `docs/guide/syntax/comments.md` — new subsection:
```
## Content comments `{/* … */}`
```
- Prose explains `{/* … */}` is parsed and dropped (unlike `<!-- -->`).
- Code fence (wrapped in `::: v-pre`) shows the syntax with the comment visible.
- Explains `{// … }` line-form works identically.
- Includes `<!--@include: ./_generated/comments/020-content-comment.md-->`.
- Closing paragraph distinguishes content comments (markup layer) from `//` Go comments (outside markup).

**Hub cell (`syntax.md:35`):** Already reads "Content comments `{/* … */}` and HTML comments" — no change needed.

---

## Fix 2 — Makefile comment on omitted `docs/guide/examples.md`

**Applied.** Added two `#` comment lines above the `ci-examples` `git diff` command explaining that `docs/guide/examples.md` is intentionally omitted — the flat gallery page is retired; all examples are routed into the Syntax pages.

---

## Fix 3 — CLAUDE.md v-pre warning

**Applied.** Added one line immediately after the VitePress `docs` job note:
> Literal `{{ }}` in `docs/guide/**` prose must be wrapped in a `::: v-pre` block — VitePress parses `{{ }}` as a Vue interpolation and the build fails otherwise.

---

## Verification

- No literal `{{ }}` introduced into prose outside v-pre/code fences in `comments.md`.
  (Both occurrences of `{{ }}` in the file are inside backtick inline code spans, which VitePress treats as `<code>` and does not process as Vue template.)
- `<!--@include: ./_generated/comments/020-content-comment.md-->` resolves to a real generated file.
- `go build ./...` passes.
- `go test ./internal/corpus -run TestExamples` passes.

---

## Concerns

None. The rendered example not showing the `{/* … */}` comment in the gsx source block is expected behaviour — `gen.Format` normalises out parse-time-dropped constructs. The explicit prose code fence compensates.
