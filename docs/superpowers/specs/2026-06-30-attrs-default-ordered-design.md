# Ordered `Attrs` by default; map becomes `AttrMap`

- **Date:** 2026-06-30
- **Status:** Design (approved direction; pending spec review)
- **Supersedes framing of:** `2026-06-29-ordered-attrs-design.md` (the `OrderedAttrs` opt-in). That feature shipped a *second* bag type; this design promotes the ordered slice to be **the** bag type and demotes the map to a convertible convenience.

## 1. Motivation

Today gsx has two attribute-bag types with inverted ergonomics:

- `gsx.Attrs = map[string]any` — the default. The implicit fallthrough bag and every
  declared bag prop. `Spread` renders it **alphabetically sorted** (a Go map has no
  order), which is surprising: `<Field type="email" placeholder="x"/>` emits
  `placeholder` before `type`.
- `gsx.OrderedAttrs = []Attr` — an opt-in (the `{{ "k": v }}` literal) for callers who
  must control order (Datastar `data-*` directives, duplicate keys).

This forces a component author to *predict* at declaration time whether a caller will
care about order. A prop declared `gsx.Attrs` (map) can never receive an ordered
`{{ }}` value — it's a hard compile error, because order is a property of the prop's
*type*, not the call site. That is backwards: **order-preserving should be the
default**, and "I have a plain map, sort it for me" should be the explicit fallback.

## 2. Goal

Flip the default. One ordered bag type everywhere; a map alias that auto-converts.

- `gsx.Attrs` becomes `[]Attr` (the old `OrderedAttrs`). It is the type of the implicit
  fallthrough bag, every declared bag prop, the `{{ }}` literal, and conditional-attr
  bags. `Spread` renders it in **slice order**.
- `gsx.AttrMap = map[string]any` (a type **alias**) is the map form. gsx **auto-converts**
  any `map[string]any`-typed value to `Attrs` at bag boundaries, sorting keys so
  map-sourced bags stay deterministic.
- `gsx.OrderedAttrs` and `Writer.SpreadOrdered` are **deleted** (no release, no external
  consumers; the slice is now `Attrs`/`Spread`).

Net consequence: fallthrough attributes render in **source order**; `{{ }}` binds to any
bag prop; the two-type dispatch in codegen collapses to one path.

## 3. Type model (runtime, root package)

```go
// Attr is one ordered attribute pair. (Unchanged.)
type Attr struct{ Key string; Value any }

// Attrs is the ordered, duplicate-tolerant attribute bag — the default bag type.
// Renders in slice order. (Was OrderedAttrs.)
type Attrs []Attr

// AttrMap is the map form of an attribute bag. It is an ALIAS for map[string]any:
// a bare map[string]any, a gsx.AttrMap{...} literal, and a map returned from user
// code are all the same type, and all auto-convert to Attrs at bag boundaries.
type AttrMap = map[string]any

// AttrsFromMap converts a map bag to an ordered bag with keys SORTED ascending, so
// map-sourced attributes render deterministically. This is what codegen inserts for
// the implicit AttrMap->Attrs coercion; it is exported for explicit use too.
func AttrsFromMap(m map[string]any) Attrs
```

`AttrMap` is an alias, not a defined type, on purpose: the user asked to "just accept
`map[string]any`." Codegen's coercion keys off the *resolved type* being
`map[string]any` (`*types.Map`, key kind string, elem the empty interface), so it fires
for both `gsx.AttrMap` and bare maps without a "which map counts" ambiguity. Because an
alias can carry no methods, the conversion is a free function, not a method.

## 4. Rendering & method semantics

`Spread` is the old `SpreadOrdered`: iterate pairs in slice order, per-pair
`validAttrName` gate (unsafe names dropped), bool → bare attribute, else
`AttrValue(toStr(v))`. No sort. `AttrMap` values reach `Spread` only *after*
`AttrsFromMap` has sorted them, so map users keep today's alphabetical output while
ordered/literal users get source order — one `Spread`, no branching.

The method surface moves onto the slice `Attrs`. Because a slice can hold a key more
than once (the implicit bag never does, but `{{ }}` and hand-built bags can), each
method's **duplicate-key rule is defined and documented**:

| Method | Rule on duplicate keys |
|---|---|
| `Class() string` | **Aggregate** — join the values of *all* `class` pairs (space-separated, trimmed). Nothing is silently dropped. |
| `Style() string` | **Aggregate** — join the values of *all* `style` pairs (`"; "`-separated). |
| `Get(key) (any, bool)` | **Last wins** — last occurrence in slice order, matching JSX-style override order. |
| `Has(key) bool` | True if any occurrence exists. |
| `Without(keys...) Attrs` | Removes **all** pairs whose key matches; preserves order of the rest; nil/empty in → nil out. |
| `Take(key) (any, Attrs)` | Returns `Get`'s last value + `Without(key)` (removes all occurrences). |
| `Merge(other Attrs) Attrs` | For each pair in `other`: `class`/`style` → **concat in place** onto the first such pair in the result (or append if none). Other keys → **overwrite in place** the last existing occurrence and drop earlier duplicates (other wins under the last-wins scalar rule); append if absent. This preserves the old map-`Merge` "other wins" + class/style-concat semantics while keeping order. |

`AttrsCond(cond, then, els func() Attrs) Attrs` is unchanged in contract (thunks so the
untaken branch never evaluates); it now returns the slice `Attrs`. A nil `Attrs` is an
empty bag (`Spread`/`Without`/`Merge` all tolerate nil).

**Aggregation is the headline semantic to document** (guide + doc comments): `Class()`
and `Style()` never lose a value even when a bag carries the key twice. Scalar duplicate
keys use last-wins semantics so later pairs can intentionally override earlier pairs.

## 5. Auto-conversion (`AttrMap` → `Attrs`)

The "implicit fallback." When a value whose resolved type is `map[string]any` lands in a
bag position, codegen wraps it with `gsx.AttrsFromMap(...)`. Two boundaries:

1. **Component bag-prop binding** — `<C attrs={m}/>` where `m : map[string]any` and the
   target field is `Attrs` → `AttrsFromMap(m)`.
2. **Element spread** — `<div { m... }>` where `m : map[string]any` →
   `Spread(ctx, AttrsFromMap(m))`.

Detection is by **resolved type**, via the existing type-check skeleton (the same
machinery that resolves prop-value and spread-expression types today). Rules:

- type `Attrs` (slice) → used directly, no wrap (e.g. a `{{ }}` literal, an `Attrs`-typed
  prop, an `Attrs`-returning call).
- type `map[string]any` (= `gsx.AttrMap`, or a bare map) → wrap with `AttrsFromMap`.
- untyped `nil` → `Attrs(nil)` (empty bag), no wrap needed.
- any other type in a bag position → existing type error (unchanged).

**Composition with `(T, error)` auto-unwrap:** a call returning `(map[string]any, error)`
unwraps to a temp first, then converts — `t, _gsxerr := f(); if _gsxerr != nil { return _gsxerr }; … AttrsFromMap(t)`. The unwrap hoist runs before the conversion wrap; both are
gated to call expressions as today.

## 6. Codegen changes (`internal/codegen`)

**Delete the two-type dispatch.** Since the slice is now the only bag type:

- Remove `SpreadOrdered` emit; the `orderedFields`/`orderedProps` lookup at the element
  spread (`emit.go:611-616`) and `*ast.SpreadAttr` (`emit.go:1328-1333`) sites; the
  `isOrderedAttrsType` predicate (`analyze.go:259-261`) and its callers
  (`analyze.go:90,65,757`); and the `orderedProps` threading through
  `module_importer.go` / `module.go` / `generateFile` / `genComponent` /
  `emitRootElement` / `emitFallthroughAttrs`.
- The element-spread dispatch is **replaced**, not removed: instead of "ordered field →
  `SpreadOrdered` else `Spread`," it is now "spread expr type is `map[string]any` →
  `Spread(AttrsFromMap(e))` else `Spread(e)`." Same resolved-type source, simpler intent.

**Implicit fallthrough bag → source-order slice literal.** The child-component bag
literal (`emit.go:2568`) and the `AttrsCond` branch literals (`emit.go:2677`,
`condBranchAttrs`) switch from `gsx.Attrs{"k": v, …}` (map literal) to
`gsx.Attrs{{Key:"k", Value:v}, …}` (slice literal). The fallthrough collection **must
emit entries in call-site source order** (it currently feeds an order-insensitive map
literal; the entry order now determines render order). Spreads mixed into the
fallthrough set continue to `.Merge(...)`-append in source order (a `map[string]any`
spread wrapped via `AttrsFromMap`); literal entries among themselves are source-ordered.
The `func() gsx.Attrs { return gsx.Attrs{…} }` thunks (`emit.go:2629-2637`) emit slice
literals.

**Synthesized `<Name>Props` bag field** stays spelled `Attrs gsx.Attrs` (`emit.go:326`,
skeleton `analyze.go:614,2122`) — the field name and type *name* are unchanged; only the
runtime meaning of `gsx.Attrs` flips. The skeleton's two sites stay in lockstep.

**Fallthrough `.Class()/.Style()/.Has()/.Without()`** emit sites (`emit.go:523-564`) are
unchanged in shape — they call the same-named methods, which now carry the slice
semantics from §4. `.Without("class","style")` still strips class/style before the root
`Spread`.

**`{{ }}` lowering** (`emit.go:2127,2519`) targets `gsx.Attrs{…}` instead of
`gsx.OrderedAttrs{…}`. Error messages mentioning `gsx.OrderedAttrs` (the `{{ }}`-on-plain-
element error `emit.go:1357-1361`, the no-field error `emit.go:2504-2507`) update to
`gsx.Attrs`.

**BYO components** (`byo.go:255-259`, `isGsxAttrsType` matching `"gsx.Attrs"`) are
unchanged — a BYO `Attrs gsx.Attrs` field is now the slice. Follow-up #3 from
`2026-06-29-ordered-attrs-followups.md` (a BYO `gsx.OrderedAttrs` field) becomes **moot**:
there is one type.

## 7. Parser / AST

The `{{ }}` grammar is unchanged. AST node names `OrderedAttrsAttr` / `OrderedPair`
(`ast/ast.go:378-399`) and `parseOrderedAttrsLiteral` / `splitOrderedPairs`
(`parser/attrs.go:380-509`) are **kept** — `{{ }}` remains a distinct, meaningful syntactic
form (explicit ordering, quoted keys, duplicate tolerance) versus a plain `={expr}` value
or an `AttrMap{}` literal. Only the lowering *target* and doc prose change. The printer
case (`printer.go:608-624`) and walker/printer cases are unaffected (they format the
`{{ }}` source, which is unchanged).

## 8. The `{{ }}` literal's role going forward

Two ways to spell a bag value at a call site, both binding to the one `Attrs` type:

- `{{ "data-signals": s, "data-on-click": h }}` — **I care about order** (and may repeat
  keys / use non-identifier keys). Lowers to a source-order `gsx.Attrs` slice literal.
- `gsx.AttrMap{"class": c, "id": i}` or any `map[string]any` value — **I don't care, sort
  it.** Auto-converts via `AttrsFromMap` (alphabetical).

## 9. Migration & breaking changes

This is the bulk of the work; there is no release and no external consumer, so a clean
break is acceptable.

- **Runtime:** rewrite `attrs.go` + `orderedattrs.go` per §3–§4; update comment-only refs
  in `class.go`.
- **Any code using `gsx.Attrs` as a map** (`a["k"]`, `range a`, `a["k"]=v`, `len`,
  `delete`) stops compiling. Within this repo that is confined to codegen *emitting* map
  literals (handled in §6) and tests. **`one-learning` (the templ→gsx migration, worktree
  `one-learning-gsx`) must be audited** for map-style `Attrs` usage and updated to the new
  semantics (declare `AttrMap` where a map is genuinely wanted, or use `Attrs`/`{{ }}`).
  This audit is a plan task.
- **Corpus goldens:** ~100 `.txtar` cases pin a bag pattern (`Spread(` ×72, `.Class()/.Style()`
  ×72, `.Without(` ×73, `Attrs gsx.Attrs` ×58, `gsx.Attrs{` map-literal ×26,
  `SpreadOrdered(` ×14, `gsx.OrderedAttrs{` ×14, `.Merge(` ×6, `AttrsCond` ×3). All
  regenerate with `-update`; the fallthrough cases' `render.golden` change from
  alphabetical to source order — **each diff is reviewed**, not blindly accepted, to
  confirm the new order is the intended source order.

## 10. Testing plan

Per the syntax-work rigor bar (`gsx-syntax-work-highest-rigor`) and the per-context
corpus rule:

- **Runtime unit tests** (root `gsx` package): each method's duplicate-key rule from §4
  gets an explicit test — `Class()`/`Style()` aggregation across two pairs, `Get`/`Has`
  last-wins/presence, `Without` removes-all, `Merge` overwrite-last-in-place +
  class/style concat, `AttrsFromMap` sort determinism, `Spread` order + last-wins scalar
  duplicate handling + `validAttrName` drop + bool handling, nil-bag tolerance.
- **Corpus cases** (regenerate all; add new where coverage is thin):
  - Fallthrough bag renders in **source order** (new behavior) — a dedicated case.
  - `AttrMap`/bare-`map[string]any` auto-conversion at **both** boundaries (prop binding
    and element spread), asserting `AttrsFromMap` wrap + sorted render.
  - `{{ }}` binding to a declared bag prop (the friction that this design removes) — a
    case proving it now compiles and renders in order.
  - `(map[string]any, error)` in a bag position → unwrap-then-convert composition.
  - Conditional-attr bag (`AttrsCond`/`Merge`) emitting slice literals.
  - Error cases: `{{ }}` on a plain element, no-field ordered literal — updated message
    text mentioning `gsx.Attrs`.
- **Adversarial probe** before merge: build throwaway `.gsx` programs exercising bag
  ordering, duplicate `class` aggregation end-to-end, map-vs-ordered determinism, and
  `gsx fmt` idempotence on `{{ }}`.

## 11. Docs & sibling repos

- **`docs/guide/syntax.md`**: rewrite the "Ordered attributes" section (`:128-241`),
  inverting the framing — `gsx.Attrs` is the ordered default; `gsx.AttrMap` is the
  auto-sorted map convenience; the comparison table (`:225-241`) flips. Document the
  `Class()`/`Style()` **aggregation** rule and the auto-conversion boundaries.
- **`../tree-sitter-gsx` / `../vscode-gsx`**: the `{{ }}` *grammar* is unchanged, so no
  grammar edit is required by *this* change; the pending follow-up #2
  (`2026-06-29-ordered-attrs-followups.md`) — grammar/highlighting for `{{ }}` and
  whitespace-around-`=` — still stands and is tracked there, not duplicated here.

## 12. Decisions (settled with the user)

- Unify on the ordered slice as the default `Attrs`; map becomes `AttrMap`. ✓
- `AttrMap` is an **alias** for `map[string]any`; auto-conversion accepts bare maps too. ✓
- Auto-conversion fires at **both** boundaries (prop binding + element spread). ✓
- `Class()`/`Style()` **aggregate**; scalar `Get`/`Spread` are **last-wins**;
  `Without` removes-all; `Merge` overwrite-last-in-place + class/style concat. ✓
- Delete `OrderedAttrs`/`SpreadOrdered` (no compat alias). ✓
- Breaking sweep accepted; one-learning updated to new semantics. ✓

## 13. Risks

- **Source-order fidelity of the fallthrough literal** — the collection must preserve
  call-site order; an order-insensitive iteration (e.g. ranging a map) would silently
  reintroduce nondeterminism. The implementation must build the literal from a
  source-ordered list, and a corpus case must pin a non-alphabetical fallthrough order to
  catch regressions.
- **Goldens reviewed, not rubber-stamped** — ~100 regenerated cases is a large diff;
  the risk is masking an unintended change inside the noise. Spot-review the fallthrough
  and conversion cases by hand.
- **Auto-conversion over-firing** — restrict detection to exactly `map[string]any`; do not
  coerce other map types (a `map[string]string` is not an attr bag and should stay a type
  error).
