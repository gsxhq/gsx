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
		gw.Spread(ctx, a.Without("class", "style"))
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
