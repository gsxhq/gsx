# Render-once

gsx does not yet have a render-once primitive. Put shared page resources in a
layout or shell component that renders once per request.

## Status

There is no `gsx.Once`, no `OnceHandle`, and no built-in deduplication of
repeated HTML. The gap is tracked in the
[Roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).

## Workaround: layout or shell component

Emit shared resources from a component that renders once per page:

```go
// layout.gsx — renders once per request; safe to place shared resources here
component Layout(p LayoutProps) {
  <!DOCTYPE html>
  <html lang="en">
    <head>
      <meta charset="UTF-8"/>
      <title>{ p.Title }</title>
      <link rel="stylesheet" href="/static/main.css"/>
      // Put component-specific shared styles here, not in leaf components
      <style>
        .highlight { background: yellow; }
      </style>
    </head>
    <body>{ p.Children }</body>
  </html>
}
```

Leaf components that need a shared resource have two choices:

1. **Move the resource to the layout.** Add the `<script>` tag once in the shell and rely on the module script's own idempotency (`<script type="module">` executes once per page regardless of how many `<script>` tags point to the same URL).

2. **Accept the duplicate.** Use this only for snippets that are safe when emitted
   more than once, such as guarded custom-element registration.
