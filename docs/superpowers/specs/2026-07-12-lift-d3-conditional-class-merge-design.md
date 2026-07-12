# Lift D3 — conditional class/style on forwarding elements merges — design

- **Date:** 2026-07-12
- **Status:** approved-direction, pre-plan
- **Depends on:** #91 (multi-spread merge) — reuses the fold (`foldElementSpreads`/`composeBag`). Stacked follow-up.
- **Reverses:** decision **D3** of `2026-07-02-attrs-forwarding-hardening-design.md`.

## Summary

A `class`/`style` inside an `{ if … }` cond-attr on a **forwarding** element (one carrying a spread) is currently a generate-time error (decision D3), redirecting authors to the composable form. This restores the original "every class is a merge target" model: such an element **folds** — the conditional class/style becomes an `AttrsCond` bag entry that aggregates at the leaf via `Attrs.Class()`/`Attrs.Style()`, exactly as multi-spread merge (#91) already does.

## Motivation

The original design (2026-06-20 composable-attrs plan) intended `{ if cond { class="x" } }` to emit and merge — it shipped as a test. The user-facing mental model is uniform: **a caller's class/style flows into the leaf and every source is a merge target.** D3 (2026-07-02) carved out one exception — a conditional class/style on a *forwarding* element — because the root's class/style merge is emitted once at a static site and a conditional contribution can't join it *without lowering the attribute section to a runtime bag, which (per sibling decision D2) loses per-context escaping — a security regression.*

**That D2 premise is stale post-#79** and is precisely what #91 removed: the leaf `Spread` routes every bag key through the same tag-aware URL/image/srcset sinks a static attribute uses, so a folded value is sanitized identically to a compile-time literal (byte-identical). #91 already reversed D2's *other* consequence (at-most-one-spread). D3 is the remaining half of that hardening PR, still standing on the dead premise.

## The three conditional class/style forms (verified current behavior)

| # | Form | Syntax | On a forwarding element today |
|---|---|---|---|
| 1 | Composable `:bool` part | `class={ "a", "on": cond }` | **merges** (fold + non-fold) |
| 2 | `if`/`else` cond-attr | `{ if active { class="on" } else { class="off" } }` | **D3-rejected** ← lift target |
| 3 | Composable `switch` part | `class={ switch { case v>0: "pos" default: "neg" } }` | **merges** (fold + non-fold) |

Notes:
- Forms 1 and 3 are composable `class={…}` *parts* (`ast.ClassPart.Cond` / `ast.ValueSwitch`), lowered by `classEntryExpr`. They are NOT D3-rejected and already merge with a spread and through the fold.
- An attr-level `{ switch … }` (a switch analog of the `if` cond-attr, holding `class=`) **does not exist** — the parser rejects `{ switch … }` in attribute position (`expected '...' trailing spread inside { } attribute`). So there is no Form-2 switch. Adding one is a **non-goal**.
- All three forms, plus a spread, already merge **together** on a folded (≥2-spread) element (probe: `class="base on pos sp hot"`). Style aggregates identically (`style="color:red; margin:0; font-weight:bold"`).

## Design

### Route Form-2 forwarding elements through the fold

Extend the `elementFolds` predicate (`internal/codegen/emit.go`) so an element also folds when it carries **a spread AND a cond-attr containing a `class`/`style` leaf** — the exact shape D3 rejects. Add a helper `hasCondClassStyle(attrs []ast.Attr) bool` that walks top-level attrs for an `*ast.CondAttr` whose `Then`/`Else` (recursively, incl. else-if) contains a `class`/`style` leaf (`ClassAttr`/`StaticAttr`/`EmbeddedText` with name class/style). Combined predicate:

```
elementFolds = (>=2 spreads)
            || (one cond-nested spread && hasRootClassStyle)   // existing (O1)
            || (has a spread && hasCondClassStyle)              // NEW (this change)
```

Once such elements fold, `composeBag` lowers the cond-attr via `condAttrsExpr` → `AttrsCond`, whose branch class/style becomes a bag `{Key:"class"/"style", Value}` entry; the leaf aggregates all class/style across the bag. **No new lowering logic** — this is the machinery #91 shipped and this spec's probes exercised.

### Delete the D3 validation

Remove the D3 rejection (the static cond-attr validation at `emit.go:876-909` that errors "conditional class inside { if } … cannot join the class merge"). After the trigger change every forwarding-element Form-2 shape folds, so the validation is dead. Confirm via `gopls check` that no path still reaches it; if a non-forwarding path shared it, keep only that path.

### Prescan consistency

`elementFolds` is shared with `scopeUsesNumeric` (the `_gsxnum` prescan). Widening it means a Form-2 forwarding element now folds → numeric attrs bake into the bag (no `_gsxnum`), and the prescan already skips folded elements. Extend the same shared predicate so both agree (the O1/I1 discipline). Probe a Form-2 element that also has a numeric attr to confirm no unused/undefined temp.

## Scope

- **class and style** both (both proven to fold-merge).
- **Forwarding elements only.** A conditional class/style on a *non-forwarding* element already emits directly via the faster static path (original 2026-06-20 behavior, pinned by `attrs/cond_attr_bool_on`) — unchanged. The two paths converge on "always mergeable."
- **Non-goal:** an attr-level `{ switch … }` cond-attr (doesn't parse; not added here).
- **Known edge (non-blocking):** an element that *also* carries a `js`/`css` embedded-hole attr trades the D3 rejection for the #92 rejection (`composeBag`'s remaining gap). Rare combination; both fail-closed with an accurate diagnostic.

## Performance

Folding routes the element through one `ConcatAttrs` allocation + the leaf instead of the compile-time static path. This is the *one* surviving fragment of D2/D3's rationale ("allocates on every render") — but it is small, only affects elements that opt into conditional-class + forwarding, and is the same cost #91 already accepted. **Benchmark** a representative nav-tab-style component (folded vs the pre-change static+composable form) and record the delta; it is not expected to block.

## Test plan

Corpus (`internal/corpus/testdata/cases/`), goldens pinned + hand-verified against source-order merge:
- **Flip** `fallthrough/cond_attr_class_rejected.txtar` from rejection to render (both branches).
- Form-2 **class**: `{ if a { class="on" } else { class="off" } }` + a spread → merges root + cond + spread class, both branches.
- Form-2 **style**: same with `style`.
- **All-forms combination**: composable `class={ "base", "on": c, switch {…} }` + a Form-2 cond-attr class + a spread → one merged class (the `base on pos sp hot` shape).
- **Numeric coexistence**: Form-2 forwarding element + a numeric attr → compiles (prescan tie).
- **Non-forwarding unchanged**: a Form-2 conditional class with NO spread → still the static path, golden byte-identical to today.
- **js/css-hole edge**: Form-2 element + a `js` hole → the #92 diagnostic (pinned).
- **Differential** (#91's `TestSpreadFoldDiff` + `FuzzAttrsFoldMatchesReference`) stays green; extend the codegen matrix with a Form-2 shape.
- **Adversarial probes** (independent reviewer): a conditional class carrying `javascript:`-style values through the fold sanitizes at the leaf; source-order last-wins across static + composable + conditional + spread.

## Sibling repos

No grammar change (all three forms already parse; only codegen behavior changes). Guide `docs/guide/syntax/composition.md` updated to state that a conditional `class`/`style` in an `{ if }` merges (drop the "use the composable form" workaround note).
