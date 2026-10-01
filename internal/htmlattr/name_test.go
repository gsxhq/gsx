package htmlattr

import "testing"

func TestValidName(t *testing.T) {
	valid := []string{
		"id", "data-x", "_", "1", "hx-on::click", ":class", "@click.away",
		".prop", ".p.q", "?disabled", "#ref", "*ngIf", "[prop]", "(event)",
		"on:click|preventDefault", "$x", "!y", "a&b", "~", "^", "%", "+", ",", ";",
		"\\", "|", "données", "日本", " nbsp", "pua",
		"\U0001f600", "�", // U+FFFD is an ordinary character when genuinely present
	}
	for _, k := range valid {
		if !ValidName(k) {
			t.Errorf("ValidName(%q) = false, want true", k)
		}
	}
	invalid := []string{
		"", " ", "a b", "a\tb", "a\nb", "a\rb", "a\x0cb", "a\x00b", "a\x1fb", "a\x7fb",
		"ab", "ab", "ab", // C1 controls
		"a﷐b", "a﷯b", "a￾b", "a￿b", "a\U0001fffeb", "a\U0010ffffb", // noncharacters
		"a\"b", "a'b", "a>b", "a/b", "a=b", "a<b", "a{b", "a}b", "{", "}", "a`b", "`",
		"a\xffb", "a\xc2", // invalid UTF-8
	}
	for _, k := range invalid {
		if ValidName(k) {
			t.Errorf("ValidName(%q) = true, want false", k)
		}
	}
}

func TestSameName(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"href", "href", true},
		{"href", "HREF", true},
		{"Data-X", "data-x", true},
		{"viewBox", "viewbox", true},
		{"@Click.Away", "@click.away", true},
		{"href", "hre", false},
		{"href", "src", false},
		{"k", "K", false},           // Kelvin sign: strings.EqualFold folds it, HTML does not
		{"data-é", "data-É", false}, // non-ASCII letters compare exactly
		{"data-é", "DATA-é", true},  // only the ASCII part folds
		{"ſ", "s", false},           // long s
		{"[", "{", false},           // 0x5B vs 0x7B differ by 0x20 but are not letters
		{"", "", true},
	} {
		if got := SameName(tc.a, tc.b); got != tc.want {
			t.Errorf("SameName(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := FoldName(tc.a) == FoldName(tc.b); got != tc.want {
			t.Errorf("FoldName(%q) == FoldName(%q) is %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if got := FoldName("Data-É-X"); got != "data-É-x" {
		t.Errorf("FoldName = %q", got)
	}
	if n := testing.AllocsPerRun(10, func() { _ = FoldName("data-x") }); n != 0 {
		t.Errorf("FoldName on a lowercase name allocates %v times", n)
	}
}
