# Fix: embedded-JS/CSS formatting corrupts multi-line-token interiors

**Goal:** Stop `gsx fmt` from injecting indentation into the interior of multi-line JS/CSS tokens (template literals, block comments) when re-indenting `<script>`/`<style>` bodies AND `js`…``/`css`…`` attribute values. Restore idempotence and value-preservation for those bodies.

**Root cause:** `internal/reindent.Reindent` is correct — an `Opaque` token (string/template/regex/comment) keeps its internal `\n`s as *content within one logical line*, so only the logical-line start is indented. But the **outer Doc-building layer** flattens that to a string and splits on **every** `\n` (`rawfmt.Format`→`reindent`, and `printer.embeddedAttrValueDoc`), emitting a managed `HardLine` (which adds base indent) for each physical line — including Opaque interiors. Verified: `x-data=js"{ html:` `` `<div>\nhi\n</div>` `` `}"` re-indents `hi`/`</div>` and is non-idempotent.

**Fix:** thread **logical lines** from the tokenizer to the Doc builder. A logical line may contain internal `\n`s (Opaque content); emitted as ONE `pretty.Text`, the pretty engine writes those newlines verbatim with NO managed indent (`internal/pretty/print.go:57`), while a `HardLine` precedes only the logical-line start. This fixes both surfaces uniformly.

**Scope:** built-in formatter path only (the `gsx fmt` CLI + `Fprint` default). External custom flat `rawfmt.Formatter`s (via `gen.WithCSSFormatter`, rare) keep current behavior — they carry no token info to fix.

## Global Constraints

- Go 1.26.1. Runtime root package stays std-lib-only (all changes are in `internal/**`).
- **Idempotence is the gate:** `fmt(fmt(x)) == fmt(x)`, enforced automatically by the `internal/gsxfmt` corpus harness (`corpus_test.go:164`) and the printer idempotence suite.
- **Value preservation:** a template literal / block comment interior must be emitted byte-identical to the author's (no injected/removed whitespace).
- Never reflow; keep every newline (ASI safety unchanged).
- Existing `<script>`/`<style>` and attribute behavior for NON-multi-line-token bodies must be unchanged (goldens byte-identical).
- `make check` must pass.

## File structure

- `internal/reindent/reindent.go` — add `ReindentLines(src, a) ([]string, bool)`; `Reindent` delegates.
- `internal/jsfmt/jsfmt.go`, `internal/cssfmt/cssfmt.go` — add `FormatLines(src, width) ([]string, bool)`.
- `internal/rawfmt/rawfmt.go` — add `LineFormatter` type, `FormatLines` (Doc from logical lines, for `<script>`/`<style>`), `FormatStringLines` (escaped+restored logical lines, for attributes), `reindentLines`, `restoreLines`.
- `internal/printer/printer.go` — printer gains `cssLineFmt, jsLineFmt rawfmt.LineFormatter`; `Fprint` sets them; `element()` (script/style) and `embeddedAttrDoc` prefer the line path; `embeddedAttrValueDoc` consumes `[]string` logical lines.
- Tests + gsxfmt corpus cases (template literal, block comment) for all four surfaces.

---

### Task 1: `reindent.ReindentLines`

**Files:** `internal/reindent/reindent.go`, `internal/reindent/reindent_test.go`

- [ ] **Step 1 — failing test.** Add to `reindent_test.go` a test using the existing test adapter (see the existing `reindent_test.go` for the fake adapter pattern) asserting that a token stream containing an `Opaque` token whose `Text` spans lines (e.g. `` `<div>\nhi\n</div>` ``) returns it as ONE logical line (the returned slice element contains the internal `\n`s), while structural `Newline` tokens split lines. Also assert `strings.Join(ReindentLines(src,a), "\n") == Reindent(src,a)`.

- [ ] **Step 2 — run, expect FAIL** (`undefined: ReindentLines`). `go test ./internal/reindent/ -run Lines -v`

- [ ] **Step 3 — implement.** Add `ReindentLines` returning `[]string` (one per logical line), refactor `Reindent` to `join`:

```go
// ReindentLines is Reindent returning the re-indented LOGICAL lines rather than a
// joined string. An Opaque token's internal newlines stay WITHIN a line (they are
// content, not line boundaries), so a returned element may itself contain '\n'.
// Reindent(src,a) == strings.Join(ReindentLines(src,a)) with "\n".
func ReindentLines(src []byte, a Adapter) ([]string, bool) {
	toks, ok := a.Tokenize(src)
	if !ok {
		return nil, false
	}
	var lines [][]Token
	var cur []Token
	for _, t := range toks {
		if t.Class == Newline {
			lines = append(lines, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	lines = append(lines, cur)

	out := make([]string, 0, len(lines))
	depth := 0
	for _, line := range lines {
		start, end := 0, len(line)
		for start < end && line[start].Class == Space {
			start++
		}
		for end > start && line[end-1].Class == Space {
			end--
		}
		content := line[start:end]
		if len(content) == 0 {
			out = append(out, "")
			continue
		}
		indent := depth
		if content[0].Class == Close && indent > 0 {
			indent--
		}
		var b strings.Builder
		for i := 0; i < indent; i++ {
			b.WriteByte('\t')
		}
		opens, closes := 0, 0
		for _, t := range content {
			b.WriteString(t.Text)
			switch t.Class {
			case Open:
				opens++
			case Close:
				closes++
			}
		}
		depth += opens - closes
		if depth < 0 {
			depth = 0
		}
		out = append(out, b.String())
	}
	return out, true
}

func Reindent(src []byte, a Adapter) (string, bool) {
	lines, ok := ReindentLines(src, a)
	if !ok {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
```

- [ ] **Step 4 — run, expect PASS**, and existing reindent tests still green: `go test ./internal/reindent/`
- [ ] **Step 5 — commit** `refactor(reindent): add ReindentLines returning logical lines`

---

### Task 2: `jsfmt.FormatLines` / `cssfmt.FormatLines`

**Files:** `internal/jsfmt/jsfmt.go`, `internal/cssfmt/cssfmt.go` (+ their `_test.go`)

- [ ] **Step 1 — failing tests.** In each package, assert `FormatLines` returns logical lines and that a multi-line template literal (js) / multi-line `/* … */` comment (css) is a SINGLE returned line containing the internal `\n`s (i.e. `strings.Contains(line, "\n")`), while ordinary statements are separate lines.
- [ ] **Step 2 — run, expect FAIL** (`undefined: FormatLines`).
- [ ] **Step 3 — implement** (mirror the existing `Format`):

```go
// jsfmt
func FormatLines(src []byte, width int) ([]string, bool) {
	return reindent.ReindentLines(src, jsAdapter{})
}
```
```go
// cssfmt
func FormatLines(src []byte, width int) ([]string, bool) {
	return reindent.ReindentLines(src, cssAdapter{})
}
```
- [ ] **Step 4 — run, expect PASS**; existing jsfmt/cssfmt tests green.
- [ ] **Step 5 — commit** `feat(jsfmt,cssfmt): FormatLines returning logical lines`

---

### Task 3: `rawfmt` line-based Doc building

**Files:** `internal/rawfmt/rawfmt.go`, `internal/rawfmt/rawfmt_test.go`

**Interfaces produced:**
- `type LineFormatter func(src []byte) (lines []string, ok bool)`
- `func FormatLines(segments, holes []string, lf LineFormatter) (pretty.Doc, bool)` — body Doc for `<script>`/`<style>`.
- `func FormatStringLines(segments, holes []string, lf LineFormatter, escape func(string) string) ([]string, bool)` — escaped + hole-restored logical lines, for attributes.

- [ ] **Step 1 — failing tests.** In `rawfmt_test.go`:
  - `TestFormatStringLinesKeepsOpaqueInterior`: a LineFormatter that returns `["a {", "html: `<div>\nhi\n</div>`", "}"]` (a line WITH an internal `\n`) and one hole; assert `FormatStringLines` returns the same logical-line count (the multi-line line stays ONE element), the hole is restored, no sentinel leaks, and the escape func was applied per line.
  - `TestFormatLinesDocVerbatimInterior`: assert the Doc from `FormatLines` for a line containing an internal `\n` renders (via `pretty.Print`) with the interior line NOT gaining indent tabs (the char after the internal `\n` is not a tab).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement:**

```go
type LineFormatter func(src []byte) (lines []string, ok bool)

// FormatStringLines runs placeholder → line-format → (escape per line) → restore
// and returns the formatted LOGICAL lines with holes restored. escape (if non-nil)
// is applied to each line before restoration (safe: sentinels contain no escapable
// char). ok=false on arity/format/restore failure.
func FormatStringLines(segments, holes []string, lf LineFormatter, escape func(string) string) ([]string, bool) {
	if len(segments) != len(holes)+1 {
		return nil, false
	}
	placeholdered, prefix := buildPlaceholdered(segments, holes)
	lines, ok := lf([]byte(placeholdered))
	if !ok {
		return nil, false
	}
	if escape != nil {
		for i := range lines {
			lines[i] = escape(lines[i])
		}
	}
	return restoreLines(lines, prefix, holes)
}

// restoreLines replaces each sentinel with its hole, verifying every sentinel
// index appears EXACTLY once across all lines (a sentinel never spans a line
// boundary). ok=false on any missing/duplicated sentinel or stray prefix.
func restoreLines(lines []string, prefix string, holes []string) ([]string, bool) {
	for i := range holes {
		tok := sentinel(prefix, i)
		count := 0
		for _, ln := range lines {
			count += strings.Count(ln, tok)
		}
		if count != 1 {
			return nil, false
		}
		for j := range lines {
			if strings.Contains(lines[j], tok) {
				lines[j] = strings.Replace(lines[j], tok, holes[i], 1)
				break
			}
		}
	}
	for _, ln := range lines {
		if strings.Contains(ln, prefix) {
			return nil, false
		}
	}
	return lines, true
}

// FormatLines renders a <script>/<style> body from logical lines. Each logical
// line becomes HardLine+Text; a line's internal newlines (Opaque content) are
// emitted verbatim by Text — no managed indent — so template-literal/comment
// interiors survive. ok=false → caller renders verbatim.
func FormatLines(segments, holes []string, lf LineFormatter) (pretty.Doc, bool) {
	lines, ok := FormatStringLines(segments, holes, lf, nil)
	if !ok {
		return pretty.Doc{}, false
	}
	return reindentLines(lines), true
}

// reindentLines is reindent for logical lines: trims leading/trailing blank
// logical lines, then one HardLine+Text per line (blank → bare "\n"). TrimRight
// on each logical line trims only its tail (the last physical line); Opaque
// interior newlines/whitespace are preserved. Wrapped in Indent + trailing
// HardLine, matching reindent's placement between the open and close tags.
func reindentLines(lines []string) pretty.Doc {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return pretty.Text("")
	}
	parts := make([]pretty.Doc, 0, len(lines)*2+1)
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			parts = append(parts, pretty.Text("\n"))
			continue
		}
		parts = append(parts, pretty.HardLine, pretty.Text(strings.TrimRight(ln, " \t")))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(parts...)), pretty.HardLine)
}
```

- [ ] **Step 4 — run, expect PASS**; existing rawfmt tests green (`Format`/`FormatString` untouched).
- [ ] **Step 5 — commit** `feat(rawfmt): line-based Doc building preserves Opaque-token interiors`

---

### Task 4: printer — use the line path; fix both surfaces

**Files:** `internal/printer/printer.go`, `internal/printer/embedded_attr_fmt_test.go`, `internal/printer/script_test.go`/`style_test.go` (assertions only if needed)

- [ ] **Step 1 — failing tests.** Add to `embedded_attr_fmt_test.go`:
  - `TestEmbeddedAttrTemplateLiteralVerbatim`: the `x-data=js"{ … html:` `` `<div>\nhi\n</div>` `` …}"` case — assert the interior `hi`/`</div>` lines are NOT re-indented (byte-check the template-literal substring is unchanged) AND idempotent (`normPrint(normPrint(x)) == normPrint(x)`).
  - `TestScriptTemplateLiteralVerbatim` (in `script_test.go`): a `<script>` body with a multi-line template literal — interior preserved + idempotent.
  These MUST fail on the current code (interior re-indented / non-idempotent).
- [ ] **Step 2 — run, expect FAIL** with the drift shown.
- [ ] **Step 3 — implement.**
  - Add printer fields `cssLineFmt, jsLineFmt rawfmt.LineFormatter`.
  - `Fprint` constructs the printer with the built-in LINE formatters set (and may leave flat `cssFmt/jsFmt` nil); `FprintWith` keeps setting the flat formatters with line formatters nil (external path unchanged). Add `defaultCSSLineFormatter(width)`/`defaultJSLineFormatter(width)` wrapping `cssfmt.FormatLines`/`jsfmt.FormatLines`.
  - `element()` `<script>`: `if p.jsLineFmt != nil { if doc, ok := rawfmt.FormatLines(segments, holes, p.jsLineFmt); ok { … } } else if p.jsFmt != nil { rawfmt.Format(…) }`. Same for `<style>`/`p.cssLineFmt`.
  - `embeddedAttrDoc`: select `lf := p.jsLineFmt`/`p.cssLineFmt` by lang. When a line formatter is present, call `rawfmt.FormatStringLines(segments, holes, lf, escape)` → `[]string` logical lines → pass to `embeddedAttrValueDoc`. When only a flat formatter is present (external), keep the current `FormatString`→split path.
  - Change `embeddedAttrValueDoc` to accept `lines []string` (logical lines) instead of a flat `formatted` string. The block/inline decision keys on `lines[0] == ""` (a leading blank logical line = block). The inline/block bodies are built from the logical lines directly (each line → `HardLine`+`Text`; a line's internal `\n`s stay in its `Text`, verbatim). For the flat fallback, split the flat string on `\n` into `lines` (current behavior) before calling it.
- [ ] **Step 4 — run, expect PASS** the new tests; then FULL `go test ./internal/printer/` (idempotence/faithfulness suites included). Fix any drift.
- [ ] **Step 5 — commit** `fix(fmt): preserve multi-line-token interiors in js/css bodies and attrs`

---

### Task 5: corpus coverage (the user's explicit ask) + full verification

**Files:** new `internal/gsxfmt/testdata/cases/*.txtar`; `make check`.

- [ ] **Step 1 — add cases** (inputs authored with multi-line tokens; goldens via `-update`). At least:
  - `embed_body_script_template_literal.txtar` — `<script>` body with a multi-line `` `…` `` template literal.
  - `embed_body_style_block_comment.txtar` — `<style>` body with a multi-line `/* … */` comment.
  - `embed_attr_js_template_literal.txtar` — `x-data=js"{ … `…\n…` … }"`.
  - `embed_attr_css_block_comment.txtar` — `style=css`\n/* multi\nline */\ncolor: red;\n``.
- [ ] **Step 2 — run, expect FAIL** (missing goldens). `go test ./internal/gsxfmt/ -run TestFmtCorpus`
- [ ] **Step 3 — `-update`, then INSPECT** each golden: the multi-line-token interior must be byte-identical to the input's interior (no injected tabs). If a golden shows injected indentation, that's a Task-4 bug — fix it, don't hand-edit.
- [ ] **Step 4 — run WITHOUT `-update`** (`-v`): golden-match + idempotence + reparse all green — the harness idempotence check is the real proof.
- [ ] **Step 5 — `make check`** — clean.
- [ ] **Step 6 — commit** `test(fmt): corpus for multi-line-token interiors (script/style/js-attr/css-attr)`

## Self-review checklist
- Opaque interior verbatim (no injected indent) — Tasks 4/5 tests + corpus.
- Idempotence — gsxfmt harness auto-check on every new golden.
- `<script>`/`<style>` non-token bodies unchanged — existing goldens byte-identical (diff them).
- JS/CSS attr non-token bodies unchanged — existing `embed_attr_*` goldens byte-identical.
- External flat-Formatter path unchanged (fallback branch retained).
- `Reindent`/`Format`/`FormatString` public behavior unchanged (delegation only).
