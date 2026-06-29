package gsx

import (
	"strings"
	"testing"
)

func renderClass(parts ...ClassPart) string {
	var b strings.Builder
	W(&b).Class(DefaultClassMerge, parts...)
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
	W(&b).ClassMerged(DefaultClassMerge, extra, parts...)
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
	if got := DefaultClassMerge([]string{"a", "b", "a"}); got != "b a" {
		t.Fatalf("got %q, want %q", got, "b a")
	}
}

func TestClassStringUsesPassedMerger(t *testing.T) {
	merge := func(tokens []string) string { return "M:" + strings.Join(tokens, ",") }
	if got := ClassString(merge, Class("a b")); got != "M:a,b" {
		t.Fatalf("got %q", got)
	}
}

func TestAttrsClassRawNoMerge(t *testing.T) {
	a := Attrs{"class": "x  y x"}
	if got := a.Class(); got != "x  y x" {
		t.Fatalf("Attrs.Class() = %q, want raw %q", got, "x  y x")
	}
}

func TestClassString(t *testing.T) {
	if got := ClassString(DefaultClassMerge, Class("a b"), ClassIf("c", false), Class("d")); got != "a b d" {
		t.Errorf("ClassString = %q, want \"a b d\"", got)
	}
}

func TestStyleString(t *testing.T) {
	// gw.Style includes a part only when its .on is true; joins decls with "; ".
	if got := StyleString(Class("color: red"), ClassIf("margin: 0", false), Class("padding: 1px")); got != "color: red; padding: 1px" {
		t.Errorf("StyleString = %q, want \"color: red; padding: 1px\"", got)
	}
}
