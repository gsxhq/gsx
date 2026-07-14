# Context

Every component body has an ambient `ctx context.Context`. The context passed to `Render(ctx, w)` is available throughout the component tree without adding it as a prop.

## Read from context

Pass `ctx` to an ordinary typed helper that owns the key and fallback behavior.

<!--@include: ./_generated/context/010-reading-context.md-->

An unexported key type avoids collisions with keys from other packages. Context works well for request-scoped concerns such as authentication, locale, request IDs, tracing, and feature flags.

## Prefer props for application data

Use explicit, typed [props](./props.md) for data that directly determines what a component renders; the declaration and call site then show the dependency. Reserve context for values that are genuinely ambient across a request.
