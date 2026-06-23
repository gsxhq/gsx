# Design: caller-wins attribute fallthrough (context-aware merge)

**Date:** 2026-06-23
**Status:** Approved (brainstorm), pending implementation plan
**Builds on:** `fd41b63` (last-wins `defaultClassMerge`, `gsx.ClassString`, `gsx.StyleValue`, the CSS-attr unlock). **Completes** the attribute-merge work whose precedence flip was deferred from that slice.

---

## 1. Goal & scope

Flip attribute fallthrough from **root-wins** to **caller-wins** (the JSX last-wins model), so a caller's attributes override a component's root defaults — **while every root attribute keeps its context-specific escaping** (`gw.URL` scheme-sanitize, `gw.JSValAttr` double-escape, CSS filtering, `gw.AttrValue`). Plus a positional escape hatch via the existing manual `{...attrs}` spread, and proper property-level `style` merging.

**Why the prior slice deferred this:** the tempting "build root attrs into a `gsx.Attrs` literal → `Attrs.Merge(bag)` → `gw.Spread`" approach is **unsafe**, because `gw.Spread` does only plain `AttrValue` escaping (`attrs.go:83`, documented "NOT URL-sanitized") — folding URL/JS root attrs through it would silently drop their context escaping. This design avoids that by **never moving root attrs into the bag**; they stay direct, context-aware emits.

**In scope:** the precedence flip (auto + manual modes); per-attr guards preserving context escaping; `gw.StyleMerged` with a robust stdlib declaration splitter (property-level last-wins dedupe); the manual `{...attrs}` positional forced/overridable split; re-baselining the root-wins tests.

**Out of scope:** changing the bag's own escaping contract (it stays author-trusted `AttrValue`); pipeline transforms; the attr-classification extension (already landed).

**Global constraints:** runtime is **stdlib-only** (so `StyleMerged`'s splitter is hand-written, NOT `tdewolff` — that's codegen-only). Threat model unchanged: template authors (component AND caller) trusted; interpolated *data* is not — so a root attr's escaping (which guards against tainted data the root author interpolated) is preserved; the bag is author-written and keeps its existing `AttrValue` contract. No regression to shipped escaping or the JS-interp/CSS corpus.

---

## 2. Precedence model (JSX, positional)

Per attribute name, the winner is resolved by tier:

| Tier | Source | Beats |
|---|---|---|
| **forced** | root attrs written *after* `{...attrs}` (manual mode only) | everything |
| **bag** | the caller's fallthrough `gsx.Attrs` | overridable root attrs |
| **overridable** | root attrs written *before* `{...attrs}`, or ALL root attrs in auto mode | — |

- **Auto mode** (no `{...attrs}` in the markup): no forced tier — the caller wins on every attribute. Least-surprising default.
- **Manual mode** (`{...attrs}` written): position decides, exactly like JSX `<div a {...props} b/>`. Attrs before the spread are overridable (caller wins); attrs after are forced (root wins).

`class` and `style` are **merged**, not replaced (caller-last); all other attrs are single-winner (loser dropped — HTML has no duplicate attributes).

---

## 3. Architecture — guarded direct emit

Root attrs stay where they are (direct, context-aware emit), each **guarded** so the caller's bag value wins:

```go
// auto mode, per root attr, KEEPING its own context escaper:
if !_gsxp.Attrs.Has("href") {                       // caller didn't override
    _gsxgw.S(` href="`); _gsxgw.URL(hrefExpr); _gsxgw.S(`"`)   // gw.URL preserved
}
if !_gsxp.Attrs.Has("onclick") { … _gsxgw.JSValAttr(…) … }    // JS preserved
// class — already caller-last-wins (ClassMerged appends the bag class LAST + last-wins defaultClassMerge)
_gsxgw.ClassMerged(_gsxp.Attrs.Class(), <root class parts…>)
// style — property-level merge, caller-last:
_gsxgw.StyleMerged(<root style string>, _gsxp.Attrs.Style())
// the remaining caller attrs (author-trusted AttrValue), minus what's handled above:
_gsxgw.Spread(ctx, _gsxp.Attrs.Without("class", "style"))
```

- `Attrs.Has(name)` is **nil-safe** (nil map → false), so an empty/absent bag → every guard true → **byte-identical to today** (the no-op property is preserved).
- The caller wins because a root attr emits *only when the bag lacks that name*; the bag's value for that name is emitted by `Spread`.
- **No root attr ever enters the bag** → no escaping is lost. Each root attr keeps `gw.URL`/`gw.JSValAttr`/`gw.AttrValue`/CSS exactly as today.

**Manual mode** (`{...attrs}` `SpreadAttr` present, expr is the `attrs` bag): split the root's attrs at the spread's source index.
- *overridable* (before the spread): guarded with `!Has(name)` as above.
- *forced* (after the spread): emitted **unguarded** (always win), and their names are added to the bag's `Without(...)` so `Spread` can't also emit them (avoids an HTML duplicate where the browser would take the bag's, defeating "forced").
- The `{...attrs}` itself becomes the `Spread(ctx, attrs.Without("class","style", <forcedNames>))` call at its position.

**Codegen** (`internal/codegen/emit.go`): `emitRootElement` (auto) and the manual-spread element path are rewritten to emit guarded attrs + the merged class/style + the trimmed `Spread`. The `cls *attrclass.Classifier` already threaded stays threaded (each guarded attr emits through `emitAttr`/its context branch). `rootWithoutArgs` (today's root-wins exclusion) is removed; the bag now only excludes `class`/`style`/forced-names.

---

## 4. `gw.StyleMerged` + the declaration splitter (the one new runtime unit)

```go
func (gw *Writer) StyleMerged(rootStyle, bagStyle string)   // emits a merged style="…"
func (a Attrs) Style() string                               // the bag's "style" value, or ""
func StyleString(parts ...ClassPart) string                 // value-form of gw.Style (for a composed root style)
```

`StyleString` is the value form of `gw.Style` (parallel to the landed `ClassString` for `gw.Class`): it returns the included parts joined with `; ` **without** attr-escaping (`StyleMerged` escapes the final result). Codegen uses it to obtain the root's `style` as a string for the merge: a static `style="x"` passes `"x"`; a composed `style={ … }` passes `gsx.StyleString(<parts…>)`. (The per-part `gsx.StyleValue` from `fd41b63` still filters/RawCSS-opts-out each dynamic part inside the parts.)

`StyleMerged` concatenates `rootStyle` then `bagStyle` (caller last), splits into declarations, **dedupes by property name keeping the LAST occurrence** (last-wins — the caller, and within either string the later declaration), preserving surviving declarations in order, then attr-escapes the joined `prop: value; …`. Example: `StyleMerged("color: red; margin: 0", "color: blue")` → `style="margin: 0; color: blue"`.

**The splitter is stdlib-only and robust** (NOT a naive `;`/`:` split). It scans a declaration string tracking:
- `()` nesting depth — so `;`/`:` inside `url(data:image/png;base64,…)` are not boundaries;
- quote state (`'`/`"`) — so `;`/`:` inside `content: "a; b"` are not boundaries;
- the **first** unnested, unquoted `:` separates property from value; an unnested, unquoted `;` ends a declaration.
Property name is trimmed + lower-cased for the dedupe key (CSS property names are case-insensitive); the emitted value/casing is the surviving declaration's original text. Malformed fragments (no `:`) are dropped (defensive; can't be a valid declaration).

This mirrors what `defaultClassMerge` does for class tokens, at the CSS-property granularity. It is independently unit-testable against the edge cases.

`Attrs.Style()` mirrors the existing `Attrs.Class()` (returns the bag's `style` entry as a string, or `""`).

---

## 5. Components & data flow

```
parse → (manual? = an {...attrs} SpreadAttr referencing the bag) →
  codegen emitRootElement / manual path:
    for each root attr: emit via its context escaper, guarded by !Attrs.Has(name)
       (forced/post-spread attrs: unguarded + name added to bag Without)
    class: ClassMerged(Attrs.Class(), root parts)        [already caller-last-wins]
    style: StyleMerged(root style, Attrs.Style())         [new: property dedupe, last-wins]
    rest:  Spread(ctx, Attrs.Without("class","style", forcedNames))
  runtime: Has (nil-safe) / ClassMerged / StyleMerged (split+dedupe) / Spread
```

Units, each independently testable:
- **`StyleMerged` + splitter** (runtime, new) — property-level last-wins merge; unit tests + edge cases.
- **`Attrs.Style()`** (runtime, new, trivial) — mirror of `Attrs.Class()`.
- **`StyleString`** (runtime, new, trivial) — value-form of `gw.Style`, mirror of the landed `ClassString`; lets codegen pass a composed root `style` to `StyleMerged`.
- **`emitRootElement` / manual-spread path** (codegen, rewritten) — guarded emit + merged class/style + trimmed Spread; corpus render goldens.
- Reused unchanged: `Attrs.Has`/`Without`/`Spread`/`ClassMerged`, the context escapers.

---

## 6. Error handling

- **Multiple `{...attrs}` spreads** on one element → a clear codegen error ("ambiguous: more than one `{...attrs}` spread") — precedence would be undefined.
- The auto-eligibility gate is unchanged (single root, no `CondAttr`/`SpreadAttr` on the root → auto; an `{...attrs}` makes it manual). A fallthrough attr onto a non-eligible multi-root/fragment still surfaces the existing Go unknown-field error.
- `StyleMerged` never errors: a malformed declaration fragment is dropped; empty inputs → empty (no `style` attr emitted when the merged result is empty, matching the empty-bag no-op).
- A bag attribute name that is not a valid HTML attr name is still dropped by `Spread`'s `validAttrName` guard (unchanged).

---

## 7. Testing

- **Runtime unit (`StyleMerged` + splitter):** `StyleMerged("color:red; margin:0", "color:blue")` → `"margin:0; color:blue"` (dedupe, caller last); within one string `"a:1; a:2"` → `"a:2"`; **edge cases** `"background: url(data:image/png;base64,AA;BB)"` (inner `;`/`:` not boundaries), `"content: \"a; b\"; color: red"` (quoted `;`), empty inputs → `""`. `Attrs.Style()` returns the bag style or `""`.
- **Corpus render goldens** (the real surface):
  - *auto caller-wins scalar:* `<a href="/default" onclick="track()">` invoked with bag `href="/custom"` → render `href="/custom"` (caller), `onclick="track()"` (root kept).
  - *auto class+style merge:* root `class="card" style="color:red"` + bag `class="featured" style="color:blue; margin:0"` → `class="card featured"`, `style="color:blue; margin:0"` (caller color wins, no duplicate `color`).
  - *URL escaping preserved under override-miss:* a root `href={ userVar }` NOT overridden by the bag → still `gw.URL`-sanitized (a `javascript:` value → `about:invalid#gsx`); a render case proves the guard keeps the escaper.
  - *manual forced:* `<div {...attrs} role="dialog">` + bag `role="alert"` → `role="dialog"`.
  - *manual overridable:* `<div id="x" {...attrs}>` + bag `id="y"` → `id="y"`.
  - *empty-bag no-op:* a component with a root, invoked with no fallthrough → byte-identical to the plain element.
- **Re-baseline:** `internal/corpus/testdata/cases/jsattr/root_wins.txtar` and any test asserting root-wins precedence — the expectation **flips to caller-wins**; surface each in the plan, don't silently update. Bump `internal/codegen/version.go`.
- Whole suite green; `go vet ./...` clean.

---

## 8. Risks

- **The declaration splitter** is the one piece of real logic — gated by the edge-case unit tests (`url()`, quotes, base64 data URIs). Seed a fuzz target (no-panic + idempotence: `StyleMerged(StyleMerged(a,b), "") == StyleMerged(a,b)`).
- **URL/JS escaping must NOT be lost** — guaranteed structurally (root attrs never enter the bag; they keep their direct context emit). The "URL preserved under override-miss" render case is the proof.
- **Manual-mode position split** by `SpreadAttr` source index; multiple spreads → error (§6).
- **Byte-parity for the empty/absent bag** (the no-op property) — a corpus case pins it.
- **`root_wins.txtar` flip** is an intentional behavior change — re-baselined and called out, not silently updated.
