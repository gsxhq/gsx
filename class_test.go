package gsx

import (
	"strings"
	"testing"
)

func renderClass(parts ...ClassPart) string {
	var b strings.Builder
	W(&b).Class(parts...)
	return b.String()
}

func TestClassComposeDedupeOrder(t *testing.T) {
	got := renderClass(
		Class("btn px-4"), // whitespace-split into two tokens
		ClassIf("active", true),
		ClassIf("hidden", false), // excluded
		Class("btn"),             // dup — last occurrence wins, moves btn to end
	)
	// last-wins: "btn" at positions 0 and 3; last wins → "px-4 active btn"
	if got != "px-4 active btn" {
		t.Fatalf("got %q", got)
	}
}

func TestClassEscapesValue(t *testing.T) {
	if got := renderClass(Class(`a"b`)); got != `a&#34;b` {
		t.Fatalf("got %q", got)
	}
}

func TestStyleJoins(t *testing.T) {
	var b strings.Builder
	W(&b).Style(Class("color: red"), ClassIf("display: none", false), Class("margin: 0"))
	if b.String() != "color: red; margin: 0" {
		t.Fatalf("got %q", b.String())
	}
}

func renderClassMerged(extra string, parts ...ClassPart) string {
	var b strings.Builder
	W(&b).ClassMerged(extra, parts...)
	return b.String()
}

func TestClassMergedEmptyWritesNothing(t *testing.T) {
	if got := renderClassMerged(""); got != "" {
		t.Fatalf("empty: got %q, want empty", got)
	}
}

func TestClassMergedPartsOnly(t *testing.T) {
	if got := renderClassMerged("", Class("btn"), Class("px-4")); got != ` class="btn px-4"` {
		t.Fatalf("parts only: got %q", got)
	}
}

func TestClassMergedExtraOnly(t *testing.T) {
	if got := renderClassMerged("w-full"); got != ` class="w-full"` {
		t.Fatalf("extra only: got %q", got)
	}
}

func TestClassMergedPartsAndExtraDeduped(t *testing.T) {
	got := renderClassMerged("btn w-full", Class("btn"), Class("px-4"))
	// tokens: ["btn"(0), "px-4"(1), "btn"(2), "w-full"(3)]
	// last-wins: "btn" last at 2 → "px-4 btn w-full" (caller/extra wins over component)
	if got != ` class="px-4 btn w-full"` {
		t.Fatalf("merged+deduped: got %q", got)
	}
}

func TestDefaultClassMergeLastWins(t *testing.T) {
	for _, tt := range []struct{ in []string; want string }{
		{[]string{"a", "b", "a"}, "b a"},
		{[]string{"a", "b"}, "a b"},
		{[]string{"x", "x", "x"}, "x"},
		{[]string{}, ""},
		{[]string{"p-2", "p-4", "p-2"}, "p-4 p-2"},
	} {
		if got := defaultClassMerge(tt.in); got != tt.want {
			t.Errorf("defaultClassMerge(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClassMergerOverride(t *testing.T) {
	orig := ClassMerger
	t.Cleanup(func() { ClassMerger = orig })
	ClassMerger = func(tokens []string) string { return "MERGED:" + strings.Join(tokens, ",") }
	if got := renderClass(Class("a b")); got != "MERGED:a,b" {
		t.Fatalf("got %q", got)
	}
}
