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
		Class("btn"),             // dup, dropped
	)
	if got != "btn px-4 active" {
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
	if got != ` class="btn px-4 w-full"` {
		t.Fatalf("merged+deduped: got %q", got)
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
