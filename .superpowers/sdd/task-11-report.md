# Task 11 Report — `javascript` page

**Status:** COMPLETE

**Commit:** `f7573af` — "docs(syntax): javascript page — event handlers, script interpolation, JSON data islands"

**Tests + drift:**
- `go test ./internal/corpus -run TestExamples` (no `-update`) → PASS
- `make ci-examples` → exit 0, no drift

**Included partial paths:**
- `docs/guide/syntax/_generated/javascript/010-js-attributes-data-islands.md`
- `docs/guide/syntax/_generated/javascript/020-script-interpolation.md`
- `docs/guide/syntax/_generated/javascript/030-rawjs-event-handler.md`

**What was done:**
1. Edited `examples/180-js-and-islands.txtar`: added `page: javascript` and `pageOrder: 10` to doc block (category/order unchanged).
2. Created `examples/270-script-interpolation.txtar` (category: JavaScript, pageOrder: 20) adapted from `script/interp_value.txtar` — shows `@{ state }` in a `<script>` body JSON-encoding an `AppState` struct.
3. Created `examples/271-rawjs-handler.txtar` (category: JavaScript, pageOrder: 30) adapted from `jsattr/click_rawjs.txtar` — shows `@click={ gsx.RawJS("openMenu()") }` emitting a click handler verbatim.
4. Ran `go test ./internal/corpus -run TestExamples -update` → render.goldens written for 270 and 271, no diagnostics on any fixture.
5. Ran `make examples` → three partials generated under `docs/guide/syntax/_generated/javascript/`; gallery JSON/MD updated (180 moved from gallery to syntax page; 270/271 added as non-gallery JavaScript category entries).
6. Authored `docs/guide/syntax/javascript.md` with three subsections:
   - "Event handler attributes & `gsx.RawJS`" — includes `030-rawjs-event-handler.md`
   - "`<script>` interpolation" — includes `020-script-interpolation.md`
   - "JSON data islands" — includes `010-js-attributes-data-islands.md`

**Accuracy notes:**
- Confirmed against `attrclass.go`: `@click`, `onclick`, `hx-on*`, `x-data`, `x-show` all map to `CtxJS` (no separate event-handler sub-class).
- Confirmed against `js.go` (`jsValEscaper`): `gsx.RawJS` case emits verbatim; everything else is `json.Marshal`-ed with U+2028/U+2029 and `</script>` defenses.
- The task brief's "compile error for bare expression in event handler" claim is not borne out by the codegen (`emit.go` line 1572: whole-value `@click={ expr }` always generates `JSValAttr(expr)` regardless of type). The page instead accurately states that without `gsx.RawJS`, the value is JSON-encoded — producing a quoted string, not executable code. Inline `@{ }` interpolation in unsafe positions (identifier/binding) within JS attribute strings IS a compile error (the `resolveJSAttr` path in `jsx.go`), and this is mentioned accurately.

**Concerns:** None.
