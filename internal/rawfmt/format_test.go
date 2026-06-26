// internal/rawfmt/format_test.go
package rawfmt

import (
	"errors"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
)

// render prints the body Doc at depth 1 (as if nested one level under a tag),
// so the re-indent's tab handling is exercised.
func render(doc pretty.Doc) string {
	return pretty.Print(pretty.Concat(pretty.Text("<style>"), pretty.Indent(doc), pretty.Text("</style>")), 80)
}

func TestFormatHappyPath(t *testing.T) {
	// Formatter splits "{" onto its own indented block and restores holes.
	f := func(src []byte) ([]byte, error) {
		s := strings.ReplaceAll(string(src), "{", " {\n")
		s = strings.ReplaceAll(s, "}", "\n}")
		return []byte(s), nil
	}
	doc, ok := Format([]string{".a{color:", "}"}, []string{"@{ fg }"}, f)
	if !ok {
		t.Fatal("Format reported failure on a faithful formatter")
	}
	out := render(doc)
	if !strings.Contains(out, "@{ fg }") {
		t.Fatalf("hole not restored:\n%s", out)
	}
	if strings.Contains(out, "__gsxhole") {
		t.Fatalf("sentinel leaked:\n%s", out)
	}
}

func TestFormatFallsBackOnError(t *testing.T) {
	f := func(src []byte) ([]byte, error) { return nil, errors.New("bad css") }
	if _, ok := Format([]string{"x"}, nil, f); ok {
		t.Fatal("Format should report ok=false on Formatter error")
	}
}

func TestFormatFallsBackOnPanic(t *testing.T) {
	f := func(src []byte) ([]byte, error) { panic("boom") }
	if _, ok := Format([]string{"x"}, nil, f); ok {
		t.Fatal("Format should recover a panic and report ok=false")
	}
}

func TestFormatFallsBackOnDroppedHole(t *testing.T) {
	// Formatter discards the sentinel → restore mismatch → fallback.
	f := func(src []byte) ([]byte, error) { return []byte("nothing here"), nil }
	if _, ok := Format([]string{"a", "b"}, []string{"@{ x }"}, f); ok {
		t.Fatal("Format should report ok=false when a hole is dropped")
	}
}

func TestFormatRejectsBadArity(t *testing.T) {
	f := func(src []byte) ([]byte, error) { return src, nil }
	// len(segments) must equal len(holes)+1.
	if _, ok := Format([]string{"a"}, []string{"@{ x }"}, f); ok {
		t.Fatal("Format should reject mismatched segment/hole arity")
	}
}

func TestFormatEmptyBodyStaysInline(t *testing.T) {
	// An empty or whitespace-only body must produce no blank line — it renders
	// as nothing between the tags. (render wraps with <style>…</style>.)
	identity := func(src []byte) ([]byte, error) { return src, nil }
	for _, segs := range [][]string{{""}, {"   "}, {"\n  \n"}} {
		doc, ok := Format(segs, nil, identity)
		if !ok {
			t.Fatalf("unexpected fallback for %q", segs)
		}
		out := render(doc) // render is the existing test helper: <style> + Indent(doc) + </style>
		if out != "<style></style>" {
			t.Fatalf("empty body not inline for %q: got %q, want \"<style></style>\"", segs, out)
		}
	}
}

func TestFormatBlankLinesHaveNoTrailingTabs(t *testing.T) {
	// A formatter that emits a blank line between rules; re-indent must not
	// leave tab-only lines (that would break idempotence).
	f := func(src []byte) ([]byte, error) {
		return []byte(".a {\n  x: 1;\n}\n\n.b {\n  y: 2;\n}\n"), nil
	}
	doc, ok := Format([]string{".a{x:1}.b{y:2}"}, nil, f)
	if !ok {
		t.Fatal("unexpected fallback")
	}
	out := render(doc)
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimRight(ln, " \t") == "" && ln != "" {
			t.Fatalf("blank line has trailing whitespace %q in:\n%s", ln, out)
		}
	}
}
