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
