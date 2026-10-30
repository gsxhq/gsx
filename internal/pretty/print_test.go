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

// block is a cluster word{ body } whose group breaks onto its own lines when
// it does not fit; forced, it carries a break the way re-parsed output does.
func block(body string, forced bool) Doc {
	inner := Concat(Text("{"), Indent(Concat(Line, Text(body))), Line, Text("}"))
	if forced {
		inner = Concat(inner, BreakParent)
	}
	return Concat(Text("word"), Group(inner))
}

func prose(cluster Doc) Doc {
	return Fill(Text("lorem"), Line, Text("ipsum"), Line, Text("dolor"), Line, cluster, Line, Text("end"))
}

// Fill measures a group that cannot stay flat as it prints: up to its first
// break. That is how the forced group the broken output re-parses into
// measures, so both lay the words out the same and a second format agrees with
// the first. Measured flat, the unforced block pushed "word" onto a new line.
func TestFillMeasuresBrokenGroupLikeForced(t *testing.T) {
	const body = "a body too long to stay flat even on a line of its own"
	got := Print(prose(block(body, false)), 40, 2)
	want := Print(prose(block(body, true)), 40, 2)
	if got != want {
		t.Errorf("unforced group that breaks:\n%s\nforced group:\n%s", got, want)
	}
	if !strings.HasPrefix(got, "lorem ipsum dolor word{\n") {
		t.Errorf("words before the block should keep their line, got:\n%s", got)
	}
}

// A cluster that fits flat at the start of a fresh line takes the break before
// it rather than breaking its group to stay on the current line.
func TestFillMovesClusterDownToKeepGroupFlat(t *testing.T) {
	got := Print(prose(block("short", false)), 30, 2)
	if want := "lorem ipsum dolor\nword{ short } end"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
