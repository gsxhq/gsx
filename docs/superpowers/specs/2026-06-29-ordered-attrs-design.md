# Ordered Attributes (`{{ }}`) — Design

- **Date:** 2026-06-29
- **Status:** Draft (awaiting review)
- **Topic:** A `gsx.OrderedAttrs` runtime type plus a `{{ "k": v, … }}` attribute-value literal, so a caller can pass an *order-preserving* attribute bag to a component prop that the component spreads onto an element.

## Problem

gsx's spread bag, `gsx.Attrs`, is a `map[string]any`. `Writer.Spread`
(`attrs.go:114`) sorts keys (`sort.Strings`, `attrs.go:122`) before emitting, so
spread attributes always render in **alphabetical** order, regardless of source
order. That sort makes output deterministic (good — golden tests are stable) but
it means a caller **cannot control attribute order** through a spread bag.

For ordinary HTML this is fine: attribute order is semantically insignificant,
and attributes written directly on an element (`<div a={x} b={y}>`) already
render in source order because they are a sequence of AST nodes, not a map. The
order problem is confined to attributes that flow through a *map* — the implicit
fallthrough bag or an explicit `gsx.Attrs` prop.

Order *does* matter for hypermedia frameworks that treat `data-*` attributes as
ordered directives — most concretely **Datastar**, where same-rank directives
apply in DOM/source order (e.g. a `data-signals` that defines a signal must
precede a directive that reads it). templ hit the same wall and added
`templ.OrderedAttributes` (PR #1139) for exactly this reason.

The specific gsx shape that needs it: a component takes a bag prop and spreads it
onto an inner element the caller can't write directly:

```gsx
component Card(containerAttrs gsx.OrderedAttrs) {
	<div class="container" { containerAttrs... }>
		<div class="body" { attrs... }>{children}</div>
	</div>
}
```

To control the order of the container's `data-*` directives, the caller needs to
hand `Card` an *ordered* value.

## Goals

- A standard-library runtime type, `gsx.OrderedAttrs`, that preserves insertion
  order and renders without sorting, reusing the exact per-attribute escaping and
  safety of `Spread`.
- An ergonomic literal — `container-attrs={{ "data-signals": sig, "data-text": txt }}`
  — so callers never hand-write the ordered slice type. This is the headline
  ergonomic win: users get ordering without importing or constructing an
  ordered-map implementation themselves.
- Order is preserved end to end: caller literal → prop field → spread onto an
  element.
- Zero impact on existing behavior. `gsx.Attrs` and today's `{ bag... }` spread
  are unchanged (still sorted, still deterministic).
- Keep the runtime root dependency-free (`gsx.OrderedAttrs` is plain stdlib).

## Non-goals

- **No standalone-element spread literal** (`<div {{ … }}>`). Attributes written
  directly on an element are already ordered; a literal spread there is
  redundant. The `{{ }}` literal appears only in attribute *value* position.
- **No generic ordered-map language construct.** `{{ }}` produces a
  `gsx.OrderedAttrs` value and nothing else. It is attribute-domain-specific.
- **No `Attributer` interface / dual-typed prop.** A prop that needs order
  declares the concrete `gsx.OrderedAttrs` type (decision (a)). One prop, one
  bag type. (templ's interface route is explicitly declined.)
- **No `style={{ }}` React-object sugar.** gsx's style path is already
  order-preserving end to end (`style` values are CSS-declaration *strings*;
  `StyleMerged`, `style_merge.go:73`, dedupes by property keeping the last
  occurrence with survivors in source order; composable `style={ a, b }` is an
  ordered part list). Style needs nothing here.
- **No class/style *merge* for ordered bags** (see Class/style scope).
- No `.Without` / `.Merge` / `.Class` / `.Style` helpers on `OrderedAttrs` in v1.
  Those stay map-only (`gsx.Attrs`).

## Surface syntax

`{{ … }}` is an **ordered-attrs literal**, valid only in attribute brace-value
position (`name={{ … }}`) at a component invocation. It is a Go-map-style literal
that *keeps order*:

```gsx
<Card container-attrs={{ "data-signals": sig, "data-text": txt, "data-show": on }}/>
```

- **Pairs:** `"<key>": <value-expr>`, comma-separated, trailing comma allowed.
- **Keys are quoted string literals.** Required — it is the only robust way to
  carry kebab / colon names like `"hx-on:click"`; a bare `hx-on:click:` would make
  `:` ambiguous as both a key character and the pair separator.
- **Values are Go expressions.** v1 supports a plain Go expression per value
  (string literal, identifier, selector, call, composite literal, etc.). A `|>`
  filter pipeline *inside* an ordered value is **deferred** (non-goal v1) — use a
  plain expression; pipelines remain available in the normal `name={ … |> … }`
  attribute-value form.
- The literal binds to a prop via the existing kebab field-matcher
  (`internal/codegen/fieldmatch.go`): `container-attrs` → field `ContainerAttrs`,
  which must be typed `gsx.OrderedAttrs`.
- A bare key with no value (`{{ "data-x" }}`) and the standalone-spread misuse
  (`<div {{ … }}>`) are parse errors with a pointed message.

### Whitespace around `=`

Optional whitespace is allowed on both sides of `=` for **every** attribute
value form — `name = "x"`, `name = {x}`, `name = {{ … }}` — and `gsx fmt`
normalizes it back to `name=…` (no surrounding space). This is a general
attribute-parsing change (gsx previously required `=` with no surrounding
whitespace for all attributes); it is included here because the `{{ }}` literal
made the omission visible, but it applies uniformly to avoid making the ordered
literal the only attribute that tolerates a space. Decided 2026-06-29 (user):
rejecting `name = {{…}}` as a syntax error would surprise users who expect the
formatter to fix spacing.

### Coexistence with `GoBlock`

`{{ … }}` already exists in gsx as `GoBlock` — the `{{ stmt }}` Go-statement
escape hatch (`ast.go:290`, parsed at `markup.go:453`) — but **only in
markup-body position**. The ordered-attrs literal lives **only in attribute-value
position** (`name={{ … }}`). The two never collide at parse time: GoBlock is read
by the body markup parser, the ordered literal by the attribute-value parser.
This is the same position-driven brace overloading gsx already uses pervasively
(`{ }` is interp / control-flow / GoBlock in the body, expr / markup value in an
attribute, and spread / cond in the attribute list). Reusing `{{ }}` for an
ordered attribute *value* is consistent with that model — same glyph, distinct
position, distinct meaning. Documented so the overload is intentional, not
accidental.

### Bool / valueless attributes

A boolean attribute (e.g. Datastar's bare `data-show`) is written with an
explicit Go bool value: `{{ "data-show": true }}` → renders bare `data-show`
when true, omitted when false. This mirrors `Spread`'s existing bool handling
(`attrs.go:127`). (We do not invent a "key with no value means true" form inside
`{{ }}` — it would reintroduce the key/`:` ambiguity and diverge from the map
bag.)

## Runtime type

In the root `gsx` package (stdlib-only):

```go
// Attr is one ordered attribute pair.
type Attr struct {
	Key   string
	Value any
}

// OrderedAttrs is an insertion-ordered, duplicate-tolerant attribute bag.
// Unlike Attrs (a map, sorted on spread), it renders in slice order.
type OrderedAttrs []Attr
```

- **Duplicate keys are tolerated** (a slice can hold them). Scalar duplicate keys use
  last-wins spread semantics, so later pairs intentionally override earlier pairs.
  `class` and `style` are aggregate keys in the unified `gsx.Attrs` model.
- A caller may hand-write `gsx.OrderedAttrs{{Key: "data-x", Value: x}}` with no
  sugar at all; `{{ }}` is purely the front door.

### Rendering: `Writer.SpreadOrdered`

```go
func (gw *Writer) SpreadOrdered(ctx context.Context, a OrderedAttrs)
```

Identical per-pair logic to `Spread` (`attrs.go:114-136`) **minus the sort**:
iterate the slice in order; for each pair skip structurally-unsafe names
(`validAttrName`, `attrs.go:143`); emit bool pairs via `BoolAttr`; otherwise emit
` key="` + `AttrValue(toStr(value))` + `"`. Same `html/template`-faithful
escaping, same tag-breakout protection — only the ordering source differs. Empty
bag is a no-op.

## Codegen lowering

### Value literal → ordered slice

`container-attrs={{ "data-signals": sig, "data-text": txt }}` lowers to a Go
composite literal bound to the matched field:

```go
Card(CardProps{
	ContainerAttrs: gsx.OrderedAttrs{
		{Key: "data-signals", Value: sig},
		{Key: "data-text", Value: txt},
	},
})
```

Keys are emitted as quoted string literals; values as the lowered Go expressions
(same path as a `name={expr}` attribute value, including any `|>` pipeline).

### Spread dispatch (type-directed)

A manual spread `{ containerAttrs... }` lowers based on the **declared type** of
the spread subject:

- subject's resolved type is `gsx.OrderedAttrs` → `_gsxgw.SpreadOrdered(ctx, containerAttrs)`
- otherwise (the `gsx.Attrs` case, the default) → today's `_gsxgw.Spread(ctx, …)`

Codegen already knows component param/field types (they are declared in the
`component Card(containerAttrs gsx.OrderedAttrs)` signature / BYO Props struct).
Because there is no standalone ordered-spread literal, the spread subject is
always a named param/field of known type, so dispatch is unambiguous. A spread
of an expression whose type cannot be resolved to `gsx.OrderedAttrs` falls to
`Spread` (and fails to compile if it is actually ordered — an acceptable,
diagnosable edge, documented).

The component-root auto-spread of the implicit fallthrough bag is unaffected:
the implicit `Attrs` field stays `gsx.Attrs` (sorted). Ordered behavior is opt-in
via an explicitly-typed `gsx.OrderedAttrs` prop.

## Class/style scope

An ordered bag does **not** participate in class/style merge. Pairs — including a
`"class"` or `"style"` pair, should one appear — emit verbatim in slice position.
Routing class/style through the merge machinery (`Class` / `StyleMerged`) would
have to relocate them out of sequence and defeat the ordering guarantee that is
the whole point. Authors who need class/style merging put those on the element
directly or in the map bag; the ordered bag is for ordered `data-*`-style
directives. This is a documented limitation, not a silent gap.

## Cache key

`{{ }}` is pure syntax/codegen — it changes generated output but adds **no new
configuration knob**, so there is nothing to fold into `computeKey`
(`gen/cachekey.go`): the generated `.x.go` already participates in the cache by
content. (Confirm during planning that no knob is introduced.)

## Security

`SpreadOrdered` reuses `validAttrName` + `AttrValue` (attribute escaping) exactly
as `Spread` does. No new escaping context is introduced — the value escaping is
the same attribute-value path, the same faithful `html/template` port. Keys are
gated by `validAttrName` (drops names that could break out of the tag). The only
behavioral difference from `Spread` is the absence of the key sort.

## Testing

Per the corpus convention (every syntax/codegen change ships a corpus case):

- **Corpus** (`internal/corpus/testdata/cases/…`):
  - value literal binds to an ordered prop (`container-attrs={{ … }}`) — pins
    `generated.x.go.golden` showing the `gsx.OrderedAttrs{…}` literal + render.
  - spread of an ordered prop preserves order, contrasted against a sorted
    `gsx.Attrs` spread of the same keys (the order-vs-sort distinction is the
    headline behavior).
  - escaping / unsafe-name drop through `SpreadOrdered`.
  - bool pair (`"data-show": true/false`).
  - duplicate-key tolerance (renders both, in order).
  - parse-error cases: bare key (`{{ "x" }}`), and standalone-spread misuse
    (`<div {{ … }}>`).
  - Datastar-flavored end-to-end case (ordered `data-*` directives) as the
    motivating example.
- **Runtime unit tests** (root `gsx` package): `SpreadOrdered` order, escaping,
  bool, unsafe-name skip, empty-bag no-op — mirroring
  `TestSpreadDeterministicAndTyped` (`attrs_test.go:54`).
- Regenerate goldens + `coverage.golden`; verify without `-update`; `make check`.

## Docs

- `docs/guide/` — document `gsx.OrderedAttrs` and the `{{ }}` literal: what it's
  for (ordered `data-*` / Datastar), the quoted-key rule, the bool form, and the
  no-class/style-merge limitation. Contrast with `gsx.Attrs` (sorted).
- `docs/ROADMAP.md` — note ordered attributes shipped.

## Risks / open questions (resolve during planning)

1. **Type resolution for spread dispatch.** Confirm the analyze phase exposes the
   spread subject's declared type at the lowering site for both param-list and
   BYO-Props components, including cross-package props. If resolution is
   unavailable in some path, define the fallback precisely (emit `Spread`;
   compile error surfaces the misuse).
2. **`{{` tokenization.** Confirm `={{` is detected in `parseSingleAttr`'s
   `=`-then-`{` branch (`attrs.go:190`) before it dispatches to
   `parseAttrBraceValue` (`markup.go:502`), and that the matching `}}` is scanned
   with Go-aware brace/quote balancing (a `}` inside a value expression or string
   must not close the literal early). The body-position `GoBlock` parser
   (`markup.go:453`) is not on this path, so there is no cross-talk.
3. **Empty literal.** `{{ }}` (no pairs) → empty `gsx.OrderedAttrs{}` (renders
   nothing). Decide whether to allow or reject; leaning allow (consistent with an
   empty map bag).
