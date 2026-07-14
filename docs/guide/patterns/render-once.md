# Render once

Use a request-scoped handle when a component can appear many times but its
markup should render only once per request.

<span id="the-recipe"></span>

## Copy the helper

Copy this into the package that owns the singleton component:

```go
package app

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
)

type OnceHandle struct{ id int64 }

var onceIndex atomic.Int64

func NewOnce() *OnceHandle {
	return &OnceHandle{id: onceIndex.Add(1)}
}

type onceState struct {
	mu   sync.Mutex
	seen map[*OnceHandle]struct{}
}

type onceKeyT struct{}

func (h *OnceHandle) firstRender(ctx context.Context) bool {
	state, _ := ctx.Value(onceKeyT{}).(*onceState)
	if state == nil {
		return true
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if _, seen := state.seen[h]; seen {
		return false
	}
	state.seen[h] = struct{}{}
	return true
}

func withOnceScope(ctx context.Context) context.Context {
	if state, _ := ctx.Value(onceKeyT{}).(*onceState); state != nil {
		return ctx
	}
	return context.WithValue(ctx, onceKeyT{}, &onceState{
		seen: make(map[*OnceHandle]struct{}),
	})
}

func OnceScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withOnceScope(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

Keep the non-zero-size `id` field. Go may give zero-size values the same
address, which would let distinct pointer-keyed handles collide.

<span id="scope-note"></span>

## Install the request scope

Wrap the handler or router before rendering a page:

```go
handler := OnceScopeMiddleware(mux)
```

The middleware creates a fresh seen-set for each request. It is idempotent, so
wrapping an already-scoped handler reuses the current set.

## Add the component

Guard the children with `firstRender`:

```gsx
component Once(handle *OnceHandle) {
	{ if handle.firstRender(ctx) { { children } } }
}
```

<span id="using-it"></span>

## Use it

Declare one package-level handle for each singleton:

```gsx
var dialogContainerOnce = NewOnce()

component DialogContainer() {
	<Once handle={dialogContainerOnce}>
		<div id="dialog-root"></div>
	</Once>
}
```

Every `<DialogContainer/>` call in one request shares that handle. A different
handle renders its own singleton, and the same handle renders again on the next
request.

<span id="why-the-middleware-is-required"></span>

## What happens without middleware

Without `OnceScopeMiddleware`, `firstRender` returns `true` every time. Content
renders on every call, but nothing panics.
