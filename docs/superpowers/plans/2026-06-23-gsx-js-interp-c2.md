# gsx Slice C2 — JS-context attributes (`x-data` and friends) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Safely inject Go data into JS-context HTML attributes — the `fmt.Sprintf` killer. Two forms: **2a literal-with-holes** `<div x-data="{ activeTab: @{ tab }, open: false }">` → `x-data="{ activeTab: &#34;opp&#34;, open: false }"`; **2b whole-value** `<div x-data={ stateStruct }>` → JSON-encoded, and `<div @click={ gsx.RawJS("toggle()") }>` → vouched JS. Plus expand the `ctxJS` attribute set to Alpine/HTMX directives.

**Architecture:** Reuse Slice C1's `internal/jsx` classifier and `gw.JS*` escapers. New: (1) runtime `gw.JS*Attr` methods = the JS escaper composed with HTML-attribute-escaping (JS-escape FIRST so the post-HTML-decode JS is safe, then HTML-attr-escape so it survives the attribute); (2) a shared `attrjs.IsJSAttr(name)` predicate (parser + codegen both need it); (3) a parser extension that splits a JS-context attribute's quoted-string value on `@{` into a new `ast.JSAttr{Name, Segments []Markup}`; (4) `jsx.ResolveJSAttr` reusing the C1 `classify` engine on the attribute-value JS fragment; (5) codegen unlock of the `ctxJS` path (currently fail-closed) for both forms.

**Tech Stack:** Go; `tdewolff/parse/v2` (codegen-time, already a dep). Runtime escapers stdlib-only.

## Global Constraints

- **Runtime (`gsx` root pkg) is stdlib-only — SACRED.** The new `gw.JS*Attr` methods live there; no third-party import.
- **Double-escaping order is mandatory and is the security crux:** for any JS value in an HTML attribute, JS-escape FIRST (produce safe JS), THEN HTML-attribute-escape (so `"`→`&#34;` etc. survive the double-quoted attribute). The browser HTML-decodes the attribute, then the JS framework evaluates the decoded JS — so the JS must be safe AFTER HTML-decode. HTML-attr-escaping is a per-character replacement, so it composes correctly applied to each segment's JS output independently.
- **`ctxJS` classification stays NAME-based** (gsx's compile-time alternative to a tokenizer), matching the existing `attrContext` in `internal/codegen/emit.go:911`. The expanded set: existing `@*` / `hx-on*` / `on[a-z]*`, plus Alpine `x-data`, `x-init`, `x-show`, `x-if`, `x-effect`, `x-on:*`, `:*` (the `x-bind:`/`:` shorthand), and HTMX `hx-on:*` (already covered by `hx-on`).
- **Fail-closed parity with C1:** a hole in a JS-attribute value is classified by the SAME `jsx` engine (same value-position allow-list, same errors, same `</script>` guard is N/A here but the identifier/binding fail-closed applies). `?` try-marker and pipeline stages in a JS attribute are rejected with a clear error (parity with `<style>`/`<script>`).
- **`:*` shorthand caution:** `:class` is Alpine class-binding (JS), but gsx already has a `class` attribute concept. Only treat `:`-prefixed names (e.g. `:class`, `:style`, `:value`) as `ctxJS`; the bare `class`/`style` keep their existing CSS/class handling. Verify no collision with the existing `ClassAttr`/`style` paths.
- After each task: `go build ./...` and `go test ./...` pass before committing.

---

### Task 1: Runtime `gw.JS*Attr` escapers (JS-then-HTML-attr)

**Files:** Modify `js.go` (add the four `*Attr` methods); Test `js_diff_test.go` (extend).

**Interfaces — Produces:**
- `func (gw *Writer) JSValAttr(v any)` — form 2b + form-2a value holes: `htmlAttrEscape(jsValEscaper(v))`.
- `func (gw *Writer) JSStrAttr(s string)` — form-2a string holes.
- `func (gw *Writer) JSTmplAttr(s string)` — form-2a template holes.
- `func (gw *Writer) JSRegexpAttr(s string)` — form-2a regex holes.

- [ ] **Step 1:** Each method computes its JS-escaped string (the SAME logic as the existing `JSVal`/`JSStr`/`JSTmpl`/`JSRegexp` from C1 — factor the core escaper into a string-returning helper if the current methods write directly to `gw`, e.g. `jsValString(v) string`, so both the bare and `*Attr` variants share it; do NOT duplicate the escaper body), then writes `htmlAttrEscape`-equivalent of that string. There is already an HTML-attribute escaper used by the writer for `AttrValue` (find it — `writeHTML`/the attr entity set in `escape.go`; `gw.AttrValue` at `writer.go:42` calls `writeHTML`). Reuse the SAME entity set so `"`→`&#34;`, `&`→`&amp;`, `<`→`&lt;`, `>`→`&gt;`, `'`→`&#39;`. Thread `gw.err` like the other methods.

- [ ] **Step 2: Differential/parity tests** in `js_diff_test.go`: assert `gw.JSValAttr(s)` equals `htmlAttrEscape(jsValOracle(s))` for the C1 corpus (compose the existing `jsValOracle` with the project's HTML-attr escaper). Critically test breakout vectors: `JSValAttr("\"><script>")` must contain NO literal `"` or `<` (both JS-escaped to `"`/`<` by JSON, then any survivors HTML-escaped) — verify the result cannot break out of EITHER the JS string or the HTML attribute. Also `JSStrAttr("a\"b")` and a value with `</script>`.

- [ ] **Step 3:** `go test .` green; `gw.JSValAttr(gsx.RawJS("x()"))` emits `x()` (RawJS passthrough preserved through the attr path — note: raw JS in an attribute is still HTML-attr-escaped so it survives the attribute, but NOT JS-escaped). Commit: `runtime: gw.JS{Val,Str,Tmpl,Regexp}Attr — JS escaper composed with HTML-attribute escaping for JS-context attributes`.

---

### Task 2: Shared `attrjs.IsJSAttr` predicate + `ctxJS` set expansion

**Files:** Create `internal/attrjs/attrjs.go` (`func IsJSAttr(name string) bool`); Modify `internal/codegen/emit.go` (`attrContext` delegates the `ctxJS` arm to `attrjs.IsJSAttr`). Test `internal/attrjs/attrjs_test.go`.

**Why a shared package:** the PARSER (Task 3) must know which attributes are JS-context to decide whether to split a quoted value on `@{`, and the parser cannot import `internal/codegen`. Extract the name predicate so both use one source of truth.

- [ ] **Step 1:** `internal/attrjs/attrjs.go`:
```go
// Package attrjs identifies HTML attributes whose value is JavaScript, so both
// the parser (which splits @{ } holes in their quoted values) and codegen (which
// escapes them as JS) agree on one set.
package attrjs

import "strings"

// IsJSAttr reports whether an attribute name carries a JavaScript value:
// inline event handlers (onclick…), Alpine directives, and HTMX hx-on.
func IsJSAttr(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "@"): // Alpine @click shorthand for x-on:
		return true
	case strings.HasPrefix(n, "hx-on"): // HTMX hx-on:*
		return true
	case strings.HasPrefix(n, "on") && len(n) > 2 && n[2] >= 'a' && n[2] <= 'z': // onclick…
		return true
	case n == "x-data" || n == "x-init" || n == "x-show" || n == "x-if" || n == "x-effect":
		return true
	case strings.HasPrefix(n, "x-on:"): // Alpine x-on:click
		return true
	case strings.HasPrefix(n, ":") && n != ":": // Alpine :class / x-bind shorthand
		return true
	default:
		return false
	}
}
```
- [ ] **Step 2:** In `internal/codegen/emit.go` `attrContext` (line 911), replace the inline `ctxJS` condition (the `strings.HasPrefix(n, "@") || …` arm) with `case attrjs.IsJSAttr(name): return ctxJS`. Keep `ctxURL`/`ctxCSS` arms; ensure `ctxURL` (e.g. `hx-get`) and `style`/`class` still resolve to their own contexts and are NOT shadowed — order the `switch` so URL/CSS win where they previously did. (Note `hx-on` is JS but `hx-get` is URL; `IsJSAttr` matches `hx-on` only, so order is safe — but add a test.)
- [ ] **Step 3: Tests** (`attrjs_test.go`): table asserting `IsJSAttr` true for `onclick`,`@click`,`x-data`,`x-on:click`,`:class`,`hx-on:click`,`x-init`; false for `class`,`style`,`href`,`hx-get`,`title`,`data-x`,`:` (bare),`one` (the `on`+non-letter guard). Commit: `attrjs: shared JS-context attribute predicate; codegen attrContext delegates to it; expand set to Alpine/HTMX`.

---

### Task 3: Parser — `@{ }` in JS-context attribute string values → `ast.JSAttr`

**Files:** Modify `ast/ast.go` (add `JSAttr`); `parser/markup.go` (attribute-value parsing). Test `parser/markup_test.go`.

**Interfaces — Produces:** `type JSAttr struct { span; Name string; Segments []Markup }` (Text + Interp, like a `<script>` body) with `func (*JSAttr) attrNode() {}`.

- [ ] **Step 1:** Add `JSAttr` to `ast/ast.go` (near the other attr types ~line 246), documented as `name="… @{ } …"` for a JS-context attribute.
- [ ] **Step 2:** In `parser/markup.go`, find where a quoted (`"`/`'`) attribute value is consumed into a `StaticAttr`. When the attribute NAME satisfies `attrjs.IsJSAttr(name)` AND the quoted value contains `@{`, parse the value into `Segments []Markup` by scanning for `@{` and reusing `parseInterp` (mirror `parseRawTextBody`'s split loop, but bounded by the closing quote instead of `</tag>`), producing a `*ast.JSAttr`. If the value has no `@{`, keep the existing `StaticAttr` (no behavior change). Regular (non-JS) attributes keep `StaticAttr` even if they contain `@{` (so `title="a@{b}"` stays literal). Import `internal/attrjs` in the parser.
- [ ] **Step 3: Tests:** `<div x-data="{ a: @{ x } }">` → `*ast.JSAttr` with `Text "{ a: "`, `Interp{x}`, `Text " }"`; `<div title="a@{b}">` → `*ast.StaticAttr` (unchanged, JS predicate false); `<div onclick="alert(@{ n })">` → `JSAttr`. Commit: `parser+ast: split @{ } holes in JS-context attribute string values into ast.JSAttr`.

---

### Task 4: jsx engine for attribute values + codegen emit (both forms) + `ctxJS` unlock

**Files:** Modify `internal/jsx/jsx.go` (add `ResolveJSAttr`); `internal/codegen/emit.go` (`emitExprAttr` ctxJS unlock = form 2b; new `emitJSAttr` = form 2a; call `ResolveJSAttr` in the pipeline or resolve at emit). Modify `internal/codegen/codegen.go`/`batch.go` if attribute holes need pre-resolution. Tests via corpus.

- [ ] **Step 1 — jsx attr resolver:** add `func ResolveJSAttr(name string, segments []ast.Markup) error` to `internal/jsx`: build the skeleton from the segments (same `_GSXJSHOLE_` scheme as `resolveScript`), run the SAME `classify`, and set each `Interp.JSCtx` (a comment context in an attribute value is degenerate — treat a comment-classified hole as fail-closed here, since an attribute value is a single JS expression, not a program with comments; OR allow it the same as scripts — choose fail-closed for attributes and document it). Reuse `classify`/`classifyHole`/`isValuePosition` unchanged. Have `ResolveScripts`'s walk ALSO visit element attributes: for each `*ast.JSAttr`, call `ResolveJSAttr`. (So the single `ResolveScripts` entry point — already wired into the pipeline in C1 — covers attributes too. Rename is optional; keep `ResolveScripts` as the entry and have it handle both, or add the attr walk inside `resolveMarkup`'s `*ast.Element` case before recursing.)
- [ ] **Step 2 — form 2b (whole-value `x-data={ expr }`):** in `emitExprAttr` (emit.go:928), replace the `ctxJS` fail-closed `return` with emission: a `gsx.RawJS`-typed value → emit the raw JS HTML-attr-escaped (`gw.JSValAttr` handles RawJS passthrough from Task 1); any other type → `gw.JSValAttr(expr)` (JSON-encode + HTML-attr-escape). Keep the `bool`→`BoolAttr` and `ctxURL` arms intact; only the `ctxJS` arm changes. Wrap with the ` name="` / `"` emit like the existing path (emit.go:968/979).
- [ ] **Step 3 — form 2a (`emitJSAttr` for `*ast.JSAttr`):** add a case in `emitAttr` (emit.go:773) for `*ast.JSAttr`. Emit ` name="`, then for each segment: `*ast.Text` → `emitS` HTML-attr-escaped literal (the static JS text must be HTML-attr-escaped too — use the static `htmlAttrEscape` at emit.go:888 wrapped in `gw.S`, OR `gw.AttrValue`); `*ast.Interp` → by `JSCtx`, emit `gw.JSValAttr`/`JSStrAttr`/`JSTmplAttr`/`JSRegexpAttr` (mirror C1's `emitJSValue`, but the `Attr` variants). Then emit the closing `"`. Reject `Try`/`Stages` per-interp with a clear error.
- [ ] **Step 4 — corpus** (`internal/corpus/testdata/cases/jsattr/`): `xdata_literal_holes.txtar` (form 2a: `x-data="{ tab: @{ tab } }"` → render shows `&#34;`-escaped value inside the attribute), `xdata_whole_value.txtar` (form 2b: `x-data={ state }` struct → JSON, HTML-attr-escaped), `click_rawjs.txtar` (`@click={ gsx.RawJS("toggle()") }` → `toggle()` raw, attr-escaped), `onclick_breakout.txtar` (SECURITY: a hole value containing `">` and `</script>` and `"` — assert the rendered attribute cannot break out: no unescaped `"` or `>` from data), and `jsattr_identifier_rejected.txtar` (fail-closed: `x-on:click="@{ stmt } = 1"` binding position → diagnostic). Bump `codegen.Version()` `"4"`→`"5"`.
- [ ] **Step 5:** `go build ./... && go test ./...` green; runtime tdewolff-free (`go list -deps github.com/gsxhq/gsx | grep -c tdewolff` → 0). Commit: `codegen: unlock JS-context attributes — form 2a (literal @{ } holes) + form 2b (whole-value JSON/RawJS); security corpus; bump version`.

---

## Self-Review

**Spec coverage (Component 3 part 2 + ctxJS set):** runtime double-escapers (T1) ✓; shared predicate + set expansion (T2) ✓; parser 2a split (T3) ✓; jsx attr resolve + emit both forms + unlock (T4) ✓. `<script>` (C1) done; data-island is C3.

**Placeholder scan:** `_GSXJSHOLE_` (reused C1 sentinel, guarded) and corpus goldens are the only literals, both by design.

**Type/name consistency:** `attrjs.IsJSAttr`, `ast.JSAttr{Name, Segments}`, `gw.JS{Val,Str,Tmpl,Regexp}Attr`, `jsx.ResolveJSAttr`, `attrContext`→`ctxJS` — consistent across tasks.

## Risks
- **Double-escape order** is the security crux (T1) — gated by the breakout parity tests + the `onclick_breakout` corpus.
- **`:` shorthand / `class`/`style` collision** (T2) — the `:`-prefix-only rule + explicit false-cases test guard it; verify the existing `ClassAttr`/style paths are untouched.
- **Parser quoted-value split** (T3) must not change non-JS attributes — explicit `title="a@{b}"`-stays-`StaticAttr` test.
- **Pipeline wiring** — `ResolveJSAttr` must run for `JSAttr`s before their holes are type-probed; fold the attr walk into the existing `ResolveScripts` entry already wired into codegen (C1), so no new pipeline insertion point is needed (verify probe coverage of attr holes — `collectExprs`/`emitProbes` must visit `JSAttr.Segments`).
