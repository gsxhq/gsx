# Phase 2 — minify js/css attribute-value literals (implementation plan)

**Goal:** minify `js"…"`/`css`…`` attribute-value bodies in generated output, gated by the existing `MinifyLevel`. Full level fully minifies JS (line breaks + whitespace + safe local mangling; object keys/globals preserved) via a fragment cascade, including holey values via a sentinel round-trip. Verified behavior-preserving on all 73 real one-learning-gsx `x-data` (0 failures, 35% smaller on landing, identical runtime trace).

**Design of record:** the Phase 2 section of
`docs/superpowers/specs/2026-07-14-embedded-attr-literal-format-minify-design.md`.

## Global constraints
- Go 1.26.1; runtime std-lib-only (all work in `internal/**` + `gen`).
- Behavior preservation is the gate: minify must not change what the JS/CSS does. Full JS minify uses tdewolff (`fullmin`) — a real parser; the safe level keeps newlines.
- JS attribute values are FRAGMENTS: `fullmin.JS` parses as a program, so an object literal must be wrapped `( … )` to parse. Cascade: `ext(raw)` → `ext("("+raw+")")` → `minifyJS` (safe, never errors).
- Holey JS attrs (full level): sentinel round-trip with a collision-free VALID-IDENTIFIER sentinel (free identifier → never mangled). Safe level: holey left unminified (matches holey `<script>`).
- No source-delimiter escaping at minify (codegen emits the value as a Go string literal). No new config surface.
- CSS attrs: reuse `minifyStyleChildren` on `Segments`.

## Files
- `internal/jsmin/file.go` — add attr walking (`minifyAttrs`) + `*ast.EmbeddedAttr` (JS) case + the fragment cascade + holey sentinel round-trip.
- `internal/cssmin/file.go` — add `*ast.EmbeddedAttr` (CSS) case to the existing `minifyAttrs`.
- Tests: `internal/jsmin/file_test.go`, `internal/cssmin/file_test.go`.
- Corpus: `internal/corpus/testdata/cases/**` (semantic: input.gsx + generated.x.go.golden + render.golden), proving minified output + still-correct render.

---

### Task 1: jsmin fragment cascade + holeless JS attr
- [ ] Failing test (`internal/jsmin/file_test.go`): a `<div x-data=js"{ open: false, active: -1 }"/>` component, `MinifyFile` with `ext=fullmin.JS` → the EmbeddedAttr `Segments` becomes a single `Text` whose value is `({open:!1,active:-1})` (or equivalent, no newlines). With `ext=nil` → `minifyJS` result (newlines kept). Assert object keys preserved.
- [ ] Implement:
  - `cascadeJS(text string, ext func(string)(string,error)) string`: `if ext!=nil { if o,e:=ext(text);e==nil {return o}; if o,e:=ext("("+text+")");e==nil {return o} }; return minifyJS(text)`.
  - In `minifyMarkup`, after handling children, call a new `minifyJSAttrs(v.Attrs, ext)` for every `*ast.Element` (script branch too).
  - `minifyJSAttrs`: for `*ast.EmbeddedAttr` where `Lang==ast.EmbeddedJS` → `minifyJSEmbedded(v, ext)`; for `*ast.MarkupAttr` recurse `minifyMarkup(v.Value, ext)`; for `*ast.CondAttr` recurse `minifyJSAttrs(v.Then/Else, ext)`.
  - `minifyJSEmbedded`: holeless (all `*ast.Text`) → `min := cascadeJS(concatText, ext)`; if `min==""` leave unchanged else `v.Segments = []ast.Markup{&ast.Text{Value: min}}`. (Holey handled in Task 2.)
- [ ] Run, verify pass; full `go test ./internal/jsmin/`.
- [ ] Commit `feat(jsmin): minify holeless js attribute values (fragment cascade)`.

### Task 2: holey JS attr via sentinel round-trip
- [ ] Failing test: `<div x-data=js"{ id: @{id}, k: 1 }"/>` (a `*ast.Interp` hole), `ext=fullmin.JS` → minified `Segments` = `Text("({id:")`, `Interp(id)`, `Text(",k:1})")` (holes preserved, structure minified, no `__gsxhole` leak). `ext=nil` → left unchanged (holey safe policy).
- [ ] Implement in `minifyJSEmbedded`:
  - detect holey (any `*ast.Interp` in Segments).
  - `ext==nil` && holey → return unchanged.
  - `ext!=nil` && holey → sentinel round-trip:
    - choose a collision-free prefix absent from all Text segments (append until absent, à la `rawfmt.buildPlaceholdered`); `sentinel(i) = prefix + itoa(i) + "q"` (valid identifier, unambiguous — scan `prefix` then digits then non-digit).
    - build one string: Text values verbatim + a sentinel per Interp.
    - `min := cascadeJS(sentineled, ext)`.
    - `splitJSSentinels(min, prefix, interps) []ast.Markup` → rebuild `Text`/`Interp`; on any missing/duplicated sentinel, bail (return original Segments unchanged).
    - assign `v.Segments`.
- [ ] Run; full `go test ./internal/jsmin/`.
- [ ] Commit `feat(jsmin): minify holey js attribute values via sentinel round-trip`.

### Task 3: cssmin css attr case
- [ ] Failing test (`internal/cssmin/file_test.go`): `<div style=css`color: red;\n  margin: 0;`/>` → `MinifyFile` → EmbeddedAttr `Segments` minified (declaration list compacted). Holey css attr → sentinel path (reuse). 
- [ ] Implement: in `cssmin`'s `minifyAttrs`, add `case *ast.EmbeddedAttr:` — `if v.Lang==ast.EmbeddedCSS { mc,err := minifyStyleChildren(v.Segments, ext); if err!=nil {return err}; if mc!=nil { v.Segments = mc } }`.
- [ ] Run; full `go test ./internal/cssmin/`.
- [ ] Commit `feat(cssmin): minify css attribute values`.

### Task 4: semantic corpus + full verification
- [ ] Add `internal/corpus` cases (input.gsx + goldens via `-update`): a `js"{…}"` x-data minified (full level); a holey `js"{ id: @{…} }"` minified with hole preserved; a `css`…`` attr minified; render still correct. Confirm the corpus harness minify level is exercised (check how corpus sets minify; if it defaults to safe-on, add a full-level case or drive via config).
- [ ] `make check` clean.
- [ ] Behavioral re-verify: regenerate an example that uses a minified x-data and confirm render.golden is the minified string.
- [ ] Commit `test(minify): corpus for js/css attribute-value minification`.

## Self-review
- Cascade order raw→wrap→safe; wrap kept in output (valid expression). ✓
- Holey: sentinel is a free identifier (unmangled); bail-to-original on split mismatch. ✓
- Safe level: holeless minifyJS, holey unchanged. ✓
- CSS reuses minifyStyleChildren. ✓
- `<script>`/`<style>` body minify unchanged (cascade is attr-only). ✓
- No new config; gated by MinifyLevel. ✓
