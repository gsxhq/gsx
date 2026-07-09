# Interop

gsx components are plain Go values that implement a single interface. That design makes them composable with the wider Go templating ecosystem without any bridging layer.

For a higher-level choice guide, see [Comparisons](../comparisons.md).

## Working with templ

`gsx.Node` is declared in `node.go` as:

```go
type Node interface {
    Render(ctx context.Context, w io.Writer) error
}
```

This method set is **identical** to `templ.Component` (templ ≥ v0.3). The two interfaces are structurally compatible in Go: a value that satisfies one automatically satisfies the other. No adapter, no cast, and no `templ` import is needed on the gsx side.

### A gsx component inside a templ template

templ's `@` call syntax invokes `.Render(ctx, w)` on the target. Because a gsx component returns a `gsx.Node`, it fits the `@` call directly:

```go
// inside a .templ file — illustrative
@LLInformation(LLInformationProps{Item: item})
```

`LLInformation` is a gsx component (returns `gsx.Node`). templ's codegen calls `.Render(ctx, w)` on the return value — the same method gsx implements — so no wrapper is needed.

### A templ component inside gsx

The reverse works too. `templ.Component` satisfies `gsx.Node` structurally, so any `templ.Component` value slots straight into a `gsx.Node`-typed prop:

```go
// illustrative — passing templ.Raw as a Children prop
card.Card(card.CardProps{
    Title:    "My card",
    Children: templ.Raw("<p>body paragraph</p>"),
})
```

`templ.Raw(...)` returns a `templ.Component`; `Children` is typed `gsx.Node`. No conversion needed.

The same applies to a `templ.Component` field in any struct. If an existing library types a slot as `templ.Component`, a gsx node can be assigned to it directly:

```go
// illustrative
tab.Content = myGSXComponent(props)  // Content is templ.Component; myGSXComponent returns gsx.Node
```

### Children don't cross by calling convention

This is the one real gotcha. templ passes children through Go's `context` value — the `@comp { … }` syntax calls `templ.WithChildren` and the receiving component calls `templ.GetChildren`. gsx uses an **explicit `Children gsx.Node` prop** instead; it never reads children from context.

This means:

```
// inside a .templ file — this does NOT work as expected
@gsxCard(gsxCard.Props{Title: "T"}) {
    <p>This child is passed via templ context — gsx will not see it.</p>
}
```

The `<p>` block is stored in context by templ's runtime but `gsxCard` reads `Children` from its props struct, not from context. The child is silently dropped.

**Fix:** pass the child as an explicit prop value, not via the `{ … }` block:

```go
// illustrative — correct way to pass children from templ to a gsx component
@gsxCard(gsxCard.Props{Title: "T", Children: templ.Raw("<p>child</p>")})
```

### Framework composition

Any framework that renders a `Render(ctx context.Context, w io.Writer) error`
value — including [structpages](https://github.com/jackielii/structpages) — can
render gsx and templ components through the same method shape.

## Working with html/template

gsx has **no built-in bridge** to `html/template`. There is no `gsx.FromGoHTML` or `gsx.ToGoHTML` helper — this is a deliberate non-goal; the runtime is standard-library only and intentionally small. The two systems are bridged at the call site, not by the library.

### gsx output into an html/template

Render the gsx node to a `bytes.Buffer`, then wrap the result as `template.HTML` to tell `html/template` to trust it:

```go
// illustrative
var buf bytes.Buffer
if err := myComponent(props).Render(ctx, &buf); err != nil {
    return err
}
data := struct{ Body template.HTML }{
    Body: template.HTML(buf.String()),
}
goTmpl.Execute(w, data)  // {{ .Body }} in the Go template emits the raw HTML
```

The trust boundary here is intentional: gsx has already escaped its output, so wrapping it as `template.HTML` is safe. Only wrap gsx's own rendered output this way — never a raw user-supplied string.

### html/template output into gsx

Render the Go template to a buffer, then embed the result with `gsx.Raw`:

```go
// illustrative
var buf bytes.Buffer
if err := goTmpl.Execute(&buf, data); err != nil {
    return err
}
rendered := buf.String()
// in gsx markup:
//   { gsx.Raw(rendered) }
```

`gsx.Raw` writes the string verbatim, bypassing gsx's auto-escaping. The same trust boundary applies: `html/template` has already escaped the output, so treating it as trusted HTML is correct. Never pass unvalidated user input to `gsx.Raw`.

### React and other client-side islands

Client-side hydration is an HTTP-layer concern, not a gsx language feature. The typical pattern is: gsx renders the SSR shell (a `<div id="root">` or equivalent), and a bundled script hydrates the island on the client. gsx makes no assumptions about the client framework.
