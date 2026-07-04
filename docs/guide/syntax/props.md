# Props

The component author owns the props type. Bring your own named struct, let gsx
generate one from inline params, or declare no params at all. The param shape at
the declaration site is the discriminator: no config, no annotations.

## The three props models

The shape of a component's parameter list determines which model applies:

| Param shape | Model | Generated Go signature |
|---|---|---|
| **Single named-struct param** `component Button(p Props)` | **Bring-your-own (byo)** — gsx uses the author's type directly; no wrapper generated | `func Button(p Props) gsx.Node` |
| **Inline params** — multiple params or a single non-struct param | **Generated** `<Name>Props` — one field per param; `Children`/`Attrs` added when used | `func Greeting(p GreetingProps) gsx.Node` |
| **Nullary** — zero non-receiver params | **No props struct** — unless `{children}` or the explicit `attrs` bag is used, in which case gsx grows a minimal props type automatically | `func Shell() gsx.Node` |

The discriminator is *discoverable*: writing `(p Props)` where `Props` resolves to a named struct in the same package opts you onto the byo path. Receiver params (`component (p Page) Render()`) are not counted.

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

Splat also works on a **templ-interop / cross-package convention component** — one whose function and `<Name>Props` struct are hand-written in a sibling `.go` file rather than declared with `component` — as long as that struct has no `Attrs gsx.Attrs` bag. With no bag to merge into, `{ x... }` is unambiguously the whole prop value, so `<CheckboxPopupSelect { f... }/>` emits `CheckboxPopupSelect(f)`. A component that *does* declare an `Attrs` bag keeps the spread as an [attribute merge](./attributes#spread) instead — the bag is what the spread flows into, and it may even coexist with field attributes.

When a field-by-field prop value returns `(T, error)` — for example `<Row label={lookup(k)}/>` where `lookup` returns `(string, error)` — gsx auto-unwraps the tuple and propagates any non-nil error from `Render`; see [auto-unwrap](./interpolation#functions-t-error-auto-unwrap).
