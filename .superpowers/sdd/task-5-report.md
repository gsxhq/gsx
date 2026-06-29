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

---

## Post-review fixes (commit a94d3d1)

**Status:** COMPLETE

**Commit:** a94d3d1 — "fix(docs): accurate hx-* URL context + self-contained 221 example"

**Fix 1 — interpolation.md `hx-*` URL-context claim:** Line 47 previously read `` `hx-*` `` implying all htmx attributes are URL context. Corrected to enumerate only the URL-context attrs (`hx-get`/`hx-post`/`hx-put`/`hx-delete`/`hx-patch`) and note that `hx-on*` is JS context and other `hx-*` attrs are plain text.

**Fix 2 — 221 Playground link broken:** Moved `lookup()` from `-- helpers.go --` into `-- input.gsx --` so the Playground link encodes a self-contained, compilable example. Deleted `-- helpers.go --` section. Regenerated `docs/guide/syntax/_generated/interpolation/030-function-t-error-unwrap.md`, `docs/examples.json`, and `playground/server/examples.json`.

**Verification:** `go test ./internal/corpus -run TestExamples` PASS; `make ci-examples` exits 0 (no drift); `030-function-t-error-unwrap.md` now shows `func lookup(k string) (string, error) { return k, nil }` in its gsx block.
