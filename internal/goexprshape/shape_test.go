package goexprshape

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		want        Shape
		wantWrapped bool
	}{
		{"value spec (var assignment)", "package p\n\nvar help = X\n", ParenWrap, false},
		{"assignment statement", "package p\n\nfunc f() {\n\thelp := 0\n\thelp = X\n}\n", ParenWrap, false},
		{"return statement", "package p\n\nfunc f() any {\n\treturn X\n}\n", ParenWrap, false},
		{"keyed composite literal field", "package p\n\nvar item = NavItem{Label: \"Home\", Icon: X}\n", ParenWrap, false},
		{"call argument", "package p\n\nvar wrapped = Wrap(X)\n", Plain, false},
		{"bare composite literal element", "package p\n\nvar ns = []any{X}\n", Plain, false},
		{"unparseable source", "this is not valid go {{{", Plain, false},
		{"already paren-wrapped value spec", "package p\n\nvar help = (X)\n", ParenWrap, true},
		{"already paren-wrapped keyed field", "package p\n\nvar item = NavItem{Label: \"Home\", Icon: (X)}\n", ParenWrap, true},
		// The placeholder sits alone on its own line inside an already-present
		// decorative paren — exactly gsx fmt's own multi-line output re-parsed.
		// A naive substitution here trips Go's automatic semicolon insertion
		// right after X, breaking the parse Classify depends on.
		{"already paren-wrapped, multi-line", "package p\n\nvar item = NavItem{Label: \"Home\", Icon: (\n\tX\n)}\n", ParenWrap, true},
		// Regression: the hole here is followed by a newline that is a REAL,
		// REQUIRED statement separator (no enclosing bracket at all) — an
		// earlier version of the whitespace-collapse fix above blindly
		// collapsed any newline touching the hole, merging this into the next
		// statement and breaking the parse in the opposite direction.
		{"hole followed by a real statement separator", "package p\n\nfunc f() {\n\tn := X\n\t_ = n\n}\n", ParenWrap, false},
		// Regression: a `var (…)` group's own closing paren immediately follows
		// an UNWRAPPED value with no relation to it — Wrapped must stay false
		// so a caller doesn't mistake the group's paren for a decorative one
		// and strip it (this broke real corpus output before Wrapped existed).
		{"var group's own closing paren is not the value's", "package p\n\nvar (\n\thello = \"x\"\n\n\txx = X\n)\n", ParenWrap, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset := max(indexOf(tt.src, "X"), 0)
			got := Classify(tt.src, []Hole{{Start: offset, End: offset + 1}})
			if len(got) != 1 || got[0].Shape != tt.want || got[0].Wrapped != tt.wantWrapped {
				t.Errorf("Classify(%q) = %+v, want Shape=%v Wrapped=%v", tt.src, got, tt.want, tt.wantWrapped)
			}
		})
	}
}

func TestClassifyMultipleHoles(t *testing.T) {
	src := "package p\n\nvar ns = []any{X}\nvar item = S{A: Y}\n"
	offX := indexOf(src, "X")
	offY := indexOf(src, "Y")
	got := Classify(src, []Hole{{Start: offX, End: offX + 1}, {Start: offY, End: offY + 1}})
	want := []Shape{Plain, ParenWrap}
	if len(got) != 2 || got[0].Shape != want[0] || got[1].Shape != want[1] {
		t.Errorf("Classify multi-hole = %+v, want shapes %v", got, want)
	}
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
