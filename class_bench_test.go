package gsx

import (
	"io"
	"testing"
)

// BenchmarkClassLoneToken is the hottest case: a component root with one static
// single-token class and no fallthrough. It must not allocate or call the merger.
func BenchmarkClassLoneToken(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.Class(DefaultClassMerge, Class("card"), Class(""))
	}
}

// BenchmarkClassSingleMultiToken is the realistic-page case: one static
// multi-token utility class, no fallthrough. A single source is verbatim, so the
// default merge does no tokenize/dedup/join.
func BenchmarkClassSingleMultiToken(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.Class(DefaultClassMerge, Class("rounded border bg-white p-4 shadow-sm"), Class(""))
	}
}

// BenchmarkClassMergeFallthrough is the genuine cross-source merge: a static
// class plus a caller fallthrough class that overlaps.
func BenchmarkClassMergeFallthrough(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.Class(DefaultClassMerge, Class("btn px-4"), Class("btn w-full"))
	}
}

// BenchmarkClassMergedRoot is the component-root form (static class + empty
// fallthrough) via ClassMerged.
func BenchmarkClassMergedRoot(b *testing.B) {
	b.ReportAllocs()
	gw := W(io.Discard)
	for b.Loop() {
		gw.ClassMerged(DefaultClassMerge, "", Class("rounded border bg-white p-4 shadow-sm"))
	}
}
