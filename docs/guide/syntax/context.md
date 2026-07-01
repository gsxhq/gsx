# Context

Go's `context.Context` carries request-scoped values — authenticated user, locale, request ID, tracing span — across the call stack. gsx components sit inside that call stack, so `ctx` is there when you need it.

## Components receive `ctx context.Context`

Every `component` declaration compiles to a function whose signature closes over a `ctx context.Context` and a `w io.Writer`. When a caller invokes `Greeting()`, the runtime passes the `ctx` it received from `Render(ctx, w)` down to every component in the tree automatically. No threading by hand; no explicit parameter on every component.

This matches templ's model exactly. The value passed to `Render(ctx, w)` at the root of the tree is the same value available as `ctx` inside every nested component body.

## Reading from context

`ctx` is in scope throughout a component body. You can pass it to any ordinary Go function:

<!--@include: ./_generated/context/010-reading-context.md-->

`userName(ctx)` calls a plain Go helper defined in the same file. The helper reads a typed value from the context via `ctx.Value(ctxKey{})`, returning `"guest"` when the value is absent. The component never has to know whether the value is present — the helper encapsulates that decision.

This pattern — **typed helper wrapping `ctx.Value`** — is the idiomatic way to consume context in gsx. The unexported key type (`ctxKey struct{}`) prevents collisions with other packages. Any function that accepts `context.Context` works the same way, including middleware-set values, tracing helpers, and feature-flag clients.

Good candidates for the context:
- Authentication (`currentUser(ctx)`)
- Locale or language (`locale(ctx)`)
- Request ID or trace ID (`requestID(ctx)`)
- Feature flags (`featureEnabled(ctx, "new-ui")`)

## gsx's preference: explicit, typed props

Context is a blunt instrument. When a helper function passes a value through five layers of ctx.Value, a component in the middle has no way to see what flows through it — and a reader has to chase the key type to understand the data. gsx can't type-check that path at codegen time.

For the data a component **specifically needs**, gsx steers you toward explicit, typed props. A component that receives `user User` as a prop is self-documenting: the call site shows exactly what is being passed, the type is checked by the compiler, and the component is fully testable in isolation with no ambient state.

This preference is a design lean, not a prohibition. See
[Why gsx](../vision#checked-by-go) for the props model behind it.

Use context for **cross-cutting, request-scoped values** that are legitimately global to a request — auth, locale, tracing — and explicit props for the data that defines what a component renders. When in doubt, start with props: you can always add a context read inside a helper if the value really is ambient.
