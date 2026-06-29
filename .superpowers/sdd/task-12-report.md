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
