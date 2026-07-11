# srcset URL-list sanitization + #78/#71 batch — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sanitize `srcset` as a URL-list sink (WHATWG srcset grammar) on both the static and spread paths, and clear the remaining #78/#71 follow-ups (nested-cond spread count, `[]byte` URL rendering, attrs-only test debt, LSP hover).

**Architecture:** `srcset`/`imagesrcset` become `CtxURL` in `attrclass`; codegen's existing `urlWriterMethod(tag,name)` seam gains a `"Srcset"` arm so static and spread paths both route to a new `gw.Srcset`/`gw.SrcsetVal` sink backed by a `srcsetSanitize` parser. `srcsetSanitize` ports the **WHATWG srcset grammar** (candidate URL = non-whitespace run; trailing commas = boundaries; descriptor inert), sanitizing each candidate URL as an image resource (`urlSanitizeImage`) — NOT html/template's srcset code, which over-blocks valid inputs. Companion cleanups are independent mechanical tasks.

**Tech Stack:** Go 1.26.1, stdlib-only runtime (root `gsx` package), `internal/codegen`, `internal/attrclass`, `internal/lsp`, txtar corpus.

## Global Constraints

- Runtime (root `gsx` package) is **stdlib-only** — no `internal/` or third-party imports in `attrs.go`/`writer.go`/`escape.go`.
- Security escaping is a **faithful port of `html/template`, never an approximation** — no "simple heuristics". **Exception (documented in the spec):** `srcset` ports the **WHATWG srcset grammar**, not html/template's srcset code, because html/template's srcset sanitizer is a broken over-approximation (blocks `a.jpg 1.5x`, mangles data URLs — proven by probe). The security-critical per-URL scheme sanitization (`urlSanitizeImage`) stays a faithful port; only the candidate split follows WHATWG.
- gsx sanitizes URLs by **scheme allow-list only, no percent-normalization** (a deliberate gsx-wide choice). Per-candidate srcset sanitization reuses `urlSanitizeImage`; the failsafe is gsx's `blockedURL` (`about:invalid#gsx`), not `html/template`'s `#ZgotmplZ`.
- **Every syntax/codegen change ships a corpus case**; new syntax valid in multiple contexts needs a case per context. Regenerate goldens with `go test ./internal/corpus -run TestCorpus -update`, then verify **without** `-update`. Never hand-edit `.x.go`/golden files.
- Pin Go to `GO_VERSION` (1.26.1); a different minor re-introduces gofmt drift.
- `go run ./cmd/gsx …` (the `gsx` binary name collides with Ghostscript).
- Before merge: `make ci` + `make lint` green.
- Commit messages end with: `Claude-Session: https://claude.ai/code/session_01SttDCwkNKd5xPqKy8KrzxG`

---

### Task 1: srcset runtime sanitizer (`srcsetSanitize`)

Port the **WHATWG srcset grammar** to a pure string→string sanitizer; no codegen, no writer wiring. NOT html/template's srcset code (see Approach below).

**Approach:** Port the **WHATWG `srcset` grammar**, NOT `html/template`'s `srcset` code. `html/template`'s sanitizer is a broken over-approximation (it blocks `a.jpg 1.5x` → `#ZgotmplZ` and mangles `data:image/…,X 1x`), proven by probe. The security-critical part — per-URL scheme sanitization — stays a faithful port (`urlSanitizeImage`); only the candidate SPLIT follows WHATWG. See the spec's "Why not a faithful html/template port" and "Behavior table" sections.

**Files:**
- Modify: `escape.go` (add `srcsetSanitize`, `writeSrcset`)
- Test: `escape_test.go` (the existing sanitizer test cluster — grep for `TestURLSanitizeImage`/`TestRefreshContent` and colocate)

**Interfaces:**
- Consumes: existing `urlSanitizeImage(s string) string`, `blockedURL` const, `isASCIIWhitespaceByte(c byte) bool`, `writeHTML(w io.Writer, s string) error` (all in `escape.go`).
- Produces: `srcsetSanitize(s string) string`; `writeSrcset(w io.Writer, s string) error`.
- Note: does NOT add `filterSrcsetElement` or `isASCIIAlnumByte` — the WHATWG parser has no descriptor-metadata gate (that gate is what broke `1.5x`, and it carries no security value once the whole output is HTML-escaped).

- [ ] **Step 1: Write the failing unit test**

In the sanitizer test cluster (root `gsx` package). Every row is from the spec's Behavior table:

```go
func TestSrcsetSanitize(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"single relative", "a.jpg", "a.jpg"},
		{"single with descriptor", "a.jpg 2x", "a.jpg 2x"},
		{"multi candidate", "a.jpg 1x, b.jpg 2x", "a.jpg 1x, b.jpg 2x"},
		{"width descriptors", "s-320.jpg 320w, s-640.jpg 640w", "s-320.jpg 320w, s-640.jpg 640w"},
		{"fractional density kept", "a.jpg 1.5x", "a.jpg 1.5x"},
		{"leading/trailing space kept", " a.jpg 1x , b.jpg 2x ", " a.jpg 1x , b.jpg 2x "},
		{"javascript candidate blocked", "javascript:alert(1) 1x", "about:invalid#gsx"},
		{"one bad candidate blocks only itself", "ok.jpg 1x, javascript:alert(1) 2x", "ok.jpg 1x, about:invalid#gsx"},
		{"data image intact", "data:image/png;base64,iVBOR 1x", "data:image/png;base64,iVBOR 1x"},
		{"data image intact multi", "data:image/png;base64,iVBOR 1x, x.jpg 2x", "data:image/png;base64,iVBOR 1x, x.jpg 2x"},
		{"data non-image one clean block", "data:text/html,<script> 1x", "about:invalid#gsx"},
		{"no-space commas single misparse", "a.jpg,b.jpg", "a.jpg,b.jpg"},
		{"http passes", "http://x/a.jpg 1x", "http://x/a.jpg 1x"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := srcsetSanitize(tt.in); got != tt.want {
				t.Errorf("srcsetSanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestSrcsetSanitize -v`
Expected: FAIL — `undefined: srcsetSanitize`.

- [ ] **Step 3: Write the implementation in `escape.go`** (place beside `writeURLImage`/`urlSanitizeImage`)

```go
// srcsetSanitize sanitizes a srcset attribute value using the WHATWG srcset
// grammar: a comma-separated list of image candidates ("url [descriptor]").
// Each candidate's URL is the run of non-whitespace bytes (commas inside a URL —
// a data: URL's ";base64," separator, a query's "?a=1,2" — stay part of the
// URL); a run's trailing commas are candidate boundaries; the rest up to the
// next comma is an inert descriptor. Each URL is sanitized as an image resource
// (urlSanitizeImage); a blocked URL collapses its whole candidate to blockedURL.
// The descriptor needs no validation — writeSrcset HTML-escapes the whole
// result, so descriptor content can never break out of the attribute. This is a
// faithful port of the WHATWG grammar, not html/template's srcset code (which
// over-blocks valid inputs like "a.jpg 1.5x" and mangles data: URLs).
func srcsetSanitize(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		// 1. Candidate separators (leading whitespace + commas) copied verbatim.
		sep := i
		for i < len(s) && (isASCIIWhitespaceByte(s[i]) || s[i] == ',') {
			i++
		}
		b.WriteString(s[sep:i])
		if i >= len(s) {
			break
		}
		// 2. URL = run of non-whitespace bytes (commas inside a URL stay).
		urlStart := i
		for i < len(s) && !isASCIIWhitespaceByte(s[i]) {
			i++
		}
		run := s[urlStart:i]
		url := strings.TrimRight(run, ",") // trailing commas are boundaries
		i -= len(run) - len(url)           // re-consume them as separators
		// 3. Descriptor: rest up to the next comma, only when the URL run had no
		//    trailing-comma boundary. Inert (HTML-escaped downstream).
		descStart := i
		if len(url) == len(run) {
			for i < len(s) && s[i] != ',' {
				i++
			}
		}
		desc := s[descStart:i]
		// 4. A blocked URL collapses the whole candidate; else URL + descriptor.
		if urlSanitizeImage(url) == blockedURL {
			b.WriteString(blockedURL)
		} else {
			b.WriteString(url)
			b.WriteString(desc)
		}
	}
	return b.String()
}

// writeSrcset streams a sanitized, attribute-escaped srcset value to w.
func writeSrcset(w io.Writer, s string) error {
	return writeHTML(w, srcsetSanitize(s))
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test . -run TestSrcsetSanitize -v`
Expected: PASS (all 14 subtests). Then `go test .` once — no root-package regression.

- [ ] **Step 5: Commit**

```bash
git add escape.go escape_test.go
git commit -m "feat(runtime): srcsetSanitize — WHATWG srcset grammar port"
```

---

### Task 2: srcset writer sinks (`gw.Srcset`, `gw.SrcsetVal`)

**Files:**
- Modify: `writer.go` (add `Srcset`, `SrcsetVal` methods)
- Test: `writer_test.go`

**Interfaces:**
- Consumes: `writeSrcset` (Task 1), existing `RawURL` type, `writeHTML`, `toStr`.
- Produces: `func (gw *Writer) Srcset(s string)`; `func (gw *Writer) SrcsetVal(v any)`.

- [ ] **Step 1: Write the failing test**

```go
func TestSrcsetSinks(t *testing.T) {
	var sb strings.Builder
	gw := &Writer{w: &sb}
	gw.Srcset("ok.jpg 1x, javascript:alert(1) 2x")
	if got := sb.String(); got != "ok.jpg 1x, about:invalid#gsx" {
		t.Fatalf("Srcset = %q", got)
	}
	// SrcsetVal: RawURL vouch passes verbatim (still attribute-escaped)
	sb.Reset()
	gw = &Writer{w: &sb}
	gw.SrcsetVal(RawURL("javascript:whatever 1x"))
	if got := sb.String(); got != "javascript:whatever 1x" {
		t.Fatalf("SrcsetVal(RawURL) = %q", got)
	}
	// SrcsetVal: non-RawURL string sanitizes
	sb.Reset()
	gw = &Writer{w: &sb}
	gw.SrcsetVal("javascript:alert(1) 1x")
	if got := sb.String(); got != "about:invalid#gsx" {
		t.Fatalf("SrcsetVal(string) = %q", got)
	}
}
```

(If `Writer`'s unexported field is not `w`, mirror the construction used by the neighboring `TestURLImageVal` in `writer_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestSrcsetSinks -v`
Expected: FAIL — `gw.Srcset undefined`.

- [ ] **Step 3: Implement in `writer.go`** (place beside `URLImageVal`)

```go
// Srcset writes s as a sanitized, escaped srcset attribute value: a
// comma-separated image-candidate list, each candidate URL sanitized as an
// image resource. Codegen emits it for srcset/imagesrcset attributes.
func (gw *Writer) Srcset(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeSrcset(gw.w, s)
}

// SrcsetVal is Srcset for a dynamically-typed bag value: a gsx.RawURL is the
// author's whole-value vouch and is emitted verbatim (still attribute-escaped);
// any other value is stringified then sanitized.
func (gw *Writer) SrcsetVal(v any) {
	if gw.err != nil {
		return
	}
	if r, ok := v.(RawURL); ok {
		gw.err = writeHTML(gw.w, string(r))
		return
	}
	gw.err = writeSrcset(gw.w, toStr(v))
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test . -run TestSrcsetSinks -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add writer.go writer_test.go
git commit -m "feat(runtime): gw.Srcset/SrcsetVal image-list sinks"
```

---

### Task 3: classify `srcset`/`imagesrcset` as URL

**Files:**
- Modify: `internal/attrclass/attrclass.go` (add both names to the builtin URL-exact set — the map whose entries include `"href": true, "src": true, …` near line 210)
- Test: `internal/attrclass/attrclass_test.go`

**Interfaces:**
- Consumes: existing builtin URL name map.
- Produces: `Classifier.Context("srcset") == CtxURL`; both names appear in `Classifier.URLExactNames()`.

- [ ] **Step 1: Write the failing test**

```go
func TestSrcsetClassifiedURL(t *testing.T) {
	c := Default() // use whatever constructor attrclass_test.go already uses for the builtin classifier
	for _, name := range []string{"srcset", "imagesrcset", "SrcSet"} {
		if got := c.Context(name); got != CtxURL {
			t.Errorf("Context(%q) = %v, want CtxURL", name, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/attrclass -run TestSrcsetClassifiedURL -v`
Expected: FAIL (`CtxPlain`, not `CtxURL`).

- [ ] **Step 3: Add the two names to the builtin URL-exact map**

In `attrclass.go`, in the builtin URL-name map literal (the one containing `"href": true, "src": true, "action": true, "formaction": true, "poster": true, … "xlink:href": true`), add:

```go
	"srcset": true, "imagesrcset": true,
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/attrclass -run TestSrcsetClassifiedURL -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/attrclass/attrclass.go internal/attrclass/attrclass_test.go
git commit -m "feat(attrclass): srcset/imagesrcset are URL-context"
```

---

### Task 4: codegen static path → `gw.Srcset`

Route the static-element URL path (`<img srcset={x}>`, `srcset=f"…"`) to the new sink via one `urlWriterMethod` arm, and pin it with corpus cases.

**Files:**
- Modify: `internal/codegen/emit.go` (`urlWriterMethod`, ~line 2641)
- Test: `internal/corpus/testdata/cases/srcset-sanitize/static_expr.txtar`, `static_fliteral.txtar`

**Interfaces:**
- Consumes: `attrclass.URLSink`, `SinkImage`; the `Context == CtxURL` static emit path already dispatches through `urlWriterMethod` at emit.go ~2773/2782.
- Produces: `urlWriterMethod(tag, "srcset") == "Srcset"`; static emit calls `_gsxgw.Srcset(...)`.

- [ ] **Step 1: Add the corpus cases (the failing test)**

Create `internal/corpus/testdata/cases/srcset-sanitize/static_expr.txtar`:

```
# srcset on a static element is a URL-list sink: a javascript: candidate is
# neutralized to about:invalid#gsx; safe candidates (incl data:image) pass.
-- input.gsx --
package views

component Pic(a string, b string) {
	<img srcset={ a } alt="x">
	<source srcset={ b }>
}
-- invoke --
Pic(PicProps{A: "ok.jpg 1x, javascript:alert(1) 2x", B: "data:image/png;base64,iVBOR 2x"})
-- diagnostics.golden --
-- render.golden --
<img srcset="ok.jpg 1x, about:invalid#gsx" alt="x"/><source srcset="data:image/png;base64,iVBOR 2x"/>
```

Create `internal/codegen/testdata/.../srcset-sanitize/static_fliteral.txtar` under the same `cases/srcset-sanitize/` dir:

```
# srcset as an f-literal (interpolation) is whole-value sanitized as a list.
-- input.gsx --
package views

component Pic(u string) {
	<img srcset=f"{u} 1x, fallback.jpg 2x">
}
-- invoke --
Pic(PicProps{U: "javascript:alert(1)"})
-- diagnostics.golden --
-- render.golden --
<img srcset="about:invalid#gsx, fallback.jpg 2x"/>
```

- [ ] **Step 2: Run to verify the cases fail**

Run: `go test ./internal/corpus -run TestCorpus/srcset-sanitize -v`
Expected: FAIL — render shows the raw `javascript:` candidate (current `CtxURL` path emits `gw.URL`, which whole-value-blocks the *entire* value rather than per-candidate; the golden won't match).

- [ ] **Step 3: Add the `urlWriterMethod` srcset arm**

In `internal/codegen/emit.go`, change `urlWriterMethod`:

```go
func urlWriterMethod(tag, name string) string {
	switch strings.ToLower(name) {
	case "srcset", "imagesrcset":
		return "Srcset"
	}
	if attrclass.URLSink(tag, name) == attrclass.SinkImage {
		return "URLImage"
	}
	return "URL"
}
```

- [ ] **Step 4: Regenerate and verify**

Run: `go test ./internal/corpus -run TestCorpus -update` then `go test ./internal/corpus -run TestCorpus`
Expected: the two new cases render as pinned; `coverage.golden` updates; no unrelated case drifts (only srcset cases are new).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/srcset-sanitize internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): static srcset routes to gw.Srcset"
```

---

### Task 5: codegen spread path + `gw.Spread` signature

Split URL-exact names three ways in the spread emitter and thread a `srcsetNames` set through `gw.Spread`, so a `srcset` key arriving via any bag sanitizes at the leaf.

**Files:**
- Modify: `attrs.go` (`Spread` signature + a srcset case)
- Modify: `internal/codegen/emit.go` (`emitSpreadCall`, ~2668)
- Test: `internal/corpus/testdata/cases/srcset-sanitize/spread.txtar`, `cond_nested_spread.txtar`

**Interfaces:**
- Consumes: `gw.SrcsetVal` (Task 2), `urlWriterMethod` (Task 4).
- Produces: `Spread(ctx, a Attrs, navNames, imageNames, srcsetNames, prefixes, excluded []string)`; `emitSpreadCall` emits 6 slice args.

- [ ] **Step 1: Add the corpus cases (the failing test)**

`internal/corpus/testdata/cases/srcset-sanitize/spread.txtar`:

```
# a srcset key arriving via a spread bag sanitizes at the leaf, per-candidate.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Pic(s string) {
	{{ b := gsx.Attrs{{Key: "srcset", Value: s}} }}
	<img { b... } alt="x">
}
-- invoke --
Pic(PicProps{S: "ok.jpg 1x, javascript:alert(1) 2x"})
-- diagnostics.golden --
-- render.golden --
<img alt="x" srcset="ok.jpg 1x, about:invalid#gsx"/>
```

`internal/corpus/testdata/cases/srcset-sanitize/cond_nested_spread.txtar`:

```
# a srcset key in a bag spread nested inside a cond-attr still sanitizes.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Pic(on bool, s string) {
	{{ b := gsx.Attrs{{Key: "srcset", Value: s}} }}
	<img alt="x" { if on { { b... } } }>
}
-- invoke --
Pic(PicProps{On: true, S: "javascript:alert(1) 1x"})
-- diagnostics.golden --
-- render.golden --
<img alt="x" srcset="about:invalid#gsx"/>
```

(Confirm attribute-order in the goldens by running `-update`; the exact bag-vs-static ordering above is indicative — pin whatever the emitter produces, then eyeball that the `srcset` value is sanitized.)

- [ ] **Step 2: Run to verify the cases fail**

Run: `go test ./internal/corpus -run TestCorpus/srcset-sanitize -v`
Expected: FAIL — `srcset` renders raw (routed through the `default` AttrValue branch of `Spread`, no sanitization).

- [ ] **Step 3: Extend `emitSpreadCall` (emit.go)**

```go
func emitSpreadCall(b *bytes.Buffer, expr, tag string, cls *attrclass.Classifier, excludedExpr string) {
	var navNames, imageNames, srcsetNames []string
	for _, name := range cls.URLExactNames() {
		switch urlWriterMethod(tag, name) {
		case "URLImage":
			imageNames = append(imageNames, name)
		case "Srcset":
			srcsetNames = append(srcsetNames, name)
		default:
			navNames = append(navNames, name)
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s, %s, %s, %s, %s, %s)\n",
		expr, goStringSliceLit(navNames), goStringSliceLit(imageNames),
		goStringSliceLit(srcsetNames), goStringSliceLit(cls.URLPrefixes()), excludedExpr)
}
```

- [ ] **Step 4: Extend `Spread` (attrs.go)** — add the param and a case before the nav case

Signature:

```go
func (gw *Writer) Spread(ctx context.Context, a Attrs, navNames, imageNames, srcsetNames, prefixes, excluded []string) {
```

Add this case in the `switch` immediately **after** the `imageNames` case and **before** the `navNames` case:

```go
		case attrNameExcluded(kv.Key, srcsetNames):
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.SrcsetVal(kv.Value)
			gw.writeStr(`"`)
```

- [ ] **Step 5: Regenerate and verify**

Run: `go test ./internal/corpus -run TestCorpus -update` then `go test ./internal/corpus -run TestCorpus && go build ./... && go test .`
Expected: all existing spread goldens regenerate with the extra `nil` (or populated) `srcsetNames` arg; new srcset cases pass; runtime builds. **Inspect the diff**: every `_gsxgw.Spread(` call gains one slice arg; no `render.golden` changes on non-srcset cases (the added set is `nil` there, behavior-identical).

- [ ] **Step 6: Commit**

```bash
git add attrs.go internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat(codegen): srcset sanitizes through element spreads"
```

---

### Task 6: docs — srcset is a sanitized URL-list sink

**Files:**
- Modify: `docs/guide/syntax/attributes.md`
- Modify: `docs/ROADMAP.md` (flip `srcset` from Deferred)

- [ ] **Step 1: Document srcset in `attributes.md`**

Add to the URL-attribute section a sentence: `srcset` (and `imagesrcset`) are parsed as comma-separated image-candidate lists (the WHATWG srcset grammar) and each candidate URL is sanitized as an image resource — a disallowed scheme in any candidate becomes `about:invalid#gsx` — identically for static attributes and spread bags. `data:image/*` candidates and fractional descriptors (`1.5x`) are preserved. (Wrap any literal `{{ }}` in a `::: v-pre` block.)

- [ ] **Step 2: Flip ROADMAP + record the structured-carrier principle**

In `docs/ROADMAP.md` (~line 890), change the `srcset` **Deferred** note to shipped, one line: "`srcset`/`imagesrcset` sanitized as URL-lists (static + spread), WHATWG grammar port." Add one line recording the structured-carrier principle (single-value URL/JS/CSS/HTML = faithful html/template ports; structured URL carriers = faithful WHATWG-grammar ports — `refreshContentSanitize` and `srcset`) and the two tracked follow-ups: **ping** (space-separated URL list, non-security) and **CSS `url()` in `style`** (separate context).

- [ ] **Step 3: Commit**

```bash
git add docs/guide/syntax/attributes.md docs/ROADMAP.md
git commit -m "docs: srcset is a sanitized URL-list sink"
```

---

### Task 7: `[]byte` URL/attr value renders as text (#78)

Align `toStr` with `anyRenderString`: a `[]byte` value stringifies to its bytes, not `fmt.Sprint`'s decimal array.

**Files:**
- Modify: `attrs.go` (or wherever `toStr` lives — grep `func toStr(`)
- Test: the `toStr` test file (grep `TestToStr`; add one if absent)

**Interfaces:**
- Produces: `toStr([]byte("hi")) == "hi"`.

- [ ] **Step 1: Write the failing test**

```go
func TestToStrBytes(t *testing.T) {
	if got := toStr([]byte("hi")); got != "hi" {
		t.Errorf("toStr([]byte) = %q, want %q", got, "hi")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run TestToStrBytes -v`
Expected: FAIL — `got = "[104 105]"`.

- [ ] **Step 3: Add the `[]byte` case to `toStr`**

```go
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case []string:
		return strings.Join(t, " ")
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test . -run TestToStrBytes -v && go test .`
Expected: PASS; no other root-package test regresses.

- [ ] **Step 5: Commit**

```bash
git add attrs.go *_test.go
git commit -m "fix(runtime): toStr renders []byte as text, matching anyRenderString"
```

---

### Task 8: at-most-one-spread counts nested-cond spreads (#78)

Extend the "at most one spread per element" rule so a second spread nested in a cond-attr is a generate-time error too (not just a top-level second). Decision from brainstorming: **extend the count** (one spread rule, no asterisk).

**Files:**
- Modify: `internal/codegen/emit.go` (add `walkSpreadAttrs`; use it at the element dispatch ~line 1631; simplify `bagSpreadIndex` to drop its now-redundant `second` return, ~line 1359)
- Test: `internal/corpus/testdata/cases/spread-sanitize/cond_nested_two_spreads.txtar`

**Interfaces:**
- Produces: `walkSpreadAttrs(attrs []ast.Attr) (first, second *ast.SpreadAttr)` — first two spreads in depth-first source order, descending into `CondAttr.Then`/`Else`. `bagSpreadIndex(attrs []ast.Attr) (idx int, found bool)`.

- [ ] **Step 1: Add the failing corpus case**

`internal/corpus/testdata/cases/spread-sanitize/cond_nested_two_spreads.txtar`:

```
# Two spreads on one element are a precedence-ambiguity error even when each is
# nested in a separate cond-attr (the count descends into cond branches).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Bad(c bool, d bool, x gsx.Attrs, y gsx.Attrs) {
	<a { if c { { x... } } } { if d { { y... } } }>hi</a>
}
-- invoke --
-- diagnostics.golden --
input.gsx:6:35: element with a spread { x... } cannot carry another spread { y... }; merge them into one spread ({ x.Merge(y)... } or { y.Merge(x)... }) so precedence is explicit
-- render.golden --
```

(The exact line:col in `diagnostics.golden` must match `walkSpreadAttrs`'s `second` position — regenerate with `-update`, then confirm the second spread `{ y... }` is the anchor.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/corpus -run TestCorpus/spread-sanitize/cond_nested_two_spreads -v`
Expected: FAIL — currently **no** error (both nested spreads emit; render is non-empty), so the missing diagnostic fails the case.

- [ ] **Step 3: Add `walkSpreadAttrs`** (near `bagSpreadIndex`, emit.go)

```go
// walkSpreadAttrs returns the first two spread attrs on an element in depth-first
// source order, descending into cond-attr branches. An element carries at most
// one spread total — top-level OR nested in a `{ if … { { x... } } }` cond-attr —
// because a second spread anywhere has no expressible precedence against the
// forwarding/guard machinery. The caller positions the ambiguity diagnostic at
// `second` and names both spread expressions.
func walkSpreadAttrs(attrs []ast.Attr) (first, second *ast.SpreadAttr) {
	var visit func(list []ast.Attr)
	visit = func(list []ast.Attr) {
		for _, a := range list {
			if second != nil {
				return
			}
			switch t := a.(type) {
			case *ast.SpreadAttr:
				if first == nil {
					first = t
				} else {
					second = t
				}
			case *ast.CondAttr:
				visit(t.Then)
				visit(t.Else)
			}
		}
	}
	visit(attrs)
	return first, second
}
```

- [ ] **Step 4: Use it at the dispatch; simplify `bagSpreadIndex`**

Replace the `bagSpreadIndex`-based second-detection at emit.go ~1631 with:

```go
		if first, second := walkSpreadAttrs(t.Attrs); second != nil {
			firstExpr := strings.TrimSpace(first.Expr)
			secondExpr := strings.TrimSpace(second.Expr)
			bag.Errorf(second.Pos(), second.End(), "attr-fallthrough",
				"element with a spread { %s... } cannot carry another spread { %s... }; merge them into one spread ({ %s.Merge(%s)... } or { %s.Merge(%s)... }) so precedence is explicit",
				firstExpr, secondExpr, firstExpr, secondExpr, secondExpr, firstExpr)
			return false
		}
		if splitIdx, found := bagSpreadIndex(t.Attrs); found {
			return emitManualSpreadElement(b, t, splitIdx, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		}
```

Then simplify `bagSpreadIndex` to drop the `second` return (it is now only the top-level split index):

```go
// bagSpreadIndex returns the index of the (single) top-level element spread and
// whether one is present. Enforcing at-most-one-spread — including nested-cond
// spreads — is done by walkSpreadAttrs before this is consulted.
func bagSpreadIndex(attrs []ast.Attr) (idx int, found bool) {
	for i, a := range attrs {
		if _, ok := a.(*ast.SpreadAttr); ok {
			return i, true
		}
	}
	return -1, false
}
```

- [ ] **Step 5: Regenerate goldens and verify**

Run: `go test ./internal/corpus -run TestCorpus -update` then `go test ./internal/corpus -run TestCorpus && go test ./internal/codegen`
Expected: new case passes; the existing `two_spreads_error.txtar` diagnostic is **unchanged** (walkSpreadAttrs yields the same two top-level spreads in the same order); no other drift.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "fix(codegen): at-most-one-spread counts nested-cond spreads"
```

---

### Task 9: attrs-only sig test debt (#71)

Add the missing `TypeParams` rejection case and de-fabricate the underlying-sig wrapper.

**Files:**
- Modify: `internal/codegen/attrsonly_test.go` (`TestAttrsOnlySig`)

**Interfaces:**
- Consumes: `attrsOnlySig(t types.Type) (variadic, needsConvert, ok bool)` — already rejects `sig.TypeParams().Len() != 0`; this task pins that path.

- [ ] **Step 1: Read `TestAttrsOnlySig`** and its fabricated-`gsx`-package `types` construction. Identify how it builds a `*types.Signature` (via `types.NewSignatureType`).

- [ ] **Step 2: Add a generic-signature rejection subtest**

Construct a signature with a non-empty type-parameter list — one type param `T`, param `gsx.Attrs`, result `gsx.Node` — and assert `attrsOnlySig` returns `ok == false`:

```go
	t.Run("type-parameterized signature rejected", func(t *testing.T) {
		tp := types.NewTypeParam(types.NewTypeName(token.NoPos, nil, "T", nil), types.NewInterfaceType(nil, nil))
		sig := types.NewSignatureType(
			nil, nil, []*types.TypeParam{tp},
			types.NewTuple(types.NewVar(token.NoPos, nil, "a", attrsType)), // attrsType = the fabricated gsx.Attrs used elsewhere in this test
			types.NewTuple(types.NewVar(token.NoPos, nil, "", nodeType)),   // nodeType = fabricated gsx.Node
			false,
		)
		if _, _, ok := attrsOnlySig(sig); ok {
			t.Error("attrsOnlySig accepted a type-parameterized signature")
		}
	})
```

(Reuse the exact `attrsType`/`nodeType`/named-type helpers already defined in the test file; adjust names to match.)

- [ ] **Step 3: Move the named-sig-underlying wrapper out of the fabricated `gsx` package** (cosmetic): the test's helper type whose underlying is a signature should live in a neutral fabricated package, not the fake `gsx` package. Adjust the `types.NewPackage(path, name)` used for that wrapper.

- [ ] **Step 4: Run**

Run: `go test ./internal/codegen -run TestAttrsOnlySig -v`
Expected: PASS including the new subtest.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/attrsonly_test.go
git commit -m "test(codegen): pin attrsOnlySig TypeParams rejection; de-fabricate wrapper"
```

---

### Task 10: extract `parseGsxTypeDecls`; defined-type `TestTypeNames` case (#71)

`gsxChunkTypeNames` and `gsxStructDecls` (both in `internal/codegen/byo.go`) duplicate ~30 lines of decl scan/walk. Extract the shared scan.

**Files:**
- Modify: `internal/codegen/byo.go`
- Test: `internal/codegen/byo_syntactic_test.go` (`TestTypeNames`)

**Interfaces:**
- Produces: `parseGsxTypeDecls(...)` — a shared helper returning the parsed type decls both callers walk. (Determine the exact signature by reading the two functions' common prefix — same `*ast.File`/chunk parse + `GenDecl`/`TypeSpec` iteration.)

- [ ] **Step 1: Read `gsxChunkTypeNames` and `gsxStructDecls`** in `byo.go`; identify the identical scan/walk prefix (parse → range decls → `*ast.GenDecl` with `token.TYPE` → `*ast.TypeSpec`).

- [ ] **Step 2: Add a failing/uncovered defined-type case to `TestTypeNames`**

Add an input exercising `type X int` (a defined type whose underlying is not a struct) and assert its name is reported:

```go
	{name: "defined type", src: "package p\ntype X int\n", want: []string{"X"}},
```

(Match the existing table's field names/shape in `byo_syntactic_test.go`.)

- [ ] **Step 3: Run to confirm current behavior**

Run: `go test ./internal/codegen -run TestTypeNames -v`
Expected: PASS or FAIL depending on current coverage — if it already passes, the case is a pin; if it fails, the extraction in Step 4 must preserve defined-type reporting.

- [ ] **Step 4: Extract `parseGsxTypeDecls`** and have both `gsxChunkTypeNames` and `gsxStructDecls` call it. Keep each function's post-scan specialization (names-only vs struct-fields) at the call site.

- [ ] **Step 5: Run the byo suite**

Run: `go test ./internal/codegen -run 'TestTypeNames|TestByo|TestStruct' -v && go test ./internal/codegen`
Expected: PASS; no golden drift (pure refactor).

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/byo.go internal/codegen/byo_syntactic_test.go
git commit -m "refactor(codegen): share parseGsxTypeDecls; pin defined-type name scan"
```

---

### Task 11: LSP hover for attrs-only component-value tags (#71)

Go-to-def already resolves attrs-only value tags (`internal/lsp/definition_attrsonly.go`); hover shows nothing. Add hover parity.

**Files:**
- Modify: `internal/lsp/hover.go`
- Test: `internal/lsp/hover_test.go` (mirror an existing hover test; grep `TestHover`)

**Interfaces:**
- Consumes: `isAttrsOnlyValueType` / the attrs-only value resolution already used by `definition_attrsonly.go` for go-to-def.
- Produces: hover on an attrs-only value tag returns the value's declaration/signature markup.

- [ ] **Step 1: Read `internal/lsp/hover.go` and `definition_attrsonly.go`.** Identify the tag-position branch that go-to-def added and where hover's position dispatch would slot the same resolution.

- [ ] **Step 2: Write a failing hover test**

Mirror an existing `TestHover*` case: place the cursor on an attrs-only component-value tag and assert the hover result is non-empty and names the value's type/signature. (Reuse the attrs-only fixture from `definition_attrsonly_test.go`.)

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/lsp -run TestHover -v`
Expected: FAIL — empty hover on the attrs-only tag.

- [ ] **Step 4: Add the hover branch** reusing the same resolution go-to-def uses (the `isAttrsOnlyValueType` seam), formatting the declaration like neighboring hover cases.

- [ ] **Step 5: Run**

Run: `go test ./internal/lsp -run TestHover -v && go test ./internal/lsp`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/lsp/hover.go internal/lsp/hover_test.go
git commit -m "feat(lsp): hover on attrs-only component-value tags"
```

---

## Final verification (before merge)

- [ ] `make ci` green (build/vet/test both modules, examples drift, gofmt + gsx fmt).
- [ ] `make lint` green.
- [ ] Corpus verified without `-update` (`go test ./internal/corpus -run TestCorpus`).
- [ ] Independent adversarial review (build throwaway probe programs — real `Render()` of `srcset` XSS vectors through static + spread + cond-nested paths; confirm `javascript:` candidates neutralize and `data:image` passes), per CLAUDE.md subsystem-merge gate.
- [ ] Sibling-repo check: no surface-syntax change, so tree-sitter-gsx / vscode-gsx / CodeMirror / `gsx fmt` need no update — confirm by grepping the plan produced no new tokens.

## Self-review notes

- **Spec coverage:** srcset port (T1), sinks (T2), classifier (T3), static path (T4), spread path (T5), docs (T6); companion cleanups nested-cond count (T8), `[]byte` (T7), attrs-only sig (T9), parseGsxTypeDecls + defined-type (T10), LSP hover (T11). All spec sections mapped.
- **Type consistency:** `Spread` gains `srcsetNames []string` (T5) — the same name is used in `emitSpreadCall`'s literal and the `attrs.go` signature. `walkSpreadAttrs` (T8) returns `(first, second *ast.SpreadAttr)`; `bagSpreadIndex` simplified to `(idx int, found bool)` and its sole caller (T8 dispatch) matches.
- **Open item resolved:** nested-cond count → extend (T8), not a doc footnote.
