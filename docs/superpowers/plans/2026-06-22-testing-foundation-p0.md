# Testing Foundation — Increment 1 (P0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lay the first foundation stones for a comprehensive compiler+HTML-templating test suite: a differential escaper oracle vs `html/template`, honest coverage measurement, a codegen-diagnostic-position audit, and a corpus contributor README.

**Architecture:** Test/tooling/docs only (no production code changes unless the differential test finds a real escaper bug). Extends the existing root-package escaper tests and the `internal/corpus` spine; adds a root `Makefile`. Follows the spec [2026-06-22-testing-foundation-p0-design.md](../specs/2026-06-22-testing-foundation-p0-design.md).

**Tech Stack:** Go 1.26; `html/template` (the oracle); `go test -coverpkg`; root package `gsx` (escaper lives in `escape.go`, tests in `escape_test.go`/`fuzz_test.go`).

## Global Constraints

- No production code changes EXCEPT fixing a genuine escaper bug the differential test surfaces (in `escape.go`), which must be called out.
- Do NOT change anti-recommendation areas: structural HTML compare, the curated `generated.x.go` golden set, no `go.work`, the single-batch corpus render.
- Escaper API (root pkg `gsx`, `escape.go`): `writeHTML(w io.Writer, s string) error`; `urlSanitize(s string) string` (scheme allow-list → `about:invalid#gsx`, else returns `s` unchanged — NO normalization); `writeURL(w,s) = writeHTML(urlSanitize(s))`; `cssValueFilter(s string) string` (port of stdlib `css.go`, → `ZgotmplZ`); exported alias `FilterCSS(s) == cssValueFilter(s)`.
- Parity policy: **byte-parity** vs `html/template` for HTML and CSS contexts; **safety-decision parity** for URL (gsx intentionally scheme-filters without normalizing, so byte-parity is inapplicable — assert gsx blocks ⟺ stdlib neutralizes). Every divergence is either fixed or recorded in a commented allow-list with a justification — no silent skips.
- Run all commands from the worktree root `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/testing-foundation`.

---

## File Structure

- `escape_diff_test.go` — CREATE (root pkg `gsx`): differential oracle test + `FuzzEscaperMatchesStdlib` + `knownDivergences` allow-list + the `html/template` render-extract helpers.
- `Makefile` — CREATE (root): `test`, `cover`, `cover-html`.
- `internal/corpus/README.md` — CREATE: contributor guide (facets, render-safety rule, `-update`, area convention, diagnostics position convention).
- `docs/superpowers/specs/codegen-diagnostic-position-audit.md` — CREATE: the audited list of codegen diagnostics lacking `.gsx` positions (backlog for the next increment).
- `docs/superpowers/specs/2026-06-22-testing-architecture-review.md` — MODIFY: correct the R3 "cheap" framing.

---

## Task 1: Differential escaper oracle vs `html/template` (R1)

**Files:**
- Create: `escape_diff_test.go` (root pkg `gsx`)

**Interfaces:**
- Consumes: `writeHTML`, `urlSanitize`, `cssValueFilter` (escape.go); `html/template`, `bytes`/`strings`.
- Produces: `TestEscaperMatchesStdlib`, `FuzzEscaperMatchesStdlib`, and the oracle helpers `tmplElementText`, `tmplStyleElement`, `tmplBlocksURL` (used only by this test file).

- [ ] **Step 1: Write the oracle helpers + the table-driven differential test**

```go
package gsx

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// --- html/template oracles ----------------------------------------------------

// htmlTextOracle returns what html/template emits for s in HTML *text* context.
func htmlTextOracle(s string) string { return template.HTMLEscapeString(s) }

// cssOracle returns what html/template emits for s inside <style>{{.}}</style>
// (CSS value context) — the layer gsx's cssValueFilter targets.
func cssOracle(s string) string {
	t := template.Must(template.New("c").Parse(`<style>{{.}}</style>`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := b.String()
	out = strings.TrimPrefix(out, "<style>")
	out = strings.TrimSuffix(out, "</style>")
	return out
}

// urlBlockedByStdlib reports whether html/template neutralizes s as a URL (its
// unsafe-scheme sentinel #ZgotmplZ appears in an href). gsx's urlSanitize does
// scheme-filtering WITHOUT normalization, so we compare the SAFETY DECISION, not
// bytes.
func urlBlockedByStdlib(s string) bool {
	t := template.Must(template.New("u").Parse(`<a href="{{.}}">`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	return strings.Contains(b.String(), "#ZgotmplZ")
}

// gsxHTML is writeHTML as a string.
func gsxHTML(s string) string {
	var b strings.Builder
	_ = writeHTML(&b, s)
	return b.String()
}

// --- divergence allow-list ----------------------------------------------------

type diffCtx int

const (
	ctxHTML diffCtx = iota
	ctxCSS
)

type diffKey struct {
	ctx diffCtx
	in  string
}

// knownDivergences records (context,input) pairs where gsx INTENTIONALLY differs
// from html/template, each with a justification. The differential test skips
// ONLY these exact pairs. Add an entry only after confirming the difference is
// deliberate and safe (cite escape.go).
var knownDivergences = map[diffKey]string{
	// (populated during Step 2 triage; URL sentinel is handled by safety-decision
	// parity, not here.)
}

func TestEscaperMatchesStdlib(t *testing.T) {
	inputs := diffCorpus()
	for _, s := range inputs {
		// HTML text context: byte-parity.
		if _, skip := knownDivergences[diffKey{ctxHTML, s}]; !skip {
			if got, want := gsxHTML(s), htmlTextOracle(s); got != want {
				t.Errorf("HTML escape mismatch for %q:\n gsx  = %q\n std  = %q\n(if intentional, add to knownDivergences with a reason)", s, got, want)
			}
		}
		// CSS value context: byte-parity.
		if _, skip := knownDivergences[diffKey{ctxCSS, s}]; !skip {
			if got, want := cssValueFilter(s), cssOracle(s); got != want {
				t.Errorf("CSS filter mismatch for %q:\n gsx  = %q\n std  = %q\n(if intentional, add to knownDivergences with a reason)", s, got, want)
			}
		}
		// URL context: SAFETY-DECISION parity (gsx blocks <=> stdlib neutralizes).
		gsxBlocked := urlSanitize(s) == "about:invalid#gsx"
		if gsxBlocked != urlBlockedByStdlib(s) {
			t.Errorf("URL safety-decision mismatch for %q: gsx blocked=%v, stdlib blocked=%v", s, gsxBlocked, urlBlockedByStdlib(s))
		}
	}
}

// diffCorpus is the shared differential input set: the existing CSS fuzz seeds,
// known XSS vectors, and boundary bytes.
func diffCorpus() []string {
	return []string{
		// benign
		"", "foo", "hello world", "a&b", "10px", "#fff", "color: red",
		"/path/to/x", "https://example.com/a?b=c#d", "mailto:a@b.com", "tel:+1",
		// HTML-significant
		`a<b>c`, `"quoted"`, "'apos'", "x&y", "<script>alert(1)</script>",
		// URL schemes (safety decision)
		"javascript:alert(1)", "JavaScript:alert(1)", "vbscript:x", "data:text/html,x",
		"http://ok", "  javascript:x", "/rel?a=b",
		// CSS (reuse FuzzCSSValueFilter seeds)
		"rgb(1,2,3)", "<!--", "-->", "</style", "expression(alert(1))", "EXPRESSION",
		"-moz-binding", `\3c script`, `-expre\69on`, "url(javascript:alert(1))",
		"a;b}c{d", "--x: ;", "1.25in", "`backtick`", "\x00",
	}
}
```

- [ ] **Step 2: Run it — expect failures (this is the point); triage each**

Run: `go test . -run TestEscaperMatchesStdlib -v`
Expected: likely FAILS, surfacing real divergences. For EACH failure decide:
- **Real bug** → fix in `escape.go` (call it out in the report), re-run.
- **Intentional difference** → add a `knownDivergences[diffKey{ctx, input}] = "reason — see escape.go:NN"` entry (HTML/CSS only; URL is handled by safety-decision parity). Every entry MUST have a justification citing why gsx is safe to differ.
Iterate until green. Record the final divergence list (and any bug fixed) in the report — this triage IS the deliverable's value.

- [ ] **Step 3: Add the fuzzer**

Append to `escape_diff_test.go`:
```go
func FuzzEscaperMatchesStdlib(f *testing.F) {
	for _, s := range diffCorpus() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if _, skip := knownDivergences[diffKey{ctxHTML, s}]; !skip {
			if got, want := gsxHTML(s), htmlTextOracle(s); got != want {
				t.Fatalf("HTML divergence for %q: gsx=%q std=%q", s, got, want)
			}
		}
		if _, skip := knownDivergences[diffKey{ctxCSS, s}]; !skip {
			if got, want := cssValueFilter(s), cssOracle(s); got != want {
				t.Fatalf("CSS divergence for %q: gsx=%q std=%q", s, got, want)
			}
		}
		if (urlSanitize(s) == "about:invalid#gsx") != urlBlockedByStdlib(s) {
			t.Fatalf("URL safety-decision divergence for %q", s)
		}
	})
}
```

- [ ] **Step 4: Run unit test + a short fuzz, verify green**

Run: `go test . -run TestEscaperMatchesStdlib -v` → PASS.
Run: `go test . -run FuzzEscaperMatchesStdlib -fuzz=FuzzEscaperMatchesStdlib -fuzztime=30s` → no NEW divergence (or, if found, triage as Step 2). Then `go test ./... -count=1` → green; `go vet ./...` → clean.

- [ ] **Step 5: Commit**

```bash
git add escape_diff_test.go escape.go   # escape.go only if a real bug was fixed
git commit -m "test: differential escaper oracle vs html/template (byte-parity HTML/CSS, safety-decision URL) + fuzzer"
```

---

## Task 2: Honest coverage Makefile (R2)

**Files:**
- Create: `Makefile` (root)

**Interfaces:**
- Produces: `make test`, `make cover`, `make cover-html`.

- [ ] **Step 1: Record the baseline (misleading) number**

Run: `go test ./internal/corpus/ -cover 2>&1 | tail -2` and `go test ./internal/codegen/ -cover 2>&1 | tail -2`. Note the `internal/codegen` % (the spec cites ~59.9%) — this is the "before".

- [ ] **Step 2: Create `Makefile`**

```makefile
# gsx developer tasks. Use tabs for recipe indentation.
.PHONY: test cover cover-html

test:
	go test ./... -count=1

# Honest cross-package coverage: -coverpkg attributes the corpus's in-process
# codegen execution (run via internal/corpus) to internal/codegen, which a plain
# per-package -cover does not. Prints the total at the end.
cover:
	go test -coverpkg=./... -coverprofile=cover.out ./... -count=1
	go tool cover -func=cover.out | tail -1

cover-html: cover
	go tool cover -html=cover.out
```

- [ ] **Step 3: Run `make cover`, verify the codegen number is now honest**

Run: `make cover`
Expected: green, prints a total coverage line. Confirm via `go tool cover -func=cover.out | grep internal/codegen/emit.go | head` that `emit.go` functions exercised by the corpus now show >0% (vs the 0% the per-package run reported). Record before→after in the report.

- [ ] **Step 4: Add `cover.out` to `.gitignore` (if not already ignored)**

Run: `grep -q '^cover.out$' .gitignore || echo 'cover.out' >> .gitignore`

- [ ] **Step 5: Commit**

```bash
git add Makefile .gitignore
git commit -m "build: Makefile with honest -coverpkg coverage target"
```

---

## Task 3: Codegen-diagnostic-position audit + review-doc correction (R3)

**Files:**
- Create: `docs/superpowers/specs/codegen-diagnostic-position-audit.md`
- Modify: `docs/superpowers/specs/2026-06-22-testing-architecture-review.md`

**Interfaces:** none (docs).

- [ ] **Step 1: Enumerate codegen diagnostic sites + classify by position**

Run:
```bash
grep -rn 'fmt.Errorf("codegen:\|fmt.Errorf("%s: codegen:\|: codegen:' internal/codegen/*.go | grep -v _test
```
For each error-producing site, classify: **has `.gsx` position** (message includes a `line:col` derived from a `token.Pos`/`fset.Position`) vs **no position** (bare `codegen: …` or only a path/import-path). Cross-check against the actual rendered diagnostics in `internal/corpus/testdata/cases/diagnostics/*.txtar` and `.../parser/*.txtar` (parser ones already carry `line:col`).

- [ ] **Step 2: Write the audit doc**

Create `docs/superpowers/specs/codegen-diagnostic-position-audit.md` with:
- A one-paragraph statement of the gap (parser diagnostics carry `line:col`; codegen diagnostics largely don't).
- A table: each codegen diagnostic site `file:line` → current message shape → has-position? (yes/no) → the AST node whose position SHOULD be threaded.
- A short "next increment" note: thread positions into the `no` rows, regenerate the affected `diagnostics.golden` files + codegen unit-test substrings, and build the `//~` annotation harness as the consumer (pitfalls per the design's §7).

(This is a documented audit — no test enforcement this increment, per spec.)

- [ ] **Step 3: Correct the review doc's R3 framing**

In `docs/superpowers/specs/2026-06-22-testing-architecture-review.md`, find the R3 bullet that calls it "cheap … the harness already has positions in hand" and append a correction note, e.g.:
> **Correction (2026-06-22):** "cheap" holds for *parser* diagnostics (already pin `line:col`); *codegen* diagnostics carry no `.gsx` position, so threading them is a real codegen change — split into its own increment. See `codegen-diagnostic-position-audit.md`.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/codegen-diagnostic-position-audit.md docs/superpowers/specs/2026-06-22-testing-architecture-review.md
git commit -m "docs: codegen-diagnostic-position audit + correct review R3 framing"
```

---

## Task 4: Corpus contributor README (R10)

**Files:**
- Create: `internal/corpus/README.md`

**Interfaces:** none (docs). Consumes facts from `internal/corpus/loader.go`, `corpus_test.go`, `coverage.golden`.

- [ ] **Step 1: Verify the facts before documenting**

Read `internal/corpus/loader.go` (facet detection: `diagnostics.golden`/`render.golden`/`generated.x.go.golden`/`ast.golden`, `renderable()`, the parser-layer `ast.golden`⇒skip-codegen rule) and `corpus_test.go` (the `-update` flag, the renderable-needs-`render.golden` safety rule, `coverage.golden` generation). Note exact section markers used in a real case (e.g. open `testdata/cases/interpolation/field_access.txtar`).

- [ ] **Step 2: Write `internal/corpus/README.md`**

Cover, accurately to the verified code:
- **What the corpus is** — the txtar fixture spine under `testdata/cases/<area>/<scenario>.txtar`; one batched `go/packages` load + one `go run` to render all cases; structural (whitespace/attr-order-insensitive) HTML comparison.
- **Facet table** — `input.gsx` (+ sibling `.go`), `invoke`, `diagnostics.golden` (always checked), `render.golden` (presence-based), `generated.x.go.golden` (curated subset), `ast.golden` (parser-layer; pinning it ⇒ codegen is skipped for that case).
- **Render-safety rule** — a renderable case (has `invoke`, empty diagnostics) MUST have `render.golden`.
- **`-update` workflow** — `go test ./internal/corpus -run TestCorpus -update`; how to read `coverage.golden`.
- **Adding a case** — a worked example (copy a small existing `.txtar`, edit, `-update`, verify).
- **Diagnostics position convention** — pinned in `diagnostics.golden`, `[path:]line:col: message`; parser carries positions, codegen is the known gap (link the audit).
- **Coverage** — `make cover` to see honest coverage.

- [ ] **Step 3: Sanity-check the README against reality**

Follow your own "adding a case" steps on a throwaway case in a temp copy (or dry-run mentally against an existing case) to confirm the instructions are correct. Confirm `go test ./internal/corpus -run TestCorpus` still passes (no accidental case added).

- [ ] **Step 4: Commit**

```bash
git add internal/corpus/README.md
git commit -m "docs: internal/corpus/README — contributor guide for the test corpus"
```

---

## Task 5: Final verification

- [ ] **Step 1:** `go test ./... -count=1` → all green.
- [ ] **Step 2:** `go vet ./...` → clean. `make cover` → runs, prints honest total.
- [ ] **Step 3:** Confirm deliverables exist: `escape_diff_test.go`, `Makefile`, `internal/corpus/README.md`, `codegen-diagnostic-position-audit.md`; review doc R3 note present.
- [ ] **Step 4:** Summarize in the final commit/report: the escaper divergences found (fixed vs allow-listed), and the coverage before→after number.

---

## Notes / Deferred (from spec §7)
Codegen position-threading + `//~` harness, R5 security matrix, R6 pipeline/codegen fuzzing, R4 doc-level differential render, R7 round-trip, R8 CI matrix, R2b coverage floor — all deferred with tradeoffs/pitfalls documented in the design spec §7. The codegen-diagnostic-position-audit.md (Task 3) is the concrete backlog for the next increment.
