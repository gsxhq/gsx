package pretty

import "testing"

// Align pins hard-line breaks inside d to the column where d began, rather than
// to the enclosing tab-indent level. The prefix reproduces the current line's
// leading whitespace VERBATIM and pads the rest with spaces, so the alignment
// holds at any tab width the reader has configured.

func TestAlignHangingIndentAtColumn(t *testing.T) {
	// "\tn := " then an aligned doc that breaks: continuation lines land under
	// the doc's start column -> one tab (verbatim) + 5 spaces for "n := ".
	d := Concat(
		Text("\tn := "),
		Align(Concat(Text("<div>"), Indent(Concat(HardLine, Text("<p/>"))), HardLine, Text("</div>"))),
	)
	got := Print(d, 80)
	want := "\tn := <div>\n\t     \t<p/>\n\t     </div>"
	if got != want {
		t.Errorf("Align mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestAlignAtColumnZeroMatchesPlainIndent(t *testing.T) {
	// With no preceding text, Align must be a no-op: the prefix is empty, so
	// output is identical to the same doc without Align.
	inner := Concat(Text("<div>"), Indent(Concat(HardLine, Text("<p/>"))), HardLine, Text("</div>"))
	if got, want := Print(Align(inner), 80), Print(inner, 80); got != want {
		t.Errorf("Align at column 0 changed output:\n got %q\nwant %q", got, want)
	}
}

func TestAlignAfterTabIndentOnly(t *testing.T) {
	// Leading whitespace only: the prefix is the tabs themselves, no spaces.
	d := Concat(Text("\t\t"), Align(Concat(Text("a"), HardLine, Text("b"))))
	if got, want := Print(d, 80), "\t\ta\n\t\tb"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAlignForcesEnclosingGroupToBreak(t *testing.T) {
	// A hard break inside an Align must still propagate to the enclosing Group,
	// or the group would render flat and swallow the newline.
	d := Group(Concat(Text("x"), Align(Concat(Text("a"), HardLine, Text("b")))))
	if got, want := Print(d, 80), "xa\n b"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAlignNestedIndentStacksTabsAfterPrefix(t *testing.T) {
	d := Concat(
		Text("  x="),
		Align(Concat(Text("a"), Indent(Concat(HardLine, Text("b"), Indent(Concat(HardLine, Text("c"))))))),
	)
	// line is "  x=" -> prefix is "  " (verbatim lead) + "  " (2 spaces for "x=").
	if got, want := Print(d, 80), "  x=a\n    \tb\n    \t\tc"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
