# Streaming flush

Flush the response partway through a page so the browser can paint the
above-the-fold markup before the slow tail finishes rendering — useful for
streamed SSR, Server-Sent Events, or [declarative partial
updates](https://developer.chrome.com/blog/declarative-partial-updates).

## Copy the helper

Copy this into the package that renders your pages:

```go
package app

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gsxhq/gsx"
)

// Flush streams whatever has been written so far to the client, invokable as a
// bare <Flush/> tag. It writes no HTML of its own.
func Flush() gsx.Node {
	return gsx.Func(func(ctx context.Context, w io.Writer) error {
		rw, ok := w.(http.ResponseWriter)
		if !ok {
			return nil
		}
		if err := http.NewResponseController(rw).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	})
}
```

`Flush` is a custom node: a plain Go func returning `gsx.Node`. Its `gsx.Func`
receives the `io.Writer` the page is rendering into. gsx's writer is unbuffered,
so the flush boundary is exactly the bytes emitted up to the tag.

`http.NewResponseController(rw).Flush()` walks the writer's `Unwrap()` chain and
prefers a `FlushError() error` method — what a buffering middleware exposes —
over the legacy `http.Flusher`, surfacing real flush errors into the render. A
writer that simply can't flush returns `ErrNotSupported`, which is a no-op.

## Use it

Drop `<Flush/>` at the point where the buffered markup should be sent:

```gsx
component Page() {
	<head>...</head>
	<body>
		<Header/>
		<Flush/>
		<SlowFeed/>
	</body>
}
```

The header streams immediately; the feed streams as it renders.

## What happens without a flushable writer

If the page is not rendering into an `http.ResponseWriter` — a `bytes.Buffer` in
a test, say — `<Flush/>` is a no-op and writes nothing. The page renders
identically, just without incremental delivery.
