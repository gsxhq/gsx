# Fold-Path Contextual Holes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close #92, #93, and #94 by giving folded embedded holes byte-identical contextual escaping/type dispatch and expanding the fold differential alphabet.

**Architecture:** Export pure runtime string helpers backed by the existing JS and dynamic-value implementations. Add codegen expression builders that assemble JS/CSS/text embedded attributes for `composeBag` without HTML escaping; the existing spread leaf remains the sole HTML escaper. Keep fuzz coverage as a separate mechanical task.

**Tech Stack:** Go, gsx codegen, txtar corpus, `testing.F`.

## Global Constraints

- Runtime is standard-library-only; add no dependency.
- JS/CSS escaping occurs before HTML attribute escaping; HTML escaping occurs exactly once at `Spread`.
- `RawJS`/`RawCSS` bypass only language filtering, never HTML escaping.
- Inline and folded outputs are byte-identical.
- Preserve PR #91 source evaluation, error short-circuiting, and probe/emit alignment.
- No parser/grammar/editor changes and no new `packages.Load` call.
- Generated goldens are regenerated, never hand-edited.

---

### Task 1: Pure contextual string helpers

**Files:**
- Modify: `js.go`, `writer.go`.
- Modify tests: `js_diff_test.go`, `writer_test.go`.

**Interfaces:**
- Produces: `EscapeJSVal(any) string`, `EscapeJSStr(string) string`, `EscapeJSTmpl(string) string`, `EscapeJSRegexp(string) string`, `AttrString(any) (string, error)`.
- Existing `Writer.JSVal`, `JSValAttr`, `JSStr`, `JSStrAttr`, `JSTmpl`, `JSTmplAttr`, `JSRegexp`, `JSRegexpAttr`, `TextAny`, and `AttrAny` delegate to these helpers.

- [ ] **Step 1: RED helper parity tests**

Add table tests using hostile strings (`a\"</script><script>alert(1)</script>`, U+2028/U+2029, backticks, `${x}`, regexp metacharacters), `RawJS`, nil, bool, and numbers. For each JS context assert the pure helper equals the pre-HTML writer method output. For `AttrString`, assert every type accepted by `anyRenderString` returns the same string and an unsupported struct returns `gsx: AttrString: unsupported dynamic type struct { X int }`.

Run `go test . -run 'TestEscapeJS|TestAttrString' -count=1`; expect compile failure because helpers do not exist.

- [ ] **Step 2: GREEN helpers and delegation**

Implement the helpers as thin exported entry points over `jsValEscaper`, `jsStrString`, `jsTmplString`, `jsRegexpString`, and `anyRenderString`. `AttrString` returns `fmt.Errorf("gsx: AttrString: unsupported dynamic type %T", v)` on failure. Rewrite writer methods to call them, retaining each writer's current error label/behavior.

Run `go test . -run 'Test(JS|EscapeJS|AttrString|Writer)' -count=1`; expect PASS. Run `gopls check -severity=hint js.go writer.go`.

- [ ] **Step 3: Commit**

`git add js.go writer.go js_diff_test.go writer_test.go && git commit -m "feat: expose contextual attribute string helpers"`

---

### Task 2: Fold JS/CSS and mixed generic holes

**Files:**
- Modify: `internal/codegen/emit.go`.
- Create corpus cases under `internal/corpus/testdata/cases/multispread/`: `jscss_holes_fold.txtar`, `jscss_holes_hostile.txtar`, `generic_hole_fold.txtar`, `context_hole_error_order.txtar`.
- Modify generated manifest/goldens via full corpus regeneration.

**Interfaces:**
- Consumes Task 1 helpers and existing `FilterCSS`.
- Produces unexported expression builders `embeddedJSValueExpr`, `embeddedCSSValueExpr`, and a shared hole-lowering path that returns a Go string expression for bag insertion.

- [ ] **Step 1: RED canonical reproductions**

Add the exact #92 two-spread `onclick=js"do(@{u})"` case and a `style=css"color:@{color}"` sibling. Add the #93 generic component:

```gsx
component Field[T string | int](value T, a gsx.Attrs, b gsx.Attrs) {
	<div { a... } title=f"value=@{value}" { b... }>x</div>
}
```

Invoke both `Field[string]` and `Field[int]`. Run targeted corpus tests and record the expected `unsupported-component-attr` / `unsupported-url-type` failures.

- [ ] **Step 2: Build JS expression lowering**

Mirror `emitJSAttrInterp` without writing: lower pipeline, unwrap `(T,error)`, apply renderer, then select by `JSCtx`: `EscapeJSVal(expr)` for value context; convert string-compatible expressions and call `EscapeJSStr`, `EscapeJSTmpl`, or `EscapeJSRegexp` for their contexts. Concatenate static segments unchanged. Reject unsupported types with the same positioned diagnostics as inline emission. Call `materializePrior()` before this lowering in `composeBag` emit mode.

- [ ] **Step 3: Build CSS expression lowering**

Mirror `emitCSSAttrInterp`: lower pipeline/tuple/renderer; `RawCSS` becomes `string(expr)`, other supported string/Stringer values become `FilterCSS(value)`. Concatenate static segments unchanged. Call `materializePrior()` before lowering.

- [ ] **Step 4: Route `catAnyMixed` through `AttrString`**

In `holeStringExpr`/`stringifyExpr`, when classification is `catAnyMixed`, generate `AttrString(expr)`, hoist its `(string,error)` using the existing render-error return shape, and use the resulting temp. Do not use `fmt.Sprint` or assume only the current `string | int` example.

- [ ] **Step 5: GREEN parity/security/error corpus**

Regenerate targeted cases. Add inline controls and assert exact byte parity for JS value/string/template/regexp, CSS, `RawJS`, `RawCSS`, quotes/angle-brackets/ampersands/U+2028/U+2029, renderer, tuple error, and generic string/int. `context_hole_error_order` must record `prior,error` and prove `late` never runs.

Run targeted corpus without update, then `go test ./internal/codegen ./internal/corpus -count=1`.

- [ ] **Step 6: Full regeneration and commit**

Run `go test ./internal/corpus -run TestCorpus -update`, inspect all churn, verify without update, then `gopls check -severity=hint internal/codegen/emit.go` and `git diff --check`.

`git add internal/codegen/emit.go internal/corpus/testdata && git commit -m "feat(codegen): fold contextual embedded holes"`

---

### Task 3: Differential fuzz tokens and roadmap

**Files:**
- Modify: `attrs_fold_fuzz_test.go`, `docs/ROADMAP.md`.

**Interfaces:**
- `foldValAlphabet` becomes `[]any`; `referenceLastWins` preserves scalar `any` and string-asserts only class/style pieces.

- [ ] **Step 1: RED focused fuzz-unit test**

Add a deterministic test calling `decodeContribs` with bytes selecting class value `" a "` and bool `true`. Assert decoded value types and that fold/reference both render ` class="a b" disabled`. Run it before changing the alphabet; expect failure because no bool token exists and whitespace is not edge-trimmed.

- [ ] **Step 2: GREEN alphabet/decoder/reference**

Use an `[]any` alphabet containing existing strings, `" a "`, `true`, and `false`; add `disabled` to the key alphabet. Preserve non-class/style values unchanged in the independent reference. For class/style, accept string pieces only and skip non-string pieces exactly as production aggregation does. Add explicit fuzz seeds selecting whitespace and bool.

- [ ] **Step 3: Docs and verification**

Mark #92, #93, and #94 fixed in `docs/ROADMAP.md`. Run `go test . -run 'Test.*Fold|FuzzAttrsFoldMatchesReference' -count=1`, `make check`, then commit:

`git add attrs_fold_fuzz_test.go docs/ROADMAP.md && git commit -m "test: expand spread-fold differential values"`

---

### Task 4: Final security review and publish

- [ ] Run `make ci` outside the sandbox for localhost tests and `make lint`.
- [ ] Dispatch an independent adversarial reviewer with throwaway inline/fold probes for all JS contexts, CSS, hostile payloads, RawJS/RawCSS, generic string/int, error ordering, and double-escape detection.
- [ ] Fix all Critical/Important findings and re-run gates.
- [ ] Push `codex/fold-context-holes` and open a draft PR with `Closes #92`, `Closes #93`, and `Closes #94`.

## Self-review

- Spec coverage: runtime purity Task 1; #92/#93 and security/evaluation Task 2; #94 Task 3; authoritative review Task 4.
- Placeholder scan: no TBD/TODO or deferred behavior.
- Type consistency: helper names/signatures match the approved spec and all consumers.
