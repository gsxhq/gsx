# Expression-Valued `js``/`css`` Literals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `js`…@{x}…`` and `css`…@{x}…`` literals become first-class Go expressions of static type `gsx.RawJS`/`gsx.RawCSS` — in top-level Go regions, `{ }` interpolation bodies, and (new for all three langs) body `{{ }}` Go blocks — with per-hole contextual escaping, no error channel (error-carrying holes are positioned diagnostics), and byte-identical output to the attribute-local forms.

**Architecture:** The parser already recognizes all three prefixes; one gate (`scanGoParts`, `off-p == len("f")`) keeps js/css out of Go-expression splits. We carry `Lang` on `ast.EmbeddedInterp`, open the gate, classify JS holes with the existing `internal/jsx` engine, type the analyze-probe IIFE as `_gsxrt.RawJS`/`RawCSS`, and lower at emit by reusing the fold path's pure-escaper assemblers (`embeddedJSValueExpr`/`embeddedCSSValueExpr`) in a new hoist-free expression mode wrapped in the trusted-type conversion. `{{ }}` blocks get the same `SplitGoExprElements` treatment `{ }` interp bodies already use.

**Tech Stack:** Go 1.26.1, `go/types`-probed codegen, txtar corpus harness (`internal/corpus`), fmt corpus (`internal/gsxfmt`), tree-sitter + TextMate grammars in sibling repos.

**Spec:** `docs/superpowers/specs/2026-07-13-expression-valued-js-css-literals-requirements.md` — read it before starting any task.

## Global Constraints

- Go pinned to **1.26.1** (`GO_VERSION` in `.github/workflows/ci.yml`).
- Runtime (root `gsx` package) is standard-library only and **needs no changes** — `RawJS`, `RawCSS`, `EscapeJSVal/Str/Tmpl/Regexp`, `StyleValue`, `FilterCSS` all exist and none returns an error. If you are editing root-package `.go` files, stop and re-read the spec.
- **Never hand-edit** `*.x.go` or `*.golden`. Regenerate: `go test ./internal/corpus -run TestCorpus -update`, then verify **without** `-update` (`-update` also rewrites `coverage.golden`; a forgotten manifest bump fails the suite). Fmt corpus: `go test ./internal/gsxfmt -run TestFmtCorpus -update`.
- **Emit ≡ probe:** any expression the emitter writes must be type-identical to what the analyze skeleton probes. When you change an emit lowering, change the matching probe site in `internal/codegen/analyze.go` in the same commit.
- Corpus is canonical: every behavior in this plan is pinned by a txtar case. New syntax valid in multiple contexts needs a case **per context**.
- Interpolation is `@{expr}` — never `${…}`. Backtick inside a backtick literal is `` \` ``. The `"`-delimited escape hatch (`js"…"`) is semantically identical.
- Diagnostics carry positions (`bag.Errorf(n.Pos(), n.End(), code, …)`); never a bare `fmt.Errorf` for a user error.
- The `gsx` binary on PATH is Ghostscript. Use `go run ./cmd/gsx …`.
- Inner dev loop: `make check`. Before merge: `make ci` (authoritative) and `make lint`.
- Work in a **git worktree** branch `goexpr-js-css-literals` created via the `superpowers:using-git-worktrees` skill, branched from current `main`.
- Sibling repos live at `~/personal/gsxhq/`: `tree-sitter-gsx`, `vscode-gsx`, `gsxhq.github.io` (local dir `website`).

---

### Task 1: `Lang` on `ast.EmbeddedInterp` + parser carries it

Every `EmbeddedInterp` gets an explicit language tag. No behavior change yet — the Go-expression gate stays closed; only `f` reaches these constructors today.

**Files:**
- Modify: `ast/ast.go:432-444` (`EmbeddedInterp` doc + struct)
- Modify: `parser/attrs.go:477-487` (`parseEmbeddedInterpPart`)
- Modify: `parser/markup.go:550-…` (`tryParseBodyEmbeddedInterp` — sets `Lang: ast.EmbeddedText` on its node)
- Modify: `internal/printer/printer.go` (`embeddedLiteralString` at ~1115 and every place the prefix `f` is printed for an `EmbeddedInterp` — print the prefix from `Lang`)
- Test: `parser/` package tests (existing test file colocated with `attrs.go` tests; add `TestParseEmbeddedInterpPartLang`)

**Interfaces:**
- Consumes: `ast.EmbeddedLang` enum (`EmbeddedJS=1, EmbeddedCSS=2, EmbeddedText=3`, `ast/ast.go:399-405`), `parseEmbeddedAttrLiteral() (lang, dquoted, segments, err)` (`parser/attrs.go:495`).
- Produces: `ast.EmbeddedInterp.Lang ast.EmbeddedLang` — set by **every** constructor; zero value is invalid. `prefixForLang(lang ast.EmbeddedLang) string` in the printer returning `"js"`, `"css"`, `"f"`.

- [ ] **Step 1: Write the failing parser test**

In the parser package test file (create `parser/embedded_interp_lang_test.go`):

```go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseEmbeddedInterpPartLang(t *testing.T) {
	cases := []struct {
		src  string
		want ast.EmbeddedLang
	}{
		{"f`hi @{x}`", ast.EmbeddedText},
		{"js`f(@{x})`", ast.EmbeddedJS},
		{"css`color:@{x}`", ast.EmbeddedCSS},
		{`js"f(@{x})"`, ast.EmbeddedJS},
	}
	for _, tc := range cases {
		p := newTestParser(t, tc.src) // use the package's existing test-parser constructor; if none exists, build one from ParseFile internals mirroring how attrs tests seat a parser
		node, err := p.parseEmbeddedInterpPart(0)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if node.Lang != tc.want {
			t.Errorf("%s: Lang = %d, want %d", tc.src, node.Lang, tc.want)
		}
		_ = token.NoPos
	}
}
```

(Adapt the parser construction to the package's existing internal-test idiom — look at how other `parser/*_test.go` files seat a `*parser` over a source string; do not invent a new harness.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./parser -run TestParseEmbeddedInterpPartLang -v`
Expected: FAIL — `node.Lang` is 0 for every case (field doesn't exist yet → compile error first; add the field, then Lang stays 0 for js/css until Step 3's parser change).

- [ ] **Step 3: Add the field and carry the lang**

`ast/ast.go` — replace the `EmbeddedInterp` doc + struct (keep `span`, `Stages`, `DoubleQuoted` as-is):

```go
// EmbeddedInterp is an interpolating prefixed literal used as a body/child
// expression ({f`…@{expr}…`}) or as a Go-expression value (f`…`, js`…`,
// css`…` in var initializers, call args, {{ }} blocks). Segments contain
// *Text and *Interp only; Stages is the optional whole-literal pipeline
// applied to the assembled string (body position only). Lang selects the
// lowering: EmbeddedText assembles a plain Go string; EmbeddedJS/EmbeddedCSS
// assemble a gsx.RawJS/gsx.RawCSS with per-hole contextual escaping. Lang is
// always set by the parser; the zero value is invalid.
type EmbeddedInterp struct {
	span
	Lang     EmbeddedLang
	Segments []Markup
	Stages   []PipeStage
	// DoubleQuoted records the delimiter: false is {f`…`}, true is {f"…"}. See
	// EmbeddedAttr.DoubleQuoted.
	DoubleQuoted bool
}
```

`parser/attrs.go:477` — capture the lang (currently discarded):

```go
func (p *parser) parseEmbeddedInterpPart(off int) (*ast.EmbeddedInterp, error) {
	p.i = off
	startPos := p.posAt(off)
	lang, dquoted, segs, err := p.parseEmbeddedAttrLiteral()
	if err != nil {
		return nil, err
	}
	node := &ast.EmbeddedInterp{Lang: lang, Segments: segs, DoubleQuoted: dquoted}
	ast.SetSpan(node, startPos, p.posAt(p.i))
	return node, nil
}
```

Then `grep -rn '&ast.EmbeddedInterp{' parser/ internal/` and set `Lang` at **every** remaining constructor (at minimum `tryParseBodyEmbeddedInterp` in `parser/markup.go` → `Lang: ast.EmbeddedText`; its gate at markup.go:592 already rejects non-text there).

Printer: in `internal/printer/printer.go`, find where an `EmbeddedInterp`'s prefix is printed (via `embeddedLiteralString` ~line 1115 and the `embeddedInterp` method ~line 729) and derive it from `Lang`:

```go
func prefixForLang(lang ast.EmbeddedLang) string {
	switch lang {
	case ast.EmbeddedJS:
		return "js"
	case ast.EmbeddedCSS:
		return "css"
	default:
		return "f"
	}
}
```

- [ ] **Step 4: Run the test and the full check**

Run: `go test ./parser -run TestParseEmbeddedInterpPartLang -v` — Expected: PASS.
Run: `make check` — Expected: PASS (no golden churn: gate still closed, `f` still prints `f`).

- [ ] **Step 5: Commit**

```bash
git add ast/ast.go parser/ internal/printer/
git commit -m "feat(ast/parser): carry Lang on EmbeddedInterp; print prefix from Lang"
```

---

### Task 2: `jsx.ResolveEmbedded` — hole classification for expression-position literals

Export a classifier for a bare segment list, reusing the exact skeleton-builder + `classify` machinery `resolveJSAttr` uses (fail-closed on comment holes and identifier-position holes, positioned diagnostics via the bag).

**Files:**
- Modify: `internal/jsx/jsx.go` (extract the segment-walking core of `resolveJSAttr` (~line 320) into a shared unexported helper; add `ResolveEmbedded`)
- Test: `internal/jsx/` (mirror the existing `resolveJSAttr` test table)

**Interfaces:**
- Consumes: `classify(skeleton, holes, bag)` (`jsx.go:414`), `classifyHole` (`jsx.go:461`), `holePrefix` sentinel (`jsx.go:28`), `ast.Interp.JSCtx` / `ast.JSCtx*` (`ast/ast.go:307,326-334`).
- Produces: `func ResolveEmbedded(segments []ast.Markup, bag *diag.Bag) bool` — sets `Interp.JSCtx` on every `*ast.Interp` segment, returns false if any diagnostic was recorded. Attribute behavior (`resolveJSAttr`) byte-for-byte unchanged.

- [ ] **Step 1: Write the failing test** — table-driven over segment lists built the way the existing jsx tests build them (find the existing test that drives `resolveJSAttr` / `ResolveScriptsErr` and mirror its construction): a value hole (`save(@{x})` → `JSCtxValue`), a string hole (`save('@{x}')` → `JSCtxString`), a template hole (`` save(`@{x}`) `` → `JSCtxTemplate`), a regexp hole (`m(/@{x}/)` → `JSCtxRegexp`), a malformed literal (`save(@{x}` with unbalanced paren → expect diagnostic), an identifier-position hole (`@{x}(1)` → fail-closed diagnostic, matching the existing `jsx-identifier-position` behavior).
- [ ] **Step 2: Run to verify failure** — `go test ./internal/jsx -run TestResolveEmbedded -v` → FAIL (undefined: ResolveEmbedded).
- [ ] **Step 3: Implement** — `resolveJSAttr` becomes a thin wrapper over the extracted helper (its `name` parameter only feeds diagnostic wording; give `ResolveEmbedded` neutral wording, e.g. "js literal"). No logic changes inside `classify`/`classifyHole`.
- [ ] **Step 4: Run** — `go test ./internal/jsx -v` → all PASS (old + new).
- [ ] **Step 5: Commit** — `git commit -m "feat(jsx): ResolveEmbedded classifies holes of expression-position js literals"`

---

### Task 3: Open the gate + JS value lowering end-to-end

The core vertical slice: js`` parses in Go-expression positions, holes classify, the probe types it `_gsxrt.RawJS`, and emit lowers to `_gsxrt.RawJS("…" + _gsxrt.EscapeJSVal(x) + "…")` with **no statement hoists** (expression positions have no error channel — Task 6 adds the diagnostics; in this task error-carrying holes may still fall into the old hoist path only if you cannot cleanly reject yet, but prefer wiring the rejection seam now and wording it in Task 6).

**Files:**
- Modify: `parser/goexpr.go:334-343, 350-359` (the two `off-p == len("f")` gates in `scanGoParts`)
- Modify: `internal/codegen/analyze.go` — classification call after `SplitGoExprElements` (~line 426); extend `jsx.ResolveScripts`'s reach to parse-time `GoWithElements` parts (see Step 4); probe IIFE typing at the region path (~line 793-802) **and** the `emitProbes` `*gsxast.EmbeddedInterp` case (~line 1421)
- Modify: `internal/codegen/emit.go` — the two `case *ast.EmbeddedInterp:` value sites (line 280 `GoWithElements`, line 2034 `Interp.Embedded`); refactor `embeddedJSValueExpr` (line 3536) and `embeddedHoleExpr` (line 3505) for segment-slice input + expression mode
- Modify: `internal/jsx/jsx.go` — `ResolveScripts` walks `GoWithElements` decls
- Create: `internal/corpus/testdata/cases/goexpr-js-literal/var.txtar`, `func_return.txtar`, `call_arg.txtar`, `attrs_map_spread.txtar`, `rawjs_slice.txtar`
- Delete/replace: `internal/corpus/testdata/cases/goexpr-f-literal/js_value_unsupported.txtar`

**Interfaces:**
- Consumes: `EmbeddedInterp.Lang` (Task 1), `jsx.ResolveEmbedded` (Task 2), `embeddedJSValueExpr`/`embeddedHoleExpr`/`stringifyJSExpr` (emit.go:3536/3505/3586), `rtImports.rt()` (rtimports.go:52), `embeddedProbeSeed` (analyze.go:802).
- Produces: `embeddedJSValueExpr(b *bytes.Buffer, segs []ast.Markup, jsCtxOf func(*ast.Interp) ast.JSCtx — NO, keep reading JSCtx off the node —, resolved, table, imports, rt, interpTemp, bag, errReturn string, exprPos bool) (string, bool)` — signature change: takes `segs []ast.Markup` instead of `*ast.EmbeddedAttr` (fold callers pass `a.Segments`), plus `exprPos bool`. Same for `embeddedCSSValueExpr` (used in Task 4) and `embeddedHoleExpr(…, errReturn string, exprPos bool)`. When `exprPos`: no `_gsxvN :=` temp materialization (concat is naturally source-ordered because nothing can hoist), and tuple/renderer-error/pipeline-error holes are rejected (Task 6 owns the final diagnostics; wire the rejection returns now with a provisional message).

- [ ] **Step 1: Write the failing corpus case** — `internal/corpus/testdata/cases/goexpr-js-literal/var.txtar`:

```txtar
# An expression-valued js`…@{x}…` literal in a top-level var initializer.
# Lowers to _gsxrt.RawJS("…" + _gsxrt.EscapeJSVal(x) + "…"): static text
# verbatim, the hole JS-value-encoded (strings get JSON quotes). The var's
# static type is gsx.RawJS; spread onto @click emits it as JS source through
# the normal HTML-attribute escape (never JSON-quoted whole).
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

var id = "abc"

var handler = js`save(@{id})`

component Page() {
	{{ attrs := gsx.Attrs{"@click": handler} }}
	<button {attrs...}>Save</button>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<button @click="save(&#34;abc&#34;)">Save</button>
```

**Note:** this case also uses a `{{ }}` block, which Task 7 delivers. For THIS task, write the component as `<button @click={handler}>Save</button>` instead and pin that render (`<button @click="save(&#34;abc&#34;)">Save</button>` — whole-value RawJS through an `{ expr }` attr goes `AttrValue(string(v))`, mirroring `jsattr/click_rawjs.txtar`); Task 7 adds the `{{ }}` variant.

- [ ] **Step 2: Run to verify current failure** — `go test ./internal/corpus -run TestCorpus 2>&1 | grep -A5 goexpr-js-literal` → FAIL with the "expected ';'" parse/skeleton error (the gate is still closed).

- [ ] **Step 3: Open the parser gate** — in `scanGoParts`, both delimiter blocks drop the length check (`langPrefixStart` already validates the prefix set):

```go
if tok == token.STRING && off < len(src) && src[off] == '`' {
	if p := langPrefixStart(src, off); p >= 0 {
		end, _ := embeddedLiteralEnd(src, off+1, '`')
		items = append(items, goSplitItem{Off: p, IsLiteral: true})
		base = end
		expectOperand = false
		advanced = true
		break
	}
}
```

(same one-line change in the `'"'` block at 350-359). Rewrite the two comment blocks above them: the gate is gone; all three prefixes split; js/css lower to `gsx.RawJS`/`gsx.RawCSS` with per-hole contextual escaping (point at the spec).

- [ ] **Step 4: Classification wiring** — two call sites:
  1. `internal/jsx/jsx.go` `ResolveScripts`: alongside the `*ast.Component` case in the `f.Decls` loop, add a case for the decl type that carries Go-region parts (`*ast.GoWithElements` — check its exact decl interface in `ast/ast.go`), walking `Parts` for `*ast.EmbeddedInterp` with `Lang == ast.EmbeddedJS` → `ResolveEmbedded(p.Segments, bag)`.
  2. `internal/codegen/analyze.go` ~426: immediately after `SplitGoExprElements` stores `interp.Embedded`, walk the returned parts for `*ast.EmbeddedInterp` with `Lang == ast.EmbeddedJS` → `jsx.ResolveEmbedded(p.Segments, bag)`. (Guard against double-classification: `ResolveEmbedded` on already-classified segments must be idempotent — it just overwrites the same `JSCtx`.)

- [ ] **Step 5: Probe typing** — region path (`analyze.go:793-802`): the IIFE return type and seed follow `p.Lang`:

```go
idx := len(gwMarkups)
gwMarkups = append(gwMarkups, p.Segments)
retType, wrapOpen, wrapClose := "string", "", ""
switch p.Lang {
case gsxast.EmbeddedJS:
	retType, wrapOpen, wrapClose = "_gsxrt.RawJS", "_gsxrt.RawJS(", ")"
case gsxast.EmbeddedCSS:
	retType, wrapOpen, wrapClose = "_gsxrt.RawCSS", "_gsxrt.RawCSS(", ")"
}
fmt.Fprintf(&compBuf, "func() %s {\n", retType)
fmt.Fprintf(&compBuf, "_gsxelem(%d)\n", idx)
compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
segCFTemp := 0
if err := emitProbes(&compBuf, p.Segments, …); err != nil { … }
fmt.Fprintf(&compBuf, "return %s%s%s\n}()", wrapOpen, embeddedProbeSeed(p.Segments, table, usedFilters), wrapClose)
```

Apply the **same** Lang-driven typing to the `emitProbes` `*gsxast.EmbeddedInterp` case at analyze.go:~1421 (read it first — it builds the analogous IIFE for `Interp.Embedded` parts; keep both sites structurally identical).

- [ ] **Step 6: Emit lowering** — refactor `embeddedJSValueExpr`/`embeddedCSSValueExpr`/`embeddedHoleExpr` signatures per the Interfaces block (mechanical: fold callers at emit.go:~6521-6565 pass `a.Segments` and `exprPos=false`). In expression mode skip the `_gsxvN :=` materialization — append `escaped` directly to `parts`. Then wire both value sites; at emit.go:280 the `case *ast.EmbeddedInterp:` becomes:

```go
case *ast.EmbeddedInterp:
	// A prefixed literal in Go-expression position. f`…` lowers to a plain Go
	// string concat; js`…`/css`…` lower to _gsxrt.RawJS/RawCSS wrapping the
	// same concat with per-hole contextual escaping (JSCtx-selected escaper /
	// StyleValue semantics). Expression positions have no statement context:
	// exprPos forbids hoists, so error-carrying holes are rejected (see
	// embeddedHoleExpr) and concat order is source order.
	if len(p.Stages) > 0 {
		bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
		partsOK = false
		break
	}
	switch p.Lang {
	case ast.EmbeddedJS:
		if val, vok := embeddedJSValueExpr(&wbuf, p.Segments, resolved, table, imports, rt, &interpTemp, bag, "", true); !vok {
			partsOK = false
		} else {
			wbuf.WriteString(rt.rt() + ".RawJS(" + val + ")")
		}
	case ast.EmbeddedCSS:
		if val, vok := embeddedCSSValueExpr(&wbuf, p.Segments, resolved, table, imports, rt, &interpTemp, bag, "", true); !vok {
			partsOK = false
		} else {
			wbuf.WriteString(rt.rt() + ".RawCSS(" + val + ")")
		}
	default:
		if val, vok := embeddedValueExpr(&wbuf, p.Segments, resolved, table, imports, rt, &interpTemp, bag, "return _gsxerr", "unsupported-node", "backtick literal value"); !vok {
			partsOK = false
		} else {
			wbuf.WriteString(val)
		}
	}
```

Mirror at emit.go:2034 (the `Interp.Embedded` site — same switch, `eb` instead of `wbuf`, `return false` instead of `partsOK`). In `embeddedHoleExpr`, `exprPos=true` short-circuits the three hoist paths (pipeline stage with `hasErr`, `(T, error)` tuple seed, renderer entry with `hasErr` — preflight the renderer table with the same key `applyRenderer` uses) into `bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", …)` returns; Task 6 finalizes wording and pins the cases.

- [ ] **Step 7: Remaining core corpus cases** — create with real inputs and expected renders (regenerate goldens after):
  - `func_return.txtar` — `func mk(id string) gsx.RawJS { return js`open(@{id})` }`, used as `<button @click={mk(props.ID)}>`; also pins that the user file needs its own `gsx` import for the signature while the *generated* escaper calls use `_gsxrt` (import need-tracking: write a second component in the same case whose helper returns `any` so the source file itself has no gsx reference other than generated `_gsxrt` — pins the PR #52 alias machinery).
  - `call_arg.txtar` — `{ consume(js`submit(@{props.FormID})`) }` in body with `func consume(h gsx.RawJS) gsx.Node`.
  - `attrs_map_spread.txtar` — top-level `func mkAttrs(id string) gsx.Attrs { return gsx.Attrs{"@click": js`select(@{id})`, "x-data": js`dialog(@{id})`} }` spread onto a button; render pins JS-source emission (HTML-escaped, never JSON-quoted).
  - `rawjs_slice.txtar` — `var vs = []gsx.RawJS{js`first()`, js`second(@{name})`}` consumed via index. Pins static assignability (spec Type Contract).
  - Replace `goexpr-f-literal/js_value_unsupported.txtar`: delete it; its role (the gate) is now inverted and covered by `var.txtar`.

- [ ] **Step 8: Regenerate + verify** — `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus` → PASS. `make check` → PASS.
- [ ] **Step 9: Commit** — `git commit -m "feat(codegen): expression-valued js literals lower to gsx.RawJS with per-hole escaping"`

---

### Task 4: CSS value lowering

**Files:**
- Modify: `internal/codegen/emit.go` (already wired by Task 3's switch — this task verifies + pins), `internal/codegen/analyze.go` (RawCSS probe typing verified)
- Create: `internal/corpus/testdata/cases/goexpr-css-literal/var_style_spread.txtar`, `hostile_zgotmplz.txtar`, `rawcss_passthrough.txtar`

**Interfaces:**
- Consumes: `embeddedCSSValueExpr` (`RawCSS`→`string(expr)` passthrough, else `rt.FilterCSS(stringify…)` — emit.go:3598), `gsx.StyleValue` semantics at the style sink.
- Produces: corpus pins only; no new functions.

- [ ] **Step 1: Failing corpus case** — `var_style_spread.txtar`:

```txtar
# An expression-valued css`…@{x}…` literal typed gsx.RawCSS, stored under a
# "style" key in gsx.Attrs and spread: composes with the class/style merge
# (StyleValue passes RawCSS verbatim; the literal's holes were already
# CSS-value-filtered at construction).
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

func mk(w int, color string) gsx.Attrs {
	return gsx.Attrs{"style": css`width:@{w}px;color:@{color}`}
}

component Page() {
	<div {mk(12, "teal")...} style="margin:0">x</div>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div style="margin:0; width:12px;color:teal">x</div>
```

(Adjust the expected merge order/separator to the actual class/style-merge semantics — run once without a golden and read the output; the POINT pinned is RawCSS passthrough through `StyleValue` + merge, holes filtered at construction. If the actual output differs in separator placement, pin the actual.)

- [ ] **Step 2:** `hostile_zgotmplz.txtar` — hole value `"red;background:url(javascript:alert(1))"` → construction bakes `ZgotmplZ` (mirror `style/explicit_css_attr_hostile.txtar`'s hostile value, but through the expression form + `{ expr }` style attr). `rawcss_passthrough.txtar` — a `gsx.RawCSS("color:red")` interpolated into a css`` hole passes verbatim (`isRawCSS` static branch), pinned in `generated.x.go.golden` as `string(expr)` not `FilterCSS`.
- [ ] **Step 3:** Regenerate, verify, `make check`.
- [ ] **Step 4: Commit** — `git commit -m "test(corpus): expression-valued css literals — RawCSS, filter failsafe, style merge"`

---

### Task 5: JS context matrix, hostile values, passthrough scope, evaluation order, dquote form

Pure corpus task pinning the spec's Interpolation Semantics section.

**Files:**
- Create under `internal/corpus/testdata/cases/goexpr-js-literal/`: `ctx_matrix.txtar`, `hostile.txtar`, `rawjs_passthrough_value.txtar`, `rawjs_escaped_in_string.txtar`, `eval_order_once.txtar`, `dquote_form.txtar`

**Interfaces:** consumes everything from Task 3; produces pins only.

- [ ] **Step 1:** `ctx_matrix.txtar` — one component, four holes in four lexical positions, pinned in the generated golden to four different escapers:

```txtar
# Four holes, four JS lexical contexts, four escapers. The generated golden
# pins EscapeJSVal / EscapeJSStr / EscapeJSTmpl / EscapeJSRegexp selection;
# the render pins the encoded output for a benign value.
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

func mk(v string) gsx.RawJS {
	return js`f(@{v}, '@{v}', \`@{v}\`, /@{v}/)`
}

component Page() {
	<button @click={mk("a")}>x</button>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<button @click="f(&#34;a&#34;, &#39;a&#39;, `a`, /a/)">x</button>
```

(Pin the actual HTML-escaped bytes the run produces; the shape above is the expectation to verify against, not to force.)

- [ ] **Step 2:** `hostile.txtar` — same four contexts invoked with `"'\"</script><x>${`+"`"+`}\nend"` — render golden proves every context neutralizes quotes, backticks, `${`, newline, HTML delimiters, script-closing text (compare against the attribute-local twins in `jsattr/*_hostile.txtar` — output for the same value in the same context must match those cases' encodings).
- [ ] **Step 3:** `rawjs_passthrough_value.txtar` — `js`wrap(@{h})`` where `h := gsx.RawJS("inner()")` → render contains `wrap(inner())` (runtime `EscapeJSVal` RawJS case). `rawjs_escaped_in_string.txtar` — `js`f('@{h}')`` with the same `h` → the RawJS is escaped **as string content** (static `JSCtxString` → `EscapeJSStr(string(h))`), NOT passed through; hostile `h := gsx.RawJS("x') ; steal('")` pins no breakout. This is the spec's "passthrough is value-position-only" rule.
- [ ] **Step 4:** `eval_order_once.txtar` — package counter `var n int; func next() int { n++; return n }`, literal `js`f(@{next()}, @{next()})`` → render pins `f(1, 2)` (left-to-right, exactly once).
- [ ] **Step 5:** `dquote_form.txtar` — `js"save(`@{x}`)"` (backtick free inside the `"`-form) in a var initializer; pins the escape-hatch delimiter in expression position.
- [ ] **Step 6:** Regenerate, verify, `make check`, commit — `git commit -m "test(corpus): js expression-literal context matrix, hostile values, passthrough scope, eval order"`

---

### Task 6: Diagnostics — error-carrying holes, body-position literal, malformed literals

**Files:**
- Modify: `internal/codegen/emit.go` (`embeddedHoleExpr` exprPos rejections — final wording), `internal/codegen/emit.go` genInterp body path (direct-literal-in-text check)
- Create: `internal/corpus/testdata/cases/goexpr-js-literal/diag_error_pipe.txtar`, `diag_error_tuple.txtar`, `diag_body_position.txtar`, `diag_malformed_js.txtar`, `indirect_rawjs_text.txtar`

**Interfaces:**
- Consumes: Task 3's `exprPos` seam; `filterEntry.hasErr` (`internal/codegen/filters.go:105-118`), renderer `hasErr` (`renderers.go:106`).
- Produces: diagnostic code `goexpr-literal-error` for all three error-carrying shapes; diagnostic code `goexpr-literal-text` for the body-position literal.

- [ ] **Step 1: Failing corpus diagnostics cases.** `diag_error_pipe.txtar`: a filter returning `(string, error)` piped in a hole of a var-position js literal —

```txtar
# A hole whose pipeline stage returns (T, error) inside a Go-expression js
# literal: no error channel exists in an arbitrary expression position, so
# this is a positioned diagnostic, never a silent hoist or miscompile.
-- filters.go --
package demo

func risky(s string) (string, error) { return s, nil }
-- input.gsx --
package demo

var h = js`f(@{name |> risky})`

var name = "x"

component Page() {
	<button @click={h}>x</button>
}
-- invoke --
Page()
-- diagnostics.golden --
input.gsx:3:14: error[goexpr-literal-error]: interpolation "name |> risky" needs error handling; a js`/css` literal in Go-expression position cannot propagate errors — handle the error in Go or move the literal to an attribute position
-- render.golden --
```

(Positions/wording: pin what the implementation actually produces after settling the message; the message MUST name the reason and both remedies. Check how filters are registered for corpus cases — see `filterPackages` in the corpus harness and existing pipe-error cases for the file layout.)

`diag_error_tuple.txtar` — hole seed is a call returning `(string, error)` directly. `diag_malformed_js.txtar` — `js`f(@{x}` (unbalanced) in var position → the `jsx.ResolveEmbedded` diagnostic surfaces, positioned in the .gsx file.

- [ ] **Step 2:** `diag_body_position.txtar` — `<div>{ js`alert(1)` }</div>`: the interp's Embedded parts are exactly one `*ast.EmbeddedInterp` with `Lang != EmbeddedText` (allow surrounding whitespace-only `GoText` parts) → `bag.Errorf(…, "goexpr-literal-text", "a js`/css` literal renders as visible text here; write the JavaScript in an attribute (@click=js`…`) or pass the value to something that consumes gsx.RawJS")`. Implement the check where the body-position `Interp` with `Embedded` parts is emitted (`genInterp`, near emit.go:2018) — gate on text sink only: `attr={ js`…` }` never reaches this path (the attr parser owns braced whole-value literals as `EmbeddedAttr`).
- [ ] **Step 3:** `indirect_rawjs_text.txtar` — `{ h }` in body where `h` is `gsx.RawJS` (declared in a sibling .go file) → renders HTML-escaped as ordinary text (pin the escaped bytes; this is EXISTING behavior — the case exists so nobody "fixes" it into raw HTML).
- [ ] **Step 4:** Regenerate/verify/`make check`; commit — `git commit -m "feat(codegen): positioned diagnostics for error-carrying holes and body-position js/css literals"`

---

### Task 7: `{{ }}` Go-block support (all three literal langs)

`GoBlock.Code` currently reaches the skeleton and the render closure verbatim; embedded literals inside are invalid Go. Give `GoBlock` the same split treatment `Interp.Embedded` gets. **Parser and printer stay untouched for round-tripping** (`Code` remains the verbatim source of truth; the printer keeps printing from `Code` — but see Task 10 for making its gofmt literal-aware). Element literals inside `{{ }}` remain unsupported → positioned diagnostic, ROADMAP entry.

**Files:**
- Modify: `ast/ast.go:456-464` — add `Embedded []GoPart` to `GoBlock` (doc: "populated by codegen's analyze split when Code contains embedded literals; nil otherwise; Code remains the verbatim round-trip source").
- Modify: `internal/codegen/analyze.go` — where component bodies are analyzed, run `parser.SplitGoExprElements` over `GoBlock.Code` (same call shape as line ~426; store on `gb.Embedded`; classify js parts via `jsx.ResolveEmbedded`); skeleton `GoBlock` case (~line 2018) walks parts when `Embedded != nil`: `GoText` verbatim, `*ast.EmbeddedInterp` → the same Lang-typed IIFE as Task 3 Step 5 (extract that IIFE emission into a shared helper `probeEmbeddedInterpIIFE(…)` rather than pasting it a third time), `*ast.Element`/`*ast.Fragment` → error (see below). Keep `ctrlOff[t]` registration exactly as-is.
- Modify: `internal/codegen/emit.go:1967-1970` — the `GoBlock` case walks parts when `Embedded != nil`: `GoText` verbatim, `EmbeddedInterp` → the same Lang switch as Task 3 Step 6 (extract into a shared helper `emitEmbeddedInterpValue(…)` used by all three sites), `*ast.Element`/`*ast.Fragment` → `bag.Errorf(part.Pos(), part.End(), "unsupported-node", "element literals inside {{ }} blocks are not supported yet")`.
- Create: `internal/corpus/testdata/cases/goblock-literal/motivating_attrs.txtar`, `f_literal.txtar`, `css_literal.txtar`, `element_rejected.txtar`
- Modify: `docs/ROADMAP.md` — element-literals-in-GoBlock gap entry.

**Interfaces:**
- Consumes: `parser.SplitGoExprElements(fset, src, base, cls)` (goexpr.go:666), Task 3's lowering helpers.
- Produces: `ast.GoBlock.Embedded []ast.GoPart`; shared helpers `probeEmbeddedInterpIIFE` (analyze) and `emitEmbeddedInterpValue` (emit) — all three container sites (`GoWithElements` parts, `Interp.Embedded`, `GoBlock.Embedded`) call them, so the lowerings can never diverge.

- [ ] **Step 1: Failing corpus case** — the spec's motivating example, `motivating_attrs.txtar`:

```txtar
# THE motivating example: a js` literal as a gsx.Attrs value inside a body
# {{ }} block. Also the first embedded literal of ANY lang inside {{ }} —
# GoBlock.Embedded is split, probed, and emitted like Interp.Embedded.
-- input.gsx --
package demo

import "github.com/gsxhq/gsx"

component Page(detail string) {
	{{
		containerAttrs := gsx.Attrs{
			"@suggest-datetime.window": js`suggest(@{detail})`,
		}
	}}
	<div {containerAttrs...}>x</div>
}
-- invoke --
Page(PageProps{Detail: "d1"})
-- diagnostics.golden --
-- render.golden --
<div @suggest-datetime.window="suggest(&#34;d1&#34;)">x</div>
```

- [ ] **Step 2: Run to verify failure** — parse/skeleton error today.
- [ ] **Step 3: Implement** per the Files list. Watch two things: (a) the skeleton `//line`-anchor discipline — `emitSkeletonClauseLine` is emitted per block today; with parts, emit a fresh line anchor before each `GoText` part so skeleton positions keep mapping back to the .gsx source after an IIFE splice shifts offsets (mirror how the `GoWithElements` region path anchors its parts — read analyze.go:~740-810 first); (b) `SplitGoExprElements` needs the correct absolute base position for `Code` (use `GoBlock.CodePos`).
- [ ] **Step 4:** `f_literal.txtar` (`{{ greeting := f`hello @{name}` }}` → `<p>{greeting}</p>`, closes the pre-existing f-gap and pins Text lang in GoBlock), `css_literal.txtar` (css` in a `{{ }}` local spread as style), `element_rejected.txtar` (`{{ x := <div/> }}` → the positioned unsupported diagnostic; pins the scope boundary).
- [ ] **Step 5:** Regenerate/verify/`make check`. Also run the LSP tests (`go test ./internal/lsp`) — CtrlMap bridging over GoBlocks now sees spliced skeleton text; if any existing nav test regresses, STOP and flag (do not paper over — the per-part line anchors in Step 3a are the real fix).
- [ ] **Step 6: Commit** — `git commit -m "feat(codegen): embedded literals inside {{ }} Go blocks (f/js/css); element literals diagnosed"`

---

### Task 8: Component-attr provenance + declared-prop binding

Two spec requirements on components. (1) Provenance: js`/css` attrs on a component fall through to the child's `Attrs` bag (existing behavior, `components/embedded_attr_prop.txtar`) — but today the bag value is a plain `string`; it must become `_gsxrt.RawJS`/`RawCSS` (render-identical through spread — `toStr` prints the content — but provenance now survives re-interpolation). (2) Prop binding: `Handler={ js`open(@{id})` }` on a component whose declared prop is `gsx.RawJS` must bind the prop.

**Files:**
- Investigate first: how a braced `Name={ js`…` }` on a component parses today (`parser/attrs.go:345` `parseBracedEmbeddedAttrValue` → `EmbeddedAttr`?) and how `childPropsLiteral` (emit.go, search it) routes `EmbeddedAttr`s on components (declared-prop match vs bag fallthrough).
- Modify: the component bag assembly in `internal/codegen/emit.go` (~6521-6565 region and/or `childPropsLiteral`) — wrap JS/CSS embedded values in `rt.rt()+".RawJS("+…+")"` / `".RawCSS("+…+")"`; and the declared-prop dispatch so a braced embedded JS/CSS attr whose name matches a declared prop binds it (typed RawJS/RawCSS — the probe side must match, per emit ≡ probe).
- Modify: `internal/corpus/testdata/cases/components/embedded_attr_prop.txtar` — generated golden gains the RawJS/RawCSS wraps (render golden unchanged — that's the point).
- Create: `internal/corpus/testdata/cases/components/embedded_prop_binding.txtar` (declared `Handler gsx.RawJS` prop bound by `Handler={ js`open(@{id})` }`; render pins the emitted handler), `embedded_attr_rawjs_bag.txtar` (undeclared name falls through as RawJS; child re-spreads; render unchanged vs today).

**Interfaces:**
- Consumes: Task 3 lowering helpers; `childPropsLiteral`; `attrsProps`/prop-field matching machinery.
- Produces: behavior change, documented in the case headers: braced embedded JS/CSS on a component now binds a **declared** prop (previously it silently fell through to the bag — a useless behavior for a declared name); unbraced embedded attrs keep bag fallthrough unchanged.

- [ ] **Step 1:** Write `embedded_prop_binding.txtar` first; run; observe today's behavior (silently bagged or diagnostic — record it in the case comment as the "before").
- [ ] **Step 2:** Implement both changes; the probe skeleton's `FieldProps` literal must type the bound prop value as RawJS (same wrap in analyze's childProps probe — find it by searching analyze.go for the childPropsLiteral counterpart).
- [ ] **Step 3:** Regenerate ALL goldens (`-update`), inspect the diff: `embedded_attr_prop.txtar`'s generated golden gains wraps, render goldens across the corpus must be **byte-identical** except the new cases. Any render.golden change outside this task's cases = a miscompile; stop and investigate.
- [ ] **Step 4:** `make check`; commit — `git commit -m "feat(codegen): RawJS/RawCSS provenance for component embedded attrs; braced literal binds declared prop"`

---

### Task 9: Differential equivalence + renderer interaction

**Files:**
- Create: `internal/corpus/testdata/cases/goexpr-js-literal/differential.txtar`, `renderer_hole.txtar`, `renderer_rawjs_registered.txtar`

**Interfaces:** consumes everything; produces pins only.

- [ ] **Step 1:** `differential.txtar` — one component, four buttons whose `@click` must render **byte-identical** values from the same literal text and inputs: (1) attribute-local unbraced `@click=js`save(@{id})``, (2) attribute-local braced `@click={ js`save(@{id})` }` (EmbeddedAttr path), (3) `@click={h}` where `{{ h := js`save(@{id})` }}` (expression form), (4) spread from `gsx.Attrs{"@click": js`save(@{id})`}`. The render golden showing four identical attribute values IS the differential proof (correctness-first rule: two lowerings, one pinned behavior). Include a hostile `id` so the equivalence covers the escapers, not just plain text. Add the css twin (style via `style=css`…`` vs expression-form spread) in the same case or a sibling.
- [ ] **Step 2:** `renderer_hole.txtar` — mirror `renderers/css_attr_hole.txtar`'s `pg.Text`→`rend.PgColor` setup but with the hole inside an **expression-position** js literal and a css literal: renderer applies first, its string result feeds the same escaper any string would (generated golden pins `_gsxf<i>.PgColor((v))` inside `EscapeJSStr(...)`). A renderer returning `(string, error)` in expression position → the Task 6 diagnostic (add to `diag_error_pipe.txtar`'s family or here).
- [ ] **Step 3:** `renderer_rawjs_registered.txtar` — register a renderer for `github.com/gsxhq/gsx.RawJS` in `gsx.toml`; pin whatever the current whole-value attribute behavior is for `@click={ gsx.RawJS(…) }` AND assert the expression form matches it exactly. If the registry rejects gsx-typed registrations, pin THAT (the diagnostic). If this surfaces incoherent behavior, stop and flag — do not invent semantics inline.
- [ ] **Step 4:** Regenerate/verify/`make check`; commit — `git commit -m "test(corpus): attr-vs-expression differential equivalence; renderer interaction pins"`

---

### Task 10: Formatter — literal-aware `{{ }}` gofmt + fmt corpus

Printer already formats hole expressions inside embedded literals (`writeEmbeddedAttrSegments`, printer.go:1096) and handles Go-expression parts via placeholder-sanitized gofmt (`fmtGoExprParts`, printer.go:1478 + `internal/goexprshape`). Two gaps: (a) `goBlock` (printer.go:748) feeds raw `Code` to `fmtStmts`, which now contains literals gofmt can't parse — give it the same `goexprshape` Sanitize→format→re-splice treatment `fmtGoExprParts` uses; (b) no fmt-corpus case pins any expression-position literal layout.

**Files:**
- Modify: `internal/printer/printer.go:748-750` (`goBlock`)
- Create: `internal/gsxfmt/testdata/cases/goexpr-js-literal.txtar`, `goblock-literal.txtar`

**Interfaces:**
- Consumes: `goexprshape.Classify`/`Sanitize` (`internal/goexprshape/shape.go`), `fmtStmts`, `writeEmbeddedAttrSegments`, Task 1's `prefixForLang`.
- Produces: `goBlock` formatting stable and idempotent for literal-bearing blocks.

- [ ] **Step 1: Failing fmt corpus cases.** `goexpr-js-literal.txtar` (`input.gsx` + `fmt.golden`): a var-position js literal with a deliberately misformatted hole (`js`save( @{ props.ID  } )``) and a multiline Go expression containing a literal — golden pins hole reformatted (`@{ props.ID }` per the existing hole style — check an existing embedded-literal fmt behavior by running the formatter once), literal text and prefix/delimiter preserved verbatim. `goblock-literal.txtar`: the motivating `{{ }}` block misindented — golden pins gofmt-normalized statements with the literal intact. Run `go test ./internal/gsxfmt -run TestFmtCorpus` → FAIL (or crash/fallback — record which).
- [ ] **Step 2: Implement** `goBlock` sanitize-format-resplice. Idempotence: the fmt corpus harness already checks format(format(x)) == format(x) — rely on it, don't hand-roll.
- [ ] **Step 3:** `-update`, verify without, `make check`. Also run the faithfulness harness if present (layout-fact AST bools zeroed — see `gsx-fmt-preserve-author-linebreaks` conventions).
- [ ] **Step 4: Commit** — `git commit -m "feat(fmt): literal-aware {{ }} formatting; fmt corpus for expression-position literals"`

---

### Task 11: LSP verification

Explorer analysis says hover/go-to-def inside holes is node-type-driven (`ExprMap` via `emitProbes` recursion + `inspectWithEmbedded`, `internal/lsp/mapping.go:25`) and arrives free once probes recurse the new positions. Verify, don't assume.

**Files:**
- Test: `internal/lsp/` — extend the existing hover/definition test tables (find the tests covering f-literal holes; add rows).

- [ ] **Step 1:** Add test rows: (a) go-to-definition from `@{detail}` inside a js literal in a `{{ }}` block → the component param; (b) hover over `@{id}` inside a js literal in a var initializer → `string`; (c) definition from a hole inside a css literal in expression position. Run `go test ./internal/lsp -run 'TestHover|TestDefinition' -v`.
- [ ] **Step 2:** If any row fails, the fix belongs in `emitProbes`/`inspectWithEmbedded` recursion (one new `case` at most — `GoBlock.Embedded` may need adding to `inspectWithEmbedded`'s walk since it's a new parts carrier). No per-position wiring lists.
- [ ] **Step 3:** `make check`; commit — `git commit -m "test(lsp): hover/definition inside expression-position literal holes"`

---

### Task 12: Sibling grammars

**Files:**
- Modify: `~/personal/gsxhq/tree-sitter-gsx/grammar.js` — add `$.embedded_js_literal, $.embedded_css_literal` to the `_expression` choice (lines 59-89; token rules already exist at 108-121) and update the comment at 85-87 stating the old attribute-only rule. Regenerate (`tree-sitter generate`) and extend the grammar's test corpus (`test/corpus/` — add expression-position js/css cases mirroring an existing f-in-expression test).
- Modify: `~/personal/gsxhq/vscode-gsx/syntaxes/gsx.tmLanguage.json` — add `#embedded-js-literal`/`#embedded-css-literal` includes wherever `#embedded-f-literal` is included for Go contexts: top-level `patterns` (~line 12) and `#go-block` (~line 166). (`#interp` at 202-205 already has them.)
- Modify: `~/personal/gsxhq/website/.vitepress/grammars/gsx.tmLanguage.json` — byte-identical synced copy of the vscode grammar; re-sync after editing (check `website/scripts/sync-docs.mjs` for the sync direction before hand-copying).
- Verify only (no change expected): the playground CodeMirror tokenizer (`website/.vitepress/theme/GsxPlayground.vue:652`) already matches `(f|js|css)` flat in any position. The playground **WASM** must be rebuilt from the new gsx codegen or expression literals will fail transform there — follow the rebuild+cache-bust steps in the docs-verify conventions (`gsx-docs-local-verify-gotchas`).

- [ ] **Step 1:** tree-sitter: failing corpus test → grammar edit → `tree-sitter generate && tree-sitter test` → PASS → commit in that repo.
- [ ] **Step 2:** vscode-gsx: edit includes; smoke-test with the extension's grammar snapshot tests if present (check `package.json` scripts); commit.
- [ ] **Step 3:** website: sync grammar; rebuild WASM; verify a js-in-`{{ }}` snippet highlights in a local VitePress build (`::: v-pre` wrapping for any literal `{{ }}` in prose!); commit.

---

### Task 13: Docs, ROADMAP, CI, adversarial review

- [ ] **Step 1: Guide docs.** Add expression-valued literals to the docs guide where f-literals/js-attributes are documented (`~/personal/gsxhq/website` guide pages). **Concise** — state the behavior plainly (js``/css`` are Go expressions typed `gsx.RawJS`/`gsx.RawCSS`; holes escape by JS/CSS context; errors can't cross, so error-returning pipes are compile errors; JS trust ≠ HTML trust — one sentence each); rationale stays in the spec. Literal `{{ }}` in prose needs `::: v-pre`.
- [ ] **Step 2: Spec + ROADMAP.** Flip the spec's Status to Implemented with a one-line pointer to this plan. Update `docs/ROADMAP.md`: element-literals-in-`{{ }}` gap (Task 7), and file the pre-existing oddity found during planning — **f-literal value positions pass `errReturn: "return _gsxerr"` unconditionally** (emit.go:289, 2043), which can emit `return _gsxerr` inside enclosing functions that return no error; track as follow-up, out of scope here.
- [ ] **Step 3:** `make ci` and `make lint` from the worktree — both PASS, output shown, no assertions without evidence.
- [ ] **Step 4: Independent adversarial review** (repo convention): a reviewer that builds throwaway probe programs — at minimum: hostile values through every new context in a real rendered page; a probe that tries to smuggle an unsanitized value through RawJS-in-string-position; a `{{ }}` block mixing literals, control flow, and shadowed names; formatter idempotence over the new cases. Findings fixed before merge.
- [ ] **Step 5:** Merge per `superpowers:finishing-a-development-branch` (PR against `main`).

---

## Self-Review (performed at plan time)

**Spec coverage:** Problem/motivating example → T3+T7; Goals 1-7 → T3 (positions, type, once/L-to-R), T2+T5 (contexts/escaping), T9 (attr behavior unchanged + differential), T6 (diagnostics); Non-goals respected (no runtime edits, url() untouched, no `${}`); Type Contract → T3 `rawjs_slice`/`func_return`; Interpolation Semantics table → T5 `ctx_matrix` + passthrough pair, T4 css; Evaluation Requirements → T5 `eval_order_once` + T6 error rejections; Attributes/spread → T3 `attrs_map_spread`, T4 style merge, T8 provenance; Body/Text → T6; Renderers → T9; Diagnostics → T6; Formatter/Tooling list → T10-T12; Required Tests 1-15 → T3(1-4,6,13), T8(5,14), T5(7-10), T3(11), T9(12,14), T6(15); Acceptance criteria → T13.
**Known intentional scope beyond the spec text:** `{{ }}` support (user-approved 2026-07-13) — spec Goal 1 should be read as including it; T13 Step 2 updates the spec accordingly.
**Type consistency:** `embeddedJSValueExpr(b, segs []ast.Markup, resolved, table, imports, rt, interpTemp, bag, errReturn string, exprPos bool)` used identically in T3/T4/T7/T8; `jsx.ResolveEmbedded(segments []ast.Markup, bag *diag.Bag) bool` in T2/T3/T7; `ast.EmbeddedInterp.Lang` in T1/T3/T7/T10; shared helpers `probeEmbeddedInterpIIFE`/`emitEmbeddedInterpValue` introduced in T7 and referenced nowhere earlier (T3 inlines, T7 extracts — deliberate).
