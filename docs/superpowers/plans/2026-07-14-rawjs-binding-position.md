# `gsx.RawJS` holes in JS binding/lvalue position — implementation plan

> **For agentic workers:** implement task-by-task. Steps use checkbox syntax.
> Design: `docs/superpowers/specs/2026-07-14-rawjs-binding-position-design.md`.

**Goal:** Allow a `@{ … }` hole of Go type `gsx.RawJS` in a JS binding/lvalue
position inside `js` literals and `<script>` blocks; splice it verbatim. Bare
holes there stay rejected — now at emit time, with a diagnostic pointing at
`gsx.RawJS`.

**Architecture:** The JS classifier (`internal/jsx`) stops rejecting
binding-position holes and instead tags them `ast.JSCtxBinding`, deferring the
legality decision to codegen emit — where the hole's Go type is known. All three
JS emit switches gate `JSCtxBinding` on `isRawJS(type)`: pass → verbatim emit
(identical to the value path, which already passes `RawJS` through); fail →
`jsx-binding-position` diagnostic.

**Tech Stack:** Go; `go/types` for static named-type detection; the txtar corpus
(`internal/corpus`) for per-context goldens.

## Global Constraints

- Runtime (root package) stays standard-library only; no new deps.
- `gsx.RawJS` detection: `types.Unalias` → `*types.Named`, `Obj().Name() ==
  "RawJS"` && `Obj().Pkg().Path() == "github.com/gsxhq/gsx"` (mirror `isRawCSS`).
- Uniform scope: **any** non-value position, gated only on the hole being
  `gsx.RawJS`. No per-position allowlist.
- JS only. CSS/`RawCSS` and `f`` untouched. No new hole syntax.
- Verbatim splice reuses the existing value-context emit (`JSVal` /
  `JSValAttr` / `EscapeJSVal`), which already emits `RawJS` verbatim at runtime
  — byte-identical to `gsx.RawJS(concat)`.
- Every syntax/semantics change ships corpus coverage per context.

---

### Task 1: `JSCtxBinding` enum + `isRawJS` helper + classifier defer + emit cases + diagnostic

This is one atomic code change: the enum value, classifier, and all three emit
switches must land together or the tree is inconsistent (a deferred hole with no
emit case falls through to the "internal error" default).

**Files:**
- Modify: `ast/ast.go` (add `JSCtxBinding` to the `JSCtx` enum)
- Modify: `internal/jsx/jsx.go` (`classifyHole`: defer instead of reject)
- Modify: `internal/codegen/emit.go` (`isRawJS` helper; `JSCtxBinding` cases in
  `emitJSValue`, `emitJSAttrValue`, `embeddedJSValueExpr`)

**Interfaces:**
- Produces: `ast.JSCtxBinding` (new enum constant, appended last so existing
  values keep their `iota` numbering); `isRawJS(t types.Type) bool`.

- [ ] **Step 1: Add the enum constant.** In `ast/ast.go`, append to the `JSCtx`
  const block (after `JSCtxRegexp`), with a doc comment:

```go
// JSCtxBinding: the hole is its own token in a non-value (binding/lvalue)
// position — assignment target, declaration or member name. Legal only when
// the hole's Go type is gsx.RawJS (decided at emit, where the type is known);
// spliced verbatim.
JSCtxBinding
```

- [ ] **Step 2: Classifier defers.** In `internal/jsx/jsx.go`, `classifyHole`,
  replace the reject tail (the `bag.Report(… "jsx-identifier-position" …);
  return false` after the `isValuePosition` check) with:

```go
if isValuePosition(prevSig) {
	h.ctx = ast.JSCtxValue
	h.resolved = true
	return true
}
// Non-value (binding/lvalue) position: whether this is legal depends on the
// hole's Go type (only gsx.RawJS may splice here), which is unknown at classify
// time. Defer to emit; mark the context so codegen can adjudicate.
h.ctx = ast.JSCtxBinding
h.resolved = true
return true
```

- [ ] **Step 3: Add `isRawJS`.** In `internal/codegen/emit.go` (next to
  `isRawURL`), mirror `isRawCSS`:

```go
func isRawJS(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Name() == "RawJS" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}
```

- [ ] **Step 4: `emitJSValue` (script path).** Add before `default`:

```go
case ast.JSCtxBinding:
	if !isRawJS(t) {
		bag.Errorf(n.Pos(), n.End(), "jsx-binding-position",
			"@{ } here is a JavaScript binding/lvalue position (assignment target, declaration or member name); only a gsx.RawJS value may be spliced here — wrap it as gsx.RawJS(...) if the bytes are trusted, or use it where a value is expected")
		return false
	}
	// Verbatim: JSVal emits a gsx.RawJS value unescaped at runtime.
	fmt.Fprintf(b, "\t\t_gsxgw.JSVal(%s)\n", expr)
	return true
```

- [ ] **Step 5: `emitJSAttrValue` (single-hole JS attribute).** Same shape,
  using `_gsxgw.JSValAttr(%s)` for the verbatim emit:

```go
case ast.JSCtxBinding:
	if !isRawJS(t) {
		bag.Errorf(n.Pos(), n.End(), "jsx-binding-position",
			"@{ } here is a JavaScript binding/lvalue position (assignment target, declaration or member name); only a gsx.RawJS value may be spliced here — wrap it as gsx.RawJS(...) if the bytes are trusted, or use it where a value is expected")
		return false
	}
	fmt.Fprintf(b, "\t\t_gsxgw.JSValAttr(%s)\n", expr)
	return true
```

- [ ] **Step 6: `embeddedJSValueExpr` (js`` literal segments).** In the
  `switch s.JSCtx` (emit.go:3954), add a `JSCtxBinding` case gating on `typ`
  (the type returned by `embeddedHoleExpr`), producing the same
  `EscapeJSVal(expr)` term the value case does:

```go
case ast.JSCtxBinding:
	if !isRawJS(typ) {
		bag.Errorf(s.Pos(), s.End(), "jsx-binding-position",
			"@{ } here is a JavaScript binding/lvalue position (assignment target, declaration or member name); only a gsx.RawJS value may be spliced here — wrap it as gsx.RawJS(...) if the bytes are trusted, or use it where a value is expected")
		return "", false
	}
	escaped = rt.rt() + ".EscapeJSVal(" + expr + ")"
```

- [ ] **Step 7: Update the jsx unit test.** `internal/jsx/jsx_test.go` has a
  test asserting the classify-time `identifier/binding` rejection
  (`jsx_test.go:335`). The classifier no longer rejects — it now returns clean
  with `JSCtx == ast.JSCtxBinding`. Change that test to assert the hole is
  classified `JSCtxBinding` (no error), matching the new deferral contract.

- [ ] **Step 8: Build + vet.**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 9: Commit.**

```bash
git add ast/ast.go internal/jsx/jsx.go internal/jsx/jsx_test.go internal/codegen/emit.go
git commit -m "feat(jsx): defer JS binding-position holes to emit, allow gsx.RawJS"
```

---

### Task 2: Corpus coverage (per context) + update the rejection case

**Files:**
- Create: `internal/corpus/testdata/cases/goexpr-js-literal/rawjs_binding_position.txtar`
  (expression-valued `js`` literal in a Go block — the `displaySyncAttrs` shape)
- Create: `internal/corpus/testdata/cases/goexpr-js-literal/rawjs_binding_uniform_positions.txtar`
  (member name `foo.@{gsx.RawJS(p)}`, declaration name `let @{gsx.RawJS(n)} = 1`)
- Create: `internal/corpus/testdata/cases/renderers/js_attr_binding.txtar`
  (a `js` attribute: `@change=js\`@{gsx.RawJS(path)} = foundId;\``)
- Create: `internal/corpus/testdata/cases/script/rawjs_binding_position.txtar`
  (`<script>@{gsx.RawJS(path)} = 1;</script>`)
- Create: `internal/corpus/testdata/cases/goexpr-js-literal/binding_non_rawjs_rejected.txtar`
  (a bare string hole in binding position → `jsx-binding-position` diagnostic)
- Modify: `internal/corpus/testdata/cases/script/interp_identifier_rejected.txtar`
  (rejection is now emit-time + reworded; and only fires for non-RawJS — keep
  it as a non-RawJS `<script>` rejection, regenerate its diagnostics.golden)

- [ ] **Step 1: Write the accepted cases** covering the three surfaces + uniform
  positions. Each should include a differential contrast where practical (the
  `gsx.RawJS(concat)` form and the `js`` form rendering identically), mirroring
  `rawjs_passthrough_value.txtar`. Use a concrete path like `form.email`.

- [ ] **Step 2: Write the rejection case** — a bare `string` hole in binding
  position — and confirm it yields the `jsx-binding-position` diagnostic (not a
  silent JSON-quote, not the old classify-time message).

- [ ] **Step 3: Regenerate goldens.**

Run: `go test ./internal/corpus -run TestCorpus -update`
Then verify clean: `go test ./internal/corpus -run TestCorpus`
Expected: PASS; `coverage.golden` updated (manifest bump).

- [ ] **Step 4: Inspect** each new `render.golden` / `generated.x.go.golden` by
  eye — accepted holes splice the path verbatim (`form.email = foundId`, not
  `"form.email"`); the rejection case has only a `diagnostics.golden`.

- [ ] **Step 5: Commit.**

```bash
git add internal/corpus/testdata/cases
git commit -m "test(corpus): gsx.RawJS holes in JS binding position (per context) + rejection"
```

---

### Task 3: Docs

**Files:**
- Modify: the `js`/`css` literal holes section of the guide
  (`docs/guide/**` — locate the page documenting `@{ }` hole escaping in
  embedded JS).

- [ ] **Step 1: Locate** the guide page covering `js`` holes / RawJS. Grep
  `docs/guide` for `RawJS` and `@{`.

- [ ] **Step 2: Add** a concise paragraph (per the docs-concise rule — state the
  behavior plainly, no rationale essay): a hole in a JS binding/lvalue position
  (assignment target, declaration or member name) must be `gsx.RawJS`; it is
  spliced verbatim. Wrap literal `{{ }}` in `::: v-pre` if present. Show the
  `@{gsx.RawJS(path)} = value` example. One sentence of principle max.

- [ ] **Step 3: Commit.**

```bash
git add docs/guide
git commit -m "docs(guide): gsx.RawJS holes in JS binding/lvalue position"
```

---

## After all tasks

- [ ] `make check` (authoritative inner-loop CI).
- [ ] Validate on `~/work/one-learning-gsx`: rewrite the
  `common_edit_components.gsx` `@change` handler as a `js`` literal with
  `@{gsx.RawJS(xModelPath)} = foundId`, regenerate, confirm the rendered JS is
  byte-identical to the current `fmt.Sprintf` output and idempotent under
  `gsx fmt`.
- [ ] Independent adversarial review (builds throwaway probe programs — LSP
  surfacing of `jsx-binding-position`, injection-safety of the type gate,
  eval-order with a binding hole beside a value hole) before merge.

## Self-review notes

- **LSP:** the diagnostic moved parse→emit. Confirm the analyzer's JS-emit path
  runs for `<script>`/attr/expression holes so LSP reports `jsx-binding-position`
  (part of the adversarial review).
- **Type consistency:** `emitJSValue`/`emitJSAttrValue` take `t types.Type`;
  `embeddedJSValueExpr` uses `typ` from `embeddedHoleExpr`. `isRawJS` takes
  `types.Type` — matches all three.
- **No sibling grammar changes** (`@{ }` syntax unchanged).
