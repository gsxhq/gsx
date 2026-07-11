package lsp

import "testing"

func TestSplitDottedTag(t *testing.T) {
	cases := []struct {
		tag           string
		wantQualifier string
		wantName      string
		wantOK        bool
	}{
		{"components.Input", "components", "Input", true},
		{"ui.Button", "ui", "Button", true},
		{"p.Content", "p", "Content", true}, // valid split; qualifier won't match an import at resolve time
		{"a.b.c", "", "", false},            // multi-dot rejected
		{"pkg.input", "", "", false},        // lowercase-initial name rejected
		{"Card", "", "", false},             // no dot
		{".x", "", "", false},               // leading dot
		{"a.", "", "", false},               // trailing dot
	}
	for _, tc := range cases {
		q, n, ok := splitDottedTag(tc.tag)
		if ok != tc.wantOK || q != tc.wantQualifier || n != tc.wantName {
			t.Errorf("splitDottedTag(%q) = (%q, %q, %v); want (%q, %q, %v)",
				tc.tag, q, n, ok, tc.wantQualifier, tc.wantName, tc.wantOK)
		}
	}
}
