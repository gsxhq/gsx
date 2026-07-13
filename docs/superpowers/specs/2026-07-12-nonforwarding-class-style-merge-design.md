# Non-Forwarding Class/Style Merge Design (#96)

## Goal

Make `class` and `style` composition uniform on elements with or without an
attribute spread. Every contribution to the same attribute merges into one
rendered attribute.

## Semantics

- `class` contributions aggregate tokens in source order.
- `style` contributions aggregate declarations in source order.
- A repeated style property is source-order last-wins.
- Contributions may be top-level or nested in conditional attributes, including
  `else` and `else if` branches.
- Static, composable, embedded text, embedded CSS, renderer, pipeline, and
  `(T, error)` forms retain their existing contextual escaping and error rules.
- An untaken conditional contribution is not evaluated.
- An element renders at most one `class` and at most one `style` attribute.

Examples:

```gsx
<div style="color:red" { if active { style="margin:0" } }>x</div>
// active: style="color:red; margin:0"

<div style="color:red" style="margin:0" style="color:blue">x</div>
// style="margin:0; color:blue"

<div class="base" { if active { class="on" } }>x</div>
// active: class="base on"
```

## Architecture

Extend the element fold decision with a real composition predicate. Walk the
attribute tree in source order and count `class` and `style` contributors
independently. A contributor is any `StaticAttr`, `ClassAttr`, or
`EmbeddedAttr` named `class` or `style`. A `CondAttr`'s branches are mutually
exclusive, so it contributes the **maximum** of its two branch counts, never
their sum. When either name can receive more than one contribution in a single
render, route the element through the existing
`foldElementSpreads`/`composeBag` path even when it has no spread.

This generalizes #95's spread-gated `hasCondClassStyle` rule without making a
single conditional class/style fold unnecessarily. A non-forwarding element
with only `{ if active { class="on" } }` — or only
`{ if active { class="on" } else { class="off" } }`, whose branches can never
co-contribute — stays on the current inline fast path. An element with root
class plus conditional style also stays inline because there is no same-name
collision to merge.

A bare bool `class`/`style` is not counted: the bag's `Class()`/`Style()`
aggregation is string-valued, so a boolean entry cannot merge; the shape stays
inline (tracked as an open edge in ROADMAP).

`elementFolds` remains the single predicate shared by emission and
`scopeUsesNumeric`, so folded numeric attributes cannot create unused scratch
buffers.

## Data flow

The fold turns each conditional branch into `AttrsCond`, concatenates all
contributors in source order, and sends the resulting bag through the existing
single-leaf renderer. `Attrs.Class()` joins all class values. `StyleMerged` and
the style bag logic parse declarations and retain the last occurrence of each
property. No new runtime API is required.

PR #97/#99's contextual-hole and `errReturn` work remains authoritative:
embedded JS/CSS URL-sink diagnostics, contextual filtering, renderer/tuple
hoists, and source evaluation order must not regress when the new no-spread
shape enters `composeBag`.

### Static URL provenance through a fold

Folding must not change the trust policy of an unrelated author-written static
URL attribute. Inline `href="javascript:void(0)"` is trusted template text. In
an **element-fold** bag, every `StaticAttr` string is therefore carried as
`gsx.RawURL("…")`, the runtime's existing per-value provenance marker, rather
than a plain string. For non-URL keys `Spread` already converts `RawURL` through
the normal string/HTML-attribute path, so output is unchanged; when the winning
key is URL-classified, the leaf recognizes the marker and skips scheme
filtering while retaining HTML attribute escaping. Component-prop bags keep
ordinary strings and are unaffected.

Trust is per contribution and source-order last-wins:

- static URL followed by a dynamic/forwarded URL: the later plain value wins
  and is URL-sanitized;
- dynamic/forwarded URL followed by a static URL: the later trusted literal
  wins and is emitted verbatim (with HTML escaping);
- two static URL contributions: the later trusted literal wins;
- JS/CSS contextual literals on URL-sink keys remain rejected as specified by
  PR #99; they are not converted to `RawURL`.

Only codegen-authored `StaticAttr` values in `bagElementFold` gain this marker.
Expression attrs,
embedded literals, spreads, pipelines, and conditional bags preserve their
existing dynamic sanitization unless their leaf is itself a `StaticAttr`.
Codegen does not classify the attribute name or duplicate URL tables: the
existing `Spread` leaf remains the single authority for navigational, image,
srcset, prefix, and custom configured URL sinks and consults the value marker
only after the winning entry is selected.

This is strictly a fold-path representation detail. A leaf element whose
attributes do not require merging stays on the existing direct emitter: static
attribute bytes are written directly into the tag, with no `Attrs` allocation,
no `RawURL` marker, and no `Spread` call. Generated-code negative controls pin
that shape for both ordinary and URL attributes.

## Tests

Canonical corpus cases cover:

- root static class/style plus conditional static class/style;
- two and three top-level style attributes with distinct and duplicate keys;
- `else` and nested `else if`;
- embedded CSS static and hole-bearing style values;
- composable class/style, renderer, pipeline, and `(T, error)` contributions;
- untaken-branch laziness and source evaluation/error ordering;
- a numeric attribute beside the merge shape (prescan agreement);
- negative controls: one conditional contribution only, and root class plus
  conditional style (different names) remain inline;
- exact generated shape and render goldens.
- static URL provenance and override order: trusted static vs later dynamic,
  dynamic vs later trusted static, two statics, conditional static branches,
  image/srcset/custom URL sinks, and HTML-special characters.
- direct-emission negative controls for leaf elements without merging, asserting
  generated output contains the literal tag write and no `Attrs`/`Spread` path.

Add the no-spread shapes to the codegen fold differential matrix and a focused
benchmark comparing the unchanged single-style fast path with the new merge
path.

## Documentation

Close #96 in `docs/ROADMAP.md` and state explicitly in the composition guide:
all `class` values merge as tokens, and all `style` values merge as declarations
with source-order last-wins per property, independent of conditional/spread
placement.

No parser, formatter, sibling grammar, editor, or runtime dependency change is
required.
