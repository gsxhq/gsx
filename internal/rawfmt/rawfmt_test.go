package rawfmt

import (
	"strings"
	"testing"
)

func TestBuildPlaceholderedInterleaves(t *testing.T) {
	segs := []string{".a{color:", ";width:", "}"}
	holes := []string{"@{ fg }", "@{ w }"}
	text, prefix := buildPlaceholdered(segs, holes)
	want := ".a{color:" + sentinel(prefix, 0) + ";width:" + sentinel(prefix, 1) + "}"
	if text != want {
		t.Fatalf("placeholdered = %q, want %q", text, want)
	}
	if strings.Contains(strings.Join(segs, ""), prefix) {
		t.Fatalf("prefix %q collides with segment text", prefix)
	}
}

func TestBuildPlaceholderedAvoidsCollision(t *testing.T) {
	// Source already contains the default prefix → the chosen prefix must differ
	// and be absent from the source.
	segs := []string{"a __gsxhole_ b ", ""}
	holes := []string{"@{ x }"}
	text, prefix := buildPlaceholdered(segs, holes)
	if strings.Count(text, sentinel(prefix, 0)) != 1 {
		t.Fatalf("sentinel not uniquely present in %q", text)
	}
	// The collision-extended prefix must not appear in the literal segments.
	if strings.Contains(segs[0], prefix) {
		t.Fatalf("extended prefix %q still collides", prefix)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	segs := []string{".a{color:", ";width:", "}"}
	holes := []string{"@{ fg }", "@{ w }"}
	text, prefix := buildPlaceholdered(segs, holes)
	// Simulate a formatter that reflows but preserves sentinels.
	formatted := strings.ReplaceAll(text, "{", " {\n  ")
	got, ok := restore(formatted, prefix, holes)
	if !ok {
		t.Fatal("restore reported failure on a faithful formatter")
	}
	for _, h := range holes {
		if !strings.Contains(got, h) {
			t.Fatalf("restored output missing hole %q:\n%s", h, got)
		}
	}
	if strings.Contains(got, prefix) {
		t.Fatalf("sentinel leaked into restored output:\n%s", got)
	}
}

func TestRestoreRejectsDroppedSentinel(t *testing.T) {
	holes := []string{"@{ a }", "@{ b }"}
	_, prefix := buildPlaceholdered([]string{"x", "y", "z"}, holes)
	// Formatter dropped sentinel 1 entirely.
	formatted := "x" + sentinel(prefix, 0) + "yz"
	if _, ok := restore(formatted, prefix, holes); ok {
		t.Fatal("restore accepted a dropped sentinel")
	}
}

func TestRestoreRejectsDuplicatedSentinel(t *testing.T) {
	holes := []string{"@{ a }"}
	_, prefix := buildPlaceholdered([]string{"x", "y"}, holes)
	formatted := "x" + sentinel(prefix, 0) + "y" + sentinel(prefix, 0)
	if _, ok := restore(formatted, prefix, holes); ok {
		t.Fatal("restore accepted a duplicated sentinel")
	}
}
