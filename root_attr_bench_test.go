package gsx

import (
	"context"
	"io"
	"testing"
)

// These cover the per-component-root attribute machinery on an empty bag — what
// every component root pays per render when the caller passes no attributes (the
// common case). The work should be near-free.

func BenchmarkStyleMergedEmpty(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.StyleMerged("", "")
	}
}

func BenchmarkWithoutEmpty(b *testing.B) {
	b.ReportAllocs()
	var a Attrs
	for b.Loop() {
		_ = a.Without("class", "style")
	}
}

func BenchmarkRootAttrMachineryEmpty(b *testing.B) {
	b.ReportAllocs()
	var a Attrs
	gw := W(io.Discard)
	ctx := context.Background()
	for b.Loop() {
		gw.StyleMerged("", a.Style())
		gw.SpreadForwarding(ctx, a, nil, nil, nil, []string{"class", "style"})
	}
}

// BenchmarkForwardingLeafNoURL measures the full leaf-forwarding shape codegen
// now emits per forwarding element — the URL-name GetFold extraction scan plus
// the case-insensitive WithoutFold residual — on a 4-entry bag that carries NO
// URL-classified key (the common case). This is the render-time cost of
// leaf-side URL sanitization: it runs the extraction machinery unconditionally,
// so the bag is scanned once per built-in URL name and the residual is rebuilt.
// It exists to keep that cost visible and regression-gated; the design accepts
// it as the price of no-silent-hole URL safety on forwarded bags.
func BenchmarkForwardingLeafNoURL(b *testing.B) {
	b.ReportAllocs()
	a := Attrs{
		{Key: "id", Value: "x"},
		{Key: "class", Value: "c"},
		{Key: "data-n", Value: "1"},
		{Key: "role", Value: "button"},
	}
	urlNames := []string{
		"action", "background", "cite", "data", "formaction", "href",
		"hx-delete", "hx-get", "hx-patch", "hx-post", "hx-put",
		"manifest", "ping", "poster", "src", "xlink:href",
	}
	gw := W(io.Discard)
	ctx := context.Background()
	for b.Loop() {
		for _, n := range urlNames {
			_, _ = a.GetFold(n)
		}
		gw.ClassMerged(DefaultClassMerge, a.Class())
		gw.StyleMerged("", a.Style())
		gw.SpreadForwarding(ctx, a, nil, nil, nil, []string{"class", "style"})
	}
}

// Non-empty paths, to keep the real work honest.
func BenchmarkStyleMergedDedup(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.StyleMerged("color: red; margin: 0", "color: blue")
	}
}

func BenchmarkWithoutAttrs(b *testing.B) {
	b.ReportAllocs()
	a := Attrs{{Key: "id", Value: "x"}, {Key: "class", Value: "c"}, {Key: "data-n", Value: "1"}}
	for b.Loop() {
		_ = a.Without("class", "style")
	}
}
