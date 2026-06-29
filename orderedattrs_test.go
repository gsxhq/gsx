package gsx

import (
	"bytes"
	"context"
	"testing"
)

func renderSpreadOrdered(a OrderedAttrs) string {
	var b bytes.Buffer
	gw := W(&b)
	gw.SpreadOrdered(context.Background(), a)
	if err := gw.Err(); err != nil {
		panic(err)
	}
	return b.String()
}

func TestSpreadOrderedPreservesOrderNoSort(t *testing.T) {
	// Keys deliberately NOT alphabetical: a sorted bag would reorder them.
	got := renderSpreadOrdered(OrderedAttrs{
		{Key: "data-signals", Value: "{count:0}"},
		{Key: "data-text", Value: "$count"},
		{Key: "data-a", Value: "z"},
	})
	want := ` data-signals="{count:0}" data-text="$count" data-a="z"`
	if got != want {
		t.Fatalf("order not preserved\n got: %q\nwant: %q", got, want)
	}
}

func TestSpreadOrderedBoolAndEscapingAndUnsafe(t *testing.T) {
	got := renderSpreadOrdered(OrderedAttrs{
		{Key: "data-show", Value: true},  // bare when true
		{Key: "data-hide", Value: false}, // omitted when false
		{Key: "title", Value: `a"b`},     // attribute-escaped
		{Key: "bad name", Value: "x"},    // unsafe name -> dropped
	})
	want := ` data-show title="a&#34;b"`
	if got != want {
		t.Fatalf("bool/escape/unsafe wrong\n got: %q\nwant: %q", got, want)
	}
}

func TestSpreadOrderedEmptyNoop(t *testing.T) {
	if got := renderSpreadOrdered(OrderedAttrs{}); got != "" {
		t.Fatalf("empty bag should write nothing, got %q", got)
	}
	if got := renderSpreadOrdered(nil); got != "" {
		t.Fatalf("nil bag should write nothing, got %q", got)
	}
}

func TestSpreadOrderedDuplicateKeysTolerated(t *testing.T) {
	got := renderSpreadOrdered(OrderedAttrs{
		{Key: "data-x", Value: "1"},
		{Key: "data-x", Value: "2"},
	})
	want := ` data-x="1" data-x="2"` // emitted in order; browser applies first-wins
	if got != want {
		t.Fatalf("dup keys\n got: %q\nwant: %q", got, want)
	}
}
