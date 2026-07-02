package ast

import "testing"

func TestIsComponentTag(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{"", false},
		{"div", false},
		{"my-el", false},
		{"Box", true},
		{"ui.Button", true},
		{"p.Row", true},
		{"strings.x", true}, // dotted always wins, mirroring both old impls
	}
	for _, c := range cases {
		if got := IsComponentTag(c.tag); got != c.want {
			t.Errorf("IsComponentTag(%q) = %v, want %v", c.tag, got, c.want)
		}
	}
}
