# Unified Go-expression Scanner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route every interpolation-family boundary through one tag/backtick-aware `go/scanner`-based expression scanner, then make `<tag>`/`<>` literals and interpolating backtick literals (`` `…@{expr}…` ``) usable as Go-expression values in every position — including inside `{ }`.

**Architecture:** gsx scans interpolation interiors with a tag-blind byte scanner (`goDepth1End`) distinct from the tag-aware top-level scanner (`scanGoElementMarks`). Unify: promote the top-level scanner into a full expression scanner that also reports the depth-0 `}` boundary, `|>` positions, and `,`/`:`, skipping tag spans and backtick literals. Reroute the byte scanners onto it (proven byte-identical first). Then lower embedded tags at the emit sites and promote the existing `EmbeddedInterp` (`@{}` backtick literal) to a first-class value.

**Tech Stack:** Go 1.26.1, `go/scanner`, txtar corpus.

## Global Constraints

- **Byte-identical delimiting for tag-and-backtick-free fragments.** Every existing `{ }` / `{{ }}` / `name={…}` / value-form / pipe must delimit and split exactly as today. This is proven by a differential harness before anything builds on the new scanner. Safety: only an operand-position `<` is a tag (`a < b`, `x <= y`, `<-ch` untouched); backtick handling reuses the existing `embeddedLiteralEnd` escape convention.
- **Performance:** `goDepth1End` is the hottest parse-time path. The unified scanner runs behind a cheap `strings.IndexByte(src, '<') < 0 && strings.IndexByte(src, '`') < 0` fast-path that keeps the current byte path for fragments with no tag and no backtick (the overwhelming majority) — same speed.
- **emit ≡ probe:** every embedded tag emitted as `gsx.Func(...)` must have a matching scope-capturing IIFE in the type-check probe. Interps inside an embedded tag resolve against the enclosing scope.
- **`|>` detection:** adjacent `token.OR`@p + `token.GTR`@p+1 (never valid Go). `GEQ` is not a pipe.
- **Backtick value model:** an interpolating backtick literal evaluates to a `string` (literal text + `@{expr}` holes via `embeddedValueExpr`); escaping stays contextual at the use site.
- **One accepted compatibility change:** a plain Go raw string containing `@{` now interpolates; `\@{` escapes to a literal `@{`. Documented; corpus-pinned.
- **No "simple heuristics" in core scanning.** The operand/operator decision comes from `go/scanner` token stream, not byte guessing.
- **Every syntax/codegen change ships a corpus case**, goldens via `go test ./internal/corpus -run TestCorpus -update` then verified without `-update`. Don't hand-edit goldens.

---

## File Structure

- `parser/goexpr.go` — promote `scanGoElementMarks` into the unified expression scanner (report boundary/`|>`/marks/backtick-spans/`,`/`:`); backtick-span recognition; fast-path guard.
- `parser/goexpr_scan_test.go` (new) — the differential harness (unified vs legacy byte scanners over the corpus) + unit tests.
- `parser/boundary.go`, `parser/pipe.go` — `goDepth1End`/`goStagesEnd`/`splitPipe`/`composedDelims`/`parenEnd` delegate to the unified scanner.
- `internal/codegen/emit.go` — a shared `lowerEmbeddedTags` over a Go-fragment string; wire into `genInterp`, `genGoBlock`, `ExprAttr` emit, value-form arms; allow/emit `EmbeddedInterp` as an expression value.
- `internal/codegen/analyze.go` — mirror the lowering in the interp/GoBlock/attr/value-form probes.
- `parser/markup.go`, `parser/attrs.go` — accept an `EmbeddedInterp` backtick literal in Go-expression position (reuse `parseEmbeddedSegments`).
- `internal/corpus/testdata/cases/goexpr-*/**` — corpus cases.
- `docs/guide/syntax/*.md`, `docs/ROADMAP.md` — docs.

---

### Task 1: Risk-gate spike — unified expression scanner + differential harness

**De-risk before building.** Implement the unified scanner as NEW code and prove it delimits/splits every existing interpolation byte-identically. Do NOT reroute production yet.

**Files:**
- Modify: `parser/goexpr.go` (add the unified scanner alongside `scanGoElementMarks`)
- Create: `parser/goexpr_scan_test.go`

**Interfaces:**
- Consumes: existing `byteBeginsTag`, `elementSpanEnd`, `tokenExpectsOperandAfter`, `goElemMark`, and `embeddedLiteralEnd`/`embeddedBacktickEscaped` (from `boundary.go`/`attrs.go`) for backtick spans.
- Produces: a `scanGoExpr(src string, from int) goExprScan` reporting, from depth 1 (as `goDepth1End` does):
  ```go
  type goExprScan struct {
      Close     int    // offset of the depth-0 '}' closing the expr; -1 if none
      Pipes     []int  // offsets of depth-0 '|' that begin a '|>' pipe operator
      Commas    []int  // offsets of depth-0 ',' (ordered-attrs)
      Colons    []int  // offsets of depth-0 ':' (ordered-attrs)
      Marks     []goElemMark    // operand-position tag/fragment starts
      Backticks [][2]int        // [start,end) spans of backtick literals (bare/js/css)
  }
  ```
  Consumed by Task 2 (reroute), Task 3–4 (Marks), Task 5 (Backticks).

- [ ] **Step 1: Write the differential harness (failing — scanner absent)**

In `parser/goexpr_scan_test.go`: a test that, for a table of representative interpolation bodies (plain expr, composite literal with `{}`, string with `}`/`|>`/apostrophe, `a < b`, `x <-ch`, `js`…@{}…`, `css`, bare backtick, nested `{ }`, pipeline `x |> f |> g`), asserts:
- `scanGoExpr(src, from).Close` equals `goDepth1End(src, from)`'s result,
- the `Pipes` split points reproduce `splitPipe`'s segmentation,
- `Commas`/`Colons` reproduce `composedDelims`.

```go
func TestScanGoExprMatchesLegacy(t *testing.T) {
	cases := []string{
		`x }`, `f(a, b) }`, `Foo{A: 1, B: 2} }`, `"has } and |> inside" }`,
		"`raw @{x}` }", "js`a\\`b @{y}` }", `a < b }`, `<-ch }`,
		`items |> render |> join(",") }`, `m{"k": v} }`, `wrap(inner) }`,
	}
	for _, c := range cases {
		got := scanGoExpr(c, 0)
		wantClose, _ := goDepth1End(c, 0)
		if got.Close != wantClose {
			t.Errorf("Close(%q) = %d, want %d", c, got.Close, wantClose)
		}
		// pipes: reconstruct segments from got.Pipes over c[:got.Close] and
		// compare to splitPipe(c[:got.Close]); commas/colons vs composedDelims.
		// (spell out the reconstruction against the real helpers)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./parser -run TestScanGoExprMatchesLegacy`
Expected: FAIL — `scanGoExpr` undefined.

- [ ] **Step 3: Implement `scanGoExpr`**

Build on `scanGoElementMarks`'s resume-past-span loop. Track paren/bracket/brace depth from the `go/scanner` token stream (`token.LPAREN`/`RPAREN`/`LBRACK`/`RBRACK`/`LBRACE`/`RBRACE`); record the depth-0 `}`; record adjacent `OR`+`GTR` at depth 0 as a pipe; record depth-0 `COMMA`/`COLON`. On an operand-position `token.LSS` with `byteBeginsTag`, record a mark and resume past `elementSpanEnd` (as today). On a backtick-literal start (bare `` ` `` at operand position, or `js`/`css` prefix — reuse `skipGSXEmbeddedLiteral`'s recognition), record a `[start,end)` span via `embeddedLiteralEnd` and resume past it. Everything skipped as a span (tag or backtick) does NOT contribute to depth or delimiters — its interior is opaque.

- [ ] **Step 4: Run harness to verify byte-identical**

Run: `go test ./parser -run TestScanGoExprMatchesLegacy`
Expected: PASS for every case.

- [ ] **Step 5: Corpus-wide differential test**

Add a test that walks every `.txtar` `input.gsx` under `internal/corpus/testdata/cases`, parses each interpolation source region, and asserts `scanGoExpr` agrees with `goDepth1End`/`splitPipe`/`composedDelims` on all of them. (Extract interp regions via the existing parser hooks, or scan for `{`/`` ` `` and compare on each.) This is the real proof that rerouting will be byte-identical.

Run: `go test ./parser -run TestScanGoExprCorpusDifferential`
Expected: PASS. Any disagreement is a bug in `scanGoExpr` to fix now.

- [ ] **Step 6: Escaped-backtick correctness test**

```go
func TestScanGoExprBacktickSpan(t *testing.T) {
	// go/scanner ends a raw string at the escaped backtick; scanGoExpr must
	// treat the WHOLE gsx literal (through the gsx-escaped `) as one span.
	got := scanGoExpr("`a\\`b @{x}` }", 0)
	if len(got.Backticks) != 1 || got.Backticks[0] != [2]int{0, 10} {
		t.Fatalf("backtick span = %v, want one [0,10)", got.Backticks)
	}
	if wantClose, _ := 11, true; got.Close != wantClose { // the '}' after the literal
		t.Fatalf("Close = %d, want %d", got.Close, wantClose)
	}
}
```
(Compute the exact offsets from the test string before pinning.)

Run: `go test ./parser -run TestScanGoExprBacktickSpan`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add parser/goexpr.go parser/goexpr_scan_test.go
git commit -m "feat(parser): unified tag/backtick-aware go-expression scanner (spike + differential harness)"
```

**Gate:** do not proceed to Task 2 until Steps 4–6 are green.

---

### Task 2: Reroute the boundary finders onto the unified scanner

**Files:**
- Modify: `parser/boundary.go` (`goDepth1End`, `composedDelims`), `parser/pipe.go` (`splitPipe`, `parenEnd`), `parser/goexpr.go` (`goStagesEnd` path if separate)
- Test: existing `parser` suite + full corpus (regression only — no new syntax rendered yet)

**Interfaces:**
- Consumes: Task 1's `scanGoExpr`.
- Produces: production delimiting flows through the unified scanner; tag/backtick spans are correctly skipped when finding `}`/`|>`/`,`/`:`. Observable parse behavior for existing inputs is unchanged.

- [ ] **Step 1: Add the fast-path guard + delegate `goDepth1End`**

`goDepth1End` first checks `if strings.IndexByte(src[from:], '<') < 0 && strings.IndexByte(src[from:], '`') < 0 { <existing byte loop> }`; otherwise returns `scanGoExpr(src, from).Close` (with the same `(int, bool)` shape). Do the analogous delegation for `splitPipe` (use `Pipes`), `composedDelims` (use `Commas`/`Colons`), `parenEnd`/`goStagesEnd`.

- [ ] **Step 2: Run the parser suite**

Run: `go test ./parser -count=1`
Expected: PASS (byte-identical behavior).

- [ ] **Step 3: Run the full corpus (regression)**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS with no golden diff — nothing new is accepted-and-rendered yet; this proves the reroute changed no existing output.

- [ ] **Step 4: Run `make check`**

Run: `make check`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add parser/boundary.go parser/pipe.go parser/goexpr.go
git commit -m "refactor(parser): route interpolation delimiting through the unified scanner"
```

---

### Task 3: Lower embedded `<tag>`/`<>` inside interpolations (`genInterp` + probe)

**Files:**
- Modify: `internal/codegen/emit.go` (`genInterp` ~line 1647; add `lowerEmbeddedTags` helper), `internal/codegen/analyze.go` (interp probe)
- Create: `internal/corpus/testdata/cases/goexpr-interp-tag/{call-arg,conditional,scope-local}.txtar`

**Interfaces:**
- Consumes: `splitGoElements`, the element/fragment lowering (`emitElementValue`/`emitFragmentValue`), the scope-capturing IIFE probe (from #42/#44).
- Produces: a `<tag>`/`<>` inside a `{ }` expression lowers to its inline `gsx.Func(...)` value; the probe type-checks it in enclosing scope.

- [ ] **Step 1: Failing corpus case**

Create `goexpr-interp-tag/call-arg.txtar`:
```
-- input.gsx --
package views

func wrap(n gsx.Node) gsx.Node { return n }

component Host() {
	<div>{ wrap(<><b>hi</b></>) }</div>
}
-- invoke --
Host(HostProps{})
-- render.golden --
<div><b>hi</b></div>
```
(Leading `component`/import per the GoWithElements import-hoisting rule if the file needs `gsx.Node` in a user signature — mirror `element-literals/return.txtar`.)

- [ ] **Step 2: Verify it fails**

Run: `go test ./internal/corpus -run TestCorpus/goexpr-interp-tag -update` then without `-update`.
Expected: the case fails to generate/render (genInterp emits `wrap(<><b>hi</b></>)` verbatim → broken Go, or a diagnostic).

- [ ] **Step 3: Implement `lowerEmbeddedTags` + wire `genInterp`**

Extract a helper that takes a Go-expression string + its base `token.Pos`, runs `splitGoElements`, and returns the source with each embedded element/fragment replaced by its lowered `gsx.Func(...)` (reusing `emitElementValue`/`emitFragmentValue`). `genInterp` runs the seed (and each stage expr) through it before emitting. Fast-path: if the expr has no `<`, emit as today.

- [ ] **Step 4: Mirror in the interp probe (`analyze.go`)**

The interp type-check probe runs the same split and emits the scope-capturing IIFE for each embedded tag, so `{ wrap(<b>{ localVar }</b>) }` resolves `localVar` in enclosing scope.

- [ ] **Step 5: Add scope + conditional cases**

`goexpr-interp-tag/scope-local.txtar` — the embedded tag interpolates a component param/local (proves emit≡probe scope). `conditional.txtar` — `{ cond ? … }` style via a helper `pick(c bool, a, b gsx.Node) gsx.Node`.

- [ ] **Step 6: Generate + verify**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: cases render as pinned; `coverage.golden` updated; second run clean. Confirm `scope-local` has empty `diagnostics.golden`.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/emit.go internal/codegen/analyze.go internal/corpus/testdata/cases/goexpr-interp-tag/ internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): lower embedded <tag>/<> inside interpolations"
```

---

### Task 4: Extend tag lowering to GoBlock / attr-value / value-form emit sites

Closes the parses-but-breaks gap: after Task 2 these positions parse a tag; now they lower it.

**Files:**
- Modify: `internal/codegen/emit.go` (`genGoBlock`, the `ExprAttr` emit path, value-form arm emit), `internal/codegen/analyze.go` (matching probes)
- Create: `internal/corpus/testdata/cases/goexpr-{goblock,attr,valueform}-tag/*.txtar`

**Interfaces:**
- Consumes: Task 3's `lowerEmbeddedTags` + probe helper.
- Produces: a `<tag>`/`<>` lowers correctly in `{{ … }}`, `name={…}`, and `{ if/switch … }` arms.

- [ ] **Step 1: Failing cases** — one per site: `{{ n := <div/> }}` used later; `name={ <span/> }` where the attr type is `gsx.Node`; a value-form arm returning a tag. Pin `render.golden`.
- [ ] **Step 2: Verify fail** — `go test ./internal/corpus/... -update` then without: broken-Go/diagnostic.
- [ ] **Step 3: Wire each emit site** through `lowerEmbeddedTags`; mirror each probe.
- [ ] **Step 4: Generate + verify** all three render; scope preserved; diagnostics empty.
- [ ] **Step 5: Commit**

```bash
git commit -am "feat(codegen): lower embedded tags in GoBlock/attr/value-form positions"
```

---

### Task 5: Interpolating backtick literals as first-class expression values

**Files:**
- Modify: `parser/markup.go`/`parser/attrs.go` (accept an `EmbeddedInterp` in Go-expression position via the unified scanner's backtick-span recognition; reuse `parseEmbeddedSegments`), `internal/codegen/emit.go` (emit `EmbeddedInterp` as a value via `embeddedValueExpr`), `internal/codegen/analyze.go` (probe)
- Create: `internal/corpus/testdata/cases/goexpr-backtick/{var,call-arg,in-interp,compat-raw-string,static-text-escape}.txtar`

**Interfaces:**
- Consumes: `embeddedValueExpr` (`emit.go:2620`), `parseEmbeddedSegments` (`attrs.go:442`), `EmbeddedInterp`, the unified scanner's `Backticks`.
- Produces: `` `hello @{name}` `` is a `string`-typed Go-expression value in any position; escaping stays contextual at the use site.

- [ ] **Step 1: Failing cases**

`goexpr-backtick/var.txtar`:
```
-- input.gsx --
package views

component Host(name string) {
	<div>{ `hello @{name}` }</div>
}
-- invoke --
Host(HostProps{Name: "world"})
-- render.golden --
<div>hello world</div>
```
Plus `compat-raw-string.txtar` pinning that `` `a@{x}b` `` interpolates and `` `a\@{x}b` `` yields literal `a@{x}b`, and `static-text-escape.txtar` pinning how literal `<` in a backtick literal renders in HTML-text context (resolves the spec's open point — pin the actual generated behavior).

- [ ] **Step 2: Verify fail** — `-update` then without.

- [ ] **Step 3: Parser — accept `EmbeddedInterp` in expression position**

When the unified scanner reports a `Backticks` span at an operand position inside a Go fragment, parse it via `parseEmbeddedSegments` into an `EmbeddedInterp` and admit it (the lowering, like tags, happens at emit time over the fragment). Reuse `tryParseBodyEmbeddedInterp`'s segment logic; do not duplicate.

- [ ] **Step 4: Codegen — emit `EmbeddedInterp` as a value**

In the emit-time fragment lowering, replace a backtick-literal span with `embeddedValueExpr`'s concatenation (a `string`), plus optional `|> stages`. Mirror in the probe. Escaping is applied by the surrounding position (unchanged).

- [ ] **Step 5: Generate + verify** all cases; resolve `static-text-escape` by pinning the generated truth; confirm `compat-raw-string` shows the documented behavior.

- [ ] **Step 6: Commit**

```bash
git commit -am "feat: interpolating backtick literals as first-class expression values"
```

---

### Task 6: Corpus completeness + docs

**Files:**
- Create: remaining position×feature corpus gaps + a JS/CSS-context type-error case (`goexpr-js-context-error.txtar`, expecting a diagnostic).
- Modify: `docs/guide/syntax/*.md`, `docs/ROADMAP.md`.

- [ ] **Step 1: Fill matrix gaps** — ensure each of {interp, GoBlock, attr, value-form, pipe-seed} × {tag literal, backtick literal} has at least one case; add the JS/CSS type-error case (a `gsx.Node` in a `<script>{…}` → diagnostic, pinned in `diagnostics.golden`).
- [ ] **Step 2: Regenerate + verify** — `-update` then without; `coverage.golden` consistent.
- [ ] **Step 3: Docs** — extend the "as values" guide: `<tag>`/`<>` now work inside `{ }` and all Go-fragment positions; interpolating backtick literals `` `…@{expr}…` `` are string values everywhere; document the `@{`-in-raw-string compatibility change; remove the "not supported inside interpolation" limitation. Back every fenced example with a corpus case verbatim. Wrap literal `{{ }}` in `::: v-pre`.
- [ ] **Step 4: Sibling grammars (read + report only)** — check tag/backtick-in-interp highlighting for tree-sitter-gsx/vscode-gsx/CodeMirror; report findings; commit only this-repo docs.
- [ ] **Step 5: `make ci`** — full authoritative run green.
- [ ] **Step 6: Commit**

```bash
git commit -am "docs+test: corpus matrix + guide for embedded tags & backtick literals as values"
```

---

## Self-Review

- **Spec coverage:** unified scanner (T1) + reroute (T2) → Feature-1 tag lowering across interp (T3) and GoBlock/attr/value-form (T4) → Feature-2 backtick values (T5) → matrix+docs+compat (T6). ✓
- **Placeholder scan:** the spike task (T1) specifies the target interface, concrete differential/escaped-backtick tests, and the acceptance gate rather than a full scanner listing — appropriate for a risk-gate spike; the mechanical tasks carry concrete edits/cases. No TBD. ✓
- **Type consistency:** `scanGoExpr(src, from) goExprScan` feeds `goDepth1End`/`splitPipe`/`composedDelims` (T2), `Marks`→`lowerEmbeddedTags` (T3/T4), `Backticks`→`EmbeddedInterp` value emit (T5). `embeddedValueExpr`/`parseEmbeddedSegments`/`genInterp` signatures match the code read. ✓
- **Risk front-loaded:** T1 gates on a corpus-wide byte-identical proof before any reroute; the fast-path guard bounds the perf cost of the hottest parse path. ✓
- **emit≡probe:** T3/T4/T5 each change emit and probe together and gate on rendering corpus cases; T3/T4 include scope-capture cases. ✓
