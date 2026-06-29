# Render-once (gap note)

## templ has `templ.Once`; gsx does not

templ ships `templ.Once` / `templ.OnceHandle` — a mechanism for components that emit shared page-level resources (a `<style>` block, a `<script>` module, a `<link>` preload) to guarantee those resources appear in the output at most once per page, regardless of how many times the component is rendered.

**gsx has no equivalent primitive.** There is no `gsx.Once`, no `OnceHandle`, no built-in deduplication of repeated HTML. This is an acknowledged gap — see the [Roadmap](../../ROADMAP) for the feature's planned status.

## Current workaround: single layout / shell component

The practical workaround is to emit shared page-level resources exactly once, from a component that itself renders once per page — typically your layout or shell component.

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

Leaf components that need a shared resource — say, a `<script>` for a custom element — have two choices:

1. **Move the resource to the layout.** Add the `<script>` tag once in the shell and rely on the module script's own idempotency (`<script type="module">` executes once per page regardless of how many `<script>` tags point to the same URL).

2. **Accept the duplicate.** For small inline snippets, emitting the same `<style>` or `<script>` block more than once is usually harmless — browsers handle duplicate `<style>` declarations correctly, and `<script>` blocks that are idempotent (defining a custom element with `customElements.define` guarded by `if (!customElements.get(...))`) execute safely more than once.

Neither workaround is a replacement for a real once primitive; they impose architectural constraints that `templ.Once` avoids. The feature is tracked in the roadmap.

## What `templ.Once` provides (for reference)

In templ you call `templ.NewOnceHandle()` to obtain a handle, then wrap a component with `.Once()` at the call site. The first render emits the wrapped content; subsequent renders of the same handle on the same request are no-ops. gsx's request-scoped `context.Context` thread already reaches every component, so when gsx adds a similar primitive it will use the same `ctx` mechanism — but the API does not exist yet.
