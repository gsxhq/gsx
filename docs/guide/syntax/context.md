# Context

Every component body has an ambient `ctx context.Context`. The context passed to `Render(ctx, w)` is available throughout the component tree without adding it as a parameter.

## Read from context

Pass `ctx` to an ordinary typed helper that owns the key and fallback behavior.

<!--@include: ./_generated/context/010-reading-context.md-->

An unexported key type avoids collisions with keys from other packages. Context works well for request-scoped concerns such as authentication, locale, request IDs, tracing, and feature flags.

## Derive a context

Rebind `ctx` in a `{{ }}` block with `=`, or with `:=` alongside at least one new name:

```gsx
component Traced() {
	{{ ctx, span := tracer.Start(ctx, "traced"); defer span.End() }}
	{{ ctx, cancel := context.WithCancel(ctx); defer cancel() }}
	<Report/>
}
```

The derived `ctx` applies to everything rendered after it in the component, including child components, up to the end of the enclosing `if`/`for`/`switch` body or component children; plain elements don't scope it. A `defer` runs when the component finishes rendering. Declaring a new `ctx` (`var ctx`, or `ctx := x` with no other new name) is rejected.

## Prefer parameters for application data

Use explicit, typed [component parameters](./props.md) for data that directly determines what a component renders; the declaration and call site then show the dependency. Reserve context for values that are genuinely ambient across a request.
