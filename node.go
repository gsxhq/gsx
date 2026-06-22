// Package gsx is the runtime that gsx-generated code calls to stream HTML to an
// io.Writer. It is dependency-free (standard library only).
package gsx

import (
	"context"
	"io"
)

// Node is gsx's own rendering interface. Its method set is identical to
// templ.Component, so a gsx.Node satisfies templ.Component structurally — no
// templ import is needed for ecosystem interop.
type Node interface {
	Render(ctx context.Context, w io.Writer) error
}

// Func adapts a plain render function to a Node (cf. templ.ComponentFunc).
type Func func(ctx context.Context, w io.Writer) error

// Render implements Node.
func (f Func) Render(ctx context.Context, w io.Writer) error { return f(ctx, w) }

// Raw wraps trusted, already-safe HTML — the opt-out from auto-escaping. The
// string is written verbatim.
func Raw(html string) Node {
	return Func(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, html)
		return err
	})
}

// RawURL marks a URL the template author vouches for — the opt-out from gsx's URL
// scheme allow-list. A RawURL value in a URL attribute (href, src, …) skips the
// scheme check, so a non-http(s)/mailto/tel scheme is NOT replaced with the
// blocked-URL sentinel. It is still entity-escaped for the attribute context, so
// it cannot break out of the quotes — "raw" means "skip gsx's safety judgement
// about the scheme", not "write byte-for-byte". Use only for URLs you control or
// have already validated.
type RawURL string
