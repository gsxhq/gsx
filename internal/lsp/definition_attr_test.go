package lsp

import "testing"

func TestParamDeclIn(t *testing.T) {
	tests := []struct {
		params, attr, want string
		ok                 bool
	}{
		{"comments []store.Comment", "comments", "comments []store.Comment", true},
		{"title string, featured bool", "featured", "featured bool", true},
		{"a, b string", "b", "b string", true},
		{"Title string", "title", "Title string", true}, // firstUpper match
		{"x int", "y", "", false},                       // no match
		{"", "x", "", false},                            // empty
		{"][", "x", "", false},                          // unparseable
	}
	for _, tc := range tests {
		got, ok := paramDeclIn(tc.params, tc.attr)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("paramDeclIn(%q,%q)=(%q,%v) want (%q,%v)",
				tc.params, tc.attr, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFirstUpper(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"comments", "Comments"},
		{"Title", "Title"},
		{"", ""},
		{"x", "X"},
	} {
		if got := firstUpper(c.in); got != c.want {
			t.Errorf("firstUpper(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestParamOffsetIn(t *testing.T) {
	tests := []struct {
		params, attr string
		wantOff      int
		wantOK       bool
	}{
		{"comments []store.Comment", "comments", 0, true},
		{"title string, featured bool", "featured", 14, true}, // "title string, " is 14 bytes
		{"a, b string", "b", 3, true},                         // grouped params: a=0, ", "=1..2, b=3
		{"Title string", "title", 0, true},                    // firstUpper match (attr lower, param upper)
		{"x int", "y", 0, false},                              // no matching param
		{"", "x", 0, false},                                   // no params
		{"][", "x", 0, false},                                 // unparseable → false, no panic
	}
	for _, tc := range tests {
		off, ok := paramOffsetIn(tc.params, tc.attr)
		if ok != tc.wantOK || (ok && off != tc.wantOff) {
			t.Errorf("paramOffsetIn(%q,%q)=(%d,%v) want (%d,%v)",
				tc.params, tc.attr, off, ok, tc.wantOff, tc.wantOK)
		}
	}
}
