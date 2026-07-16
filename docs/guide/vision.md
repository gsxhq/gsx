# Why gsx

gsx is for server-rendered Go applications that want HTML-shaped components and
Go-checked call sites.

## HTML-shaped Go components

Write the markup where you would expect to find it:

```gsx
import "github.com/gsxhq/gsx"

component Card(title string, featured bool, children gsx.Node) {
  <section class={ "card", "card-featured": featured }>
    <h2>{ title }</h2>
    <div class="body">{ children }</div>
  </section>
}
```

Elements look like HTML, components use tags, and expressions are Go. See the
[Syntax reference](./syntax.md) for the complete language.

## Checked by Go

The authored component parameters are the emitted Go signature. Markup binds
them by exact name, while direct Go and framework callers pass the same values
positionally. For a long-lived keyed API, declare one application-owned options
struct parameter. See [Component signatures](./syntax/props.md).

## A build step with a fast dev loop

gsx generates `.x.go` files, then `go build` checks and compiles them. During
development, `gsx dev` watches the project, regenerates templates, rebuilds the
server, and coordinates browser reloads. See the [Development loop](./dev-loop.md).

## Works with the Go ecosystem

Helpers and application code remain ordinary Go. `gsx.Node` also has the same
`Render(context.Context, io.Writer) error` method set as `templ.Component`, so
the two component types compose structurally. See [Interop](./syntax/interop.md)
for runnable examples and [Comparisons](./comparisons.md) for choosing a tool.

## What gsx does not provide

gsx is a template compiler, not a router, HTTP framework, or client-side UI
runtime. Its runtime uses only the Go standard library. Dynamic values are
escaped by default; see [Escaping](./syntax/escaping.md) for the trust boundary.

> **Status — alpha.** The language and APIs may change before a stable release.
> See [Status](./status.md).
