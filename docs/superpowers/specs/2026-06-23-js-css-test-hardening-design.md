# JS & CSS Interpolation — Test-Hardening Increment Design

**Date:** 2026-06-23
**Status:** Approved (brainstorm), pending implementation plan
**Basis:** Read-only audit of the JS & CSS interpolation test coverage (`/tmp/audit-js.md`, `/tmp/audit-css.md`, summarized below). This increment closes the gaps that audit found.

---

## 1. Goal & scope

JS and CSS interpolation are now feature-complete. The audit found the *escapers* and *differential oracles* are well covered and honest, but coverage is thin in two places: (a) the **codegen emit-path selection** — which escaper/method codegen wires for each context — and (b) a set of **escaper-internal decode branches** and **minification branches** that no test traverses. Several "gaps" are untested-but-still-defended (the filter catches the input; only the test branch is missing); a few are genuine 0%-coverage correctness paths.

This increment adds tests at the layer where each regression would actually surface, and removes one piece of test brittleness the audit exposed.

**In scope (all audit findings except the deferred position work):**
- JS escaper end-to-end *selection* with hostile values (script-body `JSStr`/`JSTmpl`/`JSRegexp` + the `*Attr` variants).
- `gsx.RawJS` passthrough end-to-end in a `<script>` body.
- JS `(T, error)` tuple-unwrap in script + attr contexts.
- `emitClassAttr` (composed `class={}` on a non-root element) — currently 0%.
- CSS decode branches: uppercase hex, whitespace-after-hex (tab/nl/cr/crlf), trailing backslash, `> utf8.MaxRune` clamp.
- CSS wiring/weak-golden: pin `FilterCSS` emission in `composed_injection`; add an `@import` corpus render case.
- CSS minification branches: `<style>` inside `ForMarkup`/`SwitchMarkup`/`Fragment`, and a `CondAttr` style slot.
- Test-infra: normalize the volatile diagnostic byte offset in the corpus harness.

**Out of scope / deferred (unchanged):**
- Real `line:col` codegen-diagnostic positions (position-threading increment; tracked in `codegen-diagnostic-position-audit.md`). This increment only *de-brittles* the offset, it does not thread real positions.
- R4 document-level differential render; R5 security payload×context generator.

**Global constraint:** **Test and test-infra changes only — no production codegen/runtime changes.** The corpus harness offset-normalization lives in `internal/corpus` test support, not in production codegen. If a new test *surfaces a real bug*, stop and surface it (as the P0 NUL-byte finding was) rather than papering over it.

**Anti-recommendations preserved (do NOT change):** structural HTML comparison, single-batch corpus render, no `go.work`. We *do* add `generated.x.go.golden` to a small number of cases where it pins security-relevant emission — a deliberate, targeted expansion of the curated gen-golden set, not a blanket one.

**Success criteria:**
- `make cover` shows the previously-cold branches now covered: `emitClassAttr` 0→100%; the JS script/attr string-context emit branches (`emitJSValue`/`emitJSAttrValue`) materially up; `hexDecode`/`skipCSSSpace`/`decodeCSS` to ~100% (minus the one unreachable `panic`); `minifyMarkup` For/Switch/Fragment and `minifyAttrs` CondAttr covered.
- Every new security golden pins the safety-relevant output non-vacuously (a hostile value, never a benign one like the old `"42"`).
- Adding the new corpus cases no longer churns the two `*_identifier_rejected` diagnostics goldens.
- Whole suite green; `go vet ./...` clean; all fuzzers still pass a short run.

---

## 2. Test-infra: normalize the volatile diagnostic offset

**Finding.** The JS-hole rejection diagnostic reads `jsx: @{ } at 12970 here is not a safe JavaScript value position …`. That `12970` is `h.interp.Pos()` (`internal/jsx/jsx.go:391`) rendered as a raw byte offset. In the corpus it is an offset into the **batched** `go/packages` buffer (all cases loaded together), so it is not even an offset into the user's `.gsx` file, conveys nothing, and **shifts whenever any unrelated case is added ahead of it** (the audit shifted it from `12970`→`13115` by adding cases). It cannot be a stable golden.

**Decision (chosen).** Normalize the volatile offset away in the corpus diagnostics comparison — a test-infra change, not a production change. Real `line:col` is the deferred position-threading increment's job; this increment just stops the churn.

**Where:** `internal/corpus/corpus_test.go`, the diagnostics path. `diagGot` is resolved at lines 89–101 and compared/written via `checkOrUpdateFacet(t, c, "diagnostics.golden", …)` at line 106.

**How:** add a small helper, e.g.

```go
// normalizeDiag canonicalizes volatile byte offsets in diagnostics so that
// adding unrelated corpus cases (which shifts the batched-buffer offset) does
// not churn unrelated goldens. The real fix — line:col positions — is the
// deferred codegen-diagnostic-position increment; until then the offset carries
// no stable information and is replaced with a placeholder.
//   "jsx: @{ } at 12970 here is not…"  →  "jsx: @{ } at N here is not…"
var diagOffsetRe = regexp.MustCompile(`\bat \d+\b`)
func normalizeDiag(b []byte) []byte { return diagOffsetRe.ReplaceAll(b, []byte("at N")) }
```

Apply it symmetrically for the `diagnostics.golden` facet only: normalize the computed `got` before both compare and `-update` write, and normalize the stored golden before compare, so the comparison is self-healing regardless of whether a golden on disk still has a real number. Re-baseline the two existing goldens once with `-update`.

**Pitfall.** Scope the regex to `at \d+` (the diagnostic's exact phrasing) so it cannot accidentally mask a digit that is part of a real, meaningful message. Confirm no other diagnostic uses `at <number>` to mean something a test must pin; if one does, tighten the pattern (e.g. anchor to the `@{ }` diagnostic prefix). Verify by running the full corpus before/after: only the two `*_identifier_rejected` goldens should change, and only their number → `N`.

---

## 3. JS — codegen emit-path selection (audit #2)

**Finding.** `emit.go` `emitJSValue` (≈57%) / `emitJSAttrValue` (≈43%): the branches that select `JSStr`/`JSTmpl`/`JSRegexp` (and `*Attr`) are cold at the corpus level. The one existing string case (`script/interp_string`, value `"42"`) uses a value with no characters that need escaping, so a wrong escaper or a no-op escaper would not be caught.

**Plan.** Add corpus cases that interpolate a **hostile** string into each JS string-ish context, pinning both `generated.x.go.golden` (the emitted method) and `render.golden` (the escaped output). Contexts and minimal shapes:

- **Script-body string** — `<script>const s = "@{ v }";</script>` → emits `JSStr`.
- **Script-body template literal** — `` <script>const s = `@{ v }`;</script> `` → emits `JSTmpl`.
- **Script-body regexp** — `<script>const r = /@{ v }/;</script>` → emits `JSRegexp`.
- **JS-attr string** — e.g. `x-data="{ s: '@{ v }' }"` (single-quoted string context) → emits `JSStrAttr`.
- **JS-attr template / regexp** — analogous, → `JSTmplAttr` / `JSRegexpAttr`.

Hostile payload (one value reused): contains `"`, `'`, `` ` ``, `\`, `</script>`, a newline, and `U+2028`/`U+2029`. Each render golden must show the value safely escaped (e.g. `</script>`, ` `), not raw.

**Note.** Confirm against the parser/jsx classifier which exact surface syntax lands a hole in string vs template vs regexp context (`classifyHole`, `internal/jsx/jsx.go:361`); the implementer must verify the case actually reaches the intended `JSCtx*` before pinning the golden. If a context cannot be reached from real `.gsx` syntax, record that (it would mean the emit branch is dead) rather than forcing it.

---

## 4. JS — RawJS passthrough in `<script>` (audit #3)

**Finding.** `gsx.RawJS` passthrough has an attr case (`jsattr/click_rawjs`) but no `<script>`-body end-to-end case.

**Plan.** Add `script/rawjs_passthrough.txtar`: `<script>const fn = @{ gsx.RawJS("doThing()") };</script>` invoked so the render golden shows `doThing()` emitted verbatim (not JSON-encoded/quoted), and `generated.x.go.golden` pins the `JSVal(gsx.RawJS(...))` emission (runtime passthrough).

---

## 5. JS — `(T, error)` tuple-unwrap (audit #4)

**Finding.** The `@{ f() }` where `f` returns `(T, error)` path is untested in JS contexts; CSS has `style/block_tuple_error`.

**Plan.** Mirror the CSS case for JS in both contexts:
- `script/interp_tuple_error.txtar` — `<script>const d = @{ f() };</script>` where `f() (T, error)`.
- A JS-attr equivalent if the attr path has a distinct tuple-unwrap branch (verify in `emit.go`; if it shares the script branch, one case suffices and the second is noted as redundant rather than added).

Pin whatever the established convention is (an error diagnostic if the tuple form is rejected, or the unwrapped render if it is supported) — match the existing CSS `block_tuple_error` behavior. The implementer verifies actual behavior before pinning.

---

## 6. JS — `emitClassAttr` non-root composed class (audit #5)

**Finding.** `emit.go` `emitClassAttr` is 0%. Every existing `class={}` corpus case has a single root element, so they all route through `emitRootComposedClass`; the non-root path is dead in tests. A correctness gap (wrong class merging would ship silently).

**Plan.** Add `class/non_root_class.txtar`: a component whose body is a fragment with a non-root element carrying `class={ … }` (e.g. `<>...<div class={ cls }>x</div>...</>`), invoked so `generated.x.go.golden` pins the `_gsxgw.Class(...)` emission **without** the root `ClassMerged`/`Spread` wrapping, and `render.golden` pins the merged class output.

---

## 7. CSS — escaper decode branches (audit #6)

**Finding.** `hexDecode` uppercase A–F (≈78%), `skipCSSSpace` tab/nl/ff/cr/crlf (50%), `decodeCSS` trailing backslash + `> utf8.MaxRune` clamp (87%) are untraversed. The filter still rejects these inputs (the *defense* holds) — only the branch coverage is missing.

**Plan.** Extend the escaper unit table and fuzz seeds (no new corpus cases needed; these are escaper-internal):
- `escape_test.go` `TestCSSValueFilter`: uppercase hex (`\3C script\3E` → `ZgotmplZ`); whitespace-after-hex variants (`-expre\0000073\tsion`, `…\ron`, `…\r\non` → `ZgotmplZ`); non-hex backslash escape (`foo\nbar`); trailing backslash (`foo\`); `> MaxRune` clamp (`\110000`).
- `fuzz_test.go` `FuzzCSSValueFilter`: add the same inputs as `f.Add` seeds.
- `escape_diff_test.go` `diffCorpus()`: add the same inputs so `FuzzEscaperMatchesStdlib` also seeds them (maintains html/template byte-parity over these vectors).

Each assertion states the expected output explicitly (`ZgotmplZ` or the exact safe decode). The unreachable `hexDecode` `panic` (gated by `isHex`) stays uncovered and is documented as defensive.

---

## 8. CSS — wiring & weak goldens (audit #7)

**Findings.** `style/composed_injection` pins the runtime `ZgotmplZ` but lacks `generated.x.go.golden`, so it does not pin that codegen emits `gsx.FilterCSS(...)` for the dynamic part. `@import` rejection is unit-only — no corpus render.

**Plan.**
- Add a `generated.x.go.golden` section to `style/composed_injection.txtar` pinning `gsx.FilterCSS("color: " + u)` (or the actual emitted form — verify).
- Add `style/block_import_injection.txtar`: `<style>@{ userColor }</style>` with `userColor = "@import url(evil.css)"`, render golden `ZgotmplZ`, `generated.x.go.golden` pinning the `FilterCSS`/`CSS` emission — exercising the full codegen→render pipeline for `@import`.

---

## 9. CSS — minification branches (audit #8)

**Finding.** `internal/cssmin` `minifyMarkup` `ForMarkup`/`SwitchMarkup`/`Fragment` branches (≈65%) and `minifyAttrs` `CondAttr` branch (≈67%) are untraversed: a `<style>` inside those nodes would not be minified. A minification miss, not a safety issue.

**Plan.** Add `internal/cssmin/file_test.go` cases (the natural layer — these are AST-walk branches):
- `<style>` inside a `for { … }` (`ForMarkup`).
- `<style>` inside a `switch`/case (`SwitchMarkup`).
- `<style>` inside a `<>…</>` fragment (`Fragment`).
- A conditional attribute whose slot contains a `<style>` (`CondAttr` in `minifyAttrs`).
Each asserts the style child is minified (or correctly walked) for that node type.

---

## 10. File structure

- Modify: `internal/corpus/corpus_test.go` — add `normalizeDiag` + apply in the diagnostics path (§2).
- Re-baseline: `internal/corpus/testdata/cases/{jsattr,script}/*_identifier_rejected.txtar` — one-time `-update` (§2).
- Create (corpus): `script/{string_hostile,tmpl_hostile,regexp_hostile,rawjs_passthrough,interp_tuple_error}.txtar`; `jsattr/{string_hostile,tmpl_hostile,regexp_hostile}.txtar` (names indicative); `class/non_root_class.txtar`; `style/block_import_injection.txtar` (§§3–6, 8).
- Modify (corpus): `style/composed_injection.txtar` — add `generated.x.go.golden` (§8).
- Modify (unit/fuzz): `escape_test.go`, `fuzz_test.go`, `escape_diff_test.go` (§7); `internal/cssmin/file_test.go` (§9).
- Regenerate: `internal/corpus/testdata/coverage.golden` via `-update` (reflects the new cases).

No production code changes. (A real-bug discovery is the only exception, and is surfaced, not silently fixed.)

---

## 11. Testing / verification

- Per area: the targeted branch is covered after the change (`make cover`; `go tool cover -func` filtered to the relevant function shows the before→after jump).
- Corpus: `go test ./internal/corpus -run TestCorpus` green; the only diagnostics goldens that change under `-update` are the two re-baselined ones (number→`N`), proving the normalization works.
- Each new security golden inspected to confirm it pins a hostile value's *safe* output (non-vacuous).
- Whole suite: `go test ./... -count=1` green; `go vet ./...` clean; `FuzzCSSValueFilter`, `FuzzEscaperMatchesStdlib`, `FuzzMinifyCSS`, `FuzzMinifyJS` each pass a short `-fuzztime` run with no divergence.
