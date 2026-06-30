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
	// Multiple sources are split and deduped last-wins.
	if got := DefaultClassMerge([]string{"a", "b", "a"}); got != "b a" {
		t.Fatalf("got %q, want %q", got, "b a")
	}
	// A single source is returned verbatim (nothing to merge across).
	if got := DefaultClassMerge([]string{"rounded border p-4"}); got != "rounded border p-4" {
		t.Fatalf("single source: got %q, want verbatim", got)
	}
	// Cross-source dedup still applies when sources are multi-token strings.
	if got := DefaultClassMerge([]string{"btn px-4", "btn w-full"}); got != "px-4 btn w-full" {
		t.Fatalf("multi-token sources: got %q", got)
	}
	if got := DefaultClassMerge(nil); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestClassStringUsesPassedMerger(t *testing.T) {
	// The merger receives the raw, un-split on-class strings in source order
	// (off parts excluded); it is NOT pre-tokenized by the runtime.
	merge := func(classes []string) string { return "M:" + strings.Join(classes, "|") }
	if got := ClassString(merge, Class("a b"), ClassIf("c", false), Class("d")); got != "M:a b|d" {
		t.Fatalf("got %q, want %q", got, "M:a b|d")
	}
}

func TestClassLoneTokenSkipsMerger(t *testing.T) {
	// A single lone token cannot conflict, so the merger must not be invoked.
	merge := func(classes []string) string { t.Fatalf("merger called for lone token: %q", classes); return "" }
	if got := ClassString(merge, Class("card"), ClassIf("hidden", false)); got != "card" {
		t.Fatalf("got %q, want %q", got, "card")
	}
	var b strings.Builder
	W(&b).Class(merge, Class("card"))
	if b.String() != "card" {
		t.Fatalf("Class lone token: got %q", b.String())
	}
}

func TestClassMergerReceivesRawForRealMerge(t *testing.T) {
	// A multi-token single source is NOT lone, so a custom merger still sees it
	// (e.g. a Tailwind merger must resolve px-4 vs px-8 within one string).
	seen := false
	merge := func(classes []string) string {
		seen = true
		if len(classes) != 1 || classes[0] != "px-4 px-8" {
			t.Fatalf("merger got %q, want [\"px-4 px-8\"]", classes)
		}
		return "px-8"
	}
	if got := ClassString(merge, Class("px-4 px-8")); got != "px-8" || !seen {
		t.Fatalf("got %q seen=%v", got, seen)
	}
}

func TestAttrsClassRawNoMerge(t *testing.T) {
	a := Attrs{{Key: "class", Value: "x  y x"}}
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

func TestClassJoin(t *testing.T) {
	// Flattens on, non-empty parts and applies the built-in last-wins dedup (NOT
	// the configured merger). Off parts and empties are skipped.
	// tokens ["a","b","a","d"] -> last-wins -> "b a d".
	got := ClassJoin(Class("a b"), ClassIf("c", false), Class("a"), Class(""), ClassIf("d", true))
	if got != "b a d" {
		t.Fatalf("ClassJoin = %q, want %q", got, "b a d")
	}
	// A conflict-free single source is preserved verbatim.
	if got := ClassJoin(Class("px-4 py-2 bg-blue-500")); got != "px-4 py-2 bg-blue-500" {
		t.Fatalf("single source: got %q", got)
	}
	if got := ClassJoin(); got != "" {
		t.Fatalf("empty ClassJoin = %q, want \"\"", got)
	}
	if got := ClassJoin(ClassIf("x", false)); got != "" {
		t.Fatalf("all-off ClassJoin = %q, want \"\"", got)
	}
}
