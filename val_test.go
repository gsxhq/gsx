package gsx

import (
	"strconv"
	"strings"
	"testing"
)

func renderNode(n Node) string { var b strings.Builder; _ = n.Render(nil, &b); return b.String() }

type stringerT struct{}

func (stringerT) String() string { return "S<x>" }

func TestVal(t *testing.T) {
	for _, tt := range []struct {
		in   any
		want string
	}{
		{"a", "a"}, {"<b>", "&lt;b&gt;"}, {5, "5"}, {int64(-3), "-3"}, {uint(7), "7"},
		{3.5, "3.5"}, {true, "true"}, {[]byte("<x>"), "&lt;x&gt;"},
		{stringerT{}, "S&lt;x&gt;"}, {nil, ""}, {Raw("<i>"), "<i>"},
		{[]Node{Text("a"), nil, Text("b")}, "ab"},                               // catNodeSlice parity; nil skipped
		{float32(0.1), strconv.FormatFloat(float64(float32(0.1)), 'g', -1, 64)}, // bitsize-64 parity pin (would be "0.1" at bitsize 32)
	} {
		if got := renderNode(Val(tt.in)); got != tt.want {
			t.Errorf("Val(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
func TestValError(t *testing.T) {
	var b strings.Builder
	err := Val(struct{}{}).Render(nil, &b)
	if err == nil {
		t.Error("Val(struct{}{}) should return error, got nil")
	}
	if b.Len() != 0 {
		t.Errorf("Val(struct{}{}) wrote %q, want empty", b.String())
	}
}
func TestText(t *testing.T) {
	if got := renderNode(Text("<b>")); got != "&lt;b&gt;" {
		t.Errorf("Text = %q, want escaped", got)
	}
}
func TestFragment(t *testing.T) {
	// renders each child in order, no wrapper element; nil children skipped; empty → "".
	if got := renderNode(Fragment(Text("a"), nil, Text("<b>"))); got != "a&lt;b&gt;" {
		t.Errorf("Fragment = %q, want %q", got, "a&lt;b&gt;")
	}
	if got := renderNode(Fragment()); got != "" {
		t.Errorf("Fragment() = %q, want empty", got)
	}
	// Fragment is usable as a promoted value too: Val(Fragment(...)) == Fragment(...).
	if got := renderNode(Val(Fragment(Text("x"), Text("y")))); got != "xy" {
		t.Errorf("Val(Fragment) = %q, want %q", got, "xy")
	}
}
