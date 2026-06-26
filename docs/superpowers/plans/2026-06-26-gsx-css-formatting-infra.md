# CSS Formatting Infrastructure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Format `<style>` element bodies during `gsx fmt` via a reusable language-agnostic embed layer (`internal/rawfmt`) plus a built-in minimal CSS formatter (`internal/cssfmt`) on the `pretty` Doc IR, with an in-process `gen.WithCSSFormatter` plug point.

**Architecture:** `internal/rawfmt` is string-based: it takes a `<style>` body decomposed into literal text `segments` and rendered interpolation `holes`, substitutes each hole with a collision-free sentinel token, runs a `Formatter`, restores holes (verifying each appears exactly once), re-indents the result, and returns a `pretty.Doc` — falling back (`ok=false`) on any failure. `internal/cssfmt` is the first `Formatter`: tokenize CSS → build a `pretty.Doc` → `pretty.Print`. The printer adapts `ast` body nodes into `(segments, holes)` so `rawfmt` never imports `ast` or `printer` (no cycle). `<script>` stays verbatim (JS is a follow-up spec).

**Tech Stack:** Go; `internal/pretty` (Wadler/Prettier Doc IR with `Group`/`Indent`/`HardLine`/`Fill`); existing `gsx fmt` pipeline (`gsxfmt.Format` → `printer.Fprint`).

**Spec:** `docs/superpowers/specs/2026-06-26-gsx-css-formatting-infra-design.md`

## Global Constraints

- **Default behavior:** built-in minimal CSS formatter; replaceable by a plugin. No-op/verbatim only on failure.
- **Extension point:** in-process Go func `type Formatter func(src []byte) ([]byte, error)`, registered via `gen.WithCSSFormatter(...)`, mirroring `cssMin`/`jsMin`.
- **Scope:** CSS only. `internal/jsfmt`, `gen.WithJSFormatter`, and `<script>` formatting are OUT (follow-up spec). `<script>` keeps its current `rawHoleChildren` path untouched.
- **Hole handling:** placeholder round-trip with a collision-free sentinel; restore verifies each sentinel appears exactly once; any mismatch → verbatim fallback.
- **Load-bearing invariant:** raw-text bodies parse into ONLY `*ast.Text` + `@{ }` `*ast.Interp` (no markup control flow). Every hole is a single inline token. Do not add control-flow handling.
- **Safety:** `rawfmt.Format` returns `ok=false` (caller falls back to verbatim `rawHoleChildren`) on Formatter error, recovered panic, or restore mismatch. `gsx fmt` never fails on parseable gsx.
- **Faithfulness for `<style>` bodies:** redefined as CSS token-equivalence + hole-sequence preservation + idempotence (reflowing CSS deliberately changes output bytes; CSS is whitespace-insensitive so semantics are preserved).
- **Indentation:** tabs (the printer/`pretty` engine emit tabs; `tabWidth = 4`).
- Run `go test ./...` green before each commit; `go vet ./...` clean.

---

### Task 1: `rawfmt` sentinel substitution + restore (pure strings)

The safety-critical core: collision-free sentinel selection, placeholdered-source construction, and restore-with-verification. Pure string functions, no `pretty`/`ast` — fully unit-testable in isolation.

**Files:**
- Create: `internal/rawfmt/rawfmt.go`
- Test: `internal/rawfmt/rawfmt_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only: `strconv`, `strings`).
- Produces (unexported, used by Task 2):
  - `func buildPlaceholdered(segments, holes []string) (text, prefix string)`
  - `func sentinel(prefix string, i int) string`
  - `func restore(formatted, prefix string, holes []string) (string, bool)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/rawfmt/rawfmt_test.go
package rawfmt

import (
	"strings"
	"testing"
)

func TestBuildPlaceholderedInterleaves(t *testing.T) {
	segs := []string{".a{color:", ";width:", "}"}
	holes := []string{"@{ fg }", "@{ w }"}
	text, prefix := buildPlaceholdered(segs, holes)
	want := ".a{color:" + sentinel(prefix, 0) + ";width:" + sentinel(prefix, 1) + "}"
	if text != want {
		t.Fatalf("placeholdered = %q, want %q", text, want)
	}
	if strings.Contains(strings.Join(segs, ""), prefix) {
		t.Fatalf("prefix %q collides with segment text", prefix)
	}
}

func TestBuildPlaceholderedAvoidsCollision(t *testing.T) {
	// Source already contains the default prefix → the chosen prefix must differ
	// and be absent from the source.
	segs := []string{"a __gsxhole_ b ", ""}
	holes := []string{"@{ x }"}
	text, prefix := buildPlaceholdered(segs, holes)
	if strings.Count(text, sentinel(prefix, 0)) != 1 {
		t.Fatalf("sentinel not uniquely present in %q", text)
	}
	// The collision-extended prefix must not appear in the literal segments.
	if strings.Contains(segs[0], prefix) {
		t.Fatalf("extended prefix %q still collides", prefix)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	segs := []string{".a{color:", ";width:", "}"}
	holes := []string{"@{ fg }", "@{ w }"}
	text, prefix := buildPlaceholdered(segs, holes)
	// Simulate a formatter that reflows but preserves sentinels.
	formatted := strings.ReplaceAll(text, "{", " {\n  ")
	got, ok := restore(formatted, prefix, holes)
	if !ok {
		t.Fatal("restore reported failure on a faithful formatter")
	}
	for _, h := range holes {
		if !strings.Contains(got, h) {
			t.Fatalf("restored output missing hole %q:\n%s", h, got)
		}
	}
	if strings.Contains(got, prefix) {
		t.Fatalf("sentinel leaked into restored output:\n%s", got)
	}
}

func TestRestoreRejectsDroppedSentinel(t *testing.T) {
	holes := []string{"@{ a }", "@{ b }"}
	_, prefix := buildPlaceholdered([]string{"x", "y", "z"}, holes)
	// Formatter dropped sentinel 1 entirely.
	formatted := "x" + sentinel(prefix, 0) + "yz"
	if _, ok := restore(formatted, prefix, holes); ok {
		t.Fatal("restore accepted a dropped sentinel")
	}
}

func TestRestoreRejectsDuplicatedSentinel(t *testing.T) {
	holes := []string{"@{ a }"}
	_, prefix := buildPlaceholdered([]string{"x", "y"}, holes)
	formatted := "x" + sentinel(prefix, 0) + "y" + sentinel(prefix, 0)
	if _, ok := restore(formatted, prefix, holes); ok {
		t.Fatal("restore accepted a duplicated sentinel")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/rawfmt/`
Expected: FAIL — `undefined: buildPlaceholdered` (package/functions do not exist yet).

- [ ] **Step 3: Implement the pure string core**

```go
// internal/rawfmt/rawfmt.go

// Package rawfmt is the language-agnostic embedding layer that formats the body
// of a raw-text element (today <style>) during gsx fmt. It substitutes each
// @{ } interpolation hole with a collision-free sentinel token, runs a
// Formatter on the resulting self-contained source, restores the holes, and
// re-indents the result into a pretty.Doc. On any failure it reports ok=false
// so the caller falls back to verbatim rendering.
package rawfmt

import (
	"strconv"
	"strings"
)

// sentinel returns the placeholder token for hole index i under prefix. The
// trailing "_" disambiguates indices (sentinel(1) is not a substring of
// sentinel(10)). The token is a valid CSS identifier so it survives tokenizing.
func sentinel(prefix string, i int) string {
	return prefix + strconv.Itoa(i) + "_"
}

// buildPlaceholdered returns the placeholdered source — segments interleaved
// with one sentinel per hole — and the collision-free prefix it used. segments
// and holes interleave: segments[0] sentinel0 segments[1] sentinel1 … and
// len(segments) == len(holes)+1 (the caller guarantees this). The prefix is
// chosen so neither it nor any sentinel occurs in the segments OR the holes:
// start from a base and append "x_" until absent (deterministic → idempotent).
func buildPlaceholdered(segments, holes []string) (text, prefix string) {
	var scan strings.Builder
	for _, s := range segments {
		scan.WriteString(s)
	}
	for _, h := range holes {
		scan.WriteString(h)
	}
	haystack := scan.String()
	prefix = "__gsxhole_"
	for strings.Contains(haystack, prefix) {
		prefix += "x_"
	}
	var b strings.Builder
	for i, seg := range segments {
		b.WriteString(seg)
		if i < len(holes) {
			b.WriteString(sentinel(prefix, i))
		}
	}
	return b.String(), prefix
}

// restore replaces each sentinel in formatted with its hole, verifying that
// every sentinel index appears EXACTLY once. Any missing or duplicated sentinel
// → (zero, false). Because the prefix is absent from every hole, replacing one
// sentinel never creates or destroys another, so check-and-replace in a loop is
// safe and order-independent.
func restore(formatted, prefix string, holes []string) (string, bool) {
	out := formatted
	for i := range holes {
		tok := sentinel(prefix, i)
		if strings.Count(out, tok) != 1 {
			return "", false
		}
		out = strings.Replace(out, tok, holes[i], 1)
	}
	// No sentinel for any index may remain (a stray prefix means corruption).
	if strings.Contains(out, prefix) {
		return "", false
	}
	return out, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/rawfmt/`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/rawfmt/rawfmt.go internal/rawfmt/rawfmt_test.go
git commit -m "feat(rawfmt): collision-free sentinel substitution + restore"
```

---

### Task 2: `rawfmt.Format` orchestration (dispatch + re-indent → Doc)

Tie the pieces together: the public `Formatter` type and `Format`, which dispatches with panic recovery, restores, and re-indents into a `pretty.Doc` (with clean blank lines — no trailing tabs).

**Files:**
- Modify: `internal/rawfmt/rawfmt.go`
- Test: `internal/rawfmt/format_test.go` (Create)

**Interfaces:**
- Consumes: `buildPlaceholdered`, `restore` (Task 1); `internal/pretty`.
- Produces:
  - `type Formatter func(src []byte) ([]byte, error)`
  - `func Format(segments, holes []string, f Formatter) (pretty.Doc, bool)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/rawfmt/format_test.go
package rawfmt

import (
	"errors"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
)

// render prints the body Doc at depth 1 (as if nested one level under a tag),
// so the re-indent's tab handling is exercised.
func render(doc pretty.Doc) string {
	return pretty.Print(pretty.Concat(pretty.Text("<style>"), pretty.Indent(doc), pretty.Text("</style>")), 80)
}

func TestFormatHappyPath(t *testing.T) {
	// Formatter splits "{" onto its own indented block and restores holes.
	f := func(src []byte) ([]byte, error) {
		s := strings.ReplaceAll(string(src), "{", " {\n")
		s = strings.ReplaceAll(s, "}", "\n}")
		return []byte(s), nil
	}
	doc, ok := Format([]string{".a{color:", "}"}, []string{"@{ fg }"}, f)
	if !ok {
		t.Fatal("Format reported failure on a faithful formatter")
	}
	out := render(doc)
	if !strings.Contains(out, "@{ fg }") {
		t.Fatalf("hole not restored:\n%s", out)
	}
	if strings.Contains(out, "__gsxhole") {
		t.Fatalf("sentinel leaked:\n%s", out)
	}
}

func TestFormatFallsBackOnError(t *testing.T) {
	f := func(src []byte) ([]byte, error) { return nil, errors.New("bad css") }
	if _, ok := Format([]string{"x"}, nil, f); ok {
		t.Fatal("Format should report ok=false on Formatter error")
	}
}

func TestFormatFallsBackOnPanic(t *testing.T) {
	f := func(src []byte) ([]byte, error) { panic("boom") }
	if _, ok := Format([]string{"x"}, nil, f); ok {
		t.Fatal("Format should recover a panic and report ok=false")
	}
}

func TestFormatFallsBackOnDroppedHole(t *testing.T) {
	// Formatter discards the sentinel → restore mismatch → fallback.
	f := func(src []byte) ([]byte, error) { return []byte("nothing here"), nil }
	if _, ok := Format([]string{"a", "b"}, []string{"@{ x }"}, f); ok {
		t.Fatal("Format should report ok=false when a hole is dropped")
	}
}

func TestFormatRejectsBadArity(t *testing.T) {
	f := func(src []byte) ([]byte, error) { return src, nil }
	// len(segments) must equal len(holes)+1.
	if _, ok := Format([]string{"a"}, []string{"@{ x }"}, f); ok {
		t.Fatal("Format should reject mismatched segment/hole arity")
	}
}

func TestFormatBlankLinesHaveNoTrailingTabs(t *testing.T) {
	// A formatter that emits a blank line between rules; re-indent must not
	// leave tab-only lines (that would break idempotence).
	f := func(src []byte) ([]byte, error) {
		return []byte(".a {\n  x: 1;\n}\n\n.b {\n  y: 2;\n}\n"), nil
	}
	doc, ok := Format([]string{".a{x:1}.b{y:2}"}, nil, f)
	if !ok {
		t.Fatal("unexpected fallback")
	}
	out := render(doc)
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimRight(ln, " \t") == "" && ln != "" {
			t.Fatalf("blank line has trailing whitespace %q in:\n%s", ln, out)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/rawfmt/`
Expected: FAIL — `undefined: Format` / `undefined: Formatter`.

- [ ] **Step 3: Implement `Formatter`, `Format`, `safeFormat`, `reindent`**

Add to `internal/rawfmt/rawfmt.go` (add `"fmt"` and `"github.com/gsxhq/gsx/internal/pretty"` to imports):

```go
// Formatter formats a self-contained source string of an embedded language,
// returning the formatted bytes or an error. An error is not fatal: the caller
// falls back to verbatim rendering. The signature matches the cssMin/jsMin
// minifier options so wrappers (e.g. a prettier shell-out) drop in directly.
type Formatter func(src []byte) ([]byte, error)

// Format renders a raw-text element body. segments and holes interleave with
// len(segments) == len(holes)+1; each holes[i] is the already-rendered gsx
// source of an interpolation (e.g. "@{ fg }"). It returns (doc, true) on
// success, where doc is the body content to place between the open and close
// tags: an indented block of the formatted lines plus a trailing HardLine that
// returns to the tag's own depth for the close tag.
//
// It returns (zero, false) — caller renders the body verbatim instead — on any
// arity mismatch, Formatter error, recovered panic, or hole-restoration
// mismatch. Format never itself fails fmt on parseable gsx.
func Format(segments, holes []string, f Formatter) (pretty.Doc, bool) {
	if len(segments) != len(holes)+1 {
		return pretty.Doc{}, false
	}
	placeholdered, prefix := buildPlaceholdered(segments, holes)
	formatted, err := safeFormat(f, placeholdered)
	if err != nil {
		return pretty.Doc{}, false
	}
	restored, ok := restore(string(formatted), prefix, holes)
	if !ok {
		return pretty.Doc{}, false
	}
	return reindent(restored), true
}

// safeFormat calls f, converting a panic into an error so a buggy Formatter
// (including a third-party plugin) degrades to verbatim instead of crashing fmt.
func safeFormat(f Formatter, src string) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("formatter panicked: %v", r)
		}
	}()
	return f([]byte(src))
}

// reindent converts the formatter's multi-line output into a Doc placed between
// the open and close tags. Non-blank lines become HardLine+Text (the engine
// indents them to the body's depth). A blank line becomes a bare Text("\n"):
// the engine writes it with NO indent, so blank lines never carry trailing
// tabs (which would break idempotence). Trailing whitespace on each line is
// trimmed. The final HardLine returns to the tag's depth for the close tag.
func reindent(s string) pretty.Doc {
	s = strings.Trim(s, "\n")
	lines := strings.Split(s, "\n")
	parts := make([]pretty.Doc, 0, len(lines)*2+1)
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			parts = append(parts, pretty.Text("\n"))
			continue
		}
		parts = append(parts, pretty.HardLine, pretty.Text(ln))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(parts...)), pretty.HardLine)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/rawfmt/`
Expected: PASS (all Task 1 + Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/rawfmt/rawfmt.go internal/rawfmt/format_test.go
git commit -m "feat(rawfmt): Format orchestration with re-indent + verbatim fallback"
```

---

### Task 3: `cssfmt` tokenizer

A total CSS tokenizer producing a flat token stream. Unterminated strings/comments are the only error condition (the parser in Task 4 reports structural errors).

**Files:**
- Create: `internal/cssfmt/token.go`
- Test: `internal/cssfmt/token_test.go`

**Interfaces:**
- Consumes: stdlib only.
- Produces (unexported, used by Task 4):
  - `type tokKind int` with `tWS, tComment, tString, tWord, tLBrace, tRBrace, tLParen, tRParen, tColon, tSemi, tComma, tDelim`
  - `type token struct { kind tokKind; text string }`
  - `func tokenize(src []byte) ([]token, error)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/cssfmt/token_test.go
package cssfmt

import "testing"

func kinds(toks []token) []tokKind {
	ks := make([]tokKind, len(toks))
	for i, t := range toks {
		ks[i] = t.kind
	}
	return ks
}

func TestTokenizeRule(t *testing.T) {
	toks, err := tokenize([]byte(".a{color:red}"))
	if err != nil {
		t.Fatal(err)
	}
	want := []tokKind{tDelim, tWord, tLBrace, tWord, tColon, tWord, tRBrace}
	got := kinds(toks)
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %v, want %v (%q)", i, got[i], want[i], toks[i].text)
		}
	}
}

func TestTokenizeStringWithBraces(t *testing.T) {
	toks, err := tokenize([]byte(`content:"a{b}c"`))
	if err != nil {
		t.Fatal(err)
	}
	// The braces/semicolons inside the string must stay inside ONE tString.
	var found bool
	for _, tk := range toks {
		if tk.kind == tString && tk.text == `"a{b}c"` {
			found = true
		}
	}
	if !found {
		t.Fatalf("string not tokenized atomically: %#v", toks)
	}
}

func TestTokenizeComment(t *testing.T) {
	toks, err := tokenize([]byte("/* hi {} ; */ .a"))
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].kind != tComment || toks[0].text != "/* hi {} ; */" {
		t.Fatalf("comment not tokenized atomically: %#v", toks[0])
	}
}

func TestTokenizeSentinelIsWord(t *testing.T) {
	toks, err := tokenize([]byte("color:__gsxhole_0_"))
	if err != nil {
		t.Fatal(err)
	}
	last := toks[len(toks)-1]
	if last.kind != tWord || last.text != "__gsxhole_0_" {
		t.Fatalf("sentinel must be one word token, got %#v", last)
	}
}

func TestTokenizeUnterminatedString(t *testing.T) {
	if _, err := tokenize([]byte(`content:"oops`)); err == nil {
		t.Fatal("expected error for unterminated string")
	}
}

func TestTokenizeUnterminatedComment(t *testing.T) {
	if _, err := tokenize([]byte(`/* oops`)); err == nil {
		t.Fatal("expected error for unterminated comment")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cssfmt/`
Expected: FAIL — `undefined: tokenize`.

- [ ] **Step 3: Implement the tokenizer**

```go
// internal/cssfmt/token.go
package cssfmt

import (
	"fmt"
	"strings"
)

type tokKind int

const (
	tWS tokKind = iota // whitespace run (may contain newlines)
	tComment           // /* ... */
	tString            // "..." or '...'
	tWord              // ident/number/dimension/hash/at-keyword/sentinel: any run of "word" bytes
	tLBrace            // {
	tRBrace            // }
	tLParen            // (
	tRParen            // )
	tColon             // :
	tSemi              // ;
	tComma             // ,
	tDelim             // any other single byte (> + ~ * . # = etc.)
)

type token struct {
	kind tokKind
	text string
}

// isWordByte reports whether b continues an unquoted "word" (identifier,
// number, dimension, hash, at-keyword, !important, sentinel). It deliberately
// includes everything that is not whitespace, a string/comment opener, or one
// of the structural punctuation bytes handled separately — so values like
// "12px", "#fff", "@media", "!important", "translateX" stay single tokens.
func isWordByte(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f', '"', '\'', '{', '}', '(', ')', ':', ';', ',':
		return false
	}
	return true
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f':
		return true
	}
	return false
}

// tokenize splits CSS source into a flat token stream. It is total except for
// unterminated strings and comments, which return an error (Task 4's parser
// turns that into a verbatim fallback).
func tokenize(src []byte) ([]token, error) {
	s := string(src)
	var toks []token
	i := 0
	for i < len(s) {
		b := s[i]
		switch {
		case isSpaceByte(b):
			j := i + 1
			for j < len(s) && isSpaceByte(s[j]) {
				j++
			}
			toks = append(toks, token{tWS, s[i:j]})
			i = j
		case b == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated comment")
			}
			j := i + 2 + end + 2
			toks = append(toks, token{tComment, s[i:j]})
			i = j
		case b == '"' || b == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					j += 2
					continue
				}
				if s[j] == b {
					j++
					break
				}
				if s[j] == '\n' {
					return nil, fmt.Errorf("unterminated string")
				}
				j++
			}
			if j > len(s) || (j <= len(s) && s[j-1] != b) {
				return nil, fmt.Errorf("unterminated string")
			}
			toks = append(toks, token{tString, s[i:j]})
			i = j
		case b == '{':
			toks = append(toks, token{tLBrace, "{"})
			i++
		case b == '}':
			toks = append(toks, token{tRBrace, "}"})
			i++
		case b == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case b == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case b == ':':
			toks = append(toks, token{tColon, ":"})
			i++
		case b == ';':
			toks = append(toks, token{tSemi, ";"})
			i++
		case b == ',':
			toks = append(toks, token{tComma, ","})
			i++
		case isWordByte(b):
			j := i + 1
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			toks = append(toks, token{tWord, s[i:j]})
			i = j
		default:
			toks = append(toks, token{tDelim, s[i : i+1]})
			i++
		}
	}
	return toks, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cssfmt/`
Expected: PASS (all tokenizer tests).

- [ ] **Step 5: Commit**

```bash
git add internal/cssfmt/token.go internal/cssfmt/token_test.go
git commit -m "feat(cssfmt): CSS tokenizer (total; errors only on unterminated string/comment)"
```

---

### Task 4: `cssfmt` parser + layout → `Format(src, width)`

Parse the token stream into rules/declarations/at-rules and lay them out via the `pretty` Doc IR. Selector lists and comma-separated value lists wrap with `Fill`. Structural errors (unbalanced braces) → `error` (→ verbatim fallback). This is the built-in `Formatter`.

**Files:**
- Create: `internal/cssfmt/cssfmt.go`
- Test: `internal/cssfmt/cssfmt_test.go`

**Interfaces:**
- Consumes: `tokenize`, token types (Task 3); `internal/pretty`.
- Produces: `func Format(src []byte, width int) ([]byte, error)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/cssfmt/cssfmt_test.go
package cssfmt

import (
	"strings"
	"testing"
)

func fmtCSS(t *testing.T, in string) string {
	t.Helper()
	out, err := Format([]byte(in), 80)
	if err != nil {
		t.Fatalf("Format(%q) error: %v", in, err)
	}
	return string(out)
}

func TestFormatSingleRule(t *testing.T) {
	got := fmtCSS(t, ".a{color:red;background:blue}")
	want := ".a {\n\tcolor: red;\n\tbackground: blue;\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatAddsTrailingSemicolon(t *testing.T) {
	got := fmtCSS(t, ".a{color:red}")
	if !strings.Contains(got, "color: red;") {
		t.Fatalf("missing normalized declaration:\n%s", got)
	}
}

func TestFormatSelectorList(t *testing.T) {
	got := fmtCSS(t, "h1,h2 , h3{margin:0}")
	if !strings.HasPrefix(got, "h1, h2, h3 {") {
		t.Fatalf("selector list not normalized:\n%s", got)
	}
}

func TestFormatPseudoClassColonNotSpaced(t *testing.T) {
	// A selector colon (pseudo-class) must stay attached — ".a:hover", never
	// ".a: hover" (which would be a different, broken selector). renderInline
	// injects no space around ':'; only layoutDecl spaces the declaration colon.
	got := fmtCSS(t, ".a:hover{color:red}")
	if !strings.HasPrefix(got, ".a:hover {") {
		t.Fatalf("pseudo-class colon was spaced/mangled:\n%s", got)
	}
}

func TestFormatNestedAtRule(t *testing.T) {
	got := fmtCSS(t, "@media (min-width:600px){.a{color:red}}")
	// renderInline injects NO space around ':' — the at-rule prelude keeps
	// "min-width:600px" as-is (correct + safe). The declaration colon inside the
	// nested rule IS spaced ("color: red"), because layoutDecl adds it.
	want := "@media (min-width:600px) {\n\t.a {\n\t\tcolor: red;\n\t}\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatAtRuleStatement(t *testing.T) {
	got := fmtCSS(t, `@import "x.css";`)
	if strings.TrimSpace(got) != `@import "x.css";` {
		t.Fatalf("at-rule statement not preserved:\n%s", got)
	}
}

func TestFormatPreservesComment(t *testing.T) {
	got := fmtCSS(t, "/* hi */ .a{color:red}")
	if !strings.Contains(got, "/* hi */") {
		t.Fatalf("comment dropped:\n%s", got)
	}
}

func TestFormatPreservesSentinel(t *testing.T) {
	got := fmtCSS(t, ".a{color:__gsxhole_0_}")
	if !strings.Contains(got, "__gsxhole_0_") {
		t.Fatalf("sentinel mangled:\n%s", got)
	}
}

func TestFormatRejectsUnbalanced(t *testing.T) {
	if _, err := Format([]byte(".a{color:red"), 80); err == nil {
		t.Fatal("expected error for unbalanced braces")
	}
}

func TestFormatIdempotent(t *testing.T) {
	once := fmtCSS(t, ".a{color:red;background:blue}h1,h2{margin:0}")
	twice := fmtCSS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cssfmt/`
Expected: FAIL — `undefined: Format`.

- [ ] **Step 3: Implement the parser + layout**

```go
// internal/cssfmt/cssfmt.go

// Package cssfmt is a minimal CSS formatter built on the pretty Doc IR. It is
// the built-in rawfmt.Formatter for <style> bodies during gsx fmt: tokenize →
// parse rules/declarations/at-rules → build a pretty.Doc → Print. It is
// deliberately minimal — correct-or-error, never best-effort-mangle: any
// construct it cannot represent returns an error so the caller falls back to
// verbatim. It has no knowledge of HTML nesting; rawfmt owns the outer indent.
package cssfmt

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/internal/pretty"
)

// Format formats a self-contained CSS source string at the given print width.
func Format(src []byte, width int) ([]byte, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	items, err := p.parseItems(false)
	if err != nil {
		return nil, err
	}
	if p.i != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.i].text)
	}
	doc := layoutItems(items)
	out := pretty.Print(doc, width)
	out = strings.TrimRight(out, "\n") + "\n"
	return []byte(out), nil
}

// --- parse tree -------------------------------------------------------------

// item is one top-level or in-block construct.
type item struct {
	comment string // non-empty → a standalone comment item (other fields unused)

	// rule / at-rule with a block:
	prelude []token // selector list or at-rule prelude (raw tokens, trimmed of edge WS)
	block   []item  // children when isBlock

	// declaration or at-rule statement (no block):
	decl []token // raw tokens of "prop: value" or "@import …" (trimmed of edge WS)

	isBlock bool // has { block }
	isDecl  bool // declaration or statement ending in ; (or block end)
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() (token, bool) {
	if p.i < len(p.toks) {
		return p.toks[p.i], true
	}
	return token{}, false
}

// parseItems parses items until EOF (inBlock=false) or a matching } (inBlock=true).
func (p *parser) parseItems(inBlock bool) ([]item, error) {
	var items []item
	for {
		t, ok := p.peek()
		if !ok {
			if inBlock {
				return nil, fmt.Errorf("unterminated block")
			}
			return items, nil
		}
		switch t.kind {
		case tWS:
			p.i++
		case tComment:
			items = append(items, item{comment: t.text})
			p.i++
		case tRBrace:
			if inBlock {
				p.i++ // consume }
				return items, nil
			}
			return nil, fmt.Errorf("unexpected }")
		default:
			it, err := p.parseStatement()
			if err != nil {
				return nil, err
			}
			items = append(items, it)
		}
	}
}

// parseStatement collects tokens until ';' (a declaration/at-statement), '{' (a
// rule/at-rule, then its block), or '}'/EOF. The prelude tokens are trimmed of
// edge whitespace.
func (p *parser) parseStatement() (item, error) {
	start := p.i
	for {
		t, ok := p.peek()
		if !ok {
			// Reached EOF without ; or } — a declaration with no terminator is
			// tolerated, but an unclosed block already errored above. Treat the
			// leftover as a malformed input.
			return item{}, fmt.Errorf("unterminated statement")
		}
		switch t.kind {
		case tSemi:
			toks := trimWS(p.toks[start:p.i])
			p.i++ // consume ;
			return item{decl: toks, isDecl: true}, nil
		case tLBrace:
			prelude := trimWS(p.toks[start:p.i])
			p.i++ // consume {
			block, err := p.parseItems(true)
			if err != nil {
				return item{}, err
			}
			return item{prelude: prelude, block: block, isBlock: true}, nil
		case tRBrace:
			// Declaration with no trailing ';' before the block closes.
			toks := trimWS(p.toks[start:p.i])
			if len(toks) == 0 {
				return item{}, fmt.Errorf("empty statement")
			}
			return item{decl: toks, isDecl: true}, nil
		default:
			p.i++
		}
	}
}

func trimWS(toks []token) []token {
	for len(toks) > 0 && toks[0].kind == tWS {
		toks = toks[1:]
	}
	for len(toks) > 0 && toks[len(toks)-1].kind == tWS {
		toks = toks[:len(toks)-1]
	}
	return toks
}

// --- layout -----------------------------------------------------------------

func layoutItems(items []item) pretty.Doc {
	var parts []pretty.Doc
	for i, it := range items {
		if i > 0 {
			parts = append(parts, pretty.HardLine)
		}
		parts = append(parts, layoutItem(it))
	}
	return pretty.Concat(parts...)
}

func layoutItem(it item) pretty.Doc {
	switch {
	case it.comment != "":
		return pretty.Text(it.comment)
	case it.isBlock:
		head := layoutPrelude(it.prelude)
		body := layoutItems(it.block)
		return pretty.Concat(
			head, pretty.Text(" {"),
			pretty.Indent(pretty.Concat(pretty.HardLine, body)),
			pretty.HardLine, pretty.Text("}"),
		)
	case it.isDecl:
		return layoutDecl(it.decl)
	default:
		return pretty.Text("")
	}
}

// layoutPrelude renders a selector list / at-rule prelude: top-level
// comma-separated parts joined with ", ", wrapping via Fill when too wide.
func layoutPrelude(toks []token) pretty.Doc {
	groups := splitTopLevel(toks, tComma)
	if len(groups) <= 1 {
		return pretty.Text(renderInline(toks))
	}
	var fill []pretty.Doc
	for i, g := range groups {
		if i > 0 {
			fill = append(fill, pretty.Text(","), pretty.Line)
		}
		fill = append(fill, pretty.Text(renderInline(g)))
	}
	return pretty.Group(pretty.Fill(fill...))
}

// layoutDecl renders "prop: value;". The first top-level colon splits property
// from value; the value's top-level comma list wraps via Fill.
func layoutDecl(toks []token) pretty.Doc {
	prop, value, ok := splitFirst(toks, tColon)
	if !ok {
		// No colon (e.g. a bare at-statement like @import "x"): render inline.
		return pretty.Concat(pretty.Text(renderInline(toks)), pretty.Text(";"))
	}
	groups := splitTopLevel(value, tComma)
	if len(groups) <= 1 {
		return pretty.Concat(
			pretty.Text(renderInline(prop)), pretty.Text(": "),
			pretty.Text(renderInline(value)), pretty.Text(";"),
		)
	}
	var fill []pretty.Doc
	for i, g := range groups {
		if i > 0 {
			fill = append(fill, pretty.Text(","), pretty.Line)
		}
		fill = append(fill, pretty.Text(renderInline(g)))
	}
	return pretty.Concat(
		pretty.Text(renderInline(prop)), pretty.Text(": "),
		pretty.Group(pretty.Indent(pretty.Fill(fill...))), pretty.Text(";"),
	)
}

// renderInline collapses a token run to single-line text: each whitespace run
// becomes a single space (dropped at the edges), and every other token is
// emitted verbatim and adjacent. It injects NO spacing around ':' / ',' / '>'
// etc. — that is the SAFE minimal normalization: a pseudo-class like ".a:hover"
// or a functional ":is(a:hover)" must keep its colon attached, and there is no
// purely-structural way to tell a selector colon from a media-feature colon
// without parsing the prelude grammar. Declaration colon spacing ("prop: value")
// is added explicitly by layoutDecl, and selector / value comma-lists are joined
// by the Fill in layoutPrelude / layoutDecl — so renderInline never needs to
// inject a space itself. Comments are kept inline as ordinary tokens.
func renderInline(toks []token) string {
	var b strings.Builder
	for _, t := range toks {
		if t.kind == tWS {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteString(t.text)
	}
	return strings.TrimRight(b.String(), " ")
}

// splitTopLevel splits toks on sep at paren depth 0, returning the groups
// (each trimmed of edge WS). A nested "(a, b)" is not split.
func splitTopLevel(toks []token, sep tokKind) [][]token {
	var groups [][]token
	depth := 0
	start := 0
	for i, t := range toks {
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				groups = append(groups, trimWS(toks[start:i]))
				start = i + 1
			}
		}
	}
	groups = append(groups, trimWS(toks[start:]))
	return groups
}

// splitFirst splits toks at the first top-level occurrence of sep, returning
// (before, after, true). With no top-level sep it returns (toks, nil, false).
func splitFirst(toks []token, sep tokKind) (before, after []token, ok bool) {
	depth := 0
	for i, t := range toks {
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				return trimWS(toks[:i]), trimWS(toks[i+1:]), true
			}
		}
	}
	return toks, nil, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cssfmt/`
Expected: PASS. If a spacing test fails on exact bytes, adjust `renderInline` / the test's `want` to agree — the canonical form is: one declaration per line, tab indent, `prop: value;`, `sel1, sel2 {`. Keep the formatter output and the test in lockstep; do not weaken `TestFormatIdempotent`.

- [ ] **Step 5: Commit**

```bash
git add internal/cssfmt/cssfmt.go internal/cssfmt/cssfmt_test.go
git commit -m "feat(cssfmt): minimal CSS parser + pretty-IR layout (correct-or-error)"
```

---

### Task 5: Wire `<style>` through `rawfmt` in the printer

Route `<style>` bodies through `rawfmt.Format` with the built-in `cssfmt` (default), preserving the verbatim `rawHoleChildren` fallback on `ok=false`. Add the `cssFmt` override entry point. `<script>` is untouched.

**Files:**
- Modify: `internal/printer/printer.go`
- Modify: `internal/gsxfmt/gsxfmt.go` (default closure; additive `…With` entry)
- Test: `internal/printer/style_test.go` (Create)

**Interfaces:**
- Consumes: `rawfmt.Format`, `rawfmt.Formatter` (Task 2); `cssfmt.Format` (Task 4).
- Produces:
  - `printer.Fprint(w io.Writer, f *ast.File, width int) error` — unchanged signature; now defaults `cssFmt` to a `cssfmt.Format` closure.
  - `func FprintWith(w io.Writer, f *ast.File, width int, cssFmt rawfmt.Formatter) error`
  - printer struct gains `cssFmt rawfmt.Formatter` and the `<style>` branch uses it.
  - adapter `func nodesToBody(nodes []ast.Markup) (segments, holes []string)`
  - `gsxfmt.FormatRemovingImportsWith(name string, src []byte, unused []ImportRef, width int, cssFmt rawfmt.Formatter) ([]byte, error)` (existing funcs delegate with `nil`).

- [ ] **Step 1: Write the failing tests**

```go
// internal/printer/style_test.go
package printer

import (
	"strings"
	"testing"
)

func TestStyleBodyFormatted(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>.a{color:red;background:blue}</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "color: red;") || !strings.Contains(out, "background: blue;") {
		t.Fatalf("style body not formatted:\n%s", out)
	}
}

func TestStyleBodyHolePreserved(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>.a{color:@{ fg }}</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@{ fg }") {
		t.Fatalf("hole not preserved:\n%s", out)
	}
	if strings.Contains(out, "__gsxhole") {
		t.Fatalf("sentinel leaked:\n%s", out)
	}
}

func TestStyleBodyIdempotent(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>h1,h2{margin:0}.a{color:@{ fg }}</style>\n}\n"
	once, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normPrint(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("style fmt not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestStyleMalformedFallsBackVerbatim(t *testing.T) {
	// Unbalanced CSS → cssfmt errors → verbatim fallback (body unchanged).
	src := "package p\n\ncomponent C() {\n\t<style>.a{color:red</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ".a{color:red") {
		t.Fatalf("malformed CSS should be left verbatim:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/printer/ -run TestStyleBody`
Expected: FAIL — the body is currently emitted verbatim, so `color: red;` is absent.

- [ ] **Step 3: Implement the wiring**

In `internal/printer/printer.go`:

1. Add imports `"github.com/gsxhq/gsx/internal/cssfmt"` and `"github.com/gsxhq/gsx/internal/rawfmt"`.

2. Add the field to the `printer` struct and a default helper:

```go
type printer struct {
	err    error
	cssFmt rawfmt.Formatter // nil → no CSS formatting (style stays verbatim)
}
```

3. Replace `Fprint` and add `FprintWith`:

```go
func Fprint(w io.Writer, f *ast.File, width int) error {
	return FprintWith(w, f, width, defaultCSSFormatter(width))
}

// FprintWith is Fprint with an explicit CSS Formatter for <style> bodies. A nil
// cssFmt leaves <style> bodies verbatim. The default (used by Fprint) is the
// built-in cssfmt at the given width.
func FprintWith(w io.Writer, f *ast.File, width int, cssFmt rawfmt.Formatter) error {
	p := printer{cssFmt: cssFmt}
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width))
	return err
}

// defaultCSSFormatter binds the built-in cssfmt to the print width.
func defaultCSSFormatter(width int) rawfmt.Formatter {
	return func(src []byte) ([]byte, error) { return cssfmt.Format(src, width) }
}
```

4. In `element()`, change the `<style>` branch (currently `printer.go:181-182`) to route through `rawfmt`, keeping `<script>` verbatim:

```go
	if strings.EqualFold(e.Tag, "script") {
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
	if strings.EqualFold(e.Tag, "style") {
		if p.cssFmt != nil {
			segments, holes := nodesToBody(e.Children)
			if doc, ok := rawfmt.Format(segments, holes, p.cssFmt); ok {
				return pretty.Concat(openTag, doc, close)
			}
		}
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
```

5. Add the adapter near `rawHoleChildren` (`printer.go:619`). It mirrors `writeRawHoleString`'s rendering of holes but splits the body into `(segments, holes)` with `len(segments) == len(holes)+1`:

```go
// nodesToBody splits a <style> body (only *ast.Text and @{ } *ast.Interp by the
// raw-text parser) into literal text segments and rendered holes for rawfmt.
// segments and holes interleave with len(segments) == len(holes)+1: an empty
// segment is inserted so a hole at the start/end or two adjacent holes still
// satisfy the invariant.
func nodesToBody(nodes []ast.Markup) (segments, holes []string) {
	var cur strings.Builder
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			cur.WriteString(v.Value)
		case *ast.Interp:
			segments = append(segments, cur.String())
			cur.Reset()
			holes = append(holes, renderHole(v))
		default:
			// The raw-text parser never produces other node types here; render
			// defensively so the invariant still holds.
			cur.WriteString(markupInlineString(n))
		}
	}
	segments = append(segments, cur.String())
	return segments, holes
}

// renderHole renders one interpolation hole exactly as rawHoleChildren does.
func renderHole(v *ast.Interp) string {
	var b strings.Builder
	b.WriteString("@{ ")
	b.WriteString(fmtExpr(v.Expr))
	for _, s := range v.Stages {
		b.WriteString(" |> ")
		b.WriteString(pipeStageStr(s))
	}
	b.WriteString(" }")
	return b.String()
}
```

6. Refactor `writeRawHoleString`'s `*ast.Interp` case to call `renderHole(v)` (DRY — both paths render holes identically):

```go
		case *ast.Interp:
			b.WriteString(renderHole(v))
```

In `internal/gsxfmt/gsxfmt.go`, thread an optional formatter (additive; existing callers unaffected):

```go
// FormatRemovingImportsWith is FormatRemovingImports with an explicit CSS
// Formatter for <style> bodies (nil → built-in default at the given width).
func FormatRemovingImportsWith(name string, src []byte, unused []ImportRef, width int, cssFmt rawfmt.Formatter) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	removeImports(f, unused)
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if cssFmt == nil {
		if err := printer.Fprint(&b, f, width); err != nil {
			return nil, err
		}
	} else {
		if err := printer.FprintWith(&b, f, width, cssFmt); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
```

Add `"github.com/gsxhq/gsx/internal/rawfmt"` to gsxfmt imports. Leave `Format` and `FormatRemovingImports` exactly as they are (they call `printer.Fprint`, which now uses the built-in default).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/printer/ ./internal/gsxfmt/`
Expected: PASS for the new `TestStyleBody*` tests. NOTE: existing faithfulness tests (`TestCorpusFaithfulness`, `TestCorpusInputsProperty`) may now FAIL if any corpus `<style>` body gets reformatted — that is expected and fixed in Task 7. If they fail ONLY on `<style>`-bearing inputs, proceed; if they fail otherwise, investigate.

- [ ] **Step 5: Run the full printer + fmt suite to scope any breakage**

Run: `go test ./internal/printer/ ./internal/gsxfmt/ ./gen/ ./internal/lsp/ 2>&1 | tail -30`
Expected: green EXCEPT possibly the two corpus faithfulness tests on `<style>` inputs (Task 7). Record which fail.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/printer.go internal/printer/style_test.go internal/gsxfmt/gsxfmt.go
git commit -m "feat(printer): format <style> bodies via rawfmt + built-in cssfmt (verbatim fallback)"
```

---

### Task 6: `gen.WithCSSFormatter` option + merge + `gsx fmt` threading

Register a custom CSS `Formatter` programmatically and thread it to `gsx fmt`. Mirrors the `cssMin`/`jsMin` option + `mergeConfig` pattern. CLI/LSP without an override use the built-in default.

**Files:**
- Modify: `gen/main.go` (config field + `case "fmt"` threads `merged.cssFmt`)
- Modify: `gen/options.go` (`WithCSSFormatter`)
- Modify: `gen/configfile.go` (`mergeConfig` rule)
- Modify: `gen/fmt.go` (`runFmt` accepts + uses the override)
- Test: `gen/cssformatter_test.go` (Create)

**Interfaces:**
- Consumes: `rawfmt.Formatter` (Task 2); `gsxfmt.FormatRemovingImportsWith` (Task 5).
- Produces:
  - `config.cssFmt rawfmt.Formatter`
  - `func WithCSSFormatter(f rawfmt.Formatter) Option`
  - `mergeConfig`: `merged.cssFmt = base.cssFmt; if opts.cssFmt != nil { merged.cssFmt = opts.cssFmt }`
  - `runFmt(stdout, stderr io.Writer, args []string, cssFmt rawfmt.Formatter) int`

- [ ] **Step 1: Write the failing tests**

```go
// gen/cssformatter_test.go
package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/rawfmt"
)

func TestWithCSSFormatterOption(t *testing.T) {
	var cfg config
	f := func(src []byte) ([]byte, error) { return src, nil }
	WithCSSFormatter(rawfmt.Formatter(f))(&cfg)
	if cfg.cssFmt == nil {
		t.Fatal("WithCSSFormatter did not set cfg.cssFmt")
	}
}

func TestMergeConfigCSSFormatterOptsWins(t *testing.T) {
	base := config{cssFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	opts := config{cssFmt: func(src []byte) ([]byte, error) { return []byte("opts"), nil }}
	merged := mergeConfig(base, opts)
	got, _ := merged.cssFmt(nil)
	if string(got) != "opts" {
		t.Fatalf("merged.cssFmt = %q, want opts override", got)
	}
}

func TestMergeConfigCSSFormatterFallsBackToBase(t *testing.T) {
	base := config{cssFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	merged := mergeConfig(base, config{})
	if merged.cssFmt == nil {
		t.Fatal("merged.cssFmt should fall back to base")
	}
	got, _ := merged.cssFmt(nil)
	if string(got) != "base" {
		t.Fatalf("merged.cssFmt = %q, want base", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./gen/ -run 'TestWithCSSFormatter|TestMergeConfigCSSFormatter'`
Expected: FAIL — `cfg.cssFmt undefined` / `WithCSSFormatter undefined`.

- [ ] **Step 3: Implement option, field, merge, and threading**

In `gen/main.go`, add the field to `config` (near `cssMin`/`jsMin`):

```go
	cssFmt rawfmt.Formatter
```

Add `"github.com/gsxhq/gsx/internal/rawfmt"` to `gen/main.go` imports. Change the `fmt` dispatch (currently `case "fmt": return runFmt(stdout, stderr, cmdArgs)`) to resolve config and pass the override:

```go
	case "fmt":
		merged, _, err := resolveConfig(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			return 2
		}
		return runFmt(stdout, stderr, cmdArgs, merged.cssFmt)
```

In `gen/options.go`, add (next to `WithCSSMinifier`):

```go
// WithCSSFormatter installs a custom CSS formatter for <style> bodies during
// `gsx fmt`, replacing the built-in minimal formatter. It receives complete,
// self-contained CSS (interpolation holes are substituted with sentinel tokens
// before the formatter sees them and restored afterward) and returns the
// formatted CSS, or an error to fall back to verbatim. Wrap any whole-buffer
// formatter (e.g. a prettier shell-out) in this signature:
//
//	gen.Main(gen.WithCSSFormatter(func(css []byte) ([]byte, error) { … }))
func WithCSSFormatter(f rawfmt.Formatter) Option {
	return func(cfg *config) { cfg.cssFmt = f }
}
```

Add `"github.com/gsxhq/gsx/internal/rawfmt"` to `gen/options.go` imports.

In `gen/configfile.go` `mergeConfig`, after the `jsMin` block, add:

```go
	merged.cssFmt = base.cssFmt
	if opts.cssFmt != nil {
		merged.cssFmt = opts.cssFmt
	}
```

In `gen/fmt.go`, change `runFmt`'s signature and the format call. Update the signature line:

```go
func runFmt(stdout, stderr io.Writer, args []string, cssFmt rawfmt.Formatter) int {
```

Add `"github.com/gsxhq/gsx/internal/rawfmt"` to `gen/fmt.go` imports. At the `gsxfmt.FormatRemovingImports` call (`gen/fmt.go:92`), thread the override:

```go
		formatted, err := gsxfmt.FormatRemovingImportsWith(path, orig, unusedByPath[abs], width, cssFmt)
```

(With `cssFmt == nil` this is identical to the built-in default — `FormatRemovingImportsWith` selects `printer.Fprint`.)

Update the one existing test caller `gen/fmt_test.go:15` to pass `nil` (it exercises the default built-in formatter):

```go
	code := runFmt(&out, &errb, args, nil)
```

(The other `gsxfmt.Format` caller at `gen/fmt.go:141` is left unchanged; it now formats `<style>` bodies with the built-in default, which is correct.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./gen/ -run 'TestWithCSSFormatter|TestMergeConfigCSSFormatter'`
Expected: PASS.

- [ ] **Step 5: Build + vet (signature change ripples)**

Run: `go build ./... && go vet ./gen/...`
Expected: clean. If any other caller of `runFmt` exists, update it to pass `nil`. (Current only caller is the `case "fmt"` dispatch.)

- [ ] **Step 6: Commit**

```bash
git add gen/main.go gen/options.go gen/configfile.go gen/fmt.go gen/cssformatter_test.go
git commit -m "feat(gen): WithCSSFormatter plug point threaded through gsx fmt"
```

---

### Task 7: `<style>`-aware faithfulness in the property tests

Reformatting `<style>` bodies changes output bytes, so the byte-identical `normalizedAST` faithfulness check must become CSS-token-aware. Canonicalize `<style>` bodies (token signature + preserved holes) inside `normalizedAST` so BOTH faithfulness tests (`TestCorpusFaithfulness`, `TestCorpusInputsProperty`) verify token-equivalence + hole-sequence instead of byte-identity. Add focused `<style>` property cases.

**Files:**
- Modify: `internal/printer/corpus_test.go` (`canonStyleBodies` + hook into `normalizedAST`)
- Test: `internal/printer/style_property_test.go` (Create)

**Interfaces:**
- Consumes: `cssfmt` tokenizer — expose a test-visible token signature. Add to `internal/cssfmt`: `func TokenSignature(src []byte) string` (exported helper used by the printer test).
- Produces: `canonStyleBodies(f *ast.File)` invoked by `normalizedAST`.

- [ ] **Step 1: Add the exported token-signature helper to cssfmt**

In `internal/cssfmt/cssfmt.go`:

```go
// TokenSignature returns a whitespace-, comment-, and optional-semicolon-
// agnostic signature of src: the significant tokens (tWS and tComment dropped)
// joined by "\x1f". A ';' immediately before a '}' or end of input is dropped —
// it is insignificant in CSS, and the formatter normalizes its presence (it
// always emits a trailing ';' on the last declaration in a block). So a minified
// body and its formatted form share a signature iff they are the same CSS up to
// whitespace and that optional terminator. On a tokenizer error (unterminated
// string/comment) it returns the raw source prefixed with "\x00err\x00" so
// malformed input still compares equal to itself (the printer leaves it
// verbatim).
func TokenSignature(src []byte) string {
	toks, err := tokenize(src)
	if err != nil {
		return "\x00err\x00" + string(src)
	}
	// Significant tokens only.
	var sig []token
	for _, t := range toks {
		if t.kind == tWS || t.kind == tComment {
			continue
		}
		sig = append(sig, t)
	}
	// Drop a ';' that is immediately followed by '}' or is the final token.
	var out []string
	for i, t := range sig {
		if t.kind == tSemi {
			if i == len(sig)-1 || sig[i+1].kind == tRBrace {
				continue
			}
		}
		out = append(out, t.text)
	}
	return strings.Join(out, "\x1f")
}
```

Add a test in `internal/cssfmt/cssfmt_test.go`:

```go
func TestTokenSignatureIgnoresWhitespace(t *testing.T) {
	minified := TokenSignature([]byte("h1,h2{margin:0}"))
	pretty := TokenSignature([]byte("h1, h2 {\n\tmargin: 0;\n}\n"))
	if minified != pretty {
		t.Fatalf("whitespace/optional-semicolon changed the signature:\n%q\n%q", minified, pretty)
	}
}

func TestTokenSignatureMatchesAcrossFormat(t *testing.T) {
	src := []byte(".a{color:red}h1,h2{margin:0}")
	out, err := Format(src, 80)
	if err != nil {
		t.Fatal(err)
	}
	if TokenSignature(src) != TokenSignature(out) {
		t.Fatalf("signature changed across Format:\n%q\n%q", TokenSignature(src), TokenSignature(out))
	}
}
```

Run: `go test ./internal/cssfmt/ -run TestTokenSignature`
Expected: PASS — `TokenSignature` drops whitespace, comments, and the optional terminator `;`, so a body and its formatted form (the only token-level change being the added trailing `;` before `}`) share a signature.

- [ ] **Step 2: Write the failing property test**

```go
// internal/printer/style_property_test.go
package printer

import (
	"reflect"
	"testing"
)

// styleCases are inline <style> bodies exercising the faithfulness + idempotence
// contract for CSS formatting.
var styleCases = []string{
	"package p\n\ncomponent C() {\n\t<style>.a{color:red;background:blue}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>h1,h2,h3{margin:0}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>.a{color:@{ fg };width:@{ w }}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>@media (min-width:600px){.a{color:red}}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>.a{color:red</style>\n}\n", // malformed → verbatim
}

func TestStylePropertyFaithfulAndIdempotent(t *testing.T) {
	for _, src := range styleCases {
		formatted, err := normPrint(t, src)
		if err != nil {
			t.Errorf("fmt failed: %v\n%s", err, src)
			continue
		}
		// Faithfulness: normalized ASTs (with <style> bodies canonicalized) match.
		want := normalizedAST(t, src)
		got := normalizedAST(t, formatted)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("fmt changed normalized AST (not faithful):\n--- src ---\n%s\n--- fmt ---\n%s", src, formatted)
		}
		// Idempotence.
		formatted2, err := normPrint(t, formatted)
		if err != nil {
			t.Errorf("re-fmt failed: %v", err)
			continue
		}
		if formatted != formatted2 {
			t.Errorf("not idempotent:\n--- 1 ---\n%s\n--- 2 ---\n%s", formatted, formatted2)
		}
	}
}
```

`normalizedAST` and `reflect.DeepEqual` are already the comparison used by `TestCorpusFaithfulness`/`TestCorpusInputsProperty`. This test stays red until Step 3 wires `canonStyleBodies`.

Run: `go test ./internal/printer/ -run TestStyleProperty`
Expected: FAIL — interpolated/declaration cases differ because `normalizedAST` still compares `<style>` Text byte-for-byte.

- [ ] **Step 3: Canonicalize `<style>` bodies in `normalizedAST`**

In `internal/printer/corpus_test.go`, add `canonStyleBodies` and call it inside `normalizedAST` (after `canonGo`, before `zeroSpans`). It rewrites each `<style>` element's children to a single synthetic `*ast.Text` whose value is a canonical signature: the body's placeholdered text (holes → a fixed sentinel) tokenized to a CSS token signature, with each sentinel replaced by its rendered hole — so token-equivalence AND hole-sequence are both encoded, and both `src` and `fmt(src)` reduce to the same string.

```go
// canonStyleBodies replaces every <style> element's children with a single
// synthetic Text holding a canonical signature of the body: the CSS token
// signature of the placeholdered body, with each hole sentinel mapped back to
// its rendered text. This makes the faithfulness comparison check CSS
// token-equivalence + hole-sequence (whitespace-insensitive) rather than the
// byte-identity that <style> formatting deliberately breaks.
func canonStyleBodies(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		el, ok := n.(*ast.Element)
		if !ok || !strings.EqualFold(el.Tag, "style") {
			return true
		}
		el.Children = []ast.Markup{&ast.Text{Value: styleSignature(el.Children)}}
		return false // do not descend into the rewritten children
	})
}

// styleSignature builds the canonical signature described on canonStyleBodies.
func styleSignature(nodes []ast.Markup) string {
	const sent = "\x00H" // a fixed placeholder unlikely to appear in CSS source
	var body strings.Builder
	var holes []string
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			body.WriteString(v.Value)
		case *ast.Interp:
			body.WriteString(sent)
			body.WriteString(strconv.Itoa(len(holes)))
			body.WriteString("\x00")
			holes = append(holes, renderHole(v))
		}
	}
	sig := cssfmt.TokenSignature([]byte(body.String()))
	for i, h := range holes {
		sig = strings.ReplaceAll(sig, sent+strconv.Itoa(i)+"\x00", h)
	}
	return sig
}
```

Add imports to `corpus_test.go` as needed: `"strconv"`, `"strings"`, `"github.com/gsxhq/gsx/internal/cssfmt"` (and ensure `ast` is imported). Hook into `normalizedAST`:

```go
func normalizedAST(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	canonGo(f)
	canonStyleBodies(f)
	zeroSpans(f)
	return f
}
```

Note on the fixed sentinel: `styleSignature` tokenizes the placeholdered body where holes are `\x00H<i>\x00`. `\x00` is not `isWordByte`-excluded, so `\x00H0\x00` tokenizes as part of a word run — it survives as one token chunk, then `ReplaceAll` maps it to the rendered hole. Because BOTH `src` and `fmt(src)` go through the identical `styleSignature`, they yield equal signatures iff the CSS token streams and hole sequences match. (The malformed case hits the `tokenize` error path in `TokenSignature`, returning the raw body prefixed with `\x00err\x00`; since the malformed body is left verbatim by the printer, both sides' raw bodies are byte-identical and still compare equal.)

- [ ] **Step 4: Run the property + faithfulness tests**

Run: `go test ./internal/printer/ -run 'TestStyleProperty|TestCorpusFaithfulness|TestCorpusInputsProperty'`
Expected: PASS — all three. The corpus faithfulness tests now tolerate `<style>` reflow; the new style cases pass faithfulness + idempotence.

- [ ] **Step 5: Full suite**

Run: `go test ./... && go vet ./...`
Expected: all green, vet clean.

- [ ] **Step 6: Commit**

```bash
git add internal/cssfmt/cssfmt.go internal/cssfmt/cssfmt_test.go internal/printer/corpus_test.go internal/printer/style_property_test.go
git commit -m "test(printer): <style>-aware faithfulness (CSS token-equivalence + hole-sequence)"
```

---

## Final Verification (after all tasks)

- [ ] `go test ./...` — all green.
- [ ] `go vet ./...` — clean.
- [ ] `gopls check -severity=hint internal/rawfmt/rawfmt.go internal/cssfmt/cssfmt.go internal/cssfmt/token.go` — no unused functions.
- [ ] Manual smoke: build the CLI and format a file with a `<style>`:
  ```bash
  go install ./cmd/gsx
  printf 'package p\n\ncomponent C() {\n\t<style>.a{color:red;background:blue}.b{margin:0}</style>\n}\n' > /tmp/smoke.gsx
  gsx fmt /tmp/smoke.gsx
  ```
  Expect the `<style>` body reflowed (one declaration per line, tab-indented, `prop: value;`), the rest of the file unchanged, and a second `gsx fmt` producing identical output (idempotent). Confirm `gsx version` is the freshly built binary (the `gsx` name can be shadowed by Homebrew Ghostscript on PATH).
