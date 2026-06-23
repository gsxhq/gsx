# JS & CSS Interpolation Test-Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the JS/CSS interpolation test-coverage gaps the audit found, placing each test at the layer that catches its regression, and de-brittle one fragile diagnostic golden.

**Architecture:** Three layers — (1) corpus txtar render cases (`generated.x.go.golden` + `render.golden`) for codegen *emit-path selection / wiring* gaps; (2) escaper unit + fuzz-seed additions for escaper-*internal* decode branches; (3) `internal/cssmin` unit tests for AST-walk minification branches. Plus one test-infra change: the corpus diagnostics comparison normalizes a volatile byte offset.

**Tech Stack:** Go; the gsx corpus harness (`internal/corpus`, txtar fixtures, `-update` golden regeneration, presence-based facets, structural HTML compare); `go test -coverpkg=./...` via the root `Makefile`.

## Global Constraints

- **Test and test-infra changes ONLY — no production codegen/runtime changes.** The corpus offset-normalization lives in `internal/corpus` test code, not production codegen. If a new test *surfaces a real production bug*, STOP and surface it (as the P0 NUL-byte finding was) — do not silently fix or paper over it.
- Every new **security** golden must pin a **hostile** value's *safe* output non-vacuously — never a benign value (the old `script/interp_string` used `"42"`, which would not catch a wrong/no-op escaper).
- Preserve anti-recommendations: structural HTML comparison, single-batch corpus render, **no `go.work`**. Adding `generated.x.go.golden` to a few cases is a deliberate, targeted expansion of the curated gen-golden set — not a blanket one.
- Corpus goldens are **generated** by `go test ./internal/corpus -run TestCorpus -update`, then **verified** by reading them — you do not hand-author golden bytes. The plan gives each case's `input.gsx`, `invoke`, and the exact markers the generated golden MUST contain.
- **`generated.x.go.golden` is presence-based:** the harness only (re)writes a gen golden whose section marker already exists. So for EVERY new case where this plan verifies `generated.x.go.golden`, add an empty `-- generated.x.go.golden --` marker line at the end of the txtar (after `-- diagnostics.golden --`) BEFORE running `-update`. `render.golden` and `diagnostics.golden` are auto-created for renderable cases and need no pre-added marker. (This is why each new-case template below should have `-- generated.x.go.golden --` appended; the templates show the required input/invoke/diagnostics — append the gen marker per this rule.)
- Corpus case path convention: `internal/corpus/testdata/cases/<area>/<scenario>.txtar`. See `internal/corpus/README.md`.
- After every task: `go test ./... -count=1` green and `go vet ./...` clean before commit.

---

## Task 1: Normalize the volatile diagnostic byte offset (test-infra)

Foundational — do FIRST. Later tasks add corpus cases that shift the batched-buffer byte offset embedded in the two `*_identifier_rejected` diagnostics (`jsx: @{ } at 12970 …`). Normalizing it now means those goldens stop churning when cases are added.

**Files:**
- Modify: `internal/corpus/corpus_test.go` (imports; add `normalizeDiag`; apply in the diagnostics path)
- Re-baseline (via `-update`): `internal/corpus/testdata/cases/jsattr/jsattr_identifier_rejected.txtar`, `internal/corpus/testdata/cases/script/interp_identifier_rejected.txtar`

**Interfaces:**
- Produces: `normalizeDiag([]byte) []byte` (package `corpus`, test file) — replaces `at <digits>` with `at N`.

- [ ] **Step 1: Add the `regexp` import**

In `internal/corpus/corpus_test.go`, the import block currently is:
```go
import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)
```
Add `"regexp"` (keep the block sorted — it goes after `"path/filepath"`... actually alphabetical: `bytes, flag, io/fs, os, path/filepath, regexp, sort, strings, testing`). Insert `"regexp"` between `"path/filepath"` and `"sort"`.

- [ ] **Step 2: Add the `normalizeDiag` helper**

Add near the top of `corpus_test.go` (after the `update` flag var):
```go
// diagOffsetRe matches the volatile byte offset in JS-hole diagnostics
// ("jsx: @{ } at 12970 here is not…"). The number is h.interp.Pos() rendered as
// a raw offset into the BATCHED go/packages buffer (all corpus cases loaded
// together), so it is not an offset into the user's .gsx file, conveys nothing
// stable, and shifts whenever any unrelated case is added ahead of it. Real
// line:col positions are the deferred codegen-diagnostic-position increment's
// job; until then we replace the offset with a placeholder so it stops churning
// unrelated goldens.
var diagOffsetRe = regexp.MustCompile(`\bat \d+\b`)

// normalizeDiag canonicalizes the volatile byte offset in diagnostics so adding
// unrelated corpus cases does not churn unrelated goldens.
func normalizeDiag(b []byte) []byte { return diagOffsetRe.ReplaceAll(b, []byte("at N")) }
```

- [ ] **Step 3: Apply it symmetrically in the diagnostics facet path**

In `TestCorpus`, `diagGot` is resolved (around lines 89–101) and then passed to `checkOrUpdateFacet(t, c, "diagnostics.golden", diagGot, paths[c.name])` (around line 106). Normalize `diagGot` immediately before that call so both the `-update` write and the compare use the normalized form:
```go
		diagGot = normalizeDiag(diagGot)
		checkOrUpdateFacet(t, c, "diagnostics.golden", diagGot, paths[c.name])
```
Also make the comparison self-healing for goldens that may still hold a real number on disk: in `checkOrUpdateFacet`, for the diagnostics section only, normalize the stored golden before `bytes.Equal`. Change the final compare:
```go
	if !bytes.Equal(got, c.goldens[sec]) {
```
to:
```go
	want := c.goldens[sec]
	if sec == "diagnostics.golden" {
		want = normalizeDiag(want)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: %s mismatch\n--- got ---\n%s\n--- want ---\n%s", c.name, sec, got, want)
		return
	}
```
(Keep the existing `t.Errorf` body; the point is comparing against the normalized `want`. `got` is already normalized for diagnostics from Step 3.)

- [ ] **Step 4: Re-baseline the two goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then: `git diff --stat internal/corpus/testdata/cases`
Expected: ONLY `jsattr/jsattr_identifier_rejected.txtar` and `script/interp_identifier_rejected.txtar` change, each replacing `at <number>` with `at N` (e.g. `jsx: @{ } at N here is not a safe JavaScript value position …`). If any OTHER golden changes, STOP — the regex is too broad; tighten it (anchor to the `@{ }` diagnostic, e.g. require `@\{ \}.*\bat \d+`).

- [ ] **Step 5: Verify green without update**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS. Run `go test ./... -count=1` (green) and `go vet ./...` (clean).

- [ ] **Step 6: Commit**
```bash
git add internal/corpus/corpus_test.go internal/corpus/testdata/cases/jsattr/jsattr_identifier_rejected.txtar internal/corpus/testdata/cases/script/interp_identifier_rejected.txtar
git commit -m "test(corpus): normalize volatile diagnostic byte offset to stop golden churn"
```

---

## Task 2: JS `<script>`-body emit-path coverage (audit #2, #3, #4 — script half)

Five `<script>`-body corpus cases that interpolate **hostile** values so codegen's escaper *selection* (`JSStr`/`JSTmpl`/`JSRegexp`/`JSVal` + RawJS passthrough + `(T,error)` unwrap) is pinned by `generated.x.go.golden` AND the escaped output by `render.golden`. Today only `script/interp_string` (benign `"42"`) and `script/interp_value` exist; a wrong/no-op escaper would pass.

**Files (all Create):**
- `internal/corpus/testdata/cases/script/string_hostile.txtar`
- `internal/corpus/testdata/cases/script/tmpl_hostile.txtar`
- `internal/corpus/testdata/cases/script/regexp_hostile.txtar`
- `internal/corpus/testdata/cases/script/rawjs_passthrough.txtar`
- `internal/corpus/testdata/cases/script/value_tuple.txtar`
- Regenerate: `internal/corpus/testdata/coverage.golden`

**Interfaces (emit methods these cases must pin — from `internal/codegen/emit.go`):**
- `emitJSValue`: `JSCtxString→_gsxgw.JSStr(string(x))`, `JSCtxTemplate→_gsxgw.JSTmpl(...)`, `JSCtxRegexp→_gsxgw.JSRegexp(...)`, `JSCtxValue→_gsxgw.JSVal(x)`.
- `emitJSInterp` tuple unwrap: `_gsxv0, _gsxerr := f()\nif _gsxerr != nil {\n\treturn _gsxerr\n}` then `_gsxgw.JSVal(_gsxv0)`.

- [ ] **Step 1: Write the five txtar inputs**

Each file has `-- input.gsx --`, `-- invoke --`, and an empty `-- diagnostics.golden --` (the rest are generated). Use exactly:

`string_hostile.txtar`:
```
-- input.gsx --
package views

component Page(v string) {
	<script>const s = "x@{ v }";</script>
}
-- invoke --
Page(PageProps{V: "</script>\"'`\\\n end"})
-- diagnostics.golden --
```

`tmpl_hostile.txtar` (the `<script>` body uses a JS **template literal** — backtick-delimited; the file content below has literal backticks):
~~~
-- input.gsx --
package views

component Page(v string) {
	<script>const s = `x@{ v }`;</script>
}
-- invoke --
Page(PageProps{V: "${alert(1)}`\\</script>end"})
-- diagnostics.golden --
~~~

`regexp_hostile.txtar`:
```
-- input.gsx --
package views

component Page(v string) {
	<script>const r = /x@{ v }/;</script>
}
-- invoke --
Page(PageProps{V: "</script>/\\\nend"})
-- diagnostics.golden --
```

`rawjs_passthrough.txtar`:
```
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Page() {
	<script>const fn = @{ gsx.RawJS("doThing(1)") };</script>
}
-- invoke --
Page(PageProps{})
-- diagnostics.golden --
```

`value_tuple.txtar`:
```
-- input.gsx --
package views

component Page() {
	<script>const d = @{ load() };</script>
}

func load() (string, error) { return "ok", nil }
-- invoke --
Page(PageProps{})
-- diagnostics.golden --
```

- [ ] **Step 2: Generate goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Expected: no failure; new `render.golden` / `generated.x.go.golden` sections written into the five files, and the two new cases appear in `coverage.golden`.

- [ ] **Step 3: VERIFY each generated golden pins the right escaper + safe output**

Read each file and confirm:
- `string_hostile`: `generated.x.go.golden` contains `_gsxgw.JSStr(string(v))`. `render.golden` shows the value with `</script>` NOT raw (escaped to `</script>` or similar) and the `"` escaped — i.e. the script element is not breakable.
- `tmpl_hostile`: gen contains `_gsxgw.JSTmpl(string(v))`. render shows the backtick and `</script>` neutralized (not raw), and `${` not left as a live template-interpolation breakout.
- `regexp_hostile`: gen contains `_gsxgw.JSRegexp(string(v))`. render shows `</script>` and the unescaped `/` neutralized.
- `rawjs_passthrough`: gen contains `_gsxgw.JSVal(gsx.RawJS("doThing(1)"))`. render shows `const fn = doThing(1);` (verbatim, NOT JSON-quoted).
- `value_tuple`: gen contains `_gsxv0, _gsxerr := load()`, `if _gsxerr != nil`, and `_gsxgw.JSVal(_gsxv0)`. render shows `const d = "ok";`.

If any case does NOT reach the intended `JSCtx*` (e.g. the hole lands in value context instead of string), adjust the surrounding JS so the hole is a substring of the intended token (string literal / template literal / regexp), re-run `-update`, re-verify. The classifier is `internal/jsx/jsx.go` `classifyHole` (a hole that is a strict substring of a `StringToken`→string ctx, `TemplateToken`→template, `RegExpToken`→regexp). Do NOT change production code to force a context.

- [ ] **Step 4: Confirm green + coverage moved**

Run: `go test ./internal/corpus -run TestCorpus -count=1` → PASS.
Run: `make cover` then `go tool cover -func=cover.out | grep -E 'emit\.go:(741|761|713):'` → `emitJSValue` / `emitJSString` / `emitJSInterp` materially higher than the audit baselines (emitJSValue was ~57%).

- [ ] **Step 5: Commit**
```bash
git add internal/corpus/testdata/cases/script internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): JS <script> emit-path coverage — hostile JSStr/JSTmpl/JSRegexp + RawJS + tuple unwrap"
```

---

## Task 3: JS-attribute emit-path coverage (audit #2, #4 — attr half)

JS-context **attribute** equivalents of Task 2, exercising `emitJSAttrValue` (`JSStrAttr`/`JSTmplAttr`/`JSRegexpAttr`/`JSValAttr`) and `emitJSAttrInterp`'s tuple unwrap. These apply JS-escaping **and** HTML-attr-escaping, so the render goldens carry an extra entity layer (e.g. `&#34;`). `emitJSAttrValue` was ~43% in the audit.

**REACHABILITY NOTE:** unlike `<script>`, a JS *attribute* value is a single JS expression. The string context is reachable (`x-data="{ s: '@{ v }' }"`). Template and regexp attribute contexts MAY NOT be reachable from real `.gsx` syntax. For each, attempt the case; if `-update` shows the hole did not reach the intended context (gen golden calls `JSValAttr` instead of `JSTmplAttr`/`JSRegexpAttr`, or classification fails), DO NOT force it — instead record the finding in the task report ("`JSTmplAttr`/`JSRegexpAttr` emit branch is unreachable from current attribute syntax; dead branch — candidate for removal or a future syntax") and drop that case. The string + value-tuple cases are the required deliverable; template/regexp-attr are best-effort.

**Files (Create, subject to reachability):**
- `internal/corpus/testdata/cases/jsattr/string_hostile.txtar` (required)
- `internal/corpus/testdata/cases/jsattr/value_tuple.txtar` (required)
- `internal/corpus/testdata/cases/jsattr/tmpl_hostile.txtar` (best-effort)
- `internal/corpus/testdata/cases/jsattr/regexp_hostile.txtar` (best-effort)
- Regenerate: `internal/corpus/testdata/coverage.golden`

**Interfaces:** `emitJSAttrValue` → `JSStrAttr`/`JSTmplAttr`/`JSRegexpAttr`/`JSValAttr`; `emitJSAttrInterp` tuple unwrap mirrors `emitJSInterp`.

- [ ] **Step 1: Write the required two inputs**

`string_hostile.txtar`:
```
-- input.gsx --
package views

component Page(v string) {
	<div x-data="{ s: '@{ v }' }">x</div>
}
-- invoke --
Page(PageProps{V: "'\"</script><x> end"})
-- diagnostics.golden --
```

`value_tuple.txtar`:
```
-- input.gsx --
package views

component Page() {
	<div x-data="{ d: @{ load() } }">x</div>
}

func load() (string, error) { return "ok", nil }
-- invoke --
Page(PageProps{})
-- diagnostics.golden --
```

- [ ] **Step 2: Attempt the two best-effort inputs**

`tmpl_hostile.txtar` (template literal inside attr — backtick-delimited; literal backticks in the value below):
~~~
-- input.gsx --
package views

component Page(v string) {
	<div x-data="{ s: `@{ v }` }">x</div>
}
-- invoke --
Page(PageProps{V: "${x}`end"})
-- diagnostics.golden --
~~~

`regexp_hostile.txtar` (regexp inside attr):
```
-- input.gsx --
package views

component Page(v string) {
	<div x-data="{ r: /@{ v }/ }">x</div>
}
-- invoke --
Page(PageProps{V: "/\\end"})
-- diagnostics.golden --
```

- [ ] **Step 3: Generate + verify (and prune unreachable)**

Run: `go test ./internal/corpus -run TestCorpus -update`. Then read each generated file:
- `string_hostile`: gen contains `_gsxgw.JSStrAttr(string(v))`; render shows the value JS-escaped AND attr-escaped (the `'`, `"`, `<` neutralized; element not breakable).
- `value_tuple`: gen contains `_gsxv0, _gsxerr := load()` … `_gsxgw.JSValAttr(_gsxv0)`; render shows `d: ` followed by the JSON-encoded `"ok"` (attr-escaped → `&#34;ok&#34;`).
- `tmpl_hostile` / `regexp_hostile`: IF gen contains `_gsxgw.JSTmplAttr(...)` / `_gsxgw.JSRegexpAttr(...)` respectively, keep and verify the render neutralizes the dangerous chars. IF instead it shows `JSValAttr` or the case errors, DELETE that txtar (`rm -f`), re-run `-update`, and record the unreachable-branch finding in the report.

Do not modify production code to make a branch reachable.

- [ ] **Step 4: Confirm green + coverage**

`go test ./internal/corpus -run TestCorpus -count=1` → PASS. `make cover` then `go tool cover -func=cover.out | grep -E 'emit\.go:(884|858):'` → `emitJSAttrValue` / `emitJSAttrInterp` higher than baseline.

- [ ] **Step 5: Commit**
```bash
git add internal/corpus/testdata/cases/jsattr internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): JS attribute emit-path coverage — JSStrAttr + tuple unwrap (+ tmpl/regexp if reachable)"
```

---

## Task 4: `emitClassAttr` non-root composed class (audit #5)

`emit.go` `emitClassAttr` is 0% — every existing `class={}` case has a single root, which routes through `emitRootComposedClass` (root fallthrough path, line 322). A `class={}` on a NON-root element routes through `emitAttr → emitClassAttr` (line 792). A correctness gap: wrong non-root class merging would ship silently.

**Files:**
- Create: `internal/corpus/testdata/cases/class/non_root_class.txtar`
- Regenerate: `internal/corpus/testdata/coverage.golden`

**Interfaces:** `emitClassAttr` emits ` class="`, then `_gsxgw.Class(gsx.Class(<expr>), gsx.ClassIf(<expr>, <cond>) …)`, then `"` — WITHOUT the root's `ClassMerged`/`Spread`.

- [ ] **Step 1: Write the input (non-root element carries `class={}`)**
```
-- input.gsx --
package views

component C(extra string, on bool) {
	<div><span class={ "btn", extra, "active" if on }>y</span></div>
}
-- invoke --
C(CProps{Extra: "x", On: true})
-- diagnostics.golden --
```
(The outer `<div>` is the single root and gets fallthrough handling; the inner `<span>` is non-root, so its `class={}` exercises `emitClassAttr`. The `"active" if on` part exercises the `ClassIf` branch too.)

- [ ] **Step 2: Generate**

Run: `go test ./internal/corpus -run TestCorpus -update`.

- [ ] **Step 3: Verify the generated goldens**

Read `non_root_class.txtar`:
- `generated.x.go.golden` contains, for the `<span>`: `_gsxgw.S(" class=\"")`, then `_gsxgw.Class(gsx.Class("btn"), gsx.Class(extra), gsx.ClassIf("active", on))`, then `_gsxgw.S("\"")`. Confirm there is NO `ClassMerged`/`Spread` wrapping the span's class (those belong only to the root `<div>`).
- `render.golden`: `<div><span class="btn x active">y</span></div>`.

- [ ] **Step 4: Confirm green + coverage**

`go test ./internal/corpus -run TestCorpus -count=1` → PASS. `make cover` then `go tool cover -func=cover.out | grep 'emit.go:905'` → `emitClassAttr` now 100% (was 0%).

- [ ] **Step 5: Commit**
```bash
git add internal/corpus/testdata/cases/class/non_root_class.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): non-root composed class={} exercises emitClassAttr (was 0%)"
```

---

## Task 5: CSS escaper decode-branch coverage (audit #6)

Traverse the cold decode branches: `hexDecode` uppercase A–F, `skipCSSSpace` tab/newline/form-feed/CR/CRLF, `decodeCSS` trailing backslash + `> utf8.MaxRune` clamp. The filter still *defends* these inputs; only branch coverage is missing. These are escaper-internal — unit table + fuzz seeds, no corpus cases.

**Files:**
- Modify: `escape_test.go` (`TestCSSValueFilter` table)
- Modify: `fuzz_test.go` (`FuzzCSSValueFilter` seeds)
- Modify: `escape_diff_test.go` (`diffCorpus()` CSS inputs)

**Interfaces:** `cssValueFilter(string) string` → `"ZgotmplZ"` for unsafe; `FilterCSS` is its exported alias; both fuzzers already assert the no-breakout-byte invariant.

- [ ] **Step 1: Add unit-table rows**

In `escape_test.go` `TestCSSValueFilter`, the table uses raw test cases `{css, want}`. Note CSS escapes need a LITERAL backslash, so use double-quoted Go strings with `\\` (e.g. `"\\3C"` is the 3-char CSS escape `\3C`). Add these rows (each decodes to a rejected keyword/char, so `want` is `"ZgotmplZ"` with high confidence — except the last two, see Step 2):
```go
		{"\\3C script\\3E", "ZgotmplZ"},   // uppercase hex \3C \3E -> "<script>" (hexDecode A-F)
		{"expr\\65\tssion", "ZgotmplZ"},    // tab after \65 -> "expression" (skipCSSSpace \t)
		{"expr\\65\nssion", "ZgotmplZ"},    // newline (skipCSSSpace \n)
		{"expr\\65\fssion", "ZgotmplZ"},    // form feed (skipCSSSpace \f)
		{"expr\\65\rssion", "ZgotmplZ"},    // CR (skipCSSSpace \r)
		{"expr\\65\r\nssion", "ZgotmplZ"},  // CRLF (skipCSSSpace \r\n two-byte branch)
```

- [ ] **Step 2: Add the trailing-backslash + MaxRune rows (verify-by-running)**

These decode to a *safe* value, so `want` is the decoded string, not `ZgotmplZ`. Add with best-estimate, then run and pin the actual:
```go
		{"foo\\", "foo"},          // trailing lone backslash dropped (decodeCSS len<2 break)
		{"\\110000", "�"},     // hex > utf8.MaxRune clamps to U+FFFD (decodeCSS clamp)
```
Run `go test . -run TestCSSValueFilter -v`. If either `want` mismatches, read the actual output, CONFIRM it is safe (contains no CSS breakout byte — that is the binding security property) and a sensible decode, then pin the actual observed value. Do NOT change production code.

- [ ] **Step 3: Run the unit test**

Run: `go test . -run TestCSSValueFilter -count=1`
Expected: PASS (after pinning actuals in Step 2).

- [ ] **Step 4: Add the same inputs as fuzz seeds**

In `fuzz_test.go` `FuzzCSSValueFilter`, append to the seed slice:
```go
		"\\3C script\\3E", "expr\\65\tssion", "expr\\65\nssion",
		"expr\\65\fssion", "expr\\65\rssion", "expr\\65\r\nssion",
		"foo\\", "\\110000",
```
And in `escape_diff_test.go` `diffCorpus()`, add the same eight strings to the CSS input set (so `FuzzEscaperMatchesStdlib` also seeds them, maintaining html/template byte-parity over these vectors).

- [ ] **Step 5: Run fuzzers briefly + full suite**

Run: `go test . -run x -fuzz FuzzCSSValueFilter -fuzztime=10s` → no failures.
Run: `go test . -run x -fuzz FuzzEscaperMatchesStdlib -fuzztime=10s` → no failures.
Run: `go test ./... -count=1` (green), `go vet ./...` (clean).
Run: `make cover` then `go tool cover -func=cover.out | grep -E 'escape\.go:(107|145|163):'` → `decodeCSS`/`hexDecode`/`skipCSSSpace` near 100% (the `hexDecode` panic stays uncovered — defensive, gated by `isHex`).

- [ ] **Step 6: Commit**
```bash
git add escape_test.go fuzz_test.go escape_diff_test.go
git commit -m "test: cover CSS decode branches (uppercase hex, ws-after-hex, trailing backslash, MaxRune clamp)"
```

---

## Task 6: CSS wiring & weak-golden fixes (audit #7)

Pin that codegen emits `gsx.FilterCSS(...)` for a dynamic style part (`composed_injection` currently has no gen golden), and add an `@import` injection corpus case exercising the full codegen→render pipeline (today `@import` is unit-only).

**Files:**
- Modify: `internal/corpus/testdata/cases/style/composed_injection.txtar` (add `generated.x.go.golden`)
- Create: `internal/corpus/testdata/cases/style/block_import_injection.txtar`
- Regenerate: `internal/corpus/testdata/coverage.golden`

**Interfaces:** `emitStyleAttr` emits `_gsxgw.Style(gsx.Class(gsx.FilterCSS(<dynamic expr>)))` for a dynamic style part; `emitCSSInterp` emits `_gsxgw.CSS(string(<expr>))` for a `<style>`-block hole.

- [ ] **Step 1: Add a gen golden to `composed_injection`**

The existing file (do not change `input.gsx`/`invoke`/`render.golden`/`diagnostics.golden`) is:
```
-- input.gsx --
package views

component Evil(u string) {
	<span style={ "color: " + u }>x</span>
}
-- invoke --
Evil(EvilProps{U: "red; pointer-events: none; background: url(javascript:alert(1))"})
-- render.golden --
<span style="ZgotmplZ">x</span>
-- diagnostics.golden --
```
Run `go test ./internal/corpus -run TestCorpus -update` (this writes a `generated.x.go.golden` section into the archive only if the section already exists — so it does NOT auto-add). Therefore FIRST append an empty section marker to the file so `-update` will populate it: add a trailing line `-- generated.x.go.golden --` to `composed_injection.txtar`, then run `-update`.

- [ ] **Step 2: Write the `@import` block case**

Create `block_import_injection.txtar`:
```
-- input.gsx --
package views

component Page(userColor string) {
	<style>.a{color:@{ userColor }}</style>
}
-- invoke --
Page(PageProps{UserColor: "@import url(evil.css)"})
-- diagnostics.golden --
```
(Mirrors `style/block_tuple_error` shape; the `@import` value must be rejected by the CSS filter at render.)

- [ ] **Step 3: Generate + verify**

Run: `go test ./internal/corpus -run TestCorpus -update`. Then verify:
- `composed_injection.txtar` `generated.x.go.golden` now contains `gsx.FilterCSS("color: " + u)` (the dynamic part is CSS-filtered). Confirm `render.golden` is unchanged (`<span style="ZgotmplZ">x</span>`).
- `block_import_injection.txtar`: `render.golden` is `<style>.a{color:ZgotmplZ}</style>` (the `@import` rejected); `generated.x.go.golden` contains `_gsxgw.CSS(string(userColor))`.

- [ ] **Step 4: Confirm green**

`go test ./internal/corpus -run TestCorpus -count=1` → PASS. `go test ./... -count=1` green; `go vet ./...` clean.

- [ ] **Step 5: Commit**
```bash
git add internal/corpus/testdata/cases/style/composed_injection.txtar internal/corpus/testdata/cases/style/block_import_injection.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): pin FilterCSS emission (composed_injection) + @import block injection render case"
```

---

## Task 7: CSS minification branch coverage (audit #8)

`internal/cssmin` `minifyMarkup` `ForMarkup`/`SwitchMarkup`/`Fragment` (~65%) and `minifyAttrs` `CondAttr` (~67%) are untraversed: a `<style>` inside those nodes would not be minified. Unit tests at the `internal/cssmin` layer (the AST walk), mirroring the existing `TestMinifyFileNestedStyle`.

**Files:**
- Modify: `internal/cssmin/file_test.go` (add tests)

**Interfaces (from `ast/ast.go`, verified):**
- `ForMarkup{Clause string; Body []Markup}`
- `SwitchMarkup{Tag string; Cases []*CaseClause}`, `CaseClause{List string; Default bool; Body []Markup}`
- `Fragment{Children []Markup}`
- `CondAttr{Cond string; Then []Attr; Else []Attr}`
- Existing helpers in the test file: `styleEl(children…) *ast.Element`, and the `MinifyFile(*ast.File, ext) error` entry point. A holeless `<style>` body `"  .a {\n  x: 1;\n}  "` minifies to `".a{x: 1}"`.

- [ ] **Step 1: Add the four tests**

Append to `internal/cssmin/file_test.go`:
```go
func TestMinifyFileStyleInForMarkup(t *testing.T) {
	deep := styleEl(&ast.Text{Value: "  .a {\n  x: 1;\n}  "})
	loop := &ast.ForMarkup{Clause: "_, x := range xs", Body: []ast.Markup{deep}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{loop}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != ".a{x: 1}" {
		t.Fatalf("<style> in ForMarkup.Body not minified: %q", got)
	}
}

func TestMinifyFileStyleInSwitchMarkup(t *testing.T) {
	deep := styleEl(&ast.Text{Value: "  .a {\n  x: 1;\n}  "})
	sw := &ast.SwitchMarkup{Tag: "v", Cases: []*ast.CaseClause{{List: "1", Body: []ast.Markup{deep}}}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{sw}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != ".a{x: 1}" {
		t.Fatalf("<style> in SwitchMarkup case not minified: %q", got)
	}
}

func TestMinifyFileStyleInFragment(t *testing.T) {
	deep := styleEl(&ast.Text{Value: "  .a {\n  x: 1;\n}  "})
	frag := &ast.Fragment{Children: []ast.Markup{deep}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{frag}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != ".a{x: 1}" {
		t.Fatalf("<style> in Fragment not minified: %q", got)
	}
}

func TestMinifyFileStyleInCondAttr(t *testing.T) {
	deep := styleEl(&ast.Text{Value: "  .a {\n  x: 1;\n}  "})
	host := &ast.Element{Tag: "div", Attrs: []ast.Attr{
		&ast.CondAttr{Cond: "ok", Then: []ast.Attr{
			&ast.MarkupAttr{Name: "header", Value: []ast.Markup{deep}},
		}},
	}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{host}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != ".a{x: 1}" {
		t.Fatalf("<style> in CondAttr.Then MarkupAttr not minified: %q", got)
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/cssmin -run 'TestMinifyFileStyleIn' -v`
Expected: all four PASS. If `MinifyFile` requires non-empty `span`/positions or a struct field differs, read `ast/ast.go` for the exact field and fix the literal (do not change production code). If a test reveals a node type's `<style>` is genuinely NOT walked (a real minification miss bug), STOP and surface it per the Global Constraints rather than asserting the unminified value.

- [ ] **Step 3: Confirm coverage moved + full suite**

Run: `make cover` then `go tool cover -func=cover.out | grep -E 'cssmin/file\.go:(30|138):'` → `minifyMarkup`/`minifyAttrs` higher (For/Switch/Fragment + CondAttr now covered).
Run: `go test ./... -count=1` (green), `go vet ./...` (clean).

- [ ] **Step 4: Commit**
```bash
git add internal/cssmin/file_test.go
git commit -m "test(cssmin): cover <style> minification in for/switch/fragment + CondAttr slots"
```

---

## Task 8: JSVal/JSValAttr non-string value coverage (audit #4 + review minors)

Pin `JSVal`/`JSValAttr` for non-string types (int/bool/map). This also covers `js.go` `isJSIdentPart` (28.6% — only reachable for non-string JSON output) and the review minors (`x-show={ b }` bool-in-ctxJS; form-2b whole-value with non-struct types).

**Files (Create):**
- `internal/corpus/testdata/cases/script/value_nonstring.txtar`
- `internal/corpus/testdata/cases/jsattr/whole_value_bool.txtar`
- `internal/corpus/testdata/cases/jsattr/whole_value_map.txtar`
- Regenerate: `internal/corpus/testdata/coverage.golden`

**Interfaces:** script value-context non-string → `_gsxgw.JSVal(<expr>)` (JSON-encode); JS-attr whole-value → `_gsxgw.JSValAttr(<expr>)`. `JSVal`/`JSValAttr` accept `any`.

- [ ] **Step 1: Write the inputs** (append a `-- generated.x.go.golden --` marker per the Global Constraints rule)

`value_nonstring.txtar`:
```
-- input.gsx --
package views

component Page(count int, flag bool) {
	<script>const n = @{ count }; const b = @{ flag };</script>
}
-- invoke --
Page(PageProps{Count: 42, Flag: true})
-- diagnostics.golden --
```

`whole_value_bool.txtar` (`x-show={ b }` — verify it routes to a JS context; if `x-show` is NOT JS-classified, use `x-data={ open }` instead and note it):
```
-- input.gsx --
package views

component Page(open bool) {
	<div x-show={ open }>x</div>
}
-- invoke --
Page(PageProps{Open: true})
-- diagnostics.golden --
```

`whole_value_map.txtar`:
```
-- input.gsx --
package views

component Page(m map[string]int) {
	<div x-data={ m }>x</div>
}
-- invoke --
Page(PageProps{M: map[string]int{"a": 1}})
-- diagnostics.golden --
```

- [ ] **Step 2: Generate + verify**

Run `go test ./internal/corpus -run TestCorpus -update`. Verify:
- `value_nonstring`: gen contains `_gsxgw.JSVal(count)` and `_gsxgw.JSVal(flag)`; render shows `const n = 42; const b = true;`.
- `whole_value_bool`: gen contains `_gsxgw.JSValAttr(open)`; render shows `x-show="true"` (or `x-data="true"` if you fell back). If the attr is NOT JS-classified (gen shows a plain static/expr attr, not `JSValAttr`), switch to `x-data` and re-verify.
- `whole_value_map`: gen contains `_gsxgw.JSValAttr(m)`; render shows the JSON map, attr-escaped (e.g. `x-data="{&#34;a&#34;:1}"`).

- [ ] **Step 3: Confirm green + coverage**

`go test ./internal/corpus -run TestCorpus -count=1` → PASS. `make cover` then `go tool cover -func=cover.out | grep 'js.go:364'` → `isJSIdentPart` materially higher than 28.6%.

- [ ] **Step 4: Commit**
```bash
git add internal/corpus/testdata/cases/script/value_nonstring.txtar internal/corpus/testdata/cases/jsattr/whole_value_bool.txtar internal/corpus/testdata/cases/jsattr/whole_value_map.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): JSVal/JSValAttr non-string values (int/bool/map) — covers isJSIdentPart"
```

---

## Task 9: JS escaper differential FUZZ vs html/template (review #1 — security core)

The CSS/HTML escapers have `FuzzEscaperMatchesStdlib` (parity-vs-`html/template` fuzz); the JS escapers (`JSVal`/`JSStr`/`JSTmpl`/`JSRegexp` + `*Attr`) have only fixed tables (`TestJS*Diff`, `TestJS*AttrParity`). Give them the same fuzz-parity guarantee. This is the security core.

**Files:**
- Modify: `js_diff_test.go` (add `FuzzJSEscapersMatchStdlib`)

**Interfaces (already in `js_diff_test.go`, package `gsx` — reuse, do not re-implement):**
- Oracles: `jsValOracle`, `jsStrOracle`, `jsTmplOracle`, `jsRegexpOracle` (each renders `{{.}}` through `html/template` in that JS sub-context and extracts the value).
- gsx wrappers: `gsxJSVal`, `gsxJSStr`, `gsxJSTmpl`, `gsxJSRegexp`, and the attr variants `gsxJSValAttr`, `gsxJSStrAttr`, `gsxJSTmplAttr`, `gsxJSRegexpAttr`.
- Seed corpus: `jsCorpus()`.
- The `*Attr` parity assertion the fixed tests use is `gsxJSStrAttr(s) == htmlReplacer.Replace(jsStrOracle(s))` (and likewise for the other three) — `htmlReplacer` is accessible (same package).

- [ ] **Step 1: Add the fuzz target**

In `js_diff_test.go`, add a fuzz that seeds from `jsCorpus()` and, for each input, asserts the SAME parities the existing `TestJSValDiff`/`TestJSStrDiff`/`TestJSTmplDiff`/`TestJSRegexpDiff` and `TestJS*AttrParity` tests assert — over the fuzzed input. Concretely:
```go
func FuzzJSEscapersMatchStdlib(f *testing.F) {
	for _, s := range jsCorpus() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Non-attr JS contexts: byte-parity vs the html/template sub-context oracle.
		if got, want := gsxJSVal(s), jsValOracle(s); got != want {
			t.Fatalf("JSVal(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSStr(s), jsStrOracle(s); got != want {
			t.Fatalf("JSStr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSTmpl(s), jsTmplOracle(s); got != want {
			t.Fatalf("JSTmpl(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSRegexp(s), jsRegexpOracle(s); got != want {
			t.Fatalf("JSRegexp(%q)=%q, oracle=%q", s, got, want)
		}
		// Attr variants: JS-escape composed with HTML-attr-escape (htmlReplacer).
		if got, want := gsxJSValAttr(s), htmlReplacer.Replace(jsValOracle(s)); got != want {
			t.Fatalf("JSValAttr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSStrAttr(s), htmlReplacer.Replace(jsStrOracle(s)); got != want {
			t.Fatalf("JSStrAttr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSTmplAttr(s), htmlReplacer.Replace(jsTmplOracle(s)); got != want {
			t.Fatalf("JSTmplAttr(%q)=%q, oracle=%q", s, got, want)
		}
		if got, want := gsxJSRegexpAttr(s), htmlReplacer.Replace(jsRegexpOracle(s)); got != want {
			t.Fatalf("JSRegexpAttr(%q)=%q, oracle=%q", s, got, want)
		}
	})
}
```
If any helper signature differs from the above (e.g. an oracle name), match what `js_diff_test.go` actually defines — read it first; do not invent.

- [ ] **Step 2: Run the fuzz**

Run: `go test . -run FuzzJSEscapersMatchStdlib -count=1` (seed corpus as unit test) → PASS.
Run: `go test . -run x -fuzz FuzzJSEscapersMatchStdlib -fuzztime=30s`.
- If it passes with no new failures: good.
- If the fuzz finds a divergence: this is a FINDING. Determine whether it is (a) a REAL gsx escaper bug (gsx emits something less safe than html/template) — if so, STOP and surface it per the Global Constraints (do not fix the escaper in this task; it is a production change), or (b) a deliberate, safe gsx difference — if so, add a documented, justified skip (mirror `escape_diff_test.go`'s `knownDivergences` pattern: a guarded skip with a security justification comment) and a corpus seed under `testdata/fuzz/`. NEVER silently allow-list without a justification.

- [ ] **Step 3: Commit**
```bash
git add js_diff_test.go
git commit -m "test: differential fuzz for JS escapers (JSVal/JSStr/JSTmpl/JSRegexp + *Attr) vs html/template"
```

---

## Task 10: jsmin walk-branch + data-island coverage (review #3 — twin of Task 7)

`internal/jsmin/file.go`'s walk has the same `ForMarkup`/`SwitchMarkup`/`Fragment` branches as cssmin (untested) plus a data-island skip (`isDataIslandScript`, file.go:38) that no test exercises. Mirror Task 7 for jsmin.

**Files:**
- Modify: `internal/jsmin/file_test.go`

**Interfaces (from `internal/jsmin/file_test.go`, verified):** `scriptEl(text string) *ast.Element`, `fileWith(el *ast.Element) *ast.File`, `MinifyFile(*ast.File, ext) error`. A holeless `<script>` body `"function f() {\n\treturn 1;\n}"` minifies to `"function f() {\nreturn 1;\n}"` (see `TestMinifyFileScript`). AST structs: `ForMarkup{Clause string; Body []Markup}`, `SwitchMarkup{Tag string; Cases []*CaseClause}`, `CaseClause{List string; Default bool; Body []Markup}`, `Fragment{Children []Markup}`. A data-island script is an `*ast.Element{Tag:"script"}` with a `*ast.StaticAttr{Name:"type", Value:"application/json"}` — `isDataIslandScript` returns true for any non-executable `type`, so its body must be left UNCHANGED by `MinifyFile`.

- [ ] **Step 1: Add the four tests**

Append to `internal/jsmin/file_test.go` (the helpers `scriptEl`/`fileWith` already exist):
```go
func TestMinifyFileScriptInForMarkup(t *testing.T) {
	deep := scriptEl("function f() {\n\treturn 1;\n}")
	loop := &ast.ForMarkup{Clause: "_, x := range xs", Body: []ast.Markup{deep}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{loop}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != "function f() {\nreturn 1;\n}" {
		t.Fatalf("<script> in ForMarkup.Body not minified: %q", got)
	}
}

func TestMinifyFileScriptInSwitchMarkup(t *testing.T) {
	deep := scriptEl("function f() {\n\treturn 1;\n}")
	sw := &ast.SwitchMarkup{Tag: "v", Cases: []*ast.CaseClause{{List: "1", Body: []ast.Markup{deep}}}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{sw}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != "function f() {\nreturn 1;\n}" {
		t.Fatalf("<script> in SwitchMarkup case not minified: %q", got)
	}
}

func TestMinifyFileScriptInFragment(t *testing.T) {
	deep := scriptEl("function f() {\n\treturn 1;\n}")
	frag := &ast.Fragment{Children: []ast.Markup{deep}}
	f := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{frag}}}}
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := deep.Children[0].(*ast.Text).Value; got != "function f() {\nreturn 1;\n}" {
		t.Fatalf("<script> in Fragment not minified: %q", got)
	}
}

func TestMinifyFileSkipsDataIslandScript(t *testing.T) {
	// A holeless data-block <script type="application/json"> is NOT JavaScript;
	// MinifyFile must leave its body byte-for-byte unchanged.
	body := "{\n  \"a\": 1\n}"
	el := &ast.Element{
		Tag:   "script",
		Attrs: []ast.Attr{&ast.StaticAttr{Name: "type", Value: "application/json"}},
		Children: []ast.Markup{&ast.Text{Value: body}},
	}
	f := fileWith(el)
	if err := MinifyFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if got := el.Children[0].(*ast.Text).Value; got != body {
		t.Fatalf("data-island <script> was modified: %q (want %q)", got, body)
	}
}
```

- [ ] **Step 2: Run + verify**

Run: `go test ./internal/jsmin -run 'TestMinifyFileScriptIn|TestMinifyFileSkipsDataIsland' -v` → all PASS. If a struct field differs, read `ast/ast.go` and fix the literal (no production change). If a test reveals `<script>` in some node is genuinely NOT walked (a real minification-miss bug), STOP and surface it per the Global Constraints rather than asserting the unminified value.

- [ ] **Step 3: Confirm coverage + suite**

Run: `make cover` then `go tool cover -func=cover.out | grep -E 'jsmin/file\.go:'` → the `ForMarkup`/`SwitchMarkup`/`Fragment` walk branches + the data-island skip now covered.
Run: `go test ./... -count=1` (green), `go vet ./...` (clean).

- [ ] **Step 4: Commit**
```bash
git add internal/jsmin/file_test.go
git commit -m "test(jsmin): cover <script> minify in for/switch/fragment + data-island skip"
```

---

## Task 11: jsx classifier fail-closed FUZZ (review #2 — highest-value hardening)

`internal/jsx` (the novel JS-context state machine, CVE-territory) has ZERO fuzz — only tables + corpus. Add a no-panic + fail-closed-invariant fuzz: throw arbitrary JS around a hole at `ResolveScripts`; assert it never panics and every surviving hole resolves to a real `JSCtx` (never silently left unescaped) OR `ResolveScripts` returns a fail-closed error.

**Files:**
- Create: `internal/jsx/fuzz_test.go` (package `jsx`)

**Interfaces (verified):**
- `ResolveScripts(f *ast.File) error` — the public entry; walks the file, classifies `<script>` holes, sets `Interp.JSCtx`, rewrites comment holes to literal `Text` (removing them from children), and fails closed on unclassifiable holes.
- `ast.JSCtx` constants: `JSCtxNone` (0, the unescaped/default), `JSCtxValue`, `JSCtxString`, `JSCtxTemplate`, `JSCtxRegexp` (the four real escaping contexts).
- Construct the AST directly (no parser — the fuzz input is arbitrary bytes): `&ast.File{Decls: []ast.Decl{&ast.Component{Name:"C", Body: []ast.Markup{ &ast.Element{Tag:"script", Children: []ast.Markup{ &ast.Text{Value: prefix}, &ast.Interp{Expr:"x"}, &ast.Text{Value: suffix} }} }}}}`.
- The security invariant: after `ResolveScripts` returns `nil`, every `*ast.Interp` STILL present in the script's children must have `JSCtx != ast.JSCtxNone` (comment holes are removed from children, so they are correctly not checked). An interp left at `JSCtxNone` with a `nil` error = a silently-unescaped hole = a leak.

- [ ] **Step 1: Write the fuzz**

Create `internal/jsx/fuzz_test.go`:
```go
package jsx

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// FuzzResolveScriptsFailClosed throws arbitrary JS text around a single @{ } hole
// in a <script> body and asserts the classifier (1) never panics and (2) never
// leaves the hole silently unescaped: a nil error MUST mean every surviving hole
// resolved to a real JS context (not JSCtxNone). A fail-closed error is fine.
func FuzzResolveScriptsFailClosed(f *testing.F) {
	for _, s := range []string{
		"", "var x=", "var x=\"", "var x=`", "var x=/", "let ", "//", "/*",
		"const y={a:", "return ", "x=1;y=", "function f(){", "`${", "</script>",
	} {
		f.Add(s, "")
		f.Add("", s)
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		in := &ast.Interp{Expr: "x"}
		script := &ast.Element{Tag: "script", Children: []ast.Markup{
			&ast.Text{Value: prefix}, in, &ast.Text{Value: suffix},
		}}
		file := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{script}}}}

		err := ResolveScripts(file) // must not panic
		if err != nil {
			return // fail-closed is an acceptable outcome
		}
		// nil error: every surviving hole must carry a real escaping context.
		for _, c := range script.Children {
			if ip, ok := c.(*ast.Interp); ok && ip.JSCtx == ast.JSCtxNone {
				t.Fatalf("hole left JSCtxNone (silently unescaped) for prefix=%q suffix=%q", prefix, suffix)
			}
		}
	})
}
```
If `ResolveScripts` mutates `script.Children` (comment un-split removes the interp), the loop correctly only checks surviving interps. If the real field/constant names differ, read `ast/ast.go` and `internal/jsx/jsx.go` and match them — do not invent.

- [ ] **Step 2: Run the fuzz**

Run: `go test ./internal/jsx -run FuzzResolveScriptsFailClosed -count=1` (seed corpus) → PASS.
Run: `go test ./internal/jsx -run x -fuzz FuzzResolveScriptsFailClosed -fuzztime=30s`.
- A panic or a `JSCtxNone`-with-nil-error failure is a REAL classifier bug (a fail-open leak in CVE-territory code) — STOP and surface it per the Global Constraints; do NOT weaken the assertion to make it pass.
- If it passes: the fail-closed invariant holds over the fuzzed surface.

- [ ] **Step 3: Confirm suite**

`go test ./... -count=1` (green), `go vet ./...` (clean).

- [ ] **Step 4: Commit**
```bash
git add internal/jsx/fuzz_test.go
git commit -m "test(jsx): fail-closed fuzz — classifier never panics or silently leaves a hole unescaped"
```

---

## Task 12: Final verification

- [ ] **Step 1:** `go test ./... -count=1` → all green.
- [ ] **Step 2:** `go vet ./...` → clean.
- [ ] **Step 3:** `make cover` → runs; capture `go tool cover -func=cover.out` for the audit's named functions and confirm the before→after deltas: `emitClassAttr` 0→100%; `emitJSValue`/`emitJSAttrValue` materially up; `isJSIdentPart` up from 28.6%; `hexDecode`/`skipCSSSpace`/`decodeCSS` ~100% (minus the defensive `hexDecode` panic); `cssmin` AND `jsmin` `minifyMarkup`/`minifyAttrs` For/Switch/Fragment/CondAttr + data-island skip covered.
- [ ] **Step 4:** Fuzzers each `-fuzztime=10s` with no divergence: `FuzzCSSValueFilter`, `FuzzEscaperMatchesStdlib`, `FuzzJSEscapersMatchStdlib` (new — JS escaper parity), `FuzzResolveScriptsFailClosed` (new — jsx fail-closed), `FuzzMinifyCSS`, `FuzzMinifyJS`.
- [ ] **Step 5:** Confirm `git grep -n 'go.work'` finds nothing new; structural compare and single-batch render untouched; the only production file in the branch diff is none (test/test-infra/docs only) — `git diff --stat $(git merge-base main HEAD)..HEAD` shows no `*.go` under the root package, `internal/codegen`, or runtime files except test files.
- [ ] **Step 6:** Summarize in the final report: per-function coverage before→after; any unreachable JS-attr template/regexp branch found in Task 3; any real bug surfaced (should be none).

---

## Notes / Deferred (unchanged from spec §1)

- Real `line:col` codegen-diagnostic positions (position-threading increment; consumes `codegen-diagnostic-position-audit.md`). This increment only de-brittles the offset (Task 1).
- R4 document-level differential render; R5 security payload×context generator.
