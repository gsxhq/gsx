package pretty

import "testing"

func TestTextConcat(t *testing.T) {
	got := Print(Concat(Text("a"), Text("b"), Text("c")), 80, DefaultTabWidth)
	if got != "abc" {
		t.Fatalf("got %q want %q", got, "abc")
	}
}

func TestGroupFitsStaysFlat(t *testing.T) {
	// "[a, b]" is 6 cols, fits in 80 → flat (Line renders as space).
	d := Group(Concat(Text("["), Text("a,"), Line, Text("b"), Text("]")))
	got := Print(d, 80, DefaultTabWidth)
	want := "[a, b]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGroupOverflowBreaks(t *testing.T) {
	// Width 4 forces the group to break: each Line becomes newline+indent.
	d := Group(Concat(Text("["), Indent(Concat(SoftLine, Text("aaa,"), Line, Text("bbb"))), SoftLine, Text("]")))
	got := Print(d, 4, DefaultTabWidth)
	want := "[\n\taaa,\n\tbbb\n]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNestedGroupInnerStaysFlat(t *testing.T) {
	// At width 12 the outer group's flat form "function (x, y)" (15) does not
	// fit, so it breaks; the inner group "(x, y)" (6) still fits on its own
	// indented line (tab=4 → 4+6=10 ≤ 12) and stays flat.
	inner := Group(Concat(Text("("), Text("x,"), Line, Text("y"), Text(")")))
	d := Group(Concat(Text("function "), Indent(Concat(SoftLine, inner)), SoftLine))
	got := Print(d, 12, DefaultTabWidth)
	want := "function \n\t(x, y)\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHardLineForcesEnclosingGroupBreak(t *testing.T) {
	// The group fits in 80 cols, but a HardLine inside forces it to break.
	d := Group(Concat(Text("a"), HardLine, Text("b")))
	got := Print(d, 80, DefaultTabWidth)
	want := "a\nb"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTrailingContentCountedInFit(t *testing.T) {
	// Group content "x" fits in 3, but trailing "</p>" pushes the line over →
	// group breaks. Verifies fits() looks past the group into rest commands.
	group := Group(Concat(Text("<p>"), Indent(Concat(SoftLine, Text("x"))), SoftLine, Text("</p>")))
	got := Print(group, 5, DefaultTabWidth)
	want := "<p>\n\tx\n</p>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMultibyteWidth(t *testing.T) {
	// "·····" is 5 runes (10 bytes). At width 6 it fits flat; the assertion is
	// just that it renders verbatim (measurement uses rune count, not bytes).
	got := Print(Group(Text("·····")), 6, DefaultTabWidth)
	if got != "·····" {
		t.Fatalf("got %q", got)
	}
}

func TestIfBreakFlatAndBroken(t *testing.T) {
	// Flat: trailing comma suppressed; Broken: trailing comma added.
	mk := func() Doc {
		return Group(Concat(
			Text("["),
			Indent(Concat(SoftLine, Text("a"), Text(","), Line, Text("b"), IfBreak(Text(","), Text("")))),
			SoftLine, Text("]"),
		))
	}
	if got := Print(mk(), 80, DefaultTabWidth); got != "[a, b]" {
		t.Fatalf("flat: got %q want %q", got, "[a, b]")
	}
	if got := Print(mk(), 4, DefaultTabWidth); got != "[\n\ta,\n\tb,\n]" {
		t.Fatalf("broken: got %q want %q", got, "[\n\ta,\n\tb,\n]")
	}
}

func TestFillGreedyWrap(t *testing.T) {
	// Words separated by Line; width 5 packs greedily: "aa bb" fits (5), next
	// "cc" would overflow → break before it.
	d := Fill(Text("aa"), Line, Text("bb"), Line, Text("cc"))
	got := Print(d, 5, DefaultTabWidth)
	want := "aa bb\ncc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
