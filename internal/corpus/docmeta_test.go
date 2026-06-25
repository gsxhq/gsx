package corpus

import "testing"

func TestParseDocMeta(t *testing.T) {
	in := []byte("name: Control flow\nsummary: if / else.\ncategory: Control flow\norder: 40\n")
	got := parseDocMeta(in)
	if got.Name != "Control flow" || got.Summary != "if / else." ||
		got.Category != "Control flow" || got.Order != 40 {
		t.Fatalf("parseDocMeta = %+v", got)
	}
}

func TestParseDocMetaDefaults(t *testing.T) {
	got := parseDocMeta([]byte("name: X\nunknown: y\n"))
	if got.Name != "X" || got.Order != 0 || got.Summary != "" {
		t.Fatalf("defaults wrong: %+v", got)
	}
}
