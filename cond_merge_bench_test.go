package gsx

import (
	"context"
	"io"
	"testing"
)

// BenchmarkCondMergeFolded and BenchmarkCondMergeComposable measure the
// per-render cost of a nav-tab-style element (root "tab" class + a spread + a
// runtime active/inactive class) under gsx's two codegen paths for that shape
// — the delta is what the D3 lift (Form-2 conditional class now folding on a
// forwarding element) costs per render relative to the pre-existing
// composable-form path, which does not fold. Both bodies are transcribed
// verbatim from GenerateDirs' actual output for their respective source
// (confirmed directly against codegen before writing this file; not
// re-derived/approximated here), minus the literal `gw.S(...)` string writes
// that both paths share and neither benchmark is trying to isolate.
//
//   - Folded: `<a class="tab" { attrs... } { if active { class="tab-active" }
//     else { class="tab-inactive" } }>` — a Form-2 conditional class on a
//     forwarding element. ConcatAttrs the root class with the spread,
//     AttrsCond the branch into its own Attrs, ConcatAttrs those two, then
//     ClassMerged/StyleMerged/Spread over the folded bag.
//   - Composable: `<a class={ "tab", "tab-active": active } { attrs... }>` —
//     the same shape expressed via the composable form: one gw.Class call
//     over ClassPart values, and the caller's Attrs used directly in Spread —
//     no ConcatAttrs/AttrsCond at all.
func BenchmarkCondMergeFolded(b *testing.B) {
	b.ReportAllocs()
	attrs := Attrs{{Key: "id", Value: "tab1"}, {Key: "data-n", Value: "1"}}
	navNames := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	imageNames := []string{"background"}
	srcsetNames := []string{"imagesrcset", "srcset"}
	gw := W(io.Discard)
	ctx := context.Background()
	active := true
	for b.Loop() {
		v0 := ConcatAttrs(Attrs{{Key: "class", Value: "tab"}}, attrs)
		v1, err := AttrsCond(active, func() (Attrs, error) {
			return Attrs{{Key: "class", Value: "tab-active"}}, nil
		}, func() (Attrs, error) {
			return Attrs{{Key: "class", Value: "tab-inactive"}}, nil
		})
		if err != nil {
			b.Fatal(err)
		}
		v2 := ConcatAttrs(v0, v1)
		gw.ClassMerged(DefaultClassMerge, v2.Class())
		gw.StyleMerged("", v2.Style())
		gw.Spread(ctx, v2, navNames, imageNames, srcsetNames, nil, []string{"class", "style"})
	}
}

func BenchmarkCondMergeComposable(b *testing.B) {
	b.ReportAllocs()
	attrs := Attrs{{Key: "id", Value: "tab1"}, {Key: "data-n", Value: "1"}}
	navNames := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	imageNames := []string{"background"}
	srcsetNames := []string{"imagesrcset", "srcset"}
	gw := W(io.Discard)
	ctx := context.Background()
	active := true
	for b.Loop() {
		gw.Class(DefaultClassMerge, Class("tab"), ClassIf("tab-active", active), Class(attrs.Class()))
		gw.StyleMerged("", attrs.Style())
		gw.Spread(ctx, attrs, navNames, imageNames, srcsetNames, nil, []string{"class", "style"})
	}
}

// BenchmarkNonForwardingSingleStyleInline pins the unchanged fast path for an
// element with one style contributor and no spread. The body is the generated
// attribute write from `<div style="color:red">`; it intentionally avoids the
// Attrs folding machinery.
func BenchmarkNonForwardingSingleStyleInline(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.S(" style=\"color:red\"")
	}
}

// BenchmarkNonForwardingConditionalStyleMerged measures the new no-spread
// fold for `<div style="color:red" { if active { style="margin:0" } }>`. The
// body is transcribed from the generated output: both same-name contributors
// are assembled into one bag, then emitted through the normal merged leaf.
// Compare it with BenchmarkNonForwardingSingleStyleInline to see the cost of
// composition; neither benchmark imposes a machine-specific threshold.
func BenchmarkNonForwardingConditionalStyleMerged(b *testing.B) {
	b.ReportAllocs()
	navNames := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	imageNames := []string{"background"}
	srcsetNames := []string{"imagesrcset", "srcset"}
	gw := W(io.Discard)
	ctx := context.Background()
	active := true
	for b.Loop() {
		v0 := Attrs{{Key: "style", Value: "color:red"}}
		v1, err := AttrsCond(active, func() (Attrs, error) {
			return Attrs{{Key: "style", Value: "margin:0"}}, nil
		}, nil)
		if err != nil {
			b.Fatal(err)
		}
		v2 := ConcatAttrs(v0, v1)
		gw.ClassMerged(DefaultClassMerge, v2.Class())
		gw.StyleMerged("", v2.Style())
		gw.Spread(ctx, v2, navNames, imageNames, srcsetNames, nil, []string{"class", "style"})
	}
}
