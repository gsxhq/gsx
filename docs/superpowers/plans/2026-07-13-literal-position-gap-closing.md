# Literal Position-Gap Closing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close three literal-position gaps left after PR #106: (W2) js``/css`` error-carrying holes hoist wherever f`` holes can (inside the render closure), (W3) nested prefixed literals inside `@{ }` holes work in every hole-bearing context (they already work in body position), and (W1′) a `|>` after a Go-expression-position literal reports a positioned "wrap it in a function call" diagnostic instead of `expected operand, found '>'`.

**Architecture:** All three ride existing machinery. W2 flips the js/css `exprPos` flag in `emitGoExprEmbeddedInterp` from hardcoded `true` to `!canHoist`, activating the already-shipped fold-path lowering (per-hole temps + `return _gsxerr` hoists) at the two in-closure sites. W3 has two halves: the Go-expression split's literal-end scan becomes hole-aware (the attribute parser's hole scanner already is), and `holeStringExpr`/`embeddedHoleExpr` learn to assemble their seed from `Interp.Embedded` (mirroring `genInterp`'s existing loop) instead of splicing raw `Expr`. W1′ is a peek in `scanGoParts` after a literal item.

**Tech Stack:** Go 1.26.1, `go/types`-probed codegen, txtar corpus (`internal/corpus`), fmt corpus (`internal/gsxfmt`).

**Spec:** `docs/superpowers/specs/2026-07-13-literal-position-gap-closing-design.md` — read it before starting any task.

## Global Constraints

- Go pinned to **1.26.1** (`GO_VERSION` in `.github/workflows/ci.yml`).
- Runtime (root `gsx` package) is standard-library only and **needs no changes** in this plan.
- **Never hand-edit** `*.x.go` or `*.golden`. Regenerate: `go test ./internal/corpus -run TestCorpus -update`, then verify **without** `-update`. Fmt corpus: `go test ./internal/gsxfmt -run TestFmtCorpus -update`.
- **Emit ≡ probe:** any expression the emitter writes must be type-identical to what the analyze skeleton probes. When you change an emit lowering, verify (and if needed change) the matching probe site in `internal/codegen/analyze.go` in the same commit.
- Corpus is canonical: every behavior in this plan is pinned by a txtar case; syntax valid in multiple contexts needs a case **per context**.
- Diagnostics carry positions (`bag.Errorf(n.Pos(), n.End(), code, …)`); never a bare `fmt.Errorf` for a user error.
- The `gsx` binary on PATH is Ghostscript. Use `go run ./cmd/gsx …`.
- Inner dev loop: `make check`. Before merge: `make ci` and `make lint`.
- Work in a **git worktree**, branch `literal-position-gaps`, created via the `superpowers:using-git-worktrees` skill from current `main`. All commands below run from the worktree root. **Subagents: `cd` into the worktree and verify `git branch --show-current` prints `literal-position-gaps` before ANY commit.**
- Corpus gotcha: adding a case that registers filters renumbers the batch-global `_gsxfN` filter aliases across other goldens — alias-only diffs in unrelated goldens are expected; verify by sed-normalizing before assuming a regression.
- No sibling-repo (grammar) changes: this plan adds no new accepted syntax surface that the grammars tokenize differently (nested literals already lex as Go-blob content in hole positions).

---

### Task 1 (W2): In-closure js``/css`` error-carrying holes hoist

Wherever an f`` hole can hoist (the `canHoist=true` sites: an `{ }` interp's `Interp.Embedded` split and the braced component-prop binding), js``/css`` holes gain error-pipe / `(T, error)` seed / error-renderer support via the existing fold-path lowering. Top-level and GoBlock keep the rejection. ctx gating (`rejectCtx = !hasCtx`) is untouched.

**Files:**
- Modify: `internal/codegen/emit.go` — `emitGoExprEmbeddedInterp` (~line 3378): js/css branches pass `exprPos := !canHoist` instead of literal `true`; update the function's doc comment (it currently says "js`/css` never hoist").
- Modify: `internal/codegen/emit.go` — `embeddedHoleExpr` doc comment (~3696): the exprPos story now says "set where no hoist channel exists", not "js/css always".
- Test: `internal/corpus/testdata/cases/goexpr-js-literal/err_pipe_in_closure.txtar` (new), `internal/corpus/testdata/cases/goexpr-css-literal/err_tuple_in_closure.txtar` (new), `internal/corpus/testdata/cases/goexpr-js-literal/err_hole_braced_prop.txtar` (new), `internal/corpus/testdata/cases/goexpr-js-literal/err_pipe_in_closure_halt.txtar` (new).

**Interfaces:**
- Consumes: `emitGoExprEmbeddedInterp(hoistBuf, valBuf, p, resolved, table, imports, rt, interpTemp, bag, hasCtx, canHoist)` and `embeddedJSValueExpr/embeddedCSSValueExpr(…, exprPos, rejectCtx)` — all exist.
- Produces: no signature changes. Behavior only: at `canHoist` sites the js/css assemblers run with `exprPos=false` (per-hole `_gsxvN :=` temps into `hoistBuf`, error shapes hoist via `return _gsxerr`).

- [ ] **Step 1: Write the failing corpus case**

`internal/corpus/testdata/cases/goexpr-js-literal/err_pipe_in_closure.txtar`:

```
# W2: an error-returning pipe stage in a js`` hole INSIDE a component's { }
# interp — the render closure has a hoist channel, so the hole hoists
# `_gsxvN, _gsxerr := …` exactly like the identical f`` hole does (parity
# rule: wherever f`` can hoist, js``/css`` can too). Success path.
-- gsx.toml --
filter_packages = ["corpustest/cases/goexpr_js_literal_err_pipe_in_closure/filters"]
-- filters/filters.go --
package filters

import "errors"

// Parse fails on empty input.
func Parse(s string) (string, error) {
	if s == "" {
		return "", errors.New("parse: empty input")
	}
	return s, nil
}
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

func send(r gsx.RawJS) gsx.RawJS { return r }

component Page(csv string) {
	<button onclick={ send(js`load(@{csv |> parse})`) }>go</button>
}
-- invoke --
Page(PageProps{Csv: "a,b"})
-- diagnostics.golden --
-- render.golden --
<button onclick="load(&#39;a,b&#39;)">go</button>
```

(The exact render bytes come from the harness — write the case, run with `-update`, then **read the generated goldens and verify** the hole hoisted `_gsxv0, _gsxerr := _gsxf0.Parse((csv))` into the closure and the render escaped correctly. Do not trust `-update` blindly; the render golden above is indicative, the pinned bytes are whatever the correct lowering produces.)

Check `gsx.toml` conventions against an existing case first (`internal/corpus/testdata/cases/pipeerr/text_mid_stage.txtar` uses `filter_packages = ["./filters"]` — mirror exactly what that case does; the module path form above is only needed if `./filters` doesn't resolve in this harness position).

- [ ] **Step 2: Run to verify current rejection**

Run: `go test ./internal/corpus -run 'TestCorpus/goexpr-js-literal' -v 2>&1 | head -30`
Expected: FAIL — the case currently produces the `goexpr-literal-error` "uses an error-returning filter" diagnostic instead of generating.

- [ ] **Step 3: Flip the flag**

In `internal/codegen/emit.go`, `emitGoExprEmbeddedInterp`:

```go
	exprPos := !canHoist
	switch p.Lang {
	case ast.EmbeddedJS:
		val, ok := embeddedJSValueExpr(hoistBuf, p.Segments, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr", exprPos, !hasCtx)
		…
	case ast.EmbeddedCSS:
		val, ok := embeddedCSSValueExpr(hoistBuf, p.Segments, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr", exprPos, !hasCtx)
		…
```

Update the doc comment: `exprPos` (no-hoist mode) now applies only where `canHoist=false`; at in-closure sites the js/css assemblers use the fold-path lowering (source-ordered per-hole temps into `hoistBuf`, error hoists via `return _gsxerr`), byte-compatible with how an attribute-local literal folds through a bag.

- [ ] **Step 4: Regenerate and audit goldens**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Audit the diff:
- New case generates with the hoist inside the render closure.
- **Existing in-closure js/css cases** (`goexpr-js-literal/ctx_filter_in_closure.txtar`, `call_arg.txtar`, any case with a js/css literal inside `{ }`) may churn from inline-concat to temp-materialized form — that is the expected lowering change. **Render goldens must be byte-identical** for every pre-existing case; if any `render.golden` changes, stop — that is a bug, not churn.
- Top-level and GoBlock rejection cases (`diag_error_pipe.txtar`, `diag_error_tuple.txtar`, `diag_error_renderer.txtar`, `goexpr-f-literal/diag_error_pipe_goblock.txtar`) must be **unchanged**.

- [ ] **Step 5: Add the remaining three cases**

Same pattern as Step 1:
- `goexpr-css-literal/err_tuple_in_closure.txtar` — a `(T, error)` seed in a css`` hole inside `{ }` (e.g. `style` composition via a helper consuming `gsx.RawCSS`): `css`color:@{pick(c)}`` where `func pick(s string) (string, error)`.
- `goexpr-js-literal/err_hole_braced_prop.txtar` — a **braced prop binding** with an error hole: component `Wide(Handler gsx.RawJS)` invoked as `<Wide Handler={ js`go(@{id |> parse})` }/>`; verifies the hoist lands in `componentValueEntry.stmts` and replays before the call (read the generated golden to confirm the `_gsxerr` check precedes the component call, in source order relative to other attr values).
- `goexpr-js-literal/err_pipe_in_closure_halt.txtar` — failure path: the filter errors at render (`Csv: ""`), pinned via the corpus render-error capture facet (copy the facet shape from `pipeerr/halt_on_error.txtar`).
- `goexpr-js-literal/err_hole_differential.txtar` — the spec's differential pin: the SAME error-carrying js`` literal written attribute-local (`onclick=js` form, which already supported error holes) and in-closure expression form (`onclick={ send(js`…`) }` where `send` is identity on `gsx.RawJS`) must render the byte-identical attribute value under a hostile interpolated value (copy the hostile-input pattern from `goexpr-js-literal/differential.txtar`).

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus` → PASS.

- [ ] **Step 6: Verify emit ≡ probe and full check**

The analyze probe already harvests these holes (f`` proves the same site); confirm no analyze change is needed by checking the new cases' `diagnostics.golden` are empty and generation type-checks (the corpus does this). Run: `make check` → PASS.

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "feat(codegen): js/css literal error-carrying holes hoist at in-closure sites (exprPos = !canHoist)"
```

---

### Task 2 (W1′): Positioned diagnostic for `|>` after a Go-expression-position literal

`var x = f`hi` |> upper` (and the js/css/dquote forms, at all three container sites) currently dies with `expected operand, found '>'`. Emit a targeted, positioned message instead. This is **detection only** — no pipe support.

**Files:**
- Modify: `parser/goexpr.go` — `scanGoParts` (~line 322): after consuming a prefixed-literal item (`base = end` for both the backtick and dquote branches), peek past horizontal whitespace for `|>`.
- Modify: wherever `SplitGoExprElements`/`scanGoParts` reports errors — follow the existing error path that produced `error[parse-error]` in the probe (find it: `grep -n "parse-error" parser/*.go` and trace how goexpr-region errors reach the parser's diagnostic list).
- Test: `parser/goexpr_test.go` (unit), `internal/corpus/testdata/cases/goexpr-f-literal/diag_whole_pipe_toplevel.txtar`, `internal/corpus/testdata/cases/goexpr-js-literal/diag_whole_pipe_interp.txtar`, `internal/corpus/testdata/cases/goblock-literal/diag_whole_pipe_goblock.txtar` (all new).

**Interfaces:**
- Consumes: `scanGoParts(src string) []goSplitItem`, `embeddedLiteralEnd` return offset.
- Produces: a positioned error with message exactly: `whole-literal pipelines are not supported in Go-expression position; wrap the literal in a function call instead`. How it surfaces (parse-error list vs split errs) must match how existing goexpr-region scan errors surface — do not invent a new channel.

- [ ] **Step 1: Write the failing parser unit test**

In `parser/goexpr_test.go` add:

```go
func TestScanGoPartsWholeLiteralPipeDiagnostic(t *testing.T) {
	srcs := []string{
		"var x = f`hi` |> upper",
		"var x = js`f()` |> minify",
		"var x = f\"hi\" |> upper",
	}
	for _, src := range srcs {
		_, errs := SplitGoExprElements(token.NewFileSet(), src, token.Pos(1), nil)
		found := false
		for _, e := range errs {
			if strings.Contains(e.Error(), "whole-literal pipelines are not supported in Go-expression position") {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want whole-literal-pipe diagnostic, got %v", src, errs)
		}
	}
}
```

Adapt the assertion to `SplitGoExprElements`'s actual error type (positioned error struct vs plain error — read the signature first; the test must also assert the reported **offset points at the `|>`**).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./parser -run TestScanGoPartsWholeLiteralPipeDiagnostic -v`
Expected: FAIL (today the scan produces generic Go parse errors, not this message).

- [ ] **Step 3: Implement the peek**

In both literal branches of `scanGoParts` (backtick and dquote), after computing `end`:

```go
	// A `|>` chain after a value-position literal is gsx pipe syntax that
	// cannot appear in a Go expression — without this peek it surfaces as an
	// unpositioned "expected operand, found '>'" from the region's Go parse.
	// Detection only: whole-literal pipes in Go-expression position are not
	// supported (use a function call).
	if j := skipHSpace(src, end); strings.HasPrefix(src[j:], "|>") {
		// report positioned error at j with the exact message; see Step 1
	}
```

where `skipHSpace` skips spaces/tabs (add the tiny helper if none exists — check `parser/boundary.go` for an existing one first). Wire the error into the same error list the function/caller already returns. The literal item itself is still consumed normally (the split must remain well-formed so downstream fallbacks behave).

- [ ] **Step 4: Run unit test + corpus diagnostics cases**

Run: `go test ./parser -run TestScanGoPartsWholeLiteralPipeDiagnostic -v` → PASS.

Add the three corpus diagnostic cases (mirror the structure of `goexpr-f-literal/diag_error_pipe_toplevel.txtar` — input.gsx + expected non-empty `diagnostics.golden`, no generated/render goldens):
- top-level: `var greeting = f` + "`" + `hi` + "`" + ` |> upper` (with a `filters` package providing `Upper`)
- inside `{ }`: `<p>{ wrap(f` + "`" + `hi` + "`" + ` |> upper) }</p>`
- inside `{{ }}`: `{{ h := f` + "`" + `hi` + "`" + ` |> upper }}`

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus` → PASS. Verify each `diagnostics.golden` contains the new message with a real position.

- [ ] **Step 5: Full check + commit**

Run: `make check` → PASS.

```bash
git add -A && git commit -m "feat(parser): positioned diagnostic for whole-literal pipe after a Go-expression-position literal"
```

---

### Task 3 (W3a): Hole-aware literal-end scan for Go-expression splits

`embeddedLiteralEnd` (`parser/boundary.go:160`) scans flat to the closing delimiter, so a nested literal's backtick inside an `@{ }` hole terminates the outer literal early — this is why nesting parse-errors in expression position (the attribute parser's own hole scanner is already hole-aware via `scanGoExpr`/`skipGSXEmbeddedLiteral`). Make the Go-expression split's scan hole-aware.

**Files:**
- Modify: `parser/boundary.go` — add `embeddedLiteralEndHoleAware(src string, i int, delim byte) (int, bool)`; switch `skipGSXEmbeddedLiteral` (line 136) to it; extend `skipGSXEmbeddedLiteral` to also recognize the dquote forms (`js"`, `css"`, `f"`) which it currently misses.
- Modify: `parser/goexpr.go` — the two `embeddedLiteralEnd` call sites in `scanGoParts` (~line 95 and ~115) switch to the hole-aware variant.
- Test: `parser/goexpr_scan_test.go` (extend — it already exercises `skipGSXEmbeddedLiteral`).

**Interfaces:**
- Consumes: `scanGoExpr(src string, from int) goExprScan` (`parser/boundary.go:86`) — the existing hole-body scanner that handles strings, comments, brace balance, and (via `skipGSXEmbeddedLiteral`) nested prefixed literals.
- Produces: `embeddedLiteralEndHoleAware(src, i, delim) (end int, ok bool)` — end just past the closing delim, where a `@{ … }` hole's body is skipped with full Go-expression awareness (recursively: a nested literal inside the hole is skipped by `skipGSXEmbeddedLiteral`, which itself now recurses through this function). The flat `embeddedLiteralEnd` stays for any caller that must not change (audit callers: `grep -n "embeddedLiteralEnd(" parser/*.go` — if ALL callers migrate, delete the flat version instead of keeping dead code).

- [ ] **Step 1: Write the failing scanner tests**

In `parser/goexpr_scan_test.go`:

```go
func TestEmbeddedLiteralEndHoleAware(t *testing.T) {
	cases := []struct {
		src  string // scan starts just after the opening delimiter
		want string // the full literal the scan should cover, from src start
	}{
		// nested backtick literal inside a hole must not terminate the outer
		{"f`a @{ string(js`f(@{who})`) }` + x", "f`a @{ string(js`f(@{who})`) }`"},
		// depth 2 with an inner hole
		{"f`a @{ f`b @{who}` } c`", "f`a @{ f`b @{who}` } c`"},
		// plain Go raw string inside a hole
		{"f`a @{ len(`raw`) }`", "f`a @{ len(`raw`) }`"},
		// dquote outer with backtick inner
		{`f"a @{ string(js` + "`f()`" + `) }"`, `f"a @{ string(js` + "`f()`" + `) }"`},
	}
	for _, c := range cases {
		end, ok := skipGSXEmbeddedLiteral(c.src, 0)
		if !ok || c.src[:end] != c.want {
			t.Errorf("skipGSXEmbeddedLiteral(%q) = %q, %v; want %q", c.src, c.src[:min(end, len(c.src))], ok, c.want)
		}
	}
}
```

(Adjust entry point per the real signatures — the essential assertions are the covered spans above.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./parser -run TestEmbeddedLiteralEndHoleAware -v`
Expected: FAIL — the flat scan stops at the inner literal's opening backtick.

- [ ] **Step 3: Implement**

```go
// embeddedLiteralEndHoleAware is embeddedLiteralEnd with @{ } hole awareness:
// a delimiter INSIDE a hole's Go expression (a nested prefixed literal, a raw
// string) must not terminate the outer literal. On `@{` the hole body is
// skipped with scanGoExpr's lexical rules (strings, comments, brace balance,
// nested gsx literals via skipGSXEmbeddedLiteral), making the scan mutually
// recursive with skipGSXEmbeddedLiteral for arbitrary nesting depth.
func embeddedLiteralEndHoleAware(src string, i int, delim byte) (int, bool) {
	for i < len(src) {
		if src[i] == delim && !backtickEscapedIn(src, i) {
			return i + 1, true
		}
		if src[i] == '@' && i+1 < len(src) && src[i+1] == '{' {
			i = scanHoleEnd(src, i+2) // offset just past the hole's closing '}'
			continue
		}
		i++
	}
	return len(src), true
}
```

`scanHoleEnd` is whatever the attribute path already uses to find a hole's closing `}` — locate it (`parseEmbeddedAttrLiteral`'s hole scan in `parser/attrs.go`) and **reuse, don't reimplement**; if it's inline there, extract it to `boundary.go` and use it from both places. Then:
- `skipGSXEmbeddedLiteral`: call the hole-aware variant; add the three dquote-prefix cases (`js"`, `css"`, `f"` with delim `'"'`).
- `scanGoParts`: both call sites switch to the hole-aware variant.

- [ ] **Step 4: Run tests**

Run: `go test ./parser -v 2>&1 | tail -20` → all PASS (including the pre-existing scanner equivalence tests in `goexpr_scan_test.go` — if any equivalence test pins the flat behavior, update its expectation deliberately and note why in the commit message).

Run: `go test ./internal/corpus -run TestCorpus` → PASS (no goldens change — parsing previously-broken input now succeeds, but no corpus case contains it yet).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "fix(parser): Go-expression literal-end scan is @{ }-hole-aware; dquote forms join skipGSXEmbeddedLiteral"
```

---

### Task 4 (W3b): Emit assembles hole seeds from `Interp.Embedded`

Attr-local literal holes already receive the `Embedded` split (`walkMarkupAttrs` yields `EmbeddedAttr.Segments` into `splitInterpEmbedded`'s walk), but `holeStringExpr` and `embeddedHoleExpr` splice the raw `n.Expr` — a nested literal reaches the generated Go verbatim and poisons. Assemble the seed from the split instead, exactly as `genInterp` does for body interps.

**Files:**
- Modify: `internal/codegen/emit.go` — new helper `assembleHoleSeed`; use it at the top of `holeStringExpr` (~3430) and `embeddedHoleExpr` (~3696) in place of `strings.TrimSpace(n.Expr)`.
- Modify: `internal/codegen/analyze.go` — the probe-side hole handling: wherever segment-hole probes emit `_gsxuse(<expr>)` with the raw hole expr, an `Embedded`-carrying hole must splice the probe-IIFE-composed seed instead (this is what `emitProbes`'s `*Interp` case already does for body interps — trace whether attr-segment holes flow through that same case; if they do, **no change**, verify only).
- Test: corpus cases (Step 5 list).

**Interfaces:**
- Consumes: `emitGoExprEmbeddedInterp(hoistBuf, valBuf, p, …, hasCtx, canHoist)` (Task 1's semantics), `ast.GoText`, `ast.Interp.Embedded`.
- Produces:

```go
// assembleHoleSeed returns the Go expression for a hole's seed. A plain hole
// is its Expr verbatim. A hole whose Expr carries embedded prefixed literals
// (Interp.Embedded, seated by splitInterpEmbedded) is reassembled from its
// parts: GoText runs verbatim, each nested literal lowered to its Go value by
// emitGoExprEmbeddedInterp — the same splice genInterp performs for a body
// interp's seed. Capability flags derive from the hole's own rejection flags
// (hasCtx = !rejectCtx, canHoist = !rejectErr) so a nested literal's holes
// obey the same position rules as the hole that contains them. An embedded
// *Element/*Fragment part is a positioned diagnostic — element values are
// gsx.Node closures, which no attribute-literal hole can render.
func assembleHoleSeed(hoistBuf *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, rejectErr, rejectCtx bool) (string, bool)
```

- [ ] **Step 1: Write the failing corpus case**

`internal/corpus/testdata/cases/interpolation/nested_literal_attr_hole.txtar` (context: attr-local f`` hole):

```
# W3: a nested f`` literal inside an attribute-local f`` literal's @{ } hole.
# The hole is a Go-expression position; the inner literal lowers to a plain
# string concat and the outer hole consumes it like any Go expression.
# (Body-position nesting already worked; this pins the attr context.)
-- input.gsx --
package demo

component Page(who string) {
	<p title=f`hi @{ f`name: @{who}` }!`>x</p>
}
-- invoke --
Page(PageProps{Who: "Ada"})
-- diagnostics.golden --
-- render.golden --
<p title="hi name: Ada!">x</p>
```

- [ ] **Step 2: Run to verify current failure**

Run: `go test ./internal/corpus -run 'TestCorpus/interpolation' -v 2>&1 | head -20`
Expected: FAIL — generation poisons (`format generated source: … missing ','`), because the inner literal reaches the generated Go verbatim.

- [ ] **Step 3: Implement `assembleHoleSeed` and wire it in**

Implementation shape (place next to `holeStringExpr`):

```go
func assembleHoleSeed(…) (string, bool) {
	if n.Embedded == nil {
		return strings.TrimSpace(n.Expr), true
	}
	var eb bytes.Buffer
	for _, part := range n.Embedded {
		switch p := part.(type) {
		case ast.GoText:
			eb.WriteString(p.Src)
		case *ast.EmbeddedInterp:
			if len(p.Stages) > 0 {
				bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
				return "", false
			}
			if !emitGoExprEmbeddedInterp(hoistBuf, &eb, p, resolved, table, imports, rt, interpTemp, bag, !rejectCtx, !rejectErr) {
				return "", false
			}
		default:
			bag.Errorf(n.Pos(), n.End(), "unsupported-node", "element literals are not supported inside this interpolation position; bind the element to a variable in a {{ }} block or use a { } child position")
			return "", false
		}
	}
	return strings.TrimSpace(eb.String()), true
}
```

In `holeStringExpr`, replace `expr := strings.TrimSpace(n.Expr)` with:

```go
	expr, sok := assembleHoleSeed(b, n, resolved, table, imports, rt, interpTemp, bag, rejectErr, rejectCtx)
	if !sok {
		return "", false
	}
```

Same at the top of `embeddedHoleExpr` (its flags are named `exprPos, rejectCtx` — pass `exprPos` as `rejectErr`). **Audit every use of `n.Expr` below those points** (diagnostic messages may keep `n.Expr` for wording; the `lowerPipe` seed and all emitted code must use the assembled `expr`).

- [ ] **Step 4: Probe-side verification (emit ≡ probe)**

Build a scratch module (outside the repo) with the Step 1 input and run `go run ./cmd/gsx generate` on it. If generation reports type errors referencing the raw nested-literal text, the skeleton probes attr-segment holes verbatim too — find the attr-hole probe path in `analyze.go` (start from `walkMarkupAttrs` → `collectExprs`/`emitProbes` and how segment holes become `_gsxuse(...)` probes) and apply the same assembly there using `probeEmbeddedInterpIIFE`/`embeddedProbeType` (mirror how `emitProbes`'s `*Interp` case splices embedded parts for body interps). If generation succeeds directly, probes were already Embedded-aware — note that in the commit message.

- [ ] **Step 5: Add per-context corpus cases**

Same shape as Step 1, one per context (all new):
- `interpolation/nested_literal_attr_hole.txtar` — Step 1's case (attr f`` hole).
- `jsattr/nested_f_in_js_hole.txtar` — `onclick=js` + "`" + `f(@{ f` + "`" + `id-@{who}` + "`" + ` })` + "`" — inner f`` inside a js`` attr hole; render pins JS-string/value escaping of the assembled value.
- `goexpr-js-literal/nested_f_hole_toplevel.txtar` — top-level `var h = js` + "`" + `f(@{ f` + "`" + `x` + "`" + ` })` + "`" (needs Task 3's scanner fix to parse; holes stay pure — error/ctx rejection at top level must still fire for nested content, add `goexpr-js-literal/diag_nested_err_toplevel.txtar` pinning that an error-pipe hole INSIDE the nested literal at top level still reports `goexpr-literal-error`).
- `goexpr-f-literal/nested_js_hole_interp.txtar` — `{ wrap(f` + "`" + `a @{ string(js` + "`" + `f(@{who})` + "`" + `) }` + "`" + `) }` inside a component (in-closure).
- `goblock-literal/nested_literal_hole.txtar` — `{{ h := f` + "`" + `a @{ f` + "`" + `b` + "`" + ` }` + "`" + ` }}`.
- `urlattrs/nested_literal_url_hole.txtar` — `href=f` + "`" + `/u/@{ f` + "`" + `@{id}` + "`" + ` }` + "`" — a URL-sink attr literal hole (holeStringExpr's URL branch); render pins whole-value URL sanitization is unaffected.
- `interpolation/diag_element_in_attr_hole.txtar` — `title=f` + "`" + `x @{ <b>y</b> }` + "`" → the new element diagnostic.

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus` → PASS. Read every new `generated.x.go.golden`: inner literals must appear as string-concat / `RawJS(...)` values, never as raw backtick text.

- [ ] **Step 6: Full check + commit**

Run: `make check` → PASS.

```bash
git add -A && git commit -m "feat(codegen): hole seeds assemble from Interp.Embedded — nested literals work in attr/expression holes"
```

---

### Task 5 (W3c): Pin body-position nesting, stage-args diagnostic, fmt + LSP

Body-position nesting worked before this plan but was never pinned; pin it. Literals inside pipe-stage arguments get a positioned diagnostic. Formatter idempotence and LSP nav over nested holes are verified.

**Files:**
- Create: `internal/corpus/testdata/cases/bodyinterp/nested_literal_hole.txtar`, `bodyinterp/nested_literal_hole_depth2.txtar`, `bodyinterp/nested_js_literal_hole.txtar`, `pipelines/diag_literal_in_stage_args.txtar`.
- Modify: `internal/codegen/analyze.go` — stage-args literal detection (location per Step 3).
- Create: `internal/gsxfmt/testdata/cases/nested_literal_hole.txtar` (fmt corpus).
- Test: `internal/lsp/` — extend the existing embedded-literal nav test file (`goblock_literal_nav_test.go` shows the pattern) with a nested-hole hover/def case.

**Interfaces:**
- Consumes: everything from Tasks 3–4.
- Produces: diagnostic `literal-in-stage-args` with message exactly: `prefixed literals in pipe-stage arguments are not supported; assign the literal to a variable first`. Parser-package helper `ContainsEmbeddedLiteral(src string) (off int, ok bool)` — a token-level scan (go/scanner + the `langPrefixStart` value-position test, mirroring `scanGoParts`'s STRING-token check) reporting the first prefixed literal in a Go fragment; **never a fragment parse**.

- [ ] **Step 1: Pin body-position nesting (three cases)**

From this session's live probes (N3/N4/N5) — these generate correctly today, so write case + `-update` + **audit** the goldens:
- `bodyinterp/nested_literal_hole.txtar`: `<p>{f` + "`" + `a @{ f` + "`" + `b` + "`" + ` }` + "`" + `}</p>` → renders `<p>a b</p>`.
- `bodyinterp/nested_literal_hole_depth2.txtar`: `<p>{f` + "`" + `a @{ f` + "`" + `b @{who}` + "`" + ` } c` + "`" + `}</p>` → `<p>a b Ada c</p>` (hostile `who` value in invoke to pin HTML escaping).
- `bodyinterp/nested_js_literal_hole.txtar`: `<p>{f` + "`" + `a @{ string(js` + "`" + `f(@{who})` + "`" + `) }` + "`" + `}</p>` with hostile `who` — pins `EscapeJSVal` inside `RawJS` inside HTML-text escaping.
- `bodyinterp/nested_literal_hole_dquote.txtar`: the dquote outer form — `<p>{f"a @{ f` + "`" + `b` + "`" + ` }"}</p>` (dquote outer, backtick inner) — pins the spec's "both delimiters" requirement.

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus` → PASS.

- [ ] **Step 2: Write the failing stage-args diagnostic case**

`pipelines/diag_literal_in_stage_args.txtar`: component body `<p>{ who |> printf(f` + "`" + `%s!` + "`" + `) }</p>` with expected `diagnostics.golden` carrying `literal-in-stage-args`. Run the corpus for this case — expected: FAIL (today it poisons or emits invalid Go).

- [ ] **Step 3: Implement stage-args detection**

Add `ContainsEmbeddedLiteral` to `parser` (export beside `langPrefixStart`, reusing it). In `analyze.go`, at the single choke point where every pipe stage's `Args` is visible before probe/skeleton assembly (find it: the stage handling shared by hole and interp probes — `grep -n "Stages" internal/codegen/analyze.go` and pick the collection point that runs once per stage for ALL contexts; `collectClauseSrc`'s stage-args loop at ~3585 shows the shape of iterating them), scan each non-empty `st.Args`:

```go
	if off, ok := gsxparser.ContainsEmbeddedLiteral(st.Args); ok {
		bag.Errorf(<owner>.Pos(), <owner>.End(), "literal-in-stage-args",
			"prefixed literals in pipe-stage arguments are not supported; assign the literal to a variable first")
		_ = off // position at the owning node; Args carries no own Pos
	}
```

Position at the owning `*ast.Interp`/`*ast.EmbeddedAttr` node (stages don't carry positions — verify by reading `ast.PipeStage`; if it does carry offsets, use them). Ensure the check runs before the skeleton parse so the cryptic downstream error never surfaces, and that generation for the file is gated off (mirror how other analyze-time `bag.Errorf` diagnostics gate emit).

- [ ] **Step 4: Run stage-args case + full corpus**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus` → PASS, `diagnostics.golden` pins the message.

- [ ] **Step 5: Fmt corpus + LSP nav**

- `internal/gsxfmt/testdata/cases/nested_literal_hole.txtar`: input with a nested literal in an attr hole and a body hole, deliberately mis-indented. Run `go test ./internal/gsxfmt -run TestFmtCorpus -update`, then **verify idempotence** (the suite's second-pass check) and that nested literal text round-trips byte-exact (degrade-to-verbatim for the hole expr is acceptable; corruption is not).
- LSP: in `internal/lsp`, add a test asserting hover/definition works on `who` inside `<p>{f` + "`" + `a @{ f` + "`" + `b @{who}` + "`" + ` }` + "`" + `}</p>` (the nested hole's ident — `inspectWithEmbedded` re-descends on every `*Interp`, so this should pass without code changes; the test pins it).

Run: `go test ./internal/gsxfmt ./internal/lsp` → PASS.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "test: pin nested-literal holes (body/attr/expr/fmt/LSP); reject literals in pipe-stage args"
```

---

### Task 6: ROADMAP + docs touch, final verification

**Files:**
- Modify: `docs/ROADMAP.md` — remove/replace the "nested literal directly in another literal's hole = cryptic parse error" follow-up (it ships as support); note W1′'s diagnostic under the same entry that tracked GoBlock/expression literal gaps if one exists.
- Modify: `docs/guide/` — **one sentence only** (docs-concise rule) in the page documenting embedded literals: holes accept any Go expression, including another literal. Skip entirely if the guide section on expression literals doesn't exist yet (it was deferred in #106) — in that case ROADMAP only.
- No sibling-repo changes.

**Interfaces:** none.

- [ ] **Step 1: Update ROADMAP + guide line**

Make the edits above. If touching `docs/guide/**`, remember literal `{{ }}` in prose needs a `::: v-pre` block (VitePress).

- [ ] **Step 2: Full verification**

Run: `make ci && make lint`
Expected: both PASS (uncached, authoritative).

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "docs: ROADMAP + guide touch for literal position-gap closing"
```

- [ ] **Step 4: Adversarial review gate**

Per house convention, before merge: dispatch one independent adversarial reviewer that **builds throwaway probe programs** (not just diff reading) covering: hostile values through nested literals in every context (XSS sweep: value/string/template/regexp JS holes, CSS ZgotmplZ, URL whole-value), the W2 hoist halt-on-error at render, W2 render byte-equivalence attr-local vs in-closure, deep nesting (depth 3+), and every diagnostic in this plan firing at the right position. Findings routed back as fix tasks before merge.

---

## Execution notes

- Task order: 1 and 2 are independent of everything; 3 → 4 → 5 are sequential; 6 last.
- After each task, re-run `go test ./internal/corpus -run TestCorpus` (no `-update`) to catch accidental golden drift.
- If a step's stated line number has drifted, anchor by the function name — never patch by line offset alone.
