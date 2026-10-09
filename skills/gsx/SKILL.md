---
name: gsx
description: Use when writing, editing, reviewing or migrating .gsx files (gsx Go templating) — components, attributes, class/style, Alpine/htmx attributes, inline text — or when a gsx page renders ZgotmplZ, loses the space between words, or seems to need gsx.Raw, RawJS, RawCSS or RawURL.
---

# Writing gsx

gsx is Go plus JSX-style markup: `.gsx` compiles to `.x.go` (generated, never
hand-edit). Every value is escaped for the place it lands, so **keep the syntax
static in markup and interpolate only values**. Most bad gsx is a Go or templ
habit: build a string in Go, hand it to markup.

## Existing code is not the style guide

Codebases migrated from templ or written by earlier agents contain every
pattern in the table below. "It matches the existing code" is not a reason to
write it again. New and edited lines use the gsx form.

- **Live bugs in a component you edit — fix them and tell the user.** A
  string-built JS attribute is an injection hole; a string-built `style` that
  can hold `(`, `)` or `--` renders `ZgotmplZ`. These are bugs, not style, even
  on lines the task didn't mention.
- **A new requirement needs an existing component to take a native attribute**
  (`aria-label`, `hx-*`, `id`…): add `attrs gsx.Attrs` to it and spread it.
  That is part of the task. Don't hand-copy its markup or classes next to it.
- Other smells on lines you don't otherwise touch: mention them, leave them.

## Before you write it

| About to write | Write instead |
|---|---|
| `@click={"f('" + id + "')"}`, `onclick={…}`, `x-data={…}` built from strings | same attribute, `js` literal: ``@click=js`f(@{id})` ``, ``onclick=js`f(@{id})` `` |
| `gsx.RawJS("f('" + id + "')")`, JS from `fmt.Sprintf` | a `js` literal; `gsx.RawJS` only for a trusted assignment target: ``js`@{gsx.RawJS(path)} = v` `` |
| JSON for `hx-vals` / `data-*` / `x-data` via `json.Marshal` or Sprintf | ``hx-vals=js`{"id": @{id}, "opts": @{opts}}` ``, whole value ``data-points=js`@{points}` `` |
| `style={"background:" + c}`, `style={fmt.Sprintf("width:%d%%", n)}` | ``style=css`width: @{n}%; background: var(--color-@{tone})` `` |
| `gsx.RawCSS` / `gsx.RawURL` / `gsx.Raw` to make something render | fix the shape instead (rows above); Raw only for content you control end to end, with a comment saying where it comes from |
| `id={fmt.Sprintf("row-%d", id)}`, `hx-target={"#" + id}`, `href={"/t/" + id}` | ``id=f`row-@{id}` ``, ``hx-target=f`#row-@{id}` ``, ``href=f`/t/@{id}` `` |
| `func statusClass(s string) string`, `utils.If(…)`, `class={"base " + x}` | `class={ "base", switch s { case "open": "bg-yellow-100" default: "bg-gray-100" }, "ring-2": selected }` |
| two copies of an element that differ in one attribute, class or word | one element: `disabled={closed}`, `class={ …, "text-yellow-500": starred }`, `{ if on { fill="currentColor" } else { fill="none" } }`, `{ if on { Starred } else { Star } }` |
| `aria-pressed={strconv.FormatBool(b)}`, if/else writing `"true"`/`"false"` | `aria-pressed={b}` |
| `{ fmt.Sprintf("%d", n) }`, `strconv.Itoa(n) + " comments"`, `cmp.Or(x, "—")`, a `labelFor(x) string` helper | `{ n } comments`, `{ x \|> default("—") }`, the project's filters and renderers (`gsx info` lists them) |
| `must(f())`, or `{{ v, err := f(); if err != nil { return err } }}` for a value used once | the call as the whole hole: `id={lookupID(ctx, key)}`, ``href=f`/t/@{lookupID(ctx, key)}` ``; it unwraps there and the error returns from `Render`. Not inside a struct literal or call, see below |
| `{ Card(CardProps{…}) }`, `{ Badge("x", "green") }` | `<Card title="x">…</Card>`, `<Badge tone="green">x</Badge>` |
| `type ButtonProps struct{ Type, Disabled, HxPost, Class, Label string… }` | `component Button(variant string, children gsx.Node, attrs gsx.Attrs)`, see Components |
| `gsx.Raw("<!-- note -->")`, `{{ /* note */ }}` | `// note` at line start, or `{/* note */}` |

Why these matter:

- **A plain `{expr}` never becomes code.** On an event handler (any `on…`
  name) a `{string}` is encoded as a JS *string literal*, so a handler built
  from strings renders inert. Framework attributes (`@click`, `x-*`,
  `hx-on`) get no inference: a `{string}` is only HTML-escaped, so a `'` in the
  data breaks out of your JS string. Inside
  `js`…``, `@{v}` is JSON-encoded: strings arrive quoted, structs, maps and
  slices become JSON.
- **Dynamic CSS is filtered, whole value.** A `style={expr}` value or a `css`
  hole containing `( ) ; / " ' @ [ ] { } < > \` or `--` becomes `ZgotmplZ`, so
  `var(--x)`, `oklch(…)` and `calc(…)` built in Go always fail. Static text in a
  `css` literal is never filtered: put `var(--color-` in the literal and only
  `chart-2` in the hole. The same holds for a hole: ``css`background: @{c}` ``
  with `c = "var(--color-chart-2)"` still renders `ZgotmplZ`. Make the value
  the token (change the field or its producer if needed). Only when the full
  value is a developer-owned constant (a theme table, not user or DB data)
  write `@{gsx.RawCSS(c)}`, with a comment naming where it comes from.
- **Value-form `if`/`switch` works only inside `class={…}` and `style={…}`
  lists.** For any other attribute use a conditional attribute block:
  `{ if c { data-state="open" } else { data-state="closed" } }`.
- **Fallible calls go in the hole.** A `(T, error)` call is hoisted ahead of
  the write and unwrapped when it is the whole hole: text, native
  attributes, component inputs, `f`/`js`/`css` holes, pipeline stages,
  class/style parts. Holes evaluate in source order, and the first non-nil
  error stops rendering and returns from `Render`. So no `must()`, no
  string-returning wrapper that swallows the error, and no `{{ }}` pre-compute
  for a value used once. Inside a larger Go expression (a struct-literal
  field, a call argument) the call is plain Go and won't compile. For a
  `string` there, an `f` hole still unwraps:
  ``props={ RootProps{ Target: f`@{lookupID(ctx, key)}` } }``. Otherwise bind
  it in a `{{ v, err := f(); if err != nil { return err } }}` block, as you
  also do when one result feeds several holes or the error needs wrapping.
  Use an `if` init to handle an error in place:
  `{ if v, err := f(); err != nil { <p>unavailable</p> } else { … } }`. A
  helper taking `ctx` that many templates call belongs in a filter, which gets
  `ctx` injected: `{ key |> lookupID }`.
- **Bool attributes:** `true`/`false` render as the strings `"true"`/`"false"`
  only on `aria-*`, `contenteditable`, `spellcheck`, `draggable`. Everywhere
  else a bool means presence: `data-open={b}` renders bare `data-open` or
  nothing. Need the text on `data-*`? `strconv.FormatBool(b)`.

## Whitespace: text across lines

A line break **between two words of text** renders as one space. A line break
**next to an element or `{expr}`** renders as nothing.

```gsx
<p>
	{ t.Code } · { t.Product } · submitted by
	{ t.Name }
	See the <a href={u}>guide</a> for
	details.
</p>
```

renders `C · P · submitted byJaneSee the <a…>guide</a> for details.`: both
breaks next to `{ t.Name }` vanish; the one between `for` and `details` is a
space.

Break lines between plain words, keep `word <b>x</b> word` runs on one line,
or write an explicit `{" "}` at the break. Whitespace just inside control-flow
braces is also dropped, so a separator that belongs to the conditional part
starts with `{" "}`:

```gsx
{ t.N } comments{ if t.Owner != "" { {" "}· owned by { t.Owner } } }
```

Do not collapse the line into a Go string (`fmt.Sprintf`, a helper, or one big
`f` literal) just to dodge this.

## Components

```gsx
component Button(variant string, size string, children gsx.Node, attrs gsx.Attrs) {
	<button
		type="button"
		class={ "btn", switch variant { case "danger": "btn-danger" default: "btn-primary" }, "btn-sm": size == "sm" }
		{ attrs... }
	>{ children }</button>
}

component Card(header gsx.Node, children gsx.Node) {
	<section class="card">
		{ if header != nil { <header>{ header }</header> } }
		{ children }
	</section>
}

component TicketPanel(t Ticket, closed bool) {
	<Card header={ <h2>Tickets</h2> }>
		<Button variant="danger" hx-post=f`/tickets/@{t.ID}/close` disabled={closed} aria-label=f`Close @{t.Code}`>Close</Button>
	</Card>
}
```

- Parameters are the semantic API (variant, size, a domain value). Native
  attributes (`type`, `disabled`, `hx-*`, `aria-*`, extra `class`) travel in
  `attrs` and land where you spread `{ attrs... }`.
- A scalar written before the spread is a default the caller can override;
  after the spread it is forced. `class` and `style` always merge.
- Extra markup positions are `gsx.Node` parameters (named slots); `children`
  is the tag body.
- Inside a component, read a caller's bool with `attrs.Bool("disabled")`, not
  `attrs.Has` (`Has` is true for `disabled={false}`).

## Red flags: stop and use the table

- "Matching the existing pattern in this file"
- "gsx knows `@click` / `onclick` is JavaScript"
- "Wrap it in `gsx.RawCSS` / `RawJS` / `RawURL` so it renders"
- "A small helper returning the class or label string is cleaner"
- "Build the line in Go so whitespace can't bite"
- "Duplicate the element, it's only two branches"
- "Compute it in a `{{ }}` block first / wrap it in `must()`" (for a value used once that can be the whole hole)
- "I used the literal, so it's idiomatic" (while the hole still holds `var(…)`)
- "Adding attrs to that component is out of scope, I'll write a plain button"
- "That injection / `ZgotmplZ` predates my task"

## Reference

- Docs matching the project's gsx version ship inside the module:
  `$(go list -m -f '{{.Dir}}' github.com/gsxhq/gsx)/docs/guide/syntax/` —
  `attributes.md`, `styling.md`, `javascript.md`, `composition.md`,
  `props.md`, `pipelines.md`. Online: https://gsxhq.github.io/guide/syntax
- Reviewing or cleaning up existing gsx: `bash scripts/find-smells.sh <dir>`
  (in this skill's directory) lists candidate sites for every row above.
