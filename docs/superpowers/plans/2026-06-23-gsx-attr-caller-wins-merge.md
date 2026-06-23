# Caller-wins attribute fallthrough Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Flip attribute fallthrough from root-wins to **caller-wins** (JSX last-wins) while every root attribute keeps its context escaping, with a positional `{...attrs}` escape hatch and property-level `style` merging.

**Architecture:** Root attrs stay as direct, context-aware emits, each **guarded** `if !_gsxp.Attrs.Has(name)` so the caller's bag value wins (root never folded into the bag → no escaping lost). `class` is already caller-last-wins; `style` gets a new `gw.StyleMerged` (property-level last-wins dedupe via a robust stdlib declaration splitter). Manual `{...attrs}` splits root attrs at the spread's source position into overridable (guarded) and forced (unguarded + excluded from the bag).

**Tech Stack:** Go; **stdlib-only runtime** (the `StyleMerged` splitter is hand-written, NOT `tdewolff`). Reuses landed building blocks: `defaultClassMerge` (last-wins), `ClassString`, `StyleValue`, `Attrs.Merge`/`Has`/`Without`/`Class()`, `ClassMerged`, `gw.Spread`.

## Global Constraints

- Runtime is **stdlib-only**. Threat model unchanged (template authors trusted; data not). The **bag keeps its `AttrValue` contract** (author-trusted) — do NOT context-escape bag values. Root attrs keep their direct context escaper (`gw.URL`/`gw.JSValAttr`/CSS/`gw.AttrValue`).
- **Empty/absent bag → byte-identical output to today** (`Attrs.Has` is nil-safe; preserve the no-op property — pin it with a corpus case).
- Precedence per attr name: **forced root (post-`{...attrs}`) > bag > overridable root (pre-`{...attrs}` / all of auto mode)**. Auto mode has no forced tier.
- `class`/`style` are merged caller-last (not replaced); other attrs single-winner.
- `internal/codegen/emit.go` threads `cls *attrclass.Classifier` — keep threading it.
- After each task: `go build ./...` and `go test ./...` pass. Bump `internal/codegen/version.go` when emitted code changes.

---

### Task 1: Runtime `gsx.StyleString` + `Attrs.Style()`

**Files:** Modify `class.go` (add `StyleString` next to the landed `ClassString`); `attrs.go` (add `Attrs.Style()` next to `Attrs.Class()`). Test `class_test.go`, `attrs_test.go`.

**Interfaces — Produces:**
- `func StyleString(parts ...ClassPart) string` — value-form of `gw.Style`: the included parts' values joined with `"; "`, NO attr-escaping (the caller — `StyleMerged` — escapes). Mirrors `ClassString` (value-form of `gw.Class`).
- `func (a Attrs) Style() string` — the bag's `"style"` entry as a string, or `""`. Mirrors `Attrs.Class()` (`attrs.go:66`) but WITHOUT running a merger (style is a declaration string, not tokens).

- [ ] **Step 1: Failing tests.** In `class_test.go`:
```go
func TestStyleString(t *testing.T) {
	// gw.Style includes a part only when its .on is true; joins decls with "; ".
	if got := gsx.StyleString(gsx.Class("color: red"), gsx.ClassIf("margin: 0", false), gsx.Class("padding: 1px")); got != "color: red; padding: 1px" {
		t.Errorf("StyleString = %q, want \"color: red; padding: 1px\"", got)
	}
}
```
In `attrs_test.go`:
```go
func TestAttrsStyle(t *testing.T) {
	if got := (gsx.Attrs{"style": "color: red"}).Style(); got != "color: red" {
		t.Errorf("Style() = %q, want \"color: red\"", got)
	}
	if got := (gsx.Attrs{}).Style(); got != "" {
		t.Errorf("empty Style() = %q, want \"\"", got)
	}
}
```
- [ ] **Step 2: Run** `go test .` — both FAIL (undefined).
- [ ] **Step 3: Implement.** Read `gw.Style` (`class.go:81`) — it builds `decls` from on-parts (`strings.TrimSpace(p.s)` when `p.on` and non-empty) and joins with `"; "`. `StyleString` returns that joined string (the value, pre-escape):
```go
// StyleString returns the merged style declaration string for parts (the value
// form of gw.Style), so generated code can pass a composed root style to
// StyleMerged. Like gw.Style it includes only on-parts and joins with "; ", but
// does NOT attr-escape (the caller escapes).
func StyleString(parts ...ClassPart) string {
	decls := make([]string, 0, len(parts))
	for _, p := range parts {
		if !p.on {
			continue
		}
		if d := strings.TrimSpace(p.s); d != "" {
			decls = append(decls, d)
		}
	}
	return strings.Join(decls, "; ")
}
```
(If `gw.Style` already factors its decl-building into a helper, reuse it for DRY instead of duplicating; read `class.go:81` and decide.)
Read `Attrs.Class()` (`attrs.go:66`) and add `Attrs.Style()`:
```go
// Style returns the bag's "style" declaration string, or "".
func (a Attrs) Style() string {
	v, ok := a["style"]
	if !ok {
		return ""
	}
	return toStr(v)
}
```
- [ ] **Step 4: Run** `go test .` then `go test ./...` green.
- [ ] **Step 5: Commit** `runtime: gsx.StyleString (value form of gw.Style) + Attrs.Style()`.

---

### Task 2: Runtime `gw.StyleMerged` + the declaration splitter (+ fuzz)

**Files:** Create `style_merge.go` (root `gsx` package: `StyleMerged` + `splitDecls` + `declProp`); Test `style_merge_test.go`; Fuzz in the same test file.

**Interfaces — Consumes:** `gw.AttrValue` (the existing attr escaper), `gw.writeStr`/`gw.err`. **Produces:** `func (gw *Writer) StyleMerged(rootStyle, bagStyle string)` — emits a complete ` style="…"` (or nothing when the merged result is empty).

- [ ] **Step 1: Failing tests** `style_merge_test.go`:
```go
package gsx

import "strings"

import "testing"

func styleMerged(root, bag string) string {
	var b strings.Builder
	W(&b).StyleMerged(root, bag)
	return b.String()
}

func TestStyleMerged(t *testing.T) {
	for _, tt := range []struct{ root, bag, want string }{
		{"color: red; margin: 0", "color: blue", ` style="margin: 0; color: blue"`}, // dedupe, caller last
		{"a: 1; a: 2", "", ` style="a: 2"`},                                          // within-string last-wins
		{"color: red", "", ` style="color: red"`},
		{"", "", ""},                                                                  // empty -> no attr
		{"", "color: blue", ` style="color: blue"`},
		// robust splitter: ; and : inside url()/quotes are NOT boundaries
		{"background: url(data:image/png;base64,AA;BB)", "", ` style="background: url(data:image/png;base64,AA;BB)"`},
		{`content: "a; b"; color: red`, "color: blue", ` style="content: &#34;a; b&#34;; color: blue"`},
	} {
		if got := styleMerged(tt.root, tt.bag); got != tt.want {
			t.Errorf("StyleMerged(%q,%q) = %q, want %q", tt.root, tt.bag, got, tt.want)
		}
	}
}

func FuzzStyleMerged(f *testing.F) {
	f.Add("color:red; margin:0", "color:blue")
	f.Add("background:url(data:x;base64,AA;BB)", "")
	f.Add(`content:"a;b"`, "color:red")
	f.Fuzz(func(t *testing.T, root, bag string) {
		once := styleMerged(root, bag)
		// idempotence: re-merging the already-merged value (sans the ` style="`/`"` wrapper) is stable.
		inner := strings.TrimSuffix(strings.TrimPrefix(once, ` style="`), `"`)
		twice := styleMerged(inner, "")
		// twice's inner must equal once's inner (no further change); compare unwrapped.
		t2 := strings.TrimSuffix(strings.TrimPrefix(twice, ` style="`), `"`)
		if inner != "" && t2 != inner {
			t.Fatalf("not idempotent: %q -> %q -> %q", root+"|"+bag, inner, t2)
		}
	})
}
```
(Note: the `content: "a; b"` expectation has the quote attr-escaped to `&#34;` because `StyleMerged` ends with `gw.AttrValue`. Verify the exact entity in the test once implemented; the principle is the inner `;` was NOT a declaration boundary so `content` stayed one declaration.)
- [ ] **Step 2: Run** `go test . -run TestStyleMerged` — FAIL (undefined).
- [ ] **Step 3: Implement** `style_merge.go`:
```go
package gsx

import "strings"

// splitDecls splits a CSS declaration list into trimmed declarations, treating
// ';' as a separator only when not nested in () and not inside a quote — so a ';'
// inside url(data:…;base64,…) or a quoted string is NOT a boundary.
func splitDecls(s string) []string {
	var decls []string
	depth := 0
	var quote byte // 0, '\'' or '"'
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			if d := strings.TrimSpace(s[start:i]); d != "" {
				decls = append(decls, d)
			}
			start = i + 1
		}
	}
	if d := strings.TrimSpace(s[start:]); d != "" {
		decls = append(decls, d)
	}
	return decls
}

// declProp returns the lower-cased property name of a declaration (text before
// the first ':' that is not nested in () nor inside a quote), or "" if there is
// no such ':' (a malformed fragment).
func declProp(decl string) string {
	depth := 0
	var quote byte
	for i := 0; i < len(decl); i++ {
		c := decl[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ':' && depth == 0:
			return strings.ToLower(strings.TrimSpace(decl[:i]))
		}
	}
	return ""
}

// StyleMerged emits a merged ` style="…"` attribute combining rootStyle then
// bagStyle (caller last), deduping by property keeping the LAST occurrence,
// survivors in source order. A malformed fragment (no ':') is dropped. When the
// merged result is empty it emits nothing (matching the empty-bag no-op).
func (gw *Writer) StyleMerged(rootStyle, bagStyle string) {
	if gw.err != nil {
		return
	}
	decls := append(splitDecls(rootStyle), splitDecls(bagStyle)...)
	lastIdx := make(map[string]int, len(decls))
	for i, d := range decls {
		if p := declProp(d); p != "" {
			lastIdx[p] = i
		}
	}
	out := make([]string, 0, len(decls))
	for i, d := range decls {
		p := declProp(d)
		if p == "" {
			continue
		}
		if lastIdx[p] == i {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return
	}
	gw.writeStr(` style="`)
	gw.AttrValue(strings.Join(out, "; "))
	gw.writeStr(`"`)
}
```
- [ ] **Step 4: Run** `go test . -run 'TestStyleMerged'` (green), then `go test . -run 'FuzzStyleMerged' -fuzz FuzzStyleMerged -fuzztime 20s` (no panic / no idempotence failure), then `go test ./...`.
- [ ] **Step 5: Commit** `runtime: gw.StyleMerged property-level last-wins style merge (robust stdlib splitter) + fuzz`.

---

### Task 3: Auto-mode caller-wins (`emitRootElement` rewrite)

**Files:** Modify `internal/codegen/emit.go` (`emitRootElement` ~line 299; remove `rootWithoutArgs` ~406 and the root-wins `Without` call); re-baseline `internal/corpus/testdata/cases/jsattr/root_wins.txtar`; add corpus cases.

**Interfaces — Consumes:** `Attrs.Has` (nil-safe), `ClassMerged` (bag-last + last-wins → already caller-wins), `StyleMerged`/`StyleString`/`Attrs.Style()` (Tasks 1-2), the existing per-attr context emit (`emitAttr` and its `cls.Context` branches). **Produces:** auto-mode single-root fallthrough is caller-wins.

**Design — guarded direct emit.** Read the current `emitRootElement` (it streams each root attr via `emitAttr`, specially merges class, then `Spread(ctx, _gsxp.Attrs.Without(rootWithoutArgs(el)))` = root-wins). Rewrite so:
- Each NON-class/style root attr emits via its existing context path BUT wrapped in a runtime guard so the caller wins:
  ```go
  b.WriteString("\t\tif !_gsxp.Attrs.Has(" + strconv.Quote(attrName) + ") {\n")
  // ... the existing emitAttr(...) output for this attr ...
  b.WriteString("\t\t}\n")
  ```
  (For a `BoolAttr`/`StaticAttr`/`ExprAttr`/JSAttr the inner body is exactly what `emitAttr` already emits — keep `emitAttr` for the body; just bracket it with the guard. A `SpreadAttr` here is the manual case → Task 4, not this path.)
- `class`: keep the existing class-merge emission (`emitRootComposedClass`/`emitRootStaticClass`/`ClassMerged(_gsxp.Attrs.Class(), …)`), which already appends the bag class LAST → with last-wins `defaultClassMerge` it is caller-wins. Do NOT guard class (it MERGES, not replaced).
- `style`: replace the class-like style handling with `_gsxgw.StyleMerged(<root style string>, _gsxp.Attrs.Style())` — where `<root style string>` is `strconv.Quote(staticVal)` for a static `style="x"`, or `gsx.StyleString(<parts…>)` for a composed `style={ … }` (lower the parts exactly as the composed-style path does, to the StyleString call). If the root has NO style attr, still emit `_gsxgw.StyleMerged("", _gsxp.Attrs.Style())` so a caller-only style appears (StyleMerged emits nothing when both are empty → no-op).
- The bag spread becomes `_gsxgw.Spread(ctx, _gsxp.Attrs.Without("class", "style"))` (only class/style excluded now — every other bag attr spreads, caller-wins for the ones that shadow a guarded root attr). DELETE `rootWithoutArgs` and its call.

`Attrs.Has` nil-safe ⇒ empty bag → every guard true, `ClassMerged`/`StyleMerged` with empty bag add nothing, `Spread(empty)` writes nothing ⇒ byte-identical to today.

- [ ] **Step 1: Re-baseline + new corpus (failing).** `jsattr/root_wins.txtar`: the expectation FLIPS — a bag attr that shadows a root attr now shows the CALLER's value (rename to `caller_wins.txtar` or keep the name with a flip comment; surface it). Add: `class/caller_wins_scalar.txtar` (root `<a href="/default" onclick="track()">` + bag `href="/custom"` → `href="/custom"`, `onclick="track()"`); `class/caller_wins_classstyle.txtar` (root `class="card" style="color:red"` + bag `class="featured" style="color:blue; margin:0"` → `class="card featured"`, `style="color:blue; margin:0"` — caller color wins, no duplicate `color`); `class/fallthrough_url_preserved.txtar` (root `<a href={ u }>` with `u = "javascript:alert(1)"`, NO bag override → render shows `about:invalid#gsx` — guard kept `gw.URL`); `class/empty_bag_noop.txtar` (root element, no fallthrough → byte-identical to the plain element).
- [ ] **Step 2: Run** `go test ./internal/corpus/` — FAILs (root-wins output).
- [ ] **Step 3: Implement** the rewrite. Bump `internal/codegen/version.go`.
- [ ] **Step 4: Run** `go test ./internal/corpus/` then `go test ./...`. The JS-interp corpus (`script`/`jsattr`/`datajson`) must stay green; inspect the new goldens (esp. `fallthrough_url_preserved` → `about:invalid#gsx`, and `empty_bag_noop` byte-parity).
- [ ] **Step 5: Commit** `codegen: auto-mode attribute fallthrough is caller-wins (guarded direct emit + StyleMerged); re-baseline root_wins`.

---

### Task 4: Manual `{...attrs}` positional (forced / overridable)

**Files:** Modify `internal/codegen/emit.go` (the manual-spread element path — where a `*ast.SpreadAttr` referencing the bag is emitted, ~line 806 `gw.Spread(ctx, attrs)`); add corpus.

**Interfaces — Consumes:** Task-3's guarded-emit + `Attrs.Without`. **Produces:** root attrs before `{...attrs}` are overridable (caller wins); after are forced (root wins).

**Design.** Today manual mode emits the root via normal `genNode`/`emitAttr` and the `*ast.SpreadAttr` emits `_gsxgw.Spread(ctx, attrs)` inline at its position — precedence is whatever the browser does with emission order (first-dup-wins), which does NOT give forced semantics. Rewrite the manual-spread root element so its attr emission becomes position-aware:
- Find the `*ast.SpreadAttr` whose expr is the bag (`attrs`). (Detect: a `SpreadAttr` whose trimmed `Expr == "attrs"`, the bound bag name; a `{...someOtherExpr}` keeps today's inline `gw.Spread`.)
- Root attrs BEFORE it → overridable: emit each guarded `if !_gsxp.Attrs.Has(name)` (same as Task 3).
- Root attrs AFTER it → forced: emit each UNGUARDED (always), and collect their names.
- class/style: merge as in Task 3 (`ClassMerged`/`StyleMerged`) regardless of position (they always merge; a forced class/style after the spread still merges caller-last — acceptable, or document that forced applies only to scalar attrs; keep class/style merging).
- The `{...attrs}` itself → `_gsxgw.Spread(ctx, _gsxp.Attrs.Without("class", "style", <forcedNames…>))` at its position — the forced names are excluded so the bag can't emit them (the unguarded root emit wins).
- **Multiple `{...attrs}` on one element** → a clear codegen error: `fmt.Errorf("codegen: more than one {...attrs} spread on an element; precedence is ambiguous")`.

- [ ] **Step 1: Failing corpus.** `jsattr/manual_forced.txtar`: `<div {...attrs} role="dialog">` + bag `role="alert"` → render `role="dialog"` (forced). `jsattr/manual_overridable.txtar`: `<div id="x" {...attrs}>` + bag `id="y"` → `id="y"` (caller wins). `jsattr/manual_multi_spread_rejected.txtar`: an element with two `{...attrs}` → `diagnostics.golden` with the ambiguity error (model on an existing error-case `.txtar`).
- [ ] **Step 2: Run** `go test ./internal/corpus/` — FAILs.
- [ ] **Step 3: Implement.** Factor the guarded-emit + class/style-merge + Spread logic shared with Task 3 into a helper if it reduces duplication (e.g. `emitFallthroughAttrs(b, el, splitIndex, cls, …)` where splitIndex = the SpreadAttr position, or `len(el.Attrs)` for auto mode = all overridable). Bump `internal/codegen/version.go` if not already this cycle.
- [ ] **Step 4: Run** `go test ./internal/corpus/` then `go test ./...` green; inspect the manual goldens.
- [ ] **Step 5: Commit** `codegen: manual {...attrs} positional precedence (post-spread forced); shared fallthrough-emit helper`.

---

## Self-Review

**Spec coverage:** §2 precedence → T3 (auto, all-overridable) + T4 (positional forced/overridable); §3 guarded direct emit → T3/T4; §4 `StyleMerged`+splitter+`StyleString`+`Attrs.Style()` → T1/T2; §6 multi-spread error → T4; §7 tests → each task's units + corpus, incl. URL-preserved + empty-bag-noop + root_wins flip; §8 fuzz → T2.

**Placeholder scan:** the substantive logic (`StyleMerged`/`splitDecls`/`declProp`, `StyleString`, the guard pattern, the Spread trim) is given as complete code or an exact pattern; T3/T4 reference the real `emitRootElement` for the surrounding structure (the one place byte-exact code needs the live file) with the guard/merge/spread changes fully specified.

**Type/name consistency:** `StyleString`/`Attrs.Style()`/`StyleMerged`/`splitDecls`/`declProp` consistent T1→T4; `Attrs.Has`/`Without`/`ClassMerged` reused as landed; the guard `if !_gsxp.Attrs.Has(name)` identical in T3/T4; `cls *attrclass.Classifier` threaded throughout.

## Risks
- **The splitter** is the only real logic — gated by the edge-case table + the no-panic/idempotence fuzz (T2).
- **URL/JS escaping preserved** — structural (root attrs never enter the bag), proven by `fallthrough_url_preserved.txtar` (T3).
- **Empty-bag byte-parity** — `Has` nil-safe + empty `ClassMerged`/`StyleMerged`/`Spread` no-ops; pinned by `empty_bag_noop.txtar` (T3).
- **`root_wins.txtar` flip** is intentional — re-baselined and surfaced, not silently changed (T3 Step 1).
- **Manual-mode `attrs`-expr detection** — only route the split-merge when the `SpreadAttr` expr is the bag name; a `{...otherExpr}` keeps inline `gw.Spread` (T4).
