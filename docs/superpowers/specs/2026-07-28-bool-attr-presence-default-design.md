# Presence-by-default for bool attribute values

Status: implemented · 2026-07-28

## Problem

`data-slot={true}` renders `data-slot="true"`. A bare `data-slot` renders
`data-slot`. Same authored meaning, two different bytes — and the value form is
never what the author wanted.

Today's rule (`boolattr.go`): a bool value toggles only if the NAME is in the
curated `booleanAttrs` set; every other name stringifies to `"true"`/`"false"`,
"which is what enumerated attributes (aria-\*, contenteditable, data-\*)
require". That justification is true for `aria-*` and `contenteditable`. It is
false for `data-*` and for custom-element names, which HTML gives no value
vocabulary at all.

The stringified form is not merely verbose, it is a trap: `[data-x]` matches
`data-x="false"`, so `data-open={false}` renders `data-open="false"` and every
`[data-open]` rule and Tailwind `data-open:` variant fires while the thing is
closed.

### What the ecosystem does

React renders `data-x={true}` → `data-x="true"` and `data-x={false}` →
`data-x="false"` (react#24812, filed as a bug, closed stale). Vue behaves the
same via a fixed WHATWG list. But no React component library binds a bool to a
data attribute: Radix writes `data-disabled={disabled ? '' : undefined}` and
`data-highlighted={isFocused ? '' : undefined}`, Base UI generates the same
shape from a helper. That idiom exists purely to emulate presence/absence,
which React cannot express (its SSR always writes `=""`). Enum-valued data
attributes use real strings (`data-state="open"`). templ chose explicit syntax
(`noshade?={ x }`), name-agnostic.

Conclusion: for a bool, presence is the universal intent; the value form is the
workaround people are forced into. gsx has a real `bool` type and emits genuinely
bare attributes, so it has no reason to inherit React's constraint.

## Rule

A bool renders as **presence** — bare when true, omitted when false — UNLESS
HTML's own value vocabulary for that name IS the literal strings `"true"` and
`"false"`.

| Name | `true` / `false` |
| --- | --- |
| `aria-expanded`, `aria-checked`, `aria-hidden`, … (20 generated) | `name="true"` / `name="false"` |
| `contenteditable`, `writingsuggestions` (curated) | `name="true"` / `name="false"` |
| **everything else** — `data-*`, `required`, `title`, `my-toggle active`, `x-show`, `hx-boost` | **`name` / omitted** |

Static and string values are untouched: `data-x="false"`, `hx-boost="false"` and
`` x-show=js`open` `` render verbatim. The rule is consulted only for bools, in
the two places it was consulted before — codegen (`emit.go`, static name + bool
expr) and the runtime bag leaf (`attrs.go`).

Why this is the whole exception set: those are the only names where absence and
`"false"` mean different things. A screen reader announces `aria-expanded="false"`
as collapsed and says nothing when the attribute is absent; `contenteditable`,
`spellcheck` and `writingsuggestions` inherit, so only the literal `"false"`
opts a subtree out; `draggable` is on by default for images and links. Nothing
else in HTML has a real case — `title={b}` and `id={b}` are nonsense in either
rendering.

### Escape hatches

- Value on a presence name: `data-x={strconv.FormatBool(b)}`, or a static
  `data-x="true"`.
- Presence on an exception name: `gsx.Toggle(b)` (unchanged).

## The table

Generated into the root package (`truefalseattrs.gen.go`) by
`internal/htmldata/gen`, from the already-vendored `@vscode/web-custom-data`
dataset. The criterion is mechanical: an attribute qualifies when its `valueSet`
lists both `"true"` and `"false"` (valueSets `b`, `u`, `tristate`, `current`,
`invalid`, `haspopup`). No hand-picking, and a dataset refresh carries new names
in automatically. 20 names today.

`contenteditable` and `writingsuggestions` are curated in `trueFalseExtras`
because the dataset records no value vocabulary for either — the same shape as
the existing `presenceOnlyExtras`, each entry stating why `"false"` is
load-bearing.

Emitted as a `switch` (zero init cost, no map allocation). `TestTrueFalseAttrMatchesHTMLData`
re-derives the criterion from `htmldata` and compares, so regenerating one file
without the other fails rather than silently changing how every bool renders.

`IsBooleanAttr` and its curated lists are untouched and keep their HTML meaning
(LSP completion inserts a bare name from them). They no longer participate in
the render decision: an HTML boolean attribute renders bare because *nothing*
gives it a true/false vocabulary, not because it is on a list.

## Rejected: vocabulary carve-outs

An intermediate design stringified for any name a vocabulary gsx knows gives a
value to — the full 302-name platform table, htmx's attributes, and the
JS-expression names from `internal/attrclass`. It was dropped for simplicity:
three rules to explain instead of one, and the extra ~280 names have no real
case. The argument for the carve-outs was `x-show={open}` and
`hx-boost={cfg.Boost}` breaking, but those attributes take client-side
expressions and string enums, so they are written `` x-show=js`open` `` and
`hx-boost="false"` — both untouched by this rule. A Go bool on one could only
have meant presence.

Consequence, accepted: `title={b}` renders a bare `title` rather than
`title="true"`. Binding a bool to a text attribute is a mistake in either
rendering.

## Breaking changes

Rendered bytes change for `name={boolExpr}` on every name outside the exception
set. Pre-1.0, but it needs a migration note:

- `data-x={true}` → `data-x` (was `data-x="true"`)
- `data-x={false}` → omitted (was `data-x="false"`)
- JS that reads `el.dataset.x === "true"` moves to `'x' in el.dataset` /
  `hasAttribute`, or the author asks for the string with `strconv.FormatBool`.

## Test plan

Corpus (one case per context where the decision is made):

1. `attrs/bool_attr_name_driven` — element attributes across both buckets:
   HTML booleans, `aria-*`, `contenteditable`, `spellcheck`, `draggable`,
   `title`, `data-*`, a custom element, `x-cloak`, `x-show`, `hx-boost`,
   `gsx.Toggle`, and a bare attribute.
2. `attrs/bare_presence_fallthrough` — the same decision at the bag leaf,
   through component forwarding, conditional-attr blocks and folded spreads.
3. `orderedattrs/bool_pair` — `{{ }}` bag literal.
4. `jsattr/whole_value_bool` — pins the accepted cost for a bool in a
   JS-context attribute.

Runtime: `TestBoolRendersBare` (both buckets + folding),
`TestTrueFalseAttrMatchesHTMLData` (drift gate), `TestTrueFalseExtras`,
`TestSpreadBoolByName` (the runtime leaf).
