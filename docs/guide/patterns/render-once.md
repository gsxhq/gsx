# Render once

Some markup must appear **exactly once per page**, no matter how many times the
component that owns it is invoked. A modal dialog needs a single container element;
a widget that ships an inline `<style>` block should emit it once, not once per
instance; a dev-mode asset preamble belongs at the top of the page a single time.

gsx has no built-in render-once primitive. This page shows the recommended userland
pattern: a small handle type plus an `<Once>` component that renders its children
only the first time a given handle is seen in a request.

::: tip Ported from templ
This is a faithful port of templ's [`OnceHandle`](https://github.com/a-h/templ/blob/main/once.go)
(`templ.NewOnceHandle`). The one difference is setup — see [Why the middleware is
required](#why-the-middleware-is-required) below.
:::

## The recipe

### 1. The handle and per-request state

The handle is an opaque token you declare once per singleton. Rendering is keyed by
the handle's pointer identity, and a per-request set records which handles have
already rendered.

```go
package app

import (
	"context"
	"sync/atomic"
)

// OnceHandle dedups a render within a single request: the first render of a
// handle renders; every later render of the same handle is skipped.
type OnceHandle struct{ id int64 }

var onceIndex atomic.Int64

// NewOnce creates an OnceHandle. Declare one at package level per singleton.
func NewOnce() *OnceHandle { return &OnceHandle{id: onceIndex.Add(1)} }

// onceState holds the set of handles already rendered this request. Rendering is
// single-goroutine per request, so the map needs no mutex.
type onceState struct{ seen map[*OnceHandle]struct{} }

type onceKeyT struct{}

// firstRender reports whether h is rendering for the first time this request,
// marking it seen. With no scope installed it degrades to "always render" — dedup
// is lost but nothing crashes.
func (h *OnceHandle) firstRender(ctx context.Context) bool {
	st, _ := ctx.Value(onceKeyT{}).(*onceState)
	if st == nil {
		return true
	}
	if _, done := st.seen[h]; done {
		return false
	}
	st.seen[h] = struct{}{}
	return true
}
```

::: warning The `id` field is load-bearing
The seen-set is keyed by `*OnceHandle`, and Go may place two **zero-size** structs
at the same address — which would make distinct handles collide. The non-empty `id`
field guarantees distinct handles get distinct addresses. Don't remove it.
:::

### 2. Install the per-request scope with middleware

The seen-set lives in the request `context`. Install it once per request with a
standard `net/http` middleware, before any page renders (same package as above; add
`net/http` to the imports):

```go
// withOnceScope installs the per-request dedup state; idempotent.
func withOnceScope(ctx context.Context) context.Context {
	if ctx.Value(onceKeyT{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, onceKeyT{}, &onceState{seen: map[*OnceHandle]struct{}{}})
}

// OnceScopeMiddleware installs the scope that <Once> relies on. Wrap your handler
// (or router) with it once.
func OnceScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(withOnceScope(r.Context())))
	})
}
```

Wire it in wherever your app installs middleware:

```go
handler := OnceScopeMiddleware(mux)
```

### 3. The `<Once>` component

A tiny gsx component guards its children behind `firstRender`:

```gsx
component Once(handle *OnceHandle) {
	{ if handle.firstRender(ctx) { { children } } }
}
```

`firstRender` both reports and records first-render, so the guard renders children
on the first pass and skips every later one.

## Using it

Declare a handle at package level, then invoke your singleton through `<Once>`.
Duplicate invocations anywhere on the page collapse to a single render:

```gsx
var dialogContainerOnce = NewOnce()

component DialogContainer() {
	<Once handle={dialogContainerOnce}>
		<div id="dialog-root"></div>
	</Once>
}
```

Now `<DialogContainer/>` can appear next to every dialog trigger in your tree, and
`#dialog-root` is emitted exactly once.

## Why the middleware is required

templ's generated `Render` calls `InitializeContext` at the root of every render,
which is where templ installs its own once-state — so `templ.Once` works with no
setup. gsx generates no such call: a gsx component's `Render` does nothing but write
your markup. That keeps generated code minimal and predictable, but it means the
per-request scope is **your** responsibility. `OnceScopeMiddleware` is what gives you
back what templ installed for free.

If you forget the middleware, `firstRender` finds no scope and returns `true` every
time — so the singleton renders on every invocation instead of once. It never panics;
the failure mode is duplicate output, not a crash.

## Scope note

The dedup is **per request**, keyed by handle pointer. A package-level handle is
shared across all requests, but the seen-set is fresh per request (installed by the
middleware), so one request's renders never affect another's.
