package rawfmt

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
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

func TestBuildPlaceholderedAvoidsCollisionInHole(t *testing.T) {
	// A hole whose rendered text contains the default sentinel prefix must not
	// collide: the chosen prefix is absent from segments AND holes.
	segs := []string{"a", "b"}
	holes := []string{"@{ __gsxhole_ }"}
	text, prefix := buildPlaceholdered(segs, holes)
	if strings.Contains(holes[0], prefix) {
		t.Fatalf("chosen prefix %q collides with hole text %q", prefix, holes[0])
	}
	got, ok := restore(text, prefix, holes)
	if !ok {
		t.Fatal("restore failed on a hole containing the default prefix")
	}
	if !strings.Contains(got, "@{ __gsxhole_ }") {
		t.Fatalf("hole not restored intact:\n%s", got)
	}
}

func TestFormatStringEscapeBeforeRestore(t *testing.T) {
	// identity formatter: returns input unchanged.
	id := func(src []byte) ([]byte, error) { return src, nil }
	// One hole between two segments; the segment text contains a `"` that the
	// escaper must backslash — but the restored hole must NOT be escaped.
	segments := []string{`say "hi" `, ` end`}
	holes := []string{`@{name}`}
	escape := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	got, ok := FormatString(segments, holes, id, escape)
	if !ok {
		t.Fatal("FormatString returned ok=false")
	}
	want := `say \"hi\" @{name} end`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "__gsxhole") {
		t.Fatalf("sentinel leaked: %q", got)
	}
}

func TestFormatStringNilEscapeMatchesRaw(t *testing.T) {
	id := func(src []byte) ([]byte, error) { return src, nil }
	got, ok := FormatString([]string{"a ", " b"}, []string{"@{x}"}, id, nil)
	if !ok || got != "a @{x} b" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestFormatStringArityMismatch(t *testing.T) {
	id := func(src []byte) ([]byte, error) { return src, nil }
	if _, ok := FormatString([]string{"a"}, []string{"@{x}"}, id, nil); ok {
		t.Fatal("expected ok=false on arity mismatch")
	}
}

func TestFormatStringLinesKeepsOpaqueInteriorAndHole(t *testing.T) {
	// segments/holes whose body has a multi-line template literal AND a hole.
	// The line formatter merges the backtick span into one logical line.
	segs := []string{"a {\nhtml: `<div>\nhi\n</div>`,\nk: ", "\n}"}
	holes := []string{"@{v}"}
	idLines := func(src []byte) ([]string, bool) {
		return mergeBacktick(strings.Split(string(src), "\n")), true
	}
	esc := func(s string) string { return s }
	lines, ok := FormatStringLines(segs, holes, idLines, esc)
	if !ok {
		t.Fatal("ok=false")
	}
	joined := strings.Join(lines, "\x00")
	if !strings.Contains(joined, "html: `<div>\nhi\n</div>`,") {
		t.Fatalf("opaque interior not kept in one logical line: %q", lines)
	}
	if !strings.Contains(joined, "@{v}") || strings.Contains(joined, "__gsxhole") {
		t.Fatalf("hole not restored / sentinel leaked: %q", lines)
	}
}

// mergeBacktick merges physical lines so a `...` span spanning lines becomes one.
func mergeBacktick(phys []string) []string {
	var out []string
	i := 0
	for i < len(phys) {
		line := phys[i]
		for strings.Count(line, "`")%2 == 1 && i+1 < len(phys) {
			i++
			line += "\n" + phys[i]
		}
		out = append(out, line)
		i++
	}
	return out
}

func TestFormatLinesDocLeavesInteriorVerbatim(t *testing.T) {
	// One hole-free body whose single logical line carries an internal newline.
	lf := func(src []byte) ([]string, bool) {
		return mergeBacktick(strings.Split(string(src), "\n")), true
	}
	doc, ok := FormatLines([]string{"x `a\nb`"}, nil, lf)
	if !ok {
		t.Fatal("ok=false")
	}
	out := pretty.Print(doc, 80, pretty.DefaultTabWidth)
	// The interior line "b" must NOT gain a leading tab (verbatim inside the
	// template); the logical-line start "x `a" is indented by the engine.
	if !strings.Contains(out, "a\nb`") {
		t.Fatalf("interior re-indented (expected verbatim `a\\nb`): %q", out)
	}
}
