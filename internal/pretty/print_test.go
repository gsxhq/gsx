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
