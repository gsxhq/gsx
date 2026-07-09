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
