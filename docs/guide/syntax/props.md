# Props

The component author owns the props type. Bring your own named struct, let gsx
generate one from inline params, or declare no params at all. The param shape at
the declaration site is the discriminator: no config, no annotations.

## The four props models

The shape of a component's parameter list determines which model applies:

::: v-pre
| Param shape | Model | Generated Go signature |
|---|---|---|
| **Single named-struct param** `component Button(p Props)` | **Bring-your-own (byo)** — gsx uses the author's type directly; no wrapper generated | `func Button(p Props) gsx.Node` |
| **Inline params** — multiple params or a single non-struct param | **Generated** `<Name>Props` — one field per param; `Children`/`Attrs` added when used | `func Greeting(p GreetingProps) gsx.Node` |
| **Nullary** — zero non-receiver params | **No props struct** — unless `{children}` or the explicit `attrs` bag is used, in which case gsx grows a minimal props type automatically | `func Shell() gsx.Node` |
| **Attrs-only func value** — a package-level `var`/`func` of type `func(gsx.Attrs) gsx.Node` or `func(...gsx.Attr) gsx.Node` | **Component value** — no props struct; every call-site attribute merges into one `gsx.Attrs` bag | `HomeIcon(gsx.Attrs{{Key: "class", Value: "w-5 h-5"}})` |
:::

The discriminator is *discoverable*: writing `(p Props)` where `Props` resolves to a named struct in the same package opts you onto the byo path. Receiver params (`component (p Page) Render()`) are not counted. The fourth model is different in kind from the other three — it isn't a `component` declaration at all, just a package-level value gsx recognizes by its static type; see [Attrs-only component values](#attrs-only-component-values) below.

## Bring-your-own struct

When the sole non-receiver param is a named struct from the same package, gsx uses that struct directly — no generated wrapper. Each call-site attribute maps to a field on the struct:

- Identifier → Go-capitalized: `variant` → `Variant`, `fullWidth` → `FullWidth`
- Kebab → CamelCase: `full-width` → `FullWidth`, `aria-label` → `AriaLabel`
- No matching field → collected into the `Attrs gsx.Attrs` field

`Children gsx.Node` and `Attrs gsx.Attrs` are **explicit** on the byo path: declare `Children` to receive `{children}` content, and declare `Attrs` to collect unmatched call-site attributes. Omitting either field is a codegen error if the caller supplies them.

<!--@include: ./_generated/props/010-bring-your-own-props.md-->

::: v-pre
`Button` declares a `Props` struct with `Variant string`, `Children gsx.Node`, and `Attrs gsx.Attrs`. The call `<Button variant="primary" data-test="save">Save</Button>` maps: `variant` → `Variant: "primary"`, `data-test` (no matching `DataTest` field) → `Attrs: gsx.Attrs{{Key: "data-test", Value: "save"}}`, and the text content → `Children`. Inside the body, `{ p.Attrs... }` spreads the collected attrs onto the `<button>` element.
:::

## The discriminator heuristic

The byo path activates only for a **single** non-receiver param whose type resolves to a named struct in the same package. Everything else — a single scalar param or any multi-param signature — produces a generated `<Name>Props` struct:

<!--@include: ./_generated/props/020-props-heuristic.md-->

`Greeting(name string)` has a single non-struct param → gsx generates `GreetingProps{Name string}`. `Card(title string, n int)` has multiple params → gsx generates `CardProps{Title string; N int}`. `Panel(p Props)` has a single named-struct param → byo path; `Props` is used directly, no wrapper.

The generated `<Name>Props` struct gets an `Attrs gsx.Attrs` field when the component body explicitly references `attrs`, and a `Children gsx.Node` field when the body uses `{children}` — not unconditionally. The byo struct has neither unless the author declares them.

## Whole-struct splat

When the props value is already assembled — for example, loaded from a database, threaded through a page handler, or constructed with `cardData{Title: x}` — pass it whole with `{ x... }` instead of spelling out every field:

<!--@include: ./_generated/props/030-whole-struct-splat.md-->

`<Card { cardData{Title: d.Heading}... }/>` passes the constructed `cardData` struct directly: the codegen emits `Card(cardData{Title: d.Heading})`, bypassing field-by-field building. `<p.Content { pd... }/>` splats an existing `pageData` value onto a method component: `p.Content(pd)`.

Splat is all-or-nothing — you pass the full struct, not a partial update. Build or transform the struct before the tag; field-by-field attributes and splat cannot be mixed on the same call.

Splat also works on a **templ-interop / cross-package convention component** — one whose function and `<Name>Props` struct are hand-written in a sibling `.go` file rather than declared with `component` — as long as that struct has no `Attrs gsx.Attrs` bag. With no bag to merge into, `{ x... }` is unambiguously the whole prop value, so `<CheckboxPopupSelect { f... }/>` emits `CheckboxPopupSelect(f)`. A component that *does* declare an `Attrs` bag keeps the spread as an [attribute merge](./attributes.md#spread) instead — the bag is what the spread flows into, and it may even coexist with field attributes.

When a field-by-field prop value returns `(T, error)` — for example `<Row label={lookup(k)}/>` where `lookup` returns `(string, error)` — gsx auto-unwraps the tuple and propagates any non-nil error from `Render`; see [auto-unwrap](./interpolation.md#functions-t-error-auto-unwrap).

## Attrs-only component values

A package-level `var` or `func` is tag-callable with no `<Name>Props` struct at all when its static type is exactly one of two shapes: `func(gsx.Attrs) gsx.Node` or `func(...gsx.Attr) gsx.Node`. This is the fourth model, a **component value** — the tag resolves to a plain Go value rather than a `component` declaration. It only applies when the callee's package has no `<Name>Props` type; when one exists, the byo/generated/nullary discriminator above wins exactly as before.

Recognition is by the identifier's real static type, not by matching a declaration shape. Any initializer works — `var HomeIcon = namedIcon("house")`, a factory call, a conditional expression — because gsx asks what type the identifier actually resolves to. A type alias works too: `type Component = func(...gsx.Attr) gsx.Node` is transparent to the type system, so a `var` declared through the alias is recognized the same way. The bare unnamed `func([]gsx.Attr) gsx.Node` is deliberately excluded — it's assignable to `gsx.Attrs` under the same underlying-slice-type rule, but only the *named* `gsx.Attrs` spelling is recognized.

Both shapes are accepted because they serve different call sites:

- `func(gsx.Attrs) gsx.Node` takes the named bag type directly, so `.Has`, `.Merge`, and the rest of `gsx.Attrs`'s methods are available inside the function body with no conversion.
- `func(...gsx.Attr) gsx.Node` is callable with zero arguments — `HomeIcon()` — which matters because most call sites, tag or plain Go, pass no attrs at all. The non-variadic form always needs an explicit `HomeIcon(nil)`.

There's no field-matching step for this model — no struct, so nothing to match against. **Every** call-site attribute is fallthrough into one bag, merging in the same order as the synthesized `Attrs` bag above:

::: v-pre
bare attrs and `{ x... }` spreads and conditional attrs merge in source order, and the `attrs={{ "k": v }}` ordered literal merges last regardless of where it appears — see [Attributes — targeting the synthesized attrs bag](./attributes.md#targeting-the-synthesized-attrs-bag). `<HomeIcon class="w-5 h-5"/>` compiles to `HomeIcon(gsx.Attrs{{Key: "class", Value: "w-5 h-5"}})` (the variadic form takes a trailing `...`); a call with no attrs compiles to `HomeIcon(nil)` or `HomeIcon()` respectively.
:::

Component values don't support `{children}` — there's no field to receive it. Content between the tags on one of these is a generate-time error: "component values do not support children — declare a Children slot on a named-struct component instead." Struct fields, locals, and params are never tag-callable this way either: `<item.Icon/>` resolves `item` as a value rather than a package, so it stays on the `<Name>Props` convention path and fails there if no such struct exists.

A type that matches neither signature gets a clean diagnostic naming what it actually found: `<X> is not tag-callable: its type is T, not func(gsx.Attrs) gsx.Node or func(...gsx.Attr) gsx.Node, and no XProps struct was found`.

This is the escape from writing one wrapper `component` per call-site variation. A file of near-identical icon wrappers — one per icon, differing only in the name and the default `class` — collapses to one shared component plus a thin factory:

```gsx
// icons.gsx — the one real component, shared by every icon
type iconProps struct {
	Name  string
	Attrs gsx.Attrs
}

component renderIcon(p iconProps) {
	<svg { gsx.Attrs{{Key: "class", Value: "w-5 h-5"}}.Merge(p.Attrs)... }>{p.Name}</svg>
}
```

```go
// icons.go — the only new thing: an adapter making each icon tag-callable
func namedIcon(name string) func(gsx.Attrs) gsx.Node {
	return func(attrs gsx.Attrs) gsx.Node {
		return renderIcon(iconProps{Name: name, Attrs: attrs})
	}
}

var HomeIcon = namedIcon("house")
```

`<HomeIcon class="h-3 w-3"/>` renders `<svg class="w-5 h-5 h-3 w-3">house</svg>` — the default class declared inside `renderIcon` and the caller's override both land in the one bag `Attrs.Merge` composes. `<HomeIcon/>` (no attrs) renders `<svg class="w-5 h-5">house</svg>`. Sixty near-identical wrapper components collapse to sixty one-line `var` declarations plus the one shared `renderIcon`/`iconProps` pair.
