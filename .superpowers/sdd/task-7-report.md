# Task 7 Report — `control-flow` page

**Status:** COMPLETE

**Commit:** `e72b7f7` — "docs(syntax): control-flow page"

**Tests + drift:**
- `go test ./internal/corpus -run TestExamples` (no `-update`) → PASS
- `make ci-examples` → exit 0, no drift

**Included partial paths:**
- `docs/guide/syntax/_generated/control-flow/010-if-else.md`
- `docs/guide/syntax/_generated/control-flow/020-loops-over-lists.md`
- `docs/guide/syntax/_generated/control-flow/030-switch.md`
- `docs/guide/syntax/_generated/control-flow/040-init-statement.md`

**What was done:**
1. Added `page: control-flow` + `pageOrder: 10/20/30` to `examples/40-if-else.txtar`, `50-loops.txtar`, `60-switch.txtar`.
2. Created `examples/240-init-statement.txtar` adapted from `control_flow/if_init_error_handling.txtar` — uses a `loadUser(id) (string, error)` helper with `{ if name, err := loadUser(id); err != nil { … } else { … } }`. Render golden: `<div><span>User:42</span></div>`.
3. Ran `go test ./internal/corpus -run TestExamples -update` → goldens written clean, no diagnostics.
4. Ran `make examples` → four partials generated under `docs/guide/syntax/_generated/control-flow/`. Side effect: the three existing control-flow examples were removed from `docs/guide/examples.md` (now routed to the dedicated page instead of the gallery, as expected by the generator).
5. Authored `docs/guide/syntax/control-flow.md` with four subsections: If / else, For / range, Switch, Init statements — each followed by its generated partial include.

**Concerns:** None. The removal of the "Control flow" section from `docs/guide/examples.md` is intentional: routed examples do not appear in the general gallery.
