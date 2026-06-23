# Attribute-merge (caller-wins) + `style={ }` unlock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Flip attribute-fallthrough precedence from root-wins to **caller-wins** (JSX last-wins, with a positional `{...attrs}` escape hatch) and **unlock `style={ expr }`** via contextual CSS sanitization.

**Architecture:** Auto-mode root injection stops streaming root attrs directly and instead builds the root's attributes as a runtime `gsx.Attrs` literal, then emits `<rootAttrs>.Merge(_gsxp.Attrs)` — the existing `Attrs.Merge` already gives "other (bag) wins; class/style concatenated a-then-b", i.e. caller-wins with caller cascading last. Manual mode (`{...attrs}` present) splits the root attrs at the spread's source position into *overridable* (pre) and *forced* (post) literals: `overridable.Merge(bag).Merge(forced)`. `style={ expr }` routes through the existing `cssValueFilter`/`gw.CSS` (RawCSS opt-out) and feeds the merge.

**Tech Stack:** Go; stdlib-only runtime. Existing pieces reused: `Attrs.Merge` (`attrs.go:48`), `gw.Spread` (`attrs.go:83`), `cssValueFilter`/`gw.CSS`/`isRawCSS` (the CSS-context path), `ClassMerger`/`classTokens`.

## Global Constraints

- Runtime (`gsx` root package) is **stdlib-only**.
- Threat model unchanged: template authors (component AND caller) trusted; interpolated **data** is not. `style={ expr }` sanitizes the value; author-written static class/style is trusted.
- **No regression** to the shipped contextual escaping or the JS-interpolation corpus (`internal/corpus/testdata/cases/{script,jsattr,datajson}`).
- Precedence tiers, resolved per attribute name: **forced root > bag (caller) > overridable root**. Auto mode has no forced tier (caller wins all). Class/style are merged (concatenated low→high precedence), not replaced.
- `defaultClassMerge` keeps the **last** occurrence of a duplicate token, surviving tokens in source order: `"a b a"` → `"b a"`.
- Out of scope: pipeline transforms (`?`/`map`/etc. — separate slice); the user-extensible JS-attr knob (already landed — see next).
- **Attribute-context API (changed since the spec):** `internal/attrjs` was replaced by `internal/attrclass`. Classification is now a `*attrclass.Classifier` threaded through codegen as a `cls` parameter (`generateFile`/`genComponent`/`emitRootElement`/`emitAttr`/`emitExprAttr`/`genNode`/…). Use **`cls.Context(name)`** returning **`attrclass.CtxPlain | CtxJS | CtxURL | CtxCSS`** — NOT the old free function `attrContext(name)`/`ctxCSS`. Any rewritten function that previously took the implicit context must keep threading `cls`. Confirm exact line numbers by reading `internal/codegen/emit.go` (they have shifted since this plan was first drafted).

---

### Task 1: `defaultClassMerge` — last-wins dedupe

**Files:** Modify `class.go` (`defaultClassMerge`, line 24); Test `class_test.go`.

**Interfaces — Produces:** `defaultClassMerge([]string) string` unchanged signature; behavior: last occurrence of each token wins, survivors in source order.

- [ ] **Step 1: Write the failing test** in `class_test.go`:
```go
func TestDefaultClassMergeLastWins(t *testing.T) {
	for _, tt := range []struct{ in []string; want string }{
		{[]string{"a", "b", "a"}, "b a"},
		{[]string{"a", "b"}, "a b"},
		{[]string{"x", "x", "x"}, "x"},
		{[]string{}, ""},
		{[]string{"p-2", "p-4", "p-2"}, "p-4 p-2"},
	} {
		if got := defaultClassMerge(tt.in); got != tt.want {
			t.Errorf("defaultClassMerge(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
```
- [ ] **Step 2: Run** `go test . -run TestDefaultClassMergeLastWins` — expect FAIL (current first-wins gives `"a b"` for `{a,b,a}`).
- [ ] **Step 3: Implement** — replace the body of `defaultClassMerge` (class.go:24) with a last-wins dedupe:
```go
func defaultClassMerge(tokens []string) string {
	// Keep the LAST occurrence of each token (caller/last-wins), preserving the
	// surviving tokens in source order. e.g. "a b a" -> "b a".
	lastIdx := make(map[string]int, len(tokens))
	for i, t := range tokens {
		lastIdx[t] = i
	}
	out := make([]string, 0, len(tokens))
	for i, t := range tokens {
		if lastIdx[t] == i {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}
```
- [ ] **Step 4: Run** `go test .` — the new test passes; check no existing class test broke (some may assert first-wins ordering — re-baseline any to last-wins, noting the flip in the commit message). Run the full `go test ./...`.
- [ ] **Step 5: Commit** `runtime: defaultClassMerge keeps last occurrence (caller/last-wins)`.

---

### Task 2: Runtime building blocks — `Attrs.Merge` tests + `gsx.ClassString`

**Files:** Modify `class.go` (add `ClassString`); Test `attrs_test.go`, `class_test.go`.

**Interfaces — Produces:**
- `func ClassString(parts ...ClassPart) string` — the string form of `gw.Class`: the merged class string (`ClassMerger(classTokens(parts))`). Used by codegen to put a composed `class={ … }` into an `Attrs` map value (Tasks 4/5).
- Confirms `Attrs.Merge(other)`: other-wins; `class` space-joined a-then-b; `style` `a; b`.

- [ ] **Step 1: Write failing tests.** In `attrs_test.go`:
```go
func TestAttrsMergeCallerWins(t *testing.T) {
	root := gsx.Attrs{"id": "root", "class": "base", "style": "color:red", "role": "x"}
	bag := gsx.Attrs{"id": "caller", "class": "extra", "style": "margin:0"}
	got := root.Merge(bag)
	if got["id"] != "caller" { t.Errorf("id = %v, want caller (other wins)", got["id"]) }
	if got["role"] != "x" { t.Errorf("role dropped") }
	if got["class"] != "base extra" { t.Errorf("class = %v, want \"base extra\"", got["class"]) }
	if got["style"] != "color:red; margin:0" { t.Errorf("style = %v, want \"color:red; margin:0\"", got["style"]) }
}
```
In `class_test.go`:
```go
func TestClassString(t *testing.T) {
	if got := gsx.ClassString(gsx.Class("a b"), gsx.ClassIf("c", false), gsx.Class("d")); got != "a b d" {
		t.Errorf("ClassString = %q, want \"a b d\"", got)
	}
}
```
- [ ] **Step 2: Run** both — `TestClassString` FAILs (undefined); `TestAttrsMergeCallerWins` should already PASS (Merge exists) — if it doesn't, the merge semantics differ from the spec; STOP and surface it.
- [ ] **Step 3: Implement `ClassString`** in `class.go` next to `gw.Class` (which is `gw.writeStr(ClassMerger(classTokens(parts)))` shape — read it):
```go
// ClassString returns the merged class string for parts (the value form of
// gw.Class), so generated code can place a composed class into an Attrs map.
func ClassString(parts ...ClassPart) string {
	return ClassMerger(classTokens(parts))
}
```
(Verify the exact names `ClassMerger`/`classTokens` in `class.go`; reuse them — do NOT reimplement the token logic.)
- [ ] **Step 4: Run** `go test .` green.
- [ ] **Step 5: Commit** `runtime: add gsx.ClassString (value form of gw.Class) + Attrs.Merge caller-wins tests`.

---

### Task 3: `style={ expr }` contextual unlock

**Files:** Modify `internal/codegen/emit.go` (`emitExprAttr` ~line 972, the `case attrclass.CtxCSS:` rejection ~line 977 and the value-emit section ~1014); Test corpus `internal/corpus/testdata/cases/style/`.

**Interfaces — Consumes:** existing `emitRenderCSS` CSS emission, `isRawCSS`, `cls.Context(name) == attrclass.CtxCSS` (the threaded classifier). **Produces:** a `style={ expr }` attribute emits CSS-sanitized (or RawCSS-verbatim) instead of a compile error.

- [ ] **Step 1: Add the failing corpus case** `internal/corpus/testdata/cases/style/attr_expr_sanitized.txtar` (model on an existing `style/` case + `jsattr/*_breakout` for the hostile-value shape): a component `<div style={ userStyle }>` with `userStyle string` set to a hostile value (e.g. `"color:red; background:url(javascript:alert(1))"` or a breakout attempt), and a second component `<div style={ gsx.RawCSS("color:blue") }>`. Include `generated.x.go.golden` (pins the `_gsxgw.CSS(...)` / RawCSS emission) and `render.golden` (pins the sanitized output / verbatim RawCSS). Find the corpus update flag in `internal/corpus/*_test.go`.
- [ ] **Step 2: Run** `go test ./internal/corpus/ -run TestCorpus` — FAILs: today `emitExprAttr` returns the `attrclass.CtxCSS` error ("…needs a safe type via `|> css` (not available yet)").
- [ ] **Step 3: Implement.** In `emitExprAttr`, remove the top `case attrclass.CtxCSS:` fail-closed `return` (the block that errors "expr value in CSS context… needs `|> css`" — note that message names the now-retired `|> css`, so deleting it also removes a stale reference). In the value-emit section (after ` name="` is written, alongside the existing `cls.Context(a.Name) == attrclass.CtxJS` and `== attrclass.CtxURL` branches), add a `cls.Context(a.Name) == attrclass.CtxCSS` branch that mirrors the `<style>`-block CSS emission (read `emitRenderCSS`): a `gsx.RawCSS`-typed value (`isRawCSS(t)`) → `_gsxgw.S(string(expr))` (verbatim); a string/Stringer → `_gsxgw.CSS(string(expr))` / `_gsxgw.CSS((expr).String())`; any other type → a clear error ("value of type %s not renderable in a style attribute; need string/Stringer or gsx.RawCSS"). Keep the `attrclass.CtxJS` rejection and the `bool`→`BoolAttr` short-circuit (`classify(t) == catBool && cls.Context(a.Name) != attrclass.CtxJS`) intact.
- [ ] **Step 4: Run** `go test ./internal/corpus/` then `go test ./...`. Inspect the new `render.golden`: the hostile value must be filtered (no `javascript:`/breakout survives — `cssValueFilter` neutralizes it), and the `RawCSS` case emits verbatim. Quote the safe golden line in the task report.
- [ ] **Step 5: Commit** `codegen: unlock style={ expr } via contextual CSS sanitization (gw.CSS, RawCSS opt-out)`.

NOTE: this task handles `style={ expr }` on ANY element (the `emitAttr`/`emitExprAttr` path). Its participation in the ROOT merge is Task 4 (where the root's `style` becomes an `Attrs` value).

---

### Task 4: Auto-mode caller-wins (root injection rewrite)

**Files:** Modify `internal/codegen/emit.go` (`emitRootElement` ~line 299, which now takes a `cls *attrclass.Classifier` param — keep threading it; `rootWithoutArgs` ~line 406 removed); re-baseline `internal/corpus/testdata/cases/jsattr/root_wins.txtar` + any root-wins corpus case; add corpus cases.

**Interfaces — Consumes:** `gsx.Attrs` literal, `Attrs.Merge` (Task 2 semantics), `gsx.ClassString` (Task 2), the CSS sanitization (Task 3). **Produces:** a single-root component's root attrs merge with the bag caller-wins.

**Design:** Replace "stream each root attr + `Spread(Attrs.Without(rootNames))`" with: build a `gsx.Attrs{…}` literal of the root's own attrs (each value as a Go expression), then emit one `_gsxgw.Spread(ctx, gsx.Attrs{…}.Merge(_gsxp.Attrs))`. Mapping per root attr kind (auto-mode is gated to a single root with NO `CondAttr`/`SpreadAttr`, so only these occur):
- `*ast.StaticAttr` → `"name": "value"` (raw Go string literal of the value; NOT pre-escaped — `Spread` escapes).
- `*ast.ExprAttr` (plain value) → `"name": <expr>` (for `style`, wrap the value so it is CSS-sanitized first — see below; for a URL attr, the existing URL handling must be preserved — KEEP url attrs on the direct-emit path if folding them into the bag would skip `gw.URL`; verify and state which in the report).
- `*ast.BoolAttr` → `"name": true`.
- composed `*ast.ClassAttr` (`class={ … }`) → `"class": gsx.ClassString(<parts…>)` (lower the parts exactly as `emitRootComposedClass` does, but to the string form).
- static `class="x"` → `"class": "x"`.
- `style={ expr }` → `"style": <sanitized style string>` — sanitize via the Task-3 mechanism into a string value (e.g. `string(gsx.FilterCSS(userStyle))`, or `string(gsx.RawCSS-value)` verbatim). Static `style="…"` → `"style": "…"`.

`gsx.Attrs{…}.Merge(_gsxp.Attrs)` → bag wins; root class/style placed first → caller cascades last → caller wins. For a nil/empty bag, `Merge` returns the root's attrs unchanged → byte-equivalent to a no-fallthrough element (preserve this property; add a render case asserting it).

- [ ] **Step 1: Re-baseline + new corpus (failing).** Update `jsattr/root_wins.txtar`: the expectation **flips** — the caller's bag value now WINS (rename to `root_overridden.txtar` or keep the name with a comment; the render shows the caller's `x-data`, not the root's). Add `class/caller_wins_merge.txtar`: `<div id="root" class="base" style="color:red">` invoked with a bag `{id:"caller", class:"extra", style:"margin:0"}` → render shows `id="caller"`, class merged (`base extra` order per `defaultClassMerge`), style `color:red; margin:0` (caller cascades last). Add `class/empty_bag_noop.txtar`: same component, empty/nil bag → byte-identical to the plain element.
- [ ] **Step 2: Run** `go test ./internal/corpus/` — FAILs (current root-wins output).
- [ ] **Step 3: Implement** the `emitRootElement` rewrite per the Design above: build the `gsx.Attrs{…}` literal (in source order), emit `_gsxgw.Spread(ctx, gsx.Attrs{…}.Merge(_gsxp.Attrs))` in place of the direct-attr-emit loop + the `ClassMerged`/`Spread(Without(...))` lines. Remove `rootWithoutArgs` and the `emitRootComposedClass`/`emitRootStaticClass` stream-writers if now unused (or repoint them to the string form). Keep the `<style>`/`<script>` child-emit and the void/`>` handling unchanged.
- [ ] **Step 4: Run** `go test ./internal/corpus/` then `go test ./...`. Verify the JS-interp corpus (`script`/`jsattr`/`datajson`) stays green; inspect `caller_wins_merge.golden` and `empty_bag_noop.golden`. Bump `internal/codegen/version.go`.
- [ ] **Step 5: Commit** `codegen: auto-mode attribute fallthrough is caller-wins (root.Merge(bag)); re-baseline root_wins`.

---

### Task 5: Manual-mode positional escape hatch (`{...attrs}` forced)

**Files:** Modify `internal/codegen/emit.go` (the manual-mode path — `manual` branch ~255 and how the root + `{...attrs}` `SpreadAttr` emit via `genNode`/`emitAttr` ~794); add corpus.

**Interfaces — Consumes:** Task-4's Attrs-literal construction + `Attrs.Merge`. **Produces:** in manual mode, root attrs *before* `{...attrs}` are overridable (bag wins), attrs *after* are forced (root wins).

**Design:** Today manual mode emits the root via normal `genNode`, and the `*ast.SpreadAttr` (`{...attrs}`) emits `gw.Spread(ctx, attrs)` inline at its source position — so precedence is whatever HTML does with emission order (first-dup-wins), which does NOT give post-spread-forced semantics. To get the spec's positional rule, the manual root element's attr emission must also become a merge: split the root's attrs at the `SpreadAttr` index into `overridable` (before) and `forced` (after) `gsx.Attrs` literals (built exactly as Task 4), and emit a single `_gsxgw.Spread(ctx, <overridable>.Merge(attrs).Merge(<forced>))` in place of the in-order attr/spread emission. (`forced.Merge` applied last → forced wins; class/style concatenated overridable→bag→forced.)

Detect this in the element-emit path: an element carrying a `*ast.SpreadAttr` whose expr is the fallthrough bag (`attrs`) is a manual-fallthrough root → route to a `emitManualSpreadElement` that does the split-merge instead of streaming attrs. A non-fallthrough `{...someOtherExpr}` spread keeps today's inline `gw.Spread`.

- [ ] **Step 1: Add failing corpus.** `jsattr/manual_forced.txtar`: `<div {...attrs} role="dialog">` (role AFTER the spread) invoked with a bag setting `role` and `id` → render shows `role="dialog"` (forced, root wins) and `id` from the bag. `jsattr/manual_overridable.txtar`: `<div id="x" {...attrs}>` (id BEFORE) + bag `id` → render shows the caller's `id` (bag wins).
- [ ] **Step 2: Run** `go test ./internal/corpus/` — FAILs (today's inline emission gives first-dup-wins / wrong precedence).
- [ ] **Step 3: Implement** `emitManualSpreadElement` (or extend the element path): find the `SpreadAttr` whose expr is `attrs`, split surrounding attrs into overridable/forced literals (reuse Task-4's Attrs-literal builder — factor it into a shared helper `rootAttrsLiteral(attrs []ast.Attr) (string, error)` so Tasks 4 & 5 share it, not duplicate), emit the merged Spread. Multiple `{...attrs}` spreads on one element → a clear codegen error ("ambiguous: more than one {...attrs} spread"). Handle a manual root with no surrounding attrs (just `{...attrs}`) → `attrs` spread alone.
- [ ] **Step 4: Run** `go test ./internal/corpus/` then `go test ./...` green; inspect both manual goldens. Bump `internal/codegen/version.go` if not already this cycle.
- [ ] **Step 5: Commit** `codegen: manual {...attrs} positional precedence (post-spread attrs forced); shared rootAttrsLiteral`.

---

## Self-Review

**Spec coverage:** §2 precedence tiers → T4 (auto, no forced) + T5 (manual forced/overridable); §3 `Attrs.Merge` reuse + `defaultClassMerge` last-wins → T1/T2/T4; §4 `style={}` unlock → T3 (+ T4 merge participation); §7 tests → each task's corpus + units; §8 root_wins re-baseline → T4 Step 1.

**Placeholder scan:** T4/T5 give the construction rule + the merge expression + the shared `rootAttrsLiteral` helper + exact corpus assertions; the implementer writes the literal-builder against the real `emitRootElement` (the one place the byte-exact code depends on reading the current file). URL-attr-in-bag handling is flagged as a verify-and-state decision, not left vague — the safe default (keep URL attrs on the `gw.URL` direct path) is named.

**Type/name consistency:** `ClassString` (T2) used in T4/T5; `rootAttrsLiteral` shared T4→T5; `Attrs.Merge` precedence consistent (other/bag wins, class/style concat) across T2/T4/T5; `defaultClassMerge` last-wins rule identical in T1 and referenced in T4.

## Risks
- **URL attrs folded into the bag would bypass `gw.URL`** (scheme sanitization). Mitigation (T4 Step 3): keep `ctxURL` attrs on the direct-emit/`gw.URL` path, OR sanitize into the Attrs value with `gsx.RawURL`/`urlSanitize` — the implementer verifies `href={x}` still scheme-sanitizes and states the chosen mechanism. A render case with a `javascript:` href on a root with fallthrough guards it.
- **Byte-parity of composed class/style** moving from stream-writers to `ClassString`/sanitized-string values — covered by the merge corpus goldens.
- **`Attrs.Merge` ordering** (a-then-b for class/style) must match the low→high precedence — verified in T2.
- **Manual-mode detection** must not mis-route a non-`attrs` `{...expr}` spread — T5 routes only when the spread expr is the bag.
