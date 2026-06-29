# Task 12 Report — `pipelines` page

**Status:** COMPLETE

**Commit:** `7e39295` — "docs(syntax): pipelines page — filters, (T, error) auto-unwrap, per-context escaping"

**Tests & drift:** `go test ./internal/corpus -run TestExamples` PASS; `make ci-examples` exit 0 (no drift).

**Included partials:**
- `docs/guide/syntax/_generated/pipelines/010-pipelines-filters.md`
- `docs/guide/syntax/_generated/pipelines/020-pipeline-t-error-unwrap.md`
- `docs/guide/syntax/_generated/pipelines/030-pipelines-in-attribute-context.md`

**Accuracy notes:**
- The `try` in the task brief does NOT refer to a `try` keyword or stage operator. gsx **auto-unwraps** `(T, error)` returns in all interpolation contexts (text, attribute, pipeline-lowered expr). The `?` try-marker syntax is explicitly rejected with a clear error (`try_rejected.txtar`, `attr_try_stage_rejected.txtar`). The fixture `280-pipeline-try.txtar` demonstrates auto-unwrap using a locally-defined `func greet(name string) (string, error)` in `input.gsx` — exactly as in the corpus source case `pipelines/try_unwrap.txtar`, adapted to single-file.
- `281-pipeline-contexts.txtar` adapts `pipelines/attr_url.txtar`: `{ u |> trim }` in an `href` attribute produces `about:invalid#gsx` for `javascript:alert(1)` input, demonstrating that URL sanitization is applied after pipeline filtering.
- `150-pipelines.txtar` had `page:`/`pageOrder:` added; the "Transforming values" section was removed from `docs/guide/examples.md` (fixture is now routed to the syntax page, not the general examples page). Two new fixtures (280, 281) appear in `docs/examples.json` and `playground/server/examples.json`.

**Concerns:** None.

---

## Addendum — review fix (filter-arguments example, prose corrections)

**Status:** COMPLETE

**Commit:** `461b599` — "docs(syntax): pipelines — replace try-unwrap fixture with filter-arguments example"

**What was fixed:**
1. Deleted `examples/280-pipeline-try.txtar` (bare `(T,error)` call, no `|>` — duplicated `221-func-error-unwrap`).
2. Created `examples/280-filter-arguments.txtar` (pageOrder 20, "Filter arguments", "Transforming values"): shows `{ s |> trim |> truncate(5) }` and `{ count |> format("%d comments") }` — both use `|>` with parenthesised arguments.
3. Retitled the middle page subsection from "`(T, error)` auto-unwrap" to "Filter arguments"; pointed its `<!--@include-->` at the new `020-filter-arguments.md` partial.
4. Replaced the old runnable `(T, error)` subsection with a short prose cross-link to `./interpolation`.
5. Changed "will produce a compile error" → "gsx reports an error" (it is a gsx diagnostic, not a Go compiler error, per `pipelines/try_rejected.txtar`).
6. Regenerated `docs/examples.json`, `playground/server/examples.json`, and `_generated/pipelines/020-filter-arguments.md` via `make examples`.

**Tests & drift:**
- `go test ./internal/corpus -run TestExamples` (no `-update`) → PASS
- `make ci-examples` → exit 0, no drift

**New partial:** `docs/guide/syntax/_generated/pipelines/020-filter-arguments.md` — shows `{ s |> trim |> truncate(5) }` and `{ count |> format("%d comments") }` with rendered output.
