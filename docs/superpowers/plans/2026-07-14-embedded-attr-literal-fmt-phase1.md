# Embedded-Attr JS/CSS Literal Formatting (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `gsx fmt` re-indent the body of a multi-line `js`…``/`css`…``/`js"…"`/`css"…"` **attribute value**, the way it already re-indents `<script>`/`<style>` element bodies.

**Architecture:** Add an `*ast.EmbeddedAttr` case to the printer's `attrDoc` seam. For a JS/CSS literal whose body spans lines, extract its segments+holes, run the existing `jsfmt`/`cssfmt` re-indenter through a new `rawfmt.FormatString` (the placeholder→format→restore core, with a delimiter-escaping hook applied before hole restoration), then build a `pretty.Doc` that attaches the opening delimiter to the first body line, breaks intermediate lines at the attribute's own indent, and attaches the closing delimiter to the last line. Single-line values and non-JS/CSS (`f`…``) literals are untouched (verbatim, as today).

**Tech Stack:** Go 1.26.1; `internal/rawfmt`, `internal/jsfmt`, `internal/cssfmt`, `internal/printer`, `internal/gsxfmt`; `internal/pretty` doc engine.

## Global Constraints

- Go pinned to `GO_VERSION` in `.github/workflows/ci.yml` (currently **1.26.1**) — a different minor re-introduces gofmt drift.
- Runtime (root package) stays standard-library-only; this work is entirely in `internal/**` tooling, which may use `golang.org/x/tools` (none needed here).
- **Re-indent only, never reflow** — a single-line value never becomes multi-line and token layout is never rewritten (this is what keeps JS ASI safe). Achieved for free by `jsfmt`/`cssfmt`, which only normalize leading indentation.
- **No new config knob** — formatting is on whenever `p.jsFmt`/`p.cssFmt` is set, the same gate that governs `<script>`/`<style>` body formatting. `Fprint` supplies the defaults.
- **Idempotence is the correctness gate.** `fmt(fmt(x)) == fmt(x)`. It holds because `jsfmt`/`cssfmt` *normalize* leading whitespace (strip + re-apply by brace depth), so the base indentation the pretty engine bakes into the value on pass 1 is stripped again on pass 2. The `internal/gsxfmt` corpus harness asserts this automatically (`corpus_test.go:164`).
- **Faithful escaping** — delimiter / `@{` re-escaping reuses the existing `writeEmbeddedLiteralText`; never approximate it.
- Run `make check` (inner loop) before declaring done; `make ci` mirrors GitHub CI.

## File Structure

- `internal/rawfmt/rawfmt.go` — MODIFY: extract `FormatString` (exported) from `Format`; add optional `escape func(string) string` applied before hole restoration.
- `internal/rawfmt/rawfmt_test.go` — MODIFY/CREATE: unit tests for `FormatString` (escape-before-restore, sentinel safety, identity == old `Format`).
- `internal/printer/printer.go` — MODIFY: `attrDoc` gains an `*ast.EmbeddedAttr` case; add helpers `embeddedAttrDoc`, `embeddedSegmentsMultiline`, `embeddedAttrBody`, `embeddedHoleString`, `embeddedAttrValueDoc`.
- `internal/printer/embedded_attr_fmt_test.go` — CREATE: targeted `normPrint` assertions (indent, hole preserved, delimiter round-trip, single-line stays inline, braced+stages, CSS, fallback, x-data end-to-end).
- `internal/gsxfmt/testdata/cases/embed_attr_*.txtar` — CREATE: golden cases (auto idempotence + reparse via the harness).
- `docs/guide/**` (formatting page) — MODIFY: one concise line that `js`/css`` attribute values are re-indented like `<script>`/`<style>` bodies.

---

### Task 1: `rawfmt.FormatString` — format core with an escape hook

**Files:**
- Modify: `internal/rawfmt/rawfmt.go:69-83` (the `Format` function)
- Test: `internal/rawfmt/rawfmt_test.go`

**Interfaces:**
- Consumes: existing unexported `buildPlaceholdered`, `safeFormat`, `restore`, `reindent` (same package).
- Produces:
  - `func FormatString(segments, holes []string, f Formatter, escape func(string) string) (string, bool)` — placeholder→format→(escape)→restore, returns the formatted source string (no re-indent). `escape` may be nil.
  - `func Format(segments, holes []string, f Formatter) (pretty.Doc, bool)` — unchanged signature/behavior, now delegates to `FormatString(…, nil)` then `reindent`.

- [ ] **Step 1: Write the failing test**

Add to `internal/rawfmt/rawfmt_test.go`:

```go
func TestFormatStringEscapeBeforeRestore(t *testing.T) {
	// identity formatter: returns input unchanged.
	id := func(src []byte) ([]byte, error) { return src, nil }
	// One hole between two segments; the segment text contains a `"` that the
	// escaper must backslash — but the restored hole must NOT be escaped.
	segments := []string{`say "hi" `, ` end`}
	holes := []string{`@{name}`}
	escape := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	got, ok := FormatString(segments, holes, id, escape)
	if !ok {
		t.Fatal("FormatString returned ok=false")
	}
	want := `say \"hi\" @{name} end`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "__gsxhole") {
		t.Fatalf("sentinel leaked: %q", got)
	}
}

func TestFormatStringNilEscapeMatchesRaw(t *testing.T) {
	id := func(src []byte) ([]byte, error) { return src, nil }
	got, ok := FormatString([]string{"a ", " b"}, []string{"@{x}"}, id, nil)
	if !ok || got != "a @{x} b" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestFormatStringArityMismatch(t *testing.T) {
	id := func(src []byte) ([]byte, error) { return src, nil }
	if _, ok := FormatString([]string{"a"}, []string{"@{x}"}, id, nil); ok {
		t.Fatal("expected ok=false on arity mismatch")
	}
}
```

Ensure `"strings"` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rawfmt/ -run TestFormatString -v`
Expected: FAIL — `undefined: FormatString`.

- [ ] **Step 3: Refactor `Format` and add `FormatString`**

Replace `internal/rawfmt/rawfmt.go:69-83` (the `Format` function body) with:

```go
// FormatString runs the placeholder → format → (escape) → restore pipeline and
// returns the formatted source with holes restored, WITHOUT re-indenting into a
// Doc (the caller shapes it). escape, if non-nil, is applied to the formatted
// text BEFORE hole restoration — used to re-escape an embedded literal's
// delimiter. Escaping the whole placeholdered string is safe: the hole sentinels
// are collision-free identifiers containing no escapable character, so escaping
// never corrupts one, and the restored holes are inserted afterward and stay
// unescaped. ok=false on arity mismatch, formatter error/panic, or a hole-restore
// mismatch (the caller falls back to verbatim).
func FormatString(segments, holes []string, f Formatter, escape func(string) string) (string, bool) {
	if len(segments) != len(holes)+1 {
		return "", false
	}
	placeholdered, prefix := buildPlaceholdered(segments, holes)
	formatted, err := safeFormat(f, placeholdered)
	if err != nil {
		return "", false
	}
	out := string(formatted)
	if escape != nil {
		out = escape(out)
	}
	restored, ok := restore(out, prefix, holes)
	if !ok {
		return "", false
	}
	return restored, true
}

// Format renders a raw-text element body. segments and holes interleave with
// len(segments) == len(holes)+1; each holes[i] is the already-rendered gsx
// hole. Result ok=false → caller renders verbatim.
func Format(segments, holes []string, f Formatter) (pretty.Doc, bool) {
	restored, ok := FormatString(segments, holes, f, nil)
	if !ok {
		return pretty.Doc{}, false
	}
	return reindent(restored), true
}
```

(Keep the existing doc comment above `Format` if it duplicates; the block above already carries it.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rawfmt/ -v`
Expected: PASS (new tests + existing rawfmt tests).

- [ ] **Step 5: Verify no regression in body formatting**

Run: `go test ./internal/printer/ -run 'Script|Style' -v`
Expected: PASS — `<script>`/`<style>` body formatting unchanged (still routes through `Format`).

- [ ] **Step 6: Commit**

```bash
git add internal/rawfmt/rawfmt.go internal/rawfmt/rawfmt_test.go
git commit -m "refactor(rawfmt): extract FormatString with pre-restore escape hook"
```

---

### Task 2: Printer — format multi-line js``/css`` attribute values

**Files:**
- Modify: `internal/printer/printer.go` — `attrDoc` (`:530-532`, the `default` case) + new helpers.
- Test: `internal/printer/embedded_attr_fmt_test.go` (create).

**Interfaces:**
- Consumes: `rawfmt.FormatString` (Task 1); existing `embeddedDelim`, `embeddedLangName`, `pipeStageStr`, `fmtExpr`, `markupInlineString`, `writeEmbeddedLiteralText` (all in package `printer`); `p.jsFmt`, `p.cssFmt`.
- Produces: `(*printer).embeddedAttrDoc(v *ast.EmbeddedAttr) (pretty.Doc, bool)` and pure helpers `embeddedSegmentsMultiline`, `embeddedAttrBody`, `embeddedHoleString`, `embeddedAttrValueDoc`.

- [ ] **Step 1: Write the failing tests**

Create `internal/printer/embedded_attr_fmt_test.go`:

```go
package printer

import (
	"strings"
	"testing"
)

// A multi-line js"…" attribute value: body one level under the attribute,
// brace-nested deeper, closing delimiter attached to the last line.
func TestEmbeddedAttrJSReindented(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nopen: false,\nitems() {\nreturn 1;\n}\n}\" class=\"c\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	// <form> at depth 1 → attrs at depth 2. Opening `{` attaches to js".
	if !strings.Contains(out, "x-data=js\"{") {
		t.Fatalf("opening delimiter+brace should attach:\n%s", out)
	}
	// body one level under the attribute (2 tabs base + 1 jsfmt = 3 tabs).
	if !strings.Contains(out, "\t\t\topen: false,") {
		t.Fatalf("body not one level under attribute:\n%s", out)
	}
	if !strings.Contains(out, "\t\t\t\treturn 1;") {
		t.Fatalf("nested body not two levels under attribute:\n%s", out)
	}
	// closing brace dedents to the attribute level and the delimiter attaches.
	if !strings.Contains(out, "\t\t}\"") {
		t.Fatalf("closing delimiter should attach at attribute indent:\n%s", out)
	}
	// following attribute survives.
	if !strings.Contains(out, "class=\"c\"") {
		t.Fatalf("trailing attribute lost:\n%s", out)
	}
}

func TestEmbeddedAttrIdempotent(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nopen: false,\n}\"/>\n}\n"
	once, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normPrint(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestEmbeddedAttrHolePreserved(t *testing.T) {
	src := "package p\n\ncomponent C(id string) {\n\t<form x-data=js\"{\nurl: '@{id}',\nk: 1,\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@{id}") || strings.Contains(out, "__gsxhole") {
		t.Fatalf("hole not preserved / sentinel leaked:\n%s", out)
	}
}

// A body containing the literal's own delimiter must round-trip (escaped).
func TestEmbeddedAttrDelimiterRoundTrip(t *testing.T) {
	// js"…" value whose JS contains a double-quoted string → the inner `"` is
	// escaped in source; after fmt it must still be escaped and re-parse.
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nmsg: \\\"hi\\\",\nk: 1,\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `msg: \"hi\",`) {
		t.Fatalf("inner delimiter not re-escaped:\n%s", out)
	}
	// re-parse must succeed (idempotence test covers structural stability).
	if _, err := normPrint(t, out); err != nil {
		t.Fatalf("reparse failed: %v\n%s", err, out)
	}
}

// A single-line js"…" value is left inline, unchanged.
func TestEmbeddedAttrSingleLineStaysInline(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{open:false}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x-data=js\"{open:false}\"") {
		t.Fatalf("single-line value should stay inline:\n%s", out)
	}
}

// CSS literal, backtick delimiter, multi-line.
func TestEmbeddedAttrCSSReindented(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div style=css`\ncolor: red;\nmargin: 0;\n`/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\t\tcolor: red;") {
		t.Fatalf("css body not re-indented under attribute:\n%s", out)
	}
}

// End-to-end: the motivating Alpine x-data blob converted to js"…".
func TestEmbeddedAttrXDataEndToEnd(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nopen: false,\nactive: -1,\nmoveActive(d) {\nif (!this.open) return;\n},\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"x-data=js\"{",
		"\t\t\topen: false,",
		"\t\t\tmoveActive(d) {",
		"\t\t\t\tif (!this.open) return;",
		"\t\t\t},",
		"\t\t}\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/printer/ -run TestEmbeddedAttr -v`
Expected: FAIL — values still emitted verbatim inline (e.g. `open: false` not re-indented; `x-data=js"{` not present because the whole value stays on one line via `attrInline`).

- [ ] **Step 3: Add the helpers**

Add near `writeEmbeddedAttrSegments` in `internal/printer/printer.go` (after `embeddedLiteralString`, ~`:1211`):

```go
// embeddedSegmentsMultiline reports whether an embedded literal's body spans
// more than one source line (only then does fmt re-indent it; single-line values
// stay inline, unchanged).
func embeddedSegmentsMultiline(segs []ast.Markup) bool {
	for _, n := range segs {
		if t, ok := n.(*ast.Text); ok && strings.Contains(t.Value, "\n") {
			return true
		}
	}
	return false
}

// embeddedAttrBody splits an embedded-attribute literal's Segments into raw text
// segments and rendered holes for rawfmt, mirroring nodesToBody but emitting the
// TIGHT `@{expr}` hole form used inside attribute literals (not the spaced body
// form). segments and holes interleave with len(segments) == len(holes)+1.
func embeddedAttrBody(segs []ast.Markup) (segments, holes []string) {
	var cur strings.Builder
	for _, n := range segs {
		switch v := n.(type) {
		case *ast.Text:
			cur.WriteString(v.Value)
		case *ast.Interp:
			segments = append(segments, cur.String())
			cur.Reset()
			holes = append(holes, embeddedHoleString(v))
		default:
			cur.WriteString(markupInlineString(n))
		}
	}
	segments = append(segments, cur.String())
	return segments, holes
}

// embeddedHoleString renders one `@{ expr |> stage }` hole tight (`@{expr}`),
// matching writeEmbeddedAttrSegments.
func embeddedHoleString(v *ast.Interp) string {
	var b strings.Builder
	b.WriteString("@{")
	b.WriteString(fmtExpr(v.Expr))
	for _, s := range v.Stages {
		b.WriteString(" |> ")
		b.WriteString(pipeStageStr(s))
	}
	b.WriteString("}")
	return b.String()
}

// embeddedAttrValueDoc builds the Doc for a multi-line embedded-literal attribute
// value. opener is the text up to and including the opening delimiter (e.g.
// `x-data=js"` or `x-data={js"`); formatted is the re-indented body (leading and
// trailing blank lines already trimmed); closer is the closing delimiter plus any
// `|> stage` / `}` (e.g. `"` or `" |> f}`). The first body line attaches to the
// opener, intermediate lines break at the attribute's own indent level (the
// formatter's per-line leading tabs supply the brace-depth nesting), and the
// closer attaches to the last line. Blank lines emit as bare "\n" so they never
// carry trailing tabs (idempotence).
func embeddedAttrValueDoc(opener, formatted, closer string) pretty.Doc {
	lines := strings.Split(formatted, "\n")
	parts := make([]pretty.Doc, 0, len(lines)*2+2)
	parts = append(parts, pretty.Text(opener+lines[0]))
	for _, ln := range lines[1:] {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			parts = append(parts, pretty.Text("\n"))
			continue
		}
		parts = append(parts, pretty.HardLine, pretty.Text(ln))
	}
	parts = append(parts, pretty.Text(closer))
	return pretty.Concat(parts...)
}

// embeddedAttrDoc re-indents a multi-line js`/css` attribute value's body with
// the configured JS/CSS formatter. ok=false (caller falls back to verbatim
// inline) for a non-JS/CSS literal, a nil formatter, a single-line body, or any
// formatter failure.
func (p *printer) embeddedAttrDoc(v *ast.EmbeddedAttr) (pretty.Doc, bool) {
	var f rawfmt.Formatter
	switch v.Lang {
	case ast.EmbeddedJS:
		f = p.jsFmt
	case ast.EmbeddedCSS:
		f = p.cssFmt
	default: // ast.EmbeddedText — f`…`: no sublanguage, nothing to format.
		return pretty.Doc{}, false
	}
	if f == nil || !embeddedSegmentsMultiline(v.Segments) {
		return pretty.Doc{}, false
	}
	segments, holes := embeddedAttrBody(v.Segments)
	delim := embeddedDelim(v.DoubleQuoted)
	escape := func(s string) string {
		var b strings.Builder
		writeEmbeddedLiteralText(&b, s, delim)
		return b.String()
	}
	formatted, ok := rawfmt.FormatString(segments, holes, f, escape)
	if !ok {
		return pretty.Doc{}, false
	}
	formatted = strings.Trim(formatted, "\n")
	if !strings.Contains(formatted, "\n") {
		// Formatter collapsed the body to one line — keep the inline form.
		return pretty.Doc{}, false
	}
	braced := v.Braced || len(v.Stages) > 0
	var opener strings.Builder
	opener.WriteString(v.Name)
	opener.WriteString("=")
	if braced {
		opener.WriteString("{")
	}
	opener.WriteString(embeddedLangName(v.Lang))
	opener.WriteByte(delim)
	var closer strings.Builder
	closer.WriteByte(delim)
	for _, s := range v.Stages {
		closer.WriteString(" |> ")
		closer.WriteString(pipeStageStr(s))
	}
	if braced {
		closer.WriteString("}")
	}
	return embeddedAttrValueDoc(opener.String(), formatted, closer.String()), true
}
```

- [ ] **Step 4: Wire the seam in `attrDoc`**

In `internal/printer/printer.go`, change the `default` case of `attrDoc` (`:530-531`) to first try the embedded path:

```go
	case *ast.EmbeddedAttr:
		if doc, ok := p.embeddedAttrDoc(v); ok {
			return doc
		}
		return pretty.Text(attrInline(a))
	default:
		return pretty.Text(attrInline(a))
```

(Insert the `case *ast.EmbeddedAttr:` immediately before `default:`.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/printer/ -run TestEmbeddedAttr -v`
Expected: PASS (all 7 tests).

- [ ] **Step 6: Run the full printer suite (no regressions)**

Run: `go test ./internal/printer/`
Expected: PASS — including the existing `embedded`/`Script`/`Style`/idempotence/faithfulness suites.

- [ ] **Step 7: Commit**

```bash
git add internal/printer/printer.go internal/printer/embedded_attr_fmt_test.go
git commit -m "feat(fmt): re-indent multi-line js\`/css\` attribute values"
```

---

### Task 3: gsxfmt corpus goldens (idempotence + reparse for free)

**Files:**
- Create: `internal/gsxfmt/testdata/cases/embed_attr_js_multiline.txtar`
- Create: `internal/gsxfmt/testdata/cases/embed_attr_css_multiline.txtar`
- Create: `internal/gsxfmt/testdata/cases/embed_attr_js_hole.txtar`
- Create: `internal/gsxfmt/testdata/cases/embed_attr_single_line_inline.txtar`
- Test: `internal/gsxfmt/corpus_test.go` (harness, no edit — it globs the cases)

**Interfaces:**
- Consumes: the Task 2 printer behavior (the corpus routes nil formatters → `printer.Fprint` → default `jsfmt`/`cssfmt`).
- Produces: pinned `fmt.golden` for each case; the harness asserts golden-match, idempotence (`:164`), and reparse (`:168`).

- [ ] **Step 1: Write the case inputs (goldens filled by -update)**

Create `internal/gsxfmt/testdata/cases/embed_attr_js_multiline.txtar`:

```
A multi-line js"…" attribute value is re-indented under its attribute: the
opening delimiter+brace attach to the js" line, the body sits one level deeper
(brace-nested deeper still), and the closing brace+delimiter dedent back to the
attribute's indent. Re-indent only — no reflow.

-- input.gsx --
package p

component C() {
	<form x-data=js"{
open: false,
items() {
return 1;
}
}" class="c"/>
}
```

Create `internal/gsxfmt/testdata/cases/embed_attr_css_multiline.txtar`:

```
A multi-line css`…` attribute value is re-indented under its attribute, the same
way a <style> body is.

-- input.gsx --
package p

component C() {
	<div style=css`
color: red;
margin: 0;
`/>
}
```

Create `internal/gsxfmt/testdata/cases/embed_attr_js_hole.txtar`:

```
A hole inside a multi-line js"…" attribute value survives re-indentation (the
@{expr} form is preserved tight; no sentinel leaks).

-- input.gsx --
package p

component C(id string) {
	<form x-data=js"{
url: '@{id}',
k: 1,
}"/>
}
```

Create `internal/gsxfmt/testdata/cases/embed_attr_single_line_inline.txtar`:

```
A single-line js"…" attribute value stays inline, byte-for-byte — fmt never
force-wraps an embedded literal.

-- input.gsx --
package p

component C() {
	<form x-data=js"{open:false}"/>
}
```

- [ ] **Step 2: Run to verify the cases fail (missing golden)**

Run: `go test ./internal/gsxfmt/ -run TestFmtCorpus`
Expected: FAIL — `case ... has no fmt.golden — run with -update`.

- [ ] **Step 3: Generate goldens**

Run: `go test ./internal/gsxfmt/ -run TestFmtCorpus -update`
Expected: writes `fmt.golden` into each new `.txtar`.

- [ ] **Step 4: Inspect the goldens by eye**

Run: `sed -n '/-- fmt.golden --/,$p' internal/gsxfmt/testdata/cases/embed_attr_js_multiline.txtar`
Expected: the `x-data=js"{` line, body at 3 tabs, `return 1;` at 4 tabs, `}"` at 2 tabs, `class="c"` preserved. If wrong, the bug is in Task 2 — fix there and regenerate.

- [ ] **Step 5: Verify without -update (golden + idempotence + reparse)**

Run: `go test ./internal/gsxfmt/ -run TestFmtCorpus -v`
Expected: PASS — each case matches its golden, is idempotent on its golden, and re-parses.

- [ ] **Step 6: Commit**

```bash
git add internal/gsxfmt/testdata/cases/embed_attr_*.txtar
git commit -m "test(fmt): corpus goldens for js/css attribute-value re-indent"
```

---

### Task 4: Docs + full verification

**Files:**
- Modify: the formatting page under `docs/guide/**` (find with grep in Step 1).
- Verify: whole repo via `make check`.

**Interfaces:**
- Consumes: everything above.
- Produces: one documented sentence; a clean `make check`.

- [ ] **Step 1: Find the formatting doc that mentions script/style body formatting**

Run: `grep -rln "gsx fmt\|re-indent\|<script>\|formatter" docs/guide/ | head`
Then open the formatting/tooling page.

- [ ] **Step 2: Add one concise line**

Add a single sentence near the `<script>`/`<style>` body-formatting note, e.g.:

```markdown
`js`…`` / `css`…`` **attribute** values are re-indented the same way as
`<script>`/`<style>` bodies — a multi-line value's body is laid out one level
under its attribute. Plain `"…"` string attributes are left verbatim.
```

If the surrounding prose uses literal `{{ }}`, wrap that block in `::: v-pre` (VitePress parses `{{ }}` as Vue interpolation).

- [ ] **Step 3: Run the inner-loop verification**

Run: `make check`
Expected: build/vet/test both modules pass; `gofmt` + `gsx fmt` clean; examples drift clean.

- [ ] **Step 4: Commit**

```bash
git add docs/guide
git commit -m "docs: note js/css attribute-value re-indentation in gsx fmt"
```

---

## Self-Review

**Spec coverage (Phase 1 sections only):**
- Seam `attrDoc` + `*ast.EmbeddedAttr` case → Task 2 Step 4. ✓
- Single-line inline / multi-line re-indent → Task 2 (`embeddedSegmentsMultiline`), test `TestEmbeddedAttrSingleLineStaysInline`. ✓
- Extract segments+holes (tight `@{expr}`) → `embeddedAttrBody`/`embeddedHoleString`, Task 2. ✓
- Delimiter escaping before restore → `rawfmt.FormatString` escape hook (Task 1) + escaper closure (Task 2); tests `TestFormatStringEscapeBeforeRestore`, `TestEmbeddedAttrDelimiterRoundTrip`. ✓
- Attr-anchored re-indent (attach first/last) → `embeddedAttrValueDoc`, Task 2; test `TestEmbeddedAttrJSReindented`. ✓
- Whole-literal `|> stages` + braced form → `closer`/`opener` construction, Task 2. ✓
- Fallback on formatter failure → `embeddedAttrDoc` returns `ok=false`; the `attrInline` fallback in Step 4. (Note: an unparseable body cannot easily be forced because `jsfmt`/`cssfmt` are lexer-based and rarely error; the `ok=false` path is still exercised by the non-JS/CSS and single-line early returns.) ✓
- Idempotence gate → gsxfmt corpus harness + `TestEmbeddedAttrIdempotent`. ✓
- No new config → nothing added; gated on `p.jsFmt`/`p.cssFmt`. ✓
- Phase 2 (minify) → deliberately OUT of this plan (separate plan after Phase 1 lands).

**Placeholder scan:** none — every code step carries full code; every run step carries a command + expected output.

**Type consistency:** `FormatString(segments, holes []string, f Formatter, escape func(string) string) (string, bool)` used identically in Task 1 (def) and Task 2 (call). `embeddedAttrDoc` returns `(pretty.Doc, bool)`, consumed by the `attrDoc` case. `embeddedAttrValueDoc(opener, formatted, closer string)` matches its call. Helper names (`embeddedSegmentsMultiline`, `embeddedAttrBody`, `embeddedHoleString`) are consistent between definition and use.
