# Interpolating attribute-value literals — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a backtick string literal with `@{ }` holes in attribute-value position
(`` <span class=`badge-@{variant}` /> ``), lowering to per-segment, type-aware, context-correct
writer calls — closing gsx's one interpolation gap.

**Architecture:** A third `ast.EmbeddedLang` (`EmbeddedText`) reusing the existing js/css
embedded-literal parser/scanner; a new `emitEmbeddedTextAttr` codegen path routing each hole
through the existing type-aware `emitAttrValue`, with compile-time URL-region analysis for
URL-context attrs and first-class class/style merge integration. Formatter + LSP + fuzz + corpus.

**Tech Stack:** Go (stdlib-only runtime), `internal/codegen` (go/types + text emit), `parser/`,
`ast/`, `internal/printer` (formatter), `internal/lsp`, `internal/corpus` (txtar goldens).

**Spec:** `docs/superpowers/specs/2026-07-04-attr-interp-literal-design.md`

## Global Constraints

- Runtime (root package) is **standard-library only**. Tooling may use `golang.org/x/tools`.
- Run gsx as `go run ./cmd/gsx …` (the `gsx` binary name collides with Ghostscript).
- Security escaping is a **faithful** behavior, never an approximation. URL handling stays
  gsx-consistent: **scheme-sanitize + entity-escape only, no percent-encoder**.
- Regenerate corpus goldens with `go test ./internal/corpus -run TestCorpus -update` (also
  rewrites `coverage.golden`), then verify a clean run **without** `-update`.
- **Don't hand-edit** `.x.go` or `*.golden` — change source and regenerate.
- Gate with `make check` (inner loop) and `make ci` before merge.
- Red baseline already committed: `internal/corpus/testdata/cases/textattr/{class_interp,
  class_spread_merge,url_prescheme,url_postscheme,url_seam_rejected}.txtar`.

---

## Task 1: AST — `EmbeddedText` language value

**Files:**
- Modify: `ast/ast.go:319-337` (the `EmbeddedLang` const block + `EmbeddedAttr` doc)
- Test: `parser/embedded_text_test.go` (created in Task 2; no standalone AST test)

**Interfaces:**
- Produces: `ast.EmbeddedText ast.EmbeddedLang` — the third lang value, used by parser + codegen
  + printer to mark a plain (HTML-attr-escaped) backtick literal.

- [ ] **Step 1: Add the enum value + doc**

In `ast/ast.go`, extend the const block:
```go
const (
	EmbeddedJS EmbeddedLang = iota + 1
	EmbeddedCSS
	EmbeddedText // plain backtick literal: name=`…@{expr}…`, HTML-attribute-escaped
)
```
Update the `EmbeddedAttr` doc comment to mention the bare/braced text forms:
```go
// EmbeddedAttr is an embedded-language attribute value:
//   name=js`…@{expr}…`, name={js`…`}, name=css`…`, name={css`…`},
//   name=`…@{expr}…`  (EmbeddedText — plain, HTML-attribute-escaped), name={`…`}.
// Segments contain *Text and *Interp only.
```

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: builds (nothing consumes `EmbeddedText` yet; this is a pure addition).

- [ ] **Step 3: Commit**

```bash
git add ast/ast.go
git commit -m "feat(ast): add EmbeddedText embedded-language value"
```

---

## Task 2: Parser — bare + braced backtick `EmbeddedText` dispatch

**Files:**
- Modify: `parser/attrs.go:282-362` (`parseAttrValue` switch, `parseEmbeddedAttrLiteral`)
- Test: `parser/embedded_text_test.go` (create)

**Interfaces:**
- Consumes: `ast.EmbeddedText` (Task 1), existing `parseEmbeddedSegments`,
  `parseEmbeddedAttrValue`, `parseBracedEmbeddedAttrValue`.
- Produces: parsing of `` name=`…` `` and `` name={`…`} `` into
  `*ast.EmbeddedAttr{Lang: ast.EmbeddedText, Segments: …}`.

- [ ] **Step 1: Write the failing test**

Create `parser/embedded_text_test.go`:
```go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseEmbeddedTextAttr(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class=`badge-@{v} x`>h</span> }\n"
	f, err := Parse("in.gsx", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	if ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText (%d)", ea.Lang, ast.EmbeddedText)
	}
	// segments: Text("badge-"), Interp(v), Text(" x")
	if len(ea.Segments) != 3 {
		t.Fatalf("segments = %d, want 3: %#v", len(ea.Segments), ea.Segments)
	}
	if _, ok := ea.Segments[1].(*ast.Interp); !ok {
		t.Fatalf("segment[1] = %T, want *ast.Interp", ea.Segments[1])
	}
}

func TestParseEmbeddedTextBraced(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class={`badge-@{v}`}>h</span> }\n"
	f, err := Parse("in.gsx", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ea := firstEmbeddedAttr(t, f); ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText", ea.Lang)
	}
}
```
Add a small `firstEmbeddedAttr` helper in the same file that walks `f` via `ast.Inspect` and
returns the first `*ast.EmbeddedAttr` (fail the test if none).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser -run TestParseEmbeddedText -v`
Expected: FAIL — `parseAttrValue` errors `expected attribute value ("…" or { … }) after '='`.

- [ ] **Step 3: Add bare-backtick dispatch**

In `parser/attrs.go`, extend the `parseAttrValue` switch. Change the first case:
```go
	case p.at("js`") || p.at("css`") || p.at("`"):
		return p.parseEmbeddedAttrValue(name, attrStartPos)
```
And in the `p.src[p.i] == '{'` case, extend the braced-embedded check:
```go
		if strings.HasPrefix(p.src[p.i+1:], "js`") ||
			strings.HasPrefix(p.src[p.i+1:], "css`") ||
			strings.HasPrefix(p.src[p.i+1:], "`") {
			return p.parseBracedEmbeddedAttrValue(name, attrStartPos)
		}
```
Note: the `{{` ordered-attrs check and `class`/`style` composed check already sit *after* this in
the switch, and the braced-backtick check must precede them — place it as the first sub-check
inside the `{` case (it already is, before the `{{` check). Verify ordering by reading the case.

- [ ] **Step 4: Add the `EmbeddedText` arm to `parseEmbeddedAttrLiteral`**

In `parser/attrs.go:341-362`, add a case before `default`:
```go
	case p.at("`"):
		lang = ast.EmbeddedText
		p.i += len("`")
		opener = literalStart // the backtick itself
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./parser -run TestParseEmbeddedText -v`
Expected: PASS (both cases).

- [ ] **Step 6: Regression — js/css still parse; corpus parser-level**

Run: `go test ./parser ./internal/corpus -run 'TestCorpus/(jsattr|style|script)' 2>&1 | tail`
Expected: existing embedded cases still PASS.

- [ ] **Step 7: Commit**

```bash
git add parser/attrs.go parser/embedded_text_test.go
git commit -m "feat(parser): parse bare and braced EmbeddedText backtick literals"
```

---

## Task 3: Parser — `\@{` literal-hole escape (shared scanner)

**Files:**
- Modify: `parser/attrs.go:364-436` (`parseEmbeddedSegments`, add `embeddedAtBraceEscaped`,
  extend `unescapeEmbeddedBackticks` → `unescapeEmbedded`)
- Test: `parser/embedded_text_test.go`

**Interfaces:**
- Produces: inside any embedded literal (text/js/css), `\@{` is literal `@{` text, not a hole
  trigger; `` \` `` unchanged. js/css inherit the fix.

- [ ] **Step 1: Write the failing test**

Append to `parser/embedded_text_test.go`:
```go
func TestEmbeddedTextEscapedHole(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span data-x=`lit \\@{ not a hole } @{v}`>h</span> }\n"
	f, err := Parse("in.gsx", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	// exactly one real hole (@{v}); the \@{ is literal text
	holes := 0
	for _, s := range ea.Segments {
		if _, ok := s.(*ast.Interp); ok {
			holes++
		}
	}
	if holes != 1 {
		t.Fatalf("holes = %d, want 1 (\\@{ must be literal)", holes)
	}
	// literal text must contain "@{ not a hole }" with the backslash removed
	if got := embeddedText(ea); !strings.Contains(got, "@{ not a hole }") {
		t.Fatalf("literal text = %q, want it to contain unescaped %q", got, "@{ not a hole }")
	}
}
```
Add `embeddedText` helper (concatenate all `*ast.Text` segment `.Value`s) and import `strings`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser -run TestEmbeddedTextEscapedHole -v`
Expected: FAIL — currently `\@{` triggers a hole (or the `\` stays in text), so `holes == 2` or
the assertion on unescaped text fails.

- [ ] **Step 3: Guard the `@{` trigger against an escaping backslash**

In `parseEmbeddedSegments` (`parser/attrs.go`), change the hole trigger to skip an escaped `@{`:
```go
		if p.src[p.i] == '@' && p.i+1 < len(p.src) && p.src[p.i+1] == '{' {
			if p.embeddedAtBraceEscaped(p.i) {
				p.i++ // consume '@'; '{' handled next iteration as literal
				continue
			}
			flush(p.i)
			p.i++ // past '@'; cursor now at '{' for parseInterp
			…unchanged…
		}
```
Add the helper mirroring `embeddedBacktickEscaped`:
```go
func (p *parser) embeddedAtBraceEscaped(at int) bool {
	n := 0
	for i := at - 1; i >= 0 && p.src[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}
```

- [ ] **Step 4: Unescape `\@{` (and keep `\``) in flushed text**

Rename `unescapeEmbeddedBackticks` → `unescapeEmbedded` (update the one call in `flush`), and make
it collapse both escapes. Replace the body with a single left-to-right pass:
```go
func unescapeEmbedded(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			// \`  -> `
			if s[i+1] == '`' {
				b.WriteByte('`')
				i++
				continue
			}
			// \@{ -> @{
			if s[i+1] == '@' && i+2 < len(s) && s[i+2] == '{' {
				b.WriteString("@{")
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
```
Delete the now-unused `backtickEscapedIn` if nothing else references it (`grep -rn backtickEscapedIn`);
otherwise leave it. Keep `embeddedBacktickEscaped` (still used to decide the close backtick).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./parser -run TestEmbeddedText -v`
Expected: PASS (all three tests).

- [ ] **Step 6: Regression**

Run: `go test ./parser ./internal/corpus -run 'TestCorpus/(jsattr|style|script)' 2>&1 | tail`
Expected: PASS — including `jsattr/escaped_backtick_literal`.

- [ ] **Step 7: Commit**

```bash
git add parser/attrs.go parser/embedded_text_test.go
git commit -m "feat(parser): \\@{ escapes a literal hole opener in embedded literals"
```

---

## Task 4: Codegen — `emitEmbeddedTextAttr` for plain (non-URL, non-class/style) attrs

**Files:**
- Modify: `internal/codegen/emit.go` — add `case ast.EmbeddedText:` in `emitAttr` (near 1958);
  add `emitEmbeddedTextAttr` + `emitTextAttrInterp` (near the js/css emitters ~2100)
- Test: `internal/corpus/testdata/cases/textattr/class_interp.txtar` (already red) plus a new
  plain-attr case

**Interfaces:**
- Consumes: `emitAttrValue` (`emit.go:2610`), `lowerPipe`, `hoistTuple`, `tupleUnwrapType`,
  `htmlAttrEscape`, `emitPipeWrap`.
- Produces: `emitEmbeddedTextAttr(b, a, resolved, table, imports, interpTemp, cls, bag) bool` and
  `emitTextAttrInterp(b, n, resolved, table, imports, interpTemp, cls, attrName, bag) bool`.
  (The `cls`/`attrName` params are unused until Task 5 wires URL regioning; pass them through now
  so the signature is stable.)

- [ ] **Step 1: Add a second red case (plain attr, string+int+pipeline)**

Create `internal/corpus/testdata/cases/textattr/plain_attr.txtar`:
```
# plain (non-URL) attribute: string hole entity-escaped, int hole rendered, per-hole pipeline
-- input.gsx --
package p

component Row(id string, n int) {
	<div data-key=`row-@{id}-@{n}` title=`Item @{ id |> upper }`>x</div>
}
-- invoke --
Row(RowProps{Id: "a&b", N: 5})
-- diagnostics.golden --
-- render.golden --
<div data-key="row-a&amp;b-5" title="Item A&amp;B">x</div>
```
(`upper` is `std.Upper`; `a&b` proves attr entity-escaping of the hole.)

- [ ] **Step 2: Run to verify red**

Run: `go test ./internal/corpus -run 'TestCorpus/textattr/plain_attr' 2>&1 | tail -20`
Expected: FAIL (parse/codegen — no EmbeddedText emit yet).

- [ ] **Step 3: Dispatch `EmbeddedText` in `emitAttr`**

In `emit.go` `emitAttr`'s `*ast.EmbeddedAttr` switch, add:
```go
		case ast.EmbeddedText:
			if !emitEmbeddedTextAttr(b, t, resolved, table, imports, interpTemp, cls, bag) {
				return false
			}
```

- [ ] **Step 4: Implement `emitEmbeddedTextAttr` (mirror `emitEmbeddedJSAttr`)**

Add near the js/css emitters:
```go
// emitEmbeddedTextAttr emits a plain backtick attribute literal name=`…@{expr}…`.
// Static text is HTML-attr-escaped at codegen; each hole is emitted type-aware via
// emitTextAttrInterp (URL-context regioning is applied there, Task 5).
func emitEmbeddedTextAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
		case *ast.Interp:
			if !emitTextAttrInterp(b, s, resolved, table, imports, interpTemp, cls, a.Name, bag) {
				return false
			}
		default:
			bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "attribute %q value may contain only text and @{ } interpolations, got %T", a.Name, seg)
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}
```

- [ ] **Step 5: Implement `emitTextAttrInterp` (mirror `emitJSAttrInterp`, route to `emitAttrValue`)**

```go
// emitTextAttrInterp renders one @{ } hole in a plain attribute literal. Mirrors
// emitJSAttrInterp's pipeline + (T,error) auto-unwrap, then routes through the
// type-aware emitAttrValue (string→AttrValue, numbers→strconv, Stringer→.String()).
// cls/attrName drive URL-context regioning in Task 5; here every hole is a plain value.
func emitTextAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, attrName string, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table, emitPipeWrap(b, interpTemp))
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of attribute interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitAttrValue(b, tmp, elemT, imports, n, bag)
	}
	return emitAttrValue(b, expr, t, imports, n, bag)
}
```

- [ ] **Step 6: Verify the two plain cases pass; regenerate their goldens**

Run: `go test ./internal/corpus -run 'TestCorpus/textattr/(plain_attr|class_interp)' -update`
then `go test ./internal/corpus -run 'TestCorpus/textattr/(plain_attr|class_interp)'`
Expected: PASS. Inspect the generated `generated.x.go.golden` blocks (added by `-update`) to
confirm holes lower to `_gsxgw.AttrValue(...)` and static runs to `_gsxgw.S(...)`.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/textattr/plain_attr.txtar internal/corpus/testdata/cases/textattr/class_interp.txtar internal/corpus/testdata/cases/coverage.golden
git commit -m "feat(codegen): emit plain EmbeddedText attribute literals via emitAttrValue"
```

---

## Task 5: Codegen — whole-value URL sanitization for URL-context literals

**Files:**
- Modify: `internal/codegen/emit.go` — `emitEmbeddedTextAttr`: branch on `CtxURL` and emit one
  `_gsxgw.URL(<assembled string expr>)`; non-URL keeps the Task-4 per-segment path.
- Test: corpus `url_*` cases under `internal/corpus/testdata/cases/textattr/`.

**Interfaces:**
- Consumes: `attrclass.Classifier.Context(name)`, `lowerPipe`, `hoistTuple`, `tupleUnwrapType`,
  `emitPipeWrap`, the `classify(t)` type dispatch used by `emitAttrValue` (`emit.go:2610`),
  `_gsxgw.URL` runtime.
- Produces: for a URL-context `EmbeddedText` literal, a single `_gsxgw.URL(expr)` call where
  `expr` is a Go string built by concatenating each segment.

**Design (why whole-value):** an earlier per-hole classifier had FIVE browser-confirmed XSS
bypasses (see the spec's URL section). We do NOT classify per hole. For a `CtxURL` attribute we
assemble the entire value — static text as quoted literals, each hole as a string-typed
expression — and pass it through `_gsxgw.URL`, the SAME `urlSanitize` (fail-closed allow-list
http/https/mailto/tel → `about:invalid#gsx`) that `href={ expr }` uses. Provably as safe as
`href={ expr }`; no `urlHoleRegion`, no seam logic, no scheme detection.

- [ ] **Step 1: Confirm the red security cases fail**

The baseline `textattr/url_dangerous_blocked` and `url_split_blocked` currently render the
DANGEROUS url (Task 4 routes URL holes through `AttrValue`).
Run: `go test ./internal/corpus -run 'TestCorpus/textattr/(url_dangerous_blocked|url_split_blocked)' 2>&1 | tail -20`
Expected: FAIL — got `href="javascript:alert(1)"`, want `href="about:invalid#gsx"`. This is the
XSS the task closes.

- [ ] **Step 2: Add a hole→string-expression helper**

Add to `emit.go` a helper that lowers one hole to a Go **string expression** (mirrors
`emitTextAttrInterp`'s pipeline + `(T,error)` unwrap, but returns an expression instead of
emitting a writer call), routing by the same `classify(t)` categories `emitAttrValue` uses:
```go
// holeStringExpr lowers one @{ } hole to a Go string expression for URL assembly.
// It emits any pipeline/tuple hoisting to b, then returns the string-conversion expr.
func holeStringExpr(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) (string, bool) {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table, emitPipeWrap(b, interpTemp))
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return "", false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of URL interpolation %q", n.Expr)
		return "", false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "URL interpolation %q returns %s; only (T, error) is supported", expr, t)
			return "", false
		}
		expr = hoistTuple(b, expr, interpTemp)
		t = elemT
	}
	// Route by the same categories emitAttrValue uses. Read emitAttrValue
	// (emit.go:2610) + classify() and mirror its string/[]byte/int/uint/float/
	// Stringer cases, but produce an EXPRESSION:
	//   string/[]byte -> "string("+expr+")"
	//   int   -> `strconv.FormatInt(int64(`+expr+`), 10)`   (imports["strconv"]=true)
	//   uint  -> `strconv.FormatUint(uint64(`+expr+`), 10)`
	//   float -> `strconv.FormatFloat(float64(`+expr+`), 'g', -1, 64)`
	//   Stringer -> "("+expr+").String()"
	//   else  -> bag.Errorf(..., "URL interpolation %q has unsupported type %s", n.Expr, t); return "", false
	// (fill in using the exact classify() constants from emit.go)
}
```

- [ ] **Step 3: Emit whole-value `URL()` for CtxURL attrs**

In `emitEmbeddedTextAttr`, before the per-segment loop, branch:
```go
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	if cls.Context(a.Name) == attrclass.CtxURL {
		parts := make([]string, 0, len(a.Segments))
		for _, seg := range a.Segments {
			switch s := seg.(type) {
			case *ast.Text:
				if s.Value == "" {
					continue
				}
				parts = append(parts, strconv.Quote(s.Value)) // RAW text; URL() escapes
			case *ast.Interp:
				p, ok := holeStringExpr(b, s, resolved, table, imports, interpTemp, bag)
				if !ok {
					return false
				}
				parts = append(parts, p)
			default:
				bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "attribute %q value may contain only text and @{ } interpolations, got %T", a.Name, seg)
				return false
			}
		}
		concat := `""`
		if len(parts) > 0 {
			concat = strings.Join(parts, " + ")
		}
		fmt.Fprintf(b, "\t\t_gsxgw.URL(%s)\n", concat)
	} else {
		// existing Task-4 per-segment path: Text -> S(htmlAttrEscape), Interp -> emitTextAttrInterp
		for _, seg := range a.Segments {
			…unchanged…
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
```
Note: `emitTextAttrInterp` (Task 4) is still used by the non-URL branch; keep it. It no longer
needs URL params — if you simplified its signature in Task 4 leave it; just don't pass URL info.

- [ ] **Step 4: Regenerate url_* goldens + verify security**

Follow the corpus protocol. These cases need `generated.x.go.golden` sections (add empty ones) so
the `URL(concat)` shape is pinned. `-update` ONLY the url_* cases, `git checkout` coverage.golden.
Cases: `url_path`(url_postscheme), `url_base_join`(url_prescheme), `url_dynamic_scheme`,
`url_multi_hole`, `url_dangerous_blocked`, `url_split_blocked`.
Run: `go test ./internal/corpus -run 'TestCorpus/textattr/url_' -update` then verify without.
Confirm: `url_dangerous_blocked` and `url_split_blocked` now render `href="about:invalid#gsx"`;
`url_dynamic_scheme` renders `https://ex.com`; every url_* generated golden shows a single
`_gsxgw.URL(...)` call (never `AttrValue` for a URL attr).

- [ ] **Step 5: Regression sweep**

Run: `go test ./internal/corpus 2>&1 | grep -E '^\s*--- FAIL' | grep -v textattr | grep -v 'TestCorpus (' || echo "no non-textattr failures"`
Expected: `no non-textattr failures` (still-red `class_spread_merge` is Task 6).

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/textattr/url_*.txtar
git commit -m "feat(codegen): whole-value URL sanitization for URL-context text literals"
```
(Do NOT commit coverage.golden.)

## Task 6: Codegen — class/style literal as a first-class merge target

**Files:**
- Modify: `internal/codegen/emit.go` — `emitFallthroughAttrs` merge-site finder (~562-580),
  the root class/style merge emit (`emitRootStaticClass` ~1096 and siblings), `emitSpread`'s
  class/style drop logic
- Test: corpus `class_spread_merge` (already red) + style-merge + scalar-merge cases

**Interfaces:**
- Consumes: existing `emitRootStaticClass`/`emitRootComposedClass`/`emitSpread` machinery.
- Produces: an `*ast.EmbeddedAttr{Lang: EmbeddedText}` named `class`/`style` recognized as the
  element's class/style merge site; its interpolated value emitted then the bag's tokens appended.

**Approach:** the finder currently binds `staticClass *ast.StaticAttr` / `classAttr *ast.ClassAttr`.
Add a parallel `embedClass *ast.EmbeddedAttr` / `embedStyle *ast.EmbeddedAttr` binding, exclude
them from `forcedNames` (like static/composed class), and at the merge site emit the interpolated
segments followed by the bag's class (mirroring how `emitRootStaticClass` appends
`merge(bagClass, "static")`). Because assembling the interpolated own-class into one string is
needed to hand to the class merger, this path may build one string via the merger call — that is
acceptable (class merge already allocates).

- [ ] **Step 1: Confirm the red case + add two more**

`class_spread_merge.txtar` is already red. Add:

`internal/corpus/testdata/cases/textattr/style_spread_merge.txtar`:
```
# style backtick literal is a merge target: bag style merges over interpolated declarations
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Box(w int, attrs gsx.Attrs) {
	<div style=`width:@{w}px` { attrs... }>x</div>
}
-- invoke --
Box(BoxProps{W: 10, Attrs: gsx.Attrs{{Key: "style", Value: "color:red"}, {Key: "id", Value: "b"}}})
-- diagnostics.golden --
-- render.golden --
<div style="width:10px; color:red" id="b">x</div>
```
(Confirm the exact `; `-join and caller-last order against `fallthrough/` style goldens when you
run `-update`; adjust `render.golden` to the machinery's actual output — do not force a format the
merger doesn't produce.)

`internal/corpus/testdata/cases/textattr/class_no_spread.txtar`:
```
# class literal with NO spread: direct emit, no merge machinery engaged
-- input.gsx --
package p

component B(v string) { <span class=`badge-@{v}`>h</span> }
-- invoke --
B(BProps{V: "x"})
-- diagnostics.golden --
-- render.golden --
<span class="badge-x">h</span>
```

- [ ] **Step 2: Run to verify red (spread cases) / green (no-spread)**

Run: `go test ./internal/corpus -run 'TestCorpus/textattr/(class_spread_merge|style_spread_merge|class_no_spread)' 2>&1 | tail -30`
Expected: `class_no_spread` PASSES already (Task 4 path). The two `*_spread_merge` FAIL: the bag's
class/style is emitted as a **duplicate** attribute (proving the finder ignores EmbeddedText).

- [ ] **Step 3: Bind EmbeddedText class/style in the merge-site finder**

In `emitFallthroughAttrs` (`emit.go:562`), add bindings and populate them in the loop:
```go
	var embedClass *ast.EmbeddedAttr // class=`…`
	var embedStyle *ast.EmbeddedAttr // style=`…`
	…
		case *ast.EmbeddedAttr:
			if t.Lang == ast.EmbeddedText {
				switch t.Name {
				case "class":
					embedClass = t
				case "style":
					embedStyle = t
				}
			}
	…
```
Exclude them from `forcedNames` (add to the `continue` guards alongside `staticClass`/`classAttr`).

- [ ] **Step 4: Emit the merged class/style from the EmbeddedText literal**

At the class merge site (where `emitRootStaticClass` is called for `staticClass`), branch: if
`embedClass != nil`, call a new `emitRootEmbeddedClass(b, embedClass, bagClassExpr, …)` that emits
` class="` + interpolated segments + bag-class-append + `"`. Model it on `emitRootStaticClass`
(read `emit.go:1096`) but replace the single static token string with the segment walk from
`emitEmbeddedTextAttr` (extract that walk into a shared `writeEmbeddedTextSegments` helper to stay
DRY). Do the same for style via the style merge path. Ensure `emitSpread` drops `class`/`style`
from the bag spread when `embedClass`/`embedStyle` is the merge site (same as it does for
static/composed).

- [ ] **Step 5: Update goldens and verify no duplicate attribute**

Run: `go test ./internal/corpus -run 'TestCorpus/textattr/(class_spread_merge|style_spread_merge)' -update`
then verify: `go test ./internal/corpus -run 'TestCorpus/textattr'`
Expected: PASS; render shows a single merged `class`/`style` attribute, caller-last.

- [ ] **Step 6: Full fallthrough regression**

Run: `go test ./internal/corpus -run 'TestCorpus/fallthrough'`
Expected: PASS — existing static/composed merge behavior unchanged.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/textattr/*.txtar internal/corpus/testdata/cases/coverage.golden
git commit -m "feat(codegen): EmbeddedText class/style literals are first-class merge targets"
```

---

## Task 7: Fuzz — URL scheme-safety invariant (permanent regression guard)

**Files:**
- Test: `internal/codegen/url_fuzz_test.go` (create) — reuse the existing generate→build→render
  helper the codegen/corpus tests use (`grep -rln "func Fuzz\|compileAndRender\|renderToString\|batchCodegen" internal/codegen internal/corpus`); do NOT build a new pipeline.

**Interfaces:**
- Consumes: the compile+render helper from the corpus/codegen test harness.

**Invariant:** for any static-segment shape and any hole values, a rendered URL-context backtick
literal never yields a dangerous **browser-effective** scheme; dangerous inputs resolve to
`about:invalid#gsx`. (Whole-value `_gsxgw.URL` makes this provable; the fuzzer is a permanent
guard against regression, and its `effectiveScheme` must mimic the browser normalization that
defeated the earlier per-hole design.)

- [ ] **Step 1: Locate the compile+render test helper**

Run: `grep -rln "func Fuzz\|compileAndRender\|renderToString\|batchCodegen\|func render" internal/codegen internal/corpus`
Record a helper (or a thin wrapper you add in `internal/codegen` test scope) that takes `.gsx`
source + an invoke expression and returns the rendered HTML string (and whether codegen
succeeded). Reuse the corpus harness flow; do not reimplement generate/build/render.

- [ ] **Step 2: Write the fuzz target (multi-hole + static-scheme shapes)**

Create `internal/codegen/url_fuzz_test.go`:
```go
package codegen

import (
	"strings"
	"testing"
)

// FuzzURLLiteralSchemeSafety renders `<a href=`{s1}@{a}{s2}@{b}`>` for fuzzed
// static text s1,s2 and hole values a,b, and asserts the browser-effective scheme
// is never dangerous (whole-value _gsxgw.URL must have neutralized it).
func FuzzURLLiteralSchemeSafety(f *testing.F) {
	// seeds spanning every class the per-hole design failed on:
	f.Add("/u/", "7", "/edit", "")                 // safe path
	f.Add("", "https://ex.com", "/p", "")          // safe origin from hole
	f.Add("javascript:", "alert(1)", "", "")       // static dangerous scheme
	f.Add("data:text/html,", "<script>x</script>", "", "") // static data:
	f.Add("", "javascript", ":alert(1)", "")       // split across holes
	f.Add("java\tscript:", "alert(1)", "", "")     // control-byte obfuscation
	f.Add(" javascript:", "alert(1)", "", "")      // leading-space obfuscation
	f.Add("", "javascript:alert(1)", "", "")       // whole-value in a hole
	f.Add("//", "evil.com", "/p", "")              // protocol-relative
	f.Fuzz(func(t *testing.T, s1, a, s2, b string) {
		html, ok := tryRenderHref(t, s1, a, s2, b) // Step 1 helper wrapper
		if !ok {
			return // compile error is acceptable (never an XSS)
		}
		val := extractHref(html)
		if sch := effectiveScheme(val); isDangerousScheme(sch) {
			t.Fatalf("dangerous scheme %q from s1=%q a=%q s2=%q b=%q -> href=%q",
				sch, s1, a, s2, b, val)
		}
	})
}

// effectiveScheme mimics the WHATWG URL parser's pre-scheme normalization so the
// fuzzer catches the obfuscations that defeated per-hole classification:
// remove ALL ASCII tab/LF/CR, strip leading C0-control-or-space, lowercase, then
// take the run before the first ':' — but only if no '/','?','#' precedes it.
func effectiveScheme(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if c := v[i]; c != '\t' && c != '\n' && c != '\r' {
			b.WriteByte(c)
		}
	}
	s := b.String()
	for len(s) > 0 && s[0] <= ' ' {
		s = s[1:]
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			return strings.ToLower(s[:i])
		case '/', '?', '#':
			return "" // relative — no scheme
		}
	}
	return ""
}

func isDangerousScheme(scheme string) bool {
	switch scheme {
	case "", "http", "https", "mailto", "tel", "about": // about = blocked sentinel about:invalid#gsx
		return false
	default:
		return true // javascript, data, vbscript, file, blob, … => must never appear
	}
}
```
Implement `tryRenderHref(t, s1,a,s2,b)`: builds a component
`` component L(a string, b string) { <a href=`<s1>@{a}<s2>@{b}`>x</a> } `` (with s1/s2 spliced
as raw static text — they may contain tabs/spaces/colons), renders `L(LProps{A:a, B:b})`, and
returns `(html, compiledOK)`. `extractHref` pulls the `href="…"` value (entity-DECODED, since the
browser decodes before URL parsing — decode `&amp; &#34; &lt; &gt; &#39;`). Note: because the
value is entity-encoded in the attribute, decode it before computing the effective scheme.

- [ ] **Step 3: Run the fuzz target**

Run: `go test ./internal/codegen -run FuzzURLLiteralSchemeSafety -fuzz FuzzURLLiteralSchemeSafety -fuzztime 45s`
Expected: no failures. A crasher is a REAL XSS — do not weaken the assertion; the whole-value
`_gsxgw.URL` should make all dangerous inputs render `about:invalid#gsx`. If a crasher appears,
capture the seed and investigate `_gsxgw.URL`/`urlSanitize`.

- [ ] **Step 4: Seed-corpus run for CI**

Run: `go test ./internal/codegen -run FuzzURLLiteralSchemeSafety`
Expected: PASS (seeds execute without `-fuzz`; `make ci` runs this form).

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/url_fuzz_test.go
git commit -m "test(codegen): fuzz whole-value URL literal scheme safety"
```

---

## Task 8: LSP — go-to-definition & hover inside EmbeddedAttr holes

**Files:**
- Modify: `internal/lsp/definition.go` — `nodeNavSpans` (~31-75): add `*gsxast.EmbeddedAttr` case
  yielding each `*ast.Interp` segment's `{ExprPos, len(Expr)}` and its `Stages`
- Test: `internal/lsp/definition_matrix_test.go` — extend `matrixSrc` + `cases`

**Interfaces:**
- Consumes: existing `navSpan`/`PipeStage` nav plumbing.
- Produces: gd + hover on hole identifiers and pipeline stage names inside **all** embedded
  literals (text/js/css) — this also fixes the pre-existing gap where js/css holes had no nav.

- [ ] **Step 1: Add matrix fixture + failing cases**

In `definition_matrix_test.go`, add to `matrixSrc` a component with a text literal hole, e.g.:
```
component NavText(variant string) { <span class=`badge-@{variant}`>x</span> }
```
Add a `cases` entry anchoring on `variant` inside the hole with `declOff` pointing at the param
declaration (follow the existing entry format exactly — copy a `*ast.Interp`-in-body entry and
retarget offsets). Add a `TestHoverObjectMatrix` counterpart.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/lsp -run 'TestDefinitionMatrix|TestHoverObjectMatrix' -v 2>&1 | tail -30`
Expected: FAIL — the hole's `variant` does not resolve (no `EmbeddedAttr` nav case).

- [ ] **Step 3: Add the `EmbeddedAttr` case to `nodeNavSpans`**

In `definition.go` `nodeNavSpans`:
```go
	case *gsxast.EmbeddedAttr:
		var spans []navSpan
		var stages []gsxast.PipeStage
		for _, seg := range n.Segments {
			if in, ok := seg.(*gsxast.Interp); ok {
				spans = append(spans, navSpan{Pos: in.ExprPos, Len: len(in.Expr)})
				stages = append(stages, in.Stages...)
			}
		}
		return spans, stages
```
Confirm the AST walk (`exprNodeAtOffset` / `gsxast.Inspect`) already descends into
`EmbeddedAttr.Segments`; if `Inspect` doesn't visit `Interp` children of `EmbeddedAttr`, make
`nodeNavSpans` the single source of truth (it now enumerates them directly, so no Inspect change
needed).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/lsp -run 'TestDefinitionMatrix|TestHoverObjectMatrix' -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/definition.go internal/lsp/definition_matrix_test.go
git commit -m "feat(lsp): gd + hover inside embedded-attr holes (text/js/css)"
```

---

## Task 9: Formatter — print `EmbeddedText` + `\@{` re-escape, idempotent

**Files:**
- Modify: `internal/printer/printer.go` — `embeddedLangName` (~798), `writeEmbeddedAttrSegments`
  (~809), `writeEmbeddedLiteralText` (~837)
- Test: `internal/printer/printer_test.go` (add idempotence case) and a corpus `gsx fmt` case if
  the corpus harness formats inputs

**Interfaces:**
- Produces: `` name=`…@{ expr }…` `` printed for `EmbeddedText`; `@{` re-escaped to `\@{` in
  literal text; braced input `` {`…`} `` canonicalized to direct `` `…` `` (mirroring js/css).

- [ ] **Step 1: Write the failing idempotence test**

In `internal/printer/printer_test.go`, add a case: format
`` <span class=`badge-@{v} \@{lit}`>x</span> `` and assert the output re-parses+re-prints
identically (round-trip), and that `embeddedLangName(ast.EmbeddedText)` yields `""`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/printer -run TestEmbeddedText -v`
Expected: FAIL — `embeddedLangName` has no `EmbeddedText` arm (likely returns wrong prefix or
panics on the default), and `@{` is not re-escaped.

- [ ] **Step 3: Handle `EmbeddedText` in `embeddedLangName`**

```go
func embeddedLangName(l ast.EmbeddedLang) string {
	switch l {
	case ast.EmbeddedJS:
		return "js"
	case ast.EmbeddedCSS:
		return "css"
	default: // EmbeddedText — no prefix
		return ""
	}
}
```

- [ ] **Step 4: Re-escape `\@{` in `writeEmbeddedLiteralText`**

Extend the literal-text writer (which already re-escapes bare backticks) to also emit `\@{` when
the source text contains a literal `@{` (so a round-trip does not turn literal text into a hole).

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/printer -run TestEmbeddedText -v`
Expected: PASS.

- [ ] **Step 6: Formatter regression across corpus**

Run: `go test ./internal/printer ./internal/corpus 2>&1 | tail`
Expected: PASS (js/css literal printing unchanged; new text literals round-trip).

- [ ] **Step 7: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go
git commit -m "feat(printer): format EmbeddedText literals + re-escape \\@{"
```

---

## Task 10: Docs, playground, ROADMAP, full CI

**Files:**
- Create/Modify: `docs/guide/**` (attribute-syntax / interpolation page)
- `../gsxhq.github.io` playground examples (sibling repo — see spec)
- `docs/ROADMAP.md`

**Interfaces:** none (docs + housekeeping).

- [ ] **Step 1: Document the syntax**

Add a section to the relevant `docs/guide/` page: the `` name=`…@{expr}…` `` form, `@{ }` holes,
per-hole and whole-literal pipelines, `\@{` / `` \` `` escaping, URL-region behavior (pre/post
scheme + the seam error), and class/style merge. Wrap any literal `{{ }}` in prose in a
`::: v-pre` block. Cross-check against `docs/guide/syntax/javascript.html` phrasing.

- [ ] **Step 2: Add playground examples**

In `../gsxhq.github.io` playground content, add the runnable snippets listed in the spec:
(1) `` class=`btn btn-@{variant}` ``, (2) `` href=`/u/@{id}` ``, (3) class + spread-bag merge,
(4) before/after vs `fmt.Sprintf` (zero-alloc framing). Rebuild + cache-bust `gsx.wasm` and verify
each renders live (per the `gsx-docs-local-verify-gotchas` memory).

- [ ] **Step 3: Update ROADMAP**

Mark interpolating attribute-value literals as shipped in `docs/ROADMAP.md`; add follow-ups noted
during planning (numeric-hole `IntInto` zero-alloc optimization for `emitAttrValue`; sibling
tree-sitter/vscode grammar updates).

- [ ] **Step 4: Full CI gate**

Run: `make ci`
Expected: all lanes green (build/vet/test both modules, examples drift, gofmt + gsx fmt,
coverage.golden current).

- [ ] **Step 5: Commit**

```bash
git add docs/
git commit -m "docs: attribute interpolation literals + playground examples + ROADMAP"
```

---

## Follow-ups (out of scope for this plan)

- **Numeric-hole zero-alloc:** make `emitAttrValue` route `int/uint/float` through
  `IntInto`/`UintInto`/`FloatInto` (benefits `id={n}` *and* text-literal holes). Golden churn;
  do as its own change.
- **Sibling grammars:** `../tree-sitter-gsx` (backtick + `@{}` in attr position, no lang prefix),
  `../vscode-gsx`, `gsxhq.github.io` CodeMirror.

## Self-review notes

- **Spec coverage:** syntax (T2), `@{}`/escaping (T2–T3), type-aware zero-alloc holes (T4), URL
  region + seam + fuzz (T5, T7), class/style merge (T6), LSP (T8), formatter (T9), corpus per
  context (T4–T6), docs + playground (T10). All spec sections map to a task.
- **Type consistency:** `emitEmbeddedTextAttr`/`emitTextAttrInterp` signatures fixed in T4 and
  extended (isURL/region) in T5 — T5 explicitly updates the T4 signature.
- **Known deferral:** numeric holes format via `strconv` (same as `id={n}`) in v1; true
  zero-alloc for numbers is the flagged follow-up.
