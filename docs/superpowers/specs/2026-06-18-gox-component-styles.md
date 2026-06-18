# gox — Two component styles, side by side

**Date:** 2026-06-18
**Status:** Design exploration — deciding whether to support both styles

Two real-world patterns (both observed in his-project / one-learning):

- **Style A — package of function components.** Reusable component libraries. One
  package per component family; each part is a `component` function with its own
  (generated) props. This is exactly how `ds/card`, `ds/dialog`, … are built.
- **Style B — struct with method components.** App page composition + partial
  rendering. A page struct holds the page's data; methods render the whole page or
  swappable partials, sharing data via the receiver. This is the structpages
  `(p Page) Page()/Content()` pattern.

`ctx` is **implicit** in every component body (ambient, like templ) — never a
declared param.

---

## Reusable component → Style A is the natural fit

The `ds/card` family in gox (compare to the real `card.templ`):

```go
package card

// inline params → generated CardProps{ID, Class}; `attrs`/`children` implicit.
component Card(id string, class string) {
	<div
		{ if id != "" { id={id} } }
		class={ "w-full rounded-lg border bg-card text-card-foreground shadow-xs", class }
		{...attrs}
	>
		{children}
	</div>
}

component Header(id string, class string) {
	<div { if id != "" { id={id} } } class={ "flex flex-col space-y-1.5 p-6 pb-0", class } {...attrs}>
		{children}
	</div>
}

component Title(id string, class string) {
	<h3 { if id != "" { id={id} } } class={ "text-lg font-semibold leading-none tracking-tight", class } {...attrs}>
		{children}
	</h3>
}
```
Used cross-package: `<card.Card class="mt-4"><card.Title>Hi</card.Title></card.Card>`.
(Much tidier than templ: no explicit `Props` structs, no `TwMerge(...)` wrapper —
the class merger is the configured external transform — no `{ children... }`
ceremony.)

---

## Page composition → both styles, compared

A users list page with a swappable `Content` partial. **The data is `title` +
`rows`, shared by every partial.**

### Style A — package of function components (data threaded as params)

```go
package users

component Page(title string, rows []Row) {
	<ui.AppShell title={title}>
		<Content title={title} rows={rows}/>
	</ui.AppShell>
}

component Content(title string, rows []Row) {
	<div id={ structpages.ID(ctx, List{}) } hx-get={ structpages.URLFor(ctx, List{})? } hx-swap="outerHTML">
		<h1>{title}</h1>
		<Grid rows={rows}/>
	</div>
}

component Grid(rows []Row) {
	<table><tbody>
		{ for _, r := range rows { <Row row={r}/> } }
	</tbody></table>
}

component Row(row Row) {
	<tr data-row-id={row.ID}><td>{row.Email}</td><td>{row.Role}</td></tr>
}
```
`ctx` ambient. Partials are independent same-package functions; **`title`/`rows`
are threaded through every call.** Reusable in isolation; verbose when many
partials share the same data.

### Style B — struct with method components (data in the receiver)

```go
package users

type UsersPage struct {
	Title string
	Rows  []Row
}

component (p UsersPage) Page() {
	<ui.AppShell title={p.Title}>
		<p.Content/>
	</ui.AppShell>
}

component (p UsersPage) Content() {
	<div id={ structpages.ID(ctx, List{}) } hx-get={ structpages.URLFor(ctx, List{})? } hx-swap="outerHTML">
		<h1>{p.Title}</h1>
		<p.Grid/>
	</div>
}

component (p UsersPage) Grid() {
	<table><tbody>
		{ for _, r := range p.Rows { <p.Row row={r}/> } }
	</tbody></table>
}

component (p UsersPage) Row(row Row) {
	<tr data-row-id={row.ID}><td>{row.Email}</td><td>{row.Role}</td></tr>
}
```
`ctx` ambient. **The receiver `p` carries the shared data — no threading.** Each
method is a natural HTMX partial (swap `<p.Content/>` alone). The struct *is* the
props; method params (`Row(row Row)`) map to attributes by name. This is the
structpages page pattern.

---

## Props model per style

- **Style A (function):** inline params → generated `<Name>Props` struct + `func
  Name(NameProps) gox.Node`. Attributes build the props at the call site. Optionals
  (zero value), spread, cross-package determinism. Matches `ds/*`.
- **Style B (method):** the **receiver struct is the props** — its fields are the
  page data, set once in Go (by the route handler/structpages), not via
  attributes. Method params map to attributes by name (same-package, type-resolved).
  No generated `<Name>Props`.

## Correctness: the disambiguation seam

The only correctness-sensitive question is what `<x.Foo/>` means. `go/parser`'s
scope resolves it with **no ambiguity**:

| Tag | `x` is… | Means |
|-----|---------|-------|
| `<Content/>` | (no dot) | same-package function component → `Content(ContentProps{…})` |
| `<card.Card/>` | an imported package | cross-package function component → `card.Card(card.CardProps{…})` |
| `<p.Content/>` | a local var / receiver | method component → `p.Content(…)` |

Package vs. local variable is exactly what scope analysis answers; there is no
overlap. So **supporting both styles costs no correctness** — the seam is the
identifier's binding, which the parser already knows.

## Recommendation

**Support both.** They are not redundant — they serve different jobs the real
codebases already split:

- **Style A (packages of function components)** → reusable component libraries
  (`ds/*`).
- **Style B (struct method components)** → app page composition + partial rendering
  (structpages pages).

`ctx` is ambient in both. The function/method distinction is the receiver, which
the parser sees; props are generated `XProps` for functions and the receiver
struct for methods. No correctness is sacrificed for the flexibility.

## To update if approved

- Spec §3: `ctx` is ambient (not a declared/auto-wired param); document the two
  styles and their props models; drop the `<Receiver><Method>Props` generation
  (method params map by name; the receiver is the props).
- Rewrite `examples/11_struct_methods.gox`: keep the page-composition examples,
  remove the component-family-as-struct cases (Menu, receiver polymorphism) —
  those belong in packages (Style A).
