# JSON-aware Attribute Minification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Minify JSON-shaped `js`…`` *attribute* values (e.g. htmx `hx-vals`) as JSON so they stay valid JSON, instead of JS-minifying them into `({key:…})` which htmx's `JSON.parse` rejects.

**Architecture:** In `internal/jsmin`, the attribute-value minify path classifies a `{`/`[`-leading `js`…`` value with a real `encoding/json` parse (holes substituted with JSON-valid integer sentinels). JSON-valid → minify via tdewolff's `application/json` minifier (`fullmin.JSON`); not JSON → the existing tdewolff JS-expression path, byte-for-byte unchanged. The two minifiers are injected together via a new `jsmin.Minifiers{JS, JSON}` struct (replacing the single `ext func` param).

**Tech Stack:** Go; `github.com/tdewolff/minify/v2` (already a dep; `css` + `js` registered, add `json`); `encoding/json` (stdlib).

## Global Constraints

- Runtime (root package) stays stdlib-only; this work is entirely in tooling (`internal/jsmin`, `internal/fullmin`, `internal/corpus`) — deps allowed there.
- No `js` literal syntax change → NO tree-sitter-gsx / vscode-gsx changes.
- Every codegen/behavior change ships a corpus case (`internal/corpus/testdata/cases`), regenerated with `go test ./internal/corpus -run TestCorpus -update` (also rewrites `coverage.golden`), then verified without `-update`.
- Pin Go to `GO_VERSION` in `.github/workflows/ci.yml` (1.26.1) to avoid gofmt drift.
- Final gate: `make ci` green (build/vet/test both modules, examples drift, gofmt + gsx fmt).
- No "simple heuristics" — the JSON classifier is a real `json.Valid` parse, not a shape guess.
- Scope excludes `<script>` blocks and `{{ }}`-block / `{ expr }`-embedded `js`…`` literals.

---

### Task 1: `fullmin.JSON` minifier entry

**Files:**
- Modify: `internal/fullmin/fullmin.go`
- Test: `internal/fullmin/fullmin_test.go` (create if absent)

**Interfaces:**
- Produces: `fullmin.JSON(s string) (string, error)` — compacts a complete JSON document via tdewolff `application/json`.

- [ ] **Step 1: Write the failing test**

```go
// internal/fullmin/fullmin_test.go
package fullmin

import "testing"

func TestJSON(t *testing.T) {
	got, err := JSON(`{ "exclude": "SELF-1" }`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"exclude":"SELF-1"}` {
		t.Fatalf("JSON minify = %q, want %q", got, `{"exclude":"SELF-1"}`)
	}
	// A bare integer (the hole sentinel) must survive verbatim.
	got2, _ := JSON(`{ "k": 909090900 }`)
	if got2 != `{"k":909090900}` {
		t.Fatalf("integer not preserved: %q", got2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fullmin/ -run '^TestJSON$' -v`
Expected: FAIL — `undefined: JSON`.

- [ ] **Step 3: Add the JSON func and register the minifier**

```go
// internal/fullmin/fullmin.go — add import + registration + func
import (
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
	"github.com/tdewolff/minify/v2/json"
)

func newMinifier() *minify.M {
	m := minify.New()
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("application/javascript", js.Minify)
	m.AddFunc("application/json", json.Minify)
	return m
}

// JSON aggressively minifies a complete (holeless) JSON string: whitespace is
// stripped and validity preserved (quoted keys kept, no expression rewrites).
func JSON(s string) (string, error) { return m.String("application/json", s) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fullmin/ -run '^TestJSON$' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fullmin/fullmin.go internal/fullmin/fullmin_test.go
git commit -m "feat(fullmin): add JSON (application/json) minifier"
```

---

### Task 2: Thread `Minifiers{JS, JSON}` through jsmin (refactor, no behavior change)

Replace the single `ext func(string) (string, error)` parameter with a
`Minifiers` struct carrying both `JS` and `JSON`. A nil `JS` still means the
safe level (as `ext == nil` does today). This task is a pure refactor — the
JSON field is added and wired but not yet consulted, so all existing tests stay
green.

**Files:**
- Modify: `internal/jsmin/file.go` (all functions taking `ext`: `MinifyFile`, `minifyMarkup`, `minifyGoParts`, `minifyScriptChildren`, `minifyJSAttrs`, `minifyJSSegments`, `minifyJSSegmentsHoley`, `cascadeJS`)
- Modify: `internal/jsmin/attrhelpers_test.go` (the `jsminFileMinify` helper)
- Modify caller: `internal/codegen/emit.go:53` (`jsmin.MinifyFile(file, jsMin)`)
- Modify caller: `gen/main.go` / wherever `jsMin` is passed to codegen — thread `jsonMin := c.effectiveJSONMin()` alongside (see Interfaces)

**Interfaces:**
- Produces:
  ```go
  // internal/jsmin/file.go
  type Minifiers struct {
      JS   func(string) (string, error) // nil = safe level (built-in)
      JSON func(string) (string, error) // nil = safe level (built-in)
  }
  func MinifyFile(f *ast.File, m Minifiers) error
  ```
- Consumes (Task 1): `fullmin.JSON`.

- [ ] **Step 1: Change the signatures**

In `internal/jsmin/file.go`, replace every `ext func(string) (string, error)`
parameter with `m Minifiers`, every internal `ext(src)` call with `m.JS(src)`,
and every `ext != nil` / `ext == nil` guard with `m.JS != nil` / `m.JS == nil`.
`cascadeJS(text string, ext …)` → `cascadeJS(text string, m Minifiers)` (its
body keeps using `m.JS` for now). `MinifyFile(f *ast.File, ext …)` →
`MinifyFile(f *ast.File, m Minifiers)`.

- [ ] **Step 2: Update the test helper**

```go
// internal/jsmin/attrhelpers_test.go
func jsminFileMinify(f *ast.File, ext func(string) (string, error)) error {
	return MinifyFile(f, Minifiers{JS: ext, JSON: fullmin.JSON})
}
```
(Existing tests still pass `fullminJS` as `ext`; JSON is wired but unused until Task 3/4.)

- [ ] **Step 3: Update the codegen caller and config wiring**

In `internal/codegen/emit.go:53`:
```go
if err := jsmin.MinifyFile(file, jsmin.Minifiers{JS: jsMin, JSON: jsonMin}); err != nil {
```
Thread `jsonMin func(string)(string,error)` from `gen` the same way `jsMin` is
threaded: add `effectiveJSONMin()` on `config` returning `fullmin.JSON` when
`jsMinLevel == MinifyFull` (JSON follows the JS level — one gate), else nil; pass
it through `GenerateDirs`/`generateFile`'s existing `jsMin` plumbing as a parallel
`jsonMin` argument. (Mirror `effectiveJSMin` in `gen/main.go` and the `jsMin`
parameter in `internal/codegen/emit.go:generateFile` / `gen/cache.go`.)

- [ ] **Step 4: Build and run the full jsmin + codegen suites**

Run: `go build ./... && go test ./internal/jsmin/... ./internal/codegen/... -count=1`
Expected: PASS for all existing tests; the repro test
`TestMinifyJSAttrJSONShapedValueStaysValidJSON` still FAILS (behavior unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/jsmin/file.go internal/jsmin/attrhelpers_test.go internal/codegen/emit.go gen/main.go gen/cache.go
git commit -m "refactor(jsmin): thread Minifiers{JS,JSON} in place of single ext"
```

---

### Task 3: Holeless JSON classification + routing

Route a holeless `{`/`[`-leading value that `json.Valid` accepts to `m.JSON`.

**Files:**
- Modify: `internal/jsmin/file.go` (`cascadeJS`, and add `jsonValidJS` helper)
- Test: `internal/jsmin/file_test.go`

**Interfaces:**
- Consumes: `Minifiers` (Task 2), `fullmin.JSON` (Task 1).
- Produces (used by Task 4): `func looksJSON(text string) bool` — trimmed value starts with `{` or `[` and `json.Valid([]byte(text))`.

- [ ] **Step 1: Extend the failing repro + add edge tests**

The committed `TestMinifyJSAttrJSONShapedValueStaysValidJSON` already asserts
validity; tighten it and add edges:

```go
// tighten the repro (same test): after json.Valid check, assert compact form
	if got != `{"exclude":"SELF-1"}` {
		t.Fatalf("want compact JSON {\"exclude\":\"SELF-1\"}, got %q", got)
	}

// new: internal/jsmin/file_test.go
func TestMinifyJSAttrClassification(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"json_object", `{ "a": 1, "b": "x" }`, `{"a":1,"b":"x"}`},
		{"json_array", `[ 1, 2, 3 ]`, `[1,2,3]`},
		{"json_nested", `{ "a": { "b": [1, 2] } }`, `{"a":{"b":[1,2]}}`},
		{"js_unquoted_key_stays_js", `{ open: false }`, `({open:!1})`},
		{"js_single_quoted_key_stays_js", `{ 'a': 1 }`, `({a:1})`},
		{"js_trailing_comma_stays_js", `{ "a": 1, }`, `({a:1})`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "hx-vals", Lang: ast.EmbeddedJS,
				DoubleQuoted: true, Segments: []ast.Markup{&ast.Text{Value: c.in}}}))
			if err := jsminFileMinify(f, fullminJS); err != nil {
				t.Fatal(err)
			}
			if got := attrSegs(f)[0].(*ast.Text).Value; got != c.want {
				t.Fatalf("in=%q got=%q want=%q", c.in, got, c.want)
			}
		})
	}
}
```
Note the `js_*` expectations pin the UNCHANGED tdewolff JS output (`({open:!1})`
etc.) — proof the JS path is untouched. Confirm those exact strings with a scratch
`t.Logf` run first if unsure of tdewolff's spelling, then pin them.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jsmin/ -run 'JSONShaped|Classification' -count=1 -v`
Expected: FAIL — JSON cases produce `({…})`/unquoted output.

- [ ] **Step 3: Implement classification + routing in `cascadeJS`**

```go
// internal/jsmin/file.go
import "encoding/json"

// looksJSON reports whether text is a JSON object/array literal (a real parse,
// not a shape guess). Callers pass a hole-free string (holeless value, or a
// holey value with holes replaced by JSON-valid integer sentinels).
func looksJSON(text string) bool {
	t := strings.TrimLeft(text, " \t\r\n")
	if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid([]byte(text))
}

func cascadeJS(text string, m Minifiers) string {
	// JSON-shaped attribute values (htmx hx-vals/hx-headers/hx-vars) must stay
	// valid JSON: JS minification would unquote keys and wrap objects in (…),
	// which JSON.parse rejects. Route them to the JSON minifier (whitespace-only,
	// validity-preserving). Full level only; the safe fallback below already
	// preserves validity (never rewrites values).
	if m.JSON != nil && looksJSON(text) {
		if o, err := m.JSON(text); err == nil {
			return o
		}
	}
	if m.JS != nil {
		first, second := text, "("+text+")"
		if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "{") {
			first, second = second, first
		}
		if o, err := m.JS(first); err == nil {
			return o
		}
		if o, err := m.JS(second); err == nil {
			return o
		}
	}
	return minifyJS(text)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/jsmin/ -run 'JSONShaped|Classification' -count=1 -v`
Expected: PASS (holeless JSON compacts; JS cases unchanged).

- [ ] **Step 5: Full jsmin suite**

Run: `go test ./internal/jsmin/... -count=1`
Expected: PASS (holey cases still go through the identifier-sentinel path — fixed in Task 4).

- [ ] **Step 6: Commit**

```bash
git add internal/jsmin/file.go internal/jsmin/file_test.go
git commit -m "feat(jsmin): route holeless JSON-shaped attr values to JSON minify"
```

---

### Task 4: Holey JSON classification + integer-sentinel round-trip

The holey path (`minifyJSSegmentsHoley`) substitutes holes with `gsxHole<n>z`
identifiers — not JSON. Add a JSON-valid **integer** sentinel variant: when the
sentinel string is JSON, minify with `m.JSON` and split the integers back.

**Files:**
- Modify: `internal/jsmin/file.go` (`minifyJSSegmentsHoley`, add integer-sentinel helper + `splitJSNumberSentinels`)
- Test: `internal/jsmin/file_test.go`

**Interfaces:**
- Consumes: `looksJSON` (Task 3), `Minifiers.JSON`, existing `splitJSSentinels` pattern.

- [ ] **Step 1: Write the failing holey test**

```go
func TestMinifyJSAttrHoleyJSONStaysValid(t *testing.T) {
	f := fileWith(divAttr(&ast.EmbeddedAttr{Name: "hx-vals", Lang: ast.EmbeddedJS, DoubleQuoted: true,
		Segments: []ast.Markup{
			&ast.Text{Value: `{ "exclude": `},
			&ast.Interp{Expr: "selfID"},
			&ast.Text{Value: ` }`},
		}}))
	if err := jsminFileMinify(f, fullminJS); err != nil {
		t.Fatal(err)
	}
	segs := attrSegs(f)
	var text string
	sawHole := false
	for _, s := range segs {
		switch x := s.(type) {
		case *ast.Interp:
			sawHole = true
			if x.Expr != "selfID" {
				t.Fatalf("hole expr changed: %q", x.Expr)
			}
		case *ast.Text:
			text += x.Value
		}
	}
	if !sawHole {
		t.Fatalf("hole lost: %#v", segs)
	}
	// Reassemble with a stand-in string value the render would emit, assert JSON.
	rendered := strings.Replace(joinSegs(segs), "@@HOLE@@", `"SELF-1"`, 1)
	_ = rendered
	// Structural checks: quoted key kept, no paren-wrap, whitespace gone.
	if strings.Contains(text, "(") || strings.Contains(text, "exclude:") {
		t.Fatalf("JSON structure broken: %q", text)
	}
	if !strings.Contains(text, `"exclude":`) {
		t.Fatalf("quoted key lost: %q", text)
	}
	if strings.Contains(text, " ") {
		t.Fatalf("whitespace not stripped: %q", text)
	}
	if strings.Contains(text, "gsxHole") || strings.Contains(text, "909090") {
		t.Fatalf("sentinel leaked: %q", text)
	}
}
```
(Drop the unused `rendered`/`joinSegs` scaffold if you prefer — the structural
`text` assertions are the real gate.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jsmin/ -run 'HoleyJSON' -count=1 -v`
Expected: FAIL — value is `({exclude:@{selfID}})`-shaped (paren-wrap, unquoted key).

- [ ] **Step 3: Implement the integer-sentinel JSON branch in `minifyJSSegmentsHoley`**

Before building the identifier-sentinel string, build a parallel integer-sentinel
string and test it with `looksJSON`. If JSON and `m.JSON != nil`, minify via
`m.JSON` and split the integer sentinels back; otherwise fall through to the
existing identifier-sentinel JS path unchanged.

```go
// Integer sentinel: <base><index>, base a distinctive number absent from the
// static text (grown *10 on collision), each sentinel a full number token so it
// survives the JSON minifier verbatim (verified) and splits by exact match.
func minifyJSSegmentsHoley(segments []ast.Markup, m Minifiers) []ast.Markup {
	// ... existing scan of static text into `scan` ...

	// JSON branch (full level only): try integer sentinels.
	if m.JSON != nil {
		base := int64(900000000)
		for containsAnySentinel(scan.String(), base, countInterps(segments)) {
			base *= 10
		}
		numStr, numInterps := buildNumberSentinelString(segments, base) // "{ \"exclude\": 900000000 }"
		if looksJSON(numStr) {
			if out, err := m.JSON(numStr); err == nil {
				if split, ok := splitJSNumberSentinels(out, base, numInterps); ok {
					return split
				}
			}
		}
	}

	// ... existing identifier-sentinel path unchanged (prefix "gsxHole", cascadeJS,
	//     jsfmt.Format for safe, splitJSSentinels) ...
}
```

Helpers (mirror the existing `splitJSSentinels`, matching a run of digits equal to
`base+i`):
```go
func countInterps(segments []ast.Markup) int { /* count *ast.Interp */ }
func buildNumberSentinelString(segments []ast.Markup, base int64) (string, []*ast.Interp) {
	// like the existing sentinel build, but writes strconv.FormatInt(base+i, 10)
}
func containsAnySentinel(text string, base int64, n int) bool {
	for i := 0; i < n; i++ {
		if strings.Contains(text, strconv.FormatInt(base+int64(i), 10)) { return true }
	}
	return false
}
func splitJSNumberSentinels(s string, base int64, interps []*ast.Interp) ([]ast.Markup, bool) {
	// scan s for each exact sentinel number `base+i`, replace with interps[i],
	// spans between become *ast.Text; ok=false if any sentinel is missing/dup.
	// Match longest sentinels first (or fixed width) so base+1 isn't matched
	// inside base+10.
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/jsmin/ -run 'HoleyJSON' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Full jsmin suite + guard existing holey JS test**

Run: `go test ./internal/jsmin/... -count=1`
Expected: PASS — including `TestMinifyJSAttrHoleyFull` (`x-data` `{ id: @{id} }`,
unquoted key → not JSON → identifier-sentinel JS path unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/jsmin/file.go internal/jsmin/file_test.go
git commit -m "feat(jsmin): holey JSON-shaped attr values via integer-sentinel JSON round-trip"
```

---

### Task 5: Corpus case (end-to-end parse → generate → render)

**Files:**
- Create: `internal/corpus/testdata/cases/attributes/json_attr_minify.txtar` (name per the dir's convention — inspect a sibling first)
- Regenerate: `coverage.golden`

**Interfaces:** none (golden-pinned end-to-end).

- [ ] **Step 1: Inspect a sibling attr corpus case** for the txtar layout (files `input.gsx`, `generated.x.go.golden`, `render.golden`) and how minify is enabled in corpus runs.

Run: `ls internal/corpus/testdata/cases/attributes/ && sed -n '1,40p' internal/corpus/testdata/cases/attributes/<some-attr-case>.txtar`

- [ ] **Step 2: Write `input.gsx` inside the txtar** — a component rendering both a holeless and a holey JSON-shaped `js`…`` attribute plus a control `x-data`:

```
component C(selfID string) {
	<div hx-vals=js`{ "exclude": "SELF-1" }`></div>
	<div hx-vals=js`{ "exclude": @{selfID} }`></div>
	<div x-data=js`{ open: false }`></div>
}
```

- [ ] **Step 3: Generate goldens**

Run: `go test ./internal/corpus -run TestCorpus -update`
Expected: writes `generated.x.go.golden`, `render.golden`, updates `coverage.golden`.

- [ ] **Step 4: Verify `render.golden`** shows valid compact JSON for the two hx-vals and unchanged `({open:!1})` for x-data. Confirm with:

```bash
grep -n 'hx-vals\|x-data' internal/corpus/testdata/cases/attributes/json_attr_minify.txtar
```
Expected: `hx-vals="{&#34;exclude&#34;:&#34;SELF-1&#34;}"` (and the holey one), `x-data="({open:!1})"`.

- [ ] **Step 5: Verify without `-update`**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/corpus/testdata/cases/attributes/json_attr_minify.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): JSON-shaped js attribute values minify as valid JSON"
```

---

### Task 6: Docs note

**Files:**
- Modify: `docs/guide/config.md` (the `[minify]` section) — or the minify guide page; grep for where `js = "full"` is documented.

- [ ] **Step 1: Add a short note** (no `{{ }}` literals unless wrapped in `::: v-pre`):

> JSON-shaped `js`…`` attribute values — a `{`- or `[`-leading value that parses as valid JSON, such as htmx `hx-vals`/`hx-headers`/`hx-vars` — are minified as JSON (whitespace stripped, keys and structure preserved) so they stay valid for `JSON.parse`. Values that are not valid JSON (Alpine `x-data`, event handlers) are minified as JavaScript as before.

- [ ] **Step 2: Commit**

```bash
git add docs/guide/config.md
git commit -m "docs(guide): JSON-shaped js attribute values minify as JSON"
```

---

### Task 7: End-to-end in one-learning (cross-repo verification)

Runs only after this branch is merged/available to one-learning (the `go tool
gsx` there resolves from the local `replace`). Confirms the real regression is
fixed.

**Files (in `/Users/jackieli/work/one-learning-gsx`):**
- Modify: `ui/admin_license_usage_test.go`, `ui/entity_links_test.go` — remove the `t.Skip("blocked on gsx hx-vals minification fix")` lines.

- [ ] **Step 1: Regenerate one-learning against the fixed gsx**

Run (in one-learning): `go tool gsx generate -no-cache ./ui && go build ./...`
Expected: BUILD OK.

- [ ] **Step 2: Confirm valid JSON hx-vals**

Run: `go test ./ui/ -run 'TestEntityLinksEditSection_JSDataAndRoutes' -v` (still skipped) — first remove the skip.

- [ ] **Step 3: Remove the two `t.Skip` lines and re-run**

Run: `go test ./ui/ -run 'TestEntityLinksEditSection_JSDataAndRoutes|TestLicenseUsageDashboardPartials' -count=1 -v`
Expected: PASS (rendered `hx-vals="{&#34;exclude&#34;:&#34;SELF-1&#34;}"`).

- [ ] **Step 4: Commit (in one-learning)**

```bash
git add ui/admin_license_usage_test.go ui/entity_links_test.go
git commit -m "test(ui): unskip hx-vals tests — gsx now minifies JSON attrs as valid JSON"
```

---

## Final gate

- [ ] `make ci` green in the gsx worktree.
- [ ] Independent adversarial review (per CLAUDE.md) — a reviewer that builds a throwaway probe: feed assorted `js`…`` attribute values (single-quoted keys, computed x-data, deeply nested JSON, arrays, holes in key vs value position, RawJS holes) through `MinifyFile` and check JS values are untouched and JSON values are valid+compact.
