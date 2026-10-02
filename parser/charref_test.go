package parser

import "testing"

// TestDecodeAttrValue pins HTML's character-reference decoding for a
// double-quoted attribute value, including the attribute-only rule: a legacy
// named reference without ';' followed by an alphanumeric or '=' is left as is.
func TestDecodeAttrValue(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"plain", "plain"},
		{"", ""},
		{"a &amp; b", "a & b"},
		{"say &quot;hi&quot;", `say "hi"`},
		{"&lt;b&gt;", "<b>"},
		{"&#34;&#x27;&#X41;", `"'A`},
		{"/s?a=1&amp;b=2", "/s?a=1&b=2"},
		{"/s?a=1&b=2", "/s?a=1&b=2"},   // bare & stays
		{"?x=1&copy=2", "?x=1&copy=2"}, // legacy ref then '=' : not decoded
		{"&copyright", "&copyright"},   // legacy ref then alnum : not decoded
		{"&copy", "©"},                 // legacy ref at end : decoded
		{"&copy;=2", "©=2"},            // with ';' : decoded
		{"&nbsp;x", " x"},
		{"&bogus;", "&bogus;"}, // unknown : kept
		{"&", "&"},
		{"&#0;", "�"},   // NUL → replacement
		{"&#x80;", "€"}, // windows-1252 remap
		{"a < b > c", "a < b > c"},
		{"line\nbreak", "line\nbreak"},
	} {
		if got := decodeAttrValue(c.raw); got != c.want {
			t.Errorf("decodeAttrValue(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
