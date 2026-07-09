package printer

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
)

// TestFmtGoChunkPreservesBuildComment guards the corruption bug fixed by
// wrapping GoChunk text in a synthetic package clause before formatting:
// go/format.Source's fragment mode strips a fixed byte count off its output,
// which shears a //go:build comment that go/printer hoists above the clause
// it injects internally — turning "//go:build linux" into "linux" and
// leaking "package p" into the chunk. fmtGoChunk must instead feed
// format.Source a complete, valid file (goExprWrapper + src) and remove the
// synthetic clause by parsing, so the build comment survives untouched and no
// package declaration leaks into the printed chunk.
func TestFmtGoChunkPreservesBuildComment(t *testing.T) {
	src := "//go:build linux\n\nimport (\n\t\"fmt\"\n)\n\nvar _ = fmt.Sprint"
	got := fmtGoChunk(src, 80, pretty.DefaultTabWidth)

	if !strings.Contains(got, "//go:build linux") {
		t.Fatalf("build comment not preserved verbatim; got:\n%s", got)
	}
	if strings.Contains(got, "package ") {
		t.Fatalf("synthetic package clause leaked into output; got:\n%s", got)
	}
	if strings.Contains(got, "linux\n") && !strings.Contains(got, "//go:build linux\n") {
		t.Fatalf("build comment corrupted (comment marker sheared off); got:\n%s", got)
	}
}

// TestStripSyntheticPackage covers the three shapes StripSyntheticPackage must
// handle: a clause on line 1, a clause relocated below a hoisted //go:build
// comment, and unparseable input.
func TestStripSyntheticPackage(t *testing.T) {
	t.Run("clause on line 1", func(t *testing.T) {
		formatted := []byte("package _gsxfmt\n\nimport (\n\t\"fmt\"\n)\n")
		got, ok := StripSyntheticPackage(formatted)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		want := "import (\n\t\"fmt\"\n)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("clause below hoisted build comment", func(t *testing.T) {
		// This is the shape go/printer actually produces when a //go:build
		// comment sits above the synthetic clause: the comment stays first,
		// separated from the clause by a blank line.
		formatted := []byte("//go:build linux\n\npackage _gsxfmt\n\nimport (\n\t\"fmt\"\n)\n")
		got, ok := StripSyntheticPackage(formatted)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		want := "//go:build linux\n\nimport (\n\t\"fmt\"\n)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("unparseable input", func(t *testing.T) {
		_, ok := StripSyntheticPackage([]byte("this is not { valid Go"))
		if ok {
			t.Fatalf("expected ok=false for unparseable input")
		}
	})
}
