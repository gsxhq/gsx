# Body backtick literals + whole-literal pipe — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** (1) a backtick literal in body/child `{ }` interpolates (`<p>{`row-@{id}`}</p>`), form preserved; (2) a whole-literal pipeline `` `…` |> f `` assembles the interpolated string and pipes it as a unit, in body and braced-attribute position.

**Architecture:** New `ast.EmbeddedInterp` markup node (Segments + Stages) for the body form; a `Stages` field on `ast.EmbeddedAttr` for the braced-attr whole-literal pipe. Whole-literal codegen reuses `embeddedTextValueExpr` (assemble) + `lowerPipe` (pipe), then renders/escapes for context (Text / AttrValue / URL). The URL invariant holds because sanitize runs on the pipe's output.

**Spec:** `docs/superpowers/specs/2026-07-05-body-interp-and-whole-literal-pipe-design.md`

## Global Constraints
- Runtime stdlib-only. Run gsx as `go run ./cmd/gsx …`. Don't hand-edit generated `.x.go`/`*.golden` (regenerate via `-update`).
- `make check` (now includes `make lint`) is the inner gate; `make ci` authoritative. **Corpus protocol** (`.superpowers/sdd/corpus-protocol.md` if present, else: add empty `-- generated.x.go.golden --` to pin codegen; `-update` only your cases; `git checkout` coverage.golden after a filtered `-update`; controller settles coverage once at the end).
- Security: the whole-literal pipe's **final** value is always escaped/sanitized for context (Text/AttrValue/URL). URL context sanitizes the pipe *output* → a filter returning `javascript:…` is still blocked. Never emit an un-escaped pipe result.
- Prefer unexported identifiers.

---

## Task 1: AST — `EmbeddedInterp` node + `Stages` on `EmbeddedAttr` + Inspect

**Files:** Modify `ast/ast.go`.

**Interfaces produced:**
- `ast.EmbeddedInterp struct { span; Segments []Markup; Stages []PipeStage }` with `func (*EmbeddedInterp) markupNode() {}` — the body/child interpolating backtick literal (always plain-text lang).
- `EmbeddedAttr` gains `Stages []PipeStage` (whole-literal pipeline for the braced form).

- [ ] **Step 1:** Add the `Stages []PipeStage` field to `ast.EmbeddedAttr` (ast.go:333-339). Update its doc comment to mention the optional whole-literal `|> f`.
- [ ] **Step 2:** Add the new node after `EmbeddedAttr`:
```go
// EmbeddedInterp is an interpolating backtick literal used as a body/child
// expression: {`…@{expr}…`} or {`…` |> f}. Segments contain *Text and *Interp
// only; Stages is the optional whole-literal pipeline applied to the assembled
// string. Always plain-text (HTML-text-escaped) — no js/css lang in body.
type EmbeddedInterp struct {
	span
	Segments []Markup
	Stages   []PipeStage
}

func (*EmbeddedInterp) markupNode() {}
```
- [ ] **Step 3:** Add an `Inspect` case (ast.go, near the `*EmbeddedAttr` case ~578):
```go
	case *EmbeddedInterp:
		for _, m := range n.Segments {
			Inspect(m, f)
		}
```
- [ ] **Step 4:** `go build ./...` (nothing consumes them yet; pure additive). Commit: `feat(ast): EmbeddedInterp body node + whole-literal Stages on EmbeddedAttr`.

---

## Task 2: Parser — body lone-backtick literal + trailing `|>` (body & braced attr)

**Files:** Modify `parser/markup.go` (`parseBraceNode`), `parser/attrs.go` (`parseBracedEmbeddedAttrValue`). Test: `parser/embedded_text_test.go`.

**Interfaces consumed:** `parseEmbeddedAttrLiteral` (attrs.go:343 — returns `(lang, segments, err)`), `parseEmbeddedSegments`, `parsePipe`/`splitPipe` (pipe.go), `ast.EmbeddedInterp`, `EmbeddedAttr.Stages`.

- [ ] **Step 1 (RED): body test.** In `parser/embedded_text_test.go` add:
```go
func TestParseBodyEmbeddedInterp(t *testing.T) {
	src := "package p\ncomponent C(id string, n int) { <p>{`row-@{id}-@{n}`}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil { t.Fatalf("parse: %v", err) }
	ei := firstEmbeddedInterp(t, f) // add helper: walk via ast.Inspect for *ast.EmbeddedInterp
	// segments: Text("row-"), Interp(id), Text("-"), Interp(n)
	if len(ei.Segments) != 4 { t.Fatalf("segments=%d want 4: %#v", len(ei.Segments), ei.Segments) }
	if len(ei.Stages) != 0 { t.Fatalf("stages=%d want 0", len(ei.Stages)) }
}
func TestParseBodyEmbeddedInterpPipe(t *testing.T) {
	src := "package p\ncomponent C(id string) { <p>{`row-@{id}` |> upper}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil { t.Fatalf("parse: %v", err) }
	ei := firstEmbeddedInterp(t, f)
	if len(ei.Stages) != 1 || ei.Stages[0].Name != "upper" {
		t.Fatalf("stages=%v want [upper]", ei.Stages)
	}
}
func TestBodyBacktickSubExpressionStaysGo(t *testing.T) {
	// a backtick that is NOT the whole { } value stays a Go raw string.
	src := "package p\ncomponent C(x string) { <p>{`a` + x}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil { t.Fatalf("parse: %v", err) }
	// must NOT be an EmbeddedInterp — it's an ordinary Interp with Expr "`a` + x"
	if hasEmbeddedInterp(f) { t.Fatalf("`a` + x must stay a Go expression, not EmbeddedInterp") }
}
```
Add `firstEmbeddedInterp`/`hasEmbeddedInterp` helpers (walk `ast.Inspect`).
Run: `go test ./parser -run 'TestParseBodyEmbeddedInterp|TestBodyBacktickSub' -v` → FAIL.

- [ ] **Step 2 (body dispatch).** In `parseBraceNode` (markup.go:497-519), before the final `parseInterp` fallthrough, add a lone-backtick branch. Detection must confirm the *entire* `{ }` value is a backtick literal (optionally `|> pipeline`), else fall through to `parseInterp` so `{ `a` + b }` stays a Go expression:
```go
	if in, ok, err := p.tryParseBodyEmbeddedInterp(); err != nil {
		return nil, false, err
	} else if ok {
		return in, false, nil
	}
	in, err := p.parseInterp()
	return in, false, err
```
Implement `tryParseBodyEmbeddedInterp`:
  - Save `start := p.i` (at `{`). Advance past `{`, skip spaces/tabs/newlines.
  - If not `p.at("`")` (bare backtick; NOT `js`/`css`` — those aren't valid in body): restore `p.i = start`, return `(nil,false,nil)`.
  - Parse the literal: `lang, segs, err := p.parseEmbeddedAttrLiteral()` (it handles the bare-backtick → EmbeddedText case and consumes through the closing backtick). If `lang != ast.EmbeddedText` treat as not-ours (restore + return false).
  - Skip spaces. Now parse an optional whole-literal pipeline + the closing `}`: read the remaining source up to the matching `}` (use `goExprEnd`-style or scan to `}`), take that slice, and if it begins with `|>` run it through `parsePipe("" seedless…)`. SIMPLER: after the closing backtick, if the next non-space chars are `}` → no stages; if `|>` → capture the substring from here to the matching `}` and `splitPipe`/`parsePipeStage` each stage (mirror how `parseInterp`→`parsePipe` handles stages, but with an empty seed). If neither `}` nor `|>` → this isn't a lone literal (e.g. `` `a` + b ``): restore `p.i = start`, return `(nil,false,nil)` so `parseInterp` handles it.
  - On success, consume the `}`, build `&ast.EmbeddedInterp{Segments: segs, Stages: stages}`, `ast.SetSpan`, return `(node, true, nil)`.
  Keep this helper tight; the rewind on the two "not ours" exits is essential.

- [ ] **Step 3 (braced attr trailing `|>`).** In `parseBracedEmbeddedAttrValue` (attrs.go:318-330), between `parseEmbeddedAttrLiteral` and the `}` check: skip spaces; if `p.at("|>")` (an `|` immediately followed by `>`), capture the substring to the matching `}` and parse stages the same way; set `ea.Stages`. Then require `}`. (Direct `attr=`…`` form is intentionally NOT extended — braced-only per spec.)

- [ ] **Step 4:** Run the body tests → GREEN. Add a braced-attr `|>` parser test similarly. Regression: `go test ./parser` PASS.
- [ ] **Step 5:** Commit: `feat(parser): body backtick EmbeddedInterp + whole-literal |> (body & braced attr)`.

**Note for implementer:** the stage-parsing-with-empty-seed detail — read `parsePipe` (pipe.go:209) and `parsePipeStage`. You want the stages *after* the literal, with the literal as the (implicit) seed. Factor a small helper `parseTrailingStages(srcSlice, basePos) ([]ast.PipeStage, error)` used by both body and braced-attr paths. If this proves ambiguous, STOP and report BLOCKED with specifics rather than guessing the pipe-position math.

---

## Task 3: Analyze — resolve whole-literal pipeline result type

**Files:** Modify `internal/codegen/analyze.go` (the type-probe pass). Test: covered via corpus in Task 7, but a focused analyze check is fine.

**Why:** For a whole-literal pipe, codegen must know the piped result's type to render it (string→Text, int→IntInto, …). The analyze pass already probes per-hole `Interp.Stages`; it must now also probe the node-level `Stages` on `EmbeddedInterp` and `EmbeddedAttr`, using the **assembled string** as the pipeline seed (`lowerPipe(<assembled>, node.Stages)`), and record the result type in `resolved[node]`.

- [ ] **Step 1:** READ `analyze.go` — find how `*ast.Interp` with `Stages` is probed (it builds `lowerPipe(expr, stages)` and records the result type keyed by the node) and how it walks the AST (likely `ast.Inspect` or a dedicated walker). Note the exact probe/record mechanism.
- [ ] **Step 2:** For `*ast.EmbeddedInterp` and `*ast.EmbeddedAttr` **when `len(Stages) > 0`**: build the assembled seed the same way codegen will (the analyze pass has its own probe-expression builder — assemble `"static" + string(hole) + …`; the per-hole types come from the already-probed hole Interps, or reuse the same `holeStringExpr`-equivalent the probe uses for string context). Then probe `lowerPipe(assembledSeed, node.Stages)` and record the result type in `resolved[node]`. Mirror the existing Interp+Stages probe exactly (same lowerPipe call so emit≡probe order invariant holds).
- [ ] **Step 3:** When `len(Stages) == 0`, no node-level type is needed (segments render per-hole; each hole already probed). Ensure the walker still descends into Segments to probe the holes (Task-1 Inspect case covers EmbeddedInterp; EmbeddedAttr holes were already probed in PR #33 — confirm).
- [ ] **Step 4:** Build. Commit: `feat(codegen): probe whole-literal pipeline result type`.

**If the analyze pass's seed-assembly is hard to mirror, STOP and report BLOCKED** — the emit≡probe order invariant is load-bearing; do not approximate.

---

## Task 4: Codegen — body `EmbeddedInterp` + attribute whole-literal pipe

**Files:** Modify `internal/codegen/emit.go`. Corpus in Task 7.

**Interfaces consumed:** `embeddedTextValueExpr` (emit.go:2205), `lowerPipe`+`emitPipeWrap`, `emitRender` (emit.go:1700), `genInterp` (1508), `emitEmbeddedTextAttr` (2162), the body markup dispatcher (`genNode`/`genMarkup` — find where body `*ast.Interp` is emitted).

- [ ] **Step 1 (body node emit).** Add `emitEmbeddedInterp(b, n *ast.EmbeddedInterp, …)`:
  - **No stages:** walk `n.Segments` — `*ast.Text` → emit as body static text (reuse the SAME escaping the body emits static `*ast.Text` with — find it; body text is HTML-escaped then written via `S(...)`), `*ast.Interp` → `genInterp(...)` (Text-context render, zero-alloc ints). This preserves the form (the node stays in the AST for the printer) while emitting zero-alloc per-segment output.
  - **With stages:** `concat, ok := embeddedTextValueExpr(...)`; `lowered, usedPkgs, err := lowerPipe(concat, n.Stages, table, emitPipeWrap(b, interpTemp))`; add usedPkgs to imports; `t := resolved[n]`; `emitRender(b, lowered, t, imports, n, bag)` (handles (T,error) unwrap? emitRender doesn't — but the pipe result type is already resolved; if a stage returns (T,error) that's handled inside lowerPipe's wrap). 
  Wire `emitEmbeddedInterp` into the body markup dispatcher (where `*ast.Interp` is handled).
- [ ] **Step 2 (attr whole-literal pipe).** In `emitEmbeddedTextAttr` (emit.go:2162), when `len(a.Stages) > 0`:
  - assemble `concat, ok := embeddedTextValueExpr(...)`; `lowered := lowerPipe(concat, a.Stages, …)`.
  - **URL context:** `_gsxgw.URL(string(lowered))` — sanitize the piped result (safe: pipe output is sanitized). *(The pipe result type is string in the common case; if `resolved[a]` says non-string, convert via the emitRender/holeStringExpr string dispatch first, then URL. Keep it: URL needs a string arg.)*
  - **Non-URL:** render the piped result for attr context — `emitRender`-analogue for attr (string→`AttrValue(string(x))`, etc.); reuse `emitAttrValue` with the lowered expr + `resolved[a]`.
  Keep the existing no-stages URL and per-segment branches unchanged.
- [ ] **Step 3:** (corpus verification happens in Task 7). Build + `go vet`. Commit: `feat(codegen): body EmbeddedInterp + whole-literal pipe (body & attr, URL-safe)`.

---

## Task 5: Printer — EmbeddedInterp + trailing `|>`

**Files:** Modify `internal/printer/printer.go`. Test: `internal/printer/printer_test.go`.

- [ ] **Step 1 (RED):** idempotence test: format `` <p>{`row-@{id}-@{n}`}</p> `` and `` <p>{`row-@{id}` |> upper}</p> `` and assert round-trip stable (reuse `checkFormat`). Run → FAIL (no EmbeddedInterp print case; likely panics/wrong).
- [ ] **Step 2:** Add a markup dispatch `case *ast.EmbeddedInterp:` (printer.go:455-461 area) that emits `` {` `` + `writeEmbeddedAttrSegments(sb, v.Segments)` + `` ` `` + ` |> stage…` for `v.Stages` + `}`. Reuse `writeEmbeddedAttrSegments` (printer.go:809) and `pipeStageStr`. (Use the `strings.Builder` embedded-literal helpers; adapt to the `pretty.Doc` dispatch — see how the `*ast.Interp` case at printer.go:473 returns a `pretty.Doc`; produce an equivalent `pretty.Text(...)` doc for the whole literal.)
- [ ] **Step 3:** Extend the `*ast.EmbeddedAttr` attr print case (printer.go:769-775) to append ` |> stage…` when `v.Stages` is non-empty (after the closing backtick).
- [ ] **Step 4:** Run printer tests → GREEN; `go test ./internal/printer ./internal/corpus` regression PASS. Commit: `feat(printer): print EmbeddedInterp + whole-literal pipeline`.

---

## Task 6: LSP — gd/hover in EmbeddedInterp holes

**Files:** Test: `internal/lsp/definition_matrix_test.go`. (Inspect case added in Task 1 should make it work.)

- [ ] **Step 1:** Add a `matrixSrc` component with a body EmbeddedInterp hole (`{`badge-@{variant}`}`), and a `cases` entry anchoring `variant` inside the hole → param decl. Run `go test ./internal/lsp -run TestDefinitionMatrix -v` → likely already GREEN (Inspect descends into Segments via Task 1). If RED, follow PR #33's two-bridge recipe.
- [ ] **Step 2:** Confirm `go test ./internal/lsp` PASS. Commit: `test(lsp): gd/hover inside body EmbeddedInterp holes`.

---

## Task 7: Corpus + docs + fuzzer seed + full check

**Files:** `internal/corpus/testdata/cases/**`, `docs/guide/syntax/**`, `docs/ROADMAP.md`, `internal/codegen/url_fuzz_test.go`, example fixtures.

- [ ] **Step 1: corpus cases** (per protocol, pin `generated.x.go.golden`):
  - `bodyinterp/plain` — `<p>{`row-@{id}-@{n}`}</p>` → renders `row-<id>-<n>`; generated shows per-segment `S`/`Text`/`IntInto` (no materialize).
  - `bodyinterp/whole_pipe` — `<p>{`item-@{id}` |> upper}</p>` → assembled + `_gsxstd.Upper(...)` + `Text`.
  - `bodyinterp/sub_expression` — `<p>{`a` + x}</p>` stays a Go raw string (renders `a<x>`), NOT interpolated.
  - `bodyinterp/escaped_hole` — `\@{` literal.
  - `attrs/whole_pipe_braced` — `class={`btn-@{v}` |> upper}` → assembled + upper + AttrValue.
  - `attrs/whole_pipe_url` (SECURITY) — `href={`@{u}` |> someStrFilter}` where the filter could return a dangerous scheme → still `_gsxgw.URL(...)` on the piped result → dangerous input renders `about:invalid#gsx`. (Use `std.Upper` or `std.Default` as the filter; craft an input that would be dangerous if unsanitized.)
- [ ] **Step 2: fuzzer seed** — add a whole-literal-piped `href` seed to `FuzzURLLiteralSchemeSafety` (a filter applied to a hostile URL still yields a safe effective scheme).
- [ ] **Step 3: docs** — add to `docs/guide/syntax/interpolation.md` (body backtick form) and `pipelines.md` (whole-literal pipe), and a note in `attributes.md`. Add example fixture(s) `examples/3xx-*.txtar` routed to a syntax page (feeds docs+playground); `make examples`.
- [ ] **Step 4: ROADMAP** — mark body-interp + whole-literal pipe shipped; note the direct-attr whole-pipe deferral.
- [ ] **Step 5:** controller settles `coverage.golden` (full `-update`, verify only it changed). Run **`make check`** (build/vet/test both modules + examples drift + gofmt + gsx fmt + **lint**) → must pass. Commit: `docs+test: body interp + whole-literal pipe corpus, fuzzer seed, docs, ROADMAP`.

---

## Self-review notes
- Spec coverage: body interp (T2/T4/T5), whole-literal pipe body+attr (T2/T3/T4/T5), URL-safe pipe (T4/T7 security case + fuzzer), LSP (T6), formatter preserve (T5), docs/playground (T7). All map.
- Type consistency: `EmbeddedInterp{Segments,Stages}` and `EmbeddedAttr.Stages` defined T1, consumed T2-T5.
- Load-bearing invariants flagged for BLOCKED-not-guess: parser lone-backtick rewind (T2), analyze emit≡probe seed assembly (T3), URL sanitize-after-pipe (T4).
