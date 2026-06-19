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
