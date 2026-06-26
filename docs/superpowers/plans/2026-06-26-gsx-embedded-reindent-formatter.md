# Embedded-Language Re-indent Formatter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `cssfmt`'s parse-and-reflow formatter with a conservative token-pass re-indenter, add the same for JS (`<script>`), behind a shared `internal/reindent` core — fixing embedded-code indentation only, never reflowing.

**Architecture:** A language-agnostic `internal/reindent` core takes a flat stream of classified tokens (`Open`/`Close`/`Newline`/`Space`/`Opaque`/`Other`) and re-emits each logical line at its brace-nesting depth with tabs, preserving the author's line structure exactly. CSS provides an adapter over its existing tokenizer; JS provides one over `jsmin`'s tdewolff lexer (ASI-safe by keeping every newline). Both are `rawfmt.Formatter`s wired through the existing `rawfmt` embed layer; `<script>` mirrors `<style>`.

**Tech Stack:** Go; `github.com/tdewolff/parse/v2/js` (existing dep — JS *lexer* only, no parser); `internal/rawfmt` (hole round-trip); `internal/cssfmt/token.go` (existing CSS tokenizer).

**Spec:** `docs/superpowers/specs/2026-06-26-gsx-embedded-reindent-formatter-design.md`

## Global Constraints

- **Re-indent only.** Fix leading indentation to tabs by block depth; **preserve the author's line structure exactly** — never add/remove a line break, never add/remove a blank line, never reflow, never change intra-line spacing.
- **Literals/comments opaque.** Strings, template literals, regex literals, comments pass through verbatim (internal newlines are content, not structural line breaks, and are not re-indented).
- **Tabs** for indentation (consistent with gsx's gofmt-style markup).
- **Idempotent + faithful by construction** (only leading whitespace changes). Token-equivalence + hole-sequence + idempotence must hold.
- **JS: no second parser.** Reuse the tdewolff *lexer* + `regexPosition`. ASI-safe by keeping every newline (never moves a statement across a line). No `esbuild`, no `js.Parse`.
- **Correct-or-verbatim.** A lexer/tokenizer error → the formatter returns an error → `rawfmt` falls back to verbatim. `gsx fmt` never fails on parseable gsx.
- **Scope:** `<style>` + `<script>` bodies only. Alpine *attribute* JS stays verbatim. Data-island `<script>` (e.g. `type="application/json"`) stays verbatim.
- Run `go test ./...` green and `go vet ./...` clean before each commit.

---

### Task 1: `internal/reindent` — the shared core

The language-agnostic re-indent algorithm + the `Adapter` interface. Pure; no language tokenizer imports. Fully testable via a fake adapter.

**Files:**
- Create: `internal/reindent/reindent.go`
- Test: `internal/reindent/reindent_test.go`

**Interfaces:**
- Consumes: stdlib only (`strings`).
- Produces:
  - `type Class uint8` with `Other, Open, Close, Newline, Space, Opaque`
  - `type Token struct { Class Class; Text string }`
  - `type Adapter interface { Tokenize(src []byte) (toks []Token, ok bool) }`
  - `func Reindent(src []byte, a Adapter) (string, bool)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/reindent/reindent_test.go
package reindent

import (
	"strings"
	"testing"
)

// fake is a trivial adapter: it tokenizes a tiny toy language where '{'/'}' are
// Open/Close, '\n' is Newline, runs of ' '/'\t' are Space, and every other run
// of non-space, non-brace, non-newline bytes is one Other token. A leading "ERR"
// makes Tokenize fail (ok=false).
type fake struct{}

func (fake) Tokenize(src []byte) ([]Token, bool) {
	s := string(src)
	if strings.HasPrefix(s, "ERR") {
		return nil, false
	}
	var toks []Token
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == '\n':
			toks = append(toks, Token{Newline, "\n"})
			i++
		case c == ' ' || c == '\t':
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			toks = append(toks, Token{Space, s[i:j]})
			i = j
		case c == '{':
			toks = append(toks, Token{Open, "{"})
			i++
		case c == '}':
			toks = append(toks, Token{Close, "}"})
			i++
		default:
			j := i
			for j < len(s) && s[j] != '\n' && s[j] != ' ' && s[j] != '\t' && s[j] != '{' && s[j] != '}' {
				j++
			}
			toks = append(toks, Token{Other, s[i:j]})
			i = j
		}
	}
	return toks, true
}

func reindent(t *testing.T, in string) string {
	t.Helper()
	out, ok := Reindent([]byte(in), fake{})
	if !ok {
		t.Fatalf("Reindent reported failure on %q", in)
	}
	return out
}

func TestReindentFixesDepth(t *testing.T) {
	// Messy leading whitespace, correct structure.
	in := "a {\n      b\n   c\n}"
	want := "a {\n\tb\n\tc\n}"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentCloseDedents(t *testing.T) {
	in := "a {\nb {\nc\n}\n}"
	want := "a {\n\tb {\n\t\tc\n\t}\n}"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentPreservesBlankLines(t *testing.T) {
	in := "a {\nb\n\nc\n}"
	want := "a {\n\tb\n\n\tc\n}"
	got := reindent(t, in)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// The blank line must carry NO trailing whitespace.
	for _, ln := range strings.Split(got, "\n") {
		if ln != strings.TrimRight(ln, " \t") {
			t.Fatalf("line %q has trailing whitespace", ln)
		}
	}
}

func TestReindentPreservesIntraLineSpacing(t *testing.T) {
	// Mid-line spacing is the author's; only LEADING indentation is normalized.
	in := "  x   plus   y"
	want := "x   plus   y"
	if got := reindent(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReindentOneLinerStaysOneLine(t *testing.T) {
	in := "a{b;c}"
	if got := reindent(t, in); got != "a{b;c}" {
		t.Fatalf("a one-liner must not be reflowed: got %q", got)
	}
}

func TestReindentLexFailureReportsFalse(t *testing.T) {
	if _, ok := Reindent([]byte("ERR whatever"), fake{}); ok {
		t.Fatal("expected ok=false on adapter lex failure")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/reindent/`
Expected: FAIL — `undefined: Reindent` / `undefined: Token`.

- [ ] **Step 3: Implement the core**

```go
// internal/reindent/reindent.go

// Package reindent is the language-agnostic core of gsx's embedded-language
// formatters. Given a flat stream of classified tokens from a per-language
// Adapter, it re-emits each logical line at its brace-nesting depth using tabs,
// preserving the author's line structure exactly: it never adds or removes a
// line break or a blank line, never reflows, and never alters intra-line
// spacing. Strings, templates, regex, and comments are Opaque — emitted
// verbatim, their internal newlines treated as content, not structure.
package reindent

import "strings"

// Class is how the core treats one token for indentation purposes.
type Class uint8

const (
	Other   Class = iota // ordinary token: emit verbatim, no structural effect
	Open                 // increases block depth for following lines (e.g. "{" "(" "[")
	Close                // decreases block depth; a line STARTING with it dedents
	Newline              // a real line break OUTSIDE any literal/comment
	Space                // inter-token / leading whitespace (NOT inside a literal)
	Opaque               // string/template/regex/comment: emit verbatim, may span lines
)

// Token is one classified lexical token. Text is the exact source bytes.
type Token struct {
	Class Class
	Text  string
}

// Adapter turns language source into the classified token stream. It returns
// ok=false on a lex/tokenize error; Reindent then reports failure and the caller
// renders verbatim.
type Adapter interface {
	Tokenize(src []byte) (toks []Token, ok bool)
}

// Reindent re-indents src using a. Returns (formatted, true), or ("", false) on
// an adapter failure. One tab is emitted per nesting level.
//
// Algorithm (per logical line, split on Newline tokens):
//   - indent = depth, minus one if the line's first significant token is a Close
//     (so a closer dedents to its opener's level); clamped at >= 0.
//   - emit indent tabs, then the line's content with leading and trailing Space
//     tokens dropped and everything else verbatim.
//   - depth += (Open count) - (Close count) on the line, clamped at >= 0.
// Blank lines (no content) emit just the newline — no tabs, no trailing space.
func Reindent(src []byte, a Adapter) (string, bool) {
	toks, ok := a.Tokenize(src)
	if !ok {
		return "", false
	}

	// Split into logical lines on Newline tokens. Opaque tokens keep their
	// internal newlines (they are content, not line boundaries).
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

	var b strings.Builder
	depth := 0
	for li, line := range lines {
		if li > 0 {
			b.WriteByte('\n')
		}
		// Trim leading and trailing Space tokens.
		start, end := 0, len(line)
		for start < end && line[start].Class == Space {
			start++
		}
		for end > start && line[end-1].Class == Space {
			end--
		}
		content := line[start:end]
		if len(content) == 0 {
			continue // blank line: newline only, no indent
		}
		indent := depth
		if content[0].Class == Close && indent > 0 {
			indent--
		}
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
	}
	return b.String(), true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/reindent/`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/reindent/
git commit -m "feat(reindent): language-agnostic token-pass re-indent core"
```

---

### Task 2: Rework `internal/cssfmt` to the re-indenter

Replace the parse+reflow `Format` with a `reindent`-based one via a `cssAdapter`. Keep `token.go` and `TokenSignature`. Delete the parser/layout. Rewrite the CSS tests and update the printer's `<style>` tests that asserted reflow.

**Files:**
- Modify: `internal/cssfmt/cssfmt.go` (replace `Format`; add `cssAdapter`/`splitWS`; delete parser+layout; drop the `pretty` import)
- Modify: `internal/cssfmt/cssfmt_test.go` (rewrite reflow assertions → re-indent assertions)
- Modify: `internal/printer/style_test.go` (the reflow-dependent assertions)
- Keep: `internal/cssfmt/token.go` (unchanged), `TokenSignature` (unchanged)

**Interfaces:**
- Consumes: `reindent` (Task 1); `tokenize`, token kinds (`token.go`).
- Produces: `func Format(src []byte, width int) ([]byte, error)` (signature unchanged; `width` retained but unused); `TokenSignature` unchanged.

- [ ] **Step 1: Rewrite the cssfmt tests (these define the new behavior)**

Replace the body of `internal/cssfmt/cssfmt_test.go` with:

```go
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

func TestReindentFixesIndentation(t *testing.T) {
	in := ".a {\n      color: red;\n  background: blue;\n}"
	want := ".a {\n\tcolor: red;\n\tbackground: blue;\n}"
	if got := fmtCSS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestOneLinerStaysOneLine(t *testing.T) {
	// A minified rule is NOT reflowed — only its (absent) indentation is touched.
	in := ".a{color:red;background:blue}"
	if got := fmtCSS(t, in); got != in {
		t.Fatalf("one-liner must not be reflowed: got %q", got)
	}
}

func TestNoBlankLinesInvented(t *testing.T) {
	in := ".a {\n\tcolor: red;\n}\n.b {\n\tmargin: 0;\n}"
	got := fmtCSS(t, in)
	if strings.Contains(got, "}\n\n.b") {
		t.Fatalf("a blank line was invented between rules:\n%s", got)
	}
}

func TestExistingBlankLinesPreserved(t *testing.T) {
	in := ".a {\n\tcolor: red;\n}\n\n.b {\n\tmargin: 0;\n}"
	if got := fmtCSS(t, in); got != in {
		t.Fatalf("existing blank line not preserved:\n%s", got)
	}
}

func TestNestedAtRuleIndents(t *testing.T) {
	in := "@media (min-width: 600px) {\n.a {\ncolor: red;\n}\n}"
	want := "@media (min-width: 600px) {\n\t.a {\n\t\tcolor: red;\n\t}\n}"
	if got := fmtCSS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMultiLineCommentInteriorUntouched(t *testing.T) {
	in := ".a {\n\t/* keep\n   me */\n\tcolor: red;\n}"
	got := fmtCSS(t, in)
	if !strings.Contains(got, "/* keep\n   me */") {
		t.Fatalf("multi-line comment interior was re-indented:\n%s", got)
	}
}

func TestSentinelPreserved(t *testing.T) {
	in := ".a {\ncolor: __gsxhole_0_;\n}"
	got := fmtCSS(t, in)
	if !strings.Contains(got, "__gsxhole_0_") {
		t.Fatalf("sentinel mangled:\n%s", got)
	}
}

func TestUnterminatedStringErrors(t *testing.T) {
	// A tokenizer error (unterminated string) → error → caller falls back verbatim.
	if _, err := Format([]byte(".a{content:\"oops}"), 80); err == nil {
		t.Fatal("expected error on unterminated string")
	}
}

func TestFormatIdempotent(t *testing.T) {
	once := fmtCSS(t, ".a {\n   color: red;\n}\n.b{margin:0}")
	twice := fmtCSS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
	}
}

// TokenSignature is retained as the CSS faithfulness oracle.
func TestTokenSignatureIgnoresWhitespace(t *testing.T) {
	if TokenSignature([]byte("h1,h2{margin:0}")) != TokenSignature([]byte("h1, h2 {\n\tmargin: 0;\n}\n")) {
		t.Fatal("whitespace/optional-semicolon changed the signature")
	}
}
```

(Delete the old `token_test.go` assertions only if they referenced removed symbols — they test `tokenize`, which is unchanged, so leave `token_test.go` as is.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cssfmt/`
Expected: FAIL — the new tests expect re-indent behavior the current parse+reflow `Format` doesn't produce (e.g. `TestOneLinerStaysOneLine` fails because the current `Format` reflows).

- [ ] **Step 3: Replace `Format` and delete the parser/layout**

In `internal/cssfmt/cssfmt.go`: keep the package doc (update it), keep `TokenSignature`, **delete** everything from `func Format` through the end of the file (the parser type, `parseItems`, `parseStatement`, `trimWS`, `layoutItems`, `layoutItem`, `layoutTopLevel`, `layoutPrelude`, `layoutDecl`, `renderInline`, `splitTopLevel`, `splitFirst`), and the `fmt`/`pretty` imports. Replace with:

```go
// Package cssfmt re-indents the CSS inside <style> bodies during gsx fmt. It is
// a conservative token-pass: it normalizes leading indentation to tabs by brace
// depth and changes nothing else — no reflow, no invented/stripped blank lines,
// no intra-line spacing changes. Strings and /* */ comments are opaque. It is
// the built-in rawfmt.Formatter for <style>; rawfmt owns the outer (tag-depth)
// indent.
package cssfmt

import (
	"strings"

	"github.com/gsxhq/gsx/internal/reindent"
)

// Format re-indents a self-contained CSS source string. width is accepted for
// interface symmetry with the rawfmt.Formatter wiring but is unused (a
// re-indenter does not wrap on width). Returns an error only on a tokenizer
// error (unterminated string/comment) so the caller falls back to verbatim.
func Format(src []byte, width int) ([]byte, error) {
	out, ok := reindent.Reindent(src, cssAdapter{})
	if !ok {
		return nil, errUnterminated
	}
	return []byte(out), nil
}

var errUnterminated = stringError("cssfmt: unterminated string or comment")

type stringError string

func (e stringError) Error() string { return string(e) }

// cssAdapter maps the CSS tokenizer's tokens onto reindent.Class.
type cssAdapter struct{}

func (cssAdapter) Tokenize(src []byte) ([]reindent.Token, bool) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, false
	}
	var out []reindent.Token
	for _, t := range toks {
		switch t.kind {
		case tWS:
			out = append(out, splitWS(t.text)...)
		case tComment, tString:
			// /* */ may span lines; CSS strings do not — both opaque.
			out = append(out, reindent.Token{Class: reindent.Opaque, Text: t.text})
		case tLBrace, tLParen:
			out = append(out, reindent.Token{Class: reindent.Open, Text: t.text})
		case tRBrace, tRParen:
			out = append(out, reindent.Token{Class: reindent.Close, Text: t.text})
		default:
			out = append(out, reindent.Token{Class: reindent.Other, Text: t.text})
		}
	}
	return out, true
}

// splitWS turns a CSS whitespace run (which may contain newlines) into Newline
// tokens (one per '\n', preserving blank lines) and Space tokens for the rest.
// '\r' is dropped (CRLF → LF).
func splitWS(text string) []reindent.Token {
	var out []reindent.Token
	var sp strings.Builder
	flush := func() {
		if sp.Len() > 0 {
			out = append(out, reindent.Token{Class: reindent.Space, Text: sp.String()})
			sp.Reset()
		}
	}
	for _, r := range text {
		switch r {
		case '\n':
			flush()
			out = append(out, reindent.Token{Class: reindent.Newline, Text: "\n"})
		case '\r':
			// drop
		default:
			sp.WriteRune(r)
		}
	}
	flush()
	return out
}
```

Keep `TokenSignature` exactly as it is (it still imports nothing new — it uses `tokenize` and `strings`).

- [ ] **Step 4: Run cssfmt tests**

Run: `go test ./internal/cssfmt/`
Expected: PASS (all rewritten tests + the unchanged `token_test.go`).

- [ ] **Step 5: Update the printer's `<style>` reflow assertions**

The printer test `internal/printer/style_test.go` has assertions that expected the old reflow (e.g. `TestStyleBodyFormatted` checking `color: red;` on its own line, and `TestStyleMalformedFallsBackVerbatim` relying on unbalanced braces erroring). Update them for re-indent behavior:

Replace `TestStyleBodyFormatted` with a test that the body is re-indented (not reflowed):
```go
func TestStyleBodyReindented(t *testing.T) {
	// Messy indentation inside <style> gets normalized to tabs; content otherwise
	// unchanged (no reflow).
	src := "package p\n\ncomponent C() {\n\t<style>\n        .a {\n   color: red;\n        }\n\t</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\t\t\t.a {") || !strings.Contains(out, "\t\t\t\tcolor: red;") {
		t.Fatalf("style body not re-indented to tabs:\n%s", out)
	}
}
```

Replace `TestStyleMalformedFallsBackVerbatim` (unbalanced braces no longer error — use an unterminated string, which DOES error → verbatim):
```go
func TestStyleUnterminatedStringFallsBackVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>.a{content:\"oops}</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ".a{content:\"oops}") {
		t.Fatalf("unterminated-string CSS should be left verbatim:\n%s", out)
	}
}
```

Leave `TestStyleBodyHolePreserved` and `TestStyleBodyIdempotent` as is (they still hold — holes preserved, idempotent). `internal/printer/style_property_test.go` is whitespace-agnostic (faithfulness/idempotence) and still passes.

- [ ] **Step 6: Run the printer + cssfmt suites**

Run: `go test ./internal/cssfmt/ ./internal/printer/`
Expected: PASS. (The corpus faithfulness tests still pass — `TokenSignature` is whitespace-agnostic, and the re-indenter only changes whitespace.)

- [ ] **Step 7: Commit**

```bash
git add internal/cssfmt/ internal/printer/style_test.go
git commit -m "refactor(cssfmt): re-indenter (drop parse+reflow); fix only indentation"
```

---

### Task 3: `internal/jsfmt` — JS re-indenter over the tdewolff lexer

A `jsAdapter` driving `jsmin`'s tdewolff lexer (mirroring its loop, including `regexPosition`), feeding the shared core. Plus `TokenSignature` for the `<script>` faithfulness oracle.

**Files:**
- Create: `internal/jsfmt/jsfmt.go`
- Test: `internal/jsfmt/jsfmt_test.go`

**Interfaces:**
- Consumes: `reindent` (Task 1); `github.com/tdewolff/parse/v2`, `.../v2/js`.
- Produces: `func Format(src []byte, width int) ([]byte, error)`; `func TokenSignature(src []byte) string`.

> Note for the implementer: verify the exact tdewolff token-type constant names with `GOFLAGS=-mod=mod go doc github.com/tdewolff/parse/v2/js` before relying on them — at minimum `OpenBraceToken`/`CloseBraceToken`, `OpenParenToken`/`CloseParenToken`, `OpenBracketToken`/`CloseBracketToken`, `StringToken`, `TemplateToken`/`TemplateStartToken`/`TemplateMiddleToken`/`TemplateEndToken`, `RegExpToken`, `WhitespaceToken`, `LineTerminatorToken`, `CommentToken`, `CommentLineTerminatorToken`, `DivToken`/`DivEqToken`. `regexPosition` (copied below) already references several, confirming they exist.

- [ ] **Step 1: Write the failing tests**

```go
// internal/jsfmt/jsfmt_test.go
package jsfmt

import (
	"strings"
	"testing"
)

func fmtJS(t *testing.T, in string) string {
	t.Helper()
	out, err := Format([]byte(in), 80)
	if err != nil {
		t.Fatalf("Format(%q) error: %v", in, err)
	}
	return string(out)
}

func TestReindentsToTabs(t *testing.T) {
	in := "function f() {\n      const x = 1;\n   if (x) {\nreturn x;\n   }\n}"
	want := "function f() {\n\tconst x = 1;\n\tif (x) {\n\t\treturn x;\n\t}\n}"
	if got := fmtJS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestKeepsNewlinesNoReflow(t *testing.T) {
	// A one-line body is NOT reflowed (ASI-preserving: line structure untouched).
	in := "const x=1;const y=2"
	if got := fmtJS(t, in); got != in {
		t.Fatalf("must not reflow/reformat a one-liner: got %q", got)
	}
}

func TestPreservesBlankLines(t *testing.T) {
	in := "a();\n\nb();"
	if got := fmtJS(t, in); got != in {
		t.Fatalf("blank line not preserved: got %q", got)
	}
}

func TestTemplateLiteralInteriorUntouched(t *testing.T) {
	in := "const t = `line1\n   line2`;"
	got := fmtJS(t, in)
	if !strings.Contains(got, "`line1\n   line2`") {
		t.Fatalf("template literal interior was re-indented:\n%s", got)
	}
}

func TestRegexNotMislexedAsDivision(t *testing.T) {
	// `/re/g` after `=` is a regex; must pass through verbatim, not break.
	in := "const re = /a\\/b/g;\nconst q = a / b;"
	got := fmtJS(t, in)
	if !strings.Contains(got, "/a\\/b/g") || !strings.Contains(got, "a / b") {
		t.Fatalf("regex/division mishandled:\n%s", got)
	}
}

func TestCommentInteriorUntouched(t *testing.T) {
	in := "function f() {\n\t/* a\n  b */\n\tx();\n}"
	got := fmtJS(t, in)
	if !strings.Contains(got, "/* a\n  b */") {
		t.Fatalf("block comment interior re-indented:\n%s", got)
	}
}

func TestIdempotent(t *testing.T) {
	once := fmtJS(t, "function f(){\n   const x=1\nif(x){\nreturn x\n}\n}")
	twice := fmtJS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
	}
}

func TestTokenSignatureIgnoresWhitespace(t *testing.T) {
	a := TokenSignature([]byte("const x=1;function f(){return x}"))
	b := TokenSignature([]byte("const x = 1;\nfunction f() {\n\treturn x;\n}"))
	if a != b {
		t.Fatalf("whitespace changed the signature:\n%q\n%q", a, b)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jsfmt/`
Expected: FAIL — `undefined: Format`.

- [ ] **Step 3: Implement `jsfmt`**

```go
// internal/jsfmt/jsfmt.go

// Package jsfmt re-indents the JavaScript inside executable <script> bodies
// during gsx fmt. It is a conservative token-pass driven by tdewolff's JS LEXER
// (no parser): it normalizes leading indentation to tabs by brace depth and
// changes nothing else — it KEEPS every newline, so automatic semicolon
// insertion is never altered (identical safety to internal/jsmin). Strings,
// template literals, regex, and comments are opaque. It is the built-in
// rawfmt.Formatter for <script>; rawfmt owns the outer (tag-depth) indent.
package jsfmt

import (
	"bytes"
	"io"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"

	"github.com/gsxhq/gsx/internal/reindent"
)

// Format re-indents a self-contained JS source string. width is accepted for
// interface symmetry with the rawfmt.Formatter wiring but is unused. Returns an
// error only on a lexer error so the caller falls back to verbatim.
func Format(src []byte, width int) ([]byte, error) {
	out, ok := reindent.Reindent(src, jsAdapter{})
	if !ok {
		return nil, stringError("jsfmt: lex error")
	}
	return []byte(out), nil
}

type stringError string

func (e stringError) Error() string { return string(e) }

// TokenSignature returns a whitespace- and comment-agnostic signature of src:
// the significant tokens joined by "\x1f". Two JS strings with the same
// significant tokens (e.g. messy vs re-indented) share a signature. On a lex
// error it returns the raw source prefixed with "\x00err\x00" so malformed JS
// (left verbatim by the printer) compares equal to itself.
func TokenSignature(src []byte) string {
	toks, ok := lexClassified(src)
	if !ok {
		return "\x00err\x00" + string(src)
	}
	var sig []string
	for _, t := range toks {
		switch t.Class {
		case reindent.Space, reindent.Newline, reindent.Opaque:
			// Opaque covers comments (insignificant) AND string/template/regex
			// literals (significant) — but literals are emitted verbatim by the
			// re-indenter, so they never change across formatting; to keep the
			// signature discriminating we still include literals, EXCLUDING
			// comments. Distinguish below.
			if t.Class == reindent.Opaque && !isComment(t.Text) {
				sig = append(sig, t.Text)
			}
		default:
			sig = append(sig, t.Text)
		}
	}
	return strings.Join(sig, "\x1f")
}

func isComment(s string) bool {
	return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*")
}

// jsAdapter drives tdewolff's JS lexer and classifies tokens for reindent.
type jsAdapter struct{}

func (jsAdapter) Tokenize(src []byte) ([]reindent.Token, bool) { return lexClassified(src) }

func lexClassified(src []byte) ([]reindent.Token, bool) {
	l := js.NewLexer(parse.NewInputString(string(src)))
	var toks []reindent.Token
	prevTT := js.ErrorToken
	for {
		tt, data := l.Next()
		if tt == js.ErrorToken {
			if err := l.Err(); err != nil && err != io.EOF {
				return nil, false // real lex error
			}
			break // clean EOF
		}
		switch tt {
		case js.WhitespaceToken:
			toks = append(toks, reindent.Token{Class: reindent.Space, Text: string(data)})
		case js.LineTerminatorToken:
			// Emit one Newline per '\n' so blank lines are preserved.
			for i := 0; i < bytes.Count(data, []byte("\n")); i++ {
				toks = append(toks, reindent.Token{Class: reindent.Newline, Text: "\n"})
			}
		case js.CommentToken, js.CommentLineTerminatorToken:
			// // and /* */ (the latter may span lines) — opaque, verbatim.
			toks = append(toks, reindent.Token{Class: reindent.Opaque, Text: string(data)})
		case js.DivToken, js.DivEqToken:
			if tt == js.DivToken && regexPosition(prevTT) {
				rtt, rdata := l.RegExp()
				if rtt == js.RegExpToken {
					toks = append(toks, reindent.Token{Class: reindent.Opaque, Text: string(rdata)})
					prevTT = js.RegExpToken
					continue
				}
			}
			toks = append(toks, reindent.Token{Class: reindent.Other, Text: string(data)})
			prevTT = tt
		default:
			toks = append(toks, classify(tt, data))
			prevTT = tt
		}
	}
	return toks, true
}

func classify(tt js.TokenType, data []byte) reindent.Token {
	switch tt {
	case js.StringToken, js.TemplateToken, js.TemplateStartToken,
		js.TemplateMiddleToken, js.TemplateEndToken, js.RegExpToken:
		return reindent.Token{Class: reindent.Opaque, Text: string(data)}
	case js.OpenBraceToken, js.OpenParenToken, js.OpenBracketToken:
		return reindent.Token{Class: reindent.Open, Text: string(data)}
	case js.CloseBraceToken, js.CloseParenToken, js.CloseBracketToken:
		return reindent.Token{Class: reindent.Close, Text: string(data)}
	default:
		return reindent.Token{Class: reindent.Other, Text: string(data)}
	}
}

// regexPosition reports whether a DivToken following prev should be re-lexed as
// a regex literal rather than division. Copied from internal/jsmin (same
// tdewolff-lexer disambiguation) — kept local rather than shared, mirroring how
// jsmin and jsx each keep their own copy.
func regexPosition(prev js.TokenType) bool {
	switch {
	case prev == js.ErrorToken: // start of input
		return true
	case js.IsIdentifier(prev): // a / b
		return false
	case js.IsNumeric(prev): // 1 / 2
		return false
	}
	switch prev {
	case js.CloseParenToken, js.CloseBracketToken, js.CloseBraceToken,
		js.IncrToken, js.DecrToken, js.RegExpToken, js.StringToken,
		js.TemplateToken, js.TemplateEndToken, js.TrueToken, js.FalseToken,
		js.NullToken, js.ThisToken, js.SuperToken:
		return false
	default:
		return true
	}
}
```

- [ ] **Step 4: Run jsfmt tests**

Run: `go test ./internal/jsfmt/`
Expected: PASS (all tests). If a tdewolff constant name is wrong, `go doc` it and correct it; do not change the test expectations.

- [ ] **Step 5: Commit**

```bash
git add internal/jsfmt/
git commit -m "feat(jsfmt): JS re-indenter over the tdewolff lexer (ASI-safe, keeps newlines)"
```

---

### Task 4: Wire `<script>` through `rawfmt` in the printer

Route executable `<script>` bodies through `rawfmt` with the JS re-indenter (mirror `<style>`), with a data-island guard. Thread `jsFmt` through `gsxfmt`.

**Files:**
- Modify: `internal/printer/printer.go`
- Modify: `internal/gsxfmt/gsxfmt.go`
- Test: `internal/printer/script_test.go` (Create)

**Interfaces:**
- Consumes: `jsfmt.Format` (Task 3); `rawfmt`; existing `nodesToBody`/`renderHole`.
- Produces:
  - `printer` struct gains `jsFmt rawfmt.Formatter`.
  - `Fprint(w, f, width)` unchanged signature; defaults both formatters.
  - `func FprintWith(w io.Writer, f *ast.File, width int, cssFmt, jsFmt rawfmt.Formatter) error` (adds `jsFmt` param).
  - `func defaultJSFormatter(width int) rawfmt.Formatter`.
  - `func isExecutableScript(e *ast.Element) bool`.
  - `gsxfmt.FormatRemovingImportsWith(name string, src []byte, unused []ImportRef, width int, cssFmt, jsFmt rawfmt.Formatter) ([]byte, error)`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/printer/script_test.go
package printer

import (
	"strings"
	"testing"
)

func TestScriptBodyReindented(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>\nfunction f() {\nreturn 1\n}\n\t</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	// Re-indented to tabs under the tag depth (component=1, body=2, inside fn=3).
	if !strings.Contains(out, "\t\tfunction f() {") || !strings.Contains(out, "\t\t\treturn 1") {
		t.Fatalf("script body not re-indented:\n%s", out)
	}
}

func TestScriptHolePreserved(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>const u = @{ user.ID }</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@{ user.ID }") || strings.Contains(out, "__gsxhole") {
		t.Fatalf("hole not preserved / sentinel leaked:\n%s", out)
	}
}

func TestDataIslandScriptLeftVerbatim(t *testing.T) {
	// type="application/json" is NOT executable JS — left verbatim.
	src := "package p\n\ncomponent C() {\n\t<script type=\"application/json\">  {\"a\":1}  </script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  {\"a\":1}  ") {
		t.Fatalf("data-island script should be verbatim:\n%s", out)
	}
}

func TestScriptIdempotent(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>\nif(x){\nf()\n}\n\t</script>\n}\n"
	once, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normPrint(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("script fmt not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/printer/ -run 'TestScript|TestDataIsland'`
Expected: FAIL — `<script>` is currently verbatim, so it is not re-indented.

- [ ] **Step 3: Implement the wiring**

In `internal/printer/printer.go`:

1. Add `"github.com/gsxhq/gsx/internal/jsfmt"` to imports. Add the field:
```go
type printer struct {
	err    error
	cssFmt rawfmt.Formatter
	jsFmt  rawfmt.Formatter
}
```

2. Replace `Fprint`/`FprintWith`/`defaultCSSFormatter` with:
```go
func Fprint(w io.Writer, f *ast.File, width int) error {
	return FprintWith(w, f, width, defaultCSSFormatter(width), defaultJSFormatter(width))
}

// FprintWith is Fprint with explicit CSS and JS formatters for <style>/<script>
// bodies. A nil formatter leaves that body verbatim.
func FprintWith(w io.Writer, f *ast.File, width int, cssFmt, jsFmt rawfmt.Formatter) error {
	p := printer{cssFmt: cssFmt, jsFmt: jsFmt}
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width))
	return err
}

func defaultCSSFormatter(width int) rawfmt.Formatter {
	return func(src []byte) ([]byte, error) { return cssfmt.Format(src, width) }
}

func defaultJSFormatter(width int) rawfmt.Formatter {
	return func(src []byte) ([]byte, error) { return jsfmt.Format(src, width) }
}
```

3. Replace the `<script>` branch in `element()` (currently `printer.go:196-198`):
```go
	if strings.EqualFold(e.Tag, "script") {
		if p.jsFmt != nil && isExecutableScript(e) {
			segments, holes := nodesToBody(e.Children)
			if doc, ok := rawfmt.Format(segments, holes, p.jsFmt); ok {
				return pretty.Concat(openTag, doc, close)
			}
		}
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
```

4. Add the executable-script helper (near `isPreserveTag`):
```go
// jsExecutableScriptTypes are <script type> values that run as JavaScript.
// Mirrors internal/jsx.jsExecutableTypes (kept local to avoid importing the
// codegen-time jsx package into the formatter path).
var jsExecutableScriptTypes = map[string]bool{
	"text/javascript": true, "module": true, "application/javascript": true,
	"text/ecmascript": true, "application/ecmascript": true,
}

// isExecutableScript reports whether a <script> runs as JavaScript: no static
// type attribute, or a static type in the executable set. A data island (e.g.
// type="application/json", type="text/template") is not executable and is left
// verbatim.
func isExecutableScript(e *ast.Element) bool {
	for _, a := range e.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			t := strings.ToLower(strings.TrimSpace(sa.Value))
			return t == "" || jsExecutableScriptTypes[t]
		}
	}
	return true
}
```

In `internal/gsxfmt/gsxfmt.go`: change `FormatRemovingImportsWith` to thread both formatters.
```go
// FormatRemovingImportsWith is FormatRemovingImports with explicit CSS and JS
// formatters for <style>/<script> bodies (nil → built-in default at width).
func FormatRemovingImportsWith(name string, src []byte, unused []ImportRef, width int, cssFmt, jsFmt rawfmt.Formatter) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	removeImports(f, unused)
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if cssFmt == nil && jsFmt == nil {
		if err := printer.Fprint(&b, f, width); err != nil {
			return nil, err
		}
	} else {
		if err := printer.FprintWith(&b, f, width, cssFmt, jsFmt); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
```

`FormatRemovingImportsWith` already exists with a single `cssFmt` param (CSS work); this widens it to `(cssFmt, jsFmt)`. `Format`/`FormatRemovingImports` stay unchanged (they call `printer.Fprint` = both defaults).

To keep the build green at this task, update the one existing caller in `gen/fmt.go` to pass `nil` for the new `jsFmt` arg (Task 5 threads the real value). `runFmt`'s signature still has its CSS-era trailing `cssFmt` param here:
```go
		formatted, err := gsxfmt.FormatRemovingImportsWith(path, orig, unusedByPath[abs], width, cssFmt, nil)
```
With the CLI's typical `cssFmt == nil`, this is `(nil, nil)` → `printer.Fprint` → both built-in defaults, so `<script>` (and `<style>`) are re-indented by the defaults even before Task 5.

- [ ] **Step 4: Run printer + gsxfmt**

Run: `go test ./internal/printer/ ./internal/gsxfmt/`
Expected: the new `TestScript*`/`TestDataIsland*` pass. The corpus faithfulness tests for `<script>`-bearing inputs may now FAIL (reformatting changed bytes) — that is EXPECTED and fixed in Task 6. Record which fail; if anything other than `TestCorpusFaithfulness`/`TestCorpusInputsProperty` fails, investigate.

- [ ] **Step 5: Build (signature ripple resolved)**

Run: `go build ./...`
Expected: clean — the `gen/fmt.go` caller was updated to the `(cssFmt, nil)` form above, so the tree builds.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/printer.go internal/printer/script_test.go internal/gsxfmt/gsxfmt.go gen/fmt.go
git commit -m "feat(printer): re-indent <script> bodies via rawfmt + jsfmt (data-island guard)"
```

---

### Task 5: `gen.WithJSFormatter` option + threading

Mirror the existing `WithCSSFormatter` wiring for JS, and update the `runFmt`/`gsxfmt` call sites for the new two-formatter signature.

**Files:**
- Modify: `gen/main.go` (config field; `case "fmt"` threads `cfg.jsFmt`)
- Modify: `gen/options.go` (`WithJSFormatter`)
- Modify: `gen/configfile.go` (`mergeConfig` rule)
- Modify: `gen/fmt.go` (`runFmt` signature + the `gsxfmt` call)
- Test: `gen/jsformatter_test.go` (Create)

**Interfaces:**
- Consumes: `rawfmt.Formatter`; `gsxfmt.FormatRemovingImportsWith(...,cssFmt,jsFmt)` (Task 4).
- Produces: `config.jsFmt`; `WithJSFormatter`; `mergeConfig` rule; `runFmt(stdout, stderr, args, cssFmt, jsFmt)`.

- [ ] **Step 1: Write the failing tests**

```go
// gen/jsformatter_test.go
package gen

import (
	"testing"

	"github.com/gsxhq/gsx/internal/rawfmt"
)

func TestWithJSFormatterOption(t *testing.T) {
	var cfg config
	WithJSFormatter(rawfmt.Formatter(func(src []byte) ([]byte, error) { return src, nil }))(&cfg)
	if cfg.jsFmt == nil {
		t.Fatal("WithJSFormatter did not set cfg.jsFmt")
	}
}

func TestMergeConfigJSFormatterOptsWins(t *testing.T) {
	base := config{jsFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	opts := config{jsFmt: func(src []byte) ([]byte, error) { return []byte("opts"), nil }}
	got, _ := mergeConfig(base, opts).jsFmt(nil)
	if string(got) != "opts" {
		t.Fatalf("merged.jsFmt = %q, want opts override", got)
	}
}

func TestMergeConfigJSFormatterFallsBackToBase(t *testing.T) {
	base := config{jsFmt: func(src []byte) ([]byte, error) { return []byte("base"), nil }}
	merged := mergeConfig(base, config{})
	if merged.jsFmt == nil {
		t.Fatal("merged.jsFmt should fall back to base")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./gen/ -run 'TestWithJSFormatter|TestMergeConfigJSFormatter'`
Expected: FAIL — `cfg.jsFmt` / `WithJSFormatter` undefined.

- [ ] **Step 3: Implement**

In `gen/main.go`, add to the `config` struct (next to `cssFmt`):
```go
	jsFmt rawfmt.Formatter
```
Change the `case "fmt":` dispatch to pass both:
```go
	case "fmt":
		// fmt respects gsx.toml printWidth per-dir (via printWidthFor inside
		// runFmt) and tolerates a malformed config. The CSS/JS formatter
		// overrides are programmatic options (no gsx.toml entry), so they come
		// from cfg directly — not resolveConfig (which would hard-fail on a bad
		// config).
		return runFmt(stdout, stderr, cmdArgs, cfg.cssFmt, cfg.jsFmt)
```

In `gen/options.go` (next to `WithCSSFormatter`):
```go
// WithJSFormatter installs a custom JS formatter for executable <script> bodies
// during `gsx fmt`, replacing the built-in re-indenter. It receives complete,
// self-contained JS (interpolation holes are substituted with sentinel tokens
// before it runs and restored afterward) and returns the formatted JS, or an
// error to fall back to verbatim. Wrap any whole-buffer formatter (prettier,
// biome, esbuild) in this signature:
//
//	gen.Main(gen.WithJSFormatter(func(js []byte) ([]byte, error) { … }))
func WithJSFormatter(f rawfmt.Formatter) Option {
	return func(cfg *config) { cfg.jsFmt = f }
}
```

In `gen/configfile.go` `mergeConfig`, after the `cssFmt` block:
```go
	merged.jsFmt = base.jsFmt
	if opts.jsFmt != nil {
		merged.jsFmt = opts.jsFmt
	}
```

In `gen/fmt.go`, change `runFmt`'s signature and the format call:
```go
func runFmt(stdout, stderr io.Writer, args []string, cssFmt, jsFmt rawfmt.Formatter) int {
```
and at the `gsxfmt.FormatRemovingImportsWith` call:
```go
		formatted, err := gsxfmt.FormatRemovingImportsWith(path, orig, unusedByPath[abs], width, cssFmt, jsFmt)
```
Update the existing test caller `gen/fmt_test.go` (the `runFmt(&out, &errb, args, nil)` line from the CSS work) to:
```go
	code := runFmt(&out, &errb, args, nil, nil)
```

- [ ] **Step 4: Run tests + full build**

Run: `go test ./gen/ -run 'TestWithJSFormatter|TestMergeConfigJSFormatter|TestConfigAgnostic' && go build ./... && go vet ./gen/...`
Expected: PASS; build clean (the Task-4 signature ripple is now resolved); `TestConfigAgnosticCommandsSurviveMalformedConfig` still passes.

- [ ] **Step 5: Commit**

```bash
git add gen/main.go gen/options.go gen/configfile.go gen/fmt.go gen/fmt_test.go gen/jsformatter_test.go
git commit -m "feat(gen): WithJSFormatter plug point threaded through gsx fmt"
```

---

### Task 6: `<script>`-aware faithfulness in the property tests

Extend the `<style>` faithfulness canonicalizer to also canonicalize `<script>` bodies (via `jsfmt.TokenSignature`), so `TestCorpusFaithfulness`/`TestCorpusInputsProperty` pass again on `<script>`-bearing inputs, and add a focused `<script>` property test.

**Files:**
- Modify: `internal/printer/corpus_test.go` (generalize the style canonicalizer to script)
- Test: `internal/printer/script_property_test.go` (Create)

**Interfaces:**
- Consumes: `jsfmt.TokenSignature` (Task 3); existing `canonStyleBodies`/`styleSignature`/`renderHole`.
- Produces: `canonEmbeddedBodies` covering both `<style>` and `<script>`.

- [ ] **Step 1: Add the failing property test**

```go
// internal/printer/script_property_test.go
package printer

import (
	"reflect"
	"testing"
)

var scriptCases = []string{
	"package p\n\ncomponent C() {\n\t<script>function f(){return 1}</script>\n}\n",
	"package p\n\ncomponent C() {\n\t<script>\nconst x = 1;\n\nconst y = 2;\n\t</script>\n}\n",
	"package p\n\ncomponent C() {\n\t<script>const u = @{ user.ID };const re = /a\\/b/g</script>\n}\n",
	"package p\n\ncomponent C() {\n\t<script type=\"application/json\">{\"a\":1}</script>\n}\n",
}

func TestScriptPropertyFaithfulAndIdempotent(t *testing.T) {
	for _, src := range scriptCases {
		formatted, err := normPrint(t, src)
		if err != nil {
			t.Errorf("fmt failed: %v\n%s", err, src)
			continue
		}
		if !reflect.DeepEqual(normalizedAST(t, src), normalizedAST(t, formatted)) {
			t.Errorf("fmt changed normalized AST (not faithful):\n--- src ---\n%s\n--- fmt ---\n%s", src, formatted)
		}
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

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/printer/ -run 'TestScriptProperty'`
Expected: FAIL — `normalizedAST` canonicalizes `<style>` but not `<script>`, so the reformatted `<script>` body differs byte-wise.

- [ ] **Step 3: Generalize the canonicalizer to `<script>`**

In `internal/printer/corpus_test.go`, add `"github.com/gsxhq/gsx/internal/jsfmt"` to imports. Replace `canonStyleBodies` with a version covering both tags, and call it from `normalizedAST` (where `canonStyleBodies(f)` is currently called):

```go
// canonEmbeddedBodies replaces every <style> AND <script> element's children
// with one synthetic Text holding a canonical signature of the body (the
// language's whitespace-agnostic token signature, with each hole sentinel mapped
// back to its rendered text). This makes the faithfulness comparison check
// token-equivalence + hole-sequence — the contract the re-indenter satisfies —
// rather than the byte-identity that re-indentation deliberately breaks.
func canonEmbeddedBodies(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		el, ok := n.(*ast.Element)
		if !ok {
			return true
		}
		switch {
		case strings.EqualFold(el.Tag, "style"):
			el.Children = []ast.Markup{&ast.Text{Value: embeddedSignature(el.Children, cssfmt.TokenSignature)}}
			return false
		case strings.EqualFold(el.Tag, "script"):
			el.Children = []ast.Markup{&ast.Text{Value: embeddedSignature(el.Children, jsfmt.TokenSignature)}}
			return false
		}
		return true
	})
}

// embeddedSignature builds the canonical signature: the body's placeholdered
// text (holes → a fixed sentinel) run through sig (a language TokenSignature),
// with each sentinel mapped back to its rendered hole. Both src and fmt(src)
// reduce to the same string iff their token streams and hole sequences match.
func embeddedSignature(nodes []ast.Markup, sig func([]byte) string) string {
	const sent = "\x00H"
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
	s := sig([]byte(body.String()))
	for i, h := range holes {
		s = strings.ReplaceAll(s, sent+strconv.Itoa(i)+"\x00", h)
	}
	return s
}
```

Delete the old `canonStyleBodies` and `styleSignature` (replaced by the two functions above), and change the call in `normalizedAST` from `canonStyleBodies(f)` to `canonEmbeddedBodies(f)`.

> Note: the fixed sentinel `\x00H<i>\x00` must survive each language's tokenizer as part of a token (so it appears in the signature and can be mapped back). `\x00` is a word/identifier byte for both the CSS tokenizer and tdewolff's JS lexer, so `\x00H0\x00` lexes inside an identifier-ish token. If a future tokenizer rejects `\x00`, switch the sentinel to a token both lexers accept. The malformed/data-island cases hit the `\x00err\x00` (or verbatim) path and compare equal to themselves.

- [ ] **Step 4: Run the property + faithfulness tests**

Run: `go test ./internal/printer/ -run 'TestScriptProperty|TestStyleProperty|TestCorpusFaithfulness|TestCorpusInputsProperty'`
Expected: PASS — all. If a corpus case still fails, it means a `<script>`/`<style>` body's token signature genuinely differs across formatting (a real formatter bug) — STOP and report it with the case and both signatures; do not weaken the oracle.

- [ ] **Step 5: Full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: all green, vet clean.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/corpus_test.go internal/printer/script_property_test.go
git commit -m "test(printer): <script>-aware faithfulness (JS token-equivalence + hole-sequence)"
```

---

## Final Verification (after all tasks)

- [ ] `go test ./...` green; `go vet ./...` clean.
- [ ] `gopls check -severity=hint internal/reindent/reindent.go internal/jsfmt/jsfmt.go internal/cssfmt/cssfmt.go` — no unused functions (the old cssfmt helpers are gone).
- [ ] Manual smoke on real files:
  ```bash
  go build -o /tmp/gsx-jsfmt ./cmd/gsx
  printf 'package p\n\ncomponent C() {\n\t<script>\nfunction f() {\n      const x = 1\n   if (x) {\nreturn x\n   }\n}\n\t</script>\n\t<style>\n      .a { color: red }\n</style>\n}\n' > /tmp/smoke.gsx
  /tmp/gsx-jsfmt fmt /tmp/smoke.gsx
  ```
  Expect: `<script>` and `<style>` bodies re-indented to consistent tabs under the tag depth, **no reflow, no invented blank lines**, and a second `gsx fmt` produces identical output (idempotent).
- [ ] Update `docs/guide/extensions.md`: drop the CSS "in development" caveat (shipped), and note the JS re-indenter + `WithJSFormatter` have landed. (Fold into the Task 5 or final commit.)
