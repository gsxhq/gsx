# Syntax

> **Syntax is roughly fixed, not frozen.** This page is a quick tour. The
> [test corpus](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases)
> is the canonical, always-current reference — every accepted form is a case that
> parses, generates Go, and pins its rendered output, so it can never drift from
> what the compiler actually does.

A `.gsx` file is ordinary Go (package, imports, types, funcs) plus `component`
declarations. A component has a templ-style header and a JSX-style body — the
markup *is* the result, so there is no return type and no `return`:

```gsx
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
	</section>
}
```

## Elements vs components

Capitalization decides what a tag means:

- lowercase / hyphenated → HTML element: `<div>`, `<el-dialog>`
- Capitalized / dotted → component: `<Card>`, `<ui.Button>`, `<p.Content>`

## Component props — author owns the type

The **param shape decides** which props model is used:

| Param shape | Model | Generated signature |
|-------------|-------|---------------------|
| **Single named-struct param** (`component Button(p Props)`) | **Bring-your-own (byo)** — use the author's type directly; no wrapper generated | `func Button(p Props) gsx.Node` |
| **Inline params** — multiple params or a single non-struct param | **Generated** `<Name>Props` struct (field per param + `Children`/`Attrs` when used) | `func Card(p CardProps) gsx.Node` |
| **Nullary** — zero non-receiver params | **No props** (unless `{children}` or fallthrough attrs are used, in which case a gsx-owned props is grown) | `func Shell() gsx.Node` |

The discriminator is *discoverable*: writing `(p Props)` (where `Props` resolves to a
named struct in `go/types`) opts you onto the byo path. Receiver params are not counted.

### Byo path — field-build

When gsx builds a byo-path component tag, it maps each attribute to a field of the author struct:

```gsx
<Button variant="primary" featured full-width data-id="7">Save</Button>
```
→
```go
Button(Props{
    Variant:   "primary",
    Featured:  true,            // bare bool attr → true
    FullWidth: true,            // kebab→Camel
    Attrs:     gsx.Attrs{"data-id": "7"},  // no matching field → Attrs bag
    Children:  gsx.Func(/* "Save" */),     // children → Children field
})
```

**Field matching** (default, in order):
1. Identifier → Go-capitalized: `variant` → `Variant`, `fullWidth` → `FullWidth`
2. Kebab → CamelCase: `full-width` → `FullWidth`, `aria-label` → `AriaLabel`
3. No matching field → falls through to the `Attrs gsx.Attrs` field

**`Children` and `Attrs` are explicit** on the byo path — the author's struct must
declare `Children gsx.Node` to use `{children}`, and `Attrs gsx.Attrs` to accept
unmatched attrs. Missing the field is a clear codegen error.

### Byo path — whole-struct splat `{ x... }`

Pass a prebuilt struct as the entire prop value — the dominant real-world pattern:

```gsx
<Button { data... }/>
```
→
```go
Button(data)
```

Splat is all-or-nothing; build or modify the struct before passing it. The splat
syntax is also the way method components and structpages page handlers receive a
shared props type:

```gsx
<p.Content { pd... }/>   // → p.Content(pd)
```

## Quick reference

| Form | Meaning |
|------|---------|
| `component X(params) { … }` | component declaration (emission body — no return) |
| `component (p T) Name(params) { … }` | method component (receiver) |
| `<div>`, `<el-dialog>` | HTML element (lowercase / hyphenated) |
| `<Card>`, `<ui.Button>` | component (Capitalized / dotted) |
| `{ expr }` | interpolation in body (auto HTML-escaped) |
| any expression returning `(T, error)` | auto-unwraps to `T`; error propagates from the enclosing `Render` — no marker needed, applies in all expression positions (text, attrs, child-prop values, `{{ }}` pair values, pipelines) |
| `name="lit"` | static string attribute |
| `name={ expr }` | dynamic attribute (Go expression) |
| `name` (bare) | boolean attribute = `true` |
| `disabled={ cond }` | type-driven boolean attr (bool → bare/omitted) |
| `{ expr... }` | spread/splat — on an **element**: spreads `gsx.Attrs` as HTML attrs; on a **component**: whole-struct splat (passes the prebuilt struct as props) |
| `name={{ "k": v, "k2": v2 }}` | ordered-attrs literal — binds to a `gsx.OrderedAttrs` prop; renders in source order |
| `{ if … }` / `{ for … }` inside a tag | conditional attributes |
| `{ if/for/switch … { <markup> } }` | control flow contributing children |
| `{{ stmt }}` | Go statement escape hatch (no output) |
| `<>…</>` | fragment |
| `class={ a, "cls": cond }` | composable `class`/`style` (comma list; conditional sugar) |
| `{children}` | explicit children placement |
| `gsx.Raw(s)` | unescaped HTML |

## Spread operator — trailing `{ x... }`

The spread operator mirrors Go convention (trailing dots, as in `f(x...)`):

```gsx
<div { attrs... }>          {/* element: spreads gsx.Attrs as HTML attributes */}
<Card { data... }/>         {/* component: whole-struct splat → Card(data)    */}
<ui.Button { btn... }/>     {/* dotted component: same trailing-splat syntax  */}
```

The context (element vs component tag) determines the meaning — no type resolution
is needed. The grammar treats both as the same `spread_attribute` node in the
attribute list; the code generator interprets it based on the tag kind.

## Ordered attributes — `{{ "k": v }}`

HTML attribute order is usually irrelevant, but some frameworks depend on it.
[Datastar](https://data-star.dev/) processes `data-*` directives sequentially, so
a `data-signals` initializer **must** precede any directive that reads it. A plain
Go `map` (the `gsx.Attrs` type) renders in sorted key order, which may not be the
order you want. `gsx.OrderedAttrs` solves this: it is a `[]gsx.Attr` slice that
renders in the exact order you write it.

### The `{{ }}` literal

Declare a component prop of type `gsx.OrderedAttrs` and pass a value with the
double-brace literal at the call site:

```gsx
component Counter(signals gsx.OrderedAttrs) {
    <button { signals... }>{children}</button>
}

component Page() {
    <Counter signals={{ "data-signals": "{count:0}", "data-text": "$count", "data-on-click": "$count++" }}>
        Count
    </Counter>
}
```

renders as:

```html
<button data-signals="{count:0}" data-text="$count" data-on-click="$count++">Count</button>
```

The Datastar directives arrive in exactly the order they appear in the literal —
`data-signals` first so it is defined before `data-text` and `data-on-click` read it.

### Syntax rules

- **Keys** are **quoted string literals** (e.g. `"data-on-click"`, `"hx-on:click"`).
  Quoting is required so that kebab and colon names need no special handling.
- **Values** are **Go expressions** — a string literal, an ident, a selector, a
  function call, a composite literal, or any other valid Go expression.
  A `|>` filter pipeline is not supported inside a `{{ }}` value (use a plain
  Go expression; `|>` remains available in normal `name={ expr |> … }` form).
  A value returning `(T, error)` is **auto-unwrapped** — the error propagates
  from the enclosing `Render` exactly as in any other expression position (see
  [Error propagation](#error-propagation--automatic-t-error-unwrap) below).
- **Boolean values** toggle a bare attribute: `"data-show": true` renders
  `data-show`; `"data-show": false` omits the attribute entirely.
- A **trailing comma** is allowed (idiomatic Go style). An **empty** literal
  `{{ }}` is valid (renders nothing). A leading or interior stray comma is an
  error.
- **Whitespace** around the `=` is tolerated for all attribute value forms
  (`name = {{ … }}`, `name = { … }`, `name = "…"`); `gsx fmt` normalizes all of
  them to the canonical `name=value` form.

### Prop binding and spreading

The `{{ }}` literal binds to the component prop whose name maps to a
`gsx.OrderedAttrs` field (the usual kebab→CamelCase rule applies:
`container-attrs` → `ContainerAttrs`). Inside the component, spread the value
onto any element with `{ prop... }`:

```gsx
component Card(containerAttrs gsx.OrderedAttrs) {
    <div class="container" { containerAttrs... }>{children}</div>
}

component Page() {
    <Card container-attrs={{ "data-signals": "{open:false}", "data-text": "$open" }}>
        content
    </Card>
}
```

The bag can be forwarded through multiple component layers — each layer declares a
`gsx.OrderedAttrs` prop and passes it down — and is finally spread onto an element
at whichever depth is appropriate.

### Plain elements — use direct attrs or spread a declared prop

`{{ }}` is **only valid as the value of a declared `gsx.OrderedAttrs` component
prop**. Writing it directly on a plain HTML element attribute is an error:

```gsx
{/* ERROR — {{ }} is not valid here */}
<div data-x={{ "data-a": "1" }}>…</div>
```

For a plain element, plain attributes already render in source order, so there is
nothing to gain from `{{ }}`. To conditionally reuse an ordered bag, thread a
`gsx.OrderedAttrs` prop down to the element:

```gsx
{/* ok — spread a declared prop */}
<div { myAttrs... }>…</div>
```

### Comparing `gsx.Attrs` and `gsx.OrderedAttrs`

| | `gsx.Attrs` (map) | `gsx.OrderedAttrs` (slice) |
|---|---|---|
| Go type | `map[string]any` | `[]gsx.Attr{Key, Value}` |
| Render order | **sorted** key order (deterministic, like `Spread`) | **source / slice** order |
| Duplicate keys | last write wins (map semantics) | duplicates allowed and emitted |
| Class/style merge | participates when spread via `{…attrs}` fallthrough | **no** — pairs emit verbatim |
| Literal syntax | no dedicated literal (build the map in Go) | `{{ "k": v, … }}` |
| Best for | general "extra HTML attributes" bag; fallthrough | order-sensitive frameworks (Datastar, Stimulus) |

### Security

Values are attribute-escaped identically to `gsx.Attrs` — the same faithful
`html/template` port. Attribute names are validated; structurally unsafe names
(names containing spaces or other forbidden characters) are silently dropped.
`{{ }}` does not bypass any escaping.

## Error propagation — automatic `(T, error)` unwrap

Any Go expression that returns exactly two values where the second is `error` is
automatically unwrapped by gsx: the first value is used as the result and a
non-nil error is returned from the enclosing component's `Render`, halting
rendering at that point. No marker is needed — gsx does it unconditionally. (A
`?` suffix on an expression is in fact a parse error.)

The rule is **uniform**: it applies in every position where an expression is
allowed.

| Position | Example |
|----------|---------|
| Text / body interpolation | `{lookup(key)}` |
| Element attribute value | `attr={signedURL(p)}` |
| `<style>` / `<script>` body | interpolated values in raw-text bodies |
| JS-context attribute value (`onclick`/`@click`/`hx-on*`) | `onclick={ handler(action) }` |
| `\|>` pipeline stages | each stage's return is unwrapped if `(T, error)` |
| Children / slot | `{ renderSlot(ctx) }` |
| **Child-component prop value** | `<Card title={lookup(t)}/>` |
| **`{{ }}` pair value** | `<Card signals={{ "data-signals": signals(s) }}/>` |

### Child-component prop values

```gsx
// func lookup(t string) (string, error)
component Page(t string) {
    <Card title={lookup(t)}/>
}
```

If `lookup` returns a non-nil error, `Render` returns it immediately — no `err`
variable, no `if err != nil` at the call site.

### `{{ }}` pair values

```gsx
// func computeSignals(s State) (string, error)
component Page(s State) {
    <Card container-attrs={{ "data-signals": computeSignals(s) }}/>
}
```

Each pair value in a `{{ }}` literal is an independent expression; they evaluate
in **source order**, so the first non-nil error wins.

### Constraints

- Only `(T, error)` is supported: **exactly two return values, the second typed
  `error`**. Any other multi-value shape — `(int, string)`, three values, etc. —
  is a **compile-time gsx error**: `only (T, error) is supported`.
- The unwrap is always implicit — there is no opt-in marker and no opt-out.
- When multiple prop values in a single child-component call each return
  `(T, error)`, all are hoisted to temporaries in **source order** before the
  component literal is built.

## Markup vs Go (the one subtlety)

Inside `{ }`, gsx decides markup-vs-Go positionally — the Babel rule: `{ <div/> }`
is markup, `{ a < b }` is a Go expression. When in doubt, see the
[`parser/`](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases/parser)
corpus cases.

## Escaping & safe contexts

Encoding is **automatic and context-aware** — you write the value, gsx picks the
escaper from *where* it sits (the codegen knows the context). Helpers are
**opt-outs** for trusted values, never required for safety.

| Context | What gsx does | Opt-out (trusted) |
|---------|---------------|-------------------|
| Text / attribute (`{ x }`, `attr={ x }`) | HTML / attribute escape | `gsx.Raw(s)` |
| URL attribute (`href`, `src`, `action`, `hx-*`, …) | scheme-sanitize + escape | `gsx.RawURL(s)` |
| JS value (`@{ x }` in `<script>` or a JS attr like `x-data`/`@click`/`hx-on*`) | **JSON-encode** (HTML-safe), Go value → JS literal | `gsx.RawJS(s)` |
| JSON data island (`<script type="application/json">@{ data }</script>`) | **JSON-encode** the whole body | — |
| CSS value (`<style>` body, CSS-context attrs) | value-filter (`gw.CSS`); risky tokens like `(` `/` collapse to a safe placeholder | `gsx.RawCSS(s)` |

**JSON and CSS are automatic, not filters.** Any JS-value position JSON-encodes via
the runtime `JSVal`; CSS values (`<style>` bodies, `style=` and CSS-context attrs,
composable `style={ … }`) auto value-filter via `gw.CSS`/`gw.Style`. There is no
`|> json` or `|> css`. Every context above is safe by default — **CSS is just the
most conservative** (its value-filter drops `(`/`/`, so a dynamic
`rgb(...)`/`calc(...)`/`url(...)` needs `gsx.RawCSS`). The one genuinely
*fail-closed* context is a **JS event-handler expression value** (`onclick={ … }`,
`@click={ … }`, `hx-on*`), which is a compile error — use `gsx.RawJS` for trusted
JS. See the `security/`, `style/`, `jsattr/`, and `datajson/` corpus cases.

## Learn by example

Each topic maps to a directory of [corpus cases](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases)
— every case is a `.txtar` holding the `.gsx` input, the generated Go, and the
rendered output, all verified on every test run.

| Topic | Corpus cases |
|-------|--------------|
| Elements, void, DOCTYPE, SVG, web components | `elements/`, `doctype/` |
| Interpolation, raw HTML, escaping contexts | `interpolation/`, `security/` |
| if / for / switch, fragments | `control_flow/` |
| `component` decls, props, `{children}`, slots | `components/`, `slots/` |
| The full attribute system | `attrs/`, `class/`, `style/`, `jsattr/` |
| Ordered attributes (`{{ }}` / `gsx.OrderedAttrs`) | `orderedattrs/` |
| `|>` pipelines & filters | `pipelines/` |
| Markup-vs-Go corner cases | `parser/` |
| Method components, page composition | `methods/` |
| Children & attribute fallthrough | `fallthrough/` |
| Byo props: field-build, splat, shared props | `props/` |

> **Status — alpha.** `.gsx` compiles to plain Go via `gsx generate`; syntax is
> stable but still evolving. Follow the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
