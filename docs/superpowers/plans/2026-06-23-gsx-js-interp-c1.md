# gsx Slice C1 — JS context engine + `<script>` `@{ }` interpolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely inject Go data into `<script>` blocks via `@{ goExpr }`, escaped per the hole's JS context (value→JSON, string→JS-string-escape, template→template-escape, regex→regex-escape, comment→left literal, identifier/unknown→fail-closed), built on `tdewolff/parse/v2`'s lexer + an `html/template`-derived context state machine, with `gsx.RawJS` as the opt-out.

**Architecture:** Four units. (1) Runtime `gw.JSVal/JSStr/JSRegexp/JSTmpl` escapers — faithful ports of `html/template/js.go` — plus `gsx.RawJS`. (2) `ast.Interp` gains a `JSCtx` field; the parser splits `<script>` bodies on `@{` into `Text`+`Interp` (mirroring `<style>`). (3) `internal/jsx` — a post-parse pass `ResolveScripts(*ast.File) error` that reconstructs each `<script>`'s skeleton with placeholders, lexes it with tdewolff while running the JS context state machine, classifies each hole, **un-splits comment-context holes back to literal `Text`** (so they are never interpolated nor probed), sets `Interp.JSCtx` for the rest, and fails closed on identifier/unknown/lex-error. (4) Codegen emits the `JSCtx`-selected escaper for each `<script>` Interp. The differential-oracle test (vs `html/template`) is the security gate.

**Tech Stack:** Go; `tdewolff/parse/v2` (already a dep from Slice B). Runtime escapers are stdlib-only.

## Global Constraints

- **Escapers are PORTS of `$(go env GOROOT)/src/html/template/js.go`** (`jsValEscaper`, `jsStrEscaper`, `jsTmplLitEscaper`, `jsRegexpEscaper` + their helpers `indirectToJSONMarshaler`, the U+2028/2029 + `</script>`/`*/` defenses). Do NOT invent JS-safety logic. The differential-oracle test against `html/template` is the gate — outputs must match the stdlib.
- **Runtime is stdlib-only** (the `gsx` root package — escapers live there). `internal/jsx` is codegen-time (may import tdewolff).
- **Context taxonomy (exact):** value→`gw.JSVal` (JSON), string→`gw.JSStr`, template→`gw.JSTmpl`, regex→`gw.JSRegexp`, **comment→left literal (un-split, not interpolated)**, identifier/binding→fail-closed compile error, unknown (context not statically single-valued)→fail-closed.
- **`gsx.RawJS(s string)`** — author-vouched JS, emitted raw in a **value** position (in other positions it is treated as its string and escaped by that context).
- **CVE hazards designed in:** template-literal `${}` brace-depth tracked in the state machine; a hole whose context is not statically single-valued (e.g. inside gsx `{ if }` in a script — out of MVP scope) → fail-closed `unknown`.
- **MVP scope:** straight-line `<script>` bodies (no gsx `{ if }`/`{ for }` *inside* a `<script>`). The `@{ }` Go expression must be valid Go (it is type-probed like a `<style>` hole).
- After each task: `go build ./...` and `go test ./...` pass before committing.

---

### Task 1: Runtime JS escapers + `gsx.RawJS` + differential oracle

**Files:** Create `js.go` (root pkg: `gsx.RawJS` type + `gw.JSVal/JSStr/JSRegexp/JSTmpl` + the ported helpers); Test `js_diff_test.go` (differential vs `html/template`).

**Interfaces — Produces:**
- `type RawJS string`
- `func (gw *Writer) JSVal(v any)` — value context (JSON). `gsx.RawJS` → raw; everything else → `jsValEscaper`-equivalent.
- `func (gw *Writer) JSStr(s string)` — string-literal interior.
- `func (gw *Writer) JSTmpl(s string)` — template-literal text.
- `func (gw *Writer) JSRegexp(s string)` — regex literal.

- [ ] **Step 1: Read the stdlib source** `$(go env GOROOT)/src/html/template/js.go` and port the four escapers + helpers into `js.go`, adapting: drop the `html/template`-internal typed-string machinery except keep a `RawJS` passthrough (the analogue of `template.JS`). Expose them as the four `*Writer` methods above. `JSVal(v any)` JSON-marshals `v` (with the `</script>`/`*/`/U+2028/2029 defenses from the stdlib) unless `v` is `gsx.RawJS` (emit raw) or `fmt.Stringer`/`json.Marshaler` (per stdlib). Keep the failsafe behavior on marshal error (the stdlib's comment-safe error string).

- [ ] **Step 2: Write the differential oracle test** `js_diff_test.go` — for each context, compare gsx's escaper against `html/template` rendering in that JS sub-context, e.g.:

```go
package gsx

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func jsValOracle(s string) string { // html/template value context
	t := template.Must(template.New("v").Parse(`<script>var x={{.}}</script>`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := strings.TrimPrefix(b.String(), "<script>var x=")
	return strings.TrimSuffix(out, "</script>")
}
func jsStrOracle(s string) string { // string-interior context
	t := template.Must(template.New("s").Parse(`<script>var x="{{.}}"</script>`))
	var b bytes.Buffer
	_ = t.Execute(&b, s)
	out := strings.TrimPrefix(b.String(), `<script>var x="`)
	return strings.TrimSuffix(out, `"</script>`)
}

func gsxJSVal(v any) string { var b strings.Builder; W(&b).JSVal(v); return b.String() }
func gsxJSStr(s string) string { var b strings.Builder; W(&b).JSStr(s); return b.String() }

func TestJSValDiff(t *testing.T) {
	for _, s := range []string{
		"", "abc", `a"b`, "a'b", "</script>", "<!--", "a b", "a b",
		"a\\b", "a</script>b", "x&y", "a\x00b",
	} {
		if got, want := gsxJSVal(s), jsValOracle(s); got != want {
			t.Errorf("JSVal(%q)=%q, html/template oracle=%q", s, got, want)
		}
	}
}
func TestJSStrDiff(t *testing.T) {
	for _, s := range []string{"", "abc", `a"b`, "a'b", "</script>", "a\nb", "a b", "a\\b", "x&y"} {
		if got, want := gsxJSStr(s), jsStrOracle(s); got != want {
			t.Errorf("JSStr(%q)=%q, oracle=%q", s, got, want)
		}
	}
}
```
Add analogous `TestJSRegexpDiff` (oracle `<script>var x=/{{.}}/</script>`) and `TestJSTmplDiff` (oracle `` <script>var x=`{{.}}`</script> ``) once the corresponding `<script>` JS context is confirmed to route to `jsRegexpEscaper`/`jsTmplLitEscaper` in `html/template` (if a given oracle context does not exist in `html/template`, port the escaper and test it against the stdlib `js.go` test vectors instead, and note this in the report).

- [ ] **Step 3: Run red → port → green.** `go test . -run 'TestJS.*Diff'` must pass (byte-parity with `html/template`). `gw.JSVal(gsx.RawJS("x()"))` must emit `x()` raw. Commit: `runtime: gsx.RawJS + gw.JS{Val,Str,Tmpl,Regexp} (ports of html/template/js.go) + differential oracle`.

---

### Task 2: `ast.Interp.JSCtx` + parser splits `<script>` on `@{`

**Files:** Modify `ast/ast.go` (add `JSCtx` field + the enum); `parser/markup.go` (`parseRawTextBody`: add `script` to the interpolating set).

- [ ] **Step 1:** Add to `ast/ast.go`:
```go
// JSCtx is the JavaScript context an Interp inside a <script> was classified
// into (set by internal/jsx). 0 (JSCtxNone) for non-script interps.
type JSCtx uint8
const (
	JSCtxNone JSCtx = iota
	JSCtxValue
	JSCtxString
	JSCtxTemplate
	JSCtxRegexp
)
```
and a `JSCtx JSCtx` field on `type Interp struct`.

- [ ] **Step 2:** In `parser/markup.go` `parseRawTextBody`, change the interpolation gate from `strings.EqualFold(tag, "style")` to also include `script`:
```go
	interpolate := strings.EqualFold(tag, "style") || strings.EqualFold(tag, "script")
```
(The `@{` trigger + `parseInterp` reuse is identical to `<style>`. `<script>` now yields `Text`+`Interp` children.)

- [ ] **Step 3: Tests** (`parser/markup_test.go`): `<script>let x=@{ y }</script>` splits into `Text "let x="`, `Interp{y}`, `Text ""`; a bare `@` in `<script>` stays literal. Commit: `parser+ast: @{ } interpolation in <script>; Interp.JSCtx field`.

---

### Task 3: `internal/jsx` context engine (`ResolveScripts`)

**Files:** Create `internal/jsx/jsx.go` (+ a small `internal/jsx/state.go` for the JS context state machine ported from `html/template`'s `transition.go`/`context.go`/`js.go` `nextJSCtx`+`regexpPrecederKeywords`); Test `internal/jsx/jsx_test.go`.

**Interfaces — Produces:** `func ResolveScripts(f *ast.File) error` — walks the AST; for each `<script>` element with `Interp` children, classifies each hole and either sets `Interp.JSCtx`, un-splits comment holes to `Text`, or returns a positioned error.

- [ ] **Step 1: The classification.** For a `<script>`'s `[]ast.Markup` (Text+Interp): build a skeleton string = concat of Text verbatim + a unique placeholder identifier per Interp (`_GSXJSHOLE_<i>`, after verifying the source contains no such prefix — else fail closed). Lex the skeleton with `tdewolff/parse/v2/js` (driving `l.RegExp()` via the ported `regexPosition`/jsCtx logic — reuse the approach proven in `internal/jsmin`), running a state machine that tracks, at the placeholder's position, whether it is in: value/expression, a string token, a template token (with `${}` brace-depth), a regex token, a comment token, or an identifier/binding position. Map each placeholder → context.
- [ ] **Step 2: Apply.** For each Interp: value→`JSCtxValue`; string→`JSCtxString`; template→`JSCtxTemplate`; regex→`JSCtxRegexp`; **comment→replace the Interp in the children slice with a literal `Text{"@{ "+Expr+" }"}` (un-split — never interpolated/probed)**; identifier/binding→`fmt.Errorf("%s: @{ } in a JS identifier position is unsafe", pos)`; unknown/lex-error→positioned fail-closed error.
- [ ] **Step 3: Tests** — a table of `(script body → per-hole expected JSCtx or error or un-split)`: value `let x=@{a}`; string `"hi @{n}"`; template `` `t ${js} @{g}` `` (gsx hole in template text, JS `${}` untouched); regex `/@{p}/`; comment `// @{c}` → un-split literal; identifier `let @{x}=1` → error; plus the placeholder-collision fail-closed. Commit: `jsx: ResolveScripts — classify <script> @{ } holes by JS context (html/template state machine on tdewolff lexer)`.

---

### Task 4: Codegen emit per-context + wire `ResolveScripts` + e2e

**Files:** Modify `internal/codegen/emit.go` (emit `<script>` Interp by `JSCtx`); wire `jsx.ResolveScripts(file)` into the codegen pipeline **before** type resolution (so un-split comment holes aren't probed and JSCtx is set); add corpus cases.

- [ ] **Step 1: Wire `ResolveScripts`.** In the codegen entry (where files are parsed + before skeleton/probe — e.g. in `GeneratePackagesWithFilters`/`generateFile`'s pipeline, after `wsnorm.Normalize` and before resolution), call `jsx.ResolveScripts(file)`; a returned error fails that file's codegen with the message. (Mirror where `wsnorm.Normalize` is invoked.)
- [ ] **Step 2: Emit.** In the `<script>` child-emit path (parallel to slice-1's `<style>` `genStyleChild`), emit each Interp by `JSCtx`: `JSCtxValue`→`_gsxgw.JSVal(expr)` (or `RawJS` raw); `JSCtxString`→`_gsxgw.JSStr(string(expr))`; `JSCtxTemplate`→`_gsxgw.JSTmpl(string(expr))`; `JSCtxRegexp`→`_gsxgw.JSRegexp(string(expr))`. `Text` children → `emitS` (raw, minified). Numeric/Stringer values in value context flow through `JSVal(any)` (JSON).
- [ ] **Step 3: Corpus e2e + security cases** (`internal/corpus/testdata/cases/script/`): `interp_value.txtar` (`let cfg=@{ data }` → JSON), `interp_string.txtar` (`"/api/@{ id }"`), `interp_breakout.txtar` (a value containing `</script>` → neutralized in the rendered output), `interp_comment_literal.txtar` (`// @{ x }` → literal in output), and a fail-closed case asserting a diagnostic for `let @{x}=1`. Regenerate goldens; verify the breakout case's rendered output cannot close the script.
- [ ] **Step 4:** `go build ./... && go test ./...` green. Bump `codegen.Version()` `"3"`→`"4"`. Commit: `codegen: emit <script> @{ } interpolation by JS context (jsx engine); security corpus; bump version`.

---

## Self-Review

**Spec coverage (Component 1–3, `<script>` half):** escapers ported + oracle (Task 1) ✓; `@{ }` in `<script>` + `JSCtx` (Task 2) ✓; context engine with comment-un-split + fail-closed (Task 3) ✓; per-context emit + wiring + security corpus (Task 4) ✓. JS-context *attributes* are Slice C2; data-island is C3 — out of this plan.

**Placeholder scan:** the `_GSXJSHOLE_<i>` and corpus `PLACEHOLDER` goldens are the only literal placeholders, both by design (sentinel / `-update`-generated). Escaper bodies are ported from a cited stdlib source + gated by the differential oracle.

**Type/name consistency:** `RawJS`, `(*Writer).JSVal/JSStr/JSTmpl/JSRegexp`, `ast.Interp.JSCtx` + `JSCtx{None,Value,String,Template,Regexp}`, `jsx.ResolveScripts(*ast.File) error` — consistent across tasks.

## Risks

- **Escaper port fidelity** is the security crux — gated by the differential oracle (Task 1) against `html/template`.
- **Context classification on the token stream** (binding-vs-value, template `${}` brace-depth) is novel ground (the `html/template` CVE territory). Mitigation: lift `html/template`'s `nextJSCtx`/`regexpPrecederKeywords`; fail-closed on any unresolved/ambiguous context; the security corpus + a follow-on fuzz target.
- **Pipeline ordering** (`ResolveScripts` must run before probe/resolve so comment holes are un-split and not type-probed) — verify the insertion point.
