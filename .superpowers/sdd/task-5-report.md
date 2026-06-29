# Task 5 Report — `interpolation` page

**Status:** COMPLETE

**Commit:** f16c68c — "docs(syntax): interpolation page"

**Tests:** `go test ./internal/corpus -run TestExamples -count=1` PASS; `make ci-examples` exits 0 (no drift).

**Included partials:**
- `docs/guide/syntax/_generated/interpolation/010-interpolation-props.md`
- `docs/guide/syntax/_generated/interpolation/020-field-access.md`
- `docs/guide/syntax/_generated/interpolation/030-function-t-error-unwrap.md`

**Fixtures:**
- `examples/10-interpolation.txtar` — added `page: interpolation` + `pageOrder: 10`
- `examples/220-field-access.txtar` — new; adapted from `interpolation/field_access.txtar` (struct field access)
- `examples/221-func-error-unwrap.txtar` — new; adapted from `interpolation/probe_multi_value_expr.txtar` (function returning `(T, error)` auto-unwraps)

**Concerns / notes:**
- The `(T, error)` unwrap example (`221`) doesn't include helpers.go in the Playground link (the generator only encodes `input.gsx`); the Playground link will work only if `lookup` is defined in the playground session. This is a pre-existing limitation of the playground — the example is correct as a docs illustration.
- The "Numeric & string contexts" section links to `../syntax#escaping-safe-contexts` (the existing `syntax.md` overview). When a dedicated `auto-escaping.md` page is created (future task), this link should be updated.
- `make ci-examples` requires committed state before it passes (it uses `git diff` against HEAD). Correct workflow: generate → commit → then verify. This matches how other tasks have been done.
