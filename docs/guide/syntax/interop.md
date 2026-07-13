# Interop

`gsx.Node` and `templ.Component` share the same `Render(context.Context,
io.Writer) error` method, so they work together without an adapter. For help
choosing a template system, see [Comparisons](../comparisons.md).

## templ

### Render gsx from templ

Call a gsx component directly from a `.templ` file:

```templ
templ Page() {
	@views.Card(views.CardProps{Title: "Welcome"})
}
```

`views.Card` returns a `gsx.Node`, which also satisfies `templ.Component`.

### Render templ from gsx

Accept a `gsx.Node`, then pass a templ component to it:

```gsx
component Shell(body gsx.Node) {
	<main>{body}</main>
}
```

```go
page := views.Shell(views.ShellProps{
	Body: templComponent, // templComponent is a templ.Component
})
```

The templ component satisfies `gsx.Node` structurally and renders in place.

### Children stay explicit

templ passes block children through `context.Context`; gsx receives children
through an explicit `Children gsx.Node` prop. Pass the child value directly:

```templ
templ Child() {
	<p>Child</p>
}

templ Page() {
	@views.Card(views.CardProps{Children: Child()})
}
```

Do not use templ's `@views.Card(...) { ... }` block form for a gsx component:
gsx does not read `templ.GetChildren` from the context.

## `html/template`

### Render gsx into a template

Render the node, then pass the complete result as `template.HTML`:

```go
var body bytes.Buffer
if err := views.Page(props).Render(ctx, &body); err != nil {
	return err
}

return tmpl.Execute(w, struct{ Body template.HTML }{
	Body: template.HTML(body.String()),
})
```

Bind the Go template's `Body` field where the rendered node should appear. This
conversion crosses a trust boundary: wrap only rendered output you trust, never
an unvalidated string.

### Render a template into gsx

Render the template, then pass the complete result through `gsx.Raw`:

```go
var body bytes.Buffer
if err := tmpl.Execute(&body, data); err != nil {
	return err
}

return views.Shell(views.ShellProps{
	Body: gsx.Raw(body.String()),
}).Render(ctx, w)
```

`gsx.Raw` bypasses HTML escaping. Use it only for output produced by a trusted
`html/template`, never for unvalidated input.

## Client-side islands

Render the server-owned shell and serialized data with gsx, then load the
client bundle that mounts or hydrates the island:

```gsx
component ChartIsland(data ChartData) {
	<div id="chart"></div>
	<script type="application/json" id="chart-data">@{data}</script>
	<script type="module" src="/assets/chart.js"></script>
}
```

See [JavaScript](./javascript.md#json-data-islands) for JSON data islands and
script interpolation.
