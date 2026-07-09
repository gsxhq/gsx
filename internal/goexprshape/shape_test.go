package goexprshape

import (
	"strings"
	"testing"
)

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

// Regression (found by independent adversarial review, confirmed via a real
// end-to-end gsx generate failure): when an EARLIER hole's whitespace needs
// collapsing (X, alone on its own line inside []any{...}), the resulting
// shrink must not corrupt the byte offset already recorded for it, nor the
// not-yet-processed offset of a LATER hole (Y, also alone on its own line
// inside an already-parenthesized keyed composite-literal field) — both must
// still resolve to their real AST positions afterward.
func TestClassifyMultipleHolesWhereEarlierOneCollapses(t *testing.T) {
	src := "package p\n\nvar listing = []any{\n\tX,\n}\nvar item = S{A: (\n\tY\n)}\n"
	offX := indexOf(src, "X")
	offY := indexOf(src, "Y")
	got := Classify(src, []Hole{{Start: offX, End: offX + 1}, {Start: offY, End: offY + 1}})
	if len(got) != 2 {
		t.Fatalf("Classify multi-hole = %+v, want 2 results", got)
	}
	if got[0].Shape != Plain {
		t.Errorf("X: got Shape=%v, want Plain (bare composite-lit element)", got[0].Shape)
	}
	if got[1].Shape != ParenWrap || !got[1].Wrapped {
		t.Errorf("Y: got %+v, want Shape=ParenWrap Wrapped=true (keyed field, already parenthesized)", got[1])
	}
}

// Regression (independent adversarial review): a bracket-shaped byte that is
// actually inside a comment must not be mistaken for a real bracket — doing
// so would collapse a whitespace run that isn't inside any bracket at all,
// which can merge two real statements onto the comment's line.
func TestClassifyCommentBracketIsNotReal(t *testing.T) {
	src := "package p\n\nvar help = SomeCall(a, b, // (\n\tX)\n"
	off := indexOf(src, "X")
	got := Classify(src, []Hole{{Start: off, End: off + 1}})
	if len(got) != 1 || got[0].Shape != Plain {
		t.Errorf("Classify(%q) = %+v, want [Plain] (call argument)", src, got)
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

// Sanitize is what lets a consumer hand the substituted source to go/parser or
// go/format at all: a hole alone on its line inside a bracket otherwise picks up
// an inserted semicolon. It must also be idempotent, since gsx fmt re-formats
// its own output.
func TestSanitizeCollapsesBracketAdjacentNewlines(t *testing.T) {
	// `icon: (\n\tH\n),` — gsx fmt's own decorative-paren output.
	src := "package p\nvar x = []T{\n\t{icon: (\n\t\tH\n\t), page: P{}},\n}\n"
	start := strings.Index(src, "H")
	holes := []Hole{{Start: start, End: start + 1}}

	got, gotHoles := Sanitize(src, holes)
	want := "package p\nvar x = []T{\n\t{icon: ( H ), page: P{}},\n}\n"
	if got != want {
		t.Errorf("Sanitize:\n got %q\nwant %q", got, want)
	}
	if n := len(gotHoles); n != 1 {
		t.Fatalf("got %d holes, want 1", n)
	}
	if got[gotHoles[0].Start:gotHoles[0].End] != "H" {
		t.Errorf("hole range %v does not cover the placeholder in %q", gotHoles[0], got)
	}

	again, againHoles := Sanitize(got, gotHoles)
	if again != got {
		t.Errorf("Sanitize is not idempotent:\n once %q\ntwice %q", got, again)
	}
	if againHoles[0] != gotHoles[0] {
		t.Errorf("idempotent Sanitize moved the hole: %v -> %v", gotHoles[0], againHoles[0])
	}
}

// A newline NOT adjacent to a bracket is a real statement separator that Go's
// own semicolon insertion needs; collapsing it would break the parse.
func TestSanitizeLeavesStatementSeparatingNewline(t *testing.T) {
	src := "package p\nfunc f() {\n\tn := H\n\tg()\n}\n"
	start := strings.Index(src, "H")
	got, _ := Sanitize(src, []Hole{{Start: start, End: start + 1}})
	if got != src {
		t.Errorf("Sanitize altered a non-bracket-adjacent newline:\n got %q\nwant %q", got, src)
	}
}
