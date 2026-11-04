package pretty

import (
	"strings"
	"testing"
)

// A literal tab inside a Text doc is real output width. advance() counted runes,
// so a tab read as 1 column while the printer's own indent levels read as
// tabWidth. Same tab, two answers.
func TestPrintCountsLiteralTabsAtTabWidth(t *testing.T) {
	// Text of 3 tabs + 70 runes = 76 columns at tabWidth 2, 82 at tabWidth 4.
	// Budget 78: fits at 2, must break at 4.
	lead := Text("\t\t\t" + strings.Repeat("x", 70))
	doc := Concat(lead, Group(Concat(SoftLine, Text("yy"))))

	if got := Print(doc, 78, 2); strings.Contains(got, "\n") {
		t.Errorf("tabWidth=2: 70+3*2+2 = 78 columns fits, want flat, got %q", got)
	}
	if got := Print(doc, 78, 4); !strings.Contains(got, "\n") {
		t.Errorf("tabWidth=4: 70+3*4+2 = 84 columns overflows, want a break, got %q", got)
	}
}

func TestPrintDefaultsTabWidth(t *testing.T) {
	doc := Text("\tx")
	if Print(doc, 80, 0) != Print(doc, 80, DefaultTabWidth) {
		t.Error("tabWidth<=0 must fall back to DefaultTabWidth")
	}
}

// The default print width has one home. Three call sites used to spell 80
// in-line; this pins that Print's own fallback reads the exported const, so a
// future change to it cannot leave pretty behind.
func TestPrintDefaultsWidth(t *testing.T) {
	// A doc exactly DefaultPrintWidth wide fits; one column wider breaks.
	fits := Text(strings.Repeat("x", DefaultPrintWidth))
	doc := func(t Doc) Doc { return Group(Concat(t, SoftLine, Text("y"))) }

	if got := Print(doc(fits), 0, 1); !strings.Contains(got, "\n") {
		t.Errorf("width 0 must mean DefaultPrintWidth=%d: %d+1 columns should break, got flat", DefaultPrintWidth, DefaultPrintWidth)
	}
	if got := Print(doc(Text(strings.Repeat("x", DefaultPrintWidth-1))), 0, 1); strings.Contains(got, "\n") {
		t.Errorf("width 0 must mean DefaultPrintWidth=%d: %d columns should fit, got a break", DefaultPrintWidth, DefaultPrintWidth)
	}
	if Print(doc(fits), 0, 1) != Print(doc(fits), DefaultPrintWidth, 1) {
		t.Error("width 0 must render identically to an explicit DefaultPrintWidth")
	}
}

// The product default is a deliberate choice, pinned in exactly one place so a
// change to it is a change to this line and nowhere else.
func TestDefaultsAreTheChosenValues(t *testing.T) {
	if DefaultPrintWidth != 120 {
		t.Errorf("DefaultPrintWidth = %d, want 120", DefaultPrintWidth)
	}
	if DefaultTabWidth != 2 {
		t.Errorf("DefaultTabWidth = %d, want 2", DefaultTabWidth)
	}
}

func TestIndentPreservesLiteralTextBytes(t *testing.T) {
	literal := "a\t\n\t\tb\n "
	doc := Indent(Concat(HardLine, Text(literal)))
	if got := Print(doc, 1, 4); got != "\n\t"+literal {
		t.Fatalf("literal text changed: %q", got)
	}
}

func TestFitsStopsAtLiteralNewline(t *testing.T) {
	for _, tc := range []struct{ prefix, want string }{
		{"abc", "x abc\n"},
		{"abcd", "x\nabcd\n"},
	} {
		// The first literal line matters to the preceding group's decision;
		// content after its newline belongs to a different width budget.
		tail := strings.Repeat("z", 100)
		doc := Concat(Group(Concat(Text("x"), Line)), Text(tc.prefix+"\n"+tail))
		if got := Print(doc, 5, 2); got != tc.want+tail {
			t.Fatalf("prefix %q: got %q", tc.prefix, got)
		}
	}
}

func TestLocalGroupExcludesContinuation(t *testing.T) {
	attribute := Group(Concat(Text("class="), Text(strings.Repeat("x", 95))))
	opening := Concat(Text("<div"), Indent(Concat(Line, attribute)), SoftLine, Text(">"))
	tail := Text(strings.Repeat("y", 15))
	if got := Print(Concat(LocalGroup(opening), tail), 120, 2); strings.Contains(got, "\n") {
		t.Fatalf("local opening should fit: %q", got)
	}
	if got := Print(Concat(Group(opening), tail), 120, 2); !strings.Contains(got, "\n") {
		t.Fatalf("generic group must measure continuation: %q", got)
	}
	if got := Print(Concat(LocalGroup(opening), tail), 100, 2); !strings.Contains(got, "\n") {
		t.Fatalf("long opening should wrap: %q", got)
	}
}

func TestAlignUsesPhysicalColumnWithoutChangingText(t *testing.T) {
	for _, prefix := range []string{"", "\t", "   "} {
		column := advance(0, prefix, 2)
		indent := func(n int) string { return strings.Repeat("\t", n/2) + strings.Repeat(" ", n%2) }
		doc := Indent(Concat(Text(prefix), AlignLine(Concat(Text("<x"), Indent(Concat(HardLine, Text("a"))), HardLine, Text(">literal\n \tdata</x>")))))
		want := prefix + "<x\n" + indent(column+2) + "a\n" + indent(column) + ">literal\n \tdata</x>"
		if got := Print(doc, 80, 2); got != want {
			t.Fatalf("prefix %q: got %q want %q", prefix, got, want)
		}
	}
}

func TestAlignLineIgnoresInlineProseWidth(t *testing.T) {
	doc := Concat(Text("\t<p>some long prose "), AlignLine(Concat(Text("<x"), Indent(Concat(HardLine, Text("a"))), HardLine, Text(">"))))
	want := "\t<p>some long prose <x\n\t\ta\n\t>"
	if got := Print(doc, 80, 2); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
