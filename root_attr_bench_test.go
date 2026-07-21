package gsx

import (
	"context"
	"io"
	"strconv"
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
		gw.Spread(ctx, a, nil, nil, nil, nil, []string{"class", "style"})
	}
}

// BenchmarkForwardingLeafNoURL measures the full leaf-forwarding shape codegen
// now emits per forwarding element: ClassMerged/StyleMerged followed by a
// single Spread call carrying the built-in nav/image/srcset URL name sets,
// on a 4-entry bag that carries NO URL-classified key (the common case).
// Spread IS the extraction — one ordered walk that matches each key
// against navNames/imageNames/srcsetNames and writes plain attrs inline — there is no
// separate pre-scan or residual pass to warm up first (that unrolled
// per-name-GetFold-then-WithoutFold shape predates issue #75's
// universal-spread-sanitization pass and is no longer what codegen emits).
// It exists to keep this per-render cost visible and regression-gated.
func BenchmarkForwardingLeafNoURL(b *testing.B) {
	b.ReportAllocs()
	a := Attrs{
		{Key: "id", Value: "x"},
		{Key: "class", Value: "c"},
		{Key: "data-n", Value: "1"},
		{Key: "role", Value: "button"},
	}
	// The built-in URL name sets a generated Spread call carries
	// (attrclass's builtinURL floor), split nav vs image exactly as codegen
	// emits them — see e.g. spread-sanitize/derived_local_bag.txtar's
	// generated.x.go.golden.
	navNames := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	imageNames := []string{"background"}
	srcsetNames := []string{"imagesrcset", "srcset"}
	gw := W(io.Discard)
	ctx := context.Background()
	for b.Loop() {
		gw.ClassMerged(DefaultClassMerge, a.Class())
		gw.StyleMerged("", a.Style())
		gw.Spread(ctx, a, navNames, imageNames, srcsetNames, nil, []string{"class", "style"})
	}
}

func BenchmarkSpreadNoURLLarge(b *testing.B) {
	b.ReportAllocs()
	attrs := make(Attrs, 0, 70)
	for i := range 70 {
		attrs = append(attrs, Attr{
			Key:   "data-field-" + strconv.Itoa(i),
			Value: "value",
		})
	}
	navNames := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	imageNames := []string{"background"}
	srcsetNames := []string{"imagesrcset", "srcset"}
	gw := W(io.Discard)
	ctx := context.Background()
	for b.Loop() {
		gw.Spread(ctx, attrs, navNames, imageNames, srcsetNames, nil, nil)
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
